package withdraw

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// 脱敏是"列表页永远不解密"这个设计的前提。它算错的后果分两种:
// 遮多了只是难用,遮少了就是把银行卡号和实名信息直接展示出去。
func TestMaskPayee(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		data    map[string]string
		want    string
	}{
		{
			name:    "支付宝手机号保留前3后4",
			channel: ChannelAlipay,
			data:    map[string]string{"account": "13800138000", "real_name": "张三"},
			want:    "138****8000 / 张*",
		},
		{
			name:    "银行卡只保留后4位",
			channel: ChannelBank,
			data: map[string]string{
				"bank_name": "招商银行", "account_no": "6214830112345678", "real_name": "欧阳修",
			},
			want: "招商银行 ****5678 / 欧**",
		},
		{
			name:    "链上地址保留前6后6",
			channel: ChannelUSDT,
			data:    map[string]string{"address": "TXabcdefghijklmnopqrstuvwxyz012345"},
			want:    "TXabcd...012345",
		},
		{
			name:    "PayPal 邮箱只留首字符",
			channel: ChannelPaypal,
			data:    map[string]string{"email": "zhangsan@gmail.com"},
			want:    "z***@gmail.com",
		},
		{
			name:    "未知渠道不产生任何输出",
			channel: "unknown",
			data:    map[string]string{"account": "13800138000"},
			want:    "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, maskPayee(tc.channel, tc.data))
		})
	}
}

// 中文必须按 rune 切。按字节切会把"张"劈成半个字符,输出乱码 ——
// 而乱码里可能恰好留下一个完整字节,脱敏效果无从保证。
func TestMaskName_RuneSafe(t *testing.T) {
	cases := map[string]string{
		"张":    "*",
		"张三":   "张*",
		"欧阳修":  "欧**",
		"a":    "*",
		"abcd": "a***",
		"":     "",
	}
	for in, want := range cases {
		assert.Equal(t, want, maskName(in), "输入 %q", in)
	}
}

func TestMaskAccount_Boundaries(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"12":          "**",
		"1234":        "****",
		"12345":       "****2345",
		"12345678":    "****5678",
		"123456789":   "123****6789",
		"13800138000": "138****8000",
	}
	for in, want := range cases {
		assert.Equal(t, want, maskAccount(in), "输入 %q", in)
	}
}

func TestMaskAddress_ShortAddressFullyHidden(t *testing.T) {
	// 短到留了前后各 6 位就等于全部露出的地址,只能整体遮掉。
	assert.Equal(t, "************", maskAddress("abcdefghijkl"))
	assert.Equal(t, "abcdef...uvwxyz", maskAddress("abcdefghijklmnopqrstuvwxyz"))
}

// 截断口径必须是字符数,与 MySQL utf8mb4 的 varchar(N) 一致。
// 按字节算的话 varchar(64) 的中文用户名会被砍到 21 个字。
func TestTruncate_CountsRunes(t *testing.T) {
	assert.Equal(t, "张三丰", truncate("张三丰", 5))
	assert.Equal(t, "张三", truncate("张三丰", 2))
	assert.Equal(t, "", truncate("张三丰", 0))
	assert.True(t, strings.HasPrefix("张三丰", truncate("张三丰", 2)))
	// emoji 与汉字一样按 1 个字符计。
	assert.Equal(t, "😀😀", truncate("😀😀😀", 2))
}
