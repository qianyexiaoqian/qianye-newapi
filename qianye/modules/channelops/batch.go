package channelops

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
)

// batch.go —— "逐条执行、逐条汇报"的公共骨架。
//
// # 这个文件存在的唯一理由
//
// 需求原话:「选了 20 个渠道,第 7 个删失败。不能整体回滚(前 6 个已经删了),
// 也不能报『全部成功』。要逐条返回结果并在界面上把失败的那几条单独列出来。」
//
// 这句话在协议层的含义是:批次的返回值不能是一个整数。上游三个批量端点
// (DeleteChannelBatch / BatchUpdateChannelStatus / BatchSetChannelTag)
// 回的都是整数 count,于是前端只能写 `failCount = ids.length - count` ——
// 而那个减法在启停这条路上是**错的**,见 outcomeSkipped。
//
// # 为什么 total = succeeded + skipped + failed 必须恒成立
//
// 它是前端唯一能信的东西。少了这条恒等式,"20 选中、18 成功"就无法回答
// 剩下 2 个是失败了还是本来就不用动 —— 而这两件事一个要处理、一个不用管。

// 单条结果的三档。它们**不是**成功/失败两分:
//
//	outcomeOK       真的改了库(删掉了 / 状态翻了 / 统计清了)
//	outcomeSkipped  库里本来就是目标状态,一个字节都不用动。**这不是失败**
//	outcomeFailed   没做成,需要人来看
//
// 中间这一档是本模块存在的一半理由。上游 model.UpdateChannelStatus 对
// "已经是目标状态"和"渠道不存在 / 保存失败"返回的都是 false,而
// controller.BatchUpdateChannelStatus 把 false 一律计入"没改动",
// 前端再拿 len(ids) 一减 —— 于是"选 20 个全部启用,其中 18 个本来就是启用的"
// 会显示成「18 个渠道启用失败」。管理员会去排查一个根本不存在的故障。
const (
	outcomeOK      = "ok"
	outcomeSkipped = "skipped"
	outcomeFailed  = "failed"
)

// itemResult 是单个渠道的执行结果。
//
// Name 一定要带上:失败列表里只有 id 的话,管理员得回列表页一个个对照才知道
// 挂掉的是哪个渠道,而这一屏正是他做决定的地方。
type itemResult struct {
	Id      int    `json:"id"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	// Code 是稳定标识,前端据此映射 i18n 文案;Detail 是中文兜底,
	// 只在 Code 未被前端登记时兜住,不保证可翻译。
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// batchResult 是三个端点共用的返回体。
type batchResult struct {
	Total     int          `json:"total"`
	Succeeded int          `json:"succeeded"`
	Skipped   int          `json:"skipped"`
	Failed    int          `json:"failed"`
	Items     []itemResult `json:"items"`
}

// applyFunc 对单个渠道执行一次操作,返回该条的结局。
//
// 返回 (outcome, code, detail):code/detail 只在 outcomeFailed 时有意义,
// outcomeSkipped 由调用方自己填 itemCodeNoChange 之外的解释。
type applyFunc func(ctx context.Context, ch *model.Channel) (outcome string, code string, detail string)

// runBatch 逐条执行 apply,并把结果汇总成 batchResult。
//
// # 三个刻意的选择
//
//  1. **每条一个 ColdContext**。批次不共用一个 3 秒预算:共用的话,第 200 条
//     必然超时,而它超时的原因是前面 199 条把预算花光了,不是它自己有问题。
//     每条独立计时之后,"这一条卡住了"才是一句真话。整个请求的上界由 HTTP
//     层和下面的 parent.Err() 兜住。
//
//  2. **调用方断开就停,并把没跑的标成 failed 而不是悄悄少几行**。
//     管理员关掉页面之后剩下的渠道确实没被处理,报告必须说出来 ——
//     少几行等于让 total 与三档之和对不上,那正是这份报告要消灭的模糊。
//
//  3. **先按 id 读一次行**。读不到就是 not_found(多半是别的管理员刚删掉),
//     读到了才把 Name 带进结果。这一次读也是"批量启停"能分辨
//     skipped 与 failed 的依据 —— 光看 UpdateChannelStatus 的 bool 分辨不了。
func runBatch(parent context.Context, ids []int, apply applyFunc) batchResult {
	res := batchResult{Total: len(ids), Items: make([]itemResult, 0, len(ids))}
	aborted := false
	for _, id := range ids {
		item := itemResult{Id: id}
		switch {
		case aborted || parent.Err() != nil:
			aborted = true
			item.Outcome, item.Code = outcomeFailed, itemCodeAborted
			item.Detail = "请求已中断,该渠道未被处理"
		default:
			item = applyOne(parent, id, apply)
		}
		switch item.Outcome {
		case outcomeOK:
			res.Succeeded++
		case outcomeSkipped:
			res.Skipped++
		default:
			res.Failed++
		}
		res.Items = append(res.Items, item)
	}
	return res
}

// applyOne 处理单个渠道:独立预算 → 读行 → 执行。
func applyOne(parent context.Context, id int, apply applyFunc) itemResult {
	ctx, cancel := guard.ColdContext(parent)
	defer cancel()

	item := itemResult{Id: id}
	ch, err := loadChannel(ctx, id)
	if err != nil {
		item.Outcome = outcomeFailed
		if errors.Is(err, errChannelGone) {
			item.Code, item.Detail = itemCodeNotFound, "渠道不存在,可能已被其他管理员删除"
			return item
		}
		item.Code, item.Detail = itemCodeDBError, "读取渠道失败"
		return item
	}
	item.Name = ch.Name
	item.Outcome, item.Code, item.Detail = apply(ctx, ch)
	return item
}

// normalizeIds 校验并去重入参 id。
//
// # 为什么非法 id 是整批 400 而不是逐条 failed
//
// id <= 0 不可能来自列表页的勾选框,它只可能来自手搓的请求或前端的 bug。
// 把它计成"这一条失败了"会让报告里混进一条管理员看不懂、也无从处理的行;
// 整批拒绝反而是准确的:这个请求本身有问题。
//
// 去重则相反,静默合并:同一个 id 在选中集里出现两次是前端的事,
// 对管理员来说"这个渠道被删了"就是一件事,报两行只会让计数看起来对不上。
func normalizeIds(raw []int) ([]int, error) {
	if len(raw) == 0 {
		return nil, errNoIds
	}
	if len(raw) > maxBatchIds {
		return nil, errTooMany
	}
	seen := make(map[int]struct{}, len(raw))
	out := make([]int, 0, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, errInvalidParam
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// maxBatchIds 是单次批量的上限。
//
// 取 200 与上游 model.BatchDeleteChannels 的分片大小一致,不是拍脑袋:
// 超过这个量级的操作应当分批,否则一次请求要串行跑 200 个事务,
// 前端等待期间没有任何进度可言,而中途断开的那一半会全部落进 aborted。
const maxBatchIds = 200
