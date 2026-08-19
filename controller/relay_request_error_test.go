package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
)

// 请求校验失败必须是 400,不能是 500。
//
// 本仓所有计费边界闸门(max_tokens 的 maxTokensLimit、图片 n 的 dto.MaxImageN)
// 都是在 GetAndValidateRequest 里拒绝的,而 types.NewError 的默认状态码是 500 ——
// 实测 max_tokens=2147483647、n=129 一律返回 500 {code:invalid_request}。钱没扣错
// (零扣费、无日志行),坏的是对外语义:客户端与上层网关把 500 当服务端故障重试,
// 一个写死了非法参数的客户端会变成无限重试流量,监控上也分不开"参数错"和
// "服务器炸了"。AGENTS.md 的计费安全不变量对这些闸门的要求原话就是 400。
func TestRequestValidationErrorIs400ExceptForOversizedBodies(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"max_tokens 越界", errors.New("max_tokens is invalid"), http.StatusBadRequest},
		{"图片 n 越界", errors.New("n must be an integer between 1 and 128"), http.StatusBadRequest},
		{"JSON 解不开", errors.New("json: cannot unmarshal number -1 into Go value of type uint"), http.StatusBadRequest},
		{"请求体过大单独走 413", common.ErrRequestBodyTooLarge, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := requestValidationError(tc.err)
			assert.Equal(t, tc.want, apiErr.StatusCode)
		})
	}
}
