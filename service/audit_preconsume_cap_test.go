package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_preconsume_cap_test.go —— 「余额与令牌额度到底是不是上限」。
//
// 曾经有一条信任额度旁路:余额(或令牌余额)超过 common.GetTrustQuota()
// (硬编码 10×QuotaPerUnit = $10)时把预扣额置 0,令牌预扣与资金预扣两步一起
// 跳过 —— model.TryReserveUserQuota 那把 `WHERE quota >= ?` 的原子闩根本没被
// 调用。于是 N 路并发各自读到同一个余额快照、各自判定"付得起",结算时无下限
// 地扣:实测余额 $10.000002 的账号 50 路并发打到 -$90,200 路打到 -$328。
//
// 下面两条把"预扣真的发生了"钉死。把旁路加回去(preConsume 里 effectiveQuota=0)
// 会让两条同时红。

func newWalletRelayInfo(userId, tokenId int, tokenKey string) *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		UserId:          userId,
		TokenId:         tokenId,
		TokenKey:        tokenKey,
		OriginModelName: "preconsume-cap-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		PriceData: hosttypes.PriceData{
			ModelRatio:     1,
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
	}
}

// TestRichAccountsStillReserveQuotaBeforeTheUpstreamCall 用两个**先后**建立的
// 计费会话建模两路并发:第二路在第一路结算之前就开始。
//
// 只有当第一路真的把额度预占住,第二路才可能被拒。旁路存在时余额一分未动,
// 两路都会放行 —— 并发数就是超支倍数。
func TestRichAccountsStillReserveQuotaBeforeTheUpstreamCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	truncate(t)

	trustQuota := common.GetTrustQuota()
	require.Greater(t, trustQuota, 0, "信任线必须是正数,否则这条用例证明不了任何事")

	const (
		userId   = 7301
		tokenId  = 7401
		tokenKey = "preconsume-cap-key"
	)
	// 余额刚刚越过信任线 —— 正是旧旁路会命中的那一格。
	startQuota := trustQuota + 100
	preConsume := trustQuota + 60

	seedUser(t, userId, startQuota)
	seedToken(t, tokenId, userId, tokenKey, startQuota)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Set("token_quota", startQuota)

	first := newWalletRelayInfo(userId, tokenId, tokenKey)
	require.Nil(t, PreConsumeBilling(ctx, preConsume, first))

	// 第一路的预扣必须真的落库,钱包与令牌都要少掉这么多。
	gotQuota, err := model.GetUserQuota(userId, false)
	require.NoError(t, err)
	assert.Equal(t, startQuota-preConsume, gotQuota,
		"余额充足不等于不预扣:不预占就没有任何东西能限制并发路数")

	var tok model.Token
	require.NoError(t, model.DB.Where("id = ?", tokenId).First(&tok).Error)
	assert.Equal(t, startQuota-preConsume, tok.RemainQuota,
		"令牌额度是「这把 key 最多花多少」的硬约束,同样必须被预占")

	// 第二路只剩 40 可用,要 trustQuota+60 —— 必须被拒。
	second := newWalletRelayInfo(userId, tokenId, tokenKey)
	apiErr := PreConsumeBilling(ctx, preConsume, second)
	require.NotNil(t, apiErr, "第二路并发必须被余额挡住,否则超支随并发线性放大")

	afterQuota, err := model.GetUserQuota(userId, false)
	require.NoError(t, err)
	assert.Equal(t, startQuota-preConsume, afterQuota,
		"被拒的那一路不能留下任何扣减")
}

// TestUnlimitedTokenStillReservesTheWalletSide 单独钉住「令牌无限额」这一格:
// 旧旁路的判据是 tokenTrusted && userQuota > trustQuota,令牌无限额直接让
// tokenTrusted 成立,于是钱包侧也一起被跳过。
func TestUnlimitedTokenStillReservesTheWalletSide(t *testing.T) {
	gin.SetMode(gin.TestMode)
	truncate(t)

	trustQuota := common.GetTrustQuota()
	const (
		userId   = 7302
		tokenId  = 7402
		tokenKey = "preconsume-cap-unlimited-key"
	)
	startQuota := trustQuota * 3
	preConsume := 12_345

	seedUser(t, userId, startQuota)
	seedToken(t, tokenId, userId, tokenKey, 0)
	require.NoError(t, model.DB.Model(&model.Token{}).
		Where("id = ?", tokenId).Update("unlimited_quota", true).Error)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	relayInfo := newWalletRelayInfo(userId, tokenId, tokenKey)
	relayInfo.TokenUnlimited = true
	require.Nil(t, PreConsumeBilling(ctx, preConsume, relayInfo))

	gotQuota, err := model.GetUserQuota(userId, false)
	require.NoError(t, err)
	assert.Equal(t, startQuota-preConsume, gotQuota,
		"令牌无限额只说明这把 key 没有上限,不说明这个人的钱包没有上限")
}
