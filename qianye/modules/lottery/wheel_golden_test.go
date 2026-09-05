package lottery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wheel_golden_test.go —— 转盘票面与链原像编码的**跨实现**黄金向量。
//
// 下面每一个值都由 qianye/docs/lottery-verify.py 的 wheel_ticket / wheel_pick
// 独立算出(纯标准库 hmac/hashlib,与 Go 侧没有共享一行代码),再逐字节抄进来;
// web 侧 verify-wheel.test.ts 钉的是同一组数字。三份实现在同一组输入上逐位一致,
// 这个文件锁的就是那一致性 —— 任何一份改了编码(域前缀、分隔符、dec 编码、
// 密钥取字节还是取文本),这里都会立刻变红,而不是悄悄换掉一套所有人都在用的协议。

const wheelGoldenSeed = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

func TestWheelTicketMatchesTheVerifyScriptGoldenVectors(t *testing.T) {
	cases := []struct {
		seq        int
		clientSeed string
		ticket     string
		ppm        uint32
	}{
		{7, "abc-XYZ_09", "da6c1c477d8450e535ab2ac86afaf7a6104228a450165ef31acc799beec9289c", 853212},
		// 空 client_seed 按空分量进原像:它仍然占着最后那一位。
		{1, "", "bd8210ffc88f7b286e70cd2cb58bdad77a539874654eca8d356835ed6cae9960", 740265},
	}
	for _, tc := range cases {
		got, err := WheelTicket(wheelGoldenSeed, "LOTTESTACT01", tc.seq, tc.clientSeed)
		require.NoError(t, err)
		assert.Equal(t, tc.ticket, got, "seq=%d client_seed=%q", tc.seq, tc.clientSeed)
		assert.Equal(t, tc.ppm, RollPpm(got))
	}

	// 密钥取的是种子的**字节**(hex 解码):解不开必须报错,而不是回落成把文本当
	// 密钥 —— 那会静默算出另一套结果,而 verify.py / verify.ts 在同一输入上是抛错的。
	_, err := WheelTicket("not-hex", "LOTTESTACT01", 1, "")
	assert.Error(t, err)
	_, err = WheelTicket("", "LOTTESTACT01", 1, "")
	assert.Error(t, err)
}

func TestWheelPickEncodingMatchesTheVerifyScript(t *testing.T) {
	assert.Equal(t, "w|2|384217|0|abc-XYZ_09", WheelPick(2, 384217, 0, "abc-XYZ_09"))
	assert.Equal(t, "w|0|5|3|", WheelPick(0, 5, 3, ""))
}

// 转盘的域前缀与批次玩法的票面前缀必须不同:两者的密钥不是同一个东西
// (final_seed vs. 种子本身),同一个前缀会让两种密钥下的原像互相可重放。
func TestWheelDomainIsSeparatedFromTheBatchTicket(t *testing.T) {
	assert.NotEqual(t, domainTicket, domainWheel)
	// 同一份种子、同一个活动、同一个"编号"下,转盘票面与批次票面不该撞成同一串。
	wheel, err := WheelTicket(wheelGoldenSeed, "LOTTESTACT01", 1, "")
	require.NoError(t, err)
	assert.NotEqual(t, Ticket(wheelGoldenSeed, "LOTTESTACT01", "1"), wheel)
}

// client_seed 的字符集与幂等键同一档收紧;"|" 是 WheelPick 的分隔符,尤其不能进。
func TestValidClientSeedRejectsTheWheelPickSeparator(t *testing.T) {
	for _, ok := range []string{"", "a", "abc-XYZ_09", "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_-"} {
		assert.Truef(t, validClientSeed(ok), "%q 应合法", ok)
	}
	for _, bad := range []string{"a|b", "a b", " a", "a\n", "é", "w|1|2|3|x",
		"0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_-x"} {
		assert.Falsef(t, validClientSeed(bad), "%q 应被拒绝", bad)
	}
}
