package channelops

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 批量删除:一条失败不牵连已经删掉的那几条,并且逐条说清楚。
//
// 这是需求里点名的形状:「选了 20 个渠道,第 7 个删失败。不能整体回滚
// (前 6 个已经删了),也不能报『全部成功』。」
//
// 上游 model.BatchDeleteChannels 把整批放进同一个事务,任何一条出错就全量回滚,
// 而且只回一个整数 —— 两条都不满足。这条用例同时断言两件事:
// 好的那两条**真的从库里消失了**(证明没有回滚),坏的那条被单独列出来。
func TestBatchDeleteReportsEachChannelAndKeepsSucceededOnesDeleted(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 1, "alpha", common.ChannelStatusEnabled)
	seedChannel(t, main, 2, "beta", common.ChannelStatusEnabled)

	// 3 号从来不存在 —— 与"另一个管理员刚把它删了"在库里是同一个形状。
	res := resultOf(t, call(t, "/channels/batch-delete",
		`{"ids":[1,3,2]}`, adminBatchDelete))

	assert.Equal(t, 3, res.Total)
	assert.Equal(t, 2, res.Succeeded)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, outcomeOK, itemFor(t, res, 1).Outcome)
	assert.Equal(t, outcomeOK, itemFor(t, res, 2).Outcome)

	bad := itemFor(t, res, 3)
	assert.Equal(t, outcomeFailed, bad.Outcome)
	assert.Equal(t, itemCodeNotFound, bad.Code)

	// 失败那条**没有**让前两条回滚。
	var left int64
	require.NoError(t, main.Model(&model.Channel{}).Count(&left).Error)
	assert.Zero(t, left, "1 与 2 必须真的删掉了,失败的 3 不能把它们拖回来")

	// 成功的那两条带着名字回来 —— 失败列表里只有 id 的话,
	// 管理员得回列表页逐个对照才知道挂掉的是哪个渠道。
	assert.Equal(t, "alpha", itemFor(t, res, 1).Name)
}

// 删渠道必须同时清掉 abilities,否则物化路由表里留下指向已删渠道的行。
//
// 上游 (*Channel).Delete() 是两条独立语句,第二条失败就会留下孤儿行;
// 本模块把它们放进同一个事务。这条用例锁的是"删完之后 abilities 一行不剩"。
func TestBatchDeleteAlsoRemovesAbilities(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 1, "alpha", common.ChannelStatusEnabled)
	seedChannel(t, main, 2, "beta", common.ChannelStatusEnabled)

	res := resultOf(t, call(t, "/channels/batch-delete",
		`{"ids":[1]}`, adminBatchDelete))
	require.Equal(t, 1, res.Succeeded)

	orphans := make([]int, 0, 2)
	require.NoError(t, main.Model(&model.Ability{}).
		Where("channel_id = ?", 1).Pluck("channel_id", &orphans).Error)
	assert.Empty(t, orphans, "已删渠道不能在 abilities 里留下行,否则路由表会指向不存在的渠道")

	// 没被选中的那个渠道的 abilities 一行都不能少。
	var kept int64
	require.NoError(t, main.Model(&model.Ability{}).
		Where("channel_id = ?", 2).Count(&kept).Error)
	assert.EqualValues(t, 1, kept)
}

// 批量启用:"本来就是启用的"必须报 skipped,不能混进失败数。
//
// 这是上游那条链路的实际缺陷:model.UpdateChannelStatus 对"已经是目标状态"
// 与"渠道不存在 / 保存失败"返回的都是 false,controller.BatchUpdateChannelStatus
// 把两者一起算成"没改动",前端再拿 len(ids) 一减 —— 于是全站渠道本来就全开时
// 点一次「批量启用」会显示成「N 个渠道启用失败」,管理员会去排查一个不存在的故障。
func TestBatchEnableCountsAlreadyEnabledAsSkippedNotFailed(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 1, "off", common.ChannelStatusManuallyDisabled)
	seedChannel(t, main, 2, "already-on", common.ChannelStatusEnabled)

	res := resultOf(t, call(t, "/channels/batch-status",
		`{"ids":[1,2],"status":1}`, adminBatchStatus))

	assert.Equal(t, 1, res.Succeeded)
	assert.Equal(t, 1, res.Skipped)
	assert.Zero(t, res.Failed, "已经是目标状态不是失败")
	assert.Equal(t, outcomeOK, itemFor(t, res, 1).Outcome)
	assert.Equal(t, outcomeSkipped, itemFor(t, res, 2).Outcome)

	var got model.Channel
	require.NoError(t, main.Where("id = ?", 1).First(&got).Error)
	assert.Equal(t, common.ChannelStatusEnabled, got.Status)
}

// 批量停用与启用对称:两个方向共用同一个端点、同一套分档。
func TestBatchDisableFlipsStatusAndSkipsAlreadyDisabled(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 1, "on", common.ChannelStatusEnabled)
	seedChannel(t, main, 2, "already-off", common.ChannelStatusManuallyDisabled)

	res := resultOf(t, call(t, "/channels/batch-status",
		`{"ids":[1,2],"status":2}`, adminBatchStatus))

	assert.Equal(t, 1, res.Succeeded)
	assert.Equal(t, 1, res.Skipped)
	assert.Zero(t, res.Failed)

	var got model.Channel
	require.NoError(t, main.Where("id = ?", 1).First(&got).Error)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, got.Status)
}

// 自动停用(status=3)不允许从管理端批量写入:那是系统探测到上游故障时打的标记,
// 由人手工写进去会让"这个渠道是被谁停的"永久说不清。与上游
// isManageableChannelStatus 同口径。
func TestBatchStatusRejectsNonManageableStatus(t *testing.T) {
	newQyDB(t)
	newMainDB(t)

	res := call(t, "/channels/batch-status", `{"ids":[1],"status":3}`, adminBatchStatus)
	assert.Equal(t, errBadStatus.Code, codeOf(t, res))
}

// 重置统计:只清 used_quota 时,balance 与它的时间戳一个字节都不能动。
//
// 两个字段的语义完全不同(见 adminBatchResetUsage 的注释),
// 把它们绑在一起清等于让管理员在不知情的情况下抹掉另一样东西。
func TestBatchResetClearsUsedQuotaOnlyWhenBalanceNotRequested(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 1, Name: "alpha", Status: common.ChannelStatusEnabled,
		UsedQuota: 12345, Balance: 6.25, BalanceUpdatedTime: 1700000000,
	}).Error)

	res := resultOf(t, call(t, "/channels/batch-reset-usage",
		`{"ids":[1],"reset_used_quota":true,"reset_balance":false}`, adminBatchResetUsage))
	assert.Equal(t, 1, res.Succeeded)

	var got model.Channel
	require.NoError(t, main.Where("id = ?", 1).First(&got).Error)
	assert.EqualValues(t, 0, got.UsedQuota)
	assert.EqualValues(t, 6.25, got.Balance, "没勾余额就不许动余额")
	assert.EqualValues(t, 1700000000, got.BalanceUpdatedTime)
}

// 勾了余额:balance 与 balance_updated_time 必须一起清。
//
// 只清数值而留下旧时间戳,页面上会写着「刚刚更新:余额 0」—— 那是一句假话,
// 比不清更糟。
func TestBatchResetClearsBalanceTimestampTogetherWithBalance(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 1, Name: "alpha", Status: common.ChannelStatusEnabled,
		UsedQuota: 7, Balance: 6.25, BalanceUpdatedTime: 1700000000,
	}).Error)

	res := resultOf(t, call(t, "/channels/batch-reset-usage",
		`{"ids":[1],"reset_used_quota":false,"reset_balance":true}`, adminBatchResetUsage))
	assert.Equal(t, 1, res.Succeeded)

	var got model.Channel
	require.NoError(t, main.Where("id = ?", 1).First(&got).Error)
	assert.EqualValues(t, 0, got.Balance)
	assert.EqualValues(t, 0, got.BalanceUpdatedTime,
		"余额清零必须连时间戳一起清,否则页面在说「刚查过,就是 0」")
	assert.EqualValues(t, 7, got.UsedQuota, "没勾已用额度就不许动它")
}

// 一项都没勾必须整批 400,不能静默变成一次空操作。
//
// 空操作会回一个 succeeded=N 的绿色报告,而库里什么都没变 —— 那正是
// "全部失败却回 200" 的同族缺陷。
func TestBatchResetRejectsRequestWithNothingSelected(t *testing.T) {
	newQyDB(t)
	newMainDB(t)

	res := call(t, "/channels/batch-reset-usage", `{"ids":[1]}`, adminBatchResetUsage)
	assert.Equal(t, errNothingToReset.Code, codeOf(t, res))
}

// 逐条结局的完整口径:不存在的 id、本来就是 0、真的有得清,同一批里各归各档。
//
// 「空选」与「一项都没勾」这两档是整批 4xx,不进这张表 —— 它们连一条库都没碰,
// 与逐条结局不是同一种东西(见下面两条单独的用例)。
func TestBatchResetClassifiesEachChannel(t *testing.T) {
	cases := []struct {
		name        string
		seed        map[int]int64 // 渠道 id → 清零前的 used_quota
		ids         []int
		wantOK      int
		wantSkipped int
		wantFailed  int
		wantCleared int64
		wantItem    map[int]string // id → 期望结局
	}{
		{
			name:        "全部有得清",
			seed:        map[int]int64{1: 100, 2: 250},
			ids:         []int{1, 2},
			wantOK:      2,
			wantCleared: 350,
			wantItem:    map[int]string{1: outcomeOK, 2: outcomeOK},
		},
		{
			name:        "部分成功:一个清掉、一个本来就是 0、一个不存在",
			seed:        map[int]int64{1: 100, 2: 0},
			ids:         []int{1, 2, 404},
			wantOK:      1,
			wantSkipped: 1,
			wantFailed:  1,
			wantCleared: 100,
			wantItem: map[int]string{
				1: outcomeOK, 2: outcomeSkipped, 404: outcomeFailed,
			},
		},
		{
			name:        "选中的全都不存在",
			seed:        map[int]int64{},
			ids:         []int{404, 405},
			wantFailed:  2,
			wantCleared: 0,
			wantItem:    map[int]string{404: outcomeFailed, 405: outcomeFailed},
		},
		{
			name:        "选中的全都已经是 0",
			seed:        map[int]int64{1: 0, 2: 0},
			ids:         []int{1, 2},
			wantSkipped: 2,
			wantCleared: 0,
			wantItem:    map[int]string{1: outcomeSkipped, 2: outcomeSkipped},
		},
		{
			name:        "重复 id 只算一次",
			seed:        map[int]int64{1: 70},
			ids:         []int{1, 1, 1},
			wantOK:      1,
			wantCleared: 70,
			wantItem:    map[int]string{1: outcomeOK},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newQyDB(t)
			main := newMainDB(t)
			for id, used := range tc.seed {
				require.NoError(t, main.Create(&model.Channel{
					Id: id, Name: "ch", Status: common.ChannelStatusEnabled,
					UsedQuota: used,
				}).Error)
			}

			got := resetResultOf(t, call(t, "/channels/batch-reset-usage",
				resetBody(tc.ids, true, false), adminBatchResetUsage))

			assert.Equal(t, tc.wantOK, got.Succeeded)
			assert.Equal(t, tc.wantSkipped, got.Skipped)
			assert.Equal(t, tc.wantFailed, got.Failed)
			assert.Equal(t, tc.wantCleared, got.ClearedUsedQuota,
				"合计必须等于库里真正被覆盖掉的那些值之和")
			for id, want := range tc.wantItem {
				assert.Equal(t, want, itemFor(t, got.batchResult, id).Outcome, "id=%d", id)
			}

			// 库里逐条核对:被判成 ok 的必须真的归零,其余一个字节都不许动。
			for id, before := range tc.seed {
				var after model.Channel
				require.NoError(t, main.Where("id = ?", id).First(&after).Error)
				if tc.wantItem[id] == outcomeOK {
					assert.Zero(t, after.UsedQuota, "id=%d 判成 ok 就必须真的清零", id)
					continue
				}
				assert.Equal(t, before, after.UsedQuota, "id=%d 没被清就不许动", id)
			}
		})
	}
}

// 审计必须回答「各自清掉多少」,而不只是「清了几个」。
//
// 只记 id 与计数的话,事后要回答"这个渠道被抹掉的是 3 块钱还是 3 万块"就只剩猜,
// 而 used_quota 一旦清零在 channels 表里再也无法反查(日志表里还有逐条流水,
// 但列上那个累计值回不来)。合计与逐条明细都要有:合计是成本核算真正要减掉的
// 那个数,明细回答的是"这笔减在谁头上"。
func TestBatchResetAuditRecordsPerChannelClearedAmount(t *testing.T) {
	qy := newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 11, Name: "big", Status: common.ChannelStatusEnabled, UsedQuota: 290275000,
	}).Error)
	require.NoError(t, main.Create(&model.Channel{
		Id: 12, Name: "small", Status: common.ChannelStatusEnabled, UsedQuota: 500,
	}).Error)
	require.NoError(t, main.Create(&model.Channel{
		Id: 13, Name: "clean", Status: common.ChannelStatusEnabled, UsedQuota: 0,
	}).Error)

	got := resetResultOf(t, call(t, "/channels/batch-reset-usage",
		resetBody([]int{11, 12, 13}, true, false), adminBatchResetUsage))
	require.Equal(t, 2, got.Succeeded)

	rows := auditRows(t, qy)
	require.Len(t, rows, 1)
	assert.Equal(t, actionBatchReset, rows[0].Action)
	// 谁、什么时候:操作者与时间戳由 audit.WriteConfigUpdate 从 context 取,
	// 这里连同"清了哪些渠道、各自多少"一起断言,四要素缺一不可。
	assert.Equal(t, 9001, rows[0].ActorUserId)
	assert.Equal(t, "root", rows[0].ActorName)
	assert.NotZero(t, rows[0].CreatedAt)

	after := parseResetAfter(t, rows[0].AfterSnap)
	assert.EqualValues(t, 290275500, after.ClearedUsedQuotaTotal)
	// 从大到小,而且只列真的被抹掉的那些 —— 13 号本来就是 0,它在 skipped_ids 里。
	assert.Equal(t, [][2]int64{{11, 290275000}, {12, 500}}, after.ClearedUsedQuota)
	assert.Equal(t, []int{13}, after.SkippedIds)
	assert.Zero(t, after.ClearedDetailOmitted)
}

// 同一批被清第二次:第二次必须报 skipped、金额 0,审计里也必须是 0。
//
// # 这条锁的是并发下的重复记账
//
// 两个管理员同时清同一批时,如果"清掉了多少"取自批次开头那次预读,
// 两条审计会各自宣称抹掉了同一笔钱,合计翻倍。串行跑两次是同一形状的
// 可确定复现版本:第二次读到的必须已经是 0。
//
// 顺带锁住"不出现负数":清零是赋值 0 而不是减法,重复执行不会把列压到负数。
func TestBatchResetRunTwiceDoesNotDoubleCountOrGoNegative(t *testing.T) {
	qy := newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 21, Name: "alpha", Status: common.ChannelStatusEnabled, UsedQuota: 4200,
	}).Error)

	first := resetResultOf(t, call(t, "/channels/batch-reset-usage",
		resetBody([]int{21}, true, false), adminBatchResetUsage))
	require.Equal(t, 1, first.Succeeded)
	require.EqualValues(t, 4200, first.ClearedUsedQuota)

	second := resetResultOf(t, call(t, "/channels/batch-reset-usage",
		resetBody([]int{21}, true, false), adminBatchResetUsage))
	assert.Zero(t, second.Succeeded)
	assert.Equal(t, 1, second.Skipped)
	assert.Zero(t, second.ClearedUsedQuota,
		"第二次一分钱都没抹掉,报出金额等于把同一笔钱记两遍")

	var got model.Channel
	require.NoError(t, main.Where("id = ?", 21).First(&got).Error)
	assert.Zero(t, got.UsedQuota)
	assert.GreaterOrEqual(t, got.UsedQuota, int64(0), "清零绝不能把列压到负数")

	// 两条审计,合计相加仍然等于真正被抹掉的那一笔。
	rows := auditRows(t, qy)
	require.Len(t, rows, 2)
	var total int64
	for _, row := range rows {
		total += parseResetAfter(t, row.AfterSnap).ClearedUsedQuotaTotal
	}
	assert.EqualValues(t, 4200, total)
}

// 审计里的金额必须等于**真正被覆盖掉的那个值**,而不是批次开头预读到的旧值。
//
// # 触发窗口
//
// runBatch 为了拿渠道名与判存在,先在事务外读一次行。从那一读到真正的 UPDATE
// 之间,这个渠道还在计费:model.UpdateChannelUsedQuota 随时会把 used_quota 累加
// 上去(开着 BATCH_UPDATE_ENABLED 时更明显 —— 攒够一个周期一次性加一大笔)。
// 拿预读值当作"清掉了多少",抹掉的是 150 而账上记的是 100,差额 50 从此无据可查;
// 而这正是清零操作唯一留下的凭据。
//
// 用一个只触发一次的查询回调把这个窗口做成确定的:预读刚返回就把库里的值改掉,
// 不靠 sleep、不靠并发调度。
func TestBatchResetAuditsTheValueActuallyOverwrittenNotThePreReadOne(t *testing.T) {
	qy := newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 41, Name: "still-billing", Status: common.ChannelStatusEnabled,
		UsedQuota: 100,
	}).Error)

	var bumped atomic.Bool
	require.NoError(t, main.Callback().Query().After("gorm:query").
		Register("qy_test_bump_used_quota", func(tx *gorm.DB) {
			// 只在批次开头那次预读之后开一次窗口。之后事务里的加锁重读不再受影响,
			// 否则每一次读都会把值改掉,测的就不是这条不变量了。
			if bumped.Swap(true) {
				return
			}
			require.NoError(t, main.Exec(
				"UPDATE channels SET used_quota = ? WHERE id = ?", 150, 41).Error)
		}))
	t.Cleanup(func() {
		_ = main.Callback().Query().Remove("qy_test_bump_used_quota")
	})

	got := resetResultOf(t, call(t, "/channels/batch-reset-usage",
		resetBody([]int{41}, true, false), adminBatchResetUsage))
	require.Equal(t, 1, got.Succeeded)
	assert.EqualValues(t, 150, got.ClearedUsedQuota,
		"抹掉的是 150,报 100 等于凭空丢掉 50 的账")

	var after model.Channel
	require.NoError(t, main.Where("id = ?", 41).First(&after).Error)
	assert.Zero(t, after.UsedQuota)

	rows := auditRows(t, qy)
	require.Len(t, rows, 1)
	snap := parseResetAfter(t, rows[0].AfterSnap)
	assert.EqualValues(t, 150, snap.ClearedUsedQuotaTotal)
	assert.Equal(t, [][2]int64{{41, 150}}, snap.ClearedUsedQuota)
}

// 余额已经是 0、但时间戳还挂着旧值 —— 必须仍然算作"要清",而不是"本来就干净"。
//
// 少读 balance_updated_time 的那一版把这一档判成了 skipped(结构体零值恒等于
// 库里的 0),于是页面上那句「最近更新于 …:余额 0」永远清不掉。
func TestBatchResetClearsStaleBalanceTimestampWhenBalanceAlreadyZero(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 31, Name: "stale", Status: common.ChannelStatusEnabled,
		Balance: 0, BalanceUpdatedTime: 1700000000,
	}).Error)

	got := resetResultOf(t, call(t, "/channels/batch-reset-usage",
		resetBody([]int{31}, false, true), adminBatchResetUsage))
	assert.Equal(t, 1, got.Succeeded, "时间戳还在就还有得清")

	var after model.Channel
	require.NoError(t, main.Where("id = ?", 31).First(&after).Error)
	assert.Zero(t, after.BalanceUpdatedTime,
		"旧时间戳留着等于页面在说「刚查过,余额就是 0」")
}

// 重置的 after 快照在**满批全部清零**时仍然装得下,而且是合法 JSON。
//
// 与删除那条同源但更紧:逐条明细带着金额(`[90000199,999999999999],` 24 字节),
// 满批 200 条会到 4800 字节,直接越过 4096 的切点。maxAuditClearedDetail
// 因此按金额从大到小截断,并把省略条数白纸黑字写进快照 —— 合计永远精确。
//
// 用最坏形状:id 取 8 位数(远超真实渠道 id 的量级)、金额取 12 位数。
func TestBatchResetAuditSnapshotFitsAtMaxBatch(t *testing.T) {
	qy := newQyDB(t)
	newMainDB(t)

	ids := make([]int, 0, maxBatchIds)
	cleared := make([][2]int64, 0, maxBatchIds)
	res := batchResult{Total: maxBatchIds, Succeeded: maxBatchIds,
		Items: make([]itemResult, 0, maxBatchIds)}
	var total int64
	for i := 0; i < maxBatchIds; i++ {
		id := 90000000 + i
		amount := int64(999999999999 - i)
		ids = append(ids, id)
		cleared = append(cleared, [2]int64{int64(id), amount})
		total += amount
		res.Items = append(res.Items, itemResult{
			Id: id, Name: strings.Repeat("渠", 21) + "x", Outcome: outcomeOK,
		})
	}

	rec := call(t, "/channels/batch-reset-usage", `{}`, func(c *gin.Context) {
		writeBatchAudit(c, actionBatchReset, res,
			gin.H{"ids": ids, "reset_used_quota": true, "reset_balance": false},
			clearedAuditDetail(cleared, total, 0))
		c.Status(http.StatusOK)
	})
	require.Equal(t, http.StatusOK, rec.Code)

	rows := auditRows(t, qy)
	require.Len(t, rows, 1)
	maxSnap := config.Get().Audit.SnapshotMaxBytes
	if maxSnap <= 0 {
		maxSnap = 4096
	}
	assert.NotContains(t, rows[0].AfterSnap, "[truncated]",
		"after 被截断了 —— 截断的文本不是合法 JSON,管理端连计数都读不出来")
	assert.LessOrEqual(t, len(rows[0].AfterSnap), maxSnap)

	after := parseResetAfter(t, rows[0].AfterSnap)
	assert.Equal(t, total, after.ClearedUsedQuotaTotal, "合计永远精确,不受明细上限影响")
	assert.Len(t, after.ClearedUsedQuota, maxAuditClearedDetail)
	assert.Equal(t, maxBatchIds-maxAuditClearedDetail, after.ClearedDetailOmitted,
		"省略了几笔必须白纸黑字说出来,否则事后会把明细当成全集")
	// 留下的是金额最大的那些 —— 事后要追的是"那笔大的去哪了"。
	assert.EqualValues(t, 999999999999, after.ClearedUsedQuota[0][1])
}

// 明细没超上限时不写 cleared_detail_omitted:那个字段一出现就意味着
// "这份明细不是全集",无条件写 0 会让每一条审计都看起来像被截过。
func TestClearedAuditDetailOmitsMarkerWhenEverythingFits(t *testing.T) {
	detail := clearedAuditDetail([][2]int64{{2, 10}, {1, 30}}, 40, 0)
	assert.EqualValues(t, int64(40), detail["cleared_used_quota_total"])
	assert.Equal(t, [][2]int64{{1, 30}, {2, 10}}, detail["cleared_used_quota"])
	_, has := detail["cleared_detail_omitted"]
	assert.False(t, has)
}

// 全批失败时审计必须记 result=fail,并带上失败明细。
//
// 只记计数的话,事后要回答"那 3 条到底删掉没有"就只剩猜。
func TestBatchAuditRecordsFailureDetailWhenNothingSucceeded(t *testing.T) {
	qy := newQyDB(t)
	newMainDB(t)

	res := resultOf(t, call(t, "/channels/batch-delete",
		`{"ids":[404,405]}`, adminBatchDelete))
	require.Equal(t, 2, res.Failed)

	rows := auditRows(t, qy)
	require.Len(t, rows, 1)
	assert.Equal(t, actionBatchDelete, rows[0].Action)
	assert.Equal(t, qymodel.ResultFail, rows[0].Result)
	assert.Contains(t, rows[0].AfterSnap, itemCodeNotFound,
		"审计的 after 必须带失败明细,只有计数的话事后回答不了「哪几条没删掉」")
}

// 有成功就记 result=ok —— 部分失败不是整批失败。
func TestBatchAuditRecordsOkWhenSomeSucceeded(t *testing.T) {
	qy := newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 1, "alpha", common.ChannelStatusEnabled)

	res := resultOf(t, call(t, "/channels/batch-delete",
		`{"ids":[1,404]}`, adminBatchDelete))
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, 1, res.Failed)

	rows := auditRows(t, qy)
	require.Len(t, rows, 1)
	assert.Equal(t, qymodel.ResultOK, rows[0].Result)
}

// 纯输入校验失败不写审计:与 apiaddr 同一条分界线 —— "有没有碰到库里的既有状态"。
// 管理员在页面上误点一下不该往审计里灌一条噪音。
func TestInputValidationFailureWritesNoAudit(t *testing.T) {
	qy := newQyDB(t)
	newMainDB(t)

	assert.Equal(t, errNoIds.Code,
		codeOf(t, call(t, "/channels/batch-delete", `{"ids":[]}`, adminBatchDelete)))
	assert.Empty(t, auditRows(t, qy))
}

func TestNormalizeIds(t *testing.T) {
	cases := []struct {
		name    string
		in      []int
		want    []int
		wantErr *bizError
	}{
		{name: "空选中集整批拒绝", in: nil, wantErr: errNoIds},
		{name: "重复 id 静默合并", in: []int{3, 1, 3, 1}, want: []int{3, 1}},
		{name: "保持提交顺序", in: []int{9, 2, 7}, want: []int{9, 2, 7}},
		{name: "非正 id 整批拒绝", in: []int{1, 0}, wantErr: errInvalidParam},
		{name: "负 id 整批拒绝", in: []int{-1}, wantErr: errInvalidParam},
		{name: "超上限整批拒绝", in: make([]int, maxBatchIds+1), wantErr: errTooMany},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeIds(tc.in)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, error(tc.wantErr))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// 上限刚好等于 maxBatchIds 时必须放行 —— 边界写成 >= 会让一次合法的满批被拒。
func TestNormalizeIdsAcceptsExactlyMaxBatch(t *testing.T) {
	ids := make([]int, 0, maxBatchIds)
	for i := 1; i <= maxBatchIds; i++ {
		ids = append(ids, i)
	}
	got, err := normalizeIds(ids)
	require.NoError(t, err)
	assert.Len(t, got, maxBatchIds)
}

// 请求体不是合法 JSON 时报参数错误,而不是 500 —— 500 会把 SQL 片段/表结构
// 的风险面暴露出来,也让前端分不清"我发错了"和"服务端挂了"。
func TestMalformedBodyIsRejectedAsInvalidParam(t *testing.T) {
	newQyDB(t)
	newMainDB(t)

	res := call(t, "/channels/batch-delete", `{"ids":`, adminBatchDelete)
	assert.Equal(t, errInvalidParam.Code, codeOf(t, res))
}

// 审计的 after 快照必须在**满批全失败**时仍然装得下,而且是合法 JSON。
//
// 这条锁的是一次真实的信息丢失:after 走 audit.Truncate(默认 4096 字节),
// 而逐条明细带着渠道名与中文 detail 时一条约 120~150 字节 —— 200 条会在第 30 条
// 左右被切断。切断之后事后复盘按「Before 的 id 全集 - 失败集合 = 成功的那些」
// 做减法,会把没删掉的渠道算成已删,而渠道行是硬删除,主库里再也无法反查;
// 同时切断的文本不再是合法 JSON,管理端的快照渲染整个回落成一行裸文本。
//
// 用最坏的一批来断言:满批、全部失败、id 取到 8 位数(远超真实渠道 id 的量级),
// 名字取 64 字符(渠道名列的上限量级)。这批数据在改动前会稳定超限。
func TestBatchAuditSnapshotFitsAtMaxBatch(t *testing.T) {
	qy := newQyDB(t)
	newMainDB(t)

	// 直接喂 writeBatchAudit 一个最坏形状的批次结局:满批、全部失败、
	// 渠道名与中文 detail 都取到真实上限量级、id 取 8 位数。
	// 这条路径不经过数据库执行 —— 要锁的是"结局怎么写进审计",
	// 而不是"怎么产生这个结局";后者已经由上面那些用例覆盖。
	ids := make([]int, 0, maxBatchIds)
	res := batchResult{Total: maxBatchIds, Failed: maxBatchIds,
		Items: make([]itemResult, 0, maxBatchIds)}
	for i := 0; i < maxBatchIds; i++ {
		id := 90000000 + i
		ids = append(ids, id)
		res.Items = append(res.Items, itemResult{
			Id: id, Name: strings.Repeat("渠", 21) + "x",
			Outcome: outcomeFailed, Code: itemCodeDBError,
			Detail: "删除失败,请查看后端日志",
		})
	}

	rec := call(t, "/channels/batch-delete", `{}`, func(c *gin.Context) {
		writeBatchAudit(c, actionBatchDelete, res, gin.H{"ids": ids}, nil)
		c.Status(http.StatusOK)
	})
	require.Equal(t, http.StatusOK, rec.Code)

	rows := auditRows(t, qy)
	require.Len(t, rows, 1)
	// 生效上限:配置没写时 audit.build 自己回落到 4096(defaults.go 的默认值也是它)。
	maxSnap := config.Get().Audit.SnapshotMaxBytes
	if maxSnap <= 0 {
		maxSnap = 4096
	}
	assert.NotContains(t, rows[0].AfterSnap, "[truncated]",
		"after 快照被截断了 —— 截断之后「id 全集减去失败集合」这条复盘规则会算错,"+
			"而渠道行是硬删除,主库里再也无法反查")
	assert.LessOrEqual(t, len(rows[0].AfterSnap), maxSnap)

	// 合法 JSON(被切断的文本不是),而且 200 个 id 一个不少。
	var after struct {
		Total           int              `json:"total"`
		Failed          int              `json:"failed"`
		FailedIdsByCode map[string][]int `json:"failed_ids_by_code"`
	}
	require.NoError(t, common.UnmarshalJsonStr(rows[0].AfterSnap, &after))
	assert.Equal(t, maxBatchIds, after.Total)
	assert.Len(t, after.FailedIdsByCode[itemCodeDBError], maxBatchIds)
}

// UpdateChannelStatus 返回 false 之后必须回读一次库,而不是一律报「数据库操作
// 失败,请查看后端日志」。
//
// 触发路径在生产上很常见:开着内存缓存的多节点部署里,渠道被别的节点自动停用
// 之后,本节点缓存仍是旧值,UpdateChannelStatus 会在 model/channel.go:736
// 命中「缓存里的状态已经等于目标值」直接 return false —— 而那条路径**一行日志
// 都不写**。管理员照着提示去翻一份空日志,重试还会拿到同一句话。
//
// 这里用同一个可观测形状立此存照:库里已经是目标状态时,回读要把它判成
// skipped(终态就是他要的那个),而不是 failed。
func TestStatusVerifyTreatsAlreadyTargetAsSkippedNotFailure(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 7, "gamma", common.ChannelStatusEnabled)

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	outcome, code, _ := verifyStatusAfterFalse(ctx, 7, common.ChannelStatusEnabled)
	assert.Equal(t, outcomeSkipped, outcome)
	assert.Equal(t, itemCodeNoChange, code)
}

// 库里**没有**变成目标状态时,报的必须是 not_applied 而不是 db_error:
// 前者告诉管理员"等一个缓存同步周期再来",后者把他支去看一份空日志。
func TestStatusVerifyReportsNotAppliedWhenRowUnchanged(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	seedChannel(t, main, 8, "delta", common.ChannelStatusEnabled)

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	outcome, code, detail := verifyStatusAfterFalse(ctx, 8, common.ChannelStatusManuallyDisabled)
	assert.Equal(t, outcomeFailed, outcome)
	assert.Equal(t, itemCodeNotApplied, code)
	assert.NotEmpty(t, detail)
}

// 渠道在回读时已经不在了 —— 那是 not_found,刷新列表即可,与"写失败"无关。
func TestStatusVerifyReportsNotFoundWhenRowDisappeared(t *testing.T) {
	newQyDB(t)
	newMainDB(t)

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	outcome, code, _ := verifyStatusAfterFalse(ctx, 404, common.ChannelStatusEnabled)
	assert.Equal(t, outcomeFailed, outcome)
	assert.Equal(t, itemCodeNotFound, code)
}
