package controller

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sanitizeAdvancedCustomRequestError 有两条独立的抹除路径:一条按渠道 key,
// 一条按 requestURL 的 query string。advanced-custom 渠道允许把凭据放进
// URL 参数(`?token=...`),那种凭据**不等于**渠道的 key —— 传输层错误
// (`dial ...: connection refused`、超时、TLS 失败)会把整个 URL 原样带出来,
// 直接回显给前端。
//
// 上游随 2b0efd848 带来的
// TestFetchAdvancedCustomModelsRedactsQueryKeyFromTransportErrors 里,
// query 参数的值与 key 参数传的是**同一个字符串**,于是只靠 key 那条路径
// 就能让断言通过 —— 实测把 query 那个循环整段短路掉,那条测试依然 PASS。
// 这里把两者拆开:query 里的密钥与 key 不同,且 key 为空,
// 只有 query 那条路径真的在跑时才可能变绿。
func TestSanitizeAdvancedCustomRequestErrorRedactsQuerySecretsIndependentOfKey(t *testing.T) {
	const querySecret = "qy-url-token-9f3a"

	cases := []struct {
		name       string
		key        string
		rawMessage string
		requestURL string
		mustRedact []string
	}{
		{
			name:       "secret only in query, no channel key at all",
			key:        "",
			rawMessage: "dial " + querySecret + ": connection refused",
			requestURL: "https://upstream.example/v1/models?token=" + querySecret,
			mustRedact: []string{querySecret},
		},
		{
			name:       "channel key differs from the query secret",
			key:        "sk-channel-key",
			rawMessage: "Get \"https://upstream.example/v1/models?token=" + querySecret + "\": timeout",
			requestURL: "https://upstream.example/v1/models?token=" + querySecret,
			mustRedact: []string{querySecret},
		},
		{
			name:       "percent-encoded secret in the message still gets redacted",
			key:        "",
			rawMessage: "Get \"https://upstream.example/v1/models?token=" + url.QueryEscape("a b+c/d") + "\": timeout",
			requestURL: "https://upstream.example/v1/models?token=" + url.QueryEscape("a b+c/d"),
			mustRedact: []string{url.QueryEscape("a b+c/d")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAdvancedCustomRequestError(
				errors.New(tc.rawMessage), tc.key, tc.requestURL)
			require.Error(t, got)
			for _, secret := range tc.mustRedact {
				assert.NotContains(t, got.Error(), secret,
					"a credential carried in the request URL must never reach the client")
			}
			assert.Contains(t, got.Error(), "[REDACTED]")
		})
	}
}

// 空 query 值不该把整条消息替换成 [REDACTED]:`strings.ReplaceAll(s, "", x)`
// 会在每个字符之间插入 x。这条挡住那个退化。
func TestSanitizeAdvancedCustomRequestErrorLeavesMessageIntactWithoutSecrets(t *testing.T) {
	got := sanitizeAdvancedCustomRequestError(
		errors.New("connection refused"), "",
		"https://upstream.example/v1/models?token=&page=1")
	require.Error(t, got)
	assert.Equal(t, "connection refused", got.Error())
}
