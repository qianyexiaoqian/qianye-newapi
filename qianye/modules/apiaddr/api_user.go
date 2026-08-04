package apiaddr

import (
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// handleUserList 下发用户可选的 API 地址。
//
// # 为什么"一条都没配"要返回空数组而不是报错
//
// 这个接口的唯一消费方是密钥列表上的「复制链接信息」。前端在拿到空列表时会
// **回落到站点地址**(localStorage 里的 server_address,再兜底 window.location
// .origin)——那正是本功能上线之前的行为。也就是说:运营一条都没配 = 与旧版
// 完全一致,而不是弹出一个空列表让用户无从选择。
//
// 因此这里绝不能因为"没有数据"而返回 404 或错误:那会让前端把它当成一次失败,
// 而失败与"就是没配"在前端是两种处理(前者提示重试,后者静默回落)。
func handleUserList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	rows, err := listAll(ctx, gdb, true)
	if err != nil {
		respondErr(c, err)
		return
	}

	// 显式 make:nil 切片会被序列化成 JSON `null`,前端对着它 .map 直接白屏。
	// 判据见 qianye/json_array_guard_test.go。
	items := make([]userView, 0, len(rows))
	for _, row := range rows {
		items = append(items, userView{
			Id: row.Id, Name: row.Name, Remark: row.Remark, URL: row.URL,
		})
	}
	respondOK(c, gin.H{"items": items})
}
