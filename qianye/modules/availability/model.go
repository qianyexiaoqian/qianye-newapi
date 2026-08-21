package availability

import "sort"

// Bucket 是「时间桶 × 分组 × 模型」的可用率预聚合行。
//
// 为什么是预聚合而不是原始采样表:采样跑在 relay 全量流量上,单节点上千 QPS 时
// 原始表每天上亿行,写放大会直接打爆库;而 (group, model) 的基数通常 < 500,
// 5 分钟桶下每天最多 500×288 ≈ 14.4 万行,与 QPS 完全解耦。
//
// 为什么把每一类结果都单独存一列,而不是只存「分子/分母」两列:
// 口径(4xx 算不算、429 算不算)是可配的,如果落库时就把分母算死,
// 改一次开关历史数据就永远解释不清。存原始分类计数、查询时按当前口径求和,
// 历史与当下才是同一把尺子。多出来的十几个 bigint 列在这个行数量级下不值一提。
//
// 跨库无外键:group_name / model_name 是主库 abilities 的软引用,禁止加 constraint tag。
type Bucket struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// —— 维度三元组,组成唯一索引 ——
	// utf8mb4 下这条唯一索引 = 8 + 64×4 + 128×4 = 776 字节,
	// 在 InnoDB DYNAMIC(3072 上限)下安全;模型名沿用上游 perf_metrics 的 128,
	// 放宽到 191/255 会顶到老 COMPACT 格式的 767 字节上限。
	//
	// ★ 索引名一律写成 `composite:` 而不是硬编码一个名字,因为 HourBucket 匿名
	// 内嵌本结构体、连索引标签一起继承:硬编码的名字会被两张表同时申领。
	// MySQL 的索引名是**每表**独立的,两张表各建一份、相安无事;而 PostgreSQL
	// 与 SQLite 的索引名是**schema/库级**唯一的 —— 第二张表的三条索引会被
	// GORM 的 `CREATE [UNIQUE] INDEX IF NOT EXISTS` 静默吞掉,一条都不建出来,
	// 而且每次启动都再徒劳地发一遍(空转 DDL)。后果不是"少了几条索引":
	// qy_avail_bucket_hour 因此没有 (bucket_ts,group_name,model_name) 唯一索引,
	// rollupHour 的 `ON CONFLICT (这三列) DO UPDATE` 在 PG 上恒报 42P10,
	// 小时表永远为空,而 query.go 的 useHourTable() 规定跨度 > 48 小时一律读小时表。
	//
	// `composite:dim` 让 GORM 按 `idx_<表名>_dim` 派生名字(schema/index.go:118),
	// 于是两张表各得一个互不相同的名字,三种方言下形状一致。
	// 改回硬编码名字会被 TestExtensionIndexNamesAreSchemaUnique 与
	// availability 的 rollup 单测(在 sqlite 上真的建两张表)当场打红。
	BucketTs  int64  `json:"bucket_ts" gorm:"column:bucket_ts;not null;uniqueIndex:,composite:dim,priority:1;index:,composite:ts_group,priority:1"`
	GroupName string `json:"group" gorm:"column:group_name;type:varchar(64);not null;uniqueIndex:,composite:dim,priority:2;index:,composite:ts_group,priority:2"`
	ModelName string `json:"model_name" gorm:"column:model_name;type:varchar(128);not null;uniqueIndex:,composite:dim,priority:3"`

	// —— 全部样本,含被口径排除的 ——
	// 没有它就无法回答「我明明打了 1000 次,为什么只统计了 300 次」。
	ReqTotal     int64 `json:"req_total" gorm:"column:req_total;not null;default:0"`
	SuccessCount int64 `json:"success_count" gorm:"column:success_count;not null;default:0"`

	// —— 失败归因。可用率低了之后,唯一的抓手 ——
	FailSoftStream  int64 `json:"fail_soft_stream" gorm:"column:fail_soft_stream;not null;default:0"`
	FailNoChannel   int64 `json:"fail_no_channel" gorm:"column:fail_no_channel;not null;default:0"`
	FailTimeout     int64 `json:"fail_timeout" gorm:"column:fail_timeout;not null;default:0"`
	FailUpstream    int64 `json:"fail_upstream" gorm:"column:fail_upstream;not null;default:0"`
	FailRateLimit   int64 `json:"fail_rate_limit" gorm:"column:fail_rate_limit;not null;default:0"`
	FailClientError int64 `json:"fail_client_error" gorm:"column:fail_client_error;not null;default:0"`
	FailInternal    int64 `json:"fail_internal" gorm:"column:fail_internal;not null;default:0"`

	// —— 硬排除项。必须落库,否则「排除了多少」不可解释、不可审计 ——
	// ExcChannelTest 单列是为了向管理员证明「你点的『测试全部渠道』没污染统计」。
	ExcQuota       int64 `json:"exc_quota" gorm:"column:exc_quota;not null;default:0"`
	ExcViolation   int64 `json:"exc_violation" gorm:"column:exc_violation;not null;default:0"`
	ExcClientGone  int64 `json:"exc_client_gone" gorm:"column:exc_client_gone;not null;default:0"`
	ExcChannelTest int64 `json:"exc_channel_test" gorm:"column:exc_channel_test;not null;default:0"`

	// —— 性能辅助。只累加成功样本,否则一次 600 秒超时就能把均值拉飞 ——
	// LatencyCount 必须独立计数:它不等于分母。
	LatencySumMs int64 `json:"latency_sum_ms" gorm:"column:latency_sum_ms;not null;default:0"`
	LatencyCount int64 `json:"latency_count" gorm:"column:latency_count;not null;default:0"`
	TtftSumMs    int64 `json:"ttft_sum_ms" gorm:"column:ttft_sum_ms;not null;default:0"`
	TtftCount    int64 `json:"ttft_count" gorm:"column:ttft_count;not null;default:0"`
	OutputTokens int64 `json:"output_tokens" gorm:"column:output_tokens;not null;default:0"`
	GenerationMs int64 `json:"generation_ms" gorm:"column:generation_ms;not null;default:0"`

	// SpeedCount 是「贡献了生成速度的成功样本数」,与 LatencyCount 同理必须独立计数。
	//
	// 没有它就只剩 token 总量与生成毫秒总量:能算出一个 t/s,却回答不了
	// 「这是 1 次请求还是 1000 次请求算出来的」,于是样本下限无从执行 ——
	// 一次 3 token 的回复足以产生一个荒唐的速度并被当成模型特性。
	// 本列是后加的,升级前的历史行为 0,那些时段的速度按「无数据」呈现
	// (宁可留白,也不能拿一个不知来源的数字当结论)。
	SpeedCount int64 `json:"speed_count" gorm:"column:speed_count;not null;default:0"`

	// UpdatedAt 手工赋值(禁用 autoUpdateTime,GORM 对 int64 的单位推断跨版本不稳定)。
	// 索引名同样不硬编码(见上面维度三元组那一段):不带名字的单列 `index`
	// 由 GORM 派生成 `idx_<表名>_updated_at`,两张表天然不撞。
	UpdatedAt int64 `json:"updated_at" gorm:"column:updated_at;not null;default:0;index"`
}

func (Bucket) TableName() string { return bucketTable }

// HourBucket 是 Bucket 的小时级汇总,列完全相同,只是 BucketTs 对齐到整点。
//
// 存在理由是查询成本:5 分钟桶保留 15 天约 200 万行,查 30 天趋势要扫百万行;
// 小时桶查 30 天只扫其 1/12。匿名可达的聚合查询必须有这一层,否则是廉价的 DoS 面。
//
// 用匿名内嵌复用 Bucket 的列定义:两张表的列必须逐一对齐,rollup 的
// INSERT ... SELECT 才能靠同一份列清单生成,手抄第二份迟早会漏列。
//
// 内嵌把索引标签也一起继承过来,因此 Bucket 上那三条索引**必须**用
// `composite:` 派生名字而不是硬编码 —— 理由与实测后果写在 Bucket.BucketTs 上面。
type HourBucket struct {
	Bucket
}

func (HourBucket) TableName() string { return hourTable }

const (
	bucketTable = "qy_avail_bucket"
	hourTable   = "qy_avail_bucket_hour"
)

// counterMap 是「列名 → 值」的唯一权威映射。
//
// flush 的累加 upsert、rollup 的 SUM 列表、查询的 SELECT 列表全部由它派生,
// 因此新增一个计数列只需要改这里和结构体两处,不会出现三份清单互相漂移。
func (b *Bucket) counterMap() map[string]int64 {
	return map[string]int64{
		"req_total":         b.ReqTotal,
		"success_count":     b.SuccessCount,
		"fail_soft_stream":  b.FailSoftStream,
		"fail_no_channel":   b.FailNoChannel,
		"fail_timeout":      b.FailTimeout,
		"fail_upstream":     b.FailUpstream,
		"fail_rate_limit":   b.FailRateLimit,
		"fail_client_error": b.FailClientError,
		"fail_internal":     b.FailInternal,
		"exc_quota":         b.ExcQuota,
		"exc_violation":     b.ExcViolation,
		"exc_client_gone":   b.ExcClientGone,
		"exc_channel_test":  b.ExcChannelTest,
		"latency_sum_ms":    b.LatencySumMs,
		"latency_count":     b.LatencyCount,
		"ttft_sum_ms":       b.TtftSumMs,
		"ttft_count":        b.TtftCount,
		"output_tokens":     b.OutputTokens,
		"generation_ms":     b.GenerationMs,
		"speed_count":       b.SpeedCount,
	}
}

// counterColumns 返回全部计数列名,字典序固定,供拼 SQL 使用。
func counterColumns() []string {
	m := (&Bucket{}).counterMap()
	cols := make([]string, 0, len(m))
	for col := range m {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	return cols
}

// addFrom 把另一行的计数累加进来,用于查询侧合并 DB 行与本节点内存热桶。
func (b *Bucket) addFrom(o *Bucket) {
	b.ReqTotal += o.ReqTotal
	b.SuccessCount += o.SuccessCount
	b.FailSoftStream += o.FailSoftStream
	b.FailNoChannel += o.FailNoChannel
	b.FailTimeout += o.FailTimeout
	b.FailUpstream += o.FailUpstream
	b.FailRateLimit += o.FailRateLimit
	b.FailClientError += o.FailClientError
	b.FailInternal += o.FailInternal
	b.ExcQuota += o.ExcQuota
	b.ExcViolation += o.ExcViolation
	b.ExcClientGone += o.ExcClientGone
	b.ExcChannelTest += o.ExcChannelTest
	b.LatencySumMs += o.LatencySumMs
	b.LatencyCount += o.LatencyCount
	b.TtftSumMs += o.TtftSumMs
	b.TtftCount += o.TtftCount
	b.OutputTokens += o.OutputTokens
	b.GenerationMs += o.GenerationMs
	b.SpeedCount += o.SpeedCount
}

// excludedTotal 是被硬排除的样本数(与口径开关无关的那几类)。
func (b *Bucket) excludedTotal() int64 {
	return b.ExcQuota + b.ExcViolation + b.ExcClientGone + b.ExcChannelTest
}
