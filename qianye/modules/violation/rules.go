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
	"github.com/QuantumNous/new-api/qianye/groupname"
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

// maxRequestRateThreshold 是 request_rate 规则阈值的上界。
//
// 上界的作用不是"防止阈值太大"(太大只是永不命中),而是把 pattern 锁死成一个
// 能被 strconv.Atoi 无溢出解析的十进制整数:没有上界时 "99999999999999999999"
// 会在 32 位平台上解析失败、在 64 位平台上解析成功,同一条规则在两台机器上
// 一条编译不过、一条永不命中,而两边都不会报错。
const maxRequestRateThreshold = 1_000_000

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

	// statusScope 是 StatusScope 列编译出来的前置条件区间表。空 = 不限状态码。
	// 与 statuses 是两回事:statuses 是 MatchStatusCode 这一种**匹配方式**的模式,
	// statusScope 是一道与全部匹配方式正交的**作用域**闸(见 model.go 的说明)。
	statusScope []statusRange

	// rateThreshold 是 request_rate 规则的每分钟阈值,其余匹配方式恒为 0。
	rateThreshold int

	// aiCats 是 ai_review 规则的违规类型白名单(pattern 编译而来)。
	// nil 或空 = 不限类型,只要模型判违规就命中。
	aiCats map[string]struct{}

	// scopeMatcher 是模型 + 分组这两道作用域闸。
	//
	// 内嵌一个共享类型而不是继续摊三个裸字段:AI 审核的作用域策略
	// (aireview_scope.go)判的是**同一件事** —— 同一个 UsingGroup、同一份
	// 前后缀通配语法。让它们共用字面意义上的同一段代码,是因为第二份实现
	// 的失效形状已经在本模块出现过两次(groupname 大小写):两份判据不一致时,
	// 症状是"界面上配好了、线上从不生效",而没有任何报错指向它。
	scopeMatcher
}

// scopeMatcher 是「模型 + 用户分组」作用域的唯一一份判据。
//
// 它同时服务违规规则(compiledRule)与 AI 审核作用域策略(aiScopeRT)。
// 两者对"作用域"的定义必须逐字节相同,否则运营在两页上看到同样的输入框、
// 得到不同的结果。
type scopeMatcher struct {
	// modelPats 为空表示全部模型;groups 为空表示全部分组。
	// groups 的键一律是 groupname.Effective 归一后的比较键。
	modelPats []string
	groups    map[string]struct{}
	// groupExclude 为真时 groups 是黑名单(豁免分组),否则是白名单。
	groupExclude bool
}

// compileScope 把三格文本(模型作用域 / 分组作用域 / 方向)编译成运行期判据。
//
// 分组名必须走 groupname:扩展库与主库的分组列都是大小写不敏感的排序规则,
// 而 Go 的 map 查表是精确匹配。管理端配 "VIP"、用户实际分组是 "vip" 时,
// 这里不归一就是一条保存成功、界面正常、线上永不命中的配置 ——
// 同形缺陷在 commission 与 transfer 已经各出过一次。
func compileScope(modelScope, groupScope, groupScopeMode string) scopeMatcher {
	m := scopeMatcher{modelPats: splitList(strings.ToLower(modelScope))}
	if g := splitList(groupScope); len(g) > 0 {
		m.groups = make(map[string]struct{}, len(g))
		for _, v := range g {
			m.groups[groupname.Effective(v)] = struct{}{}
		}
		m.groupExclude = groupScopeMode == GroupScopeExclude
	}
	return m
}

// snapshot 是规则的只读内存视图。整体替换(atomic.Pointer),读端零锁;
// 正在使用旧快照的请求持有引用,不会读到半成品。
type snapshot struct {
	version int64
	loadAt  int64

	promptRules []*compiledRule
	postRules   []*compiledRule
	// asyncRules 是 phase = post_async 的规则(目前只有 ai_review 会用它)。
	// 独立一桶而不是并进 postRules:后者的扫描发生在上游返回错误之后、
	// relay 线程上,而这一桶整个跑在异步 worker 里,两者的输入与预算都不同。
	asyncRules []*compiledRule

	// shadowRules / enforceRules 是已启用规则按模式的分布。
	//
	// 摆出来是因为删掉全局开关之后,"现在到底有没有规则在真实扣钱"不再是一个
	// 布尔值。管理端必须能一眼回答它,否则运营会以为自己还在观察期,
	// 而其实已经有几条规则在扣费 —— 那正是旧的全局横幅唯一还算有用的功能。
	shadowRules  int
	enforceRules int

	// hasRate 表示快照里存在至少一条 request_rate 规则。
	//
	// 它决定热路径要不要去推进请求频率计数(一次 Redis 往返)。没有这个标志就只有
	// 两种选择:每个请求都数(为一个没人用的功能给全站加一次 Redis 往返),
	// 或者在扫描到规则时才懒惰地数(那样同一个请求会不会被计入取决于前面有没有
	// 更高优先级的规则先命中 —— 计数会随规则表的形状漂移,彻底不可解释)。
	hasRate bool

	// ai 是 AI 审核的运行期配置(设置行 + 已解密的渠道池)。
	//
	// nil 表示"这次请求一个字节都不用为 AI 审核付出":总开关关着、设置行还没建、
	// 抽样率为 0、或者一个可用渠道都没有 —— 四种情况在热路径上收敛成同一个
	// 指针判空,而项目方要的「概率为 0 时整条路径零开销,不要每次都算一遍再丢弃」
	// 正是它。判据在快照构建时算一次,不在每个请求上现算。
	ai *aiRuntime
	// hasAIPrompt / hasAIAsync 表示两个时机各自有没有 ai_review 规则。
	//
	// 两个布尔而不是一个:只配了转发后审核的站点不该为转发前那条同步路径付
	// 任何代价,而那条路径的代价是给每个被抽中的请求加一次外部调用的延迟。
	hasAIPrompt bool
	hasAIAsync  bool

	// promptWords / postWords 是各阶段全部 keyword 规则的词表并集,
	// 一次 AC 扫描服务全部关键词规则,而不是每条规则扫一遍。
	promptWords []string
	postWords   []string

	// catById / catByKey / catFallback 是违规类型表的只读视图。
	//
	// 与规则同快照、同版本号,不另建一份 TTL 独立的类型缓存 —— 理由写在
	// category.go 顶部:两份缓存会让一条命中读到"新规则 + 旧类型",
	// 计数加到旧类型上,而这种错位在任何日志里都看不出来。
	//
	// catFallback 是「未分类」那一行,catById 里查不到的 id 一律折进它
	// (见 categoryForRule)。种子还没落地时它是零值,此时类型计数整体跳过,
	// 账号总量线照常工作 —— 与本模块其余部分的 fail-open 同口径。
	catById     map[int64]Category
	catByKey    map[string]Category
	catFallback Category

	// aiVocab 是发给审核模型的违规类型清单,从**同一批**类型行算出来。
	// 与 catById 同源同刻:两次查询会让规则、类型计数、AI 闭集来自不同时刻,
	// 而"规则按新类型过滤、闭集还是旧的"的表现是那条规则永不命中。
	aiVocab aiVocabulary
}

func (s *snapshot) hasPrompt() bool { return s != nil && len(s.promptRules) > 0 }
func (s *snapshot) hasPost() bool   { return s != nil && len(s.postRules) > 0 }

// aiOn 是热路径上"这次请求要不要考虑 AI 审核"的唯一判据:
// 一次指针判空加两次布尔读,没有分配、没有加锁、没有随机数。
func (s *snapshot) aiOn() bool {
	return s != nil && s.ai != nil && (s.hasAIPrompt || s.hasAIAsync)
}

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

	// 类型表与规则表一起装进同一份快照。查询失败不放弃整份快照:类型只影响
	// "这次命中算哪一类、要不要撞类型线",而规则决定"要不要拦、要不要扣钱" ——
	// 为了前者放弃后者是把主要能力赔给次要能力。此时 catById 为空,
	// categoryForRule 全部折进零值兜底,类型计数整体跳过,账号总量线不受影响。
	var cats []Category
	if err := gdb.Find(&cats).Error; err != nil {
		refreshFails.Add(1)
		common.SysError("qianye/violation: 违规类型加载失败(本次快照不含类型,类型计数暂停): " + err.Error())
	} else {
		s.catById = make(map[int64]Category, len(cats))
		s.catByKey = make(map[string]Category, len(cats))
		for _, c := range cats {
			s.catById[c.Id] = c
			s.catByKey[c.Key] = c
			if c.IsFallback {
				s.catFallback = c
			}
		}
	}
	// 类型表加载失败时 cats 为 nil,闭集因此是零值 —— resolveCategory 会把
	// 一切判定折进兜底类型,不限类型的 ai_review 规则照常命中。少一个能力,
	// 不是整体停摆,与本模块其余部分同口径。
	s.aiVocab = buildAIVocabulary(cats)

	for i := range rows {
		cr, err := compile(rows[i])
		if err != nil {
			// 单条规则编译失败绝不能拖垮整份快照:那等于一次手滑就让全部风控失效。
			common.SysError(fmt.Sprintf("qianye/violation: 规则 %d(%s)编译失败,已跳过: %v",
				rows[i].Id, rows[i].Name, err))
			continue
		}
		// 判据与 effectiveShadow 完全一致(`== ModeEnforce`),不能写成
		// `== ModeShadow`:那样空 mode 的行会既不算影子也不算真实,统计里凭空少掉。
		if cr.R.Mode == ModeEnforce {
			s.enforceRules++
		} else {
			s.shadowRules++
		}
		switch cr.R.Phase {
		case PhasePrompt:
			s.promptRules = append(s.promptRules, cr)
			s.promptWords = append(s.promptWords, cr.words...)
			switch cr.R.MatchType {
			case MatchRequestRate:
				s.hasRate = true
			case MatchAIReview:
				s.hasAIPrompt = true
			}
		case PhasePostAsync:
			// 转发后(异步)审核只可能是 ai_review —— ValidateRule 拒绝其它匹配方式
			// 落在这一档上,因为别的判据在本地就能算,没有任何理由推迟到异步。
			s.asyncRules = append(s.asyncRules, cr)
			s.hasAIAsync = true
		default:
			s.postRules = append(s.postRules, cr)
			s.postWords = append(s.postWords, cr.words...)
		}
	}
	s.promptWords = dedupe(s.promptWords)
	s.postWords = dedupe(s.postWords)

	// AI 运行期配置与规则同快照、同版本号 —— 与类型表同一条理由(见上面那段):
	// 两份独立 TTL 的缓存会让一次请求读到"新规则 + 旧渠道",而那种错位的表现是
	// 「刚删掉的渠道还在被调用」,在任何日志里都看不出因果。
	// 装配失败同样不放弃整份快照:AI 审核停摆好过全部规则停摆。
	if rt, err := buildAIRuntime(gdb, s.hasAIPrompt || s.hasAIAsync, s.aiVocab); err != nil {
		common.SysError("qianye/violation: AI 审核配置加载失败(本次快照不含 AI 审核,全部放行): " + err.Error())
	} else {
		s.ai = rt
	}

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
	case MatchRequestRate:
		n, err := strconv.Atoi(strings.TrimSpace(r.Pattern))
		if err != nil {
			return nil, fmt.Errorf("request_rate 规则的阈值必须是整数: %q", r.Pattern)
		}
		// 下界是 1 而不是 0:阈值 0 会让每一个非流式请求都命中,包括那些根本没有
		// 推进过计数的(计数失败时热路径 fail-open 返回 0)。热路径也依赖这条下界 ——
		// PreRelayGuard 在"没有 prompt 文本且计数为 0"时直接返回,前提就是
		// 计数 0 不可能命中任何频率规则。
		if n < 1 || n > maxRequestRateThreshold {
			return nil, fmt.Errorf("request_rate 阈值必须在 1..%d 之间,当前为 %d",
				maxRequestRateThreshold, n)
		}
		cr.rateThreshold = n
	case MatchAIReview:
		// pattern 是违规类型白名单,允许为空(= 只要模型判违规就命中)。
		// 归一成小写:模型返回的 category 也走 normalizeAICategory 归小写,
		// 两侧不同口径就会得到一条"保存成功、界面正常、线上永不命中"的规则 ——
		// 与 groupname 那两次同形缺陷完全一样。
		if cats := splitList(strings.ToLower(r.Pattern)); len(cats) > 0 {
			cr.aiCats = make(map[string]struct{}, len(cats))
			for _, v := range cats {
				cr.aiCats[v] = struct{}{}
			}
		}
	default:
		return nil, fmt.Errorf("未知的 match_type: %q", r.MatchType)
	}

	// 状态码作用域与 MatchStatusCode 的 pattern 共用 parseStatusRange:
	// 管理员在两个格子里写的是同一种语法,给它们两套解析器是在制造第二份事实。
	for _, v := range splitList(r.StatusScope) {
		sr, err := parseStatusRange(v)
		if err != nil {
			return nil, fmt.Errorf("状态码作用域: %w", err)
		}
		cr.statusScope = append(cr.statusScope, sr)
	}

	cr.scopeMatcher = compileScope(r.ModelScope, r.GroupScope, r.GroupScopeMode)
	return cr, nil
}

// ruleVarcharLimit 是 Rule 上一个 varchar 列的字符数上限。
type ruleVarcharLimit struct {
	// Field 是结构体字段名。它不参与校验,只用来跟 model.go 的 gorm tag 对账
	// (见 TestRuleVarcharLimitsMatchColumnTags)。
	Field string
	// Label 是报错里用的中文名,与管理端表单上的标签一致 —— 管理员看到
	// "备注过长" 才知道该去删哪一格,看到 "remark" 不知道。
	Label string
	Max   int
	Get   func(*Rule) string
}

// ruleVarcharLimits 是写入前的长度校验表,一行对应 model.go 里的一个 varchar 列。
//
// # 为什么生产代码里要有这张表
//
// 没有它的时候,超长字段是靠 MySQL 的 `Error 1406 Data too long` 挡下来的,
// 而那条错误会被 internalError 折成一句"处理失败,请稍后重试":既没有字段名也没有
// 长度提示,管理员无从判断是哪一格填多了。SQLite 更糟 —— 它根本不校验 varchar 长度,
// 于是同一份数据在 SQLite 上存得进去、迁到 MySQL 就整条 INSERT 失败。
//
// # 为什么不是"截断"
//
// 这里原本是一串 `truncate(r.Remark, 512)` 式的静默截断。静默截断有两个问题:
// 一是管理员保存成功、回到列表才发现备注被拦腰截断,没有任何提示;二是那个
// truncate 是**字节**口径的,一段 300 字的中文(约 900 字节)会在第 170 字处被切掉,
// 而 300 字在 varchar(512) 的字符口径下完全合法 —— 可用容量被砍到标称值的三分之一。
//
// # 计长口径:rune
//
// MySQL(utf8mb4,见 qianye/db/db.go 的 DSN)与 PostgreSQL 的 varchar(N) 都是
// N 个**字符**,Go 的 rune 数正是它们说的字符数。byte 口径不是"更保守",
// 它是另一条不成立的约束,会在正确的中文数据上误报。
//
// # 两份事实的对账
//
// 这张表是 gorm tag 的生产侧副本。两侧一旦漂移,校验就会放过数据库拒绝的行
// (列被改窄)或拦下数据库接受的行(列被改宽)—— 两种都是本轮事故的同一个形状。
// TestRuleVarcharLimitsMatchColumnTags 逐字段比对,不允许任何一侧单独变。
var ruleVarcharLimits = []ruleVarcharLimit{
	{Field: "Name", Label: "规则名称", Max: 128, Get: func(r *Rule) string { return r.Name }},
	{Field: "Remark", Label: "备注", Max: 512, Get: func(r *Rule) string { return r.Remark }},
	{Field: "PublicReason", Label: "对外原因", Max: 128, Get: func(r *Rule) string { return r.PublicReason }},
	{Field: "Mode", Label: "模式", Max: 16, Get: func(r *Rule) string { return r.Mode }},
	{Field: "Source", Label: "来源", Max: 16, Get: func(r *Rule) string { return r.Source }},
	{Field: "BuiltinKey", Label: "内置规则 key", Max: 64, Get: func(r *Rule) string { return r.BuiltinKey }},
	{Field: "BuiltinFingerprint", Label: "内置规则指纹", Max: 64, Get: func(r *Rule) string { return r.BuiltinFingerprint }},
	{Field: "Phase", Label: "生效阶段", Max: 24, Get: func(r *Rule) string { return r.Phase }},
	{Field: "MatchType", Label: "匹配方式", Max: 24, Get: func(r *Rule) string { return r.MatchType }},
	{Field: "StatusScope", Label: "状态码作用域", Max: 64, Get: func(r *Rule) string { return r.StatusScope }},
	{Field: "ModelScope", Label: "模型作用域", Max: 2048, Get: func(r *Rule) string { return r.ModelScope }},
	{Field: "GroupScope", Label: "分组作用域", Max: 1024, Get: func(r *Rule) string { return r.GroupScope }},
	{Field: "GroupScopeMode", Label: "分组作用域方向", Max: 8, Get: func(r *Rule) string { return r.GroupScopeMode }},
	{Field: "Action", Label: "处置动作", Max: 24, Get: func(r *Rule) string { return r.Action }},
	{Field: "FeeMode", Label: "计费方式", Max: 24, Get: func(r *Rule) string { return r.FeeMode }},
	{Field: "BlockMessage", Label: "阻断文案", Max: 512, Get: func(r *Rule) string { return r.BlockMessage }},
}

// ValidateRule 是管理端写入前的校验。与 compile 共用编译路径,
// 保证"管理端保存成功"等价于"运行期一定能编译"。
func ValidateRule(r *Rule) error {
	switch r.Phase {
	case PhasePrompt, PhaseUpstreamErr, PhaseRejectReason, PhasePostAsync:
	default:
		return fmt.Errorf("phase 取值非法: %q", r.Phase)
	}
	switch r.Action {
	case ActionRecord, ActionCharge, ActionBlock, ActionBlockAndCharge:
	default:
		return fmt.Errorf("action 取值非法: %q", r.Action)
	}
	// mode 在这里必须是枚举里的值。运行期对未知取值是按影子兜底的(见 effectiveShadow),
	// 但写入口不能靠兜底放行:那会让"保存成功"与"我设成了真实执行"之间出现一个
	// 只有读源码才能发现的落差。
	switch r.Mode {
	case ModeShadow, ModeEnforce:
	default:
		return fmt.Errorf("mode 取值非法: %q(只能是 %q 或 %q)", r.Mode, ModeShadow, ModeEnforce)
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
	// 状态码作用域只有"已经拿到上游响应"的两个阶段填得有意义。
	//
	// scanInput.StatusCode 只在 PostRelayGuard 里被填(见 guard.go),prompt 与
	// post_async 两个阶段拿到的恒是 0,而 statusInScope 对任何非空作用域都会把 0
	// 判在外面 —— 于是这条规则永久关在门外:保存成功、界面正常、命中量恒为 0,
	// 没有任何报错。
	//
	// prompt 那一半原本就挡住了;post_async 是**本轮新增**的阶段(AI 转发后审核
	// 专用),它同样拿不到状态码。漏掉它的后果最隐蔽:转发后审核本来就是"秋后
	// 算账",没人会盯着它当场生效,一条永不命中的规则可以挂几个月不被发现。
	if r.Phase == PhasePrompt || r.Phase == PhasePostAsync {
		if strings.TrimSpace(r.StatusScope) != "" {
			return fmt.Errorf("状态码作用域只能用于上游阶段(当前 phase 为 %q):"+
				"该阶段还没有上游响应,状态码恒为 0,填了它这条规则一次都不会命中", r.Phase)
		}
	}
	// 反过来:请求频率只有在"即将发往上游"这一刻才有意义。挂在上游阶段的话,
	// 只有失败的请求会被评估,而蒸馏采集方的请求绝大多数是成功的 —— 那是一条
	// 保存得下去、也确实会执行、但永远数不到真实频率的规则。
	if r.MatchType == MatchRequestRate && r.Phase != PhasePrompt {
		return fmt.Errorf("match_type %q 只能用于 %q 阶段", MatchRequestRate, PhasePrompt)
	}
	if err := validateAIRule(r); err != nil {
		return err
	}
	switch r.GroupScopeMode {
	case "", GroupScopeInclude, GroupScopeExclude:
	default:
		return fmt.Errorf("group_scope_mode 取值非法: %q", r.GroupScopeMode)
	}
	if err := validateGroupScopeList(r.GroupScope); err != nil {
		return err
	}
	// 长度校验排在枚举校验之后:一个填错的 mode 该报"取值非法"而不是"模式过长",
	// 前者直接指向问题,后者会把人引去数长度。
	for _, lim := range ruleVarcharLimits {
		if n := utf8.RuneCountInString(lim.Get(r)); n > lim.Max {
			return fmt.Errorf("%s过长(%d 字,上限 %d 字)", lim.Label, n, lim.Max)
		}
	}
	// Pattern 是 text 列,没有字符数上限,这里挡的是"别把一整本书编译成正则"。
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

// applies 是**全部作用域闸的唯一入口**:模型、分组、上游状态码。
//
// 提成一个方法而不是让调用方自己 && 起来:作用域闸有三道,而它们的调用点有两处
// (scan 的热路径与管理端试跑)。两处各写一遍 && 的后果不是"多写一行",
// 是两处迟早不一致 —— 而不一致的表现恰好是最坏的那种:试跑面板说"不在作用域"、
// 线上照样命中,或者反过来。管理端试跑的全部价值就是它与线上判据逐字节相同。
func (cr *compiledRule) applies(in scanInput) bool {
	return cr.inScope(in.Model, in.Group) && cr.statusInScope(in.StatusCode)
}

// statusInScope 判断上游状态码是否落在规则声明的状态码作用域内。空作用域 = 全部。
//
// 这道闸让"status_code + 正文"成为一条规则:正文由 MatchType 判,状态码由这里判,
// 两者是 AND。没有它的话,项目方给的那条 Anthropic 拒绝
// (400 + "flagged for possible cybersecurity risk")只能拆成两条规则,
// 而两条规则会各自命中、各自计数、各自扣费 —— 一次上游拒绝算成两次违规。
func (cr *compiledRule) statusInScope(status int) bool {
	if len(cr.statusScope) == 0 {
		return true
	}
	for _, sr := range cr.statusScope {
		if status >= sr.lo && status <= sr.hi {
			return true
		}
	}
	return false
}

// inScope 判断作用域是否覆盖当前模型与分组。空作用域 = 全部。
//
// 挂在 scopeMatcher 上、由 compiledRule 内嵌继承:违规规则与 AI 审核作用域
// 策略调用的是同一个方法体,不存在"两处各判一次"的可能。
func (m scopeMatcher) inScope(model, group string) bool {
	return m.groupInScope(group) && m.modelInScope(model)
}

// groupInScope 与 modelInScope 拆开的理由与 applies 提成方法完全相同:
// 管理端试跑必须回答"是哪一道闸把它挡在门外"(模型?分组?状态码?),
// 而"未命中"与"不在作用域,而且是分组那一道"对管理员是两个截然不同的下一步。
// 让试跑自己再判一次名单,就是本文件反复警告的那种两份判据 —— 它迟早与线上不一致。
//
// 分组名两侧都走 groupname.Effective:编译时归一了名单,判定时不归一等于没归一。
// 顺带把 UsingGroup 为空的历史账号折叠进 default —— 那批账号恰恰最可疑,
// 而在此之前任何一条挂在 default 上的规则都盖不住它们。
func (m scopeMatcher) groupInScope(group string) bool {
	if len(m.groups) == 0 {
		return true
	}
	_, listed := m.groups[groupname.Effective(group)]
	// include 模式(groupExclude=false):不在名单里 → 不生效。
	// exclude 模式(groupExclude=true):在名单里 → 豁免,不生效。
	return listed != m.groupExclude
}

// validateGroupScopeList 挡住写在分组作用域里的通配符。
//
// 模型作用域走 matchGlob(支持 `*`、前缀、后缀),分组作用域走的是 map 精确查表 ——
// 两列在同一张表单上紧挨着,语法却不一样。写 `*` 的人本意是"全站都监控",
// 实际得到的是"一个都不监控"(除非真有一个名字就叫 `*` 的分组),而界面上没有
// 任何一处会告诉他:AI 审核那一侧的"必须绑定分组"闸只看这一格是不是空串,
// `*` 照样通过,汇总里还显示成"已绑定"。
//
// 这道闸尤其重要,因为分组作用域正是"哪些用户的请求正文会被发往第三方审核服务"
// 的唯一入口 —— 它错在哪个方向都必须当场报出来,而不是变成一条永不生效的策略。
//
// 规则那一侧同理:一条 enforce 的规则写 `*` 就是一条永不生效的规则。
func validateGroupScopeList(raw string) error {
	for _, item := range splitList(raw) {
		if strings.Contains(item, "*") {
			return fmt.Errorf("用户分组作用域不支持通配符(%q):这一列是**精确名单**,"+
				"`*` 会被当成一个名叫 `*` 的分组,于是这一档一个真实分组都匹配不到。"+
				"模型作用域才支持 `*`,两列语法不同。"+
				"请逐个列出要生效的分组;违规规则想对全站生效就把这一格留空", item)
		}
	}
	return nil
}

func (m scopeMatcher) modelInScope(model string) bool {
	if len(m.modelPats) == 0 {
		return true
	}
	low := strings.ToLower(model)
	for _, p := range m.modelPats {
		if matchGlob(p, low) {
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

	// RateCount 是本次请求推进之后、该用户在 rateWindowSeconds 窗口内的非流式
	// 请求条数(含本次)。只有 prompt 阶段会填,且只有快照里存在 request_rate
	// 规则时才会真的去数;计数失败一律 fail-open 落回 0。
	RateCount int

	ErrCode      string
	StatusCode   int
	UpstreamText string
	RejectReason string

	// AI 是本次请求的外部审核结论。只有被抽中且审核成功返回时才非 nil ——
	// 也就是说 ai_review 规则在**没抽中、审核失败、审核超时**时一律不命中,
	// 这正是"失败即放行"在匹配层的表达。
	AI *aiOutcome
}

// verdict 是匹配结论。只取优先级最高的一条规则作为处置依据 ——
// 一次请求扣两次费、封两次号在任何口径下都是错的。
type verdict struct {
	Rule    *compiledRule
	Terms   []string
	Snippet string
	Elapsed time.Duration
	Timeout bool
	// CategoryOverride 非 0 时**覆盖规则自己绑的违规类型**,由 newRecord 消费。
	//
	// 目前唯一的来源是 AI 审核作用域策略上的「这一档的命中一律记为」。
	// 本地扫描永远留 0:那条路上没有第二个类型来源,而多一个恒为 0 的字段
	// 比多一条"只有 AI 才走"的分支容易读得多。
	//
	// 它是**记录归档**的覆盖,不是判据的覆盖:命中与否仍然完全由规则决定。
	CategoryOverride int64
}

// scanPrompt 执行 prompt 阶段匹配。
func scanPrompt(s *snapshot, in scanInput) *verdict {
	return scan(s.promptRules, s.promptWords, in, in.Text)
}

// scanPost 执行上游阶段匹配。关键词/正则作用于"错误文本 + 软违规原因"的拼接,
// 这样一条词表规则既能匹配上游错误消息,也能匹配 content_filter 之类的软信号。
func scanPost(s *snapshot, in scanInput) *verdict {
	return scan(s.postRules, s.postWords, in, scanPostText(in))
}

// scanPostText 拼出上游阶段参与文本匹配的那一段。
//
// 提成函数是因为管理端试跑必须用**同一段**拼接:试跑面板的全部价值是
// "它与线上判据逐字节相同",而这里的拼接顺序(错误正文在前、软违规原因在后,
// 中间一个换行)会影响 snippetAround 截出来的窗口,抄第二份迟早会漂。
func scanPostText(in scanInput) string {
	if in.RejectReason == "" {
		return in.UpstreamText
	}
	return in.UpstreamText + "\n" + in.RejectReason
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

	// 字符层归一**必须在这一步**,而不是靠某条规则的模式串去兼容:
	// `Ign<U+200B>ore all previous instructions` 对 RE2 与 AC 都是一个全新的字符串,
	// 任何写得再好的规则都接不住它(实测:去噪之前,内置目录对这个输入零命中)。
	text = normalizeForScan(text)
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

	// 预算的起点在归一与词典扫描**之后**。
	//
	// 它原来取在函数第一行,于是 normalizeForScan + strings.ToLower + AcSearch
	// (词典变了要现建一次自动机)这三步的耗时也算进预算里,机器一忙就会出现
	// "一条规则都没跑就 break、按放行返回"的结局:对外只有 scanTimeouts 加 1,
	// 管理端看不到这一条压根没被扫过。注释里说的"规则数 × 文本长度的累积开销"
	// 也正是从这里开始才成立,一次性的准备工作不属于它。
	deadline := time.Now().Add(budget)
	return scanRulesBefore(rules, in, text, lower, hitWords, start, deadline)
}

// scanRulesBefore 在 deadline 之前尽可能多地评估规则。
//
// 单独一层是为了让「预算耗尽时到底跑没跑过规则」可以被直接断言:计时做判据
// 在纪律里是禁止的,而这里唯一说得清的性质就是"给一个已经过期的 deadline,
// 第一条规则仍然必须被评估"。
func scanRulesBefore(rules []*compiledRule, in scanInput, text, lower string,
	hitWords map[string]struct{}, start time.Time, deadline time.Time) *verdict {
	timedOut := false
	for i, cr := range rules {
		// 超时预算按规则粒度检查。RE2 单条正则在 64KB 上界内的耗时是可预测的,
		// 真正需要防的是"规则数 × 文本长度"的累积开销。
		//
		// 第一条规则**永远**要跑:预算的意义是"别把 N 条规则全跑完",不是
		// "一条都别跑"。零命中地放行与"扫过了、没事"在日志里长得一模一样,
		// 而后者才是这个模块存在的理由。
		if i > 0 && time.Now().After(deadline) {
			timedOut = true
			scanTimeouts.Add(1)
			break
		}
		if !cr.applies(in) {
			continue
		}
		terms := matchRule(cr, in, text, lower, hitWords)
		if len(terms) == 0 {
			continue
		}
		// 频率命中与文本无关,截一段 prompt 当"证据"只会把与判定无关的用户内容
		// 抄进一张管理端列表直接返回的表。要看上下文的规则自己开 archive_context。
		snippet := ""
		if cr.R.MatchType != MatchRequestRate {
			snippet = snippetAround(text, terms[0])
		}
		return &verdict{
			Rule:    cr,
			Terms:   clipTerms(terms),
			Snippet: snippet,
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
	case MatchRequestRate:
		if in.RateCount < cr.rateThreshold {
			return nil
		}
		// 命中词写"实测值/阈值":这是管理端复核误判时唯一能用的数字,
		// 只写阈值的话每一条记录都长得一样,分不出"刚过线"和"高出十倍"。
		return []string{fmt.Sprintf("req_rate %d>=%d/%ds", in.RateCount, cr.rateThreshold, rateWindowSeconds)}
	case MatchAIReview:
		return matchAIRule(cr, in.AI)
	}
	return nil
}

// matchAIRule 判断一条 ai_review 规则是否被这次审核结论命中。
//
// 三道闸,顺序即代价从低到高:有没有结论 → 判没判违规 → 类型与置信度对不对得上。
//
// 每一道的失败方向都是**不命中**(放行)。这不是可选的:审核没跑、跑挂了、
// 模型给了个我们不认识的类型 —— 这三件事都不能推导出"这个用户违规了"。
func matchAIRule(cr *compiledRule, out *aiOutcome) []string {
	if out == nil || !out.decided() || !out.Violated {
		return nil
	}
	if len(cr.aiCats) > 0 {
		if _, ok := cr.aiCats[out.Category]; !ok {
			return nil
		}
	}
	// 置信度下限。规则没配(0)时不设限 —— 与 fee_max_quota 等其余"0 = 不限"
	// 的字段同口径,而不是让 0 变成"必须 100% 确定"那种反直觉的严格。
	if cr.R.AIMinConfidence.IsPositive() && out.Confidence.LessThan(cr.R.AIMinConfidence) {
		return nil
	}
	// 命中词写"类型@置信度":与频率规则同理,管理端复核误判时要能一眼分出
	// "刚过线"和"模型非常确定",只写类型名的话每条记录都长得一样。
	cat := out.Category
	if cat == "" {
		cat = "unknown"
	}
	if out.CategoryUnknown {
		// 折进兜底的那一票必须在命中词上就看得出来。只写 uncategorized 的话,
		// 复核的人会以为模型明确说了"归不了类",而它实际上说的是 "porn" ——
		// 两者的下一步完全不同(前者调提示词,后者去建一个类型)。
		raw := out.RawCategory
		if raw == "" {
			raw = "空"
		}
		return []string{fmt.Sprintf("ai:%s(raw=%s)@%s", cat, raw, out.Confidence.String())}
	}
	return []string{fmt.Sprintf("ai:%s@%s", cat, out.Confidence.String())}
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

// invisibleRune 判断一个 rune 是否"排版上不占位、语义上不表意"。
//
// 这张表是刻意最小化的,只收**删掉之后人眼看到的文字不变**的字符。判据就是这一句:
// 删掉它,用户屏幕上的文字一个都不变,所以删掉它不可能凭空造出一次误伤 ——
// 而不删掉它,`Ign<U+FE0F>ore` 对 RE2 就是一个全新的字符串,再好的规则也接不住。
//
// 收录范围分四组:
//   - 零宽/连接控制:ZWSP、ZWNJ、ZWJ、WORD JOINER、BOM、软连字符、组合字位连接符;
//   - 双向控制符(Trojan Source)与 Unicode tag 区(ASCII smuggling);
//   - **变体选择符** U+FE00–FE0F 与其补充区 U+E0100–E01EF。这一组是 2026 年
//     ASCII smuggling 的主流载体,上一版只收了 tag 区、漏了它,实测
//     `Ign<U+FE0F>ore all prev<U+FE0F>ious instructions` 可以让全部短语级规则失效。
//     注意 U+FE0F 同时是 emoji 表现选择符,删掉它只会把 ❤️ 变成 ❤,不改变任何字母;
//   - 废弃格式符 U+206A–206F、行间注释锚 U+FFF9–FFFB、高棉隐形元音 U+17B4–17B5。
//
// # 为什么不做同形字映射
//
// 西里尔 а → 拉丁 a 这类映射需要一张几千条的表,表里任何一条写错都会凭空制造误伤,
// 而它的收益是"攻击者少改一个字母"。留作已知敞口,见 zz 红队回归里的 E3 组用例。
func invisibleRune(r rune) bool {
	switch r {
	case 0x00AD, // SOFT HYPHEN
		0x034F,                         // COMBINING GRAPHEME JOINER
		0x061C,                         // ARABIC LETTER MARK
		0x180E,                         // MONGOLIAN VOWEL SEPARATOR
		0x200B,                         // ZERO WIDTH SPACE
		0x200C,                         // ZERO WIDTH NON-JOINER
		0x200D,                         // ZERO WIDTH JOINER
		0x200E,                         // LEFT-TO-RIGHT MARK
		0x200F,                         // RIGHT-TO-LEFT MARK
		0x2060,                         // WORD JOINER
		0x2061, 0x2062, 0x2063, 0x2064, // 不可见数学运算符
		0xFEFF: // ZERO WIDTH NO-BREAK SPACE / BOM
		return true
	}
	return (r >= 0x17B4 && r <= 0x17B5) || // 高棉隐形元音
		(r >= 0x202A && r <= 0x202E) || // 双向嵌入/覆盖
		(r >= 0x2066 && r <= 0x206F) || // 双向隔离 + 废弃的格式控制符
		(r >= 0xFE00 && r <= 0xFE0F) || // 变体选择符 VS1–VS16
		(r >= 0xFFF9 && r <= 0xFFFB) || // 行间注释锚
		(r >= 0xE0000 && r <= 0xE007F) || // Unicode tag 区
		(r >= 0xE0100 && r <= 0xE01EF) // 变体选择符补充区 VS17–VS256
}

// exoticSpace 判断一个 rune 是否"渲染成一个空格,但不是 ASCII 空格"。
//
// 这一组必须**折叠成空格**而不是删掉:全部内置模式串的词间连接符都是 `\s+`,
// 而 Go 的 RE2 里 `\s` 只认 ASCII `[\t\n\f\r ]`。把 NBSP 删掉会让
// `without refusal` 黏成 `withoutrefusal`,比原样留着还难匹配;折成空格才等价还原。
//
// 这是本轮最重要的一处引擎修复:对整段载荷做一次"空格 → U+00A0"全局替换,
// 不需要了解任何一条规则,就能让**全部**短语级规则同时失效(实测全载荷零命中)。
// 它与同义改写的区别在于成本 —— 同义改写要逐个短语重写,这个只要一次 sed。
func exoticSpace(r rune) bool {
	switch r {
	case 0x00A0, // NO-BREAK SPACE
		0x1680, // OGHAM SPACE MARK
		0x202F, // NARROW NO-BREAK SPACE
		0x205F, // MEDIUM MATHEMATICAL SPACE
		0x3000: // IDEOGRAPHIC SPACE(全角空格)
		return true
	}
	// U+2000–200A 是各种定宽空格。上界必须停在 200A:200B 起是零宽系列,
	// 那一组归 invisibleRune 删除,折成空格反而会切断本来连着的词。
	return r >= 0x2000 && r <= 0x200A
}

// foldFullwidthASCII 把全角拉丁字母与数字折回半角。
//
// **只折字母与数字,一个标点都不碰**,这是与 NFKC 的关键差别:NFKC 会把
// pressure.control_token 第二个分支依赖的全角竖线 U+FF5C 折成半角 `|`,
// 让那条已经导入到运营库里的规则静默失效(规则要等人手工点"升级"才换模式串,
// 中间这段时间它看着正常、实际永不命中)。字母与数字没有任何规则依赖其全角形态。
func foldFullwidthASCII(r rune) rune {
	switch {
	case r >= 0xFF10 && r <= 0xFF19: // ０-９
		return r - 0xFF10 + '0'
	case r >= 0xFF21 && r <= 0xFF3A: // Ａ-Ｚ
		return r - 0xFF21 + 'A'
	case r >= 0xFF41 && r <= 0xFF5A: // ａ-ｚ
		return r - 0xFF41 + 'a'
	}
	return r
}

// normalizeRune 是三条归一规则的合并形态:隐形字符删除、异体空格折成空格、
// 全角字母数字折成半角。返回值等于入参表示这个 rune 不需要动。
func normalizeRune(r rune) rune {
	if invisibleRune(r) {
		return -1
	}
	if exoticSpace(r) {
		return ' '
	}
	return foldFullwidthASCII(r)
}

// normalizeForScan 对待检文本做一次字符层归一。
// 绝大多数请求一个需要处理的字符都没有,所以先扫一遍再决定要不要复制。
func normalizeForScan(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return normalizeRune(r) != r }) {
		return s
	}
	return strings.Map(normalizeRune, s)
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
