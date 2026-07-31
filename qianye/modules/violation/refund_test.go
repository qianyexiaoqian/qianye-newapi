package violation

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/service"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newMainDB 建一个只承载主库三张相关表的内存库。
//
// 退款要同时动 users(钱包 + used_quota)、tokens(remain_quota + used_quota)、
// user_subscriptions(amount_used)。"退了哪几张表、各退了多少"是这条链路的全部要害,
// mock 掉任何一张都等于把要验的东西验没了。
func newMainDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// seedCharged 复现"罚款已经扣掉"之后主库的状态。
//
// 参数刻意照抄 service.PostConsumeQuota 的实际行为:
// 钱包或订阅池扣 fee、tokens.remain_quota 扣 fee 且 used_quota 加 fee,
// 随后 chargeFee 还会给 users.used_quota 再加一笔 fee。
func seedCharged(t *testing.T, gdb *gorm.DB, sub bool, fee int) {
	t.Helper()
	wallet := 0
	if !sub {
		wallet = 1000000 - fee // 钱包扣过 fee
	}
	require.NoError(t, gdb.Create(&model.User{
		Id: 88, Username: "u88", Quota: wallet, UsedQuota: 2000 + fee, RequestCount: 7,
	}).Error)
	require.NoError(t, gdb.Create(&model.Token{
		Id: 55, UserId: 88, Key: "k55", RemainQuota: 3000000 - fee, UsedQuota: 4000 + fee,
	}).Error)
	if sub {
		require.NoError(t, gdb.Create(&model.UserSubscription{
			Id: 31, UserId: 88, AmountTotal: 5000000, AmountUsed: int64(900000 + fee),
		}).Error)
	}
}

func chargedRecord(sub bool, fee int64) *Record {
	rec := &Record{
		Id: 1, RecNo: "vr_req1_9", UserId: 88, TokenId: 55,
		FeeQuota: fee, FeeStatus: FeeStatusCharged,
	}
	if sub {
		rec.BillingSource = service.BillingSourceSubscription
		rec.SubscriptionId = 31
	}
	return rec
}

func loadUser(t *testing.T, gdb *gorm.DB) model.User {
	t.Helper()
	var u model.User
	require.NoError(t, gdb.Where("id = ?", 88).Take(&u).Error)
	return u
}

func loadToken(t *testing.T, gdb *gorm.DB) model.Token {
	t.Helper()
	var tk model.Token
	require.NoError(t, gdb.Where("id = ?", 55).Take(&tk).Error)
	return tk
}

// TestRefundReturnsToEveryPoolTheChargeTouched 是 A2 的核心回归。
//
// 扣费经 service.PostConsumeQuota 一次动了两处(钱包**或**订阅池,外加
// tokens.remain_quota),chargeFee 之后还把 users.used_quota 加了一笔。
// 旧实现的退款只有一条 `users.quota + amount`:
//   - 令牌额度永久少掉这一笔;
//   - 订阅用户的订阅池消耗从未归还,钱包却凭空多出等额额度(净增发)。
//
// 两个子用例在修复前都会失败:钱包子用例失败在令牌两列,订阅子用例失败在
// "钱包不该被加钱"与"订阅池必须回冲"。
func TestRefundReturnsToEveryPoolTheChargeTouched(t *testing.T) {
	const fee = int64(500000)

	t.Run("钱包扣费:钱包与令牌都要回冲", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, false, int(fee))
		require.NoError(t, applyRefundOnMainDB(gdb, chargedRecord(false, fee), fee))

		u := loadUser(t, gdb)
		assert.EqualValues(t, 1000000, u.Quota, "钱包必须恢复到扣费前")
		assert.EqualValues(t, 2000, u.UsedQuota, "used_quota 必须回冲,否则消费统计永远虚高")
		assert.EqualValues(t, 7, u.RequestCount, "退款不是一次新请求,request_count 不得增加")

		tk := loadToken(t, gdb)
		assert.EqualValues(t, 3000000, tk.RemainQuota, "令牌可用额度必须归还")
		assert.EqualValues(t, 4000, tk.UsedQuota)
	})

	t.Run("订阅扣费:退回订阅池,绝不加钱包", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, true, int(fee))
		require.NoError(t, applyRefundOnMainDB(gdb, chargedRecord(true, fee), fee))

		u := loadUser(t, gdb)
		assert.EqualValues(t, 0, u.Quota,
			"订阅用户的钱包不得凭空多出额度 —— 那是净增发,不是退款")
		assert.EqualValues(t, 2000, u.UsedQuota)

		var sub model.UserSubscription
		require.NoError(t, gdb.Where("id = ?", 31).Take(&sub).Error)
		assert.EqualValues(t, 900000, sub.AmountUsed, "订阅池消耗必须归还")

		tk := loadToken(t, gdb)
		assert.EqualValues(t, 3000000, tk.RemainQuota)
		assert.EqualValues(t, 4000, tk.UsedQuota)
	})
}

// TestRefundEdgeCases 覆盖"扣费之后主库状态被改过"的几种真实情况。
//
// 这些分支的共同要求:要么把钱退到位,要么明确失败,绝不允许把用户的额度
// 写成负数,也绝不允许因为一个次要对象消失就让整笔退款永远退不回去。
func TestRefundEdgeCases(t *testing.T) {
	const fee = int64(500000)

	t.Run("令牌已被删除时仍要退钱包", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, false, int(fee))
		require.NoError(t, gdb.Where("id = ?", 55).Delete(&model.Token{}).Error)

		require.NoError(t, applyRefundOnMainDB(gdb, chargedRecord(false, fee), fee),
			"令牌没了不该把整笔退款回滚 —— 那会让用户连钱包里的钱也拿不回来")
		assert.EqualValues(t, 1000000, loadUser(t, gdb).Quota)
	})

	t.Run("订阅已不存在时回落到钱包", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, true, int(fee))
		require.NoError(t, gdb.Where("id = ?", 31).Delete(&model.UserSubscription{}).Error)

		require.NoError(t, applyRefundOnMainDB(gdb, chargedRecord(true, fee), fee))
		assert.EqualValues(t, fee, loadUser(t, gdb).Quota,
			"订阅池没了只能退钱包,否则资金单落 failed,幂等键决定它再也不会成功")
	})

	t.Run("订阅池被周期重置后不得把 amount_used 写成负数", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, true, int(fee))
		// 模拟按月刷新:amount_used 已被清到远小于本次退款额。
		require.NoError(t, gdb.Model(&model.UserSubscription{}).Where("id = ?", 31).
			Update("amount_used", 100).Error)

		require.NoError(t, applyRefundOnMainDB(gdb, chargedRecord(true, fee), fee))
		var sub model.UserSubscription
		require.NoError(t, gdb.Where("id = ?", 31).Take(&sub).Error)
		assert.EqualValues(t, 0, sub.AmountUsed, "下界必须夹到 0,与 PostConsumeUserSubscriptionDelta 同口径")
	})

	t.Run("used_quota 被管理员重置后不得写成负数", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, false, int(fee))
		require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", 88).
			Update("used_quota", 10).Error)
		require.NoError(t, gdb.Model(&model.Token{}).Where("id = ?", 55).
			Update("used_quota", 3).Error)

		require.NoError(t, applyRefundOnMainDB(gdb, chargedRecord(false, fee), fee))
		assert.EqualValues(t, 0, loadUser(t, gdb).UsedQuota)
		assert.EqualValues(t, 0, loadToken(t, gdb).UsedQuota)
	})

	t.Run("钱包加款会溢出 int32 时整笔拒绝", func(t *testing.T) {
		gdb := newMainDB(t)
		seedCharged(t, gdb, false, int(fee))
		require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", 88).
			Update("quota", common.MaxQuota).Error)

		err := applyRefundOnMainDB(gdb, chargedRecord(false, fee), fee)
		require.Error(t, err, "加爆 int32 会翻成负数,等于把误判赔偿变成账号清零")
		assert.EqualValues(t, common.MaxQuota, loadUser(t, gdb).Quota)
	})
}

// ───────────────────────── 撤销的重入自愈(B1) ─────────────────────────

func newRecordDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Record{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// TestClaimRevokeKeepsSelfHealPathOpen 是 B1 的核心回归。
//
// 旧实现在 CAS 落空(记录已是 revoked)时直接 `return 0, nil`,调用方的退款分支
// 根本不会被执行。可撤销与退款是两步跨库操作,第一次点击完全可能停在
// "记录已 revoked、退款没成功"的中间态(退款失败只记日志、扩展库回写超时、
// 进程重启都会导致它)。于是管理员再点一次也修不好,只能人工补单 —— 而人工补单
// 与后来收敛的补偿任务撞在一起,同一笔就退了两次。
//
// 新实现在落空时回读记录:调用方据此看到真实的 fee_status,该补退款就补退款。
func TestClaimRevokeKeepsSelfHealPathOpen(t *testing.T) {
	cases := []struct {
		name          string
		seedStatus    string
		seedFeeStatus string
		wantFirst     bool
		wantRevoked   bool
		wantFeeStatus string
	}{
		{"首次撤销 active 记录", RecordActive, FeeStatusCharged, true, true, FeeStatusCharged},
		{"申诉通过撤销 appealed 记录", RecordAppealed, FeeStatusCharged, true, true, FeeStatusCharged},
		{
			// 这条就是被堵死的自愈入口:第一次点击撤销成功、退款没成功。
			name: "重入:已撤销但退款未完成", seedStatus: RecordRevoked, seedFeeStatus: FeeStatusCharged,
			wantFirst: false, wantRevoked: true, wantFeeStatus: FeeStatusCharged,
		},
		{
			name: "重入:已撤销且退款已完成", seedStatus: RecordRevoked, seedFeeStatus: FeeStatusRefunded,
			wantFirst: false, wantRevoked: true, wantFeeStatus: FeeStatusRefunded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newRecordDB(t)
			require.NoError(t, gdb.Create(&Record{
				Id: 1, RecNo: "vr_req1_9", UserId: 88, Status: tc.seedStatus,
				FeeQuota: 500000, FeeStatus: tc.seedFeeStatus, CreatedAt: common.GetTimestamp(),
			}).Error)

			// 调用方拿到的往往是一份读于点击之前的旧快照,这里刻意模拟这一点。
			rec := &Record{Id: 1, RecNo: "vr_req1_9", UserId: 88,
				Status: RecordActive, FeeQuota: 500000, FeeStatus: FeeStatusCharged}
			first, err := claimRevoke(gdb, rec, "误判", 3)
			require.NoError(t, err)

			assert.Equal(t, tc.wantFirst, first)
			if tc.wantRevoked {
				assert.Equal(t, RecordRevoked, rec.Status,
					"撤销状态必须成立,调用方才会继续走到退款分支")
			}
			assert.Equal(t, tc.wantFeeStatus, rec.FeeStatus,
				"重入时必须回读真实的 fee_status,它决定要不要补做退款")
		})
	}
}

// TestMarkRecordRefundedIsIdempotent 保证退款回写可以被执行任意多次。
//
// 它同时挂在 twophase 的 LocalCommit 与补偿 Resolver 上,同一单会被补偿多轮;
// 条件里的 fee_status 集合就是幂等保证。
func TestMarkRecordRefundedIsIdempotent(t *testing.T) {
	cases := []struct {
		name       string
		seed       string
		wantStatus string
		wantQuota  int64
	}{
		{"charged 会被标成 refunded", FeeStatusCharged, FeeStatusRefunded, 500000},
		{"truncated(余额不足只扣了一部分)同样要退", FeeStatusTruncated, FeeStatusRefunded, 500000},
		{"已 refunded 不再重写", FeeStatusRefunded, FeeStatusRefunded, 0},
		{"从未扣成的记录不得被标成已退款", FeeStatusFailed, FeeStatusFailed, 0},
		{"影子模式记录不得被标成已退款", FeeStatusShadow, FeeStatusShadow, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newRecordDB(t)
			require.NoError(t, gdb.Create(&Record{
				Id: 1, RecNo: "vr_req1_9", UserId: 88,
				FeeQuota: 500000, FeeStatus: tc.seed, CreatedAt: common.GetTimestamp(),
			}).Error)

			// 跑两遍:第二遍模拟"LocalCommit 已写过,补偿 Resolver 又来一次"。
			require.NoError(t, markRecordRefunded(gdb, "vr_req1_9", 500000))
			require.NoError(t, markRecordRefunded(gdb, "vr_req1_9", 500000))

			var got Record
			require.NoError(t, gdb.Where("id = ?", 1).Take(&got).Error)
			assert.Equal(t, tc.wantStatus, got.FeeStatus)
			assert.Equal(t, tc.wantQuota, got.RefundQuota)
		})
	}
}

// ─────────────────── 幂等命中不得报出并不存在的退款(NEW-3) ───────────────────

// newFundDB 建一个同时承载违规记录与资金单的内存库。
//
// 两张表都在扩展库里,而 NEW-3 的要害正是"资金单说成功、记录说还扣着"这种跨表
// 错位,只 mock 其中一张就把要验的东西验没了。
func newFundDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Record{}, &qymodel.FundOrder{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

func seedRefundOrder(t *testing.T, gdb *gorm.DB, orderNo, recNo string, status int8, amount int64) *qymodel.FundOrder {
	t.Helper()
	now := common.GetTimestamp()
	row := &qymodel.FundOrder{
		OrderNo: orderNo, Kind: qymodel.KindViolationFee, Status: status,
		IdemScope: idemScopeViolationRefund, IdemKey: recNo,
		UserId: 88, AmountQuota: amount, RefType: "violation_record", RefId: "1",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, gdb.Create(row).Error)
	return row
}

// TestConfirmRefundSettledNeverReportsAPhantomRefund 是 NEW-3 的核心回归。
//
// 复现的缺陷:升级前那批退款单被旧补偿任务(当时还没有 Resolver)推成 Success,
// 而 qy_violation_record.fee_status 仍是 charged。管理员再点一次"撤销+退款",
// twophase.Execute 在 resolveExisting 里幂等命中 `case StatusSuccess: return order, nil`,
// LocalCommit 在这条路径上根本不执行;旧代码却直接 `refunded = rec.FeeQuota`,
// 于是接口回 200 + refunded_quota、审计表多一条 records.revoke 成功记录,
// 而库里纹丝不动 —— 点几次写几条假审计。
//
// 断言分两半,缺一不可:
//   - 声称退了多少,库里就必须真的写着退了多少(收敛);
//   - 收敛不了就必须报错、必须回 0,绝不能报一个金额(不许假报)。
func TestConfirmRefundSettledNeverReportsAPhantomRefund(t *testing.T) {
	const amount = int64(500000)

	cases := []struct {
		name          string
		seedFeeStatus string
		seedRefund    int64
		orderStatus   int8
		wantErr       bool
		wantRefunded  int64
		wantFeeStatus string
	}{
		{
			// 这条就是缺陷本身:资金单早已 Success,LocalCommit 从没跑过。
			name: "遗留错位单:资金单已成功、记录仍写着已扣费", seedFeeStatus: FeeStatusCharged,
			orderStatus:  qymodel.StatusSuccess,
			wantRefunded: amount, wantFeeStatus: FeeStatusRefunded,
		},
		{
			name: "余额不足只扣了一部分的记录同样要收敛", seedFeeStatus: FeeStatusTruncated,
			orderStatus:  qymodel.StatusSuccess,
			wantRefunded: amount, wantFeeStatus: FeeStatusRefunded,
		},
		{
			name: "LocalCommit 已经写过:回读到它写的金额", seedFeeStatus: FeeStatusRefunded,
			seedRefund: amount, orderStatus: qymodel.StatusSuccess,
			wantRefunded: amount, wantFeeStatus: FeeStatusRefunded,
		},
		{
			// 主库已生效但扩展库回写失败,单据留在 pending 等补偿任务。
			// 钱大概率动了,但此刻不能对管理员声称"退款完成"。
			name: "资金单尚未落定", seedFeeStatus: FeeStatusCharged,
			orderStatus: qymodel.StatusPending,
			wantErr:     true, wantFeeStatus: FeeStatusCharged,
		},
		{
			name: "资金单已被冲正", seedFeeStatus: FeeStatusCharged,
			orderStatus: qymodel.StatusReversed,
			wantErr:     true, wantFeeStatus: FeeStatusCharged,
		},
		{
			// 影子模式的记录从来没被扣过钱,markRecordRefunded 的条件挡住它。
			// 挡住之后必须报错,否则就是给一笔没发生的扣费报了退款。
			name: "记录处在不可退状态", seedFeeStatus: FeeStatusShadow,
			orderStatus: qymodel.StatusSuccess,
			wantErr:     true, wantFeeStatus: FeeStatusShadow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newFundDB(t)
			require.NoError(t, gdb.Create(&Record{
				Id: 1, RecNo: "vr_req1_9", UserId: 88, Status: RecordRevoked,
				FeeQuota: amount, FeeStatus: tc.seedFeeStatus, RefundQuota: tc.seedRefund,
				CreatedAt: common.GetTimestamp(),
			}).Error)
			order := seedRefundOrder(t, gdb, "VF-1", "vr_req1_9", tc.orderStatus, amount)

			// 调用方手上是一份点击之前读到的旧快照。
			rec := &Record{Id: 1, RecNo: "vr_req1_9", UserId: 88,
				FeeQuota: amount, FeeStatus: FeeStatusCharged}
			got, err := confirmRefundSettled(gdb, rec, order, amount)

			if tc.wantErr {
				require.Error(t, err, "收敛不了就必须报错,绝不能对外声称退款成功")
				assert.Zero(t, got, "报不出金额时必须回 0 —— 调用方拿它写审计")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantRefunded, got,
					"返回值必须是库里真实的 refund_quota,不是本次算出来的应退额")
			}

			var stored Record
			require.NoError(t, gdb.Where("id = ?", 1).Take(&stored).Error)
			assert.Equal(t, tc.wantFeeStatus, stored.FeeStatus,
				"声称退了多少,库里就必须真的写着退了多少")
		})
	}
}

// TestReconcileRefundStatesConvergesLegacyRows 覆盖遗留错位行的自动收敛。
//
// 管理员再点一次只能救到他碰过的那几条,剩下的行会让用户端的"违规扣费"一直把
// 已经退回的钱算进去。收敛条件必须严格:只认本模块自己的退款单(kind + idem_scope),
// 且只认已经确定生效(Success)的那些 —— 放宽任何一条,都会把一笔还没落定、
// 甚至根本不是退款的操作写成"已退款"。
func TestReconcileRefundStatesConvergesLegacyRows(t *testing.T) {
	gdb := newFundDB(t)
	now := common.GetTimestamp()

	seedRec := func(id int64, recNo, feeStatus string) {
		require.NoError(t, gdb.Create(&Record{
			Id: id, RecNo: recNo, UserId: 88, Status: RecordRevoked,
			FeeQuota: 500000, FeeStatus: feeStatus, CreatedAt: now,
		}).Error)
	}
	seedRec(1, "vr_stale", FeeStatusCharged)      // 错位行,必须被收敛
	seedRec(2, "vr_pending", FeeStatusCharged)    // 资金单还没落定,不能动
	seedRec(3, "vr_done", FeeStatusRefunded)      // 已经收敛过,不该重写
	seedRec(4, "vr_shadow", FeeStatusShadow)      // 从没扣过钱
	seedRec(5, "vr_no_order", FeeStatusCharged)   // 压根没有退款单
	seedRec(6, "vr_other_kind", FeeStatusCharged) // 幂等键同名但不是违规退款单

	seedRefundOrder(t, gdb, "VF-1", "vr_stale", qymodel.StatusSuccess, 500000)
	seedRefundOrder(t, gdb, "VF-2", "vr_pending", qymodel.StatusPending, 500000)
	seedRefundOrder(t, gdb, "VF-3", "vr_done", qymodel.StatusSuccess, 500000)
	seedRefundOrder(t, gdb, "VF-4", "vr_shadow", qymodel.StatusSuccess, 500000)
	require.NoError(t, gdb.Create(&qymodel.FundOrder{
		OrderNo: "TR-9", Kind: qymodel.KindTransfer, Status: qymodel.StatusSuccess,
		IdemScope: "transfer", IdemKey: "vr_other_kind", UserId: 88,
		AmountQuota: 500000, CreatedAt: now, UpdatedAt: now,
	}).Error)

	// 跑两遍:第二遍必须完全空转,否则收敛动作不是幂等的。
	reconcileRefundStates(context.Background(), gdb)
	reconcileRefundStates(context.Background(), gdb)

	want := map[string]struct {
		status string
		quota  int64
	}{
		"vr_stale":      {FeeStatusRefunded, 500000},
		"vr_pending":    {FeeStatusCharged, 0},
		"vr_done":       {FeeStatusRefunded, 0}, // 本来就是 refunded,不该被重写
		"vr_shadow":     {FeeStatusShadow, 0},
		"vr_no_order":   {FeeStatusCharged, 0},
		"vr_other_kind": {FeeStatusCharged, 0},
	}
	for recNo, exp := range want {
		var got Record
		require.NoError(t, gdb.Where("rec_no = ?", recNo).Take(&got).Error)
		assert.Equal(t, exp.status, got.FeeStatus, "记录 "+recNo+" 的扣费状态")
		assert.Equal(t, exp.quota, got.RefundQuota, "记录 "+recNo+" 的退款金额")
	}
}

// ─────────────────── deferred 封禁的人工出口(OLD-5) ───────────────────

// TestClaimUnbanCoversEveryStatusNeedingAHumanExit 是 OLD-5 的回归。
//
// tasks.go 的注释写着"deferred 行由管理员在封禁列表里处理",但受理集合里没有
// deferred:管理员在列表上看得见这一行,点解除只会拿到"该用户没有待解除的违规封禁"。
// 于是速率闸("先让人看一眼")实际退化成"延迟封号" —— 唯一出口是该用户再违规一次
// 被自动提升执行,人根本没有说"不"的机会。
//
// 同时锁定返回的是**了结之前**的状态:unbanUser 靠它决定要不要去主库放人,而
// deferred 从来没有禁用过任何人,对它调 enableUserAfterUnban 会把一个正因别的原因
// 被停用的账号直接放出来。
func TestClaimUnbanCoversEveryStatusNeedingAHumanExit(t *testing.T) {
	cases := []struct {
		name       string
		seed       string
		wantClaim  bool
		wantEnable bool // 了结后是否应该去主库放人
	}{
		{"已封禁", BanBanned, true, true},
		{"已认领但主库六步未完成", BanPending, true, true},
		{"封禁执行失败", BanFailed, true, true},
		// 这条在修复前拿不到行:受理集合只有 banned/pending/failed。
		{"速率闸挡下的 deferred", BanDeferred, true, false},
		{"已解除:没有待了结的行", BanUnbanned, false, false},
		{"目标是 root 或已禁用,无需处理", BanSkipped, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newBanDB(t)
			require.NoError(t, gdb.Create(&Ban{
				Id: 1, UserId: 88, BanCycle: 0, Status: tc.seed,
				HitCountAt: 3, Threshold: 3, CreatedAt: common.GetTimestamp(),
			}).Error)

			ban, err := claimUnban(gdb, 88, "误判", 7)
			if !tc.wantClaim {
				require.Error(t, err, "不在可了结集合里的行不该被当成解封目标")
				var stored Ban
				require.NoError(t, gdb.Where("id = ?", 1).Take(&stored).Error)
				assert.Equal(t, tc.seed, stored.Status, "失败时不得改写任何状态")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, ban)
			assert.Equal(t, tc.seed, ban.Status,
				"返回的必须是了结前的状态,调用方靠它决定要不要动主库用户行")
			assert.Equal(t, tc.wantEnable, ban.Status != BanDeferred,
				"deferred 从未禁用过任何人,不能顺手把账号放出来")

			var stored Ban
			require.NoError(t, gdb.Where("id = ?", 1).Take(&stored).Error)
			assert.Equal(t, BanUnbanned, stored.Status)
			assert.Equal(t, 7, stored.UnbannedBy)
			assert.NotZero(t, stored.UnbannedAt)
			assert.Equal(t, "误判", stored.UnbanNote)

			// 再点一次:行已终态,必须报"没有待解除的封禁"而不是又了结一遍。
			_, err = claimUnban(gdb, 88, "误判", 7)
			assert.Error(t, err)
		})
	}
}
