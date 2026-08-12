package violation

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// category_suggest.go —— 内置类型的建议阈值:预览与一键落库。
//
// # 为什么建议值不能进种子
//
// ensureSeedCategories 出厂 Threshold 恒为 0,理由写在那里:种子一旦带值,
// 升级上来的站点会在部署完成的那一秒开始按一套**没有人设定过**的线封人。
// 但"全是 0"的另一面是这套功能在界面上等于不存在 —— 六个类型摆在那里,
// 没有一个能回答"到多少次封号"。
//
// 这个文件是这两难的出口:建议值只活在代码里,要落库必须有人**看着影响面按下确认**。
// 于是「我们替站点想过合理的线」与「封号线只能由站点自己拍板」同时成立。
//
// # 三条不可越过的线
//
//  1. **只补空,不覆盖。** 只有当前阈值为 0(state == unset)的类型才会被写。
//     管理员手填过的 10 次绝不会被"建议的 3 次"顶掉 —— 那是一次静默收紧,
//     而收紧方向的错误会直接把人封掉。
//  2. **阈值开关关着的不写。** 写进去也不生效(categoryReached 里 Enabled=false
//     等价于 Threshold=0),而界面会显示一个看起来在生效的数字。
//  3. **确认前必须先出数。** 没有 confirm 一律 409,连同"这一改会让多少人
//     立刻处在越线状态"一起回去。

// suggestedThreshold 是一个内置类型的建议线。
//
// Why 会原样出现在管理端的确认弹窗里:一个没有理由的数字,管理员只能选择
// 全盘照抄或全盘不用,而这两种都不是"按自己站点的情况拍板"。
type suggestedThreshold struct {
	Threshold   int
	WindowHours int
	Why         string
}

// suggestedCategoryThresholds 是全部内置类型的建议线,键与 seedCategories 同源。
//
// **每加一条种子类型,这里就必须跟着加一行**,否则那一类会永远停在
// "仅记录、不计封号门槛"—— 它在管理端与用户端都长得像一个已经配好的类型,
// 只是那条线不存在。TestEverySeededCategoryHasASuggestedLine 钉的就是这件事,
// 它是实测抓到的:上一轮补进九条上游合规类型时,这张表没跟着长,
// 于是十四个公示类型里有九个对"到多少次封号"给不出答案。
//
// # 定线的两个变量:意图有多明确、误伤面有多大
//
// 越是"单次命中就能确认恶意"的类型,线越低;越是"正常使用也会撞上"的类型,
// 线越高。判据来自 seedCategories 里各自的 Desc(那是这一类的匹配口径)。
//
// ── 前五类:判据是本仓自己的规则(词表 / 频率 / 上游状态码)──
//
//   - jailbreak / reverse 的判据是文本里出现了明确的越狱人格或"复述系统提示词"
//     这类要求。正常使用几乎撞不上,一次就能确认意图,给两次容错即可。
//   - pressure 的判据是控制 token 与角色标签,而"粘贴一段带 role: 标记的聊天记录"
//     是正常用法,误伤面明显更大,线要比前两类宽。
//   - distill 的判据是请求频率,压测、批处理、脚本重试都会撞上,误伤面最大。
//   - upstream 的判据是**上游的结论**,里面混着大量与用户意图无关的误拒
//     (上游策略抖动、模型侧过敏)。它更适合用来挡"持续踩线"的账号,而不是
//     "偶尔被拒"的账号,所以线最高。
//
// ── 后九类:判据是**审核模型的判断**,所以定线的口径不一样 ──
//
// 这九类没有确定性的词表可依,命中来自 AI 审核的一次分类。分类会错,而错的
// 方向不对称:把正常请求判成违规,代价由用户承担;漏判一次,代价由站点承担。
// 于是线跟着"这一类判错一次有多贵"走,而不是跟着"这一类有多严重"走:
//
//   - minor_safety 是唯一一类站点侧几乎没有可接受误放行的内容,线最紧(2),
//     仍然留一次容错 —— 单次即封会让一次模型抖动直接封掉一个账号。
//   - cyber_attack / violent_extremism / illegal_goods / privacy_doxxing /
//     hate_harassment 的判据都是"索要可实施的方法或对具体个人的攻击",
//     与安全研究、历史讨论、公开信息检索的边界已经写进各自的 AIGuidance,
//     与 jailbreak 同一档(3),给两次容错。
//   - sexual 是站点尺度差异最大的一类:创作类站点会有大量落在边缘的正常请求,
//     线放宽到 5,由站点自己按需收紧。
//   - fraud_spam 的判据里混着正常营销文案与反诈科普,误伤面接近 distill,同为 5。
//   - self_harm **刻意给最宽的线**。它的误判人群是正在求助的人,把他们按违规
//     处置是本模块能造成的最坏后果 —— 比漏判一次更坏。判据已经明确排除求助
//     场景(见 seedCategories 里那一条),剩下的模型误差用最宽的线兜。
//
// 兜底类型 uncategorized **刻意不给建议**:它是"还没归类"的落点而不是一个业务
// 类型,给它一条线等于给"任何一条还没被归类的规则"一条线 —— 而那批规则是什么
// 没有人知道。AI 审核判了违规却给不出类型时,那一票也落在这里,于是那条线还会
// 把"模型没说清楚"直接算成一次违规。它的命中照常计入账号总量线。
var suggestedCategoryThresholds = map[string]suggestedThreshold{
	CatJailbreak: {3, 24, "判据是明确的越狱人格与关闭安全过滤的要求,正常使用几乎撞不上,单次即可确认意图;给两次容错后收口。"},
	CatReverse:   {3, 24, "判据是索要系统提示词与初始设定,目标是服务方资产,与 jailbreak 同级;给两次容错后收口。"},
	CatPressure:  {5, 24, "判据是控制 token 与伪造角色标签,而粘贴带 role 标记的聊天记录是正常用法,误伤面比前两类大,线要放宽。"},
	CatDistill:   {5, 24, "判据是请求频率而非文本,压测、批处理与脚本重试都会撞上,误伤面最大之一;线放宽,靠连续多次才收口。"},
	CatUpstream:  {10, 24, "判据是上游的拒绝结论,混着大量与用户意图无关的误拒(上游策略抖动、模型侧过敏)。它用来挡持续踩线的账号,不是偶尔被拒的账号。"},

	CatMinorSafety:    {2, 24, "站点侧几乎没有可接受的误放行,是这批里唯一收得比 jailbreak 更紧的一类;仍留一次容错,避免一次模型抖动就封掉账号。"},
	CatCyberAttack:    {3, 24, "判据是索要可实施的攻击、入侵与破解方法,与安全研究、自有软件调试的边界已写进判定说明;与 jailbreak 同档,给两次容错。"},
	CatViolentExtreme: {3, 24, "判据是极端暴力与武器制造的可实施方法,与历史讨论、虚构创作的边界已写进判定说明;与 jailbreak 同档。"},
	CatIllegalGoods:   {3, 24, "判据是管制物品与非法服务的获取方法,与药理化学科普的边界已写进判定说明;与 jailbreak 同档。"},
	CatPrivacyDoxxing: {3, 24, "判据是对具体自然人的起底与监控部署,检索公开的公司与公众人物信息不算;意图明确,与 jailbreak 同档。"},
	CatHateHarassment: {3, 24, "判据是基于受保护特征的贬损与针对个人的定向骚扰,讨论歧视现象本身不算;与 jailbreak 同档。"},
	CatSexual:         {5, 24, "站点尺度差异最大的一类,创作类站点会有大量落在边缘的正常请求;线先放宽到与 distill 同档,由站点自己按需收紧。"},
	CatFraudSpam:      {5, 24, "判据里混着正常营销文案与反诈科普的边缘样本,误伤面接近 distill;线放宽,靠反复命中才收口。"},
	CatSelfHarm:       {10, 24, "刻意给最宽的线:这一类的误判人群是正在求助的人,把他们按违规处置比漏判一次更坏。判定说明已排除求助场景,剩下的模型误差用最宽的线兜。"},
}

// 类型阈值的三态。0 与"停用"在判定上等价(都不出线),但在**界面上必须分开** ——
// 一个从没配过的类型显示成 0,看起来像"0 次就封",而管理员看不出这一类还没配。
const (
	// thresholdUnset:从来没配过线。它的命中照常计数、照常计入账号总量线。
	thresholdUnset = "unset"
	// thresholdDisabled:配过线但阈值开关关着。数字还在,当下不生效。
	thresholdDisabled = "disabled"
	// thresholdActive:线正在生效。
	thresholdActive = "active"
)

// categoryThresholdState 把一个类型的阈值折成三态。
//
// 判定侧只认 categoryReached(Enabled && Threshold > 0),这里多出来的一档
// 纯粹是给界面用的:unset 与 disabled 在封号判定上完全一样,但对着管理员时
// 它们是两句不同的话 —— 前者是"你还没配",后者是"你配了但关着"。
func categoryThresholdState(cat Category) string {
	if cat.Threshold <= 0 {
		return thresholdUnset
	}
	if !cat.Enabled {
		return thresholdDisabled
	}
	return thresholdActive
}

// suggestionView 是一条建议在报文里的形状。
type suggestionView struct {
	Id   int64  `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`

	CurrentThreshold   int    `json:"current_threshold"`
	CurrentWindowHours int    `json:"current_window_hours"`
	CurrentEnabled     bool   `json:"current_enabled"`
	State              string `json:"state"`

	SuggestedThreshold   int    `json:"suggested_threshold"`
	SuggestedWindowHours int    `json:"suggested_window_hours"`
	Why                  string `json:"why"`

	// Applicable 为 false 时 SkipReason 必有值。两者一起给,是为了让界面能把
	// "为什么这一行是灰的"直接显示出来 —— 一个只灰不给理由的行会被当成 bug。
	Applicable bool   `json:"applicable"`
	SkipReason string `json:"skip_reason"`

	// Impact 是"按建议线,这一类现在有多少存量账号已经处在越线状态"。
	// 语义与 categoryImpact 逐字一致:**不是**"这次应用会封掉几个人"。
	Impact categoryImpact `json:"impact"`
}

// suggestionPreview 是预览与 409 回执共用的整体报文。
type suggestionPreview struct {
	Items []suggestionView `json:"items"`
	// ApplicableCount 是这次点下去真的会被写的类型数。
	ApplicableCount int `json:"applicable_count"`
	// AffectedUsers 是**去重后**的账号数。逐类相加会把同时越两类线的人算两次,
	// 而管理员按下确认之前读的就是这个数 —— 多算等于虚报影响面。
	AffectedUsers int  `json:"affected_users"`
	Capped        bool `json:"capped"`

	// AccountAction 是这些人越线之后会被怎么处置。它来自**兜底策略档** ——
	// 类型线只决定"几次",动作一律由用户所在分组的策略档决定(见 anyReached
	// 与 resolveBanClaim)。不带上它,确认弹窗就只能说"会触发处置",
	// 而站点当前的兜底动作可能正是 ban。
	AccountAction    string `json:"account_action"`
	AccountThreshold int    `json:"account_threshold"`

	// ThresholdSemantics 与列表、用户端公示同一个源头,理由见 adminListCategories。
	ThresholdSemantics string `json:"threshold_semantics"`
}

// buildSuggestionPreview 把库里的类型表 + 代码里的建议表,折成一份可以直接渲染的预览。
func buildSuggestionPreview(ctx context.Context, gdb *gorm.DB) (suggestionPreview, error) {
	out := suggestionPreview{
		Items:              make([]suggestionView, 0, len(suggestedCategoryThresholds)),
		ThresholdSemantics: thresholdSemanticsAnyLine,
	}
	if gdb == nil {
		return out, db.ErrNotReady
	}
	policy := banPolicies().fallback
	out.AccountAction = policy.Action
	out.AccountThreshold = policy.Threshold

	var rows []Category
	if err := gdb.WithContext(ctx).Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out, err
	}

	applicable := make([]Category, 0, len(rows))
	for _, cat := range rows {
		sug, ok := suggestedCategoryThresholds[cat.Key]
		if !ok {
			continue
		}
		state := categoryThresholdState(cat)
		view := suggestionView{
			Id: cat.Id, Key: cat.Key, Name: cat.Name,
			CurrentThreshold: cat.Threshold, CurrentWindowHours: cat.WindowHours,
			CurrentEnabled: cat.Enabled, State: state,
			SuggestedThreshold: sug.Threshold, SuggestedWindowHours: sug.WindowHours,
			Why: sug.Why,
		}
		// 只补空:手填过的线绝不被建议值顶掉,那是一次静默收紧。
		switch {
		case state != thresholdUnset:
			view.SkipReason = fmt.Sprintf("这一类已经配过线(%d 次 / %d 小时),建议值不会覆盖已有配置",
				cat.Threshold, cat.WindowHours)
		case !cat.Enabled:
			view.SkipReason = "这一类的阈值开关当前是关闭的,写进去也不会生效,请先在编辑里打开"
		default:
			view.Applicable = true
			probe := cat
			probe.Threshold = sug.Threshold
			probe.WindowHours = sug.WindowHours
			probe.Enabled = true
			impact, err := countCategoryImpact(ctx, gdb, probe)
			if err != nil {
				return out, err
			}
			view.Impact = impact
			if impact.Capped {
				out.Capped = true
			}
			out.ApplicableCount++
			applicable = append(applicable, probe)
		}
		out.Items = append(out.Items, view)
	}

	affected, capped, err := countSuggestionAffectedUsers(ctx, gdb, applicable)
	if err != nil {
		return out, err
	}
	out.AffectedUsers = affected
	if capped {
		out.Capped = true
	}
	return out, nil
}

// countSuggestionAffectedUsers 数出**去重后**会立刻处在越线状态的账号数。
//
// 一次 OR 查询而不是逐类相加:同一个账号同时越过破限线与逆向线时,相加会把他
// 算两次,而这个数字正是管理员按下确认之前唯一会读的东西。虚报影响面的方向是
// "看起来更吓人",于是要么吓得不敢点(功能白做),要么发现虚报之后不再信任
// 这个数(下次直接点过去)—— 两种都比不给数更糟。
func countSuggestionAffectedUsers(ctx context.Context, gdb *gorm.DB, cats []Category) (int, bool, error) {
	if gdb == nil {
		return 0, false, db.ErrNotReady
	}
	if len(cats) == 0 {
		return 0, false, nil
	}
	now := common.GetTimestamp()
	parts := make([]string, 0, len(cats))
	args := make([]any, 0, len(cats)*3)
	for _, cat := range cats {
		windowHours := cat.WindowHours
		if windowHours <= 0 {
			windowHours = 24
		}
		parts = append(parts, "(category_id = ? AND hit_count >= ? AND window_start >= ?)")
		args = append(args, cat.Id, cat.Threshold, now-int64(windowHours)*3600)
	}
	// 列名全是普通标识符(category_id / hit_count / window_start / user_id),
	// 三家数据库都不需要方言引号 —— 这里不能出现 `group` / `key` 那类保留字。
	var ids []int
	if err := gdb.WithContext(ctx).Model(&CategoryCounter{}).
		Where(strings.Join(parts, " OR "), args...).
		Distinct("user_id").
		Limit(impactScanCap+1).
		Pluck("user_id", &ids).Error; err != nil {
		db.MarkFailure(err)
		return 0, false, err
	}
	if len(ids) > impactScanCap {
		return impactScanCap, true, nil
	}
	return len(ids), false, nil
}

// adminSuggestedThresholds 是"建议阈值"的只读预览。
//
// 与 adminCategoryImpact 同理:管理员必须在按下应用**之前**看到影响面。
// 一个只在应用之后才出现的数字,等于让人拿真实的封号线去探路。
func adminSuggestedThresholds(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	preview, err := buildSuggestionPreview(c.Request.Context(), gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	respond(c, preview)
}

// adminApplySuggestedThresholds 把建议线落库。**这个动作会真的改变谁会被封号。**
//
// # 为什么它不是一个静默的迁移
//
// 项目方要的是"六个类型都有一条线",而最省事的做法是把建议值写进种子 ——
// 那样升级上来的站点会在部署完成的那一秒按一套没人设定过的线开始封人。
// 这个接口把那件事变成一次**有人按过、有影响面、有审计**的动作:
// 没有 confirm 一律 409,连同去重后的越线账号数一起回去。
//
// # 保存本身不处置任何人
//
// 与 adminUpsertCategory 同口径:写的只是类型表。已经越线的账号会在各自
// **下一次违规命中**时才走 resolveBanClaim,按所在分组的策略档处置。
// 界面文案必须照这个语义写,否则"应用之后一个人都没被封"会被当成没生效。
func adminApplySuggestedThresholds(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}

	preview, err := buildSuggestionPreview(c.Request.Context(), gdb)
	if err != nil {
		internalError(c, err)
		return
	}
	if preview.ApplicableCount == 0 {
		badRequest(c, "没有可应用的类型:内置类型要么已经配过线,要么阈值开关是关闭的")
		return
	}
	if !req.Confirm {
		respondFailData(c, 409, "confirm_required",
			fmt.Sprintf("这次会给 %d 个还没配线的类型写入建议阈值,写完之后有 %d 个存量账号立刻处在越线状态"+
				"(应用本身不处置任何人,他们会在各自下一次违规命中时按所在分组的处置策略处置),请确认后重试",
				preview.ApplicableCount, preview.AffectedUsers),
			gin.H{"preview": preview})
		return
	}

	now := common.GetTimestamp()
	actor := c.GetInt("id")
	applied := make([]gin.H, 0, preview.ApplicableCount)
	for _, item := range preview.Items {
		if !item.Applicable {
			continue
		}
		// 快照必须取**整行**,不能拿 suggestionView 上那几列拼一个 Category 出来。
		// 拼出来的那一份里 published / is_fallback 恒为零值,于是审计会把一个
		// 正在公示的类型记成"未公示"—— 而事后要回答的问题正是"当时这一类对用户
		// 是可见的吗"。少几列的快照比没有快照更坏:它看起来是可信的。
		var before Category
		if err := gdb.Where("id = ?", item.Id).Take(&before).Error; err != nil {
			db.MarkFailure(err)
			writeCategoryAudit(c, "categories.apply_suggested", qymodel.ResultFail,
				Category{Id: item.Id, Key: item.Key, Name: item.Name}, Category{}, err)
			internalError(c, err)
			return
		}
		after := before
		after.Threshold = item.SuggestedThreshold
		after.WindowHours = item.SuggestedWindowHours

		// `threshold <= 0` 留在 WHERE 里是一道 CAS:预览与写入之间有人手填了一条线时
		// 这一行影响 0 条,建议值不会把它顶掉。没有它,"只补空"这条纪律只在
		// 单人操作时成立,而收紧方向的静默覆盖会直接把人封掉。
		res := gdb.Model(&Category{}).
			Where("id = ? AND threshold <= ?", item.Id, 0).
			Updates(map[string]any{
				"threshold":    item.SuggestedThreshold,
				"window_hours": item.SuggestedWindowHours,
				"updated_at":   now,
				"updated_by":   actor,
			})
		if res.Error != nil {
			db.MarkFailure(res.Error)
			writeCategoryAudit(c, "categories.apply_suggested", qymodel.ResultFail, before, after, res.Error)
			internalError(c, res.Error)
			return
		}
		if res.RowsAffected == 0 {
			// 被 CAS 挡下:别人刚配过线。跳过并如实回报,绝不重试成覆盖。
			continue
		}
		writeCategoryAudit(c, "categories.apply_suggested", qymodel.ResultOK, before, after, nil)
		applied = append(applied, gin.H{
			"id": item.Id, "key": item.Key, "name": item.Name,
			"threshold": item.SuggestedThreshold, "window_hours": item.SuggestedWindowHours,
		})
	}
	afterCategoryChange()
	respond(c, gin.H{
		"applied":        applied,
		"applied_count":  len(applied),
		"affected_users": preview.AffectedUsers,
		// 应用只写类型表。这一位由后端下发而不是前端写死:它是"为什么点完没人被封"
		// 的唯一答案,而前端各写一份的结果是两处文案早晚不一致。
		"acts_immediately": false,
	})
}
