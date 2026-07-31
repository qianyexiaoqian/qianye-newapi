package availability

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gorm.io/gorm/schema"
)

// rollup 的 INSERT ... SELECT 依赖两张表的列完全一致,列清单又由 counterMap 派生。
// 任何一处漂移都会让 rollup 在运行期报 "Column count doesn't match",
// 而那时 5 分钟桶已经开始按保留期被删,历史数据就真的没了。
func TestBucketAndHourTableShareSchema(t *testing.T) {
	cache := &sync.Map{}
	namer := schema.NamingStrategy{}

	bucketSchema, err := schema.Parse(&Bucket{}, cache, namer)
	require.NoError(t, err)
	hourSchema, err := schema.Parse(&HourBucket{}, cache, namer)
	require.NoError(t, err)

	assert.Equal(t, bucketTable, bucketSchema.Table)
	assert.Equal(t, hourTable, hourSchema.Table)

	for _, col := range append(counterColumns(),
		"bucket_ts", "group_name", "model_name", "updated_at") {
		assert.Containsf(t, bucketSchema.FieldsByDBName, col, "%s 缺少列 %s", bucketTable, col)
		assert.Containsf(t, hourSchema.FieldsByDBName, col, "%s 缺少列 %s", hourTable, col)
	}
	assert.Len(t, hourSchema.FieldsByDBName, len(bucketSchema.FieldsByDBName))
}

// counterMap 是列名的唯一权威:结构体新增一个计数字段却忘了登记,
// 该字段就永远不会被 flush 写进 DB —— 静默丢数据,没有任何报错。
func TestCounterMapCoversEveryCounterField(t *testing.T) {
	bucketSchema, err := schema.Parse(&Bucket{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	registered := (&Bucket{}).counterMap()
	// 维度列与运维列不属于计数器,其余 int64 列都必须登记。
	nonCounter := map[string]bool{
		"id": true, "bucket_ts": true, "group_name": true,
		"model_name": true, "updated_at": true,
	}
	for name := range bucketSchema.FieldsByDBName {
		if nonCounter[name] {
			continue
		}
		assert.Containsf(t, registered, name, "计数列 %s 未登记进 counterMap", name)
	}
}

func TestAddFromAccumulatesEveryCounter(t *testing.T) {
	src := &Bucket{}
	// 用 counterMap 的键数反推:逐列赋不同的值,累加两次后必须整齐翻倍。
	src.ReqTotal, src.SuccessCount = 10, 9
	src.FailSoftStream, src.FailNoChannel, src.FailTimeout = 1, 2, 3
	src.FailUpstream, src.FailRateLimit, src.FailClientError, src.FailInternal = 4, 5, 6, 7
	src.ExcQuota, src.ExcViolation, src.ExcClientGone, src.ExcChannelTest = 8, 9, 10, 11
	src.LatencySumMs, src.LatencyCount = 100, 5
	src.TtftSumMs, src.TtftCount = 50, 5
	src.OutputTokens, src.GenerationMs, src.SpeedCount = 200, 1000, 5

	dst := &Bucket{}
	dst.addFrom(src)
	dst.addFrom(src)

	for col, v := range src.counterMap() {
		assert.Equalf(t, v*2, dst.counterMap()[col], "列 %s 未被 addFrom 累加", col)
	}
}
