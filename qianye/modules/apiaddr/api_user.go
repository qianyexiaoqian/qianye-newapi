package apiaddr

import (
	"strings"

	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/gin-gonic/gin"
)

// handleUserList 下发**当前用户的分组**可见的 API 地址。
//
// # 分组可见性
//
// 每条地址可绑定若干用户分组(Address.UserGroups),不绑定 = 对所有分组可见。
// 用户看到的是:自己分组专属的地址 + 所有未绑定分组的地址(默认兜底线路)。
// 一个没配过专属线路的分组,看到的恰好就是那批未绑定的 —— "默认地址兜底"
// 不需要第二条规则。
//
// 用户分组取 c.GetString("group"):本路由挂 UserAuth,setDashboardAuthContext
// 必已写入 user.Group,与鉴权用的是同一份快照 —— 这里再查一次库反而可能与
// 鉴权时看到的不一致(violation / groupvis 同款口径)。
//
// # 为什么"过滤后一条不剩"也返回空数组而不是报错
//
// 这个接口的唯一消费方是密钥列表上的「复制链接信息」。前端在拿到空列表时会
// **回落到站点地址**(localStorage 里的 server_address,再兜底 window.location
// .origin)——那正是本功能上线之前的行为。也就是说:运营一条都没配、或这个
// 分组恰好一条都看不到,都与旧版完全一致,而不是弹出一个空列表让用户无从选择。
//
// 因此这里绝不能因为"没有数据"而返回 404 或错误:那会让前端把它当成一次失败,
// 而失败与"就是没配"在前端是两种处理(前者提示重试,后者静默回落)。
func handleUserList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	// surface 声明调用方是哪个展示位置(console = 控制台「API信息」卡片,
	// picker = 复制链接信息 / CC Switch),据此在服务端按 Address.Surfaces
	// 过滤。缺省 = 不过滤:旧版前端不带这个参数,它们看到全量与升级前一致。
	// 未知取值直接 400 而不是当成"不过滤":那多半是新前端的拼写错误,
	// 静默放行等于把一条只该在某个位置出现的地址下发到了别处。
	surface := strings.ToLower(strings.TrimSpace(c.Query("surface")))
	if surface != "" && !allowedSurfaces[surface] {
		respondErr(c, errSurfaceInvalid)
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

	userGroup := c.GetString("group")
	// 显式 make:nil 切片会被序列化成 JSON `null`,前端对着它 .map 直接白屏。
	// 判据见 qianye/json_array_guard_test.go。
	items := make([]userView, 0, len(rows))
	for _, row := range rows {
		if !visibleToUserGroup(row.UserGroups, userGroup) {
			continue
		}
		if !visibleOnSurface(row.Surfaces, surface) {
			continue
		}
		items = append(items, userView{
			Id: row.Id, Name: row.Name, Remark: row.Remark, URL: row.URL,
			Color: row.Color,
		})
	}
	respondOK(c, gin.H{"items": items})
}
