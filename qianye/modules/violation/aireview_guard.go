package violation

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// aireview_guard.go —— 护栏模型(guardrail model)这一条审核协议。
//
// ══════════════════════ 两条路,不是两套系统 ══════════════════════
//
// 本模块原有的那条路是「通用大模型 + 提示词工程」:发一段几百 token 的系统
// 提示词(判定口径 + 违规类型闭集),要求模型吐一个 JSON。它的长处是类型体系
// 完全由本站的违规类型表决定 —— 运营在类型页加一行,模型下一轮就认得它。
//
// 护栏模型是另一条路:它是**专门为安全分类微调**的小模型,不需要提示词工程,
// 直接吃对话、吐一组固定的安全标签。代表是阿里 Qwen 团队的 Qwen3Guard 系列
// (0.6B / 4B / 8B),项目方点名的 qwen3guard:0.6b 是它的 Ollama tag。
//
//	                   json_prompt(通用模型)        qwen3guard(护栏模型)
//	提示词             几百~上千 token,每次都付      不发提示词
//	输出               JSON,~50 token                两三行标签,~15 token
//	延迟               取决于模型规模,云端常 1~3s    0.6B 本地推理常 <200ms
//	                                                  (但首次调用要加载模型)
//	违规类型           本站类型表,运营可增删          模型训练时钉死的 9 类
//	判准                通用模型的常识 + 提示词        安全语料上专门微调
//	提示词注入          有敞口(送审内容是用户可控的    分类器不执行指令,
//	                    文本,只能靠提示词声明约束)    这一面天然小得多
//
// 两条路**并列**,不互相取代:护栏模型判不出"套系统提示词""批量蒸馏"这种
// 本站特有的违规,而通用模型在色情/自伤这类标准安全类目上又比 0.6B 的专用
// 模型贵一个数量级。同一个站点可以两个渠道都配上,靠权重分流或作用域指定。
//
// ══════════════════════ 协议依据:实读,不是推测 ══════════════════════
//
// 本文件的解析口径取自 **Wei-Shaw/sub2api** 的
// `backend/internal/securityaudit/prompt_qwen3guard.go`(项目方点名的参考实现),
// 与 Qwen 官方 README 的 extract_label_and_categories 相互印证。
//
// 传输层是 OpenAI 兼容的 `/chat/completions`,请求体只有五个字段:
//
//	{"model": …, "messages": [{"role":"user","content": <待检文本>}],
//	 "temperature": 0, "max_tokens": 64, "seed": 42}
//
// **没有 system 提示词,没有提示词工程,不包 <content> 标签** —— 安全指令
// 由模型自己的 chat template 注入,我们再塞一段进去只会往它的输入分布里掺
// 训练时没见过的东西。`Authorization` 仅在密钥非空时设置(自部署的 Ollama
// 通常不需要)。
//
// 响应是**纯文本,不是 JSON**,从 OpenAI 信封的 content 里按行取:
//
//	Safety: Safe | Controversial | Unsafe
//	Categories: <逗号分隔;None / N/A 视为空>
//	Refusal: Yes | No        ← 只有回答审核才有,不影响判定
//
// ══════════════════════ 与参考实现的三处刻意分歧 ══════════════════════
//
//  1. **分块 vs 截断**。sub2api 把长文本切块、每块发一次调用、再聚合。本仓
//     一次审核只发**一次**调用(见 aireview.go 顶部),超长内容由
//     reviewText 取头尾截断。理由是成本与延迟:分块把"一次抽样 = 一次调用"
//     变成"一次抽样 = N 次调用",而 N 由用户的输入长度决定 —— 那是一条
//     用户可以自己拉高的账单。截断的代价(中段内容不过审)由
//     max_input_chars 这一格显式承担,而它在界面上写着。
//  2. **失败方向**。sub2api 有 blocking 档(审核不可用即拒绝请求)。本仓
//     一律 fail-open,理由见 aireview.go 顶部。所以它的 ErrorCode* 那一套
//     在这里统一落成 OutcomeBadJSON / OutcomeUpstreamError / OutcomeTimeout。
//  3. **判定 → 处置**。sub2api 的 Allow/Warn/Block 是它自己的处置体系。
//     本仓已有 shadow/enforce、扣费、计次、封号那一整套,所以护栏模型的三档
//     输出接的是 aiVerdict{Violated, Confidence},再由既有规则决定处置。
//     映射与可配项见 guardPolicy。

const (
	// AIProtocolJSONPrompt 是原有的那条路:提示词 + JSON 结论。
	//
	// 它是**零值档**:AIChannel.Protocol 为空串(存量行 ADD COLUMN 之后的取值)
	// 一律归它,所以这一列的加入对任何既有站点都是逐字节无变化的。
	AIProtocolJSONPrompt = "json_prompt"
	// AIProtocolQwen3Guard 是护栏模型那条路:不发提示词,解析安全标签。
	AIProtocolQwen3Guard = "qwen3guard"
)

// normalizeAIProtocol 把空串与未知取值折回默认档。
//
// 未知取值也折回 json_prompt 而不是报错:这一列可能被 DBA 手工写坏,而
// 「不认识就按老路走」的最坏后果是多付一点提示词的钱;反过来(不认识就
// 拒绝调用)会让整个渠道静默失效,而失效在本模块一律等于放行。
func normalizeAIProtocol(p string) string {
	if strings.TrimSpace(p) == AIProtocolQwen3Guard {
		return AIProtocolQwen3Guard
	}
	return AIProtocolJSONPrompt
}

// aiProtocolValid 是**写入侧**的判据,比 normalizeAIProtocol 严格。
//
// 两者故意不同:写入时必须把拼错的取值当场 400 顶回去(运营看得到、改得掉),
// 运行期则必须容忍任何脏值(那时已经没有人能被告知了)。
func aiProtocolValid(p string) bool {
	switch p {
	case "", AIProtocolJSONPrompt, AIProtocolQwen3Guard:
		return true
	}
	return false
}

// ───────────────────────── 九个类别 ─────────────────────────

// Qwen3Guard 的九个安全类别。取值用 sub2api 的 snake_case id,而不是官方
// README 里的展示名("Non-violent Illegal Acts"):
//
//   - 它要进数据库列(AIChannel.GuardCategories,逗号分隔)、进接口、进前端
//     的 value 属性。带空格与 `&` 的展示名在这三处都要额外转义。
//   - 它同时是**运营可以拿来建类型的 key**(见 guardCategorySiteKey):本站
//     类型 key 的形状就是 snake_case。
//
// 展示名在 guardCategoryLabels 里,只用于界面与日志。
const (
	GuardCatViolent           = "violent"
	GuardCatNonViolentIllegal = "non_violent_illegal_acts"
	GuardCatSexual            = "sexual_content_or_sexual_acts"
	GuardCatPII               = "pii"
	GuardCatSelfHarm          = "suicide_and_self_harm"
	GuardCatUnethical         = "unethical_acts"
	GuardCatPolitical         = "politically_sensitive_topics"
	GuardCatCopyright         = "copyright_violation"
	GuardCatJailbreak         = "jailbreak"
)

// guardAllCategories 是九个类别的**固定顺序**(与官方 README 一致)。
//
// 顺序必须稳定:它决定界面上的排列、Reason 里的拼接、以及 guardCategoryKey
// 挑类型时的先后。按 map 迭代序输出会让同一次判定在两次进程里给出不同的
// 主类型,而主类型决定这一票加到谁的计数上。
var guardAllCategories = []string{
	GuardCatViolent,
	GuardCatNonViolentIllegal,
	GuardCatSexual,
	GuardCatPII,
	GuardCatSelfHarm,
	GuardCatUnethical,
	GuardCatPolitical,
	GuardCatCopyright,
	GuardCatJailbreak,
}

// guardCategoryLabels 是九个类别的官方展示名(英文)。中文名在前端 i18n 里,
// 不放这里:后端只有日志会用到它,而日志不做多语言。
var guardCategoryLabels = map[string]string{
	GuardCatViolent:           "Violent",
	GuardCatNonViolentIllegal: "Non-violent Illegal Acts",
	GuardCatSexual:            "Sexual Content or Sexual Acts",
	GuardCatPII:               "PII",
	GuardCatSelfHarm:          "Suicide & Self-Harm",
	GuardCatUnethical:         "Unethical Acts",
	GuardCatPolitical:         "Politically Sensitive Topics",
	GuardCatCopyright:         "Copyright Violation",
	GuardCatJailbreak:         "Jailbreak",
}

// guardCategoryAliases 把模型写出来的类别名归一到上面九个 id。
//
// ═══════════ 为什么是别名表而不是一条正则闭集 ═══════════
//
// 上一版用一条九选一的正则去 FindAllString。那样做有一个**看不见的**后果:
// 正则匹配不上的类别名压根不会出现在结果里 —— 模型回了 `Categories: Weapons`,
// 我们解析出零个类别,于是这一票的类型信息**静默消失**,而 Safety: Unsafe
// 还在,表现是"判了违规、没有类型、落兜底",与"模型说它归不了类"完全同形。
// 两者的处置人不同:前者要去看这个部署为什么吐了个表外的词(量化版本?
// 微调版?协议漂移?),后者不需要任何人做任何事。
//
// 现在:表里查不到的落进 guardLabels.Unknown,原样带到 Reason 与告警里。
// 归一规则与 sub2api 一致 —— 下划线、连字符、`&`、`/`、全角破折号一律折成
// 空格再压缩,查表;查不到就把空格换回下划线当作未知 id。
var guardCategoryAliases = map[string]string{
	"violent":  GuardCatViolent,
	"violence": GuardCatViolent,

	"non violent illegal acts": GuardCatNonViolentIllegal,
	"non violent illegal act":  GuardCatNonViolentIllegal,
	"illegal acts":             GuardCatNonViolentIllegal,

	"sexual content or sexual acts": GuardCatSexual,
	"sexual content":                GuardCatSexual,
	"sexual":                        GuardCatSexual,

	"pii":                               GuardCatPII,
	"personal identifying information":  GuardCatPII,
	"personal identifiable information": GuardCatPII,
	"privacy":                           GuardCatPII,

	"suicide and self harm": GuardCatSelfHarm,
	"suicide self harm":     GuardCatSelfHarm,
	"self harm":             GuardCatSelfHarm,

	"unethical acts": GuardCatUnethical,
	"unethical":      GuardCatUnethical,

	"politically sensitive topics": GuardCatPolitical,
	"politically sensitive":        GuardCatPolitical,
	"political":                    GuardCatPolitical,

	"copyright violation": GuardCatCopyright,
	"copyright":           GuardCatCopyright,

	"jailbreak":        GuardCatJailbreak,
	"prompt injection": GuardCatJailbreak,
}

// guardCategoryReplacer 把分隔符折成空格。`&` 折成 " and " 而不是空格:
// "Suicide & Self-Harm" 折成 "suicide self harm" 与 "suicide and self harm"
// 是两个不同的键,而别名表两个都收 —— 折成 " and " 让官方拼写走上前一条,
// 少一次"表里少写一个键就静默变未知"的机会。
var guardCategoryReplacer = strings.NewReplacer(
	"_", " ", "&", " and ", "/", " ", "-", " ", "–", " ", "—", " ",
)

// normalizeGuardCategory 归一一个类别名。返回 (id, 是否是已知的九类之一)。
func normalizeGuardCategory(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = guardCategoryReplacer.Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", false
	}
	if id, ok := guardCategoryAliases[s]; ok {
		return id, true
	}
	return strings.ReplaceAll(s, " ", "_"), false
}

// guardCategoryEmpty 认 sub2api 的两个空值写法。它们不是未知类别 ——
// 把 "None" 记成一个未知类别会让每一条 Safe 判定都刷一条告警。
func guardCategoryEmpty(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	return s == "" || s == "none" || s == "n/a" || s == "na" || s == "无"
}

// ───────────────────────── 三档输出 → 本站处置 ─────────────────────────

const (
	// GuardControversialSafe:把 Controversial 当作**未违规**。这是零值档。
	//
	// Qwen3Guard 比常见护栏模型多一档 Controversial(有争议),官方的说法是
	// 让站点按自己的尺度决定。默认取宽松档的理由与本模块其余部分一致:
	// 新增能力不得替站点收紧处置。收紧是一次显式动作,放宽不是。
	GuardControversialSafe = "safe"
	// GuardControversialSensitive 是 **sub2api 的那套策略**:Controversial
	// 本身不算违规,但命中"敏感类别"时升级成违规。
	//
	// 参考实现把这三类钉死在代码里(jailbreak / pii / suicide_and_self_harm,
	// 它的 isElevatedControversial)。本仓把清单做成渠道上的一格:三类是
	// **它们的**运营判断,不是协议的一部分 —— 一个只关心破限的站点会想去掉
	// pii,一个面向未成年人的站点会想加上 sexual。留空 = 用参考实现的三类。
	GuardControversialSensitive = "sensitive"
	// GuardControversialUnsafe:把 Controversial 一律当作违规,置信度 0.6。
	//
	// 选它之后仍然有第二道旋钮:规则上的 ai_min_confidence。填 > 0.6 的规则
	// 只吃 Unsafe(0.95),填 <= 0.6 的连 Controversial 一起吃。
	GuardControversialUnsafe = "unsafe"
)

// guardDefaultElevated 是 sub2api 的 isElevatedControversial 那三类。
// 它是 sensitive 档留空时的取值,不是硬编码 —— 见 GuardControversialSensitive。
var guardDefaultElevated = []string{GuardCatJailbreak, GuardCatPII, GuardCatSelfHarm}

func normalizeGuardControversial(v string) string {
	switch strings.TrimSpace(v) {
	case GuardControversialUnsafe:
		return GuardControversialUnsafe
	case GuardControversialSensitive:
		return GuardControversialSensitive
	}
	return GuardControversialSafe
}

func guardControversialValid(v string) bool {
	switch v {
	case "", GuardControversialSafe, GuardControversialSensitive, GuardControversialUnsafe:
		return true
	}
	return false
}

const (
	// guardMaxTokens 是护栏模型这条路的输出上限,与参考实现一致。
	//
	// 结论只有两三行标签,64 token 绰绰有余。它比 json_prompt 那条路的 256
	// 低一档不是抠门:护栏模型没有"多写几句解释"的用法,给它 256 只会让一个
	// 跑飞的部署有机会吐 256 token 的垃圾,而那 256 个 token 我们照付。
	guardMaxTokens = 64

	// guardSeed 是请求上的 seed,与参考实现一致(42)。
	//
	// 它不是仪式:护栏模型的输出是几个离散标签,而采样噪声足以让同一段内容
	// 在两次调用里落到不同的档上。审核结果要能复现 —— 用户申诉时"再跑一次
	// 看看"必须给出同一个答案,否则申诉流程无法收敛。temperature=0 在多数
	// 实现里已经足够,但 vLLM/Ollama 在 batch 调度下仍有非确定性,seed 是
	// 第二道保险。不支持 seed 的实现会忽略这个字段(OpenAI 兼容层的惯例)。
	guardSeed = 42

	// guardConfidenceUnsafe / guardConfidenceControversial 是把三档标签
	// 映射到本仓 [0,1] 置信度的取值。
	//
	// 护栏模型**不返回置信度**,所以这两个数是我们赋的、代表档位而不是概率。
	// 它们必须落在 0.6 与 0.95 这样一个"中间留得下阈值"的位置上:规则的
	// ai_min_confidence 是既有的旋钮,运营填 0.8 就得到"只吃 Unsafe",
	// 填 0.5 就得到"Controversial 也吃"。两个数挨得太近会让那个旋钮失效。
	guardConfidenceUnsafe        = 0.95
	guardConfidenceControversial = 0.6

	// guardUnknownRunes / guardUnknownMax 限制未知类别名进 Reason 的体量。
	//
	// 未知类别名是**模型自由生成的文本**,不是闭集取值 —— 一个跑飞的部署
	// 可以把半段用户原文塞进 `Categories:` 那一行。它仍然会过 redactSnippet
	// (见 callAIChannel),但先按码点截断、只留前几个,是不让一行 512 字的
	// Reason 全被一个坏部署占满。
	guardUnknownRunes = 32
	guardUnknownMax   = 3
)

// errGuardNoSafetyLabel 是"回复里没有 Safety 标签"。
//
// 它与 errGuardInvalidResponse 都被翻译成 OutcomeBadJSON,而不是各占一个新的
// outcome 取值。理由:outcome 这一列的分档标准是「处置人不同」(见
// aireview_model.go),而这两种失败的处置人是同一个 —— 去看渠道的协议与地址
// 配错没有。多一个取值只会让成本页和统计卡多一根谁也解释不清的柱子,还要在
// 前端 QyAiOutcome 联合类型、i18n 七语种、统计聚合三处各加一份。
// (排查用的原文由管理端试跑的 raw_response 回显,那才是真正需要的粒度。)
var errGuardNoSafetyLabel = errors.New("护栏模型回复里没有 Safety 标签")

// errGuardInvalidResponse 是"有标签但形状不对":Safety 取值不在三档里、
// 同一个字段出现两次、或者**缺 Categories 整行**。
//
// 缺 Categories 判错而不是当作空 —— 这一条来自参考实现,而它的理由值得写下来:
// 一个只回 `Safety: Safe` 的端点,与一个真的判了 Safe 且类别为 None 的端点,
// 在"当作空"的口径下完全同形。前者是协议对不上(多半是地址指到了一个通用
// 模型,它随口说了句 "Safety: Safe" 就完了),而它会让这个渠道**永远放行**。
// 判错则会走 fail-open + 落一行 bad_json,于是查得出来。
var errGuardInvalidResponse = errors.New("护栏模型回复的形状不对(标签重复、取值非法或缺少 Categories 行)")

// guardPolicy 是一个护栏渠道上的判定策略,已经归一。
//
// 它在**渠道**上而不是全局:同一个站点完全可能同时挂一个严格档的自建护栏
// 模型和一个宽松档的云端护栏模型,而全局一个开关会让两者只能取同一个尺度。
type guardPolicy struct {
	// Controversial 恒是 safe / sensitive / unsafe 三者之一。
	Controversial string
	// Enabled 是**启用的类别子集**。nil 表示九类全启用(零值档,也是这一格
	// 加入之前的唯一行为)。
	//
	// 它对应参考实现的 enabledScanners。停用一个类别不等于"当它没发生":
	// 见 toVerdict —— Unsafe 且解析出的类别全被停用时,判定仍然成立,只是
	// 置信度降到 0.6,由规则的 ai_min_confidence 决定要不要吃。静默丢弃一票
	// Unsafe 是本模块最不能接受的一种失效。
	Enabled map[string]struct{}
	// Elevate 是 Controversial = sensitive 档下"命中即升级"的类别。
	// nil 表示用 guardDefaultElevated(参考实现的三类)。
	Elevate map[string]struct{}
}

func (p guardPolicy) enabled(id string) bool {
	if p.Enabled == nil {
		return true
	}
	_, ok := p.Enabled[id]
	return ok
}

func (p guardPolicy) elevated(id string) bool {
	if p.Elevate == nil {
		for _, d := range guardDefaultElevated {
			if d == id {
				return true
			}
		}
		return false
	}
	_, ok := p.Elevate[id]
	return ok
}

// parseGuardCategoryList 把库里那一列(逗号分隔)读成集合。
//
// 空串 → nil,而 nil 的含义由字段自己定(Enabled: 全启用;Elevate: 内置三类)。
// 这是本文件里唯一一处"零值不是空集合"的地方,所以两个字段的注释都写明了。
func parseGuardCategoryList(csv string) map[string]struct{} {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(csv, ",") {
		id, known := normalizeGuardCategory(part)
		if !known {
			// 库里的脏值直接跳过。运行期不报错,与 normalizeAIProtocol 同口径:
			// 写入侧已经 400 拦过一次,能走到这里的只有手工改库。
			continue
		}
		out[id] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// formatGuardCategoryList 把集合写回那一列,按 guardAllCategories 的固定顺序。
// 顺序固定,否则每次保存都会产生一次内容相同、字节不同的写入与审计差异。
func formatGuardCategoryList(set map[string]struct{}) string {
	if len(set) == 0 {
		return ""
	}
	out := make([]string, 0, len(set))
	for _, id := range guardAllCategories {
		if _, ok := set[id]; ok {
			out = append(out, id)
		}
	}
	return strings.Join(out, ",")
}

// guardPolicyFromChannel 把库里那一行折成运行期策略。装配期调用一次。
func guardPolicyFromChannel(row AIChannel) guardPolicy {
	return guardPolicy{
		Controversial: normalizeGuardControversial(row.GuardControversial),
		Enabled:       parseGuardCategoryList(row.GuardCategories),
		Elevate:       parseGuardCategoryList(row.GuardElevate),
	}
}

// canonicalGuardCategoryCSV 是**写入侧**的校验与归一:拒绝表外的类别名,
// 去重,按 guardAllCategories 排序。
//
// 与 parseGuardCategoryList(运行期,静默跳过脏值)刻意不同,理由与
// aiProtocolValid 那一对完全一样:保存那一刻还有人看得到错误消息,
// 热路径上没有。
func canonicalGuardCategoryCSV(csv, field string) (string, error) {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return "", nil
	}
	set := map[string]struct{}{}
	for _, part := range strings.Split(csv, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		id, known := normalizeGuardCategory(part)
		if !known {
			return "", errors.New(field + " 里有不认识的类别 " + strconv.Quote(strings.TrimSpace(part)) +
				" —— 只能取 Qwen3Guard 的九类之一:" + strings.Join(guardAllCategories, "、"))
		}
		set[id] = struct{}{}
	}
	return formatGuardCategoryList(set), nil
}

// guardLabels 是护栏模型返回的那几行标签,**原样**保存。
//
// 归一化(映射到本站类型 key、折成布尔)放在 toVerdict 里做,解析这一步只负责
// 忠实还原模型说了什么 —— 试跑时回显的正是这一层,而归一之后的值答不出
// "模型到底回的是哪个词"。
type guardLabels struct {
	// Safety 恒是 safe / controversial / unsafe(小写归一后)。
	Safety string
	// Categories 是已知的九类之一,按 guardAllCategories 的固定顺序、已去重。
	Categories []string
	// Unknown 是**表外**的类别名(已归一成 snake_case),按字典序、已去重。
	// 它不参与判定,但必须留痕 —— 见 guardCategoryAliases 的说明。
	Unknown []string
	// Refusal 是 Yes / No,只有回答审核才有。空串表示这一次没有这一行。
	// 参考实现直接忽略这一行;我们解析并留在 Reason 里,免得日后有人把渠道
	// 指向 Stream / 回答审核变体时,看不出返回形状其实变了。
	Refusal string
}

// guardFieldValue 认一行是不是 `<name>:` 开头的字段行,并返回冒号右边那一段。
//
// 三处容错,每一处都对应一种真实见过的输出:
//
//	前缀 `#` / `-` / `*` / `>`   官方博客示例里带 `#`;一些量化版会拿 markdown
//	                             列表符号起头。
//	全角冒号 `:`                 中文语料上微调过的部署会写全角。
//	名字与冒号之间的空格         `Safety :` —— 分词器切出来的空格。
//
// 容错只放在**分隔符**上,不放在取值上:取值一律精确匹配三个档
// (见 parseGuardVerdict)。`Safety: Safe-ish` 必须是非法响应,而不是被
// 前缀匹配成 Safe —— 上一版的正则 `(safe|unsafe|controversial)` 恰好会把它
// 读成 Safe,也就是把一个形状不对的响应静默变成一次放行。
func guardFieldValue(line, name string) (string, bool) {
	rest := strings.TrimLeft(line, "#-*> \t")
	if len(rest) < len(name) || !strings.EqualFold(rest[:len(name)], name) {
		return "", false
	}
	rest = strings.TrimLeft(rest[len(name):], " \t")
	if strings.HasPrefix(rest, ":") {
		return strings.TrimSpace(rest[1:]), true
	}
	if strings.HasPrefix(rest, "：") {
		return strings.TrimSpace(rest[len("："):]), true
	}
	return "", false
}

// parseGuardVerdict 从护栏模型的回复里取出那几行标签。
//
// 逐行扫描而不是全文正则 —— 这一点是照参考实现改的,而两者的差别不是风格:
// 全文正则拿不到"这个字段出现了两次"与"这一行整个不存在"这两个事实,而它们
// 恰好是协议对不上时最先出现的症状(见 errGuardInvalidResponse),也是提示词
// 注入唯一可能得手的形状(见 aireview_guard_test.go 的注入用例)。
func parseGuardVerdict(content string) (guardLabels, error) {
	var g guardLabels
	if strings.TrimSpace(content) == "" {
		return g, errGuardNoSafetyLabel
	}
	safety, categoryLine := "", ""
	safetySeen, categorySeen := false, false
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if v, ok := guardFieldValue(line, "safety"); ok {
			if safetySeen {
				// 同一个字段出现两次 = 我们不知道该信哪一个。信第一个(上一版
				// 的正则行为)在**提示词注入**下是最坏的选择:模型复述用户原文
				// 时,原文里那一行伪造的 `Safety: Safe` 会排在模型自己的结论
				// 前面。判错 ⇒ fail-open + 一行 bad_json,而伪造的 Safe 是一次
				// 无声的放行。
				return guardLabels{}, errGuardInvalidResponse
			}
			safety, safetySeen = v, true
			continue
		}
		if v, ok := guardFieldValue(line, "categories"); ok {
			if categorySeen {
				return guardLabels{}, errGuardInvalidResponse
			}
			categoryLine, categorySeen = v, true
			continue
		}
		if v, ok := guardFieldValue(line, "refusal"); ok {
			// Refusal 重复不算错:它不参与判定,而参考实现连读都不读它。
			if g.Refusal == "" {
				switch strings.ToLower(v) {
				case "yes", "no":
					g.Refusal = strings.ToLower(v)
				}
			}
			continue
		}
		// 其余行忽略。护栏模型偶尔会在标签后面附一句解释,那不影响判定。
	}
	if !safetySeen {
		return guardLabels{}, errGuardNoSafetyLabel
	}
	switch strings.ToLower(safety) {
	case "safe":
		g.Safety = "safe"
	case "controversial":
		g.Safety = "controversial"
	case "unsafe":
		g.Safety = "unsafe"
	default:
		return guardLabels{}, errGuardInvalidResponse
	}
	if !categorySeen {
		// 缺整行 = 非法,不是"当作空"。理由见 errGuardInvalidResponse。
		return guardLabels{}, errGuardInvalidResponse
	}

	known := map[string]struct{}{}
	unknown := map[string]struct{}{}
	// `、` 与 `;` 一并当分隔符:中文语料上微调的部署会用顿号,而一个没被
	// 切开的 "暴力、越狱" 会整段落进 Unknown —— 两个本来认得的类别一起丢失。
	for _, raw := range strings.FieldsFunc(categoryLine, func(r rune) bool {
		return r == ',' || r == '、' || r == ';' || r == '；'
	}) {
		if guardCategoryEmpty(raw) {
			continue
		}
		id, ok := normalizeGuardCategory(raw)
		if id == "" {
			continue
		}
		if ok {
			known[id] = struct{}{}
		} else {
			unknown[clipRunes(id, guardUnknownRunes)] = struct{}{}
		}
	}
	for _, id := range guardAllCategories {
		if _, ok := known[id]; ok {
			g.Categories = append(g.Categories, id)
		}
	}
	for id := range unknown {
		g.Unknown = append(g.Unknown, id)
	}
	sort.Strings(g.Unknown)
	if len(g.Unknown) > guardUnknownMax {
		g.Unknown = g.Unknown[:guardUnknownMax]
	}
	return g, nil
}

// guardCategoryKeys 是 Qwen3Guard 的固定类别 → 本站违规类型 key 的**内置**映射。
//
// ══════════════ 为什么必须有一张表,而不能像 json_prompt 那样把类型清单
// 发给模型 ══════════════
//
// json_prompt 那条路的类型闭集是**运行期算出来发过去**的(见 aireview_vocab.go),
// 运营在类型页加一行,模型下一轮就认得它。护栏模型没有这个自由度:它的 9 个
// 类别在训练时就钉死了,提示词改不动。所以两边的词汇表只能靠映射对齐。
//
// 右侧取的都是本仓 seedCategories 里已经存在的 key —— 一个刚部署的站点接上
// qwen3guard,九类里有六类当场落到正确的类型上。
//
// 剩下三类(unethical_acts / politically_sensitive_topics / copyright_violation)
// 本站没有对应类型,**刻意不硬塞给一个语义最近的**:塞错的后果是那一类的计数
// 混进另一类,而计数正在决定谁会被封号。它们在这张表里是空串,由
// guardCategorySiteKey 走"用 guard 类别 id 本身当 key"那一条 —— 运营在违规
// 类型页建一行 key = politically_sensitive_topics 的类型就接上了,不需要改代码。
var guardCategoryKeys = map[string]string{
	GuardCatViolent:           CatViolentExtreme,
	GuardCatNonViolentIllegal: CatIllegalGoods,
	GuardCatSexual:            CatSexual,
	GuardCatPII:               CatPrivacyDoxxing,
	GuardCatSelfHarm:          CatSelfHarm,
	GuardCatJailbreak:         CatJailbreak,
	GuardCatUnethical:         "",
	GuardCatPolitical:         "",
	GuardCatCopyright:         "",
}

// guardCategorySiteKey 把一个护栏类别落到本站违规类型的 key 上。
//
// ══════════════ 映射机制:三级,全都不需要改代码 ══════════════
//
//  1. 内置映射目标存在于本站类型表   → 用它。六类开箱即用。
//  2. 否则,guard 类别 id 本身是一个本站类型 key → 用它。
//     这是**运营的自助通道**:在违规类型页建一行 key = copyright_violation
//     的类型,这一类就有了独立的计数、阈值与处置动作。界面上的对照表
//     (aiGuardCategoryView)会把这一位显示出来。
//  3. 都不成立 → 返回一个**候选 key**(内置目标优先,没有就用类别 id),
//     交给 vocab.resolveCategory 落兜底「未分类」,原值进 AIReview.RawCategory,
//     并打一条指名道姓的告警。静默丢弃在本模块一律不接受。
//
// 为什么第 1 级优先于第 2 级:内置目标是本站 seed 里那个语义对得上的类型
// (violent → violent_extremism),而它多半已经绑好了规则与阈值。一个恰好
// 也叫 violent 的自建类型是个巧合,不该抢走已经在生效的那一条链。
func guardCategorySiteKey(id string, vocab aiVocabulary) (key string, resolved bool) {
	builtin := guardCategoryKeys[id]
	if builtin != "" && vocab.known(builtin) {
		return builtin, true
	}
	if vocab.known(id) {
		return id, true
	}
	if builtin != "" {
		return builtin, false
	}
	return id, false
}

// guardCategoryMapping 是管理端那份对照表的一行:护栏的类别 → 本站的类型。
// 顺序固定(guardAllCategories),否则每次刷新界面都换一个排法。
type guardCategoryMapping struct {
	// Id 是九类的 snake_case id,也是接口与前端用的 value。
	Id string
	// Label 是官方展示名。
	Label string
	// Key 是这一类会落到的本站类型 key(第 1/2 级的结果,或第 3 级的候选)。
	Key string
	// Present 为真表示本站真的有这个类型 —— 也就是这一类不会落兜底。
	Present bool
}

func guardCategoryMappings(vocab aiVocabulary) []guardCategoryMapping {
	out := make([]guardCategoryMapping, 0, len(guardAllCategories))
	for _, id := range guardAllCategories {
		key, ok := guardCategorySiteKey(id, vocab)
		out = append(out, guardCategoryMapping{
			Id: id, Label: guardCategoryLabels[id], Key: key, Present: ok,
		})
	}
	return out
}

// guardCategoryKey 从模型给出的一组类别里挑一个落到本站类型 key 上。
//
// 挑法:**优先挑本站类型表里真实存在的那一个**。模型可以一次给多个类别
// (`Categories: Violent, Jailbreak`),而本仓的一条记录只有一个类型;
// 挑一个存在的,规则的类型过滤才可能命中 —— 恒挑第一个的话,一次
// "Copyright Violation, Jailbreak" 会因为第一个类型本站没有而整票掉进兜底,
// 尽管第二个类型本站建得好好的、还配着规则。
//
// 一个已知类别都没有(全是表外的词)时,拿**第一个未知类别名**当候选:
// 它注定要落兜底,但带着 "weapons" 这样一个词落兜底,RawCategory 上留下的
// 就是一句能照着做的话,而不是一个空串。
func guardCategoryKey(g guardLabels, p guardPolicy, vocab aiVocabulary) string {
	first := ""
	for _, id := range g.Categories {
		if !p.enabled(id) {
			continue
		}
		key, ok := guardCategorySiteKey(id, vocab)
		if first == "" {
			first = key
		}
		if ok {
			return key
		}
	}
	if first != "" {
		return first
	}
	// 启用清单把已知类别全过滤掉了,或者压根没有已知类别。前者仍然要给一个
	// 候选(判定成立,只是置信度降档,见 toVerdict),所以停用的类别在这里
	// 仍然参与挑选 —— 它只影响置信度与升级,不影响"这一票叫什么"。
	for _, id := range g.Categories {
		key, ok := guardCategorySiteKey(id, vocab)
		if ok {
			return key
		}
		if first == "" {
			first = key
		}
	}
	if first != "" {
		return first
	}
	if len(g.Unknown) > 0 {
		return g.Unknown[0]
	}
	return ""
}

// toVerdict 把三档标签折成本模块通用的结论形态。
//
// ══════════════ 三档 → 本站处置的完整映射 ══════════════
//
//	Safe          未违规,置信度 0。
//	Unsafe        违规。置信度 0.95;但若"解析出了已知类别、却全部被停用、
//	              而且没有未知类别",降到 0.6 —— 这是参考实现里
//	              Warn 那一档的等价物。**不降成未违规**:一票 Unsafe 被
//	              启用清单静默吃掉,是本模块最不能接受的失效。降档之后
//	              规则上的 ai_min_confidence 就是那个旋钮。
//	Controversial 由渠道上的 Controversial 那一格决定:
//	                safe       未违规(零值档)。置信度仍记 0.6 ——
//	                           它是"这一档尺度放过了多少擦边内容"唯一的痕迹。
//	                sensitive  命中"敏感类别"才算违规(参考实现的策略),
//	                           升级后置信度 0.95;没命中则同 safe。
//	                unsafe     一律违规,置信度 0.6。
//
// 处置动作(shadow / enforce、扣费、计次、封号)一概不在这里决定 ——
// 它们由既有的规则体系按 Violated + Category + Confidence 算出来。护栏协议
// 因此不引入任何第二套处置语义,这是它能"并列"而不是"替换"的前提。
func (g guardLabels) toVerdict(p guardPolicy, vocab aiVocabulary) aiVerdict {
	v := aiVerdict{Reason: g.summary(), GuardUnknown: g.Unknown}
	switch g.Safety {
	case "unsafe":
		v.Violated = true
		v.Confidence = guardConfidenceUnsafe
		if len(g.Categories) > 0 && len(g.Unknown) == 0 && !g.anyEnabled(p) {
			v.Confidence = guardConfidenceControversial
		}
	case "controversial":
		v.Confidence = guardConfidenceControversial
		switch p.Controversial {
		case GuardControversialUnsafe:
			v.Violated = true
		case GuardControversialSensitive:
			for _, id := range g.Categories {
				if p.enabled(id) && p.elevated(id) {
					v.Violated = true
					v.Confidence = guardConfidenceUnsafe
					break
				}
			}
		}
	default:
		// Safe。置信度留 0 —— 未违规时这一列不参与任何判定。
	}
	if v.Violated {
		v.Category = guardCategoryKey(g, p, vocab)
	}
	return v
}

func (g guardLabels) anyEnabled(p guardPolicy) bool {
	for _, id := range g.Categories {
		if p.enabled(id) {
			return true
		}
	}
	return false
}

// summary 是落进 AIReview.Reason 的那一句。
//
// 除 unknown= 之外**只由闭集里的取值拼成**(Safety 三选一、类别九选一、
// Refusal 二选一)。unknown= 那一段是模型自由生成的文本,所以它在
// parseGuardVerdict 里已经按码点截断、限了条数,而调用方还会再过一次
// redactSnippet(与 json_prompt 那条路的 reason 同规格)。
//
// 未知类别**必须**出现在这里:它是"这个部署吐了个表外的词"这件事在明细表上
// 唯一查得到的痕迹 —— RawCategory 只在整票落兜底时才有值,而一次
// "Violent, Weapons" 会挑中 Violent 落到真类型上,那一列就是空的。
func (g guardLabels) summary() string {
	parts := make([]string, 0, 4)
	parts = append(parts, "qwen3guard safety="+g.Safety)
	if len(g.Categories) > 0 {
		parts = append(parts, "categories="+strings.Join(g.Categories, ","))
	}
	if len(g.Unknown) > 0 {
		parts = append(parts, "unknown="+strings.Join(g.Unknown, ","))
	}
	if g.Refusal != "" {
		parts = append(parts, "refusal="+g.Refusal)
	}
	return strings.Join(parts, "; ")
}
