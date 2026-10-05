package availability

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const (
	maxModelFilter = 200
	// maxGroupFilter 与 maxModelFilter 同理:groups 参数是逗号分隔的自由文本,
	// 不设上限的话一条 URL 就能构造出几十万项的 IN 子句。
	maxGroupFilter = 200
	// maxCSVItems 既是 splitCSV 的兜底值也是它的封顶值:limit<=0 或大于它,
	// 一律回落到它。刻意不让 limit<=0 表示"不限" —— 本仓库反复出现的失败形状
	// 就是"能力做对了、调用点没接上",而 splitCSV(x, 0) 正是那个形状。
	// 忘了传上限应当退化成一个安全的默认值,而不是退化成无限。
	maxCSVItems     = 200
	maxPageSize     = 200
	defaultPageSize = 50
)

// listPaging 是看板的分页口径。注意它与扩展其余模块**不同**:参数名是 ?page=
// 而不是 ?p=,默认 50 条、上限 200 条 —— 前端 availability 页面按这套发请求,
// 收敛到 httpq 时逐字段核对过,一个都没改。
//
// 页码上界(httpq.MaxPage)不是可选项:没有它,?page=184467440737095518 能被
// 解析成功,(page-1)*pageSize 溢出成负数,httpq.Slice 里的 names[start:end]
// 直接 panic —— 一个只读看板端点被一个查询参数打崩。
var listPaging = httpq.Spec{PageKey: "page", DefaultSize: defaultPageSize, MaxSize: maxPageSize}

// ─────────────────────────────── 用户端 ───────────────────────────────

type groupInfo struct {
	Group string `json:"group"`
	Desc  string `json:"desc"`
}

type matrixResponse struct {
	StartTs       int64       `json:"start_ts"`
	EndTs         int64       `json:"end_ts"`
	BucketSeconds int64       `json:"bucket_seconds"`
	Granularity   string      `json:"granularity"`
	Definition    Definition  `json:"definition"`
	Groups        []groupInfo `json:"groups"`
	Models        []string    `json:"models"`
	// CoveredModels 是 Models 里**真正被查询覆盖**的那些。
	//
	// 没有它,截断就是不可分辨的:被截掉的格子在响应里长得和"该分组不提供这个
	// 模型"一模一样(两者都是 cells 里没有这一项),前端于是把一个根本没查过的
	// 格子渲染成了"未提供" —— 一个肯定断言。Truncated 只说明"有东西被截了",
	// 说不出被截的是哪些,前端无法据此把结论降级成"未知"。
	CoveredModels []string `json:"covered_models"`
	TotalModels   int      `json:"total_models"`
	Page          int      `json:"page"`
	PageSize      int      `json:"page_size"`
	Truncated     bool     `json:"truncated"`
	Cells         []cell   `json:"cells"`
	Overall       cell     `json:"overall"`
}

// getMatrix 是主看板:分组 × 模型的可用率矩阵。
func getMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagAvailability) {
		return
	}
	serveMatrix(c, visibleGroups(c))
}

// getPlazaMatrix 是模型广场详情页的入口,登录与否都能访问。
//
// 项目方要求:模型广场一律用扩展的分组可用率,不再用上游 perf_metrics;未登录访客
// 看到的是「注册默认用户分组」的口径。可见分组因此按 plazaVisibleGroups 取 ——
// 已登录走本人,未登录走默认分组,与 /api/pricing 的匿名口径同一条链路,
// 保证「访客看到的 == 刚注册的用户看到的」。
//
// 鉴权由路由上的 HeaderNavModuleAuth("pricing") 决定(模型广场关掉 → 403,
// 要求登录 → UserAuth),与模型广场本身同进同退。匿名可访问,所以只许按模型查:
// models 必填,拉全量矩阵仍然只有登录后的可用率页一个入口。
func getPlazaMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagAvailability) {
		return
	}
	// 上界比 maxModelFilter 小得多:详情页一次只查一个模型,而这条路没有按用户的
	// 搜索限流,不封顶就等于给匿名访客开了一个分批拉全站矩阵的口子。
	switch n := len(splitCSV(c.Query("models"), maxModelFilter)); {
	case n == 0:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "models 必填"})
		return
	case n > maxPlazaModels:
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"message": "models 最多 " + strconv.Itoa(maxPlazaModels) + " 个"})
		return
	}
	serveMatrix(c, plazaVisibleGroups(c))
}

// maxPlazaModels 是模型广场入口一次能查的模型数上界。
const maxPlazaModels = 20

// plazaAllGroups 是前端「全部分组」筛选的取值(web 侧 FILTER_ALL)。
const plazaAllGroups = "all"

// plazaHour 是卡片状态条的一格。
type plazaHour struct {
	Ts int64 `json:"ts"`
	// SuccessRate 是这一小时计入口径的成功率,不套 min_samples:状态条是一条
	// 走势线,低流量时段画成灰色会让绝大多数模型整条发灰。没有计入样本时为 nil(灰格)。
	SuccessRate *float64 `json:"success_rate"`
}

// plazaCard 是一张模型卡片的状态:可用率与状态判定和「模型可用率」页同一口径。
type plazaCard struct {
	Model        string   `json:"model"`
	Availability *float64 `json:"availability"`
	State        string   `json:"state"`
	perf
	Hours []plazaHour `json:"hours"`
}

// getPlazaCards 给模型广场卡片列表用:一页模型在「选中分组」上的可用率与 24 格小时线。
//
// 截图里的问题:切换分组时价格变了,卡片上的状态始终是同一个 99.3% —— 它读的是
// 上游 perf_metrics 的全站汇总,不分分组。这里按 group 参数取:选中具体分组就只看
// 那一个(必须在可见集合内),「全部分组」则把可见分组合在一起。可见集合的身份
// 规则与 getPlazaMatrix 相同(未登录 = 注册默认分组)。
func getPlazaCards(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagAvailability) {
		return
	}
	models := splitCSV(c.Query("models"), maxModelFilter)
	switch {
	case len(models) == 0:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "models 必填"})
		return
	case len(models) > maxPlazaModels:
		c.JSON(http.StatusBadRequest, gin.H{"success": false,
			"message": "models 最多 " + strconv.Itoa(maxPlazaModels) + " 个"})
		return
	}

	visible := plazaVisibleGroups(c)
	var requested []string
	if g := strings.TrimSpace(c.Query("group")); g != "" && g != plazaAllGroups {
		requested = []string{g}
	}
	// 选了一个看不见的分组时交集为空,整页按"无数据"回答,而不是悄悄换成全部分组。
	groups := intersectGroups(requested, visible)

	r := resolveRange(0, 0, 24)
	d := definitionOf(config.Get().Availability)
	cells, err := queryCells(r, groups, models)
	if err != nil {
		unavailable(c)
		return
	}
	series, err := querySeriesByModel(r, models, groups)
	if err != nil {
		unavailable(c)
		return
	}

	out := make([]plazaCard, 0, len(models))
	for _, name := range models {
		total := &Bucket{}
		for _, g := range groups {
			if b := cells[cellKey{group: g, model: name}]; b != nil {
				total.addFrom(b)
			}
		}
		state, av := stateOf(total, true, d)
		card := plazaCard{Model: name, Availability: av, State: state, perf: perfOf(total)}
		hours := series[name]
		timestamps := make([]int64, 0, len(hours))
		for ts := range hours {
			timestamps = append(timestamps, ts)
		}
		slices.Sort(timestamps)
		card.Hours = make([]plazaHour, 0, len(timestamps))
		for _, ts := range timestamps {
			b := hours[ts]
			card.Hours = append(card.Hours, plazaHour{Ts: ts, SuccessRate: availabilityOf(b.SuccessCount, counted(b, d))})
		}
		out = append(out, card)
	}
	respond(c, gin.H{"cards": out, "definition": d})
}

// serveMatrix 是两个入口共用的矩阵查询;visible 是调用方按身份算好的可见分组。
func serveMatrix(c *gin.Context, visible map[string]string) {
	r := resolveRange(httpq.Int64(c, "start_ts", 0), httpq.Int64(c, "end_ts", 0), httpq.Int(c, "hours", 24))
	d := definitionOf(config.Get().Availability)

	// 权限:服务端强制裁剪,绝不依赖前端传什么就查什么。
	// groups 参数只用于在可见集合内进一步收窄,永远先取交集。
	groups := intersectGroups(splitCSV(c.Query("groups"), maxGroupFilter), visible)
	if len(groups) == 0 {
		// 交集为空返回空矩阵而不是 403:用"有没有权限"的差异去探测分组是否存在,
		// 本身就是一条侧信道。
		respond(c, matrixResponse{
			StartTs: r.StartTs, EndTs: r.EndTs,
			BucketSeconds: r.granularitySeconds(), Granularity: granularityLabel(r),
			Definition: d, Groups: []groupInfo{}, Models: []string{},
			CoveredModels: []string{}, Cells: []cell{},
		})
		return
	}

	cells, err := queryCells(r, groups, splitCSV(c.Query("models"), maxModelFilter))
	if err != nil {
		unavailable(c)
		return
	}

	names := modelNames(cells, strings.TrimSpace(c.Query("q")))
	sortMode := c.Query("sort")
	sortModels(names, cells, groups, d, sortMode)

	page, pageSize := httpq.Paginate(c, listPaging)
	total := len(names)
	pageModels := httpq.Slice(names, page, pageSize)

	limit := maxSeries()
	// 预分配必须先被 limit 夹住。下面的循环到 limit 就会 break,但容量是在循环
	// 之前一次性算出来的:平台分组数一多,这里就先按 页内模型数 × 分组数 分配了
	// 一大片内存,而真正用到的永远不超过 limit。
	out := make([]cell, 0, min(limit, len(pageModels)*len(groups)))
	covered := make([]string, 0, len(pageModels))
	truncated := false
	// used 只数「有内容」的格子:该分组提供这个模型,或时段内有样本。
	//
	// 「不提供」的格子仍然整行下发(前端靠它区分「不提供」与「没查过」),但它们
	// 没有任何数据,不该占 max_series_per_query 的配额。原先按 行数 × 分组数 计:
	// 管理员可见 16 个分组,200 / 16 ≈ 12 个模型就截断,一页 30 个模型里 18 个
	// (包括全部 Gemini)整行变成「未知」,而那 16 格里绝大多数都是「不提供」。
	used := 0
	for _, name := range pageModels {
		row := make([]cell, 0, len(groups))
		weight := 0
		for _, g := range groups {
			key := cellKey{group: g, model: name}
			_, offered := groupOfferedModels(g)[name]
			b := cells[key]
			if offered || b != nil {
				weight++
			}
			row = append(row, buildCell(key, b, offered, d))
		}
		// ★ 整行要么全给、要么不给。逐格截断是最坏的一种:半行留下的空缺在响应里
		// 与"该分组不提供这个模型"完全不可分辨,前端于是把根本没查过的格子画成"未提供"。
		if used+weight > limit {
			truncated = true
			break
		}
		used += weight
		out = append(out, row...)
		covered = append(covered, name)
	}
	sortCells(out, sortMode)

	respond(c, matrixResponse{
		StartTs: r.StartTs, EndTs: r.EndTs,
		BucketSeconds: r.granularitySeconds(),
		Granularity:   granularityLabel(r),
		Definition:    d,
		Groups:        groupInfos(groups, visible),
		Models:        pageModels,
		CoveredModels: covered,
		TotalModels:   total,
		Page:          page,
		PageSize:      pageSize,
		Truncated:     truncated,
		Cells:         out,
		// KPI 只汇总真正查过的模型:拿被截掉的模型去凑总数,页面上会出现
		// "整体可用率的分母比矩阵里所有格子加起来还大"这种对不上的账。
		Overall: overallCell(out, cells, groups, covered, d),
	})
}

type seriesPoint struct {
	Ts           int64    `json:"ts"`
	Availability *float64 `json:"availability"`
	State        string   `json:"state"`
	ReqTotal     int64    `json:"req_total"`
	Counted      int64    `json:"counted"`
	Success      int64    `json:"success"`
	// 与矩阵共用 perfOf 的口径:趋势图能画哪几条线,取决于这里有哪几个字段。
	perf
}

type seriesLine struct {
	Group  string        `json:"group"`
	Points []seriesPoint `json:"points"`
}

type seriesResponse struct {
	ModelName     string       `json:"model_name"`
	StartTs       int64        `json:"start_ts"`
	EndTs         int64        `json:"end_ts"`
	BucketSeconds int64        `json:"bucket_seconds"`
	Granularity   string       `json:"granularity"`
	Definition    Definition   `json:"definition"`
	Truncated     bool         `json:"truncated"`
	Series        []seriesLine `json:"series"`
}

// getSeries 返回单个模型在各可见分组下的可用率趋势。
func getSeries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagAvailability) {
		return
	}
	modelName := strings.TrimSpace(c.Query("model"))
	if modelName == "" {
		fail(c, http.StatusBadRequest, "qy_avail_model_required", "缺少 model 参数")
		return
	}
	r := resolveRange(httpq.Int64(c, "start_ts", 0), httpq.Int64(c, "end_ts", 0), httpq.Int(c, "hours", 24))
	d := definitionOf(config.Get().Availability)

	visible := visibleGroups(c)
	requested := splitCSV(c.Query("group"), maxGroupFilter)
	requested = append(requested, splitCSV(c.Query("groups"), maxGroupFilter)...)
	groups := intersectGroups(requested, visible)

	points, err := querySeriesPoints(r, modelName, groups)
	if err != nil {
		unavailable(c)
		return
	}

	limit := maxSeries()
	lines := make([]seriesLine, 0, len(points))
	truncated := false
	for _, g := range groups {
		byTs := points[g]
		if len(byTs) == 0 {
			continue
		}
		if len(lines) >= limit {
			truncated = true
			break
		}
		timestamps := make([]int64, 0, len(byTs))
		for ts := range byTs {
			timestamps = append(timestamps, ts)
		}
		sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })

		line := seriesLine{Group: g, Points: make([]seriesPoint, 0, len(timestamps))}
		for _, ts := range timestamps {
			b := byTs[ts]
			// 时序上每个点都有样本(没有样本就不会有这一行),因此按 has_channel=true
			// 计算状态;真正的 not_offered 由矩阵负责表达。
			state, av := stateOf(b, true, d)
			line.Points = append(line.Points, seriesPoint{
				Ts:           ts,
				Availability: av,
				State:        state,
				ReqTotal:     b.ReqTotal,
				Counted:      counted(b, d),
				Success:      b.SuccessCount,
				perf:         perfOf(b),
			})
		}
		lines = append(lines, line)
	}

	respond(c, seriesResponse{
		ModelName:     modelName,
		StartTs:       r.StartTs,
		EndTs:         r.EndTs,
		BucketSeconds: r.granularitySeconds(),
		Granularity:   granularityLabel(r),
		Definition:    d,
		Truncated:     truncated,
		Series:        lines,
	})
}

// ─────────────────────────────── 已移除:管理端 ───────────────────────────────
//
// 原 adminStats(/admin/availability/stats)与它的两个专属查询
// allGroupsInRange / storageStats 已按项目方要求整体删除。
// 它们只服务管理端页面,与采样/聚合/落库/清理零耦合,因此删除不影响数据链路。
//
// 副作用(已知,不修):aggregate.go 里的采样/落库计数器失去了唯一读侧,
// 变成只写不读;dropped_series_limit、truncated_names、rollup/cleanup 进度与
// 存储水位不再有任何 UI 出口。

// ─────────────────────────────── 公共辅助 ───────────────────────────────

// visibleGroups 取调用者的可用分组白名单。
//
// 与需求 5 的分组泄漏修复共用同一份口径 —— 模型广场、模型详情性能、本页三处
// 共用 service.QyUsableGroupsForUser,日后要做真·隐藏分组体系时只需要改那一个
// 函数。这里刻意自己调用而不复用 groupvis 模块的内部函数:两个模块的开关互相
// 独立,可用率页不能因为分组可见性开关被关掉就泄漏数据。
//
// **必须带 userId**:套餐解锁是 per-user 的,不带身份的 GetUserUsableGroups
// 拿不到用户买来的模型分组。少这一个参数的表现是"令牌页选得到、可用率页一条
// 数据都看不到" —— 同一个人在同一个站点的两个页面得到互相矛盾的答案,
// 而那正是 QyUsableGroupsForUser 这个封装存在的全部理由。
func visibleGroups(c *gin.Context) map[string]string {
	return service.QyUsableGroupsForUser(c.GetInt("id"), c.GetString("group"))
}

// plazaVisibleGroups 是模型广场口径的可见分组:已登录取本人,未登录取注册默认
// 用户分组。model.QyNewUserGroup 绝不返回空串,所以匿名不会落进上游「空分组 =
// 全站白名单」那条语义(见 controller/qy_plaza_viewer.go)。
func plazaVisibleGroups(c *gin.Context) map[string]string {
	if c.GetInt("id") > 0 {
		return visibleGroups(c)
	}
	return service.QyUsableGroupsForUser(0, model.QyNewUserGroup())
}

// intersectGroups 把请求的分组收窄到可见集合。requested 为空表示"全部可见分组"。
//
// 这是两个只读端点唯一的分组取值入口,因此去重与上限都钉在这里,而不是钉在
// 各个调用点上:调用点会被复制、会被新增,漏掉一处就等于没做。
// 上限用 requested 的口径 —— 可见集合是服务端算出来的,不是用户输入,
// 截断它只会让有很多分组的用户静默看不到数据。
func intersectGroups(requested []string, visible map[string]string) []string {
	if len(requested) == 0 {
		out := make([]string, 0, len(visible))
		for g := range visible {
			out = append(out, g)
		}
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, min(len(requested), maxGroupFilter))
	seen := make(map[string]struct{}, cap(out))
	for _, g := range requested {
		if len(out) >= maxGroupFilter {
			break
		}
		if _, ok := visible[g]; !ok {
			continue
		}
		// 去重:?groups=vip,vip,vip 会让同一个分组在 IN 子句里出现 N 次,
		// 更要命的是矩阵里同一个格子被渲染 N 遍,而 maxSeries 的截断
		// 会被这些重复项吃掉,真实分组反而挤不进结果。
		if _, dup := seen[g]; dup {
			continue
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func groupInfos(groups []string, visible map[string]string) []groupInfo {
	out := make([]groupInfo, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupInfo{Group: g, Desc: visible[g]})
	}
	return out
}

// modelNames 取矩阵要展示的模型清单。q 是模型名模糊匹配,在聚合后的内存结果上做,
// 不下推成 SQL LIKE —— 前缀通配的 LIKE 用不上索引,而这里数据已经在手上了。
func modelNames(cells map[cellKey]*Bucket, q string) []string {
	seen := map[string]struct{}{}
	names := make([]string, 0, len(cells))
	q = strings.ToLower(q)
	for key := range cells {
		if _, ok := seen[key.model]; ok {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(key.model), q) {
			continue
		}
		seen[key.model] = struct{}{}
		names = append(names, key.model)
	}
	sort.Strings(names)
	return names
}

// sortModels 决定模型的展示顺序。默认按该模型在各分组中最差的可用率升序,
// 让最需要关注的模型出现在第一页。
//
// 排序必须在这一层做而不能交给前端:分页是按模型切的,前端只拿得到当前页,
// 切到「最慢优先」时真正最慢的那个模型可能压根不在这一页里。
//
// 「该模型的表现」= 各分组中最差的那个分组:一个模型只要在任意一个分组上
// 挂了/变慢,它就值得被排到前面,按平均会把单分组故障稀释掉。
func sortModels(names []string, cells map[cellKey]*Bucket, groups []string, d Definition, mode string) {
	worstAv := make(map[string]*float64, len(names))
	worstLatency := make(map[string]*int64, len(names))
	worstTtft := make(map[string]*int64, len(names))
	worstTps := make(map[string]*float64, len(names))
	totals := make(map[string]int64, len(names))
	for _, name := range names {
		for _, g := range groups {
			b, ok := cells[cellKey{group: g, model: name}]
			if !ok {
				continue
			}
			totals[name] += b.ReqTotal

			p := perfOf(b)
			if p.AvgLatencyMs != nil {
				if cur, ok := worstLatency[name]; !ok || *p.AvgLatencyMs > *cur {
					worstLatency[name] = p.AvgLatencyMs
				}
			}
			if p.AvgTtftMs != nil {
				if cur, ok := worstTtft[name]; !ok || *p.AvgTtftMs > *cur {
					worstTtft[name] = p.AvgTtftMs
				}
			}
			if p.AvgTps != nil {
				if cur, ok := worstTps[name]; !ok || *p.AvgTps < *cur {
					worstTps[name] = p.AvgTps
				}
			}

			_, av := stateOf(b, true, d)
			if av == nil {
				continue
			}
			if cur, ok := worstAv[name]; !ok || *av < *cur {
				v := *av
				worstAv[name] = &v
			}
		}
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, b := names[i], names[j]
		switch mode {
		case "model_asc":
			return a < b
		case "requests_desc":
			if totals[a] != totals[b] {
				return totals[a] > totals[b]
			}
		case "latency_desc":
			av, bv := worstLatency[a], worstLatency[b]
			if (av == nil) != (bv == nil) {
				return av != nil // 无延迟数据的排最后
			}
			if av != nil && *av != *bv {
				return *av > *bv
			}
		case "ttft_desc":
			av, bv := worstTtft[a], worstTtft[b]
			if (av == nil) != (bv == nil) {
				return av != nil
			}
			if av != nil && *av != *bv {
				return *av > *bv
			}
		case "tps_asc":
			av, bv := worstTps[a], worstTps[b]
			if (av == nil) != (bv == nil) {
				return av != nil
			}
			if av != nil && *av != *bv {
				return *av < *bv
			}
		default:
			av, bv := worstAv[a], worstAv[b]
			if (av == nil) != (bv == nil) {
				return av != nil // 无可用率的排最后
			}
			if av != nil && *av != *bv {
				return *av < *bv
			}
		}
		return a < b
	})
}

// overallCell 汇总当前页的整体可用率,给 KPI 卡用。
func overallCell(cells []cell, raw map[cellKey]*Bucket, groups, models []string, d Definition) cell {
	total := &Bucket{}
	inPage := toSet(models)
	for key, b := range raw {
		if _, ok := inPage[key.model]; !ok {
			continue
		}
		total.addFrom(b)
	}
	out := buildCell(cellKey{group: "*", model: "*"}, total, true, d)
	// 最差分组由已构造好的格子推导,避免再算一遍。
	var worstGroup string
	var worstAv float64 = 101
	for _, cl := range cells {
		if cl.Availability != nil && *cl.Availability < worstAv {
			worstAv, worstGroup = *cl.Availability, cl.Group
		}
	}
	out.Group = worstGroup
	return out
}

func granularityLabel(r timeRange) string {
	if r.useHourTable() {
		return "1h"
	}
	return strconv.FormatInt(bucketSeconds(), 10) + "s"
}

// splitCSV 解析逗号分隔的过滤参数。
//
// limit<=0 表示"用兜底上限",而不是"不限":调用点忘了传上限是常态,
// 而"不限"意味着一条 URL 就能让服务端解析出几十万个字符串并塞进 IN 子句。
// 让默认行为是安全的,那么少传一个参数就只是收窄了过滤范围,不是开了个洞。
//
// 逐段扫描而不是 strings.Split 整串:Split 会先把全部分段都物化出来,
// 之后再截断已经晚了 —— 上限要在分配之前生效,而不是在分配之后。
func splitCSV(s string, limit int) []string {
	if limit <= 0 || limit > maxCSVItems {
		limit = maxCSVItems
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := make([]string, 0, min(strings.Count(s, ",")+1, limit))
	for s != "" && len(out) < limit {
		part := s
		if i := strings.IndexByte(s, ','); i >= 0 {
			part, s = s[:i], s[i+1:]
		} else {
			s = ""
		}
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func fail(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": msg})
}

// unavailable 用 503 而不是把错误details 抛给前端:这是只读看板,
// 让用户稍后重试即可,把 SQL 错误文本下发只会泄漏表结构。
func unavailable(c *gin.Context) {
	c.Header("Retry-After", "30")
	fail(c, http.StatusServiceUnavailable, "qy_avail_unavailable", "可用率数据服务暂不可用,请稍后重试")
}
