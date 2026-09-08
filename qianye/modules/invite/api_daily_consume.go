package invite

// api_daily_consume.go —— 「昨天哪个用户消费了多少」。
//
// # 数据源:主库 logs,不是星屑的日桶
//
// 星屑那一侧的 qy_sd_invite_accrual 看着现成 —— 它天生就有 (bucket_date, invitee_id,
// base_quota) 三列,一条 GROUP BY 就出报表。但它**结构性地**答不了这个问题:
// 它是「谁给谁挣了星屑」,不是「谁消费了多少」。没有上线的账号、被拉黑的关系、
// 邀请人分组档是 0 的下线、违规扣费、渠道测试 —— 每一条都是"这笔消费在日桶里
// 不存在",而运营打开这张表恰恰是为了"谁在花钱"。所以口径只能是主库 logs 的 type=2。
//
// # 两个数并排,而不是二选一
//
// 进了下线消费返的基数照样给,放在消费额旁边,并给出 uncounted = 消费额 − 基数。
// 运营看到两个数不一样的时候必须能当场知道为什么,不能让他自己猜。
//
// # 性能:这条查询上线前必须先有索引
//
// 备份库 logs 447 万行(其中 type=2 416 万),实测(MySQL 8.0.28,同一台机器):
//
//	区间        原有索引 idx_created_at_type   加 idx_qy_logs_daily_consume 之后
//	1 天(最忙)  3915 ms                        418 ms
//	7 天        > 540000 ms(9 分钟未跑完)      1501 ms
//	31 天       > 120000 ms                    3688 ms
//	全量 150 天  —                              8005 ms
//
// 原有索引是 (created_at, type):range 能用上,但 user_id / quota 不在索引里,
// 38 万行要逐行回表,而区间一放大优化器干脆放弃索引改走全表。
// 新索引 (type, created_at, user_id, quota) 让这条聚合变成纯覆盖扫描。
//
// **Select 的列集合是有承重作用的**:实测多取一个 prompt_tokens,覆盖立刻失效,
// 同一天的查询从 418 ms 退回 5020 ms。所以这里只取 user_id / COUNT(*) /
// SUM(quota) 三样,想加列之前先把索引一起加宽,否则就是把慢查询悄悄放回来。
//
// 即便如此也不假设它一定在:createLogsDailyConsumeIndex 是后台补建的,
// 而 DBA 也可能把它删掉。所以每条查询都带 dailyConsumeQueryTimeout 的
// context 截止时间 —— 索引不在时这条接口自己超时报错,而不是把主库拖住。

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/csvsafe"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	// maxDailyConsumeDays 是查询区间的天数上界(含首尾)。
	//
	// 31 天不是随手取的:它是上面那张实测表里最后一个还在秒级的档位
	// (3688 ms)。再往上就要按月物化,而不是继续放宽这个数。
	maxDailyConsumeDays = 31

	// maxDailyConsumeRows 是聚合结果的行数上界。
	//
	// 超过就明确报错,**不截断** —— 截断出来的是一张看起来正常、实际少了人的
	// 消费报表,而运营会拿它去对账。
	maxDailyConsumeRows = 20000

	// maxDailyConsumeKeywordHits 是按用户名搜索时先在主库解析出的用户数上界。
	// 关键词太泛(比如一个 "a")时宁可让运营再打几个字,也不要把 IN 列表撑爆。
	maxDailyConsumeKeywordHits = 2000

	// dailyConsumeQueryTimeout 是聚合查询的截止时间。
	//
	// 它是"索引没了"这件事的兜底:没有它,一条 7 天的查询会在主库上跑 9 分钟,
	// 而运营只会不停地刷新页面,把 9 分钟乘以刷新次数。
	dailyConsumeQueryTimeout = 20 * time.Second

	// logsDailyConsumeIndex 是主表(按人聚合)依赖的覆盖索引名。
	logsDailyConsumeIndex = "idx_qy_logs_daily_consume"

	// logsTokenDailyIndex 是按天下钻(按人等值 + 按天聚合)与密钥页「今日消耗」
	// (按人等值 + 按令牌聚合)共用的覆盖索引名。
	// 两条为什么不能合并、以及没有它时下钻有多慢,见 logs_index.go 的文件头。
	logsTokenDailyIndex = "idx_qy_logs_token_daily"

	// logsUserDailyIndex 是 logsTokenDailyIndex 的前身,列集合是它的前缀。
	// 已退役,只在 retiredLogsIndexes 里作为「要删掉的历史索引名」出现。
	logsUserDailyIndex = "idx_qy_logs_user_daily"
)

var (
	errDailyRangeFormat = errors.New("日期格式必须是 yyyymmdd")
	errDailyRangeOrder  = errors.New("开始日期不能晚于结束日期")
	errDailyRangeTooBig = fmt.Errorf("查询区间最多 %d 天", maxDailyConsumeDays)
	errDailyTooManyRows = fmt.Errorf("区间内消费用户数超过 %d,请缩小日期区间或加上用户筛选", maxDailyConsumeRows)
	errDailyKeywordWide = fmt.Errorf("用户关键词命中超过 %d 个账号,请输入更完整的用户名", maxDailyConsumeKeywordHits)
)

// dailyRange 是一次查询的时间口径。
//
// 日界完全走 dayline.go(dayKeyStart / dayStart),不自己算 —— 那是扩展唯一的
// "一天"定义,受 invite.day_offset_minutes 管辖。报表与星屑日结必须是同一个日界:
// 差一个小时,"昨日消费"里就会混进今天凌晨的单子,而运营拿它对的是昨天的账。
type dailyRange struct {
	StartDay string // yyyymmdd,含
	EndDay   string // yyyymmdd,含
	StartTs  int64  // [StartTs, EndTs) 是对应的 unix 秒半开区间
	EndTs    int64
	Days     int
}

// parseDailyRange 解析 ?start_date= / ?end_date=,默认昨日。
//
// 零值口径:两个参数都缺省 = 昨日一天,这正是需求原话里的「昨日使用记录」;
// 只给 start_date = 从那天到那天(而不是到今天),因为"我要看 8 月 3 日"
// 是最常见的单日诉求,自动延伸到今天会让页面上多出一堆他没要的行。
func parseDailyRange(c *gin.Context, now int64) (dailyRange, error) {
	yesterday := dayKey(dayStart(now) - 1)

	startDay := strings.TrimSpace(c.Query("start_date"))
	endDay := strings.TrimSpace(c.Query("end_date"))
	if startDay == "" && endDay == "" {
		startDay, endDay = yesterday, yesterday
	} else if startDay == "" {
		startDay = endDay
	} else if endDay == "" {
		endDay = startDay
	}

	startTs, ok := dayKeyStart(startDay)
	if !ok {
		return dailyRange{}, errDailyRangeFormat
	}
	endStart, ok := dayKeyStart(endDay)
	if !ok {
		return dailyRange{}, errDailyRangeFormat
	}
	if endStart < startTs {
		return dailyRange{}, errDailyRangeOrder
	}
	days := int((endStart-startTs)/secondsPerDay) + 1
	if days > maxDailyConsumeDays {
		return dailyRange{}, errDailyRangeTooBig
	}
	return dailyRange{
		StartDay: startDay,
		EndDay:   endDay,
		StartTs:  startTs,
		EndTs:    endStart + secondsPerDay,
		Days:     days,
	}, nil
}

// consumeAgg 是 logs 侧聚合出来的一行。列集合与覆盖索引严格对应,见文件头。
type consumeAgg struct {
	UserId       int   `gorm:"column:user_id"`
	RequestCount int64 `gorm:"column:request_count"`
	ConsumeQuota int64 `gorm:"column:consume_quota"`
}

// aggregateConsumeFromLogs 按用户汇总区间内的消费额。
//
// userIds 为 nil 表示不限用户;非 nil 且为空表示"筛选命中了零个用户",
// 此时直接返回空而不是退化成全表 —— 那是本仓 httpq 注释里点名的那类
// "筛选静默失效,接口返回未经筛选的全站数据"。
func aggregateConsumeFromLogs(ctx context.Context, r dailyRange, userIds []int) ([]consumeAgg, error) {
	if userIds != nil && len(userIds) == 0 {
		return nil, nil
	}
	if model.LOG_DB == nil {
		return nil, errors.New("日志库未初始化")
	}
	q := model.LOG_DB.WithContext(ctx).Model(&model.Log{}).
		Select("user_id, COUNT(*) AS request_count, COALESCE(SUM(quota),0) AS consume_quota").
		Where("type = ?", model.LogTypeConsume).
		Where("created_at >= ? AND created_at < ?", r.StartTs, r.EndTs)
	if userIds != nil {
		q = q.Where("user_id IN ?", userIds)
	}
	var rows []consumeAgg
	// 多取一行用来判断"是不是超过上界了",不是用来展示的。
	if err := q.Group("user_id").Limit(maxDailyConsumeRows + 1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > maxDailyConsumeRows {
		return nil, errDailyTooManyRows
	}
	return rows, nil
}

// dayBucketSQL 是"把 created_at 归到哪一天"的 SQL 表达式,返回该天日界的
// **偏移后**秒数(减掉 dayOffsetSeconds 才是真实 unix 秒)。
//
// # 为什么是纯整数取模,不是 DATE()/FROM_UNIXTIME()/DATE_TRUNC()
//
// 三种数据库的日期函数没有一个是通用的:MySQL 的 FROM_UNIXTIME 受会话时区
// 影响、PostgreSQL 只有 to_timestamp + date_trunc、SQLite 是 strftime,
// 而 ClickHouse 又是另一套。更要命的是**时区**:任何一个走"日期类型"的写法
// 都会把日界交给数据库会话的 TZ 决定,而这里的日界是 dayline.go 里那一个
// 固定偏移 —— 两者一旦不同,下钻出来的每日金额与主表的区间合计对不上,
// 而且是差一小时那种最难发现的对不上。
//
// `+`、`-`、`%` 在四种方言下语义完全一致(注意不能用 `/`:MySQL 的 `/` 返回
// DECIMAL 而不是整除)。off 走参数而不是拼进字符串。
//
// 实测这个表达式不破坏覆盖索引:EXPLAIN 的 Extra 仍是 `Using index`。
func dayBucketSQL(column string) (string, int64) {
	off := dayOffsetSeconds()
	shifted := "(" + column + " + ?)"
	return shifted + " - (" + shifted + " % " + strconv.FormatInt(secondsPerDay, 10) + ")", off
}

// userDayAgg 是某个用户某一天的 logs 侧聚合。
type userDayAgg struct {
	DayStart     int64 `gorm:"column:day_start"`
	RequestCount int64 `gorm:"column:request_count"`
	ConsumeQuota int64 `gorm:"column:consume_quota"`
}

// aggregateUserDailyConsume 按天汇总**一个**用户在区间内的消费。
//
// 为什么下钻是单独一条接口、而不是给主表加一个天维度:主表一行 = 一个人,
// 行数不随天数膨胀,maxDailyConsumeRows 那个上界才守得住"20000 个人"这个
// 语义;把天加进主表的 GROUP BY,同一份数据在 31 天区间下会变成人数 × 天数。
//
// 单人下钻的代价则是**有界的**:输出至多 maxDailyConsumeDays 行,而扫描量
// 由 (user_id, type, created_at, token_id, quota) 这条索引收窄到这一个人在这段时间里的
// 行。没有那条索引时优化器会改走 idx_user_id_id 并逐行回表,实测 6.5 秒
// (见 logs_index.go 的文件头)—— 所以这条接口与那条索引是一起交付的。
func aggregateUserDailyConsume(ctx context.Context, r dailyRange, userId int) ([]userDayAgg, error) {
	if model.LOG_DB == nil {
		return nil, errors.New("日志库未初始化")
	}
	expr, off := dayBucketSQL("created_at")
	var rows []userDayAgg
	err := model.LOG_DB.WithContext(ctx).Model(&model.Log{}).
		Select(expr+" AS day_start, COUNT(*) AS request_count, COALESCE(SUM(quota),0) AS consume_quota",
			off, off).
		Where("user_id = ?", userId).
		Where("type = ?", model.LogTypeConsume).
		Where("created_at >= ? AND created_at < ?", r.StartTs, r.EndTs).
		Group("day_start").
		Limit(maxDailyConsumeDays + 1).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// dailyConsumeRow 是管理端的一行。
type dailyConsumeRow struct {
	UserId       int    `json:"user_id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	UserGroup    string `json:"user_group"`
	RequestCount int64  `json:"request_count"`
	// ConsumeQuota 是这个区间里**真实扣掉**的额度,口径 = 主库 logs 的 type=2。
	ConsumeQuota int64 `json:"consume_quota"`
	// InviteBaseQuota 是同一区间进了下线消费返日桶的基数。它天然 <= ConsumeQuota。
	InviteBaseQuota int64 `json:"invite_base_quota"`
	// UncountedQuota = ConsumeQuota − InviteBaseQuota,即"消费了但没给上线返"的部分。
	// 单列出来是因为运营的下一个问题永远是"那少的那块去哪了"。
	UncountedQuota int64 `json:"uncounted_quota"`
	// InviteGross 是同一区间给上线的下线消费返毛额(星屑,全精度字符串)。
	InviteGross      string `json:"invite_gross"`
	HasInviteAccrual bool   `json:"has_invite_accrual"`
	InviterId        int    `json:"inviter_id"`
	InviterUsername  string `json:"inviter_username"`
	// AccountRemoved 表示这一行的账号在 users 里已经不在了(软删或被硬删)。
	// 它的 uncounted_quota 恒等于全额消费额,而运营按那几条原因去查会一条都
	// 对不上 —— 必须由这张表自己说出这一条:账号已被删除。
	AccountRemoved bool `json:"account_removed"`
}

var dailyConsumeSorts = map[string]func(a, b dailyConsumeRow) bool{
	"consume_quota":     func(a, b dailyConsumeRow) bool { return a.ConsumeQuota < b.ConsumeQuota },
	"request_count":     func(a, b dailyConsumeRow) bool { return a.RequestCount < b.RequestCount },
	"invite_base_quota": func(a, b dailyConsumeRow) bool { return a.InviteBaseQuota < b.InviteBaseQuota },
	"uncounted_quota":   func(a, b dailyConsumeRow) bool { return a.UncountedQuota < b.UncountedQuota },
	"user_id":           func(a, b dailyConsumeRow) bool { return a.UserId < b.UserId },
}

// sortDailyConsume 就地排序。
//
// 次序键固定加上 user_id:仅按金额排时大量并列的 0 会在不同页之间换位置,
// 翻页会看到同一个人出现两次、另一个人一次都不出现。
func sortDailyConsume(rows []dailyConsumeRow, sortKey, order string) {
	less, ok := dailyConsumeSorts[sortKey]
	if !ok {
		less = dailyConsumeSorts["consume_quota"]
	}
	desc := order != "asc"
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if less(a, b) {
			return !desc
		}
		if less(b, a) {
			return desc
		}
		return a.UserId < b.UserId
	})
}

// resolveKeywordUserIds 把用户关键词解析成主库里的 user_id 集合。
//
// 返回 nil 表示"没有给关键词",与"给了但一个都没命中"(空切片)是两种
// 完全不同的结果,调用方必须区分,见 aggregateConsumeFromLogs。
func resolveKeywordUserIds(ctx context.Context, keyword string) ([]int, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, nil
	}
	if model.DB == nil {
		return nil, errors.New("主库未初始化")
	}
	// Unscoped 与 loadUserProfiles 同一条理由:已删除账号的消费仍在 logs 里,
	// 报表上看得见就必须搜得出来,否则运营连下钻自查的路都没有。
	q := model.DB.WithContext(ctx).Model(&model.User{}).Unscoped()
	// 纯数字先按 id 精确命中:运营从别的页面下钻过来带的就是 id,
	// 而 LIKE '%1622%' 会把 11622、16220 一起捞进来。
	if id, err := strconv.Atoi(keyword); err == nil && id > 0 {
		q = q.Where("id = ?", id)
	} else {
		// 子串匹配,但必须走 httpq.SearchLike:转义 % 与 _、折叠大小写,三种数据库同一口径。
		expr, pattern := httpq.SearchLike(keyword, httpq.MatchContains, "username", "display_name", "email")
		q = q.Where(expr, pattern, pattern, pattern)
	}
	var ids []int
	if err := q.Limit(maxDailyConsumeKeywordHits+1).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) > maxDailyConsumeKeywordHits {
		return nil, errDailyKeywordWide
	}
	if ids == nil {
		ids = []int{}
	}
	return ids, nil
}

// userProfile 是补名字用的最小主库投影。
type userProfile struct {
	Id          int    `gorm:"column:id"`
	Username    string `gorm:"column:username"`
	DisplayName string `gorm:"column:display_name"`
	Email       string `gorm:"column:email"`
	Group       string `gorm:"column:group"`
	InviterId   int    `gorm:"column:inviter_id"`
	// DeletedAt 有值表示这是一个**已删除**的账号(管理端删除按钮走的是软删)。
	// 用 gorm.DeletedAt 而不是 *time.Time:它自带 Scanner,三种库的
	// datetime/NULL 都由 GORM 自己处理。
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at"`
}

// loadUserProfiles 按 id 批量取主库的展示字段。
//
// 分批而不是一条 IN:导出路径最多会带 maxDailyConsumeRows 个 id,
// 一条几万元素的 IN 在 MySQL 上会超过 max_allowed_packet。
func loadUserProfiles(ctx context.Context, ids []int) (map[int]userProfile, error) {
	out := make(map[int]userProfile, len(ids))
	if len(ids) == 0 || model.DB == nil {
		return out, nil
	}
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		var rows []userProfile
		err := model.DB.WithContext(ctx).Model(&model.User{}).
			// Unscoped 是必需的:model.User 带 gorm.DeletedAt,管理端的删除按钮
			// 走软删,而 logs 是永久的。默认作用域会自动补 deleted_at IS NULL,
			// 于是一个被删掉的账号在这张报表里渲染成用户名/分组/上线四列全空的
			// 一行,与"确实没有上线的正常用户"完全分不开,按 id 搜还搜不出来。
			Unscoped().
			// 列名分成多个参数传:GORM 会逐个按当前方言加引号,group 这个
			// 保留字因此在三种库上都是对的。
			Select("id", "username", "display_name", "email", "group", "inviter_id", "deleted_at").
			Where("id IN ?", ids[start:end]).Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.Id] = r
		}
	}
	return out, nil
}

// buildDailyConsumeRows 把三份数据(logs 聚合、日桶汇总、主库画像)拼成展示行。
//
// 名字只给**需要展示的那一批**补:管理端分页时是当前页,导出时是全部行。
func buildDailyConsumeRows(ctx context.Context, aggs []consumeAgg,
	accruals map[int]DayAccrual) ([]dailyConsumeRow, error) {

	ids := make([]int, 0, len(aggs))
	for _, a := range aggs {
		ids = append(ids, a.UserId)
	}
	profiles, err := loadUserProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	inviterIds := make([]int, 0, 16)
	seen := map[int]bool{}
	for _, p := range profiles {
		if p.InviterId > 0 && !seen[p.InviterId] {
			seen[p.InviterId] = true
			inviterIds = append(inviterIds, p.InviterId)
		}
	}
	inviters, err := loadUserProfiles(ctx, inviterIds)
	if err != nil {
		return nil, err
	}

	rows := make([]dailyConsumeRow, 0, len(aggs))
	for _, a := range aggs {
		p, found := profiles[a.UserId]
		acc, hasAcc := accruals[a.UserId]
		uncounted := a.ConsumeQuota - acc.BaseQuota
		if uncounted < 0 {
			// 基数大于消费额只有一种可能:日志被保留期清理掉了一部分,
			// 而日桶还在。夹到 0 而不是显示负数 —— 负的"未计入额"没有意义,
			// 真正的信号是 meta 里的 accrual_users_without_logs。
			uncounted = 0
		}
		rows = append(rows, dailyConsumeRow{
			UserId:           a.UserId,
			Username:         p.Username,
			DisplayName:      p.DisplayName,
			Email:            p.Email,
			UserGroup:        p.Group,
			RequestCount:     a.RequestCount,
			ConsumeQuota:     a.ConsumeQuota,
			InviteBaseQuota:  acc.BaseQuota,
			UncountedQuota:   uncounted,
			InviteGross:      acc.Gross.String(),
			HasInviteAccrual: hasAcc,
			InviterId:        p.InviterId,
			InviterUsername:  inviters[p.InviterId].Username,
			AccountRemoved:   !found || p.DeletedAt.Valid,
		})
	}
	return rows, nil
}

// dailyConsumeQuery 是管理端两条路由(列表 / 导出)共用的取数过程。
type dailyConsumeQuery struct {
	Range    dailyRange
	Aggs     []consumeAgg
	Accruals map[int]DayAccrual
	// AccrualOnly 是"日桶里有、logs 里没有"的下线数。
	//
	// 正常情况下恒为 0。不为 0 的唯一合理解释是日志保留期把这段消费清掉了,
	// 而日桶是永久账本 —— 那时这张报表的消费额一侧天然缺一块,必须让运营
	// 看见这个数字,而不是让他对着一张少了行的表算账。
	AccrualOnly int
}

func runDailyConsumeQuery(c *gin.Context) (dailyConsumeQuery, error) {
	var out dailyConsumeQuery
	r, err := parseDailyRange(c, common.GetTimestamp())
	if err != nil {
		return out, err
	}
	out.Range = r

	ctx, cancel := context.WithTimeout(c.Request.Context(), dailyConsumeQueryTimeout)
	defer cancel()

	ids, err := resolveKeywordUserIds(ctx, c.Query("keyword"))
	if err != nil {
		return out, err
	}
	aggs, err := aggregateConsumeFromLogs(ctx, r, ids)
	if err != nil {
		return out, err
	}
	accruals, err := InviteAccrualByInvitee(ctx, r.StartDay, r.EndDay, 0)
	if err != nil {
		return out, err
	}
	out.Aggs, out.Accruals = aggs, accruals

	inLogs := make(map[int]bool, len(aggs))
	for _, a := range aggs {
		inLogs[a.UserId] = true
	}
	for inviteeId := range accruals {
		if !inLogs[inviteeId] {
			out.AccrualOnly++
		}
	}
	return out, nil
}

// adminListDailyConsume 是管理端的日消费明细。
func adminListDailyConsume(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	q, err := runDailyConsumeQuery(c)
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}
	page, size := httpq.Paginate(c, listPaging)

	// 排序在拼行**之前**做不了(要按基数排),所以先拼一次轻量行:
	// 这一步不补名字,只算数,补名字留给切完页之后。
	light := make([]dailyConsumeRow, 0, len(q.Aggs))
	for _, a := range q.Aggs {
		acc := q.Accruals[a.UserId]
		uncounted := a.ConsumeQuota - acc.BaseQuota
		if uncounted < 0 {
			uncounted = 0
		}
		light = append(light, dailyConsumeRow{
			UserId:          a.UserId,
			RequestCount:    a.RequestCount,
			ConsumeQuota:    a.ConsumeQuota,
			InviteBaseQuota: acc.BaseQuota,
			UncountedQuota:  uncounted,
		})
	}
	sortDailyConsume(light, c.Query("sort"), c.Query("order"))
	total := len(light)
	pageRows := httpq.Slice(light, page, size)

	pageAggs := make([]consumeAgg, 0, len(pageRows))
	for _, r := range pageRows {
		pageAggs = append(pageAggs, consumeAgg{
			UserId:       r.UserId,
			RequestCount: r.RequestCount,
			ConsumeQuota: r.ConsumeQuota,
		})
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), dailyConsumeQueryTimeout)
	defer cancel()
	items, err := buildDailyConsumeRows(ctx, pageAggs, q.Accruals)
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}

	var sumConsume, sumBase, sumReq int64
	for _, r := range light {
		sumConsume += r.ConsumeQuota
		sumBase += r.InviteBaseQuota
		sumReq += r.RequestCount
	}
	respond(c, gin.H{
		"items":     items,
		"total":     total,
		"p":         page,
		"page_size": size,
		"range": gin.H{
			"start_date": q.Range.StartDay,
			"end_date":   q.Range.EndDay,
			"days":       q.Range.Days,
			"max_days":   maxDailyConsumeDays,
		},
		"summary": gin.H{
			"user_count":        total,
			"request_count":     sumReq,
			"consume_quota":     sumConsume,
			"invite_base_quota": sumBase,
			"uncounted_quota":   maxInt64(sumConsume-sumBase, 0),
		},
		// index_ready 让"这张表今天为什么这么慢"有一个可以直接看的答案。
		"index_ready":                logsDailyConsumeIndexReady(),
		"accrual_users_without_logs": q.AccrualOnly,
	})
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// adminUserDailyConsume 是主表某一行的按天下钻:一个人 × 区间内的每一天。
//
// 区间内每一天都出一行,没消费的那天全是 0。缺行的表会让运营把"这天没花钱"
// 与"这天的数据没查出来"看成同一件事,而这恰恰是他打开下钻要区分的东西。
// 行数上界因此就是 maxDailyConsumeDays,与查询本身返回多少行无关。
func adminUserDailyConsume(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	// 走 httpq.Int 而不是 strconv.Atoi:上界必须是解析的一部分。
	// 缺失、非纯数字、负号、超界一律回落 0,在这里就是"没给 user_id",
	// 而 user_id 必填正是这条接口不退化成全站按天聚合的前提。
	userId := httpq.Int(c, "user_id", 0)
	if userId <= 0 {
		badRequest(c, "qy_daily_user_required", "必须指定 user_id")
		return
	}
	r, err := parseDailyRange(c, common.GetTimestamp())
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), dailyConsumeQueryTimeout)
	defer cancel()

	aggs, err := aggregateUserDailyConsume(ctx, r, userId)
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}
	accruals, err := InviteAccrualByDay(ctx, userId, r.StartDay, r.EndDay)
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}

	// 归一到日键之后再对齐:logs 侧给的是偏移后的日界秒数,日桶侧给的是
	// yyyymmdd。两者都由 dayline.go 定义,换算走同一处,不在这里自己算。
	off := dayOffsetSeconds()
	byDay := make(map[string]userDayAgg, len(aggs))
	for _, a := range aggs {
		byDay[dayKey(a.DayStart-off)] = a
	}

	items := make([]gin.H, 0, r.Days)
	var sumReq, sumConsume, sumBase int64
	sumGross := decimal.Zero
	for i := 0; i < r.Days; i++ {
		ts := r.StartTs + int64(i)*secondsPerDay
		day := dayKey(ts)
		a := byDay[day]
		acc := accruals[day]
		uncounted := maxInt64(a.ConsumeQuota-acc.BaseQuota, 0)
		sumReq += a.RequestCount
		sumConsume += a.ConsumeQuota
		sumBase += acc.BaseQuota
		sumGross = sumGross.Add(acc.Gross)
		items = append(items, gin.H{
			"date":              day,
			"day_start":         ts,
			"request_count":     a.RequestCount,
			"consume_quota":     a.ConsumeQuota,
			"invite_base_quota": acc.BaseQuota,
			"uncounted_quota":   uncounted,
			"invite_gross":      acc.Gross.String(),
		})
	}

	respond(c, gin.H{
		"user_id": userId,
		"items":   items,
		"range": gin.H{
			"start_date": r.StartDay,
			"end_date":   r.EndDay,
			"days":       r.Days,
			"max_days":   maxDailyConsumeDays,
		},
		"summary": gin.H{
			"request_count":     sumReq,
			"consume_quota":     sumConsume,
			"invite_base_quota": sumBase,
			"uncounted_quota":   maxInt64(sumConsume-sumBase, 0),
			"invite_gross":      sumGross.String(),
		},
		// 这一条报的是**下钻自己那条**索引,不是主表那条:两条各建各的,
		// 主表快不代表下钻快,反过来也一样。
		"index_ready": logsIndexReady(logsTokenDailyIndex),
	})
}

// adminExportDailyConsume 导出 CSV。
//
// 运营对这类表的第一个动作就是"发给财务"。不做的话他会自己翻页复制,
// 复制出来的数是没有区间标注的 —— 而这张表最容易出错的恰恰是区间。
//
// 它写审计:导出是**一次性把全站消费额带走**,是这个模块里泄漏面最大的读操作。
// 它不改钱,但"谁在什么时候导走了哪个区间的全站消费明细"必须留痕,
// 否则事后查数据外流时这里是个盲区。
func adminExportDailyConsume(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagInvite) {
		return
	}
	q, err := runDailyConsumeQuery(c)
	if err != nil {
		writeDailyConsumeExportAudit(c, dailyRange{}, 0, qymodel.ResultFail, err.Error())
		respondDailyConsumeError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), dailyConsumeQueryTimeout)
	defer cancel()
	rows, err := buildDailyConsumeRows(ctx, q.Aggs, q.Accruals)
	if err != nil {
		writeDailyConsumeExportAudit(c, q.Range, 0, qymodel.ResultFail, err.Error())
		respondDailyConsumeError(c, err)
		return
	}
	sortDailyConsume(rows, c.Query("sort"), c.Query("order"))
	renderDailyConsumeCSV(c, q.Range, rows)
	writeDailyConsumeExportAudit(c, q.Range, len(rows), qymodel.ResultOK, "")
}

// renderDailyConsumeCSV 把结果行写成 CSV 响应体。
//
// 它必须是一个独立的函数:qianye/audit_coverage_guard_test.go 认的是**方法名**,
// 凡是名字叫 Write 的调用都算一次审计写入。csv.Writer.Write 与 gin 的 c.Writer.Write
// 正好都叫这个名字,留在 adminExportDailyConsume 里会让那条 want=3 的下界被
// 十几次 CSV 写白送满 —— 埋点被整段删掉,守卫照样绿。
func renderDailyConsumeCSV(c *gin.Context, r dailyRange, rows []dailyConsumeRow) {
	filename := fmt.Sprintf("qy-daily-consume-%s-%s.csv", r.StartDay, r.EndDay)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	// UTF-8 BOM:没有它 Excel 会把中文用户名和分组名读成乱码,而运营导出
	// 的第一个动作就是用 Excel 打开。
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{
		"user_id", "username", "display_name", "email", "user_group",
		"request_count", "consume_quota", "invite_base_quota",
		"uncounted_quota", "invite_gross", "inviter_id", "inviter_username",
		"account_removed",
	})
	for _, row := range rows {
		// 五个字符串列全部过 csvsafe.Cell:用户名、昵称、邮箱、分组名与上线用户名
		// 都是用户自己填的,而这份文件写了 BOM 就是为了让运营用 Excel 打开它。
		// 数字列不需要 —— strconv 的输出不可能以 = + - @ 开头(负数会,但那是
		// 我们自己算出来的数,不是用户输入)。InviteGross 是 decimal 的字符串形态,
		// 同理由服务端产生,留原样以免在表格里被当成文本而无法求和。
		_ = w.Write([]string{
			strconv.Itoa(row.UserId),
			csvsafe.Cell(row.Username), csvsafe.Cell(row.DisplayName),
			csvsafe.Cell(row.Email), csvsafe.Cell(row.UserGroup),
			strconv.FormatInt(row.RequestCount, 10),
			strconv.FormatInt(row.ConsumeQuota, 10),
			strconv.FormatInt(row.InviteBaseQuota, 10),
			strconv.FormatInt(row.UncountedQuota, 10),
			row.InviteGross,
			strconv.Itoa(row.InviterId), csvsafe.Cell(row.InviterUsername),
			strconv.FormatBool(row.AccountRemoved),
		})
	}
	w.Flush()
}

func writeDailyConsumeExportAudit(c *gin.Context, r dailyRange, rowCount int, result, reason string) {
	snap, _ := common.Marshal(gin.H{
		"start_date": r.StartDay,
		"end_date":   r.EndDay,
		"days":       r.Days,
		"keyword":    c.Query("keyword"),
		"row_count":  rowCount,
	})
	audit.Write(c, audit.Entry{
		Category:    qymodel.AuditCategoryInvite,
		Action:      "invite.daily_consume.export",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		AfterSnap:   string(snap),
	})
}

var dailyConsumeErrCodes = map[error]string{
	errDailyRangeFormat: "qy_daily_range_format",
	errDailyRangeOrder:  "qy_daily_range_order",
	errDailyRangeTooBig: "qy_daily_range_too_big",
	errDailyTooManyRows: "qy_daily_too_many_rows",
	errDailyKeywordWide: "qy_daily_keyword_too_wide",
}

func respondDailyConsumeError(c *gin.Context, err error) {
	if code, ok := dailyConsumeErrCodes[err]; ok {
		badRequest(c, code, err.Error())
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		respondFail(c, http.StatusGatewayTimeout, "qy_daily_query_timeout",
			"消费明细查询超时,请缩小日期区间;若持续发生请检查 logs 表上的 "+logsDailyConsumeIndex+" 索引是否存在")
		return
	}
	internalError(c, err)
}
