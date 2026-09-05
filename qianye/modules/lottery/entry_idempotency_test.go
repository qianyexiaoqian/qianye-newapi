package lottery

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entry_idempotency_test.go —— 参与入口的幂等契约与四个时刻的绝对上界。

// 同一个 client_request_id 的重试必须算出同一个指纹。
//
// entry_no 由 newEntryNo() 每次请求现生成,它一旦进指纹,这条幂等键就在结构上
// 永远命中不了:用户超时重试会拿到 409「本次提交与此前同一请求标识的内容不一致」,
// 合理地以为第一次没成功,换个 crid 再投一注 —— 而幂等键存在的全部意义就是
// 拦住这一注。api_user.go 要求前端"打开弹窗时生成、重试沿用同一个",
// 这条断言就是那份契约。
func TestEntryFingerprintIsStableAcrossRetries(t *testing.T) {
	act := &Activity{ActNo: "LT20260101-0123456789abcdef", Kind: KindDraw, DrawMode: DrawModeBall}
	first := entryFingerprint(act, 4242, 1000, 2, "01,02,03|01")
	second := entryFingerprint(act, 4242, 1000, 2, "01,02,03|01")
	assert.Equal(t, first, second,
		"同一个 client_request_id 的重试算出了不同指纹 —— 幂等键在结构上永远命中不了")
	assert.Len(t, first, 64, "指纹落在 varchar(64) 里,必须恰好是一个 sha256 的十六进制")

	// 换参重放仍然必须被识破:指纹收的是"用户这次请求说了什么"。
	changed := map[string]string{
		"换选项": entryFingerprint(act, 4242, 1000, 3, "01,02,03|01"),
		"换号码": entryFingerprint(act, 4242, 1000, 2, "01,02,04|01"),
		"换金额": entryFingerprint(act, 4242, 9000, 2, "01,02,03|01"),
		"换用户": entryFingerprint(act, 4243, 1000, 2, "01,02,03|01"),
	}
	for name, fp := range changed {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, first, fp, "换参重放必须算出不同指纹")
		})
	}

	t.Run("换活动", func(t *testing.T) {
		other := &Activity{ActNo: "LT20260101-fedcba9876543210", Kind: KindDraw, DrawMode: DrawModeBall}
		assert.NotEqual(t, first, entryFingerprint(other, 4242, 1000, 2, "01,02,03|01"),
			"活动维度由 act_no 承载:同一个 crid 在两场活动上是两笔不同的参与")
	})
}

// 四个时刻必须有绝对上界。
//
// 没有它:① 竞猜把 settle_deadline 填成 2100 年,就原样恢复了"管理员无限期
// 扣着奖池不结算"这个 settle_deadline 本身要防的风险;② 抽奖连逾期兜底都没有,
// close_at 在 2200 年的活动会正常收钱然后永远停在 published;
// ③ close_at 接近 int64 上界时 `DrawAt < CloseAt + RevealDelaySeconds` 静默溢出
// 成负数,那条揭示间隔校验恒真。
func TestValidateScheduleRejectsUnboundedSchedules(t *testing.T) {
	newPayoutEnv(t, config.Lottery{
		Enabled: true, EntryCloseGraceSeconds: 60, RevealDelaySeconds: 60,
		MaxStakeStardust: 5_000_000,
	})
	now := common.GetTimestamp()
	const day = int64(86400)

	cases := []struct {
		name    string
		act     Activity
		wantErr bool
	}{
		{"正常的一天", Activity{Kind: KindDraw,
			OpenAt: now, CloseAt: now + day, DrawAt: now + day + 600}, false},
		{"一年内", Activity{Kind: KindDraw,
			OpenAt: now, CloseAt: now + 300*day, DrawAt: now + 300*day + 600}, false},
		{"截止时间超出地平线", Activity{Kind: KindDraw,
			OpenAt: now, CloseAt: now + 400*day, DrawAt: now + 400*day + 600}, true},
		{"结算截止超出地平线", Activity{Kind: KindGuess,
			OpenAt: now, CloseAt: now + day, DrawAt: now + day + 600,
			SettleDeadline: 4102444800 + 400*day}, true},
		{"close_at 取 int64 上界(溢出旁路)", Activity{Kind: KindDraw,
			OpenAt: now, CloseAt: 9223372036854775807, DrawAt: now + 600}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSchedule(&tc.act, now)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
