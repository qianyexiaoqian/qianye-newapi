package groupmatrix

// newgroup.go —— 「新建的用户分组默认全遮断」。
//
// ═════════════════════ 「新分组」这件事发生在哪里 ═════════════════════
//
// 用户分组没有自己的表。它的事实清单就是 **options.GroupRatio 的键集合**:
// controller/group.go 的 GetGroups 直接把 ratio_setting.GetGroupRatioCopy()
// 的 key 列出来当分组下拉,middleware/auth.go 用 ContainsGroupRatio 判定分组
// 是否"已被弃用",本模块的 listUserGroups / listModelGroups 也都取自同一张 map。
// 所以「新增一个用户分组」= 在上游「系统设置-分组倍率」表单里加一个 key。
//
// 那次写入走的是通用的 model.UpdateOption("GroupRatio", ...) —— 一个不带任何
// 分组语义的 KV 写入,没有钩子、没有事件、没有 diff。要在上游挂一个"分组被创建"
// 的 hook,就得动 setting/ratio_setting 或 model/option.go,而那两处都在
// 上游改动预算之外。
//
// 因此本模块用**对账**而不是事件:后台任务周期性地把 GroupRatio 的键集合与扩展库
// 里的登记簿(Seen)做差集,差出来的就是新分组。代价只有一个 —— 最长一个周期的
// 发现延迟,而且延迟的方向是安全的:窗口期内新分组按上游宽松白名单放行,
// 与今天的行为一致,不会先拒后放。
//
// ═════════════════════ 三道必须存在的安全闸门 ═════════════════════
//
// 这段代码的失败方式是「把一批本来好好的用户分组一次性遮断」,而遮断的表现是
// 那一档的人在令牌页一个模型分组都选不了。三道闸门各挡一种触发方式:
//
//  1. **倍率表为空 → 整轮跳过。** GroupRatio 在 options 加载前只有上游内置的
//     三个默认键;真读到空 map 说明状态不可信。此时若把空集当基线登记下来,
//     下一轮那 7 个真实分组就全部变成"新分组"。
//
//  2. **首轮 = 基线,一个都不遮断。** 登记簿为空时把当时的全部分组登记成
//     Baseline 并直接返回。这条同时兜住了"有人清空了登记簿"这种意外 ——
//     重新基线是宽松方向。
//
//  3. **一轮最多自动遮断 maxAutoMaskPerRound 个。** 一次冒出一批新分组不是
//     "运营加了一个分组",而是数据异常(倍率表被整体替换、登记簿被截断)。
//     超过就全部只登记不遮断并打 SysError,自愈且不留地雷。
//
// 外加一条与"新"字面语义直接相关的判据:**已经有用户在用的分组不算新分组。**
// 它是既存分组被补登进倍率表的那种情形,遮断它会当场打断一批在线用户。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"gorm.io/gorm"
)

const auditActionNewGroupMask = "groupmatrix.new_group.auto_mask"

// maxAutoMaskPerRound 是单轮自动遮断的上限。
//
// 3 不是拍脑袋:运营在分组倍率表单里一次加两三个分组是正常操作,一次加十个
// 不是。超过上限的那一轮**全部只登记不遮断** —— 因为这种规模的差集只可能来自
// 倍率表被整体替换或登记簿被截断,而在那两种情况下遮断谁都是错的。
const maxAutoMaskPerRound = 3

var (
	// autoMaskedTotal 是本进程累计自动遮断的分组数,进 /admin/health。
	// 自动收紧如果没有计数器,运营只能靠"用户来报障"发现它生效过。
	autoMaskedTotal atomic.Int64
	// lastScanAt 是最近一次对账完成的时间戳。0 = 从未跑过。
	// 它回答的是"这个默认到底有没有在工作",而不是"有没有遮断过谁"。
	lastScanAt atomic.Int64
)

// userCountOf 返回某个用户分组当前的在册人数。第二个返回值为 false 表示
// **查不到**(主库未就绪或查询失败),与"人数为 0"必须分开。
//
// 分开的理由就是本文件的立论:人数为 0 是"这确实是个新分组,可以遮断",
// 查不到是"不知道",而不知道的时候遮断一个分组可能当场打断一批在线用户。
// 查不到时整轮跳过,下一轮再来 —— 主库不可用是短暂的,遮断错了是持久的。
//
// 抽成变量是为了让对账逻辑可测:单测里主库(model.DB)恒为 nil,
// 不给这个接缝的话每一个用例都会停在"查不到 → 跳过"上,全绿而实现体可以写反。
var userCountOf = func(userGroup string) (int64, bool) {
	if model.DB == nil {
		return 0, false
	}
	var n int64
	// group 是三种数据库里的保留字,列名必须走 model.QyCommonGroupCol()。
	err := model.DB.Model(&model.User{}).
		Where(model.QyCommonGroupCol()+" = ?", userGroup).Count(&n).Error
	if err != nil {
		common.SysError("qianye/groupmatrix: 统计用户分组人数失败(本轮新分组对账跳过): " + err.Error())
		return 0, false
	}
	return n, true
}

// newGroupScanInterval 是对账周期(秒)。
func newGroupScanInterval() int {
	n := cfg().NewGroupScanIntervalSeconds
	if n <= 0 {
		n = 60
	}
	return n
}

// runNewGroupScan 是 lease.Run 的入口。
//
// 整轮吞掉错误只记日志:这是一个后台默认值维护任务,失败一轮的代价是新分组
// 晚一个周期被遮断(宽松方向),而让它把错误抛给 lease 只会换来一条同样的日志。
func runNewGroupScan(ctx context.Context) {
	if err := reconcileNewGroups(ctx); err != nil {
		common.SysError("qianye/groupmatrix: 新分组对账失败(下一周期重试): " + err.Error())
	}
}

// reconcileNewGroups 把 options.GroupRatio 的键集合与登记簿对账一次。
//
// 返回 error 只表示"这一轮没跑完",调用方重试即可;它**从不**表示数据被改坏了 ——
// 每个分组的处置各自一个事务,失败的那个下一轮重来,成功的不回滚。
func reconcileNewGroups(ctx context.Context) error {
	if !enabled() {
		// 模块整体关掉时连登记簿都不维护:那是 L1 kill switch 的语义,
		// 关掉之后扩展在这条路径上不做任何事。
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	// ── 闸门 1:倍率表为空 → 整轮跳过 ────────────────────────────────
	//
	// 空 map 只可能是"上游 options 还没加载"或"倍率表真的被清空了",两者都
	// 不足以支撑一次基线登记 —— 而错误的基线会在下一轮把全部真实分组判成新的。
	ratios := ratio_setting.GetGroupRatioCopy()
	names := make([]string, 0, len(ratios))
	for name := range ratios {
		if name == "" || name == autoGroup {
			// auto 是伪分组,它永远不是一个用户分组(adminPutScope 也显式拒绝它)。
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		common.SysError("qianye/groupmatrix: 分组倍率表为空,本轮新分组对账跳过 —— " +
			"把空集当基线登记下来,会让下一轮把站内全部真实分组判成「新分组」并一次遮断")
		return nil
	}
	sort.Strings(names)

	var seenRows []Seen
	if err := gdb.Find(&seenRows).Error; err != nil {
		return err
	}
	// 登记簿的键按**大小写折叠**比对,尽管分组名本身是精确匹配的。
	//
	// 这不是口径松动,是让 Go 侧与存储侧说同一句话:扩展库固定是 MySQL
	// (qianye/db/db.go 只 Open 了 mysql 驱动),qy_group_seen.user_group 是
	// varchar 主键、走库的默认 utf8mb4_general_ci —— 在库看来 "VIP" 与 "vip"
	// 就是同一行。Go 侧若按精确匹配算差集,站里已有 vip 时新建的 VIP 会:
	// 每轮都被判成 fresh → 回查 Take 命中 vip 那一行 → 当成"已登记"直接跳过,
	// 于是它**永远**写不进登记簿,每 60 秒重来一次;走到遮断分支时则是
	// Create 撞主键 → 事务回滚 → 每 60 秒一条 SysError,而 VIP 永远不会被遮断。
	//
	// 代价写在明处:与已登记名只差大小写的新分组不会被自动遮断,而是登记成
	// Declined 并在矩阵页上说出来,由运营手动接管。让 Go 比库更严格换不来更多
	// 安全,只换来一个每分钟报一次错、且永远不会自愈的循环。
	seen := make(map[string]Seen, len(seenRows))
	for _, row := range seenRows {
		seen[foldGroupKey(row.UserGroup)] = row
	}

	// ── 闸门 2:首轮 = 基线,一个都不遮断 ────────────────────────────
	//
	// 项目方明确:「只对新建的用户分组生效,既存的 7 个用户分组保持原样。」
	// 那句话在代码里就是这一段。它同时兜住"登记簿被清空"这种意外 ——
	// 重新基线是宽松方向,不会有人因此被挡在门外。
	if len(seenRows) == 0 {
		if err := recordSeen(gdb, names, false,
			"首轮基线登记:扩展首次对账时它已存在,不属于新分组"); err != nil {
			return err
		}
		lastScanAt.Store(common.GetTimestamp())
		common.SysLog(fmt.Sprintf(
			"qianye/groupmatrix: 新分组登记簿首次建立,已把当前 %d 个用户分组登记为基线并**一个都不遮断** —— "+
				"此后只有新出现的分组才会被自动全遮断", len(names)))
		return nil
	}

	fresh := make([]string, 0)
	for _, name := range names {
		row, ok := seen[foldGroupKey(name)]
		if !ok {
			fresh = append(fresh, name)
			continue
		}
		if row.UserGroup != name {
			// 折叠之后撞上了另一个只差大小写的已登记名。它进不了登记簿(主键会撞),
			// 因此这条日志是后端侧唯一的痕迹。界面侧不靠它:matrixWarnings 已经
			// 无条件把"仅大小写不同的分组名"列成 warning,那条提示与这里同源。
			common.SysLog(fmt.Sprintf(
				"qianye/groupmatrix: 用户分组 %q 与登记簿里的 %q 仅大小写不同,扩展库(MySQL,不区分大小写)"+
					"把它们视为同一行,因此**不会**对 %q 自动遮断 —— 需要遮断请在管理端矩阵页手动接管",
				name, row.UserGroup, name))
		}
	}
	lastScanAt.Store(common.GetTimestamp())
	if len(fresh) == 0 {
		return nil
	}

	// ── 开关关着:只登记,不遮断 ──────────────────────────────────
	//
	// 刻意仍然登记。不登记的话,开关关闭期间新建的分组会攒成一批"从未见过"的
	// 名字,运营某天把开关打开,它们会在同一轮里被一起遮断 —— 而那批分组早已
	// 有人在用。开关的语义必须是"从现在起",不是"把这段时间补上"。
	if !cfg().NewGroupDefaultDenyOn() {
		// declined=false:开关关着的时候"没有遮断"正是运营看到的那句话
		// (矩阵页常驻提示写着「新分组默认全遮断:已关闭」),不构成预期落差。
		return recordSeen(gdb, fresh, false,
			"发现时 group_matrix.new_group_default_deny 是关闭的,只登记不遮断")
	}

	// ── 闸门 3:一轮冒出太多 → 全部只登记不遮断 ──────────────────────
	if len(fresh) > maxAutoMaskPerRound {
		common.SysError(fmt.Sprintf(
			"qianye/groupmatrix: 一轮对账冒出 %d 个从未见过的用户分组(上限 %d),已全部只登记、不遮断:%v —— "+
				"这个规模的差集不像是有人加了一个分组,更像是分组倍率表被整体替换或登记簿被截断。"+
				"如果这批分组确实是新的且需要遮断,请在管理端矩阵页逐个接管",
			len(fresh), maxAutoMaskPerRound, fresh))
		// declined=true:开关是开着的、这些确实是新分组,而它们没有被遮断。
		// 登记簿一旦写下永不重判,所以这批分组**永远**不会被补遮断,
		// 而矩阵页顶部仍然写着「新分组默认全遮断:已开启」。这个落差必须上界面。
		return recordSeen(gdb, fresh, true, fmt.Sprintf(
			"一轮对账同时出现 %d 个新分组(超过上限 %d),按数据异常处理:只登记不遮断",
			len(fresh), maxAutoMaskPerRound))
	}

	masked := 0
	for _, name := range fresh {
		did, err := handleFreshGroup(gdb, name)
		if err != nil {
			// 单个分组失败不影响其余:它下一轮会重来(登记簿里还没有它)。
			common.SysError(fmt.Sprintf(
				"qianye/groupmatrix: 处置新用户分组 %q 失败(下一周期重试): %v", name, err))
			continue
		}
		if did {
			masked++
		}
	}
	if masked > 0 {
		autoMaskedTotal.Add(int64(masked))
		// 本节点立刻用上新 scope 行,其余节点靠周期刷新感知。
		if err := InvalidateAndReload(); err != nil {
			common.SysError("qianye/groupmatrix: 自动遮断后刷新快照失败(其它节点仍会按周期刷新): " + err.Error())
		}
	}
	return nil
}

// handleFreshGroup 处置一个从未登记过的用户分组,返回是否真的遮断了它。
//
// 幂等由两件事共同保证,缺一不可:
//
//	登记簿行  —— 主键是分组名,写下就永不删除。它才是"这个名字已经处置过"的
//	             唯一判据,因此运营撤销接管(删 scope 行)之后不会被再次遮断。
//	scope 行  —— 已经存在就绝不覆盖:运营可能已经手动接管并配好了清单,
//	             用一条零 grant 的 enforce 行盖上去等于一次静默的全量撤销。
func handleFreshGroup(gdb *gorm.DB, userGroup string) (bool, error) {
	var existing Scope
	err := gdb.Where("user_group = ?", userGroup).Take(&existing).Error
	switch {
	case err == nil:
		// 已被手动接管。只补登记簿,一个字节都不改它。
		// declined=false:运营自己配过这一行,他看到的状态就是他配的状态,
		// 没有"以为被遮断了、其实没有"的落差。
		return false, recordSeen(gdb, []string{userGroup}, false,
			"发现时该分组已被手动接管,自动遮断不介入")
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return false, err
	}

	// 「已经有用户在用的分组不算新分组」。
	//
	// 这是既存分组被**补登进倍率表**的那种情形:分组早就在 users.group 上跑着,
	// 只是倍率表里一直没有它(本站的孤儿分组正是这个形状)。遮断它会当场打断
	// 一批在线用户,而运营那次操作的本意恰恰是"把它配上倍率、让它正常工作"。
	users, ok := userCountOf(userGroup)
	if !ok {
		// 查不到人数就什么都不做,连登记簿也不写:下一轮主库好了再判。
		// 写了登记簿等于用一次"不知道"永久放弃对这个分组的处置。
		return false, nil
	}
	if users > 0 {
		// 这一条是本文件里最容易被误读的分支,所以它必须同时留下三样东西:
		// 后端日志、登记簿的 Declined 标记、以及矩阵页上的一条待办。
		//
		// 触发它的是最自然的运营顺序:在分组倍率表单里加一个分组 → 立刻去用户
		// 管理把几个人划进去。60 秒后对账跑到时人数已经 > 0,于是判成「既存分组
		// 补登倍率」只登记不遮断,而登记簿永不重判 —— 这个分组**永远**不会被
		// 遮断。运营这一刻在矩阵页上看到的却是「新分组默认全遮断:已开启」,
		// 他会以为清单是空的、可以慢慢配,而那一档的用户当场就能选任意模型分组。
		// 这正是这个默认要挡的那件事。
		common.SysLog(fmt.Sprintf(
			"qianye/groupmatrix: 新用户分组 %q 发现时已有 %d 个用户,判定为「既存分组补登倍率」,"+
				"**未**自动遮断(遮断会当场打断这批在线用户)。登记簿永不重判,因此它此后也不会被补遮断 —— "+
				"如果它确实需要遮断,请在管理端矩阵页手动接管", userGroup, users))
		return false, recordSeen(gdb, []string{userGroup}, true, fmt.Sprintf(
			"发现时已有 %d 个用户属于该分组,判定为「既存分组补登倍率」而非新建,只登记不遮断", users))
	}

	now := common.GetTimestamp()
	scope := newScope(userGroup, ModeEnforce, false,
		"由「新分组默认全遮断」自动接管:清单为空,请在此为它添加可用的模型分组", 0, now)
	reason := "新建的用户分组,按 group_matrix.new_group_default_deny 自动全遮断"

	err = gdb.Transaction(func(tx *gorm.DB) error {
		// 登记簿先写:它是幂等键。反过来的话,scope 建成功而登记簿写失败时,
		// 下一轮会再次进入本函数 —— 那一次会撞上"已被手动接管"分支,
		// 把一次自动遮断记成"运营手动配的",审计从此对不上。
		if err := tx.Create(&Seen{
			UserGroup: userGroup, FirstSeenAt: now,
			Baseline: false, AutoMasked: true, Reason: truncate(reason, maxNoteLen),
		}).Error; err != nil {
			return err
		}
		return tx.Create(scope).Error
	})
	if err != nil {
		return false, err
	}

	// 自动收紧同样是一次"决定谁能发出请求"的写入,必须留痕。
	// ActorSystem + 空 gin.Context:这条不是任何管理员按出来的,
	// 记成某个人的操作比不记更糟 —— 事后复盘会去问一个根本没动过手的人。
	audit.Write(nil, audit.Entry{
		Category: auditCategoryGroupMatrix, Action: auditActionNewGroupMask,
		ActorType: qymodel.ActorSystem,
		Reason: fmt.Sprintf("检测到新用户分组 %q(在册 0 人),自动接管为 mode=enforce、零条可选模型分组 —— "+
			"该分组的用户在配好可选模型分组之前无法把令牌指向任何模型分组。"+
			"关掉 group_matrix.new_group_default_deny 可停止这个默认", userGroup),
		BeforeSnap: "",
		AfterSnap:  snapshotJSON(scope),
	})
	common.SysLog(fmt.Sprintf(
		"qianye/groupmatrix: 新用户分组 %q 已按默认全遮断(mode=enforce,可选模型分组 0 个)—— "+
			"请在管理端矩阵页为它添加可用的模型分组", userGroup))
	return true, nil
}

// foldGroupKey 把分组名折成登记簿主键在扩展库里的实际比较口径。
//
// 扩展库固定是 MySQL(qianye/db/db.go 只 Open 了 mysql 驱动),qy_group_seen 的
// varchar 主键走库的默认 utf8mb4_general_ci,"VIP" 与 "vip" 在它看来是同一行。
// 差集若按精确匹配算,Go 与库的口径就相反,而相反的后果不是漏判一次,
// 是一个每 60 秒重来一次、永远不会自愈的循环(详见 reconcileNewGroups)。
//
// **只用于登记簿的键**。分组名本身仍然精确匹配 —— Scope / Grant / 倍率侧
// 都不折叠,理由见 Scope.UserGroup 的注释。
func foldGroupKey(name string) string { return strings.ToLower(name) }

// recordSeen 幂等地把一批分组名写进登记簿。
//
// 先查再建而不是 upsert:登记簿的 Reason 记的是**第一次**见到它时的判断依据,
// 被后来的一次覆盖掉,"当初为什么没遮断它"这个问题就再也答不上来了。
//
// declined 见 Seen.Declined:开关开着、名字确实是新的、而扩展刻意没遮断它。
func recordSeen(gdb *gorm.DB, userGroups []string, declined bool, reason string) error {
	now := common.GetTimestamp()
	for _, name := range userGroups {
		var existing Seen
		err := gdb.Where("user_group = ?", name).Take(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		row := &Seen{
			UserGroup: name, FirstSeenAt: now,
			Baseline: true, AutoMasked: false, Declined: declined,
			Reason: truncate(reason, maxNoteLen),
		}
		if err := gdb.Create(row).Error; err != nil {
			// 并发下另一个节点抢先登记(lease 之外还有管理端触发路径):
			// 目标状态已经达成,不是错误。
			var probe Seen
			if gdb.Where("user_group = ?", name).Take(&probe).Error == nil {
				continue
			}
			return err
		}
	}
	return nil
}

// loadSeen 读出全部登记簿行,供管理端矩阵回显「新分组·待配置」。
//
// 读失败只记日志、返回空 map:这是展示数据,不能让它挡住整个矩阵页 ——
// 而矩阵页打不开的时刻,恰恰是最需要看它的时刻(与 listWriteDenies 同一个判断)。
func loadSeen(gdb *gorm.DB) map[string]Seen {
	out := map[string]Seen{}
	if gdb == nil {
		return out
	}
	var rows []Seen
	if err := gdb.Find(&rows).Error; err != nil {
		common.SysError("qianye/groupmatrix: 读取新分组登记簿失败(矩阵页不再标注「新分组·待配置」): " + err.Error())
		return out
	}
	for _, row := range rows {
		out[row.UserGroup] = row
	}
	return out
}
