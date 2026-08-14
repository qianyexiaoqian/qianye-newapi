package setting

// token_default_group.go —— 「令牌创建时按用户分组预选哪个模型分组」。
//
// ═════════════════════════ 它与另外两个「默认」的区别 ═════════════════════════
//
// 站内一共有三处叫「默认分组」的东西,语义各不相同,配错地方的表现都是
// 「配了没反应」,因此这里一次讲清:
//
//	本文件                              令牌创建**界面**的预选值。纯 UI 建议,
//	                                    用户可以当场改掉;不改变任何已存在的令牌。
//
//	qy_user_groups.default_model_group  **请求期**的解析(qianye/modules/groupns)。
//	(default_mode = pin/deny/inherit)   只作用于 group 为空的令牌,在
//	                                    middleware/auth.go 上决定这一笔用哪个模型分组。
//
//	setting.AutoGroups / DefaultUseAutoGroup
//	                                    auto 自动分组的候选池与「新建令牌是否默认勾选
//	                                    auto」,与具体选哪个模型分组无关。
//
// 三者刻意不合并:前两者的作用对象不同(新建界面 vs 存量空分组令牌),
// 合并会让「我只想让新令牌默认选 X,不想动线上那 200 个空分组令牌」表达不出来。
//
// ═════════════════════════ 为什么不做写入期的存在性校验 ═════════════════════════
//
// 「这个模型分组存不存在」是全局判据,而「这个用户能不能选它」是 per-user 判据 ——
// 同一个 X 对用户分组 A 合法、对 B 非法完全正常(权威可选清单就是干这个的)。
// 写入期只能校验前者,校验通过也不代表任何一个用户真的能看到它。
//
// 因此存在性判定**只在读取期做一次**,判据就是该用户此刻真实的可选清单
// (controller.GetUserGroups 里那张已经算好的 map)。落在清单外时静默降级成
// 「没有默认值」,而不是把一个用户根本选不了的分组预选进他的下拉框 ——
// 后者的表现是用户一提交就被写入侧校验拒绝,而他没动过那一栏。
//
// 写入期只校验**结构**(见 ValidateTokenDefaultGroups):键值都不许是空串。
// 空键无法对应任何用户分组,空值等价于没配,两者留在库里只会让运营以为配过了。

import (
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// tokenDefaultGroups 是「用户分组 → 模型分组」的预选映射。
//
// key 是 users.group(用户分组),value 是模型分组名(可以是 "auto")。
// 未配置的用户分组不出现在 map 里 —— 缺键即「没有默认值」,与上游行为一致。
var tokenDefaultGroups = map[string]string{}
var tokenDefaultGroupsMutex sync.RWMutex

// GetTokenDefaultGroup 返回某个用户分组的预选模型分组,未配置时返回空串。
//
// 只回答"配了什么",**不回答"这个用户能不能选"** —— 后者由调用方拿真实可选
// 清单判定(理由见文件头)。调用方必须做那一步,否则会把一个用户选不了的分组
// 预选进他的下拉框。
func GetTokenDefaultGroup(userGroup string) string {
	if userGroup == "" {
		return ""
	}
	tokenDefaultGroupsMutex.RLock()
	defer tokenDefaultGroupsMutex.RUnlock()
	return tokenDefaultGroups[userGroup]
}

// TokenDefaultGroupsCopy 返回全量映射的副本。
//
// **只给管理端用。** 这份映射的键集合等于「本站有哪些用户分组」,把它下发给
// 普通用户等于把全站的分档结构暴露给每一个登录账号 —— 与
// web/src/features/keys/lib/group-options.ts 里记的是同一条理由。
func TokenDefaultGroupsCopy() map[string]string {
	tokenDefaultGroupsMutex.RLock()
	defer tokenDefaultGroupsMutex.RUnlock()

	out := make(map[string]string, len(tokenDefaultGroups))
	for k, v := range tokenDefaultGroups {
		out[k] = v
	}
	return out
}

func TokenDefaultGroups2JSONString() string {
	tokenDefaultGroupsMutex.RLock()
	defer tokenDefaultGroupsMutex.RUnlock()

	jsonBytes, err := common.Marshal(tokenDefaultGroups)
	if err != nil {
		common.SysLog("error marshalling token default groups: " + err.Error())
		return "{}"
	}
	return string(jsonBytes)
}

// ValidateTokenDefaultGroups 校验待保存的 JSON 结构。
//
// 在 model/option.go 的校验开关里被调用,失败时管理端保存直接报错 ——
// 而不是存进去之后变成一份永远不生效的配置。
func ValidateTokenDefaultGroups(jsonStr string) error {
	parsed := make(map[string]string)
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return fmt.Errorf("令牌默认分组配置不是合法的 JSON 对象: %w", err)
	}
	for userGroup, modelGroup := range parsed {
		if userGroup == "" {
			return fmt.Errorf("令牌默认分组配置里存在空的用户分组名 —— 它对应不到任何用户")
		}
		if modelGroup == "" {
			return fmt.Errorf("用户分组 %q 的默认模型分组为空 —— "+
				"想取消默认请直接删掉这一项,留一个空值只会让人以为配过了", userGroup)
		}
	}
	return nil
}

// UpdateTokenDefaultGroupsByJSONString 覆盖整份映射。
//
// 先解析到临时 map 再整体替换:直接往 tokenDefaultGroups 上 Unmarshal 的话,
// 一份中途解析失败的 JSON 会留下一张半新半旧的映射,而调用方拿到 error 之后
// 通常只会把它记进日志。
func UpdateTokenDefaultGroupsByJSONString(jsonStr string) error {
	parsed := make(map[string]string)
	if err := common.UnmarshalJsonStr(jsonStr, &parsed); err != nil {
		return err
	}
	tokenDefaultGroupsMutex.Lock()
	defer tokenDefaultGroupsMutex.Unlock()
	tokenDefaultGroups = parsed
	return nil
}
