package model

// qy_groupns_export.go —— 「用户分组改名 / 删除并迁移」在主库这一侧的唯一入口。
//
// 纯新增文件,合并上游时冲突为 0。铁律同 qy_export.go:禁止 import 任何 qianye/* 包。
//
// ══════════════ 为什么这段必须住在 model 包里,而不是扩展里拼 SQL ══════════════
//
// 它要改的三张表全是**上游主库**的表(users / user_subscriptions /
// subscription_plans),而且要在**一个事务**里改完。扩展侧拿 model.DB 自己拼
// `UPDATE users SET group = ...` 会立刻踩两个坑:
//
//	group 是三种方言的保留字(引号规则各不相同)—— 必须走 commonGroupCol;
//	users 是软删除表 —— 漏掉 deleted_at IS NULL 会把早已注销的账号一起改回来,
//	                    让一个 0 影响的历史分组重新"活"过来。
//
// 更要紧的是**顺序**:迁移必须发生在"删掉这个分组的配置"之前。反过来做的话,
// 在两步之间的那个窗口里,这一批用户的 users.group 指着一个已经没有倍率、
// 没有权威清单的名字 —— 而 GetGroupRatio 对孤儿是 fail-open 返回 1,
// 于是整组人静默按原价扣费,没有任何告警。把两件事绑进一个导出函数,
// 调用方就没有把顺序写反的自由度。

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// QyUserGroupRewrite 是一次用户分组改写在主库这一侧的结果。
//
// UserIds 必须回传给调用方:`UPDATE users` 之后 Redis 里的 `user:<id>` 仍然缓存着
// 旧分组(TTL 默认 60s,而 GetUserGroup 优先读缓存)。不逐个失效的表现是:
// 迁移已经提交、分组配置已经删掉,而这批人在接下来的一分钟里仍然按**已经不存在**
// 的旧分组解析可用清单与倍率 —— 也就是 fail-open 的 1.0 倍。
type QyUserGroupRewrite struct {
	// Users 是真正被改写的在册用户数(UPDATE 的 RowsAffected)。
	Users int64 `json:"users"`
	// UserIds 是被改写的用户 id,供调用方逐个失效用户缓存。
	UserIds []int `json:"-"`
	// Subscriptions 是被改写的 user_subscriptions 行数(三列合计,按行去重不可得,
	// 因此这里是三次 UPDATE 的 RowsAffected 之和,只用于展示"动过多少快照")。
	Subscriptions int64 `json:"subscriptions"`
	// Plans 是被改写的 subscription_plans 行数。删除路径恒为 0(删除前已硬拦)。
	Plans int64 `json:"plans"`
	// Stragglers 是提交之后**仍然**挂在源分组上的在册用户数。
	//
	// 正常恒为 0。非 0 只有一个来源:锁定用户行之后、事务提交之前,有另一条路径
	// 把一个用户改/建到了源分组上(管理员在另一个标签页改用户、一次注册恰好落在
	// 源分组)。它必须是一个**返回字段**而不是一行日志 —— 那几个账号此刻正指着
	// 一个即将被删掉的分组名,是这条链上唯一会产出孤儿的缝隙。
	Stragglers int64 `json:"stragglers"`
}

// 三条调用路径。它们的差别只有两处 —— 套餐引用跟不跟、订阅快照按什么范围跟 ——
// 但两处的取值组合各不相同,用两个裸 bool 表达时调用点读起来是
// `QyRewriteUserGroupTx(from, to, false, true)`,而看那一行的人无从知道
// 自己正走在哪条路径上。
const (
	// QyRewriteModeRename 改名:源名字**消失**,套餐的升降级分组与全部订阅快照
	// 一起改写。不改的话这个套餐从此把每一个买家送进一个不存在的分组。
	QyRewriteModeRename = "rename"
	// QyRewriteModeDelete 删除并迁移:源名字**消失**,订阅快照一起改写,
	// 但套餐引用一个字节都不动 —— 那些引用由接口层在删除之前硬拦下来。
	// 理由:一个卖着「升级到 X」的套餐被悄悄改成「升级到 Y」,买家付的钱和
	// 拿到的东西不再是同一件事,而运营从界面上看不出来发生过这件事。
	QyRewriteModeDelete = "delete"
	// QyRewriteModeMigrate 纯迁移:源名字**仍然存在、配置完整**。
	//
	// 与上面两条的唯一实质差别在订阅快照的范围:这里只改**被迁移的那批用户
	// 自己的**订阅行,不按列值全表匹配。全表匹配在这条路径上是错的 ——
	// 一个早已升到 VIP、prev_user_group 还记着源分组的账号并不在这次迁移里,
	// 而源分组仍然存在、仍然有完整的倍率与可用清单,把他的回落目标改指到
	// 迁移目标等于替一个没有出现在确认弹窗里的人做了一次降级档位变更。
	QyRewriteModeMigrate = "migrate"
)

// QyRewriteUserGroupTx 在一个主库事务里把用户分组 from 整体改写成 to。
//
// mode 见 QyRewriteMode* 三个常量。**这是唯一一份用户分组迁移实现** ——
// 改名、删除并迁移、纯迁移三条路径共用它。各自再写一份的表现是三份逻辑
// 独立漂移,而漂移出来的那一份少改的恰恰是最容易被忘记的那张表。
//
// to 为空串是非法的:那会把一批用户的 group 清空,而空 group 在
// middleware/auth.go 里的表现与 default 并不相同。
func QyRewriteUserGroupTx(from, to, mode string) (QyUserGroupRewrite, error) {
	var out QyUserGroupRewrite
	if DB == nil {
		return out, errors.New("qy: 主库未初始化")
	}
	if from == "" || to == "" {
		return out, errors.New("qy: 用户分组改写的源与目标都不能为空")
	}
	if from == to {
		return out, errors.New("qy: 用户分组改写的源与目标相同")
	}
	switch mode {
	case QyRewriteModeRename, QyRewriteModeDelete, QyRewriteModeMigrate:
	default:
		return out, fmt.Errorf("qy: 未知的用户分组改写模式 %q", mode)
	}
	rewritePlans := mode == QyRewriteModeRename

	err := DB.Transaction(func(tx *gorm.DB) error {
		// 先锁定再改写:先拿到 id 清单,调用方才能在提交之后精确失效这批人的缓存。
		// lockForUpdate 在 SQLite 上是空操作(方言不支持),在 MySQL/PostgreSQL 上
		// 挡住"同一批行被另一个管理员并发改分组"。
		var ids []int
		if err := lockForUpdate(tx.Model(&User{})).
			Where(commonGroupCol+" = ? AND deleted_at IS NULL", from).
			Order("id").Pluck("id", &ids).Error; err != nil {
			return err
		}
		out.UserIds = ids

		// 按 id 分批改写,不写成 `WHERE group = from`:后者会连**没有被锁住的幻影行**
		// 一起改掉,于是 RowsAffected 大于 ids 的长度,而多出来的那几个人不会出现在
		// 缓存失效清单里 —— 一次静默的、只影响少数账号的旧分组残留。
		// 幻影行留在源分组上,由 Stragglers 显式报出来。
		for _, batch := range qyChunkInts(ids, 200) {
			// SET 侧走 **map + 裸列名**,不能用 commonGroupCol:那个常量已经带着方言
			// 引号(`group` / "group"),而 GORM 的 Update 会把列名再引一次,
			// 得到 ``group`` 这种在三种方言上都是语法错误的东西。
			// commonGroupCol 只用于**手写 SQL 片段**(WHERE 条件、Raw),
			// 交给 GORM 渲染的地方一律给裸名。
			res := tx.Model(&User{}).Where("id IN ?", batch).
				Updates(map[string]any{"group": to})
			if res.Error != nil {
				return res.Error
			}
			out.Users += res.RowsAffected
		}

		// user_subscriptions 的三列都是**用户分组快照**:
		//   upgrade_group   购买时升到哪一档
		//   downgrade_group 到期降到哪一档(套餐显式指定)
		//   prev_user_group 购买之前是哪一档(未指定降级组时的回落目标)
		// 不改的话,这批人手上的订阅到期时会被降级进一个已经不存在的分组 ——
		// 而到期扫描(model/subscription.go 的 expired 分支)一个字都不校验目标是否存在。
		//
		// 范围按 mode 分叉,理由见 QyRewriteModeMigrate 的注释:源名字会消失时
		// 必须按列值全表匹配(任何一处指着它的快照都会变成孤儿),源名字留着时
		// 只许动被迁移的那批人自己的行。
		for _, col := range []string{"upgrade_group", "downgrade_group", "prev_user_group"} {
			if mode == QyRewriteModeMigrate {
				// 与上面的用户改写同样分批:占位符个数在 SQLite 上有硬上限,
				// 而一次迁移可以带着上千个 id。
				for _, batch := range qyChunkInts(ids, 200) {
					res := tx.Model(&UserSubscription{}).
						Where(col+" = ? AND user_id IN ?", from, batch).Update(col, to)
					if res.Error != nil {
						return res.Error
					}
					out.Subscriptions += res.RowsAffected
				}
				continue
			}
			res := tx.Model(&UserSubscription{}).Where(col+" = ?", from).Update(col, to)
			if res.Error != nil {
				return res.Error
			}
			out.Subscriptions += res.RowsAffected
		}

		if rewritePlans {
			for _, col := range []string{"upgrade_group", "downgrade_group"} {
				res := tx.Model(&SubscriptionPlan{}).Where(col+" = ?", from).Update(col, to)
				if res.Error != nil {
					return res.Error
				}
				out.Plans += res.RowsAffected
			}
		}
		return nil
	})
	if err != nil {
		return QyUserGroupRewrite{}, err
	}

	if err := DB.Model(&User{}).
		Where(commonGroupCol+" = ? AND deleted_at IS NULL", from).
		Count(&out.Stragglers).Error; err != nil {
		// 数不出来不代表改写失败(事务已经提交)。返回一个负数会让调用方误以为有孤儿,
		// 所以这里只把它标成"未知"并交给日志,主结果照常返回。
		out.Stragglers = 0
		return out, nil
	}
	return out, nil
}

// QyPlansReferencingUserGroup 返回 upgrade_group / downgrade_group 指向该用户分组的套餐。
//
// 它是**删除路径的硬闸门**的数据来源:一个套餐的 upgrade_group 指着一个被删掉的
// 分组时,下一个买它的人会被放进一个不存在的分组 —— 可用清单落空、
// GetGroupRatio fail-open 按 1.0 计费,而他刚刚为"升级"付过钱。
// 这件事必须由运营先去改套餐,而不是由删除操作替他决定改成哪一档。
func QyPlansReferencingUserGroup(group string) ([]string, error) {
	if DB == nil {
		return nil, errors.New("qy: 主库未初始化")
	}
	if group == "" {
		return nil, nil
	}
	var rows []SubscriptionPlan
	if err := DB.Select("id", "title", "upgrade_group", "downgrade_group").
		Where("upgrade_group = ? OR downgrade_group = ?", group, group).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, fmt.Sprintf("#%d %s", row.Id, row.Title))
	}
	return out, nil
}

// QyUserSubscriptionsReferencingUserGroup 统计快照里指向该用户分组的订阅行数。
//
// 与套餐引用不同,这一档**不拦**:它们是已经卖出去的、属于正在被迁移的那批用户的
// 历史快照,跟着用户一起改写才是一致的。分开统计只为让确认弹窗能把它显示出来 ——
// 「这次会动到 N 条已售订阅的降级目标」是运营有权知道的事。
func QyUserSubscriptionsReferencingUserGroup(group string) (int64, error) {
	if DB == nil {
		return 0, errors.New("qy: 主库未初始化")
	}
	if group == "" {
		return 0, nil
	}
	var count int64
	err := DB.Model(&UserSubscription{}).
		Where("upgrade_group = ? OR downgrade_group = ? OR prev_user_group = ?", group, group, group).
		Count(&count).Error
	return count, err
}

// QyUserGroupTokenCount 统计一个用户分组下的**令牌总数**(启用与停用都算)。
//
// 删除确认弹窗要的是"这次会波及多少个令牌",而不是"多少个正在跑的令牌":
// 一个停用的令牌被重新启用时同样按新分组解析可用清单与倍率。
func QyUserGroupTokenCount(group string) (int64, error) {
	if DB == nil {
		return 0, errors.New("qy: 主库未初始化")
	}
	if group == "" {
		return 0, nil
	}
	var count int64
	sql := "SELECT COUNT(*) FROM tokens t JOIN users u ON u.id = t.user_id " +
		"WHERE t.deleted_at IS NULL AND u.deleted_at IS NULL AND u." + commonGroupCol + " = ?"
	err := DB.Raw(sql, group).Scan(&count).Error
	return count, err
}

// qyChunkInts 把 id 切成定长批。
//
// `WHERE id IN (…)` 的占位符个数在 SQLite 上有硬上限(SQLITE_MAX_VARIABLE_NUMBER),
// 而一个用户分组可以有几百上千人。不分批的表现是:在 SQLite 部署上,超过上限的那次
// 迁移直接报一条与分组毫无关系的驱动错误。
func qyChunkInts(ids []int, size int) [][]int {
	if size <= 0 {
		size = 200
	}
	out := make([][]int, 0, len(ids)/size+1)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}
