package transfer

import (
	"math"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

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

// acceptedFor 是一份"已通过受理校验"的请求,单个用例只改自己关心的字段。
func acceptedFor(toUserId int, amount, fee int64) acceptedRequest {
	return acceptedRequest{
		FromUserId: 5,
		ToUserId:   toUserId,
		Amount:     amount,
		Fee:        fee,
		Total:      amount + fee,
		IdemKey:    "5:abc",
	}
}

// TestFundingFactsFingerprintCoversEveryFundingElement 锁定幂等指纹的覆盖面。
//
// 复现的缺陷:用户提交 {to:7, amount:100, client_request_id:"abc"} 成功后,
// 再提交 {to:9, amount:50000000, client_request_id:"abc"} —— 唯一索引只保证
// "不重复执行",幂等命中会直接返回原单成功。指纹是唯一能识破"这是另一笔请求"
// 的判据,漏掉任何一个资金要素(尤其是 PeerUserId 与 AmountQuota),
// 换收款人/换金额的重放就会被当成正常重试放过去。
func TestFundingFactsFingerprintCoversEveryFundingElement(t *testing.T) {
	base := acceptedFor(7, 100, 5)
	baseFp := fundingFacts(base).Fingerprint
	require.NotEmpty(t, baseFp, "指纹不能为空:空值在 twophase 侧一律跳过校验")
	require.Len(t, baseFp, 64, "必须放得进 qy_fund_orders.fingerprint(varchar(64))")

	cases := []struct {
		name     string
		mutate   func(*acceptedRequest)
		wantSame bool
	}{
		{name: "换收款人", mutate: func(a *acceptedRequest) { a.ToUserId = 9 }},
		{name: "换金额", mutate: func(a *acceptedRequest) { a.Amount = 50000000 }},
		{name: "换发起人", mutate: func(a *acceptedRequest) { a.FromUserId = 6 }},
		// 手续费不是资金要素而是服务端派生量:它由受理时的 transfer.fee_* 算出,
		// 用户在请求里说不了它。用户首次提交超时、运营在这中间调了费率,同一个
		// client_request_id 的重试会算出不同的 Fee —— 判成 409 会让一笔根本没成功的
		// 划转既转不成也不敢重发。详见 twophase.Request.Digest。
		{name: "费率被运营调过", mutate: func(a *acceptedRequest) { a.Fee = 999 }, wantSame: true},
		// 备注不是资金要素:改文案后重试是正常行为,判成 409 会把用户逼去换一个
		// client_request_id 重发,那才真的会转两笔。
		{name: "只改备注", mutate: func(a *acceptedRequest) { a.Remark = "换个备注" }, wantSame: true},
		{name: "完全相同的重试", mutate: func(*acceptedRequest) {}, wantSame: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			got := fundingFacts(req).Fingerprint
			if tc.wantSame {
				assert.Equal(t, baseFp, got)
				return
			}
			assert.NotEqual(t, baseFp, got)
		})
	}
}

// TestFundingFactsCarriesRequestIntoOrder 指纹之外,资金单本身也必须如实记录本次要素 ——
// 审计与响应体都以它为准。
func TestFundingFactsCarriesRequestIntoOrder(t *testing.T) {
	acc := acceptedFor(7, 100, 5)
	req := fundingFacts(acc)

	assert.Equal(t, qymodel.KindTransfer, req.Kind)
	assert.Equal(t, idemScope, req.IdemScope)
	assert.Equal(t, acc.IdemKey, req.IdemKey)
	assert.Equal(t, acc.FromUserId, req.UserId)
	assert.Equal(t, acc.ToUserId, req.PeerUserId)
	assert.Equal(t, acc.Amount, req.AmountQuota)
	assert.Equal(t, acc.Fee, req.FeeQuota)
	// 指纹必须与 twophase 侧用同一套要素算出来,否则正常重试会被误判成冲突。
	assert.Equal(t, req.Digest(), req.Fingerprint)
}

// TestTransferCreatedAuditUsesOrderNotRequest 复现审计表被污染的那一步。
//
// 场景:攻击者用已成功单据的 client_request_id 重提一笔 {to:9, amount:5000万}。
// 幂等命中返回的是原单(to:7, amount:100),资金侧毫无变化 —— 但只要审计取的是
// **本次请求**的金额与收款人,qy_audit_logs 就会多出一条
// "trace_no=真实单号 / amount=5000万 / target=9 / result=ok" 的记录。
// 审计表是这套资金系统事后仲裁的唯一凭据,它必须只反映真实生效的单据。
//
// 指纹校验会拦住绝大多数这类重放,但拦不住升级前落库的历史单(指纹为空一律跳过
// 校验),所以这一层不能省。
func TestTransferCreatedAuditUsesOrderNotRequest(t *testing.T) {
	stored := &qymodel.FundOrder{
		OrderNo:     "TR20260730000001",
		Status:      qymodel.StatusSuccess,
		UserId:      5,
		PeerUserId:  7,
		AmountQuota: 100,
		FeeQuota:    5,
	}
	// 本次请求(攻击者构造)刻意与原单完全不同,任何一项串到审计里都是伪造。
	replay := acceptedFor(9, 50000000, 0)

	entry := transferCreatedAudit(stored, "alice")

	assert.Equal(t, stored.OrderNo, entry.TraceNo)
	assert.Equal(t, stored.AmountQuota, entry.AmountQuota)
	assert.Equal(t, stored.PeerUserId, entry.TargetUserId)
	assert.Equal(t, stored.UserId, entry.ActorUserId)
	assert.NotEqual(t, replay.Amount, entry.AmountQuota, "审计金额取了本次请求的值")
	assert.NotEqual(t, replay.ToUserId, entry.TargetUserId, "审计收款人取了本次请求的值")

	assert.Equal(t, qymodel.AuditCategoryTransfer, entry.Category)
	assert.Equal(t, "transfer.create", entry.Action)
	assert.Equal(t, qymodel.ActorUser, entry.ActorType)
	assert.Equal(t, qymodel.ResultOK, entry.Result)
	assert.Equal(t, "success", entry.Reason)
	assert.Equal(t, "alice", entry.ActorName)
}

// TestTransferCreatedAuditReflectsOrderStatus 幂等命中一笔已冲正的单据时,
// 审计的 reason 必须说它是 reversed,不能因为"本次 Execute 没报错"就写成 success。
func TestTransferCreatedAuditReflectsOrderStatus(t *testing.T) {
	for status, want := range map[int8]string{
		qymodel.StatusSuccess:  "success",
		qymodel.StatusReversed: "reversed",
	} {
		entry := transferCreatedAudit(&qymodel.FundOrder{OrderNo: "TR1", Status: status}, "bob")
		assert.Equal(t, want, entry.Reason)
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
			err := evaluateRisk(tc.state, UserState{UserId: 2}, cfg, cfg, tc.total, now)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
		})
	}
}

// TestEvaluateRiskGuardsReceiverDailyInCount 锁定 receiver_daily_max_in_count 这道洗号闸门。
//
// 它拦的是发起方限额天然拦不住的形状:N 个小号各转一笔到同一个汇集账号 ——
// 每一笔都在各自的发起方额度内,只有收款方侧的计数会涨。这道闸门曾经只有配置项
// 没有消费方,200 个小号可以全部通过。
func TestEvaluateRiskGuardsReceiverDailyInCount(t *testing.T) {
	const now int64 = 1_700_000_000

	cases := []struct {
		name     string
		limit    int
		receiver UserState
		wantErr  *bizError
	}{
		{name: "首次收款放行", limit: 50, receiver: UserState{}},
		{name: "刚好用满收款笔数放行", limit: 50, receiver: UserState{DayInCount: 49}},
		{name: "超出收款笔数即拒绝", limit: 50, receiver: UserState{DayInCount: 50}, wantErr: errReceiverDailyInExceeded},
		{name: "远超上限仍然拒绝", limit: 50, receiver: UserState{DayInCount: 200}, wantErr: errReceiverDailyInExceeded},
		{name: "上限为一时第二笔即拒绝", limit: 1, receiver: UserState{DayInCount: 1}, wantErr: errReceiverDailyInExceeded},
		// 与其它四项同口径:0 表示不限制。改成"0 = 禁止"会让沿用旧配置的部署全线拒收。
		{name: "配置为零表示不限制", limit: 0, receiver: UserState{DayInCount: math.MaxInt32 - 1}},
		{name: "配置为负同样表示不限制", limit: -1, receiver: UserState{DayInCount: 9999}},
		// DayInCount 由调用方在 rollDay 之后传入,跨日后是 0,不该继续沿用昨天的计数。
		{name: "跨日清零后放行", limit: 50, receiver: UserState{DayBucket: 20260730, DayInCount: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.ReceiverDailyMaxInCount = tc.limit
			// 发起方一侧全部留空,确保命中的确实是收款方闸门而不是别的判定。
			err := evaluateRisk(UserState{UserId: 1}, tc.receiver, cfg, cfg, 1, now)
			if tc.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Same(t, tc.wantErr, err)
		})
	}
}

// TestEvaluateRiskAccumulatesReceiverCountAcrossSenders 复现审计里的攻击形状:
// 多个互不相干的小号先后向同一个账号打款,发起方一侧永远干净,
// 只有收款方的 DayInCount 在涨,闸门必须在第 limit+1 笔上关闭。
func TestEvaluateRiskAccumulatesReceiverCountAcrossSenders(t *testing.T) {
	const now int64 = 1_700_000_000
	const limit = 3
	cfg := baseConfig()
	cfg.ReceiverDailyMaxInCount = limit

	receiver := UserState{UserId: 999}
	bucket := dayBucket(now)
	for i := 1; i <= limit; i++ {
		sender := UserState{UserId: i} // 每笔都是一个全新的小号
		require.NoError(t, evaluateRisk(sender, receiver, cfg, cfg, 1, now), "第 %d 笔应放行", i)
		applyReservation(&sender, &receiver, 1, 1, now, bucket)
	}
	assert.Equal(t, limit, receiver.DayInCount)

	err := evaluateRisk(UserState{UserId: limit + 1}, receiver, cfg, cfg, 1, now)
	require.Error(t, err)
	assert.Same(t, errReceiverDailyInExceeded, err)
}

// TestEvaluateRiskTreatsZeroAsUnlimited 固化"配置 0 = 不限制"的口径。
// 若哪天改成"0 = 禁止",所有沿用默认配置的部署会在升级瞬间全线拒绝划转。
func TestEvaluateRiskTreatsZeroAsUnlimited(t *testing.T) {
	cfg := config.Transfer{}
	sender := UserState{DayOutQuota: math.MaxInt32, DayOutCount: 9999, LastOutAt: 1_700_000_000}
	receiver := UserState{DayInCount: 9999}
	assert.NoError(t, evaluateRisk(sender, receiver, cfg, cfg, math.MaxInt32, 1_700_000_000))
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

// TestReceiverScopedGateReadsTheReceiverTier —— 收款方口径的闸门必须读
// **收款方那一份**门槛,发起方那一份对它没有任何发言权。
//
// 这是纯函数层的定位断言,与 grouplimit_create_test.go 里那两条 create() 级用例
// 配套:那两条证明「create 把两份门槛都传对了」,这一条证明「evaluateRisk 读对了
// 其中哪一份」。把 receiverCfg.ReceiverDailyMaxInCount 写回 cfg.
// ReceiverDailyMaxInCount,这条立刻变红。
func TestReceiverScopedGateReadsTheReceiverTier(t *testing.T) {
	const now int64 = 1_700_000_000
	// 发起方那一档很宽(20 笔),收款方那一档很紧(1 笔)。
	senderCfg := baseConfig()
	senderCfg.ReceiverDailyMaxInCount = 20
	receiverCfg := baseConfig()
	receiverCfg.ReceiverDailyMaxInCount = 1

	sender := UserState{UserId: 1}
	receiver := UserState{UserId: 2, DayInCount: 1}

	err := evaluateRisk(sender, receiver, senderCfg, receiverCfg, 1, now)
	require.Error(t, err)
	assert.Same(t, errReceiverDailyInExceeded, err,
		"按发起方那一档(20)判会放行 —— 闸门的上限就成了被它约束的那一方自己挑的")

	// 反方向:发起方那一档紧、收款方那一档宽时,绝不能把发起方的限制强加给收款人。
	assert.NoError(t, evaluateRisk(sender, receiver, receiverCfg, senderCfg, 1, now),
		"发起方那一档的收款方闸门被错误地强加到了收款人身上")
}
