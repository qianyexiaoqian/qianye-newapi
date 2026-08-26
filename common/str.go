package common

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"math"
	"strconv"
	"strings"
	"unsafe"

	"github.com/samber/lo"
)

const LocalLogContentLimit = 2048

// LocalLogPreview limits log-only content unless debug logging is enabled.
func LocalLogPreview(content string) string {
	if DebugEnabled || len(content) <= LocalLogContentLimit {
		return content
	}
	return fmt.Sprintf("%s... [truncated, original_length=%d, limit=%d]", content[:LocalLogContentLimit], len(content), LocalLogContentLimit)
}

func GetStringIfEmpty(str string, defaultValue string) string {
	if str == "" {
		return defaultValue
	}
	return str
}

func GetRandomString(length int) string {
	if length <= 0 {
		return ""
	}
	return lo.RandomString(length, lo.AlphanumericCharset)
}

func MapToJsonStr(m map[string]interface{}) string {
	bytes, err := json.Marshal(m)
	if err == nil {
		return string(bytes)
	}
	// 一个编不出来的值不许把整张 map 抹掉。
	//
	// 这个函数的主要调用方是 model.RecordConsumeLog / RecordTaskBillingLog 的
	// `other` 列 —— 计费上下文(model_ratio / group_ratio / cache_ratio /
	// use_channel)与钳制审计标记都住在那里。encoding/json 遇到 ±Inf / NaN
	// 会对**整张 map** 返回 UnsupportedValueError,原先直接 return "" 于是
	// 让整条日志的计费上下文一起消失,而这类日志恰恰是异常请求的那一条,
	// 最需要看的就是它。落库侧 createLog 又不检查空串,所以静默无声。
	//
	// 退而求其次:把非有限浮点换成它的字符串形式再编一次。仍然失败(别的
	// 不可编码类型)才返回空串,与原行为一致。
	if sanitized, changed := sanitizeJSONValue(m); changed {
		if bytes, err = json.Marshal(sanitized); err == nil {
			return string(bytes)
		}
	}
	return ""
}

// sanitizeJSONValue 递归地把 ±Inf / NaN 换成字符串,返回是否真的改过。
func sanitizeJSONValue(v interface{}) (interface{}, bool) {
	switch val := v.(type) {
	case float64:
		if math.IsInf(val, 0) || math.IsNaN(val) {
			return strconv.FormatFloat(val, 'g', -1, 64), true
		}
	case float32:
		f := float64(val)
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return strconv.FormatFloat(f, 'g', -1, 32), true
		}
	case map[string]interface{}:
		changed := false
		out := make(map[string]interface{}, len(val))
		for k, item := range val {
			fixed, itemChanged := sanitizeJSONValue(item)
			out[k] = fixed
			changed = changed || itemChanged
		}
		if changed {
			return out, true
		}
	case []interface{}:
		changed := false
		out := make([]interface{}, len(val))
		for i, item := range val {
			fixed, itemChanged := sanitizeJSONValue(item)
			out[i] = fixed
			changed = changed || itemChanged
		}
		if changed {
			return out, true
		}
	}
	return v, false
}

func StrToMap(str string) (map[string]interface{}, error) {
	m := make(map[string]interface{})
	err := Unmarshal([]byte(str), &m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func StrToJsonArray(str string) ([]interface{}, error) {
	var js []interface{}
	err := json.Unmarshal([]byte(str), &js)
	if err != nil {
		return nil, err
	}
	return js, nil
}

func IsJsonArray(str string) bool {
	var js []interface{}
	return json.Unmarshal([]byte(str), &js) == nil
}

func IsJsonObject(str string) bool {
	var js map[string]interface{}
	return json.Unmarshal([]byte(str), &js) == nil
}

func String2Int(str string) int {
	num, err := strconv.Atoi(str)
	if err != nil {
		return 0
	}
	return num
}

func StringsContains(strs []string, str string) bool {
	for _, s := range strs {
		if s == str {
			return true
		}
	}
	return false
}

// StringToByteSlice []byte only read, panic on append
func StringToByteSlice(s string) []byte {
	tmp1 := (*[2]uintptr)(unsafe.Pointer(&s))
	tmp2 := [3]uintptr{tmp1[0], tmp1[1], tmp1[1]}
	return *(*[]byte)(unsafe.Pointer(&tmp2))
}

func EncodeBase64(str string) string {
	return base64.StdEncoding.EncodeToString([]byte(str))
}

func GetJsonString(data any) string {
	if data == nil {
		return ""
	}
	b, _ := json.Marshal(data)
	return string(b)
}

// MaskEmail masks a user email to prevent PII leakage in logs
// Returns "***masked***" if email is empty, otherwise shows only the domain part
func MaskEmail(email string) string {
	if email == "" {
		return "***masked***"
	}

	// Find the @ symbol
	atIndex := strings.Index(email, "@")
	if atIndex == -1 {
		// No @ symbol found, return masked
		return "***masked***"
	}

	// Return only the domain part with @ symbol
	return "***@" + email[atIndex+1:]
}

// MaskCredential 把一段仍然有效的凭据(兑换码、令牌、密钥)压成只够对号入座的
// 尾巴,供日志与错误文本使用。
//
// 日志的读者面比数据库宽得多:文件、容器 stdout、日志采集平台、日志备份。
// 一串还没被兑换的兑换码落进这些地方,等于把它的面值送给任何有日志读取权的人。
// 但把它整段抹成 "***" 又会让客服无法把用户报的那张码与日志里的那一行对上,
// 于是保留末 4 位:32 位十六进制码丢掉 16 bit 之后仍有约 106 bit 不可猜,
// 而"末四位 1a2b 那张"足够定位到唯一一行。
//
// 短于 8 个字符的值一个字符都不露:那种长度的凭据本来就撑不住部分泄漏。
func MaskCredential(secret string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) < 8 {
		return "***"
	}
	return "***" + secret[len(secret)-4:]
}

// MaskSensitiveInfo moved to the conversion kit (kitutil) because the types
// package error formatting depends on it; host callers keep this name.
func MaskSensitiveInfo(str string) string {
	return kitutil.MaskSensitiveInfo(str)
}
