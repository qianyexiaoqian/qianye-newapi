package service

// qy_logmetrics_export.go —— 千夜扩展「使用日志两列」与上游 service 包之间的唯一耦合面。
//
// 这是一个纯新增文件:它的存在不需要修改任何上游既有文件,因此合并上游时冲突为 0。
// 因为与调用点同包,上游既有文件里只需插入一行普通调用,连 import 都不必改 ——
// 这是把改动压到最小的关键(见 qianye/docs/99-coherence-review.md 裁定 C5)。
//
// 铁律:本文件禁止 import 任何 qianye/* 包。service 是被扩展依赖的下层包,
// 反向依赖会形成 import 环。实现体由 qianye/modules/logmetrics 在
// qianye.Init() 阶段注入,那一刻早于任何 HTTP 请求与后台协程,不存在并发读写窗口。
//
// 默认实现是空操作,因此调用点不需要 nil 判断;扩展未安装时行为与上游逐字节一致。

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

var (
	// QyLogMetricsAttachReasoning 在消费日志的 other 构造完基础字段后触发,
	// 用于补齐「推理强度」列所需的归一化数据。
	//
	// 挂载点 GenerateTextOtherInfo 是文本/音频/实时/Claude 四条计费路径的共同入口,
	// 一处挂载即可全覆盖。
	//
	// 实现约定:纯内存 map 写入,禁止任何 DB / Redis / 网络 IO ——
	// 它跑在 relay 的同步结算链路上;并且必须自行吞掉 panic,
	// 因为 panic 逃逸会让整条计费日志丢失。
	QyLogMetricsAttachReasoning = func(c *gin.Context, relayInfo *relaycommon.RelayInfo, other map[string]interface{}) {
	}

	// QyLogMetricsAttachCacheBasis 在缓存相关字段全部写完、日志落库之前触发,
	// 把「缓存百分比」的权威分母固化进 other。
	//
	// 之所以必须挂在这里而不是旁路中间件:isClaudeUsageSemantic 决定了
	// prompt_tokens 是否已经包含 cached_tokens,而这个判别只有 relay 内部才拿得到。
	// 分母算错会得到一个看起来合理却是错的百分比,比不显示更糟。
	QyLogMetricsAttachCacheBasis = func(other map[string]interface{}, promptTokens, cacheReadTokens, cacheWriteTokens int, isClaudeUsageSemantic bool) {
	}
)
