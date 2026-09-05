package lottery

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 标题与说明不进承诺原像,发布之后仍可改;结算 / 结束之后不能改(证据链已按旧名公示)。
func TestActivityBasicsEditableAfterPublish(t *testing.T) {
	env := newWheelEnv(t, nil)
	actNo := env.publishWheel(t)
	env.user = wheelAdminId

	code, body := env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/basics",
		`{"title":"  周末星屑转盘  ","intro":"每人每天三次"}`)
	require.Equalf(t, http.StatusOK, code, "已发布活动改名应当成功: %s", body)
	act, err := loadActivityAny(context.Background(), env.ext, actNo)
	require.NoError(t, err)
	assert.Equal(t, "周末星屑转盘", act.Title, "标题两端空白被裁掉")
	assert.Equal(t, "每人每天三次", act.Intro)
	assert.Equal(t, StatusPublished, act.Status, "改名不动状态")

	var events []Event
	require.NoError(t, env.ext.Where("act_id = ? AND action = ?", act.Id, ActionBasicsChanged).Find(&events).Error)
	assert.Len(t, events, 1, "每次改名留一行事件")

	code, body = env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/basics", `{"title":"   ","intro":""}`)
	assert.Equalf(t, http.StatusBadRequest, code, "空标题应当 400: %s", body)

	// 提前结束(cancelWheel → 无转动则作废 → finished)之后不能再改名。
	code, body = env.call(t, http.MethodPost, "/admin/lottery/activities/"+actNo+"/cancel", `{"reason":"测试收口"}`)
	require.Equalf(t, http.StatusOK, code, "取消失败: %s", body)
	code, body = env.call(t, http.MethodPut, "/admin/lottery/activities/"+actNo+"/basics", `{"title":"改不了","intro":""}`)
	assert.Equalf(t, http.StatusConflict, code, "结束后的活动改名应当 409: %s", body)
	assert.Contains(t, string(body), "qy_lot_basics_locked")
}
