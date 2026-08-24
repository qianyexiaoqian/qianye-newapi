package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 「音频账单可复算」的另一半:消费日志**行本身**的 prompt_tokens /
// completion_tokens 两列。
//
// 既有的 TestAudioConsumeLogCanBeRecomputedFromItsOwnFields 只核对
// GenerateAudioOtherInfo 产出的 other 明细,一条断言都没碰过
// model.RecordConsumeLog 收到的那两个字段 —— 而它们正是用量日志界面上
// 「输入 / 输出」两格、tokens/s 速率、以及对账脚本第一眼看的东西。
//
// 实测把 quota.go 那一行改回 `CompletionTokens: usage.CompletionTokens`
// (即上一轮修复前的写法),`go test ./service/` 全绿:一道刚统一的口径
// 没有任何闸门守着,下一次重构可以无声地退回去。退回之后这一行日志写着
// 「输出 1 个 token」而实收 884,申诉与退款仲裁都答不出这 884 是怎么来的。
func TestAudioConsumeLogRowRecordsNormalizedCompletionTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	truncate(t)

	const userId, channelId, tokenId = 72001, 72002, 72003
	require.NoError(t, model.DB.Create(&model.User{
		Id: userId, Username: "audio-log-row", Quota: 10_000_000, Status: common.UserStatusEnabled,
	}).Error)
	require.NoError(t, model.DB.Create(&model.Channel{Id: channelId, Name: "audio-log-row"}).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		Id: tokenId, UserId: userId, Key: "audiologrowkey", Name: "audio-log-row",
		Status: common.TokenStatusEnabled, RemainQuota: 10_000_000, UnlimitedQuota: true,
	}).Error)

	require.NoError(t, ratio_setting.UpdateCompletionRatioByJSONString(`{"audio-log-row-model":3}`))
	require.NoError(t, ratio_setting.UpdateAudioRatioByJSONString(`{"audio-log-row-model":10}`))
	require.NoError(t, ratio_setting.UpdateAudioCompletionRatioByJSONString(`{"audio-log-row-model":2}`))
	t.Cleanup(func() {
		_ = ratio_setting.UpdateCompletionRatioByJSONString(`{}`)
		_ = ratio_setting.UpdateAudioRatioByJSONString(`{}`)
		_ = ratio_setting.UpdateAudioCompletionRatioByJSONString(`{}`)
	})

	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("token_name", "audio-log-row")

	relayInfo := &relaycommon.RelayInfo{
		UserId:          userId,
		TokenId:         tokenId,
		TokenKey:        "audiologrowkey",
		OriginModelName: "audio-log-row-model",
		UsingGroup:      "default",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelId},
		PriceData: types.PriceData{
			ModelRatio:     2,
			GroupRatioInfo: types.GroupRatioInfo{GroupRatio: 1},
		},
	}

	// 上游原样上报的形态:不报 text_tokens,思考 token 落在 completion 之外。
	// {p:100, c:1, total:154, audio_in:20, reasoning:53}
	usage := &dto.Usage{
		PromptTokens:           100,
		CompletionTokens:       1,
		TotalTokens:            154,
		PromptTokensDetails:    dto.InputTokenDetails{AudioTokens: 20},
		CompletionTokenDetails: dto.OutputTokenDetails{ReasoningTokens: 53},
	}

	PostAudioConsumeQuota(c, relayInfo, usage, "")

	var logs []model.Log
	require.NoError(t, model.DB.Where("user_id = ?", userId).Find(&logs).Error)
	require.Len(t, logs, 1)
	row := logs[0]

	// 独立算出的期望(与既有的 other 复算用例同一笔):
	//   text_in  = 100 − 20 = 80
	//   text_out = (1 + 53) − 0 = 54
	//   quota = (80 + 54×3 + 20×10) × modelRatio 2 × groupRatio 1 = 884
	assert.Equal(t, 884, row.Quota, "先确认计费侧本身没漂")
	assert.Equal(t, 100, row.PromptTokens)
	assert.Equal(t, 54, row.CompletionTokens,
		"日志行的 completion_tokens 必须是归一化之后的 54 —— 计费用的就是它。"+
			"记 1 的话，界面上写着「输出 1 个 token」而这一行扣了 884")
}
