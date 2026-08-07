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
