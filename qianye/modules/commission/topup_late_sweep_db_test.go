package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件锁的是「迟付回收」这一趟。
//
// 前向游标为了不被永不支付的死单钉死,会放过窗口外的 pending 单。放过之后
// 那笔订单仍然可以被网关合法回调付成 success —— epay 对订单年龄零校验 ——
// 而扫描语句 `WHERE id > low` 单向不可回头,佣金就此永久丢失,
// 且账本三张表全部自洽,站内没有任何一处能看出少了一行。
//
// 判据必须打在**计佣行**上:游标值、扫描计数、日志都可能正常,
// 唯一能证明这笔佣金没丢的东西是 qy_commission_accrual 里那一行。

// seedTopUpAt 插一笔可控时间的充值订单。
// 用 creem 渠道是因为它的 Amount 本身就是额度,基数换算不掺 QuotaPerUnit,
// 断言里的数字就是订单上的数字。
func seedTopUpAt(t *testing.T, mdb *gorm.DB, id, userId int, amount int64, status string, createTime, completeTime int64) {
	t.Helper()
	require.NoError(t, mdb.Create(&model.TopUp{
		Id:              id,
		UserId:          userId,
		Amount:          amount,
		TradeNo:         "T" + itoa(id),
		PaymentProvider: model.PaymentProviderCreem,
		CreateTime:      createTime,
		CompleteTime:    completeTime,
		Status:          status,
	}).Error)
}

func seedKV(t *testing.T, gdb *gorm.DB, key string, v int64) {
	t.Helper()
	require.NoError(t, gdb.Create(&qymodel.KV{
		K: key, V: itoa64(v), UpdatedAt: common.GetTimestamp(),
	}).Error)
}

func accrualByIdem(t *testing.T, gdb *gorm.DB, idem string) *Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("idem_key = ?", idem).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	require.Len(t, rows, 1, "同一笔订单只能有一行计佣")
	return &rows[0]
}

func TestSweepLateTopupsRecoversOrdersTheCursorPassed(t *testing.T) {
	t.Run("窗口外的未决订单被越过后再付款,佣金必须补上", func(t *testing.T) {
		gdb := newTestDB(t)
		mdb := useMainDB(t, &model.TopUp{})
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)
		setSettingOverride(t, gdb, keyTopupRatePercent, "10")
		require.Positive(t, effective().TopupRateUnits, "前提:充值返佣费率非零")

		now := common.GetTimestamp()
		getInviterCache().Set(900, inviterEntry{
			InviterId:      42,
			InviteeName:    "u900",
			InviteeCreated: now - 30*86400,
		})

		seedKV(t, gdb, topupCursorKey, 100)
		seedKV(t, gdb, topupSweepKey, now-3600)

		// 101:窗口外(默认 lookback 72 小时)的未决订单,游标会放过它。
		seedTopUpAt(t, mdb, 101, 900, 25_000_000, common.TopUpStatusPending, now-73*3600, 0)
		// 102:窗口内的未决订单,把游标钉在 101。
		seedTopUpAt(t, mdb, 102, 900, 25_000_000, common.TopUpStatusPending, now-60, 0)

		runTopupScan(context.Background())
		require.EqualValues(t, 101, peekTopupCursor(), "前提:游标确实越过了窗口外那笔")
		require.Nil(t, accrualByIdem(t, gdb, topupIdemKey("T101")), "此刻它还没付款,不该有计佣")

		// 网关隔了几天才把这笔合法回调打回来:订单转 success,用户额度照常到账,
		// 而它已经落在 `WHERE id > low` 的下方。
		require.NoError(t, mdb.Model(&model.TopUp{}).Where("id = ?", 101).
			Updates(map[string]any{"status": common.TopUpStatusSuccess, "complete_time": now}).Error)

		sweptBefore := topupLateSwept.Load()
		runTopupScan(context.Background())

		row := accrualByIdem(t, gdb, topupIdemKey("T101"))
		require.NotNil(t, row, "被游标越过的订单转 success 之后必须被回收计佣")
		assert.EqualValues(t, 42, row.InviterId)
		assert.EqualValues(t, 900, row.InviteeId)
		assert.EqualValues(t, 25_000_000, row.BaseQuota)
		assert.EqualValues(t, 1000, row.RateUnits)
		// 独立算式:25,000,000 × 10% = 2,500,000
		assert.Equal(t, "2500000", row.GrossAmount.String())
		assert.EqualValues(t, 1, topupLateSwept.Load()-sweptBefore)
	})

	t.Run("重复运行不会重复计佣", func(t *testing.T) {
		gdb := newTestDB(t)
		mdb := useMainDB(t, &model.TopUp{})
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)
		setSettingOverride(t, gdb, keyTopupRatePercent, "10")

		now := common.GetTimestamp()
		getInviterCache().Set(900, inviterEntry{
			InviterId: 42, InviteeName: "u900", InviteeCreated: now - 30*86400,
		})
		seedKV(t, gdb, topupCursorKey, 100)
		seedKV(t, gdb, topupSweepKey, now-3600)
		seedTopUpAt(t, mdb, 101, 900, 25_000_000, common.TopUpStatusSuccess, now-73*3600, now-30)
		// 钉住游标,让 101 一直留在 `id <= low` 的回收面里。
		seedTopUpAt(t, mdb, 102, 900, 25_000_000, common.TopUpStatusPending, now-60, 0)

		for i := 0; i < 3; i++ {
			runTopupScan(context.Background())
		}
		var n int64
		require.NoError(t, gdb.Model(&Accrual{}).Where("idem_key = ?", topupIdemKey("T101")).Count(&n).Error)
		assert.EqualValues(t, 1, n, "幂等键必须让重复回收落成同一行")
	})

	t.Run("水位线之前完成的历史订单不补返佣金", func(t *testing.T) {
		gdb := newTestDB(t)
		mdb := useMainDB(t, &model.TopUp{})
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)
		setSettingOverride(t, gdb, keyTopupRatePercent, "10")

		now := common.GetTimestamp()
		getInviterCache().Set(900, inviterEntry{
			InviterId: 42, InviteeName: "u900", InviteeCreated: now - 30*86400,
		})
		// 游标已经在 101 之上:前向扫描够不着它,能不能被翻出来完全取决于回收面。
		seedKV(t, gdb, topupCursorKey, 105)
		seedKV(t, gdb, topupSweepKey, now-3600)
		// 水位线之前就已完成:平台接入佣金之前的存量订单正是这个形状,
		// 把它们补返一遍等于凭空发钱。
		seedTopUpAt(t, mdb, 101, 900, 25_000_000, common.TopUpStatusSuccess, now-10*86400, now-7200)

		runTopupScan(context.Background())
		assert.Nil(t, accrualByIdem(t, gdb, topupIdemKey("T101")),
			"回收面只包含水位线之后完成的订单")
	})
}

// 管理员补单必须留下 complete_source 这个真判据,风控开关才有东西可认。
//
// 旧判据 payment_method == "manual" 全仓无人写入:开关打开之后
// 「管理员补单不返佣」这半边从来没生效过,而运营读到的配置注释说的是两边都堵上了。
func TestExcludedTopUpRecognizesAdminCompletedOrders(t *testing.T) {
	cfg := commissionConfig(0)
	cfg.Commission.ExcludeRedemptionAndManual = true
	useConfig(t, cfg)

	for _, tc := range []struct {
		name string
		row  model.TopUp
		want bool
	}{
		{
			name: "管理员补单被排除",
			row:  model.TopUp{PaymentMethod: "alipay", PaymentProvider: model.PaymentProviderEpay, CompleteSource: model.TopUpCompleteSourceAdmin},
			want: true,
		},
		{
			name: "同样长相的真实付款照常计佣",
			row:  model.TopUp{PaymentMethod: "alipay", PaymentProvider: model.PaymentProviderEpay},
			want: false,
		},
		{
			name: "支付方式被命名成 manual 的真实付款不能被误杀",
			row:  model.TopUp{PaymentMethod: "manual", PaymentProvider: model.PaymentProviderEpay},
			want: false,
		},
		{
			name: "余额支付永远排除,与开关无关",
			row:  model.TopUp{PaymentMethod: model.PaymentMethodBalance, PaymentProvider: model.PaymentProviderBalance},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, excludedTopUp(&tc.row))
		})
	}

	t.Run("开关关闭时管理员补单照常计佣", func(t *testing.T) {
		useConfig(t, commissionConfig(0))
		assert.False(t, excludedTopUp(&model.TopUp{
			PaymentMethod:  "alipay",
			CompleteSource: model.TopUpCompleteSourceAdmin,
		}))
	})
}
