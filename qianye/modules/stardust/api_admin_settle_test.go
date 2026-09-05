package stardust

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api_admin_settle_test.go —— 结算状态与重跑两条管理端路由(契约 §3):形状、拒绝、审计留痕。

func TestAdminSettleStatusAndRerunEndpoints(t *testing.T) {
	env := newTaskEnv(t, nil)
	ctx := context.Background()
	r := adminRouter(7, common.RoleAdminUser)
	now := common.GetTimestamp()
	runDate, day, _ := settleTargetDay(now, 0)

	data := dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/settle/status", ""))
	assert.Nil(t, data["last_run"])
	assert.Equal(t, day, data["target_day"])
	assert.Equal(t, true, data["ready"])
	assert.EqualValues(t, invite.DayStart(now), data["next_settle_at"])

	// 非法日键与尚未封口的今天都被拒,且各留一条失败审计。
	rec := call(t, r, http.MethodPost, "/api/qy/admin/stardust/settle/rerun", `{"day":"2026-09-03"}`)
	assert.Equal(t, codeBadRequest, codeOf(t, rec, http.StatusBadRequest))
	rec = call(t, r, http.MethodPost, "/api/qy/admin/stardust/settle/rerun", `{"day":"`+runDate+`"}`)
	assert.Equal(t, codeBadRequest, codeOf(t, rec, http.StatusBadRequest))

	seedMainUser(t, env.main, model.User{Id: 401})
	start, ok := invite.DayKeyStart(day)
	require.True(t, ok)
	require.NoError(t, env.main.Create(&[]model.Log{consumeLog(401, start+10, 1_000_000, "")}).Error)
	data = dataOf(t, call(t, r, http.MethodPost, "/api/qy/admin/stardust/settle/rerun", `{"day":"`+day+`"}`))
	assert.Equal(t, day, data["day"])
	assert.EqualValues(t, 1, data["recomputed"])
	assert.EqualValues(t, 1, data["settled"])
	assert.EqualValues(t, 0, data["held"])
	assert.EqualValues(t, 0, data["skipped"])
	assert.EqualValues(t, 2, balanceOf(t, env.ext, 401).Available)

	logs := auditRowsOf(t, env.ext, "stardust.settle.rerun")
	okCount, failCount := countResults(logs)
	assert.Equal(t, 1, okCount)
	assert.Equal(t, 2, failCount)
	assert.EqualValues(t, 2, logs[len(logs)-1].AmountQuota, "成功那条带上实际发出的星屑数")

	runSettle(ctx)
	data = dataOf(t, call(t, r, http.MethodGet, "/api/qy/admin/stardust/settle/status", ""))
	last, ok := data["last_run"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, runDate, last["run_date"])
	assert.Equal(t, SettleRunDone, last["status"])
	assert.EqualValues(t, invite.NextDayStart(now), data["next_settle_at"], "今天已 done 就指向明天")
}
