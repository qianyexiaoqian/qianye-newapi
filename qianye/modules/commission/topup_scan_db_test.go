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

// 本文件是 A4 的调度层回归。
//
// A4 的纯函数(lowWaterAfter / scanBatch)已有测试,但那两条测不到
// runTopupScan 到底把哪个数写进了游标。把 `next := lowWaterAfter(out)`
// 改回 `next := out.MaxScanned`,纯函数测试仍然全绿,而那笔计佣失败的
// 充值订单就此永久漏发 —— 扫描语句是 `WHERE id > low`,单向不可回头。
// 这里让整条链路(读游标 → 扫主库 → 计佣 → 写游标)真跑一遍。

// seedTopUp 插一笔充值订单。
func seedTopUp(t *testing.T, mdb *gorm.DB, id, userId int, amount int64, status string) {
	t.Helper()
	require.NoError(t, mdb.Create(&model.TopUp{
		Id:              id,
		UserId:          userId,
		Amount:          amount,
		TradeNo:         "T" + itoa(id),
		PaymentProvider: model.PaymentProviderCreem, // Amount 本身即额度,不再乘 QuotaPerUnit
		CreateTime:      common.GetTimestamp() - 60,
		Status:          status,
	}).Error)
}

// TestRunTopupScanPinsCursorBeforeFailedOrder 锁定低水位游标的落库值。
//
// 场景照抄审计报告 A4:本批含三笔已成功的充值,中间那笔计佣写库失败
// (这里用"扩展库那张表不可写"来代替生产上的 Deadlock / Lock wait timeout ——
// 两者都不是 isConnLevelError 认的连接级错误,熔断不会打开,扫描照常跑完整批)。
// 游标必须停在失败订单之前,而不是推到本批最大 id。
func TestRunTopupScanPinsCursorBeforeFailedOrder(t *testing.T) {
	t.Run("游标停在计佣失败的订单之前", func(t *testing.T) {
		gdb := newTestDB(t)
		mdb := useMainDB(t, &model.TopUp{})
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)

		require.NoError(t, gdb.Create(&qymodel.KV{
			K: topupCursorKey, V: "100", UpdatedAt: common.GetTimestamp(),
		}).Error)
		// 费率必须非零,否则 gross 为零、writeAccrual 一进门就返回,
		// "计佣失败"这个前提根本不成立,测试会变成永真。
		setSettingOverride(t, gdb, keyTopupRatePercent, "5")
		require.Positive(t, effective().TopupRateUnits, "前提:充值返佣费率非零")

		// 901 没有邀请人 → accrueOneShot 一进门就返回 nil,不写任何库;
		// 900 有邀请人 42 → 会走到 writeAccrual。
		getInviterCache().Set(901, inviterEntry{})
		getInviterCache().Set(900, inviterEntry{
			InviterId:      42,
			InviteeName:    "u900",
			InviteeCreated: common.GetTimestamp() - 30*86400,
		})

		seedTopUp(t, mdb, 101, 901, 0, common.TopUpStatusSuccess)    // 基数为零,直接放行
		seedTopUp(t, mdb, 102, 900, 1000, common.TopUpStatusSuccess) // 这笔会写库失败
		seedTopUp(t, mdb, 103, 901, 0, common.TopUpStatusSuccess)

		// 让 102 的计佣写入失败:表不在了。
		require.NoError(t, gdb.Migrator().DropTable(&Accrual{}))

		scannedBefore, heldBefore := topupScanned.Load(), topupHeld.Load()
		runTopupScan(context.Background())

		assert.EqualValues(t, 3, topupScanned.Load()-scannedBefore, "三笔订单都应被扫到")
		assert.EqualValues(t, 1, topupHeld.Load()-heldBefore, "游标被钉住必须留下可观测的痕迹")
		assert.EqualValues(t, 101, peekTopupCursor(),
			"游标越过计佣失败的 102 = 那笔返佣永远不会被重扫,唯一索引只防重复不防遗漏")
	})

	t.Run("全部成功时游标推进到本批最大 id", func(t *testing.T) {
		// 反向约束:没有失败订单时游标必须照常前进,否则扫描会原地打转。
		gdb := newTestDB(t)
		mdb := useMainDB(t, &model.TopUp{})
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)

		require.NoError(t, gdb.Create(&qymodel.KV{
			K: topupCursorKey, V: "100", UpdatedAt: common.GetTimestamp(),
		}).Error)
		getInviterCache().Set(901, inviterEntry{})

		seedTopUp(t, mdb, 101, 901, 0, common.TopUpStatusSuccess)
		seedTopUp(t, mdb, 102, 901, 0, common.TopUpStatusSuccess)
		seedTopUp(t, mdb, 103, 901, 0, common.TopUpStatusSuccess)

		runTopupScan(context.Background())
		assert.EqualValues(t, 103, peekTopupCursor())
	})

	t.Run("窗口内的未决订单同样把游标钉住", func(t *testing.T) {
		gdb := newTestDB(t)
		mdb := useMainDB(t, &model.TopUp{})
		useConfig(t, commissionConfig(0))
		useMoneyGlobals(t, 7.3, 500000)

		require.NoError(t, gdb.Create(&qymodel.KV{
			K: topupCursorKey, V: "100", UpdatedAt: common.GetTimestamp(),
		}).Error)
		getInviterCache().Set(901, inviterEntry{})

		seedTopUp(t, mdb, 101, 901, 0, common.TopUpStatusSuccess)
		seedTopUp(t, mdb, 102, 901, 0, common.TopUpStatusPending) // 还没付款,不能越过
		seedTopUp(t, mdb, 103, 901, 0, common.TopUpStatusSuccess)

		runTopupScan(context.Background())
		assert.EqualValues(t, 101, peekTopupCursor(),
			"订单先以 pending 插入、之后才转 success,越过它就再也扫不到")
	})
}
