package setting

import (
	"fmt"
	"math"
	"sync"

	"github.com/QuantumNous/new-api/common"
)

var ModelRequestRateLimitEnabled = false
var ModelRequestRateLimitDurationMinutes = 1
var ModelRequestRateLimitCount = 0
var ModelRequestRateLimitSuccessCount = 1000
var ModelRequestRateLimitGroup = map[string][2]int{}
var ModelRequestRateLimitMutex sync.RWMutex

// ModelRequestConcurrencyGroup 是「同一分组同时最多有几个请求在飞」。
//
// 它与上面那张 RPM 表是**两件事**,刻意分开存:
//   - RPM 是固定窗口的**计数**,一分钟内准入多少次,与每次跑多久无关;
//   - 并发是**在途请求数**,与每次跑多久强相关。
//
// 一个跑 5 分钟的长上下文请求在 RPM 表里只占一次计数,却会实打实占住上游一条
// 连接整整 5 分钟。只配 RPM 的站点,20 个这样的请求就能把上游打满,而限流面板
// 上显示「远未达到上限」—— 那正是运营最难自己想明白的一种超卖。
//
// 0 = 不限制,与 RPM 表的口径一致。
var ModelRequestConcurrencyGroup = map[string]int{}

func ModelRequestRateLimitGroup2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := common.Marshal(ModelRequestRateLimitGroup)
	if err != nil {
		common.SysLog("error marshalling model request rate limit group: " + err.Error())
	}
	return string(jsonBytes)
}

// UpdateModelRequestRateLimitGroupByJSONString 整体替换分组限流表。
//
// 这里必须是 Lock 而不是 RLock:它**写** ModelRequestRateLimitGroup(先换成一张
// 新 map,再由 Unmarshal 往里填)。原先用的是 RLock —— 两个读锁互不排斥,于是
// 管理员按下保存的那一瞬,与线上每一个 relay 请求里的 GetGroupRateLimit 并发
// 读写同一张 map,是确凿的数据竞争(Go 运行时对并发读写 map 会直接 fatal,
// 不是"偶尔读到旧值"这种可以将就的形态)。
func UpdateModelRequestRateLimitGroupByJSONString(jsonStr string) error {
	next := make(map[string][2]int)
	// 先解析到局部变量再换上去:解析失败时那张表必须保持原样,
	// 而不是被清空成一张空表(那会让全站分组限流静默失效)。
	if err := common.UnmarshalJsonStr(jsonStr, &next); err != nil {
		return err
	}

	ModelRequestRateLimitMutex.Lock()
	defer ModelRequestRateLimitMutex.Unlock()
	ModelRequestRateLimitGroup = next
	return nil
}

func GetGroupRateLimit(group string) (totalCount, successCount int, found bool) {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	if ModelRequestRateLimitGroup == nil {
		return 0, 0, false
	}

	limits, found := ModelRequestRateLimitGroup[group]
	if !found {
		return 0, 0, false
	}
	return limits[0], limits[1], true
}

func CheckModelRequestRateLimitGroup(jsonStr string) error {
	checkModelRequestRateLimitGroup := make(map[string][2]int)
	err := common.UnmarshalJsonStr(jsonStr, &checkModelRequestRateLimitGroup)
	if err != nil {
		return err
	}
	for group, limits := range checkModelRequestRateLimitGroup {
		if limits[0] < 0 || limits[1] < 1 {
			return fmt.Errorf("group %s has negative rate limit values: [%d, %d]", group, limits[0], limits[1])
		}
		if limits[0] > math.MaxInt32 || limits[1] > math.MaxInt32 {
			return fmt.Errorf("group %s [%d, %d] has max rate limits value 2147483647", group, limits[0], limits[1])
		}
	}

	return nil
}

// ─────────────── 分组并发上限(与 RPM 表共用同一把锁)───────────────

func ModelRequestConcurrencyGroup2JSONString() string {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	jsonBytes, err := common.Marshal(ModelRequestConcurrencyGroup)
	if err != nil {
		common.SysLog("error marshalling model request concurrency group: " + err.Error())
	}
	return string(jsonBytes)
}

func UpdateModelRequestConcurrencyGroupByJSONString(jsonStr string) error {
	next := make(map[string]int)
	if err := common.UnmarshalJsonStr(jsonStr, &next); err != nil {
		return err
	}

	ModelRequestRateLimitMutex.Lock()
	defer ModelRequestRateLimitMutex.Unlock()
	ModelRequestConcurrencyGroup = next
	return nil
}

// GetGroupConcurrencyLimit 返回该分组的在途请求上限。0 / 未配置 = 不限。
func GetGroupConcurrencyLimit(group string) int {
	ModelRequestRateLimitMutex.RLock()
	defer ModelRequestRateLimitMutex.RUnlock()

	if ModelRequestConcurrencyGroup == nil {
		return 0
	}
	return ModelRequestConcurrencyGroup[group]
}

// CheckModelRequestConcurrencyGroup 校验并发表。
//
// 上界取 MaxInt32 与 RPM 表一致;下界拒负数。**0 是合法的**,它的意思是
// "这一档不限并发",与整张表里没有这个键等价 —— 运营把一档临时调成不限时
// 不必去删那一行。
func CheckModelRequestConcurrencyGroup(jsonStr string) error {
	next := make(map[string]int)
	if err := common.UnmarshalJsonStr(jsonStr, &next); err != nil {
		return err
	}
	for group, limit := range next {
		if limit < 0 {
			return fmt.Errorf("group %s has negative concurrency limit: %d", group, limit)
		}
		if limit > math.MaxInt32 {
			return fmt.Errorf("group %s concurrency limit %d exceeds 2147483647", group, limit)
		}
	}
	return nil
}
