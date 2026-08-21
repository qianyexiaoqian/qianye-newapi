package groupns

// usergroup_store.go —— 用户分组的新建 / 改名 / 删除并迁移。
//
// ══════════════════════ 为什么这件事没有一次原子写 ══════════════════════
//
// 一个用户分组的名字同时住在**三个**存储里:
//
//	主库    users.group、user_subscriptions 的三列快照、subscription_plans 的两列
//	主库    options.GroupGroupRatio 的外层键、options.TopupGroupRatio 的键
//	扩展库  qy_user_groups / qy_group_scopes / qy_group_grants / …
//
// 主库与扩展库是两个数据库,跨库事务不存在。所以这里不假装原子,而是**用顺序
// 把最坏的失败方向钉死**:
//
//	改名  先把配置复制到新名字 → 再迁用户 → 最后删旧名字的配置
//	删除  先迁用户 → 再删配置
//
// 两条路径共用同一条不变量:**任何一个中断点上,users.group 指向的那个名字
// 都仍然有完整的配置。** 反过来做(先删配置再迁用户)会在窗口期造出孤儿 ——
// 而 GetGroupRatio 对孤儿是 fail-open 返回 1.0,整组人静默按原价扣费、零告警。
// 那不是一次报错,是一段没人会发现的账目损失。
//
// 中断的代价因此收敛成两种,都可以靠**再按一次同一个按钮**修好:
//
//	改名中断在迁用户之前 → 新名字的配置已经建好、还没有人 → 重试即可(幂等)
//	删除中断在清配置之前 → 用户已经迁走、源分组空了 → 再删一次即可(此时无需迁移)

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"gorm.io/gorm"
)

// 改写的三个阶段。部分失败时它进返回值与审计正文 ——
// 「失败在哪一步」直接决定运营接下来该做什么,而这个信息在一句
// 「保存失败,请重试」里是彻底丢失的。
const (
	StagePrepare = "prepare"
	StageMigrate = "migrate"
	StageCleanup = "cleanup"
	// StageTopup 是「展示属性已落扩展库、充值倍率没写进上游 options」这一档。
	// 它不属于改名/删除那条链路,但半成状态的形状与处置完全相同,
	// 所以复用同一个类型而不是另造一个 —— 前端只有一条"原样弹出并常驻"的分支。
	StageTopup = "topup"
)

// RewritePartial 是一次跨库改写的半成状态。
//
// **只在部分失败时出现,而且必须原样进界面。** 两库不原子的最坏失败方式是运营
// 看到一句绿色的「已完成」然后走人,而线上停在中间态。
type RewritePartial struct {
	Stage string `json:"stage"`
	// Message 必须包含**下一步怎么做**,不能只说哪里错了。
	Message string `json:"message"`
}

// RewriteResult 是一次改名 / 删除的结果。
type RewriteResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Rename 为 true 表示这是一次改名(To 是一个新名字),false 表示删除并迁移。
	Rename bool `json:"rename"`

	Users         int64 `json:"users"`
	Subscriptions int64 `json:"subscriptions"`
	Plans         int64 `json:"plans"`
	// Stragglers 是提交之后仍然挂在源分组上的在册用户数,正常恒为 0。
	Stragglers int64 `json:"stragglers"`
	// CacheMisses 是缓存失效失败的用户数。非 0 表示这批人在缓存 TTL 内
	// 仍然按旧分组解析可用清单与倍率。
	CacheMisses int `json:"cache_misses"`

	Residues []Residue       `json:"residues"`
	Partial  *RewritePartial `json:"partial,omitempty"`
}

// rewriteUserGroup 是改名与删除共用的那一条主干。
//
// to 在删除路径上可以为空 —— 那只发生在源分组已经一个用户都没有的时候
// (由调用方保证:有用户时目标是必填的)。
func rewriteUserGroup(gdb *gorm.DB, from, to string, rename bool, operatorId int) (RewriteResult, error) {
	res := RewriteResult{From: from, To: to, Rename: rename}

	// ── 阶段一:改名时先把配置复制到新名字 ──
	//
	// 复制而不是"移动":两者在中断时的差别是决定性的。移动 = 先删后建,中断在
	// 中间意味着这一档人的倍率与权威清单**同时消失**,而他们的 users.group 还没动;
	// 复制则在整个窗口里让新旧两个名字都是完整的,users.group 指向哪个都对。
	if rename {
		if err := gdb.Transaction(func(tx *gorm.DB) error {
			return duplicateUserGroupConfig(tx, from, to, operatorId)
		}); err != nil {
			res.Partial = &RewritePartial{
				Stage:   StagePrepare,
				Message: "复制配置到新名字失败,**没有任何东西被改动**,可以直接重试: " + err.Error(),
			}
			return res, err
		}
		if err := duplicateUserGroupOptions(from, to); err != nil {
			res.Partial = &RewritePartial{
				Stage: StagePrepare,
				Message: "新名字的登记与范围已经建好,但倍率/充值折扣还没复制过去 —— " +
					"用户尚未迁移,线上行为未变。请重试同一次改名(重复执行是幂等的): " + err.Error(),
			}
			return res, err
		}
	}

	// ── 阶段二:迁用户(主库一个事务) ──
	//
	// 两条路径共用 model.QyRewriteUserGroupTx 这一份实现,纯迁移那条(见
	// usergroup_migrate.go)也是同一个。差别只由 mode 表达:这里源名字都会消失,
	// 所以订阅快照按列值全表匹配;纯迁移那条源名字留着,只动被迁移的那批人。
	if to != "" {
		mode := model.QyRewriteModeDelete
		if rename {
			mode = model.QyRewriteModeRename
		}
		moved, err := model.QyRewriteUserGroupTx(from, to, mode)
		if err != nil {
			res.Partial = &RewritePartial{
				Stage: StageMigrate,
				Message: "迁移用户失败,主库事务已回滚 —— 没有任何用户被改动。" +
					"源分组的配置仍然完整,请重试: " + err.Error(),
			}
			return res, err
		}
		res.Users, res.Subscriptions, res.Plans = moved.Users, moved.Subscriptions, moved.Plans
		res.Stragglers = moved.Stragglers
		// 缓存失效必须在提交之后、清配置之前。Redis 里的 user:<id> 缓存着旧分组
		// (TTL 默认 60s,而 GetUserGroup 优先读缓存)。不失效就等于在这一分钟里
		// 让这批人继续按一个即将被删掉的名字解析清单与倍率。
		res.CacheMisses = invalidateUserCaches(moved.UserIds)

		// ── 掉队的人必须在清配置之前把整件事停住 ──
		//
		// 行锁只锁得住**已经存在**的行,拦不住 INSERT。阶段二执行期间(以及它
		// 提交之后、这里之前)注册进源分组的账号,users.group 仍然是源分组名。
		// 此时继续走阶段三,他们的分组名会在下一秒失去倍率与权威清单 ——
		// 而 GetGroupRatio 对孤儿是 fail-open 返回 1.0,表现是整组人静默按原价
		// 扣费、零告警。这正是本文件全部顺序安排要防的那一件事,不能因为
		// "只有几个人"就放过去。
		//
		// 停在这里是安全的:用户已经迁完(幂等),源分组的配置**一个字节都没动**,
		// 所以掉队的那几个账号仍然指着一个配置完整的名字。运营把他们改到目标
		// 分组之后再按一次同一个按钮即可收敛。
		if res.Stragglers > 0 {
			res.Partial = &RewritePartial{
				Stage: StageMigrate,
				Message: fmt.Sprintf(
					"已经迁走 %d 名用户,但迁移期间又有 %d 名用户被写进了分组 %q"+
						"(并发注册或另一个管理员同时改了分组)。**源分组的配置一个字节都没有动**,"+
						"所以这几个账号此刻仍然按完整配置计费,不是孤儿。"+
						"请先把他们改到 %q,再按一次同样的操作即可完成删除",
					res.Users, res.Stragglers, from, to),
			}
			return res, fmt.Errorf(
				"qianye/groupns: 用户分组 %q 上仍有 %d 名用户,已停在清理配置之前", from, res.Stragglers)
		}
	}

	// ── 阶段三:清掉源分组的配置 ──
	//
	// 扩展库这一半整体在一个事务里:不会出现"范围清了、费率还在"。
	// options 那一半在它之后,单独失败时源分组只剩几个不会被任何人读到的键
	// (那个名字已经没有用户了),再删一次即可清干净。
	residues, err := ProbeResidues(gdb, from)
	if err != nil {
		common.SysError("qianye/groupns: 清理前探测残留失败(不影响已完成的迁移): " + err.Error())
	}
	res.Residues = residues

	if err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := SweepResidues(tx, from, to, rename); err != nil {
			return err
		}
		return tx.Where("name = ?", from).Delete(&UserGroup{}).Error
	}); err != nil {
		res.Partial = &RewritePartial{
			Stage: StageCleanup,
			Message: fmt.Sprintf(
				"用户已经迁移完成(%d 人),但源分组 %q 的登记行与配置残留没有清掉 —— "+
					"线上计费与路由已经按新分组走,残留只是一批读不到的死配置。"+
					"再执行一次同样的操作即可清干净(此时源分组已经没有用户,不需要再选迁移目标): %s",
				res.Users, from, err.Error()),
		}
		return res, err
	}
	// 事务提交之后才通知各模块刷新进程内缓存。在事务里刷会在回滚时留下
	// 「库里是旧值、进程里是新值」的分叉,而那种分叉只有重启才会自愈。
	CommitResidues(from, to, rename)

	if err := dropUserGroupOptions(from); err != nil {
		res.Partial = &RewritePartial{
			Stage: StageCleanup,
			Message: fmt.Sprintf(
				"用户已经迁移、扩展库配置已经清干净,但 options 里 %q 的交叉倍率/充值折扣键"+
					"没能删掉。它们此刻读不到任何用户,不影响计费;再执行一次同样的操作即可: %s",
				from, err.Error()),
		}
		return res, err
	}

	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupns: 改写后重载登记快照失败: " + err.Error())
	}
	return res, nil
}

// duplicateUserGroupConfig 把 from 的**扩展库**配置整份复制到 to。
//
// 登记行走 Create 而不是 Save:目标名字已经存在时必须失败,而不是覆盖 ——
// 覆盖会让一次改名静默吞掉另一个分组的全部配置。
func duplicateUserGroupConfig(tx *gorm.DB, from, to string, operatorId int) error {
	var src UserGroup
	if err := tx.Where("name = ?", from).Take(&src).Error; err != nil {
		return err
	}
	dst := src
	dst.Name = to
	dst.OperatorId = operatorId
	dst.CreatedAt = now()
	dst.UpdatedAt = now()
	if err := tx.Create(&dst).Error; err != nil {
		return err
	}
	// 别的模块的配置(范围、授权、费率、划转规则)由各自的 Sweep 在改名模式下
	// **原地改写**,不在这里复制:那些表的形状只有它们自己知道,而复制与改写
	// 两份逻辑一旦分家,漏掉的那一半永远不会有人发现。
	return nil
}

// duplicateUserGroupOptions 把 from 在 options 里的两处键复制到 to。
//
// 两处键都是**用户分组**为键:
//
//	GroupGroupRatio  外层键 = 用户分组,内层键 = 模型分组
//	TopupGroupRatio  键     = 用户分组(充值折扣)
//
// 复制而不是移动的理由与 duplicateUserGroupConfig 相同:中断时两个名字都完整。
func duplicateUserGroupOptions(from, to string) error {
	cross, err := loadCrossRatios()
	if err != nil {
		return err
	}
	if row, ok := cross[from]; ok && len(row) > 0 {
		copied := make(map[string]float64, len(row))
		for mg, v := range row {
			copied[mg] = v
		}
		cross[to] = copied
		if err := saveCrossRatios(cross); err != nil {
			return err
		}
	}

	topup, err := loadTopupRatios()
	if err != nil {
		return err
	}
	if v, ok := topup[from]; ok {
		topup[to] = v
		if err := saveTopupRatios(topup); err != nil {
			return err
		}
	}
	return nil
}

// dropUserGroupOptions 删掉 from 在 options 里的两处键。
func dropUserGroupOptions(from string) error {
	cross, err := loadCrossRatios()
	if err != nil {
		return err
	}
	if _, ok := cross[from]; ok {
		delete(cross, from)
		if err := saveCrossRatios(cross); err != nil {
			return err
		}
	}

	topup, err := loadTopupRatios()
	if err != nil {
		return err
	}
	if _, ok := topup[from]; ok {
		delete(topup, from)
		if err := saveTopupRatios(topup); err != nil {
			return err
		}
	}
	return nil
}

// loadCrossRatios / saveCrossRatios 读写 options.GroupGroupRatio。
//
// 读的是 **ratio_setting 的内存快照**而不是 options 表:热路径乘的就是那一份,
// 管理端必须与它同源,否则又会出现"界面显示 A、热路径乘 B"。
func loadCrossRatios() (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	raw := ratio_setting.GroupGroupRatio2JSONString()
	if raw == "" {
		return out, nil
	}
	if err := common.UnmarshalJsonStr(raw, &out); err != nil {
		return nil, fmt.Errorf("上游 GroupGroupRatio 不是合法 JSON: %w", err)
	}
	return out, nil
}

func saveCrossRatios(m map[string]map[string]float64) error {
	// 空行连外层键都不写:留一个空对象在里面,GetGroupGroupRatio 的第一层查找
	// 会命中、第二层落空,结论一样,但 options 里会堆满永远读不到的空壳。
	cleaned := make(map[string]map[string]float64, len(m))
	for ug, row := range m {
		if len(row) == 0 {
			continue
		}
		cleaned[ug] = row
	}
	payload, err := common.Marshal(cleaned)
	if err != nil {
		return err
	}
	return updateOptionVerified("GroupGroupRatio", string(payload))
}

func loadTopupRatios() (map[string]float64, error) {
	out := map[string]float64{}
	raw := common.TopupGroupRatio2JSONString()
	if raw == "" {
		return out, nil
	}
	if err := common.UnmarshalJsonStr(raw, &out); err != nil {
		return nil, fmt.Errorf("上游 TopupGroupRatio 不是合法 JSON: %w", err)
	}
	return out, nil
}

func saveTopupRatios(m map[string]float64) error {
	payload, err := common.Marshal(m)
	if err != nil {
		return err
	}
	return updateOptionVerified("TopupGroupRatio", string(payload))
}

// updateOptionVerified 写一条 option 并**回查确认它真的落库了**。
//
// 上游 model.UpdateOption 对 DB.FirstOrCreate / DB.Save 的返回值一个都不检查,
// 只把内存写的错误返回出来。库写失败(磁盘满 / 只读 / 行锁超时)时它照样返回 nil,
// 于是本节点内存里删掉了、库里还在、别的节点永远拿不到、本节点重启后又回来 ——
// 表现是「删掉的用户分组过几天自己长回来」,而删除接口当时返回的是 200。
//
// 与 groupmatrix.verifyOptionPersisted 同源;两处刻意各写一份而不是抽公共函数:
// 抽出来要么让 groupns import groupmatrix(依赖打结),要么再造一个只有两个调用者
// 的公共包,而这段逻辑只有 10 行且不会再变。
func updateOptionVerified(key, want string) error {
	if err := model.UpdateOption(key, want); err != nil {
		return err
	}
	if model.DB == nil {
		return fmt.Errorf("主库未就绪,无法确认 options.%s 是否真的落库 —— "+
			"上游 UpdateOption 不检查库写错误,不回查就等于没写", key)
	}
	// 条件走**结构体**而不是 "key = ?":key 是 MySQL 的保留字,
	// 裸 SQL 片段在三种方言上的引号规则不同;交给 GORM 的 schema 渲染才跨库正确。
	var got model.Option
	if err := model.DB.Where(&model.Option{Key: key}).Take(&got).Error; err != nil {
		return fmt.Errorf("回查 options.%s 失败,无法确认是否落库: %w", key, err)
	}
	if got.Value != want {
		return fmt.Errorf("options.%s 回查值与写入值不一致 —— 库写很可能失败了"+
			"(上游 UpdateOption 不检查库写错误),本节点内存已是新值而库里不是,请重试", key)
	}
	return nil
}

// invalidateUserCache 是缓存失效的**唯一出口**,做成变量只为让用例能观测它。
//
// 生产恒为 model.InvalidateUserCache。用例里 Redis 与内存缓存都是关掉的,
// 而关掉时上游这个函数直接返回 nil —— 于是"一个都没调"与"每个都调了"
// 产出的 CacheMisses 同样是 0,断言不到任何东西。而漏掉这一步的表现
// 恰恰是这批人在缓存 TTL 内继续按旧分组解析可用清单与倍率(默认 60s 的错价)。
var invalidateUserCache = model.InvalidateUserCache

// invalidateUserCaches 逐个失效被迁移用户的缓存,返回失败数。
//
// 失败不回滚:迁移已经提交,回滚不了。失败数进返回值与审计 ——
// 「有 N 个人在接下来一分钟里还按旧分组算」是运营需要知道的事,
// 而它在一句「迁移成功」里完全看不见。
func invalidateUserCaches(ids []int) int {
	misses := 0
	for _, id := range ids {
		if err := invalidateUserCache(id); err != nil {
			misses++
		}
	}
	if misses > 0 {
		common.SysError(fmt.Sprintf(
			"qianye/groupns: %d 个用户的缓存失效失败 —— 他们在缓存 TTL 内仍按旧用户分组"+
				"解析可用清单与倍率,过期后自动恢复", misses))
	}
	return misses
}

// ─────────────────────────── 命名校验 ───────────────────────────

// validateNewUserGroupName 是新建与改名共用的那一道校验。
//
// 五条判据,顺序即报错优先级:
//
//  1. 非空、长度按 **rune** 计(见 model.go 的 maxGroupNameLen)
//  2. 不是 auto —— 它是令牌的伪分组,abilities 里根本没有这个键
//  3. 不与已登记的用户分组重名(**归一化后**比较,理由见下)
//  4. 不与模型分组 roster 冲突(跨命名空间唯一性)
//  5. 不是一个已经有人在用、只是还没登记的名字 —— 那种情况该走回填
//
// 第 3 条按 groupname.Normalize 比较而不是精确比较。它原本是为 MySQL 写的:
// qy_user_groups.name 继承库默认排序规则(5.7 general_ci / 8.0 0900_ai_ci),
// **两者都大小写不敏感**,精确比较放行的 "VIP" 会在 INSERT 时撞主键,
// 报出来的是一条 Error 1062,而运营看到的是「保存失败,请稍后重试」。
//
// 扩展库支持 PostgreSQL 之后这条判据反而更重要:PostgreSQL 的默认排序规则
// **大小写敏感**,库不会再替我们兜住 "VIP" 与 "vip"。也就是说唯一性从
// "库和应用各有一道" 变成了 "只剩应用这一道" —— 去掉归一化的后果在 MySQL 上
// 是一条难看的报错,在 PostgreSQL 上是两个同名分组真的并存,而计费侧是精确匹配。
func validateNewUserGroupName(gdb *gorm.DB, name string) error {
	if !registrableGroupName(name) {
		return fmt.Errorf("分组名不能为空,且长度不能超过 %d 个字符(按字符计,不是字节)", maxGroupNameLen)
	}
	if strings.ContainsAny(name, ",") {
		// channels.group 是逗号串,abilities 的键由 strings.Split(group, ",") 落成。
		// 一个带逗号的用户分组名今天不会走到那条路径上,但它会让任何一处把两个
		// 命名空间混用的代码把它拆成两半 —— 而本仓正在从那种混用里往外爬。
		return fmt.Errorf("分组名不能包含英文逗号:逗号在渠道分组里是分隔符")
	}
	if name != strings.TrimSpace(name) {
		return fmt.Errorf("分组名的首尾不能有空白字符 —— 它在界面上看不出来,而计费侧是精确匹配")
	}
	if name == autoGroup {
		return fmt.Errorf("auto 是令牌的自动分组伪名字,不能作为用户分组")
	}

	var rows []UserGroup
	if err := gdb.Select("name").Find(&rows).Error; err != nil {
		return err
	}
	target := normalizeGroupKey(name)
	for _, row := range rows {
		if normalizeGroupKey(row.Name) == target {
			return fmt.Errorf("用户分组 %q 已经存在(分组名在存储层大小写不敏感,"+
				"%q 与 %q 会是同一行)", row.Name, name, row.Name)
		}
	}

	// 跨命名空间唯一性:一个名字不能既是用户分组又是模型分组。
	//
	// **只对新建与改名生效**,对回填不生效(见 store.go):存量有 5 个这样的名字,
	// 对回填也生效会让 S0 整个失败,而 S0 的全部价值就是"先把现状看清楚"。
	// 从此重名集合只减不增。
	var models []ModelGroup
	if err := gdb.Select("name").Find(&models).Error; err != nil {
		return err
	}
	for _, row := range models {
		if normalizeGroupKey(row.Name) == target {
			return fmt.Errorf("%q 已经是一个**模型分组**(渠道池子)。"+
				"一个名字同时当两种分组正是本轮在拆掉的东西,请换一个名字", row.Name)
		}
	}
	if ratio_setting.ContainsGroupRatio(name) {
		return fmt.Errorf("%q 已经在分组倍率表(options.GroupRatio)里 —— "+
			"那张表的键是**模型分组**。请换一个名字", name)
	}
	if routed, err := HasRoute(context.Background(), model.DB, name); err == nil && routed {
		return fmt.Errorf("%q 在 abilities 里有启用的渠道,它是一个**模型分组**。请换一个名字", name)
	}

	inUse, err := model.QyDistinctUserGroups()
	if err == nil {
		for _, existing := range inUse {
			if normalizeGroupKey(existing) == target {
				return fmt.Errorf("已经有用户挂在 %q 上,只是这个名字还没登记。"+
					"请先执行一次回填把它登记进来,而不是新建一个同名的", existing)
			}
		}
	}
	return nil
}

// normalizeGroupKey 是**存储层**的比较键:去两侧空白 + 折叠大小写。
//
// 刻意不复用 qianye/groupname.Normalize:那个包的语义是"判定与计费口径",
// 而这里回答的是另一个问题 ——「这两个名字在 MySQL 的 ci 排序规则下会不会撞主键」。
// 今天两者的实现恰好一样,但把它们绑在一起会让将来任何一侧的调整静默影响另一侧。
func normalizeGroupKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ─────────────────────────── 迁移影响面 ───────────────────────────

// UsableChange 是迁移之后某个模型分组上的变化。
type UsableChange struct {
	ModelGroup string `json:"model_group"`
	// Kind ∈ added / removed / repriced。
	Kind string `json:"kind"`
	// FromRatio / ToRatio 是十进制字符串,不是 float64:它们会被当成报价读,
	// 而一次 JSON float64 往返会把 0.1 印成 0.10000000000000001。
	FromRatio string `json:"from_ratio,omitempty"`
	ToRatio   string `json:"to_ratio,omitempty"`
}

// MigrationDiff 回答「这批人迁过去之后,可用模型分组与倍率会怎么变」。
//
// ══════════ 为什么这一段不能省 ══════════
//
// 迁移改变的不只是一个字符串。用户分组决定两件直接花钱的事:
//
//	可用模型分组  service.GetUserUsableGroups(用户分组)
//	实际倍率      GroupGroupRatio[用户分组][模型分组],未配则回落 GroupRatio[模型分组]
//
// 只显示「把 707 个人从 A 迁到 B」而不显示这两项,运营就是在盲按 —— 而迁移过去
// 之后少掉的每一个模型分组都是那批人手上令牌的当场 403,多出来的每一格倍率差
// 都是从下一秒开始的账单变化。
type MigrationDiff struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Changes 按 kind 再按分组名排序,顺序稳定。
	Changes []UsableChange `json:"changes"`
	// Unchanged 是两边都能用且倍率相同的模型分组数。
	Unchanged int `json:"unchanged"`
	// LosesEverything 为 true 表示迁过去之后一个模型分组都不可用。
	// 那等于这批人的全部令牌当场停摆,必须在弹窗里单独醒目提示。
	LosesEverything bool `json:"loses_everything"`
}

// BuildMigrationDiff 现算两个用户分组的可用清单与倍率差。
//
// 走 service.GetUserUsableGroups 与 ratio_setting.InspectGroupRatio,与鉴权和
// 三条计费路径**同源**。刻意不在这里重写一份判据:第四份复制品迟早自己漂移,
// 而漂移的表现正是"预览说不变、迁完之后全 403"。
func BuildMigrationDiff(from, to string, usable func(string) map[string]string) MigrationDiff {
	diff := MigrationDiff{From: from, To: to}
	if to == "" {
		return diff
	}
	before := usable(from)
	after := usable(to)

	seen := make(map[string]bool, len(before)+len(after))
	for name := range before {
		seen[name] = true
	}
	for name := range after {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	changes := make([]UsableChange, 0, len(names))
	for _, name := range names {
		_, hadBefore := before[name]
		_, hasAfter := after[name]
		switch {
		case hadBefore && !hasAfter:
			changes = append(changes, UsableChange{ModelGroup: name, Kind: "removed"})
		case !hadBefore && hasAfter:
			changes = append(changes, UsableChange{ModelGroup: name, Kind: "added"})
		default:
			fromRatio := ratio_setting.InspectGroupRatio(from, name).Ratio
			toRatio := ratio_setting.InspectGroupRatio(to, name).Ratio
			if fromRatio == toRatio {
				diff.Unchanged++
				continue
			}
			changes = append(changes, UsableChange{
				ModelGroup: name, Kind: "repriced",
				FromRatio: ratioText(fromRatio), ToRatio: ratioText(toRatio),
			})
		}
	}
	diff.Changes = changes
	diff.LosesEverything = len(after) == 0
	return diff
}
