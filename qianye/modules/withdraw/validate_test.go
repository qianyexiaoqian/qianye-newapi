package withdraw

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testWithdrawCfg() config.Withdraw {
	return config.Withdraw{
		Enabled:        true,
		Methods:        []string{config.WithdrawMethodQuota, config.WithdrawMethodFiat},
		MinQuota:       500000,
		RemarkMaxRunes: 200,
	}
}

// 需求写的是"200 字",不是 200 字节。用 len() 的话一个汉字占 3 字节,
// 用户写 67 个汉字就会被拒 —— 这是最容易被漏掉的坑,必须按 rune 计。
func TestCheckRunes_CountsCharactersNotBytes(t *testing.T) {
	cases := []struct {
		name  string
		input string
		ok    bool
	}{
		{"200 个汉字恰好通过", strings.Repeat("字", 200), true},
		{"201 个汉字被拒", strings.Repeat("字", 201), false},
		{"200 个 4 字节 emoji 恰好通过", strings.Repeat("😀", 200), true},
		{"201 个 emoji 被拒", strings.Repeat("😀", 201), false},
		{"200 个 ASCII 通过", strings.Repeat("a", 200), true},
		{"中英混排按字符计", strings.Repeat("中a", 100), true},
		{"中英混排超一个字符即拒", strings.Repeat("中a", 100) + "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkRunes(tc.input, 200)
			if !tc.ok {
				assert.ErrorIs(t, err, errRemarkTooLong)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.input, got)
		})
	}
}

// 字节数上界剪枝(len > max*4)不能把合法输入误杀:200 个 4 字节 emoji
// 恰好是 800 字节,正好落在边界上。
func TestCheckRunes_BytePrecheckDoesNotRejectMaxEmoji(t *testing.T) {
	s := strings.Repeat("😀", 200)
	require.Equal(t, 800, len(s))
	_, err := checkRunes(s, 200)
	assert.NoError(t, err)
}

func TestCheckRunes_TrimsSurroundingSpace(t *testing.T) {
	got, err := checkRunes("  说明  ", 200)
	require.NoError(t, err)
	assert.Equal(t, "说明", got)
}

func TestAcceptCreate_Rejections(t *testing.T) {
	cfg := testWithdrawCfg()
	valid := createRequest{
		ClientRequestId: "11111111-2222-3333",
		Method:          config.WithdrawMethodQuota,
		Quota:           500000,
	}

	cases := []struct {
		name  string
		mutar func(*createRequest)
		want  error
	}{
		{"幂等键过短", func(r *createRequest) { r.ClientRequestId = "abc" }, errIdemKeyRequired},
		{"幂等键过长", func(r *createRequest) { r.ClientRequestId = strings.Repeat("x", 65) }, errIdemKeyRequired},
		{"未开放的方式", func(r *createRequest) { r.Method = "crypto" }, errMethodNotAllowed},
		{"额度为 0", func(r *createRequest) { r.Quota = 0 }, errAmountOutOfRange},
		{"额度为负", func(r *createRequest) { r.Quota = -1 }, errAmountOutOfRange},
		{"额度超过主库 int32 上限", func(r *createRequest) { r.Quota = int64(common.MaxQuota) + 1 }, errAmountOutOfRange},
		{"低于最低提现额", func(r *createRequest) { r.Quota = 499999 }, errAmountTooSmall},
		{"说明超长", func(r *createRequest) { r.Remark = strings.Repeat("字", 201) }, errRemarkTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutar(&req)
			_, err := acceptCreate(req, cfg)
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestAcceptCreate_FiatRequiresPayee(t *testing.T) {
	cfg := testWithdrawCfg()
	req := createRequest{
		ClientRequestId: "11111111-2222-3333",
		Method:          config.WithdrawMethodFiat,
		Quota:           500000,
	}
	_, err := acceptCreate(req, cfg)
	assert.ErrorIs(t, err, errPayeeInvalid)

	// 引用已保存的收款方式时不必再传明文字段。
	req.PayeeRef = "abc123"
	acc, err := acceptCreate(req, cfg)
	require.NoError(t, err)
	assert.Equal(t, "abc123", acc.PayeeRef)
}

func TestAcceptPayee(t *testing.T) {
	t.Run("丢弃规格之外的字段", func(t *testing.T) {
		// 多传一个字段就多一份 PII 被加密留存,宽容在这里是有代价的。
		_, data, err := acceptPayee(ChannelAlipay, map[string]string{
			"real_name": "张三", "account": "13800138000", "id_card": "440000199001011234",
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"real_name": "张三", "account": "13800138000"}, data)
	})

	t.Run("必填字段缺失被拒", func(t *testing.T) {
		_, _, err := acceptPayee(ChannelBank, map[string]string{"real_name": "张三"})
		assert.ErrorIs(t, err, errPayeeRequired)
	})

	t.Run("可选字段可缺省", func(t *testing.T) {
		_, data, err := acceptPayee(ChannelBank, map[string]string{
			"real_name": "张三", "bank_name": "招商银行", "account_no": "6214830112345678",
		})
		require.NoError(t, err)
		assert.NotContains(t, data, "branch")
	})

	t.Run("长度越界被拒", func(t *testing.T) {
		_, _, err := acceptPayee(ChannelUSDT, map[string]string{"address": "TXshort"})
		assert.ErrorIs(t, err, errPayeeInvalid)
	})

	t.Run("控制字符被拒", func(t *testing.T) {
		// 0x1e/0x1f 是指纹规范化的分隔符,放进字段值会制造指纹碰撞。
		_, _, err := acceptPayee(ChannelAlipay, map[string]string{
			"real_name": "张三", "account": "1380013\x1f8000",
		})
		assert.ErrorIs(t, err, errPayeeInvalid)
	})

	t.Run("未知渠道被拒", func(t *testing.T) {
		_, _, err := acceptPayee("bitcoin", map[string]string{"address": "xxx"})
		assert.ErrorIs(t, err, errPayeeInvalid)
	})
}

// 渠道白名单与字段规格必须一一对应:少一条规格意味着该渠道的收款信息
// 会被整体丢弃,前端却看不出任何异常。
func TestSupportedChannelsAllHaveSpecs(t *testing.T) {
	for _, ch := range SupportedChannels() {
		assert.Contains(t, payeeSpecs, ch)
	}
	assert.Len(t, payeeSpecs, len(SupportedChannels()))
}
