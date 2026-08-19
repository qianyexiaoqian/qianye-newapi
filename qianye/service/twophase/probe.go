package twophase

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
)

// MainVerdict 是"主库那一侧到底动没动"的判定结果。
//
// 必须是三态而不是 bool。资金系统里"我不知道"与"确定没动"是两件完全不同的事:
// 前者只允许转人工,后者才允许回滚预占 / 换代次重发。把二者合并成一个 false
// 正是超发与错扣的来源 —— 探针关掉、探针查询报错、探针行已被清理,这三种
// "查不到"都会被读成"确定没动",于是一笔已经动过的钱被再动一次。
type MainVerdict int8

const (
	// MainUnknown 是零值,也是唯一安全的默认:任何忘记赋值、任何新增的
	// 判不出来的分支,都退化成"交给人",而不是退化成"可以再发一次钱"。
	MainUnknown MainVerdict = iota
	// MainApplied 表示主库事务确定已提交(探针行在)。
	MainApplied
	// MainNotApplied 表示主库事务确定没有提交(探针可用、查得清楚、且探针行
	// 不可能被清理掉)。只有这一个取值允许调用方做不可逆的回滚或重发。
	MainNotApplied
)

// ProbeMainSide 用主库 outbox 探针判定一张资金单的主库副作用是否真的生效。
//
// 为什么需要它:主库事务在 commit 阶段断连时 GORM 会返回错误,但事务其实可能
// 已经提交。markFailed 对**任何** mainErr 都把单置成 Failed,所以 Failed
// 绝不蕴含"主库没扣/没加钱"。唯一的权威判据是与资金变更同事务写入的 outbox 行。
//
// 判不出来的三种情况一律 MainUnknown:
//
//   - 探针整体关掉(main_outbox_enabled=false):此时 applyOnMainDB 连认领都不做,
//     跨单去重完全不存在,"查不到"更不能当成"没生效"。
//   - 探针查询报错:主库不可用不代表钱没动。
//   - 单据本身缺失。
//
// 这条政策与 compensateOne 的 markUncertain("绝不猜测 —— 猜错就是资损")一致,
// 也与 design-00-foundation.md「查不到一律置 Uncertain,不敢置 Failed」一致。
//
// MainNotApplied 的正确性依赖一条跨函数不变量:**PruneOutbox 只清理 Success 单的
// 探针行**。非 Success 的单(Failed / Pending / Uncertain)正是需要探针来判定的那些,
// 它们的探针行必须永久保留,否则"查不到"就变成了保留期的副产品。
func ProbeMainSide(order *qymodel.FundOrder) MainVerdict {
	if order == nil || order.OrderNo == "" {
		return MainUnknown
	}
	if !config.Get().TwoPhase.OutboxEnabled() {
		common.SysError("qianye: 单号 " + order.OrderNo +
			" 未启用主库 outbox 探针,无法判定主库是否已生效,按不可判定处理")
		return MainUnknown
	}
	applied, err := model.QyProbeFundOutbox(order.OrderNo)
	if err != nil {
		common.SysError("qianye: 单号 " + order.OrderNo +
			" 探测主库 outbox 失败,按不可判定处理: " + err.Error())
		return MainUnknown
	}
	if applied {
		return MainApplied
	}
	return MainNotApplied
}
