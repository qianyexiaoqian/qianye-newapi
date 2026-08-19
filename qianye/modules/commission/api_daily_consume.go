package commission

// api_daily_consume.go —— 「昨天哪个用户消费了多少」。
//
// # 数据源:主库 logs,不是计佣表
//
// qy_commission_accrual 看着现成 —— 它天生就有 (bucket_date, invitee_id,
// base_quota) 三列,一条 GROUP BY 就出报表,而且住在扩展库里,不碰主库。
// 但它**结构性地**答不了这个问题:它是「谁产生了佣金」,不是「谁消费了多少」。
//
// accrueConsume 里每一条 return 都是一次"这笔消费在计佣表上不存在":
//
//	e.InviterId == 0            没有邀请人 —— 全站绝大多数账号
//	e.InviterId == ev.InviteeId 自我邀请
//	blockedInvitees[...]        关系被管理员拉黑
//	MinInviteeAgeHours          绑定还没成熟
//	gross.IsZero()              分组费率 0% —— 照常扣钱,一行都不落
//	hardExcluded(params)        违规扣费、渠道测试
//	ExcludeSubscriptionConsume  订阅额度消费
//
// 这七条不是理论。备份库 2026-08-17 这**一天**里同时踩中了其中四条:
//
//	9900106  logs 1125   计佣表无行   分组 qy-rt-gd 费率 0%
//	9900101  logs 2250   计佣表无行   token_name=模型测试 / channel_test:true
//	9900104  logs 31750  计佣表 6750  差的 25000 是一笔违规扣费(qy_violation_rec_no)
//	全站     logs 602 人在消费,users 里只有 385 条邀请绑定
//
// 也就是说,只读计佣表的报表会让运营看不见 0% 分组的客户、看不见被罚过款的
// 客户、看不见全站六成以上根本没有上线的客户。而运营打开这张表恰恰是为了
// "谁在花钱",不是"谁在给谁赚佣金"。所以口径只能是主库 logs 的 type=2。
//
// # 两个数并排,而不是二选一
//
// 计佣基数照样给,放在消费额旁边,并给出 gap = 消费额 − 计佣基数。
// 运营看到两个数不一样的时候必须能当场知道为什么,不能让他自己猜 ——
// 前端把上面那七条原因写在列头的说明里,行上再给 has_commission / gap
// 两个可读的标记(见 web/.../admin-daily-consume)。
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
// 38 万行要逐行回表(Using MRR),而区间一放大优化器干脆放弃索引改走全表。
// 新索引 (type, created_at, user_id, quota) 让这条聚合变成纯覆盖扫描
// (EXPLAIN 的 Extra 从 `Using index condition; Using MRR` 变成 `Using index`)。
//
// **Select 的列集合是有承重作用的**:实测多取一个 prompt_tokens,覆盖立刻失效,
// 同一天的查询从 418 ms 退回 5020 ms。所以这里只取 user_id / COUNT(*) /
// SUM(quota) 三样,想加列之前先把索引一起加宽,否则就是把慢查询悄悄放回来。
//
// 索引本身对写入的代价很小:logs 上已经有 13 个二级索引,而 (type, created_at)
// 这个前缀随时间单调递增,新行永远落在 B+ 树最右边的少数几个叶页上,
// 不像 request_id / ip 那几个是随机插入。
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
	"github.com/QuantumNous/new-api/qianye/db"
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
	// 消费报表,而运营会拿它去对账。与 maxCandidateUsers 同一个理由、同一个数。
	maxDailyConsumeRows = 20000

	// maxDailyConsumeKeywordHits 是按用户名搜索时先在主库解析出的用户数上界。
	// 关键词太泛(比如一个 "a")时宁可让运营再打几个字,也不要把 IN 列表撑爆。
	maxDailyConsumeKeywordHits = 2000

	// dailyConsumeQueryTimeout 是聚合查询的截止时间。
	//
	// 它是"索引没了"这件事的兜底:没有它,一条 7 天的查询会在主库上跑 9 分钟,
	// 而运营只会不停地刷新页面,把 9 分钟乘以刷新次数。
	dailyConsumeQueryTimeout = 20 * time.Second

	// logsDailyConsumeIndex 是本报表依赖的覆盖索引名。
	logsDailyConsumeIndex = "idx_qy_logs_daily_consume"
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
// 日界完全走 dayline.go(dayKeyStart / dayStart),不自己算 —— 那是返佣模块
// 唯一的"一天"定义,受 commission.day_offset_minutes 管辖。报表与结算、
// 与 accrual.bucket_date 必须是同一个日界:差一个小时,"昨日消费"里就会
// 混进今天凌晨的单子,而运营拿它对的是昨天的账。
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

// commissionAgg 是计佣表侧同一区间的汇总。
type commissionAgg struct {
	InviteeId int             `gorm:"column:invitee_id"`
	BaseQuota int64           `gorm:"column:base_quota"`
	Gross     decimal.Decimal `gorm:"column:gross"`
}

// aggregateCommissionByInvitee 按下线汇总区间内的消费计佣。
//
// 用 bucket_date 而不是 created_at 做区间:bucket_date 就是这笔计佣归属的
// 那一天,而 created_at 是它**第一次**被写进库的时刻 —— 日聚合行会被同一天里
// 后续的消费反复累加,created_at 永远停在当天第一笔,拿它筛区间会把整行
// 归到错误的日期上。两者的日界都来自 dayline.go,所以与 logs 侧对得上。
func aggregateCommissionByInvitee(r dailyRange, inviterId int) (map[int]commissionAgg, error) {
	q := db.Get().Model(&Accrual{}).
		Select("invitee_id, COALESCE(SUM(base_quota),0) AS base_quota, COALESCE(SUM(gross_amount),0) AS gross").
		Where("source_type = ?", SourceConsume).
		Where("bucket_date >= ? AND bucket_date <= ?", r.StartDay, r.EndDay).
		Where("status <> ?", StatusVoided)
	if inviterId > 0 {
		q = q.Where("inviter_id = ?", inviterId)
	}
	var rows []commissionAgg
	if err := q.Group("invitee_id").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	out := make(map[int]commissionAgg, len(rows))
	for _, row := range rows {
		out[row.InviteeId] = row
	}
	return out, nil
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
	// CommissionBaseQuota 是同一区间进了计佣表的基数。它天然 <= ConsumeQuota。
	CommissionBaseQuota int64 `json:"commission_base_quota"`
	// UncountedQuota = ConsumeQuota − CommissionBaseQuota,即"消费了但没计佣"的部分。
	// 单列出来是因为运营的下一个问题永远是"那少的那块去哪了"。
	UncountedQuota  int64  `json:"uncounted_quota"`
	CommissionGross string `json:"commission_gross"`
	HasCommission   bool   `json:"has_commission"`
	InviterId       int    `json:"inviter_id"`
	InviterUsername string `json:"inviter_username"`
	// AccountRemoved 表示这一行的账号在 users 里已经不在了(软删或被硬删)。
	// 它的 uncounted_quota 恒等于全额消费额,而运营按那七条原因去查会一条都
	// 对不上 —— 必须由这张表自己说出第八条:账号已被删除。
	AccountRemoved bool `json:"account_removed"`
}

var dailyConsumeSorts = map[string]func(a, b dailyConsumeRow) bool{
	"consume_quota":         func(a, b dailyConsumeRow) bool { return a.ConsumeQuota < b.ConsumeQuota },
	"request_count":         func(a, b dailyConsumeRow) bool { return a.RequestCount < b.RequestCount },
	"commission_base_quota": func(a, b dailyConsumeRow) bool { return a.CommissionBaseQuota < b.CommissionBaseQuota },
	"uncounted_quota":       func(a, b dailyConsumeRow) bool { return a.UncountedQuota < b.UncountedQuota },
	"user_id":               func(a, b dailyConsumeRow) bool { return a.UserId < b.UserId },
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
		like := "%" + keyword + "%"
		q = q.Where("username LIKE ? OR display_name LIKE ? OR email LIKE ?", like, like, like)
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
			// 数字全对,缺的是"这个账号已经被删了"这条解释 —— 那正是这张表
			// 承诺要当场回答的"两个数为什么不一样"里没被列进去的一条。
			Unscoped().
			// 列名分成多个参数传:GORM 会逐个按当前方言加引号,group 这个
			// 保留字因此在三种库上都是对的,不需要自己拼引号,也不依赖
			// model.QyCommonGroupCol() —— 那个值只有 model.InitDB 跑过才有。
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

// buildDailyConsumeRows 把三份数据(logs 聚合、计佣汇总、主库画像)拼成展示行。
//
// 名字只给**需要展示的那一批**补:管理端分页时是当前页,导出时是全部行。
func buildDailyConsumeRows(ctx context.Context, aggs []consumeAgg,
	commissions map[int]commissionAgg) ([]dailyConsumeRow, error) {

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
		cm, hasCm := commissions[a.UserId]
		uncounted := a.ConsumeQuota - cm.BaseQuota
		if uncounted < 0 {
			// 计佣基数大于消费额只有一种可能:日志被保留期清理掉了一部分,
			// 而计佣行还在。夹到 0 而不是显示负数 —— 负的"未计佣额"没有意义,
			// 真正的信号是 meta 里的 accrual_users_without_logs。
			uncounted = 0
		}
		rows = append(rows, dailyConsumeRow{
			UserId:              a.UserId,
			Username:            p.Username,
			DisplayName:         p.DisplayName,
			Email:               p.Email,
			UserGroup:           p.Group,
			RequestCount:        a.RequestCount,
			ConsumeQuota:        a.ConsumeQuota,
			CommissionBaseQuota: cm.BaseQuota,
			UncountedQuota:      uncounted,
			CommissionGross:     cm.Gross.String(),
			HasCommission:       hasCm,
			InviterId:           p.InviterId,
			InviterUsername:     inviters[p.InviterId].Username,
			AccountRemoved:      !found || p.DeletedAt.Valid,
		})
	}
	return rows, nil
}

// dailyConsumeQuery 是管理端两条路由(列表 / 导出)共用的取数过程。
type dailyConsumeQuery struct {
	Range dailyRange
	Aggs  []consumeAgg
	Comms map[int]commissionAgg
	// AccrualOnly 是"计佣表里有、logs 里没有"的下线数。
	//
	// 正常情况下恒为 0。不为 0 的唯一合理解释是日志保留期把这段消费清掉了,
	// 而计佣行是永久账本 —— 那时这张报表的消费额一侧天然缺一块,必须让运营
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
	comms, err := aggregateCommissionByInvitee(r, 0)
	if err != nil {
		return out, err
	}
	out.Aggs, out.Comms = aggs, comms

	inLogs := make(map[int]bool, len(aggs))
	for _, a := range aggs {
		inLogs[a.UserId] = true
	}
	for inviteeId := range comms {
		if !inLogs[inviteeId] {
			out.AccrualOnly++
		}
	}
	return out, nil
}

// adminListDailyConsume 是管理端的日消费明细。
func adminListDailyConsume(c *gin.Context) {
	q, err := runDailyConsumeQuery(c)
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}
	page, size := httpq.Paginate(c, listPaging)

	// 排序在拼行**之前**做不了(要按计佣基数排),所以先拼一次轻量行:
	// 这一步不补名字,只算数,补名字留给切完页之后。
	light := make([]dailyConsumeRow, 0, len(q.Aggs))
	for _, a := range q.Aggs {
		cm := q.Comms[a.UserId]
		uncounted := a.ConsumeQuota - cm.BaseQuota
		if uncounted < 0 {
			uncounted = 0
		}
		light = append(light, dailyConsumeRow{
			UserId:              a.UserId,
			RequestCount:        a.RequestCount,
			ConsumeQuota:        a.ConsumeQuota,
			CommissionBaseQuota: cm.BaseQuota,
			UncountedQuota:      uncounted,
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
	items, err := buildDailyConsumeRows(ctx, pageAggs, q.Comms)
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}

	var sumConsume, sumBase, sumReq int64
	for _, r := range light {
		sumConsume += r.ConsumeQuota
		sumBase += r.CommissionBaseQuota
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
			"user_count":            total,
			"request_count":         sumReq,
			"consume_quota":         sumConsume,
			"commission_base_quota": sumBase,
			"uncounted_quota":       maxInt64(sumConsume-sumBase, 0),
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

// adminExportDailyConsume 导出 CSV。
//
// # 为什么做导出
//
// 运营对这类表的第一个动作就是"发给财务"。不做的话他会自己翻页复制,
// 复制出来的数是没有区间标注的 —— 而这张表最容易出错的恰恰是区间。
//
// # 为什么它写审计
//
// 导出是**一次性把全站消费额带走**,是这个模块里泄漏面最大的读操作。
// 它不改钱,所以不在 auditRequired 的资金路径里;但"谁在什么时候导走了
// 哪个区间的全站消费明细"必须留痕,否则事后查数据外流时这里是个盲区。
func adminExportDailyConsume(c *gin.Context) {
	q, err := runDailyConsumeQuery(c)
	if err != nil {
		writeDailyConsumeExportAudit(c, dailyRange{}, 0, qymodel.ResultFail, err.Error())
		respondDailyConsumeError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), dailyConsumeQueryTimeout)
	defer cancel()
	rows, err := buildDailyConsumeRows(ctx, q.Aggs, q.Comms)
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
// # 它为什么必须是一个独立的函数
//
// qianye/audit_coverage_guard_test.go 认的是**方法名**:凡是名字叫 Write 的
// 调用都算一次审计写入。csv.Writer.Write 与 gin 的 c.Writer.Write 正好都叫
// 这个名字,留在 adminExportDailyConsume 里会让那条 want=3 的下界被
// 十几次 CSV 写白送满 —— 埋点被整段删掉,守卫照样绿。
//
// 实测过:把导出失败那条埋点删掉,守卫在合并之前是 SURVIVED 的。
// 拆出来之后同一个变异是 KILLED。
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
		"request_count", "consume_quota", "commission_base_quota",
		"uncounted_quota", "commission_gross", "inviter_id", "inviter_username",
		"account_removed",
	})
	for _, row := range rows {
		_ = w.Write([]string{
			strconv.Itoa(row.UserId), row.Username, row.DisplayName, row.Email, row.UserGroup,
			strconv.FormatInt(row.RequestCount, 10),
			strconv.FormatInt(row.ConsumeQuota, 10),
			strconv.FormatInt(row.CommissionBaseQuota, 10),
			strconv.FormatInt(row.UncountedQuota, 10),
			row.CommissionGross,
			strconv.Itoa(row.InviterId), row.InviterUsername,
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
		Category:    qymodel.AuditCategoryCommission,
		Action:      "commission.daily_consume.export",
		ActorType:   qymodel.ActorAdmin,
		ActorUserId: c.GetInt("id"),
		ActorName:   c.GetString("username"),
		Result:      result,
		Reason:      reason,
		AfterSnap:   string(snap),
	})
}

// listMyInviteeDailyConsume 是**邀请人**看自己名下下线的区间消费。
//
// # 它的口径与管理端刻意不同,这不是偷懒
//
// 管理端那张表的消费额来自主库 logs,是"真实扣掉了多少"。这一条**只读计佣表**,
// 给的是 base_quota —— 也就是佣金基数。差别在于 logs 里那部分没能计佣的消费:
//
//	违规扣费     下线被罚了多少款,是纪律信息,不是拉他进来的人该知道的
//	渠道测试     根本不是这个人的消费
//	0% 分组      运营给这个下线单独定的商务价,泄漏的是平台的定价策略
//	订阅额度消费 站点配置排除掉的口径,给出来会与他看到的佣金对不上
//	拉黑期间     "你这个下线被我们拉黑了"不该由这张表说出口
//
// 也就是说,把 logs 侧的消费额下发给上线,每一项都是**新增**的泄漏,
// 而其中一项(违规扣费)直接暴露了下线的处罚记录。
//
// 反过来,base_quota 不是新增泄漏:它早就在 /api/qy/commission/records
// 里逐笔下发了(那里一行 accrual 就是一个下线一天的计佣,带 bucket_date)。
// 佣金基数本来就是"我凭什么拿到这笔钱"的凭据,不给等于让人无法核对自己的收入。
//
// 所以这条接口新增的东西只有一个:**区间**。/commission/invitees 给的是
// 开天辟地以来的累计,回答不了"我上周推的那批人这周还在用吗"。
//
// 人名一律走 InviteRelation 里预先算好的脱敏名与 invitee_ref,
// 与既有两条用户端列表完全一致,真实用户名/邮箱/user_id 一个都不下发。
func listMyInviteeDailyConsume(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	inviterId := c.GetInt("id")
	r, err := parseDailyRange(c, common.GetTimestamp())
	if err != nil {
		respondDailyConsumeError(c, err)
		return
	}
	comms, err := aggregateCommissionByInvitee(r, inviterId)
	if err != nil {
		internalError(c, err)
		return
	}

	type row struct {
		inviteeId int
		agg       commissionAgg
	}
	all := make([]row, 0, len(comms))
	for id, agg := range comms {
		all = append(all, row{inviteeId: id, agg: agg})
	}
	// 按基数降序:上线打开这一页想知道的是"这段时间谁在给我贡献"。
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].agg.BaseQuota != all[j].agg.BaseQuota {
			return all[i].agg.BaseQuota > all[j].agg.BaseQuota
		}
		return all[i].inviteeId < all[j].inviteeId
	})

	page, size := httpq.Paginate(c, listPaging)
	total := len(all)
	pageRows := httpq.Slice(all, page, size)

	ids := make([]int, 0, len(pageRows))
	for _, r := range pageRows {
		ids = append(ids, r.inviteeId)
	}
	rels := map[int]InviteRelation{}
	if len(ids) > 0 {
		var found []InviteRelation
		if err := db.Get().Where("inviter_id = ? AND invitee_id IN ?", inviterId, ids).
			Find(&found).Error; err != nil {
			db.MarkFailure(err)
			internalError(c, err)
			return
		}
		for _, rel := range found {
			rels[rel.InviteeId] = rel
		}
	}

	var sumBase int64
	sumGross := decimal.Zero
	for _, r := range all {
		sumBase += r.agg.BaseQuota
		sumGross = sumGross.Add(r.agg.Gross)
	}

	items := make([]gin.H, 0, len(pageRows))
	for _, r := range pageRows {
		rel := rels[r.inviteeId]
		items = append(items, gin.H{
			"ref":         rel.InviteeRef,
			"masked_name": rel.MaskedName,
			"base_quota":  r.agg.BaseQuota,
			"commission":  r.agg.Gross.String(),
			"blocked":     rel.Blocked,
		})
	}
	respond(c, gin.H{
		"items":     items,
		"total":     total,
		"p":         page,
		"page_size": size,
		"range": gin.H{
			"start_date": r.StartDay,
			"end_date":   r.EndDay,
			"days":       r.Days,
			"max_days":   maxDailyConsumeDays,
		},
		"summary": gin.H{
			"invitee_count": total,
			"base_quota":    sumBase,
			"commission":    sumGross.String(),
		},
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
