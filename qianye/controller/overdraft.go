package controller

import (
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/overdraft"

	"github.com/gin-gonic/gin"
)

// overdraft.go —— 「接受透支」这条取舍的可运维出口。
//
// 本站的结算补收刻意没有余额下限(拍板记录见 qianye/docs/decisions.md 的 D-01,
// 代码侧的落点在 relay/helper/price.go 与 service/funding_source.go 的注释里)。
// 取舍本身不打算改,但它必须**可运维**:运营要能回答「现在有多少账号是负的、
// 合计欠多少、最深的是谁」,才谈得上决定要不要追欠费或封号。
//
// 这个端点是那个答案,而且只读 —— 它不追欠、不封号、不清零。处置动作仍然在
// 上游的用户管理页。

// AdminOverdraftOverview 负余额账号总览。
// GET /api/qy/admin/overdraft?limit=N
//
// # 为什么不走 requireCore
//
// 与 AdminRestrictedAccountsOverview / UserSessionStats 同一理由:本端点
// **一个字都不碰扩展库** —— 它查的是上游主库的 users.quota。让它在扩展库不可用时
// 503,只会让运营在最需要弄清「站上现在欠了多少钱」的时刻拿到一个数据库错误。
//
// # 为什么查不到就 500,而不是回一份空报告
//
// 「0 个账号欠 0 元」与「查询挂了」在界面上长得一模一样,而运营结论正好相反:
// 前者是「今天很干净」,后者是「你什么都不知道」。所以错误一路上抛成 500。
func AdminOverdraftOverview(c *gin.Context) {
	// 走 httpq.Int 而不是 strconv:请求参数的上界是解析的一部分
	// (strconv.Atoi 的上界是 MaxInt64)。解析不出来时退回 0 = 用默认长度 ——
	// 这里刻意不 400,limit 是纯展示参数,为它拒掉一次排障请求不划算,
	// 而越界由 overdraft.Scan 自己夹到 MaxTopLimit。
	limit := httpq.Int(c, "limit", 0)

	// 挂冷路径预算:主库病态时这两条聚合可能一直等到驱动层超时,
	// 而这个端点是管理员随手点的,不接预算就会把 HTTP 请求一起挂住。
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	report, err := overdraft.Scan(ctx, limit)
	if err != nil {
		serverError(c, err)
		return
	}
	ok(c, report)
}
