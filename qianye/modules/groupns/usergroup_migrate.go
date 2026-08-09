package groupns

// usergroup_migrate.go —— 「把这一档人整体挪到另一档」这个**独立动作**。
//
// ══════════════════ 项目方原话 ══════════════════
//
//	「既然 default 的用户分组无法删除,那么你就在用户分组这里增加一个用户分组迁移的
//	 功能,显示这个用户组有多少人,一键迁移。到哪个用户组。」
//
// ══════════════ 为什么它必须是一个独立接口,而不是删除的一个选项 ══════════════
//
// 迁移这段逻辑此前只作为**删除的副产品**存在(adminDeleteUserGroup 里的那一步)。
// 而删除对 default 是硬拒的 —— default 是 users.group 这一列的数据库默认值,
// 删掉它之后任何一次没有显式指定分组的建号都会落进一个不存在的分组。
// 于是运营真正想做的那件事(「把这 707 个人挪走」)在界面上无路可走:
// 唯一的入口被一道与他的目的毫无关系的闸门挡着。
//
// ══════════════ 迁移与删除在后端也必须是两件事 ══════════════
//
//	源分组的配置     一个字节都不动(范围 / 授权 / 交叉倍率 / 充值倍率 / 登记行全留着)
//	源分组本身       仍然存在。迁完之后它只是变成了一档 0 人的分组
//	套餐引用         **不拦**。套餐指向的是分组名,人走了名字还在,那些套餐照常工作
//	block 残留       **不拦**。它们指着一个仍然存在、仍然配置完整的名字,不是孤儿
//	订阅快照         只改**被迁移的那批人自己的**(见 model.QyRewriteModeMigrate)
//
// 反过来说,不拦的这两项恰恰意味着**迁完之后这一档会被重新填回来**:套餐把下一个
// 买家送进来、「新注册用户默认分组」把下一个注册的人送进来。这件事必须在返回值里
// 显式列出来(Refills),否则运营会以为迁完就一劳永逸了,而人数会自己长回去。

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const auditActionUserGroupMigrate = "groupns.user_group.migrate"

type migrateUserGroupRequest struct {
	// Target 是这批用户要迁去的用户分组。**必填**,且必须已登记。
	Target string `json:"target"`
	// ExpectUsers 是运营在界面上看到的人数,对不上返回 409。
	//
	// 与删除路径同一道闸门、同一个理由:弹窗打开时是 3 个人、按下按钮时已经是
	// 300 个人,而运营是照着 3 这个数字做的决定。负数表示调用方放弃这道闸门(脚本用)。
	ExpectUsers int64 `json:"expect_users"`
	// AckLosesEverything 是「迁过去之后这批人一个模型分组都用不了」的显式覆盖。
	AckLosesEverything bool `json:"ack_loses_everything"`
}

// UserGroupMigrateResult 是一次纯迁移的结果。
//
// 刻意**不复用 RewriteResult**:那个结构体带着 Residues 与 Rename 两个字段,
// 而纯迁移一条残留都不清、也不是改名。带着两个恒为空/恒为 false 的字段回到界面上,
// 读它的人第一件事是去猜"是不是有什么没清干净"。
type UserGroupMigrateResult struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Users 是真正被改写的在册用户数。
	Users int64 `json:"users"`
	// Subscriptions 是被改写的订阅快照行数(只含被迁移用户自己的那些)。
	Subscriptions int64 `json:"subscriptions"`
	// Stragglers 是提交之后仍然挂在源分组上的在册用户数。
	//
	// 与删除路径不同,它在这里**不是错误**:源分组仍然完整,掉队的那几个账号
	// 指着一个配置齐全的名字,不是孤儿。再按一次迁移即可收敛。
	Stragglers int64 `json:"stragglers"`
	// CacheMisses 是缓存失效失败的用户数。非 0 = 这批人在 TTL 内仍按旧分组计价。
	CacheMisses int `json:"cache_misses"`
	// Diff 是迁移前后的可用清单与倍率差,与确认弹窗看到的那一份同源。
	Diff MigrationDiff `json:"diff"`
	// SourceRemains 恒为 true,而且必须出现在响应里。
	//
	// 「迁移」在运营的直觉里常常等于「挪完就没了」,而这里源分组连同它的全部配置
	// 都留着。不说出来的话,下一步他会去找一个已经消失的分组,或者以为迁移失败了。
	SourceRemains bool `json:"source_remains"`
	// Refills 是「迁完之后仍然会有人被放回源分组」的来源清单。
	//
	// 它是这条路径与删除路径最重要的行为差别:删除会把这些引用一并拦下或改写,
	// 迁移一个都不动。列不出来的话,运营会看着人数在几天后自己长回去。
	Refills []string `json:"refills"`
}

// adminMigrateUserGroup 把一个用户分组下的全部在册用户整体迁到另一个分组。
//
// ══════════════ 顺序 ══════════════
//
//  1. 目标必填、必须已登记、不能是自己
//  2. 源分组必须真的有人(0 人时没有东西可迁,那多半是重复提交)
//  3. 一致性闸门:回传的人数与此刻不符 ⇒ 409
//  4. 「目标一个模型分组都用不了」⇒ 必须显式勾选覆盖
//  5. 迁用户(主库一个事务)→ 逐个失效 Redis 用户缓存
//
// 第 5 步之后**没有第 6 步** —— 这正是它与删除的全部差别。
func adminMigrateUserGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	// 与影响面和删除同一道判据:**不要求有登记行**。只在 users.group 里的历史
	// 遗留分组同样要能把人挪走 —— 它们正是最需要被清空的那一类。
	before, registered, err := lookupUserGroup(c, gdb, name)
	if err != nil {
		badRequest(c, "qy_groupns_unknown", err.Error())
		return
	}
	var req migrateUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}

	target := strings.TrimSpace(req.Target)
	if target == "" {
		badRequest(c, "qy_groupns_migration_required",
			"迁移必须给出目标用户分组 —— 目标为空会把这批账号的 users.group 清空,"+
				"而空 group 与 default 在鉴权中间件里并不等价")
		return
	}
	if target == name {
		badRequest(c, "qy_invalid_param", "迁移目标不能是这一档自己")
		return
	}
	var dst UserGroup
	if err := gdb.WithContext(c).Where("name = ?", target).Take(&dst).Error; err != nil {
		badRequest(c, "qy_groupns_unknown_target",
			"迁移目标 "+target+" 不是一个已登记的用户分组 —— "+
				"迁进一个没有登记行的名字等于把这批人挪进一个谁都配不了的档")
		return
	}

	users := countByGroupCtx(c, "users")[name]
	if users == 0 {
		badRequest(c, "qy_groupns_nothing_to_migrate", fmt.Sprintf(
			"用户分组 %q 当前一个在册用户都没有,没有东西可迁。"+
				"(这一档仍然存在,它的配置、以及指向它的套餐与默认注册分组都没有变)", name))
		return
	}
	if req.ExpectUsers >= 0 && req.ExpectUsers != users {
		conflict(c, "qy_groupns_impact_drift", fmt.Sprintf(
			"这一档的在册用户数已经从 %d 变成 %d —— 请重新载入影响面再操作",
			req.ExpectUsers, users))
		return
	}

	diff := BuildMigrationDiff(name, target, service.GetUserUsableGroups)
	if diff.LosesEverything && !req.AckLosesEverything {
		badRequest(c, "qy_groupns_target_unusable", fmt.Sprintf(
			"迁移目标 %q 当前一个模型分组都选不到 —— 这 %d 名用户的全部令牌会在迁移完成的"+
				"那一刻同时 403。确认这是有意的请显式勾选强制覆盖", target, users))
		return
	}

	moved, err := model.QyRewriteUserGroupTx(name, target, model.QyRewriteModeMigrate)
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: auditActionUserGroupMigrate, Result: qymodel.ResultFail,
			Reason: fmt.Sprintf("用户分组迁移 %q → %q 失败,主库事务已回滚 —— "+
				"没有任何用户被改动,源分组的配置完整: %s", name, target, err.Error()),
			Before: before,
		})
		internalError(c, err)
		return
	}

	out := UserGroupMigrateResult{
		From: name, To: target,
		Users: moved.Users, Subscriptions: moved.Subscriptions,
		Stragglers: moved.Stragglers,
		// 缓存失效必须紧跟提交:Redis 里的 user:<id> 缓存着旧分组(TTL 默认 60s,
		// 而 GetUserGroup 优先读缓存),不失效就等于让这批人在这一分钟里继续按
		// 旧分组解析可用清单与倍率 —— 而运营刚刚在弹窗里看过那两者会怎么变。
		CacheMisses:   invalidateUserCaches(moved.UserIds),
		Diff:          diff,
		SourceRemains: true,
		Refills:       userGroupRefillSources(gdb, name),
	}

	reason := fmt.Sprintf(
		"用户分组迁移:把 %q 的 %d 名在册用户挪到 %q(同时改写这批人自己的 %d 条订阅快照)。"+
			"**这不是删除** —— %q 仍然存在,它的登记行、可用范围、授权、交叉倍率与充值倍率"+
			"一个字节都没有动,指向它的套餐引用也没有动",
		name, out.Users, target, out.Subscriptions, name)
	if !registered {
		reason += ";(**该分组此前只存在于 users.group,没有登记行**,因此 before 快照只有名字)"
	}
	reason += ";" + describeDiff(diff)
	if req.AckLosesEverything && diff.LosesEverything {
		reason += ";**强制覆盖「目标分组一个模型分组都用不了」闸门**"
	}
	if out.Stragglers > 0 {
		reason += fmt.Sprintf(";**仍有 %d 名用户挂在源分组上**(迁移期间被并发写入)。"+
			"源分组配置完整,他们不是孤儿;再执行一次迁移即可收敛", out.Stragglers)
	}
	if out.CacheMisses > 0 {
		reason += fmt.Sprintf(";%d 个用户的缓存失效失败,他们在 TTL 内仍按旧分组计价", out.CacheMisses)
	}
	if len(out.Refills) > 0 {
		reason += ";**迁完之后仍会有人被放回源分组**:" + strings.Join(out.Refills, "、")
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: auditActionUserGroupMigrate, Result: qymodel.ResultOK,
		Reason: reason, Before: before, After: out,
	})
	respond(c, out)
}

// userGroupRefillSources 列出「迁完之后仍然会往这一档里放人」的来源。
//
// 它不是一道闸门,是一句必须说出口的话。删除路径把这两类引用分别硬拦(套餐)
// 与改写(新注册默认分组),而纯迁移一个都不动 —— 于是迁完之后人数会自己长回去,
// 而界面上没有任何一处解释过为什么。
//
// 探测失败不阻断迁移(迁移已经提交,而这只是一段说明),只降级成一条日志。
func userGroupRefillSources(gdb *gorm.DB, name string) []string {
	out := make([]string, 0, 4)
	plans, err := model.QyPlansReferencingUserGroup(name)
	if err != nil {
		common.SysError("qianye/groupns: 迁移后统计套餐引用失败: " + err.Error())
	}
	for _, plan := range plans {
		out = append(out, "套餐 "+plan+" 的升级/降级分组仍然指向它")
	}
	residues, err := ProbeResidues(gdb, name)
	if err != nil {
		common.SysError("qianye/groupns: 迁移后探测残留失败: " + err.Error())
		return out
	}
	for _, row := range residues {
		// 只有 rewrite 这一档会往分组里"放人"(最典型的是新注册用户的默认分组)。
		// clean / keep / block 三档是配置与历史值,它们不产生新用户。
		if row.Disposition == ResidueRewrite && row.Rows > 0 {
			out = append(out, fmt.Sprintf("%s(%s,%d 行)仍然指向它", row.Label, row.Table, row.Rows))
		}
	}
	return out
}
