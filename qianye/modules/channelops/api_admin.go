package channelops

import (
	"context"
	"errors"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// api_admin.go —— 渠道批量操作的三个管理端接口。
//
// # 审计口径
//
// 每个接口**恰好一处** audit.WriteConfigUpdate,成功与失败共用同一条路径:
// Result 由本批的结局算出来(一条都没成功 = fail),After 里带着完整的分档计数
// 与失败明细。写成两个分支只会得到两段除 Result 以外逐字节相同的代码,
// 而漏掉其中一段正是本仓反复出现的形状。
//
// 纯输入校验失败(没勾渠道、超上限、状态值非法)**不写**审计:它们一条库都没碰,
// 与 apiaddr 的分界线一致 —— "有没有碰到库里的既有状态"。
//
// # 为什么整批一律 200,即使一条都没成功
//
// 因为 data 里的逐条明细才是这个接口的产品。把"全失败"表达成 success:false
// 会让信封的 data 整个作废(qy 前端的 unwrap 在 success:false 时直接抛错、丢掉
// data),管理员就只剩一句"操作失败",连哪几条失败、为什么失败都看不到 ——
// 那恰恰是本模块要解决的问题。
//
// 这与"全部失败却回 200 谎报成功"不是一回事:响应体里 succeeded=0、failed=N
// 白纸黑字,前端据此弹的是红色 toast 而不是绿色。判据是**响应体说没说真话**,
// 不是 HTTP 状态码好不好看。
const (
	actionBatchStatus = "channel.batch_status"
	actionBatchDelete = "channel.batch_delete"
	actionBatchReset  = "channel.batch_reset_usage"
)

type statusReq struct {
	Ids    []int `json:"ids"`
	Status int   `json:"status"`
}

type idsReq struct {
	Ids []int `json:"ids"`
}

// resetReq 的两个开关刻意分开,而且都是普通 bool(缺省 false)。
//
// 不给默认值是有意的:这是一个抹掉历史统计的操作,"请求里没说要清什么"
// 唯一安全的解释是"什么都别清",而不是"那就按最常见的来"。
// 两个都没勾时整批 400(errNothingToReset),不会静默变成一次空操作 ——
// 空操作会回一个 succeeded=N 的绿色报告,而库里什么都没变。
type resetReq struct {
	Ids            []int `json:"ids"`
	ResetUsedQuota bool  `json:"reset_used_quota"`
	ResetBalance   bool  `json:"reset_balance"`
}

// adminBatchStatus 批量启用 / 手动停用。
//
// 只允许启用与手动停用两个目标值,与上游 isManageableChannelStatus 同口径:
// 自动停用(status=3)是系统在探测到上游故障时打的标记,由人从管理端手动写进去
// 会让"这个渠道是被谁停的"永久说不清。
func adminBatchStatus(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req statusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if req.Status != common.ChannelStatusEnabled &&
		req.Status != common.ChannelStatusManuallyDisabled {
		respondErr(c, errBadStatus)
		return
	}
	ids, err := normalizeIds(req.Ids)
	if err != nil {
		respondErr(c, err)
		return
	}

	// 上游 model.UpdateChannelStatus 不收 ctx(它自己走 model.DB 与内存缓存),
	// 这一步用不上;ctx 在这里只喂给失败之后的那次回读 —— 那条语句是我们自己发的,
	// 必须带上本条渠道的预算。判据见 qianye/ctx_guard_test.go。
	res := runBatch(c.Request.Context(), ids,
		func(ctx context.Context, ch *model.Channel) (string, string, string) {
			// 已经是目标状态 —— 这不是失败,是本来就不用动。
			// 上游把这一档和真失败一起算成"没改动",前端再拿总数一减,
			// 于是全站渠道本来就全开时点一次"批量启用"会报「N 个启用失败」。
			if ch.Status == req.Status {
				return outcomeSkipped, itemCodeNoChange, ""
			}
			// 状态变更整体复用上游 model.UpdateChannelStatus:多 Key 渠道的
			// 逐 Key 状态表、内存缓存同步、abilities 的 enabled 翻转都在里面,
			// 自己写一条 UPDATE 会漏掉后两件事,而漏掉的表现是"页面显示已启用、
			// 请求仍然不走这个渠道"。
			if !model.UpdateChannelStatus(ch.Id, "", req.Status, "qy batch operation") {
				return verifyStatusAfterFalse(ctx, ch.Id, req.Status)
			}
			return outcomeOK, "", ""
		})

	// 重建条件是「有没有调用过 UpdateChannelStatus」,不是「有没有成功」。
	//
	// 那个函数在返回 false **之前**就已经改过本节点的内存缓存了:停用方向的
	// CacheUpdateChannelStatus 会把渠道从 group2model2channels 这张路由表里摘掉。
	// 只在 Succeeded > 0 时重建的话,「整批全失败」会留下一个报告说什么都没变、
	// 而本节点在接下来最长一个 SYNC_FREQUENCY 周期里真的不再把请求发给这些渠道 ——
	// 表现为某些模型突然「没有可用渠道」,同时每一处界面都说一切正常。
	//
	// 多重建一次的代价只是一次全表读,而这是管理员按出来的低频动作。
	if res.Succeeded > 0 || res.Failed > 0 {
		model.InitChannelCache()
	}
	writeBatchAudit(c, actionBatchStatus, res, gin.H{"status": req.Status, "ids": ids}, nil)
	respondOK(c, res)
}

// verifyStatusAfterFalse 在 model.UpdateChannelStatus 返回 false 之后回读一次库,
// 把「什么都没发生」与「真的写失败了」分开。
//
// # 为什么必须回读
//
// UpdateChannelStatus 只回一个 bool,而它有四条返回 false 的路径,其中三条
// **一行日志都不写**(model/channel.go:718 缓存里没有这个渠道、736 缓存里的状态
// 已经等于目标值、754 GetChannelById 失败),只有 SaveWithoutKey 失败那一条会
// SysLog。把 false 一律翻译成"数据库操作失败,请查看后端日志"因此是一句假话:
// 多节点部署下最常见的那一条(渠道刚被别的节点自动停用,本节点缓存还是旧值)
// 后端日志里什么都没有,管理员翻完空日志重试,还会拿到同一句话。
//
// 回读一次就能给出确定的答案:库里现在是不是目标状态。这一次读是本条渠道
// ColdContext 预算内的第二条语句,不额外开事务。
func verifyStatusAfterFalse(ctx context.Context, id, want int) (string, string, string) {
	after, err := loadChannel(ctx, id)
	switch {
	case errors.Is(err, errChannelGone):
		return outcomeFailed, itemCodeNotFound, "渠道不存在,可能已被其他管理员删除"
	case err != nil:
		common.SysError("qianye/channelops: 状态回读失败: " + err.Error())
		return outcomeFailed, itemCodeDBError, "状态回读失败,请查看后端日志"
	case after.Status == want:
		// 库里已经是目标状态了。这一批开始时它还不是(runBatch 先读过一次),
		// 所以要么这次写其实成功了、要么另一个管理员/节点抢在前面写了同一个值。
		// 两种情况的终态都是管理员要的那个,报 skipped 而不是 failed ——
		// 报 failed 会让他去修一个已经不存在的问题。
		return outcomeSkipped, itemCodeNoChange, ""
	default:
		// 库里没变。到这里唯一没被上面排除的原因就是那三条静默路径。
		return outcomeFailed, itemCodeNotApplied,
			"状态未生效:本节点缓存与数据库不一致(多节点部署下常见),渠道仍是原状态。" +
				"等一个缓存同步周期后重试即可,重试不会产生副作用"
	}
}

// adminBatchDelete 批量删除。每条一个事务,渠道行与 abilities 行同生共死。
func adminBatchDelete(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req idsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	ids, err := normalizeIds(req.Ids)
	if err != nil {
		respondErr(c, err)
		return
	}

	res := runBatch(c.Request.Context(), ids,
		func(ctx context.Context, ch *model.Channel) (string, string, string) {
			switch err := deleteChannelWithAbilities(ctx, ch.Id); {
			case err == nil:
				return outcomeOK, "", ""
			case errors.Is(err, errChannelGone):
				return outcomeFailed, itemCodeNotFound, "渠道不存在,可能已被其他管理员删除"
			default:
				common.SysError("qianye/channelops: 删除渠道失败: " + err.Error())
				return outcomeFailed, itemCodeDBError, "删除失败,请查看后端日志"
			}
		})

	// 缓存失效照抄上游 controller.DeleteChannelBatch 的完整口径:
	// InitChannelCache 重建路由用的渠道表,ResetProxyClientCache 清掉按代理地址
	// 缓存的 http.Client —— 后者不清的话,被删渠道的代理连接池会一直留在内存里。
	// 逐条 InvalidateProxyClient 做不到:本模块只读了 5 个列,拿不到 setting.proxy,
	// 而为了清缓存去把 setting 整列读进来不值得(上游批量删除同样是整体 Reset)。
	if res.Succeeded > 0 {
		model.InitChannelCache()
		service.ResetProxyClientCache()
	}
	writeBatchAudit(c, actionBatchDelete, res, gin.H{"ids": ids}, nil)
	respondOK(c, res)
}

// adminBatchResetUsage 批量重置本站统计。
//
// # 两个字段的真实语义(读代码得来的,不是望文生义)
//
//	used_quota  本站自己记的账:每次结算由 model.UpdateChannelUsedQuota 累加。
//	            列表页的「已用额度」列就是它。清零 = 真的抹掉这个累计值,
//	            而且没有任何补算路径(日志表里还有逐条流水,但列上的数回不来)。
//
//	balance     上游账户余额,**只是一份缓存的展示值**。唯一的写入者是
//	            (*Channel).UpdateBalance,而它只被 controller/channel-billing.go
//	            的 updateChannelBalance 调用 —— 那个函数只对 8 类渠道有实现
//	            (AIProxy/API2GPT/AIGC2D/SiliconFlow/DeepSeek/OpenRouter/Moonshot
//	            与 OpenAI 系),其余一律返回「尚未实现」,也就是说绝大多数渠道的
//	            balance 从建库那天起就是 0,清它等于什么都没做;而对那 8 类,
//	            下一次「更新余额」会把它原样覆盖回来。
//
// 结论:**真正有意义的是 used_quota**。balance 仍然给了一个开关,但默认不勾,
// 且必须由前端显式传 true —— 它唯一的用处是渠道换了上游账号之后,
// 让那个属于旧账号的余额数字不要继续挂在页面上骗人。
//
// balance 清零时连 balance_updated_time 一起清:留着旧时间戳等于宣称
// "刚刚查过,余额就是 0",而那是一句假话。
func adminBatchResetUsage(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req resetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	if !req.ResetUsedQuota && !req.ResetBalance {
		respondErr(c, errNothingToReset)
		return
	}
	ids, err := normalizeIds(req.Ids)
	if err != nil {
		respondErr(c, err)
		return
	}

	// 逐条真正抹掉的金额。它同时是三样东西的来源:审计里的「各自清掉多少」、
	// 响应里回给管理员的合计、以及本函数唯一能证明"确实动了库"的凭据。
	// 金额一律来自 resetChannelCounters 的**锁内重读**,不是 runBatch 那次预读 ——
	// 理由见 resetChannelCounters 的注释(两个管理员同时清会让审计金额翻倍)。
	cleared := make([][2]int64, 0, len(ids))
	var clearedUsedTotal int64
	var clearedBalanceTotal float64

	res := runBatch(c.Request.Context(), ids,
		func(ctx context.Context, ch *model.Channel) (string, string, string) {
			out, err := resetChannelCounters(ctx, ch.Id, req.ResetUsedQuota, req.ResetBalance)
			switch {
			case errors.Is(err, errChannelGone):
				return outcomeFailed, itemCodeNotFound, "渠道不存在,可能已被其他管理员删除"
			case err != nil:
				common.SysError("qianye/channelops: 重置渠道统计失败: " + err.Error())
				return outcomeFailed, itemCodeDBError, "重置失败,请查看后端日志"
			}
			// 库里本来就是 0 —— 报 skipped 而不是 ok。两者的区别对管理员是实的:
			// "18 条已清零、2 条本来就是 0"说明选择范围没问题,
			// 而"20 条全部已清零"会让人以为那 2 条也曾经有过消耗。
			if !out.Changed {
				return outcomeSkipped, itemCodeNoChange, ""
			}
			if out.UsedQuota != 0 {
				cleared = append(cleared, [2]int64{int64(ch.Id), out.UsedQuota})
				clearedUsedTotal += out.UsedQuota
			}
			clearedBalanceTotal += out.Balance
			return outcomeOK, "", ""
		})

	// 渠道缓存里带着 used_quota / balance 的旧值。不重建的话,列表页刷新出来是
	// 新值、而缓存里还是旧值,下一次任何以缓存对象为基准的写回都会把它带回来。
	if res.Succeeded > 0 {
		model.InitChannelCache()
	}
	writeBatchAudit(c, actionBatchReset, res, gin.H{
		"ids":              ids,
		"reset_used_quota": req.ResetUsedQuota,
		"reset_balance":    req.ResetBalance,
	}, clearedAuditDetail(cleared, clearedUsedTotal, clearedBalanceTotal))
	respondOK(c, resetResult{
		batchResult:      res,
		ClearedUsedQuota: clearedUsedTotal,
		ClearedBalance:   clearedBalanceTotal,
	})
}

// resetResult 在通用批次报告之外多回两个合计。
//
// 确认框里那个"合计已用额度"是前端拿列表页缓存的行算出来的**估算**:那份数据
// 可能已经过期几分钟,期间渠道还在计费。回来这两个数才是库里真正被抹掉的量,
// 前端据此把成功提示写成"共抹掉 X",而不是把估算值当成结果复述一遍。
type resetResult struct {
	batchResult
	ClearedUsedQuota int64   `json:"cleared_used_quota"`
	ClearedBalance   float64 `json:"cleared_balance"`
}

// maxAuditClearedDetail 是 after 快照里逐条列出多少笔清零明细的上限。
//
// # 为什么必须有上限
//
// 快照被 audit.Truncate 按 SnapshotMaxBytes(默认 4096 字节)硬切,而切断的
// 后果不是"少看几行":切断的文本不再是合法 JSON,管理端的快照渲染整个回落成
// 一行裸文本,连计数都读不出来(与 writeBatchAudit 注释里那次失败同源)。
//
// 一笔明细是 `[90000199,999999999999],` 共 24 字节;失败 / 跳过那两档每个 id
// 9 字节。三档互斥且合起来等于全集,所以 after 的规模上界是
// `24*min(N,cap) + 9*(200-N)`,在 N=cap 处取到最大值。cap=100 时约 3300 字节,
// 加上固定的键名与失败码分组约 400 字节,仍留着 ~390 字节余量。
//
// 超出上限时按金额从大到小保留 —— 事后要追的是"那笔大的去哪了",
// 并且 cleared_detail_omitted 会白纸黑字说清省略了几笔,合计永远是精确值。
const maxAuditClearedDetail = 100

// clearedAuditDetail 把逐条清零金额整理成 after 快照里的三个字段。
//
// 合计(cleared_used_quota_total)永远精确、永远不被上限影响:它才是事后成本
// 核算真正要减掉的那个数,而逐条明细回答的是"这笔减在谁头上"。
func clearedAuditDetail(cleared [][2]int64, usedTotal int64, balanceTotal float64) gin.H {
	detail := gin.H{
		"cleared_used_quota_total": usedTotal,
		"cleared_balance_total":    balanceTotal,
	}
	if len(cleared) == 0 {
		return detail
	}
	// 从大到小:上限砍掉的必须是最不值得追的那些。
	sort.Slice(cleared, func(i, j int) bool { return cleared[i][1] > cleared[j][1] })
	if len(cleared) > maxAuditClearedDetail {
		detail["cleared_detail_omitted"] = len(cleared) - maxAuditClearedDetail
		cleared = cleared[:maxAuditClearedDetail]
	}
	detail["cleared_used_quota"] = cleared
	return detail
}

// writeBatchAudit 落一条批次审计。
//
// # 快照里放什么
//
//	Before  请求本身(选了哪些 id、目标状态 / 勾了哪些重置项)
//	After   分档计数 + **按失败原因分组的 id** + 跳过的 id
//
// # 为什么 After 里放 id 而不是逐条明细
//
// 审计的两个快照列各自被 audit.Truncate 按 Audit.SnapshotMaxBytes(默认 4096
// 字节)硬切。逐条明细带着渠道名与中文 detail,一条约 120~150 字节,200 条的
// 上限批次会在第 30 条左右被切断 —— 而切断的后果不是"少看几行":
//
//  1. 事后复盘按「Before 的 id 全集 - 失败集合 = 成功的那些」做减法,会把没删掉
//     的渠道算成已删。渠道行是硬删除,主库里再也无法反查,这条审计就是唯一凭据。
//  2. 被切断的文本不再是合法 JSON,管理端的快照渲染整个回落成一行 4096 字符的
//     裸文本。
//
// 换成「id 按失败码分组 + 跳过的 id」之后,最坏情况(200 条全部失败)的 After
// 约 1.6 KB,**结构上**装得下满批,再也不会截断。逐条的渠道名与中文 detail 仍然
// 原样回给操作者(响应体里的 items),审计要回答的是"哪些 id 落在哪一档",
// 那是一个不会被名字长度左右的事实。判据见 TestBatchAuditSnapshotFitsAtMaxBatch。
//
// ok 那一档不记 id:Before 有 id 全集,减去这里的 failed 与 skipped 就是它,
// 而这一次减法是精确的 —— 三档互斥且合起来等于全集(runBatch 的恒等式)。
//
// # extra
//
// 端点特有的 after 字段。重置统计用它带上「各自清掉多少」与合计 ——
// 那是这三个动作里唯一一个"抹掉了一笔可计量的东西"的动作,只记 id 的话,
// 事后要回答"这个渠道被清掉的是 3 块钱还是 3 万块"就只剩猜。
// 另外两个端点传 nil:启停可以原路改回去,删除的对象已经不存在,
// 都没有"多少"这个维度。
func writeBatchAudit(c *gin.Context, action string, res batchResult, before gin.H, extra gin.H) {
	result := qymodel.ResultOK
	reason := ""
	if res.Succeeded == 0 && res.Failed > 0 {
		result = qymodel.ResultFail
		reason = "本批全部失败"
	}
	failedByCode := map[string][]int{}
	skipped := make([]int, 0, res.Skipped)
	for _, item := range res.Items {
		switch item.Outcome {
		case outcomeFailed:
			code := item.Code
			if code == "" {
				code = itemCodeDBError
			}
			failedByCode[code] = append(failedByCode[code], item.Id)
		case outcomeSkipped:
			skipped = append(skipped, item.Id)
		}
	}
	after := gin.H{
		"total": res.Total, "succeeded": res.Succeeded,
		"skipped": res.Skipped, "failed": res.Failed,
		"failed_ids_by_code": failedByCode,
		"skipped_ids":        skipped,
	}
	for key, value := range extra {
		after[key] = value
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: action, Result: result, Reason: reason,
		Before: before,
		After:  after,
	})
}
