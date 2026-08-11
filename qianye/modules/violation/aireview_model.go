package violation

import "github.com/shopspring/decimal"

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

	KeyNonce   []byte `json:"-" gorm:"type:varbinary(32)"`
	KeyCipher  []byte `json:"-" gorm:"type:varbinary(512)"`
	KeyVersion int    `json:"key_version" gorm:"not null;default:0"`
	// KeyHint 是掩码提示(如 "****a1b2"),写入时算好存下来。
	// 不在读取时现算:那需要先解密,而列表接口没有任何理由碰明文密钥。
	KeyHint string `json:"key_hint" gorm:"type:varchar(32);not null;default:''"`

	// TimeoutMs 是这个渠道单次调用的超时。0 = 用当前时机的全局预算。
	// 渠道级存在的理由:同一站点可能同时挂一个本地小模型(50ms)与一个云端
	// 大模型(2s),用一个数字卡住两者,要么本地的白等、要么云端的必超时。
	TimeoutMs int `json:"timeout_ms" gorm:"not null;default:0"`
	// Weight 是加权随机的权重,>= 1。多渠道时按权重分流,而不是永远打第一个。
	Weight  int  `json:"weight" gorm:"not null;default:1"`
	Enabled bool `json:"enabled" gorm:"not null;default:false"`

	// PriceInPerM / PriceOutPerM 是每百万 token 的美元单价,用于把 token 换算成
	// 花费。0 表示"没填",此时 cost_usd 恒为 0,界面会把这一列标成"单价未配"——
	// 刻意不猜一个默认价:猜错的成本数字比没有数字更糟,它会被当成真的。
	PriceInPerM  decimal.Decimal `json:"price_in_per_m" gorm:"type:decimal(18,8);not null;default:0"`
	PriceOutPerM decimal.Decimal `json:"price_out_per_m" gorm:"type:decimal(18,8);not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(512);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null"`
	UpdatedBy int    `json:"updated_by" gorm:"not null;default:0"`
}

func (AIChannel) TableName() string { return "qy_violation_ai_channel" }

// HasKey 供接口下发:界面据此显示"已配置密钥 / 未配置",而不是显示密钥本身。
func (ch AIChannel) HasKey() bool { return len(ch.KeyCipher) > 0 }

// AISetting 是 AI 审核的全局设置,单行表(Id 恒为 1)。
//
// # 为什么是全局而不是挂在规则上
//
// 抽样率是**成本闸门**,它只有一个:三条 ai_review 规则各配 30% 不等于
// "总共 30%",而运营心里想的一定是后者。提示词与超时同理 —— 一次请求只发
// 一次审核调用(见 aireview.go),多条规则共用同一份结论,那么这些参数本来
// 就只可能有一份。挂到规则上会造出 N 份必须手工保持一致的拷贝。
//
// # 抽样率用万分比整数
//
// 与本仓其余比率字段同口径(5% = 500)。浮点百分比在"抽样"这种要复现的地方
// 是纯粹的负担:0.1 往返一次 JSON 就不再是 0.1。
type AISetting struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement:false"` // 恒为 1

	// Enabled 是 AI 审核的总开关。关掉之后 ai_review 规则一条都不会命中
	// (快照直接不装配它们),热路径连一次随机数都不摇。
	Enabled bool `json:"enabled" gorm:"not null;default:false"`

	// SampleRateBps 是抽样率,0..10000(万分比)。0 = 不抽,整条路径零开销。
	SampleRateBps int `json:"sample_rate_bps" gorm:"not null;default:0"`

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
	ThirdPartyNoticeAck bool `json:"third_party_notice_ack" gorm:"not null;default:false"`

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
	Violated   bool            `json:"violated" gorm:"not null;default:false"`
	Category   string          `json:"category" gorm:"type:varchar(64);not null;default:''"`
	Confidence decimal.Decimal `json:"confidence" gorm:"type:decimal(5,4);not null;default:0"`
	// Reason 是模型给的理由。它可能复述用户原文,所以与 Record.MatchSnippet
	// 同规格做脱敏(redactSnippet)之后再落库。
	Reason string `json:"reason" gorm:"type:varchar(512);not null;default:''"`

	PromptTokens     int `json:"prompt_tokens" gorm:"not null;default:0"`
	CompletionTokens int `json:"completion_tokens" gorm:"not null;default:0"`
	TotalTokens      int `json:"total_tokens" gorm:"not null;default:0"`
	// CostUsd 由渠道单价 × token 算出。渠道没填单价时恒为 0,
	// 界面据 priced 标记区分"没花钱"与"不知道花了多少"。
	CostUsd   decimal.Decimal `json:"cost_usd" gorm:"type:decimal(18,8);not null;default:0"`
	LatencyMs int             `json:"latency_ms" gorm:"not null;default:0"`

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
