package model

import (
	"net"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
)

// allow_ips 是密钥泄漏之后用户唯一的自助止损手段,它的解析有两条判据:
//
//  1. **分隔符**:界面提示"一行一个",但逗号是所有人的第一直觉。旧实现把逗号
//     **删掉**而不是当分隔符,于是 `127.0.0.1,10.0.0.1` 被拼成
//     `127.0.0.110.0.0.1` 这一条垃圾条目 —— 用户的 key 在 relay 与两个只读接口
//     上一律 403,而后台没有任何一条日志说得清为什么(IsIpInCIDRList 对解析
//     不出来的条目静默 continue)。方向是失败关闭,但那是一次无法自查的失效。
//
//  2. **失败方向**:调用方原先的判据是 `len(GetIpLimits()) > 0`,把"没声明"
//     与"声明了但一条都解析不出来"当成同一件事。而后者(例如 allow_ips 填成
//     `, `)的正确处置是**一条都不许通过** —— 白名单里一个地址都没有,含义是
//     "没有任何来源被允许"。旧实现在这一档上是 fail-open:一把看起来已经绑死
//     IP 的令牌实际不限制任何来源,而库里那一列还是非空的、前端也照样显示成
//     一条规则。
func TestTokenIpLimitParsingAndFailDirection(t *testing.T) {
	ptr := func(s string) *string { return &s }

	cases := []struct {
		name string
		// allow 为 nil 表示这一列没有声明过。
		allow     *string
		wantLimit bool     // HasIpLimit
		wantList  []string // GetIpLimits
	}{
		{"没声明", nil, false, []string{}},
		{"空串", ptr(""), false, []string{}},
		{"只有空白", ptr("  \n  "), false, []string{}},
		{"单条", ptr("127.0.0.1"), true, []string{"127.0.0.1"}},
		{"换行分隔", ptr("127.0.0.1\n10.0.0.1"), true, []string{"127.0.0.1", "10.0.0.1"}},
		{"逗号分隔(第一直觉写法)", ptr("127.0.0.1,10.0.0.1"), true, []string{"127.0.0.1", "10.0.0.1"}},
		{"逗号加空格", ptr("127.0.0.1, 10.0.0.1"), true, []string{"127.0.0.1", "10.0.0.1"}},
		{"两种分隔符混用", ptr("127.0.0.1,10.0.0.1\n192.168.0.0/24"), true,
			[]string{"127.0.0.1", "10.0.0.1", "192.168.0.0/24"}},
		{"CRLF", ptr("127.0.0.1\r\n10.0.0.1"), true, []string{"127.0.0.1", "10.0.0.1"}},
		{"CIDR", ptr("203.0.113.0/24"), true, []string{"203.0.113.0/24"}},
		{"IPv6", ptr("2001:db8::1"), true, []string{"2001:db8::1"}},
		// 关键的一档:声明了、但一条都解析不出来。
		{"只有标点", ptr(", "), true, []string{}},
		{"只有逗号", ptr(","), true, []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok := &Token{AllowIps: tc.allow}
			assert.Equal(t, tc.wantLimit, tok.HasIpLimit(),
				"HasIpLimit 回答的是『声明过吗』,不是『解析出几条』")
			assert.Equal(t, tc.wantList, tok.GetIpLimits())
		})
	}

	t.Run("只有标点的白名单必须谁都不放行,不是谁都放行", func(t *testing.T) {
		tok := &Token{AllowIps: ptr(", ")}
		// 调用方的完整判据:HasIpLimit() 为真 → 进入检查 → 空清单 → 一律拒绝。
		if assert.True(t, tok.HasIpLimit()) {
			limits := tok.GetIpLimits()
			assert.Empty(t, limits)
			for _, probe := range []string{"127.0.0.1", "203.0.113.5", "10.0.0.1"} {
				assert.False(t,
					common.IsIpInCIDRList(net.ParseIP(probe), limits),
					"%s 不该通过一个一条地址都没有的白名单", probe)
			}
		}
	})

	t.Run("逗号分隔的白名单必须真的按两条生效", func(t *testing.T) {
		tok := &Token{AllowIps: ptr("203.0.113.5,10.0.0.1")}
		limits := tok.GetIpLimits()
		assert.True(t, common.IsIpInCIDRList(net.ParseIP("203.0.113.5"), limits))
		assert.True(t, common.IsIpInCIDRList(net.ParseIP("10.0.0.1"), limits))
		assert.False(t, common.IsIpInCIDRList(net.ParseIP("198.51.100.7"), limits),
			"白名单之外的地址仍然必须被拒")
	})
}
