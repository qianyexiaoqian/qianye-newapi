package setting

import (
	"encoding/json"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var userUsableGroups = map[string]string{
	"default": "默认分组",
	"vip":     "vip分组",
}
var userUsableGroupsMutex sync.RWMutex

// GetUserUsableGroupsCopy 返回全局「用户可选分组」白名单的一份副本。
//
// value 经 QyDescribeGroup 归一:模型分组备注非空时覆盖白名单里那段说明,
// 否则逐位沿用。默认实现恒返回空串 ⇒ 扩展未启用时这一步是空操作。
// 优先级的完整理由见 setting/qy_groupnote_export.go —— 只在这一处应用,
// 是为了让全站(令牌下拉、模型广场、价格页、权威清单)拿到同一段文案。
func GetUserUsableGroupsCopy() map[string]string {
	userUsableGroupsMutex.RLock()
	defer userUsableGroupsMutex.RUnlock()

	copyUserUsableGroups := make(map[string]string)
	for k, v := range userUsableGroups {
		copyUserUsableGroups[k] = QyDescribeGroup(k, v)
	}
	return copyUserUsableGroups
}

// RawUserUsableGroupsCopy 返回**未经备注覆盖**的白名单副本。
//
// 只给管理端用:模型分组登记页要同屏显示"白名单原文"与"备注",运营才看得出
// 哪一段被覆盖了。任何面向用户的读取路径都必须走 GetUserUsableGroupsCopy。
func RawUserUsableGroupsCopy() map[string]string {
	userUsableGroupsMutex.RLock()
	defer userUsableGroupsMutex.RUnlock()

	out := make(map[string]string, len(userUsableGroups))
	for k, v := range userUsableGroups {
		out[k] = v
	}
	return out
}

func UserUsableGroups2JSONString() string {
	userUsableGroupsMutex.RLock()
	defer userUsableGroupsMutex.RUnlock()

	jsonBytes, err := json.Marshal(userUsableGroups)
	if err != nil {
		common.SysLog("error marshalling user groups: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateUserUsableGroupsByJSONString(jsonStr string) error {
	userUsableGroupsMutex.Lock()
	defer userUsableGroupsMutex.Unlock()

	userUsableGroups = make(map[string]string)
	return json.Unmarshal([]byte(jsonStr), &userUsableGroups)
}

// GetUsableGroupDescription 给出一个模型分组的说明文案。
//
// 与 GetUserUsableGroupsCopy 同一条优先级(备注 > 白名单 value > 分组名),
// 两处必须同源:它们分别服务"分组在白名单里"与"分组只由权威清单/套餐解锁"
// 两条路径,给出不同答案会让同一个分组在两个页面上显示两段说明。
func GetUsableGroupDescription(groupName string) string {
	userUsableGroupsMutex.RLock()
	desc, ok := userUsableGroups[groupName]
	userUsableGroupsMutex.RUnlock()

	if !ok {
		desc = groupName
	}
	return QyDescribeGroup(groupName, desc)
}
