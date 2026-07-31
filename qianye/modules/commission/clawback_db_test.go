package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 本文件是 B5 的数据库级回归。
//
// 已有的 TestSameClawbackRequestRejectsReplayedParams 只测那个纯比对函数,
// 完全测不到"manualClawback 到底会不会调它"。而 B5 的实际缺陷正是在
// 调用链上:writeAccrual 的 OnConflict{DoNothing} 冲突不报错,回读拿到旧单
// 被当成"本次新建",调用方照着**本次请求的**金额写下一条成功审计。
// 把 manualClawback 里 `if !inserted { sameClawbackRequest(...) }` 整段删掉,
// 那条纯函数测试仍然全绿。这里让 ON CONFLICT 由真实数据库执行一遍。

// TestManualClawbackDetectsIdemReplay 锁定人工冲正的幂等命中判定。
func TestManualClawbackDetectsIdemReplay(t *testing.T) {
	ctx := context.Background()

	t.Run("换了参数复用同一个幂等键必须报冲突", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})
		other := seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})

		created, err := manualClawback(ctx, origin.Id, 500, "req-x", "拒付")
		require.NoError(t, err)
		require.Equal(t, "-500", created.GrossAmount.String())
		require.EqualValues(t, origin.Id, created.RefAccrualId)
		require.EqualValues(t, -500, created.BaseQuota, "请求金额必须冻结成幂等指纹")

		// 管理员在同一个弹窗里改了目标与金额重提,client_request_id 沿用。
		_, err = manualClawback(ctx, other.Id, 9999, "req-x", "拒付")
		require.ErrorIs(t, err, ErrClawbackIdemConflict,
			"返回旧单 = 调用方会照着 9999 写下一条与账本矛盾的成功审计")

		// 资金侧必须一分没动:只有第一次那条负额行。
		var rows []Accrual
		require.NoError(t, gdb.Where("idem_scope = ?", SourceClawback).Find(&rows).Error)
		require.Len(t, rows, 1)
		assert.Equal(t, "-500", rows[0].GrossAmount.String())
	})

	t.Run("同一请求原样重放返回同一张单,不再新建", func(t *testing.T) {
		// 反向约束:合法重试(网络超时后前端重发)必须放行,否则管理员
		// 会被这个 client_request_id 永久卡住,只能换个键再冲一次 —— 那才是资损。
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})

		first, err := manualClawback(ctx, origin.Id, 300, "req-y", "拒付")
		require.NoError(t, err)
		second, err := manualClawback(ctx, origin.Id, 300, "req-y", "拒付")
		require.NoError(t, err)
		assert.Equal(t, first.Id, second.Id)
		assert.Equal(t, first.AccrualNo, second.AccrualNo)

		var n int64
		require.NoError(t, gdb.Model(&Accrual{}).
			Where("idem_scope = ?", SourceClawback).Count(&n).Error)
		assert.EqualValues(t, 1, n, "重放不得再落一条负额行")
	})

	t.Run("审计金额取账本真值而不是请求参数", func(t *testing.T) {
		// 管理员填 9999,而这个下线名下净佣金只有 500,落库的是 -500。
		// 审计写 9999 就是一条与账本矛盾的"成功"记录。
		gdb := newTestDB(t)
		useConfig(t, commissionConfig(0))

		origin := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.GrossAmount = decimal.NewFromInt(500)
		})

		created, err := manualClawback(ctx, origin.Id, 9999, "req-z", "刷单")
		require.NoError(t, err)
		assert.Equal(t, "-500", created.GrossAmount.String(), "冲正被 remaining 削到净佣金")
		assert.EqualValues(t, 500, clawbackAuditAmount(created))
		assert.EqualValues(t, -9999, created.BaseQuota,
			"指纹记的是请求说了什么,不能拿被削过的 Gross 反推")
	})
}

// clawbackRowOf 回读某个下线名下唯一的一条冲正行。
func clawbackRowOf(t *testing.T, gdb *gorm.DB, inviteeId int) Accrual {
	t.Helper()
	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ? AND source_type = ?", inviteeId, SourceClawback).
		Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 1, "下线 %d 应当恰好有一条冲正行", inviteeId)
	return rows[0]
}

// TestClawbackCopiesConsumeRateNotLatestAccrual 是 M3 的回归。
//
// 缺陷:clawback 用 `invitee_id = ? AND gross_amount > 0 ORDER BY id DESC LIMIT 1`
// 取"原单",而这一行可以是任意来源。默认配置充值 10%、消费 5%,于是
// "当天先消费、晚些时候充值"这种普通用量,就会让随后的一笔任务退款按 10%
// 去冲一笔只发过 5% 的佣金 —— 正好 2 倍超额冲正,邀请人被多扣一半。
//
// 断言必须钉在"消费退款的冲正费率 == 消费费率"上:只测 calcGross 的话,
// 把整条选行条件换回旧写法,那种测试照样全绿 —— 缺陷不在算术里,在 WHERE 里。
func TestClawbackCopiesConsumeRateNotLatestAccrual(t *testing.T) {
	ctx := context.Background()
	today := bucketDate(common.GetTimestamp())

	t.Run("当天先消费后充值时按消费费率冲正", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		consume := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.IdemScope, a.SourceType = SourceConsume, SourceConsume
			a.IdemKey = "consume:900:" + today
			a.BucketDate = today
			a.RateUnits = 500 // 5%
			a.GrossAmount = decimal.NewFromInt(500)
		})
		// 同一天晚些时候的充值行:id 更大、费率是消费的两倍。
		seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 42
			a.RateUnits = 1000 // 10%
			a.GrossAmount = decimal.NewFromInt(1000)
		})

		require.NoError(t, clawback(ctx, 900, 900, "clawback:task:t-1", "t-1", "task refund"))

		row := clawbackRowOf(t, gdb, 900)
		assert.Equal(t, 500, row.RateUnits, "退款来自消费路径,冲正费率必须是消费费率")
		assert.Equal(t, "-45", row.GrossAmount.String(),
			"900 × 5% = 45;按充值行的 10% 冲会是 90,正好 2 倍超额且不可更正")
		assert.EqualValues(t, consume.Id, row.RefAccrualId,
			"溯源必须指向被退款的那条消费行,挂到充值行会让管理端的冲正溯源全错")
	})

	t.Run("优先命中被退款那一天的日聚合桶", func(t *testing.T) {
		// 升级前落的历史消费行没有 bucket_date,且 id 可能更大。
		// 只按 id desc 取会拿到它冻结的旧费率。
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))

		bucket := seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.IdemScope, a.SourceType = SourceConsume, SourceConsume
			a.IdemKey = "consume:900:" + today
			a.BucketDate = today
			a.RateUnits = 500
			a.GrossAmount = decimal.NewFromInt(500)
		})
		seedAccrual(t, gdb, 2, func(a *Accrual) {
			a.InviterId = 42
			a.IdemScope, a.SourceType = SourceConsume, SourceConsume
			a.IdemKey = "consume:900:legacy"
			a.RateUnits = 800 // 8%,升级前的旧费率
			a.GrossAmount = decimal.NewFromInt(800)
		})

		require.NoError(t, clawback(ctx, 900, 900, "clawback:task:t-2", "t-2", "task refund"))

		row := clawbackRowOf(t, gdb, 900)
		assert.Equal(t, 500, row.RateUnits, "必须冲被退款那一天的桶,不是历史上任意一条消费行")
		assert.Equal(t, "-45", row.GrossAmount.String())
		assert.EqualValues(t, bucket.Id, row.RefAccrualId)
	})

	t.Run("没有消费行时回落当前消费费率而不是充值行的费率", func(t *testing.T) {
		gdb := newTestDB(t)
		useConfig(t, commissionRateConfig("10", "5"))
		mdb := useMainDB(t, &model.User{})
		require.NoError(t, mdb.Create(&model.User{
			Id: 900, Username: "invitee900", InviterId: 42, Group: "default",
		}).Error)

		// 名下只有充值佣金。冲正仍然发生(净佣金够冲),但绝不能借用 10%。
		seedAccrual(t, gdb, 1, func(a *Accrual) {
			a.InviterId = 42
			a.RateUnits = 1000
			a.GrossAmount = decimal.NewFromInt(1000)
		})

		require.NoError(t, clawback(ctx, 900, 900, "clawback:task:t-3", "t-3", "task refund"))

		row := clawbackRowOf(t, gdb, 900)
		assert.Equal(t, 500, row.RateUnits, "取不到消费行时回落的必须是当前消费费率(YAML 的 5%)")
		assert.Equal(t, "-45", row.GrossAmount.String())
		assert.EqualValues(t, 42, row.InviterId)
		assert.EqualValues(t, 0, row.RefAccrualId, "没有可指向的原单时不得随便挂一行,否则溯源是假的")
	})
}
