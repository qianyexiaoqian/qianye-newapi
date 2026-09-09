package riskwatch

import (
	"context"
	"crypto/rand"
	"math/big"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// capture.go —— 热路径上的那一段。
//
// 它挂在 relayguard 的**观察者**一侧,没有返回值,因此结构上不可能拦住任何请求。
// 观察者排在闸门之前,所以被违规规则拒掉的那一次同样会被抓到 —— 一个账号连续
// 撞违规词、每次都被拒,恰恰是最该留档的形状。
//
// 这条路径上只做三件事:读一次内存快照、摇几次骰子、把要用的值从 gin.Context
// 与 RelayInfo 上**抄下来**。落库全部走 guard.HotAsync。

// observe 是模块暴露给 relayguard 的唯一入口。
func observe(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta) {
	// 热路径绝不能因为本模块 panic:relay 是主业务,取证是附加物。
	defer guard.RecoverHot("riskwatch.observe")

	if c == nil || info == nil {
		return
	}
	// Playground 是站内调试点击,不是这个账号在"用"这个站。把管理员自己在
	// 游乐场里的每一次试打进取证记录,只会让真正要看的那几十条被冲掉。
	if info.IsPlayground {
		return
	}

	maybeRefresh()
	snap := Snapshot()
	if len(snap.tasks) == 0 {
		return
	}

	now := common.GetTimestamp()
	// 请求这一侧走 Effective(空串折叠成 default),任务那一侧走 Normalize
	// (空串保持空串 = 不限)。方向相反是刻意的:users.group 为空串的历史账号
	// 在业务上就是默认分组的用户,一个盯着 default 的监听任务必须盖住他们 ——
	// 而他们恰恰常常是最可疑的那批。反过来,任务的空分组要是被折成 default,
	// 一个"不限分组"的任务会突然只盯默认分组。
	group := groupname.Effective(info.UsingGroup)

	// 作用域闸排在抽样之前(理由见 watchTask.matches),抽样又排在**取文本**
	// 之前 —— 取文本要重建整段 CombineText,是这条路径上最贵的一步,
	// 而绝大多数请求根本不该走到那里。
	var hits []*watchTask
	for _, t := range snap.tasks {
		if !t.alive(now) || !t.matches(info.UserId, group, info.OriginModelName) {
			continue
		}
		if !sample(t.SampleBps) {
			continue
		}
		hits = append(hits, t)
	}
	if len(hits) == 0 {
		return
	}

	// 存储节点不可用时直接放弃这一批。判据是**存储节点**而不是主扩展库:
	// 主库挂了与这个功能无关(两张表都不在那里)。放在这里而不是函数开头,
	// 是为了让不可用状态下的开销仍然是"一次原子读 + 几次内存比较"。
	if !db.WatchAvailable() {
		return
	}

	// gin.Context 与 RelayInfo 上的值必须在**这一刻**抄下来:异步那一侧几百
	// 毫秒之后才会用到它们,而那时 c 已经被 gin 的 sync.Pool 交给下一个请求了。
	row := snapshotRow(c, info, meta)

	for _, t := range hits {
		task := t
		// 每个任务各写一行:它们各有各的条数上限与保留期,折成一行之后
		// 两个任务的计数会互相污染。同一次请求被两个任务抓到是正常的
		// (一个盯用户、一个盯模型),而那正是"这两条线索指向同一个人"的证据。
		one := row
		// TaskId 由 persist 在预留成功的那一刻写死(见 store.persist),这里
		// 只算保留期:它逐任务不同,而预留那一步不知道全局默认是多少。
		one.ExpiresAt = expiryFor(task.RetentionDays, one.CreatedAt)
		guard.HotAsync("riskwatch.capture", func(ctx context.Context) error {
			return writeCapture(ctx, task, &one)
		})
	}
}

// writeCapture 预留名额并落库;名额抢不到就把任务停掉。
func writeCapture(ctx context.Context, task *watchTask, row *Capture) error {
	gdb := db.Watch()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	reserved, err := persist(ctx, gdb, task.Id, row)
	if err != nil {
		return err
	}
	if reserved {
		return nil
	}
	// 没抢到名额只有一种可能:这个任务在快照拍下之后抽满了(或者已经被别人
	// 停掉了)。把它停掉是**幂等**的 —— finishTask 的 WHERE 里有 status =
	// running,多个 worker 同时发现时只有第一个改得动。
	//
	// 立刻重载快照:否则这个已经满了的任务还会在快照里活到下一个刷新周期,
	// 期间每一次命中都要跑一次事务、再回到这里。
	if err := finishTask(ctx, gdb, task.Id, StatusFinished, ReasonMaxRecords); err != nil {
		return err
	}
	return reloadCtx(ctx)
}

// snapshotRow 把这一刻的请求压成一行待写记录。
//
// 它自己不碰任何会在请求结束后失效的东西 —— 抄完之后返回的结构体可以安全地
// 交给异步 worker。列宽在这里就截好:varchar(64) 的列存不下 21 个中文的用户名,
// 而那次插入失败在异步路径上只会留下一行日志。
func snapshotRow(c *gin.Context, info *relaycommon.RelayInfo, meta *types.TokenCountMeta) Capture {
	requestId := c.GetString(common.RequestIdKey)
	if requestId == "" {
		requestId = info.RequestId
	}
	text := sanitizeText(promptText(meta, info), config.Get().RiskWatch.CaptureMaxChars)
	return Capture{
		UserId:       info.UserId,
		Username:     truncate(c.GetString("username"), 64),
		TokenId:      info.TokenId,
		TokenName:    truncate(c.GetString("token_name"), 64),
		UserGroup:    truncate(info.UsingGroup, 64),
		ModelName:    truncate(info.OriginModelName, 128),
		RequestId:    truncate(requestId, 64),
		ClientIP:     truncate(common.ClientIP(c), 64),
		IsStream:     info.IsStream,
		PromptTokens: info.GetEstimatePromptTokens(),
		Content:      text.Text,
		ContentChars: text.Chars,
		Truncated:    text.Truncated,
		Files:        describeFiles(filesOf(meta, info)),
		CreatedAt:    common.GetTimestamp(),
	}
}

// expiryFor 把保留天数折算成绝对时刻。0 天 = 永久保留,返回 0。
func expiryFor(retentionDays int, now int64) int64 {
	if retentionDays <= 0 {
		return 0
	}
	return now + int64(retentionDays)*86400
}

// promptText 取本次请求的归一化上下文。
//
// 与违规检测同一条回落链:meta 可能是 fastTokenCountMetaForPricing 产出的
// 精简版(上游关掉 token 计数时),那时 CombineText 是空的,要自己重建一次。
// 重建不便宜,所以调用点必须排在抽样之后。
func promptText(meta *types.TokenCountMeta, info *relaycommon.RelayInfo) string {
	if meta != nil && meta.CombineText != "" {
		return meta.CombineText
	}
	if info.Request == nil {
		return ""
	}
	rebuilt := info.Request.GetTokenCountMeta()
	if rebuilt == nil {
		return ""
	}
	return rebuilt.CombineText
}

// filesOf 取多模态输入的描述符。与 promptText 同一条回落链。
func filesOf(meta *types.TokenCountMeta, info *relaycommon.RelayInfo) []*types.FileMeta {
	if meta != nil && len(meta.Files) > 0 {
		return meta.Files
	}
	if info.Request == nil {
		return nil
	}
	m := info.Request.GetTokenCountMeta()
	if m == nil {
		return nil
	}
	return m.Files
}

// sample 按万分比摇一次骰子。
//
// 用 crypto/rand 而不是 math/rand,与 AI 审核抽样一致:一个能预测自己会不会
// 被记录的用户,可以专挑不会被记录的那些请求发违规内容。代价是一次系统调用,
// 而它只发生在**作用域内**的请求上 —— 作用域外一次都不摇,那正是界面上那个
// 百分比能保持字面意思的原因。
//
// 拿不到随机数时返回 false(不抓)。反过来(默认抓)会让一次熵源故障变成
// "全部作用域内请求 100% 留档",而那是这个存储节点最不能承受的一种失效。
func sample(bps int) bool {
	if bps <= 0 {
		return false
	}
	if bps >= 10000 {
		return true
	}
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return false
	}
	return n.Int64() < int64(bps)
}
