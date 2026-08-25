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

	// Prompt 是审核提示词。空串时回落到 defaultAIPrompt ——
	// "可配,但要有一个能用的默认值"。
	Prompt string `json:"prompt" gorm:"type:text"`

	// MaxInputChars 是送审内容的字符上限。它既是成本闸(按 token 计费),
	// 也是隐私闸(送出去的越少越好)。0 时回落到 defaultAIMaxInputChars。
	MaxInputChars int `json:"max_input_chars" gorm:"not null;default:0"`

	// ThirdPartyNoticeAck 记录管理员是否已经确认"用户请求内容会被发往第三方"。
	//
	// 它是一个**必须显式勾过一次**的闸:未确认时保存 enabled=true 会被 400 拒绝。
	// 不做成纯前端提示,是因为纯前端的提示在下一次改版里会被顺手删掉,
	// 而"用户内容出境"这件事需要一条查得到的记录(它连同审计一起留痕)。
	ThirdPartyNoticeAck bool `json:"third_party_notice_ack" gorm:"not null"`

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

	RequestId  string `json:"request_id" gorm:"type:varchar(64);not null;default:'';index:idx_qy_vai_reqid"`
	ModelName  string `json:"model_name" gorm:"type:varchar(128);not null;default:''"`
	UsingGroup string `json:"using_group" gorm:"column:using_group;type:varchar(64);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_vai_user,priority:2;index:idx_qy_vai_created"`
}

func (AIReview) TableName() string { return "qy_violation_ai_review" }
