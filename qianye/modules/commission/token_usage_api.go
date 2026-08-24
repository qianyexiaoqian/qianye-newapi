package commission

// token_usage_api.go —— 「这一把密钥今天花了多少」。
//
// 消费方是 API 密钥页的「今日消耗」列(qianye/controller/token_today_usage.go)。
// 它住在本模块而不是 qianye/controller,理由是**数据源**:它与日消费明细
// 查的是同一张表、同一个 type 过滤,而且共用 logs_index.go 里那条覆盖索引的
// 补建与退役逻辑 —— 那些全在本模块里。
//
// # 日界:不在本模块,由调用方给
//
// 早先这一列与日消费明细共用返佣的「消费日」(commission.day_offset_minutes),
// 理由是"同一笔钱不该在两张页面上算进不同的天"。**项目方后来明确要求改成
// 服务器本地时区的自然日**:「api 密钥的今日消耗以服务器的时间为准,即
// 0 点到 23 点 59 分 59 秒是今日的消耗。」
//
// 于是日界搬去了 qianye/serverday(与提现/划转的日限额窗口同一份实现),
// 本函数只收 [startTs, endTs) 两个参数,对"这是哪一天"不再有任何主张。
// 代价是明摆着的:演示机上 day_offset_minutes=0(UTC)而机器本地是 PST,
// 密钥页的「今日」与日消费明细的「今日」差 7 小时,两个数都对、两边都不报错。
// 所以那一列的界面上必须写清区间与时区 —— 见 qianye/controller/token_today_usage.go。
//
// # 数据源:主库 logs 的 type=2,不是 quota_data
//
// quota_data 看着更便宜(它天生就有 token_id + quota,而且已经按小时预聚合)。
// 但它答不了这个问题,三条各自独立:
//
//	① 它由 common.DataExportEnabled 这个后台开关控制,关掉之后一行都不写 ——
//	   表现是「今日消耗」全站恒为 0,而不是报错;
//	② 它每 DataExportInterval 分钟才落盘一次,刚发的请求在列里看不见;
//	③ 它**只精确到小时**(LogQuotaData 里 createdAt -= createdAt%3600),
//	   而日界不保证落在整点上:服务器本地时区可以是 UTC+05:30(Asia/Kolkata)
//	   或 UTC+05:45(Asia/Kathmandu),小时桶根本表达不了。
//
// 所以口径与 api_daily_consume.go 完全一致:logs 的 type = LogTypeConsume。
// 退款(LogTypeRefund=6)不计入 —— 日消费明细也不计,两处必须一致。
//
// # 性能:一次聚合,而且必须走覆盖索引
//
// 备份库实测(MySQL 8.0.28,logs 447 万行 / type=2 416 万行,缓冲池已热,
// 取全表最重的那一个 (用户, 日) 组合:user_id=1009,当天 10.6 万行):
//
//	只有 (user_id, type, created_at, quota)            3865 / 3967 / 4071 ms
//	    EXPLAIN Extra: Using index condition; Using MRR; Using temporary
//	换成 (user_id, type, created_at, token_id, quota)   231 / 249 / 433 ms
//	    EXPLAIN Extra: Using where; Using index; Using temporary
//
// 差别全在 token_id 上:它不在旧索引里,于是 21 万行要逐行回表。
// **Select 的列集合是有承重作用的**(与 api_daily_consume.go 同一条纪律):
// 这里只取 token_id 与 SUM(quota),想加列之前先把索引一起加宽。
//
// 而且**必须一次查完全部行**:密钥页一页 20 行,逐行发一次聚合就是一次页面
// 加载 20 条这样的查询打在主库上。本函数按 user_id 一次聚合出该用户今天
// 用过的全部令牌,页面只做 map 查表。

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/model"
)

const (
	// maxTokenDayUsageRows 是一次聚合允许返回的令牌数上界。
	//
	// 超过就**报错而不是截断**:被截掉的那几把密钥会在界面上显示成「今日 0」,
	// 而那是一个看起来完全正常的金额。与 maxDailyConsumeRows 同一条理由。
	//
	// 1000 远高于现实(备份库单用户令牌数最多 37 把),它挡的是
	// 「某个账号被脚本刷出几万把令牌」那一类失控,不是日常路径。
	maxTokenDayUsageRows = 1000

	// tokenDayUsageQueryTimeout 是这条聚合的截止时间。
	//
	// 它是「覆盖索引不在」的兜底:没有它,上面那张表里 3.9 秒的那一档会直接
	// 加在每一次打开密钥页上。索引由 ensureLogsIndexes 后台补建,补建期间
	// (备份库实测建一条 498 秒)这条接口宁可自己超时,也不把主库拖住。
	tokenDayUsageQueryTimeout = 8 * time.Second
)

var errTokenDayUsageTooManyRows = errors.New("今日使用过的密钥数量过多,无法汇总")

// TokenDayUsageIndexReady 报告这条聚合依赖的覆盖索引在不在。
// 只是个显示位,判据是查询自己的超时 —— 见 logs_index.go。
func TokenDayUsageIndexReady() bool { return logsIndexReady(logsTokenDailyIndex) }

// TokenDayUsage 汇总某个用户在 [startTs, endTs) 内每一把令牌的消费额。
//
// 返回 token_id → 消费额(额度单位,与 logs.quota 同一单位)。
// **今天没有消费的令牌不会出现在这张表里** —— 调用方必须把「缺席」渲染成 0,
// 不是「-」也不是「加载中」:缺席与 SUM 恰好为 0(比如倍率 0 的免费分组)
// 在用户眼里是同一件事,都是「今天没花钱」。
func TokenDayUsage(ctx context.Context, userId int, startTs, endTs int64) (map[int]int64, error) {
	if model.LOG_DB == nil {
		return nil, errors.New("日志库未初始化")
	}
	if userId <= 0 {
		return nil, errors.New("用户不合法")
	}
	if endTs <= startTs {
		return nil, errors.New("时间区间不合法")
	}

	type row struct {
		TokenId      int   `gorm:"column:token_id"`
		ConsumeQuota int64 `gorm:"column:consume_quota"`
	}
	var rows []row
	// 多取一行用来判断"是不是超过上界了",不是用来展示的。
	err := model.LOG_DB.WithContext(ctx).Model(&model.Log{}).
		Select("token_id, COALESCE(SUM(quota),0) AS consume_quota").
		Where("user_id = ?", userId).
		Where("type = ?", model.LogTypeConsume).
		Where("created_at >= ? AND created_at < ?", startTs, endTs).
		Group("token_id").
		Limit(maxTokenDayUsageRows + 1).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > maxTokenDayUsageRows {
		return nil, errTokenDayUsageTooManyRows
	}
	out := make(map[int]int64, len(rows))
	for _, r := range rows {
		// token_id = 0 是「这条日志没有令牌」(后台任务、渠道测试)。
		// 它对不上任何一行密钥,留在结果里只会让调用方多一次无用的查表。
		if r.TokenId <= 0 {
			continue
		}
		out[r.TokenId] = r.ConsumeQuota
	}
	return out, nil
}

// TokenDayUsageTimeout 是 TokenDayUsage 建议的 context 截止时间。
// 导出是为了让 HTTP 层能把这个上界写进自己的超时里,而不是各写一个数。
func TokenDayUsageTimeout() time.Duration { return tokenDayUsageQueryTimeout }
