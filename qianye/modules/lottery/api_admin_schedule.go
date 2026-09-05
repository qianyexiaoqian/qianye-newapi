package lottery

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_schedule.go —— 已发布的星屑转盘改排期(开始 / 结束)。
//
// # 为什么转盘可以在发布之后改时刻,而批次玩法不行
//
// 批次玩法(rank / prob / ball / guess)的结果由封盘那一刻冻结的名单决定:谁能挑
// 封盘时刻,谁就能在看到某个人报名之后立刻关门 —— 选时攻击。所以它们的四个时刻
// 在 publish 时进承诺哈希,之后只有租约任务按时间触发(api_admin.go 顶部)。
//
// 转盘没有"封盘摇号"这一步:票面 = HMAC(seed, act_no ‖ seq ‖ client_seed),每一转
// 当场开出、当场派奖。排期改成什么都不会改变任何一张已经开出的票,也不会改变
// 下一张票的推导;它只决定"什么时候收转",像抽卡卡池的上下架时间。项目方
// 2026-09-04 原话:「转盘应像游戏抽卡卡池一样:开始时间、结束时间就可以了。」
// 于是转盘的承诺原像**不含**三个时刻(CommitHashV2),排期成了与封面、单次注数
// 上限同一类的"发布后仍可写"字段 —— 每一次改动写事件行 + 审计,before/after
// 各带三个时刻。
//
// # 只有三个动作
//
//   - 改排期:本接口,body {open_at, close_at};draw_at 不由运营填,重新派生为
//     close_at + reveal_delay_seconds(与创建时同一条派生,api_admin.go 的 buildActivity)。
//   - 立即开始:同一接口,open_at = now。
//   - 提前结束:沿用既有「取消」(cancelWheel 的提前封盘)。**不另造端点** ——
//     它要冻结名单、写封盘事件,与到点封盘逐字同一段事务体,多一条路就多一处漂移。
//
// 只对 status=published 开放:封盘之后排期已经没有意义(名单已冻结、只等揭示),
// 草稿走编辑草稿那条整体替换的路。

// scheduleInput 是「改排期」的请求体。两个都是 unix 秒。
type scheduleInput struct {
	OpenAt  int64 `json:"open_at"`
	CloseAt int64 `json:"close_at"`
}

// ActionScheduleChanged 是改排期的事件行 action。放在这里而不是 model.go 的常量表:
// 它是转盘独有的动作,批次玩法永远不会写出这一行。
const ActionScheduleChanged = "schedule_changed"

var (
	// errScheduleNotWheel:批次玩法的时刻进承诺原像,发布后不可改 —— 这不是状态
	// 冲突,刷新一万次都一样。
	errScheduleNotWheel = newBizError(http.StatusConflict, "qy_lot_schedule_not_wheel",
		"只有转盘能在发布后改排期:其它玩法的四个时刻在发布那一刻进了承诺哈希")
	// errWheelScheduleLocked:转盘不在 published(草稿、已封盘、结算中、已结束)。
	// 封盘之后名单已冻结、只等揭示,排期对它已经没有意义。
	errWheelScheduleLocked = newBizError(http.StatusConflict, "qy_lot_wheel_schedule_locked",
		"只有进行中的转盘能改排期:草稿请直接编辑;已结束(含提前结束)的转盘不能再改")
)

// scheduleSnapshot 是审计与事件行里的排期快照:只有三个时刻,不整行序列化 Activity。
func scheduleSnapshot(openAt, closeAt, drawAt int64) map[string]any {
	return map[string]any{"open_at": openAt, "close_at": closeAt, "draw_at": drawAt}
}

// handleSetWheelSchedule 改一场已发布转盘的开始 / 结束时刻:
// PUT /admin/lottery/activities/:act_no/schedule。
//
// 校验与创建时的 validateSchedule 同一口径里转盘用得上的那几条:open_at > 0、
// close_at > max(open_at, now)、地平线上界。没有 "close_at - open_at > grace"
// 那条 —— 转盘不用 entry_close_grace_seconds。open_at 允许早于 now(一场已经开放的
// 转盘只改结束时间时,运营原样回传当前的 open_at;「立即开始」发的就是 now)。
//
// 状态闸门在事务内的 CAS 上(WHERE status='published'),handler 外那次预检只为了
// 给出更准确的错误码;CAS 落空一律回 errWheelScheduleLocked —— 读到 published、
// 落锁时已被到点封盘 / 提前结束,对运营来说就是"它已经结束了"。
func handleSetWheelSchedule(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	const action = "lottery.activity.schedule"
	actNo := c.Param("act_no")
	var in scheduleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	now := common.GetTimestamp()
	cfg := config.Get().Lottery
	var bad error
	switch {
	case in.OpenAt <= 0 || in.CloseAt <= 0:
		bad = errBadRequest("开始与结束两个时刻都必须填写")
	case in.CloseAt <= in.OpenAt:
		bad = errBadRequest("结束时间必须晚于开始时间")
	case in.CloseAt <= now:
		bad = errBadRequest("结束时间必须晚于当前时间;要立刻收转请用「提前结束」")
	case in.OpenAt > now+maxScheduleHorizonSeconds || in.CloseAt > now+maxScheduleHorizonSeconds:
		bad = errBadRequest(fmt.Sprintf("开始与结束时间都不得晚于当前时间之后 %d 天", maxScheduleHorizonSeconds/86400))
	}
	if bad != nil {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(bad), "",
			snapText(scheduleSnapshot(in.OpenAt, in.CloseAt, 0)))
		respondErr(c, bad)
		return
	}
	// draw_at 与创建时同一条派生:结束后过一个强制间隔自动揭示种子。地平线已经
	// 夹住 close_at,这条加法溢不出去(validateSchedule 里那段溢出旁路的说明)。
	drawAt := in.CloseAt + int64(cfg.RevealDelaySeconds)
	after := snapText(scheduleSnapshot(in.OpenAt, in.CloseAt, drawAt))

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, actNo)
	if err != nil {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(err), "", after)
		respondErr(c, err)
		return
	}
	before := snapText(scheduleSnapshot(act.OpenAt, act.CloseAt, act.DrawAt))
	if act.DrawMode != DrawModeWheel {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(errScheduleNotWheel), before, after)
		respondErr(c, errScheduleNotWheel)
		return
	}
	if act.Status != StatusPublished {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(errWheelScheduleLocked), before, after)
		respondErr(c, errWheelScheduleLocked)
		return
	}

	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// CAS 只认 published:并发的到点封盘 / 提前结束 / 库存耗尽封盘任何一个先落地,
		// 这次改动就整笔不生效 —— 封盘后的排期不能再动,名单已经冻结。
		res := tx.Model(&Activity{}).
			Where("id = ? AND status = ?", act.Id, StatusPublished).
			Updates(map[string]any{
				"open_at":    in.OpenAt,
				"close_at":   in.CloseAt,
				"draw_at":    drawAt,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errWheelScheduleLocked
		}
		return writeActivityEvent(tx, act.Id, StatusPublished, StatusPublished, ActionScheduleChanged,
			qymodel.ActorAdmin, c.GetInt("id"), map[string]any{
				"before": scheduleSnapshot(act.OpenAt, act.CloseAt, act.DrawAt),
				"after":  scheduleSnapshot(in.OpenAt, in.CloseAt, drawAt),
			})
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("更新转盘排期", err)
		}
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(err), before, after)
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, action, actNo, qymodel.ResultOK, "", before, after)
	respondOK(c, gin.H{
		"act_no": actNo, "status": StatusPublished,
		"open_at": in.OpenAt, "close_at": in.CloseAt, "draw_at": drawAt,
	})
}
