package violation

import (
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
func disableUserForViolation(userId int, ban *Ban) error {
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

	err = model.DB.Transaction(func(tx *gorm.DB) error {
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
	if _, e := model.RevokeAllUserSessions(userId, "qy_violation_auto_ban"); e != nil {
		common.SysError("qianye/violation: RevokeAllUserSessions 失败: " + e.Error())
	}

	model.RecordLogWithAdminInfo(userId, model.LogTypeManage,
		fmt.Sprintf("账号因违规次数达到阈值(%d 次)被系统自动禁用", ban.HitCountAt),
		map[string]interface{}{
			"source":            "qy_violation",
			"qy_ban_id":         ban.Id,
			"qy_ban_cycle":      ban.BanCycle,
			"qy_threshold":      ban.Threshold,
			"qy_hit_count":      ban.HitCountAt,
			"qy_trigger_record": ban.TriggerRecordId,
		})
	return nil
}

// enableUserAfterUnban 是解封,与封号严格对称。
//
// 同样是条件 UPDATE + 同事务 bump auth_version:auth_version 递增会让封禁期间
// 签发的任何残留凭证一并失效,解封后用户重新登录拿到干净的会话。
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
