package commission

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// MaskUsername 把用户名/邮箱脱敏为可展示形式。
//
// 邀请人有权知道"谁是我的下线",但无权拿到对方的真实身份。返回值是列表页
// 唯一会出现的人名形态,真实用户名、邮箱、user_id 一律不下发。
//
// 必须按 rune 处理:按 byte 切会把一个中文切成半个,输出乱码。
func MaskUsername(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "***"
	}
	// 邮箱形态:本地部分按普通规则脱敏,域名只留顶级域。
	// 保留 TLD 是为了让用户能大致辨认"是不是我认识的那个人",
	// 同时不足以反查出具体邮箱。
	if at := strings.LastIndex(s, "@"); at > 0 && at < len(s)-1 {
		local, domain := s[:at], s[at+1:]
		tld := domain
		if dot := strings.LastIndex(domain, "."); dot >= 0 && dot < len(domain)-1 {
			tld = domain[dot+1:]
		}
		return maskCore(local) + "@***." + tld
	}
	return maskCore(s)
}

// maskCore 是脱敏的核心规则。
//
// 短名字(1~2 字符,中文昵称的常见形态)不能只遮中间 —— 那样等于没遮,
// 所以统一只保留首字符。
func maskCore(s string) string {
	r := []rune(s)
	switch n := len(r); {
	case n == 0:
		return "***"
	case n <= 2:
		// "王" → "王**";"张三" → "张**"
		return string(r[0]) + "**"
	case n <= 4:
		// "abcd" → "a**d"
		return string(r[0]) + "**" + string(r[n-1])
	default:
		// "zhangsan" → "zh***an"
		return string(r[:2]) + "***" + string(r[n-2:])
	}
}

// maskRef 脱敏订单号一类的外部引用,只保留后 4 位。
// 下线的订单号属于下线的隐私,邀请人只需要"能和客服对上号"的程度。
func maskRef(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return ""
	}
	if len(r) <= 4 {
		return "****"
	}
	return "****" + string(r[len(r)-4:])
}

// inviteeRef 生成下线的对外标识。
//
// 用 HMAC 而不是裸 sha256:裸哈希的输入空间是连续的小整数,任何人都能在
// 毫秒内枚举出 ref → user_id 的完整映射表。salt 由部署自动生成并持久化,
// 见 settings.go 的 refSalt()。
func inviteeRef(userId int, salt string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte("qy-invitee|" + strconv.Itoa(userId)))
	return hex.EncodeToString(mac.Sum(nil))[:12]
}
