package twophase

import (
	"context"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// PostCommit 是"主库已确认生效之后必须发生的非事务收尾"的**无状态**版本。
//
// Request.AfterCommit 是同一件事的有状态版本:它是业务线程里的闭包,捎带着
// 余额快照、明细结构体这些只存在于那个进程内的东西。进程一崩,那份闭包就消失了,
// 于是 commit 断连(以及"主库已提交但扩展库回写失败")这两条路径上,
// 缓存失效与账本行**从来不会发生** —— 用户的额度已经变了,账单里没有任何一行,
// 缓存 TTL 内看到的余额还是旧的。
//
// PostCommit 只从 qy_fund_orders 那一行重建收尾所需的全部信息,因此补偿任务
// 在任何节点、任何时间都能补做。代价是丢掉进程内快照(例如"变动后余额"),
// 这些字段在补做时留空 —— 少一个展示字段,好过整条账本行不存在。
type PostCommit func(ctx context.Context, order *qymodel.FundOrder) error

var postCommitRegistry = map[string]PostCommit{}

// RegisterPostCommit 由各业务模块在初始化时调用,与 RegisterResolver 成对。
//
// 两者的分工:Resolver 收尾**扩展库的业务明细**(明细置终态、扣佣金余额),
// PostCommit 收尾**主库那一侧的可见结果**(用户缓存、账本日志)。
// 分开是因为前者必须在扩展库事务里跑,后者恰恰不能进任何事务。
func RegisterPostCommit(kind string, fn PostCommit) { postCommitRegistry[kind] = fn }

// claimAfterCommit 认领一张单的"提交后收尾"执行权。
//
// # 为什么需要认领而不是各跑各的
//
// 收尾有两条入口:业务线程的 Request.AfterCommit,与补偿任务的 PostCommit。
// 两条都必须存在(少任何一条就有一类场景永远收不了尾),但账本行只能写一次:
// 写两条,用户在账单里看到的就是同一笔钱被扣了两次 —— 那比少一条更糟,
// 因为它会让用户认定自己被多扣了,而资金侧根本查不出第二笔。
//
// # 为什么是"先认领再执行"而不是"先执行再打标"
//
// 先执行再打标是"至少一次":崩在中间会重放,重放就是重复账本行。
// 先认领再执行是"至多一次":崩在中间会丢一次收尾。资金系统里
// 「少一条信息性日志」远轻于「多一条看起来像重复扣款的日志」,所以选前者。
// 缓存失效不受这个取舍影响 —— 它幂等,补偿任务无条件做一次(见 resolveApplied)。
//
// 认领语句本身失败(扩展库不可用)时返回 false:不抢跑,交给补偿任务。
func claimAfterCommit(ctx context.Context, gdb *gorm.DB, order *qymodel.FundOrder) bool {
	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND after_commit_at = ?", order.OrderNo, 0).
		Update("after_commit_at", now)
	if res.Error != nil {
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 认领提交后收尾失败,本次跳过并交由补偿任务补做: %v",
			order.OrderNo, res.Error))
		return false
	}
	if res.RowsAffected == 0 {
		return false
	}
	order.AfterCommitAt = now
	return true
}

// runPostCommit 在补偿任务确认主库已生效后补做提交后收尾。
//
// 缓存失效**不在认领之内**:它幂等,而且是"主库额度变了"最直接的用户可见后果。
// 认领失败只说明账本行已经有人写过,不说明缓存已经失效过(认领方可能正是崩在
// 写账本之前的那个进程)。所以这里无条件失效一次,再按认领结果决定要不要跑
// 模块自己的 PostCommit。
func runPostCommit(ctx context.Context, gdb *gorm.DB, order *qymodel.FundOrder) {
	// recover 必须包住整个函数体,包括缓存失效。这段代码跑在补偿任务的后台协程里,
	// 而它要去碰 Redis 与各模块的回调 —— 任何一处 panic 冒泡到顶层都会把整个进程
	// 带走,而这一步本身只是"信息性收尾"。
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf(
				"qianye: 单号 %s 的提交后补做发生 panic(已拦截): %v", order.OrderNo, r))
		}
	}()

	invalidateOrderCaches(order)

	fn, ok := postCommitRegistry[order.Kind]
	if !ok {
		return
	}
	if !claimAfterCommit(ctx, gdb, order) {
		return
	}
	if err := fn(ctx, order); err != nil {
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 的提交后补做失败: %v", order.OrderNo, err))
	}
}

// invalidateOrderCaches 失效这张单涉及到的用户缓存。
//
// 主库的 users.quota 已经变了,而用户缓存还是旧值 —— 在 TTL 内,用户看到的
// 余额、以及**限额判定读到的余额**都是错的。资金单上有 UserId / PeerUserId
// 两个字段就够了,不需要任何进程内状态,因此这一步对所有 Kind 通用。
func invalidateOrderCaches(order *qymodel.FundOrder) {
	ids := []int{order.UserId}
	if order.PeerUserId > 0 && order.PeerUserId != order.UserId {
		ids = append(ids, order.PeerUserId)
	}
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if err := model.InvalidateUserCache(id); err != nil {
			common.SysError(fmt.Sprintf(
				"qianye: 单号 %s 失效用户 %d 缓存失败: %v", order.OrderNo, id, err))
		}
	}
}
