package violation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// gcBatchSize 是单批删除的行数。
//
// 分批而不是一条 DELETE 删完:证据表的行可达数百 KB,一次删几十万行会产生
// 超长事务,拖垮从库延迟并让 binlog 暴涨。每批之间还要主动让出,不与业务抢 IO。
const gcBatchSize = 500

// runRetentionGC 按保留期清理证据与记录。
func runRetentionGC(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	days := config.Get().Violation.EvidenceRetentionDays
	if days <= 0 {
		return
	}
	before := common.GetTimestamp() - int64(days)*86400

	// 证据先删。它是体积的绝对大头,也是隐私风险最高的部分,
	// 保留期一到必须最先消失。
	for {
		if ctx.Err() != nil {
			return
		}
		res := gdb.WithContext(ctx).Exec(
			`DELETE FROM qy_violation_payload WHERE created_at < ? ORDER BY created_at LIMIT ?`,
			before, gcBatchSize)
		if res.Error != nil {
			db.MarkFailure(res.Error)
			common.SysError("qianye/violation: 清理证据失败: " + res.Error.Error())
			return
		}
		if res.RowsAffected < gcBatchSize {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 记录本身保留更久(它只有约 1KB/行,而且是申诉与对账的依据),
	// 这里只把已经没有证据的行标回 has_payload=false,避免管理端点开空白详情。
	if err := gdb.WithContext(ctx).Exec(
		`UPDATE qy_violation_record SET has_payload = 0
		 WHERE has_payload = 1 AND created_at < ?`, before).Error; err != nil {
		db.MarkFailure(err)
	}

	// 已经归零且从未被封禁的计数行没有任何价值,顺手回收。
	windowHours := config.Get().Violation.AutoBanWindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	staleBefore := common.GetTimestamp() - int64(windowHours)*3600*2
	if err := gdb.WithContext(ctx).Exec(
		`DELETE FROM qy_violation_counter
		 WHERE ban_cycle = 0 AND total_count = 0 AND last_hit_at < ?`, staleBefore).Error; err != nil {
		db.MarkFailure(err)
	}
}

// runRefundStateReconcile 收敛"退款资金单已 Success、违规记录却仍写着已扣费"的行。
//
// 这批行的来源是升级:旧版补偿任务还没有 Resolver,确认主库生效后直接把资金单标成
// Success 就完事,fee_status 永久停在 charged —— 用户端的"本月违规扣费"会一直把这笔
// 已退的钱算进去,而管理员再点一次退款只会撞上幂等键(见 confirmRefundSettled)。
// 靠管理员逐条重点是碰运气,必须有一条自动收敛路径。
//
// 收敛动作复用 markRecordRefunded(带 fee_status 条件,幂等),所以这个任务重复跑、
// 与补偿任务并跑都不会把 refund_quota 写第二遍。收敛成功的行会立刻掉出扫描集合,
// 任务因此是自限的,不会每轮全表扫。
func runRefundStateReconcile(ctx context.Context) {
	reconcileRefundStates(ctx, db.Get())
}

// reconcileRefundStates 是上面那条收敛逻辑的本体,gdb 由调用方注入。
//
// 独立出来是为了能直接测:"哪些单算错位、哪些不算"是这里唯一的判断,
// 放宽任何一条(不限 kind、不限 idem_scope、不要求 Success)都会把一笔还没落定、
// 甚至根本不是违规退款的操作写成"已退款"。
func reconcileRefundStates(ctx context.Context, gdb *gorm.DB) {
	if gdb == nil {
		return
	}
	// 子查询而不是先捞记录再逐条查单:退款是低频操作,资金单侧按
	// (idem_scope, idem_key) 唯一索引定位,一次往返就能圈出全部错位行。
	staleRecords := gdb.Model(&Record{}).Select("rec_no").
		Where("fee_status IN ?", []string{FeeStatusCharged, FeeStatusTruncated})

	var orders []qymodel.FundOrder
	if err := gdb.WithContext(ctx).
		Where("kind = ? AND idem_scope = ? AND status = ? AND idem_key IN (?)",
			qymodel.KindViolationFee, idemScopeViolationRefund,
			qymodel.StatusSuccess, staleRecords).
		Order("id asc").Limit(gcBatchSize).Find(&orders).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/violation: 扫描错位的退款单失败: " + err.Error())
		return
	}
	for i := range orders {
		if ctx.Err() != nil {
			return
		}
		// IdemKey 就是 rec_no,见 refundFee。
		if err := markRecordRefunded(gdb.WithContext(ctx),
			orders[i].IdemKey, orders[i].AmountQuota); err != nil {
			db.MarkFailure(err)
			common.SysError("qianye/violation: 收敛退款单 " + orders[i].OrderNo +
				" 的记录状态失败: " + err.Error())
			return
		}
		common.SysLog(fmt.Sprintf(
			"qianye/violation: 退款单 %s 已生效但记录仍写着已扣费,已补写为已退款(记录 %s,额度 %d)",
			orders[i].OrderNo, orders[i].IdemKey, orders[i].AmountQuota))
	}
}

// maxBanAttempts 是封禁执行的重试上限。超过后转 failed 并等人工处理:
// 无限重试只会在主库真的坏掉时把日志刷爆。
const maxBanAttempts = 5

// runBanCompensate 收敛认领成功但主库六步未完成的封禁。
//
// 存在理由:认领(扩展库)与执行(主库)跨两个数据库,没有分布式事务。
// 认领后进程崩溃会留下 pending 行,不补偿就等于"计数到了阈值但人没被封"。
//
// 刻意不扫 BanDeferred:那是速率闸主动做出的"先让人看一眼"的决定,
// 自动补做等于把速率闸彻底架空(限 10 人/小时,补偿任务 5 分钟就能把 100 行全封了)。
// deferred 行有且只有两个出口,都不在这里:
//   - 管理员在封禁列表里对该行调 /violation/bans/:userId/unban,判定"不予封禁"
//     (unbanUser 接受 deferred,见那里的说明);
//   - 速率窗口滚过之后,该用户的下一次违规在 resolveBanClaim 里把它提升成 pending。
func runBanCompensate(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 留 60 秒宽限:刚认领还在执行中的行不该被补偿任务抢着重做。
	cutoff := common.GetTimestamp() - 60

	var rows []Ban
	if err := gdb.WithContext(ctx).
		Where("status IN ? AND created_at < ? AND attempts < ?",
			[]string{BanPending, BanFailed}, cutoff, maxBanAttempts).
		Order("id asc").Limit(gcBatchSize).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return
	}

	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		b := rows[i]
		// 先占坑再执行:attempts 的条件更新是这里唯一的互斥手段,
		// 保证同一行不会被两个节点(或本节点的两轮)同时重试。
		res := gdb.WithContext(ctx).Model(&Ban{}).
			Where("id = ? AND attempts = ?", b.Id, b.Attempts).
			Update("attempts", b.Attempts+1)
		if res.Error != nil || res.RowsAffected == 0 {
			continue
		}
		err := disableUserForViolation(ctx, b.UserId, &b)
		switch {
		case err == nil:
			markBan(gdb, b.Id, BanBanned, "")
		case isSkipped(err):
			markBan(gdb, b.Id, BanSkipped, "")
		default:
			markBan(gdb, b.Id, BanFailed, err.Error())
			common.SysError(fmt.Sprintf(
				"qianye/violation: 补偿封禁失败(ban=%d user=%d attempts=%d): %v",
				b.Id, b.UserId, b.Attempts+1, err))
		}
	}
}

func isSkipped(err error) bool { return errors.Is(err, errBanSkipped) }
