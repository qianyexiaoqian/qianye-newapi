package service

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// SelfUsableGroupDescription 是「用户分组同时是一个模型分组」时补进可选清单的说明文案。
//
// 它是上游的字面量,原样保留 —— 它会出现在用户的令牌分组下拉里,改动即是一次
// 用户可见的文案变更,而这一轮改的是逻辑不是文案。
const SelfUsableGroupDescription = "用户分组"

// GetUserUsableGroups 给出一个用户分组能选的模型分组清单(分组名 → 说明文案)。
//
// ══════════════════ 上游那一段「大杂烩」已经拆掉 ══════════════════
//
// 拆之前这里做三件事:全局白名单副本 → 叠 GroupSpecialUsableGroup 的 +:/-: 差分 →
// **无条件**把 userGroup 自己塞回去。三件事叠在一起的净效果是「只要在全局白名单里、
// 或者名字恰好等于你自己,谁都能选」,而第二步因为第三步永远无效
// (`-:自己` 被无条件补回抵消)。差分整套已下线,见 ratio_setting.GroupRatioSetting。
//
// ══════════════════ 剩下的自我补入为什么**收窄**而不是删掉 ══════════════════
//
// 删干净的诱惑很大,但它会直接打到线上:存量有 5 个名字**同时**是用户分组和真的能
// 路由的模型分组(legacy_dual,最典型的是「浅夜の梦专属号池」:539 个用户 + 76 行
// abilities)。这些名字未必写在 options.UserUsableGroups 里 —— 它们一直靠这一步
// 隐式获得可选性。整段删掉的表现是:那几档人**已经存在的、显式指向自己分组的令牌**
// 在下一次请求同时 403,而管理端没有任何一处会预告这件事。
//
// 全局白名单是**全站共用**的一张 map,所以也不能靠"把这些名字补进白名单"来兜底 ——
// 那等于把这几个私有号池向全站所有用户开放,方向正好反了。能表达
// 「per-(用户分组, 模型分组)」的只有 qianye/modules/groupmatrix 的权威清单。
//
// 所以这里保留一条**有判据的**补入,并把"无条件"这三个字消掉:
// 只有当这个名字确实是一个**配了倍率的模型分组**时才补。
//
// ── 收窄的可观测差异,逐条 ──
//
// 鉴权与选择性三处本来就要求 ContainsGroupRatio,所以完全不受影响:
// middleware/auth.go 紧接着还要过一次 ContainsGroupRatio,controller.GetUserGroups
// 只遍历 GroupRatio 的键,IsUserSelectableGroup 同样要求它。唯一的差别是令牌
// group 等于自己的用户分组、而该名字不在倍率表时,403 的文案从「分组已被弃用」
// 变成「无权访问 X 分组」——仍然是 403,而这种令牌本来就是孤儿。
//
// **但清单本身还有两条消费方不过 ContainsGroupRatio**,那里的差异是真实的:
//
//	controller/user.go GetUserModels  直接把可选清单的**键**当成 groupsToQuery
//	                                  去查 GetGroupEnabledModels
//	controller/pricing.go GetPricing  用可选清单的键过滤 EnableGroup
//
// 于是一个「是用户分组、有 enabled abilities、却不在 options.GroupRatio 里」的
// 私有号池,收窄之后不再自动出现在它自己那一档人的模型选择器与价格页上。
// 本站现有三个这样的名字(管理员分组 / 反重力的哈基米 / 初衷专属分组,共 6 个
// 用户),它们当前能路由到的模型是「浅夜の梦专属号池」的子集,所以今天的差值
// 恰好为 0 —— 但只要给这些池子里的渠道加一个别处没有的模型,那个模型就只在这
// 两个页面上看不见,而用空分组令牌发请求仍然成功。
//
// 这个方向是可接受的:一个没有倍率的模型分组本来就不该出现在**价格页**上
// (GetGroupRatio 对它 fail-open 返回 1.0,页面会印出一个假价格),而正确的
// 修法是给它配一条倍率,或者用 groupmatrix 的权威清单显式授权 —— 两者都是
// 显式的、有预览的。靠「名字恰好相同」隐式获得可见性不是一条可维护的路径。
//
// 想让某一档人**连自己都不能选**,唯一受支持的做法是给那个用户分组设定范围
// (groupmatrix 的 scope + grants):它是显式的、有预览的、能审计的,
// 而且 hook 挂在本函数最后一条 return 上,可以整份替换掉这里的结论。
func GetUserUsableGroups(userGroup string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" && ratio_setting.ContainsGroupRatio(userGroup) {
		if _, ok := groupsCopy[userGroup]; !ok {
			groupsCopy[userGroup] = setting.QyDescribeGroup(userGroup, SelfUsableGroupDescription)
		}
	}
	return QyResolveUsableGroups(userGroup, groupsCopy)
}

func GroupInUserUsableGroups(userGroup, groupName string) bool {
	_, ok := GetUserUsableGroups(userGroup)[groupName]
	return ok
}

func IsUserSelectableGroup(userGroup, groupName string) bool {
	if groupName == "" || groupName == "auto" {
		return false
	}
	return GroupInUserUsableGroups(userGroup, groupName) && ratio_setting.ContainsGroupRatio(groupName)
}

// GetUserAutoGroup 根据用户分组获取自动分组设置
func GetUserAutoGroup(userGroup string) []string {
	autoGroups := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range setting.GetAutoGroups() {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		autoGroups = append(autoGroups, group)
	}
	return autoGroups
}

// FilterUserTokenAutoGroups applies current permissions before the current
// per-token limit. It intentionally does not fall back to the global Auto list.
func FilterUserTokenAutoGroups(userGroup string, groups []string) []string {
	maxCount := setting.GetMaxTokenAutoGroups()
	filtered := make([]string, 0, min(len(groups), maxCount))
	seen := make(map[string]struct{})
	for _, group := range groups {
		if !IsUserSelectableGroup(userGroup, group) {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		filtered = append(filtered, group)
		if len(filtered) == maxCount {
			break
		}
	}
	return filtered
}

// GetRequestAutoGroups resolves the ordered Auto groups for the current token.
// The absence of the context value means that the token inherits the complete
// global Auto list; a present (even empty) value is an explicit token snapshot.
func GetRequestAutoGroups(c *gin.Context, userGroup string) []string {
	value, ok := common.GetContextKey(c, constant.ContextKeyTokenAutoGroups)
	if !ok {
		return GetUserAutoGroup(userGroup)
	}
	groups, ok := value.([]string)
	if !ok {
		return []string{}
	}
	return FilterUserTokenAutoGroups(userGroup, groups)
}

// GetGroupsEnabledModels 按 groups 顺序获取各分组启用的模型并去重
func GetGroupsEnabledModels(groups []string) []string {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, group := range groups {
		for _, modelName := range model.GetGroupEnabledModels(group) {
			if _, ok := seen[modelName]; !ok {
				seen[modelName] = struct{}{}
				models = append(models, modelName)
			}
		}
	}
	return models
}

// GetUserGroupRatio 获取用户使用某个分组的倍率
// userGroup 用户分组
// group 需要获取倍率的分组
//
// 它是**展示口径**:调用点是可选分组列表、价格页、管理端预览。因此走
// ratio_setting.InspectGroupRatio —— 与三条计费路径同一段函数体、同一个优先级,
// 只差一个 billing 布尔:miss 时不告警、不进 admin_info。
// 见 setting/ratio_setting/qy_ratio_export.go。
func GetUserGroupRatio(userGroup, group string) float64 {
	return ratio_setting.InspectGroupRatio(userGroup, group).Ratio
}
