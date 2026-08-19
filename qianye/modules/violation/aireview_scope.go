package violation

import (
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"gorm.io/gorm"
)

// AI 审核的作用域与分档抽样。
//
// ═════════════════════ 项目方要的是什么 ═════════════════════
//
// 原话:「AI内容审核要可以监控分组,因为AI审核基本都是后审核,秋后算账的」。
// 拆开是两件事:
//
//  1. **只盯某几个分组**。全站一个抽样率表达不了"自助注册的分组重点看,
//     内部对接的分组根本不用看"—— 而后者往往占了全站绝大多数流量,
//     等于把审核预算全花在最不需要审的请求上。
//  2. **不同分组不同力度**。高风险分组 50%、其余 1%,比全站一个数有用得多。
//     一个数字只能在"贵得离谱"与"抽不到任何东西"之间二选一。
//
// ═════════════════════ 为什么是多条策略而不是配置上的几个字段 ═════════════════════
//
// 把 group_scope / model_scope 直接加在 AISetting(单例配置)上是更小的改动,
// 但它**表达不了分档**:一份作用域只能配一个抽样率,于是"高风险 50%、其余 1%"
// 仍然无解。而分档正是项目方那句话里最实的那一半。
//
// 所以是一张策略表:每条策略 = 一个作用域 + 两个时机各自的抽样率。
// 热路径按 priority 升序取**第一条**匹配的策略(与规则表同一套心智:
// 优先级最高的一条说了算,不叠加)。
//
// ═════════════════════ 没有兜底档 ═════════════════════
//
// 项目方的原话:「移除兜底策略,策略要手动添加,绑定分组。才能生效。
// 全局审核抽样率移除一下」。所以语义是一句话:
//
//	**没有任何作用域策略命中 ⇒ 不审核。**
//
// 曾经在 AISetting 上的那一列全局抽样率(sample_rate_bps)已经删掉,连同
// scopeFor 末尾那条回落。理由不是"少一个字段更干净",是它把这一页最重要的
// 那个问题变得答不出来:界面上一条策略都没有、看起来什么都没监控,而线上
// 全站 5% 的请求内容正在被发往第三方 —— 一个没有任何入口能看见的成本与
// 隐私事实。策略表反过来:表上写着谁在被监控,表上没有的就是没有。
//
// 存量那一个值不会被静默丢弃,迁移把它转成一条策略行(见 migrate.go 的
// migrateAISampleRateToScope)。那条行**一律是停用的**:它的作用域覆盖全站,
// 而覆盖全站的策略现在不允许启用(见下一段)。值原样留在行上,补齐分组就能开。
//
// ═════════════════════ 启用中的策略必须绑定分组 ═════════════════════
//
// 项目方原话:「强制绑定分组,全站模型还是太高了一点」。一条覆盖全站的策略把
// 抽样率这唯一的成本闸门作用在**所有**用户身上,而它与一条只盯一个分组的策略
// 在列表上长得完全一样。所以 validateAIScope 拒绝启用两种写法:分组名单为空,
// 以及 exclude 名单(= 名单之外的全部分组,而且随着新分组的建立自动变宽)。
// 存量的这种行不会被改写,只会被标出来 —— 判据是 aiScopeGroupUnbound。

// AIScope 是一条 AI 审核作用域策略。
//
// # 与违规规则的作用域列逐项同名同义
//
// ModelScope / GroupScope / GroupScopeMode 三列的语法、大小写折叠、通配语义
// 全部来自 compileScope —— 与 qy_violation_rule 上那三列**共用同一段代码**。
// 运营在两个页面上看到的是同一个输入框,得到的必须是同一个结果。
//
// # 为什么抽样率是两个而不是一个
//
// 两个时机的代价完全不同:
//
//	转发前(pre)   同步,结论回来才继续。它直接加在被抽中请求的首字节延迟上。
//	转发后(async) 异步,本次请求一秒不等。它只花钱,不花用户的时间。
//
// 运营对这两件事的容忍度差一个数量级:后审核("秋后算账")可以对全站开到
// 10%,而转发前审核通常只敢对最可疑的那一两个分组开。共用一个数字等于逼人
// 按较严的那一侧配,于是后审核的覆盖率被前审核的延迟顾虑白白拖低。
type AIScope struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// Name 是给人看的档名(「自助注册分组」「内部对接·免审」)。
	// 必填:一张只有作用域表达式的策略表,三个月后没人说得出每条是干什么的。
	Name    string `json:"name" gorm:"type:varchar(64);not null;default:''"`
	Enabled bool   `json:"enabled" gorm:"not null;default:false"`

	// Priority 升序,第一条匹配的策略说了算(与规则表同一套心智)。
	// 不叠加:两条策略同时命中却各给一个抽样率时,"叠加"没有任何一种
	// 定义是运营预期的(取大?取小?相乘?),而取第一条是可解释的。
	Priority int `json:"priority" gorm:"not null;default:100;index:idx_qy_vais_pri,priority:1"`

	ModelScope     string `json:"model_scope" gorm:"type:varchar(2048);not null;default:''"`
	GroupScope     string `json:"group_scope" gorm:"type:varchar(1024);not null;default:''"`
	GroupScopeMode string `json:"group_scope_mode" gorm:"type:varchar(8);not null;default:'include'"`

	// PreSampleRateBps / AsyncSampleRateBps 是万分比(30% = 3000),0 = 该时机不审。
	//
	// 两个都填 0 是**有意义的配置**,不是"没配":它表达「这些分组一律不送审」,
	// 也就是免审名单。放在高优先级上,它比 exclude 名单更直白 ——
	// exclude 要反过来想一遍,而"这一档抽 0%"是字面意思。
	PreSampleRateBps   int `json:"pre_sample_rate_bps" gorm:"not null;default:0"`
	AsyncSampleRateBps int `json:"async_sample_rate_bps" gorm:"not null;default:0"`

	// Prompt 是**这一档自己的**审核提示词。空 = 用 AISetting.Prompt(它再空则用
	// defaultAIPrompt)。项目方的原话是「设置这个分组的AI审核提示词」。
	//
	// # 为什么一份全局提示词不够
	//
	// 作用域已经能表达"只盯自助注册分组",但送过去问的仍然是同一句话。而运营给
	// 不同分组开审核的**理由**本来就不同:自助注册分组要看的是批量套模型,
	// 内部对接分组要看的是有没有人拿它跑越权内容。用一份提示词同时问这两件事,
	// 只能写成一份把两边都稀释掉的通用文案 —— 那正是"开了审核但判不准"的来源。
	//
	// # 它只覆盖"判定说明",类型清单仍然自动生成
	//
	// 发出去的那一份由 renderAIPrompt 拼:这一列(或全局那一列)是**基底**,
	// 违规类型闭集永远由 qy_violation_category 现算并追加/替换占位符。
	// 所以作用域提示词**不需要、也不应该**手抄一份类型清单 —— 抄了就会在
	// 运营新建一个类型的第二天开始说谎(见 aireview_vocab.go 顶部)。
	//
	// # 与全局那一列的一个刻意差别:这里不做"逐字等于默认 → 存空"的折叠
	//
	// 全局那一列折叠是为了让站点跟随 defaultAIPrompt 的后续加固。这一列的空串
	// 语义是**"用全局那一份"**,而全局那一份可能是本站自定义的 —— 把一段逐字
	// 等于默认的作用域提示词折成空串,会把它悄悄换成"跟随全局自定义",
	// 与运营写下它时的意思完全相反。
	Prompt string `json:"prompt" gorm:"type:text"`

	// CategoryId 是「这一档的命中一律记为哪个违规类型」。0 = 不指定。
	//
	// # 优先级:它覆盖规则自己绑的类型,而 AI 回的类型永不直接决定记录类型
	//
	// 完整取舍写在 resolveCategoryOverride 上。一句话:它是运营点选过的确定值,
	// 而模型返回的 category 是一次自由输出 —— 把封号计数挂在后者上,
	// "这个用户在破限这一类上第几次"就成了一个不可复现的数。
	//
	// 0 时行为与这一列存在之前**逐字节相同**(记录类型仍来自规则),
	// 因此升级上来的站点什么都不用做。
	CategoryId int64 `json:"category_id" gorm:"not null;default:0"`

	// ChannelId 是「这一档送到哪个审核渠道」。0 = 不指定,走默认选取。
	//
	// # 「默认」到底是谁:加权随机,不是"第一个"
	//
	// 留空时 pickAIChannels 在**全部启用中的渠道**里按 AIChannel.Weight 做加权
	// 随机,最多试 maxAIAttempts 个。渠道表上没有 priority 这种东西 ——
	// 权重就是运营表达"主用哪个、备用哪个"的唯一方式。所以留空的准确含义是
	// 「按权重随机分流」,不是「用某一个固定渠道」,界面上必须这么写:
	// 把它说成"默认渠道"会让人以为存在一个确定的答案,而两个渠道各 50% 时
	// 那句话就是假的。
	//
	// # 为什么需要指定
	//
	// 作用域已经能表达"只盯自助注册分组",但送到哪里仍然是全站一份加权随机。
	// 而运营给不同分组开审核的**约束**本来就不同:内部对接分组的内容可能只
	// 允许发给自建的那个端点,自助注册分组用便宜的小模型就够。混在一个随机
	// 池里时,前者会以某个概率把内容发给云端厂商 —— 一次没人授权、也没有任何
	// 症状的数据出境。
	//
	// # 指定的渠道停用/删除时:这一档不审核,**绝不回落到默认池**
	//
	// (以下描述的是 ChannelFailover 关着时的行为,也就是出厂行为。)
	//
	// 回落是"静默降级"的教科书形状:配置看起来还在、审核照跑、花销照付,
	// 而内容被发去了一个运营明确没有选的地方 —— 上一段那条约束正是为了挡住
	// 这件事,回落等于在它最要紧的时候把它关掉。
	//
	// 所以运行期的处置是 OutcomeNoChannel:本次不审核、请求放行(与本模块
	// 其余失败方向一致),并在 qy_violation_ai_review 上留一行 no_channel。
	// 它不是无声的 —— 成本页按 outcome 分组,一档指定的渠道被停掉之后
	// no_channel 会立刻长出来;管理端作用域列表也会把这一行标红。
	//
	// 写入侧另有一道闸:upsert 时渠道必须存在**且启用**,删除渠道时若还有
	// 策略指着它则直接拒绝(见 api_admin_aiscope.go / adminDeleteAIChannel)。
	// 两道闸的方向一致 —— 让"这一档不再审核"永远是一次显式动作的结果。
	ChannelId int64 `json:"channel_id" gorm:"not null;default:0"`

	// ChannelFailover 是「指定的渠道不可用时,退到加权随机池」。
	//
	// 只在 ChannelId > 0 时有意义(没指定时本来就走池子),validateAIScope
	// 会在 ChannelId 归零时把它一并归零 —— 留一个悬空的"开着的开关"会让
	// 列表上出现"按权重随机 · 故障转移开"这种自相矛盾的一格。
	//
	// # 为什么是开关,而不是直接改成"总是能退"
	//
	// 项目方的原话是「加一下故障转移吧」,而上面那一整段说的是"指定渠道
	// 往往表达的是数据流向约束"。两件事都是真的,冲突点在**默认值**:
	// 默认打开等于把存量里每一条"只发给 A"静默改写成"A 不行就发给任何人",
	// 那是一次没人按下过、也没有任何症状的数据出境扩大。
	//
	// 所以出厂 false:升级上来的站点行为逐字节不变。打开它是一次显式动作,
	// 进审计,而且作用域列表那一格会写着"故障转移: 开" —— 因为运营看到
	// "我指定了 A"时的预期是只有 A,这个预期不能在他不知情时变成假的。
	//
	// # 打开之后退到哪、退几次
	//
	// 指定的那个排第一,后面按权重从**其余**启用渠道里随机补位,整条链
	// 最多 maxAIAttempts 个渠道,全部挤在**同一份**时间预算里
	// (见 pickAIChannels 与 aiAttemptBudget)。指定的渠道已经被停用/删除时
	// 同样退到池子:开关的字面意思就是"这一档可以用别的渠道"。
	ChannelFailover bool `json:"channel_failover" gorm:"not null;default:false"`

	Remark    string `json:"remark" gorm:"type:varchar(512);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null"`
	UpdatedBy int    `json:"updated_by" gorm:"not null;default:0"`
}

func (AIScope) TableName() string { return "qy_violation_ai_scope" }

// maxAIScopes 是启用中的策略条数上限。
//
// 上限的作用不是"防止配太多",是把热路径最坏耗时钉死:sampleRatesFor 是
// 每个请求都要跑的线性扫描,而它跑在 relay 线程上。64 条足够表达任何真实的
// 分组分档(现网用户分组数是个位数),而 64 次字符串比较在这条路径上不可测量。
const maxAIScopes = 64

// aiScopeRT 是策略的运行期形态:作用域已编译,只活在快照里。
type aiScopeRT struct {
	Id   int64
	Name string
	// scopeMatcher 与违规规则内嵌的是同一个类型、同一段判定代码。
	scopeMatcher
	PreBps   int
	AsyncBps int
	// Prompt 空 = 用 aiRuntime.Prompt。取值见 aiRuntime.promptFor。
	Prompt string
	// CategoryId 0 = 不指定,命中仍按规则自己绑的类型记。见 resolveCategoryOverride。
	CategoryId int64
	// ChannelId 0 = 不指定,按权重在全部启用渠道里随机。见 pickAIChannels。
	ChannelId int64
	// ChannelFailover 为真时,指定的渠道失败后退到加权随机池补位。
	// 只在 ChannelId > 0 时有意义,见 AIScope.ChannelFailover。
	ChannelFailover bool
}

// aiSampleRolls 是**抽样函数被调用**的累计次数。
//
// 它不是"送审次数"(那是 qy_violation_ai_review 的行数),而是摇骰子的次数,
// 也就是通过了作用域闸的请求数。摆出来有两个用处:
//
//   - 运营侧:它是抽样率的分母。审核明细只有 300 行、抽样率 10% 时,
//     这个数应该在 3000 上下;差一个数量级说明作用域闸把流量挡在了外面
//     (或者相反,某条策略比想象中覆盖得广)。
//   - 契约侧:「不在作用域内的请求连抽样都不算」这条不变量只能靠它来断言 ——
//     没有计数器时,"先抽中再丢弃"与"先判作用域再抽样"在外部完全同形,
//     而两者对抽样率含义的破坏是彻底的(10% 会变成"作用域内的某个未知比例")。
var aiSampleRolls atomic.Int64

// scopeFor 解析本次请求落在哪一档:第一条匹配的策略,以及它的两个抽样率。
//
// **sc 为 nil ⇒ 两个抽样率都是 0 ⇒ 不审核。** 没有兜底档:一条策略都不命中的
// 请求不产生任何审核开销,连一次随机数都不摇。这条回落曾经落到
// AISetting.SampleRateBps 上,那一列已经删掉,理由见本文件顶部。
//
// 纯内存比较:没有分配、没有加锁、没有随机数、没有数据库。它排在抽样之前,
// 这个顺序是**契约**而不是优化 —— 反过来(先抽样再判作用域)会让界面上那个
// "10%" 变成"作用域内的 10% 乘以一个谁也说不出来的数",而抽样率是本功能
// 唯一的成本闸门,它必须是字面意思。
//
// 返回策略本体而不只是两个数字,是因为"审谁、抽多少、问什么、送到哪、
// 记成哪一类"现在是**同一条策略**上的五件事。分两次查(一次拿抽样率、
// 一次拿提示词)会让两者在并发刷新快照时来自不同版本 —— 那种错位的表现是
// "这条命中被记到了另一档指定的类型上",而记录里没有任何字段能指向原因。
func (rt *aiRuntime) scopeFor(model, group string) (sc *aiScopeRT, pre, async int) {
	if rt == nil {
		return nil, 0, 0
	}
	for _, s := range rt.Scopes {
		if s.inScope(model, group) {
			return s, s.PreBps, s.AsyncBps
		}
	}
	return nil, 0, 0
}

// promptFor 给出这一次审核真正要用的**基底**提示词(类型清单还没拼进来)。
//
// 三档回落,顺序固定:作用域自己的 → 全局 AISetting.Prompt → defaultAIPrompt
// (最后一档在 renderAIPrompt 里)。空白串按"没写"处理:一个只按了几下空格的
// 输入框与真正留空在运营心里是同一件事,而在这里分开会让那一档送出去一份
// 只有空白的判定说明。
func (rt *aiRuntime) promptFor(sc *aiScopeRT) string {
	if sc != nil && strings.TrimSpace(sc.Prompt) != "" {
		return sc.Prompt
	}
	if rt == nil {
		return ""
	}
	return rt.Prompt
}

// scopeCategoryId 是这一档指定的"命中一律记为"类型 id,0 = 不指定。
// 收在一个函数里是因为 sc 可能为 nil(兜底档),而每个调用点各写一次
// `if sc != nil` 迟早会漏掉其中一处,漏掉的表现是一次 nil 解引用 panic
// —— 在 relay 热路径上。
func scopeCategoryId(sc *aiScopeRT) int64 {
	if sc == nil {
		return 0
	}
	return sc.CategoryId
}

// buildAIScopes 把启用中的策略行编译进快照。
//
// 顺序是 priority 升序、id 升序 —— 与规则表同一个 ORDER BY,因为"第一条匹配的
// 说了算"这句话只有在顺序确定时才有意义。同优先级不给保证的话,同一份配置在
// 两个节点上会给出不同的抽样率,而那种差异查不出来。
func buildAIScopes(gdb *gorm.DB) ([]*aiScopeRT, error) {
	if gdb == nil {
		return nil, nil
	}
	var rows []AIScope
	if err := gdb.Where("enabled = ?", true).
		Order("priority asc, id asc").Limit(maxAIScopes).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*aiScopeRT, 0, len(rows))
	for _, row := range rows {
		out = append(out, &aiScopeRT{
			Id: row.Id, Name: row.Name,
			scopeMatcher: compileScope(row.ModelScope, row.GroupScope, row.GroupScopeMode),
			PreBps:       clampInt(row.PreSampleRateBps, 0, 10000),
			AsyncBps:     clampInt(row.AsyncSampleRateBps, 0, 10000),
			Prompt:       row.Prompt,
			CategoryId:   row.CategoryId,
			ChannelId:    row.ChannelId,
			// 漏掉这一位的表现是"开关在界面上是开的、线上从不转移" ——
			// 一次故障时它看起来只是"审核又放行了",没有任何地方指向这里。
			ChannelFailover: row.ChannelFailover,
		})
	}
	return out, nil
}

// aiScopesReachAnySampling 回答"这份配置有没有任何一条路径会真的送审"。
//
// 全局抽样率删掉之后判据只剩策略表:一条启用的、某个时机非 0 的策略都没有时,
// 整份 AI 配置这一轮不装配,热路径连快照上那个指针都不用读。
//
// 它同时是"策略要手动添加才能生效"这句话在装配层的落点 —— 空表 = 不生效,
// 而不是空表 = 用某个隐藏的默认值。
func aiScopesReachAnySampling(scopes []*aiScopeRT) bool {
	for _, s := range scopes {
		if s.PreBps > 0 || s.AsyncBps > 0 {
			return true
		}
	}
	return false
}

// validateAIScope 是策略的写入校验。归一(去空白)也在这里,与 validateAISetting 同规格。
func validateAIScope(s *AIScope) error {
	s.Name = strings.TrimSpace(s.Name)
	s.ModelScope = strings.TrimSpace(s.ModelScope)
	s.GroupScope = strings.TrimSpace(s.GroupScope)
	s.Remark = strings.TrimSpace(s.Remark)
	if s.GroupScopeMode == "" {
		s.GroupScopeMode = GroupScopeInclude
	}

	if s.Name == "" {
		return fmt.Errorf("策略名称不能为空 —— 一张只有作用域表达式的列表,三个月后没人说得出每条是干什么的")
	}
	if n := utf8.RuneCountInString(s.Name); n > 64 {
		return fmt.Errorf("策略名称过长(%d 字,上限 64 字)", n)
	}
	switch s.GroupScopeMode {
	case GroupScopeInclude, GroupScopeExclude:
	default:
		return fmt.Errorf("group_scope_mode 取值非法: %q(只能是 %q 或 %q)",
			s.GroupScopeMode, GroupScopeInclude, GroupScopeExclude)
	}
	// ── 启用中的策略必须绑定到具体分组 ──
	//
	// 项目方原话:「强制绑定分组,全站模型还是太高了一点」。要挡的不是"作用域这一格
	// 是空的",是**一条策略能覆盖全站**这件事 —— 空名单与 exclude 名单是它的两种写法,
	// 只挡前一种等于加了一道多点一次鼠标就能绕过的闸。
	//
	//	空名单        compileScope 不建 groups 集合,groupInScope 恒为 true = 全部分组。
	//	exclude 名单  匹配的是**名单之外的全部分组** —— 而且它随时间变宽:明天新建的
	//	              分组第二天就自动进入监控,没有任何人配置过这件事。
	//	              「豁免某几个分组」的写法在本模块另有一个更直白的:把那几个分组
	//	              放在高优先级、两个抽样率都填 0(见 PreSampleRateBps 的说明)。
	//
	// # 为什么只在 Enabled 时要求,而不是无条件必填
	//
	// 与上面那两道引用闸(类型已归档 / 渠道已停用,见 api_admin_aiscope.go)同一个判据,
	// 理由也同一条:停用的档一次都不会被 scopeFor 看到,拦它挡不住任何东西,而拦下来的
	// 代价是实打实的 —— 列表上的一键启停走的正是这个 upsert,无条件必填会让一条**存量的、
	// 正在生效的**空作用域策略连"关掉"都做不到(那次提交带的 group_scope 仍然是空的)。
	// 一道本意是收缩暴露面的校验,反过来把唯一能立刻收缩暴露面的动作堵死。
	//
	// 判据放在 Enabled 上不留任何缺口:buildAIScopes 只装配启用中的行,
	// 一条停用的空作用域策略在热路径上与不存在完全等价,而它想生效就必须再过一次这道闸。
	if s.Enabled {
		if s.GroupScope == "" {
			return fmt.Errorf("启用中的作用域策略必须绑定用户分组 —— " +
				"分组留空等于匹配全站:这一档的抽样率会作用在所有用户身上,把他们的请求内容发往第三方。" +
				"请在「用户分组」里列出要监控的分组;想先存草稿,可以把这一档保存为停用")
		}
		if s.GroupScopeMode == GroupScopeExclude {
			return fmt.Errorf("启用中的作用域策略不能用「排除」方向 —— " +
				"排除的含义是「名单之外的全部分组」,它同样覆盖全站,而且会随时间变宽:" +
				"明天新建的分组会自动进入监控,没有人配置过这件事。" +
				"请改用「包含」并逐个列出要监控的分组;要豁免某几个分组," +
				"给它们单建一档高优先级、两个抽样率都填 0 的策略")
		}
	}
	if n := utf8.RuneCountInString(s.ModelScope); n > 2048 {
		return fmt.Errorf("模型作用域过长(%d 字,上限 2048 字)", n)
	}
	if err := validateGroupScopeList(s.GroupScope); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(s.GroupScope); n > 1024 {
		return fmt.Errorf("分组作用域过长(%d 字,上限 1024 字)", n)
	}
	if n := utf8.RuneCountInString(s.Remark); n > 512 {
		return fmt.Errorf("备注过长(%d 字,上限 512 字)", n)
	}
	if s.Priority < 0 || s.Priority > 10000 {
		return fmt.Errorf("优先级必须在 0..10000 之间(升序,越小越先匹配),当前为 %d", s.Priority)
	}
	if s.PreSampleRateBps < 0 || s.PreSampleRateBps > 10000 {
		return fmt.Errorf("转发前抽样率必须在 0..10000 之间(万分比,30%% = 3000),当前为 %d", s.PreSampleRateBps)
	}
	if s.AsyncSampleRateBps < 0 || s.AsyncSampleRateBps > 10000 {
		return fmt.Errorf("转发后抽样率必须在 0..10000 之间(万分比,30%% = 3000),当前为 %d", s.AsyncSampleRateBps)
	}
	// 只有空白的提示词一律归成空串(= 用全局那一份)。留着它会让这一档发出去
	// 一份只有空白的判定说明,而界面上"这一档有自己的提示词"那个标记是亮的。
	if strings.TrimSpace(s.Prompt) == "" {
		s.Prompt = ""
	}
	if n := utf8.RuneCountInString(s.Prompt); n > maxAIPromptRunes {
		return fmt.Errorf("这一档的审核提示词过长(%d 字,上限 %d 字)—— 它每次调用都要作为 token 付一遍钱",
			n, maxAIPromptRunes)
	}
	// 提示词里禁止手抄类型清单的**占位符之外**的东西这件事不在这里挡(那是自由
	// 文本,挡不住也不该挡),但负数 id 是纯粹的脏数据:它永远解析不到任何类型,
	// 而 resolveCategoryOverride 会因此每次命中打一条告警。
	if s.CategoryId < 0 {
		return fmt.Errorf("违规类型 id 非法(%d);不指定请留 0", s.CategoryId)
	}
	// 渠道 id 同理:负数永远解析不到任何渠道,而它的表现是这一档从此每次都走
	// no_channel —— 一个只在成本页上看得出来的静默失效。渠道**存不存在、启没启用**
	// 不在这里挡(这个函数是纯校验、手上没有库句柄),那道闸在 adminUpsertAIScope。
	if s.ChannelId < 0 {
		return fmt.Errorf("审核渠道 id 非法(%d);不指定(按权重随机)请留 0", s.ChannelId)
	}
	// 没指定渠道时,故障转移这一位归零而不是报错。
	//
	// 它不是"悄悄改写运营的配置":没指定渠道时本来就走加权随机池,这一位开着
	// 与关着的运行期行为**逐字节相同**,归零改的只是它的显示形态。留着一个
	// 悬空的 true,列表上会出现「按权重随机 · 故障转移: 开」这种自相矛盾的一格,
	// 而运营会据此以为自己配了点什么。
	//
	// 报错是更糟的那个选择:表单上这一格在"不指定"时是隐藏的,报错会让一次
	// 「把指定渠道改回不指定」的正常保存莫名其妙地 400。
	if s.ChannelId == 0 {
		s.ChannelFailover = false
	}
	return nil
}

// aiScopeGroupUnbound 回答「这一档有没有真的绑定到具体分组上」。
//
// 两种写法都是"没绑":名单为空(compileScope 不建 groups 集合,分组这道闸恒为真),
// 以及 exclude 名单(匹配名单之外的全部分组)。写入闸、汇总表与启动期的存量巡检
// 必须用同一个判据 —— 三处各写一遍 `GroupScope == ""` 时,漏掉 exclude 的那一处
// 就是这条规则的缺口,而缺口的表现是一条覆盖全站的策略在列表上显示得完全正常。
//
// 入参是两列原文而不是 *AIScope:调用点之一(启动期巡检)读的是库里的存量行,
// 那些行的 group_scope 可能带着两侧空白 —— 归一只在写入侧发生过。
func aiScopeGroupUnbound(groupScope, mode string) bool {
	return strings.TrimSpace(groupScope) == "" || mode == GroupScopeExclude
}

// aiScopePromptSource 回答"这一档的提示词是继承全局的还是自己写的"。
// 与全局那一格的 aiPromptSource 分开:两者的空串含义不同(那边空 = 内置默认,
// 这边空 = 跟随全局),共用一个函数会让界面上把"继承"显示成"默认"。
func aiScopePromptSource(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return aiScopePromptInherit
	}
	return aiPromptSourceCustom
}

// aiScopePromptInherit 是"这一档没写自己的提示词,用全局那一份"。
const aiScopePromptInherit = "inherit"

// aiScopeSummaryRow 是「现在到底哪些分组在被监控、各自多少」这个问题的一行答案。
//
// 它不是策略行的回显:Shadowed 列在库里不存在,它是**这一份配置作为整体**的
// 性质,而运营真正要看的正是这个整体。
//
// 曾经这张表末尾还有一行「未匹配任何策略」的兜底档(Fallback=true),取值来自
// 那一列全局抽样率。两者一起删掉了:现在"未匹配任何策略"的答案恒为不审核,
// 而画一行恒为 0% 的假行只会让人以为那里还有个可调的旋钮。
type aiScopeSummaryRow struct {
	Id             int64  `json:"id"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Priority       int    `json:"priority"`
	ModelScope     string `json:"model_scope"`
	GroupScope     string `json:"group_scope"`
	GroupScopeMode string `json:"group_scope_mode"`
	PreBps         int    `json:"pre_sample_rate_bps"`
	AsyncBps       int    `json:"async_sample_rate_bps"`
	// PromptSource 是 "inherit"(用全局那一份)或 "custom"(这一档自己写了一份)。
	//
	// 摆在汇总表上而不是只在编辑表单里:一份写坏的作用域提示词与一份正常的
	// 在列表上长得完全一样,而它的后果是这一档的判定口径整体偏掉 ——
	// 抽样率照跑、花销照付、结论全是 clean。兜底档恒为 inherit。
	PromptSource string `json:"prompt_source"`
	// CategoryId 是这一档指定的"命中一律记为"类型,0 = 不指定(按规则自己绑的记)。
	// 只下发 id,类型名由界面用**已有的**违规类型清单接口去 join ——
	// 在这里再拼一份名字就是第二份会漂移的事实。
	CategoryId int64 `json:"category_id"`
	// ChannelId 是这一档指定的审核渠道,0 = 不指定(按权重随机)。
	// 与 CategoryId 同样只下发 id,名字由界面用**已有的**渠道列表接口去 join ——
	// 那张表上还有"启用中没有",而"指定的渠道被停用了"正是这一格最要紧的一种
	// 状态,只有 join 之后才看得出来。
	ChannelId int64 `json:"channel_id"`
	// ChannelFailover 是「指定的渠道不可用时退到加权随机池」。ChannelId 为 0 时恒假。
	//
	// 必须出现在列表上,不能只藏在编辑表单里:它改变的是**用户内容会被发到
	// 哪些第三方端点**。运营看到「审核渠道: 内部自建」时的默认理解是"只有它",
	// 而开着这一位时那句话是假的 —— 一次超时就足以让内容去到别处。
	// 一格 badge 的成本换的是这个预期不再有机会静默地变成假的。
	ChannelFailover bool `json:"channel_failover"`
	// GroupUnbound 为真表示这一行**没有绑定到任何具体分组**:名单是空的
	// (= 全部分组),或者方向是排除(= 名单之外的全部分组)。
	//
	// 这样的行按现在的写入闸**存不进来**(见 validateAIScope),所以它只可能是
	// 存量:在「强制绑定分组」之前建的,或者是全局抽样率迁移出来的那一条。
	// 它们一律照原样留着 —— 静默改写或丢弃一条正在生效的风控配置比多一条
	// 不合规的行危险得多 —— 但必须在这一页上看得见,因为一条覆盖全站的策略与
	// 一条只盯一个分组的策略在列表上长得完全一样,而两者的成本与数据出境面
	// 差着整个站点。启用中的那些还在照常匹配,停用的那些则是"补齐分组才能启用"。
	GroupUnbound bool `json:"group_unbound"`
	// Shadowed 为真表示这一行**永远不会被匹配到**:它前面有一条作用域为空
	// (= 匹配一切)的启用策略把所有请求都收走了。
	//
	// 这一列存在的唯一理由是本模块反复出现的失效形状:配置保存成功、界面
	// 显示正常、线上一次都不生效,而且没有任何报错。一条被遮住的策略与一条
	// 配错了作用域的策略在列表上长得一模一样。
	Shadowed bool `json:"shadowed"`
}

// summarizeAIScopes 把策略表折成一张"谁在被监控"的表。
//
// 排序即匹配顺序 —— 因为热路径就是这样跑的,界面上的顺序与判定顺序不一致时,
// 运营会照着一个错误的心智模型去调优先级。
//
// 注意入参是**库里的全部策略行**(含未启用的),不是快照里那份:管理列表要
// 显示停用的档,而停用的档不参与遮蔽计算(它一个请求都收不到)。
func summarizeAIScopes(rows []AIScope) []aiScopeSummaryRow {
	out := make([]aiScopeSummaryRow, 0, len(rows))
	covered := false // 前面是否已经有一条启用的、作用域为空的策略
	for _, r := range rows {
		out = append(out, aiScopeSummaryRow{
			Id: r.Id, Name: r.Name, Enabled: r.Enabled, Priority: r.Priority,
			ModelScope: r.ModelScope, GroupScope: r.GroupScope,
			GroupScopeMode: r.GroupScopeMode,
			PreBps:         r.PreSampleRateBps, AsyncBps: r.AsyncSampleRateBps,
			PromptSource: aiScopePromptSource(r.Prompt),
			CategoryId:   r.CategoryId,
			ChannelId:    r.ChannelId,
			// 没指定渠道时恒假,与 validateAIScope 的归一同口径:存量里可能躺着
			// 一行 channel_id=0 而 channel_failover=1(这一列刚加,写入闸之前存的),
			// 照原样下发会让列表画出一个不存在的状态。
			ChannelFailover: r.ChannelId > 0 && r.ChannelFailover,
			GroupUnbound:    aiScopeGroupUnbound(r.GroupScope, r.GroupScopeMode),
			Shadowed:        r.Enabled && covered,
		})
		if r.Enabled && strings.TrimSpace(r.ModelScope) == "" && strings.TrimSpace(r.GroupScope) == "" {
			covered = true
		}
	}
	return out
}
