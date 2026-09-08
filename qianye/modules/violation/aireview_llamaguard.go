package violation

import (
	"errors"
	"sort"
	"strings"
)

// aireview_llamaguard.go —— Meta Llama Guard 这条审核协议。
//
// # 它凭什么值得第四条协议
//
// 现有三条各有各的形状,而 Llama Guard 哪一条都对不上:
//
//	json_prompt       通用模型 + 提示词,吐 JSON
//	qwen3guard        吐 `Safety: …` / `Categories: …` 两行标签
//	granite_guardian  只吐一个词:Yes 或 No
//	llama_guard       吐 `safe`,或者 `unsafe` + 换行 + 一行逗号分隔的 S 码
//
// 更要紧的是它**一次调用就给出类别**,而 Granite 那条路只有二值 —— 项目方
// 问"有没有准确性高一点又不太大的",Llama-Guard-3-1B / 8B 正是这一档里
// 少数几个既小又出类别的。少了这条适配,把它配上去的后果不是报错而是
// **静默失效**:三种解析全部失败 → fail-open 放行 + 落一行 bad_json。
//
// # 复用的是哪一层
//
// 解析出来的是 guardLabels —— 与 qwen3guard **同一个结构体**。于是它下游的
// 一切原样生效:九类启用子集、类别 → 本站类型的三级解析、置信度降档、
// 未知类别告警、以及规则体系里的 ai_min_confidence。这条协议因此没有引入
// 任何第二套处置语义,与 qwen3guard 是并列关系而不是特例。
//
// 唯一不适用的是 Controversial 那一档:Llama Guard 只有 safe / unsafe 两态,
// 没有"有争议"。写入侧(validateAIChannel)把那一格清空,与 granite 同处置。
//
// # 三个已知边界,都写在这里而不是藏在代码里
//
//  1. **S 码的编号在不同代际之间会变。** Llama Guard 2 的 S5 是
//     Specialized Advice,而 Llama Guard 3/4 的 S5 是 Defamation。本表按
//     **3/4 代**(MLCommons 分类法)映射 —— 那是现在实际在用的那一版。
//     配 2 代模型时,少数几类会被记到相邻的桶里。
//     **它只影响"记成哪一类",不影响"拦不拦"** —— 后者只看 safe/unsafe。
//  2. **九类装不下十三类。** 下游那套九类词表是 Qwen3Guard 的分类法,而
//     Llama Guard 有 13(+1)类。映射因此是**多对一**的:S4(儿童性剥削)
//     折进 sexual、S10(仇恨)折进 unethical_acts —— 九类里没有它们的槽。
//     折进去之后原始 S 码**仍然留在 Reason 里**,所以取证时不丢信息;
//     但如果你要"未成年人""仇恨"各自成一档去算封号线,请用 qwen3guard
//     或 json_prompt 的通用模型。
//  3. **它判的是对话,不是一段文本。** 官方模板区分 prompt 分类与 response
//     分类,而本模块只有请求内容这一个挂载点(见 PhasePostAsync 的说明),
//     所以送过去的恒是一条 user 消息 —— 也就是 prompt 分类那一档。

// AIProtocolLlamaGuard 是 Meta Llama Guard 那条路:不发提示词,
// 解析 `safe` / `unsafe\nS1,S10` 形状的回复。
const AIProtocolLlamaGuard = "llama_guard"

// llamaGuardMaxTokens 是回复的 token 上界。
//
// 比 granite 的 8 大、比 qwen3guard 的预算小:最长的合法回复是
// `unsafe` + 换行 + 十几个 S 码,三十来个 token 足够。给大了不会更准,
// 只会让一个跑飞的部署(比如地址指到了通用模型)把钱烧在这条路上。
const llamaGuardMaxTokens = 48

// errLlamaGuardInvalidResponse 是"回复不是 safe / unsafe 那两种形状"。
//
// 与另外两条护栏协议同一条哲学:**宁可可见地失败,也不要静默地判错**。
// 不做包含匹配(把 "I'm sorry, that would be unsafe" 读成违规)—— 那种写法
// 会把一个地址指错、后面挂着通用模型的渠道变成一台随机拦截器。
// 这里返回错误会走 fail-open + 落一行 bad_json,查得出来。
var errLlamaGuardInvalidResponse = errors.New(
	"Llama Guard 回复的形状不对(期望首行恰好是 safe 或 unsafe)")

// llamaGuardCodes 把 S 码折成下游那套九类 id。
//
// 按 Llama Guard 3 / 4 的 MLCommons 分类法。多对一是刻意的,理由见文件头 ②。
var llamaGuardCodes = map[string]string{
	"s1":  GuardCatViolent,           // Violent Crimes
	"s2":  GuardCatNonViolentIllegal, // Non-Violent Crimes
	"s3":  GuardCatSexual,            // Sex-Related Crimes
	"s4":  GuardCatSexual,            // Child Sexual Exploitation —— 九类里没有独立的槽
	"s5":  GuardCatUnethical,         // Defamation
	"s6":  GuardCatUnethical,         // Specialized Advice
	"s7":  GuardCatPII,               // Privacy
	"s8":  GuardCatCopyright,         // Intellectual Property
	"s9":  GuardCatViolent,           // Indiscriminate Weapons
	"s10": GuardCatUnethical,         // Hate —— 同样没有独立的槽
	"s11": GuardCatSelfHarm,          // Suicide & Self-Harm
	"s12": GuardCatSexual,            // Sexual Content
	"s13": GuardCatPolitical,         // Elections
	"s14": GuardCatNonViolentIllegal, // Code Interpreter Abuse(3 代 8B 工具版才有)

	// O 码是 Llama Guard **1 代**的写法。它不是 S 码的别名 —— 1 代只有六类,
	// 而且分法完全不同(O1 把暴力与仇恨合在一起)。所以这里是独立一张表,
	// 不是把 o1 转写成 s1。
	"o1": GuardCatViolent,           // Violence and Hate
	"o2": GuardCatSexual,            // Sexual Content
	"o3": GuardCatNonViolentIllegal, // Criminal Planning
	"o4": GuardCatViolent,           // Guns and Illegal Weapons
	"o5": GuardCatNonViolentIllegal, // Regulated or Controlled Substances
	"o6": GuardCatSelfHarm,          // Self-Harm
}

// llamaGuardCodeLabels 是 S 码的官方展示名,只进 Reason。
//
// 留着它是因为映射是有损的(见文件头 ②):折进 sexual 的那一票到底是
// S4 还是 S12,只有这里说得出来 —— 而这两者在事后研判时完全不是一回事。
var llamaGuardCodeLabels = map[string]string{
	"s1": "Violent Crimes", "s2": "Non-Violent Crimes", "s3": "Sex-Related Crimes",
	"s4": "Child Sexual Exploitation", "s5": "Defamation", "s6": "Specialized Advice",
	"s7": "Privacy", "s8": "Intellectual Property", "s9": "Indiscriminate Weapons",
	"s10": "Hate", "s11": "Suicide & Self-Harm", "s12": "Sexual Content",
	"s13": "Elections", "s14": "Code Interpreter Abuse",

	"o1": "Violence and Hate", "o2": "Sexual Content", "o3": "Criminal Planning",
	"o4": "Guns and Illegal Weapons", "o5": "Regulated or Controlled Substances",
	"o6": "Self-Harm",
}

// parseLlamaGuardVerdict 把 Llama Guard 的回复读成 guardLabels。
//
// 合法形状只有两种:
//
//	safe
//	unsafe\nS1,S10
//
// 容忍的只有排版:首尾空白、大小写、S 码之间的逗号/空格/换行混用,以及
// 官方模板偶尔在 safe 后面多一个换行。**不容忍**首行之外的任何东西 ——
// 那正是"这个渠道其实挂着一个通用模型"的形状。
func parseLlamaGuardVerdict(content string) (guardLabels, error) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	head := strings.ToLower(strings.TrimSpace(lines[0]))
	// 官方偶有把结论写成 "safe." 的部署,句点不改变含义。
	head = strings.TrimRight(head, ".。!!")

	switch head {
	case "safe":
		return guardLabels{Safety: "safe"}, nil
	case "unsafe":
	default:
		return guardLabels{}, errLlamaGuardInvalidResponse
	}

	labels := guardLabels{Safety: "unsafe", Source: AIProtocolLlamaGuard}
	known := map[string]struct{}{}
	unknown := map[string]struct{}{}
	hit := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, raw := range llamaGuardCodeTokens(lines[1:]) {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		hit = append(hit, raw)
		if id, ok := llamaGuardCodes[raw]; ok {
			known[id] = struct{}{}
			continue
		}
		// 表外的码原样带走。判定照常成立(unsafe 那一行才是结论),但它是
		// 协议漂移唯一的早期信号 —— 换了代际、换了微调版、或者这个渠道其实
		// 指向了另一个模型。静默丢弃会让它只表现为"某一类的计数莫名其妙少了"。
		unknown[clipRunes(raw, guardUnknownRunes)] = struct{}{}
	}
	// 与 parseGuardVerdict 同一套出场顺序与上限 —— guardLabels 的契约写在
	// 那个结构体上,两个生产者必须给出同一种形状,否则下游按顺序取首个类别
	// 的那段逻辑会因协议而异。
	for _, id := range guardAllCategories {
		if _, ok := known[id]; ok {
			labels.Categories = append(labels.Categories, id)
		}
	}
	for id := range unknown {
		labels.Unknown = append(labels.Unknown, id)
	}
	sort.Strings(labels.Unknown)
	if len(labels.Unknown) > guardUnknownMax {
		labels.Unknown = labels.Unknown[:guardUnknownMax]
	}
	labels.Detail = llamaGuardDetail(hit)
	// `unsafe` 却一个码都没给:判定仍然成立(拦不拦只看这一行),类别留空
	// 交给下游折进兜底类型。**不当成解析失败** —— 那会把一票真实的违规判定
	// 变成 fail-open 放行,方向正好反了。
	return labels, nil
}

// llamaGuardCodeTokens 从 unsafe 之后的行里切出 S 码(已小写)。
//
// 分隔符按逗号、空格、分号、顿号一起切:官方是逗号,而实测不同部署会
// 混用空格与换行,把它们当成同一件事不会引入任何歧义 —— S 码本身不含
// 这些字符。
func llamaGuardCodeTokens(rest []string) []string {
	joined := strings.ToLower(strings.Join(rest, ","))
	fields := strings.FieldsFunc(joined, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '、' || r == '\t' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// llamaGuardDetail 把原始 S 码拼成 Reason 末尾那一段。
//
// 九类映射是有损的,而这一段是唯一还留着原始信息的地方(见文件头 ②):
// 折进 sexual 的那一票到底是 S4 还是 S12,事后只有这里说得出来。
//
// 展示名取自闭集表;表外的码只回显它自己 —— 那一段是模型生成的文本,
// 所以按 guardUnknownRunes 截断过,并且调用方还会再过一次 redactSnippet。
func llamaGuardDetail(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	if len(codes) > guardUnknownMax {
		codes = codes[:guardUnknownMax]
	}
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		up := strings.ToUpper(c)
		if label := llamaGuardCodeLabels[c]; label != "" {
			parts = append(parts, up+" "+label)
			continue
		}
		parts = append(parts, clipRunes(up, guardUnknownRunes))
	}
	return "codes=" + strings.Join(parts, ", ")
}
