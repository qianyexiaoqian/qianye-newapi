package twophase

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newStateDB 建一个只承载 qy_fund_orders 的内存库。
//
// 状态机的核心不变量是"CAS 落空时不许伪造终态",这条只有真的跑一次
// UPDATE ... WHERE status = pending 才能验证,所以这里不 mock。
func newStateDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&qymodel.FundOrder{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

func seedOrder(t *testing.T, gdb *gorm.DB, orderNo string, status int8) *qymodel.FundOrder {
	t.Helper()
	now := common.GetTimestamp()
	row := &qymodel.FundOrder{
		OrderNo:     orderNo,
		Kind:        qymodel.KindTransfer,
		Status:      status,
		IdemScope:   "transfer",
		IdemKey:     orderNo,
		UserId:      5,
		PeerUserId:  7,
		AmountQuota: 100,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	require.NoError(t, gdb.Create(row).Error)
	return row
}

func loadOrder(t *testing.T, gdb *gorm.DB, orderNo string) qymodel.FundOrder {
	t.Helper()
	var got qymodel.FundOrder
	require.NoError(t, gdb.Where("order_no = ?", orderNo).Take(&got).Error)
	return got
}

// B7:补偿任务把单据推成 Uncertain 之后,业务线程迟到的错误不得伪造成 Failed。
//
// Uncertain 是"我不知道钱动没动,禁止自动回滚"的唯一出口。内存里被改成 Failed
// 会让调用方(transfer.releaseOnFailure / withdraw.creditQuota)按 order.Status
// == StatusFailed 走回滚:清风控预占、解冻佣金。最终资金单等人工、业务明细已
// 回滚、审计声称转成 failed,三份记录互相矛盾且永不自愈 —— Compensate 只扫
// pending,不会再碰 Uncertain 单。
func TestMarkFailed_DoesNotOverrideOtherPath(t *testing.T) {
	cases := []struct {
		name     string
		seeded   int8
		wantRow  int8
		wantMem  int8
		rollback bool // 调用方是否会据此回滚业务明细
	}{
		{"补偿任务已转人工", qymodel.StatusUncertain, qymodel.StatusUncertain, qymodel.StatusUncertain, false},
		{"补偿任务已确认成功", qymodel.StatusSuccess, qymodel.StatusSuccess, qymodel.StatusSuccess, false},
		{"补偿任务已判定失败", qymodel.StatusFailed, qymodel.StatusFailed, qymodel.StatusFailed, true},
		{"本线程赢下 CAS", qymodel.StatusPending, qymodel.StatusFailed, qymodel.StatusFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newStateDB(t)
			order := seedOrder(t, gdb, "TR-"+tc.name, tc.seeded)

			markFailed(gdb, order, errors.New("主库事务返回确定性错误"))

			assert.Equal(t, tc.wantRow, loadOrder(t, gdb, order.OrderNo).Status,
				"库里的状态只能由赢下 CAS 的那一方改写")
			assert.Equal(t, tc.wantMem, order.Status,
				"内存状态必须等于库里的真实状态,调用方的回滚判定完全依赖它")
			assert.Equal(t, tc.rollback, order.Status == qymodel.StatusFailed,
				"只有真的转成 failed 才允许调用方回滚预占/解冻")
		})
	}
}

// B7:markFailed 写入的 last_error 必须是 rune 安全的。
// last_error 是 utf8mb4 的 varchar(512),裸字节切出的非法尾巴会让整条 UPDATE
// 被 1366 拒绝,单据留在 pending 被补偿任务反复空转。
func TestMarkFailed_ErrorMessageStaysValidUTF8(t *testing.T) {
	gdb := newStateDB(t)
	order := seedOrder(t, gdb, "TR-utf8", qymodel.StatusPending)

	long := strings.Repeat("主库锁等待超时", 200) // 每字符 3 字节,远超 512
	markFailed(gdb, order, errors.New(long))

	got := loadOrder(t, gdb, order.OrderNo)
	assert.Equal(t, qymodel.StatusFailed, got.Status)
	assert.LessOrEqual(t, len(got.LastError), maxErrBytes)
	assert.True(t, utf8.ValidString(got.LastError),
		"截断后的 last_error 必须仍是合法 UTF-8,否则 utf8mb4 列会拒绝整行")
}

// B7 的另一半:markSuccess 的 CAS 落空时,不能让 Execute 按"本次成功"收尾。
// 对方推成 Failed/Uncertain 却写下 ResultOK 审计,等于给一笔没成功的操作发凭据。
func TestMarkSuccess_YieldsToOtherPath(t *testing.T) {
	cases := []struct {
		name        string
		seeded      int8
		wantWon     bool
		wantLocal   bool // LocalCommit 是否应被执行
		wantDiverge bool // Execute 是否应向调用方报错
	}{
		{"本线程赢下 CAS", qymodel.StatusPending, true, true, false},
		{"补偿任务已确认成功", qymodel.StatusSuccess, false, false, false},
		{"补偿任务已判定失败", qymodel.StatusFailed, false, false, true},
		{"补偿任务已转人工", qymodel.StatusUncertain, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newStateDB(t)
			order := seedOrder(t, gdb, "TR-ms-"+tc.name, tc.seeded)

			localRan := false
			won, err := markSuccess(gdb, order, func(tx *gorm.DB, o *qymodel.FundOrder) error {
				localRan = true
				return nil
			})
			require.NoError(t, err)

			assert.Equal(t, tc.wantWon, won)
			assert.Equal(t, tc.wantLocal, localRan,
				"业务副作用只能由赢下 CAS 的一方执行,否则会重复扣佣金余额")
			if !tc.wantWon {
				assert.Equal(t, tc.seeded, order.Status,
					"让出时内存状态必须回读成库里的真实状态")
			}
			if tc.wantDiverge {
				assert.ErrorIs(t, divergedError(order), ErrOrderDiverged,
					"对方推成非成功态时必须向调用方报错,绝不能返回成功")
			} else {
				assert.NoError(t, divergedError(order))
			}
		})
	}
}

// reloadStatus 回读失败时保留 pending:那是所有分支里最安全的结论
// (调用方对 pending 一律不回滚,交给补偿任务)。
func TestReloadStatus_KeepsPendingWhenRowMissing(t *testing.T) {
	gdb := newStateDB(t)
	order := &qymodel.FundOrder{OrderNo: "TR-missing", Status: qymodel.StatusPending}

	reloadStatus(gdb, order)

	assert.Equal(t, qymodel.StatusPending, order.Status,
		"回读不到就绝不猜终态,pending 才是安全默认值")
}

// B4:同一幂等键换金额/换收款人时,resolveExisting 必须拒绝而不是返回原单成功。
// 返回原单会让调用方拿本次请求的参数写下"成功"审计,而审计表是事后仲裁的唯一凭据。
func TestResolveExisting_FingerprintMismatch(t *testing.T) {
	const stored = "fp-original"
	cases := []struct {
		name        string
		orderFp     string
		wantFp      string
		status      int8
		wantConflct bool
	}{
		{"指纹一致且原单成功", stored, stored, qymodel.StatusSuccess, false},
		{"指纹不一致(换了金额或收款人)", stored, "fp-tampered", qymodel.StatusSuccess, true},
		{"指纹不一致且原单失败", stored, "fp-tampered", qymodel.StatusFailed, true},
		{"历史单无指纹,跳过校验", "", "fp-new", qymodel.StatusSuccess, false},
		{"调用方未接入指纹,跳过校验", stored, "", qymodel.StatusSuccess, false},
		{"两侧都为空,跳过校验", "", "", qymodel.StatusSuccess, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			order := &qymodel.FundOrder{
				OrderNo:     "TR-fp",
				Status:      tc.status,
				Fingerprint: tc.orderFp,
			}
			got, err := resolveExisting(order, tc.wantFp)
			require.NotNil(t, got)

			if tc.wantConflct {
				assert.ErrorIs(t, err, ErrIdemConflict,
					"参数被换过就不是同一个请求的重放,必须让调用方按 409 处理")
				return
			}
			assert.NotErrorIs(t, err, ErrIdemConflict)
			if tc.status == qymodel.StatusSuccess {
				assert.NoError(t, err, "同一请求的重放应幂等返回原单")
			}
		})
	}
}

// 指纹必须对每一个决定资金去向的要素敏感,且对同一请求稳定。
// 漏掉任何一个要素,换那个要素重放就能绕过校验。
func TestRequestDigest_CoversFundingFacts(t *testing.T) {
	base := Request{
		Kind:        qymodel.KindTransfer,
		IdemScope:   "transfer",
		IdemKey:     "abc",
		UserId:      5,
		PeerUserId:  7,
		AmountQuota: 100,
		FeeQuota:    1,
		RefType:     "log",
		RefId:       "42",
	}
	baseline := base.Digest()
	assert.Len(t, baseline, 64, "指纹必须能放进 varchar(64)")
	assert.Equal(t, baseline, base.Digest(), "同一请求必须算出同一指纹,否则正常重试会被误判为冲突")

	// 手续费刻意不在这张表里 —— 它是服务端派生量,不是请求要素。
	// 见 TestRequestDigest_IgnoresServerDerivedFee。
	mutations := map[string]func(*Request){
		"换收款人":    func(r *Request) { r.PeerUserId = 9 },
		"换金额":     func(r *Request) { r.AmountQuota = 50000000 },
		"换发起人":    func(r *Request) { r.UserId = 6 },
		"换业务类型":   func(r *Request) { r.Kind = qymodel.KindWithdrawQuota },
		"换幂等域":    func(r *Request) { r.IdemScope = "withdraw_credit" },
		"换业务引用类型": func(r *Request) { r.RefType = "withdraw" },
		"换业务引用":   func(r *Request) { r.RefId = "43" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			mutated := base
			mutate(&mutated)
			assert.NotEqual(t, baseline, mutated.Digest())
		})
	}

	t.Run("额外要素参与计算", func(t *testing.T) {
		assert.NotEqual(t, baseline, base.Digest("payee:alipay:1234"))
	})

	// 分隔符不能是可能出现在业务串里的字符,否则相邻字段的边界可以被挪动。
	t.Run("字段边界不可被挪动", func(t *testing.T) {
		a := base
		a.RefType, a.RefId = "log", "42"
		b := base
		b.RefType, b.RefId = "log42", ""
		assert.NotEqual(t, a.Digest(), b.Digest())
	})
}

// NEW-4:指纹只收"用户这次请求说了什么",不收服务端派生量。
//
// 复现的缺陷:用户提交一笔划转 → 网关超时,客户端并不知道成没成;运营在这中间调了
// transfer.fee_*;客户端拿同一个 client_request_id 重试 —— 受理时按新配置算出的
// 手续费不同,若手续费进指纹,resolveExisting 就会判 ErrIdemConflict,接口回 409
// 「请到划转记录中核对」。而原单其实根本没成功:用户既转不成,也不敢换一个
// client_request_id 重发(怕转两笔),这笔钱就卡死在两个人之间。
//
// 口径与 commission/clawback.go 的 sameClawbackRequest 刻意不比 GrossAmount 一致:
// 被服务端削过或派生出来的量不能当指纹分量。
func TestRequestDigest_IgnoresServerDerivedFee(t *testing.T) {
	base := Request{
		Kind:        qymodel.KindTransfer,
		IdemScope:   "transfer",
		IdemKey:     "client-req-abc",
		UserId:      5,
		PeerUserId:  7,
		AmountQuota: 100,
		FeeQuota:    5,
	}
	stored := base.Digest()

	for _, fee := range []int64{0, 1, 999, 50_000_000} {
		retried := base
		retried.FeeQuota = fee // 运营调过费率之后,同一笔请求算出的手续费
		assert.Equal(t, stored, retried.Digest(),
			"手续费改变不得改变指纹:那是服务端派生量,用户在请求里说不了它")
	}

	// 端到端到 resolveExisting:费率改动后的重试必须仍被当成同一笔请求的幂等重放。
	order := &qymodel.FundOrder{
		OrderNo: "TR-fee", Status: qymodel.StatusSuccess, Fingerprint: stored,
	}
	repriced := base
	repriced.FeeQuota = 999
	got, err := resolveExisting(order, repriced.Digest())
	require.NotNil(t, got)
	assert.NoError(t, err, "费率变了不代表换了一笔请求,不能回 409")

	// 但用户请求里真正说了的东西一改,指纹必须立刻识破 —— 这条是指纹的存在理由,
	// 去掉手续费不能把它一起削掉。
	tampered := base
	tampered.AmountQuota = 50_000_000
	_, err = resolveExisting(order, tampered.Digest())
	assert.ErrorIs(t, err, ErrIdemConflict, "换金额重放必须仍被拒绝")
}
