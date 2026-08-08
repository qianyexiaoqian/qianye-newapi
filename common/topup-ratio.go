package common

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
)

var topupGroupRatio = map[string]float64{
	"default": 1,
	"vip":     1,
	"svip":    1,
}
var topupGroupRatioMutex sync.RWMutex

func TopupGroupRatio2JSONString() string {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	jsonBytes, err := json.Marshal(topupGroupRatio)
	if err != nil {
		SysError("error marshalling topup group ratio: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateTopupGroupRatioByJSONString(jsonStr string) error {
	topupGroupRatioMutex.Lock()
	defer topupGroupRatioMutex.Unlock()
	topupGroupRatio = make(map[string]float64)
	return json.Unmarshal([]byte(jsonStr), &topupGroupRatio)
}

// MaxTopupGroupRatio 是充值倍率的上界。它被直接乘进 controller/topup.go 的
// payMoney,一次手滑填成 1e18 就会把金额推过 decimal → float 的可用范围。
const MaxTopupGroupRatio = 1000

// CheckTopupGroupRatio 是充值倍率**落库之前**的校验。
//
// ══════════ 为什么这道闸门必须在 model.UpdateOption 上,而不是只在扩展侧 ══════════
//
// 扩展侧的 PUT /group-namespace/user-groups/:name 已经有一道
// groupns.validateTopupRatio,但那不是这个值唯一的入口:通用的
// PUT /api/option(以及系统设置里的 JSON 抽屉)可以直接提交整份 TopupGroupRatio,
// 而 validateOptionValue 此前对这个键一条 case 都没有。
// 一条 `{"vip":-5}` 因此可以先落库、再进内存,随后 vip 用户充 100 元得到
// payMoney = -500 —— 一张负价订单,正是扩展侧那道闸门声称要防的那一种。
//
// 拒绝 0 的理由与 groupns.validateTopupRatio 相同,而且更硬:四条支付路径读到 0
// 之后一律 `if ratio == 0 { ratio = 1 }`,所以 0 在库里既不免费也不打折,
// 只是让"配置值"和"收款值"分家。
func CheckTopupGroupRatio(jsonStr string) error {
	if jsonStr == "" {
		return nil
	}
	parsed := map[string]float64{}
	if err := Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return fmt.Errorf("充值倍率不是合法的 JSON 对象: %w", err)
	}
	for name, ratio := range parsed {
		switch {
		case math.IsNaN(ratio) || math.IsInf(ratio, 0):
			return fmt.Errorf("用户分组 %q 的充值倍率不是一个有限数值", name)
		case ratio < 0:
			return fmt.Errorf("用户分组 %q 的充值倍率是 %v —— 负倍率会把充值变成一张负价订单", name, ratio)
		case ratio == 0:
			return fmt.Errorf("用户分组 %q 的充值倍率是 0。0 在充值这一侧不等于免费:"+
				"支付路径读到 0 之后按 1 收款,而不抬的话订单会因为金额低于 0.01 被拒。"+
				"要按原价收款请把这个键删掉", name)
		case ratio > MaxTopupGroupRatio:
			return fmt.Errorf("用户分组 %q 的充值倍率 %v 超过上限 %d", name, ratio, MaxTopupGroupRatio)
		}
	}
	return nil
}

func GetTopupGroupRatio(name string) float64 {
	topupGroupRatioMutex.RLock()
	defer topupGroupRatioMutex.RUnlock()
	ratio, ok := topupGroupRatio[name]
	if !ok {
		SysError("topup group ratio not found: " + name)
		return 1
	}
	return ratio
}
