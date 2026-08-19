package ali

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metadata 能越过 validateTaskDurationBounds 直接改写 Parameters.Duration。
// 这一组用例钉住的是「计费用的那个数与发给上游的那个数必须是同一个」。
func TestConvertToAliRequestKeepsDurationInBillableRange(t *testing.T) {
	adaptor := &TaskAdaptor{}
	baseReq := func(metadata map[string]any) relaycommon.TaskSubmitReq {
		return relaycommon.TaskSubmitReq{
			Model:    "wan2.2-t2v-plus",
			Prompt:   "a cat",
			Size:     "1920*1080",
			Metadata: metadata,
		}
	}

	t.Run("metadata 把时长打成 0 时必须回落成默认 5 秒", func(t *testing.T) {
		aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), baseReq(map[string]any{
			"parameters": map[string]any{"duration": 0},
		}))

		require.NoError(t, err)
		// 计费侧:seconds 乘数 = 5,而不是被 AddOtherRatio 当非法值丢弃后的隐含 1。
		assert.Equal(t, 5, aliReq.Parameters.Duration)

		// 转发侧:duration 带 omitempty,0 会让整个字段从报文里消失,上游按自己的
		// 默认时长出片。回落成 5 之后字段必须仍在报文里。
		body, err := common.Marshal(aliReq)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"duration":5`)
	})

	t.Run("metadata 把时长打成负数时同样回落", func(t *testing.T) {
		aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), baseReq(map[string]any{
			"parameters": map[string]any{"duration": -1},
		}))

		require.NoError(t, err)
		assert.Equal(t, 5, aliReq.Parameters.Duration)
	})

	t.Run("metadata 把时长推过计费上界时必须拒绝", func(t *testing.T) {
		_, err := adaptor.convertToAliRequest(testRelayInfo(), baseReq(map[string]any{
			"parameters": map[string]any{"duration": relaycommon.MaxTaskDurationSeconds + 1},
		}))

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid duration")
	})

	t.Run("上界之内的时长照常放行", func(t *testing.T) {
		aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), baseReq(map[string]any{
			"parameters": map[string]any{"duration": 10},
		}))

		require.NoError(t, err)
		assert.Equal(t, 10, aliReq.Parameters.Duration)
	})
}

// EstimateBilling 是 seconds 乘数的唯一来源,它读的就是 convertToAliRequest 的产物。
func TestEstimateBillingSecondsRatioFollowsTheForwardedDuration(t *testing.T) {
	aliReq, err := (&TaskAdaptor{}).convertToAliRequest(testRelayInfo(), relaycommon.TaskSubmitReq{
		Model:    "wan2.2-t2v-plus",
		Prompt:   "a cat",
		Size:     "1920*1080",
		Metadata: map[string]any{"parameters": map[string]any{"duration": 0}},
	})
	require.NoError(t, err)

	ratios, err := ProcessAliOtherRatios(aliReq)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"resolution-1080P": 0.7 / 0.14}, ratios)
	// seconds 乘数在 EstimateBilling 里就是 float64(Duration);Duration 回落成 5
	// 之后它是 5,而不是被静默丢弃的 0。
	assert.Equal(t, 5, aliReq.Parameters.Duration)
}

// sizeToResolution 认不出来的 size 之前会让 ProcessAliOtherRatios 整段报错,
// EstimateBilling 连 seconds 一起丢掉乘数,而那个 size 照样原样转发。
func TestConvertToAliRequestRejectsSizesBillingCannotClassify(t *testing.T) {
	adaptor := &TaskAdaptor{}

	t.Run("表外尺寸必须拒绝", func(t *testing.T) {
		_, err := adaptor.convertToAliRequest(testRelayInfo(), relaycommon.TaskSubmitReq{
			Model:  "wan2.2-t2v-plus",
			Prompt: "a cat",
			Size:   "1920*1081",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid size")
	})

	t.Run("metadata 换成表外尺寸同样拒绝", func(t *testing.T) {
		_, err := adaptor.convertToAliRequest(testRelayInfo(), relaycommon.TaskSubmitReq{
			Model:    "wan2.2-t2v-plus",
			Prompt:   "a cat",
			Size:     "1920*1080",
			Metadata: map[string]any{"parameters": map[string]any{"size": "1920*1081"}},
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid size")
	})

	t.Run("表内尺寸照常放行", func(t *testing.T) {
		aliReq, err := adaptor.convertToAliRequest(testRelayInfo(), relaycommon.TaskSubmitReq{
			Model:  "wan2.2-t2v-plus",
			Prompt: "a cat",
			Size:   "1920*1080",
		})

		require.NoError(t, err)
		assert.Equal(t, "1920*1080", aliReq.Parameters.Size)
	})
}
