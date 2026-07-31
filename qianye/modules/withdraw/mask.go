package withdraw

import (
	"strings"
	"unicode/utf8"
)

// 收款信息脱敏。
//
// 脱敏结果在写入时一次生成并明文存储:列表页、导出、管理端队列全部只读它,
// 永远不解密。把脱敏放到读取时做,等于每渲染一页列表就解密一批银行卡号。
//
// 全部按 rune 切片。按 byte 切会把一个中文姓名劈成半个字符,输出乱码 ——
// 而且乱码里可能恰好保留了完整的一个字节,脱敏效果无从保证。

// maskPayee 按渠道生成脱敏展示串。
//
// 刻意不带渠道中文名(如"支付宝"):渠道标识单独下发,由前端做 i18n。
// 把中文写进库里,换语言就要洗数据。
func maskPayee(channel string, data map[string]string) string {
	switch channel {
	case ChannelAlipay, ChannelWechat:
		return joinMask(maskAccount(data["account"]), maskName(data["real_name"]))
	case ChannelBank:
		bank := strings.TrimSpace(data["bank_name"])
		return joinMask(bank+" "+maskTail(data["account_no"], 4), maskName(data["real_name"]))
	case ChannelUSDT:
		return maskAddress(data["address"])
	case ChannelPaypal:
		return maskEmail(data["email"])
	default:
		return ""
	}
}

func joinMask(primary, secondary string) string {
	primary = strings.TrimSpace(primary)
	secondary = strings.TrimSpace(secondary)
	if secondary == "" {
		return primary
	}
	if primary == "" {
		return secondary
	}
	return primary + " / " + secondary
}

// maskAccount 脱敏手机号一类的账号:保留前 3 后 4。
func maskAccount(s string) string {
	r := []rune(strings.TrimSpace(s))
	switch n := len(r); {
	case n == 0:
		return ""
	case n <= 4:
		// 太短的账号无论怎么留都等于没脱敏,整体遮掉。
		return strings.Repeat("*", n)
	case n <= 8:
		return "****" + string(r[n-4:])
	default:
		return string(r[:3]) + "****" + string(r[n-4:])
	}
}

// maskTail 只保留末尾 keep 位,用于银行卡号 —— 前 6 位是发卡行标识,
// 保留它等于把持卡人所在银行和卡种一并暴露。
func maskTail(s string, keep int) string {
	r := []rune(strings.TrimSpace(s))
	n := len(r)
	if n == 0 {
		return ""
	}
	if n <= keep {
		return strings.Repeat("*", n)
	}
	return "****" + string(r[n-keep:])
}

// maskName 保留姓氏,其余按字符数补星号。
// 补足原长度而不是固定两颗星:固定长度会抹掉"两字名还是三字名"这个核对线索,
// 管理员反而更难与用户实名信息比对。
func maskName(s string) string {
	r := []rune(strings.TrimSpace(s))
	switch n := len(r); {
	case n == 0:
		return ""
	case n == 1:
		return "*"
	default:
		return string(r[0]) + strings.Repeat("*", n-1)
	}
}

// maskEmail 只保留本地部分首字符与完整域名。
func maskEmail(s string) string {
	s = strings.TrimSpace(s)
	at := strings.LastIndex(s, "@")
	if at <= 0 || at >= len(s)-1 {
		return maskAccount(s)
	}
	local := []rune(s[:at])
	return string(local[0]) + "***" + s[at:]
}

// maskAddress 脱敏链上地址:保留前 6 后 6。
// 链上地址本身是公开的,但把它与平台账号关联起来就构成身份信息,仍需遮蔽。
func maskAddress(s string) string {
	r := []rune(strings.TrimSpace(s))
	n := len(r)
	if n == 0 {
		return ""
	}
	if n <= 12 {
		return strings.Repeat("*", n)
	}
	return string(r[:6]) + "..." + string(r[n-6:])
}

// truncate 按字符数截断,口径与 MySQL utf8mb4 的 varchar(N) 一致 ——
// 那个 N 计的是字符而不是字节。
//
// 按字节切有两个问题:会留下半个中文字符让 MySQL 整条报错,而且会把一个
// varchar(64) 的中文用户名砍到 21 个字。
func truncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes])
}
