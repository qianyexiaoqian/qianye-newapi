package audit

// redact.go —— 请求台账入库前的脱敏。
//
// # 判据是键名,不是值
//
// 值级探测(正则找"看起来像密钥的字符串")在这类系统里必然失守:支付密码是
// 6 位数字,与页码没有任何形态差别;而 base64 的 IV 与随机 nonce 又会被误伤。
// 因此这里只认键名,并且把键名先归一化 —— private_key / privateKey /
// apiV3Key / api-key 在归一化之后都是同一个词。没有归一化的子串清单会
// 默默假设整个代码库都写 snake_case,而支付渠道的字段名恰恰不是。
//
// # 命中即整体擦除,不做部分保留
//
// 保留首尾(sk-abc****wxyz)对排障几乎没有帮助,却把密钥的搜索空间砍掉两截。
// 请求台账不是用来核对某个 key 对不对的,是用来回答"谁调了什么"。
//
// # 非 JSON 一律不入库
//
// 表单 / multipart 没有键级结构可依:pay_password=123456 与 page=2 在字节层面
// 长得一样。文本兜底脱敏只会给人"已经脱敏了"的错觉,所以直接只留占位说明。

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const (
	// BodyCaptureLimit 是参与脱敏解析的原始请求体上限。
	// 超过它的请求体连读都不读满 —— 中间件按这个上限做 LimitReader,
	// 避免一次导入请求在内存里被完整复制两份。
	BodyCaptureLimit = 256 * 1024
	// bodyStoreLimit 是脱敏后**入库**的上限,刻意远小于捕获上限:
	// 捕获上限保护的是内存,入库上限保护的是这张表的可扫读性与体积。
	bodyStoreLimit = 16 * 1024
	// redactMaxDepth 给递归封顶。恶意构造的深层嵌套 JSON 能让递归吃满栈,
	// 而请求台账绝不该有能力打死进程。
	redactMaxDepth = 24

	redactedPlaceholder = "***"
)

// sensitiveExactKeys 是归一化后精确匹配的敏感键。
//
// 精确匹配用于那些**太短、作为子串会误伤**的名字:key 作为子串会命中
// monkey、keyword;code 会命中 country_code、error_code。
var sensitiveExactKeys = map[string]bool{
	"key":        true,
	"code":       true,
	"codes":      true,
	"pin":        true,
	"cvv":        true,
	"session":    true,
	"cookie":     true,
	"auth":       true,
	"sign":       true,
	"nonce":      true,
	"iv":         true,
	"ciphertext": true,
	// 本扩展自己的字段:支付密码与它的确认/旧值,以及提现收款信息明文。
	"paypassword":    true,
	"paypass":        true,
	"oldpaypassword": true,
	"newpaypassword": true,
	"account":        true,
	"accountno":      true,
	"payeeaccount":   true,
}

// nonSecretKeySuffixes 是**以 key 结尾但不是凭证**的归一化键名。
//
// 存在理由见 IsSensitiveKey 的后缀规则:密钥字段的命名花样太多
// (api_key / apiV3Key / app_secret_key / mch_key),靠枚举子串必然漏。
// 因此判据反过来:以 key 结尾一律视为敏感,这张小表是唯一的豁免口。
// 表里每一项都是"业务标识"而非"凭证",擦掉它们只会损失排障信息。
var nonSecretKeySuffixes = map[string]bool{
	"idemkey":   true, // 幂等键:客户端自选的请求标识,不是凭证
	"cachekey":  true,
	"sortkey":   true,
	"groupkey":  true,
	"rowkey":    true,
	"mapkey":    true,
	"routekey":  true,
	"orderkey":  true,
	"objectkey": true,
}

// sensitiveSubstrings 是归一化后的包含匹配。命中任一即整体擦除该键的值。
var sensitiveSubstrings = []string{
	"password", "passwd", "secret", "token",
	"apikey", "accesskey", "privatekey", "publickey",
	"credential", "otp", "totp", "captcha",
	"authorization", "signature", "serviceaccount", "sessionkey",
	// 收款信息:提现的钱最终去哪。它在 qy_pii_audits 里有独立的、
	// 保留期更长的明文访问审计,绝不能在请求台账里再落一份明文。
	"idcard", "bankcard", "cardno", "iban", "swift", "wallet",
}

// normalizeKey 归一化键名:小写并去掉 _ - . 与空格。
//
// 这一步是整套脱敏的地基。少了它,子串清单等于在赌全仓统一用 snake_case,
// 而 apiV3Key、privateKey 这类写法一个都不会被命中。
func normalizeKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		switch r {
		case '_', '-', '.', ' ':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsSensitiveKey 判断某个 JSON 键的值是否必须擦除。
//
// 导出是为了让守卫测试能直接对着这条判据断言 —— 脱敏清单是会随业务增长的,
// 而"新加的敏感字段忘了进清单"没有任何其他方式能被发现。
// 三条判据依次是:精确表、子串表、**以 key 结尾**。
//
// 第三条是本文件与参照实现最大的分歧点。参照实现靠一张手工维护的精确键名表
// 收住 apiV3Key 这类写法,而那张表来自它自己的支付渠道字段清单 —— 换一个
// 渠道、换一个命名习惯就漏。这里把默认值翻过来:凡是以 key 结尾的字段一律
// 当凭证,除非它在 nonSecretKeySuffixes 里被明确豁免。
// 两个方向的代价不对称 —— 误判一个业务标识只损失一条排障信息,
// 漏判一个密钥则是把它明文写进一张管理员随手能翻的表。
func IsSensitiveKey(key string) bool {
	k := normalizeKey(key)
	if sensitiveExactKeys[k] {
		return true
	}
	for _, sub := range sensitiveSubstrings {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return strings.HasSuffix(k, "key") && !nonSecretKeySuffixes[k]
}

// RedactBody 把原始请求体转成可入库的脱敏文本。
//
// raw 可能已经被中间件按 BodyCaptureLimit 截断,因此"长度恰好等于上限"要按
// 超限处理 —— 中间件多读一个字节正是为了让这里能分辨这两种情况。
func RedactBody(raw []byte, contentType string) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw) > BodyCaptureLimit {
		// 不报具体字节数:raw 已被截断,真实体积只会更大,报一个已知偏小的
		// 数字比不报更糟 —— 它会被当成事实用来判断"这次导入传了多少"。
		return "<body omitted: exceeds " + strconv.Itoa(BodyCaptureLimit) + " bytes>"
	}
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return "<non-json body omitted: " + strconv.Itoa(len(raw)) +
			" bytes, content-type=" + Truncate(strings.TrimSpace(contentType), 64) + ">"
	}

	var value any
	if err := common.Unmarshal(raw, &value); err != nil {
		// 声称是 JSON 却解析不了:它可能是被截断的 JSON,也可能是伪装的
		// 二进制。两种情况都不该原样入库。
		return "<unparsable body omitted>"
	}
	encoded, err := common.Marshal(redactValue(value, 0))
	if err != nil {
		return "<redacted>"
	}
	// 用 Truncate 而不是裸切:脱敏结果里有用户填的中文备注,
	// 裸字节切点会造出非法 UTF-8 尾巴,而扩展库 DSN 强制 utf8mb4、
	// MySQL 严格模式下会以 1366 拒绝**整行**(与 AuditLog.Reason 同一个坑)。
	return Truncate(string(encoded), bodyStoreLimit)
}

// redactValue 递归擦除敏感键的值,保留其余结构。
//
// 保留非敏感字段是刻意的:排查越权时需要看到"他试图改的是哪个用户的哪条规则",
// 整体擦成 *** 的台账等于没有台账。
func redactValue(value any, depth int) any {
	if depth > redactMaxDepth {
		return "<depth limit exceeded>"
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			if IsSensitiveKey(k) {
				out[k] = redactedPlaceholder
				continue
			}
			out[k] = redactValue(item, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactValue(item, depth+1)
		}
		return out
	default:
		return value
	}
}

// RedactQuery 按同一套键名判据擦除 URL query 的取值。
//
// 不用 url.ParseQuery:解析失败时它返回部分结果,那会让一个畸形 query
// 的敏感段悄悄漏进台账。这里按 & 和 = 手工切,任何一段解析不出键名都整段丢弃。
func RedactQuery(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		key, _, found := strings.Cut(part, "=")
		if !found {
			// 没有 = 的裸片段既可能是标志位也可能是被 URL 编码的凭证,
			// 无法判定归属就不入库。
			out = append(out, "<malformed>")
			continue
		}
		if IsSensitiveKey(key) {
			out = append(out, key+"="+redactedPlaceholder)
			continue
		}
		out = append(out, part)
	}
	return Truncate(strings.Join(out, "&"), 1024)
}
