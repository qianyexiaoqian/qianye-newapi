package helper

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_tiered_group_switch_test.go —— 锁死「阶梯计价 + 分组级乘数」在 auto 重试
// 切分组时的预留额口径。
//
// 这条链路横跨两个包,任何一个包内的单测都看不全:
//
//	relay/helper.modelPriceHelperTiered      算首次预扣、写快照
//	service.PrepareTieredBillingForSelectedGroup  每次重试按当前分组重算预留额
//
// 缺陷形状是「快照里存了已乘原分组乘数的值,切组后被当成原始值再乘一次新倍率」。
// 它只在两个包接起来看的时候才暴露,所以测试写在 relay/helper(本包已依赖
// service,反向不成立)。

// recordingReserver 只记录 Reserve 目标值,不碰数据库。
type recordingReserver struct {
	preConsumed int
	targets     []int
}

func (*recordingReserver) Settle(int) error           { return nil }
func (*recordingReserver) Refund(*gin.Context)        {}
func (*recordingReserver) NeedsRefund() bool          { return false }
func (r *recordingReserver) GetPreConsumedQuota() int { return r.preConsumed }

func (r *recordingReserver) Reserve(target int) error {
	r.targets = append(r.targets, target)
	if target > r.preConsumed {
		r.preConsumed = target
	}
	return nil
}

// stubTieredGroupMultiplier 把两个挂载点同时换成「按 UsingGroup 查表的乘数」,
// 模拟 grouppricing 给不同分组配了不同折扣。两个挂载点必须换成同一份实现 ——
// 生产里它们本来就是同一个函数体(applyTieredQuota)。
func stubTieredGroupMultiplier(t *testing.T, byGroup map[string]float64) {
	t.Helper()
	savedPrice := QyGroupTieredQuota
	savedSettle := service.QyGroupTieredSettle
	t.Cleanup(func() {
		QyGroupTieredQuota = savedPrice
		service.QyGroupTieredSettle = savedSettle
	})

	apply := func(info *relaycommon.RelayInfo, quotaBeforeGroup float64) float64 {
		m, ok := byGroup[info.UsingGroup]
		if !ok {
			return quotaBeforeGroup
		}
		return quotaBeforeGroup * m
	}
	QyGroupTieredQuota = apply
	service.QyGroupTieredSettle = apply
}

func setTieredTestGroupRatios(t *testing.T, jsonStr string) {
	t.Helper()
	saved := ratio_setting.GroupRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(saved))
	})
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(jsonStr))
}

func setTieredTestBillingConfig(t *testing.T, kv map[string]string) {
	t.Helper()
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(kv))
}

// priceTieredForGroup 走完整的上游计价入口,返回首次预扣额。
func priceTieredForGroup(t *testing.T, model, group string, promptTokens int) (*relaycommon.RelayInfo, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", group)

	info := &relaycommon.RelayInfo{
		OriginModelName: model,
		UserGroup:       group,
		UsingGroup:      group,
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
	}
	priceData, err := ModelPriceHelper(ctx, info, promptTokens, &types.TokenCountMeta{})
	require.NoError(t, err)
	return info, priceData.QuotaToPreConsume
}

// TestTieredSnapshotKeepsQuotaBeforeGroupUnmultiplied 锁住快照字段的语义。
//
// EstimatedQuotaBeforeGroup 的语义是「未乘任何分组因子的表达式结果」。
// 把已乘乘数的值写回去(改动前的写法)不会让任何算术出错,首次预扣依然正确 ——
// 错的只有后续按新分组重算预留额那一步。因此必须直接断言这个字段本身。
func TestTieredSnapshotKeepsQuotaBeforeGroupUnmultiplied(t *testing.T) {
	setTieredTestBillingConfig(t, map[string]string{
		"billing_setting.billing_mode": `{"qy-tiered-snapshot":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"qy-tiered-snapshot":"tier(\"base\", p * 2)"}`,
	})
	setTieredTestGroupRatios(t, `{"qy-grp-a":0.5}`)
	stubTieredGroupMultiplier(t, map[string]float64{"qy-grp-a": 0.4})

	info, preConsumed := priceTieredForGroup(t, "qy-tiered-snapshot", "qy-grp-a", 1000)
	require.NotNil(t, info.TieredBillingSnapshot)

	// rawCost = 1000 * 2 = 2000 → quota = 2000 / 1e6 * 500000 = 1000
	assert.Equal(t, 1000.0, info.TieredBillingSnapshot.EstimatedQuotaBeforeGroup,
		"快照必须存未乘分组乘数的原始表达式额度;存成 400(已乘 0.4)会让切组后的预留额把原分组的乘数带进新分组")
	// 首次预扣仍然按分组乘数打折:1000 * 0.4 * 0.5 = 200
	assert.Equal(t, 200, preConsumed)
	assert.Equal(t, 200, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
}

// TestTieredReservationRecomputesGroupMultiplierOnRetry 是这条链路的端到端锁。
//
// 三个子用例覆盖三种切组形状,其中「倍率相同、分组不同」是改动前唯一无法靠
// GroupRatio 短路发现的那一种 —— 也是最容易被漏掉的一种。
func TestTieredReservationRecomputesGroupMultiplierOnRetry(t *testing.T) {
	setTieredTestBillingConfig(t, map[string]string{
		"billing_setting.billing_mode": `{"qy-tiered-retry":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"qy-tiered-retry":"tier(\"base\", p * 2)"}`,
	})
	// qy-grp-a 与 qy-grp-b 倍率**相同**,只有分组级乘数不同。
	setTieredTestGroupRatios(t, `{"qy-grp-a":0.5,"qy-grp-b":0.5,"qy-grp-c":2}`)
	stubTieredGroupMultiplier(t, map[string]float64{
		"qy-grp-a": 0.4,
		"qy-grp-b": 1,
		"qy-grp-c": 0.25,
	})

	tests := []struct {
		name        string
		retryGroup  string
		retryRatio  float64
		wantReserve int
	}{
		// 没切组:重算必须落回首次预扣的同一个数。
		// 这一条同时挡住「快照存了已乘值 + 刷新再乘一次」的双重相乘。
		{name: "不切组必须幂等", retryGroup: "qy-grp-a", retryRatio: 0.5, wantReserve: 200},
		// 倍率相同、分组不同:1000 * 1 * 0.5 = 500(改动前会停在 200)
		{name: "倍率相同分组不同", retryGroup: "qy-grp-b", retryRatio: 0.5, wantReserve: 500},
		// 倍率也变:1000 * 0.25 * 2 = 500(改动前会算成 1000*0.4*2 = 800)
		{name: "倍率与乘数都变", retryGroup: "qy-grp-c", retryRatio: 2, wantReserve: 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, preConsumed := priceTieredForGroup(t, "qy-tiered-retry", "qy-grp-a", 1000)
			require.Equal(t, 200, preConsumed)

			billing := &recordingReserver{preConsumed: preConsumed}
			info.Billing = billing

			// getChannel 在调用 PrepareTieredBillingForSelectedGroup 之前
			// 已经跑过 HandleGroupRatio:UsingGroup 与 GroupRatioInfo 都已是新分组。
			info.UsingGroup = tt.retryGroup
			info.PriceData.GroupRatioInfo.GroupRatio = tt.retryRatio

			require.Nil(t, service.PrepareTieredBillingForSelectedGroup(nil, info))

			require.Equal(t, []int{tt.wantReserve}, billing.targets)
			assert.Equal(t, tt.wantReserve, info.TieredBillingSnapshot.EstimatedQuotaAfterGroup)
			assert.Equal(t, tt.retryRatio, info.TieredBillingSnapshot.GroupRatio)
		})
	}
}
