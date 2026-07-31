package usergroup

import (
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// upstreamDefaultGroup 是上游 model.User 的 Group 字段上
// gorm:"default:'default'" 兜底出来的值。
//
// 本模块从不写它 —— 未配置时 hook 原样返回空串,让数据库默认值继续生效。
// 这里定义它只为了在管理端把「不配置会得到什么」明确展示给运营看。
const upstreamDefaultGroup = "default"

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
// 判据是分组倍率表(setting/ratio_setting 的 GroupRatio):它是运营方声明
// 「本站有哪些分组」的唯一事实清单,上游的分组接口(controller.GetUserGroups)、
// 模型广场、令牌分组选择器全都以它为基准。刻意不用 abilities:abilities 只反映
// 「当前有渠道在跑的分组」,新部署渠道还没配全时它是空的,拿它当硬校验会让
// 运营在最需要配置默认分组的时刻配不进去。abilities 的信息在管理端以
// has_channels 提示的形式给出,是警告而不是闸门。
//
// 纯内存 RWMap 查找,没有 I/O —— 这是它能在用户创建事务里被再校验一次的前提。
func groupExists(name string) bool {
	if name == "" || name == autoGroup {
		return false
	}
	return ratio_setting.ContainsGroupRatio(name)
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
	if !ratio_setting.ContainsGroupRatio(name) {
		return errGroupNotExist
	}
	return nil
}

// groupOption 是管理端下拉的一项。
type groupOption struct {
	Name  string  `json:"name"`
	Ratio float64 `json:"ratio"`
	// HasChannels 表示 abilities 里至少有一条启用的行落在该分组。
	// 为 false 时该分组下没有任何可用模型,选它等于让新用户寸步难行。
	HasChannels bool `json:"has_channels"`
	// PublicUsable 表示该分组在「用户可选分组」白名单里。不是必要条件:
	// service.GetUserUsableGroups 会把用户自己的分组补进可选列表。
	PublicUsable bool `json:"public_usable"`
}

// listGroupOptions 汇总下拉选项。第二个返回值表示 abilities 探测是否成功 ——
// 探测失败时全部 has_channels 都是 false,必须让前端知道那是「不确定」
// 而不是「确实没有渠道」,否则运营会被一片红叹号吓住。
func listGroupOptions() ([]groupOption, bool) {
	ratios := ratio_setting.GetGroupRatioCopy()
	usable := setting.GetUserUsableGroupsCopy()
	withChannels, probeOK := groupsWithEnabledAbilities()

	options := make([]groupOption, 0, len(ratios))
	for name, ratio := range ratios {
		if name == autoGroup {
			continue
		}
		_, publicUsable := usable[name]
		options = append(options, groupOption{
			Name:         name,
			Ratio:        ratio,
			HasChannels:  withChannels[name],
			PublicUsable: publicUsable,
		})
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
