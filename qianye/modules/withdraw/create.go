package withdraw

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// idemScope 与 qy_withdrawals.(idem_scope, idem_key) 的双列唯一索引配套(裁定 C11)。
const idemScope = "withdraw"

// create 提交一笔提现申请。
//
// 关键设计:申请阶段【完全不碰主库】,全部副作用都在一个扩展库事务里完成。
// 佣金在这一刻就离开 available,之后无论兑现阶段发生什么,用户都不可能拿同一笔
// 佣金再发一单。最坏情况只是佣金卡在 frozen 需要人工处理,永远不会变成
// "佣金可无限重复领取"。
func create(c *gin.Context, userId int, req createRequest) (*orderView, error) {
	cfg := config.Get().Withdraw
	acc, err := acceptCreate(req, cfg)
	if err != nil {
		return nil, err
	}

	user, err := model.GetUserById(userId, false)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errUserUnavailable
		}
		return nil, err
	}
	if user.Status != common.UserStatusEnabled {
		return nil, errUserUnavailable
	}

	// 锁外余额预检只为尽早给出明确错误。真正的准入判定是 FreezeForWithdraw
	// 内部的 `WHERE available_quota >= ?` CAS —— 两次读之间余额可能被并发改动。
	available, err := commission.Withdrawable(userId)
	if err != nil {
		return nil, err
	}
	if available < acc.Quota {
		return nil, errInsufficient
	}

	w, payee, err := buildWithdrawal(c, user, acc, cfg)
	if err != nil {
		return nil, err
	}

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		if err := checkDailyCount(tx, userId, cfg.DailyMaxCount); err != nil {
			return err
		}
		// 先落单再冻结:单据的唯一索引冲突要在动佣金之前就把事务打断,
		// 否则一次重复提交会白白走一遍冻结再回滚。
		if err := tx.Create(w).Error; err != nil {
			return err
		}
		if payee != nil {
			payee.WithdrawalId = w.Id
			if err := tx.Create(payee).Error; err != nil {
				return err
			}
		}
		if err := commission.FreezeForWithdraw(tx, userId, w.Quota, w.WithdrawNo); err != nil {
			return err
		}
		return writeEvent(tx, w, transition{
			To:        StatusPending,
			Action:    ActionSubmit,
			ActorType: qymodel.ActorUser,
			ActorId:   userId,
			ActorName: user.Username,
			IP:        w.ClientIp,
			Detail:    submitDetail(w),
		})
	})
	if err != nil {
		if isDuplicateKey(err) {
			// 幂等命中:同一个 client_request_id 重复提交,返回原单而不是报错。
			// 这是双击、多标签、客户端超时重试的正常结果,不该让用户看到失败。
			return loadByIdemKey(userId, acc.IdemKey)
		}
		db.MarkFailure(err)
		return nil, err
	}

	audit.Write(c, audit.Entry{
		TraceNo:      w.WithdrawNo,
		Category:     qymodel.AuditCategoryWithdraw,
		Action:       "withdraw.submit",
		ActorType:    qymodel.ActorUser,
		ActorUserId:  userId,
		ActorName:    user.Username,
		TargetUserId: userId,
		AmountQuota:  w.Quota,
		AmountFiat:   w.NetAmount,
		Currency:     w.Currency,
		FrozenRate:   w.FrozenFxRate,
		Result:       qymodel.ResultOK,
	})
	return toUserView(w, nil), nil
}

// buildWithdrawal 组装待落库的提现单与收款信息快照。
func buildWithdrawal(c *gin.Context, user *model.User, acc acceptedRequest, cfg config.Withdraw) (
	*Withdrawal, *Payee, error) {

	rates, err := freezeRates()
	if err != nil {
		return nil, nil, err
	}

	kind := qymodel.KindWithdrawQuota
	if acc.Method == config.WithdrawMethodFiat {
		kind = qymodel.KindWithdrawFiat
	}
	now := common.GetTimestamp()
	w := &Withdrawal{
		// 单号用 crypto/rand 派生(裁定 C9),禁止 common.GetRandomString ——
		// math/rand 可预测,资金单号可预测意味着可以被枚举和伪造。
		WithdrawNo:         twophase.NewOrderNo(kind),
		IdemScope:          idemScope,
		IdemKey:            strconv.Itoa(user.Id) + ":" + acc.IdemKey,
		UserId:             user.Id,
		Username:           truncate(user.Username, 64),
		Method:             acc.Method,
		Status:             StatusPending,
		Quota:              acc.Quota,
		FrozenQuotaPerUnit: rates.QuotaPerUnit,
		FrozenFxRate:       rates.FxRate,
		Remark:             acc.Remark,
		ClientIp:           truncate(c.ClientIP(), 64),
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if acc.Method != config.WithdrawMethodFiat {
		return w, nil, nil
	}

	amounts, err := computeFiat(acc.Quota, rates, cfg.FiatFeeBps)
	if err != nil {
		return nil, nil, err
	}
	minAmount, err := minFiatAmount()
	if err != nil {
		return nil, nil, err
	}
	if amounts.Gross.LessThan(minAmount) {
		return nil, nil, errFiatBelowMin
	}
	w.Currency = cfg.FiatCurrency
	w.GrossAmount, w.FeeAmount, w.NetAmount = amounts.Gross, amounts.Fee, amounts.Net
	w.FeeBps = cfg.FiatFeeBps

	channel, data, err := resolvePayee(user.Id, acc)
	if err != nil {
		return nil, nil, err
	}
	payee, err := buildPayeeSnapshot(w.WithdrawNo, user.Id, channel, data)
	if err != nil {
		return nil, nil, err
	}
	w.PayeeChannel = channel
	w.PayeeMasked = payee.Masked
	w.PayeeDigest = payee.Digest
	w.RiskFlags = digestRiskFlag(user.Id, payee.Digest)
	return w, payee, nil
}

// checkDailyCount 限制单日提现次数。
//
// 已撤销的单不计入:用户填错收款信息撤销重填是正常操作,把它算进次数
// 等于惩罚一次手滑。次数限流的目标是脚本刷单与人工审核工作量。
func checkDailyCount(tx *gorm.DB, userId, max int) error {
	if max <= 0 {
		return nil
	}
	var cnt int64
	if err := tx.Model(&Withdrawal{}).
		Where("user_id = ? AND created_at >= ? AND status <> ?", userId, dayStart(), StatusCancelled).
		Count(&cnt).Error; err != nil {
		return err
	}
	if cnt >= int64(max) {
		return errDailyCountReached
	}
	return nil
}

// dayStart 返回服务器本地时区今日 0 点的 unix 秒。
//
// 用本地时区而不是 UTC:"今天最多提 3 次"是给运营和用户看的口径,
// 跟着服务器所在地走才不会出现"晚上八点就换天了"的困惑。
func dayStart() int64 {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}

func submitDetail(w *Withdrawal) string {
	return common.MapToJsonStr(map[string]any{
		"method":                w.Method,
		"quota":                 w.Quota,
		"currency":              w.Currency,
		"frozen_fx_rate":        w.FrozenFxRate.String(),
		"frozen_quota_per_unit": w.FrozenQuotaPerUnit.String(),
		"gross_amount":          w.GrossAmount.String(),
		"fee_amount":            w.FeeAmount.String(),
		"net_amount":            w.NetAmount.String(),
		"fee_bps":               w.FeeBps,
	})
}

// loadByIdemKey 幂等命中时回读已有单。
func loadByIdemKey(userId int, clientKey string) (*orderView, error) {
	var w Withdrawal
	err := db.Get().
		Where("idem_scope = ? AND idem_key = ?", idemScope, strconv.Itoa(userId)+":"+clientKey).
		Take(&w).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, fmt.Errorf("qianye/withdraw: 幂等冲突但无法回读原单: %w", err)
	}
	return toUserView(&w, nil), nil
}

// isDuplicateKey 判断错误是否为唯一索引冲突。
// MySQL 驱动不导出结构化错误码,只能按文本匹配 —— 与地基 twophase 保持一致。
func isDuplicateKey(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate entry") ||
		strings.Contains(msg, "error 1062") ||
		strings.Contains(msg, "duplicate key")
}
