// Package channelops 提供渠道列表页的批量操作:批量启停、批量删除、
// 批量重置本站统计(已用额度 / 余额显示值)。
//
// # 为什么住在扩展侧而不是往上游加接口
//
// 铁律:不动原项目。上游本来就有三个批量端点(见下表),它们的口径没有问题,
// 问题在**返回值的形状**:一律只回一个整数 count。于是"选了 20 个,第 7 个失败了"
// 这件事在协议层就说不出来 —— 前端只能拿 len(ids) - count 去猜,
// 而那个减法是错的(见 outcomeSkipped 的注释)。
//
// 本模块因此只做一件上游做不到的事:**逐条返回结果**。业务口径全部照抄上游:
//
//	批量启停  model.UpdateChannelStatus(id, "", status, reason)  ← 与 controller.BatchUpdateChannelStatus 同一个函数
//	批量删除  channels 行 + abilities 行,同一事务                ← 与 model.BatchDeleteChannels 同一组语句
//	缓存失效  model.InitChannelCache() + service.ResetProxyClientCache()
//
// 唯一的差别是**事务粒度**:上游 BatchDeleteChannels 把 200 个 id 放进同一个
// 事务,任何一条出错就整体回滚;本模块每条一个事务,失败的那条不牵连已经成功的
// 前六条。这不是优化,是需求本身:"不能整体回滚(前 6 个已经删了),
// 也不能报全部成功"。
//
// # 为什么删除必须自己写而不是调 channel.Delete()
//
// 上游的 `(*Channel).Delete()` 是两条独立语句(先删 channels 再删 abilities),
// 中间任何一次失败都会在物化路由表里留下指向已删渠道的行 —— 那正是
// 需求里点名要避免的形状。本模块把这两条语句放进同一个事务,
// 每个渠道各一个,既保证单条原子,又保证批次可部分成功。
package channelops

import (
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/gin-gonic/gin"
)

// Mod 把渠道批量操作接进扩展的模块注册表。
//
// 对原项目后端的改动为 0 行:没有 hook、没有后台任务、没有自己的表 ——
// 它读写的是上游主库的 channels / abilities,靠上游导出的 model.* 完成。
type Mod struct{ module.Base }

func (Mod) Name() string { return "channelops" }

// RegisterAdminRoutes 挂载管理端接口。传入的组只挂了 AdminAuth。
//
// # 三条路由为什么各自带一个 RequirePermission
//
// 扩展的 admin 组只有 AdminAuth,而上游的渠道接口在 AdminAuth 之上还逐条挂了
// authz 权限。照搬路径而漏掉权限,等于给"有 channel:operate 但没有
// channel:sensitive_write 的运营管理员"开了一条删渠道的后门 —— 上游那边删不掉,
// 换个 URL 就删得掉。分档与上游 router/channel-router.go 逐字对齐:
//
//	POST /channel/status/batch  → ChannelOperate         本模块 batch-status 同档
//	POST /channel/batch(删除)   → ChannelSensitiveWrite  本模块 batch-delete 同档
//
// 重置统计上游没有对应端点。归到 ChannelSensitiveWrite 而不是 ChannelOperate:
// 它抹掉的是计费统计的历史依据,不可撤销,与"测一下渠道通不通"不是一个量级;
// 而上游把 used_quota 列进 channelReadOnlyFields(编辑接口一律忽略),
// 说明它本来就被当作"服务端自己记账的列",能改它的人应当是最窄的那一档。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.POST("/channels/batch-status",
		middleware.RequirePermission(authz.ChannelOperate), adminBatchStatus)
	g.POST("/channels/batch-delete",
		middleware.RequirePermission(authz.ChannelSensitiveWrite), adminBatchDelete)
	g.POST("/channels/batch-reset-usage",
		middleware.RequirePermission(authz.ChannelSensitiveWrite), adminBatchResetUsage)
}

func init() { module.Register(Mod{}) }
