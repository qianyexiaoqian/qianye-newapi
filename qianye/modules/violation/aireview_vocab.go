package violation

import (
	"fmt"
	"sort"
	"strings"
)

// aireview_vocab.go —— AI 审核的违规类型清单,以及由它生成的那一段提示词。
//
// ══════════════ 为什么类型闭集不能再写死在代码里 ══════════════
//
// 上一版的形状是:代码里一个 `aiCategories` map,默认提示词里一行
// `category 只能取以下之一:none, sexual, ...`,两份事实靠一条测试
// (TestAIPromptDeclaresEveryCategory)保持同步。它有两个各自独立的问题:
//
//  1. **那个闭集与本站真正的违规类型表毫无关系。** 出厂闭集是
//     {sexual, violence, hate, self_harm, illegal, privacy, jailbreak,
//     malware, spam},而 qy_violation_category 的出厂类型是
//     {jailbreak, reverse, distill, pressure, upstream} —— 只有 jailbreak
//     一个词碰巧重合。运营在类型页新建一个「未成年人相关」,AI 永远不会返回它;
//     模型回一个 sexual,类型表里根本没有这一类。两套词汇表各说各话。
//  2. **同步靠人。** 加一个类型要改两处(map 与提示词那一行),而漏改的表现是
//     "按这个类型过滤的规则永不命中"—— 接口 200、界面正常、日志无声。
//     那条测试的存在本身就是这个缺陷的证据:它防的是一个不该存在的重复。
//
// 现在:**类型清单只有一个来源 —— qy_violation_category**。发给模型的那一段
// 由本文件从类型表现算,运营在类型页加一行,下一次快照刷新之后模型就认得它。
//
// ══════════════ 三份文本的边界(不要混用) ══════════════
//
//	Category.Remark       内部备注   → 管理端。不进提示词,不进用户端。
//	Category.PublicTitle  公示文案   → 用户端。绝不写判据。
//	Category.PublicDesc
//	Category.AIGuidance   判定说明   → 提示词(发往第三方审核服务)。不进用户端。
//
// 完整理由写在 model.go 的 Category.AIGuidance 上。这里只强调一条:
// **本文件永远不读 Remark**。它是唯一会把类型文本送出本站的地方,
// 读错一列的后果是把内部口径(含人名、复核流程)发给第三方审核服务。

const (
	// aiNoViolationCategory 是"未违规"这个取值。它**不是**一个违规类型,
	// 因此不落在 qy_violation_category 上,也不能被运营建成一个类型
	// (validateCategory 拒绝这个 key)—— 否则会出现"违规类型:未违规"
	// 这种自相矛盾的行,而它的计数没有任何解释。
	aiNoViolationCategory = "none"

	// aiPromptCategoryPlaceholder 是自定义提示词里声明"类型清单插在这里"的占位符。
	//
	// 没写占位符也没关系:渲染时把清单**追加**在末尾(见 renderAIPrompt)。
	// 占位符的价值是让运营能决定清单出现在哪一段,而不是被迫接受"总在最后"。
	aiPromptCategoryPlaceholder = "{{categories}}"

	// aiGuidanceRenderMax 是单条判定说明进提示词时的字数上限。
	//
	// 列宽是 512(varchar),写入校验是 256(见 categoryAIGuidanceMax)。这里再低
	// 一档是渲染侧的兜底:类型条数 × 每条字数是**每一次审核调用都要付的 token**,
	// 而类型条数由运营决定、没有上界。存量库里可能已经有一条 512 字的说明
	// (列宽允许),渲染侧不设限的话它会一直被付钱。
	aiGuidanceRenderMax = 256
)

// aiCategoryDef 是发给审核模型的一条类型定义。
//
// 字段清单就是隔离本身:这里**只有** Key / Name / Guidance,没有 Remark、
// 没有 PublicTitle。复用 Category 并在渲染时挑字段是负面清单,下一次给
// Category 加一列内部字段时它会默认被发出去,而发出去就收不回来了。
type aiCategoryDef struct {
	Key      string
	Name     string
	Guidance string
}

// aiVocabulary 是一次快照里的类型闭集:模型允许返回哪些 category。
//
// 零值是可用的 —— 类型表加载失败时它就是零值,此时 resolveCategory 把一切
// 判定折进 FallbackCategoryKey,规则里"不限类型"的那些照常命中。
// 与本模块其余部分同口径:少一个能力,不是整体停摆。
type aiVocabulary struct {
	// Defs 是有序的类型清单,兜底类型恒在第一条。顺序进提示词,
	// 所以它必须是稳定的(按 SortOrder、再按 Id),否则每次刷新快照
	// 提示词都会变一个样,而提示词变了模型的输出分布就会变。
	Defs []aiCategoryDef
	// FallbackKey 是「未分类」的 key。模型回了清单外的类型时折到这里。
	FallbackKey string

	keys map[string]struct{}
}

// buildAIVocabulary 从类型表算出这一轮的闭集。
//
// 入参是**快照已经拉好**的那一份类型行,不再单独查一次库:两次查询会让
// 规则、类型、AI 闭集三者可能来自不同时刻,而"规则按新类型过滤、闭集还是旧的"
// 的表现是那条规则永不命中 —— 本模块反复出现的那一种静默失效。
func buildAIVocabulary(cats []Category) aiVocabulary {
	v := aiVocabulary{FallbackKey: FallbackCategoryKey, keys: map[string]struct{}{}}

	ordered := make([]Category, 0, len(cats))
	var fallback *Category
	for i := range cats {
		if cats[i].IsFallback {
			// 兜底类型恒定参与,即使被标了排除。它不是一个"可选的类型",
			// 而是"归不了类"这个状态的名字 —— 把它从清单里拿掉,模型遇到
			// 归不了类的内容时只能瞎猜一个,而瞎猜的那一票会加到别人头上。
			fallback = &cats[i]
			continue
		}
		if cats[i].AIExcluded {
			continue
		}
		ordered = append(ordered, cats[i])
	}
	sort.SliceStable(ordered, func(a, b int) bool {
		if ordered[a].SortOrder != ordered[b].SortOrder {
			return ordered[a].SortOrder < ordered[b].SortOrder
		}
		return ordered[a].Id < ordered[b].Id
	})

	if fallback != nil {
		v.FallbackKey = fallback.Key
		v.Defs = append(v.Defs, aiCategoryDef{
			Key: fallback.Key, Name: fallback.Name,
			Guidance: aiFallbackGuidance(fallback.AIGuidance),
		})
	}
	for _, d := range v.Defs {
		v.keys[d.Key] = struct{}{}
	}
	for _, c := range ordered {
		// 同 key 只进一次。GORM 的软删作用域已经保证"每个 key 只有一行活着",
		// 但那条保证依赖 (key, archive_seq) 唯一索引真的建对了 —— 而**本仓刚
		// 修过一次它没建对**(旧定义是 (key, deleted_at),三家数据库都把 NULL
		// 视为互不相等,于是同一个 key 攒下了四行活的;演示库里至今还留着那批
		// 归档行,见 reconcileCategoryKeyIndex)。没修过的存量库直接把重复行
		// 灌进提示词,表现是同一个类型在清单里出现四遍 —— 白付四份 token,
		// 还给模型一份看起来自相矛盾的清单。
		if _, dup := v.keys[c.Key]; dup {
			continue
		}
		v.keys[c.Key] = struct{}{}
		v.Defs = append(v.Defs, aiCategoryDef{
			Key: c.Key, Name: c.Name, Guidance: clipRunes(c.AIGuidance, aiGuidanceRenderMax),
		})
	}
	return v
}

// aiFallbackGuidance 保证兜底那一条**永远**带着"归不了类时用它"这句话。
//
// 运营可以在类型页给「未分类」写自己的说明,但不能把这句话删掉:删掉之后
// 模型遇到归不了类的内容会从清单里挑一个最像的,而那一票会加到一个语义
// 完全不同的类型的计数上 —— 那正是"计数说得通、结论是错的"。
func aiFallbackGuidance(custom string) string {
	const must = "确实违规、但无法归入下列任何一类时使用。"
	custom = clipRunes(strings.TrimSpace(custom), aiGuidanceRenderMax)
	if custom == "" {
		return must
	}
	return must + custom
}

// known 回答"这个 key 在本轮闭集里"。none 不算 —— 它不是一个违规类型。
func (v aiVocabulary) known(key string) bool {
	if len(v.keys) == 0 {
		return false
	}
	_, ok := v.keys[key]
	return ok
}

// keyList 是闭集里全部 key 的有序切片,供接口下发与审计使用。
func (v aiVocabulary) keyList() []string {
	out := make([]string, 0, len(v.Defs))
	for _, d := range v.Defs {
		out = append(out, d.Key)
	}
	return out
}

// aiCategoryResolution 是"模型给的类型名落到哪一行类型上"的结论。
type aiCategoryResolution struct {
	// Key 是落定的类型 key,一定在闭集里(或闭集为空时是 FallbackCategoryKey)。
	Key string
	// Raw 是模型原样返回的那个值,仅在 Fallback 为真时有意义。空字符串表示
	// 模型压根没给 category 字段。
	Raw string
	// Fallback 为真表示这一票**没能**落到一个真实类型上,已折进兜底。
	// 它是告警与 AIReview.RawCategory 的唯一触发条件。
	Fallback bool
}

// resolveCategory 把模型给的类型名归一到闭集上。
//
// ══════════════ 清单外的类型:落「未分类」+ 告警 + 留原值 ══════════════
//
// 三种可选处置,选了第一种:
//
//	(a) 落「未分类」并告警        ← 选它
//	(b) 整条判定作废(当成未违规)
//	(c) 原样保留模型给的字符串
//
// 为什么不是 (b):`violation=true` 是这次调用里**最贵也最可信**的那半个信号,
// 而 category 只是给它贴的标签。因为标签错了就把"这段内容违规"整个丢掉,
// 等于让模型的一次拼写错误变成一次放行 —— 而放行是不可逆的(请求已经发往上游)。
// 何况绝大多数不在清单里的返回值(porn / adult / NSFW)恰恰说明模型判对了、
// 只是没照着清单说话。
//
// 为什么不是 (c):原样保留会让 Category 列长出无穷多个近义词,而规则的类型
// 过滤是精确匹配,那张表永远追不上 —— 这是上一版就写明的理由,仍然成立。
//
// (a) 的代价必须说清楚:一条**专门过滤兜底类型**的规则会连带命中这些拼错的票。
// 那是可解释的(管理员显式选了「未分类」),而且方向安全 —— 它只会让更多东西
// 落进一个阈值出厂为 0 的类型。原值写进 AIReview.RawCategory 并打一条
// SysError,于是"模型一直在回 porn"查得出来,而不是消失在归一之后。
func (v aiVocabulary) resolveCategory(raw string, violated bool) aiCategoryResolution {
	norm := strings.ToLower(strings.TrimSpace(raw))
	if !violated {
		// 未违规时 category 不参与任何判定(matchAIRule 的第一道闸就是
		// out.Violated)。清单内的值原样留着供调参,其余一律记成 none ——
		// 在这里告警只会为"模型判了 clean 却顺手写了个标签"刷屏。
		if norm != "" && norm != aiNoViolationCategory && v.known(norm) {
			return aiCategoryResolution{Key: norm}
		}
		return aiCategoryResolution{Key: aiNoViolationCategory}
	}
	// 判了违规却回 none,与回一个不认识的词是同一种事故:这一票没有类型。
	// 分开处理会多一个只有它自己走的分支,而两者的正确处置完全一样。
	if norm != "" && norm != aiNoViolationCategory && v.known(norm) {
		return aiCategoryResolution{Key: norm}
	}
	fallbackKey := v.FallbackKey
	if fallbackKey == "" {
		fallbackKey = FallbackCategoryKey
	}
	return aiCategoryResolution{Key: fallbackKey, Raw: norm, Fallback: true}
}

// categoryBlock 是提示词里那一段类型清单的正文。
//
// 措辞里"以本节为准"不是客套:自定义提示词很可能还留着上一版手抄的那一行
// (例如 `none, sexual, violence, ...`),而本节是追加在它后面的。两份清单同时
// 出现时,必须有一句话明确谁说了算 —— 否则模型的选择取决于它自己的取舍,
// 而那意味着同一份配置在换一个审核模型之后行为就变了。
func (v aiVocabulary) categoryBlock() string {
	var b strings.Builder
	b.WriteString("可用的 category 取值(**以本节为准**;上文如果还有别的清单,一律以本节为准):\n")
	fmt.Fprintf(&b, "- %s:未违规时使用。\n", aiNoViolationCategory)
	for _, d := range v.Defs {
		b.WriteString("- ")
		b.WriteString(d.Key)
		if name := strings.TrimSpace(d.Name); name != "" {
			b.WriteString("(")
			b.WriteString(name)
			b.WriteString(")")
		}
		if g := strings.TrimSpace(d.Guidance); g != "" {
			b.WriteString(":")
			b.WriteString(g)
		}
		b.WriteString("\n")
	}
	b.WriteString("不要使用清单之外的取值;确实违规但归不进上面任何一类时,用兜底那一项。")
	return b.String()
}

// renderAIPrompt 把存下来的提示词与本轮类型清单拼成**真正发出去**的那一份。
//
// 两条路径,都必须让清单出现在最终文本里:
//
//   - 提示词里有占位符 → 原地替换。运营决定清单的位置。
//   - 没有占位符(默认提示词以外的绝大多数自定义)→ 追加在末尾。
//
// 为什么"没有占位符就追加"而不是"什么都不做":什么都不做的后果是这份自定义
// 提示词永远停留在它被写下那一天的类型清单上。运营在类型页新建了「未成年人
// 相关」,提示词里没有它,模型不会返回它,那一类的计数永远是 0 —— 而界面上
// 类型建好了、规则也绑上了,一切看起来都对。这正是本轮要根除的那种失效。
func renderAIPrompt(prompt string, v aiVocabulary) string {
	base := prompt
	if strings.TrimSpace(base) == "" {
		base = defaultAIPrompt
	}
	block := v.categoryBlock()
	if strings.Contains(base, aiPromptCategoryPlaceholder) {
		return strings.ReplaceAll(base, aiPromptCategoryPlaceholder, block)
	}
	return strings.TrimRight(base, "\n") + "\n\n" + block
}

// clipRunes 按码点截断。按字节截会把一个中文字符切成两半,而本仓的这一段
// 文本会进 utf8mb4 列与出站 JSON —— 半个字符两边都会出问题。
func clipRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
