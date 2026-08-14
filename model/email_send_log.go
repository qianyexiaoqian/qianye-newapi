package model

// email_send_log.go —— SMTP 发件台账与按账号的发送量统计。
//
// ═══════════════════════════ 它回答两个问题 ═══════════════════════════
//
//	「这个人为什么没收到验证码」   → 按收件人查台账,失败记录带原始错误
//	「哪个账号快发到上限了」       → 按账号 × 时间窗聚合
//
// 第二个问题正是多账号这个功能存在的理由:单个发件邮箱一小时内发太多会被收件方
// 判成垃圾,而 SMTP 依旧返回成功 —— 不落台账就完全看不见。
//
// ═══════════════════════════ 统计为什么从台账现算 ═══════════════════════════
//
// 另一条路是在账号上挂一个计数器字段。不选它,因为账号表存在 options 里,
// 那是**配置**:把一个每封邮件都要自增的计数器写进配置行,既要处理并发写,
// 又会让「管理员打开设置页」和「后台正在发邮件」抢同一行。
//
// 台账现算没有这些问题,而且天然与台账一致 —— 不会出现「计数说发了 100 封、
// 台账里只有 60 条」这种谁都解释不清的状态。发件是低频动作(验证码、通知),
// 一次带索引的 COUNT 完全承受得起。

import (
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// EmailSendLog 是一次发件尝试的台账。成功与失败都记。
type EmailSendLog struct {
	Id int `json:"id" gorm:"primaryKey;autoIncrement"`
	// AccountId 是 common.SMTPAccountConfig.ID。
	// 刻意存字符串而不是外键:账号表在 options 里,删掉一个账号之后
	// 历史台账仍然要能说出"当时是哪个号发的"。
	AccountId   string `json:"account_id" gorm:"type:varchar(64);index:idx_email_log_account_time,priority:1"`
	AccountName string `json:"account_name" gorm:"type:varchar(128)"`
	FromAddr    string `json:"from_addr" gorm:"type:varchar(255)"`
	Receiver    string `json:"receiver" gorm:"type:varchar(255);index"`
	Subject     string `json:"subject" gorm:"type:varchar(512)"`
	Success     bool   `json:"success" gorm:"index"`
	// ErrorMsg 只在失败时非空。截断到 1000 字符 —— 上游 SMTP 的错误里
	// 偶尔会带整段服务器响应,不截断会让一次群发把台账表撑大。
	ErrorMsg   string `json:"error_msg" gorm:"type:varchar(1000)"`
	DurationMs int64  `json:"duration_ms"`
	// CreatedAt 是 unix 秒。与 Log 表同口径,避免两张表的时间字段类型不同,
	// 跨库(SQLite/MySQL/PostgreSQL)的 DATETIME 语义差异也一并绕开。
	CreatedAt int64 `json:"created_at" gorm:"bigint;index:idx_email_log_account_time,priority:2"`
}

const emailSendLogErrorMaxLen = 1000

// init 把台账与用量统计接到 common 包的两个 hook 上。
//
// 在 init 里赋值是安全的:两个实现体自己判 `DB == nil` 并退化成
// 「不记录 / 用量为 0」,因此即使在 InitDB 之前就有人发邮件(不会发生,
// 但不该靠"不会发生"来保证不 panic),也只是这一封没有台账。
//
// 走 hook 而不是让 common 直接调 model:model import common,反向依赖会成环。
func init() {
	common.SMTPRecordSend = RecordEmailSend
	common.SMTPHourlyUsage = EmailHourlySendCount
}

// RecordEmailSend 落一条发件台账。
//
// **吞掉一切错误。** 它跑在发件的返回路径上,台账写不进去是运维问题,
// 而让一封已经发出去的验证码因为记日志失败而被判成发送失败,是用户问题。
func RecordEmailSend(rec common.SMTPSendRecord) {
	if DB == nil {
		return
	}
	errMsg := rec.ErrorMsg
	if len(errMsg) > emailSendLogErrorMaxLen {
		errMsg = errMsg[:emailSendLogErrorMaxLen]
	}
	entry := &EmailSendLog{
		AccountId:   rec.AccountID,
		AccountName: rec.AccountName,
		FromAddr:    rec.From,
		Receiver:    rec.Receiver,
		Subject:     rec.Subject,
		Success:     rec.Success,
		ErrorMsg:    errMsg,
		DurationMs:  rec.DurationMs,
		CreatedAt:   common.GetTimestamp(),
	}
	if err := DB.Create(entry).Error; err != nil {
		common.SysError("failed to record email send log: " + err.Error())
	}
}

// EmailHourlySendCount 返回某个账号最近一小时**成功发出**的封数。
//
// 只数成功的:小时上限是为了避开收件方的风控,而失败的那些根本没有到达对方,
// 把它们算进去会让一个正在故障的账号越算越"忙",从而永远被跳过 ——
// 那是与意图相反的行为(故障应当被看见,而不是被静默绕开)。
func EmailHourlySendCount(accountId string) int {
	if DB == nil || accountId == "" {
		return 0
	}
	since := common.GetTimestamp() - int64(time.Hour/time.Second)
	var count int64
	err := DB.Model(&EmailSendLog{}).
		Where("account_id = ? AND success = ? AND created_at >= ?", accountId, true, since).
		Count(&count).Error
	if err != nil {
		common.SysError("failed to count hourly email sends: " + err.Error())
		// 数不出来时返回 0 = 不跳过这个账号。
		// 反过来(返回一个大数)会在数据库抖动时把全部账号一起judged成触顶,
		// 于是每一封邮件都走"全部触顶"的告警分支 —— 一次读失败升级成全站告警风暴。
		return 0
	}
	return int(count)
}

// EmailAccountStat 是单个账号的发送量统计。
type EmailAccountStat struct {
	AccountId   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Total       int64  `json:"total"`
	Success     int64  `json:"success"`
	Failed      int64  `json:"failed"`
	LastHour    int64  `json:"last_hour"`
	LastSentAt  int64  `json:"last_sent_at"`
}

// EmailAccountStats 汇总各账号的发送量。
//
// sinceTs 为 0 时统计全部历史。分成三次聚合查询而不是一次带条件聚合:
// SUM(CASE WHEN ...) 在三种数据库上的布尔字面量写法不一致(见 commonTrueVal),
// 而这是管理端冷路径,三次带索引的 GROUP BY 比一句要分方言维护的 SQL 划算。
func EmailAccountStats(sinceTs int64) ([]EmailAccountStat, error) {
	type row struct {
		AccountId   string
		AccountName string
		Cnt         int64
		LastSentAt  int64
	}

	agg := func(extra func(*gorm.DB) *gorm.DB) (map[string]row, error) {
		q := DB.Model(&EmailSendLog{}).
			Select("account_id, max(account_name) as account_name, count(*) as cnt, max(created_at) as last_sent_at").
			Group("account_id")
		if sinceTs > 0 {
			q = q.Where("created_at >= ?", sinceTs)
		}
		if extra != nil {
			q = extra(q)
		}
		var rows []row
		if err := q.Scan(&rows).Error; err != nil {
			return nil, err
		}
		out := make(map[string]row, len(rows))
		for _, r := range rows {
			out[r.AccountId] = r
		}
		return out, nil
	}

	totals, err := agg(nil)
	if err != nil {
		return nil, err
	}
	successes, err := agg(func(q *gorm.DB) *gorm.DB {
		return q.Where("success = ?", true)
	})
	if err != nil {
		return nil, err
	}
	hourAgo := common.GetTimestamp() - int64(time.Hour/time.Second)
	lastHour, err := agg(func(q *gorm.DB) *gorm.DB {
		return q.Where("success = ? AND created_at >= ?", true, hourAgo)
	})
	if err != nil {
		return nil, err
	}

	stats := make([]EmailAccountStat, 0, len(totals))
	for id, t := range totals {
		s := successes[id].Cnt
		stats = append(stats, EmailAccountStat{
			AccountId:   id,
			AccountName: t.AccountName,
			Total:       t.Cnt,
			Success:     s,
			Failed:      t.Cnt - s,
			LastHour:    lastHour[id].Cnt,
			LastSentAt:  t.LastSentAt,
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].AccountId < stats[j].AccountId })
	return stats, nil
}

// EmailSendLogFilter 是台账查询条件。零值表示不过滤。
type EmailSendLogFilter struct {
	AccountId string
	Receiver  string
	// Status ∈ {"", "success", "failed"}
	Status  string
	StartTs int64
	EndTs   int64
}

// GetEmailSendLogs 分页查询发件台账,按时间倒序。
func GetEmailSendLogs(f EmailSendLogFilter, offset int, limit int) ([]*EmailSendLog, int64, error) {
	q := DB.Model(&EmailSendLog{})
	if f.AccountId != "" {
		q = q.Where("account_id = ?", f.AccountId)
	}
	if f.Receiver != "" {
		q = q.Where("receiver LIKE ?", "%"+f.Receiver+"%")
	}
	switch f.Status {
	case "success":
		q = q.Where("success = ?", true)
	case "failed":
		q = q.Where("success = ?", false)
	}
	if f.StartTs > 0 {
		q = q.Where("created_at >= ?", f.StartTs)
	}
	if f.EndTs > 0 {
		q = q.Where("created_at <= ?", f.EndTs)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var logs []*EmailSendLog
	err := q.Order("id desc").Offset(offset).Limit(limit).Find(&logs).Error
	return logs, total, err
}

// DeleteEmailSendLogsBefore 清理指定时间之前的台账,返回删除条数。
func DeleteEmailSendLogsBefore(ts int64) (int64, error) {
	res := DB.Where("created_at < ?", ts).Delete(&EmailSendLog{})
	return res.RowsAffected, res.Error
}
