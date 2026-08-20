package guard

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

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

// ── 目标可作用性的统一入口 ────────────────────────────────────────────────
//
// SelfDealing 与 ManageableTarget 是两条判据,但「管理端的这个写动作能不能落在
// 这个用户头上」在实践中永远是同一个问题:先问是不是自己,再问对方是不是同级
// 或更高。分开摆着的结果是每个调用点自己拼一遍,而本轮梳理查出的六处漏判
// (佣金已提现迁移、邀请关系绑定/换绑、手动结算、提现同级互批、支付密码重置、
// 违规记录撤销/解封/计数清零)全部是「拼漏了其中一条」的形状。
//
// 因此判据留在上面不动,组合与角色回查收进 ActorMayActOn 一处。它只返回哨兵
// 错误、不写响应:withdraw 走 respondErr、commission 走 respondFail、
// violation 与 paypass 各有自己的错误码表,把 abort 写进 guard 会逼其中三家
// 接受第四家的响应形状。

var (
	// ErrActorIsTarget:操作人就是受益人。
	ErrActorIsTarget = errors.New("不能对自己执行这个操作,请由另一位管理员操作")
	// ErrTargetNotLower:目标是同级或更高权限的账号。
	ErrTargetNotLower = errors.New("不能对同级或更高权限的账号执行这个操作")
	// ErrTargetMissing:目标用户不存在。
	ErrTargetMissing = errors.New("目标用户不存在")
)

// ActorMayActOn 回答「操作人能不能把这个写动作落在 targetUserId 头上」。
//
// 顺序刻意是「先自营、后越级」:自营不需要查库,而越级要回查目标角色。一个
// role=10 管理员对自己发起的操作,两条判据都会拒,但只有前者的错误信息能告诉
// 他「换个人来」—— 后者会说「不能操作同级」,读起来像是他填错了 user_id。
//
// 零值口径:
//   - actorId <= 0 走 SelfDealing 的 fail-closed(见上),直接 ErrActorIsTarget;
//   - targetUserId <= 0 是调用点没校验参数,返回 ErrTargetMissing 而不是去查库;
//   - actorRole 为 0 时 ManageableTarget 恒 false,同样 fail-closed。
func ActorMayActOn(ctx context.Context, actorId, actorRole, targetUserId int) error {
	if SelfDealing(actorId, targetUserId) {
		return ErrActorIsTarget
	}
	if targetUserId <= 0 {
		return ErrTargetMissing
	}
	if model.DB == nil {
		return db.ErrNotReady
	}
	// Unscoped:软删除的账号仍然要按它的角色判。上游的删除是软删,一个被删掉的
	// role=100 账号如果因为 deleted_at 不为空就查不到,判据会当成"目标不存在"
	// 而放行 —— 而放行的正是"对一个更高权限账号动手"这件事。
	var row struct{ Role int }
	err := model.DB.WithContext(ctx).Unscoped().Model(&model.User{}).
		Select("role").Where("id = ?", targetUserId).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrTargetMissing
	}
	if err != nil {
		return err
	}
	if !ManageableTarget(actorRole, row.Role) {
		return ErrTargetNotLower
	}
	return nil
}

// ActorMayActOnCtx 是 ActorMayActOn 的 gin 版:操作人一律取鉴权中间件写进
// context 的身份,调用点不许自己传 —— 传参数的版本迟早会有人把请求体里的
// user_id 当成操作人传进来。
func ActorMayActOnCtx(c *gin.Context, targetUserId int) error {
	return ActorMayActOn(c.Request.Context(), c.GetInt("id"), c.GetInt("role"), targetUserId)
}
