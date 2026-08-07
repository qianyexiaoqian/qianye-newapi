// Package groupvis 修复「模型广场泄漏用户无权使用的分组」这个信息泄漏缺陷
// (CWE-200)。
//
// 根因不是缺少分组可见性体系,而是两个下游出口**没有消费已有的白名单**:
//
//	controller/pricing.go      Pricing.EnableGroup 来自 abilities 聚合,是与用户
//	                           完全无关的全站全量分组集合,被原样序列化成
//	                           enable_groups 下发。上游的 filterPricingByUsableGroups
//	                           只做了行过滤(这个模型要不要显示),没做列裁剪。
//	controller/perf_metrics.go 过滤基准是 GroupRatio(全站分组事实清单)而非用户
//	                           可用分组,且 ?group= 没有归属校验,可主动探测任意
//	                           分组的延迟与成功率。
//
// 两条路径都绕过了 service.GetUserUsableGroups 这张白名单,于是**匿名访客**也能
// 拿到内部/合作方专属分组的名称、该分组下的模型清单,以及它的运营数据。
//
// 修复方式是在这两个出口与白名单求交集,不新建分组实体表、不加 hidden 标记 ——
// 全仓没有任何分组可见性字段,分组只是散落在几个 map 里的字符串 key,
// 为一个泄漏 BUG 引入一套分组实体体系不成比例。
//
// # 为什么只有两个出口,没有第三个
//
// 分组 API(controller.GetUserGroups,挂在 /api/user/groups 与
// /api/user/self/groups)不在修复范围内,因为它本来就不泄漏:它把
// ratio_setting.GetGroupRatioCopy() 与 service.GetUserUsableGroups(userGroup)
// 求交集后才下发,匿名请求(userGroup 为空串)退化成运营方主动配置的
// 公开可用分组 —— 与本包 usableGroupsOf("") 的口径完全一致。
// 它与上面两个出口的差别正在于:那两处的过滤基准是"全站事实清单"
// (abilities 聚合 / GroupRatio),这一处的基准一开始就是用户白名单。
//
// 曾经存在的 group_visibility.filter_group_api 开关因此被删除:它有默认值、
// 写进了示例 YAML,却没有任何代码读它。硬接一个恒等变换的 hook 只会让
// 运维误以为"关掉它"能改变那一路的行为,而实际上那里没有任何东西可过滤。
// 如果哪天上游把 GetUserGroups 改成下发全量分组,该补的是那里的交集,
// 而不是把开关加回来。
//
// 本模块不碰数据库、不注册路由、不起后台任务,因此也不需要 guard:
// guard.Feature 会把「扩展库不可用」判成功能不可用,而分组裁剪是纯内存计算,
// 数据库挂掉时更不该退回泄漏状态。
package groupvis

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/service"
)

type Mod struct{ module.Base }

func (Mod) Name() string { return "groupvis" }

// InstallHooks 无条件注入三个 hook。
//
// 开关在**调用时**读而不是在这里读:config.WatchReload 支持热更新,
// 安装期快照会让改完 YAML 必须重启才生效。一次 atomic 指针加载的代价可以忽略,
// 而这三个 hook 都不在 relay 热路径上。
func (Mod) InstallHooks() {
	controller.QyGroupVisFilterPricing = pricingHook
	controller.QyGroupVisFilterGroupKeys = groupKeysHook
	controller.QyGroupVisFilterPerfGroups = perfGroupsHook
}

func pricingHook(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	g := config.Get().GroupVisibility
	if !g.On() || !g.PricingOn() {
		return pricing
	}
	return filterPricing(pricing, usableGroup, g.KeepAutoGroup())
}

func groupKeysHook(userId int, userGroup string, groups []string) []string {
	g := config.Get().GroupVisibility
	if !g.On() || !g.PerfMetricsOn() {
		return groups
	}
	return filterGroupKeys(groups, usableGroupsOf(userId, userGroup), g.KeepAutoGroup())
}

func perfGroupsHook(userId int, userGroup string, groups []perfmetrics.GroupResult) []perfmetrics.GroupResult {
	g := config.Get().GroupVisibility
	if !g.On() || !g.PerfMetricsOn() {
		return groups
	}
	return filterPerfGroups(groups, usableGroupsOf(userId, userGroup), g.KeepAutoGroup())
}

// usableGroupsOf 取调用者的可用分组白名单。
//
// userGroup 来自 c.GetString("group") —— UserAuth 与 TryUserAuth 都经
// setDashboardAuthContext 写入 user.Group,匿名请求下为空串。
// 空串时 GetUserUsableGroups 退化为「运营方主动配置的公开可用分组」
// (setting.userUsableGroups),这正是匿名访客应该看到的范围:
// 隐藏分组按定义就不该进那张表,而未登录用户仍能在模型广场看到公开分组的价格。
//
// **必须带 userId**:可用分组还包含该用户买的套餐解锁的分组,不带身份会把它们
// 滤掉 —— 用户在令牌页选得到那个分组,在模型广场与性能页却看不到它,
// 同一个人在同一个站点的不同页面得到互相矛盾的答案。userId <= 0 时逐位退化为
// GetUserUsableGroups(userGroup),匿名口径一个字节不变。
func usableGroupsOf(userId int, userGroup string) map[string]string {
	return service.QyUsableGroupsForUser(userId, userGroup)
}

func init() { module.Register(Mod{}) }
