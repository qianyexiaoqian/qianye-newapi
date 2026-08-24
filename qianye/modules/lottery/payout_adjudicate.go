package lottery

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// payout_adjudicate.go —— 「凭人工核对结论落账」的最终裁决口。
//
// # 它补的是哪一个死角
//
// 一笔出款同时满足下面三条时,在此之前**没有任何人、任何任务能处置它**:
//
//	业务侧 status = held        —— 出款 worker 只扫 planned/paying/failed,不扫 held;
//	本代次资金单 status = failed —— 补偿任务只扫 pending/in_doubt,复判只扫 uncertain;
//	主库探针不是 MainNotApplied  —— 说"已生效"或"判不出来",于是 RetryPayout 的
//	                              换代次分支(唯一能让钱真的出手的那一支)被 403 掉。
//
// 三条路全堵死的后果不是"少了个按钮":这一场活动因此永远过不了删除闸门②
// (checkActivityDeletable 要求全部出款落到 paid/granted),那笔钱永久挂在
// 「冻结中」,而用户既没收到钱、也拿不到任何结论。
//
// # 为什么出口是「人工核对结论」而不是「再探一次针」
//
// 探针已经答过了,它给的正是那个把三条路堵死的答案。再探一百次也只会得到
// 同一个"已生效/判不出来"。真正缺的证据在探针够不着的地方 —— 主库 logs 里
// 那一行、users.quota 的真实值、DBA 的 binlog。所以这个端点收的是**人看过
// 主库之后的结论**,系统据此落账,而不是再做一次自动判定。
//
// # 两种结论各自落成什么
//
//	credited(确实已发放)   → 资金单 failed→success 走与补偿任务逐字相同的收尾链路
//	                        (提交后收尾补账本行 + Resolver 把出款 held→paid),
//	                        钱不再动一分,只是把账做平。
//	not_credited(确实没发放)→ 换代次(epoch+1 = 换幂等键)并推回 planned,让出款
//	                        worker 重新出手。资金单本身保持 failed —— 那是事实,
//	                        账本 append-only,不改写历史行。
//
// # 与补偿任务的互斥
//
// 互斥是**结构性**的,不是靠一把锁:本端点只接受资金单处于 failed 的那一笔,
// 而 failed 是补偿链路谁都不再碰的终态(Compensate 扫 UnsettledStatuses、
// reprobeUncertain 扫 Uncertain、resolveNotApplied/finalizeFailed 都是写入 failed
// 之后就收手)。单据处在 pending / in_doubt / uncertain 时一律拒绝并明说
// "补偿任务还在处理" —— 那三种状态下人和任务会对同一笔单双写。
// 出款行这一侧同理:held 不在 DrivePayouts 的扫描集合里,两个动作不可能并发。
// 最后仍然用两次 CAS 兜底(资金单 failed→success、出款行 held→planned),
// 抢输的一方拿到的是"状态已变化,请刷新",而不是第二次落账。

// 人工核对的两种结论。取值直接进审计,因此用稳定的英文标识而不是中文。
const (
	// VerdictCredited = 人核对过主库,钱**确实已经**到用户账上。
	VerdictCredited = "credited"
	// VerdictNotCredited = 人核对过主库,钱**确实没有**到账,可以安全地重发。
	VerdictNotCredited = "not_credited"
)

// maxAdjudicateReasonRunes 与整场取消的理由上限同一个数(api_admin.go 的 200)。
//
// 两处都是"必须写清楚为什么"的人工动作,给不同的上限只会让运营记两套规矩。
const maxAdjudicateReasonRunes = 200

var (
	errAdjudicateVerdict = newBizError(http.StatusBadRequest, "qy_lot_adjudicate_verdict",
		"核对结论只能是 credited(确实已发放)或 not_credited(确实没发放)")
	errAdjudicateReason = newBizError(http.StatusBadRequest, "qy_lot_adjudicate_reason",
		"必须写清楚核对依据(查了主库的什么、看到了什么),它是这笔钱事后唯一的解释")
	// errAdjudicateNotHeld 只认 held:planned/paying/failed 都还在自动链路里,
	// 人插手等于与 worker 双写;paid/granted 已经落定,没有可裁决的东西。
	errAdjudicateNotHeld = newBizError(http.StatusConflict, "qy_lot_adjudicate_not_held",
		"只有「冻结中」的出款才需要人工落账:其余状态要么还在自动重试,要么已经落定")
	// errAdjudicateNoOrder 与 errAdjudicateOrderSettled 刻意分成两条:两句话
	// 要求运营做的下一步是同一个按钮(「重试」),但原因完全不同,而写成一条
	// 之后没有任何人能从提示里分清"从没发出去过"和"其实已经到账了"。
	errAdjudicateNoOrder = newBizError(http.StatusConflict, "qy_lot_adjudicate_no_order",
		"本代次没有资金单,这一笔从未真的出手过,不存在要核对的主库流水;直接用「重试」")
	errAdjudicateOrderSettled = newBizError(http.StatusConflict, "qy_lot_adjudicate_order_settled",
		"本代次的资金单已经成功,钱是到账的;用「重试」收尾即可,不必人工裁决")
	// errAdjudicateOrderOpen 就是与补偿任务的互斥判据。
	errAdjudicateOrderOpen = newBizError(http.StatusConflict, "qy_lot_adjudicate_order_open",
		"本代次的资金单还在补偿任务手里(未落定),此刻人工落账会与它同时改同一笔单;"+
			"等它收敛成成功或失败之后再来")
	// 操作人判据的三个方向,**必须分成三条**而不是两条。
	//
	// 这条路由挂着 RootActionGate,所以能走到这里的操作人恒为 role=100;
	// 而 guard.ManageableTarget 对 root 恒返回 true(root 管得了任何角色)。
	// 也就是说 ErrTargetNotLower 那一支在本端点上**永远走不到** ——
	// 曾经把它和 ErrTargetMissing 合并成一句"不能给同级或更高权限账号名下的
	// 出款落账",实际发生的只可能是后者:目标账号在主库里查不到
	// (硬删用户不级联清理 qy_ext 的出款行,所以这一档是真的会出现的)。
	// 实测:对一笔属于不存在用户的出款调用 → 403 + 那句关于权限等级的话,
	// 而同一次请求写下的审计里内部原因是「目标用户不存在」。
	// 运营会照着界面上那句话去找一个并不存在的"更高权限账号"。
	errAdjudicateSelfDealing = newBizError(http.StatusForbidden, "qy_self_dealing",
		"不能给自己名下的出款落账,请由另一位超级管理员处理")
	errAdjudicateTargetHigher = newBizError(http.StatusForbidden, "qy_target_not_manageable",
		"不能给同级或更高权限账号名下的出款落账")
	errAdjudicateTargetMissing = newBizError(http.StatusNotFound, "qy_target_missing",
		"这笔出款的收款账号在主库里查不到(多半已被硬删)。"+
			"硬删用户不会级联清理扩展库里的出款行,所以这一笔仍然挂着 —— "+
			"请先确认这个账号的去向,再决定这笔钱怎么处置")
)

// adjudicateRequest 是人工落账的报文。
type adjudicateRequest struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// AdjudicatePayout 把一次人工核对结论落成账。
//
// 返回落账之后那一行出款的真实状态,让调用方写审计时不必假定终态
// (与 handleRetryPayout 回读终态是同一条纪律:写死一个从未发生过的状态迁移,
// 排障的人会照着它把方向找反)。
func AdjudicatePayout(ctx context.Context, payoutNo, verdict, reason string) (*Payout, error) {
	if verdict != VerdictCredited && verdict != VerdictNotCredited {
		return nil, errAdjudicateVerdict
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	// 句柄一次性绑上调用方的预算:逐条 WithContext 漏一条,
	// 就等于在这条链路上开了一个没有上界的口子。
	gdb = gdb.WithContext(ctx)

	var p Payout
	if err := gdb.Where("payout_no = ?", payoutNo).Take(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errPayoutNotFound
		}
		db.MarkFailure(err)
		return nil, wrapInternal("读取出款", err)
	}
	if p.Status != PayoutHeld {
		return nil, errAdjudicateNotHeld
	}

	var order qymodel.FundOrder
	err := gdb.
		Where("idem_scope = ? AND idem_key = ?", idemScopePayout, payoutIdemKey(&p)).
		Take(&order).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, errAdjudicateNoOrder
	case err != nil:
		db.MarkFailure(err)
		return nil, wrapInternal("读取资金单", err)
	case order.Status == qymodel.StatusSuccess:
		return nil, errAdjudicateOrderSettled
	case order.Status != qymodel.StatusFailed:
		return nil, errAdjudicateOrderOpen
	}

	if verdict == VerdictCredited {
		settled, err := twophase.AdjudicateFailedAsApplied(ctx, &order, "人工核对确认主库已发放: "+reason)
		if err != nil {
			return nil, wrapInternal("按已发放落账", err)
		}
		if !settled {
			return nil, errStatusConflict
		}
		after, err := reloadPayoutRow(gdb, payoutNo)
		if err != nil {
			return nil, err
		}
		// 回读确认业务侧真的落定了,与 settleGuard 同一条纪律。
		//
		// 资金单已经是 success,而出款行推进 paid 靠的是**注册进 twophase 的那份
		// 补偿回调**(InstallResolvers)。回调没注册、或者它自己失败,resolveApplied
		// 都不会报错 —— 那正是本仓 violation 上真实发生过的形状:单据变了、业务侧
		// 永远停在中间态。此刻对管理员宣布"落账完成"会让他关掉页面,而那一场活动
		// 仍然删不掉。宁可回"尚未落定,请稍后复核"。
		if after.Status != PayoutPaid {
			common.SysError("qianye/lottery: 出款 " + payoutNo +
				" 的资金单已改判为成功,但业务侧未落定(补偿回调未注册或执行失败),当前状态: " + after.Status)
			return nil, errNotSettled
		}
		return after, nil
	}

	// 确实没发放:换代次重开一张单。代次一变幂等键就变,下一轮 DrivePayouts
	// 会真的对主库再加一次钱 —— 这正是本端点唯一会增发的分支,它的全部安全性
	// 来自"人核对过主库确认没到账"这一个前提,所以它只对超级管理员开放。
	res := gdb.Model(&Payout{}).
		Where("payout_no = ? AND status = ?", payoutNo, PayoutHeld).
		Updates(map[string]any{
			"status":          PayoutPlanned,
			"epoch":           gorm.Expr("epoch + 1"),
			"attempts":        0,
			"next_attempt_at": 0,
			"last_error":      audit.Truncate("人工核对确认主库未发放,已换代次重排: "+reason, 512),
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return nil, wrapInternal("按未发放重排", res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, errStatusConflict
	}
	return reloadPayoutRow(gdb, payoutNo)
}

// reloadPayoutRow 回读落账之后那一行的真实状态。
// gdb 必须是**已经绑好 ctx 的句柄**。
func reloadPayoutRow(gdb *gorm.DB, payoutNo string) (*Payout, error) {
	var after Payout
	if err := gdb.Where("payout_no = ?", payoutNo).Take(&after).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("回读出款", err)
	}
	return &after, nil
}

// handleAdjudicatePayout 是人工落账的 HTTP 落点。
//
// 路由上已经挂了 middleware.RootActionGate(RootActionLotteryPayoutAdjudicate),
// 因此这里不再判角色 —— 判据只有一处,理由见 middleware/root_action.go。
func handleAdjudicatePayout(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	payoutNo := c.Param("payout_no")
	var req adjudicateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdjudicateAudit(c, payoutNo, nil, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	verdict := strings.TrimSpace(req.Verdict)
	reason := strings.TrimSpace(req.Reason)
	if verdict != VerdictCredited && verdict != VerdictNotCredited {
		writeAdjudicateAudit(c, payoutNo, nil, qymodel.ResultFail, "核对结论非法: "+verdict, "", "")
		respondErr(c, errAdjudicateVerdict)
		return
	}
	if reason == "" || utf8.RuneCountInString(reason) > maxAdjudicateReasonRunes {
		writeAdjudicateAudit(c, payoutNo, nil, qymodel.ResultFail, "未填写核对依据", "", "")
		respondErr(c, errAdjudicateReason)
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	var p Payout
	if err := gdb.WithContext(ctx).Where("payout_no = ?", payoutNo).Take(&p).Error; err != nil {
		writeAdjudicateAudit(c, payoutNo, nil, qymodel.ResultFail, "出款记录不存在", "", "")
		respondErr(c, errPayoutNotFound)
		return
	}
	// URL 里那段 :act_no 必须真的是这笔出款所属的活动,理由与 handleRetryPayout
	// 同一条:不校验的话一次拼错 act_no 的写调用会成功,而任何按路径段做作用域
	// 限制的外部手段(反代规则、按活动分派的脚本)被静默绕过。
	if actNo := c.Param("act_no"); actNo != "" {
		var act Activity
		if err := gdb.WithContext(ctx).Where("id = ?", p.ActId).Take(&act).Error; err != nil || act.ActNo != actNo {
			writeAdjudicateAudit(c, payoutNo, &p, qymodel.ResultFail, "出款不属于该活动", "", "")
			respondErr(c, errPayoutNotFound)
			return
		}
	}

	// 操作人判据。这是动钱的写入口:not_credited 分支会让主库再加一次钱,
	// credited 分支会把一笔"平台还欠着"的钱在账上宣布为已付清 —— 两个方向都
	// 不允许自营,也不允许对同级/更高权限的账号动手。
	if err := guard.ActorMayActOnCtx(c, p.UserId); err != nil {
		writeAdjudicateAudit(c, payoutNo, &p, qymodel.ResultFail, "操作人判据拒绝: "+err.Error(), "", "")
		switch {
		case errors.Is(err, guard.ErrActorIsTarget):
			respondErr(c, errAdjudicateSelfDealing)
		case errors.Is(err, guard.ErrTargetMissing):
			respondErr(c, errAdjudicateTargetMissing)
		case errors.Is(err, guard.ErrTargetNotLower):
			respondErr(c, errAdjudicateTargetHigher)
		default:
			respondErr(c, wrapInternal("操作人判据", err))
		}
		return
	}

	after, err := AdjudicatePayout(ctx, payoutNo, verdict, reason)
	if err != nil {
		writeAdjudicateAudit(c, payoutNo, &p, qymodel.ResultFail, auditReason(err),
			snapText(payoutSnapshot(&p)), "")
		respondErr(c, err)
		return
	}

	writeAdjudicateAudit(c, payoutNo, &p, qymodel.ResultOK, verdict+": "+reason,
		snapText(payoutSnapshot(&p)),
		snapText(map[string]any{"status": after.Status, "epoch": after.Epoch, "verdict": verdict}))
	common.SysLog("qianye/lottery: 出款 " + payoutNo + " 已按人工核对结论落账(" + verdict + ")")
	respondOK(c, gin.H{"payout_no": payoutNo, "status": after.Status, "verdict": verdict})
}

// writeAdjudicateAudit 落一条人工落账审计。
//
// 与 writeAdminAudit 分开,是因为这一条必须带上**受益人与金额**:仲裁台按
// target_user_id / amount_quota 检索,而"谁的钱、多少钱"正是事后复核这条裁决
// 时要问的第一个问题。出款行还没读出来时(参数校验失败)两个字段留零值 ——
// 那时确实还不知道是谁的钱,填任何东西都是编的。
func writeAdjudicateAudit(c *gin.Context, payoutNo string, p *Payout, result, reason, before, after string) {
	e := audit.Entry{
		TraceNo:     payoutNo,
		Category:    auditCategory,
		Action:      "lottery.payout.adjudicate",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      audit.Truncate(reason, 500),
		BeforeSnap:  before,
		AfterSnap:   after,
	}
	if p != nil {
		e.TargetUserId = p.UserId
		e.AmountQuota = p.AmountQuota
	}
	audit.Write(c, e)
}
