package violation

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/service"

	"github.com/shopspring/decimal"
)

// maxFeeMultiple 是 model_price_multiple 模式下的倍数上界,与 YAML 的
// violation.fee_multiplier(config/validate.go 校验 0..100)必须同口径 ——
// 两处不一致时,严的那一处就形同虚设。
const maxFeeMultiple = 100

// maxScanBytes 是单次请求参与匹配的文本上限(头尾各取一半)。
//
// 为什么必须有:body 上限是 128MB,把整段 prompt 丢进 AC 与正则会让 relay 线程
// 停滞数百毫秒。Go 的 regexp 是 RE2、线性时间,没有回溯灾难,真正的风险只有
// "输入过长",所以限住输入就等于限住了最坏耗时。
const maxScanBytes = 64 << 10

// maxSnippetRunes 是命中窗口的半径(前后各取这么多字符)。
const maxSnippetRunes = 160

// 命中词写库前的硬上限。AcSearch(stopImmediately=false) 在大词表 + 长文本下
// 可能返回上万个命中,不截断会直接把 varchar(1024) 写爆(MySQL 严格模式报错而非截断)。
const (
	maxMatchedTerms   = 16
	maxMatchedTermLen = 64
)

// statusRange 表示 status_code 规则里的一段 HTTP 状态码区间。
type statusRange struct{ lo, hi int }

// compiledRule 是规则的运行期形态:所有解析与编译在快照构建时一次性完成,
// 热路径只做比较,不做解析。
type compiledRule struct {
	R Rule

	re       *regexp.Regexp
	words    []string // keyword:已小写去重
	subs     []string // upstream_text:按 CaseSensitive 决定是否已小写
	codes    map[string]struct{}
	statuses []statusRange

	// modelPats 为空表示全部模型;groups 为空表示全部分组。
	modelPats []string
	groups    map[string]struct{}
}

// snapshot 是规则的只读内存视图。整体替换(atomic.Pointer),读端零锁;
// 正在使用旧快照的请求持有引用,不会读到半成品。
type snapshot struct {
	version int64
	loadAt  int64

	promptRules []*compiledRule
	postRules   []*compiledRule

	// promptWords / postWords 是各阶段全部 keyword 规则的词表并集,
	// 一次 AC 扫描服务全部关键词规则,而不是每条规则扫一遍。
	promptWords []string
	postWords   []string
}

func (s *snapshot) hasPrompt() bool { return s != nil && len(s.promptRules) > 0 }
func (s *snapshot) hasPost() bool   { return s != nil && len(s.postRules) > 0 }

var (
	current       atomic.Pointer[snapshot]
	nextRefreshAt atomic.Int64
	refreshFails  atomic.Int64
	scanTimeouts  atomic.Int64
)

// Snapshot 返回当前规则快照,永不返回 nil。
func Snapshot() *snapshot {
	if s := current.Load(); s != nil {
		return s
	}
	return &snapshot{}
}

// maybeRefresh 是快照的自维护入口:热路径每次调用只做一次 atomic 比较,
// 到期才通过 HotAsync 触发一次异步重载。
//
// 为什么不用后台协程:规则快照是"每个节点各自持有"的进程内缓存,
// lease.Run 只会让一个节点刷新,其余节点的规则永远陈旧;而裸 goroutine 又违反
// 模块约定。CAS 推进下次刷新时间既避免了并发重复加载,也让无流量的节点零开销。
func maybeRefresh() {
	now := common.GetTimestamp()
	next := nextRefreshAt.Load()
	if now < next {
		return
	}
	every := int64(config.Get().Violation.RuleCacheSeconds)
	if every <= 0 {
		every = 60
	}
	if !nextRefreshAt.CompareAndSwap(next, now+every) {
		return // 别的请求已经抢到刷新权
	}
	guard.HotAsync("violation.rule_refresh", func(ctx context.Context) error {
		return reloadCtx(ctx, false)
	})
}

// reload 用冷路径预算重载快照,供管理端写入后刷新与启动预热使用。
//
// 这两个调用点手上都没有现成的 ctx,而"没有 ctx"不等于"不需要上界":
// 管理端保存规则时若扩展库正病态,不接预算就会一直等到 DSN readTimeout(30 秒),
// 整个 HTTP 请求跟着挂住。热路径刷新走 reloadCtx,直接沿用 guard worker 的预算。
func reload(force bool) error {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return reloadCtx(ctx, force)
}

// reloadCtx 重新构建快照。force=false 时先比对版本号,版本未变直接返回,
// 避免每个刷新周期都全表拉规则。
//
// ctx 必须一路挂到 GORM 语句上:guard.HotAsync 承诺的 hot_async_timeout_ms(3 秒)
// 只对 WithContext(ctx) 的语句生效。漏接的后果不只是这条语句慢 —— 两个模块的刷新
// 周期同为 60 秒、由同一批 relay 流量触发,很容易同时占满仅有的 2 个 hot worker
// 长达 readTimeout(30 秒),这期间 commission.consume 事件会把 4096 槽队列填满并
// 溢出丢弃,而丢弃是"用户该拿的钱没拿到"的唯一路径,返佣没有 outbox 补偿。
// 次生问题:不带 ctx 时 db.WithOpProbe 认不到这些语句,hotRunWithBudget 就只会在
// 失败时 MarkFailure、成功时永不 MarkSuccess,熔断的健康票被单向截断。
func reloadCtx(ctx context.Context, force bool) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	var ver RuleVersion
	if err := gdb.Where("id = ?", 1).Take(&ver).Error; err != nil {
		// 版本行还不存在(首次部署)不是错误,按 version=0 继续加载。
		ver = RuleVersion{Id: 1, Version: 0}
	}
	if cur := current.Load(); cur != nil && !force && cur.version == ver.Version {
		cur.loadAt = common.GetTimestamp()
		return nil
	}

	var rows []Rule
	if err := gdb.Where("enabled = ?", true).Order("priority asc, id asc").Find(&rows).Error; err != nil {
		refreshFails.Add(1)
		return err
	}

	s := &snapshot{version: ver.Version, loadAt: common.GetTimestamp()}
	for i := range rows {
		cr, err := compile(rows[i])
		if err != nil {
			// 单条规则编译失败绝不能拖垮整份快照:那等于一次手滑就让全部风控失效。
			common.SysError(fmt.Sprintf("qianye/violation: 规则 %d(%s)编译失败,已跳过: %v",
				rows[i].Id, rows[i].Name, err))
			continue
		}
		switch cr.R.Phase {
		case PhasePrompt:
			s.promptRules = append(s.promptRules, cr)
			s.promptWords = append(s.promptWords, cr.words...)
		default:
			s.postRules = append(s.postRules, cr)
			s.postWords = append(s.postWords, cr.words...)
		}
	}
	s.promptWords = dedupe(s.promptWords)
	s.postWords = dedupe(s.postWords)
	current.Store(s)
	return nil
}

// compile 把一条规则翻译成运行期形态,顺带完成全部合法性校验。
func compile(r Rule) (*compiledRule, error) {
	cr := &compiledRule{R: r}

	switch r.MatchType {
	case MatchKeyword:
		// AC 自动机在建表时统一小写,因此关键词匹配一律不区分大小写。
		// 这是刻意的:违规词的大小写变体是最廉价的绕过手段。
		cr.words = dedupe(splitLines(strings.ToLower(r.Pattern)))
		if len(cr.words) == 0 {
			return nil, fmt.Errorf("keyword 规则的词表为空")
		}
	case MatchRegex:
		pat := r.Pattern
		if !r.CaseSensitive {
			pat = "(?i)" + pat
		}
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, err
		}
		cr.re = re
	case MatchErrorCode:
		cr.codes = map[string]struct{}{}
		for _, v := range splitList(r.Pattern) {
			cr.codes[strings.ToLower(v)] = struct{}{}
		}
		if len(cr.codes) == 0 {
			return nil, fmt.Errorf("error_code 规则未配置任何错误码")
		}
	case MatchStatusCode:
		for _, v := range splitList(r.Pattern) {
			sr, err := parseStatusRange(v)
			if err != nil {
				return nil, err
			}
			cr.statuses = append(cr.statuses, sr)
		}
		if len(cr.statuses) == 0 {
			return nil, fmt.Errorf("status_code 规则未配置任何状态码")
		}
	case MatchUpstreamText:
		subs := splitLines(r.Pattern)
		if !r.CaseSensitive {
			for i := range subs {
				subs[i] = strings.ToLower(subs[i])
			}
		}
		cr.subs = dedupe(subs)
		if len(cr.subs) == 0 {
			return nil, fmt.Errorf("upstream_text 规则的子串表为空")
		}
	default:
		return nil, fmt.Errorf("未知的 match_type: %q", r.MatchType)
	}

	cr.modelPats = splitList(strings.ToLower(r.ModelScope))
	if g := splitList(r.GroupScope); len(g) > 0 {
		cr.groups = make(map[string]struct{}, len(g))
		for _, v := range g {
			cr.groups[v] = struct{}{}
		}
	}
	return cr, nil
}

// ValidateRule 是管理端写入前的校验。与 compile 共用编译路径,
// 保证"管理端保存成功"等价于"运行期一定能编译"。
func ValidateRule(r *Rule) error {
	switch r.Phase {
	case PhasePrompt, PhaseUpstreamErr, PhaseRejectReason:
	default:
		return fmt.Errorf("phase 取值非法: %q", r.Phase)
	}
	switch r.Action {
	case ActionRecord, ActionCharge, ActionBlock, ActionBlockAndCharge:
	default:
		return fmt.Errorf("action 取值非法: %q", r.Action)
	}
	switch r.FeeMode {
	case FeeNone, FeeFixed, FeeModelPriceMultiple:
	default:
		return fmt.Errorf("fee_mode 取值非法: %q", r.FeeMode)
	}
	// 阻断只在转发之前有意义:上游阶段字节已经发出去了,再"阻断"只会误导管理员。
	if blocks(r.Action) && r.Phase != PhasePrompt {
		return fmt.Errorf("action 含 block 时 phase 必须为 %q", PhasePrompt)
	}
	if r.FeeMode != FeeNone && !charges(r.Action) {
		return fmt.Errorf("fee_mode 非 none 时 action 必须含 charge")
	}
	// prompt 阶段拿不到上游错误,这三种匹配方式在该阶段永远不会命中,
	// 允许保存等于埋一条静默失效的规则。
	if r.Phase == PhasePrompt {
		switch r.MatchType {
		case MatchErrorCode, MatchStatusCode, MatchUpstreamText:
			return fmt.Errorf("match_type %q 只能用于上游阶段", r.MatchType)
		}
	}
	if len(r.Pattern) > 8192 {
		return fmt.Errorf("pattern 过长(%d 字节,上限 8192)", len(r.Pattern))
	}
	if r.CountWeight < 0 {
		return fmt.Errorf("count_weight 不得为负数")
	}
	if r.FeeMaxQuota < 0 || r.FeeMaxQuota > int64(common.MaxQuota) {
		return fmt.Errorf("fee_max_quota 必须在 0..%d 之间", common.MaxQuota)
	}
	if r.FeeFixed.IsNegative() || r.FeeMultiple.IsNegative() {
		return fmt.Errorf("fee_fixed / fee_multiple 不得为负数")
	}
	// 上界必须与 YAML 的 violation.fee_multiplier 同口径(config/validate.go 校验 0..100)。
	// 只校验非负的话,规则级倍数就是一条绕过全局限制的旁路:管理端存一个 1e9,
	// 一旦运维把 violation.max_fee_quota 设成 0(在 checkQuotaCap 里合法,含义是"不限"),
	// computeFee 的两道 clamp 全部失效,单条规则即可一次扣光用户余额。
	if r.FeeMultiple.GreaterThan(decimal.NewFromInt(maxFeeMultiple)) {
		return fmt.Errorf("fee_multiple 必须在 0..%d 之间", maxFeeMultiple)
	}
	_, err := compile(*r)
	return err
}

func blocks(action string) bool {
	return action == ActionBlock || action == ActionBlockAndCharge
}

func charges(action string) bool {
	return action == ActionCharge || action == ActionBlockAndCharge
}

// ───────────────────────────── 作用域 ─────────────────────────────

// inScope 判断规则是否作用于当前模型与分组。空作用域 = 全部。
func (cr *compiledRule) inScope(model, group string) bool {
	if len(cr.groups) > 0 {
		if _, ok := cr.groups[group]; !ok {
			return false
		}
	}
	if len(cr.modelPats) == 0 {
		return true
	}
	m := strings.ToLower(model)
	for _, p := range cr.modelPats {
		if matchGlob(p, m) {
			return true
		}
	}
	return false
}

// matchGlob 只支持前后缀通配。刻意不引入完整 glob:模型名里出现的 `*`
// 一律按通配处理即可,而更复杂的语法会让管理员写出自己也预测不了的作用域。
func matchGlob(pattern, s string) bool {
	switch {
	case pattern == "*":
		return true
	case strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") && len(pattern) > 2:
		return strings.Contains(s, pattern[1:len(pattern)-1])
	case strings.HasPrefix(pattern, "*"):
		return strings.HasSuffix(s, pattern[1:])
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(s, pattern[:len(pattern)-1])
	default:
		return pattern == s
	}
}

// ───────────────────────────── 匹配 ─────────────────────────────

// scanInput 是一次匹配的全部输入。上游阶段的四个字段在 prompt 阶段为空。
type scanInput struct {
	Model string
	Group string

	Text string // prompt 归一化文本

	ErrCode      string
	StatusCode   int
	UpstreamText string
	RejectReason string
}

// verdict 是匹配结论。只取优先级最高的一条规则作为处置依据 ——
// 一次请求扣两次费、封两次号在任何口径下都是错的。
type verdict struct {
	Rule    *compiledRule
	Terms   []string
	Snippet string
	Elapsed time.Duration
	Timeout bool
}

// scanPrompt 执行 prompt 阶段匹配。
func scanPrompt(s *snapshot, in scanInput) *verdict {
	return scan(s.promptRules, s.promptWords, in, in.Text)
}

// scanPost 执行上游阶段匹配。关键词/正则作用于"错误文本 + 软违规原因"的拼接,
// 这样一条词表规则既能匹配上游错误消息,也能匹配 content_filter 之类的软信号。
func scanPost(s *snapshot, in scanInput) *verdict {
	text := in.UpstreamText
	if in.RejectReason != "" {
		text = text + "\n" + in.RejectReason
	}
	return scan(s.postRules, s.postWords, in, text)
}

func scan(rules []*compiledRule, dict []string, in scanInput, text string) *verdict {
	if len(rules) == 0 {
		return nil
	}
	start := time.Now()
	budget := time.Duration(config.Get().Violation.ScanTimeoutMs) * time.Millisecond
	if budget <= 0 {
		budget = 20 * time.Millisecond
	}
	deadline := start.Add(budget)

	lower := strings.ToLower(text)

	// 关键词只扫一次:全部 keyword 规则共用一份词典,命中集合再按规则求交。
	var hitWords map[string]struct{}
	if len(dict) > 0 && lower != "" {
		if ok, words := service.AcSearch(lower, dict, false); ok {
			hitWords = make(map[string]struct{}, len(words))
			for _, w := range words {
				hitWords[strings.ToLower(w)] = struct{}{}
			}
		}
	}

	timedOut := false
	for _, cr := range rules {
		// 超时预算按规则粒度检查。RE2 单条正则在 64KB 上界内的耗时是可预测的,
		// 真正需要防的是"规则数 × 文本长度"的累积开销。
		if time.Now().After(deadline) {
			timedOut = true
			scanTimeouts.Add(1)
			break
		}
		if !cr.inScope(in.Model, in.Group) {
			continue
		}
		terms := matchRule(cr, in, text, lower, hitWords)
		if len(terms) == 0 {
			continue
		}
		return &verdict{
			Rule:    cr,
			Terms:   clipTerms(terms),
			Snippet: snippetAround(text, terms[0]),
			Elapsed: time.Since(start),
		}
	}
	if timedOut {
		return &verdict{Timeout: true, Elapsed: time.Since(start)}
	}
	return nil
}

func matchRule(cr *compiledRule, in scanInput, text, lower string, hitWords map[string]struct{}) []string {
	switch cr.R.MatchType {
	case MatchKeyword:
		if len(hitWords) == 0 {
			return nil
		}
		var out []string
		for _, w := range cr.words {
			if _, ok := hitWords[w]; ok {
				out = append(out, w)
			}
		}
		return out
	case MatchRegex:
		if text == "" {
			return nil
		}
		if m := cr.re.FindString(text); m != "" {
			return []string{m}
		}
		// 零宽匹配(如 `^$`)也算命中,但没有可展示的片段。
		if cr.re.MatchString(text) {
			return []string{cr.R.Name}
		}
		return nil
	case MatchErrorCode:
		if in.ErrCode == "" {
			return nil
		}
		if _, ok := cr.codes[strings.ToLower(in.ErrCode)]; ok {
			return []string{in.ErrCode}
		}
		return nil
	case MatchStatusCode:
		for _, sr := range cr.statuses {
			if in.StatusCode >= sr.lo && in.StatusCode <= sr.hi {
				return []string{strconv.Itoa(in.StatusCode)}
			}
		}
		return nil
	case MatchUpstreamText:
		hay := text
		if !cr.R.CaseSensitive {
			hay = lower
		}
		var out []string
		for _, sub := range cr.subs {
			if sub != "" && strings.Contains(hay, sub) {
				out = append(out, sub)
			}
		}
		return out
	}
	return nil
}

// ───────────────────────────── 文本工具 ─────────────────────────────

// clipHeadTail 把文本压到 maxScanBytes 以内,取头尾各一半。
//
// 取头尾而不是只取头:违规内容常被刷子塞在长 padding 之后;
// 按 rune 边界切是硬要求,否则 AC 会拿到替换字符,写库时 utf8mb4 直接报错。
func clipHeadTail(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := max / 2
	head := s[:safeCut(s, half)]
	tailFrom := len(s) - half
	for tailFrom < len(s) && !utf8.RuneStart(s[tailFrom]) {
		tailFrom++
	}
	return head + "\n...[truncated]...\n" + s[tailFrom:]
}

// safeCut 返回不超过 n 且落在 rune 边界上的切点。
func safeCut(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// snippetAround 截取命中点前后各 maxSnippetRunes 个字符的窗口,供管理端快速研判。
func snippetAround(text, term string) string {
	if text == "" || term == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(text), strings.ToLower(term))
	if idx < 0 {
		return clipHeadTail(text, 2*maxSnippetRunes)
	}
	runes := []rune(text)
	// 把字节下标换算成 rune 下标,避免中文场景下窗口偏移。
	at := utf8.RuneCountInString(text[:idx])
	lo := at - maxSnippetRunes
	if lo < 0 {
		lo = 0
	}
	hi := at + utf8.RuneCountInString(term) + maxSnippetRunes
	if hi > len(runes) {
		hi = len(runes)
	}
	return string(runes[lo:hi])
}

// clipTerms 限制命中词的数量与单项长度。
func clipTerms(terms []string) []string {
	if len(terms) > maxMatchedTerms {
		terms = terms[:maxMatchedTerms]
	}
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		if len(t) > maxMatchedTermLen {
			t = t[:safeCut(t, maxMatchedTermLen)]
		}
		out = append(out, t)
	}
	return out
}

func splitLines(s string) []string {
	return splitBy(s, func(r rune) bool { return r == '\n' || r == '\r' })
}

func splitList(s string) []string {
	return splitBy(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
}

func splitBy(s string, sep func(rune) bool) []string {
	parts := strings.FieldsFunc(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func parseStatusRange(s string) (statusRange, error) {
	if lo, hi, ok := strings.Cut(s, "-"); ok {
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil || a > b {
			return statusRange{}, fmt.Errorf("非法的状态码区间: %q", s)
		}
		return statusRange{lo: a, hi: b}, nil
	}
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return statusRange{}, fmt.Errorf("非法的状态码: %q", s)
	}
	return statusRange{lo: v, hi: v}, nil
}
