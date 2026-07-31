package commission

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLowWaterAfterNeverSkipsFailedTopUp 锁定低水位游标的推进口径。
//
// 缺陷复现:扫描语句是 `WHERE id > low`,单向不可回头。一次死锁 / 锁等待超时
// (isConnLevelError 不认这两类,熔断不会打开、扫描照常跑完整批)会让某一笔
// 订单计佣失败,而旧实现照样把游标推到 MaxScanned —— 那笔返佣再也不会被扫到。
// 每一条 wantNext 都必须 <= MinFailed-1,否则就是永久漏单。
func TestLowWaterAfterNeverSkipsFailedTopUp(t *testing.T) {
	cases := []struct {
		name     string
		outcome  scanOutcome
		wantNext int64
	}{
		{"全部成功且无未决", scanOutcome{MaxScanned: 1050}, 1050},
		{"窗口内有未决订单", scanOutcome{MaxScanned: 1050, MinPending: 1020}, 1019},
		{"有计佣失败", scanOutcome{MaxScanned: 1050, MinFailed: 1000}, 999},
		{"失败早于未决", scanOutcome{MaxScanned: 1050, MinPending: 1030, MinFailed: 1000}, 999},
		{"未决早于失败", scanOutcome{MaxScanned: 1050, MinPending: 1005, MinFailed: 1040}, 1004},
		{"批内第一行就失败", scanOutcome{MaxScanned: 1050, MinFailed: 1}, 0},
		{"空批", scanOutcome{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lowWaterAfter(tc.outcome)
			assert.Equal(t, tc.wantNext, got)
			if tc.outcome.MinFailed > 0 {
				assert.LessOrEqual(t, got, tc.outcome.MinFailed-1,
					"游标越过计佣失败的订单 = 那笔佣金永久丢失")
			}
			if tc.outcome.MinPending > 0 {
				assert.LessOrEqual(t, got, tc.outcome.MinPending-1)
			}
			assert.LessOrEqual(t, got, tc.outcome.MaxScanned, "游标不得超过本批实际扫到的最大 id")
		})
	}
}

// TestScanBatchHoldsCursorOnlyForRealFailures 覆盖批内循环。
//
// 关键的两条反向断言:口径排除(余额支付)与零基数订单**不算失败**,
// 否则一个永远返不了佣的订单会把整条扫描线永久钉死。
func TestScanBatchHoldsCursorOnlyForRealFailures(t *testing.T) {
	const lookback = int64(1000)
	boom := errors.New("Error 1213: Deadlock found when trying to get lock")

	rows := []model.TopUp{
		{Id: 100, Status: common.TopUpStatusSuccess, TradeNo: "T100"},
		{Id: 101, Status: common.TopUpStatusSuccess, TradeNo: "T101"},
		{Id: 102, Status: common.TopUpStatusPending, TradeNo: "T102", CreateTime: lookback + 1},
		{Id: 103, Status: common.TopUpStatusPending, TradeNo: "T103", CreateTime: lookback - 1},
		{Id: 104, Status: common.TopUpStatusSuccess, TradeNo: "T104"},
	}

	t.Run("全部成功时只受未决订单约束", func(t *testing.T) {
		var seen []int
		out := scanBatch(rows, lookback, func(r *model.TopUp) error {
			seen = append(seen, r.Id)
			return nil
		})
		assert.Equal(t, []int{100, 101, 104}, seen, "只对已成功的订单计佣")
		assert.EqualValues(t, 104, out.MaxScanned)
		assert.EqualValues(t, 102, out.MinPending, "窗口外的 103 是死单,不守候")
		assert.EqualValues(t, 0, out.MinFailed)
		assert.EqualValues(t, 101, lowWaterAfter(out))
	})

	t.Run("失败订单把游标钉在它之前", func(t *testing.T) {
		out := scanBatch(rows, lookback, func(r *model.TopUp) error {
			if r.Id == 101 {
				return boom
			}
			return nil
		})
		require.EqualValues(t, 101, out.MinFailed)
		assert.EqualValues(t, 100, lowWaterAfter(out))
	})

	t.Run("取首个失败而非最后一个", func(t *testing.T) {
		out := scanBatch(rows, lookback, func(r *model.TopUp) error { return boom })
		assert.EqualValues(t, 100, out.MinFailed)
		assert.EqualValues(t, 99, lowWaterAfter(out), "下一轮必须从 100 重新扫起")
	})
}

// TestAccrueTopUpTreatsExclusionAsSuccess 确认"不该返佣"与"返佣失败"分得开。
//
// 两者都不产生计佣行,但前者必须让游标越过去:余额支付订单永远不会返佣,
// 把它当失败会让扫描停在那里再也不动。这条不碰任何数据库 ——
// excludedTopUp 与 topUpBaseQuota 都在任何 DB 访问之前短路。
func TestAccrueTopUpTreatsExclusionAsSuccess(t *testing.T) {
	original := common.QuotaPerUnit
	common.QuotaPerUnit = 500000
	defer func() { common.QuotaPerUnit = original }()

	cases := []struct {
		name  string
		topUp model.TopUp
	}{
		{"余额支付订单", model.TopUp{Id: 1, PaymentProvider: model.PaymentProviderBalance, Amount: 10}},
		{"零基数订单", model.TopUp{Id: 2, PaymentProvider: model.PaymentProviderCreem, Amount: 0, Money: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NoError(t, accrueTopUp(context.Background(), &tc.topUp))
		})
	}
}
