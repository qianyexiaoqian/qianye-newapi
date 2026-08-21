package service

import (
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wallet_overdraft_message_test.go —— 「余额用完了」与「你已经欠费了」必须是两句话。
//
// ═══════════ 为什么这是一条真契约,而不是在测文案 ═══════════
//
// 本站**刻意接受**结算把余额扣成负数(拍板与代价见 qianye/docs/decisions.md
// 的 D-01)。于是 userQuota < 0 是一个正常会出现的状态,而不是脏数据。
//
// 在此之前这一档与「余额为 0」共用一句「用户额度不足, 剩余额度: -$1.23」。
// 那句话对欠费用户是错的指引:他看到的是一个带负号的余额,既不知道那是欠款,
// 也不知道为什么充值 1 块钱之后仍然被拒(充完还是负的)。**用户按这句话去做
// 的动作(小额充值)不会解决他的问题** —— 这才是本条测试要钉死的东西。
//
// 同时钉死**错误码不变**:客户端的重试/降级按码走,为了分辨文案而换码,
// 会让所有既有集成在这一档上悄悄改变行为。区分只落在人读的 message 上。

const overdraftMsgUser = 90_301

func newOverdraftRelayInfo(requestId string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		RequestId:       requestId,
		UserId:          overdraftMsgUser,
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: "gpt-test",
		// 令牌额度不是本表要验的东西,绕开它。
		IsPlayground:    true,
		ForcePreConsume: true,
		UserSetting:     dto.UserSetting{},
	}
}

func TestPreConsumeRejectionDistinguishesEmptyWalletFromOverdraft(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name        string
		walletQuota int
		// wantContains / wantNotContains 是**语义**断言,不是逐字比对:
		// 要守的是"这句话有没有告诉用户他欠着钱",不是标点。
		wantContains    []string
		wantNotContains []string
		why             string
	}{
		{
			name:            "余额恰好为 0 —— 用完了",
			walletQuota:     0,
			wantContains:    []string{"额度不足"},
			wantNotContains: []string{"透支", "欠费"},
			why:             "零余额是「用完了」,充值即可继续,不该说成欠费",
		},
		{
			name:        "余额为负 —— 欠费了",
			walletQuota: -140_000,
			// 欠款额必须出现,而且是**正数**形态:让用户知道要补多少才能继续。
			// 140000 额度 ÷ 500000 (QuotaPerUnit) = 0.28 —— 独立算出的期望。
			// 只比数字部分:货币符号由站点配置决定(实测渲染成全角 ＄),
			// 把符号也钉进断言等于让一次货币配置变更把这条测试弄红。
			wantContains:    []string{"透支", "欠费", "0.280000"},
			wantNotContains: []string{"-0.280000"},
			why: "透支是本站接受的取舍(decisions.md D-01),用户必须被告知" +
				"「你欠了多少」而不是「你余额是负的」—— 后者会让他做出小额充值这个" +
				"解决不了问题的动作",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			truncate(t)
			seedUser(t, overdraftMsgUser, tc.walletQuota)
			stubCandidateUsable(t, true)
			stubFundingGate(t, true)

			ctx, _ := gin.CreateTestContext(nil)
			session, apiErr := NewBillingSession(ctx, newOverdraftRelayInfo("overdraft-"+tc.name), 100)

			require.NotNilf(t, apiErr, "这一格必须被拒。理由:%s", tc.why)
			assert.Nil(t, session)
			// 错误码在两档之间刻意保持一致。
			assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode(),
				"分辨文案不许换错误码 —— 客户端的重试/降级策略按码走")

			msg := apiErr.Error()
			for _, want := range tc.wantContains {
				assert.Containsf(t, msg, want,
					"这句话必须包含 %q。理由:%s(实际:%s)", want, tc.why, msg)
			}
			for _, bad := range tc.wantNotContains {
				assert.NotContainsf(t, msg, bad,
					"这句话不该包含 %q。理由:%s(实际:%s)", bad, tc.why, msg)
			}
		})
	}
}

// 两档的文案必须**互不相同**。
//
// 单独一条,是因为上面那张表可以在两句话完全一样时仍然全绿:只要那一句同时
// 含"额度不足"又不含"透支"……不,做不到 —— 但反过来可以:把欠费档的文案抄成
// 零余额档的文案再加上"透支"两个字,表还是绿的,而用户看到的仍然是同一句废话。
// 这条断言要的是"这两档在产品上被当成两件事"这个事实本身。
func TestEmptyWalletAndOverdraftMessagesAreNotTheSame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	messageFor := func(t *testing.T, quota int) string {
		t.Helper()
		truncate(t)
		seedUser(t, overdraftMsgUser, quota)
		stubCandidateUsable(t, true)
		stubFundingGate(t, true)

		ctx, _ := gin.CreateTestContext(nil)
		_, apiErr := NewBillingSession(ctx, newOverdraftRelayInfo("overdraft-distinct"), 100)
		require.NotNil(t, apiErr)
		return apiErr.Error()
	}

	var empty, overdrawn string
	t.Run("empty", func(t *testing.T) { empty = messageFor(t, 0) })
	t.Run("overdrawn", func(t *testing.T) { overdrawn = messageFor(t, -140_000) })

	require.NotEmpty(t, empty)
	require.NotEmpty(t, overdrawn)
	assert.NotEqual(t, empty, overdrawn,
		"余额为 0 与余额为负必须给出不同的话 —— 用户要采取的动作不一样")
	assert.False(t, strings.EqualFold(empty, overdrawn))
}
