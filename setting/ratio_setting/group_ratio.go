package ratio_setting

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
)

var defaultGroupRatio = map[string]float64{
	"default": 1,
	"vip":     1,
	"svip":    1,
}

var groupRatioMap = types.NewRWMap[string, float64]()

var defaultGroupGroupRatio = map[string]map[string]float64{
	"vip": {
		"edit_this": 0.9,
	},
}

var groupGroupRatioMap = types.NewRWMap[string, map[string]float64]()

// GroupRatioSetting 是分组倍率的两张表。
//
// ══════════ 曾经的第三个字段 GroupSpecialUsableGroup 已整体下线 ══════════
//
// 它是「用户分组 → (+:X / -:X / 裸 X) → 说明文案」的差分规则,叠在全局
// options.UserUsableGroups 之上。下线的理由不是"用得少",是**它从来没有真正生效过**:
// service.GetUserUsableGroups 在差分算完之后**无条件**把 userGroup 自己补回去,
// 于是唯一有意义的那一半(`-:自己`,把一个分组从它自己的清单里遮掉)恒被抵消 ——
// 本站线上那条 `{"浅夜の自己人":{"-:浅夜の自己人":"remove"}}` 就是这样静默无效了很久。
//
// 「哪一档人能选哪些模型分组」现在由 qianye/modules/groupmatrix 的权威清单
// (qy_group_scopes + qy_group_grants)回答:它是 per-(用户分组, 模型分组) 的显式
// 授权,能表达差分表达不了的一切,而且带预览、审计与影子期。
//
// **库里残留的 options 行 `group_ratio_setting.group_special_usable_group` 无害**:
// setting/config 的 LoadFromDB 按结构体字段反查 map,字段不存在时那一行被直接忽略。
// 刻意不写迁移去 DELETE 它 —— 它是"这里曾经有过一套规则"的唯一证据,
// 而一条谁都不读的 options 行不值得一次跨三种数据库的迁移。
type GroupRatioSetting struct {
	GroupRatio      *types.RWMap[string, float64]            `json:"group_ratio"`
	GroupGroupRatio *types.RWMap[string, map[string]float64] `json:"group_group_ratio"`
}

var groupRatioSetting GroupRatioSetting

func init() {
	groupRatioMap.AddAll(defaultGroupRatio)
	groupGroupRatioMap.AddAll(defaultGroupGroupRatio)

	groupRatioSetting = GroupRatioSetting{
		GroupRatio:      groupRatioMap,
		GroupGroupRatio: groupGroupRatioMap,
	}

	config.GlobalConfig.Register("group_ratio_setting", &groupRatioSetting)
}

func GetGroupRatioSetting() *GroupRatioSetting {
	return &groupRatioSetting
}

func GetGroupRatioCopy() map[string]float64 {
	return groupRatioMap.ReadAll()
}

func ContainsGroupRatio(name string) bool {
	_, ok := groupRatioMap.Get(name)
	return ok
}

func GroupRatio2JSONString() string {
	return groupRatioMap.MarshalJSONString()
}

func UpdateGroupRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonString(groupRatioMap, jsonStr)
}

func GetGroupRatio(name string) float64 {
	ratio, ok := lookupGroupRatio(name)
	if !ok {
		// 汇处的上报:本函数的调用点远不止三条计费路径(可选分组列表、价格页、
		// 管理端下拉都经 GetUserGroupRatio 落到它),今后新增的第四处不接任何 hook
		// 也会被这里捞到 —— 这就是「只堵源会漏掉下一个调用点」的补丁。
		// billing 恒为 false:这里拿不到用户分组,也判断不出这一次是不是真的在扣钱,
		// 逐笔归因由 ResolveGroupRatio 负责。详见 qy_ratio_export.go。
		QyNoteGroupRatioMiss(name, false)
	}
	return ratio
}

func GetGroupGroupRatio(userGroup, usingGroup string) (float64, bool) {
	gp, ok := groupGroupRatioMap.Get(userGroup)
	if !ok {
		return -1, false
	}
	ratio, ok := gp[usingGroup]
	if !ok {
		return -1, false
	}
	return ratio, true
}

func GroupGroupRatio2JSONString() string {
	return groupGroupRatioMap.MarshalJSONString()
}

func UpdateGroupGroupRatioByJSONString(jsonStr string) error {
	return types.LoadFromJsonString(groupGroupRatioMap, jsonStr)
}

// MaxGroupRatio 是分组倍率的上界。上游对倍率本身没有上界,但它会被直接乘进
// 每一笔账单:一次手滑把倍率填成 1e18,配合大 token 数就会把中间结果推过额度上界,
// 而饱和之后的账单没有任何意义(见 AGENTS.md 的计费安全不变量)。
// 取值与 qianye/modules/groupmatrix 的 maxGroupRatio 一致 —— 两个写入口对同一张表
// 用两套界限,等于其中一套形同虚设。
const MaxGroupRatio = 1000000

// checkRatioValue 是两张倍率表共用的单值判据。
//
// 负倍率是这里唯一会把「扣费」变成「给用户充值」的取值:
// common/quota_math.go 的饱和转换只在 <= MinQuota 时才夹,区间内的负值原样返回,
// 于是预扣与结算双双为负,一路走到 IncreaseUserQuota。
func checkRatioValue(name string, ratio float64) error {
	if ratio != ratio { // NaN
		return errors.New("group ratio is not a valid number: " + name)
	}
	if ratio < 0 {
		return errors.New("group ratio must be not less than 0: " + name)
	}
	if ratio > MaxGroupRatio {
		return fmt.Errorf("group ratio must be not greater than %d: %s", MaxGroupRatio, name)
	}
	return nil
}

func CheckGroupRatio(jsonStr string) error {
	checkGroupRatio := make(map[string]float64)
	err := json.Unmarshal([]byte(jsonStr), &checkGroupRatio)
	if err != nil {
		return err
	}
	for name, ratio := range checkGroupRatio {
		if err := checkRatioValue(name, ratio); err != nil {
			return err
		}
	}
	return nil
}

// CheckGroupGroupRatio 校验交叉倍率表 GroupGroupRatio[用户分组][模型分组]。
//
// ── 为什么它必须和 CheckGroupRatio 一样存在 ──
//
// 交叉倍率是全站唯一「按用户分组定价」的载体,却一度是两张表里**没有任何数值
// 校验**的那一张:GroupRatio 在 controller 层有 CheckGroupRatio 挡着,
// GroupGroupRatio 一条 case 都没有。于是从原始 option 接口写进一个负倍率,
// ResolveGroupRatio 会原样返回它,预扣和结算都是负数 —— 等于给用户充值。
//
// 解析失败也必须在**落库之前**报出来:上游 UpdateOption 是「先 DB.Save,
// 后 updateOptionMap」,坏值先持久化,重启也不自愈。
func CheckGroupGroupRatio(jsonStr string) error {
	checkGroupGroupRatio := make(map[string]map[string]float64)
	if err := json.Unmarshal([]byte(jsonStr), &checkGroupGroupRatio); err != nil {
		return err
	}
	for userGroup, inner := range checkGroupGroupRatio {
		for modelGroup, ratio := range inner {
			if err := checkRatioValue(userGroup+"/"+modelGroup, ratio); err != nil {
				return err
			}
		}
	}
	return nil
}
