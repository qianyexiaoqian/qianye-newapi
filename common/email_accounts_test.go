package common

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withAccounts 装一份账号表与发件模式，测试结束后原样还原。
//
// 这些都是包级全局状态，不还原的话同包内其它测试会读到上一条用例留下的账号表 ——
// 而 email_test.go 里那批老用例正是靠"账号表是空的"才走进 legacy 分支的。
func withAccounts(t *testing.T, jsonStr string, mode string, fixedID string) {
	t.Helper()
	prevProvider := SMTPAccountsProvider
	prevMode, prevFixed := SMTPSendMode, SMTPFixedAccountID
	prevUsage := SMTPHourlyUsage
	t.Cleanup(func() {
		SMTPAccountsProvider = prevProvider
		SMTPSendMode, SMTPFixedAccountID = prevMode, prevFixed
		SMTPHourlyUsage = prevUsage
	})

	var accounts []SMTPAccountConfig
	require.NoError(t, UnmarshalJsonStr(jsonStr, &accounts))
	SMTPAccountsProvider = func() []SMTPAccountConfig { return accounts }
	SMTPSendMode, SMTPFixedAccountID = mode, fixedID
}

const twoAccounts = `[
  {"id":"a","name":"A","enabled":true,"server":"smtp.a.com","port":587,"account":"a@a.com","token":"t"},
  {"id":"b","name":"B","enabled":true,"server":"smtp.b.com","port":587,"account":"b@b.com","token":"t"}
]`

// TestLegacySMTPAccountMapsOldGlobals 钉住老单账号配置 → 账号的换算。
//
// 它只服务**一次性迁移**(model.MigrateLegacySMTPAccount),发件路径已经不再
// 回落到它。换算错了的表现是升级当天迁进来一条连不上的账号,而运营在新界面上
// 看到的字段全是对的 —— 因为错的是没显示的那几个。
func TestLegacySMTPAccountMapsOldGlobals(t *testing.T) {
	withSMTPSettings(t)
	SMTPServer = "smtp.legacy.com"
	SMTPPort = 465
	SMTPAccount = "legacy@x.com"
	SMTPToken = "tok"
	SMTPFrom = ""
	SMTPSSLEnabled = true

	got := LegacySMTPAccount()
	assert.Equal(t, "legacy", got.ID)
	assert.Equal(t, "smtp.legacy.com", got.Server)
	assert.Equal(t, 465, got.Port)
	assert.Equal(t, "tok", got.Token)
	assert.True(t, got.SSLEnabled)
	// From 为空时回落 Account —— 与旧代码那句 `if SMTPFrom == "" {...}` 同义，
	// 但不写回全局变量。
	assert.Equal(t, "legacy@x.com", got.FromAddress())
	assert.Equal(t, "", SMTPFrom, "解析发件地址不得写回全局配置")
}

// TestResolveErrorsWhenNoAccounts 账号表为空 → 报错,**不再**回落老全局变量。
//
// 留着回落的话,运营把最后一个账号停用之后,系统会悄悄改用一套他以为早就
// 废弃的凭据继续发信。
func TestResolveErrorsWhenNoAccounts(t *testing.T) {
	// 顺序要紧:withSMTPSettings 会装一个「由老全局变量现算」的 provider
	// (那批协议用例要靠它),所以清空账号表的 withAccounts 必须排在它之后,
	// 否则这条用例测到的是 legacy provider 而不是空账号表。
	withSMTPSettings(t)
	SMTPServer = "smtp.legacy.com"
	SMTPAccount = "legacy@x.com"
	SMTPToken = "tok"
	withAccounts(t, `[]`, SMTPSendModeSequential, "")

	_, err := ResolveSMTPAccount()
	require.Error(t, err, "账号表为空必须报错,而不是改用老配置继续发")
}

// TestResolveSequentialRotates 依次模式必须轮着来。
//
// 它不轮转的表现就是这个功能整体失效：账号配了一排，量仍然全压在第一个上，
// 而那正是要规避的「单账号小时发送过多」。
func TestResolveSequentialRotates(t *testing.T) {
	withAccounts(t, twoAccounts, SMTPSendModeSequential, "")

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		got, err := ResolveSMTPAccount()
		require.NoError(t, err)
		seen[got.ID]++
	}
	assert.Equal(t, 3, seen["a"])
	assert.Equal(t, 3, seen["b"])
}

// TestResolveFixedUsesConfiguredAccount 固定模式就用指定的那个。
func TestResolveFixedUsesConfiguredAccount(t *testing.T) {
	withAccounts(t, twoAccounts, SMTPSendModeFixed, "b")

	for i := 0; i < 3; i++ {
		got, err := ResolveSMTPAccount()
		require.NoError(t, err)
		assert.Equal(t, "b", got.ID)
	}
}

// TestResolveFixedFallsBackWhenTargetGone 固定的那个被停用/删掉时回落，而不是发不出去。
func TestResolveFixedFallsBackWhenTargetGone(t *testing.T) {
	withAccounts(t, twoAccounts, SMTPSendModeFixed, "missing")

	got, err := ResolveSMTPAccount()
	require.NoError(t, err)
	assert.Equal(t, "a", got.ID)
}

// TestResolveFixedIgnoresHourlyLimit 固定模式不参与小时上限的跳过。
//
// 明确指定了还被换掉，发件人地址就与运营的预期不一致 —— 而「固定」这个模式
// 存在的唯一理由就是发件人地址稳定。
func TestResolveFixedIgnoresHourlyLimit(t *testing.T) {
	withAccounts(t, `[
      {"id":"a","enabled":true,"server":"s","account":"a@a.com","hourly_limit":1}
    ]`, SMTPSendModeFixed, "a")
	SMTPHourlyUsage = func(string) int { return 9999 }

	got, err := ResolveSMTPAccount()
	require.NoError(t, err)
	assert.Equal(t, "a", got.ID)
}

// TestResolveSkipsDisabledAndInvalid 停用的、配置不全的都不参与。
func TestResolveSkipsDisabledAndInvalid(t *testing.T) {
	withAccounts(t, `[
      {"id":"off","enabled":false,"server":"s","account":"x@x.com"},
      {"id":"empty","enabled":true},
      {"id":"ok","enabled":true,"server":"s","account":"ok@x.com"}
    ]`, SMTPSendModeSequential, "")

	for i := 0; i < 4; i++ {
		got, err := ResolveSMTPAccount()
		require.NoError(t, err)
		assert.Equal(t, "ok", got.ID)
	}
}

// TestResolveErrorsWhenAllAccountsUnusable 账号表非空但全都不可用 → 报错。
//
// 与「表为空」刻意分开：表为空是"还没配"，该走 legacy；全被停用是"配了但都关着"，
// 那时候回落 legacy 会用上一套运营以为已经停用的凭据发信。
func TestResolveErrorsWhenAllAccountsUnusable(t *testing.T) {
	withAccounts(t, `[{"id":"off","enabled":false,"server":"s","account":"x@x.com"}]`,
		SMTPSendModeSequential, "")

	_, err := ResolveSMTPAccount()
	require.Error(t, err)
}

// TestResolveSkipsAccountsOverHourlyLimit 触顶的账号被跳过。
func TestResolveSkipsAccountsOverHourlyLimit(t *testing.T) {
	withAccounts(t, `[
      {"id":"full","enabled":true,"server":"s","account":"f@x.com","hourly_limit":10},
      {"id":"free","enabled":true,"server":"s","account":"r@x.com","hourly_limit":10}
    ]`, SMTPSendModeSequential, "")
	SMTPHourlyUsage = func(id string) int {
		if id == "full" {
			return 10
		}
		return 0
	}

	for i := 0; i < 4; i++ {
		got, err := ResolveSMTPAccount()
		require.NoError(t, err)
		assert.Equal(t, "free", got.ID, "触顶的账号不该被选中")
	}
}

// TestResolveStillSendsWhenAllOverLimit 全部触顶时照发，不报错。
//
// 拒发会让用户收不到验证码 —— 一个确定的、当场可见的故障；
// 超额发送的代价只是概率性进垃圾箱。这是一条显式决定，不是遗漏。
func TestResolveStillSendsWhenAllOverLimit(t *testing.T) {
	withAccounts(t, `[
      {"id":"a","enabled":true,"server":"s","account":"a@x.com","hourly_limit":1},
      {"id":"b","enabled":true,"server":"s","account":"b@x.com","hourly_limit":1}
    ]`, SMTPSendModeSequential, "")
	SMTPHourlyUsage = func(string) int { return 100 }

	got, err := ResolveSMTPAccount()
	require.NoError(t, err, "全部触顶时必须照发，而不是让验证码发不出去")
	assert.Contains(t, []string{"a", "b"}, got.ID)
}

// TestResolveHourlyLimitZeroMeansUnlimited 0 = 不限，不是"一封都不许发"。
func TestResolveHourlyLimitZeroMeansUnlimited(t *testing.T) {
	withAccounts(t, `[{"id":"a","enabled":true,"server":"s","account":"a@x.com","hourly_limit":0}]`,
		SMTPSendModeSequential, "")
	SMTPHourlyUsage = func(string) int { return 100000 }

	got, err := ResolveSMTPAccount()
	require.NoError(t, err)
	assert.Equal(t, "a", got.ID)
}

// TestResolveRandomStaysWithinEligible 随机模式只在候选集内挑。
func TestResolveRandomStaysWithinEligible(t *testing.T) {
	withAccounts(t, `[
      {"id":"a","enabled":true,"server":"s","account":"a@x.com"},
      {"id":"off","enabled":false,"server":"s","account":"o@x.com"}
    ]`, SMTPSendModeRandom, "")

	for i := 0; i < 20; i++ {
		got, err := ResolveSMTPAccount()
		require.NoError(t, err)
		assert.Equal(t, "a", got.ID)
	}
}

func TestValidateSMTPSendMode(t *testing.T) {
	for _, mode := range []string{SMTPSendModeFixed, SMTPSendModeRandom, SMTPSendModeSequential} {
		require.NoError(t, ValidateSMTPSendMode(mode))
	}
	require.Error(t, ValidateSMTPSendMode("roundrobin"), "拼错的模式必须被拒，不能静默退回依次")
	require.Error(t, ValidateSMTPSendMode(""))
}
