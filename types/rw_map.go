package types

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

type RWMap[K comparable, V any] struct {
	data  map[K]V
	mutex sync.RWMutex
}

// UnmarshalJSON 先解析到一份临时 map,解析成功才整体换上去。
//
// 顺序反过来(先清空再解析)会让一次**格式非法**的输入把整张表清空:上游
// options 的写入路径是「先 DB.Save,后 updateOptionMap」,于是坏值先落库、
// 内存后被清空,重启也不自愈。分组倍率表被这样清空一次,全站所有用户分组
// 谈好的价当场消失、每一笔回落兜底价,而且 BaseMissing=false ⇒ 一条告警都不发。
// 见 setting/ratio_setting/qy_ratio_export.go 的 SilentFallback。
func (m *RWMap[K, V]) UnmarshalJSON(b []byte) error {
	parsed := make(map[K]V)
	if err := common.Unmarshal(b, &parsed); err != nil {
		return err
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = parsed
	return nil
}

func (m *RWMap[K, V]) MarshalJSON() ([]byte, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return common.Marshal(m.data)
}

func NewRWMap[K comparable, V any]() *RWMap[K, V] {
	return &RWMap[K, V]{
		data: make(map[K]V),
	}
}

func (m *RWMap[K, V]) Get(key K) (V, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	value, exists := m.data[key]
	return value, exists
}

func (m *RWMap[K, V]) Set(key K, value V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data[key] = value
}

func (m *RWMap[K, V]) AddAll(other map[K]V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for k, v := range other {
		m.data[k] = v
	}
}

func (m *RWMap[K, V]) Clear() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[K]V)
}

func (m *RWMap[K, V]) ReadAll() map[K]V {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	copiedMap := make(map[K]V)
	for k, v := range m.data {
		copiedMap[k] = v
	}
	return copiedMap
}

func (m *RWMap[K, V]) Len() int {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return len(m.data)
}

// LoadFromJsonString 整表替换,**解析失败时一个字节都不改**。
//
// 与 UnmarshalJSON 同一条理由:这是倍率表、价格表、可选分组表的共同装载口,
// 「先清空再解析」会把一次 JSON 语法错误放大成一次全站定价事故。
func LoadFromJsonString[K comparable, V any](m *RWMap[K, V], jsonStr string) error {
	parsed := make(map[K]V)
	if err := common.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return err
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = parsed
	return nil
}

func LoadFromJsonStringWithCallback[K comparable, V any](m *RWMap[K, V], jsonStr string, onSuccess func()) error {
	parsed := make(map[K]V)
	if err := common.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return err
	}
	m.mutex.Lock()
	m.data = parsed
	m.mutex.Unlock()
	if onSuccess != nil {
		onSuccess()
	}
	return nil
}

func (m *RWMap[K, V]) MarshalJSONString() string {
	bytes, err := m.MarshalJSON()
	if err != nil {
		return "{}"
	}
	return string(bytes)
}
