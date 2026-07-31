package grouppricing

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/bytedance/gopkg/util/gopool"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// shadow.go —— 影子模式的差额记录。
//
// 影子模式是这次改动唯一的安全阀:完整算出"若启用分组价会扣多少",记录下来,
// 但实际仍按旧价扣费。运营对账确认无误后再切真实模式。计费改错不可逆 ——
// 用户的钱已经扣走了,没有任何补偿路径能精确还回去。
//
// 记录链路刻意分成三段,每段的性能约束完全不同:
//
//	relay 线程   note()       纯内存:一次 map 查找 + 一次 atomic 累加,无 I/O
//	worker 队列  guard.HotAsync 承接上面那次累加(顺带拿到 panic 拦截与超时)
//	后台协程     flushLoop()  每分钟把内存热桶累加 upsert 进扩展库
//
// relay 线程绝不等待扩展库。记录丢一条只是统计偏差,而 relay 慢一毫秒是全站问题。

const (
	// shadowBucketSeconds 是聚合桶的粒度。取整点小时:运营问的是
	// "切换后这个月会多收/少收多少",小时粒度足够,而更细的粒度会让
	// 桶数与唯一索引行数按倍数增长,收益为零。
	shadowBucketSeconds = 3600

	// maxShadowKeys 是内存热桶的基数上限。
	//
	// 正常基数是 分组 × 模型 × 口径,个位数到百量级。会失控的只有一种情况:
	// 规则被频繁改动导致 (旧值,新值) 组合爆炸。超过上限即丢弃并告警,
	// 绝不让一份统计数据把节点内存吃光。
	maxShadowKeys = 5000

	shadowGCInterval   = 6 * time.Hour
	shadowGCBatchSize  = 5000
	shadowGCMaxBatches = 200
)

// shadowKey 是内存热桶与数据库唯一索引的共同维度。
//
// (旧值, 新值) 必须进键:同一小时内规则被改过,两段区间的折算系数不同,
// 合并成一行就再也算不出差额了。
type shadowKey struct {
	BucketTs  int64
	GroupName string
	ModelName string
	Mode      string
	OldValue  string
	NewValue  string
	Exact     bool
}

type shadowCell struct {
	requests  atomic.Int64
	sampleReq atomic.Value // string
}

var (
	hotShadow     sync.Map // shadowKey -> *shadowCell
	hotShadowKeys atomic.Int64

	shadowDropped   atomic.Int64
	shadowFlushed   atomic.Int64
	shadowFlushFail atomic.Int64
	shadowFlushOnce sync.Once
)

// note 记录一次"若启用分组价会怎样"。
//
// 只在影子模式下记录。真实模式下主库消费日志里的金额**已经**是按分组价扣的,
// 再按 `实际扣费 × (新值/旧值 - 1)` 折算就成了二次应用,得到的差额是错的 ——
// 而一个看起来合理却是错的对账数字,比没有数字危险得多。
// 切到真实模式后这张表停止增长,这个事实本身就是"已经切过了"的信号。
//
// exact 表示这一段的差额能否按比例精确折算。旧值为 0、或计费口径发生切换
// (原本按 token 计费却配了按次价)时线性不成立,那种行汇总时单独列出。
func note(info *relaycommon.RelayInfo, mode, oldValue string, rule *compiledRule, exact bool) {
	if info == nil || rule == nil || !config.Get().GroupPricing.IsShadow() {
		return
	}
	key := shadowKey{
		BucketTs:  alignBucket(common.GetTimestamp()),
		GroupName: rule.GroupName,
		ModelName: truncate(info.OriginModelName, maxModelNameLen),
		Mode:      mode,
		OldValue:  oldValue,
		NewValue:  rule.ValueText,
		Exact:     exact,
	}
	reqId := truncate(info.RequestId, 64)

	// 必须走 HotAsync:它自带 panic 拦截、超时预算与有界队列。observe 本身是
	// 纯内存 O(1),因此 "grouppricing.shadow" 登记在 guard.syncSafeJobs 里 ——
	// 队列高水位时它降级为同步执行(几十纳秒)而不是被丢弃,
	// 否则对账数字会在高峰期系统性偏小,而偏小的结论恰恰会让人放心地关掉影子模式。
	hotAsync("grouppricing.shadow", func(ctx context.Context) error {
		observe(key, reqId)
		return nil
	})
}

// hotAsync 是 guard.HotAsync 的间接层。
//
// 存在的唯一理由是可测:guard.HotAsync 依赖真实的扩展库健康状态与后台 worker,
// 单元测试里既起不来也无法等待完成,于是"影子模式下差额确实被记下来了"这条
// 断言就只能靠猜。测试把它换成同步执行,生产路径分毫不改。
// shadow_test.go 会断言默认值就是 guard.HotAsync,防止有人把它改成裸调用。
var hotAsync = guard.HotAsync

// observe 把一次命中累加进内存热桶。纯内存,无 I/O。
func observe(key shadowKey, requestId string) {
	if v, ok := hotShadow.Load(key); ok {
		cell := v.(*shadowCell)
		cell.requests.Add(1)
		if requestId != "" {
			cell.sampleReq.Store(requestId)
		}
		return
	}
	if hotShadowKeys.Load() >= maxShadowKeys {
		if n := shadowDropped.Add(1); n == 1 || n%1000 == 0 {
			common.SysError(fmt.Sprintf(
				"qianye/grouppricing: 影子差额维度基数已达上限 %d,累计丢弃 %d 条 —— "+
					"通常意味着规则被频繁改动,对账数字会偏小", maxShadowKeys, n))
		}
		return
	}
	cell := &shadowCell{}
	cell.sampleReq.Store("")
	actual, loaded := hotShadow.LoadOrStore(key, cell)
	if !loaded {
		hotShadowKeys.Add(1)
	}
	c := actual.(*shadowCell)
	c.requests.Add(1)
	if requestId != "" {
		c.sampleReq.Store(requestId)
	}
}

func alignBucket(ts int64) int64 { return ts - ts%shadowBucketSeconds }

// ─────────────────────────────── 落库 ───────────────────────────────

func startShadowFlush() {
	// flush 刻意不加租约:每个节点持有自己的内存热桶,必须各自落库。
	// 累加 upsert + 唯一索引保证多节点的结果被正确合并到同一行。
	shadowFlushOnce.Do(func() { gopool.Go(shadowFlushLoop) })
}

func shadowFlushInterval() time.Duration {
	n := config.Get().GroupPricing.ShadowFlushIntervalSeconds
	if n <= 0 {
		n = 60
	}
	return time.Duration(n) * time.Second
}

// shadowFlushLoop 用 sleep 而非固定 ticker:间隔支持配置热更新。
func shadowFlushLoop() {
	for {
		time.Sleep(shadowFlushInterval())
		if !config.Get().GroupPricing.Enabled || !db.Available() {
			continue
		}
		flushShadow()
	}
}

// flushShadow 把全部内存热桶落库。
//
// 失败即把计数加回内存等下一轮,绝不重试、绝不阻塞:flush 协程卡住会让
// 内存热桶无限堆积,而这只是一份统计数据,没有任何理由把它变成故障源。
func flushShadow() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	now := common.GetTimestamp()
	current := alignBucket(now)

	hotShadow.Range(func(k, v any) bool {
		key := k.(shadowKey)
		cell := v.(*shadowCell)
		n := cell.requests.Swap(0)
		if n == 0 {
			// 只回收两个桶周期之前的空桶:更新的桶随时可能被并发写入,
			// 删掉之后并发的 observe 仍持有旧指针,那次累加就白丢了。
			if key.BucketTs < current-2*shadowBucketSeconds {
				if _, ok := hotShadow.LoadAndDelete(k); ok {
					hotShadowKeys.Add(-1)
				}
			}
			return true
		}
		sample, _ := cell.sampleReq.Load().(string)
		if err := upsertShadow(gdb, key, n, sample, now); err != nil {
			cell.requests.Add(n)
			shadowFlushFail.Add(1)
			db.MarkFailure(err)
			common.SysError("qianye/grouppricing: 影子差额落库失败(已退回内存,下轮重试): " + err.Error())
			return true
		}
		shadowFlushed.Add(n)
		return true
	})
}

// upsertShadow 执行累加 upsert。
//
// 幂等性来自唯一索引 + 累加语义:同一节点不会重复提交同一批(Swap 已清零),
// 多节点同时提交则被行锁串行化,结果正确合并。
func upsertShadow(gdb *gorm.DB, key shadowKey, delta int64, sample string, now int64) error {
	row := &ShadowBucket{
		BucketTs:        key.BucketTs,
		GroupName:       key.GroupName,
		ModelName:       key.ModelName,
		Mode:            key.Mode,
		OldValue:        key.OldValue,
		NewValue:        key.NewValue,
		Exact:           key.Exact,
		Requests:        delta,
		SampleRequestId: sample,
		UpdatedAt:       now,
	}
	assignments := map[string]interface{}{
		"requests":   gorm.Expr("qy_group_price_shadow.requests + ?", delta),
		"updated_at": now,
	}
	if sample != "" {
		assignments["sample_request_id"] = sample
	}
	return gdb.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "bucket_ts"}, {Name: "group_name"}, {Name: "model_name"},
			{Name: "mode"}, {Name: "old_value"}, {Name: "new_value"}, {Name: "exact"},
		},
		DoUpdates: clause.Assignments(assignments),
	}).Create(row).Error
}

// runShadowGC 清理过期的影子差额行。
func runShadowGC(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	days := config.Get().GroupPricing.ShadowRetentionDays
	if days <= 0 {
		return
	}
	cutoff := common.GetTimestamp() - int64(days)*86400
	for i := 0; i < shadowGCMaxBatches; i++ {
		if ctx.Err() != nil {
			return // 租约已丢失,立刻停手,否则就是双跑
		}
		res := gdb.Where("bucket_ts < ?", cutoff).Limit(shadowGCBatchSize).Delete(&ShadowBucket{})
		if res.Error != nil {
			db.MarkFailure(res.Error)
			common.SysError("qianye/grouppricing: 影子差额清理失败: " + res.Error.Error())
			return
		}
		if res.RowsAffected < shadowGCBatchSize {
			return
		}
	}
}

// ─────────────────────────────── 辅助 ───────────────────────────────

// formatFloat 把上游的 float64 价格/倍率折成规范十进制字面量,用作桶的维度键。
//
// 走 decimal 而不是 strconv:两个数学上相等的 float64 可能打印成不同字面量,
// 那会把同一段区间拆成两行,汇总时看起来像是规则被改过。
func formatFloat(f float64) string {
	return normalizeDecimal(decimal.NewFromFloat(f))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
