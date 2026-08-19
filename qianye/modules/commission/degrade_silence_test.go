package commission

// degrade_silence_test.go —— 降级计数器只记计佣,不记页面刷新。
//
// 被守的缺陷(审计 consistency 视角 #4):degraded.* 是"哪段时间的佣金要复核"
// 这个问题的**唯一**取证入口 —— 降级算出来的账目与正常账目在流水上长得一模一样,
// accrual 行上没有任何一列记着"本行走的是回落层"。
//
// 费率与法币比例被收进 resolveInviterPricing 一个入口之后,用户端「我的推广」
// 也走同一条解析路径,而且对同一个人连解析三次(充值/消费/兑换码)。于是任何一个
// 已登录用户按住 F5 就能把计数器按 3 倍速率推上去:实测 3 次 GET = +9,
// 50 次 = +150,同期该用户名下 accrual 行数为 0。last_at 被一路推到"刚刚",
// last_reason 被跨原因覆盖,60 秒的告警槽也被只读流量占满 —— 真正的降级被淹掉。
//
// 判据一律是"计数器的增量",不是"函数被调了几次"。

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func degradeCount(d *degradeRecord) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.count
}

// seedIllegalFiatRate 造一条真实的降级前提:库里存着一个非法的分组法币比例。
// 写入侧的 400 挡着这种值,它只可能来自手工 UPDATE / 备份恢复 / 迁移 ——
// fiatRateSane 存在的全部理由。
func seedIllegalFiatRate(t *testing.T, gdb *gorm.DB, group string) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&FiatRate{
		GroupName: group, Rate: decimal.Zero, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	invalidateFiatRatesLocal()
}

// TestDisplayPathDoesNotPolluteDegradeCounters 是本条修复的本体:
// 用户端展示路径解析三次,计数器一次都不许动。
func TestDisplayPathDoesNotPolluteDegradeCounters(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useMoneyGlobals(t, 7.3, 500000)
	main := useMainDB(t, &model.User{})
	require.NoError(t, main.Create(&model.User{
		Id: 7701, Username: "qy-cs-up", Group: "vip",
	}).Error)
	seedIllegalFiatRate(t, gdb, "vip")

	before := degradeCount(fiatRateDegrade)

	gin.SetMode(gin.TestMode)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("GET", "/api/qy/commission/summary", nil)
		c.Set("id", 7701)
		out := rateSummary(c, 7701, effective())
		// 展示的数值必须仍然正确 —— 静默的是上报,不是解析。
		require.Equal(t, "vip", out["group"], "闭嘴不等于不解析")
	}

	assert.Equal(t, before, degradeCount(fiatRateDegrade),
		"三次页面刷新把取证计数器推了上去 —— 那个数字从此不是佣金的代理量")
}

// TestAccrualPathStillReportsDegrade 是反向守卫:真正会把结果冻结进账本的
// 那条路径必须照常计数。少了它,这条修复就变成"把唯一的取证入口整个关掉"。
func TestAccrualPathStillReportsDegrade(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	useMoneyGlobals(t, 7.3, 500000)
	main := useMainDB(t, &model.User{})
	require.NoError(t, main.Create(&model.User{
		Id: 7702, Username: "qy-cs-up2", Group: "vip",
	}).Error)
	seedIllegalFiatRate(t, gdb, "vip")

	before := degradeCount(fiatRateDegrade)
	p := resolveInviterPricing(context.Background(), 7702, SourceConsume, effective())
	require.Equal(t, "vip", p.Fiat.Group)

	assert.Greater(t, degradeCount(fiatRateDegrade), before,
		"计佣路径上的降级必须留痕,否则那批佣金事后无从复核")
}

// TestDegradeSilenceIsOptIn 钉住零值口径:ctx 上没有标记 = 计佣路径 = 照常上报。
// 漏加标记只会多计,不会把一次真实降级吞掉。
func TestDegradeSilenceIsOptIn(t *testing.T) {
	assert.False(t, degradeSilenced(context.Background()))
	assert.False(t, degradeSilenced(nil))
	assert.True(t, degradeSilenced(silentDegradeCtx(context.Background())))
}
