package grouppricing

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/shopspring/decimal"
)

// maxValueScale 是覆盖值允许的最大小数位,与 Rule.Value 的列定义 decimal(24,10) 一致。
//
// 超出一律拒绝而不是四舍五入:静默把 0.00000000005 变成 0.0000000001 是一次
// 没有人签字的调价。列宽与校验必须同口径,否则写进去的值与读出来的值不同,
// 而"写进去的"才是管理员看过并确认的那个数。
const maxValueScale = 10

// compiledRule 是规则的运行期形态。
//
// 全部解析、校验、float64 折算在快照构建时一次性完成,热路径只做 map 查找与
// 字符串前缀比较,不做任何解析、不碰 decimal 运算。
type compiledRule struct {
	GroupName string
	ModelName string
	Mode      string

	Value decimal.Decimal
	// ValueFloat 是喂给上游 hook 的 float64。上游的价格与倍率本身就是 float64,
	// 这里做且只做一次转换 —— 转换点越少,精度损失的来源就越可数。
	ValueFloat float64
	// ValueText 是 Value 的规范十进制字面量,用作影子桶的唯一键。
	ValueText string

	// prefix 非空表示这是前缀规则("gpt-4*" → prefix="gpt-4");
	// 为空表示精确匹配。modelWildcard("*")是 prefix=="" 且 wildcard==true。
	prefix   string
	wildcard bool
}

// groupRules 是单个分组下的全部规则。
type groupRules struct {
	exact map[string]*compiledRule
	// prefixes 按前缀长度降序排列,第一个命中的就是最长前缀。
	// 排序在构建期完成,热路径只顺序扫一遍(单个分组下的前缀规则是个位数量级)。
	prefixes []*compiledRule
	wildcard *compiledRule
}

// snapshot 是规则的只读内存视图。整体替换(atomic.Pointer),读端零锁;
// 正在使用旧快照的请求持有引用,不会读到半成品。
type snapshot struct {
	version int64
	byGroup map[string]*groupRules
	// rules 保留编译后的规则原序,供管理端与测试断言,不参与热路径。
	rules []*compiledRule
}

var (
	current atomic.Pointer[snapshot]
	// loadedAt 刻意放在 snapshot 之外:snapshot 一旦发布就是只读的,
	// 在里面就地改时间戳会与并发读构成数据竞争(按 Go 内存模型是未定义行为)。
	loadedAt      atomic.Int64
	nextRefreshAt atomic.Int64
	refreshFails  atomic.Int64
	// staleDrops 统计"快照过期被丢弃、回落成无覆盖"的次数。
	// 这个数字长期非零意味着扩展库有问题,而表现出来只是"分组价好像时灵时不灵"。
	staleDrops atomic.Int64
)

func cacheSeconds() int64 {
	n := int64(config.Get().GroupPricing.RuleCacheSeconds)
	if n <= 0 {
		n = 60
	}
	return n
}

func maxStaleSeconds() int64 {
	n := int64(config.Get().GroupPricing.MaxStaleSeconds)
	if n <= 0 {
		n = 300
	}
	if c := cacheSeconds(); n < c {
		// 配置校验已经挡过这种组合,这里再兜一次:陈旧上限小于刷新周期会让
		// 快照每轮刷新前都先过期,分组价在生效与不生效之间来回抖动。
		n = c
	}
	return n
}

// activeSnapshot 返回可用于计价的快照,过期或从未加载成功时返回 nil。
//
// 返回 nil 的语义是**无覆盖,走全局价** —— 这是本模块最重要的一条降级约定。
// 少一个折扣是运营问题,按一份来历不明的旧规则扣钱是资损问题,两者不对称,
// 所以任何"拿不准"的情形都必须倒向前者。
//
// 具体三种情形都会返回 nil:
//  1. 冷启动时预热失败(current 从未被赋值)
//  2. 刷新持续失败,距上一次成功加载超过 max_stale_seconds
//  3. 扩展库不可用导致 HotAsync 一直跳过刷新 —— 最终也落到第 2 种
func activeSnapshot() *snapshot {
	s := current.Load()
	if s == nil {
		return nil
	}
	age := common.GetTimestamp() - loadedAt.Load()
	if age > maxStaleSeconds() {
		if n := staleDrops.Add(1); n == 1 || n%1000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/grouppricing: 规则快照已陈旧 %d 秒(上限 %d),累计 %d 次回落为「无覆盖」—— "+
					"这段时间的请求一律按全局价计费,请检查扩展库健康状态",
				age, maxStaleSeconds(), n))
		}
		return nil
	}
	return s
}

// lookupOverride 查出 (分组, 模型) 上生效的覆盖规则。
//
// 匹配优先级:精确 > 最长前缀 > "*"。与扫描顺序无关 ——
// 一笔请求按什么价扣钱必须是看一眼就能确定的。
func lookupOverride(group, model string) (*compiledRule, bool) {
	s := activeSnapshot()
	if s == nil {
		return nil, false
	}
	return s.lookup(group, model)
}

func (s *snapshot) lookup(group, model string) (*compiledRule, bool) {
	g := s.byGroup[normalizeGroup(group)]
	if g == nil {
		return nil, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, false
	}
	if r, ok := g.exact[model]; ok {
		return r, true
	}
	for _, r := range g.prefixes {
		if strings.HasPrefix(model, r.prefix) {
			return r, true
		}
	}
	if g.wildcard != nil {
		return g.wildcard, true
	}
	return nil, false
}

// maybeRefresh 是快照的自维护入口:热路径每次调用只做一次 atomic 比较,
// 到期才通过 HotAsync 触发一次异步重载。
//
// 为什么不用后台协程:规则快照是"每个节点各自持有"的进程内缓存,
// lease.Run 只会让一个节点刷新,其余节点的价格永远陈旧。CAS 推进下次刷新时间
// 既避免了并发重复加载,也让无流量的节点零开销。
func maybeRefresh() {
	now := common.GetTimestamp()
	next := nextRefreshAt.Load()
	if now < next {
		return
	}
	if !nextRefreshAt.CompareAndSwap(next, now+cacheSeconds()) {
		return // 别的请求已经抢到刷新权
	}
	guard.HotAsync("grouppricing.rule_refresh", func(ctx context.Context) error {
		return reloadCtx(ctx, false)
	})
}

// reload 用冷路径预算重载快照,供管理端写入后刷新与启动预热使用。
//
// 这两个调用点手上都没有现成的 ctx,而"没有 ctx"不等于"不需要上界":
// 管理端保存规则时若扩展库正病态,不接预算就会一直等到 DSN readTimeout(30 秒),
// 整个 HTTP 请求跟着挂住。热路径刷新走 reloadCtx,直接沿用 guard worker 的预算。
//
// 与 violation/rules.go 的同名函数刻意保持逐字相同:这两份拷贝已经因为
// "各自漂移"被审计出过一次,形状不同才是下一次漂移的开始。
func reload(force bool) error {
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	return reloadCtx(ctx, force)
}

// reloadCtx 重新构建快照。force=false 时先比对版本号,版本未变直接返回,
// 避免每个刷新周期都全表拉规则。
//
// 失败时**不动 current**:保留上一次成功的快照,由 activeSnapshot 的陈旧上限
// 决定它还能用多久。这不是"读失败当成有覆盖"—— 那指的是把错误当成命中;
// 这里用的是一份确实成功读到过的规则,而且有硬性时效。
//
// ctx 必须一路挂到 GORM 语句上:guard.HotAsync 承诺的 hot_async_timeout_ms(3 秒)
// 只对 WithContext(ctx) 的语句生效。漏接的后果不只是这条语句慢 —— 本模块与
// violation 的刷新周期同为 60 秒、由同一批 relay 流量触发,很容易同时占满仅有的
// 2 个 hot worker 长达 readTimeout(30 秒),这期间 commission.consume 事件会把
// 4096 槽队列填满并溢出丢弃,而丢弃是"用户该拿的钱没拿到"的唯一路径。
// 次生问题:不带 ctx 时 db.WithOpProbe 认不到这些语句,hotRunWithBudget 就只会在
// 失败时 MarkFailure、成功时永不 MarkSuccess,熔断的健康票被单向截断。
func reloadCtx(ctx context.Context, force bool) error {
	gdb := db.Get()
	if gdb == nil {
		refreshFails.Add(1)
		return db.ErrNotReady
	}
	gdb = gdb.WithContext(ctx)

	var ver RuleVersion
	if err := gdb.Where("id = ?", 1).Take(&ver).Error; err != nil {
		// 版本行还不存在(首次部署)不是错误,按 version=0 继续加载。
		ver = RuleVersion{Id: 1, Version: 0}
	}
	if cur := current.Load(); cur != nil && !force && cur.version == ver.Version {
		loadedAt.Store(common.GetTimestamp())
		return nil
	}

	limit := config.Get().GroupPricing.MaxRules
	if limit <= 0 {
		limit = 2000
	}
	var rows []Rule
	if err := gdb.Where("enabled = ?", true).
		Order("id asc").Limit(limit + 1).Find(&rows).Error; err != nil {
		refreshFails.Add(1)
		return err
	}
	if len(rows) > limit {
		// 截断而不是全量加载:上限存在的意义就是"每个节点每个周期要拉的行数有界"。
		// 但截断会让一部分规则静默失效,所以必须喊出来。
		rows = rows[:limit]
		common.SysError(fmt.Sprintf(
			"qianye/grouppricing: 启用中的规则超过 group_pricing.max_rules(%d),"+
				"按 id 升序只加载前 %d 条,其余规则不会生效", limit, limit))
	}

	s := buildSnapshot(rows, ver.Version)
	current.Store(s)
	loadedAt.Store(common.GetTimestamp())
	return nil
}

// buildSnapshot 把数据库行编译成只读视图。
//
// 单条规则编译失败绝不能拖垮整份快照:那等于一次手滑就让全站分组价失效。
// 但它也绝不能"尽量用一下"—— 一条值非法的规则被跳过等于无覆盖(安全),
// 被勉强采用则等于按一个不确定的价格扣钱(资损)。
func buildSnapshot(rows []Rule, version int64) *snapshot {
	s := &snapshot{version: version, byGroup: map[string]*groupRules{}}
	for i := range rows {
		if !rows[i].Enabled {
			// reload 的 WHERE 已经过滤过一遍。这里再挡一次是刻意的:
			// 停用的规则一旦被编译进快照,"关掉这条规则"就变成了一个
			// 只在数据库里生效、在内存里不生效的操作 —— 而运营看到的是前者。
			continue
		}
		cr, err := compile(rows[i])
		if err != nil {
			common.SysError(fmt.Sprintf(
				"qianye/grouppricing: 规则 %d(%s/%s)非法,已跳过(该分组该模型按全局价计费): %v",
				rows[i].Id, rows[i].GroupName, rows[i].ModelName, err))
			continue
		}
		g := s.byGroup[cr.GroupName]
		if g == nil {
			g = &groupRules{exact: map[string]*compiledRule{}}
			s.byGroup[cr.GroupName] = g
		}
		switch {
		case cr.wildcard:
			g.wildcard = cr
		case cr.prefix != "":
			g.prefixes = append(g.prefixes, cr)
		default:
			g.exact[cr.ModelName] = cr
		}
		s.rules = append(s.rules, cr)
	}
	for _, g := range s.byGroup {
		sort.SliceStable(g.prefixes, func(i, j int) bool {
			return len(g.prefixes[i].prefix) > len(g.prefixes[j].prefix)
		})
	}
	return s
}

// compile 把一行规则翻译成运行期形态,顺带完成全部合法性校验。
//
// 这是第二道校验(第一道在管理端写入)。两道都必须有:手改数据库、
// 从旧版本继承的历史行、迁移脚本回填 —— 都会绕过接口直达这里,
// 而这里的输出会被直接乘进账单。
func compile(r Rule) (*compiledRule, error) {
	group := normalizeGroup(r.GroupName)
	if group == "" {
		return nil, fmt.Errorf("分组名不能为空")
	}
	if len(group) > maxGroupNameLen {
		return nil, fmt.Errorf("分组名超过 %d 字节", maxGroupNameLen)
	}
	model := strings.TrimSpace(r.ModelName)
	if model == "" {
		return nil, fmt.Errorf("模型名不能为空")
	}
	if len(model) > maxModelNameLen {
		return nil, fmt.Errorf("模型名超过 %d 字节", maxModelNameLen)
	}
	if err := ValidateValue(r.Mode, r.Value); err != nil {
		return nil, err
	}

	cr := &compiledRule{
		GroupName:  group,
		ModelName:  model,
		Mode:       r.Mode,
		Value:      r.Value,
		ValueFloat: r.Value.InexactFloat64(),
		ValueText:  normalizeDecimal(r.Value),
	}
	switch {
	case model == modelWildcard:
		cr.wildcard = true
	case strings.HasSuffix(model, "*"):
		cr.prefix = strings.TrimSuffix(model, "*")
		if cr.prefix == "" {
			cr.wildcard = true
		}
	}
	// 折算成 float64 之后必须仍是有限值。decimal 本身不会产生 NaN/Inf,
	// 但一个极端量级的值转成 float64 会溢出成 ±Inf,而 ±Inf 进了额度换算只会
	// 被饱和成上限 —— 那是一张没有意义的账单。ValidateValue 的上界已经挡住了
	// 这种取值,这里是喂给 hook 之前的最后一道断言。
	if math.IsInf(cr.ValueFloat, 0) || math.IsNaN(cr.ValueFloat) {
		return nil, fmt.Errorf("覆盖值 %s 折算成浮点后不是有限值", r.Value.String())
	}
	return cr, nil
}

// ValidateValue 校验覆盖值的口径与取值范围。写入侧与快照编译侧共用同一份实现 ——
// 两处各写一套判定,严的那一处迟早会形同虚设。
func ValidateValue(mode string, v decimal.Decimal) error {
	var upper int64
	switch mode {
	case ModePrice:
		upper = maxPriceUSD
	case ModeRatio:
		upper = maxModelRatio
	case ModeTiered:
		upper = maxTieredMultiplier
	default:
		return fmt.Errorf("口径 %q 非法(可选 price|ratio|tiered)", mode)
	}
	if v.IsNegative() {
		return fmt.Errorf("覆盖值不得为负数,收到 %s —— 负价会把扣费变成给用户充值", v.String())
	}
	if v.GreaterThan(decimal.NewFromInt(upper)) {
		return fmt.Errorf("%s 口径的覆盖值不得超过 %d,收到 %s", mode, upper, v.String())
	}
	if mode == ModeTiered && v.IsZero() {
		// price / ratio 的 0 是有意义的("这个分组免费用这个模型"),
		// 而阶梯乘数的 0 会让整条表达式的结果归零,与"免费"是同一个效果,
		// 但它看起来像"没填",太容易是手滑。要免费请用 price=0。
		return fmt.Errorf("tiered 口径的乘数必须大于 0(要让该分组免费请用 price 口径填 0)")
	}
	if v.Exponent() < -maxValueScale {
		return fmt.Errorf("覆盖值最多 %d 位小数,收到 %s", maxValueScale, v.String())
	}
	return nil
}

// normalizeGroup 归一分组名。空分组名归一成 default,与主库 users.group 的列默认值一致。
//
// 口径与 commission/transfer 共用 groupname 包(去空白 + 折叠大小写 + 空串→default)。
// 这是全仓第三张以分组名为键的**money 表**:不折叠大小写时,管理端给 VIP 配的分组价
// 在 MySQL 上会写进 vip 那一行(列排序规则大小写不敏感),而热路径又按 VIP 精确查、
// 查不到 —— 结果是"配了折扣、界面显示已保存、实际按原价扣钱"。
func normalizeGroup(g string) string { return groupname.Effective(g) }

// normalizeDecimal 给出十进制值的规范字面量,用作影子桶的唯一键。
//
// 必须规范化:同一个值可能以 "1.0" 与 "1.00" 两种字面量出现(前端输入、
// 数据库列宽补零),不归一会让同一段区间被拆成两行,汇总时看起来像是
// 规则被改过两次。
func normalizeDecimal(d decimal.Decimal) string {
	return d.Round(maxValueScale).String()
}
