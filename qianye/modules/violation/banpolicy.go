package violation

import (
	"context"
	"fmt"
	"sync/atomic"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// banpolicy.go —— 按用户分组的违规处置策略档:解析、快照、兜底。
//
// 表结构与三道"兜底档不可删"的锁写在 model.go 的 BanPolicy 上。这里只做三件事:
// 把一个分组名解析成一份生效策略、把策略表缓存进进程、保证兜底档存在。
//
// # 为什么要缓存
//
// 解析点在 persistRecord(异步 worker)与 userSummary(用户端接口)。前者每条
// 违规记录一次、后者每次打开页面一次,单看都不贵;但策略表是**决定谁被封号**的
// 那张表,读不到它的后果是"该封的没封"或"不该封的封了",所以它必须有一条
// 与扩展库故障解耦的路径 —— 缓存在这里的作用不是省一次查询,是让扩展库抖动的
// 那几秒钟里判据仍然是确定的,而不是每条记录各自碰运气。

// banPolicySnapshot 是策略表的进程内只读快照。
type banPolicySnapshot struct {
	loadAt int64
	// byGroup 的键是 groupname.Effective 归一后的分组名,兜底档不在里面。
	byGroup map[string]BanPolicy
	// fallback 是兜底档。它**永远有值**:库里没有兜底行时由 yamlFallbackPolicy 填。
	fallback BanPolicy
	// fromDB 记录 fallback 是不是真的来自库里那一行。false 表示当前跑在 YAML
	// 兜底上 —— 管理端要能看见这个事实,否则"我明明改了兜底档"会变成一个
	// 只有读源码才能发现的落差。
	fromDB bool
}

var (
	policySnap    atomic.Pointer[banPolicySnapshot]
	policyNextAt  atomic.Int64
	policyRefresh atomic.Int64 // 刷新失败次数,进 stats
)

// yamlFallbackPolicy 是"库里一行策略都没有"时的最终兜底。
//
// 种子取 YAML 的两个旧字段(violation.auto_ban_threshold /
// auto_ban_window_hours),因此升级上来的站点在建表之前、建表失败、
// 或扩展库不可达时,行为与改造之前逐字节一致。这不是保守起见的冗余:
// 这两个字段是现网**唯一**已经被运维设定过的值,换成任何硬编码默认都是
// 在事故当天悄悄改变封号阈值。
func yamlFallbackPolicy() BanPolicy {
	cfg := config.Get().Violation
	return BanPolicy{
		IsDefault: true,
		Enabled:   true,
		// YAML 里写 -1 同样表示不限期限:这个字段是兜底档的种子,让它比库里那一行
		// 少一种取值,会让"我在 YAML 里配了无限、界面上却显示 24 小时"成立。
		WindowHours: effectiveWindowHours(cfg.AutoBanWindowHours),
		Threshold:   cfg.AutoBanThreshold,
		Action:      PolicyActionBan,
	}
}

// resolveBanPolicy 给出某个用户分组当前生效的策略档。
//
// 优先级只有两级,刻意不做继承链:分组专属档 → 兜底档。多级继承(分组 → 父分组
// → 兜底)在这里没有对应的业务概念,而"这个人到底按哪一档判的"必须一眼可答 ——
// Ban.PolicyGroup 冻结的就是这个答案。
//
// 停用(Enabled=false)的专属档等价于"没配",回落兜底档而不是"不处置":
// 关掉一档策略最不该产生的效果就是"这个分组从此免罚"。
func resolveBanPolicy(group string) BanPolicy {
	s := banPolicies()
	if key := groupname.Effective(group); key != "" {
		if p, ok := s.byGroup[key]; ok && p.Enabled {
			return p
		}
	}
	return s.fallback
}

// banPolicies 返回当前快照,永不返回 nil,并在到期时触发一次同步重载。
//
// 与规则快照(maybeRefresh + guard.HotAsync)不同,这里是**同步**重载:
// 调用点全都不在 relay 热路径上(异步 persist worker、用户端接口、管理端接口),
// 而异步刷新会让"管理员刚改完阈值、立刻点影响面预览"读到旧值 —— 那正是
// 这个功能最需要准确的一刻。
func banPolicies() *banPolicySnapshot {
	now := common.GetTimestamp()
	if next := policyNextAt.Load(); now >= next {
		every := int64(config.Get().Violation.RuleCacheSeconds)
		if every <= 0 {
			every = 60
		}
		if policyNextAt.CompareAndSwap(next, now+every) {
			if err := reloadBanPolicies(context.Background(), db.Get()); err != nil {
				policyRefresh.Add(1)
			}
		}
	}
	if s := policySnap.Load(); s != nil {
		return s
	}
	return &banPolicySnapshot{fallback: yamlFallbackPolicy(), loadAt: now}
}

// reloadBanPolicies 重建策略快照。
//
// 失败时**保留旧快照**:一次扩展库抖动不该让全站回落到 YAML 阈值,
// 那会在几秒钟内按另一套阈值判人。只有从来没加载成功过时才落到 YAML。
func reloadBanPolicies(ctx context.Context, gdb *gorm.DB) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	var rows []BanPolicy
	if err := gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return err
	}
	s := &banPolicySnapshot{
		loadAt:   common.GetTimestamp(),
		byGroup:  make(map[string]BanPolicy, len(rows)),
		fallback: yamlFallbackPolicy(),
	}
	for _, p := range rows {
		if p.IsDefault {
			s.fallback = p
			s.fromDB = true
			continue
		}
		if key := groupname.Effective(p.UserGroup); key != "" {
			s.byGroup[key] = p
		}
	}
	policySnap.Store(s)
	return nil
}

// invalidateBanPolicies 让下一次读立刻回源。管理端每次写策略后调用。
//
// 只清本节点。多节点靠 TTL 收敛(默认 60 秒),与规则快照的口径一致 ——
// 给策略表单独造一张版本表是同一个事实的第二份拷贝,而 60 秒的滞后在
// "改阈值"这个动作上是可接受的:它本来就要人二次确认。
func invalidateBanPolicies() {
	policyNextAt.Store(0)
}

// ensureDefaultBanPolicy 保证兜底档那一行存在。启动期调用,幂等。
//
// 这是"兜底档不可删"的第二道锁。第一道(删除接口拒绝)挡的是管理员,
// 这一道挡的是"表刚建出来"与"有人直接进库 DELETE"。
//
// OnConflict DoNothing:多节点同时启动只会有一个插入成功,其余静默跳过。
// 绝不 Upsert —— 那会在每次重启时把管理员改过的兜底阈值覆盖回 YAML 值。
func ensureDefaultBanPolicy(ctx context.Context, gdb *gorm.DB) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	seed := yamlFallbackPolicy()
	seed.UserGroup = ""
	seed.Remark = "默认兜底策略:没有专属策略档的用户分组都按这一档判定。此行不可删除。"
	seed.CreatedAt = common.GetTimestamp()
	seed.UpdatedAt = seed.CreatedAt
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&seed)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	return nil
}

// policyLabel 是策略档在日志与审计里的人类可读名。
// 兜底档没有分组名,写成空串会让日志出现 `策略档 ""` 这种读不懂的行。
func policyLabel(p BanPolicy) string {
	if p.IsDefault || p.UserGroup == "" {
		return "默认兜底"
	}
	return p.UserGroup
}

// validateBanPolicy 校验一档策略。管理端写入前调用。
func validateBanPolicy(p *BanPolicy) error {
	switch p.Action {
	case PolicyActionRecord, PolicyActionRestrict, PolicyActionBan:
	default:
		return fmt.Errorf("action 取值非法: %q(只能是 %q / %q / %q)",
			p.Action, PolicyActionRecord, PolicyActionRestrict, PolicyActionBan)
	}
	// 与 validateCategory 逐字同构:1..上界的小时数,或哨兵 WindowUnlimited。
	// 两处窗口的取值域必须一样,否则管理端会出现"类型能配无限、策略档不能"这种
	// 说不出理由的差别,而它落到用户端就是两条并排的线用两套时间口径说话。
	if p.WindowHours != WindowUnlimited &&
		(p.WindowHours < 1 || p.WindowHours > maxPolicyWindowHours) {
		return fmt.Errorf("统计窗口必须在 1..%d 小时之间,或取 %d 表示不限期限(达到次数即触发,不看时间跨度),当前为 %d",
			maxPolicyWindowHours, WindowUnlimited, p.WindowHours)
	}
	if p.Threshold < 0 || p.Threshold > maxPolicyThreshold {
		return fmt.Errorf("次数阈值必须在 0..%d 之间(0 表示不自动处置),当前为 %d",
			maxPolicyThreshold, p.Threshold)
	}
	// 这里必须用 Normalize 而不是 Effective:Effective 会把空串折成 "default",
	// 于是"根本没填分组名"与"填了 default"变成同一个值,这条校验永远为假。
	// 折叠是**判定侧**的口径(用户的空分组按 default 算),写入侧要的恰恰相反 ——
	// 一档谁都不匹配的策略必须在保存时被拒,而不是悄悄变成 default 那一档。
	if !p.IsDefault && groupname.Normalize(p.UserGroup) == "" {
		return fmt.Errorf("非兜底档必须指定用户分组")
	}
	if p.IsDefault && p.UserGroup != "" {
		return fmt.Errorf("兜底档的用户分组必须为空")
	}
	// 与 Rule.Remark 同口径:varchar(N) 在 MySQL/PostgreSQL 上是 N 个**字符**,
	// 按 byte 判会在正确的中文数据上误报(见 builtin_column_fit_test.go 的推导)。
	if n := utf8.RuneCountInString(p.Remark); n > banPolicyRemarkLimit {
		return fmt.Errorf("备注过长(%d 字,上限 %d 字)", n, banPolicyRemarkLimit)
	}
	return nil
}

const (
	// banPolicyRemarkLimit 必须与 BanPolicy.Remark 的 gorm tag 同值。
	// 两侧漂移的后果与 Rule 那次 Error 1406 完全同形,
	// 由 TestBanPolicyRemarkLimitMatchesColumnTag 双向钉住。
	banPolicyRemarkLimit = 512
	// maxPolicyWindowHours 是统计窗口上界(90 天)。
	//
	// 上界的作用不是"防止窗口太长",而是防止 `now - windowHours*3600` 在
	// int64 上算出一个负数或远古时间戳:那会让窗口判据恒为真,
	// 用户的违规计数从此**永不过期**,一年前的一次命中仍然算数。
	maxPolicyWindowHours = 24 * 90
	// maxPolicyThreshold 与 CountWeight 的量级对齐即可。阈值再大只是永不触发,
	// 但它必须是个能被人看懂的数,而不是一个让界面显示成科学计数法的值。
	maxPolicyThreshold = 1_000_000
)

// banPolicyImpact 是"把某个分组切成这套阈值,有多少存量账号已经越过这条线"的预览结果。
//
// # 为什么这个数必须在保存之前算出来
//
// 改阈值不是改一个配置项:计数是滚动窗口里早就攒好的,阈值一降,一批账号当场
// 就处在越线状态,他们**各自的下一次违规命中**会立刻把他们收走。没有这个预览,
// 管理员按下保存时手上唯一的信息是"我把 10 改成了 3",而实际后果可能是
// "37 个账号在接下来几分钟内陆续被限制"。
//
// # 它不是"这次保存会封掉几个人"
//
// 保存只写策略表,不处置任何人(见 adminUpsertBanPolicy 的说明)。Matched 的
// 语义是"已越线的存量账号数",报文与界面文案必须照这个语义写 —— 说成
// "保存后立刻处置" 会让管理员去封禁列表里找一批根本不会出现的行。
type banPolicyImpact struct {
	// Matched 是命中的账号数。Capped 为真表示扫描到了上限,真实值 >= Matched。
	Matched int  `json:"matched"`
	Capped  bool `json:"capped"`
	// Scanned 是参与判定的计数行数,用来解释 Matched 为什么是这个数。
	Scanned int `json:"scanned"`
	// Action 与 Threshold 原样回显:预览结果与请求参数必须绑在一起返回,
	// 否则前端把两次请求的结果串在一起显示时没人能发现。
	Action      string `json:"action"`
	Threshold   int    `json:"threshold"`
	WindowHours int    `json:"window_hours"`
	// UserIds 是前 impactSampleSize 个样本,给管理员一个"到底是谁"的抓手。
	UserIds []int `json:"user_ids"`
}

const (
	// impactScanCap 是影响面预览最多扫描的计数行数。
	// 这是一个管理端的只读预览,不值得为它做一次无上限的跨库扫描。
	impactScanCap = 5000
	// impactSampleSize 是回显的样本账号数。
	impactSampleSize = 20
)

// countBanPolicyImpact 计算影响面。
//
// 跨库是硬约束:计数在扩展库(qy_violation_counter),用户分组与账号状态在主库
// (users)。两库之间没有任何可用的 join,所以只能"扩展库出候选 → 主库过滤"。
// 顺序不能反 —— 反过来是"主库全表用户 → 逐个查计数",那是一次全站扫描。
//
// 主库那一步同时承担三件过滤:分组匹配、只算当前可用账号(status=enabled,
// 已经被限制的人不会被"再限制一次")、排除 root(disableUserForViolation 永不
// 处置 root,把它算进预览等于虚报)。
func countBanPolicyImpact(ctx context.Context, gdb *gorm.DB, mainDB *gorm.DB, group string, threshold, windowHours int, action string) (banPolicyImpact, error) {
	// WindowHours 回显**生效值**而不是入参原样:入参可能是 0(前端漏传),
	// 而预览是按 24 小时算的。回显原样会让弹窗上的"按 0 小时窗口"与实际口径对不上。
	out := banPolicyImpact{
		Action: action, Threshold: threshold, WindowHours: effectiveWindowHours(windowHours),
		UserIds: make([]int, 0, impactSampleSize),
	}
	if gdb == nil || mainDB == nil {
		return out, db.ErrNotReady
	}
	// 阈值 0 表示不处置,影响面恒为 0 —— 但仍然要返回一个结构完整的结果,
	// 否则前端得为这一种情况单独写一条分支。
	if threshold <= 0 {
		return out, nil
	}
	// 无限窗口下 winFrom 是负数,`window_start >= ?` 因此放行全部计数行 ——
	// 影响面必须与判定同口径,少一条过滤都会让预览少报。
	winFrom := windowFloor(common.GetTimestamp(), windowHours)

	var candidates []int
	if err := gdb.WithContext(ctx).Model(&Counter{}).
		Where("hit_count >= ? AND window_start >= ?", threshold, winFrom).
		Order("hit_count desc, user_id asc").
		Limit(impactScanCap+1).
		Pluck("user_id", &candidates).Error; err != nil {
		db.MarkFailure(err)
		return out, err
	}
	if len(candidates) > impactScanCap {
		out.Capped = true
		candidates = candidates[:impactScanCap]
	}
	out.Scanned = len(candidates)
	if len(candidates) == 0 {
		return out, nil
	}

	// 主库过滤分批做:IN 列表长度直接进 SQL,5000 个占位符在 MySQL 上会撞
	// max_allowed_packet,在 SQLite 上会撞 SQLITE_MAX_VARIABLE_NUMBER(默认 999)。
	// 999 是三种目标数据库里最小的那个上限,按它切。
	const batch = 900
	for start := 0; start < len(candidates); start += batch {
		end := start + batch
		if end > len(candidates) {
			end = len(candidates)
		}
		q := mainDB.WithContext(ctx).Table("users").
			Where("id IN ?", candidates[start:end]).
			Where("status = ? AND role < ?", common.UserStatusEnabled, common.RoleRootUser)
		// 兜底档要匹配的是"没有专属档的全部分组",那不是一个可以写进 WHERE 的
		// 集合(它取决于策略表里还有哪些档)。所以兜底档的预览按"全部分组"算,
		// 并由接口层把这个口径如实标注出来 —— 虚报比少报安全,但必须说清楚。
		//
		// 空 group 就是兜底档,必须**不加过滤**。用 Effective 会把它折成 "default",
		// 于是兜底档的影响面只数 default 分组的人 —— 少报,而少报的方向是
		// 「看起来无害、实际会处置一批人」,正是这个预览要防的。
		//
		// group 是三种数据库里的保留字,列名必须走 model.QyCommonGroupCol()
		// (PostgreSQL 用 "group",MySQL/SQLite 用 `group`)。
		if key := groupname.Normalize(group); key != "" {
			q = q.Where(model.QyCommonGroupCol()+" = ?", key)
		}
		var ids []int
		if err := q.Pluck("id", &ids).Error; err != nil {
			return out, err
		}
		out.Matched += len(ids)
		for _, id := range ids {
			if len(out.UserIds) >= impactSampleSize {
				break
			}
			out.UserIds = append(out.UserIds, id)
		}
	}
	return out, nil
}
