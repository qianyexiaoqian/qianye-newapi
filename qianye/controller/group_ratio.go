package controller

import (
	"github.com/QuantumNous/new-api/qianye/groupratio"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// group_ratio.go —— 分组倍率失配的诊断出口。
//
// 上游 ratio_setting.GetGroupRatio 找不到分组时返回 1 并只写一行 SysLog
// (setting/ratio_setting/group_ratio.go:79-86)。SysLog 会被滚走、没有界面、
// 没有计数器,于是"某个免费/极低价分组被改名或删掉"的表现是**静默按 1.0 倍扣费**。
//
// 这个端点就是那条缺口的可观测信号:把"哪些分组名不在倍率表里、影响多少用户、
// 多少令牌"变成一页能打开的东西。它是只读的,不改任何配置 —— 修复动作仍然在
// 上游的「分组倍率」设置页,这里只负责让人知道要去修。
//
// 判据与降级约定见 qianye/groupratio 的包注释。

// AdminGroupRatioOrphans 列出全部失配分组名及其影响面。
//
// ?refresh=1 强制重扫(忽略 60 秒缓存)。默认走缓存:这两条查询是 users / tokens
// 上的全表 GROUP BY,而排障页会被反复刷新。
func AdminGroupRatioOrphans(c *gin.Context) {
	if !requireCore(c) {
		return
	}
	// 挂冷路径预算:主库病态时这两条聚合可能一直等到驱动层超时,
	// 而这个端点是管理员随手点的,不接预算就会把 HTTP 请求一起挂住。
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	scan := groupratio.Scan(ctx, c.Query("refresh") == "1")
	ok(c, gin.H{
		// scan 是**主动**信号:覆盖全站 users/tokens,包括扩展从没解析过的分组。
		"scan": scan,
		// observed 是**被动**信号:扩展自己解析倍率时真的撞到过的失配名,
		// 带累计次数与首末次时间。两者互补 —— 一个回答"谁会被影响",
		// 另一个回答"已经影响到了没有"。
		"observed": groupratio.Observed(),
	})
}
