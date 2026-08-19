package violation

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// userRecordView 是给用户看的违规记录,由服务端白名单构造。
//
// 刻意不复用 Record 加 `json:"-"`:那是负面清单,新增字段会默认泄露。
// 白名单是正面清单,新增字段默认不泄露 —— 对一张同时存着命中词、命中片段、
// IP 与内部规则名的表来说,这个差别就是"会不会把整套规则库送给刷子"。
//
// 明确不返回:matched_terms / match_snippet / rule_name(内部名)/ rule_id /
// ip / channel_id / group_ratio / 归档上下文。
type userRecordView struct {
	Id        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	ModelName string `json:"model_name"`
	// Reason 用规则的对外文案,不是内部规则名。
	Reason string `json:"reason"`
	// Category 用记录里冻结的**公示标题**(Record.CategoryPublicTitle),
	// 绝不是 CategoryName —— 后者是内部名,常含代号。冻结列而不是现查类型表:
	// 类型被归档或改名之后,这一条记录仍要显示当时那个名字。
	// 该类型当时未公示(公示标题为空)时给空串,前端按"未公示"处理。
	Category     string `json:"category"`
	Blocked      bool   `json:"blocked"`
	FeeQuota     int64  `json:"fee_quota"`
	FeeStatus    string `json:"fee_status"`
	Status       string `json:"status"`
	CounterAfter int    `json:"counter_after"`
}

func toUserView(r *Record) userRecordView {
	reason := r.PublicReason
	if reason == "" {
		reason = "内容违反使用策略"
	}
	return userRecordView{
		Id:           r.Id,
		CreatedAt:    r.CreatedAt,
		ModelName:    r.ModelName,
		Reason:       reason,
		Category:     r.CategoryPublicTitle,
		Blocked:      r.Blocked,
		FeeQuota:     r.FeeQuota,
		FeeStatus:    r.FeeStatus,
		Status:       r.Status,
		CounterAfter: r.CounterAfter,
	}
}

// userListRecords 返回当前用户自己的违规记录。
//
// 为什么要给用户看:钱被扣了必须给理由,否则就是黑箱扣费,只会换来工单与差评。
// 影子模式下的记录同样不展示 —— 那些是"如果当时真扣会怎样",用户并没有被扣钱,
// 展示出来只会制造恐慌与无效申诉。
func userListRecords(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		badRequest(c, "无法识别当前用户")
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	q := db.Get().Model(&Record{}).Where("user_id = ? AND shadow = ?", userId, false)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		internalError(c, err)
		return
	}
	var rows []Record
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}
	items := make([]userRecordView, 0, len(rows))
	for i := range rows {
		items = append(items, toUserView(&rows[i]))
	}
	respond(c, gin.H{"items": items, "total": total})
}

// userSummary 告诉用户"当前窗口违规几次、还差几次会被封号"。
//
// 威慑价值大于泄露价值:知道"再违规 2 次就封号"的用户会主动收敛,
// 不知道的只会在被封之后来发工单。
func userSummary(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		badRequest(c, "无法识别当前用户")
		return
	}
	// 阈值与窗口按**这个用户所在分组**的策略档取。取全局值的话,配了专属档的
	// 分组会看到一组与自己无关的数字 —— 而这个接口的全部目的就是让用户知道
	// "我还剩几次",给错的数比不给更糟。
	policy := resolveBanPolicy(c.GetString("group"))

	var counter Counter
	_ = db.Get().Where("user_id = ?", userId).Take(&counter).Error

	// windowHours 可能是 WindowUnlimited(-1)。它原样下发给前端 ——
	// 前端据此把"24 小时内累计"换成"累计",而不是显示一个 -1 小时的窗口。
	windowHours := effectiveWindowHours(policy.WindowHours)
	// 窗口已经滚过就不该再展示旧计数,否则用户看到的"剩余次数"是错的。
	// 无限窗口下这个下界是负数,判据恒为假,计数一直展示。
	now := common.GetTimestamp()
	hit := counter.HitCount
	if counter.WindowStart < windowFloor(now, windowHours) {
		hit = 0
	}
	// "还差几次"必须跨**两条线**取最小值,不能只算账号总量线。
	// 处置由 anyReached 的 OR 判定触发,最先到达的那条线说了算;只按账号线算,
	// 在"账号线 10、某一类 3"这种普通配置下会告诉用户"还剩 8 次",
	// 而他下一次命中就被封了。给反向的数字比不给数字更糟。
	cats, catHits := userCategoryLines(userId, now)
	nearest := nearestThresholdLine(policy.Threshold, hit, windowHours, cats, catHits)

	var feeTotal int64
	db.Get().Model(&Record{}).
		Where("user_id = ? AND shadow = ? AND status = ?", userId, false, RecordActive).
		Select("COALESCE(SUM(fee_quota),0)").Scan(&feeTotal)

	respond(c, gin.H{
		"hit_count": hit,
		// window_hours / ban_threshold 描述的仍然是**账号总量线**那一条。
		// 上面那个 "N / M" 统计块问的就是这条线,换成最近那条线会让同一个
		// hit_count 配上另一条线的分母。
		"window_hours":  windowHours,
		"ban_threshold": policy.Threshold,
		// remaining 是**两条线里最近的那一条**还差几次,只在 remaining_line
		// 不是 none 时有意义。前端不要再用 ban_threshold > 0 判断要不要显示它:
		// 账号线关着、某一类开着 3 次,是完全合法的配置。
		"remaining":      nearest.Remaining,
		"remaining_line": nearest.Line,
		// 最近那条线**自己的**三个数。缺了它们,界面上就会出现「触发线:类型」
		// 配「阈值 0、窗口 24 小时」这种自相矛盾的一句话 —— 那两个数描述的是
		// 账号总量线,而把人封掉的是另一条(实测:类型线阈值 2、不限期限)。
		// 用户没有任何办法从这一份响应里看出数字对不上。
		"remaining_threshold":    nearest.Threshold,
		"remaining_window_hours": nearest.WindowHours,
		"remaining_hit_count":    nearest.HitCount,
		// 达到阈值之后会发生什么必须一并告知:同一个"还剩 2 次"在
		// 「仅记录」档下不会有任何后果,在「封号」档下是账号被限制。
		// 只给数字不给动作,用户无从判断该不该紧张。
		"policy_action": policy.Action,
		// banned 让前端能停掉倒计时。已经被封的人看到"距离封号还剩 7"
		// 是这一页最糟的一种错:他会以为自己还能用,而实际每一次调用都是 403。
		"banned":          violationBanned(userId, counter.BanCycle),
		"total_fee_quota": feeTotal,
	})
}

// userCategoryLines 读出当前对这个用户**生效**的全部单类型线,以及各自窗口内的计数。
//
// 入选条件与 categoryReached 逐字同构(Enabled 且 Threshold > 0),
// 刻意**不看 published**:published 只决定这一类出不出现在公示列表里,
// 不决定它计不计数、触不触发。按 published 过滤会让"还差几次"在观察期类型上
// 重新失真,而那正是这个函数存在的原因。
//
// 读失败返回空:倒计时算不出来时退回"看不出最近的线"是安全方向,
// 一次扩展库抖动不该让整页打不开。
func userCategoryLines(userId int, now int64) ([]Category, map[int64]int) {
	gdb := db.Get()
	if gdb == nil {
		return nil, nil
	}
	var cats []Category
	if err := gdb.Where("enabled = ? AND threshold > ?", true, 0).Find(&cats).Error; err != nil {
		return nil, nil
	}
	if len(cats) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(cats))
	for _, cat := range cats {
		ids = append(ids, cat.Id)
	}
	var counters []CategoryCounter
	if err := gdb.Where("user_id = ? AND category_id IN ?", userId, ids).Find(&counters).Error; err != nil {
		return cats, nil
	}
	hits := make(map[int64]int, len(counters))
	byId := make(map[int64]Category, len(cats))
	for _, cat := range cats {
		byId[cat.Id] = cat
	}
	for _, ct := range counters {
		cat, ok := byId[ct.CategoryId]
		if !ok {
			continue
		}
		// 窗口已经滚过就不算(与 toUserCategoryView / userSummary 同口径)。
		// 无限窗口的下界是负数,这一条永远不会跳过。
		if ct.WindowStart < windowFloor(now, cat.WindowHours) {
			continue
		}
		hits[ct.CategoryId] = ct.HitCount
	}
	return cats, hits
}

// violationBanned 回答"这个用户此刻是不是正被违规系统封着"。
//
// 判据是**当前封禁周期**上有一行 banned:BanCycle 在每次解封时 +1,
// 所以旧周期的封禁行不会把一个已经解封的人继续说成被封。
// 查不到或读失败一律按未封禁 —— 把"没被封"说成"被封"会让人白白去发工单,
// 而反方向的错(把被封说成没被封)由 remaining 那一路兜住。
func violationBanned(userId, banCycle int) bool {
	gdb := db.Get()
	if gdb == nil {
		return false
	}
	var count int64
	if err := gdb.Model(&Ban{}).
		Where("user_id = ? AND ban_cycle = ? AND status = ?", userId, banCycle, BanBanned).
		Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

// userCategoryView 是**对用户公示**的违规类型,由服务端白名单构造。
//
// # 这个结构体的字段清单就是隔离本身
//
// Category 上有两组文案:内部的 Name / Remark(写匹配细节、误杀场景、运营口径)
// 与对外的 PublicTitle / PublicDesc。这里**只有后者**,而且是正面清单 ——
// 复用 Category 加 `json:"-"` 是负面清单,下一次给 Category 加一列内部字段时
// 它会默认泄露出去,而泄露的内容恰好是"怎么绕过这条线"。
//
// 明确不返回:Name(内部名,常含代号)、Remark(内部说明,写的就是匹配细节)、
// Key(外部审核来源绑定用的标识)、Enabled / SortOrder / 时间戳 / 操作人。
type userCategoryView struct {
	Id int64 `json:"id"`
	// Title / Description 一律取 PublicTitle / PublicDesc。它们为空时这一类
	// 根本进不了公示列表(validateCategory 要求勾了公示就必须填标题)。
	Title       string `json:"title"`
	Description string `json:"description"`
	// Threshold 是这一类累计多少次会触发处置,0 表示这一类不单独触发。
	Threshold int `json:"threshold"`
	// WindowHours 是这一类的统计窗口。取 WindowUnlimited(-1)表示**不限期限**,
	// 前端必须据此改口径("累计"而不是"N 小时内累计")。它不是一个可以参与算术
	// 的小时数,前端拿它做除法或格式化成时长都会得到一个负数时间。
	WindowHours int `json:"window_hours"`
	HitCount    int `json:"hit_count"`
	Remaining   int `json:"remaining"`
}

// toUserCategoryView 把一个违规类型 + 该用户在这一类上的计数,折成对外视图。
//
// 提成函数不是为了缩短 handler:**内部说明与公示文案的隔离**就落在这一个函数上,
// 而它必须能被直接测到(TestUserCategoryViewHidesInternalText 会给 Name / Remark
// 塞进独一无二的串,再断言序列化结果里一个字都没有)。留在 handler 里的话,
// 验证它就需要一整套 gin + 数据库夹具,而那种测试没人愿意在加字段时补。
func toUserCategoryView(cat Category, ct CategoryCounter, now int64) userCategoryView {
	// WindowHours 原样下发 WindowUnlimited(-1),由前端换成"累计"的说法。
	// 折成一个具体小时数会在公示页写下一个错的时间口径,而这一页的读者正在
	// 用它判断"我还剩几次" —— 说成"24 小时内累计"会让他以为等一天就清零。
	windowHours := effectiveWindowHours(cat.WindowHours)
	// 窗口已经滚过就不展示旧计数,否则用户看到的"剩余次数"是错的(与 userSummary 同口径)。
	hit := 0
	if ct.CategoryId == cat.Id && ct.WindowStart >= windowFloor(now, windowHours) {
		hit = ct.HitCount
	}
	// Enabled=false 的类型对用户而言就是"这一类不单独触发",阈值按 0 展示。
	// 展示库里那个正数会让用户以为还有一条线在,而它当下并不生效。
	threshold := 0
	if cat.Enabled {
		threshold = cat.Threshold
	}
	remaining := 0
	if threshold > 0 {
		remaining = threshold - hit
		if remaining < 0 {
			remaining = 0
		}
	}
	return userCategoryView{
		Id: cat.Id,
		// 只取对外两列。**绝不**在这里回落到 cat.Name:那是内部名,常含代号,
		// 而"公示标题没填"已经被 validateCategory 挡在写入口(勾了公示就必须填)。
		// 加一条回落等于给那道校验开一个后门,而后门的出口是用户的浏览器。
		Title:       cat.PublicTitle,
		Description: cat.PublicDesc,
		Threshold:   threshold,
		WindowHours: windowHours,
		HitCount:    hit,
		Remaining:   remaining,
	}
}

// userListCategories 是"违规类型公示"接口:有哪些类型、各自多少次会被处置、
// 以及**自己当前每一类的计数**。
//
// # 为什么要公示
//
// 项目方原话:「这些在用户前端要公示出来」。理由与 userSummary 一致 ——
// 威慑价值大于泄露价值:知道"这一类再犯 1 次就会被处置"的用户会主动收敛,
// 不知道的只会在被处置之后来发工单。公示的是**后果**,不是**判据**:
// 后者写在 Remark 里,永远不出现在这个接口的响应里。
//
// # 两条线都要给
//
// 用户会撞的线有两条:账号总量线(跨全部类型,来自所在分组的策略档)与单类型线。
// 只公示其中一条会让"到底几次"这个问题在另一条线上失真 —— 而失真的方向是
// "我以为还剩 5 次,结果第 3 次就被限制了"。所以 account_* 与 items 一起返回。
func userListCategories(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		badRequest(c, "无法识别当前用户")
		return
	}
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	// published = true 是这张列表的唯一入选条件。未公示的类型照常计数、照常封号,
	// 只是不在这里出现 —— 那是"仍在观察期的新类型"的用法(见 Category.Published)。
	var rows []Category
	if err := gdb.Where("published = ?", true).
		Order("sort_order asc, id asc").Find(&rows).Error; err != nil {
		internalError(c, err)
		return
	}

	counters := make(map[int64]CategoryCounter, len(rows))
	if len(rows) > 0 {
		ids := make([]int64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.Id)
		}
		var cs []CategoryCounter
		// 计数拿不到不算失败:公示的主体是"有哪些类型、几次会怎样",
		// 计数为 0 的列表仍然有价值,而一次扩展库抖动不该让整页打不开。
		if err := gdb.Where("user_id = ? AND category_id IN ?", userId, ids).Find(&cs).Error; err == nil {
			for _, v := range cs {
				counters[v.CategoryId] = v
			}
		}
	}

	now := common.GetTimestamp()
	items := make([]userCategoryView, 0, len(rows))
	for _, r := range rows {
		items = append(items, toUserCategoryView(r, counters[r.Id], now))
	}

	policy := resolveBanPolicy(c.GetString("group"))
	accountWindow := effectiveWindowHours(policy.WindowHours)
	var counter Counter
	_ = db.Get().Where("user_id = ?", userId).Take(&counter).Error
	accountHit := counter.HitCount
	if counter.WindowStart < windowFloor(now, accountWindow) {
		accountHit = 0
	}

	respond(c, gin.H{
		"items": items,
		// 账号总量线:跨全部类型的那条线。与 my-summary 是同一组数字,
		// 这里一并返回是为了让"公示"这一页自己就能把两条线讲清楚,
		// 而不是要求前端拼两个接口的结果 —— 拼错的方向是少显示一条线。
		"account_threshold":    policy.Threshold,
		"account_hit_count":    accountHit,
		"account_window_hours": accountWindow,
		"policy_action":        policy.Action,
		// banned 让公示卡片能在"已达门槛"与"已经被处置"之间分开说话。
		// 达到门槛的那一刻账号就已经被封了(anyReached 用的是"已达"而不是
		// "恰好跨越"),此时再写一句"下一次违规就会被封"是一句已经过期的预告 ——
		// 而这个用户此刻最需要的是知道自己已经被封、该去申诉。
		"banned": violationBanned(userId, counter.BanCycle),
		// 两条线是"任一越过即触发"。这句口径由后端下发,前端不要自己写死:
		// 判定侧改了口径而文案没改,用户看到的就是一句谎话。
		"threshold_semantics": thresholdSemanticsAnyLine,
	})
}

// minAppealReasonRunes 是申诉理由的最短长度。
// 太短的理由("误判")对复核没有任何帮助,只会把人工队列堵死。
const minAppealReasonRunes = 20

func userCreateAppeal(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagViolation) {
		return
	}
	userId := c.GetInt("id")
	if userId <= 0 {
		badRequest(c, "无法识别当前用户")
		return
	}
	var req struct {
		RecordId int64  `json:"record_id"`
		Reason   string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "请求体格式错误")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < minAppealReasonRunes {
		badRequest(c, "请填写至少 20 个字的申诉理由")
		return
	}

	var rec Record
	// 必须带 user_id 条件:只凭 record_id 查会让任何人枚举出他人的违规记录。
	if err := db.Get().Where("id = ? AND user_id = ?", req.RecordId, userId).Take(&rec).Error; err != nil {
		notFound(c)
		return
	}
	if rec.Status != RecordActive {
		badRequest(c, "该记录已处理,无需申诉")
		return
	}

	now := common.GetTimestamp()
	row := &Appeal{
		UserId:    userId,
		RecordId:  rec.Id,
		Reason:    truncate(reason, 2000),
		Status:    AppealPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// record_id 唯一索引:一条记录只允许一次申诉,冲突时静默视为已提交。
	res := db.Get().Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		internalError(c, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		badRequest(c, "该记录已提交过申诉,请等待处理")
		return
	}
	_ = db.Get().Model(&Record{}).Where("id = ? AND status = ?", rec.Id, RecordActive).
		Update("status", RecordAppealed).Error

	// 申诉提交要与裁决成对留痕。只记裁决那一半,时间线上就少了"用户什么时候
	// 提的、提的是哪条记录、扣了多少" —— 而争议里被质疑的往往正是这一段。
	// TraceNo 与 appeals.review 取同一个形状,两条记录能被串起来。
	audit.Write(c, audit.Entry{
		TraceNo:      fmt.Sprintf("appeal-%d", row.Id),
		Category:     qymodel.AuditCategoryViolation,
		Action:       "appeals.create",
		ActorType:    qymodel.ActorUser,
		ActorUserId:  userId,
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		AmountQuota:  rec.FeeQuota,
		Result:       qymodel.ResultPending,
		Reason:       truncate("提交违规申诉: "+reason, 500),
		AfterSnap: fmt.Sprintf(`{"appeal_id":%d,"record_id":%d,"rule_name":%q,"fee_quota":%d}`,
			row.Id, rec.Id, rec.RuleName, rec.FeeQuota),
	})
	respond(c, gin.H{"id": row.Id})
}
