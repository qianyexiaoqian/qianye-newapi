package availability

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

const (
	maxRangeHours = 720 // 30 天
	// hourTableSpanHours 之后改读小时表:5 分钟桶查 30 天要扫百万行,
	// 而这个端点是登录用户可达的聚合查询,必须有成本上限。
	hourTableSpanHours   = 48
	defaultMaxSeries     = 2000
	maxSeriesHardLimit   = 20000
	channelModelCacheTTL = 60 * time.Second
)

// cellKey 是矩阵的一个格子:分组 × 模型。
type cellKey struct {
	group string
	model string
}

type timeRange struct {
	StartTs int64
	EndTs   int64
}

func (r timeRange) spanHours() int64 { return (r.EndTs - r.StartTs + 3599) / 3600 }

// useHourTable 决定读哪张表。跨度大就读小时表,同时把返回粒度一并降下来。
func (r timeRange) useHourTable() bool { return r.spanHours() > hourTableSpanHours }

func (r timeRange) granularitySeconds() int64 {
	if r.useHourTable() {
		return 3600
	}
	return bucketSeconds()
}

func (r timeRange) tableName() string {
	if r.useHourTable() {
		return hourTable
	}
	return bucketTable
}

// resolveRange 解析并夹紧时间范围。
// 上限 720 小时不是产品偏好:更长的跨度即使走小时表也会扫出百万级行。
func resolveRange(startTs, endTs int64, hours int) timeRange {
	now := common.GetTimestamp()
	if startTs > 0 && endTs > startTs {
		if endTs > now {
			endTs = now
		}
		if endTs-startTs > maxRangeHours*3600 {
			startTs = endTs - maxRangeHours*3600
		}
		return timeRange{StartTs: startTs, EndTs: endTs}
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > maxRangeHours {
		hours = maxRangeHours
	}
	return timeRange{StartTs: now - int64(hours)*3600, EndTs: now}
}

func maxSeries() int {
	n := config.Get().Availability.MaxSeriesPerQuery
	if n <= 0 {
		n = defaultMaxSeries
	}
	if n > maxSeriesHardLimit {
		n = maxSeriesHardLimit
	}
	return n
}

// ─────────────────────────────── 聚合查询 ───────────────────────────────

// queryCells 按 (分组, 模型) 聚合出矩阵所需的原始计数。
//
// groups 必须已经过权限裁剪:本函数不做任何可见性判断,它只是照单查询。
// 空 groups 直接返回空结果 —— 绝不能退化成"不过滤",那等于把全部分组下发。
func queryCells(r timeRange, groups, models []string) (map[cellKey]*Bucket, error) {
	out := map[cellKey]*Bucket{}
	if len(groups) == 0 {
		return out, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}

	var rows []Bucket
	q := gdb.Table(r.tableName()).
		Select(selectSums("model_name, group_name")).
		Where("bucket_ts >= ? AND bucket_ts <= ?", r.StartTs, r.EndTs).
		Where("group_name IN ?", groups)
	q = whereModels(q, models)
	if err := q.Group("model_name, group_name").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	for i := range rows {
		row := rows[i]
		out[cellKey{group: row.GroupName, model: row.ModelName}] = &row
	}

	if !r.useHourTable() {
		mergeHotCells(out, r, groups, models)
	}
	return out, nil
}

// querySeriesPoints 按 (分组, 时间桶) 聚合单个模型的趋势。
func querySeriesPoints(r timeRange, modelName string, groups []string) (map[string]map[int64]*Bucket, error) {
	out := map[string]map[int64]*Bucket{}
	if len(groups) == 0 || modelName == "" {
		return out, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}

	var rows []Bucket
	err := gdb.Table(r.tableName()).
		Select(selectSums("group_name, bucket_ts")).
		Where("bucket_ts >= ? AND bucket_ts <= ?", r.StartTs, r.EndTs).
		Where("group_name IN ?", groups).
		Where("model_name = ?", modelName).
		Group("group_name, bucket_ts").
		Order("bucket_ts ASC").
		Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	for i := range rows {
		row := rows[i]
		putSeriesPoint(out, row.GroupName, row.BucketTs, &row)
	}

	if !r.useHourTable() {
		size := bucketSeconds()
		allowed := toSet(groups)
		hotBuckets.Range(func(key, value any) bool {
			k := key.(dimKey)
			if k.model != modelName || k.bucketTs < r.StartTs || k.bucketTs > r.EndTs {
				return true
			}
			if _, ok := allowed[k.group]; !ok {
				return true
			}
			snap := value.(*counters).snapshot()
			if snap.ReqTotal == 0 {
				return true
			}
			putSeriesPoint(out, k.group, alignBucket(k.bucketTs, size), &snap)
			return true
		})
	}
	return out, nil
}

func putSeriesPoint(dst map[string]map[int64]*Bucket, group string, ts int64, row *Bucket) {
	if dst[group] == nil {
		dst[group] = map[int64]*Bucket{}
	}
	if cur, ok := dst[group][ts]; ok {
		cur.addFrom(row)
		return
	}
	cp := *row
	cp.BucketTs = ts
	dst[group][ts] = &cp
}

// mergeHotCells 叠加本节点尚未 flush 的内存热桶。
//
// 多节点部署下这只补上本节点的最新一个 flush 周期,节点之间的读数会有
// ≤flush_interval 的轻微差异。相比"刚打完请求却显示无数据"的困惑,这个代价更小。
func mergeHotCells(dst map[cellKey]*Bucket, r timeRange, groups, models []string) {
	allowedGroups := toSet(groups)
	allowedModels := toSet(models)
	hotBuckets.Range(func(key, value any) bool {
		k := key.(dimKey)
		if k.bucketTs < r.StartTs || k.bucketTs > r.EndTs {
			return true
		}
		if _, ok := allowedGroups[k.group]; !ok {
			return true
		}
		if len(allowedModels) > 0 {
			if _, ok := allowedModels[k.model]; !ok {
				return true
			}
		}
		snap := value.(*counters).snapshot()
		if snap.ReqTotal == 0 {
			return true
		}
		ck := cellKey{group: k.group, model: k.model}
		if cur, ok := dst[ck]; ok {
			cur.addFrom(&snap)
			return true
		}
		snap.GroupName = k.group
		snap.ModelName = k.model
		dst[ck] = &snap
		return true
	})
}

// selectSums 拼出 "维度列, SUM(c) AS c, ..." 的投影。
// 列清单由 counterMap 派生,新增计数列不需要改这里。
func selectSums(dims string) string {
	cols := counterColumns()
	parts := make([]string, 0, len(cols)+1)
	parts = append(parts, dims)
	for _, col := range cols {
		parts = append(parts, "SUM("+col+") AS "+col)
	}
	return strings.Join(parts, ", ")
}

func whereModels(q *gorm.DB, models []string) *gorm.DB {
	if len(models) == 0 {
		return q
	}
	return q.Where("model_name IN ?", models)
}

func toSet(items []string) map[string]struct{} {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]struct{}, len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

// ─────────────────────────────── 结果组装 ───────────────────────────────

type cell struct {
	Group         string   `json:"group"`
	Model         string   `json:"model"`
	Availability  *float64 `json:"availability"` // null 表示无数据 / 样本不足,前端绝不能渲染成 0%
	State         string   `json:"state"`
	ReqTotal      int64    `json:"req_total"`
	Counted       int64    `json:"counted"`
	Success       int64    `json:"success"`
	ExcludedTotal int64    `json:"excluded_total"`
	TopReason     string   `json:"top_reason,omitempty"`
	AvgLatencyMs  int64    `json:"avg_latency_ms"`
	AvgTtftMs     int64    `json:"avg_ttft_ms"`
	AvgTps        float64  `json:"avg_tps"`
	HasChannel    bool     `json:"has_channel"`
}

func buildCell(key cellKey, b *Bucket, hasChannel bool, d Definition) cell {
	if b == nil {
		b = &Bucket{}
	}
	state, av := stateOf(b, hasChannel, d)
	return cell{
		Group:         key.group,
		Model:         key.model,
		Availability:  av,
		State:         state,
		ReqTotal:      b.ReqTotal,
		Counted:       counted(b, d),
		Success:       b.SuccessCount,
		ExcludedTotal: b.excludedTotal(),
		TopReason:     topReason(b),
		AvgLatencyMs:  avgOf(b.LatencySumMs, b.LatencyCount),
		AvgTtftMs:     avgOf(b.TtftSumMs, b.TtftCount),
		AvgTps:        tpsOf(b),
		HasChannel:    hasChannel,
	}
}

func avgOf(sum, count int64) int64 {
	if count <= 0 {
		return 0
	}
	return sum / count
}

func tpsOf(b *Bucket) float64 {
	if b.OutputTokens <= 0 || b.GenerationMs <= 0 {
		return 0
	}
	return math.Round(float64(b.OutputTokens)/(float64(b.GenerationMs)/1000)*100) / 100
}

// sortCells 默认按可用率升序 —— 打开页面先看最烂的那几个,这是监控页的本分。
// 无数据的格子永远排在最后:它们不是"最好"也不是"最差",不该抢占视线。
func sortCells(cells []cell, mode string) {
	sort.SliceStable(cells, func(i, j int) bool {
		a, b := cells[i], cells[j]
		switch mode {
		case "requests_desc":
			if a.ReqTotal != b.ReqTotal {
				return a.ReqTotal > b.ReqTotal
			}
		case "model_asc":
			if a.Model != b.Model {
				return a.Model < b.Model
			}
		default: // availability_asc
			if (a.Availability == nil) != (b.Availability == nil) {
				return a.Availability != nil
			}
			if a.Availability != nil && *a.Availability != *b.Availability {
				return *a.Availability < *b.Availability
			}
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Group < b.Group
	})
}

// ────────────────────── 该分组是否提供这个模型(has_channel) ──────────────────────

// model.GetGroupEnabledModels 每次都会打一条主库 DISTINCT 查询。
// 矩阵一次要问 N 个分组,而分组下的模型清单几分钟才变一次,必须缓存,
// 否则这个只读页面会给主库带来与刷新频率成正比的压力。
var (
	channelModelMu    sync.Mutex
	channelModelCache = map[string]channelModelEntry{}
)

type channelModelEntry struct {
	models   map[string]struct{}
	expireAt time.Time
}

func groupOfferedModels(group string) map[string]struct{} {
	channelModelMu.Lock()
	entry, ok := channelModelCache[group]
	channelModelMu.Unlock()
	if ok && time.Now().Before(entry.expireAt) {
		return entry.models
	}

	set := toSet(model.GetGroupEnabledModels(group))
	if set == nil {
		set = map[string]struct{}{}
	}
	channelModelMu.Lock()
	channelModelCache[group] = channelModelEntry{models: set, expireAt: time.Now().Add(channelModelCacheTTL)}
	channelModelMu.Unlock()
	return set
}
