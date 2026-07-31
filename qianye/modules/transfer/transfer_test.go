package transfer

import (
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseConfig 是一份宽松到不会误伤测试用例的配置,单个用例只覆盖自己关心的字段。
func baseConfig() config.Transfer {
	return config.Transfer{
		Enabled:         true,
		MinQuota:        500000,
		MaxPerTxQuota:   50000000,
		DailyMaxQuota:   200000000,
		DailyMaxCount:   20,
		CooldownSecs:    10,
		RecipientLookup: config.RecipientLookupID,
	}
}

func validRequest() createRequest {
	return createRequest{
		ToUserId:        2,
		Amount:          1000000,
		ClientRequestId: "3f2a9c1e-4b7d-42aa-9f01-0b1c2d3e4f55",
		Confirm:         true,
	}
}

// TestValidateCreateRejectsUnsafeRequests 锁定受理阶段的硬性拒绝条件。
// 这些是本模块最贴近资损的一组不变量:任何一条失守都意味着可以凭空造钱。
func TestValidateCreateRejectsUnsafeRequests(t *testing.T) {
	cfg := baseConfig()

	cases := []struct {
		name    string
		mutate  func(*createRequest)
		from    int
		wantErr *bizError
	}{
		{
			name:    "未确认不可逆提示",
			mutate:  func(r *createRequest) { r.Confirm = false },
			wantErr: errConfirmRequired,
		},
		{
			name:    "转给自己",
			mutate:  func(r *createRequest) { r.ToUserId = 1 },
			wantErr: errSelfTransfer,
		},
		{
			name:    "负数金额",
			mutate:  func(r *createRequest) { r.Amount = -1 },
			wantErr: errAmountOutOfRange,
		},
		{
			name:    "零金额",
			mutate:  func(r *createRequest) { r.Amount = 0 },
			wantErr: errAmountOutOfRange,
		},
		{
			name:    "低于单笔下限",
			mutate:  func(r *createRequest) { r.Amount = cfg.MinQuota - 1 },
			wantErr: errAmountOutOfRange,
		},
		{
			name:    "高于单笔上限",
			mutate:  func(r *createRequest) { r.Amount = cfg.MaxPerTxQuota + 1 },
			wantErr: errAmountOutOfRange,
		},
		{
			name:    "超过主库 int32 容量",
			mutate:  func(r *createRequest) { r.Amount = int64(common.MaxQuota) + 1 },
			wantErr: errAmountOutOfRange,
		},
		{
			name:    "缺少幂等键",
			mutate:  func(r *createRequest) { r.ClientRequestId = "" },
			wantErr: errIdemKeyRequired,
		},
		{
			name:    "收款人 ID 非法",
			mutate:  func(r *createRequest) { r.ToUserId = 0 },
			wantErr: errInvalidParam,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mutate(&req)
			_, err := validateCreate(1, req, cfg)
			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
		})
	}
}

// TestValidateCreateAcceptsAndNormalizes 确认通过受理的请求被正确规范化。
func TestValidateCreateAcceptsAndNormalizes(t *testing.T) {
	cfg := baseConfig()
	cfg.FeeBps = 500 // 5%
	req := validRequest()
	req.Amount = 1000000
	req.Remark = "  转\x00账\n给同事  "

	acc, err := validateCreate(7, req, cfg)
	require.NoError(t, err)
	assert.Equal(t, 7, acc.FromUserId)
	assert.Equal(t, 2, acc.ToUserId)
	assert.Equal(t, int64(1000000), acc.Amount)
	assert.Equal(t, int64(50000), acc.Fee)
	assert.Equal(t, int64(1050000), acc.Total)
	assert.Equal(t, "转账给同事", acc.Remark)
	assert.Equal(t, "7:3f2a9c1e-4b7d-42aa-9f01-0b1c2d3e4f55", acc.IdemKey)
}

// TestValidateCreateRejectsFeeOverflow 覆盖"金额本身合法但加上手续费越界"。
// 上限判断必须在 int64 上做:在 int32 上直接相加会溢出成负数,把扣款变成加款。
func TestValidateCreateRejectsFeeOverflow(t *testing.T) {
	cfg := baseConfig()
	cfg.MinQuota = 1
	cfg.MaxPerTxQuota = int64(common.MaxQuota)
	cfg.FeeBps = 100

	req := validRequest()
	req.Amount = int64(common.MaxQuota)

	_, err := validateCreate(1, req, cfg)
	require.Error(t, err)
	assert.Same(t, errAmountOutOfRange, err)
}

func TestComputeFee(t *testing.T) {
	cases := []struct {
		name        string
		amount      int64
		bps         int
		minFee      int64
		want        int64
		expectError bool
	}{
		{name: "费率为零时不收费", amount: 1000000, bps: 0, minFee: 10000, want: 0},
		{name: "百分之五", amount: 1000000, bps: 500, want: 50000},
		{name: "万分之一向上取整到整数额度", amount: 12345, bps: 1, want: 1},
		{name: "低于下限时抬到下限", amount: 1000, bps: 100, minFee: 5000, want: 5000},
		{name: "接近满额的百分百费率", amount: int64(common.MaxQuota) - 1, bps: 10000, want: int64(common.MaxQuota) - 1},
		// common.saturateQuota 的判定是 value >= MaxQuota,恰好等于上限也算溢出。
		// 这里显式锁住这个边界:静默 clamp 会把手续费悄悄改成另一个数。
		{name: "手续费恰好触到上限即报错", amount: int64(common.MaxQuota), bps: 10000, expectError: true},
		{name: "下限本身越界", amount: 1000, bps: 100, minFee: math.MaxInt64, expectError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fee, err := computeFee(tc.amount, tc.bps, tc.minFee)
			if tc.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, fee)
		})
	}
}

// TestBuildIdemKey 幂等键是防重复扣款的唯一可靠防线,它的构造必须逐字节确定。
func TestBuildIdemKey(t *testing.T) {
	key, err := buildIdemKey(42, "  b1d9f0aa-1111-2222-3333-444455556666  ")
	require.NoError(t, err)
	assert.Equal(t, "42:b1d9f0aa-1111-2222-3333-444455556666", key)

	// 不同用户的同一个 client_request_id 绝不能塌缩成同一个键。
	a, err := buildIdemKey(1, "same-token")
	require.NoError(t, err)
	b, err := buildIdemKey(11, "same-token")
	require.NoError(t, err)
	assert.NotEqual(t, a, b)

	for _, bad := range []string{"", "   ", "with space", "semi;colon", "斜杠/号", "quote'"} {
		_, err := buildIdemKey(1, bad)
		assert.Error(t, err, "应拒绝 %q", bad)
	}

	_, err = buildIdemKey(1, strings.Repeat("a", maxClientRequestIdLen+1))
	assert.Same(t, errInvalidParam, err)

	// 拼出来的键必须放得进 qy_fund_orders.idem_key(varchar(96))。
	long, err := buildIdemKey(math.MaxInt32, strings.Repeat("a", maxClientRequestIdLen))
	require.NoError(t, err)
	assert.LessOrEqual(t, len(long), 96)
}

// TestEvaluateRisk 覆盖限额判定的每一条边界。差一即等于放行一笔超额划转。
func TestEvaluateRisk(t *testing.T) {
	cfg := baseConfig()
	cfg.DailyMaxCount = 3
	cfg.DailyMaxQuota = 1000
	cfg.CooldownSecs = 60
	const now int64 = 1_700_000_000

	cases := []struct {
		name    string
		state   UserState
		total   int64
		wantErr *bizError
	}{
		{name: "首次划转放行", state: UserState{}, total: 1000},
		{name: "刚好用满日额度放行", state: UserState{DayOutQuota: 400}, total: 600},
		{name: "超出日额度一个单位即拒绝", state: UserState{DayOutQuota: 400}, total: 601, wantErr: errDailyLimitExceeded},
		{name: "刚好用满日笔数放行", state: UserState{DayOutCount: 2}, total: 1},
		{name: "超出日笔数即拒绝", state: UserState{DayOutCount: 3}, total: 1, wantErr: errDailyCountExceeded},
		{name: "冷却期内拒绝", state: UserState{LastOutAt: now - 59}, total: 1, wantErr: errCooldown},
		{name: "冷却期满放行", state: UserState{LastOutAt: now - 60}, total: 1},
		{name: "存在未结算划转即拒绝", state: UserState{PendingCount: 1}, total: 1, wantErr: errPendingExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateRisk(tc.state, cfg, tc.total, now)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
		})
	}
}

// TestEvaluateRiskTreatsZeroAsUnlimited 固化"配置 0 = 不限制"的口径。
// 若哪天改成"0 = 禁止",所有沿用默认配置的部署会在升级瞬间全线拒绝划转。
func TestEvaluateRiskTreatsZeroAsUnlimited(t *testing.T) {
	cfg := config.Transfer{}
	state := UserState{DayOutQuota: math.MaxInt32, DayOutCount: 9999, LastOutAt: 1_700_000_000}
	assert.NoError(t, evaluateRisk(state, cfg, math.MaxInt32, 1_700_000_000))
}

// TestReservationRoundTrip 预占与退还必须完全对称,否则失败的划转会永久吃掉用户额度。
func TestReservationRoundTrip(t *testing.T) {
	const now int64 = 1_700_000_000
	bucket := dayBucket(now)

	sender := UserState{UserId: 1, DayBucket: bucket, DayOutQuota: 100, DayOutCount: 1, LifetimeOutQuota: 100, LastOutAt: now - 999}
	receiver := UserState{UserId: 2, DayBucket: bucket, DayInCount: 2, LifetimeInQuota: 500}
	beforeSender, beforeReceiver := sender, receiver

	applyReservation(&sender, &receiver, 1000, 1050, now, bucket)
	assert.Equal(t, int64(1150), sender.DayOutQuota)
	assert.Equal(t, 2, sender.DayOutCount)
	assert.Equal(t, 1, sender.PendingCount)
	assert.Equal(t, now, sender.LastOutAt)
	assert.Equal(t, 3, receiver.DayInCount)
	assert.Equal(t, int64(1500), receiver.LifetimeInQuota)

	undoReservation(&sender, &receiver, 1000, 1050, true, now)
	assert.Equal(t, beforeSender.DayOutQuota, sender.DayOutQuota)
	assert.Equal(t, beforeSender.DayOutCount, sender.DayOutCount)
	assert.Equal(t, beforeSender.LifetimeOutQuota, sender.LifetimeOutQuota)
	assert.Equal(t, 0, sender.PendingCount)
	assert.Equal(t, beforeReceiver.DayInCount, receiver.DayInCount)
	assert.Equal(t, beforeReceiver.LifetimeInQuota, receiver.LifetimeInQuota)
	// LastOutAt 刻意不回滚:上一笔的时刻已被覆盖,无法还原。
	assert.Equal(t, now, sender.LastOutAt)
}

// TestUndoReservationAcrossDayBoundary 跨日退还只能退终身累计。
// 日计数已被 rollDay 清零,再减一次等于凭空放大今天的可用额度。
func TestUndoReservationAcrossDayBoundary(t *testing.T) {
	sender := UserState{UserId: 1, DayOutQuota: 0, DayOutCount: 0, LifetimeOutQuota: 1050, PendingCount: 1}
	receiver := UserState{UserId: 2, DayInCount: 0, LifetimeInQuota: 1000}

	undoReservation(&sender, &receiver, 1000, 1050, false, 1_700_000_000)
	assert.Equal(t, int64(0), sender.DayOutQuota)
	assert.Equal(t, 0, sender.DayOutCount)
	assert.Equal(t, int64(0), sender.LifetimeOutQuota)
	assert.Equal(t, 0, sender.PendingCount)
	assert.Equal(t, 0, receiver.DayInCount)
	assert.Equal(t, int64(0), receiver.LifetimeInQuota)
}

// TestUndoReservationNeverGoesNegative 计数被扣成负数意味着限额从此永久失效,
// 比少统计一笔严重得多。
func TestUndoReservationNeverGoesNegative(t *testing.T) {
	sender := UserState{UserId: 1}
	receiver := UserState{UserId: 2}
	undoReservation(&sender, &receiver, 1000, 1050, true, 1_700_000_000)

	assert.Equal(t, int64(0), sender.DayOutQuota)
	assert.Equal(t, 0, sender.DayOutCount)
	assert.Equal(t, int64(0), sender.LifetimeOutQuota)
	assert.Equal(t, 0, sender.PendingCount)
	assert.Equal(t, 0, receiver.DayInCount)
	assert.Equal(t, int64(0), receiver.LifetimeInQuota)
}

// TestRollDayResetsOnlyDailyCounters 跨日就地重置,不能碰终身累计与冷却时刻。
func TestRollDayResetsOnlyDailyCounters(t *testing.T) {
	s := UserState{
		DayBucket: 20260729, DayOutQuota: 999, DayOutCount: 5, DayInCount: 3,
		LifetimeOutQuota: 10000, LastOutAt: 1_700_000_000,
	}
	rollDay(&s, 20260730)
	assert.Equal(t, int32(20260730), s.DayBucket)
	assert.Equal(t, int64(0), s.DayOutQuota)
	assert.Equal(t, 0, s.DayOutCount)
	assert.Equal(t, 0, s.DayInCount)
	assert.Equal(t, int64(10000), s.LifetimeOutQuota)
	assert.Equal(t, int64(1_700_000_000), s.LastOutAt)

	// 同一天再调用必须是空操作,否则同一自然日内的计数会被反复清零。
	s.DayOutCount = 2
	rollDay(&s, 20260730)
	assert.Equal(t, 2, s.DayOutCount)
}

// TestMaskUsername 脱敏在后端做,任何长度都不能把原文原样吐回去。
func TestMaskUsername(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"a":        "*",
		"ab":       "a*",
		"abc":      "a*c",
		"abcd":     "a**d",
		"alice":    "al***ce",
		"张三":       "张*",
		"张三丰":      "张*丰",
		"用户名很长的账号": "用户***账号",
	}
	for in, want := range cases {
		assert.Equal(t, want, maskUsername(in), "输入 %q", in)
	}
	// 任何非空输入的脱敏结果都必须与原文不同,否则等于没脱敏。
	for _, in := range []string{"a", "ab", "abc", "abcd", "alice", "张三"} {
		assert.NotEqual(t, in, maskUsername(in), "输入 %q 未被脱敏", in)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.com": "a***@example.com",
		"a@example.com":     "*@example.com",
		"a.b+c@mail.co.uk":  "a***@mail.co.uk",
		"":                  "",
		"noatsign":          "",
		"@example.com":      "",
		"trailing@":         "",
	}
	for in, want := range cases {
		assert.Equal(t, want, maskEmail(in), "输入 %q", in)
	}
}

// TestClassifyIdentifier 收款人查找绝不能退化成用户名搜索,那等于开放用户枚举。
func TestClassifyIdentifier(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		mode       string
		wantOK     bool
		wantByType string
	}{
		{name: "纯数字按 ID", raw: "1234", mode: config.RecipientLookupID, wantOK: true, wantByType: lookupByID},
		{name: "ID 模式拒绝用户名", raw: "alice", mode: config.RecipientLookupID},
		{name: "ID 模式拒绝邮箱", raw: "alice@example.com", mode: config.RecipientLookupID},
		{name: "邮箱模式接受完整邮箱", raw: "alice@example.com", mode: config.RecipientLookupIDEmail, wantOK: true, wantByType: lookupByEmail},
		{name: "邮箱模式仍拒绝用户名", raw: "alice", mode: config.RecipientLookupIDEmail},
		{name: "拒绝 SQL 通配符", raw: "a%@example.com", mode: config.RecipientLookupIDEmail},
		{name: "拒绝下划线通配符", raw: "a_b@example.com", mode: config.RecipientLookupIDEmail},
		{name: "拒绝缺少本地部分", raw: "@example.com", mode: config.RecipientLookupIDEmail},
		{name: "拒绝缺少域名", raw: "alice@", mode: config.RecipientLookupIDEmail},
		{name: "拒绝零号用户", raw: "0", mode: config.RecipientLookupID},
		{name: "拒绝空输入", raw: "", mode: config.RecipientLookupIDEmail},
		{name: "拒绝超长输入", raw: strings.Repeat("a", maxIdentifierLen) + "@x.com", mode: config.RecipientLookupIDEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			byType, value, ok := classifyIdentifier(tc.raw, tc.mode)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				assert.Empty(t, value)
				return
			}
			assert.Equal(t, tc.wantByType, byType)
			assert.Equal(t, strings.TrimSpace(tc.raw), value)
		})
	}
}

// TestSanitizeRemark 备注会进主库账本日志的 content,控制字符是典型的日志注入面。
func TestSanitizeRemark(t *testing.T) {
	assert.Equal(t, "abc", sanitizeRemark("a\x00b\nc"))
	assert.Equal(t, "hello", sanitizeRemark("  hello  "))
	assert.Equal(t, maxRemarkRunes, len([]rune(sanitizeRemark(strings.Repeat("字", 500)))))
	// 截断按 rune 而不是字节,否则多字节字符会被劈成半个。
	assert.True(t, strings.HasPrefix(sanitizeRemark(strings.Repeat("字", 500)), "字字字"))
}

// TestTruncateKeepsUTF8Boundary 失败原因含中文,按字节硬切会产生非法 UTF-8,
// MySQL 会整条报错而不是截断。
func TestTruncateKeepsUTF8Boundary(t *testing.T) {
	out := truncate("余额不足,请调整划转金额", 7)
	assert.LessOrEqual(t, len(out), 7)
	assert.True(t, strings.HasPrefix("余额不足,请调整划转金额", out))
	for _, r := range out {
		assert.NotEqual(t, '�', r)
	}
}
