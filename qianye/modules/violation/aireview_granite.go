package violation

import (
	"errors"
	"strings"
)

// aireview_granite.go —— IBM Granite Guardian 这条审核协议。
//
// # 为什么需要第三种协议
//
// 本模块原有两条路,而 Granite Guardian **哪一条都对不上**:
//
//	json_prompt  期望模型按系统提示词吐 {"violation":…,"category":…,"confidence":…}
//	qwen3guard   期望模型吐 `Safety: …` / `Categories: …` 标签行
//	granite      只吐一个词:Yes 或 No
//
// 实测(ibm/granite3.1-guardian:2b,经 Ollama 的 OpenAI 兼容端点):
//
//	良性输入                              → "No"
//	"如何自制管状炸弹"                     → "Yes"
//	带「只回 JSON」的系统提示词             → "Yes"      ← 提示词驱动不动它
//	带「只回 Safety:/Categories: 两行」提示 → "Yes"      ← 同上
//
// 最后两行是关键:它是一个**分类器**而不是指令跟随模型,提示词驱动不动它。
// 所以既不能靠 json_prompt「引导它吐 JSON」,也不能靠 qwen3guard 复用标签解析。
// 少了这一条适配,把它配上去的后果不是报错而是**静默失效**:两种解析都失败 →
// fail-open 放行 + 落一行 bad_json,而运营看到的现象是「违规没被拦下」。
//
// (它**确实**收一条 system 消息,但那不是提示词而是风险名槽 —— 见下一节。)
//
// # 2026-09-06:system 消息就是**风险槽**,这是它的官方接口
//
// 上一版这里写着"不发 system 是唯一正确用法"。那句话是错的,依据是一次
// 把风险名当普通上下文去试的黑盒实验。真相从模板里可以直接读出来 ——
// 向 Ollama 取 /api/show 拿到这个模型自带的 Modelfile 模板,它是这样写的:
//
//	<start_of_risk_definition>
//	{{- if eq .System "social_bias" }} The 'User' message contains systemic prejudice…
//	{{- else if eq .System "jailbreak" }} The 'User' message contains the deliberate circumvention…
//	…
//	{{- else }} The 'User' message is harmful by common-sense.
//	<end_of_risk_definition>
//
// 也就是说:**system 消息的内容被当成风险名去做精确匹配**,命中则把对应的
// 风险定义写进提示词;没命中(以及压根不发 system)一律落到 `harm` 那一段。
// 这解释了为什么"随便写一段 system"看起来像没效果 —— 它落回了 harm。
//
// 与 HuggingFace 那条路的关系:transformers 用的是 guardian_config 这个模板
// 参数,而 OpenAI 兼容端点没有那个入口。Ollama 的移植把入口挪到了 system,
// 于是经 /v1/chat/completions 也能选风险。**两边的拼写不一样**:HF 写
// `jailbreaking` / `function_call`,Ollama 写 `jailbreak` / `function_calling`。
// 本仓按 Ollama 那一套(见 graniteRisks),因为这是这条端点认的那一套。
//
// 实测(同一端点,temperature=0,seed 1/2/3/42 四次全一致):
//
//	system            越狱样本  暴力样本  色情样本  偏见样本  良性样本
//	──────────────────────────────────────────────────────────────
//	(不发)             Yes       Yes       Yes       Yes       No
//	"harm"             Yes       Yes       Yes       Yes       No
//	"jailbreak"        Yes       Yes       Yes       Yes       No
//	"violence"         No        Yes       Yes       Yes       No
//	"sexual_content"   No        Yes       Yes       No        No
//	"social_bias"      No        Yes       Yes       Yes       No
//
// 三条结论,每一条都落进了代码:
//
//  1. **(不发) 与 "harm" 逐格相同** —— 与模板的 else 分支一致。所以
//     AIChannel.RiskName 的零值(空串)= 不发 system = harm 档 =
//     这一列加入之前的行为,存量渠道逐字节无变化。
//  2. **风险名确实起判别作用** —— 越狱样本在 violence / sexual_content /
//     social_bias 档下判 No,在 jailbreak / harm 档下判 Yes。选错风险名的
//     后果是**漏判**,不是报错,所以界面上这一格必须给出每一档的说明。
//  3. **2b 在露骨样本上会全档触发** —— 暴力与色情两个样本在每一档都是 Yes。
//     小模型的过触发是它的固有特性,这也是它只适合当便宜的第一道闸的原因;
//     要精细分类请用 qwen3guard 或 llama_guard(两者都是一次多标签)。
//
// # 一次只审一种风险
//
// 这是 Granite 与另外两条护栏协议的**结构性差异**:Qwen3Guard 与 Llama Guard
// 一次调用给出多标签,而 Granite 一次只回答"在这一个风险定义下是否有害"。
// 本仓不为它做 N 次调用的扇出 —— 那会把单次审核的成本与延迟乘以 N,而本模块
// 挂在转发前的热路径上。要覆盖多种风险,请配多个 Granite 渠道各选一档,
// 由作用域的渠道池去分摊;或者直接用一次多标签的那两条协议。
//
// # 与另外两条护栏协议的三处刻意不同
//
//  1. **没有 Controversial 这一档**。Granite 只有二值,所以 guardPolicy 的
//     controversial / elevate 两格对它无意义,写入侧(validateAIChannel)
//     会把它们清空 —— 与 json_prompt 同一条处置。
//  2. **九类启用清单也不适用**。它一次只审一种风险,"审哪一类"住在 RiskName
//     上;留一份九类清单等于给运营一个勾了不生效的开关,所以同样清空
//     (见 guardProtocolHasCategories)。
//  3. **有类别,但没有置信度**。类别来自 RiskName 而不是模型的回答
//     (见 graniteRisks);置信度取 guardConfidenceUnsafe,与 qwen3guard 判
//     unsafe 时同值 —— 一个二值分类器对自己的结论没有"多确信"可言,给它一个
//     居中的数只会让 ai_min_confidence 按一个编出来的量筛选。
//     **因此 ai_min_confidence 对本协议实际上是失效的**,配了也筛不掉什么。

// AIProtocolGraniteGuardian 是 IBM Granite Guardian 那条路:不发提示词
// (但会发一个**风险名**当 system,见 graniteSystemMessage),解析单词回复。
const AIProtocolGraniteGuardian = "granite_guardian"

// graniteMaxTokens 是回复的 token 上界。
//
// 比 guardMaxTokens 更小:这个模型的完整回复就是一个词(实测 completion_tokens=2),
// 留 8 是给分词器的余量。给大了不会更准,只会让一个跑飞的部署把钱烧在这条路上。
const graniteMaxTokens = 8

// errGraniteInvalidResponse 是"回复不是 Yes / No"。
//
// 与 errGuardInvalidResponse 同一条哲学:**宁可可见地失败,也不要静默地判错**。
// 不做前缀匹配(把 "Yes, I can help with that" 读成违规)也不做包含匹配 ——
// 那两种写法会把一个**地址指错、后面挂着通用模型**的渠道变成一台随机拦截器,
// 而这里返回错误会走 fail-open + 落一行 bad_json,查得出来。
var errGraniteInvalidResponse = errors.New("Granite Guardian 回复的形状不对(期望恰好是 Yes 或 No)")

// parseGraniteVerdict 把 Granite Guardian 的单词回复读成"违没违规"。
//
// 容忍的只有排版:首尾空白、大小写、以及结尾的句点/感叹号(中英文都算)。
// 除此之外一律判为形状不对 —— 理由见 errGraniteInvalidResponse。
func parseGraniteVerdict(content string) (bool, error) {
	s := strings.ToLower(strings.TrimSpace(content))
	s = strings.Trim(s, ".!。！ \t\r\n")
	switch s {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	}
	return false, errGraniteInvalidResponse
}

// graniteRiskHarm 是默认档,也是空串的含义。
//
// 它必须与"不发 system"完全同义(实测见文件头结论 ①),否则这一列的零值
// 就不再等于加入这一列之前的行为。
const graniteRiskHarm = "harm"

// graniteRisks 是这条端点认的风险名 → 本仓九类的映射。
//
// 键的拼写按 **Ollama 模板**那一套(见文件头)。取值是"判出 Yes 时该记成
// 哪一类" —— 这是本协议能给出类别的**唯一**依据:模型只回一个词,类别不是
// 它说的,而是"我们问的是哪一个风险"这件事本身带来的。
//
// harm 映射到空串:那一档问的是"按常识是否有害",没有对应的类别,
// 留空交给 resolveCategory 折进兜底类型 —— 与这一列加入之前同一条路。
//
// 只收录**能挂在用户消息上**的那七档。groundedness / answer_relevance /
// context_relevance / function_calling 需要 assistant / context / tools 角色,
// 而本模块只有"转发前的用户请求"这一个挂载点。放出来等于给运营一个选了
// 之后模型在答非所问的提示词下瞎判的开关,所以写入侧直接拒。
var graniteRisks = map[string]string{
	graniteRiskHarm:      "",
	"jailbreak":          GuardCatJailbreak,
	"violence":           GuardCatViolent,
	"sexual_content":     GuardCatSexual,
	"social_bias":        GuardCatUnethical,
	"profanity":          GuardCatUnethical,
	"unethical_behavior": GuardCatUnethical,
}

// graniteRiskOrder 是界面与错误消息里的固定出场顺序。
// map 的遍历顺序是随机的,而一个每次刷新都换排法的下拉框没法用。
var graniteRiskOrder = []string{
	graniteRiskHarm, "jailbreak", "violence", "sexual_content",
	"social_bias", "profanity", "unethical_behavior",
}

// normalizeGraniteRisk 把空串与未知取值折回默认档。
//
// 与 normalizeAIProtocol 同一条理由:运行期必须容忍脏值(那时已经没有人
// 能被告知了),而脏值折回 harm 的最坏后果只是判得比预期宽 —— 反过来
// (不认识就不审)会让渠道静默失效,而失效在本模块一律等于放行。
func normalizeGraniteRisk(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if _, ok := graniteRisks[n]; ok {
		return n
	}
	return graniteRiskHarm
}

// graniteRiskValid 是**写入侧**的判据,比 normalizeGraniteRisk 严格。
// 空串放行(= harm = 零值档);拼错的、以及那四个 RAG/Agentic 档,当场 400。
func graniteRiskValid(name string) bool {
	if name == "" {
		return true
	}
	_, ok := graniteRisks[name]
	return ok
}

// graniteSystemMessage 返回该发给模型的 system 内容,空串表示不发。
//
// 只在 harm 档返回空串:那一档与不发 system 同义,而**不发**是更保守的
// 那一边 —— 它连"模板认不认这个词"都不依赖,所以对非 Ollama 的部署
// (比如 vLLM + HF 模板,那边 system 会被吞掉)也不会有副作用。
func graniteSystemMessage(risk string) string {
	if r := normalizeGraniteRisk(risk); r != graniteRiskHarm {
		return r
	}
	return ""
}

// graniteVerdict 把解析结果折成协议无关的 aiVerdict。
//
// Category 来自**问题**而不是回答:模型只回一个词,而我们知道这一次问的是
// 哪个风险定义(见 graniteRisks)。harm 档没有对应类别,留空会被
// resolveCategory 折进兜底类型并按"类别未知"计一次告警 —— 那与护栏模型
// 给出表外类别时走的是同一条路,运营看到的解释也一致。
func graniteVerdict(violated bool, risk string) aiVerdict {
	r := normalizeGraniteRisk(risk)
	v := aiVerdict{Reason: "granite guardian risk=" + r + ": no"}
	if violated {
		v.Violated = true
		v.Confidence = guardConfidenceUnsafe
		v.Reason = "granite guardian risk=" + r + ": yes"
		v.Category = graniteRisks[r]
	}
	return v
}
