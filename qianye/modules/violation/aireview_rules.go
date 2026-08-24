package violation

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// AI 审核在规则层与快照层的接线。
//
// 拆成独立文件而不是塞进 rules.go:rules.go 已经是本模块最长的文件,
// 而这里的两个函数(写入校验 + 快照装配)彼此相关、与本地匹配引擎无关。

// validateAIRule 是 ai_review 规则与 post_async 阶段的写入校验。
//
// 它挡的全部是"保存得下去、但线上永远不会按你想的那样跑"的组合 ——
// 本模块反复出现的失效形状。每一条都写清楚了拒绝的理由,因为管理员看到
// 报错时唯一能做的就是照着改。
func validateAIRule(r *Rule) error {
	// ── 阶段侧:post_async 只接受 ai_review ──
	//
	// 别的匹配方式在本地就能算出结果(词表、正则、状态码),把它们推迟到异步
	// 只会带来一件事:结论晚几百毫秒,而且再也不能拦截。那是纯粹的损失,
	// 允许保存等于给运营留一个只有坏处的选项。
	if r.Phase == PhasePostAsync && r.MatchType != MatchAIReview {
		return fmt.Errorf("阶段 %q(转发后异步审核)只支持 match_type %q,当前为 %q —— "+
			"其它匹配方式在本地即可判定,推迟到异步只会失去拦截能力",
			PhasePostAsync, MatchAIReview, r.MatchType)
	}
	if r.MatchType != MatchAIReview {
		// 非 AI 规则不该带置信度下限:它是 ai_review 专属的闸,填在别的规则上
		// 会让人以为"这条正则也有置信度",而它实际上一个字节都不生效。
		if r.AIMinConfidence.IsPositive() {
			return fmt.Errorf("ai_min_confidence 只对 match_type %q 有意义,当前为 %q",
				MatchAIReview, r.MatchType)
		}
		return nil
	}

	// ── AI 规则侧 ──
	switch r.Phase {
	case PhasePrompt, PhasePostAsync:
	default:
		return fmt.Errorf("match_type %q 只能用于 %q(转发前,同步、可拦截)或 %q(转发后,异步、只记录计次)阶段,当前为 %q",
			MatchAIReview, PhasePrompt, PhasePostAsync, r.Phase)
	}
	// 转发后审核跑在异步 worker 上,那里既没有 gin.Context 也没有 relayInfo 的
	// 计费路由(BillingSource / SubscriptionId),扣费所需的上下文根本不存在。
	// 强行扣只能凭猜,而猜错的方向是"从错误的池子里扣走用户的钱"。
	// 项目方对这一档的要求原话也是「异步,只记录与计次,不影响本次请求」。
	if r.Phase == PhasePostAsync && r.Action != ActionRecord {
		return fmt.Errorf("阶段 %q 只支持 action %q(只记录与计次):"+
			"异步时机拿不到本次请求的计费上下文,扣费无法落到正确的额度池;"+
			"要扣费请把这条规则改成 %q 阶段",
			PhasePostAsync, ActionRecord, PhasePrompt)
	}
	if r.AIMinConfidence.IsNegative() || r.AIMinConfidence.GreaterThan(decimal.NewFromInt(1)) {
		return fmt.Errorf("ai_min_confidence 必须在 0..1 之间(0 表示不设下限),当前为 %s",
			r.AIMinConfidence.String())
	}
	// 类型白名单里的每一项都要能与模型返回的 category 精确对上。
	//
	// 这里挡两件事:字符集(大小写与空格是最常见的填法错误,表现是"这条规则
	// 永远不命中"),以及保留值 none。**不挡具体取值** —— 类型清单现在来自
	// qy_violation_category,而这个函数是纯校验、手上没有库句柄;更实际的是
	// 运营完全可能先写规则再建类型,在这里拒绝会把那个顺序变成非法。
	// 填了一个不存在的类型的后果由管理端的对账提示承担,不由写入闸承担。
	for _, cat := range splitList(strings.ToLower(r.Pattern)) {
		for _, ch := range cat {
			if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
				continue
			}
			return fmt.Errorf("违规类型 %q 含非法字符:类型名只能包含小写字母、数字、下划线与连字符"+
				"(它要与审核模型返回的 category 精确匹配,一个空格就会让这条规则永不命中)", cat)
		}
		// none 是"未违规"的取值,不是一个违规类型。把它写进白名单的规则
		// 一次都不会命中(matchAIRule 的第一道闸就是 out.Violated),
		// 而界面上它看起来配得很合理 —— 又一条"保存得下去、线上永不生效"。
		if cat == aiNoViolationCategory {
			return fmt.Errorf("%q 表示「未违规」,不是一个违规类型,不能写进类型白名单:"+
				"这条规则只在模型判定违规时才会被考虑,填 %q 等于让它永不命中。"+
				"要「只要判违规就命中、不看类型」请把类型白名单留空",
				aiNoViolationCategory, aiNoViolationCategory)
		}
	}
	return nil
}

// validateAISetting 校验全局设置。上下界都不是装饰:
// 每一个越界值都对应一种"看起来开着、实际不生效或代价失控"的状态。
func validateAISetting(s *AISetting) error {
	// 提示词的归一放在校验里,而不是各个 handler 里:这是唯一的写入校验闸,
	// 放这里意味着以后新增任何一条写入路径都自动带上"逐字等于默认 → 存空串"。
	// 见 aireview_prompt.go 顶部对这条折叠的完整理由。
	s.Prompt = normalizeAIPrompt(s.Prompt)
	if s.PreTimeoutMs < minAITimeoutMs || s.PreTimeoutMs > maxPreTimeoutMs {
		return fmt.Errorf("转发前审核超时必须在 %d..%d 毫秒之间(它直接加在被抽中请求的响应延迟上),当前为 %d",
			minAITimeoutMs, maxPreTimeoutMs, s.PreTimeoutMs)
	}
	if s.AsyncTimeoutMs < minAITimeoutMs || s.AsyncTimeoutMs > maxAsyncTimeoutMs {
		return fmt.Errorf("转发后审核超时必须在 %d..%d 毫秒之间,当前为 %d",
			minAITimeoutMs, maxAsyncTimeoutMs, s.AsyncTimeoutMs)
	}
	if s.MaxInputChars < 200 || s.MaxInputChars > maxAIMaxInputChars {
		return fmt.Errorf("送审内容上限必须在 200..%d 字之间,当前为 %d", maxAIMaxInputChars, s.MaxInputChars)
	}
	if n := utf8.RuneCountInString(s.Prompt); n > maxAIPromptRunes {
		return fmt.Errorf("审核提示词过长(%d 字,上限 %d 字)—— 它每次调用都要作为 token 付一遍钱",
			n, maxAIPromptRunes)
	}
	// 打开总开关必须先确认"用户请求内容会被发往第三方"。
	// 这是本功能唯一一个**对用户有外部影响**的事实,不能靠一句会被下次改版
	// 顺手删掉的前端提示来承载,见 AISetting.ThirdPartyNoticeAck。
	if s.Enabled && !s.ThirdPartyNoticeAck {
		return fmt.Errorf("启用 AI 审核前必须先确认「被抽中的请求内容会被发送到所配置的第三方审核服务」")
	}
	return nil
}

// validateAIChannel 校验一个审核渠道。
func validateAIChannel(ch *AIChannel) error {
	ch.Name = strings.TrimSpace(ch.Name)
	ch.BaseUrl = strings.TrimSpace(ch.BaseUrl)
	ch.Model = strings.TrimSpace(ch.Model)
	ch.Protocol = strings.TrimSpace(ch.Protocol)
	ch.GuardControversial = strings.TrimSpace(ch.GuardControversial)

	// 写入侧比运行期严格:拼错的协议名在这里当场 400,而运行期一律折回
	// json_prompt。理由写在 aiProtocolValid 上 —— 保存那一刻还有人看得到
	// 错误消息,而热路径上没有。
	if !aiProtocolValid(ch.Protocol) {
		return fmt.Errorf("审核协议只能是 %q(提示词 + JSON)或 %q(护栏模型安全标签),当前为 %q",
			AIProtocolJSONPrompt, AIProtocolQwen3Guard, ch.Protocol)
	}
	if !guardControversialValid(ch.GuardControversial) {
		return fmt.Errorf("「有争议」档的处理只能是 %q、%q 或 %q,当前为 %q",
			GuardControversialSafe, GuardControversialSensitive,
			GuardControversialUnsafe, ch.GuardControversial)
	}
	// 类别清单在写入侧就归一成"固定顺序、去重、只含九类之一"的规范形态:
	// 前端传过来的顺序是复选框的点击顺序,而每次保存都写一个字节不同、含义
	// 相同的值,会让审计差异页每一行都亮起来 —— 那样的差异没人会去读。
	var err error
	if ch.GuardCategories, err = canonicalGuardCategoryCSV(ch.GuardCategories, "启用的审核类别"); err != nil {
		return err
	}
	if ch.GuardElevate, err = canonicalGuardCategoryCSV(ch.GuardElevate, "升级为拦截的敏感类别"); err != nil {
		return err
	}
	// json_prompt 渠道上把这三格清空:留着一个被忽略的取值,下一个人照着
	// 界面回显去查"为什么设了 unsafe 却没生效",而答案是这条路根本没有
	// Controversial 这一档。
	if normalizeAIProtocol(ch.Protocol) != AIProtocolQwen3Guard {
		ch.GuardControversial = ""
		ch.GuardCategories = ""
		ch.GuardElevate = ""
	}

	if ch.Name == "" {
		return fmt.Errorf("渠道名称不能为空")
	}
	if utf8.RuneCountInString(ch.Name) > 64 {
		return fmt.Errorf("渠道名称过长(上限 64 字)")
	}
	// 只放行 http/https。少了这道闸,一个 file:// 或 gopher:// 的地址会把
	// 出站调用变成一次本机文件读取或协议走私,而这一格是管理端可写的。
	low := strings.ToLower(ch.BaseUrl)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return fmt.Errorf("地址必须以 http:// 或 https:// 开头,当前为 %q", ch.BaseUrl)
	}
	if utf8.RuneCountInString(ch.BaseUrl) > 256 {
		return fmt.Errorf("地址过长(上限 256 字)")
	}
	if err := rejectCloudMetadataHost(ch.BaseUrl); err != nil {
		return err
	}
	if ch.Model == "" {
		return fmt.Errorf("模型名不能为空")
	}
	if utf8.RuneCountInString(ch.Model) > 128 {
		return fmt.Errorf("模型名过长(上限 128 字)")
	}
	if ch.TimeoutMs < 0 || ch.TimeoutMs > maxAsyncTimeoutMs {
		return fmt.Errorf("渠道超时必须在 0..%d 毫秒之间(0 表示跟随全局),当前为 %d",
			maxAsyncTimeoutMs, ch.TimeoutMs)
	}
	// 权重下界是 1 而不是 0:权重 0 的渠道在加权随机里永远抽不中,
	// 但它在列表上仍然显示"已启用" —— 又一条"看起来开着、实际零流量"。
	// 要停用请用 enabled 开关,那是唯一能在界面上看懂的表达。
	if ch.Weight < 1 || ch.Weight > 1000 {
		return fmt.Errorf("权重必须在 1..1000 之间(要停用请用启用开关,权重 0 会让渠道永远抽不中却仍显示已启用),当前为 %d", ch.Weight)
	}
	if ch.PriceInPerM.IsNegative() || ch.PriceOutPerM.IsNegative() {
		return fmt.Errorf("单价不得为负数")
	}
	if utf8.RuneCountInString(ch.Remark) > 512 {
		return fmt.Errorf("备注过长(上限 512 字)")
	}
	return nil
}

// rejectCloudMetadataHost 拦掉指向云厂商元数据端点的地址。
//
// # 为什么只拦这一档,不拦整个内网
//
// 这个按钮本来就要能指向内网:护栏协议的出厂默认地址就是自建 Ollama
// (http://localhost:11434/v1),局域网里的自建审核服务同样是被推荐的用法。
// 一刀切禁掉私网会把这个功能的主要部署形态直接废掉。
//
// 但链路本地地址(169.254.0.0/16 与 IPv6 的 fe80::/10)不一样:那个网段上没有
// 任何人会部署审核服务,它在云上唯一的用途就是实例元数据服务
// (169.254.169.254)—— 而管理端的连通性试跑会把上游响应体**原样回显**
// (raw_response,截断到 2000 码点),2000 码点足够带走一整份 IAM 凭据 JSON。
// 也就是说这一档是"零合法用途 + 最高价值目标",拦它没有代价。
//
// 这不是一道完整的 SSRF 防线:DNS 重绑定、指向内网管理面的探测仍然做得到,
// 那一面由"这是 role>=10 的受信管理员 + 关键操作限流 + 全程写审计"承担,
// 与上游把渠道地址交给管理员是同一个信任假设。这里堵的是其中危害最不对称的
// 那一格。
func rejectCloudMetadataHost(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("地址无法解析: %v", err)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("地址不能指向链路本地网段(%s)—— 那个网段上不会有审核服务,"+
			"它在云上的唯一用途是实例元数据服务(169.254.169.254),"+
			"而连通性试跑会把上游响应体原样回显", host)
	}
	return nil
}

// buildAIRuntime 把设置行与启用中的渠道装配成快照里的那一份。
//
// needed=false(一条 ai_review 规则都没有)时直接返回 nil,连查都不查:
// 没有规则消费它,拉两张表纯属给每个刷新周期加两次往返。
//
// 返回 (nil, nil) 表示"本功能这一轮不生效",这是一个**正常**结果而不是错误:
// 总开关关着、一条能送审的策略都没有(含空表)、一个渠道都没启用、渠道密钥
// 全都解不开 —— 四种都归它。只有查询本身失败才返回 error。
// vocab 是**快照已经从同一批类型行算好**的那份闭集。传进来而不是在这里再查
// 一次库:两次查询会让规则、类型计数、AI 闭集三者可能来自不同时刻,而"规则按
// 新类型过滤、闭集还是旧的"的表现是那条规则永不命中。
func buildAIRuntime(gdb *gorm.DB, needed bool, vocab aiVocabulary) (*aiRuntime, error) {
	if !needed || gdb == nil {
		return nil, nil
	}
	var setting AISetting
	if err := gdb.Where("id = ?", 1).Take(&setting).Error; err != nil {
		// 设置行还没建(第一次部署)不是错误,按"未启用"处理。
		return nil, nil
	}
	if !setting.Enabled {
		return nil, nil
	}
	// 送不送审只由策略表回答:一条启用的、某个时机非 0 的策略都没有 = 整份
	// 配置这一轮不生效。这就是「策略要手动添加才能生效」在装配层的落点。
	scopes, err := buildAIScopes(gdb)
	if err != nil {
		return nil, err
	}
	if !aiScopesReachAnySampling(scopes) {
		return nil, nil
	}

	var rows []AIChannel
	if err := gdb.Where("enabled = ?", true).Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}

	rt := &aiRuntime{
		PreTimeoutMs:   clampInt(setting.PreTimeoutMs, minAITimeoutMs, maxPreTimeoutMs),
		AsyncTimeoutMs: clampInt(setting.AsyncTimeoutMs, minAITimeoutMs, maxAsyncTimeoutMs),
		Prompt:         setting.Prompt,
		Vocab:          vocab,
		MaxInputChars:  clampInt(setting.MaxInputChars, 200, maxAIMaxInputChars),
		Scopes:         scopes,
	}
	// 闭集只剩兜底那一条(或干脆是空的)时告警一次:此时模型只能回 none 或
	// 「未分类」,按具体类型过滤的规则一条都不会命中。原因通常是类型表这一轮
	// 没加载上,或者全部类型都被标了"不参与 AI 审核"。不阻断 —— 不限类型的
	// 规则照常工作,而把整个功能关掉是更大的损失。
	if len(rt.Vocab.Defs) <= 1 {
		common.SysError("qianye/violation: AI 审核的违规类型清单为空(只有兜底类型)," +
			"模型将无法给出具体类型,按类型过滤的 ai_review 规则不会命中。" +
			"请确认违规类型页至少有一条未被标记为「不参与 AI 审核」的类型")
	}
	for i := range rows {
		row := rows[i]
		url := chatCompletionsURL(row.BaseUrl)
		if url == "" {
			continue
		}
		// 解密失败的渠道整条跳过,不是"当成没有密钥去裸调" —— 后者会把请求
		// 连同用户内容发给一个我们已经无法鉴权的端点,并得到一串 401。
		key, err := openAIKey(row.KeyNonce, row.KeyCipher, aiChannelAAD(row.Id), row.KeyVersion)
		if err != nil {
			common.SysError(fmt.Sprintf(
				"qianye/violation: AI 审核渠道 %d(%s)的密钥无法解密,本轮跳过该渠道: %v",
				row.Id, row.Name, err))
			continue
		}
		weight := row.Weight
		if weight < 1 {
			weight = 1
		}
		rt.Channels = append(rt.Channels, &aiChannelRT{
			Id: row.Id, Name: row.Name, URL: url, Model: row.Model, APIKey: key,
			// 协议在装配期归一一次。库里的脏值(空串、DBA 手工写错的取值)
			// 在这里就折回 json_prompt,热路径不再判第二遍。
			Protocol: normalizeAIProtocol(row.Protocol),
			Guard:    guardPolicyFromChannel(row),
			// 策略在装配期归一一次:热路径不再解析那两列 CSV,而每次审核都
			// 重新 split 一遍一整轮快照都不会变的字符串,只是白烧 CPU。
			TimeoutMs: row.TimeoutMs, Weight: weight,
			PriceInPerM: row.PriceInPerM, PriceOutPerM: row.PriceOutPerM,
		})
		// 权重合计刻意**不**在这里缓存一份:pickAIChannels 要在摘掉"这一档指定
		// 的那个渠道"之后再算,而一个装配期算好的合计在那一刻就是错的 ——
		// 错的表现是加权随机偏向某一个渠道,一种只能靠统计才看得出来的偏差。
	}
	if len(rt.Channels) == 0 {
		// 一个可用渠道都没有 = 每次都会走 no_channel 分支放行。
		// 直接收敛成 nil,让热路径连随机数都不用摇。
		return nil, nil
	}
	return rt, nil
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
