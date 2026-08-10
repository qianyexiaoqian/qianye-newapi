package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func GetPricing(c *gin.Context) {
	pricing := model.GetPricing()
	groupRatio := map[string]float64{}
	for s, f := range ratio_setting.GetGroupRatioCopy() {
		groupRatio[s] = f
	}

	// 未登录访客按「注册默认用户分组」渲染,已登录按本人分组 —— 判据只在这一处,
	// 理由与边界见 controller/qy_plaza_viewer.go。下面每一行对两种身份完全相同,
	// 这正是「未登录看到的 == 该分组已登录用户看到的」的来源。
	group := plazaViewerUserGroup(c)
	anonymous := c.GetInt("id") <= 0

	if group != "" {
		// 走全仓唯一的解析器(展示口径,只计数不告警),不在这里手抄
		// 「先铺兜底、再用交叉格覆盖」那两句 —— 那是第四份复制品,
		// 而复制品迟早会漂移成「页面上的价与实扣价分家」。
		// 见 setting/ratio_setting/qy_ratio_export.go。
		for g := range groupRatio {
			groupRatio[g] = ratio_setting.InspectGroupRatio(group, g).Ratio
		}
	}

	// userId 仍取 c 的真实值:匿名恒为 0,于是 QyPlanUnlockGroups 按契约 (b)
	// 恒等返回。默认分组不会因为"某个用户买了套餐"而多出解锁分组 ——
	// 套餐是 per-user 的,而这里渲染的是"还不存在的那个新用户"。
	usableGroup := service.QyUsableGroupsForUser(c.GetInt("id"), group)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	pricing = QyGroupVisFilterPricing(pricing, usableGroup)
	// check groupRatio contains usableGroup
	for group := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               pricing,
		"vendors":            model.GetVendors(),
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        service.GetUserAutoGroup(group),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
		// 未登录预览标记。**刻意不下发分组名**:名字对访客没有意义,而"这一页
		// 是按新用户的默认档算的、登录后按你自己的档算"才是他需要知道的那句话。
		// 少了它,一个属于别的分组的用户退出登录后会看到一套不同的价格,
		// 而页面上没有任何东西解释为什么。
		"anonymous_preview": anonymous,
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "重置模型倍率成功",
	})
}
