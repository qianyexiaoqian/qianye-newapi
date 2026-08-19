package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subscription_settle_deleted_row_test.go —— 订阅行在请求在途时被硬删掉之后,
// 这一笔仍然要收得到钱。
//
// ═══════════ 被修掉的洞 ═══════════
//
// 管理端的「删除」按钮(AdminDeleteUserSubscription)硬删 user_subscriptions 行,
// 且不理会 subscription_pre_consume_records 里那条在途记录。请求结算时
// SettleUserSubscriptionDelta 的 First 报 record not found →
// SubscriptionFunding.Settle 报错 → BillingSession.Settle 直接 return →
// SettleBilling 的三个调用方都只打一行日志就继续把**全额**写进消费日志。
//
// 实测(真 HTTP + 真管理端 DELETE):同一笔消费,对照组收 25,010,删行组收 **0**
// —— 其中本该由钱包补收的 20,010 一分没扣,令牌余额也停在预扣值,而两条日志的
// quota 都写 25,010。日结返佣读的正是 logs.quota,于是上线拿到了平台从未收到的
// 那笔钱的佣金。
//
// ═══════════ 修完的口径 ═══════════
//
//	delta > 0(还要再收钱) → applied=0,不报错。撞上限之后本来就该由钱包补收,
//	                         那一段与被删掉的行无关。
//	delta < 0(要退回套餐) → 原样报错。行没了就是真退不回去,而那是"用户少拿钱"
//	                         的方向,必须让调用方保留人工对账标记。

const (
	deletedRowUser = 6801
	deletedRowSub  = 6802 // 故意不建这一行
)

// TestSettleChargesTheWalletWhenTheSubscriptionRowIsGone 正差额:钱照收。
func TestSettleChargesTheWalletWhenTheSubscriptionRowIsGone(t *testing.T) {
	truncate(t)
	seedUser(t, deletedRowUser, 5_000_000)

	funding := &SubscriptionFunding{userId: deletedRowUser, subscriptionId: deletedRowSub}
	require.NoError(t, funding.Settle(20_010),
		"订阅行没了不等于这一笔不用收 —— 结算链不该在这里整段断掉")
	assert.Equal(t, int64(0), funding.SettleApplied(), "套餐这一侧确实无处可落")
	assert.Equal(t, int64(20_010), funding.SettleWalletShortfall(), "差额必须落到钱包")

	quota, err := model.GetUserQuota(deletedRowUser, true)
	require.NoError(t, err)
	assert.Equal(t, 5_000_000-20_010, quota, "钱包必须真的被扣掉那 20,010")
}

// TestSettleStillFailsWhenRefundingIntoAGoneSubscription 负差额:必须报错。
//
// 没有它,「两个方向都宽容」这个更省事的写法会通过 —— 而那会把一笔本该人工
// 跟进的退款静默记成"已退"(service.RefundTaskQuota 正是靠这个错误保住
// task.Quota 这个重试标记的)。
func TestSettleStillFailsWhenRefundingIntoAGoneSubscription(t *testing.T) {
	truncate(t)
	seedUser(t, deletedRowUser, 5_000_000)

	funding := &SubscriptionFunding{userId: deletedRowUser, subscriptionId: deletedRowSub}
	require.Error(t, funding.Settle(-1_200),
		"退款方向退不回去就是退不回去,必须让调用方看见")

	quota, err := model.GetUserQuota(deletedRowUser, true)
	require.NoError(t, err)
	assert.Equal(t, 5_000_000, quota, "退款失败时钱包一分都不许动")
}

// failingFunding 是一个恒定失败的资金来源,用来把「资金侧失败」与「令牌侧调整」
// 这两件事拆开看。
type failingFunding struct{ settleCalls int }

func (f *failingFunding) Source() string       { return BillingSourceWallet }
func (f *failingFunding) PreConsume(int) error { return nil }
func (f *failingFunding) Settle(int) error {
	f.settleCalls++
	return errors.New("funding backend is down")
}
func (f *failingFunding) Refund() error { return nil }

// TestTokenQuotaIsStillAdjustedWhenFundingSettleFails 资金侧失败不许把令牌那一步
// 一起跳过。
//
// 令牌的 remain_quota 是「这把 key 最多还能花多少」,与资金侧是两笔独立的账。
// 跳过它会让它永久停在预扣值:正差额时用户白得一截消费上限,负差额时被多占一截。
func TestTokenQuotaIsStillAdjustedWhenFundingSettleFails(t *testing.T) {
	truncate(t)
	seedUser(t, deletedRowUser, 5_000_000)
	token := &model.Token{
		Id: 6803, UserId: deletedRowUser, Key: "settledeletedrowtoken00000000000",
		Status: 1, RemainQuota: 100_000, UnlimitedQuota: false,
	}
	require.NoError(t, model.DB.Create(token).Error)

	funding := &failingFunding{}
	session := &BillingSession{
		relayInfo: &relaycommon.RelayInfo{
			UserId: deletedRowUser, TokenId: token.Id, TokenKey: token.Key,
		},
		funding:          funding,
		preConsumedQuota: 3_000,
	}

	err := session.Settle(5_000)
	require.Error(t, err, "资金侧的失败必须原样返回,否则日志里那条 SettleFailure 标记就没人写了")
	assert.Equal(t, 1, funding.settleCalls)

	var after model.Token
	require.NoError(t, model.DB.Where("id = ?", token.Id).First(&after).Error)
	assert.Equal(t, 100_000-2_000, after.RemainQuota,
		"令牌额度必须按差额补扣,不能因为资金侧失败就停在预扣值")

	// 再结算一次不许把令牌调第二遍(资金侧仍然失败,session 也仍未 settled)。
	require.Error(t, session.Settle(5_000))
	require.NoError(t, model.DB.Where("id = ?", token.Id).First(&after).Error)
	assert.Equal(t, 100_000-2_000, after.RemainQuota, "令牌那一步至多执行一次")
}
