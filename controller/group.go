package controller

import (
	"net/http"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// GetGroups 返回**模型分组**清单。
//
// Deprecated: 语义已收敛为模型分组。历史上它同时被用户编辑下拉(想要用户分组)
// 与渠道/令牌分组下拉(想要模型分组)消费,而两者拿到的是同一份
// options.GroupRatio 的键 —— 这正是「用户分组下拉里出现模型分组」的唯一根因。
// 新代码请改用 GetModelGroupOptions / GetUserGroupOptions。
func GetGroups(c *gin.Context) {
	c.Header("Deprecation", "true")
	c.Header("Link", `</api/model-group/options>; rel="successor-version"`)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    modelGroupNames(),
	})
}

// GetModelGroupOptions 是**模型分组**的候选清单。
//
// 消费方:渠道分组、令牌分组、auto 顺序、套餐解锁绑定。
// 口径恒为 options.GroupRatio 的键 —— 那是"这个站点有哪些模型分组"的唯一事实源,
// 扩展的登记表刻意不参与(它是 fail-open 的,漏登记一个名字不该让下拉少一项)。
func GetModelGroupOptions(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    modelGroupNames(),
	})
}

// GetUserGroupOptions 是**用户分组**的候选清单。
//
// 消费方:用户编辑的分组下拉、充值折扣(TopupGroupRatio)、套餐升降级分组。
//
// ── 口径为什么是 users.group 而不是 GroupRatio 的键 ──
//
// 用户分组回答的是"这个人是谁",它的事实源只能是 users 表。用 GroupRatio 的键
// 去喂用户编辑下拉,等于把"有哪些渠道池子"当成"有哪些用户身份":管理员会在
// 用户编辑页看到一堆模型分组名,而真正在用的用户分组反而可能不在列表里
// (它只要没被配过倍率就消失了)。
//
// 查库而不是走缓存是刻意的:这是管理端的冷路径,一次 DISTINCT 换一份不会骗人的
// 清单;而任何缓存都要回答"新建的分组多久出现在下拉里",那个问题没有好答案。
//
// ── 为什么还要并上「已声明」的分组 ──
//
// `SELECT DISTINCT users.group` 有一个结构性盲区:**一个刚建出来的分组还没有人**。
// 而"建一档新的、再把人挪进去"正是新建这个功能唯一的用法 —— 只认 users.group
// 会让它在结构上不可能完成:新建成功、列表里没有、于是没有任何一个入口能把人放进去。
//
// service.QyDeclaredUserGroups 读的是扩展的登记表(qy_user_groups),默认实现
// 返回 nil ⇒ 扩展未启用时这一行是空操作,清单逐位等于 users.group。
func GetUserGroupOptions(c *gin.Context) {
	names, err := model.QyDistinctUserGroups()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		seen[name] = true
	}
	for _, name := range service.QyDeclaredUserGroups() {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    names,
	})
}

func modelGroupNames() []string {
	ratios := ratio_setting.GetGroupRatioCopy()
	groupNames := make([]string, 0, len(ratios))
	for groupName := range ratios {
		groupNames = append(groupNames, groupName)
	}
	sort.Strings(groupNames)
	return groupNames
}

func GetUserGroups(c *gin.Context) {
	usableGroups := make(map[string]map[string]interface{})
	userGroup := ""
	userId := c.GetInt("id")
	userGroup, _ = model.GetUserGroup(userId, false)
	userUsableGroups := service.QyUsableGroupsForUser(userId, userGroup)
	for groupName, _ := range ratio_setting.GetGroupRatioCopy() {
		// UserUsableGroups contains the groups that the user can use
		if desc, ok := userUsableGroups[groupName]; ok {
			usableGroups[groupName] = map[string]interface{}{
				"ratio": service.GetUserGroupRatio(userGroup, groupName),
				"desc":  desc,
			}
		}
	}
	if _, ok := userUsableGroups["auto"]; ok {
		usableGroups["auto"] = map[string]interface{}{
			"ratio": "自动",
			"desc":  setting.GetUsableGroupDescription("auto"),
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    usableGroups,
	})
}
