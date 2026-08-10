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
	Reason       string `json:"reason"`
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

	windowHours := policy.WindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	// 窗口已经滚过就不该再展示旧计数,否则用户看到的"剩余次数"是错的。
	hit := counter.HitCount
	if counter.WindowStart < common.GetTimestamp()-int64(windowHours)*3600 {
		hit = 0
	}
	remaining := 0
	if policy.Threshold > 0 {
		remaining = policy.Threshold - hit
		if remaining < 0 {
			remaining = 0
		}
	}

	var feeTotal int64
	db.Get().Model(&Record{}).
		Where("user_id = ? AND shadow = ? AND status = ?", userId, false, RecordActive).
		Select("COALESCE(SUM(fee_quota),0)").Scan(&feeTotal)

	respond(c, gin.H{
		"hit_count":     hit,
		"window_hours":  windowHours,
		"ban_threshold": policy.Threshold,
		"remaining":     remaining,
		// 达到阈值之后会发生什么必须一并告知:同一个"还剩 2 次"在
		// 「仅记录」档下不会有任何后果,在「封号」档下是账号被限制。
		// 只给数字不给动作,用户无从判断该不该紧张。
		"policy_action":   policy.Action,
		"total_fee_quota": feeTotal,
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
