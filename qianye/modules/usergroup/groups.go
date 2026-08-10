package usergroup

import (
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// upstreamDefaultGroup 是上游 model.User 的 Group 字段上
// gorm:"default:'default'" 兜底出来的值。
//
// 本模块从不写它 —— 未配置时 hook 原样返回空串,让数据库默认值继续生效。
// 这里定义它只为了在管理端把「不配置会得到什么」明确展示给运营看。
//
// 引用上游那个常量而不是再抄一遍字面量:model.QyNewUserGroup 的默认实现
// (扩展未启用时)返回的就是它,而 newUserGroup() 在配置失效时返回的也是它。
// 两处抄成两份字符串的表现是「管理端说新用户进 A、库里进 B」。
const upstreamDefaultGroup = model.UpstreamDefaultUserGroup

// autoGroup 是「按倍率自动选分组」的伪分组名。
//
// 它可以是**令牌**的分组(见 setting.DefaultUseAutoGroup),但绝不能是
// **用户**的分组:abilities 表里不存在 group='auto' 的行,把新用户默认分组
// 设成它等于让所有新用户一个模型都调不通。上游 GroupRatio 里也没有这个 key,
// 但运营完全可能手工往倍率表里加一个,所以这里显式挡一道。
const autoGroup = "auto"

// maxGroupNameLen 对齐 users.group 的 varchar(64)。
// 超长的名字写进去会被数据库截断,截断后的分组必然不存在。
const maxGroupNameLen = 64

var (
	errGroupEmptyIsClear = errors.New("qianye/usergroup: 空值表示取消配置")
	errGroupTooLong      = errors.New("分组名过长")
	errGroupIsAuto       = errors.New("auto 是令牌的自动分组,不能作为用户分组")
	errGroupNotExist     = errors.New("目标分组不存在")
)

// groupExists 判断分组是否真实存在。
//
// ── 判据是两份清单的并集,不是单独的分组倍率表 ──
//
// 本模块最初写成「只认 ratio_setting.GroupRatio」,那在用户分组与模型分组还是
// 同一份清单的时候是对的。分组彻底拆开之后 `GroupRatio` 的键是**模型分组**
// (它是 GroupGroupRatio[用户分组][模型分组] 未命中时的兜底价,由「模型分组」页
// 编辑),而用户分组的事实清单是登记表 `qy_user_groups`。继续只认前者的后果不是
// 「少了几个选项」,是**运营在登记表里新建出来的用户分组一个都选不了**:保存时
// 400「目标分组不存在」,即便绕过保存,resolveNewUserGroup 也会在注册事务里再判
// 一次假、静默把新用户丢回 default —— 注册成功、一个模型都调不通,而这正是这条
// 配置存在的全部理由。
//
// 并集而不是替换:`GroupRatio` 里那些历史上兼作用户分组的名字(default/vip/svip,
// 以及回填之前就在 users.group 上跑着的)必须继续可选,否则这次判据切换本身就是
// 一次能力回退。扩展关闭 / 快照没加载时 QyDeclaredUserGroups 返回 nil,行为逐字
// 回到切换之前。
//
// 刻意不用 abilities:abilities 只反映「当前有渠道在跑的分组」,新部署渠道还没配全
// 时它是空的,拿它当硬校验会让运营在最需要配置默认分组的时刻配不进去。abilities
// 的信息在管理端以 has_channels 提示的形式给出,是警告而不是闸门。
//
// 两份清单都是内存查找(RWMap / groupns 的活跃快照),没有 I/O —— 这是它能在用户
// 创建事务里被再校验一次的前提。
func groupExists(name string) bool {
	if name == "" || name == autoGroup {
		return false
	}
	if ratio_setting.ContainsGroupRatio(name) {
		return true
	}
	for _, declared := range service.QyDeclaredUserGroups() {
		if declared == name {
			return true
		}
	}
	return false
}

// validateDefaultGroup 校验管理端提交的默认分组。
//
// 返回 errGroupEmptyIsClear 表示「这是一次清空操作」,不是错误 ——
// 调用方据此区分「保存空值」与「保存非法值」。
func validateDefaultGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errGroupEmptyIsClear
	}
	if len(name) > maxGroupNameLen {
		return errGroupTooLong
	}
	if name == autoGroup {
		return errGroupIsAuto
	}
	if !groupExists(name) {
		return errGroupNotExist
	}
	return nil
}

// groupOption 是管理端下拉的一项。
type groupOption struct {
	Name string `json:"name"`
	// Ratio 是该名字在 GroupRatio 里的值,**可以是 null**。
	//
	// 指针而不是裸 float64:拆分之后 GroupRatio 的键是模型分组,一个只登记在
	// `qy_user_groups` 里的用户分组在那张表里根本没有条目。裸 float64 会把这种
	// 情况渲染成一个货真价实的 `×0`,而 0 在这套系统里是「显式免费」的意思 ——
	// 一个没有配过任何倍率的分组不该看起来像是免费的。
	//
	// 刻意不加 omitempty:字段名本身是与 transfer 的 groupCandidate 共用的跨模块
	// JSON 契约(见 transfer/grouprule_test.go 的 TestGroupCandidateShapeMatches…),
	// 缺席与 null 对前端也不是一回事 —— null 是「后端确实回答了:没有」。
	Ratio *float64 `json:"ratio"`
	// HasChannels 表示 abilities 里至少有一条启用的行落在该分组。
	// 为 false 时该分组下没有任何可用模型,选它等于让新用户寸步难行。
	HasChannels bool `json:"has_channels"`
	// PublicUsable 表示该分组在「用户可选分组」白名单里。不是必要条件:
	// service.GetUserUsableGroups 会把用户自己的分组补进可选列表。
	PublicUsable bool `json:"public_usable"`
	// Registered 表示它在用户分组登记表 `qy_user_groups` 里。
	//
	// false 意味着这个名字只是历史上留在 GroupRatio 里的:它仍然可选(向后兼容),
	// 但它不在「用户分组登记」那张卡片上,改名与删除那套带迁移的流程管不到它。
	Registered bool `json:"registered"`
}

// listGroupOptions 汇总下拉选项。第二个返回值表示 abilities 探测是否成功 ——
// 探测失败时全部 has_channels 都是 false,必须让前端知道那是「不确定」
// 而不是「确实没有渠道」,否则运营会被一片红叹号吓住。
//
// 清单与 groupExists 的判据**必须同源**(登记表 ∪ GroupRatio)。两边各列一遍的
// 表现永远是同一种:下拉里选得到、保存时报「目标分组不存在」。
func listGroupOptions() ([]groupOption, bool) {
	ratios := ratio_setting.GetGroupRatioCopy()
	declared := service.QyDeclaredUserGroups()
	usable := setting.GetUserUsableGroupsCopy()
	withChannels, probeOK := groupsWithEnabledAbilities()

	registered := make(map[string]bool, len(declared))
	for _, name := range declared {
		registered[name] = true
	}

	names := make(map[string]struct{}, len(ratios)+len(declared))
	for name := range ratios {
		names[name] = struct{}{}
	}
	for _, name := range declared {
		names[name] = struct{}{}
	}

	options := make([]groupOption, 0, len(names))
	for name := range names {
		if name == autoGroup {
			continue
		}
		_, publicUsable := usable[name]
		option := groupOption{
			Name:         name,
			HasChannels:  withChannels[name],
			PublicUsable: publicUsable,
			Registered:   registered[name],
		}
		if ratio, ok := ratios[name]; ok {
			option.Ratio = &ratio
		}
		options = append(options, option)
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Name < options[j].Name })
	return options, probeOK
}

// groupsWithEnabledAbilities 取 abilities 里所有仍有启用渠道的分组。
//
// 走 GORM 的 Model + Pluck 而不是拼 SQL:group 是三种数据库里的保留字,
// 只有让 GORM 拿着 model.Ability 的 schema 去渲染,引号规则才会自动跟着方言走
// (MySQL/SQLite 反引号、PostgreSQL 双引号)。手写 "DISTINCT group" 在
// 任何一种上都是语法错误。
func groupsWithEnabledAbilities() (map[string]bool, bool) {
	if model.DB == nil {
		return nil, false
	}
	var names []string
	err := model.DB.Model(&model.Ability{}).
		Where("enabled = ?", true).
		Distinct().
		Pluck("group", &names).Error
	if err != nil {
		return nil, false
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set, true
}
