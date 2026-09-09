package violation

import (
	"strings"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/shopspring/decimal"
)

// AI 审核的数据模型。
//
// # 它不是第二套违规记录
//
// 命中之后落的是普通的 qy_violation_record,规则是普通的 qy_violation_rule
// (match_type = ai_review)。也就是说影子/真实、扣费、计数、封号策略、申诉、
// 作用域、证据归档全部沿用既有那一套,一行都不重写。项目方的原话是
// 「AI审核规则,可以是转发前审核、转发后审核。和当前的规则设计是一样的」——
// 这里把它当字面要求实现:AI 只是**第七种匹配方式**,不是第二个子系统。
//
// 本文件只新增三张表,而且三张各自回答一个既有表回答不了的问题:
//
//   - AIChannel  —— 审核请求发到哪里、用哪把钥匙。密钥必须密文,规则表放不下。
//   - AISetting  —— 抽样率、提示词、超时。这些是**全局**的,挂在任何一条规则上
//     都会让"改一次影响全部"变成"改 N 次、漏一条就不一致"。
//   - AIReview   —— 每一次审核调用的 token 与花费。**没命中也要记**,而 Record
//     只在命中时才有行 —— 用它算成本会把绝大多数花销漏掉。

// AI 审核的生效时机。取值落在 Rule.Phase 上,与既有阶段同一个枚举。
//
// # 为什么 PhasePostAsync 是新的一档,而不是复用 PhaseUpstreamErr
//
// PhaseUpstreamErr 的挂载点(PostRelayGuard)只在**上游返回错误时**才被调用
// (见 controller/relay.go 的 defer:`if newAPIError != nil`)。把"转发后审核"
// 挂上去,结果是它只审失败的请求 —— 而内容审核关心的绝大多数请求都是成功的。
// 那会是一条保存得下去、也确实会执行、但永远审不到正常流量的规则,
// 与本模块反复警惕的"静默失效"完全同形。
//
// 所以 post_async 是独立一档:调度点在转发**之前**(PreRelayGuard,每个请求
// 都会经过),但审核调用与其全部后果都被丢进 guard.HotAsync 的异步队列,
// 本次请求一秒都不等。语义因此是准确的「不影响本次请求的事后审核」。
//
// # 已知边界(不要在文档里糊过去)
//
// 它审的是**请求内容**,不是上游的回答 —— 这个挂载点拿不到响应体,而本轮
// 不改 relay 主干去加一个响应钩子。要审回答需要另一个挂载点,见返回值 not_done。
const PhasePostAsync = "post_async"

// MatchAIReview 是第七种匹配方式:把上下文送给一个外部模型,由它判违规。
//
// Pattern 的语义是**类型白名单**:换行或逗号分隔的违规类型名,命中条件为
// "模型判定违规 **且** 它给出的 category 在这张表里"。留空 = 只要判违规就命中。
//
// 为什么类型过滤要放在 pattern 上而不是另加一列:这样一条规则 = 一个违规类型,
// 而"违规类型"正是既有体系里由 Rule.PublicReason / Rule.Name 承载的东西。
// 运营配三条 ai_review 规则(涉黄 / 涉政 / 越狱),就自然得到三个类型、三档
// 处置动作、三份计数权重 —— 不需要任何新概念。
const MatchAIReview = "ai_review"

// 一次审核调用的结局。落在 AIReview.Outcome 上。
//
// 除 OutcomeViolation / OutcomeClean 之外**全部是失败**,而失败一律放行
// (见 aireview.go 顶部的失败方向表)。分这么细是因为它们的处置人不同:
// timeout 找网络、bad_json 找提示词、upstream_error 找渠道、no_channel 找配置。
// 合并成一个 "failed" 会让"AI 审核最近怎么全放行了"这个问题无从下手。
const (
	OutcomeClean         = "clean"          // 判定未违规
	OutcomeViolation     = "violation"      // 判定违规(是否真的处置还要看规则的类型过滤与模式)
	OutcomeTimeout       = "timeout"        // 超过本次时机的时间预算
	OutcomeBadJSON       = "bad_json"       // 返回的不是可解析的结构化结论
	OutcomeUpstreamError = "upstream_error" // 网络错误 / 非 2xx
	OutcomeNoChannel     = "no_channel"     // 一个可用渠道都没有(含密钥解不开)
)

// 审核请求的时机标签,落在 AIReview.Phase 上。与规则的 Phase 取值一致。

// AIChannel 是一个审核模型渠道。
//
// # 密钥的三条硬约束
//
//  1. **落库必须是密文**(KeyCipher + KeyNonce + KeyVersion,AES-256-GCM,
//     见 aireview_crypto.go)。没配 violation.ai_review_key 时,保存带
//     api_key 的渠道直接 400 —— 绝不回落到明文列。
//  2. **接口永不回显**。列表与详情返回的是 KeyHint(掩码,尾 4 位)与
//     HasKey(布尔),明文密钥在本进程里唯一的去向是出站请求的 Authorization 头。
//  3. **绝不进日志**。审核调用失败时打印的是渠道 id 与名字,不是地址+密钥。
//
// KeyCipher / KeyNonce 打 `json:"-"`:这不是洁癖 —— 本仓的管理端列表接口
// 习惯直接 `respond(c, rows)` 整行下发,少一个 tag 就等于把密文连同 nonce
// 一起交给浏览器,而密文一旦离开服务器,轮换密钥就再也补不回来了。
type AIChannel struct {
	Id   int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Name string `json:"name" gorm:"type:varchar(64);not null;default:''"`
	// BaseUrl 是 OpenAI 兼容端点的**基地址**,例如 https://api.deepseek.com/v1。
	// 调用时拼 /chat/completions;调用方已经写了 /chat/completions 的也能识别,
	// 见 aireview.go 的 chatCompletionsURL —— 那是运营最容易填错的一格。
	BaseUrl string `json:"base_url" gorm:"type:varchar(256);not null;default:''"`
	Model   string `json:"model" gorm:"type:varchar(128);not null;default:''"`

	// Protocol 是这个渠道说哪一种"审核方言",取值见 aireview_guard.go:
	//
	//	""(零值)/ json_prompt  发提示词、要 JSON。**存量行与出厂行为**。
	//	qwen3guard              不发提示词,解析 Safety/Categories 安全标签。
	//
	// # 零值必须落在 json_prompt 上
	//
	// AutoMigrate 给存量行 ADD COLUMN 时回填的是空串(gorm default:''),而空串
	// 经 normalizeAIProtocol 归到 json_prompt —— 也就是这一列加入之前的唯一行为。
	// 换成"零值 = qwen3guard"会让每一个已经在跑的站点在升级那一秒静默换掉
	// 审核协议,而症状是全部渠道开始 bad_json、fail-open 放行,界面上一切正常。
	//
	// 它是**渠道级**而不是全局级:护栏模型与通用模型的长短互补(见
	// aireview_guard.go 顶部的对照表),同一个站点两种都挂是预期用法,
	// 一个全局开关会让这变得不可能。
	Protocol string `json:"protocol" gorm:"type:varchar(24);not null;default:''"`

	// RiskName 只在 Protocol = granite_guardian 时有意义:Granite Guardian
	// 一次只审**一种**风险,而审哪一种由这一格决定(取值见 graniteRisks)。
	//
	// # 零值:空串 = harm 档 = 不发 system
	//
	// 实测(见 aireview_granite.go 文件头)"不发 system"与 system="harm" 在
	// 每一个样本上结果相同 —— 模板对认不出来的 system 一律落回 harm 那段
	// 风险定义。所以空串既是"这一列加入之前的行为",也是语义上的默认档,
	// 存量渠道 ADD COLUMN 之后逐字节无变化。
	//
	// 选错风险名的后果是**漏判**而不是报错(越狱内容在 violence 档下判 No),
	// 所以界面上这一格必须逐档给出说明,写入侧也只放行能挂在用户消息上的
	// 那七档 —— 理由见 graniteRisks。
	RiskName string `json:"risk_name" gorm:"type:varchar(32);not null;default:''"`

	// GuardControversial 只在 Protocol = qwen3guard 时有意义:Qwen3Guard 比常见
	// 护栏模型多一档 Controversial(有争议),这一格决定把它当违规还是不当。
	//
	// 取值 safe / sensitive / unsafe,零值(空串)= safe = **不当违规**。
	// 方向与本模块一贯的取舍一致:新增能力不得替站点收紧处置。
	// json_prompt 渠道上这一列恒被忽略,不做校验以外的处理。
	GuardControversial string `json:"guard_controversial" gorm:"type:varchar(16);not null;default:''"`

	// GuardCategories 是**启用的类别子集**,逗号分隔的九类 id(见 aireview_guard.go)。
	//
	// # 零值:空串 = 九类全启用
	//
	// 这是唯一正确的零值方向。存量行 ADD COLUMN 回填空串,而空串必须等于
	// 这一列存在之前的行为 —— 那时没有任何过滤。反过来(空串 = 一个都不启用)
	// 会让升级那一秒起所有护栏渠道的判定全部降档,而界面上一切正常。
	//
	// 停用一个类别**不等于**丢弃它:Unsafe 且解析出的类别全被停用时,判定仍然
	// 成立,只是置信度从 0.95 降到 0.6(见 guardLabels.toVerdict)。静默吃掉一票
	// Unsafe 在本模块一律不接受。
	GuardCategories string `json:"guard_categories" gorm:"type:varchar(256);not null;default:''"`

	// GuardElevate 是 GuardControversial = sensitive 档下"命中即升级成违规"的
	// 类别,同样是逗号分隔的九类 id。
	//
	// # 零值:空串 = 参考实现的三类
	//
	// 空串回落到 jailbreak / pii / suicide_and_self_harm(Wei-Shaw/sub2api 的
	// isElevatedControversial)。它不能是"空集合" —— sensitive 档配一个空的
	// 升级清单等于把这一档变成 safe,而界面上它写着"命中敏感类别时拦截"。
	// 想要"完全不升级"的运营应该选 safe 档,那一格的字面意思就是这个。
	GuardElevate string `json:"guard_elevate" gorm:"type:varchar(256);not null;default:''"`

	KeyNonce   qymodel.VarBinary `json:"-" gorm:"size:32"`
	KeyCipher  qymodel.VarBinary `json:"-" gorm:"size:512"`
	KeyVersion int               `json:"key_version" gorm:"not null;default:0"`
	// KeyHint 是掩码提示(如 "****a1b2"),写入时算好存下来。
	// 不在读取时现算:那需要先解密,而列表接口没有任何理由碰明文密钥。
	KeyHint string `json:"key_hint" gorm:"type:varchar(32);not null;default:''"`

	// KeyEndpoint 是**密钥最后一次写入时这个渠道的 base_url**。
	//
	// # 它挡的是什么
	//
	// 上游密钥对 role=10 是刻意 write-only 的:列表/详情/PUT 回显只给 key_hint,
	// 全站没有任何读回明文的口。但改地址与试跑**都是 role=10 权限**,于是曾经
	// 有一条两步走通的越权路径:把某个已配密钥的渠道的 base_url 指到自己的机器上
	// 保存,再点一次连通性测试 —— 出站请求带着 `Authorization: Bearer <原密钥>`
	// 打过来,一把他读不到的密钥被完整取走。热路径同形:改完地址之后,下一条
	// 真实审核流量会把同一把密钥送到同一个地方。
	//
	// 曾经的堵法是「改地址就清空密钥」。那条被撤回了 —— 改地址就只是改地址,
	// 保存不再动任何别的列。现在的堵法是把密钥**绑在它被写入时的那个地址上**:
	// 地址与绑定不一致时,这把密钥一律不出站(热路径跳过该渠道,试跑直接拒绝),
	// 直到有人在表单里重填一次密钥 —— 而重填的前提正是他**知道**那把密钥是什么。
	//
	// 攻击链因此断在第二步:第一步(改地址保存)照样成功,但它同时让绑定失配,
	// 而攻击者没有任何办法把绑定改回来 —— 写绑定的唯一入口是写密钥。
	//
	// # 零值:空串
	//
	//	空串 + 没有密钥  正常的免鉴权渠道。没有密钥就没有可泄漏的东西,不设防。
	//	空串 + 有密钥    这一列存在之前写入的历史行。按「无绑定」处理(照常出站),
	//	                 也就是与这一列存在之前逐字节一致的行为 —— 升级不改变
	//	                 任何一个现存渠道的可用性。启动时的 migrateAIChannelKeyEndpoint
	//	                 会把这些行回填成它们当时的 base_url,回填之后闸门才生效。
	//
	// 空串**不能**当成"失配"来处理:那会让回填没跑到的部署在升级那一秒
	// 全部审核渠道同时失效,而 AI 审核失败的方向是放行 —— 一次静默的风控关闭。
	//
	// 攻击者无法制造"空串 + 有密钥":写这一列的唯一地方是 applyAIChannelKey,
	// 而它只在清空密钥时写空串;adminUpdateAIChannel 的 Select 白名单里没有这一列。
	KeyEndpoint string `json:"-" gorm:"type:varchar(512);not null;default:''"`

	// TimeoutMs 是这个渠道单次调用的超时。0 = 用当前时机的全局预算。
	// 渠道级存在的理由:同一站点可能同时挂一个本地小模型(50ms)与一个云端
	// 大模型(2s),用一个数字卡住两者,要么本地的白等、要么云端的必超时。
	TimeoutMs int `json:"timeout_ms" gorm:"not null;default:0"`
	// Weight 是加权随机的权重,>= 1。多渠道时按权重分流,而不是永远打第一个。
	Weight  int  `json:"weight" gorm:"not null;default:1"`
	Enabled bool `json:"enabled" gorm:"not null"`

	// PriceInPerM / PriceOutPerM 是每百万 token 的美元单价,用于把 token 换算成
	// 花费。0 表示"没填",此时 cost_usd 恒为 0,界面会把这一列标成"单价未配"——
	// 刻意不猜一个默认价:猜错的成本数字比没有数字更糟,它会被当成真的。
	PriceInPerM  decimal.Decimal `json:"price_in_per_m" gorm:"type:decimal(18,8);not null;default:0.00000000"`
	PriceOutPerM decimal.Decimal `json:"price_out_per_m" gorm:"type:decimal(18,8);not null;default:0.00000000"`

	// Group 是**审核渠道分组**:一组可以互相顶替的审核端点。
	//
	// 与用户分组、模型分组都不是一回事 —— 它只在这一页里有意义。作用域策略
	// 从此选的是"哪一个渠道分组"(或明确指定几个渠道),而不再是"全部启用渠道":
	// 后者会让一条本来只该发给自建端点的流量,在别的渠道被启用的那一刻起
	// 悄悄流向它。分组把这件事变成一次显式的归属声明。
	//
	// 空串是一个**合法的分组名**(可以理解为"未分组"),不是"属于所有分组" ——
	// 后者会让分组这层约束在存量渠道上直接失效。存量行 ADD COLUMN 回填空串,
	// 于是它们全部落进"未分组"这一档,而作用域必须显式选中它才会用到。
	Group string `json:"group" gorm:"column:group_name;type:varchar(64);not null;default:'';index:idx_qy_vac_group"`

	// Prompt 是**这个渠道的**审核提示词(空 = 用内置默认 defaultAIPrompt)。
	//
	// 它以前住在两个地方:AISetting 的全局那一份,以及每条作用域自己那一份。
	// 2026-09-06 项目方拍板整体搬到渠道上并删掉另外两处 —— 理由是提示词与
	// **协议**绑死:json_prompt 要求模型吐 JSON,而护栏协议(qwen3guard /
	// granite_guardian)压根不发提示词。把它挂在作用域上,就允许"一条作用域
	// 的提示词被分发到一个根本不读提示词的渠道"这种配得出来、也不报错、
	// 只是完全不生效的组合。挂在渠道上,提示词与读它的那个端点永远在一起。
	//
	// 发出去的那一份仍由 renderAIPrompt 把违规类型清单拼进来 —— 这一列是**基底**。
	// 护栏协议的渠道上它恒被忽略(见 buildReviewRequest),写入侧不拦:
	// 换协议是一次编辑就能做的事,清空它反而会让人换回来时发现提示词没了。
	Prompt string `json:"prompt" gorm:"type:text"`

	// BlockMessage 是这个渠道判出违规、并且规则动作是拦截时,**返回给用户**的那句话。
	//
	// 空 = 不覆盖,沿用规则自己的 Rule.BlockMessage(它再空则用 defaultBlockMessage)。
	// 优先级是「渠道 > 规则 > 内置」:项目方要的是"这个返回文案在审核渠道里设定",
	// 而规则那一份要留着 —— 本地词表/正则规则根本没有渠道可言,它们的拦截文案
	// 只可能来自规则。两者都存在时以渠道为准,因为渠道是更靠近"这一次是谁判的"
	// 那一端的信息。
	//
	// 它只影响 **AI 审核**这条路的拦截:本地规则命中时手上没有渠道,取不到也用不上。
	BlockMessage string `json:"block_message" gorm:"type:varchar(512);not null;default:''"`

	// CategoryId 是「这个渠道判出的违规,计次记到哪个违规类型上」。0 = 不指定。
	//
	// # 它解决的是"护栏模型的类别体系对不上本站类型表"
	//
	// 护栏协议(qwen3guard / llama_guard / granite_guardian)的类别是训练时钉死的,
	// 改提示词改不动。本站类型表里没有同名标识的那几类,判定会折进兜底「未分类」,
	// 而兜底类型的阈值出厂是 0 —— 也就是判了、记了,却一次都不推进封号线。运营
	// 要么去类型页把缺的标识逐个建出来,要么在这里指定一个类型,让这个渠道判出来的
	// 每一条都记进它并计次。这一格是后者。
	//
	// # 优先级:作用域 > 渠道 > 规则
	//
	// 作用域那一格的字面意思是「这一档的命中**一律**记为」,而渠道比它宽 ——
	// 同一个渠道会被多档作用域用到。所以作用域配了就是作用域说了算,这一格只在
	// 它没配时生效;两个都没配时仍然按规则自己绑的类型记。取舍与解析见
	// aiCategoryOverride 与 resolveCategoryOverride。
	//
	// # 零值:0 = 不指定
	//
	// 存量行 ADD COLUMN 回填 0,而 0 的路径与这一列存在之前逐字节相同。
	// 指向一个已归档类型时**不**折成「未分类」,而是退回规则自己那一档并打一条
	// 告警:静默换掉计数落点等于静默换掉封号判据,而那件事没有任何症状。
	CategoryId int64 `json:"category_id" gorm:"not null;default:0"`

	// NotifyEmail 决定这个渠道判出违规时,要不要给**被判的那个用户**发一封邮件。
	//
	// # 为什么挂在渠道上而不是规则或全局
	//
	// 与 BlockMessage 同一条理由:发不发、发什么,是"谁判的"这一端的属性。
	// 本地词表/正则规则根本没有渠道,它们那条路不发邮件 —— 那不是遗漏,
	// 是这一格的作用域本来就只覆盖 AI 审核。
	//
	// # 影子命中恒不发
	//
	// 影子模式的契约是「不扣费,不封号,不记录违规次数」,而给用户发一封
	// "你违规了"的邮件是**执行**里最外露的一种。判据写在 persistRecord 上,
	// 与"影子不推进计数"是同一道闸。
	NotifyEmail bool `json:"notify_email" gorm:"not null;default:false"`
	// EmailSubject / EmailBody 是这封邮件的标题与正文模板,空 = 用内置默认
	// (defaultViolationEmailSubject / defaultViolationEmailBody)。
	//
	// 正文按 **HTML** 发送:common.SendEmail 的信头恒是 text/html,所以模板
	// 里写标签就是所见即所得。占位符与转义规则见 aireview_notify.go ——
	// 简言之:模板本身是管理员写的、当作可信 HTML 原样输出,而**替换进去的
	// 每一个值**(用户名、模型名、判定理由)都先过一遍 HTML 转义。
	// 少了后半句,一个把用户名改成 <script> 的人就能往站点发出的邮件里注入脚本。
	EmailSubject string `json:"email_subject" gorm:"type:varchar(200);not null;default:''"`
	EmailBody    string `json:"email_body" gorm:"type:text"`

	Remark    string `json:"remark" gorm:"type:varchar(512);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null"`
	UpdatedBy int    `json:"updated_by" gorm:"not null;default:0"`
}

func (AIChannel) TableName() string { return "qy_violation_ai_channel" }

// HasKey 供接口下发:界面据此显示"已配置密钥 / 未配置",而不是显示密钥本身。
func (ch AIChannel) HasKey() bool { return len(ch.KeyCipher) > 0 }

// KeyBoundElsewhere 回答「这把密钥是写给另一个地址的吗」。
//
// 为真时密钥一律不出站:热路径跳过该渠道,试跑直接拒绝。完整理由见 KeyEndpoint。
// 空绑定(历史行)恒为假 —— 零值方向同样写在 KeyEndpoint 上。
func (ch AIChannel) KeyBoundElsewhere() bool {
	if !ch.HasKey() || strings.TrimSpace(ch.KeyEndpoint) == "" {
		return false
	}
	return !sameAIChannelEndpoint(ch.KeyEndpoint, ch.BaseUrl)
}

// AISetting 是 AI 审核的全局设置,单行表(Id 恒为 1)。
//
// # 为什么是全局而不是挂在规则上
//
// 提示词与超时:一次请求只发一次审核调用(见 aireview.go),多条规则共用同一
// 份结论,那么这些参数本来就只可能有一份。挂到规则上会造出 N 份必须手工保持
// 一致的拷贝。
//
// # 这里**没有**抽样率
//
// 曾经有一列 sample_rate_bps(全局抽样率 + 作用域都不命中时的兜底)。它已经
// 连同那条兜底一起删掉:它把这一页最重要的问题变得答不出来 —— 作用域表上
// 一条策略都没有、看起来什么都没监控,而线上全站 5% 的请求内容正在被发往
// 第三方。现在"送不送审"只由作用域策略表回答,表上没有的就是不审。
// 存量值的迁移见 migrate.go 的 migrateAISampleRateToScope。
type AISetting struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement:false"` // 恒为 1

	// Enabled 是 AI 审核的总开关。关掉之后 ai_review 规则一条都不会命中
	// (快照直接不装配它们),热路径连一次随机数都不摇。
	Enabled bool `json:"enabled" gorm:"not null"`

	// PreTimeoutMs 是**转发前审核**的时间预算,上限 maxPreTimeoutMs。
	//
	// 这个数字直接加在每一个被抽中的请求的首字节延迟上,这是转发前审核不可
	// 消除的代价,必须写在界面上而不是只写在这里。上限存在的理由:它是
	// "审核服务变慢时全站变慢多少"的唯一闸门。
	PreTimeoutMs int `json:"pre_timeout_ms" gorm:"not null;default:0"`
	// AsyncTimeoutMs 是**转发后审核**的时间预算。它不占用户的时间,
	// 所以可以宽松得多,只受 guard 异步 worker 自己的预算约束。
	AsyncTimeoutMs int `json:"async_timeout_ms" gorm:"not null;default:0"`

	// **这里没有 Prompt。** 2026-09-06 起提示词整体搬到 AIChannel.Prompt,
	// 全局这一份连同 AIScope.Prompt 一起删了(存量值由 migrateAIPromptToChannels
	// 抄到各渠道之后才 DROP 列)。理由写在 AIChannel.Prompt 上:提示词与协议绑死,
	// 而协议是渠道的属性。留一个全局兜底会让"渠道说它不读提示词、却仍然从别处
	// 继承到一份"这种组合继续存在,而它配得出来、不报错、完全不生效。

	// MaxInputChars 是送审内容的字符上限。它既是成本闸(按 token 计费),
	// 也是隐私闸(送出去的越少越好)。0 时回落到 defaultAIMaxInputChars。
	MaxInputChars int `json:"max_input_chars" gorm:"not null;default:0"`

	// ThirdPartyNoticeAck 记录管理员是否已经确认"用户请求内容会被发往第三方"。
	//
	// 它是一个**必须显式勾过一次**的闸:未确认时保存 enabled=true 会被 400 拒绝。
	// 不做成纯前端提示,是因为纯前端的提示在下一次改版里会被顺手删掉,
	// 而"用户内容出境"这件事需要一条查得到的记录(它连同审计一起留痕)。
	ThirdPartyNoticeAck bool `json:"third_party_notice_ack" gorm:"not null"`

	// ─────────────────── 审核日志(qy_violation_ai_review)───────────────────

	// LogContent 决定审核明细里要不要留一份**送审内容**。
	//
	// # 为什么默认打开
	//
	// 它看起来是在扩大隐私面,其实不是:能走到这一行的前提是站点已经显式勾过
	// ThirdPartyNoticeAck —— 也就是承认「被抽中的请求内容会被发送到第三方」。
	// 相比之下,在自己的库里留一份**脱敏后、截断到几百字、三天就滚掉**的副本
	// 是严格更小的暴露面。而没有它,审核日志只能回答"判了违规",回答不了
	// "凭什么" —— 误判申诉、提示词调优、以及"这条规则是不是配错了"三件事
	// 全都无从下手。
	//
	// 关掉之后只是不再写新的内容,历史行原样保留到保留期结束。
	//
	// # 零值方向:ADD COLUMN 回填 false,但启动期会**一次性**补成 true
	//
	// 这是本模块少数几处"升级会改变行为"的地方之一,写在这里是为了让它可查:
	// AutoMigrate 给存量行填 false,而 migrateAIReviewLogDefaults 在启动时把
	// 那些**从未被人设置过**的行(三列全是零值)补成出厂档并打一条日志。
	//
	// 为什么不留 false:留存内容是这张表**唯一**回答"凭什么"的东西,而升级前
	// 它压根不存在,所以 false 不是"运维做过的决定",只是一个新列的零值。让
	// 每个站点都从"日志开着但看不出内容"起步,只会让人以为功能没做完。
	// 一旦有人在设置页保存过一次,那三列就是显式值,迁移永远不再碰它们。
	LogContent bool `json:"log_content" gorm:"not null"`

	// LogContentViolationFull 决定**判定违规**的那些行要不要特殊对待:
	// 始终留存,而且留**完整**的送审内容(不受上面那个开关与下面那个字数上限约束)。
	//
	// # 为什么违规行该单独一档
	//
	// 上面那个上限(默认 1000 字)是为**量**设的:未违规的行占九成九以上,它们只需要
	// 够看清"这是什么内容"。而判了违规的行是另一种东西 —— 它要回答的是"凭什么",
	// 而回答它的人是在处理申诉、复核误判、调提示词。给这种行一段砍掉后半截的文本,
	// 恰好砍掉的常常就是违规的那一段。它们的量也不构成问题:命中是稀有事件,
	// 一个开了 10% 抽样的站点一天几十万次审核里,判违规的通常是三位数。
	//
	// # 零值方向:NULL = 打开
	//
	// 用 *bool 而不是 bool,是因为这一列需要三个状态:**从没设置过**(NULL,
	// AutoMigrate 给存量行填的就是它)、显式开、显式关。plain bool 只有两个,
	// 于是"升级带来的 false"与"运维想清楚了、就是不想留"在库里长得一模一样,
	// 而这两者的正确处置正好相反。
	//
	// NULL 读作 true(见 aiRuntime 的装配):它是一次新增能力,而不是谁做过的决定。
	// 这样也就**不需要**任何回填迁移 —— 少一次"会改变行为的启动期写库"。
	// 一旦有人在设置页保存过一次,这一列就是显式值,再也不会被当成默认。
	LogContentViolationFull *bool `json:"log_content_violation_full"`

	// LogContentMaxChars 是留存内容的字符上限,0 回落到 defaultAIReviewContentChars。
	//
	// 它是这张表体积的两个乘数之一(另一个是保留期)。刻意比 MaxInputChars
	// 小一档:送审要完整(判得准),留存只要够看清"这是什么内容"。
	//
	// **判定违规的行不受它约束**,见 LogContentViolationFull。
	LogContentMaxChars int `json:"log_content_max_chars" gorm:"not null;default:0"`

	// LogRetentionDays 是审核明细的保留天数,0 回落到 defaultAIReviewRetentionDays(3)。
	//
	// # 为什么没有"永久保留"这一档
	//
	// 这张表的行数正比于**被抽中的请求数**。一个百万级站点开 10% 抽样就是一天
	// 十万行,带内容是数百 MB;给它一个"0 = 永久"的档位,等于在设置页上放一个
	// 会在三个月后撑爆磁盘的选项,而那一天到来之前没有任何征兆。要长期留存的
	// 站点应该配 log_database 分家,再按自己的备份策略归档 —— 那是有人负责的
	// 保留,而不是一个忘了改的开关。
	//
	// 存量行回填 0 → 3 天,这是唯一一处"升级会开始删数据"的地方,刻意如此:
	// 在此之前这张表**从来没有被清理过**,那是缺陷不是特性(证据表、计数表
	// 都有保留期,唯独它没有)。
	LogRetentionDays int `json:"log_retention_days" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (AISetting) TableName() string { return "qy_violation_ai_setting" }

// AIReview 是**每一次审核调用**的明细,含 token 与花费。
//
// # 为什么不能靠 qy_violation_record 算成本
//
// Record 只在命中时才有行。抽样 30% 跑一个月,命中可能只有千分之一 ——
// 用 Record 算成本会漏掉 99.9% 的花销,而那 99.9% 正是钱花在哪里的答案。
// 项目方的原话:「没有这个,开了概率抽样之后没人知道花了多少」。
//
// # 它也是失败的唯一痕迹
//
// 失败一律放行(fail-open),也就是说审核挂了对用户完全无感、对 relay 完全
// 无感。没有这张表,"最近两周 AI 审核其实一次都没成功"是查不出来的。
type AIReview struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// ReviewNo 是幂等键:同一个 request_id + 同一个时机只可能有一行。
	// 与 Record.RecNo 同理 —— defer 重入与重试循环都可能让同一次请求走到两次。
	ReviewNo string `json:"review_no" gorm:"type:varchar(80);not null;uniqueIndex:uk_qy_vai_no"`

	UserId   int    `json:"user_id" gorm:"not null;index:idx_qy_vai_user,priority:1"`
	Username string `json:"username" gorm:"type:varchar(64);not null;default:''"`
	Phase    string `json:"phase" gorm:"type:varchar(24);not null;default:''"`

	ChannelId   int64  `json:"channel_id" gorm:"not null;default:0"`
	ChannelName string `json:"channel_name" gorm:"type:varchar(64);not null;default:''"`
	// ReviewModel 是**审核用的**模型名,与被审请求的 ModelName 是两回事。
	// 两列都要有:成本按前者归集,误判分析按后者筛。
	ReviewModel string `json:"review_model" gorm:"type:varchar(128);not null;default:''"`

	Outcome string `json:"outcome" gorm:"type:varchar(24);not null;default:'';index:idx_qy_vai_outcome"`
	// Violated / Category / Confidence / Reason 是模型给出的结构化结论。
	// Outcome 不是 clean/violation 时这四列无意义(全零值)。
	Violated bool   `json:"violated" gorm:"not null"`
	Category string `json:"category" gorm:"type:varchar(64);not null;default:''"`
	// RawCategory 是模型**原样**返回的那个类型名,只在它不在类型清单里时非空。
	//
	// 归一之后 Category 会变成兜底类型的 key,于是"模型一直在回 porn"这件事
	// 在归一后的列上完全看不出来 —— 而它正是"提示词与类型表脱节了"的唯一症状。
	// 静默丢弃原值等于把唯一的线索扔掉,所以留一列。
	//
	// 它是模型输出的一小段文本,不是用户内容,但仍按 64 字截断:模型偶尔会
	// 把整句理由塞进 category 字段,而那一句可能复述用户原文。
	RawCategory string          `json:"raw_category" gorm:"type:varchar(64);not null;default:''"`
	Confidence  decimal.Decimal `json:"confidence" gorm:"type:decimal(5,4);not null;default:0.0000"`
	// Reason 是模型给的理由。它可能复述用户原文,所以与 Record.MatchSnippet
	// 同规格做脱敏(redactSnippet)之后再落库。
	Reason string `json:"reason" gorm:"type:varchar(512);not null;default:''"`

	// 这四列是**整条重试链**的合计,不是最后一次调用的用量。
	//
	// 故障转移让"一次抽样 = 一次调用"变成了"一次抽样 = 最多 maxAIAttempts 次
	// 调用",而其中 bad_json 那一种是已经付过钱的。只记最后一次会让这张表
	// 系统性低估花费 —— 而这张表存在的全部理由就是回答"到底花了多少"。
	// 累加口径见 aiChainCost。
	PromptTokens     int `json:"prompt_tokens" gorm:"not null;default:0"`
	CompletionTokens int `json:"completion_tokens" gorm:"not null;default:0"`
	TotalTokens      int `json:"total_tokens" gorm:"not null;default:0"`
	// CostUsd 由渠道单价 × token 算出。渠道没填单价时恒为 0,
	// 界面据 priced 标记区分"没花钱"与"不知道花了多少"。
	CostUsd decimal.Decimal `json:"cost_usd" gorm:"type:decimal(18,8);not null;default:0.00000000"`
	// CostUnknown 为真表示上面那个数字是**下界**而不是真值:这一次审核的重试链上
	// 至少有一次调用产生了 token 却算不出钱(那个渠道没填单价)。
	//
	// # 为什么靠 cost_usd 反推不出来
	//
	// 成本页原先用 `total_tokens > 0 AND cost_usd <= 0` 找"算不出钱的调用"。
	// 混价链(一个填了单价的渠道 + 一个没填的,两次都产生了 token)算出来的
	// cost_usd 是**正数**,于是这一行从那个判据下面溜过去 —— 而它恰恰正是偏低的
	// 那一种,界面还会把它当成准确值展示。偏低是最没人会去核对的方向。
	// 故障转移把"一次抽样 = 一次调用"变成了"最多三次调用",混价链因此不再是
	// 一种理论情形:池子里只要有一个渠道没填单价,它随时会被加权随机抽到。
	//
	// # 为什么叫 cost_unknown 而不是 cost_known
	//
	// 为了让零值站在正确的一边。AutoMigrate 给存量行 ADD COLUMN 时回填 false,
	// 含义是"没有任何理由认为这一行算不准",与这一列存在之前的口径逐字节一致。
	// 反过来(cost_known 默认 false)会把全部历史行一夜之间判成"算不准",
	// 在成本页上凭空点亮一条谁也复核不了的告警。
	CostUnknown bool `json:"cost_unknown" gorm:"not null"`
	LatencyMs   int  `json:"latency_ms" gorm:"not null;default:0"`
	// Attempts 是这一次审核实际发出的调用次数(含失败的那几次)。
	//
	// 它是上面那几列的**分母**:没有它,一行 3 倍于常态的花费看不出是"这次
	// 送审的内容特别长"还是"前两个渠道挂了各付了一次钱",而两者的处置人不同
	// (前者调 max_input_chars,后者去修渠道)。
	//
	// 0 表示这一行是在这一列存在之前写下的(或者一次调用都没发出去,
	// 例如 no_channel)—— 刻意不回填 1:猜一个数字比留一个空更糟。
	Attempts int `json:"attempts" gorm:"not null;default:0"`

	// RuleId / RecordId 是与既有违规体系的接线口:判违规且落到某条 ai_review
	// 规则上时,这里写下那条规则与它产生的记录 id。0 表示没落到任何规则
	// (判了违规,但类型不在任何规则的过滤表里,或规则都不在作用域内)。
	RuleId   int64 `json:"rule_id" gorm:"not null;default:0"`
	RecordId int64 `json:"record_id" gorm:"not null;default:0"`

	RequestId string `json:"request_id" gorm:"type:varchar(64);not null;default:'';index:idx_qy_vai_reqid"`
	// ModelName / UsingGroup 是**被审那次请求**的模型与用户分组,不是审核渠道的。
	//
	// 两列都带索引,而且索引是给**筛选**用的,不是给统计用的:这张表存在的
	// 第一个问题是「哪个分组的哪个模型在被审」,而它的量级(抽中即一行)让
	// 全表扫在几百万行之后就不可接受。前缀 idx_qy_vai_model / _group 与
	// idx_qy_vai_created 组合,是列表页三种最常见筛法的直接支撑。
	ModelName  string `json:"model_name" gorm:"type:varchar(128);not null;default:'';index:idx_qy_vai_model,priority:1"`
	UsingGroup string `json:"using_group" gorm:"column:using_group;type:varchar(64);not null;default:'';index:idx_qy_vai_group,priority:1"`

	// Content 是**这一次真正送去审核的那一段文本**,脱敏与截断之后的副本。
	//
	// # 为什么必须是它,而不是"原始请求内容"
	//
	// 送审文本经过 reviewText 的头尾截断(max_input_chars),模型看到的就是这一段。
	// 存原文会让审核日志与判定依据对不上:一条判"未违规"的记录旁边放着一段
	// 明显违规的原文,而真相是那一段根本没被送出去 —— 那不是可用的排障材料,
	// 是误导。这一列的契约因此是「模型读到的是这些」。
	//
	// # 三道闸
	//
	//	内联二进制剥离  base64 图片一律换成描述符,与证据归档同一套(stripInlineBinary)
	//	脱敏           手机号/邮箱/密钥/证件号在**入库前**替换,库里从不存在未脱敏的原文
	//	字符上限       log_content_max_chars,与保留期一起决定这张表的体积上界
	//
	// # 零值:空串
	//
	// 三种情况都落空串,且都是正确的:管理员关掉了留存(log_content=false)、
	// 这一行写于本列存在之前、以及这一次压根没有可送审的内容。它们在界面上
	// 都显示成"未留存",不区分 —— 区分需要第四种状态,而那要么骗人要么没用。
	Content string `json:"content" gorm:"type:text"`
	// ContentChars 是送审文本**截断入库之前**的字符数。
	//
	// 它不是 len(Content):后者是留存的那一份,前者是模型读到的那一份。两者
	// 不等就说明日志里这一段是**残缺**的,界面必须据此打上"已截断"——
	// 没有这一列的话,一段被砍掉后半截的内容看起来与完整内容一模一样,
	// 而"后半截才是违规的那部分"恰恰是最常见的情形。
	ContentChars int `json:"content_chars" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_vai_user,priority:2;index:idx_qy_vai_created;index:idx_qy_vai_model,priority:2;index:idx_qy_vai_group,priority:2"`
}

func (AIReview) TableName() string { return "qy_violation_ai_review" }
