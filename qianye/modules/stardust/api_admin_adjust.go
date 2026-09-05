package stardust

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_adjust.go —— 管理员手工增减某个人的星屑(design §4.5)。
//
// # 为什么它落成一条 manual 流水,而不是把余额列改掉
//
// qy_sd_balance 上的每一个数字都是派生量,由流水解释(doc.go 的 I0 / I1)。直接
// UPDATE available 会当场打破两条恒等式,而没有任何一行流水能解释差额 —— ledger-check
// 会把这个人报成 drifted,但报出来之后谁也说不清是谁改的。所以手调走账本本来就有的
// 那条路:Credit / Debit(KindManual),幂等键 (sd_manual, manual:<操作人>:<client_request_id>)。
//
// # 幂等先于上下界
//
// 事务内锁住余额行之后**先查**幂等键是否命中,命中才比 user_id 与 delta:一致返回原单
// (replayed=true),不一致 409。上下界(|delta| ≤ max_manual_adjust、扣减不得到负)只在
// 未命中时判。顺序不能反:重放时账本上早就有那一行了,拿此刻的余额重新校验会因为
// "上一次已经扣过"而失败,运营会以为自己从来没成功过,于是换个数再发一次 ——
// 那才是真的扣两遍。
//
// # 三道闸
//
//	RootActionGate(RootActionStardustAdjust)  只许超管:手调是凭空造出可在商城变现的东西
//	CriticalRateLimit                          排在闸门**之后**,被拒的越权尝试不消耗限流桶
//	guard.ActorMayActOnCtx                     自营 / 越级 fail-closed,开事务之前判
//
// 成功与失败各写一条审计(category stardust),金额取**实际落账**的那个数:
// 幂等重放与被拒的那次都记 0,审计表是这套账本事后仲裁的唯一凭据。

const (
	// adjustIdemScope 是手调流水的幂等 scope;键形状 manual:<操作人 id>:<client_request_id>,
	// 把操作人算进去是因为两个管理员各自前端生成的 client_request_id 没有理由互相撞。
	adjustIdemScope   = "sd_manual"
	adjustAuditAction = "stardust.adjust"
	// minAdjustReasonRunes:事由至少 4 个字符,"补发" 两个字说不清为什么。
	minAdjustReasonRunes = 4
	// maxAdjustClientKeyLen 与 lottery 的 maxClientRequestID 同值;超长的键会被 sha256 折叠,
	// 这里挡的只是明显不像一个请求标识的输入。
	maxAdjustClientKeyLen = 64
)

var (
	// 操作人判据的三个失败方向各自一个 code:前端据此提示"换一位管理员操作"(403)
	// 或"检查 user_id"(400),而不是让他改数字重试。前两个 code 与 invite 同名,
	// 前端已有这两条的文案。
	errAdjustSelfDealing = newBizError(http.StatusForbidden, "qy_self_dealing",
		"不能调整自己的账户,请由另一位管理员操作")
	errAdjustTargetPeer = newBizError(http.StatusForbidden, "qy_target_not_manageable",
		"无权调整同级或更高权限账号的账户")
	errAdjustTargetMissing = newBizError(http.StatusBadRequest, "qy_sd_user_not_found",
		"目标用户查不到(user_id 可能填错,或账号已被硬删除)")
)

func init() { adminRouteInstallers = append(adminRouteInstallers, installAdjustRoutes) }

// installAdjustRoutes 单独成函数而不是像其它文件那样写成闭包:
// qianye/root_action_guard_test.go 按 (file, fn, recv) 解析路由注册,闸门必须挂在
// 一个能被点名的函数里,它同时断言 RootActionGate 排在 CriticalRateLimit 之前。
func installAdjustRoutes(g *gin.RouterGroup) {
	g.POST("/stardust/adjust",
		middleware.RootActionGate(middleware.RootActionStardustAdjust),
		middleware.CriticalRateLimit(),
		handleAdminAdjust)
}

// manualAdjust 是一次手调的全部参数。
type manualAdjust struct {
	UserId     int
	Delta      int64
	Reason     string
	OperatorId int
	// ClientKey 已经过 qymodel.NormalizeIdemClientKey。
	ClientKey string
}

// adjustOutcome 是一次手调的结局。
type adjustOutcome struct {
	LedgerNo     string
	BalanceAfter int64
	// Replayed 为真表示同一个幂等键的重放,账本没有再动。
	Replayed bool
	Before   Balance
}

// adjustAuditRecord 是一条手调审计的输入,成功与失败共用。
type adjustAuditRecord struct {
	TraceNo      string
	TargetUserId int
	Amount       int64
	Result       string
	Reason       string
	Before       *Balance
	After        *Balance
}

// handleAdminAdjust 是 POST /stardust/adjust。
func handleAdminAdjust(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	var req struct {
		UserId int `json:"user_id"`
		// 指针:0 必须被当成"填了个没有意义的值"而不是"根本没填",两者提示不同。
		Delta           *int64 `json:"delta"`
		Reason          string `json:"reason"`
		ClientRequestId string `json:"client_request_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdjustAudit(c, adjustAuditRecord{Result: qymodel.ResultFail, Reason: "请求体解析失败"})
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	clientKey, keyOK := qymodel.NormalizeIdemClientKey(req.ClientRequestId)
	var bad error
	switch {
	case req.UserId <= 0:
		bad = errBadRequest("必须指定 user_id")
	case req.Delta == nil:
		bad = errBadRequest("必须给出 delta")
	case *req.Delta == 0:
		bad = errBadRequest("delta 不能为 0")
	case *req.Delta > int64(common.MaxQuota) || *req.Delta < -int64(common.MaxQuota):
		bad = errBadRequest("delta 必须落在正负系统上界之内")
	case utf8.RuneCountInString(reason) < minAdjustReasonRunes:
		bad = errBadRequest("事由至少 " + strconv.Itoa(minAdjustReasonRunes) + " 个字符")
	case !keyOK || len(clientKey) > maxAdjustClientKeyLen:
		// 幂等键必填:加钱 / 减钱的接口没有幂等键时,一次网络超时重试就是第二笔。
		bad = errBadRequest("client_request_id 必填,且只接受不超过 " +
			strconv.Itoa(maxAdjustClientKeyLen) + " 个 [0-9a-zA-Z_-] 字符")
	}
	if bad != nil {
		writeAdjustAudit(c, adjustAuditRecord{TargetUserId: req.UserId, Result: qymodel.ResultFail,
			Reason: "参数不合法: " + bad.Error()})
		respondErr(c, bad)
		return
	}
	delta := *req.Delta

	// 自营与越级在**开事务之前**判掉:它们与余额、幂等键都无关,越早拒绝,余额行锁被
	// 握住的时间越短。ActorMayActOnCtx 同时确认目标账号真的存在 —— 不问的话,
	// user_id 打错一位就会凭空建出一行永远没人认领的余额。
	var denied error
	switch err := guard.ActorMayActOnCtx(c, req.UserId); {
	case err == nil:
	case errors.Is(err, guard.ErrActorIsTarget):
		denied = errAdjustSelfDealing
	case errors.Is(err, guard.ErrTargetNotLower):
		denied = errAdjustTargetPeer
	case errors.Is(err, guard.ErrTargetMissing):
		denied = errAdjustTargetMissing
	default:
		denied = wrapInternal("核对目标账号", err)
	}
	if denied != nil {
		writeAdjustAudit(c, adjustAuditRecord{TargetUserId: req.UserId, Result: qymodel.ResultFail,
			Reason: "操作人判据拒绝: " + denied.Error() + " | 事由: " + reason})
		respondErr(c, denied)
		return
	}

	ctx := c.Request.Context()
	out, err := applyManualAdjust(ctx, manualAdjust{
		UserId: req.UserId, Delta: delta, Reason: reason,
		OperatorId: c.GetInt("id"), ClientKey: clientKey,
	})
	if err != nil {
		// 审计落在事务之外,否则它会跟着一起回滚 —— 而"有人在这一刻试图给某个人
		// 加 / 减星屑、失败了"正是最需要留痕的事实。金额记 0:账本上什么都没发生。
		snap := currentBalance(ctx, req.UserId)
		writeAdjustAudit(c, adjustAuditRecord{TargetUserId: req.UserId, Result: qymodel.ResultFail,
			Reason: "手调失败(事务已回滚): " + err.Error() + " | 事由: " + reason, Before: snap, After: snap})
		respondErr(c, err)
		return
	}

	applied := delta
	auditReason := "手调 " + strconv.FormatInt(delta, 10) + " " + UnitName() + ": " + reason
	if out.Replayed {
		applied = 0
		auditReason = "幂等重放,账本未再变动: " + reason
	}
	writeAdjustAudit(c, adjustAuditRecord{
		TraceNo: out.LedgerNo, TargetUserId: req.UserId, Amount: applied,
		Result: qymodel.ResultOK, Reason: auditReason,
		Before: &out.Before, After: currentBalance(ctx, req.UserId),
	})
	respondOK(c, gin.H{
		"ledger_no":     out.LedgerNo,
		"user_id":       req.UserId,
		"delta":         delta,
		"balance_after": out.BalanceAfter,
		"replayed":      out.Replayed,
	})
}

// applyManualAdjust 在一个扩展库事务里完成:锁余额行 → 幂等判定 → 上下界 → 记账。
//
// 四件事必须在同一把锁下:锁外做校验时,结算任务或一笔商城下单可以在校验与写入之间
// 把余额搬走,那时这条扣减要么失败要么把账扣穿。
func applyManualAdjust(ctx context.Context, in manualAdjust) (*adjustOutcome, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	// 上限直接取 YAML:它决定一个 HTTP 接口一次能凭空造出多少星屑,刻意不进 qy_settings。
	// 配成 0 就是"关掉手调"—— fail-closed,而不是静默放开到系统上界。
	limit := config.Get().Stardust.MaxManualAdjust
	rawKey := "manual:" + strconv.Itoa(in.OperatorId) + ":" + in.ClientKey
	idemKey := normalizeIdemKey(rawKey)

	out := &adjustOutcome{}
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bal, err := LockBalance(tx, in.UserId)
		if err != nil {
			return err
		}
		out.Before = *bal

		var existing []Ledger
		if err := tx.Where("idem_scope = ? AND idem_key = ?", adjustIdemScope, idemKey).
			Limit(1).Find(&existing).Error; err != nil {
			db.MarkFailure(err)
			return wrapInternal("查询手调幂等键", err)
		}
		if len(existing) == 1 {
			prior := existing[0]
			if prior.UserId != in.UserId || prior.Amount != in.Delta {
				return errIdemConflict
			}
			out.LedgerNo, out.BalanceAfter, out.Replayed = prior.LedgerNo, prior.BalanceAfter, true
			return nil
		}

		if in.Delta > limit || in.Delta < -limit {
			return errAdjustTooLarge(limit)
		}
		p := Posting{
			UserId: in.UserId, Kind: KindManual, Amount: in.Delta,
			IdemScope: adjustIdemScope, IdemKey: rawKey,
			RefType: "manual", RefNo: "OP" + strconv.Itoa(in.OperatorId),
			Remark: "手工调整: " + in.Reason, OperatorId: in.OperatorId,
		}
		var res Result
		if in.Delta > 0 {
			res, err = Credit(tx, p)
		} else {
			p.Amount = -in.Delta
			res, err = Debit(tx, p)
		}
		if err != nil {
			return err
		}
		if !res.Inserted {
			// 上面刚查过不存在,这里却命中了 = 同一个幂等键的并发重放。
			// 回 409 而不是假装成功:账本上执行的是另一次请求的参数。
			return errIdemConflict
		}
		out.LedgerNo, out.BalanceAfter = res.LedgerNo, res.BalanceAfter
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// currentBalance 读一份余额行给审计快照用;读不到(行不存在或库出错)返回 nil,
// 快照渲染成空串 —— 审计不能因为一次读失败而丢掉整条记录。
func currentBalance(ctx context.Context, userId int) *Balance {
	gdb := db.Get()
	if gdb == nil {
		return nil
	}
	var rows []Balance
	if err := gdb.WithContext(ctx).Where("user_id = ?", userId).Limit(1).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

// balanceSnap 把余额行渲染成审计快照;nil 渲染成空串(该行不存在)。
func balanceSnap(b *Balance) string {
	if b == nil {
		return ""
	}
	raw, _ := common.Marshal(newBalanceView(*b))
	return string(raw)
}

// writeAdjustAudit 落一条手调审计,成功与失败共用。
//
// AmountQuota 这一列在本模块记的是**星屑整数**而不是额度:审计表是全扩展共用的,
// 列名沿用,单位由 category=stardust 说明。TraceNo 挂流水号,审计与账本行互相指得回去。
func writeAdjustAudit(c *gin.Context, r adjustAuditRecord) {
	audit.Write(c, audit.Entry{
		TraceNo:      r.TraceNo,
		Category:     qymodel.AuditCategoryStardust,
		Action:       adjustAuditAction,
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: r.TargetUserId,
		AmountQuota:  r.Amount,
		Result:       r.Result,
		Reason:       r.Reason,
		BeforeSnap:   balanceSnap(r.Before),
		AfterSnap:    balanceSnap(r.After),
	})
}
