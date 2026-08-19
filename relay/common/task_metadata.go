package common

import "strings"

// task 的 metadata 是一个纯透传袋:适配器把它按 JSON 合并进已经装配好的上游
// 请求体,合并时**不区分**哪些字段是普通参数、哪些字段决定了「这次到底用哪个
// 模型」。而计费与鉴权只认请求体顶层的 model 字段
// (middleware/distributor.go 用 gjson 只取 "model"),于是 metadata 里换掉的
// 那个模型既不进价格表、也不进渠道 models / 令牌 model_limits / 分组 abilities。
// 用户点便宜模型的价、拿贵模型的货。
//
// 各家上游对「模型」这个概念的字段名并不统一,所以黑名单必须按**语义**列全,
// 而不是只列 "model" 一个键:
//   - model      —— ali / doubao / hailuo / vidu / sora
//   - model_name —— kling(快手 Kling 认的就是这个字段)
//   - req_key    —— jimeng(火山即梦用 req_key 选模型)
//
// 新增适配器时,只要它的模型字段落在这张表外,relay/channel/task/taskcommon
// 的 TestModelSelectionKeysCoverEveryTaskAdaptor 会当场变红。
var modelSelectionMetadataKeys = []string{
	"model",
	"model_name",
	"modelName",
	"req_key",
	"reqKey",
}

// ModelSelectionMetadataKeys 返回模型选择字段名的副本(供守卫测试与文档使用)。
func ModelSelectionMetadataKeys() []string {
	keys := make([]string, len(modelSelectionMetadataKeys))
	copy(keys, modelSelectionMetadataKeys)
	return keys
}

// StripModelSelectionMetadata 就地删除 metadata 里所有能改写模型选择的键。
// 删除而不是报错:metadata 常被客户端整包回传(kling 原生路由就把整个原始 body
// 塞进 metadata),报错会打断合法调用;而删掉之后适配器会用鉴权/计费认定的那个
// 模型把字段重新写回去,语义上等价于「这个字段不接受用户输入」。
// 大小写必须一起删:encoding/json 在找不到精确匹配时会退回**大小写不敏感**的
// 字段匹配,于是 {"MODEL_NAME":"..."} 照样能填进 ModelName —— 只按精确键名
// delete 等于给这条旁路留了一扇门。
func StripModelSelectionMetadata(metadata map[string]any) {
	if len(metadata) == 0 {
		return
	}
	for key := range metadata {
		for _, reserved := range modelSelectionMetadataKeys {
			if strings.EqualFold(key, reserved) {
				delete(metadata, key)
				break
			}
		}
	}
}
