package violation

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBlockErrorSurvivesUpstreamWrapping 固化本模块唯一的跨模块契约。
//
// 挂载点只有一行:PreRelayGuard 的返回值被塞进上游已有的 `err` 分支,
// 由 types.NewError(err, ErrorCodeModelPriceError, ...) 包一层再交给
// relay.go 的 defer 序列化。这条路径成立的全部前提是 types.NewError 内部的
// errors.As 会原样保留已经是 *NewAPIError 的错误。
//
// 一旦上游改掉这个行为,拦截错误会退化成 "model_price_error" 并丢掉 skip-retry
// (违规请求会被换渠道重试),而编译不会报任何错。所以必须有这条断言。
func TestBlockErrorSurvivesUpstreamWrapping(t *testing.T) {
	blocked := types.NewErrorWithStatusCode(
		errors.New(defaultBlockMessage),
		types.ErrorCode(violationErrorCode(7)),
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
		types.ErrOptionWithNoRecordErrorLog(),
	)

	wrapped := types.NewError(error(blocked), types.ErrorCodeModelPriceError,
		types.ErrOptionWithStatusCode(http.StatusBadRequest))

	require.NotNil(t, wrapped)
	assert.Equal(t, types.ErrorCode("qy_violation.7"), wrapped.GetErrorCode(),
		"上游包装后错误码必须仍是违规规则的码,而不是 model_price_error")
	assert.Equal(t, http.StatusBadRequest, wrapped.StatusCode)
	assert.Equal(t, defaultBlockMessage, wrapped.Error())
	assert.True(t, types.IsSkipRetryError(wrapped),
		"违规拦截必须跳过重试,否则同一段违规 prompt 会被发给每一个候选渠道")
	assert.False(t, types.IsRecordErrorLog(wrapped),
		"违规拒绝不是渠道故障,不该计入渠道错误统计")
}

// TestPreRelayGuardIsFailOpen 保证挂载点在任何异常输入下都不改变主流程行为。
//
// 扩展是附加物,relay 是主业务:上游已经失败时必须原样透传,
// 未启用或参数缺失时必须放行,绝不能在这里制造新的失败模式。
func TestPreRelayGuardIsFailOpen(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  shadow_mode: false\n  precheck_enabled: true\n")

	t.Run("上游错误原样透传", func(t *testing.T) {
		upstream := errors.New("model price error")
		assert.Same(t, upstream, PreRelayGuard(nil, nil, nil, upstream))
	})

	t.Run("参数缺失时放行", func(t *testing.T) {
		assert.NoError(t, PreRelayGuard(nil, nil, nil, nil))
	})

	t.Run("功能关闭时放行", func(t *testing.T) {
		useTestConfig(t, "  enabled: false\n")
		assert.NoError(t, PreRelayGuard(nil, nil, nil, nil))
	})
}
