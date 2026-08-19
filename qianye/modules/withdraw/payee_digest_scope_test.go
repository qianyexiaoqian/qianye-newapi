package withdraw

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// payee_digest_scope_test.go —— 风控指纹的口径必须是"钱去哪",不是"表单填了什么"。
//
// # 被测的缺陷(端到端实测过)
//
// 指纹原像此前是整条收款信息(含自由文本 real_name / bank_name / 可选 branch,
// 且 email 不折大小写)。于是同一张银行卡只要在支行栏里多打一个字符,指纹就
// 完全不同,shared_payee 永不触发 —— 提现路径上唯一的跨账户刷单信号,一个字符
// 即可关闭。实测:三个账号同一张卡 6225880137624156,只有 branch 多一个 "x"
// 的那个账号零报警,而字段逐字相同的那个按预期标红。
//
// 更要命的是它不需要攻击者知道这回事:三个小号各自手填一遍卡信息,只要有人把
// "招商银行"写成"招商银行股份有限公司"、名字里多一个空格,报警就自动消失。
// 漏报是常态而不是例外。
//
// # 判据
//
// 同一个收款账号的不同写法必须摘出同一个指纹;换一个收款账号必须换指纹。
// 判据打在 payeeDigest 的返回值与 qy_withdrawals.risk_flags 上,不打在原像串上。

const (
	digestTestKey  = "digest-scope-secret"
	digestTestCard = "6225880137624156"
	digestTestUSDT = "TQmXyZ9aBcDeFgHiJkLmNoPqRsTuVwXyZ1"
)

func digestOf(t *testing.T, channel string, data map[string]string) string {
	t.Helper()
	d, err := payeeDigest(channel, data)
	require.NoError(t, err)
	require.NotEmpty(t, d)
	return d
}

// 同一张卡的不同写法 → 同一个指纹;换一张卡 → 换指纹。
func TestPayeeDigest_TracksTheAccountNotTheWholeForm(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, digestTestKey)

	for _, tc := range []struct {
		name    string
		channel string
		base    map[string]string
		other   map[string]string
		same    bool
	}{
		{
			name:    "银行卡:可选支行栏多一个字符仍是同一张卡",
			channel: ChannelBank,
			base:    map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard},
			other:   map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard, "branch": "x"},
			same:    true,
		},
		{
			name:    "银行卡:行名写全称仍是同一张卡",
			channel: ChannelBank,
			base:    map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard},
			other:   map[string]string{"real_name": "张三", "bank_name": "招商银行股份有限公司", "account_no": digestTestCard},
			same:    true,
		},
		{
			name:    "银行卡:户名写法不同仍是同一张卡",
			channel: ChannelBank,
			base:    map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard},
			other:   map[string]string{"real_name": "张 三", "bank_name": "招商银行", "account_no": digestTestCard},
			same:    true,
		},
		{
			name:    "银行卡:卡号按四位分组书写仍是同一张卡",
			channel: ChannelBank,
			base:    map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard},
			other:   map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": "6225 8801 3762 4156"},
			same:    true,
		},
		{
			name:    "银行卡:卡号用连字符分组仍是同一张卡",
			channel: ChannelBank,
			base:    map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard},
			other:   map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": "6225-8801-3762-4156"},
			same:    true,
		},
		{
			name:    "银行卡:换一个卡号就是换一个收款目的地",
			channel: ChannelBank,
			base:    map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard},
			other:   map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": "6225880137624157"},
			same:    false,
		},
		{
			name:    "paypal:信箱大小写不同仍是同一个信箱",
			channel: ChannelPaypal,
			base:    map[string]string{"email": "mule@example.com"},
			other:   map[string]string{"email": "Mule@Example.COM"},
			same:    true,
		},
		{
			name:    "paypal:换一个信箱就是换一个收款目的地",
			channel: ChannelPaypal,
			base:    map[string]string{"email": "mule@example.com"},
			other:   map[string]string{"email": "mule2@example.com"},
			same:    false,
		},
		{
			name:    "支付宝:户名加空格中点不改变收款账号",
			channel: ChannelAlipay,
			base:    map[string]string{"real_name": "张三", "account": "13800138000"},
			other:   map[string]string{"real_name": "张·三 ", "account": "13800138000"},
			same:    true,
		},
		{
			name:    "支付宝:账号中间的空格不改变收款账号",
			channel: ChannelAlipay,
			base:    map[string]string{"real_name": "张三", "account": "13800138000"},
			other:   map[string]string{"real_name": "张三", "account": "138 0013 8000"},
			same:    true,
		},
		{
			name:    "微信:换一个账号就是换一个收款目的地",
			channel: ChannelWechat,
			base:    map[string]string{"real_name": "张三", "account": "13800138000"},
			other:   map[string]string{"real_name": "张三", "account": "13800138001"},
			same:    false,
		},
		{
			name:    "USDT:地址里混进空白不改变地址",
			channel: ChannelUSDT,
			base:    map[string]string{"address": digestTestUSDT},
			other:   map[string]string{"address": " TQmXyZ9aBcDeFgHiJk LmNoPqRsTuVwXyZ1 "},
			same:    true,
		},
		{
			name:    "USDT:Base58 大小写是地址的一部分,折叠会把两个钱包并成一个",
			channel: ChannelUSDT,
			base:    map[string]string{"address": digestTestUSDT},
			other:   map[string]string{"address": "tQmXyZ9aBcDeFgHiJkLmNoPqRsTuVwXyZ1"},
			same:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := digestOf(t, tc.channel, tc.base)
			other := digestOf(t, tc.channel, tc.other)
			if tc.same {
				assert.Equal(t, base, other, "同一个收款账号必须摘出同一个指纹")
				return
			}
			assert.NotEqual(t, base, other, "不同的收款账号必须摘出不同的指纹")
		})
	}
}

// 同一串账号出现在不同渠道时不能撞成同一个指纹:支付宝的 13800138000 与
// 微信的 13800138000 是两个收款目的地,合并会凭空标红两个不相干的人。
func TestPayeeDigest_ChannelStaysInThePreimage(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, digestTestKey)

	alipay := digestOf(t, ChannelAlipay, map[string]string{"real_name": "张三", "account": "13800138000"})
	wechat := digestOf(t, ChannelWechat, map[string]string{"real_name": "张三", "account": "13800138000"})
	assert.NotEqual(t, alipay, wechat)
}

// 认不出账号字段时必须回落到整条记录的口径,**绝不能**摘出同一个空指纹。
//
// 这一条守的不是漏报而是误判:一批指纹相同的单会互相标红,更要命的是
// ensureReplayMatches 用指纹判断"重放的这一笔钱去的还是不是原来那张卡",
// 空指纹互相相等意味着换了收款人的重放会被当成同一笔,钱照着原来那张卡打出去。
func TestPayeeDigest_FallsBackToTheWholeRecordWhenTheAccountIsUnknown(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, digestTestKey)

	// 渠道不在 payeeAccountKey 里(将来新增渠道忘了登记的形态)。
	unknownA := digestOf(t, "sepa", map[string]string{"iban": "DE0000000000000001"})
	unknownB := digestOf(t, "sepa", map[string]string{"iban": "DE0000000000000002"})
	assert.NotEqual(t, unknownA, unknownB, "认不出账号字段时仍要靠整条记录区分收款目的地")

	// 账号字段缺失(脏数据 / 旧规格快照)。
	noAccountA := digestOf(t, ChannelBank, map[string]string{"real_name": "张三", "bank_name": "招商银行"})
	noAccountB := digestOf(t, ChannelBank, map[string]string{"real_name": "李四", "bank_name": "招商银行"})
	assert.NotEqual(t, noAccountA, noAccountB)

	// 回落口径本身仍要稳定:同一条记录两次算出同一个指纹。
	assert.Equal(t, noAccountA, digestOf(t, ChannelBank,
		map[string]string{"bank_name": "招商银行", "real_name": "张三"}))
}

// 端到端:两个人用同一张卡、只在支行/行名写法上不同,风控标记必须照样亮。
//
// 这是整条链路的判据 —— payeeDigest 归一 → 落单 → markSharedPayee 跨用户计数
// → risk_flags。旧口径下第二张单的指纹与第一张不同,两张单都干干净净地进了
// 管理端队列,而 risk_only=true 的过滤会把它们一起藏起来。
func TestSharedPayeeFiresWhenTheSameCardIsWrittenDifferently(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, digestTestKey)
	gdb := newTestDB(t)

	plain := map[string]string{"real_name": "张三", "bank_name": "招商银行", "account_no": digestTestCard}
	// 同一张卡,第二个人多填了支行、行名写全称、卡号按四位分组。
	dressedUp := map[string]string{
		"real_name": "张 三", "bank_name": "招商银行股份有限公司",
		"account_no": "6225 8801 3762 4156", "branch": "x",
	}

	first := submitFiatWith(t, gdb, 9201, digestOf(t, ChannelBank, plain), "idem-dg-a")
	assert.Empty(t, first.RiskFlags, "只有一个人用这张卡时不该标红")

	second := submitFiatWith(t, gdb, 9202, digestOf(t, ChannelBank, dressedUp), "idem-dg-b")
	assert.Equal(t, RiskSharedPayee, second.RiskFlags,
		"同一张卡换个写法就没有报警,等于把提现侧唯一的跨账号信号关掉")
	assert.Equal(t, RiskSharedPayee, riskFlagsOf(t, gdb, first.WithdrawNo),
		"最早那张单必须被回补 —— 它正是队列里排最前、最先被打款的那张")

	// 反向:真的换一张卡不能被并进来,否则报警变成噪音。
	otherCard := map[string]string{"real_name": "王五", "bank_name": "招商银行", "account_no": "6225880137624157"}
	third := submitFiatWith(t, gdb, 9203, digestOf(t, ChannelBank, otherCard), "idem-dg-c")
	assert.Empty(t, third.RiskFlags, "不同的卡不该被标红")
}
