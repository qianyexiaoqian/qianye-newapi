package controller

// qy_plaza_viewer.go —— 模型广场(/api/pricing 与 /api/perf-metrics/*)对
// 「这次请求该按哪个用户分组渲染」这个问题的唯一答案。
//
// 纯新增文件,合并上游时冲突为 0;上游那两个 handler 各只改一行。
//
// ══════════════════════ 这一路修的是什么 ══════════════════════
//
// 未登录访客走的是 TryUserAuth:它**不**写 id / group,于是两个 handler 拿到的
// 用户分组恒为空串,而空串在 service.GetUserUsableGroups 里的语义是「匿名口径」——
// 结果只剩下 options.UserUsableGroups 那张**全站共用**的白名单。
//
// 本站把那张白名单清空成 `{}` 之后,未登录访客在模型广场上看到的是:
//
//	data: []   group_ratio: {}   usable_group: {}
//
// 一个模型都没有,而注册之后立刻就能看到一整页。这不是"隐藏",是"空页面",
// 它把一个正常运营的站点显示成一个没有任何模型的站点。
//
// 正确的展示口径由项目方给定,而且**不是**"某个模型分组的名字":
//
//	注册默认用户分组  →  它的可用模型分组清单  →  这些分组下的模型与倍率
//
// 第一步是 model.QyNewUserGroup();第二、三步完全复用已登录用户那条链路
// (service.QyUsableGroupsForUser + ratio_setting.InspectGroupRatio),
// 一行都不新写 —— 这是「未登录看到的 == 该分组已登录用户看到的」这条不变式
// 唯一可证的形式。手抄一份过滤逻辑的表现是两条路径慢慢漂移,而漂移的方向
// 恰好是"页面上的价与实扣价分家"。
//
// ══════════════════════ 边界:三种"看起来像坏了"的情况 ══════════════════════
//
//  1. **没配过默认分组 / 配的分组已被删** → model.QyNewUserGroup() 回落
//     model.UpstreamDefaultUserGroup("default"),因为那时新用户确实落进 default。
//     在没有 "default" 这一档的站点上,GetUserUsableGroups("default") 与
//     GetUserUsableGroups("") 逐位相同(自我补入要求该名字在分组倍率表里),
//     所以这类站点上线当天零变化。
//
//  2. **默认分组的可用模型分组清单为空**(groupmatrix enforce + 零授权,
//     且分组名本身不在倍率表里)→ 依旧是空列表,**不回落全量**。
//     回落全量会把一个新用户根本调不通的模型印在价格页上,那是比空页面更坏的
//     误导:他会照着页面去充值。空列表至少与"注册之后看到的"完全一致,
//     前端据此显示一句「当前默认分组下暂无可用模型」而不是「换个筛选条件试试」。
//
//  3. **分组有清单但清单里的分组没有可用渠道** → model.GetPricing() 本身就是
//     abilities 聚合出来的,没有渠道的模型压根不会出现在 pricing 里,
//     因此这一档不需要额外处理,天然与已登录用户一致。
//
// 「要求登录才能查看」那个开关(options.HeaderNavModules 的 pricing.requireAuth)
// **不在本文件里判**:它在路由上就已经把 TryUserAuth 换成 UserAuth,未登录请求
// 根本进不到 handler。本文件只服务于"开关关着 + 未登录"这一种组合。

import (
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// plazaViewerUserGroup 给出本次模型广场请求应当采用的用户分组。
//
// 已登录:c.GetString("group")。它由 middleware.setDashboardAuthContext 在**本次
// 请求**的鉴权阶段从同一条 user 记录上写入,与再查一次 GetUserCache 同值,
// 但少一次缓存读、也少一条"缓存读失败就退化成匿名口径"的暗路径。
//
// 未登录:注册默认用户分组。它绝不返回空串(见 model.QyNewUserGroup 的契约),
// 所以调用方不需要为"匿名"单开一条分支 —— 空串那条语义只剩下一个来源,
// 就是"已登录但 users.group 本身是空的",那与今天的行为一致。
func plazaViewerUserGroup(c *gin.Context) string {
	if c.GetInt("id") > 0 {
		return c.GetString("group")
	}
	return model.QyNewUserGroup()
}
