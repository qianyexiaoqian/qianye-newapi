package guard

import "github.com/QuantumNous/new-api/common"

// fund_actor.go —— 管理端动钱接口的操作人判据。
//
// 上游对「管理员管理用户」这件事有一条明确的自我保护:controller/user.go 的
// canManageTargetRole 要求 myRole == Root || myRole > targetRole,所以一个
// role=10 的管理员走 POST /api/user/manage {action:add_quota} 给**自己**加额度
// 会被拒。扩展这一侧的动钱接口(手工增减佣金、提现人工裁决)当初直接挂在
// middleware.AdminAuth() 上就算完事,谁都没有再问一遍「操作人和受益人是不是
// 同一个人」—— 于是同一个 role=10 账号可以:凭空给自己记一笔佣金 → 给自己发起
// 提现 → 自己批准这笔提现,终点是可消费的主库额度(quota 单)或站外真钱(fiat 单),
// 全程只有事后审计,没有任何事前控制。
//
// 这里只放**判据**,不放响应:两个模块的错误信封与错误码表各自维护(withdraw 走
// respondErr + errors.go 的错误码表,commission 走 respondFail),把 abort 写进
// guard 会逼其中一边接受另一边的响应形状。判据只有这一份,谁都不许再抄。

// SelfDealing 回答"这次操作的操作人就是受益人吗"。
//
// 零值口径:actorId <= 0 表示**请求上下文里没有可归属的操作人**。这三个调用点
// 全部挂在 middleware.AdminAuth() 之后(它必然 c.Set("id", user.Id)),所以生产
// 上不可能出现;真出现了说明这个处理器被挂到了鉴权链之外,那时"查不出是谁在动钱"
// 本身就该拒绝,而不是因为 0 != targetUserId 顺理成章地放行。
// 因此无操作人一律判为自营,fail-closed。
func SelfDealing(actorId, targetUserId int) bool {
	if actorId <= 0 {
		return true
	}
	return actorId == targetUserId
}

// ManageableTarget 与上游 controller/user.go:373 的 canManageTargetRole 同一条
// 判据:root 谁都能管,其余角色只能管**严格低于**自己的角色。
//
// 它守的是 SelfDealing 之后剩下的那一半:两个 role=10 互相给对方记佣金、
// 再互相批准对方的提现,自审自批的闸门一个都不会响。加上这一条之后,
// 想让佣金账本凭空多出一笔,受益人必须是一个普通用户 —— 那是一条会在
// 用户列表、佣金流水和提现队列里同时留下痕迹的路,而不是管理员之间的闭环。
//
// 零值口径:actorRole 为 0(RoleGuestUser)表示上下文里没有角色,与 SelfDealing
// 同理 fail-closed —— 0 不大于任何合法角色,判据自然返回 false,不需要特判。
func ManageableTarget(actorRole, targetRole int) bool {
	return actorRole == common.RoleRootUser || actorRole > targetRole
}
