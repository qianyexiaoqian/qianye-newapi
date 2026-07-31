package availability

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
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
	TotalModels   int         `json:"total_models"`
	Page          int         `json:"page"`
	PageSize      int         `json:"page_size"`
	Truncated     bool        `json:"truncated"`
	Cells         []cell      `json:"cells"`
	Overall       cell        `json:"overall"`
}

// getMatrix 是主看板:分组 × 模型的可用率矩阵。
func getMatrix(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagAvailability) {
		return
	}
	r := resolveRange(httpq.Int64(c, "start_ts", 0), httpq.Int64(c, "end_ts", 0), httpq.Int(c, "hours", 24))
	d := definitionOf(config.Get().Availability)

	// 权限:服务端强制裁剪,绝不依赖前端传什么就查什么。
	// groups 参数只用于在可见集合内进一步收窄,永远先取交集。
	visible := visibleGroups(c)
	groups := intersectGroups(splitCSV(c.Query("groups"), maxGroupFilter), visible)
	if len(groups) == 0 {
		// 交集为空返回空矩阵而不是 403:用"有没有权限"的差异去探测分组是否存在,
		// 本身就是一条侧信道。
		respond(c, matrixResponse{
			StartTs: r.StartTs, EndTs: r.EndTs,
			BucketSeconds: r.granularitySeconds(), Granularity: granularityLabel(r),
			Definition: d, Groups: []groupInfo{}, Models: []string{}, Cells: []cell{},
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
	truncated := false
	for _, name := range pageModels {
		for _, g := range groups {
			if len(out) >= limit {
				truncated = true
				break
			}
			key := cellKey{group: g, model: name}
			_, offered := groupOfferedModels(g)[name]
			out = append(out, buildCell(key, cells[key], offered, d))
		}
		if truncated {
			break
		}
	}
	sortCells(out, sortMode)

	respond(c, matrixResponse{
		StartTs: r.StartTs, EndTs: r.EndTs,
		BucketSeconds: r.granularitySeconds(),
		Granularity:   granularityLabel(r),
		Definition:    d,
		Groups:        groupInfos(groups, visible),
		Models:        pageModels,
		TotalModels:   total,
		Page:          page,
		PageSize:      pageSize,
		Truncated:     truncated,
		Cells:         out,
		Overall:       overallCell(out, cells, groups, pageModels, d),
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

// ─────────────────────────────── 管理端 ───────────────────────────────

// adminStats 是运维视角的总览:口径、采样与落库健康度、存储水位、全分组可用率。
//
// 与用户端的根本差别是不做任何分组裁剪 —— 管理员必须看得到隐藏分组,
// 以及 UsingGroup 解析失败留下的 unknown 行(它是 hook 或渠道配置出问题的信号)。
func adminStats(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagAvailability) {
		return
	}
	cfg := config.Get().Availability
	d := definitionOf(cfg)
	r := resolveRange(0, 0, httpq.Int(c, "hours", 24))

	data := gin.H{
		"definition": d,
		"config": gin.H{
			"bucket_seconds":         bucketSeconds(),
			"flush_interval_seconds": int64(flushInterval().Seconds()),
			"retention_days":         cfg.RetentionDays,
			"max_series_per_query":   maxSeries(),
			"hot_series_limit":       hotSeriesLimit,
			// attempt 级采样需要在 controller/relay.go 增加挂载点,本期未实现。
			// 明确回传 false,避免运维以为已经开启。
			"sample_attempt_level_requested": cfg.SampleAttemptLevel,
			"sample_attempt_level_supported": false,
		},
		"sampler": gin.H{
			"observed":             statObserved.Load(),
			"dropped_no_model":     statNoModel.Load(),
			"dropped_series_limit": statSeriesFull.Load(),
			"truncated_names":      statTruncated.Load(),
			"hot_series":           hotSeries.Load(),
		},
		"flush": gin.H{
			"runs":     statFlushRuns.Load(),
			"rows":     statFlushRows.Load(),
			"failures": statFlushFail.Load(),
			"last_at":  statFlushAt.Load(),
		},
		"rollup": gin.H{
			"rows":    statRollupRows.Load(),
			"last_at": statRollupAt.Load(),
		},
		"cleanup": gin.H{
			"rows":    statCleanupRow.Load(),
			"last_at": statCleanupAt.Load(),
		},
		"hot_queue": guard.QueueStats(),
	}

	if storage, err := storageStats(); err == nil {
		data["storage"] = storage
	}

	groups, err := allGroupsInRange(r)
	if err != nil {
		unavailable(c)
		return
	}
	cells, err := queryCells(r, groups, nil)
	if err != nil {
		unavailable(c)
		return
	}

	perGroup := map[string]*Bucket{}
	for key, b := range cells {
		if perGroup[key.group] == nil {
			perGroup[key.group] = &Bucket{}
		}
		perGroup[key.group].addFrom(b)
	}
	summaries := make([]cell, 0, len(perGroup))
	for g, b := range perGroup {
		summaries = append(summaries, buildCell(cellKey{group: g, model: "*"}, b, true, d))
	}
	sortCells(summaries, "availability_asc")

	worst := make([]cell, 0, len(cells))
	for key, b := range cells {
		worst = append(worst, buildCell(key, b, true, d))
	}
	sortCells(worst, "availability_asc")
	if len(worst) > 20 {
		worst = worst[:20]
	}

	data["start_ts"] = r.StartTs
	data["end_ts"] = r.EndTs
	data["groups"] = summaries
	data["worst_cells"] = worst
	respond(c, data)
}

// allGroupsInRange 取时间范围内出现过的全部分组(管理端专用,不做白名单裁剪)。
func allGroupsInRange(r timeRange) ([]string, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var groups []string
	err := gdb.Table(r.tableName()).
		Where("bucket_ts >= ? AND bucket_ts <= ?", r.StartTs, r.EndTs).
		Distinct("group_name").
		Pluck("group_name", &groups).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	// 内存热桶里可能有刚出现、DB 还没有的分组。
	seen := toSet(groups)
	hotBuckets.Range(func(key, _ any) bool {
		k := key.(dimKey)
		if k.bucketTs < r.StartTs || k.bucketTs > r.EndTs {
			return true
		}
		if _, ok := seen[k.group]; !ok {
			groups = append(groups, k.group)
			if seen == nil {
				seen = map[string]struct{}{}
			}
			seen[k.group] = struct{}{}
		}
		return true
	})
	sort.Strings(groups)
	return groups, nil
}

func storageStats() (gin.H, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var bucketRows, hourRows, oldest int64
	if err := gdb.Table(bucketTable).Count(&bucketRows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if err := gdb.Table(hourTable).Count(&hourRows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	if err := gdb.Table(bucketTable).Select("COALESCE(MIN(bucket_ts), 0)").Scan(&oldest).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return gin.H{"bucket_rows": bucketRows, "hour_rows": hourRows, "oldest_bucket_ts": oldest}, nil
}

// ─────────────────────────────── 公共辅助 ───────────────────────────────

// visibleGroups 取调用者的可用分组白名单。
//
// 与需求 5 的分组泄漏修复共用 service.GetUserUsableGroups —— 模型广场、
// 模型详情性能、本页三处共用同一份可见性口径,日后要做真·隐藏分组体系时
// 只需要改那一个函数。这里刻意自己调用而不复用 groupvis 模块的内部函数:
// 两个模块的开关互相独立,可用率页不能因为分组可见性开关被关掉就泄漏数据。
func visibleGroups(c *gin.Context) map[string]string {
	return service.GetUserUsableGroups(c.GetString("group"))
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
