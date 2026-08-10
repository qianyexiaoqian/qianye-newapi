package violation

import (
	"context"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"gorm.io/gorm"
)

// errBanSkipped 表示目标不需要封禁(root / 已禁用 / 不存在)。是幂等成功,不是错误。
var errBanSkipped = errors.New("qianye/violation: 封禁已跳过")

// disableUserForViolation 是完整的封号实现。
//
// 主库没有可复用的原子封号函数,必须自己拼齐六步。少任何一步都会留下
// "已被禁用但旧凭证仍可用"的安全洞,也就是封号形同虚设:
//
//	① 条件 UPDATE status —— 天然幂等,并发只会有一个 RowsAffected == 1
//	② 同事务递增 auth_version —— 与 ① 必须同事务:先改 status 后崩溃,旧 JWT/session
//	   仍有效;先 bump 后崩溃,用户被登出却没被禁用,重新登录即可继续用
//	③ PublishUserAuthCache —— middleware/auth.go 判 relay 放行读的就是这份用户缓存
//	④ InvalidateUserTokensCache —— 不清则已缓存的 API 令牌在 TTL 内仍能过第一道校验
//	⑤ RevokeAllUserSessions —— 显式吊销控制台会话,并让管理端看得到
//	⑥ 写主库审计 —— 与管理员手动封禁同口径,事故复盘时能分清人干的还是程序干的
//
// 刻意不用 user.Update(false):它是 Updates(struct)、零值跳过、无 RowsAffected
// 幂等保护,多节点并发会双写、双 bump、双吊销、双审计。
//
// ctx 由调用方(guard 的热路径 worker / 封禁补偿任务)注入并透传到主库事务上。
// 不接 ctx 的话,这条链路上最重的一步 —— 带 FOR UPDATE 的主库事务 —— 完全没有
// 上界,慢查询会把 worker 长期占死(C3)。
//
// 仍然没有 ctx 的三步是上游函数,签名里就没有 ctx:PublishUserAuthCache、
// InvalidateUserTokensCache、RevokeAllUserSessions(后者是无上限的批循环)。
// 它们的风险由 guard 侧兜住:封号作业不在 syncSafeJobs 里,永远不会同步跑在
// relay 线程上,最坏只是占用一个后台 worker。
func disableUserForViolation(ctx context.Context, userId int, ban *Ban) error {
	u, err := model.GetUserById(userId, false)
	if err != nil {
		return err
	}
	if u == nil {
		return errBanSkipped
	}
	// 永不自动封禁 root:一条写错的规则把超管封了,就再也没人能进来改规则。
	if u.Role >= common.RoleRootUser {
		return errBanSkipped
	}
	if u.Status != common.UserStatusEnabled {
		return errBanSkipped
	}

	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).
			Where("id = ? AND status = ? AND role < ?",
				userId, common.UserStatusEnabled, common.RoleRootUser).
			Update("status", common.UserStatusDisabled)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errBanSkipped
		}
		_, e := model.IncrementUserAuthVersionWithTx(tx, userId)
		return e
	})
	if errors.Is(err, errBanSkipped) {
		return errBanSkipped
	}
	if err != nil {
		return err
	}

	// 缓存失效三处缺一不可。任何一处失败都只记日志:用户已经在 DB 层被禁用,
	// 缓存最坏是在 TTL 内延迟生效,回滚封禁反而更糟。
	if e := model.PublishUserAuthCache(userId); e != nil {
		common.SysError("qianye/violation: PublishUserAuthCache 失败: " + e.Error())
		// 兜底:退化成删缓存,让下一次请求回源数据库。
		if e2 := model.InvalidateUserCache(userId); e2 != nil {
			common.SysError("qianye/violation: InvalidateUserCache 同样失败: " + e2.Error())
		}
	}
	if e := model.InvalidateUserTokensCache(userId); e != nil {
		common.SysError("qianye/violation: InvalidateUserTokensCache 失败: " + e.Error())
	}
	// 第 ⑤ 步只属于 ban 档。restrict 刻意保留控制台会话:那一档的目的是止损
	// (relay 已经被 status 挡死),不是把人赶出站 —— 用户仍然登录着,
	// 能立刻看到自己被限制并直接提工单。auth_version 已经在事务里递增过,
	// 所以两档在凭证层面的强度是一样的,差别只在"要不要主动踢掉会话"。
	//
	if revokesSessions(ban.PolicyAction) {
		if _, e := model.RevokeAllUserSessions(userId, "qy_violation_auto_ban"); e != nil {
			common.SysError("qianye/violation: RevokeAllUserSessions 失败: " + e.Error())
		}
	}

	model.RecordLogWithAdminInfo(userId, model.LogTypeManage,
		fmt.Sprintf("账号因违规次数达到阈值(%d 次)被系统自动置为受限", ban.HitCountAt),
		map[string]interface{}{
			"source":            "qy_violation",
			"qy_ban_id":         ban.Id,
			"qy_ban_cycle":      ban.BanCycle,
			"qy_threshold":      ban.Threshold,
			"qy_hit_count":      ban.HitCountAt,
			"qy_trigger_record": ban.TriggerRecordId,
			// 策略档必须进用户日志:同一个"受限"结果可能来自不同分组的不同阈值,
			// 而客服在工单里首先要回答的就是"我为什么在第 3 次就被限制了"。
			"qy_policy_group":  ban.PolicyGroup,
			"qy_policy_action": ban.PolicyAction,
		})
	return nil
}

// revokesSessions 回答"这一档处置要不要把用户踢出控制台"。
//
// 提成函数不是为了缩短调用点,而是因为这是 restrict 与 ban **唯一**的行为差别,
// 而它藏在一个需要主库、用户表、会话表才能跑起来的函数中间 —— 埋在那里的话,
// 任何一次"顺手把条件反过来"的改动都不会有任何测试变红。
//
// 判据写成"哪些档要吊销"而不是"哪些档不吊销":空 PolicyAction 是本列出现之前
// 写下的历史行,它们当时的行为就是吊销会话,所以未知取值必须落在吊销那一侧。
// 反着写(`!= PolicyActionRestrict`)在正常数据上完全等价,差别只在
// 有人新增第四档动作却忘了改这里的那一天。
func revokesSessions(action string) bool {
	switch action {
	case PolicyActionRestrict:
		// 受限档保留会话:relay 已经被 status 挡死,目的是止损而不是驱逐。
		// 用户仍然登录着,能立刻看到自己被限制并直接提工单。
		return false
	case PolicyActionRecord:
		// 「仅记录」根本走不到这里(resolveBanClaim 在它之前就返回了)。
		// 真的走到了说明上游判据被改坏,此时最不该做的就是把人踢出去。
		return false
	default:
		return true
	}
}

// enableUserAfterUnban 是解封,与封号严格对称。
//
// 同样是条件 UPDATE + 同事务 bump auth_version:auth_version 递增会让封禁期间
// 签发的任何残留凭证一并失效,解封后用户重新登录拿到干净的会话。
//
// 只对"可能真的禁用过用户"的封禁行调用(banned / pending / failed)。BanDeferred
// 从来没有执行过主库那六步,对它调这里会把一个因别的原因被停用的账号放出来 ——
// 判定在 unbanUser 里,见那里的说明。
func enableUserAfterUnban(userId int, ban *Ban, operatorId int) error {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.User{}).
			Where("id = ? AND status = ?", userId, common.UserStatusDisabled).
			Update("status", common.UserStatusEnabled)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errBanSkipped
		}
		_, e := model.IncrementUserAuthVersionWithTx(tx, userId)
		return e
	})
	if err != nil && !errors.Is(err, errBanSkipped) {
		return err
	}

	if e := model.PublishUserAuthCache(userId); e != nil {
		common.SysError("qianye/violation: 解封后 PublishUserAuthCache 失败: " + e.Error())
		_ = model.InvalidateUserCache(userId)
	}
	if e := model.InvalidateUserTokensCache(userId); e != nil {
		common.SysError("qianye/violation: 解封后 InvalidateUserTokensCache 失败: " + e.Error())
	}

	model.RecordLogWithAdminInfo(userId, model.LogTypeManage, "违规自动封禁已由管理员解除",
		map[string]interface{}{
			"source":       "qy_violation",
			"qy_ban_id":    ban.Id,
			"qy_ban_cycle": ban.BanCycle,
			"qy_operator":  operatorId,
		})
	return nil
}
