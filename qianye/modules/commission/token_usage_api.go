package commission

// token_usage_api.go —— 「这一把密钥今天花了多少」。
//
// 消费方是 API 密钥页的「今日消耗」列(qianye/controller/token_today_usage.go)。
// 它住在本模块而不是 qianye/controller,理由只有一条:**日界与数据源**。
//
// # 日界:与日消费明细共用同一个「一天」
//
// 本模块的 dayline.go 是消费口径唯一的「一天」定义,受
// commission.day_offset_minutes 管辖;日消费明细(api_daily_consume.go)、
// 计佣分桶、日封顶全部走它。密钥页那一列显示的同样是**消费额**,
// 所以它必须落在同一个日界上 —— 否则同一个用户的同一笔消费,
// 在「今日消耗」里算今天、在日消费明细里算昨天,而两张页面都不会报错。
//
// 本站还有另一个「今天」:划转/提现的日限额窗口走服务器本地时区的午夜
// (qianye/modules/withdraw/create.go 的 dayStart)。那是**风控窗口**,
// 不是消费统计,刻意不在这里统一 —— 挪动它等于挪动已经在跑的限额边界。
// 两者当前在演示机上差 7 小时(day_offset_minutes=0 即 UTC,机器本地是 PST),
// 因此接口必须把自己用的窗口(day_start/day_end/day_offset_minutes)一起下发,
// 让界面能把「今日」到底是哪一段写给用户看。
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
//	   而 day_offset_minutes 的取值范围是 -720..840 **分钟**,
//	   非整点日界根本无法用小时桶表达。
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

// ConsumeDayStart 返回 ts 所在「消费日」的起点(unix 秒)。
//
// 导出它而不是让调用方自己算,是 dayline.go 那段文件注释的直接延伸:
// 消费口径的「一天」只能有一处定义。第二处实现哪怕当天写得一模一样,
// 也会在有人调 day_offset_minutes 的那一刻分家,而两边都不会报错。
func ConsumeDayStart(ts int64) int64 { return dayStart(ts) }

// ConsumeDayOffsetMinutes 返回消费日界相对 UTC 的偏移(分钟)。
// 界面要用它把「今日」具体是哪一段写清楚,见本文件头。
func ConsumeDayOffsetMinutes() int { return int(dayOffsetSeconds() / 60) }

// ConsumeDaySeconds 是一整天的秒数,供调用方算出日界的右端点。
const ConsumeDaySeconds = secondsPerDay

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
