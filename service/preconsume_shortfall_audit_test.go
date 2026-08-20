package service

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 预扣没兜住真实花费时，消费日志必须留下可审计的标记。
//
// 这是预扣估算模型的固有敞口，而它的上界不受余额闸门约束：客户端省略
// max_tokens（OpenAI 协议里它本来就是可选的）时，预扣按
// defaultPreConsumeMaxTokens=8192 兜底估输出侧，结算按上游真实
// completion_tokens 无条件扣款 —— 当代模型输出上限普遍 32k~128k，一次完全正常
// 的长输出就能把钱包扣成负数，并发只是线性放大。余额闸门此时只限制并发路数，
// 不再限制总金额。
//
// 结构性地关上这个口要么截断用户输出、要么改预扣模型，都是产品决策；
// 在那之前至少要让它可审计：按 admin_info.pre_consume_shortfall 能把所有
// “预扣没兜住”的笔捞出来算放大倍数与坏账。
func TestPreConsumeShortfallLandsInAdminInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)

	t.Run("兜住了就不写标记", func(t *testing.T) {
		info := &relaycommon.RelayInfo{}
		other := map[string]interface{}{}
		attachPreConsumeShortfall(ctx, info, other)
		assert.NotContains(t, other, "admin_info", "正常笔不该留噪音")
	})

	t.Run("没兜住就写标记,数字逐项落地", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			PreConsumeShortfall: &relaycommon.ReservationShortfall{
				Reserved: 59152, Charged: 194000, Shortfall: 134848,
			},
		}
		other := map[string]interface{}{}
		attachPreConsumeShortfall(ctx, info, other)

		adminInfo, ok := other["admin_info"].(map[string]interface{})
		require.True(t, ok, "标记必须嵌在 admin_info 下 —— 非管理员日志视图会把它整段剥掉")
		marker, ok := adminInfo["pre_consume_shortfall"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, 59152, marker["reserved"])
		assert.Equal(t, 194000, marker["charged"])
		assert.Equal(t, 134848, marker["shortfall"],
			"差额就是余额闸门没能拦住的那一段,统计放大倍数与坏账全靠它")
	})

	t.Run("已有的 admin_info 不被覆盖", func(t *testing.T) {
		info := &relaycommon.RelayInfo{
			PreConsumeShortfall: &relaycommon.ReservationShortfall{Reserved: 10, Charged: 30, Shortfall: 20},
		}
		other := map[string]interface{}{"admin_info": map[string]interface{}{"settle_failed": "boom"}}
		attachPreConsumeShortfall(ctx, info, other)
		adminInfo := other["admin_info"].(map[string]interface{})
		assert.Equal(t, "boom", adminInfo["settle_failed"], "同一块 admin_info 上的别的标记不许被冲掉")
		assert.NotNil(t, adminInfo["pre_consume_shortfall"])
	})
}
