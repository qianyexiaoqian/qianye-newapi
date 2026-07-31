package grouppricing

import (
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
)

// reconcile.go —— 影子差额的对账口径。
//
// 运营要回答的问题只有一个:「切换成分组价之后,这个月会多收还是少收多少?」
//
// 计算方式:
//
//	差额 = 该维度这段时间的实际扣费 × (新值 / 旧值 - 1)
//
// 这不是估算,是精确值。因为实际扣费对被覆盖的那个值是**严格线性**的:
//
//	按 token: quota = 加权 token 数 × model_ratio × 分组倍率
//	按次:     quota = model_price × QuotaPerUnit × 分组倍率 × 其它倍率
//	阶梯:     quota = 表达式结果 × 乘数 × 分组倍率
//
// 三条路径里被覆盖的那个因子都只以一次乘法出现,所以换成新值等价于整体乘一个
// 常数系数。这也正是本模块刻意不覆盖 completion_ratio 的原因 —— 覆盖它会改变
// 加权 token 数本身的权重,上面这个等式立刻不成立。
//
// 实际扣费从主库**日志库**取(model.LOG_DB 的 logs 表),按 (model_name, group)
// 维度求和。影子桶只提供维度与系数,不存金额:计价时还不知道这次请求最终会
// 消耗多少 token,预扣额度不是实际扣费。

const (
	// maxReconcileDays 限制单次对账的时间跨度。logs 是全站最大的表之一,
	// 无上界的范围查询会把日志库拖垮,而这个接口是管理员随手点的。
	maxReconcileDays = 31
	// maxReconcilePairs 限制回主库取金额的 (分组, 模型) 组合数。
	// 每个组合一次聚合查询,不设上界时一次点击可以打出几千条查询。
	maxReconcilePairs = 200
)

// ShadowSegment 是一段"规则值保持不变"的区间在某个维度上的汇总。
type ShadowSegment struct {
	GroupName string `json:"group_name"`
	ModelName string `json:"model_name"`
	Mode      string `json:"mode"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`

	// Exact 为 false 表示这一段无法按比例折算(旧值为 0,或计费口径发生切换)。
	// 这类行的 DeltaQuota 恒为 0 并单独计数,绝不混进合计里假装精确。
	Exact  bool   `json:"exact"`
	Reason string `json:"inexact_reason,omitempty"`

	Requests        int64  `json:"requests"`
	SampleRequestId string `json:"sample_request_id,omitempty"`

	// ActualQuota 是该 (分组, 模型) 在整个查询区间内的真实扣费合计。
	//
	// 注意它的维度比本行粗:主库日志没有"当时生效的是哪一版规则"这个信息,
	// 所以同一个 (分组, 模型) 在区间内换过规则值时,只能按各段的请求数占比
	// 把金额分摊回去。RequestShare 就是那个占比,ShareIsExact 标记它是否为 1。
	ActualQuota     int64  `json:"actual_quota"`
	RequestShare    string `json:"request_share"`
	ShareIsExact    bool   `json:"share_is_exact"`
	AttributedQuota int64  `json:"attributed_quota"`

	// Factor = 新值 / 旧值。DeltaQuota = 分摊到本段的实际扣费 × (Factor - 1)。
	// 负数表示切换后少收(用户变便宜),正数表示多收。
	Factor     string `json:"factor"`
	DeltaQuota int64  `json:"delta_quota"`

	// factorDec 是 Factor 的十进制原值。全程用它做乘除,只在输出时转成字符串:
	// 这个系数会被乘上整月的扣费合计,float64 的舍入误差在那个量级上是可见的钱。
	factorDec decimal.Decimal
}

// ShadowSummary 是对账结果。
type ShadowSummary struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`

	Segments []ShadowSegment `json:"segments"`

	TotalRequests int64 `json:"total_requests"`
	// TotalActualQuota 是可折算部分当前的真实扣费合计。
	TotalActualQuota int64 `json:"total_actual_quota"`
	// TotalDeltaQuota 是切换后的净变化:正 = 多收,负 = 少收。
	TotalDeltaQuota int64 `json:"total_delta_quota"`
	// InexactRequests 是无法折算的请求数。它必须单独露出来 ——
	// 把它默默算成 0 差额,会让"合计"看起来完整而实际上漏了一块。
	InexactRequests int64 `json:"inexact_requests"`

	// Truncated 表示维度组合数超过上限、只取了请求数最多的前若干组。
	Truncated bool `json:"truncated"`
	// QuotaSourceError 非空表示主库日志聚合失败,此时金额列全为 0,
	// 只有请求数与系数可信。宁可返回一半数据并说明,也不能让运营
	// 拿着一份看起来完整实则缺金额的报表去做上线决策。
	QuotaSourceError string `json:"quota_source_error,omitempty"`
}

// buildShadowSummary 汇总 [start, end) 区间内的影子差额。
func buildShadowSummary(start, end int64) (*ShadowSummary, error) {
	if end <= start {
		return nil, fmt.Errorf("结束时间必须大于开始时间")
	}
	if end-start > maxReconcileDays*86400 {
		return nil, fmt.Errorf("单次对账区间不得超过 %d 天", maxReconcileDays)
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}

	var rows []ShadowBucket
	if err := gdb.Where("bucket_ts >= ? AND bucket_ts < ?", alignBucket(start), end).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	segs, pairRequests := foldShadowBuckets(rows)
	out := &ShadowSummary{Start: start, End: end, Segments: segs}
	if len(segs) == 0 {
		out.Segments = []ShadowSegment{}
		return out, nil
	}
	if len(pairRequests) > maxReconcilePairs {
		out.Truncated = true
		segs, pairRequests = trimToTopPairs(segs, pairRequests, maxReconcilePairs)
		out.Segments = segs
	}

	actual, err := actualQuotaByPair(pairRequests, start, end)
	if err != nil {
		out.QuotaSourceError = err.Error()
	}

	for i := range out.Segments {
		s := &out.Segments[i]
		out.TotalRequests += s.Requests
		if !s.Exact {
			out.InexactRequests += s.Requests
			continue
		}
		pk := pairKey{s.GroupName, s.ModelName}
		s.ActualQuota = actual[pk]
		total := pairRequests[pk]

		// 全程 decimal:这个系数要乘上整月的扣费合计,float64 的舍入误差
		// 在那个量级上就是肉眼可见的钱。IntPart 是截断,截断方向对"多收多少"
		// 是保守的(宁可报小,不能报大后让人以为涨价幅度可接受)。
		share := decimal.NewFromInt(1)
		s.ShareIsExact = true
		if total > 0 && s.Requests != total {
			share = decimal.NewFromInt(s.Requests).Div(decimal.NewFromInt(total))
			s.ShareIsExact = false
		}
		s.RequestShare = share.StringFixed(6)

		attributed := decimal.NewFromInt(s.ActualQuota).Mul(share)
		s.AttributedQuota = attributed.IntPart()
		s.DeltaQuota = attributed.Mul(s.factorDec.Sub(decimal.NewFromInt(1))).IntPart()
		s.Factor = s.factorDec.StringFixed(6)

		out.TotalActualQuota += s.AttributedQuota
		out.TotalDeltaQuota += s.DeltaQuota
	}
	return out, nil
}

type pairKey struct{ group, model string }

// foldShadowBuckets 把小时桶折叠成"维度 × 规则区间"的段,并算出折算系数。
func foldShadowBuckets(rows []ShadowBucket) ([]ShadowSegment, map[pairKey]int64) {
	type segKey struct {
		pairKey
		mode, oldV, newV string
		exact            bool
	}
	agg := map[segKey]*ShadowSegment{}
	pairRequests := map[pairKey]int64{}

	for i := range rows {
		r := rows[i]
		pk := pairKey{r.GroupName, r.ModelName}
		k := segKey{pk, r.Mode, r.OldValue, r.NewValue, r.Exact}
		s := agg[k]
		if s == nil {
			s = &ShadowSegment{
				GroupName: r.GroupName, ModelName: r.ModelName, Mode: r.Mode,
				OldValue: r.OldValue, NewValue: r.NewValue, Exact: r.Exact,
			}
			s.factorDec, s.Exact, s.Reason = segmentFactor(r)
			agg[k] = s
		}
		s.Requests += r.Requests
		if r.SampleRequestId != "" {
			s.SampleRequestId = r.SampleRequestId
		}
		if s.Exact {
			pairRequests[pk] += r.Requests
		}
	}

	out := make([]ShadowSegment, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		if out[i].GroupName != out[j].GroupName {
			return out[i].GroupName < out[j].GroupName
		}
		return out[i].ModelName < out[j].ModelName
	})
	return out, pairRequests
}

// segmentFactor 算出一段区间的折算系数,并判定它到底精不精确。
//
// 这个判定必须保守:任何拿不准的情形都要标成不精确并说明理由,
// 因为运营会拿这个数字去决定要不要真的开始按新价扣钱。
func segmentFactor(r ShadowBucket) (decimal.Decimal, bool, string) {
	if !r.Exact {
		return decimal.Zero, false, "计价口径发生切换(该模型原本不按此口径计费),差额无法按比例折算"
	}
	oldV, err := decimal.NewFromString(r.OldValue)
	if err != nil {
		return decimal.Zero, false, "旧值不是合法数值: " + r.OldValue
	}
	newV, err := decimal.NewFromString(r.NewValue)
	if err != nil {
		return decimal.Zero, false, "新值不是合法数值: " + r.NewValue
	}
	if oldV.IsZero() {
		return decimal.Zero, false, "旧值为 0,实际扣费也为 0,差额无法按比例折算"
	}
	return newV.DivRound(oldV, 12), true, ""
}

// trimToTopPairs 只保留请求数最多的前 n 个 (分组, 模型) 组合。
//
// 截断必须按维度而不是按段:同一维度的几段必须整体保留或整体丢弃,
// 否则分摊占比的分母就是错的,算出来的差额会比真实值大。
func trimToTopPairs(segs []ShadowSegment, pairRequests map[pairKey]int64, n int) ([]ShadowSegment, map[pairKey]int64) {
	pairs := make([]pairKey, 0, len(pairRequests))
	for k := range pairRequests {
		pairs = append(pairs, k)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairRequests[pairs[i]] != pairRequests[pairs[j]] {
			return pairRequests[pairs[i]] > pairRequests[pairs[j]]
		}
		if pairs[i].group != pairs[j].group {
			return pairs[i].group < pairs[j].group
		}
		return pairs[i].model < pairs[j].model
	})
	if n > len(pairs) {
		n = len(pairs)
	}
	keep := map[pairKey]int64{}
	for _, p := range pairs[:n] {
		keep[p] = pairRequests[p]
	}
	out := segs[:0]
	for _, s := range segs {
		if _, ok := keep[pairKey{s.GroupName, s.ModelName}]; ok {
			out = append(out, s)
		}
	}
	return out, keep
}

// actualQuotaByPair 回主库日志库取每个 (分组, 模型) 的真实扣费合计。
//
// 为什么一个组合一次查询,而不是一条 GROUP BY:logs 的 group 是 SQL 保留字,
// 手写 GROUP BY 就必须自己按方言加引号(主库可能是 MySQL / PostgreSQL / SQLite,
// 三种引号各不相同),而结构体条件由 GORM 的方言层负责加引号,天然跨库正确。
// 组合数已经被 maxReconcilePairs 钳住,查询次数有界。
func actualQuotaByPair(pairs map[pairKey]int64, start, end int64) (map[pairKey]int64, error) {
	out := make(map[pairKey]int64, len(pairs))
	if model.LOG_DB == nil {
		return out, fmt.Errorf("主库日志库未初始化")
	}
	for pk := range pairs {
		if pk.group == "" || pk.model == "" {
			continue
		}
		var sum int64
		err := model.LOG_DB.Model(&model.Log{}).
			Where(&model.Log{ModelName: pk.model, Group: pk.group}).
			Where("type = ? AND created_at >= ? AND created_at < ?", model.LogTypeConsume, start, end).
			Select("COALESCE(SUM(quota), 0)").Scan(&sum).Error
		if err != nil {
			return out, err
		}
		out[pk] = sum
	}
	return out, nil
}
