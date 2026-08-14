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
		// default_group 是令牌创建界面的**预选值**,不是权限。
		//
		// 只下发这一个已解析的字符串,绝不下发那张「用户分组 → 模型分组」全量映射:
		// 那张映射的键集合等于本站有哪些用户分组,下发给普通用户等于把全站分档结构
		// 暴露给每一个登录账号(与 GetUserGroupOptions 只给管理端是同一条理由)。
		//
		// 落在该用户真实可选清单之外时返回空串,由前端退回原有的默认选中逻辑 ——
		// 判据用的就是上面刚算完的 usableGroups,因此它天然同时满足
		// 「模型分组存在」与「这个人能选」两件事,不会预选一个用户一提交就被
		// 写入侧校验拒绝的分组。
		"default_group": resolveTokenDefaultGroup(userGroup, usableGroups),
	})
}

// resolveTokenDefaultGroup 把配置里的预选值裁到该用户真实可选的范围内。
//
// 单独成函数是为了让这条裁剪有直接的测试落点:它是全站唯一一处把
// 「运营配了什么」与「这个人能选什么」求交的地方,而两者分家的表现是
// 用户打开新建令牌就看到一个他提交不了的分组,且完全不知道该改哪里。
func resolveTokenDefaultGroup(userGroup string, usableGroups map[string]map[string]interface{}) string {
	name := setting.GetTokenDefaultGroup(userGroup)
	if name == "" {
		return ""
	}
	if _, ok := usableGroups[name]; !ok {
		return ""
	}
	return name
}
