package paypass

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// feature_gate_test.go —— 「支付密码能不能设」不许跟着划转走。
//
// # 被测的缺陷:钱被困住
//
// paypass 的用户接口(含首次设置)此前全部门在 guard.FlagTransfer 上,
// 而 withdraw/api_user.go 的验密是无条件的。于是
// transfer.enabled=false + withdraw.enabled=true 这个完全合法的组合下:
//
//	POST /api/qy/withdraw     → 403 qy_pay_pwd_not_set(请先设置支付密码)
//	POST /api/qy/pay-password → 404 qy_feature_off  (该功能未启用)
//
// 两个错误互不提及对方,佣金永久滞留在 available_quota,而管理端提现队列
// 空空如也、看起来一切正常。管理员也救不了:他只能 reset(清空)与 unlock,
// 不能代设。出厂 qianye.example.yaml 正好是 transfer/withdraw 都关,
// 「只开佣金+提现、不开站内互转」是一个再自然不过的产品选择。
//
// 根因是一条过期假设:paypass 起初只服务划转,gate.go 与 design-13 都写着
// 「功能总闸是 transfer.enabled」。提现与抽奖接进来之后没人回头改这个 flag。

// 派生开关的真值表。谁接了验密,谁就要能把这个开关顶起来。
func TestPayPasswordFlagFollowsEveryPathThatRequiresIt(t *testing.T) {
	cases := []struct {
		name                        string
		transfer, withdraw, lottery bool
		want                        bool
	}{
		{"三条路径全关", false, false, false, false},
		{"只开划转", true, false, false, true},
		{"只开提现(缺陷现场:此前恒 false)", false, true, false, true},
		{"只开抽奖(超过门槛的参与同样要验密)", false, false, true, true},
		{"划转关、提现开", false, true, true, true},
		{"全开", true, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := qyConfig.Swap(&config.Config{
				Enabled:  true,
				Transfer: config.Transfer{Enabled: tc.transfer},
				Withdraw: config.Withdraw{Enabled: tc.withdraw},
				Lottery:  config.Lottery{Enabled: tc.lottery},
			})
			t.Cleanup(func() { qyConfig.Store(prev) })

			assert.Equal(t, tc.want, guard.FeatureConfigured(guard.FlagPayPassword))
		})
	}
}

// 端到端:关掉划转、开着提现时,设置/查询/找回三个入口必须仍然可达。
//
// 这条是行为锁,守的是**调用点**。上面那条真值表只证明 guard 算得对 ——
// 只要有人把 api_user.go 的 RequireAPI 参数改回 FlagTransfer,真值表照样全绿,
// 而用户又一次没有地方能把支付密码设上。
func TestUserEndpointsStayReachableWhenOnlyWithdrawIsOn(t *testing.T) {
	gdb := newTestDB(t)
	// newTestDB 装的是 transfer=true 的配置,这里换成缺陷现场那一组。
	prev := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: false},
		Withdraw: config.Withdraw{Enabled: true},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })
	require.NotNil(t, gdb)

	const userId = 7810
	r := newRouter(t, userId)

	status := do(r, http.MethodGet, "/api/qy/pay-password", "")
	assert.Equal(t, http.StatusOK, status.Code,
		"连「设没设过」都查不到,前端连该弹设置框还是输入框都判断不了。响应: %s", status.Body.String())

	set := do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"`+goodPassword+`"}`)
	assert.Equal(t, http.StatusOK, set.Code,
		"划转关掉时首次设置被 404 qy_feature_off 挡下 —— 提现方要求验密,而这里是唯一的设置入口。响应: %s",
		set.Body.String())

	// 设上之后验密立刻可用,提现那一侧才真的走得通。
	assert.NoError(t, verify(context.Background(), userId, goodPassword),
		"密码设上了却验不过,等于这条逃生口只是看起来通了")
}

// 三条路径全关时,这几个接口应当照旧 404:支付密码不是一个独立功能,
// 没有任何路径会要它的时候,不该给用户一个设完也用不上的入口。
func TestUserEndpointsAreOffWhenNoPathRequiresPayPassword(t *testing.T) {
	gdb := newTestDB(t)
	prev := qyConfig.Swap(&config.Config{Enabled: true})
	t.Cleanup(func() { qyConfig.Store(prev) })
	require.NotNil(t, gdb)

	r := newRouter(t, 7811)
	assert.Equal(t, http.StatusNotFound, do(r, http.MethodGet, "/api/qy/pay-password", "").Code)
	assert.Equal(t, http.StatusNotFound,
		do(r, http.MethodPost, "/api/qy/pay-password", `{"password":"`+goodPassword+`"}`).Code)
}
