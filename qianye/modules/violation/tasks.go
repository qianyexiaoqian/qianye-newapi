package violation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
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

// maxBanAttempts 是封禁执行的重试上限。超过后转 failed 并等人工处理:
// 无限重试只会在主库真的坏掉时把日志刷爆。
const maxBanAttempts = 5

// runBanCompensate 收敛认领成功但主库六步未完成的封禁。
//
// 存在理由:认领(扩展库)与执行(主库)跨两个数据库,没有分布式事务。
// 认领后进程崩溃会留下 pending 行,不补偿就等于"计数到了阈值但人没被封",
// 而计数已经推过阈值,正常路径再也不会触发第二次。
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
		err := disableUserForViolation(b.UserId, &b)
		switch {
		case err == nil:
			markBan(b.Id, BanBanned, "")
		case isSkipped(err):
			markBan(b.Id, BanSkipped, "")
		default:
			markBan(b.Id, BanFailed, err.Error())
			common.SysError(fmt.Sprintf(
				"qianye/violation: 补偿封禁失败(ban=%d user=%d attempts=%d): %v",
				b.Id, b.UserId, b.Attempts+1, err))
		}
	}
}

func isSkipped(err error) bool { return errors.Is(err, errBanSkipped) }
