package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetOptionsWithholdsSecretsWithoutASensitiveSuffix 钉死设置页读取接口的脱敏面。
//
// 这条脱敏原本只有一条后缀启发式(Token / Secret / Key / …),而**聚合键**撑不起后缀:
// SMTPAccounts 的值是一整个发件账号数组,每个元素都带明文密码,键名却结尾是个 "s",
// 于是整块 JSON 连同密码一起下发给了设置页。options 表是全站唯一"任何键都能落进来、
// 随后原样进 OptionMap"的表,所以这道闸不能只靠"把库里那行删掉"。
//
// 同时断言普通键仍然照常下发 —— 否则一条"什么都不发"的实现也能让这个测试变绿。
func TestGetOptionsWithholdsSecretsWithoutASensitiveSuffix(t *testing.T) {
	const smtpAccountsSecret = "smtp-account-secret-placeholder"
	const legacyTokenSecret = "legacy-token-secret-placeholder"
	previousMap := common.OptionMap
	common.OptionMap = map[string]string{
		"SMTPAccounts": `[{"id":"smtp_a","name":"a","server":"smtp.example.com","port":587,` +
			`"account":"no-reply@example.com","token":"` + smtpAccountsSecret + `"}]`,
		"SMTPToken":  legacyTokenSecret,
		"SystemName": "new-api",
	}
	t.Cleanup(func() { common.OptionMap = previousMap })

	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)

	GetOptions(context)

	require.Equal(t, http.StatusOK, response.Code)
	assert.NotContains(t, response.Body.String(), smtpAccountsSecret,
		"SMTPAccounts 整块下发等于把全部发件凭据的明文交给设置页")
	assert.NotContains(t, response.Body.String(), legacyTokenSecret)

	var payload struct {
		Success bool `json:"success"`
		Data    []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	require.True(t, payload.Success)

	delivered := make(map[string]string, len(payload.Data))
	for _, option := range payload.Data {
		delivered[option.Key] = option.Value
	}
	assert.NotContains(t, delivered, "SMTPAccounts")
	assert.NotContains(t, delivered, "SMTPToken")
	assert.Equal(t, "new-api", delivered["SystemName"], "普通配置项必须照常下发")
}
