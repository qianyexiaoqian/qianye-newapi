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
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// 扣了钱就必须记进统计,反过来说:统计那道门的判据必须与 SettleBilling 一致。
//
// PostTextConsumeQuota 里 SettleBilling 是**无条件**执行的,而
// UpdateUserUsedQuotaAndRequestCount / UpdateChannelUsedQuota 曾经只在
// hasBillableUsage() 为真时才跑。按次计价(UsePrice)时金额与 token 数无关 ——
// 上面那段刻意只在按量计费下把 Quota 清零,按次计费不能因为 token 数为 0 就免单。
// 于是存在一条真实可达的路:
//
//   - /v1/responses 非流式,上游返回一份**完全合法、只是没带 usage** 的报文
//     (OaiResponsesHandler 对 Usage == nil 一条兜底都没有);
//   - 或上游自报负 token,被 clampUpstreamTokenCount 夹成 0。
//
// 这一次照样扣了全额,而 users.used_quota、users.request_count 与
// channels.used_quota 一个都不加。后果是用户面板上的"已用额度"与实际扣掉的
// 余额长期对不上、且不可事后自愈,渠道用量也永久少记 —— 而消费日志正文写的是
// 「上游没有返回计费信息,无法扣费」,与实际扣了钱正好相反。
//
// 音频/实时两条路(service/quota.go)早就是 `totalTokens > 0 || quota > 0`,
// 文本路是漏改的那一处。
func TestPerCallPricingWithZeroTokensStillCountsTowardUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.Channel{}))

	prevDB, prevLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = gdb, gdb
	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
		_ = sqlDB.Close()
	})

	const (
		userId    = 99001
		channelId = 99002
		// 按次价 0.5 × QuotaPerUnit 500000 × 分组倍率 1 = 250000
		wantQuota = 250000
	)
	require.NoError(t, gdb.Create(&model.User{Id: userId, Username: "percall-probe", Quota: 10_000_000}).Error)
	require.NoError(t, gdb.Create(&model.Channel{Id: channelId, Name: "percall-probe"}).Error)

	res := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(res)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("token_name", "percall-probe")

	relayInfo := &relaycommon.RelayInfo{
		UserId:          userId,
		OriginModelName: "percall-probe-model",
		StartTime:       time.Now(),
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelId},
		PriceData: types.PriceData{
			UsePrice:   true,
			ModelPrice: 0.5,
			GroupRatioInfo: types.GroupRatioInfo{
				GroupRatio: 1,
			},
		},
	}

	// 上游返回了一份合法但**不带 usage** 的响应:三个数字全是 0。
	usage := &dto.Usage{}

	PostTextConsumeQuota(c, relayInfo, usage, nil)

	var u model.User
	require.NoError(t, gdb.Where("id = ?", userId).Take(&u).Error)
	var ch model.Channel
	require.NoError(t, gdb.Where("id = ?", channelId).Take(&ch).Error)

	assert.Equal(t, wantQuota, u.UsedQuota,
		"按次计价这一笔实扣 %d,users.used_quota 必须同步 —— "+
			"扣了钱却不进统计会让用户面板上的『已用额度』与余额长期对不上,而且不可事后自愈",
		wantQuota)
	assert.Equal(t, 1, u.RequestCount, "请求数同样必须加,否则这一次调用在统计上不存在")
	assert.Equal(t, int64(wantQuota), ch.UsedQuota, "渠道用量少记会让成本归集永久偏低")

	// 对照:金额确实是按次价算出来的,而不是被别的分支凑巧填上的。
	require.Equal(t, wantQuota, int(0.5*common.QuotaPerUnit*1))
}
