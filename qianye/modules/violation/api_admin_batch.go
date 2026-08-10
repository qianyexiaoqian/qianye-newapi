package violation

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_batch.go —— 规则列表的多选批量操作。
//
// 项目方原话:「违规规则配置,增加一个多选,可以批量进行作用分组的划分,启动,禁用。」
//
// ══════════════════════ 「作用分组」是哪个维度 ══════════════════════
//
// 是**模型分组**,不是用户分组。判据不在命名上,在 compiledRule.inScope 比的那个值:
// 它比的是 relayInfo.UsingGroup —— 这次请求实际路由到的模型分组,而不是"这个人是谁"。
// 同一个事实在 modelgroup_residue.go 里已经登记过一次(违规规则挂在**模型分组**删除
// 这条链上,用户分组删除那条链把它显式豁免掉了)。批量层不再重新发明一个维度。
//
// 落到列上就是 group_scope(逗号分隔的分组名)+ group_scope_mode(include / exclude)。
//
// ══════════════════════ 三条不能塌的边 ══════════════════════
//
//  1. **方向必须由调用方明说,而且跟着一起写。**
//     同一串分组名在 include 与 exclude 两个方向下的含义完全相反:前者是"只对这些
//     分组生效",后者是"对这些分组豁免"。批量追加时如果只写名单不写方向,给一条
//     exclude 规则追加 "vip" 就是**多豁免了一个分组**,而操作者以为自己多防了一个。
//     所以 group_scope_mode 是必填,不是可选;而且 append / remove 遇到方向与请求
//     不一致的规则一律拒做(batchCodeDirectionMismatch),绝不"顺手帮它翻个向"。
//
//  2. **mode(影子 / 真实)不进批量。** 见 adminBatchSetRuleEnabled 的注释。
//
//  3. **逐条独立成败,整批不回滚。** 规则彼此之间没有任何关系,一条写失败就把已经
//     改好的其余几条退回去,只会让管理员失去已完成的部分,而重试的结果完全一样。
//     与 adminImportBuiltinRules 同口径。
//
// ══════════════════════ 为什么整批一律 200,即使一条都没成功 ══════════════════════
//
// 因为 data 里的**逐条明细**才是这两个接口的产品:哪几条失败、各自为什么,是管理员
// 接下来唯一能依据的东西。把"全失败"表达成 success:false 会让信封的 data 整个作废
// —— qy 前端的 unwrap 在 success:false 时直接抛错并丢掉 data,管理员就只剩一句
// "操作失败",连哪几条失败都看不到。这与 channelops 的批量端点是同一条结论。
//
// 这与"全部失败却回 200 谎报成功"不是一回事:响应体里 succeeded=0、failed=N 白纸黑字,
// 前端据此弹的是红色 toast + 明细弹窗,而不是绿色。判据是**响应体说没说真话**,
// 不是 HTTP 状态码好不好看。
//
// 与同模块的 adminImportBuiltinRules(全失败回 500)不同,是因为那条路径的失败明细
// 渲染在导入面板自己的成功分支里,而这里的明细弹窗是批量操作唯一的产出。
//
// 整批级别的 4xx 只留给**一条库都没碰**的入参问题(没勾规则、超上限、方向没填、
// 未确认的 enforce 启用)。

// 批量动作的审计 action。
const (
	actionBatchSetEnabled    = "rules.batch_set_enabled"
	actionBatchSetGroupScope = "rules.batch_set_group_scope"
)

// 单条结果的三档。它们**不是**成功/失败两分:
//
//	batchItemOK       真的改了库
//	batchItemSkipped  库里本来就是目标状态,一个字节都不用动。**这不是失败**
//	batchItemFailed   没做成,需要人来看
//
// 中间那一档必须独立存在。把"本来就是启用的"算进失败里,一次"全选 → 批量启用"
// 会报「18 条启用失败」,管理员会去排查一个根本不存在的故障 —— 上游渠道批量接口
// 正是栽在这里(见 qianye/modules/channelops/batch.go 的同名注释)。
//
// 恒等式 Total == Succeeded + Skipped + Failed 必须成立:它是前端唯一能信的东西。
// 少了它,"选 20 条、成功 18 条"就无法回答剩下 2 条是失败了还是本来就不用动。
const (
	batchItemOK      = "ok"
	batchItemSkipped = "skipped"
	batchItemFailed  = "failed"
)

// 单条级别的结果码。整批仍然 200,这些码落在 Items[i].Code 里由前端逐条渲染。
//
// 分这么多档而不是一句"失败":它们要求管理员做的下一步完全不同 —— 已被别人删掉的
// 只需刷新列表,编译不过的要去修那条规则的模式串,方向不一致的要先想清楚自己
// 到底在编辑哪一种名单,而"已经是目标状态"根本不是失败。
const (
	batchCodeNotFound          = "qy_vio_batch_item_not_found"
	batchCodeNoChange          = "qy_vio_batch_item_no_change"
	batchCodeWontCompile       = "qy_vio_batch_item_wont_compile"
	batchCodeTooLong           = "qy_vio_batch_item_scope_too_long"
	batchCodeDirectionMismatch = "qy_vio_batch_item_direction_mismatch"
	batchCodeEnforceAck        = "qy_vio_batch_item_enforce_ack"
	batchCodeStale             = "qy_vio_batch_item_stale"
	batchCodeDBError           = "qy_vio_batch_item_db_error"
)

// 整批级别的失败码。这一类让整个请求 4xx / 5xx,与逐条码不在同一个空间。
const (
	// batchCodeEnforceAckRequired 是"选中里有真实模式的规则,但请求没有确认过"。
	// 单独成码而不是并进 qy_vio_bad_request:前端要靠它把返回的 enforce 清单
	// 摊到二次确认框里,而不是弹一句"参数不合法"。
	batchCodeEnforceAckRequired = "qy_vio_batch_enforce_ack_required"
)

// maxRuleBatchIds 是单次批量的上限。
//
// 与 channelops 的 maxBatchIds 同取 200:超过这个量级要串行跑 200 次读+写,
// 前端等待期间没有任何进度可言。规则表本身也远小于这个量级 —— 站点规则总数
// 上百就已经是异常配置。
const maxRuleBatchIds = 200

// batchRuleItem 是单条规则的执行结果。
//
// Name 一定要带上:失败列表里只有 id 的话,管理员得回列表页一个个对照才知道
// 挂掉的是哪条规则,而这一屏正是他做决定的地方。
type batchRuleItem struct {
	Id      int64  `json:"id"`
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	// Code 是稳定标识,前端据此映射 i18n 文案;Detail 是中文兜底,
	// 只在 Code 未被前端登记时兜住,不保证可翻译。
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// batchRuleResult 是两个批量端点共用的返回体。
type batchRuleResult struct {
	Total     int             `json:"total"`
	Succeeded int             `json:"succeeded"`
	Skipped   int             `json:"skipped"`
	Failed    int             `json:"failed"`
	Items     []batchRuleItem `json:"items"`
}

// runRuleBatch 逐条执行 apply 并把结果汇总。
//
// 先按 id 读一次行:读不到就是 not_found(多半是别的管理员刚删掉),读到了才把
// Name 带进结果。apply 拿到的是这一行的最新快照,它自己负责 CAS。
func runRuleBatch(gdb *gorm.DB, ids []int64,
	apply func(row *Rule) (outcome string, code string, detail string)) batchRuleResult {
	res := batchRuleResult{Total: len(ids), Items: make([]batchRuleItem, 0, len(ids))}
	for _, id := range ids {
		item := batchRuleItem{Id: id}
		var row Rule
		switch err := gdb.Where("id = ?", id).Take(&row).Error; {
		case err == nil:
			item.Name = row.Name
			item.Outcome, item.Code, item.Detail = apply(&row)
		case errors.Is(err, gorm.ErrRecordNotFound):
			item.Outcome, item.Code = batchItemFailed, batchCodeNotFound
			item.Detail = "规则不存在,可能已被其他管理员删除"
		default:
			db.MarkFailure(err)
			common.SysError("qianye/violation: 批量操作读取规则失败: " + err.Error())
			item.Outcome, item.Code = batchItemFailed, batchCodeDBError
			item.Detail = "读取规则失败,请查看后端日志"
		}
		switch item.Outcome {
		case batchItemOK:
			res.Succeeded++
		case batchItemSkipped:
			res.Skipped++
		default:
			res.Failed++
		}
		res.Items = append(res.Items, item)
	}
	return res
}

// normalizeRuleIds 校验并去重入参 id。
//
// # 为什么非法 id 是整批 400 而不是逐条 failed
//
// id <= 0 不可能来自列表页的勾选框,它只可能来自手搓的请求或前端的 bug。把它计成
// "这一条失败了"会让报告里混进一条管理员看不懂、也无从处理的行;整批拒绝反而准确:
// 这个请求本身有问题。
//
// 去重则相反,静默合并:同一个 id 在选中集里出现两次是前端的事,对管理员来说
// "这条规则被启用了"就是一件事,报两行只会让计数看起来对不上。
func normalizeRuleIds(raw []int64) ([]int64, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("请先勾选要操作的规则")
	}
	if len(raw) > maxRuleBatchIds {
		return nil, fmt.Errorf("一次最多操作 %d 条规则,请分批进行", maxRuleBatchIds)
	}
	seen := make(map[int64]struct{}, len(raw))
	out := make([]int64, 0, len(raw))
	for _, id := range raw {
		if id <= 0 {
			return nil, fmt.Errorf("非法的规则 id: %d", id)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// ───────────────────────── 批量启用 / 禁用 ─────────────────────────

// batchEnabledReq 是批量启停的入参。
//
// Enabled 收 *bool 而不是 bool,理由与单条启停一致:漏传字段的 bool 零值是 false,
// 于是一次拼错字段名的调用会变成"静默批量停用"—— 而停用正是这条路径上唯一
// 没有症状的方向。
type batchEnabledReq struct {
	Ids     []int64 `json:"ids"`
	Enabled *bool   `json:"enabled"`
	// AckEnforce 是"我已经看到选中里有哪些真实模式的规则,并且确认要把它们打开"。
	//
	// 它只在 enabled=true 且选中里存在**当前停用的 enforce 规则**时才被要求。
	// 见 adminBatchSetRuleEnabled 的「为什么 mode 不进批量」。
	AckEnforce bool `json:"ack_enforce"`
}

// adminBatchSetRuleEnabled 批量启用 / 禁用规则。
//
// ══════════════ 为什么 mode(影子 / 真实)**不**进批量 ══════════════
//
// 项目方要的三件事是"作用分组的划分、启动、禁用",mode 不在其中,而把它顺手放进来
// 是有具体代价的:
//
//   - 批量改 mode 的杀伤面与批量启停完全不是一个量级。启用一批**影子**规则的后果
//     是多写几行记录;把一批规则从 shadow 切成 enforce,下一秒就开始真的扣费、阻断、
//     累计封号 —— 而这一页的规则里有 `.*` 这种量级的模式串,一条就能在 30 秒内封掉
//     全站用户(model.go 开篇的第一条铁律)。
//   - 影子模式的**唯一用途**是"拿这条规则抓到的日志做误判分析"(项目方原话)。
//     转正的前提是逐条看过它的命中分布 —— 那是一个逐条的判断,批量把它抹平了。
//   - 单条编辑抽屉里改 mode 已经是一次带上下文的操作(能同时看到 pattern、作用域、
//     扣费方式)。批量入口只会让人在看不到这些的情况下改掉最重的那一列。
//
// 所以 mode 只能在单条编辑里改。批量层不提供它,也不因为任何别的字段被顺手改掉。
//
// ══════════════ 但「批量启用」本身仍然会碰到 enforce ══════════════
//
// 启用一条已经是 enforce 的规则,效果与把它从 shadow 切成 enforce 一模一样:
// 下一秒开始真的扣钱。所以这条路径上必须把影响面摆出来 ——
//
//	预检:选中里有多少条**当前停用的 enforce 规则**。有就整批 400,
//	      带上它们的 id 与名字,由前端摊进二次确认框;确认之后带 ack_enforce=true 重来。
//	兜底:逐条执行时再判一次。预检到写入之间别人完全可以把某条规则切成 enforce,
//	      而那一条不该借着这次已经确认过的批次溜进真实执行。
//
// 停用方向不需要确认闸:关掉一条正在误伤的规则是紧急出口,不能被校验挡住
// (与 setRuleEnabled 的编译闸同一条取舍)。它的代价由前端的二次确认负责提示。
func adminBatchSetRuleEnabled(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req batchEnabledReq
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		badRequest(c, "enabled 字段必填,且只能是 true 或 false")
		return
	}
	ids, err := normalizeRuleIds(req.Ids)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	gdb := ctxDB(c)
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	enabled := *req.Enabled
	if enabled && !req.AckEnforce {
		pending, err := pendingEnforceRules(gdb, ids)
		if err != nil {
			internalError(c, err)
			return
		}
		if len(pending) > 0 {
			// 一条库都没碰,不写审计(与 channelops 的分界线一致:有没有碰到库里的
			// 既有状态)。它只是把影响面还给前端,紧接着就会有一次带 ack 的重试。
			respondFailData(c, http.StatusBadRequest, batchCodeEnforceAckRequired,
				fmt.Sprintf("选中的规则里有 %d 条处于真实模式且当前是停用状态,启用后会立即开始扣费、阻断与累计封号,请确认后重试", len(pending)),
				gin.H{"enforce_rules": pending})
			return
		}
	}

	now := common.GetTimestamp()
	operatorId := c.GetInt("id")
	res := runRuleBatch(gdb, ids, func(row *Rule) (string, string, string) {
		return applyBatchEnabled(gdb, row, enabled, req.AckEnforce, operatorId, now)
	})

	afterRuleBatch(c, actionBatchSetEnabled, res, gin.H{"ids": ids, "enabled": enabled})
	respond(c, res)
}

// applyBatchEnabled 对单条规则执行这次批量启停。
//
// 复用单条启停的 setRuleEnabled,而不是在这里另写一条 UPDATE:它带着这条路径上
// 三件必须做的事(启用前先编译一次、CAS 带旧值、只写三列),批量分叉出去写一份
// 迟早会漏掉其中一件 —— 而漏掉编译闸的表现是"批量启用报成功、线上零命中"。
// 代价是这一行被读了两次(runRuleBatch 一次拿名字,setRuleEnabled 一次做 CAS)。
func applyBatchEnabled(gdb *gorm.DB, row *Rule, enabled, ackEnforce bool,
	operatorId int, now int64) (string, string, string) {
	// 兜底闸:预检之后、写入之前被别人切成 enforce 的那一条,不该借着这次
	// 已经确认过的批次溜进真实执行。ack 过了就照做 —— 那正是 ack 的含义。
	if enabled && !ackEnforce && row.Mode == ModeEnforce && !row.Enabled {
		return batchItemFailed, batchCodeEnforceAck,
			"这条规则刚被改成真实模式,本次批量没有确认过它;请刷新列表后重新确认"
	}
	_, changed, err := setRuleEnabled(gdb, row.Id, enabled, operatorId, now)
	switch {
	case errors.Is(err, errRuleWontCompile):
		return batchItemFailed, batchCodeWontCompile,
			"这条规则无法编译,启用后不会命中任何请求;请先修正它的模式串"
	case errors.Is(err, gorm.ErrRecordNotFound):
		return batchItemFailed, batchCodeNotFound, "规则不存在,可能已被其他管理员删除"
	case err != nil:
		common.SysError("qianye/violation: 批量启停失败: " + err.Error())
		return batchItemFailed, batchCodeDBError, "写入失败,请查看后端日志"
	case !changed:
		// 库里本来就是目标状态(重复提交,或别人抢先改成了同一个值)。
		return batchItemSkipped, batchCodeNoChange, ""
	}
	return batchItemOK, "", ""
}

// pendingEnforceRules 找出选中集里"当前停用 + 真实模式"的规则 —— 也就是这次批量
// 启用真正会送进真实执行的那些。
//
// 刻意只算这一档,而不是"所有 enforce 规则":一条已经启用的 enforce 规则再点一次
// 启用是彻底的空操作,把它算进确认框里会让数字虚高,而虚高的警告数字会训练人
// 闭着眼睛点确认。
func pendingEnforceRules(gdb *gorm.DB, ids []int64) ([]gin.H, error) {
	var rows []Rule
	if err := gdb.Where("id IN ?", ids).
		Where("mode = ?", ModeEnforce).Where("enabled = ?", false).
		Order("id asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"id": row.Id, "name": row.Name, "action": row.Action})
	}
	return out, nil
}

// ───────────────────────── 批量设置作用分组 ─────────────────────────

// 批量作用分组的三种写法。
//
// # 为什么覆盖与追加都要有
//
// 它们服务的是两个真实且互不替代的动作:
//
//	replace  「这一批规则的作用分组统一改成这几个」—— 治理一批配歪了的规则
//	append   「新上线一个模型分组,把它并进这一批规则的作用域」—— 增量,最高频
//	remove    append 的逆操作。没有它,一次追加错了就只能逐条打开编辑抽屉去删
//
// 只做其中一种都会把另一种逼成"20 次手工编辑"。所以三种都做,但**必须在界面上
// 说清楚现在是哪一种** —— 让人猜"批量设置分组"到底是覆盖还是追加,正是本仓反复
// 出问题的形状。
const (
	batchScopeReplace = "replace"
	batchScopeAppend  = "append"
	batchScopeRemove  = "remove"
)

// batchGroupScopeReq 是批量设置作用分组的入参。
//
// Mode(group_scope_mode)是**必填**,三种写法都要。理由见文件头第 1 条:
// 同一串分组名在 include 与 exclude 下含义完全相反,不明说方向的批量追加
// 会让人以为自己多防了一个分组,实际是多豁免了一个。
type batchGroupScopeReq struct {
	Ids    []int64  `json:"ids"`
	Op     string   `json:"op"`
	Groups []string `json:"groups"`
	Mode   string   `json:"group_scope_mode"`
}

// groupScopeMaxRunes 取自 ruleVarcharLimits,不另抄一个字面量。
//
// 两份长度上限一旦漂移,批量就会写进一条数据库拒绝的行(列被改窄)或拦下一条
// 数据库接受的行(列被改宽)—— 那正是 ruleVarcharLimits 这张表存在的理由。
var groupScopeMaxRunes = func() int {
	for _, lim := range ruleVarcharLimits {
		if lim.Field == "GroupScope" {
			return lim.Max
		}
	}
	panic("qianye/violation: ruleVarcharLimits 缺少 GroupScope,批量作用分组失去长度上限")
}()

// maxGroupNameRunes 是单个分组名的长度上限。
//
// 主库与扩展库里以分组名为键的列都是 varchar(64)。挡在这里而不是等 group_scope
// 整串超长才报错:后者给出的错误是"分组作用域过长",而真正的问题是某一格里
// 粘进了一整段文本。
const maxGroupNameRunes = 64

// adminBatchSetRuleGroupScope 批量设置规则的作用分组(模型分组维度)。
//
// 请求体:
//
//	{"ids":[1,2], "op":"append", "groups":["vip"], "group_scope_mode":"include"}
//
// # 覆盖 / 追加 / 移除的确切语义
//
//	replace  规则的作用域整串换成 groups,方向换成 group_scope_mode。
//	         groups 为空 = 清空作用域 = **对全部分组生效**(方向随之折回 include)。
//	         这是一次放宽,前端必须单独说明。
//	append   把 groups 里还不在名单上的追加到末尾,方向写成 group_scope_mode。
//	remove   把 groups 从名单里摘掉,方向写成 group_scope_mode。
//
// # append / remove 遇到方向不一致的规则:拒做,不翻向
//
// 给一条 exclude 规则追加 "vip",在 include 语义下是"多防一个分组",在它自己的
// exclude 语义下却是"多豁免一个分组"—— 两者结果相反。任何一种自动处理都是替
// 操作者做了一个他没做过的决定:翻成 include 会把这条规则原有的豁免名单整个变成
// 白名单(杀伤面最大的一种误操作),照 exclude 追加则与他在界面上读到的说明不符。
// 所以这两种写法一律拒做并如实上报,由操作者决定是分两批做,还是用 replace 明确
// 统一方向。replace 不受此限 —— "覆盖"这个词本身就包含了方向。
//
// 空作用域的规则(名单为空 = 对全部分组生效)没有方向可言(空名单时 mode 恒为
// include,见 ruleUpsertReq.apply),所以 append 到它上面永远合法,方向取请求值。
func adminBatchSetRuleGroupScope(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	var req batchGroupScopeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	ids, err := normalizeRuleIds(req.Ids)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	op := strings.ToLower(strings.TrimSpace(req.Op))
	switch op {
	case batchScopeReplace, batchScopeAppend, batchScopeRemove:
	default:
		badRequest(c, fmt.Sprintf("op 取值非法: %q(只能是 %q / %q / %q)",
			req.Op, batchScopeReplace, batchScopeAppend, batchScopeRemove))
		return
	}
	wantMode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch wantMode {
	case GroupScopeInclude, GroupScopeExclude:
	default:
		// 刻意不给默认值。默认成 include 意味着一次漏传字段的调用会把一批
		// exclude 规则的豁免名单当作白名单来编辑,而那是本模块杀伤面最大的误操作。
		badRequest(c, fmt.Sprintf("group_scope_mode 必填,且只能是 %q 或 %q",
			GroupScopeInclude, GroupScopeExclude))
		return
	}
	groups, err := normalizeBatchGroups(req.Groups)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if len(groups) == 0 && op != batchScopeReplace {
		badRequest(c, "请至少填写一个分组名")
		return
	}
	gdb := ctxDB(c)
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	now := common.GetTimestamp()
	operatorId := c.GetInt("id")
	// 被覆盖掉的旧作用域**在别处不存在第二份**。规则行会被原地改写,而单条编辑接口的
	// 审计快照里本来就没有 group_scope 这一列 —— 一次 replace 之后,"这 20 条规则原来
	// 各自对哪些分组生效"就再也重建不出来了。所以这里把改动前的作用域收下来。
	prevScopes := make(gin.H, len(ids))
	res := runRuleBatch(gdb, ids, func(row *Rule) (string, string, string) {
		prev := effectiveScopeMode(row.GroupScopeMode) + ":" + row.GroupScope
		outcome, code, detail := applyBatchGroupScope(gdb, row, op, groups, wantMode, operatorId, now)
		if outcome == batchItemOK {
			prevScopes[strconv.FormatInt(row.Id, 10)] = prev
		}
		return outcome, code, detail
	})

	before := gin.H{"ids": ids, "op": op, "groups": groups, "group_scope_mode": wantMode}
	// 快照列被 audit.Truncate 按字节硬切,切断之后的文本不再是合法 JSON,管理端的
	// 快照渲染会整个回落成一行裸文本 —— 那样连 op 与 groups 都读不出来了。所以宁可
	// 在**明知装不下**的时候整块换成一句可读的说明,也不让它把整条快照拖成乱码。
	// 真装不下只可能是"满批 200 条、每条都挂着一长串分组名",而那一批的逐条明细
	// 仍然原样回给了操作者(响应体里的 items)。
	if snap := common.MapToJsonStr(prevScopes); len(snap) > scopeSnapshotBudget {
		before["before_scopes_omitted"] = len(prevScopes)
	} else if len(prevScopes) > 0 {
		before["before_scopes"] = prevScopes
	}
	afterRuleBatch(c, actionBatchSetGroupScope, res, before)
	respond(c, res)
}

// scopeSnapshotBudget 是"改动前作用域"这一块在 BeforeSnap 里能占的字节上限。
//
// 取 2048 而不是 audit 的 4096:同一个快照里还要装下 id 全集、op、groups 与方向,
// 而那几项才是这条审计的骨架 —— 骨架被挤掉的代价比少一块 before_scopes 大得多。
const scopeSnapshotBudget = 2048

// applyBatchGroupScope 对单条规则执行这次批量作用分组变更。
func applyBatchGroupScope(gdb *gorm.DB, row *Rule, op string, groups []string,
	wantMode string, operatorId int, now int64) (string, string, string) {
	next, nextMode, outcome, code, detail := planGroupScope(row, op, groups, wantMode)
	if outcome != batchItemOK {
		return outcome, code, detail
	}
	if n := utf8.RuneCountInString(next); n > groupScopeMaxRunes {
		return batchItemFailed, batchCodeTooLong,
			fmt.Sprintf("分组作用域过长(%d 字,上限 %d 字)", n, groupScopeMaxRunes)
	}
	// 只写这四列。Save(row) 会把 pattern / mode / action 一起写回去,而 row 是我们刚
	// 从库里读的、期间可能已被别人改过 —— 那就成了一次静默回滚,而回滚的正是决定谁被
	// 扣钱、谁被封号的那几列(与 setRuleEnabled 同一条取舍)。
	//
	// CAS 带上读到的旧作用域:两个管理员同时批量改同一批规则时,后到的那次
	// RowsAffected=0,如实报"已被改过",而不是把别人刚写的东西盖掉。
	upd := gdb.Model(&Rule{}).
		Where("id = ? AND group_scope = ? AND group_scope_mode = ?",
			row.Id, row.GroupScope, row.GroupScopeMode).
		Updates(map[string]any{
			"group_scope":      next,
			"group_scope_mode": nextMode,
			"updated_at":       now,
			"updated_by":       operatorId,
		})
	if upd.Error != nil {
		db.MarkFailure(upd.Error)
		common.SysError("qianye/violation: 批量设置作用分组失败: " + upd.Error.Error())
		return batchItemFailed, batchCodeDBError, "写入失败,请查看后端日志"
	}
	if upd.RowsAffected == 0 {
		return batchItemFailed, batchCodeStale,
			"这条规则的作用分组刚被其他管理员改过,本次没有覆盖它;请刷新列表后重试"
	}
	return batchItemOK, "", ""
}

// planGroupScope 算出这条规则的新作用域与新方向。
//
// 返回 (scope, mode, outcome, code, detail):outcome != batchItemOK 时前三个值无意义。
// 拆出来是因为它是这一批里唯一的业务判断,而且是表驱动测试的直接对象 ——
// "覆盖 vs 追加、方向不一致、清空"这几条边全在这里。
func planGroupScope(row *Rule, op string, groups []string, wantMode string) (
	scope string, mode string, outcome string, code string, detail string) {
	current := splitList(row.GroupScope)
	// 空名单没有方向可言:"空黑名单"与"空白名单"都表示"对全部分组生效"
	// (ruleUpsertReq.apply 把空名单的方向强制折回 include)。所以方向一致性
	// 只对非空名单成立。
	if len(current) > 0 && effectiveScopeMode(row.GroupScopeMode) != wantMode {
		if op != batchScopeReplace {
			return "", "", batchItemFailed, batchCodeDirectionMismatch,
				fmt.Sprintf("这条规则当前是「%s」名单,与本次批量的「%s」方向不一致;"+
					"追加/移除不会替你翻转方向 —— 请分两批处理,或用「覆盖」明确统一方向",
					scopeModeLabel(effectiveScopeMode(row.GroupScopeMode)), scopeModeLabel(wantMode))
		}
	}

	var next []string
	switch op {
	case batchScopeReplace:
		next = append(make([]string, 0, len(groups)), groups...)
	case batchScopeAppend:
		next = append(make([]string, 0, len(current)+len(groups)), current...)
		have := make(map[string]struct{}, len(current))
		for _, name := range current {
			have[groupname.Effective(name)] = struct{}{}
		}
		for _, name := range groups {
			key := groupname.Effective(name)
			if _, dup := have[key]; dup {
				continue
			}
			have[key] = struct{}{}
			next = append(next, name)
		}
	default: // batchScopeRemove
		drop := make(map[string]struct{}, len(groups))
		for _, name := range groups {
			drop[groupname.Effective(name)] = struct{}{}
		}
		next = make([]string, 0, len(current))
		for _, name := range current {
			if _, hit := drop[groupname.Effective(name)]; hit {
				continue
			}
			next = append(next, name)
		}
	}

	nextMode := wantMode
	if len(next) == 0 {
		// 与 ruleUpsertReq.apply 同口径:名单为空时方向恒为 include。留两个等价
		// 状态只会让界面上出现一个看得见、却什么都不改变的开关。
		nextMode = GroupScopeInclude
	}
	nextScope := strings.Join(next, ",")

	// "结果与现状等价"判定按**归一后的名单**比,而不是按原始字符串:库里存的可能是
	// "vip, svip"(带空格),照字符串比会把一次纯粹的空白重排报成"改动过",于是
	// 一批什么都没变的规则会刷新 updated_at、bump 规则版本、让所有节点白拉一遍全表。
	if effectiveScopeMode(row.GroupScopeMode) == nextMode && sameGroupList(current, next) {
		return "", "", batchItemSkipped, batchCodeNoChange, ""
	}
	return nextScope, nextMode, batchItemOK, "", ""
}

// effectiveScopeMode 把历史行的空串方向读成 include。
//
// 空串是这一列出现之前的唯一语义(滚动升级期间旧节点写下的行、DBA 手工插的行),
// 回落到 include 不改变任何既有规则的行为 —— 与 model.go 的声明一致。
func effectiveScopeMode(mode string) string {
	if mode == GroupScopeExclude {
		return GroupScopeExclude
	}
	return GroupScopeInclude
}

func scopeModeLabel(mode string) string {
	if mode == GroupScopeExclude {
		return "豁免(exclude)"
	}
	return "仅对这些分组生效(include)"
}

// sameGroupList 按判定口径(groupname.Effective)逐项比两份名单。
//
// 顺序敏感:名单顺序在 compiledRule 里不影响判定,但它是管理员在界面上读到的
// 东西,一次把 "vip,svip" 重排成 "svip,vip" 的写入对他来说就是一次改动。
func sameGroupList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if groupname.Effective(a[i]) != groupname.Effective(b[i]) {
			return false
		}
	}
	return true
}

// normalizeBatchGroups 校验并去重请求里的分组名。
//
// 三件事:
//
//  1. **名字里不许出现分隔符。** group_scope 是一串逗号/换行分隔的名字,一个带逗号
//     的"名字"存进去就是两个分组,而界面上还显示成一个 —— 保存成功、看着正常、
//     判定却挂在两个都不存在的名字上。
//  2. **按判定口径去重。** compiledRule 用 groupname.Effective 建索引,"VIP" 与
//     "vip" 是同一个分组;不折叠去重的话名单里会留下两份等价项,白占长度上限。
//  3. **保留操作者写下的原始大小写。** 判定不看大小写,但界面看 —— 把他填的 "VIP"
//     改写成 "vip" 是一次没有人要求过的改写。
func normalizeBatchGroups(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if strings.ContainsAny(name, ",\n\r") {
			return nil, fmt.Errorf("分组名不能包含逗号或换行: %q", name)
		}
		if n := utf8.RuneCountInString(name); n > maxGroupNameRunes {
			return nil, fmt.Errorf("分组名过长(%d 字,上限 %d 字): %q", n, maxGroupNameRunes, name)
		}
		key := groupname.Effective(name)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

// ───────────────────────── 收尾:审计 + 快照刷新 ─────────────────────────

// afterRuleBatch 统一处理批量写入后的三件事:版本号 +1、本节点重载、写审计。
//
// # 与单条路径的两处不同
//
//  1. **审计无条件写。** 单条启停在"什么都没发生"时刻意不写审计(重复点击不该在
//     审计里留下一条"改过")。批量不一样:一次"选 20 条按启用"是一个明确的操作意图,
//     而"20 条全都本来就是启用的"恰恰常常意味着选错了范围 —— 那正是事后要查的东西。
//     一次批量只产生一行审计,不存在单条路径的刷屏问题。
//  2. **只在真的写过库时才 bump + reload。** 一次全跳过的批量不该让所有节点白拉
//     一遍全表规则。
//
// # 快照里放什么
//
//	Before  请求本身(选了哪些 id、目标状态 / 写法 / 分组名 / 方向)
//	After   分档计数 + **按结果码分组的 id** + 跳过的 id
//
// 不放逐条明细:审计的两个快照列各自被 audit.Truncate 按 SnapshotMaxBytes(默认
// 4096 字节)硬切,而逐条明细带着规则名与中文 detail,满批会在第 30 条左右被切断。
// 切断的后果不只是"少看几行"—— 被切断的文本不再是合法 JSON,管理端的快照渲染会
// 整个回落成一行裸文本。逐条明细仍然原样回给操作者(响应体里的 items)。
// ok 那一档不记:Before 有 id 全集,减去这里的 failed 与 skipped 就是它,而这次
// 减法是精确的(runRuleBatch 的恒等式保证三档互斥且合起来等于全集)。
func afterRuleBatch(c *gin.Context, action string, res batchRuleResult, before gin.H) {
	if res.Succeeded > 0 {
		bumpRuleVersion()
		if err := reload(true); err != nil {
			common.SysError("qianye/violation: 批量规则变更后重载失败: " + err.Error())
		}
	}
	result := qymodel.ResultOK
	reason := fmt.Sprintf("共 %d 条:生效 %d 条,无变化 %d 条,失败 %d 条",
		res.Total, res.Succeeded, res.Skipped, res.Failed)
	if res.Failed > 0 && res.Succeeded == 0 {
		result = qymodel.ResultFail
	}
	failedByCode := map[string][]int64{}
	skipped := make([]int64, 0, res.Skipped)
	for _, item := range res.Items {
		switch item.Outcome {
		case batchItemFailed:
			code := item.Code
			if code == "" {
				code = batchCodeDBError
			}
			failedByCode[code] = append(failedByCode[code], item.Id)
		case batchItemSkipped:
			skipped = append(skipped, item.Id)
		}
	}
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryViolation,
		Action:      action,
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		BeforeSnap:  common.MapToJsonStr(before),
		AfterSnap: common.MapToJsonStr(map[string]any{
			"total": res.Total, "succeeded": res.Succeeded,
			"skipped": res.Skipped, "failed": res.Failed,
			"failed_ids_by_code": failedByCode,
			"skipped_ids":        skipped,
		}),
	})
}
