package channelops

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// 库里本来就是 0 → skipped,不是 ok。
//
// 区别对管理员是实的:"18 条已清零、2 条本来就是 0"说明选择范围没问题,
// 而"20 条全部已清零"会让人以为那 2 条也曾经有过消耗。
func TestBatchResetReportsAlreadyZeroChannelsAsSkipped(t *testing.T) {
	newQyDB(t)
	main := newMainDB(t)
	require.NoError(t, main.Create(&model.Channel{
		Id: 1, Name: "used", Status: common.ChannelStatusEnabled, UsedQuota: 99,
	}).Error)
	require.NoError(t, main.Create(&model.Channel{
		Id: 2, Name: "clean", Status: common.ChannelStatusEnabled, UsedQuota: 0,
	}).Error)

	res := resultOf(t, call(t, "/channels/batch-reset-usage",
		`{"ids":[1,2],"reset_used_quota":true}`, adminBatchResetUsage))

	assert.Equal(t, 1, res.Succeeded)
	assert.Equal(t, 1, res.Skipped)
	assert.Zero(t, res.Failed)
	assert.Equal(t, itemCodeNoChange, itemFor(t, res, 2).Code)
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
		writeBatchAudit(c, actionBatchDelete, res, gin.H{"ids": ids})
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
