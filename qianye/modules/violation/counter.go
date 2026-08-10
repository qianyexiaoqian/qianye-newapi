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

	windowHours := policy.WindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	now := common.GetTimestamp()
	winFrom := now - int64(windowHours)*3600

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

// claimBan 尝试认领一次封号。
//
// (user_id, ban_cycle) 唯一索引就是分布式互斥锁:一个封禁周期内只可能有一个
// 节点插入成功。created == false 表示本周期已被认领,此时返回库里那一行 ——
// 调用方需要看它的状态才能判断"是已有结论"还是"被速率闸推迟、现在可以提升执行"。
func claimBan(ctx context.Context, gdb *gorm.DB, userId, cycle, hitCount int, recordId int64, status string, policy BanPolicy) (*Ban, bool, error) {
	if gdb == nil {
		return nil, false, db.ErrNotReady
	}
	row := &Ban{
		UserId:          userId,
		BanCycle:        cycle,
		TriggerRecordId: recordId,
		HitCountAt:      hitCount,
		// 阈值与动作来自**本次判定用的那一档**,不是"现在配置里写着什么"。
		// 后者会在管理员改完策略之后把历史封禁行解释成另一个原因。
		Threshold:    policy.Threshold,
		PolicyGroup:  truncate(policy.UserGroup, 64),
		PolicyAction: policy.Action,
		Status:       status,
		CreatedAt:    common.GetTimestamp(),
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
	return gdb.Exec(`UPDATE qy_violation_counter
		SET hit_count = GREATEST(hit_count - ?, 0),
		    total_count = GREATEST(total_count - ?, 0),
		    updated_at = ?
		WHERE user_id = ? AND window_start = ?`,
		weight, weight, common.GetTimestamp(), userId, windowStart).Error
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
func resetUserCounter(ctx context.Context, gdb *gorm.DB, userId int) (Counter, bool, error) {
	if gdb == nil {
		return Counter{}, false, db.ErrNotReady
	}
	var before Counter
	err := gdb.WithContext(ctx).Where("user_id = ?", userId).Take(&before).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Counter{}, false, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return Counter{}, false, err
	}
	now := common.GetTimestamp()
	if err := gdb.WithContext(ctx).Model(&Counter{}).Where("user_id = ?", userId).
		Updates(map[string]any{
			"hit_count":    0,
			"window_start": now,
			"updated_at":   now,
		}).Error; err != nil {
		db.MarkFailure(err)
		return before, false, err
	}
	return before, true, nil
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
	return gdb.Exec(fmt.Sprintf("UPDATE qy_violation_counter SET %s WHERE user_id = ?", sets),
		append(args, userId)...).Error
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
	if gdb == nil || !st.Reached {
		return nil
	}
	if tripped, reason := breakerTripped(); tripped {
		shadowHits.Add(1)
		common.SysLog(fmt.Sprintf(
			"qianye/violation: 熔断已触发(%s),全部规则临时按影子执行;用户 %d 违规计数已达 %d,未执行自动封号",
			reason, rec.UserId, st.HitCount))
		return nil
	}

	// 「仅记录」档在这里止步:落一行 observed 让管理员看得见谁越了线,
	// 但一个字节都不碰主库。它是终态,不会被补偿任务捡起来执行 ——
	// 那正是这一档的定义。认领仍然走 claimBan,是因为
	// (user_id, ban_cycle) 唯一索引同样要保证一个周期只写一行:
	// 越线之后的每一次违规都会再走到这里,不去认领就会刷出一串重复行。
	if st.Policy.Action == PolicyActionRecord {
		if _, created, err := claimBan(ctx, gdb, rec.UserId, st.BanCycle, st.HitCount,
			rec.Id, BanObserved, st.Policy); err == nil && created {
			common.SysLog(fmt.Sprintf(
				"qianye/violation: 用户 %d 违规计数已达 %d(策略档 %q,阈值 %d),该档只要求记录,未处置账号",
				rec.UserId, st.HitCount, policyLabel(st.Policy), st.Policy.Threshold))
		}
		return nil
	}

	rateExceeded := banRateExceeded()
	status := BanPending
	if rateExceeded {
		status = BanDeferred
	}
	ban, created, err := claimBan(ctx, gdb, rec.UserId, st.BanCycle, st.HitCount, rec.Id, status, st.Policy)
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
