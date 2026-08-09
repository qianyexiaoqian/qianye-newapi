package groupns

// usergroup_admin.go —— 用户分组的新建 / 编辑 / 改名 / 删除并迁移。
//
// ══════════════════ 项目方原话 ══════════════════
//
//	「用户分组当前无法删除/无法添加。用户分组删除时,若存在用户,弹出选择窗口,
//	 这批用户要迁移到哪个用户分组?」
//
// ══════════════════ 这四个接口里唯一危险的是删除 ══════════════════
//
// 新建只写一行登记,改名有完整的三阶段补偿(见 usergroup_store.go)。删除不同:
// 它同时是一次**批量改价**与一次**批量权限变更**,而这两件事在「删掉一个分组」
// 这句话里一个字都没提到。所以删除路径上有四道东西是必须的:
//
//	影响面接口   删之前必须能看到人数、令牌数、以及迁移前后的可用清单与倍率差
//	强制迁移目标 有用户时不许"直接删" —— 那会造出孤儿 users.group,
//	             而 GetGroupRatio 对孤儿是 fail-open 返回 1.0,整组人静默按原价扣费
//	一致性闸门   请求体回传运营看到的用户数,对不上返回 409
//	硬闸门       套餐引用、以及任何模块声明为 block 的残留,存在即拒绝

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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

const (
	auditActionUserGroupCreate = "groupns.user_group.create"
	auditActionUserGroupUpdate = "groupns.user_group.update"
	auditActionUserGroupRename = "groupns.user_group.rename"
	auditActionUserGroupDelete = "groupns.user_group.delete"
)

// maxNoteLen 对齐 UserGroup.Note 的 varchar(255)。
const maxNoteLen = 255

// ratioText 把倍率渲染成十进制字符串。
//
// 'f' + 精度 -1 给出能原样往返的最短表示:0.1 就是 "0.1",不会变成
// 0.10000000000000001。影响面弹窗里每一个数字都会被运营当成报价读。
func ratioText(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// ─────────────────────── GET /user-groups/:name/impact ───────────────────────

// UserGroupImpact 是删除确认弹窗的**全部**内容。
//
// 它必须一次性给全:分两次请求拿的话,运营会在两份不同时刻的快照之间做决定,
// 而人数正是这段时间里最可能变的东西。
type UserGroupImpact struct {
	Name string `json:"name"`
	// Users / Tokens 是项目方点名要的那两个数:「该用户分组下有多少用户、多少令牌」。
	Users  int64 `json:"users"`
	Tokens int64 `json:"tokens"`
	// EmptyGroupTokens 是其中**没有指定令牌分组**的那些 —— 它们的模型分组由
	// users.group(或本分组的默认模型分组)解析,是迁移后行为变化最直接的一批。
	EmptyGroupTokens int64 `json:"empty_group_tokens"`
	// Subscriptions 是快照里指向它的已售订阅行数。它们跟着用户一起改写,不拦。
	Subscriptions int64 `json:"subscriptions"`

	// BlockingPlans 是 upgrade_group / downgrade_group 指向它的**套餐**。
	// 非空即拒绝删除:一个卖着「升级到 X」的套餐,在 X 被删之后会把下一个买家
	// 放进一个不存在的分组 —— 他刚刚为"升级"付过钱,而系统按 fail-open 的 1.0 给他计费。
	// 正确的新值只有运营知道,所以这里只拦不改。
	BlockingPlans []string `json:"blocking_plans"`

	// Residues 是各模块声明的残留(范围、授权、费率、划转规则、新用户默认分组…),
	// 每一条都带自己的处置(clean / rewrite / keep / block)。
	Residues []Residue `json:"residues"`
	// Blocking 是 Residues 里处置为 block 且真的有行的那些。非空即拒绝删除。
	Blocking []Residue `json:"blocking"`

	// Targets 是可选的迁移目标(除自己之外的全部已登记用户分组)。
	Targets []MigrationTarget `json:"targets"`
	// Diff 只在请求带了 target 时非空。
	Diff *MigrationDiff `json:"diff,omitempty"`

	// Deletable 汇总"现在能不能删"。它是服务端的结论,前端不自己推 ——
	// 两边各推一遍必然漂移,而漂移的方向是"按钮亮着、点下去 400"。
	Deletable bool `json:"deletable"`
	// BlockReason 在 Deletable 为 false 时说明原因。
	BlockReason string `json:"block_reason,omitempty"`
	// BlockCode 是 BlockReason 的**机器可读版本**,取值见 UserGroupBlock* 常量。
	//
	// 为什么不让前端去 match 那段中文:BlockReason 回答的是「为什么不能删」,
	// 而运营站在弹窗前真正要问的下一个问题是「那我该干什么」。后者是一条与
	// 界面强相关的指路(去哪一页、改哪个字段),写死在后端正文里就没法随界面
	// 改版一起改;而按中文子串去猜分支,任何一次文案润色都会静默让指路消失。
	BlockCode string `json:"block_code,omitempty"`

	// Renamable / RenameBlock* 与上面三个同形,回答的是**改名**能不能进行。
	//
	// 改名与删除共用一道 block 残留闸门(见 adminRenameUserGroup 的注释:
	// 对一份冻结的引用来说"改个名"与"删掉"完全等价),所以两者必须在同一份
	// 影响面里一起给出 —— 否则运营会发现"删不掉就改个名",而那是同一次事故
	// 换了个入口,并且他要等到提交之后才知道这条路也走不通。
	Renamable         bool   `json:"renamable"`
	RenameBlockReason string `json:"rename_block_reason,omitempty"`
	RenameBlockCode   string `json:"rename_block_code,omitempty"`
}

// 拒绝删除 / 改名的分支代码。
//
// 它们是**契约的一部分**:前端按这几个值给出「下一步该做什么」的指路文案
// (见 web/src/features/qy/pages/admin-user-groups/roster/lib/gates.ts)。
// 新增一条拒绝分支时必须同时新增一个代码并在前端登记,否则那一条分支在界面上
// 只剩一句"为什么不行"、没有"那我该干什么" —— 而后者才是运营下一步要做的事。
const (
	// UserGroupBlockDefault:default 是 users.group 的数据库默认值,永不可删、永不可改名。
	UserGroupBlockDefault = "upstream_default"
	// UserGroupBlockPlans:套餐的升级/降级分组指向它。
	UserGroupBlockPlans = "blocking_plans"
	// UserGroupBlockResidues:某个模块声明了处置为 block 的残留。
	UserGroupBlockResidues = "blocking_residues"
	// UserGroupBlockNoTarget:这一档还有人,但站上没有第二个已登记分组可迁。
	UserGroupBlockNoTarget = "no_migration_target"
)

// MigrationTarget 是迁移目标下拉的一项。
type MigrationTarget struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Users       int64  `json:"users"`
	// UsableGroups 是这一档人能选的模型分组数。它必须出现在下拉里:
	// 迁进一个 usable=0 的分组等于让这批人的全部令牌当场停摆。
	UsableGroups int  `json:"usable_groups"`
	Enabled      bool `json:"enabled"`
}

func adminUserGroupImpact(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		badRequest(c, "qy_invalid_param", "缺少用户分组名")
		return
	}
	// **不要求有登记行。** 管理端那张表的清单是「观测值 ∪ 登记表」,一个只在
	// users.group 里的名字同样要能看影响面 —— 而它恰恰是最需要看的那一类
	// (历史遗留、没人记得它是干什么的)。拿"还没有登记"把它挡在门外,
	// 表现是这一档人永远删不掉,而运营看到的理由与他要做的事毫无关系。
	if _, _, err := lookupUserGroup(c, gdb, name); err != nil {
		badRequest(c, "qy_groupns_unknown", err.Error())
		return
	}

	impact, err := buildUserGroupImpact(c, gdb, name)
	if err != nil {
		internalError(c, err)
		return
	}
	if target := strings.TrimSpace(c.Query("target")); target != "" {
		diff := BuildMigrationDiff(name, target, service.GetUserUsableGroups)
		impact.Diff = &diff
	}
	respond(c, impact)
}

// buildUserGroupImpact 汇总一个用户分组此刻的全部影响面。
//
// 任何一处探测失败都整体返回错误:一份缺了一块的影响面清单与完整的清单在界面上
// 长得一模一样,而运营会照着它按下删除。
func buildUserGroupImpact(ctx context.Context, gdb *gorm.DB, name string) (*UserGroupImpact, error) {
	out := &UserGroupImpact{Name: name}

	out.Users = countByGroupCtx(ctx, "users")[name]
	tokens, err := model.QyUserGroupTokenCount(name)
	if err != nil {
		return nil, err
	}
	out.Tokens = tokens
	// enabledOnly=false:这个数字在弹窗里是作为 out.Tokens 的**子集**呈现的
	// (「N 个,其中空分组令牌 M 个」),而 QyUserGroupTokenCount 启用与停用
	// 都算。两个口径不同的表现是 M 不是 N 的子集,运营据此以为只有 M 个令牌
	// 的路由会变 —— 而停用的空分组令牌一旦被重新启用,同样按新分组解析。
	if counts, err := EmptyGroupTokenCounts(ctx, model.DB, false); err == nil {
		out.EmptyGroupTokens = counts[name]
	}
	subs, err := model.QyUserSubscriptionsReferencingUserGroup(name)
	if err != nil {
		return nil, err
	}
	out.Subscriptions = subs

	plans, err := model.QyPlansReferencingUserGroup(name)
	if err != nil {
		return nil, err
	}
	out.BlockingPlans = plans

	residues, err := ProbeResidues(gdb, name)
	if err != nil {
		return nil, err
	}
	out.Residues = residues
	out.Blocking = BlockingResidues(residues)

	targets, err := listMigrationTargets(ctx, gdb, name)
	if err != nil {
		return nil, err
	}
	out.Targets = targets

	out.Deletable, out.BlockCode, out.BlockReason = deletability(out)
	out.Renamable, out.RenameBlockCode, out.RenameBlockReason = renamability(name, out.Residues)
	return out, nil
}

// deletability 汇总"现在能不能删"、属于哪一条拒绝分支、以及为什么不能。
func deletability(impact *UserGroupImpact) (bool, string, string) {
	if normalizeGroupKey(impact.Name) == upstreamDefaultUserGroup {
		return false, UserGroupBlockDefault,
			"default 是 users.group 这一列的数据库默认值 —— 删掉它之后," +
				"任何一次没有显式指定分组的建号都会落进一个不存在的分组。这一档不可删除"
	}
	if len(impact.BlockingPlans) > 0 {
		return false, UserGroupBlockPlans,
			"以下套餐的升级/降级分组指向它,删掉之后下一个买家会被放进一个" +
				"不存在的分组并按 fail-open 的 1.0 倍计费:" + strings.Join(impact.BlockingPlans, "、") +
				"。请先在套餐里改掉这两处引用"
	}
	if len(impact.Blocking) > 0 {
		return false, UserGroupBlockResidues,
			"以下配置指向它且必须由人来决定新值:" + describeBlockingResidues(impact.Blocking)
	}
	if len(impact.Targets) == 0 && impact.Users > 0 {
		return false, UserGroupBlockNoTarget,
			"这一档还有用户,但站上没有第二个已登记的用户分组可以迁 —— " +
				"请先新建一个目标分组"
	}
	return true, "", ""
}

// renamability 汇总"现在能不能改名"、属于哪一条拒绝分支、以及为什么不能。
//
// 它同时被影响面接口与改名端点调用,这是刻意的:两处各写一遍必然漂移,而漂移的
// 方向是"界面说能改、提交下去 400"。前者让运营在**打开弹窗时**就看见这条路走
// 不通,后者是最终把关。
//
// 与 deletability 的差别只有两条:改名不需要迁移目标(没有人会变成孤儿),
// 也不受套餐引用影响(rewriteUserGroup 会把套餐的升降级分组一起改掉)。
// 剩下的两条 —— default 与 block 残留 —— 逐字相同。
func renamability(name string, residues []Residue) (bool, string, string) {
	if normalizeGroupKey(name) == upstreamDefaultUserGroup {
		return false, UserGroupBlockDefault,
			"default 是 users.group 这一列的数据库默认值 —— 改掉它之后,任何一次没有显式指定" +
				"分组的建号都会落进一个不存在的分组。这一档不可改名"
	}
	if blocking := BlockingResidues(residues); len(blocking) > 0 {
		return false, UserGroupBlockResidues,
			"以下配置指向它且改不动(它们是冻结值),必须由人来先处理:" +
				describeBlockingResidues(blocking)
	}
	return true, "", ""
}

// describeBlockingResidues 把 block 残留列成"名称(表名,N 行)"。
//
// 表名必须在里面:运营要能拿着它去核对,而"某处配置"这种说法换不来任何行动。
func describeBlockingResidues(blocking []Residue) string {
	labels := make([]string, 0, len(blocking))
	for _, row := range blocking {
		labels = append(labels, fmt.Sprintf("%s(%s,%d 行)", row.Label, row.Table, row.Rows))
	}
	return strings.Join(labels, "、")
}

// upstreamDefaultUserGroup 是 model.User.Group 上 gorm:"default:'default'" 兜底出来的值。
const upstreamDefaultUserGroup = "default"

func listMigrationTargets(ctx context.Context, gdb *gorm.DB, exclude string) ([]MigrationTarget, error) {
	var rows []UserGroup
	if err := gdb.WithContext(ctx).Order("sort_order asc, name asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	counts := countByGroupCtx(ctx, "users")
	out := make([]MigrationTarget, 0, len(rows))
	for _, row := range rows {
		if row.Name == exclude {
			continue
		}
		out = append(out, MigrationTarget{
			Name: row.Name, DisplayName: row.DisplayName,
			Users:        counts[row.Name],
			UsableGroups: len(service.GetUserUsableGroups(row.Name)),
			Enabled:      row.Enabled,
		})
	}
	return out, nil
}

// ─────────────────────────── POST /user-groups ───────────────────────────

type createUserGroupRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Note        string `json:"note"`
	SortOrder   int    `json:"sort_order"`
}

// CreateUserGroupResult 是新建的返回值。
type CreateUserGroupResult struct {
	UserGroup
	// Warnings 不是错误,是"建好了,但它现在还不能用"。
	//
	// 一个刚建出来的用户分组天然没有任何渠道:它的**空分组令牌**会走
	// UsingGroup = users.group,而 abilities 里没有这个键 ⇒ 503。这件事必须在
	// 新建成功的那一刻就说出来,而不是等运营把人挪进去之后自己去撞。
	Warnings []string `json:"warnings"`
}

func adminCreateUserGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var req createUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := validateNewUserGroupName(gdb.WithContext(c), name); err != nil {
		badRequest(c, "qy_groupns_invalid_name", err.Error())
		return
	}

	row := newUserGroup(name, c.GetInt("id"), now())
	row.DisplayName = truncateRunes(strings.TrimSpace(req.DisplayName), maxGroupNameLen)
	row.Note = truncateRunes(strings.TrimSpace(req.Note), maxNoteLen)
	row.SortOrder = req.SortOrder

	if err := gdb.WithContext(c).Create(row).Error; err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: auditActionUserGroupCreate, Result: qymodel.ResultFail,
			Reason: "新建用户分组失败: " + err.Error(), After: row,
		})
		internalError(c, err)
		return
	}
	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupns: 新建后重载登记快照失败: " + err.Error())
	}

	out := CreateUserGroupResult{UserGroup: *row, Warnings: newUserGroupWarnings(name)}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: auditActionUserGroupCreate, Result: qymodel.ResultOK,
		Reason: "新建用户分组(0 人、0 令牌,零行为变化;它要有人挂上去才会生效)",
		After:  out,
	})
	respond(c, out)
}

// newUserGroupWarnings 给出"建好了但还不能用"的具体清单。
func newUserGroupWarnings(name string) []string {
	out := make([]string, 0, 2)
	usable := service.GetUserUsableGroups(name)
	if len(usable) == 0 {
		out = append(out, fmt.Sprintf(
			"用户分组 %q 当前一个模型分组都选不到 —— 把用户挪进来之后他们的全部令牌会立刻 403。"+
				"请先在「用户分组 × 模型分组」里给它授权", name))
	}
	if routed, err := HasRoute(context.Background(), model.DB, name); err == nil && !routed {
		out = append(out, fmt.Sprintf(
			"用户分组 %q 没有同名的渠道池子,因此这一档人的**空分组令牌**(没有指定令牌分组的那些)"+
				"会 503。请为它配一个默认模型分组", name))
	}
	return out
}

// ─────────────────────────── PUT /user-groups/:name ───────────────────────────

type updateUserGroupRequest struct {
	// 四个字段都是指针:不给 = 不改。用零值表达"不改"会让"把备注清空"变得不可能。
	DisplayName *string `json:"display_name"`
	Note        *string `json:"note"`
	Enabled     *bool   `json:"enabled"`
	SortOrder   *int    `json:"sort_order"`

	// TopupRatio 是充值倍率 options.TopupGroupRatio[分组名]。
	//
	// ***float64:给了就设(**含 0**),不给 = 不改。** 用 float64 会让运营填的 0
	// 在反序列化时与"没给"完全一样,一个本该 0 元充值的分组静默留在原价上。
	//
	// 它落在**上游 options**,不落扩展库:充值倍率的唯一真相源是
	// common.topupGroupRatio,四条支付路径(易支付 / Stripe / Waffo / Pancake)
	// 直接乘它。镜像一份到扩展库的同步失败表现是"管理端显示 0.9、收款按 1.0",
	// 与本仓 groupmatrix/store.go 已经论证过的形状逐字相同。
	TopupRatio *float64 `json:"topup_ratio"`
	// ClearTopupRatio 是"把这个键整个删掉"。
	//
	// **必须与 TopupRatio 分开,不能用 null 表达。** Go 的 JSON 反序列化里
	// `"topup_ratio": null` 与字段缺席都得到 nil 指针,两者不可区分;而这两件事的
	// 后果不同:一个是"这次没打算改",另一个是"回落到上游兜底"。
	// 而上游 GetTopupGroupRatio 的兜底是 **1 + 一条 SysError**(不是 0),
	// 所以"删掉键"是一个会改变收款金额的动作,不能靠一个歧义值触发。
	ClearTopupRatio bool `json:"clear_topup_ratio"`
}

// adminUpdateUserGroup 改**展示属性 + 充值倍率**,不碰名字。
//
// 改名单开一个接口(见下),因为两者的量级差了三个数量级:这一个是一次
// UPDATE,那一个要横跨两个数据库改六张表。混在同一个 handler 里的表现是
// 运营改一次备注也要走一遍跨库补偿逻辑。
//
// ══════════════ 没有登记行时**自动补登记**,而不是 400 ══════════════
//
// 管理端那张表的清单是「观测值(users.group)∪ 登记表」,所以表上一定会出现
// 只在观测里、还没有登记行的名字(历史遗留分组、以及回填周期还没跑到的新名字)。
// 对着这样一行按编辑却收到「还没有登记」,运营唯一能做的事是去别处找一个
// 叫「回填」的按钮 —— 而那个按钮解决的是一个他不需要知道的内部区分。
//
// 所以这里补一行再改,并把这件事写进审计正文。补登记本身零行为变化:
// 新行恒为 default_mode=inherit(见 newUserGroup),而登记表是 fail-open 的,
// 它不参与路由、鉴权与计费。
//
// **只对"确实有人挂着"的名字自动补。** 一个既没登记、也没有任何用户的名字
// 意味着请求里的分组名是拼错的(或者那一档刚被删掉),此时建行等于把一个错字
// 变成一个正式分组。那种情况仍然 400。
func adminUpdateUserGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	before, backfilled, err := takeOrRegisterUserGroup(c, gdb, name)
	if err != nil {
		badRequest(c, "qy_groupns_unknown", err.Error())
		return
	}
	var req updateUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.TopupRatio != nil && req.ClearTopupRatio {
		badRequest(c, "qy_invalid_param",
			"topup_ratio 与 clear_topup_ratio 不能同时给出 —— 一个是设值、一个是删键,"+
				"同时给出时服务端无法判断哪一个是本意")
		return
	}
	if req.TopupRatio != nil {
		if err := validateTopupRatio(*req.TopupRatio); err != nil {
			badRequest(c, "qy_invalid_param", err.Error())
			return
		}
	}

	after := before
	patch := map[string]any{"operator_id": c.GetInt("id"), "updated_at": now()}
	if req.DisplayName != nil {
		after.DisplayName = truncateRunes(strings.TrimSpace(*req.DisplayName), maxGroupNameLen)
		patch["display_name"] = after.DisplayName
	}
	if req.Note != nil {
		after.Note = truncateRunes(strings.TrimSpace(*req.Note), maxNoteLen)
		patch["note"] = after.Note
	}
	if req.Enabled != nil {
		after.Enabled = *req.Enabled
		patch["enabled"] = after.Enabled
	}
	if req.SortOrder != nil {
		after.SortOrder = *req.SortOrder
		patch["sort_order"] = after.SortOrder
	}

	if err := gdb.WithContext(c).Model(&UserGroup{}).Where("name = ?", name).Updates(patch).Error; err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: auditActionUserGroupUpdate, Result: qymodel.ResultFail,
			Reason: "编辑用户分组失败: " + err.Error(), Before: before, After: after,
		})
		internalError(c, err)
		return
	}

	// ── 充值倍率落在**上游 options**,与登记表不在同一个库,因此不原子 ──
	//
	// 顺序是算出来的:展示属性(备注/显示名/启停/排序)先写,充值倍率后写。
	// 前者失败时后者一个字节都没动,零资金影响;反过来做,倍率写成功而登记行
	// 写失败时,收款金额已经变了而界面上那一行看起来什么都没保存 ——
	// 运营的下一步必然是"再改一次",于是他会以为自己第一次没点上。
	//
	// 部分失败必须在响应里被看见,不能只回一句绿色的"已保存"。
	topupChanged := ""
	var partial *RewritePartial
	if req.TopupRatio != nil || req.ClearTopupRatio {
		var topupErr error
		topupChanged, topupErr = applyTopupRatio(name, req.TopupRatio, req.ClearTopupRatio)
		if topupErr != nil {
			// ── 半成状态:200 + partial,而不是 500 ────────────────────────
			//
			// 展示属性已经落库了。回 500 的表现是弹窗弹出一句通用的
			// 「处理失败,请稍后重试」,运营合理推断"什么都没保存",而下一次回读
			// 会显示新备注 + 旧倍率 —— 那份解释此前只写在审计正文里,
			// 而看审计的人不是此刻站在弹窗前的那个人。
			//
			// 前端对 partial 的处理是"原样弹出并常驻"(见 user-group-dialog.tsx),
			// 这条链路上唯一的半成状态因此才真的能被看见。
			audit.WriteConfigUpdate(c, audit.ConfigChange{
				Action: auditActionUserGroupUpdate, Result: qymodel.ResultFail,
				Reason: "展示属性已保存,但充值倍率没能写进 options(**收款金额仍按旧倍率**," +
					"请重试;不重试的话界面显示的是新值而收款按旧值): " + topupErr.Error(),
				Before: before, After: after,
			})
			partial = &RewritePartial{
				Stage: StageTopup,
				Message: "备注等展示属性**已经保存**,但充值倍率**没有写进去** —— " +
					"收款此刻仍然按旧倍率。请重新打开这一档确认充值倍率,并重试一次。" +
					"(失败原因: " + topupErr.Error() + ")",
			}
		}
	}

	if err := InvalidateAndReload(); err != nil {
		common.SysError("qianye/groupns: 编辑后重载登记快照失败: " + err.Error())
	}
	// 基句必须说到"钱":这个 handler 会写 options.TopupGroupRatio,而那个数
	// 直接乘进收款金额。早先写死的「不影响路由与计费」会让事后按这句话做审计
	// 排除的人整条漏掉一次改价,而 Before/After 快照是 UserGroup 结构体、
	// 充值倍率根本不在里面 —— 正文是这次改动唯一的证据来源。
	reason := "编辑用户分组的展示属性(备注/显示名/启停/排序,不影响路由与鉴权)"
	if backfilled {
		reason += ";**该名字此前只存在于 users.group,本次编辑顺带补了一行登记**" +
			"(default_mode=inherit,零行为变化)"
	}
	if topupChanged != "" {
		// 充值倍率直接乘进收款金额,必须单独进审计正文而不是只留在 before/after 的
		// 字段差里 —— 它根本不在 UserGroup 这个结构体上,字段差里看不到它。
		reason += ";" + topupChanged
	}
	if partial == nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: auditActionUserGroupUpdate, Result: qymodel.ResultOK,
			Reason: reason, Before: before, After: after,
		})
	}
	respond(c, gin.H{
		"user_group": after, "backfilled": backfilled,
		"topup_change": topupChanged, "partial": partial,
	})
}

// maxTopupRatio 是充值倍率的上界。
//
// 上游对它没有任何校验,而它被直接乘进 controller/topup.go 的 payMoney:
// 一次手滑填成 1e18,配合任意充值额就会把金额推过 decimal → float 的可用范围,
// 而支付网关收到的那个数没有任何意义。负数更直接 —— 它是一张负价订单。
const maxTopupRatio = 1000

// validateTopupRatio 是充值倍率的取值闸门。
//
// ══════════════ 为什么 **0 也被拒绝** ══════════════
//
// 「0 = 显式免费」是本仓在**计费倍率**上的口径(交叉倍率的 0 会命中
// relay/helper/price.go 的整体替换分支,真的免费)。充值倍率不是同一件事:
//
//	controller/topup.go:158 / topup_stripe.go:389,403 / topup_waffo.go:94 /
//	topup_waffo_pancake.go:59 —— 五处逐字相同:
//	    ratio := common.GetTopupGroupRatio(group)
//	    if ratio == 0 { ratio = 1 }        ← 0 在这里被抬回 1
//
// 也就是说库里的 0 与"没配过"收同样的钱。就算把这五处 if 删掉,
// controller/topup.go:208 还有一道 `payMoney < 0.01 → 拒单`,结果是
// 这一档人**充不了值**,而不是"充值免费"。
//
// 两种结局都不是运营填 0 时想要的,而界面此前恰恰写着「填 0 = 这一档充值免费」。
// 与其让一个值在三层实现里表达三种意思,不如在入口把它拒掉并说清楚:
// 要免费发额度走「额度」那一侧,要按 1 收款就清空这个键。
func validateTopupRatio(v float64) error {
	if v != v { // NaN
		return errors.New("充值倍率不是合法数值")
	}
	if v < 0 {
		return fmt.Errorf("充值倍率不得为负数,收到 %v —— 负倍率会把充值变成一张负价订单", v)
	}
	if v == 0 {
		return errors.New("充值倍率不能是 0。0 在充值这一侧**不等于免费**:" +
			"四条支付路径读到 0 之后一律按 1 收款,而且就算不抬,订单也会因为金额低于 0.01 被拒 —— " +
			"这一档人会变成充不了值。要按原价收款请清空这个值(留空 = 按 1 收款),要免费发额度请走额度那一侧")
	}
	if v > maxTopupRatio {
		return fmt.Errorf("充值倍率不得超过 %d,收到 %v", maxTopupRatio, v)
	}
	return nil
}

// applyTopupRatio 写一次 options.TopupGroupRatio,返回进审计正文的那句话。
//
// 读改写整张 map:上游只提供整表的 JSON 读写口。并发窗口与
// groupmatrix.publishRatioMatrix 同形,由 updateOptionVerified 的回查兜底
// (它读的是 options 表本身,不是我们刚写进内存的那份)。
//
// **值没变时不写。** 一次无变化的 UpdateOption 会广播给全部节点并刷一次内存快照,
// 而它换不到任何东西;更要紧的是它会在审计里留下一条"改过充值倍率"的记录,
// 而事后复盘看到那条记录的人会去找一个并不存在的改动。
func applyTopupRatio(name string, ratio *float64, clear bool) (string, error) {
	current, err := loadTopupRatios()
	if err != nil {
		return "", err
	}
	old, had := current[name]
	switch {
	case clear:
		if !had {
			return "", nil
		}
		delete(current, name)
		if err := saveTopupRatios(current); err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"充值倍率由 %s **删除**(此后上游 GetTopupGroupRatio 找不到这个键,"+
				"回落到 1 并写一条 SysError —— 那个 1 不是任何人配出来的)", ratioText(old)), nil
	default:
		if had && old == *ratio {
			return "", nil
		}
		current[name] = *ratio
		if err := saveTopupRatios(current); err != nil {
			return "", err
		}
		if !had {
			return fmt.Sprintf("充值倍率**新配**为 %s(此前未配置,按兜底 1 收款)",
				ratioText(*ratio)), nil
		}
		return fmt.Sprintf("充值倍率 %s → %s", ratioText(old), ratioText(*ratio)), nil
	}
}

// lookupUserGroup 取登记行,允许它不存在。
//
// 第二个返回值报告登记行在不在。名字既没登记、users.group 里也没人挂着时返回
// 错误 —— 那是一个不存在的分组,不是一个"未登记"的分组,两者在界面上必须分开。
func lookupUserGroup(c *gin.Context, gdb *gorm.DB, name string) (UserGroup, bool, error) {
	var row UserGroup
	if name == "" {
		return row, false, errors.New("缺少用户分组名")
	}
	err := gdb.WithContext(c).Where("name = ?", name).Take(&row).Error
	if err == nil {
		return row, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, err
	}
	if countByGroupCtx(c, "users")[name] == 0 {
		return row, false, fmt.Errorf(
			"用户分组 %q 既没有登记行,users.group 里也没有任何人挂着它 —— "+
				"名字可能拼错了,或者这一档已经被删掉", name)
	}
	row.Name = name
	return row, false, nil
}

// takeOrRegisterUserGroup 取登记行;只在观测里的名字**顺带补一行登记**。
//
// 第二个返回值报告是否真的补过 —— 它进审计正文与响应,
// 因为"这一行是这次编辑顺带建出来的"是事后唯一能解释
// 「这个分组的 created_at 为什么是那一天」的信息。
//
// 判据是 users.group 里此刻有没有人挂着(见 adminUpdateUserGroup 的注释):
// 没有人挂着的未登记名字一律拒绝,那种请求里的名字多半是拼错的。
func takeOrRegisterUserGroup(c *gin.Context, gdb *gorm.DB, name string) (UserGroup, bool, error) {
	var row UserGroup
	if name == "" {
		return row, false, errors.New("缺少用户分组名")
	}
	err := gdb.WithContext(c).Where("name = ?", name).Take(&row).Error
	if err == nil {
		return row, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, err
	}
	if countByGroupCtx(c, "users")[name] == 0 {
		return row, false, fmt.Errorf(
			"用户分组 %q 既没有登记行,users.group 里也没有任何人挂着它 —— "+
				"名字可能拼错了,或者这一档已经被删掉。要新建一档请走「新建用户分组」", name)
	}
	if !registrableGroupName(name) {
		return row, false, fmt.Errorf(
			"用户分组 %q 超过 %d 个字符,登记表存不下它(见 registrableGroupName)—— "+
				"它仍然可以正常计费与路由(登记表是 fail-open 的),但配不了备注与默认模型分组",
			name, maxGroupNameLen)
	}
	fresh := newUserGroup(name, c.GetInt("id"), now())
	if err := gdb.WithContext(c).Create(fresh).Error; err != nil {
		return row, false, err
	}
	return *fresh, true, nil
}

// ─────────────────────── POST /user-groups/:name/rename ───────────────────────

type renameUserGroupRequest struct {
	NewName string `json:"new_name"`
	// ExpectUsers 是运营在界面上看到的人数。对不上返回 409。
	//
	// 它不是防并发改名(那由名字本身的唯一性挡住),是防**影响面漂移**:
	// 弹窗打开时是 3 个人、按下按钮时已经是 300 个人,而运营是照着 3 这个数字
	// 做的决定。ExpectUsers 为负表示调用方放弃这道闸门(脚本用)。
	ExpectUsers int64 `json:"expect_users"`
}

// adminRenameUserGroup 改名。
//
// 改名 = 把配置复制到新名字 → 迁用户 → 删旧名字的配置。三阶段的理由与补偿
// 见 usergroup_store.go 的文件头。
func adminRenameUserGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	from := strings.TrimSpace(c.Param("name"))
	var before UserGroup
	if err := gdb.WithContext(c).Where("name = ?", from).Take(&before).Error; err != nil {
		badRequest(c, "qy_groupns_unknown", "用户分组 "+from+" 还没有登记")
		return
	}
	var req renameUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	to := strings.TrimSpace(req.NewName)
	if to == from {
		badRequest(c, "qy_invalid_param", "新名字与原名字相同")
		return
	}
	if err := validateNewUserGroupName(gdb.WithContext(c), to); err != nil {
		badRequest(c, "qy_groupns_invalid_name", err.Error())
		return
	}
	// 改名也要过 block 闸门。
	//
	// 直觉上 block 是"删除专用"的:删除让名字消失,改名只是换个字。但对一份
	// **冻结的、动不了的**引用来说两者完全等价 —— 抽奖活动的参与名单在发布时
	// 进了 commit_hash,改名之后那份名单指着一个再也没有人的名字,与被删掉一样。
	// 少了这一道,运营会发现"删不掉就改个名",而那是同一次事故换了个入口。
	//
	// 判据与影响面接口下发给界面的那一份**是同一个函数**(renamability):
	// 两处各写一遍的表现是"弹窗说能改、按下去 400"。
	residues, err := ProbeResidues(gdb.WithContext(c), from)
	if err != nil {
		internalError(c, err)
		return
	}
	if ok, code, reason := renamability(from, residues); !ok {
		// 两个历史错误码保持不变:default 那条是"这一档天生如此",残留那条是
		// "先去处理别的东西",调用方(以及既有测试)按码分流。
		if code == UserGroupBlockDefault {
			badRequest(c, "qy_groupns_protected", reason)
			return
		}
		badRequest(c, "qy_groupns_rename_blocked", reason)
		return
	}

	users := countByGroupCtx(c, "users")[from]
	if req.ExpectUsers >= 0 && req.ExpectUsers != users {
		conflict(c, "qy_groupns_impact_drift", fmt.Sprintf(
			"这一档的在册用户数已经从 %d 变成 %d —— 请重新载入影响面再操作",
			req.ExpectUsers, users))
		return
	}

	res, err := rewriteUserGroup(gdb.WithContext(c), from, to, true, c.GetInt("id"))
	reason := fmt.Sprintf("用户分组改名 %q → %q(迁移 %d 名在册用户、%d 条订阅快照、%d 个套餐引用)",
		from, to, res.Users, res.Subscriptions, res.Plans)
	if res.Stragglers > 0 {
		reason += fmt.Sprintf(";**仍有 %d 名用户挂在旧名字上**(改写期间被并发写入)", res.Stragglers)
	}
	if res.CacheMisses > 0 {
		reason += fmt.Sprintf(";%d 个用户的缓存失效失败,他们在 TTL 内仍按旧分组计价", res.CacheMisses)
	}
	if err != nil {
		if res.Partial != nil {
			reason += ";" + res.Partial.Message
		}
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: auditActionUserGroupRename, Result: qymodel.ResultFail,
			Reason: "改名失败: " + reason + ";" + err.Error(), Before: before, After: res,
		})
		partialResponse(c, "qy_groupns_rename_partial", res)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: auditActionUserGroupRename, Result: qymodel.ResultOK,
		Reason: reason, Before: before, After: res,
	})
	respond(c, res)
}

// ─────────────────────── DELETE /user-groups/:name ───────────────────────

type deleteUserGroupRequest struct {
	// MigrateTo 是这批用户要迁去的用户分组。**有用户时必填。**
	MigrateTo string `json:"migrate_to"`
	// ExpectUsers 见 renameUserGroupRequest.ExpectUsers。
	ExpectUsers int64 `json:"expect_users"`
	// AckLosesEverything 是「迁过去之后这批人一个模型分组都用不了」这道闸门的
	// 显式覆盖。它进审计。
	//
	// 单独一道闸门而不是并进 ExpectUsers:两者的后果方向完全不同 —— 一个是
	// 「你看到的数字过期了」,另一个是「这 700 个人的令牌下一秒全部 403」,
	// 而后者不会有任何界面提示,只会有工单。
	AckLosesEverything bool `json:"ack_loses_everything"`
}

// adminDeleteUserGroup 删除一个用户分组,必要时先把它的用户迁走。
//
// ══════════════ 顺序是这个接口的全部安全性来源 ══════════════
//
//  1. 算影响面并复核硬闸门(套餐引用、block 残留、default)
//  2. 有用户 ⇒ 迁移目标必填、必须已登记、不能是自己
//  3. 一致性闸门:回传的人数与此刻不符 ⇒ 409
//  4. 迁用户(主库一个事务)→ 失效缓存 → 清扩展库配置 → 清 options 键
//
// 第 4 步的每一个中断点上,users.group 指向的名字都仍然有完整配置(见
// usergroup_store.go)。反过来做会造出孤儿,而孤儿的表现是**静默按 1.0 计费**。
func adminDeleteUserGroup(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	// 与影响面接口同一道判据:**不要求有登记行**。
	// 只在 users.group 里的历史遗留分组同样要能删(而且要能带迁移地删)——
	// 它们正是最需要被清掉的那一类。rewriteUserGroup 的第三阶段对一条不存在的
	// 登记行是无操作,其余残留清理与 options 清理与登记行毫无关系。
	before, registered, err := lookupUserGroup(c, gdb, name)
	if err != nil {
		badRequest(c, "qy_groupns_unknown", err.Error())
		return
	}
	var req deleteUserGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}

	impact, err := buildUserGroupImpact(c, gdb, name)
	if err != nil {
		internalError(c, err)
		return
	}
	if !impact.Deletable {
		badRequest(c, "qy_groupns_delete_blocked", impact.BlockReason)
		return
	}
	if req.ExpectUsers >= 0 && req.ExpectUsers != impact.Users {
		conflict(c, "qy_groupns_impact_drift", fmt.Sprintf(
			"这一档的在册用户数已经从 %d 变成 %d —— 请重新载入影响面再操作",
			req.ExpectUsers, impact.Users))
		return
	}

	target := strings.TrimSpace(req.MigrateTo)
	var diff MigrationDiff

	// ── 什么时候必须给迁移目标 ──
	//
	// 在册用户数不是唯一的来源。处置为 rewrite 的残留同样只有在给了目标时才
	// 有得可写:最典型的是「新注册用户的默认分组」—— 那一档人**还不存在**,
	// 一个人都不在 impact.Users 里,而目标为空时那一行只能被清掉,
	// 此后每一个新注册用户静默落进上游 default 档(不同的倍率、不同的可用清单)。
	// 这正是弹窗把那一行标成「跟着改写」时承诺的相反面。
	rewriteRows := RewriteResidueRows(impact.Residues)
	if target == "" && (impact.Users > 0 || rewriteRows > 0) {
		if impact.Users > 0 {
			badRequest(c, "qy_groupns_migration_required", fmt.Sprintf(
				"用户分组 %q 下还有 %d 名在册用户、%d 个令牌。删除之前必须选一个迁移目标 —— "+
					"直接删会让这批账号的 users.group 指向一个不存在的分组,"+
					"而分组倍率对孤儿是 fail-open 返回 1.0,他们会静默按原价扣费",
				name, impact.Users, impact.Tokens))
			return
		}
		badRequest(c, "qy_groupns_migration_required", fmt.Sprintf(
			"用户分组 %q 已经没有在册用户,但仍有 %d 处配置指向它、且处置是「跟着改写」"+
				"(%s)。删除之前必须选一个迁移目标 —— 目标为空时这些配置只能被清掉,"+
				"而清掉它们等于做出一次没有任何人决定过的策略变更",
			name, rewriteRows, describeRewriteResidues(impact.Residues)))
		return
	}

	// 目标一旦给了就必须校验,与在册用户数无关:即使这一档已经没有用户,
	// 这个名字仍然会被写进 user_subscriptions 的三列快照(那三条 UPDATE 是
	// 按列值全表匹配的,与被迁移的用户无关)以及默认注册分组。
	// 一个从未校验过存在性的名字写进这两处,表现是买家将来被降级进一个
	// 不存在的分组、新用户注册进一个不存在的分组 —— 两者都静默按 1.0 计费。
	if target != "" {
		if target == name {
			badRequest(c, "qy_invalid_param", "迁移目标不能是正在被删除的分组本身")
			return
		}
		var dst UserGroup
		if err := gdb.WithContext(c).Where("name = ?", target).Take(&dst).Error; err != nil {
			badRequest(c, "qy_groupns_unknown_target",
				"迁移目标 "+target+" 不是一个已登记的用户分组")
			return
		}
	}
	if impact.Users > 0 {
		diff = BuildMigrationDiff(name, target, service.GetUserUsableGroups)
		if diff.LosesEverything && !req.AckLosesEverything {
			badRequest(c, "qy_groupns_target_unusable", fmt.Sprintf(
				"迁移目标 %q 当前一个模型分组都选不到 —— 这 %d 名用户的全部令牌会在迁移完成的"+
					"那一刻同时 403。确认这是有意的请显式勾选强制覆盖",
				target, impact.Users))
			return
		}
	}

	res, err := rewriteUserGroup(gdb.WithContext(c), name, target, false, c.GetInt("id"))
	res.Residues = impact.Residues

	reason := fmt.Sprintf("删除用户分组 %q", name)
	if !registered {
		// 这一档从来没有登记行,所以 before 快照里除了名字之外什么都没有。
		// 不写明的话,事后看到一条 before 几乎全空的删除记录,第一反应会是
		// 「审计埋点坏了」,而真相是这个分组本来就只存在于 users.group 里。
		reason += "(**该分组此前只存在于 users.group,没有登记行**,因此 before 快照只有名字)"
	}
	if target != "" {
		reason += fmt.Sprintf(",把 %d 名在册用户 / %d 个令牌迁到 %q(同时改写 %d 条订阅快照)",
			res.Users, impact.Tokens, target, res.Subscriptions)
		reason += ";" + describeDiff(diff)
	} else {
		reason += "(该分组已经没有任何在册用户,无需迁移)"
	}
	if req.AckLosesEverything && diff.LosesEverything {
		reason += ";**强制覆盖「目标分组一个模型分组都用不了」闸门**"
	}
	if res.Stragglers > 0 {
		reason += fmt.Sprintf(";**仍有 %d 名用户挂在被删的分组上**(迁移期间被并发写入),"+
			"他们此刻是孤儿,请手工改到目标分组", res.Stragglers)
	}
	if res.CacheMisses > 0 {
		reason += fmt.Sprintf(";%d 个用户的缓存失效失败,他们在 TTL 内仍按旧分组计价", res.CacheMisses)
	}
	reason += ";" + describeResidues(res.Residues)

	if err != nil {
		if res.Partial != nil {
			reason += ";" + res.Partial.Message
		}
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: auditActionUserGroupDelete, Result: qymodel.ResultFail,
			Reason: "删除失败: " + reason + ";" + err.Error(), Before: before, After: res,
		})
		partialResponse(c, "qy_groupns_delete_partial", res)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: auditActionUserGroupDelete, Result: qymodel.ResultOK,
		Reason: reason, Before: before, After: res,
	})
	respond(c, res)
}

// describeDiff 把可用清单与倍率差压成一句进审计的话。
//
// 只记条数不记内容是不够的:复盘时的第一个问题是「那次迁移之后他们少了哪几个分组」,
// 而那份清单在别处再也拿不到 —— 迁完之后源分组已经不存在了,重算不出来。
func describeDiff(diff MigrationDiff) string {
	if len(diff.Changes) == 0 {
		return fmt.Sprintf("可用模型分组与倍率完全不变(%d 个分组)", diff.Unchanged)
	}
	var removed, added, repriced []string
	for _, ch := range diff.Changes {
		switch ch.Kind {
		case "removed":
			removed = append(removed, ch.ModelGroup)
		case "added":
			added = append(added, ch.ModelGroup)
		default:
			repriced = append(repriced, fmt.Sprintf("%s %s→%s", ch.ModelGroup, ch.FromRatio, ch.ToRatio))
		}
	}
	parts := make([]string, 0, 3)
	if len(removed) > 0 {
		parts = append(parts, "**失去** "+strings.Join(removed, "、"))
	}
	if len(added) > 0 {
		parts = append(parts, "获得 "+strings.Join(added, "、"))
	}
	if len(repriced) > 0 {
		parts = append(parts, "**改价** "+strings.Join(repriced, "、"))
	}
	return "可用模型分组变化:" + strings.Join(parts, ";")
}

// describeResidues 把连带清理的结果压成一句进审计的话。
// describeRewriteResidues 把"哪几处配置在等一个迁移目标"列成一句话。
//
// 只给条数是不够的:运营看到「还有 1 处」的第一反应是去猜那是哪儿,
// 而这一处恰恰是**没有任何界面会显示**的那一处(新注册用户的默认分组)。
func describeRewriteResidues(rows []Residue) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Disposition == ResidueRewrite && row.Rows > 0 {
			parts = append(parts, fmt.Sprintf("%s(%s)", row.Label, row.Table))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "、")
}

func describeResidues(rows []Residue) string {
	if len(rows) == 0 {
		return "没有任何模块声明过以这个分组名为键的配置"
	}
	sorted := append(make([]Residue, 0, len(rows)), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Module != sorted[j].Module {
			return sorted[i].Module < sorted[j].Module
		}
		return sorted[i].Table < sorted[j].Table
	})
	parts := make([]string, 0, len(sorted))
	for _, row := range sorted {
		if row.Rows == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s %s %d 行(%s)",
			row.Module, row.Table, row.Label, row.Rows, row.Disposition))
	}
	if len(parts) == 0 {
		return "连带清理:全部登记的关联表都没有命中行"
	}
	return "连带清理:" + strings.Join(parts, "、")
}

// truncateRunes 按**字符**截断,不是字节。
//
// 按字节截会把一个中文备注砍出半个 UTF-8 序列,MySQL 在 STRICT_TRANS_TABLES 下
// 以 Error 1366 拒绝整条 UPDATE —— 而报错文案与备注毫无关系。
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// countByGroupCtx 是 countByGroup 的 context 版本。
//
// 影响面接口不在 gin.Context 之外调用,但删除路径上的复核会在 handler 内部
// 二次调用,拿 context.Context 比拿 *gin.Context 更诚实。
func countByGroupCtx(ctx context.Context, table string) map[string]int64 {
	out := map[string]int64{}
	if model.DB == nil {
		return out
	}
	col := model.QyCommonGroupCol()
	var rows []struct {
		Grp string `gorm:"column:grp"`
		Cnt int64  `gorm:"column:cnt"`
	}
	sql := "SELECT " + col + " AS grp, COUNT(*) AS cnt FROM " + table +
		" WHERE deleted_at IS NULL GROUP BY " + col
	if err := model.DB.WithContext(ctx).Raw(sql).Scan(&rows).Error; err != nil {
		return out
	}
	for _, row := range rows {
		out[row.Grp] = row.Cnt
	}
	return out
}

func conflict(c *gin.Context, code, msg string) {
	c.JSON(http.StatusConflict, gin.H{"success": false, "code": code, "message": msg})
}

// partialResponse 回一个**带完整结果体**的失败。
//
// 不能只回一句 message:半成状态下运营最需要知道的正是"已经做到哪一步了",
// 而那全在 RewriteResult.Partial 里。
func partialResponse(c *gin.Context, code string, res RewriteResult) {
	msg := "操作未能完整完成"
	if res.Partial != nil {
		msg = res.Partial.Message
	}
	c.JSON(http.StatusInternalServerError, gin.H{
		"success": false, "code": code, "message": msg, "data": res,
	})
}
