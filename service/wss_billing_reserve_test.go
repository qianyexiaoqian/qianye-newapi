package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A realtime (WSS) session bills twice unless the per-turn amounts are
// *reserved* on the billing session instead of being consumed outright.
//
// PostWssConsumeQuota recomputes the whole session from the accumulated usage
// and hands that total to SettleBilling, whose delta is measured against the
// session's pre-consumed quota. When the per-turn amounts were charged through
// PostConsumeQuota they were invisible to the session, so the wallet paid
// Σ(turns) once directly and then the full session total again as settlement
// delta — roughly 2x for every per-token realtime session.
//
// This test pins the accounting identity: whatever the per-turn path reserves,
// the wallet movement over the whole session must equal the session total
// exactly once. Reverting PreWssConsumeQuota to PostConsumeQuota makes the
// wallet drop by total + Σ(turns) and turns this red.
func TestRealtimePerTurnReservationIsCreditedAgainstSessionSettlement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	truncate(t)

	const (
		userId       = 6101
		tokenId      = 6201
		tokenKey     = "wss-reserve-key"
		startQuota   = 5_000_000
		startRemain  = 5_000_000
		preConsumed  = 100
		turnOneQuota = 900
		turnTwoQuota = 1500
	)
	seedUser(t, userId, startQuota)
	seedToken(t, tokenId, userId, tokenKey, startRemain)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("GET", "/v1/realtime", nil)
	ctx.Set("token_quota", startRemain)

	relayInfo := &relaycommon.RelayInfo{
		UserId:          userId,
		TokenId:         tokenId,
		TokenKey:        tokenKey,
		OriginModelName: "wss-reserve-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		PriceData: hosttypes.PriceData{
			ModelRatio: 1,
			GroupRatioInfo: hosttypes.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
	}
	require.Nil(t, PreConsumeBilling(ctx, preConsumed, relayInfo))
	require.NotNil(t, relayInfo.Billing)

	// Two turns, charged incrementally by the realtime adaptor. Text tokens
	// only, so the quota per turn is simply tokens * modelRatio * groupRatio.
	require.NoError(t, PreWssConsumeQuota(ctx, relayInfo, &dto.RealtimeUsage{
		TotalTokens:       turnOneQuota,
		InputTokenDetails: dto.InputTokenDetails{TextTokens: turnOneQuota},
	}))
	require.NoError(t, PreWssConsumeQuota(ctx, relayInfo, &dto.RealtimeUsage{
		TotalTokens:       turnTwoQuota,
		InputTokenDetails: dto.InputTokenDetails{TextTokens: turnTwoQuota},
	}))

	sessionTotal := turnOneQuota + turnTwoQuota
	require.NoError(t, SettleBilling(ctx, relayInfo, sessionTotal))

	quotaAfter, err := model.GetUserQuota(userId, true)
	require.NoError(t, err)
	assert.Equal(t, startQuota-sessionTotal, quotaAfter,
		"the whole realtime session must be charged exactly once")

	tokenAfter, err := model.GetTokenById(tokenId)
	require.NoError(t, err)
	assert.Equal(t, startRemain-sessionTotal, tokenAfter.RemainQuota,
		"token quota must move by the same amount as the wallet")
}
