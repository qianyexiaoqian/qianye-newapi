package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qy_task_billing_pin_test.go —— Task 差额结算的两条交叉倍率不变量。
//
// 两条都不是"覆盖率",它们各自对应一次真实的错账:
//
//  1. 交叉倍率的坐标是 (用户分组, 模型分组) 这**一对**。此前只 pin 了模型分组
//     (task.Group),用户分组是结算这一刻从 users 表现读的 —— 任务运行期间的一次
//     降级/升级会让预扣与结算落在矩阵的两个不同格子上,差额以追扣落到用户头上;
//     而日志里 other["group_ratio"] 写的又是提交时刻的值,事后对账查不出来。
//  2. 静默 fail-open 必须逐笔落进 other.admin_info.group_ratio_missing。Task 是
//     单笔金额最大的那条计费链路,它不写这个标记的话,运维按这个键全量扫描补差会
//     得出「Task 没事」这个正好相反的结论。

// withGroupRatios 临时替换两张倍率表。
func withGroupRatios(t *testing.T, groupRatio, groupGroupRatio string) {
	t.Helper()
	prevBase := ratio_setting.GroupRatio2JSONString()
	prevCross := ratio_setting.GroupGroupRatio2JSONString()
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(groupRatio))
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(groupGroupRatio))
	t.Cleanup(func() {
		_ = ratio_setting.UpdateGroupRatioByJSONString(prevBase)
		_ = ratio_setting.UpdateGroupGroupRatioByJSONString(prevCross)
	})
}

func newPinnedTask(t *testing.T, userId int, modelGroup, pinnedUserGroup string, preConsumed int) *model.Task {
	t.Helper()
	task := &model.Task{
		TaskID:    "task_pin_" + time.Now().Format("150405.000000"),
		UserId:    userId,
		ChannelId: 1,
		Quota:     preConsumed,
		Status:    model.TaskStatus(model.TaskStatusSuccess),
		Group:     modelGroup,
		Data:      json.RawMessage(`{}`),
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
		Properties: model.Properties{
			OriginModelName: "qy-pin-model",
		},
		PrivateData: model.TaskPrivateData{
			BillingSource: "wallet",
			BillingContext: &model.TaskBillingContext{
				ModelRatio:      1,
				GroupRatio:      0.5,
				OriginModelName: "qy-pin-model",
				UserGroup:       pinnedUserGroup,
			},
		},
	}
	require.NoError(t, task.Insert())
	return task
}

// TestTaskSettlementPinsBothAxesOfTheCrossCell —— 用户分组这一轴必须来自提交时刻的
// BillingContext,而不是结算这一刻的 users 表。
func TestTaskSettlementPinsBothAxesOfTheCrossCell(t *testing.T) {
	truncate(t)
	withGroupRatios(t, `{"视频池":2}`, `{"vip":{"视频池":0.5}}`)
	prevRatio, prevHas, _ := ratio_setting.GetModelRatio("qy-pin-model")
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":1}`))
	t.Cleanup(func() {
		if !prevHas {
			_ = ratio_setting.UpdateModelRatioByJSONString(`{}`)
			return
		}
		_ = ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":` + formatFloat(prevRatio) + `}`)
	})

	seedUser(t, 91, 1000000)
	// 提交时是 vip(交叉格 0.5),结算前被降级到 default(交叉格未命中 → 兜底 2)。
	task := newPinnedTask(t, 91, "视频池", "vip", 500)
	require.NoError(t, model.DB.Exec("UPDATE users SET `group` = 'default' WHERE id = 91").Error)

	RecalculateTaskQuotaByTokens(context.Background(), task, 1000)

	var settled model.Task
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&settled).Error)
	// 1000 tokens × modelRatio 1 × groupRatio 0.5 = 500,与预扣一致 ⇒ quota 不变。
	assert.Equal(t, 500, settled.Quota,
		"结算必须按提交时刻的 (vip, 视频池) = 0.5 算;读成 (default, 视频池) = 2 会算出 2000 并向用户追扣 1500")
}

// TestTaskSettlementFallsBackToLiveUserGroupForLegacyRows —— 历史行(没有 UserGroup
// 字段)必须逐位保持改动前的行为:现读 users.group。
func TestTaskSettlementFallsBackToLiveUserGroupForLegacyRows(t *testing.T) {
	truncate(t)
	withGroupRatios(t, `{"视频池":2}`, `{"vip":{"视频池":0.5}}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":1}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(`{}`) })

	seedUser(t, 92, 1000000)
	require.NoError(t, model.DB.Exec("UPDATE users SET `group` = 'vip' WHERE id = 92").Error)
	task := newPinnedTask(t, 92, "视频池", "", 500) // UserGroup 为空 = 历史行

	RecalculateTaskQuotaByTokens(context.Background(), task, 1000)

	var settled model.Task
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&settled).Error)
	assert.Equal(t, 500, settled.Quota, "历史行回落现读 users.group=vip ⇒ 0.5 ⇒ 500")
}

// TestTaskSettlementRecordsGroupRatioFailOpen —— 模型分组不在 GroupRatio 里时,
// 这一笔差额按凭空的 1.0 扣掉,必须在消费日志的 admin_info 里留下可补差的凭据。
func TestTaskSettlementRecordsGroupRatioFailOpen(t *testing.T) {
	truncate(t)
	// 「浅夜の梦专属号池」有 enabled abilities、却被从 GroupRatio 删掉 —— 孤儿令牌的成因。
	withGroupRatios(t, `{"default":1}`, `{}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":1}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(`{}`) })

	seedUser(t, 93, 1000000)
	task := newPinnedTask(t, 93, "浅夜の梦专属号池", "default", 100)

	RecalculateTaskQuotaByTokens(context.Background(), task, 1000)

	var logs []model.Log
	require.NoError(t, model.DB.Where("user_id = ?", 93).Find(&logs).Error)
	require.NotEmpty(t, logs, "差额结算必须写一条日志")

	found := false
	for _, entry := range logs {
		other := map[string]any{}
		if err := common.Unmarshal([]byte(entry.Other), &other); err != nil {
			continue
		}
		admin, ok := other["admin_info"].(map[string]any)
		if !ok {
			continue
		}
		miss, ok := admin["group_ratio_missing"].(map[string]any)
		if !ok {
			continue
		}
		found = true
		assert.Equal(t, "浅夜の梦专属号池", miss["model_group"])
		assert.Equal(t, "default", miss["user_group"])
		assert.Equal(t, float64(1), miss["applied_ratio"])
	}
	assert.True(t, found,
		"Task 差额结算漏挂 group_ratio_missing —— 运维按这个键全量扫描补差时会得出"+
			"「只有文本/WSS 受影响、Task 没事」这个完全错误的结论")
}

func formatFloat(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// newTaskWithPinnedRatio 造一条**带倍率 pin** 的任务行(线上新数据的形状)。
//
// 与 newPinnedTask 的差别只有 GroupRatioPinned 这一位,而那一位正是被测行为:
// 它为 false 的历史行必须回落现算(见上面那两条),为 true 的新行必须沿用 pin。
func newTaskWithPinnedRatio(t *testing.T, userId int, modelGroup, userGroup string, pinnedRatio float64, preConsumed int) *model.Task {
	t.Helper()
	task := newPinnedTask(t, userId, modelGroup, userGroup, preConsumed)
	task.PrivateData.BillingContext.GroupRatio = pinnedRatio
	task.PrivateData.BillingContext.GroupRatioPinned = true
	require.NoError(t, model.DB.Model(&model.Task{}).Where("task_id = ?", task.TaskID).
		Update("private_data", task.PrivateData).Error)
	return task
}

// taskConsumeOther 读回某个用户的那条差额结算日志的 other。
func taskConsumeOther(t *testing.T, userId int) map[string]any {
	t.Helper()
	var logs []model.Log
	require.NoError(t, model.DB.Where("user_id = ?", userId).Find(&logs).Error)
	require.NotEmpty(t, logs, "差额结算必须写一条日志")
	other := map[string]any{}
	require.NoError(t, common.Unmarshal([]byte(logs[len(logs)-1].Other), &other))
	return other
}

// TestTaskSettlementUsesThePinnedRatioWhenTheMatrixChangesMidFlight ——
// 任务运行期间倍率表被改动时,差额结算必须按**提交那一刻**的倍率算。
//
// 场景取自真实的运维动作:vip 谈好 (vip, pool) = 0.2,任务在跑;运营在「用户分组」
// 页把 vip 改名或删除,groupns 的 dropUserGroupOptions 删掉 GroupGroupRatio 的
// 外层键;结算若重新解析,就回落到兜底的 2 —— 同一笔任务从 200 变成 2000,
// 差额 1800 以**追扣**落到用户头上,而且零告警:兜底价存在 ⇒ BaseMissing=false
// ⇒ SilentFallback() 为假 ⇒ 不写 admin_info、不 LogWarn。
func TestTaskSettlementUsesThePinnedRatioWhenTheMatrixChangesMidFlight(t *testing.T) {
	truncate(t)
	withGroupRatios(t, `{"pool":2}`, `{"vip":{"pool":0.2}}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":1}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(`{}`) })

	seedUser(t, 94, 1000000)
	// 1000 tokens × modelRatio 1 × 0.2 = 200,与预扣一致。
	task := newTaskWithPinnedRatio(t, 94, "pool", "vip", 0.2, 200)

	// 任务运行期间交叉倍率被删(改名/删除分组的 phase 3)。
	require.NoError(t, ratio_setting.UpdateGroupGroupRatioByJSONString(`{}`))
	require.Equal(t, float64(2), ratio_setting.ResolveGroupRatio("vip", "pool").Ratio,
		"前置条件:此刻现算会拿到兜底的 2")

	RecalculateTaskQuotaByTokens(context.Background(), task, 1000)

	var settled model.Task
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&settled).Error)
	assert.Equal(t, 200, settled.Quota,
		"结算必须沿用提交时刻 pin 下来的 0.2;重新解析会算出 2000 并向用户追扣 1800,且零告警")
}

// TestTaskSettlementLogsTheRatioItActuallyCharged —— 日志里的 group_ratio 必须是
// **这一笔真正乘进金额**的那个值。
//
// 历史行(没有 pin)在倍率表变过之后按新值收费,而 taskBillingOther 写的是
// BillingContext 里那个提交时刻的旧值。两者只在倍率被改过时不同 —— 而那正是
// 唯一需要事后对账的时刻:审计员用 tokens × model_ratio × group_ratio 复算,
// 得到的数与实收差好几倍,却没有任何字段指向这一笔。
func TestTaskSettlementLogsTheRatioItActuallyCharged(t *testing.T) {
	truncate(t)
	withGroupRatios(t, `{"pool":2}`, `{}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":1}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(`{}`) })

	seedUser(t, 95, 10000000)
	// 历史行:BillingContext.GroupRatio 停留在 0.5,而现算是兜底的 2。
	task := newPinnedTask(t, 95, "pool", "vip", 500)

	RecalculateTaskQuotaByTokens(context.Background(), task, 1000)

	var settled model.Task
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&settled).Error)
	require.Equal(t, 2000, settled.Quota, "历史行回落现算 ⇒ 1000 × 1 × 2 = 2000")

	other := taskConsumeOther(t, 95)
	assert.EqualValues(t, 2, other["group_ratio"],
		"日志里的 group_ratio 必须是实际乘进金额的 2,而不是 BillingContext 里那个陈旧的 0.5")
	assert.EqualValues(t, 1, other["model_ratio"],
		"model_ratio 同理:结算路径会再过一次 QyGroupTaskRatio,日志必须记它的结果")
}

// TestPinnedZeroRatioIsNotMistakenForALegacyRow —— 显式配成 0 的免费档不能被
// 当成「历史行」。
//
// GroupRatio 带 omitempty,0 根本不进 JSON,读回来也是 0 —— 「运营配的免费」与
// 「这一行没有这个字段」在结构上完全一样,所以才需要 GroupRatioPinned 这一位。
// 判错的方向是资损:0 被当成缺席就会去现算,免费档的用户在结算时按兜底价被追扣。
func TestPinnedZeroRatioIsNotMistakenForALegacyRow(t *testing.T) {
	truncate(t)
	withGroupRatios(t, `{"pool":2}`, `{}`)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"qy-pin-model":1}`))
	t.Cleanup(func() { _ = ratio_setting.UpdateModelRatioByJSONString(`{}`) })

	seedUser(t, 96, 1000000)
	task := newTaskWithPinnedRatio(t, 96, "pool", "vip", 0, 0)

	var reloaded model.Task
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&reloaded).Error)
	require.NotNil(t, reloaded.PrivateData.BillingContext)
	require.True(t, reloaded.PrivateData.BillingContext.GroupRatioPinned,
		"倍率 0 的行落库后必须仍然带着 pin 这一位,否则结算会把它当成历史行去现算兜底价 2")
	require.Zero(t, reloaded.PrivateData.BillingContext.GroupRatio)

	RecalculateTaskQuotaByTokens(context.Background(), &reloaded, 1000)

	var settled model.Task
	require.NoError(t, model.DB.Where("task_id = ?", task.TaskID).First(&settled).Error)
	assert.Zero(t, settled.Quota, "免费档结算恒为 0;按兜底价现算会变成 2000 的凭空追扣")
}
