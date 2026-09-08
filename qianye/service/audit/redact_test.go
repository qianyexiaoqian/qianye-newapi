package audit

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redact_test.go —— 请求台账入库前的脱敏。
//
// 这组断言守的是一件事:**凭证不得以任何写法穿过脱敏进入 qy_request_audits**。
// 台账是给管理员随手翻的,它一旦落进明文密码,泄露面就从"数据库里的一张
// 加密表"扩大到"任何能打开审计页的人"。

// 键名归一化是整套脱敏的地基:少了它,子串清单等于在赌全仓统一写 snake_case。
// 这里列的每一种写法在真实代码库里都出现过。
func TestIsSensitiveKey_MatchesAcrossNamingStyles(t *testing.T) {
	sensitive := []string{
		"password", "Password", "new_password", "newPassword", "NEW-PASSWORD",
		"pay_password", "payPassword", "old_pay_password",
		"api_key", "apiKey", "API-KEY", "apiV3Key",
		"secret", "client_secret", "clientSecret",
		"access_token", "accessToken", "refresh_token",
		"private_key", "privateKey",
		"totp", "otp_code", "captcha",
		"authorization", "Cookie", "session",
		"id_card", "idCard", "bank_card", "card_no", "iban", "wallet_address",
		"key", "code", "pin", "cvv", "nonce", "iv", "ciphertext",
		"account", "account_no", "payee_account",
		// 收货信息:商城实物单的地址与联系方式。它在 mall 那一侧是加密落盘、
		// 按单解密、90 天擦除的,台账不能成为同一份数据的明文旁路。
		"address", "shipping_address", "receiverAddress", "consignee",
		"contact", "contact_name", "receiver", "phone", "phoneNumber",
		"mobile", "tel",
	}
	for _, k := range sensitive {
		assert.Truef(t, IsSensitiveKey(k), "键 %q 必须被判定为敏感", k)
	}

	// 误伤同样是缺陷:全部擦成 *** 的台账等于没有台账 ——
	// 排查越权时需要看到"他想改的是哪个用户的哪条规则"。
	safe := []string{
		"user_id", "page", "page_size", "amount", "quota", "group",
		"model_name", "rule_id", "decision", "note", "reason", "label",
		"channel", "enabled", "status", "remark", "keyword",
		"error_code", "country_code", "encoding", "idem_key", "cache_key",
		// mobile / tel 走精确匹配而不是子串,就是为了让这两个不被误伤。
		"is_mobile", "telemetry",
	}
	for _, k := range safe {
		assert.Falsef(t, IsSensitiveKey(k), "键 %q 不该被误判为敏感", k)
	}
}

// 嵌套结构里的凭证同样要擦:真实请求体的密码常常挂在 payee/credentials 之下。
func TestRedactBody_ErasesNestedAndArrayCredentials(t *testing.T) {
	raw := []byte(`{
		"user_id": 42,
		"pay_password": "123456",
		"payee": {"channel":"bank","account_no":"6222020202","label":"工资卡"},
		"items": [{"accessToken":"sk-live-abc"},{"note":"keep me"}]
	}`)
	got := RedactBody(raw, "application/json")

	for _, leaked := range []string{"123456", "6222020202", "sk-live-abc"} {
		assert.NotContainsf(t, got, leaked, "脱敏结果里泄露了 %q", leaked)
	}
	// 非敏感字段必须留下,否则台账失去追责价值。
	assert.Contains(t, got, "42")
	assert.Contains(t, got, "工资卡")
	assert.Contains(t, got, "keep me")
	assert.Contains(t, got, redactedPlaceholder)

	// 结果必须仍是合法 JSON:审计页要按 JSON 渲染它。
	var back map[string]any
	require.NoError(t, common.Unmarshal([]byte(got), &back))
	assert.Equal(t, redactedPlaceholder, back["pay_password"])
}

// 商城实物单的下单请求体:地址与联系方式必须擦掉,而 product_no 与
// client_request_id 必须留下 —— 这条路由刻意不走 credentialBodyRoutes 的整体丢弃,
// 因为那两个字段是排查"下单失败 / 重复下单"唯一有用的线索。
func TestRedactBody_ErasesShippingPIIButKeepsOrderTrace(t *testing.T) {
	raw := []byte(`{
		"product_no": "P-20260906-0001",
		"client_request_id": "cri-8f21",
		"address": "XX省YY市ZZ区某小区3栋201",
		"contact": "13800138000"
	}`)
	got := RedactBody(raw, "application/json")

	for _, leaked := range []string{"XX省YY市ZZ区某小区3栋201", "13800138000"} {
		assert.NotContainsf(t, got, leaked, "台账里泄露了收货信息 %q", leaked)
	}
	assert.Contains(t, got, "P-20260906-0001", "商品编号是排障线索,不该被擦")
	assert.Contains(t, got, "cri-8f21", "幂等键是排障线索,不该被擦")
}

// 非 JSON 一律不入库。表单里 pay_password=123456 与 page=2 在字节层面长得一样,
// 没有键级结构可依,任何"文本兜底脱敏"都只是给人已经脱敏了的错觉。
func TestRedactBody_NeverStoresNonJSONBodies(t *testing.T) {
	form := []byte("pay_password=123456&user_id=42")
	got := RedactBody(form, "application/x-www-form-urlencoded")
	assert.NotContains(t, got, "123456")
	assert.NotContains(t, got, "pay_password=")
	assert.Contains(t, got, "non-json body omitted")

	// 声称是 JSON 但解析不了(可能是被截断的 JSON,也可能是伪装的二进制)
	// 同样不能原样入库。
	broken := RedactBody([]byte(`{"pay_password":"123456"`), "application/json")
	assert.NotContains(t, broken, "123456")
	assert.Contains(t, broken, "unparsable")
}

// 超过捕获上限只留占位:raw 已被中间件截断,把半截 JSON 存进去既解析不了,
// 也可能把一个恰好落在密钥中段的片段留下来。
func TestRedactBody_OversizeBodyIsNotStored(t *testing.T) {
	oversize := make([]byte, BodyCaptureLimit+1)
	for i := range oversize {
		oversize[i] = 'a'
	}
	got := RedactBody(oversize, "application/json")
	assert.Contains(t, got, "exceeds "+strconv.Itoa(BodyCaptureLimit))
	assert.Less(t, len(got), 128, "占位说明不该把原文带进来")
}

// 入库上限必须落在 rune 边界上:切点切开一个汉字会造出非法 UTF-8 尾巴,
// 而扩展库 DSN 强制 utf8mb4,MySQL 严格模式会以 1366 拒绝**整行** ——
// 丢的不是备注的尾巴,而是整条台账。
func TestRedactBody_TruncationStaysOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("备注", bodyStoreLimit) // 远超入库上限,且每字符 3 字节
	body, err := common.Marshal(map[string]string{"remark": long})
	require.NoError(t, err)

	got := RedactBody(body, "application/json")
	assert.LessOrEqual(t, len(got), bodyStoreLimit)
	assert.True(t, utf8.ValidString(got),
		"截断结果必须是合法 UTF-8,否则整行会被 utf8mb4 列拒绝")
}

func TestRedactQuery_ErasesSensitiveValuesAndKeepsFilters(t *testing.T) {
	got := RedactQuery("p=1&page_size=20&access_token=sk-live-abc&user_id=42")
	assert.NotContains(t, got, "sk-live-abc")
	assert.Contains(t, got, "access_token="+redactedPlaceholder)
	assert.Contains(t, got, "p=1")
	assert.Contains(t, got, "user_id=42")

	// 没有 = 的裸片段无法判定归属(可能是标志位,也可能是被编码的凭证),
	// 整段丢弃而不是原样保留。
	assert.NotContains(t, RedactQuery("sk-live-abc"), "sk-live-abc")
	assert.Empty(t, RedactQuery(""))
}
