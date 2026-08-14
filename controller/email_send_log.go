package controller

// email_send_log.go —— SMTP 发件台账与按账号统计的管理端接口。
//
// 全部挂在 AdminAuth 之下:台账里带收件人邮箱与发件账号名,
// 两者合起来足以还原"站点在用哪些邮箱、给谁发过信"。

import (
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// GetEmailSendLogs 分页查询发件台账。
func GetEmailSendLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)

	filter := model.EmailSendLogFilter{
		AccountId: c.Query("account_id"),
		Receiver:  c.Query("receiver"),
		Status:    c.Query("status"),
		StartTs:   startTimestamp,
		EndTs:     endTimestamp,
	}
	logs, total, err := model.GetEmailSendLogs(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
}

// GetEmailAccountStats 返回各发件账号的发送量统计。
//
// 同时回带每个账号**当前配置的小时上限**,而不是让前端自己去账号表里对 ——
// 那份对照是"这个号还能发几封"的唯一意义所在,拆到两个接口去取,
// 中间只要有一次刷新不同步,页面上就会出现一个算错的余量。
//
// 账号表里存在、但一封都没发过的账号也要出现在结果里(补零行):
// 缺行会让"刚加的新号还没被轮到"和"这个号根本没生效"长得一模一样。
func GetEmailAccountStats(c *gin.Context) {
	sinceTs, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	stats, err := model.EmailAccountStats(sinceTs)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	byId := make(map[string]int, len(stats))
	for i, s := range stats {
		byId[s.AccountId] = i
	}
	type statWithLimit struct {
		model.EmailAccountStat
		HourlyLimit int  `json:"hourly_limit"`
		Enabled     bool `json:"enabled"`
		Configured  bool `json:"configured"`
	}
	out := make([]statWithLimit, 0, len(stats))
	seen := make(map[string]bool, len(stats))

	for _, a := range common.SMTPAccounts() {
		seen[a.ID] = true
		row := statWithLimit{HourlyLimit: a.HourlyLimit, Enabled: a.Enabled, Configured: true}
		if i, ok := byId[a.ID]; ok {
			row.EmailAccountStat = stats[i]
		} else {
			row.EmailAccountStat = model.EmailAccountStat{AccountId: a.ID, AccountName: a.Name}
		}
		out = append(out, row)
	}
	// 台账里有、账号表里已经没有的(被删掉的号)也要显示,标成未配置 ——
	// 否则历史统计的总数对不上,而运营会以为是统计漏了。
	for _, s := range stats {
		if !seen[s.AccountId] {
			out = append(out, statWithLimit{EmailAccountStat: s})
		}
	}
	common.ApiSuccess(c, out)
}

// DeleteEmailSendLogs 清理指定时间戳之前的发件台账。
func DeleteEmailSendLogs(c *gin.Context) {
	targetTimestamp, _ := strconv.ParseInt(c.Query("target_timestamp"), 10, 64)
	if targetTimestamp == 0 {
		common.ApiErrorMsg(c, "target_timestamp 不能为空")
		return
	}
	count, err := model.DeleteEmailSendLogsBefore(targetTimestamp)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, count)
}
