package commission

import "strings"

// maskRef 脱敏订单号一类的外部引用,只保留后 4 位。
// 下线的订单号属于下线的隐私,邀请人只需要"能和客服对上号"的程度。
//
// 用户名的脱敏与下线对外标识都在 invite(MaskUsername / invitee_ref),
// 本包的流水页直接读关系快照上算好的脱敏名,不再自己算一遍。
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
