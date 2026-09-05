package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

// UserTokenLiveStats 返回当前用户**每一把** API 密钥此刻的在途请求数与近 60 秒
// 请求数。
//
// 项目方原话:「api密钥增加一个 sub2 一样显示一下 key 当前并发数(每)分钟。」
//
// ─────────────── 数从哪来 ───────────────
//
// 全部来自进程内存(middleware/qy_token_live_export.go),一个字节都不碰数据库 ——
// 主库也不碰,扩展库也不碰。这条接口被密钥页每 5 秒轮询一次,任何一次落库
// 都会把"看一眼并发"变成一件按打开页数收费的事。
//
// 也正因为如此,它**不走 requireCore**:与同目录 UserSessionStats、
// UserTokenTodayUsage 同一理由,让一个不依赖扩展库的只读计数在扩展库不可用时
// 503 是纯粹的负收益。
//
// ─────────────── 越权面 ───────────────
//
// 计数在写入时就记下了令牌的属主(TokenAuth 在同一次请求里 c.Set("id",
// token.UserId)),这里按**会话身份**取自己的那一份。前端不传令牌 id,
// 因此不存在"传别人的 id 去探别人的并发"这条路 —— 那种设计需要在这里再查一次
// 归属,而"忘了查归属"正是这类批量读接口最常见的漏法。
//
// ─────────────── 零值口径 ───────────────
//
//	此刻没在跑、近 60 秒也没请求 → 不在 stats 里。前端渲染 0,不是 "-"。
//	取不到(网络/500)            → 前端渲染 "—",不能渲染 0。
//
// 与「今日消耗」那一列逐字同一套口径。把"取不到"画成 0 是在编一个数字。
//
// ─────────────── 这个数只覆盖本节点 ───────────────
//
// 计数是进程内的(理由见 middleware 那一侧的文件头)。多节点部署下,每个节点
// 只看得见打到自己身上的那一部分,而用户的两次刷新可能落在不同节点上,
// 于是数字会跳。这件事必须写在界面上,所以 window_seconds 与那句提示都由
// 后端下发,而不是前端写死。
func UserTokenLiveStats(c *gin.Context) {
	userId := c.GetInt("id")
	if userId <= 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "unauthorized"})
		return
	}

	live := middleware.QyTokenLiveSnapshot(userId)

	// 键必须是字符串:JSON 对象的键本来就只能是字符串,显式转一次是为了让前端
	// 那一侧的类型与这里逐字对上,而不是依赖 encoding/json 对整数键的隐式转换。
	stats := make(map[string]gin.H, len(live))
	for tokenId, v := range live {
		stats[strconv.Itoa(tokenId)] = gin.H{
			"in_flight": v.InFlight,
			"requests":  v.Requests,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"window_seconds": middleware.QyTokenLiveWindowSeconds,
			"stats":          stats,
		},
	})
}
