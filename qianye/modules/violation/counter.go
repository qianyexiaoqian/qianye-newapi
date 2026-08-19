package violation

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// counterState 是一次计数推进后的结果。
type counterState struct {
	HitCount int
	BanCycle int
	// Reached 表示推进后的计数已经达到封号阈值。
	//
	// 刻意用"已达"而不是"恰好跨越":跨越是一个只出现一次的瞬时信号,一旦被
	// 速率闸或一次执行失败消费掉,下一次违规的 after-weight 就已经越过阈值了,
	// 判据永远为假 —— 该用户在整个滚动窗口(默认 24 小时)内再也不会被封号,
	// 而补偿任务只扫已存在的封禁行,对"从未认领成功"的跨越无能为力。
	// 改成"已达"之后,判据完全由持久化的 hit_count 推导,不再是一次性的:
	// 阻碍解除后的下一次违规会重新走到封号判定。
	// 重复封号由 (user_id, ban_cycle) 唯一索引兜住,代价只是一次冲突插入。
	Reached bool
	// Category / CatHitCount / CatReached 是**单类型线**那一半。
	//
	// # 两条线,一个封禁周期
	//
	// HitCount/Reached 是账号总量线(跨全部类型,阈值来自用户分组的 BanPolicy);
	// CatHitCount/CatReached 是这次命中所属类型自己的线(阈值来自 Category)。
	// 两者是 OR:任一越过都要求处置。合起来的答案由 anyReached 给出。
	//
	// 为什么必须是 OR 而不是"类型线覆盖总量线":总量线回答的是"这个账号整体上
	// 违规太多",类型线回答的是"这一类特别严重"。让后者覆盖前者,一个在五个类型上
	// 各犯 9 次、每类阈值都是 10 的账号就永远不会被处置 —— 而那正是最典型的滥用形状。
	//
	// 封号只会发生一次:认领的互斥键是 (user_id, ban_cycle),ban_cycle 只存在于
	// 账号维度的 Counter 上,两条线共用它。
	Category    Category
	CatHitCount int
	CatReached  bool
	// Policy 是本次推进所依据的策略档(按用户当时的分组解析)。
	//
	// 必须随计数一起返回,而不是让下游再解析一次:两次解析之间策略表可能被改
	// (管理端改阈值 → invalidateBanPolicies → 下一次读回源),于是"按 A 档
	// 判定达到阈值"和"按 B 档执行处置"会落在同一条链路上,
	// 而 Ban 行冻结下来的 threshold 与真正触发它的那个数字对不上。
	Policy BanPolicy
}

// bumpCounter 原子地推进用户的滚动窗口计数。
//
// **调用方只有一处,且必须先排除影子命中**(guard.go 的 persist)。
// 这张表是自动封号判据的唯一数据源,往里写一次影子命中就等于把"不会真实执行"
// 变成"延迟几分钟之后真实执行"。
//
// 并发正确性是这个函数存在的全部理由:多节点同时把计数推过阈值时,
// 必须保证只有一个节点观察到"跨越",否则会重复封号、重复告警。
//
// 实现要点:
//   - INSERT ... ON DUPLICATE KEY UPDATE 是单条原子语句,窗口过期判断与重置
//     都在这条语句里完成,不存在"读到过期窗口再写"的竞态;
//   - 紧随其后的 SELECT 在同一个事务里执行。upsert 已经对该行加了排他锁并持有到
//     提交,因此这次读到的必然是本次推进的结果,而不会读到别人已经推进过的值。
//     (刻意不用 LAST_INSERT_ID():它是会话级变量,GORM 连接池会把 Exec 与 Raw
//     发到不同连接上,跨连接读到的是别人的值 —— 这是最隐蔽的一类 bug。)
//
// userGroup 决定用哪一档策略:窗口长度与阈值都来自那一档,不再是全局 YAML。
// 空分组由 resolveBanPolicy 折进兜底档。
func bumpCounter(ctx context.Context, gdb *gorm.DB, userId, weight int, userGroup string) (counterState, error) {
	var st counterState
	policy := resolveBanPolicy(userGroup)
	st.Policy = policy
	if weight <= 0 {
		return st, nil
	}
	if gdb == nil {
		return st, db.ErrNotReady
	}

	now := common.GetTimestamp()
	winFrom := windowFloor(now, policy.WindowHours)

	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO qy_violation_counter
			(user_id, window_start, hit_count, total_count, ban_cycle, last_hit_at, updated_at)
			VALUES (?, ?, ?, ?, 0, ?, ?)
			ON DUPLICATE KEY UPDATE
				hit_count    = IF(window_start < ?, ?, hit_count + ?),
				window_start = IF(window_start < ?, ?, window_start),
				total_count  = total_count + ?,
				last_hit_at  = ?,
				updated_at   = ?`,
			userId, now, weight, weight, now, now,
			winFrom, weight, weight,
			winFrom, now,
			weight, now, now).Error; err != nil {
			return err
		}
		var row Counter
		if err := tx.Where("user_id = ?", userId).Take(&row).Error; err != nil {
			return err
		}
		st.HitCount = row.HitCount
		st.BanCycle = row.BanCycle
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		// 失败也要带着策略回去:调用方靠 st.Policy 写审计与日志,
		// 返回一个零值 Policy 会让"当时按哪一档判的"变成空白。
		return counterState{Policy: policy}, err
	}

	st.Reached = reachedThreshold(st.HitCount, policy.Threshold)
	return st, nil
}

// reachedThreshold 判断计数是否已经达到封号阈值。阈值 <= 0 表示关闭自动封号。
func reachedThreshold(after, threshold int) bool {
	return threshold > 0 && after >= threshold
}

// ───────────────────────────── 统计窗口 ─────────────────────────────
//
// 窗口这一列同时出现在两处:BanPolicy.WindowHours(账号总量线)与
// Category.WindowHours(单类型线)。两处的取值语义、回落规则、以及"无限"的
// 表示法必须逐字一致 —— 界面上它们并排显示,一个说"24 小时内累计"、另一个说
// "累计",用户会以为其中一个写错了。所以口径只落在下面这三个函数上,
// **任何地方都不要再写 `if windowHours <= 0 { windowHours = 24 }`**。

// WindowUnlimited 是「统计窗口没有期限」的哨兵值:达到次数就处置,
// 不管那些命中分布在多长的时间里。
//
// # 为什么是 -1,不是 0,也不是另加一个布尔列
//
// 0 **已经有语义了**:本文件、category.go、api_user.go、api_admin_category.go、
// banpolicy.go 一共七处读点全都写着 `if windowHours <= 0 { windowHours = 24 }`,
// 也就是"没配 ⇒ 按 24 小时算"。拿 0 当"无限"要同时做两件事:把字面义反过来
// (窗口为零 = 永不过期),以及静默改写任何一行存量 0 的含义 —— 那一行原本按
// 24 小时判,改完之后一年前的命中重新算数,**用户会因为一次配置迁移被封号**,
// 而库里没有任何东西记录这件事发生过。本仓在 `commission.holding_days: 0`
// 上栽过同一个坑(显式 0 被当成没填),不再栽第二次。
//
// 另加一个布尔列 window_unlimited 的语义最清楚,代价是"两个字段要保持一致":
// (unlimited=true, hours=72) 这种组合能被写进库,而它没有意义;要挡住它就得
// 在两个校验器、两个表单、两条迁移上各写一遍归一,而漏掉的那一处会让界面显示
// 72 小时、判定按无限执行。哨兵值是单字段,不存在"不一致"这个状态。
//
// # -1 是干净的:它在存量数据里不可能出现
//
// 两个写入口(validateBanPolicy / validateCategory)历来都要求 >= 1;种子、
// AutoMigrate 默认值、yamlFallbackPolicy 给的都是 24。也就是说本列的持久化取值
// 只可能是正数 —— 现网核对过一遍,两张表全部行都是 24,一个 0 都没有。
// 因此启用这个哨兵**不改写任何一行存量配置的语义**,不需要迁移。
//
// # 其余负数一律不是"无限"
//
// 判据刻意写成 `== WindowUnlimited` 而不是 `< 0`。DBA 手工插进来的 -7 会落进
// 下面的 `<= 0` 回落分支变成 24 小时 —— 那是**保守方向**(窗口更短、更少人被封)。
// 反过来把任意负数都当成无限,一次手滑就是全站范围的窗口放大。
const WindowUnlimited = -1

// defaultWindowHours 是窗口没配(0 或无法识别的负数)时的回落值。
// 它同时是 BanPolicy / Category 两张表的 gorm 默认值,三处必须同数。
const defaultWindowHours = 24

// windowUnlimitedFloor 是无限窗口的时间下界。
//
// 它会直接进 SQL 与 `window_start < ?` 比较,而 window_start 是 Unix 时间戳、
// 恒 >= 0,所以任何负数都能保证"窗口永不过期"。取 -1 而不是 math.MinInt64:
// 后者一旦被谁当成时间戳打印出来是公元前 2.9 亿年,而 -1 一眼就是个哨兵。
const windowUnlimitedFloor int64 = -1

// effectiveWindowHours 把库里那一列折成**实际生效**的窗口,给展示与回显用。
//
// 无限窗口原样返回 WindowUnlimited:它不是一个小时数,任何调用方拿它做算术
// (乘 3600、跟别的窗口比大小)都是错的,所以刻意不折成一个很大的数字 ——
// 那种"够大就当无限"的写法会在下一次有人把上界调大时静默失效。
func effectiveWindowHours(windowHours int) int {
	if windowHours == WindowUnlimited {
		return WindowUnlimited
	}
	if windowHours <= 0 {
		return defaultWindowHours
	}
	return windowHours
}

// windowFloor 给出滚动窗口的时间下界:window_start 小于它就说明窗口已经滚过,
// 计数该清零;计数查询也用它当"只算这个时间点之后的"下界。
//
// 无限窗口返回 windowUnlimitedFloor,于是窗口永不滚动、计数只增不清。
//
// # 无限窗口到底"累计"到多久以前 —— 界面必须照这句话写
//
// 它累计的是**计数器行**里的 hit_count,不是违规记录表的历史。所以它的真实
// 含义是"自这一行计数器建立、或最近一次被清零起的累计",而清零有三个来源:
// 管理员在计数器列表里点「重置」(resetUserCounter)、解封时勾了「清零计数」
// (openNewBanCycle)、以及管理员撤销一条违规记录时的回退(revertHitCounters)。
// 违规记录表的保留期清理(runRetentionGC)**不影响**这个数 —— 计数从来不从
// 记录表现算。反过来说,超过保留期的那几次在管理端已经查不到明细,但它们仍然
// 算在这个数里,这一点也必须让运营知道。
func windowFloor(now int64, windowHours int) int64 {
	if windowHours == WindowUnlimited {
		return windowUnlimitedFloor
	}
	return now - int64(effectiveWindowHours(windowHours))*3600
}

// windowWidens 判断窗口从 before 改成 next 是不是**变长了**。
//
// 它只服务于二次确认那条判据(tightensBanPolicy / categoryTightens):窗口变长
// 意味着更久以前的命中重新算数,一批原本够不到线的账号当场处在越线状态。
//
// 无限必须排在所有有限值之上。写成裸的 `next.WindowHours > before.WindowHours`
// 时 -1 比任何正数都小,于是"24 小时 → 无限"这个**本轮最激进的一种改动**
// 会被判成放宽,连影响面预览都不弹 —— 那正是二次确认要防的形状。
func windowWidens(before, next int) bool {
	if next == WindowUnlimited {
		return before != WindowUnlimited
	}
	if before == WindowUnlimited {
		return false
	}
	return effectiveWindowHours(next) > effectiveWindowHours(before)
}

// thresholdSemanticsAnyLine 是"两条线任一越过即触发"这个口径的机器可读名。
//
// 它挂在 anyReached 旁边而不是接口层,因为下面那个 switch **就是**这个口径本身。
// 管理端列表、建议阈值预览、用户端公示三处都下发这个常量:有人把判定改成
// "两条都要越过"时,改的是 anyReached,而这个常量必须跟着改 —— 于是三处文案
// 一起变。三处各写一份字面量的结果是界面说 OR、实际是 AND,而没有任何测试会红。
const thresholdSemanticsAnyLine = "any_line"

// anyReached 回答"这次命中之后要不要走封号判定",并说明是撞了哪条线。
//
// 这是"到底几次封号"这个问题在代码里的**唯一**答案:两条线各自独立判定,
// 任一越过即触发,撞了哪条由 BanTrigger* 冻结进封禁行。没有这个单一出口,
// 判据会散在 resolveBanClaim 与用户端公示两处,而它们一旦不一致,
// 用户看到的"还剩几次"就会与真实处置对不上 —— 那比不给数字更糟。
func anyReached(st counterState) (bool, string) {
	switch {
	case st.Reached && st.CatReached:
		return true, BanTriggerBoth
	case st.CatReached:
		return true, BanTriggerCategory
	case st.Reached:
		return true, BanTriggerGlobal
	}
	return false, ""
}

// 用户端"还差几次"落在哪条线上。
const (
	ThresholdLineNone     = "none"
	ThresholdLineAccount  = "account"
	ThresholdLineCategory = "category"
)

// thresholdLineState 是"离处置最近的那条线"的答案:哪条线、还差几次。
type thresholdLineState struct {
	// Line 取 ThresholdLine* 之一。none 表示当前一条生效的线都没有,
	// 此时 Remaining 无意义(调用方必须按"不设门槛"渲染,而不是渲染 0)。
	Line string
	// Remaining 是这条线上还差几次。已达门槛时为 0。
	Remaining int
	// CategoryId 只在 Line == ThresholdLineCategory 时有意义。
	// 它是**内部标识**,调用方要拿它去查公示标题时必须自己确认那一类已公示。
	CategoryId int64

	// Threshold / WindowHours / HitCount 是**这条线自己的**三个数。
	//
	// 用户端曾经只下发账号总量线的窗口与阈值,同时把 remaining_line 报成
	// "类型" —— 于是被类型线封掉的人看到的是「触发线:类型」配上「阈值 0、
	// 窗口 24 小时」,而真正把他封掉的那条线是「阈值 2、不限期限」。
	// 一句话里两条线的数字混在一起,用户没有任何办法看出来。
	Threshold   int
	WindowHours int
	HitCount    int
}

// nearestThresholdLine 在账号总量线与全部单类型线里挑出"还差最少"的那一条。
//
// # 为什么必须取全局最小值,而不是只算账号总量线
//
// anyReached 的语义是 OR:任一条线越过就触发处置。于是用户真正会被处置的时点
// 由**最先到达的那条线**决定,而不是账号总量线。只按账号总量线算"还剩几次",
// 在"账号线 10、某一类 3"这种再普通不过的配置下会告诉用户"还剩 8 次",
// 而他下一次命中就被封了 —— 这不是少给信息,是给了一个反向的信息。
//
// # 为什么未公示的类型也要参与
//
// published 只决定"这一类出不出现在公示列表里",不决定它计不计数、触不触发。
// 把未公示的类型排除在这个最小值之外,就是让公示页在观察期类型上重新失真。
// 折中点在于:数字必须诚实,而**是哪一类**可以不说 —— 这里只回 CategoryId,
// 调用方对未公示的类型不给名字即可。
//
// # 判据必须与 categoryReached / reachedThreshold 逐字同构
//
// 一条线"生效"的条件就是那两个函数里的条件:账号线要 threshold > 0,
// 类型线要 Enabled 且 Threshold > 0。任何一处放宽,用户看到的倒计时
// 就会与真实处置对不上。
func nearestThresholdLine(accountThreshold, accountHit, accountWindow int, cats []Category, catHits map[int64]int) thresholdLineState {
	best := thresholdLineState{Line: ThresholdLineNone}
	consider := func(line string, categoryId int64, threshold, hit, window int) {
		remaining := threshold - hit
		if remaining < 0 {
			remaining = 0
		}
		// 严格小于:两条线并列最近时保留先看到的那一条,而账号总量线先被考虑。
		// 并列时报账号线更保守 —— 它是所有用户都存在的那条线,不需要额外解释。
		if best.Line != ThresholdLineNone && remaining >= best.Remaining {
			return
		}
		best = thresholdLineState{
			Line: line, Remaining: remaining, CategoryId: categoryId,
			Threshold: threshold, WindowHours: window, HitCount: hit,
		}
	}

	if accountThreshold > 0 {
		consider(ThresholdLineAccount, 0, accountThreshold, accountHit, accountWindow)
	}
	for _, cat := range cats {
		if !cat.Enabled || cat.Threshold <= 0 {
			continue
		}
		consider(ThresholdLineCategory, cat.Id, cat.Threshold, catHits[cat.Id],
			effectiveWindowHours(cat.WindowHours))
	}
	return best
}

// claimBan 尝试认领一次封号。
//
// (user_id, ban_cycle) 唯一索引就是分布式互斥锁:一个封禁周期内只可能有一个
// 节点插入成功。created == false 表示本周期已被认领,此时返回库里那一行 ——
// 调用方需要看它的状态才能判断"是已有结论"还是"被速率闸推迟、现在可以提升执行"。
func claimBan(ctx context.Context, gdb *gorm.DB, userId, cycle int, recordId int64, status string, st counterState, trigger string) (*Ban, bool, error) {
	if gdb == nil {
		return nil, false, db.ErrNotReady
	}
	policy := st.Policy
	row := &Ban{
		UserId:          userId,
		BanCycle:        cycle,
		TriggerRecordId: recordId,
		HitCountAt:      st.HitCount,
		// 阈值与动作来自**本次判定用的那一档**,不是"现在配置里写着什么"。
		// 后者会在管理员改完策略之后把历史封禁行解释成另一个原因。
		Threshold:    policy.Threshold,
		PolicyGroup:  truncate(policy.UserGroup, 64),
		PolicyAction: policy.Action,
		// 类型线那一半同样要冻结。不冻结的话,一行 hit_count=3 / threshold=10 的
		// 封禁记录在管理端看起来完全说不通 —— 它其实撞的是"破限类 3 次"那条线,
		// 而那个事实只活在一次函数调用里。
		TriggerKind:       trigger,
		TriggerCategoryId: st.Category.Id,
		CategoryHitCount:  st.CatHitCount,
		CategoryThreshold: st.Category.Threshold,
		Status:            status,
		CreatedAt:         common.GetTimestamp(),
	}
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return nil, false, res.Error
	}
	if res.RowsAffected == 1 {
		return row, true, nil
	}
	var existing Ban
	if err := gdb.WithContext(ctx).
		Where("user_id = ? AND ban_cycle = ?", userId, cycle).Take(&existing).Error; err != nil {
		db.MarkFailure(err)
		return nil, false, err
	}
	return &existing, false, nil
}

// revertCounter 在管理员撤销违规记录时回退计数。
//
// 带 window_start 条件:窗口已经滚动过就不回退 —— 那个计数值已经失效,
// 强行减会把当前窗口的合法计数扣掉,反而放过真正的违规用户。
func revertCounter(userId, weight int, windowStart int64) error {
	if weight <= 0 {
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	// 夹到 0 用 CASE WHEN 而不是 GREATEST:后者是 MySQL/PostgreSQL 的函数,
	// SQLite 上根本不存在(它的对应物是标量 MAX(a,b),名字与聚合函数冲突)。
	// 扩展库按设计只跑 MySQL,但这条语句一旦在 SQLite 上执行就整条报错 ——
	// 而调用方只把错误写进日志,于是"撤销记录时计数没退"在任何测试里都看不出来。
	// 换成三家都认的写法,这条路径才真的被执行过一次(见
	// TestRevertHitCountersRevertsBothLines),而不是只被推理过。
	return gdb.Exec(`UPDATE qy_violation_counter
		SET hit_count = CASE WHEN hit_count - ? < 0 THEN 0 ELSE hit_count - ? END,
		    total_count = CASE WHEN total_count - ? < 0 THEN 0 ELSE total_count - ? END,
		    updated_at = ?
		WHERE user_id = ? AND window_start = ?`,
		weight, weight, weight, weight, common.GetTimestamp(), userId, windowStart).Error
}

// resetUserCounter 把某个用户当前窗口的违规计数清零,并返回清零前的那一行。
//
// # 它为什么必须存在
//
// 本轮之前,影子命中会照常推进 hit_count(见 persist 的说明)。也就是说
// **现网的计数器里已经混进了影子命中**,而修复只能保证从此以后不再混入,
// 无法分辨历史行里哪几次是影子。静默把这张表清掉是不可接受的:
// 那会连真实违规的累计一起抹掉,等于给所有正在攒次数的用户一次赦免,
// 而且没有任何记录说明这件事发生过。
//
// 所以给管理员一个显式动作:看得见、要人点、写审计。
//
// # 为什么只清 hit_count 与 window_start
//
// hit_count 是自动封号判据的唯一输入,它是被污染的那一个。
// total_count 是终身累计的展示值,清掉它会让"这个账号历史上违规过多少次"
// 这条运营信息永久消失;ban_cycle 更不能动 —— 它是封禁认领的互斥键,
// 回退它会让该用户的自动封号撞上历史唯一键从此静默失效。
//
// # 两条线必须一起清
//
// 封号判据是 OR:账号总量线(本表)与单类型线(qy_violation_cat_counter)任一
// 越线都要求处置。只清总量线的话,一个被类型线封掉的账号在"解封 + 重置计数"
// 之后类型计数仍然停在阈值上,而判据是 `after >= threshold` —— 下一次同类命中
// 必然再次越线。类型窗口配成 -1(不限期限)时那个计数永远不会自然滚出,账号
// 进入"解封 → 下一次同类违规立刻再封"的稳定态,而管理端没有任何页面显示类型线,
// 运营只能改数据库。
//
// 第二个返回值是清零前各类型线上的计数,给审计与响应用:管理员要看得见自己
// 这一次到底动了什么 —— 尤其是那些把他封掉的类型线。
func resetUserCounter(ctx context.Context, gdb *gorm.DB, userId int) (Counter, []CategoryCounter, bool, error) {
	if gdb == nil {
		return Counter{}, nil, false, db.ErrNotReady
	}
	var before Counter
	err := gdb.WithContext(ctx).Where("user_id = ?", userId).Take(&before).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Counter{}, nil, false, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return Counter{}, nil, false, err
	}
	var catsBefore []CategoryCounter
	if err := gdb.WithContext(ctx).Where("user_id = ? AND hit_count > 0", userId).
		Order("category_id asc").Find(&catsBefore).Error; err != nil {
		db.MarkFailure(err)
		return before, nil, false, err
	}
	now := common.GetTimestamp()
	if err := gdb.WithContext(ctx).Model(&Counter{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"hit_count":    0,
			"window_start": now,
			"updated_at":   now,
		}).Error; err != nil {
		db.MarkFailure(err)
		return before, catsBefore, false, err
	}
	if _, err := resetUserCategoryCounters(ctx, gdb, userId); err != nil {
		db.MarkFailure(err)
		return before, catsBefore, false, err
	}
	return before, catsBefore, true, nil
}

// resetUserCategoryCounters 把某个用户**全部类型线**的当前窗口计数清零,
// 返回被清掉的行数。
//
// 与总量线同口径:只清 hit_count 与 window_start,保留 total_count(终身累计的
// 展示值)。类型线上没有 ban_cycle —— 封禁认领的互斥键只存在于 Counter 上,
// 两条线共用同一个周期。
func resetUserCategoryCounters(ctx context.Context, gdb *gorm.DB, userId int) (int64, error) {
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Model(&CategoryCounter{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"hit_count":    0,
			"window_start": now,
			"updated_at":   now,
		})
	return res.RowsAffected, res.Error
}

// openNewBanCycle 在解封时把周期 +1。
//
// 不 +1 的后果:下次达到阈值时 claimBan 的唯一键必然冲突,自动封号从此
// 对该用户静默失效。这是本模块最隐蔽的失效模式,必须与解封绑定执行。
//
// resetCount 的语义在封号判据改成"已达阈值"之后变得更实在了:不清零就意味着
// 这些次数仍然算数,该用户解封后只要再违规一次就会立刻被重新封禁。
// 想给一次真正的重新开始,解封时必须勾上 reset_counter。
// (旧的"恰好跨越"判据下不清零等于白留 —— 计数摆在那里却永远不会再触发封号,
// 那正是 B3 要消除的静默失效。)
func openNewBanCycle(userId int, resetCount bool) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	sets := "ban_cycle = ban_cycle + 1, updated_at = ?"
	args := []any{common.GetTimestamp()}
	if resetCount {
		sets += ", hit_count = 0"
	}
	if err := gdb.Exec(fmt.Sprintf("UPDATE qy_violation_counter SET %s WHERE user_id = ?", sets),
		append(args, userId)...).Error; err != nil {
		return err
	}
	if !resetCount {
		return nil
	}
	// 类型线必须跟着清。两条线是 OR,只清总量线的话"给一次重新开始"实际给的是
	// "再犯一次就立刻再封" —— 而且封他的那条线在管理端一个页面都看不到。
	_, err := resetUserCategoryCounters(context.Background(), gdb, userId)
	return err
}

// resolveBanClaim 决定本次是否要执行封号,并把这个决定持久化。
//
// 返回非 nil 表示本节点拿到了执行权,调用方必须紧接着执行主库封号。返回 nil 的
// 每一种情况都在库里或日志里留了痕:绝不允许"该封没封"只活在一次函数调用里。
//
// 三条分支的取舍:
//   - 熔断钳位:一行封禁记录都不写。影子的定义就是"只观察、不产生任何处置副作用",
//     写认领行会污染管理端的封禁列表。
//     全局模式删除后,这条分支只可能被一种时序命中:命中当时规则是 enforce
//     且熔断未触发(于是 persist 推进了计数),而异步 worker 跑到这里时熔断刚好
//     跳闸。影子命中本身根本走不到这里 —— persist 在 bumpCounter 之前就返回了。
//     影子期间的命中**不会**累积到 hit_count,所以"熔断解除后下一次违规会重新
//     走到这里"依赖的是那次**真实**命中自己的权重,而不是影子期间攒下的计数。
//     这正是裁决 2 要的语义:观察期不给用户留下任何处置负债。
//   - 「仅记录」档:落一行 observed 就返回。它与熔断分支的区别是**必须留行** ——
//     熔断是系统自己的临时刹车(不该污染封禁列表),而「仅记录」是管理员选定的
//     长期口径,那份"谁越了线"的名单正是这一档存在的全部理由。
//   - 速率闸:直接以 deferred 状态落行(而不是"先落 pending 再改状态"),
//     进程在两步之间崩溃会留下一行会被补偿任务执行的 pending,那等于绕过速率闸。
//   - 已存在的行:只有 deferred 与 observed 可以被提升(两者都还没有结论)。
//     pending / failed 是补偿任务的地盘,banned / skipped / unbanned 是终态。
func resolveBanClaim(ctx context.Context, gdb *gorm.DB, rec *Record, st counterState) *Ban {
	reached, trigger := anyReached(st)
	if gdb == nil || !reached {
		return nil
	}
	if tripped, reason := breakerTripped(); tripped {
		shadowHits.Add(1)
		common.SysLog(fmt.Sprintf(
			"qianye/violation: 熔断已触发(%s),全部规则临时按影子执行;用户 %d 违规计数已达 %d(触发线 %s),未执行自动封号",
			reason, rec.UserId, st.HitCount, trigger))
		return nil
	}

	// 「仅记录」档在这里止步:落一行 observed 让管理员看得见谁越了线,
	// 但一个字节都不碰主库。它是终态,不会被补偿任务捡起来执行 ——
	// 那正是这一档的定义。认领仍然走 claimBan,是因为
	// (user_id, ban_cycle) 唯一索引同样要保证一个周期只写一行:
	// 越线之后的每一次违规都会再走到这里,不去认领就会刷出一串重复行。
	if st.Policy.Action == PolicyActionRecord {
		if _, created, err := claimBan(ctx, gdb, rec.UserId, st.BanCycle,
			rec.Id, BanObserved, st, trigger); err == nil && created {
			common.SysLog(fmt.Sprintf(
				"qianye/violation: 用户 %d 违规计数已达 %d(触发线 %s,策略档 %q,阈值 %d),该档只要求记录,未处置账号",
				rec.UserId, st.HitCount, trigger, policyLabel(st.Policy), st.Policy.Threshold))
		}
		return nil
	}

	rateExceeded := banRateExceeded()
	status := BanPending
	if rateExceeded {
		status = BanDeferred
	}
	ban, created, err := claimBan(ctx, gdb, rec.UserId, st.BanCycle, rec.Id, status, st, trigger)
	if err != nil || ban == nil {
		return nil
	}
	if rateExceeded {
		if created {
			common.SysError(fmt.Sprintf(
				"qianye/violation: 每小时自动封号已达上限,用户 %d 的封号已记为 deferred(ban=%d)待人工处理",
				rec.UserId, ban.Id))
		}
		return nil
	}
	if !created {
		// 可提升的只有两种"还没有结论"的状态:
		//   - deferred:速率闸挡下的,闸松了就该执行;
		//   - observed:落行时策略档是「仅记录」,而现在这一档已经被改成
		//     restrict/ban。不提升的话,管理员把策略收紧之后,**已经越线的存量
		//     账号永远不会被处置** —— 他们的本周期唯一键早已被 observed 行占住,
		//     后续每一次违规都撞冲突返回,收紧动作对他们完全无效。
		//     这正是影响面预览要回答的那批人,预览说会处置、实际不处置是最坏的组合。
		// pending / failed 是补偿任务的地盘,banned / skipped / unbanned 是终态。
		if ban.Status != BanDeferred && ban.Status != BanObserved {
			return nil
		}
		from := ban.Status
		// CAS 精确锁定读到的那个状态,是这条提升路径唯一的互斥手段。
		// 提升的同时把阈值与动作改写成**当前这一档**:这一行原本冻结的是
		// 「仅记录」档的判据,执行的却是新档,两者不一致会让复盘对不上。
		res := gdb.WithContext(ctx).Model(&Ban{}).
			Where("id = ? AND status = ?", ban.Id, from).
			Updates(map[string]any{
				"status":        BanPending,
				"threshold":     st.Policy.Threshold,
				"policy_group":  truncate(st.Policy.UserGroup, 64),
				"policy_action": st.Policy.Action,
				// 触发线也要跟着改写:这一行原本冻结的可能是"上次撞的是类型线",
				// 而这次提升执行依据的是**本次**判定。两者不一致会让复盘对不上,
				// 与 threshold/policy_action 是同一个理由。
				"trigger_kind":        trigger,
				"trigger_category_id": st.Category.Id,
				"category_hit_count":  st.CatHitCount,
				"category_threshold":  st.Category.Threshold,
			})
		if res.Error != nil {
			db.MarkFailure(res.Error)
			return nil
		}
		if res.RowsAffected == 0 {
			return nil
		}
		ban.Status = BanPending
		ban.Threshold = st.Policy.Threshold
		ban.PolicyGroup = truncate(st.Policy.UserGroup, 64)
		ban.PolicyAction = st.Policy.Action
		ban.TriggerKind = trigger
		ban.TriggerCategoryId = st.Category.Id
		ban.CategoryHitCount = st.CatHitCount
		ban.CategoryThreshold = st.Category.Threshold
	}
	noteBan()
	return ban
}

// maybeAutoBan 在计数达到阈值时执行封号。返回是否真的封了。
func maybeAutoBan(ctx context.Context, gdb *gorm.DB, rec *Record, st counterState) bool {
	ban := resolveBanClaim(ctx, gdb, rec, st)
	if ban == nil {
		return false
	}
	if err := disableUserForViolation(ctx, rec.UserId, ban); err != nil {
		if errors.Is(err, errBanSkipped) {
			markBan(gdb, ban.Id, BanSkipped, "")
			return false
		}
		markBan(gdb, ban.Id, BanFailed, err.Error())
		common.SysError(fmt.Sprintf("qianye/violation: 用户 %d 自动封禁失败: %v", rec.UserId, err))
		return false
	}
	markBan(gdb, ban.Id, BanBanned, "")
	return true
}

// markBan 更新封禁执行结果。失败时只记日志:封禁本身已经生效,
// 状态回写失败会被补偿任务收敛。
func markBan(gdb *gorm.DB, id int64, status, lastErr string) {
	if gdb == nil {
		return
	}
	updates := map[string]any{"status": status, "last_error": truncate(lastErr, 512)}
	if status == BanBanned {
		updates["banned_at"] = common.GetTimestamp()
	}
	if err := gdb.Model(&Ban{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf("qianye/violation: 回写封禁状态失败(id=%d): %v", id, err))
	}
}
