# 中转链路与违规检测挂载点

## 【已勘察范围】relay 中转链路 · 违规检测挂载点

---

## 1. 一个 OpenAI chat completion 请求的完整链路

### 1.1 全局中间件（`gin.New()` 之后，所有路由生效）
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\main.go`

| 顺序 | 行 | 调用 |
|---|---|---|
| 1 | `main.go:174` | `server := gin.New()` |
| 2 | `main.go:175` | `middleware.ConfigureTrustedProxies(server)` |
| 3 | `main.go:179` | `gin.CustomRecovery(...)` |
| 4 | `main.go:190` | `server.Use(middleware.RequestId())` |
| 5 | `main.go:191` | `server.Use(middleware.Version())` |
| 6 | `main.go:192` | `server.Use(middleware.I18n())` |
| 7 | `main.go:193` | `middleware.SetUpLogger(server)` → 内部 `server.Use(gin.LoggerWithFormatter(...))` |
| 8 | `main.go:198` | `router.SetRouter(server, ...)` |

`router/main.go:16-19` 的调用顺序：`SetApiRouter` → `SetDashboardRouter` → **`SetRelayRouter`** → `SetVideoRouter` → `SetWebRouter`。

### 1.2 relay 路由链
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\router\relay-router.go`

```go
func SetRelayRouter(router *gin.Engine) {        // :13
	router.Use(middleware.CORS())                     // :14
	router.Use(middleware.DecompressRequestMiddleware()) // :15
	router.Use(middleware.BodyStorageCleanup())       // :16  ← 请求结束清理 body 存储
	router.Use(middleware.StatsMiddleware())          // :17
	...
	relayV1Router := router.Group("/v1")              // :69
	relayV1Router.Use(middleware.RouteTag("relay"))   // :70
	relayV1Router.Use(middleware.SystemPerformanceCheck()) // :71
	relayV1Router.Use(middleware.TokenAuth())         // :72  ← 此后 user_id/token_id/group 已进 Context
	relayV1Router.Use(middleware.ModelRequestRateLimit()) // :73
	{
		httpRouter := relayV1Router.Group("")         // :84
		httpRouter.Use(middleware.Distribute())       // :85  ← 选渠道，解析 body 取 model
		httpRouter.POST("/chat/completions", func(c *gin.Context) {  // :96
			controller.Relay(c, types.RelayFormatOpenAI)
		})
	}
}
```

> 注意：`:14-17` 的四个 `router.Use()` 是挂在 **`*gin.Engine` 根组**上的，因此也会作用于之后注册的 `SetVideoRouter` / `SetWebRouter`，但不作用于之前注册的 `SetApiRouter` / `SetDashboardRouter`。

### 1.3 完整调用栈（按顺序）

| # | file:line | 函数 | 说明 |
|---|---|---|---|
| 1 | `middleware/request-id.go:10` | `RequestId()` | 写 `common.RequestIdKey` |
| 2 | `middleware/i18n.go:13` | `I18n()` | |
| 3 | `middleware/cors.go:9` | `CORS()` | |
| 4 | `middleware/gzip.go:25` | `DecompressRequestMiddleware()` | 解 gzip/br 请求体 |
| 5 | `middleware/body_cleanup.go:11` | `BodyStorageCleanup()` | `c.Next()` 后清理 |
| 6 | `middleware/stats.go:17` | `StatsMiddleware()` | |
| 7 | `middleware/logger.go:12` | `RouteTag("relay")` | |
| 8 | `middleware/performance.go:14` | `SystemPerformanceCheck()` | CPU/内存/磁盘阈值 |
| 9 | `middleware/auth.go:352` | `TokenAuth()` | 校验 sk-，`SetupContextForToken` (`middleware/auth.go:485`) |
| 10 | `middleware/model-rate-limit.go:168` | `ModelRequestRateLimit()` | |
| 11 | `middleware/distributor.go:33` | `Distribute()` | `getModelRequest` (`:253`) → `getModelFromJSONBody` (`:209`, gjson 取 model/group)；选渠道；`SetupContextForSelectedChannel` (`:444`)；`:164` 写 `ContextKeyRequestStartTime` |
| 12 | `router/relay-router.go:96` | 闭包 | `controller.Relay(c, types.RelayFormatOpenAI)` |
| 13 | **`controller/relay.go:71`** | `Relay(c, relayFormat)` | 主编排 |
| 14 | `controller/relay.go:112` | `helper.GetAndValidateRequest(c, relayFormat)` | → `relay/helper/valid_request.go:21` |
| 15 | `relay/helper/valid_request.go:312` | `GetAndValidateTextRequest(c, relayMode)` | `:314` `common.UnmarshalBodyReusable(c, textRequest)` ← **DTO 解析最早点** |
| 16 | `controller/relay.go:123` | `relaycommon.GenRelayInfo(...)` | → `relay/common/relay_info.go:548` → `genBaseRelayInfo` (`:444`) |
| 17 | **`controller/relay.go:129-146`** | **现有敏感词检查块** | 见 §6 |
| 18 | `controller/relay.go:148` | `service.EstimateRequestToken(c, meta, relayInfo)` | |
| 19 | `controller/relay.go:156` | `helper.ModelPriceHelper(...)` | |
| 20 | `controller/relay.go:167` | `service.PreConsumeBilling(...)` | 预扣费 |
| 21 | `controller/relay.go:194` | `for retry` 循环开始 | `getChannel` (`:296`)、`common.GetBodyStorage` (`:204`) |
| 22 | `controller/relay.go:224` | `relayHandler(c, relayInfo)` | → `controller/relay.go:36` switch by `info.RelayMode` |
| 23 | **`relay/compatible_handler.go:25`** | `relay.TextHelper(c, info)` | default 分支 |
| 24 | `relay/compatible_handler.go:26` | `info.InitChannelMeta(c)` | → `relay/common/relay_info.go:194` |
| 25 | `relay/compatible_handler.go:42` | `helper.ModelMappedHelper(c, info, request)` | |
| 26 | `relay/compatible_handler.go:67` | `adaptor := GetAdaptor(info.ApiType)` | → `relay/relay_adaptor.go:56` |
| 27 | `relay/compatible_handler.go:109` | `adaptor.ConvertOpenAIRequest(c, info, request)` | → `relay/channel/openai/adaptor.go:244` |
| 28 | `relay/compatible_handler.go:157` | `common.Marshal(convertedRequest)` | **上游 body 生成点** |
| 29 | `relay/compatible_handler.go:163` | `relaycommon.RemoveDisabledFields(...)` | |
| 30 | `relay/compatible_handler.go:170` | `relaycommon.ApplyParamOverrideWithRelayInfo(...)` | |
| 31 | `relay/compatible_handler.go:189` | `adaptor.DoRequest(c, info, requestBody)` | → `relay/channel/openai/adaptor.go:623` → `channel.DoApiRequest` (`relay/channel/api_request.go:307`) ← **真正发出 HTTP 的最后一刻** |
| 32 | `relay/compatible_handler.go:207` | `adaptor.DoResponse(c, httpResp, info)` | → `relay/channel/openai/adaptor.go:635` |
| 33a | `relay/channel/openai/relay-openai.go:104` | `OaiStreamHandler` | **流式** |
| 33b | `relay/channel/openai/relay-openai.go:222` | `OpenaiHandler` | **非流式** |
| 34 | `relay/compatible_handler.go:218/220` | `service.PostTextConsumeQuota(c, info, usage, nil)` | → `service/text_quota.go:397` ← **所有文本类 relay 的统一结算收口** |

---

## 2. 中间件清单与插入位置

`middleware/` 下全部导出中间件（`C:\Users\Administrator\Desktop\qianye\qianye-newapi\middleware\`）：

| 文件 | 函数 | 用途 |
|---|---|---|
| `auth.go:78/92/98/104` | `TryUserAuth` `UserAuth` `AdminAuth` `RootAuth` | session/JWT |
| `auth.go:248/279/352` | `TokenOrUserAuth` `TokenAuthReadOnly` **`TokenAuth`** | API key |
| `auth.go:226` | `RequirePermission(authz.Permission)` | casbin |
| `auth_origin.go:18` | `SessionCookieOriginGuard` | |
| `body_cleanup.go:11` | `BodyStorageCleanup` | |
| `cache.go:7` / `disable-cache.go:5` | `Cache` / `DisableCache` | |
| `cors.go:9/18` | `CORS` / `Version` | |
| `distributor.go:33` | **`Distribute`** | 选渠道 |
| `email-verification-rate-limit.go:61` | `EmailVerificationRateLimit` | |
| `gzip.go:25` | `DecompressRequestMiddleware` | |
| `header_nav.go:104/125` | `HeaderNavModuleAuth` / `HeaderNavModulePublicOrUserAuth` | |
| `i18n.go:13` | `I18n` | |
| `jimeng_adapter.go:15` / `kling_adapter.go:14` | `JimengRequestConvert` / `KlingRequestConvert` | |
| `logger.go:12/19` | `RouteTag` / `SetUpLogger` | |
| `model-rate-limit.go:168` | `ModelRequestRateLimit` | |
| `performance.go:14` | `SystemPerformanceCheck` | |
| `rate-limit.go:160/167/174/181/185/238` | `GlobalWebRateLimit` `GlobalAPIRateLimit` `CriticalRateLimit` `DownloadRateLimit` `UploadRateLimit` `SearchRateLimit` | |
| `recover.go:12` | `RelayPanicRecover` | |
| `request-id.go:10` | `RequestId` | |
| `request_body_limit.go:12` | `AnonymousRequestBodyLimit` | |
| `secure_verification.go:14` | `SecureVerificationRequired` | |
| `stats.go:17` | `StatsMiddleware` | |
| `trusted_proxies.go:22` | `ConfigureTrustedProxies` | |
| `turnstile-check.go:15` | `TurnstileCheck` | |
| `audit.go:106/122` | `beginAdminAudit` / `finishAdminAudit`（非导出，由 `authHelper` 调用） | **响应体捕获范式，见 §4.3** |
| `utils.go:12/29` | `abortWithOpenAiMessage` / `abortWithMidjourneyMessage`（非导出） | **中止请求的标准写法** |

**注册顺序定义位置**：全局在 `main.go:179-193`；relay 域在 `router/relay-router.go:14-17`（引擎级）与 `:70-73`（`/v1` 组级）、`:85`（`httpRouter` 子组级）。

### 推荐插入位置
**`router/relay-router.go:73` 之后、`:85` 的 `Distribute()` 之前**，即：

```go
relayV1Router.Use(middleware.ModelRequestRateLimit())  // :73
relayV1Router.Use(qymw.ViolationPromptCheck())         // ← 新增 1 行
```

理由：
- `TokenAuth()` 已跑完 → `constant.ContextKeyUserId` / `ContextKeyTokenId` / `ContextKeyUsingGroup` / `ContextKeyTokenGroup` 都已在 Context 中，可按用户/令牌/分组做差异化策略。
- 在 `Distribute()` **之前**拦截 → 省掉选渠道、预扣费、模型定价的全部开销，且不会污染渠道亲和性缓存（`distributor.go:167-169` 的 `RecordChannelAffinity`）。
- 挂在 `relayV1Router` 上一次即覆盖 `/v1/chat/completions`、`/v1/messages`、`/v1/responses`、`/v1/embeddings`、`/v1/images/*`、`/v1/audio/*` 等全部 OpenAI/Claude 端点（`wsRouter` + `httpRouter` 两个子组都继承）。Gemini 原生端点 `/v1beta` 是另一个组（`relay-router.go:187-198`），需要再加 1 行。

---

## 3. 请求体解析 / 可重复读 body

### 3.1 可重复读 body 辅助函数（**项目已有**）
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\common\gin.go`

```go
const KeyRequestBody = "key_request_body"   // :20
const KeyBodyStorage = "key_body_storage"   // :21
var ErrRequestBodyTooLarge = errors.New("request body too large") // :23

func IsRequestBodyTooLargeError(err error) bool                    // :25
func GetRequestBody(c *gin.Context) (io.Seeker, error)             // :36
func GetBodyStorage(c *gin.Context) (BodyStorage, error)           // :86
func CleanupBodyStorage(c *gin.Context)                            // :99
func UnmarshalBodyReusable(c *gin.Context, v any) error            // :108  ★
```

`UnmarshalBodyReusable` 行为：
- 从 `BodyStorage`（内存或磁盘，超 `constant.MaxRequestBodyMB`（默认 128MB）报错）读取；
- `application/json` 且磁盘存储 → `DecodeJson(storage, v)` 流式解码（`:118-130`）；
- 否则 `storage.Bytes()` → `Unmarshal` / `parseFormData` / `parseMultipartFormData`（`:136-145`）；
- **结束时 `storage.Seek(0)` + `c.Request.Body = io.NopCloser(storage)`**（`:150-153`），因此可无限次重复读。

Context 存取辅助（`common/gin.go:157-197`）：`SetContextKey` / `GetContextKey` / `GetContextKeyString` / `GetContextKeyInt` / `GetContextKeyBool` / `GetContextKeyStringSlice` / `GetContextKeyStringMap` / `GetContextKeyTime` / `GetContextKeyType[T]`。

### 3.2 拿到完整 prompt 的最早时机

| 时机 | file:line | 拿到的东西 |
|---|---|---|
| 最早（原始字节） | `middleware/distributor.go:210-214` | `common.GetBodyStorage(c)` → `storage.Bytes()`，仅 gjson 取了 `model`/`group` |
| DTO 解析 | `relay/helper/valid_request.go:314` | `*dto.GeneralOpenAIRequest`（含完整 `Messages`） |
| **归一化文本（推荐）** | `controller/relay.go:134` | `meta := request.GetTokenCountMeta()` → `meta.CombineText` |

`types.TokenCountMeta`（`relaykit/types/request_meta.go:20`）：
```go
type TokenCountMeta struct {
	TokenType     TokenType   `json:"token_type,omitempty"`
	CombineText   string      `json:"combine_text,omitempty"`   // ★ 所有 message 文本拼接
	ToolsCount    int
	NameCount     int
	MessagesCount int
	Files         []*FileMeta  // 图片/音频/视频/文件，含 Source、Detail
	MaxTokens     int
	ImagePriceRatio float64
	BillingRatios   map[string]float64
}
```

`GetTokenCountMeta()` 是 `dto.Request` 接口方法，`GeneralOpenAIRequest`（`relaykit/dto/openai_request.go:119`）、`ClaudeRequest`、`GeminiChatRequest`、`OpenAIResponsesRequest`、`EmbeddingRequest`、`ImageRequest` 等**都实现了**。这意味着：**用 `CombineText` 做违规检测可以一次覆盖所有 relay format，不必为每种协议写解析逻辑。**

---

## 4. 响应处理

### 4.1 非流式
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\relay\channel\openai\relay-openai.go`

```go
func OpenaiHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {  // :222
	responseBody, err := io.ReadAll(resp.Body)   // :226  ★ 完整响应体在此
	...
	err = common.Unmarshal(responseBody, &simpleResponse)  // :247  → dto.OpenAITextResponse
	...
	service.IOCopyBytesGracefully(c, resp, responseBody)   // :334  ★ 写给客户端的唯一出口
	return &simpleResponse.Usage, nil
}
```
- `simpleResponse.Choices[i].Message.StringContent()` 即 assistant 文本（`:279` 有用例）。
- **在 `:334` 之前拦截是可行的**——可以直接 `return types.NewError(...)` 改成错误响应，客户端还没收到任何字节。
- Claude 同构：`relay/channel/claude/relay-claude.go:268` `ClaudeHandler` → `:277` `io.ReadAll` → `HandleClaudeResponseData`（`:217`）→ `:263` `service.IOCopyBytesGracefully`。

`service.IOCopyBytesGracefully`（`service/http.go:44`）：拷贝上游 header → 设 `Content-Length` → `WriteHeader` → `io.Copy(c.Writer, body)`。

### 4.2 流式（有聚合，但**边收边发**）
```go
func OaiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) { // :104
	var responseTextBuilder strings.Builder   // :117  ★ 聚合 assistant 全文
	...
	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {  // :128
		if lastStreamData != "" {
			HandleStreamFormat(c, info, lastStreamData, ...)   // :130  ★ 已发送给客户端（延迟一帧）
		}
		if len(data) > 0 {
			lastStreamData = data                               // :141
			collectStreamFunctionCallNames(data, ...)           // :142
			processTokenData(info.RelayMode, data, &responseTextBuilder, &toolCount)  // :143 ★ 累积文本
		}
	})
	...
	usage = service.ResponseText2Usage(c, responseTextBuilder.String(), ...)  // :182  ★ 全文可见，但已发完
	HandleFinalResponse(c, info, lastStreamData, ...)  // :192
}
```
- `processTokenData`（`relay/channel/openai/helper.go:110`）→ `ProcessStreamResponse(streamResponse, responseTextBuilder, toolCount)`。
- `helper.StreamScannerHandler`（`relay/helper/stream_scanner.go:77`）：scanner goroutine 逐行读 SSE（`:240-281`），`data` 经 `dataChan`（缓冲 10）交给 handler goroutine（`:202-224`），`writeMutex` 串行化写。
- **关键约束：`sendStreamData` 有一帧延迟（`lastStreamData` 机制），所以最多能"晚一帧"拦截；真正做到"发之前拦"需要自己在 `StreamResult.Stop(err)` 上做文章。** `sr.Stop(err)` 会让 handler goroutine 返回并终止流（`stream_scanner.go:220-222`）。
- Claude 流式同构：`relay/channel/claude/relay-claude.go:194` `ClaudeStreamHandler`，`claudeInfo.ResponseText strings.Builder`（`:199`）。

### 4.3 全协议统一的响应捕获（**零侵入方案**）
`middleware/audit.go:18-38` 已有现成范式：

```go
type auditResponseWriter struct {
	gin.ResponseWriter
	body    *bytes.Buffer
	maxSize int
}
func (w *auditResponseWriter) Write(b []byte) (int, error)       // :24
func (w *auditResponseWriter) WriteString(s string) (int, error) // :36
```
在 `beginAdminAudit`（`:106`）里 `c.Writer = writer`（`:116`），`c.Next()` 后读 `writer.body`。

同样的包装可以用在 relay 上：`service.IOCopyBytesGracefully` 走 `io.Copy(c.Writer, ...)`，`helper.StringData`（`relay/helper/common.go:97`）走 `c.Render(-1, common.CustomEvent{...})` → 最终也是 `c.Writer.Write`。**因此包装 `c.Writer` 一处即可捕获流式 SSE + 非流式 JSON + Claude/Gemini 各种格式的全部下行字节，完全不改任何 adaptor。**

> 注意：`relay/helper/stream_scanner.go:74` 用 `http.NewResponseController(c.Writer)` 设写超时；包装 struct 需实现 `Unwrap() http.ResponseWriter` 才能透传（不实现也只是 `_ =` 忽略错误，不会崩，但会丢失写超时保护）。

---

## 5. 中止请求并返回错误

### 5.1 中间件层
`middleware/utils.go:12`
```go
func abortWithOpenAiMessage(c *gin.Context, statusCode int, message string, code ...types.ErrorCode) {
	c.JSON(statusCode, gin.H{"error": gin.H{
		"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
		"type":    "new_api_error",
		"code":    codeStr,
	}})
	c.Abort()
	logger.LogError(c.Request.Context(), fmt.Sprintf("user %d | %s", userId, message))
}
```
> 该函数**未导出**。新包里的中间件需要自己写一份等价实现（或在 `middleware/` 包内新增文件复用它 —— 后者不算修改原文件，只是新增同包文件，是干净的做法）。

Claude 格式：`middleware/performance.go:20-27` 演示了按 path 前缀 `/v1/messages` 返回 `err.ToClaudeError()`。

### 5.2 controller / relay handler 层
错误构造函数（`relaykit/types/error.go`）：
```go
func NewError(err error, errorCode ErrorCode, ops ...NewAPIErrorOptions) *NewAPIError                       // :244  默认 500
func NewOpenAIError(err error, errorCode ErrorCode, statusCode int, ...) *NewAPIError                        // :266
func NewErrorWithStatusCode(err error, errorCode ErrorCode, statusCode int, ...) *NewAPIError                // :299
func WithOpenAIError(openAIError OpenAIError, statusCode int, ...) *NewAPIError                              // :317
func WithClaudeError(claudeError ClaudeError, statusCode int, ...) *NewAPIError                              // :349
func InitOpenAIError(errorCode ErrorCode, statusCode int, ...) *NewAPIError                                  // :291
// Options
func ErrOptionWithSkipRetry() NewAPIErrorOptions        // :381  ★ 违规拒绝必须加，否则会重试其他渠道
func ErrOptionWithStatusCode(statusCode int) ...        // :393
func ErrOptionWithNoRecordErrorLog() ...                // :387
func ErrOptionWithHideErrMsg(replaceStr string) ...     // :399
// 序列化
func (e *NewAPIError) ToOpenAIError() OpenAIError       // :180
func (e *NewAPIError) ToClaudeError() ClaudeError       // :213
```

错误码常量（`relaykit/types/error.go:40-88`），与违规相关的现成项：
```go
ErrorCodeSensitiveWordsDetected ErrorCode = "sensitive_words_detected"  // :42
ErrorCodeViolationFeeGrokCSAM   ErrorCode = "violation_fee.grok.csam"   // :43
ErrorCodePromptBlocked          ErrorCode = "prompt_blocked"            // :79
ErrorCodeAccessDenied           ErrorCode = "access_denied"             // :66
```

统一输出在 `controller/relay.go:92-110` 的 defer 里，按 `relayFormat` 分发：
```go
switch relayFormat {
case types.RelayFormatOpenAIRealtime: helper.WssError(c, ws, newAPIError.ToOpenAIError())
case types.RelayFormatClaude:         c.JSON(code, gin.H{"type":"error","error": newAPIError.ToClaudeError()})
default:                              c.JSON(code, gin.H{"error": newAPIError.ToOpenAIError()})
}
```
**在 `controller.Relay` 内部只需给 `newAPIError` 赋值 + `return`，格式适配自动完成。**

---

## 6. 已有的内容审核 / 敏感词机制（**AC 自动机现成设施**）

### 6.1 AC 自动机（`anknown/ahocorasick`）
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\service\str.go` —— **全项目唯一使用点**

```go
import goahocorasick "github.com/anknown/ahocorasick"          // :11

func SundaySearch(text string, pattern string) bool             // :14  单模式串（仅被 relay/channel/openai/audio.go:42 用来找 "usage"）
func RemoveDuplicate(s []string) []string                       // :51
func InitAc(dict []string) *goahocorasick.Machine               // :63
var acCache sync.Map                                            // :73  ★ 按词典哈希缓存已编译的自动机
func acKey(dict []string) string                                // :75  归一化(小写+trim+排序) + fnv64a
func getOrBuildAC(dict []string) *goahocorasick.Machine         // :98  ★ 懒构建 + LoadOrStore
func readRunes(dict []string) [][]rune                          // :120
func AcSearch(findText string, dict []string, stopImmediately bool) (bool, []string)  // :132  ★ 主入口
```
`AcSearch` 内部：`m.MultiPatternSearch([]rune(findText), stopImmediately)`，返回 `(命中?, 命中词列表)`。

**这套设施是词典无关的** —— `AcSearch(text, dict, stop)` 的 `dict` 是参数，`acCache` 按词典内容哈希缓存。**新功能可以直接传入自己 MySQL 库里的违禁词表，零改动复用，无需碰 `service/str.go`。**

### 6.2 敏感词业务层
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\service\sensitive.go`
```go
func CheckSensitiveMessages(messages []dto.Message) ([]string, error)          // :11  ← 全项目无调用者（死代码）
func CheckSensitiveText(text string) (bool, []string)                          // :35
func SensitiveWordContains(text string) (bool, []string)                       // :40  → AcSearch(小写(text), setting.SensitiveWords, true)
func SensitiveWordReplace(text string, returnImmediately bool) (bool, []string, string) // :52 ← 无调用者，把命中词替换成 "**###**"
```

`C:\Users\Administrator\Desktop\qianye\qianye-newapi\setting\sensitive.go`（全局变量，来自系统设置 OptionMap）
```go
var CheckSensitiveEnabled = true          // :5
var CheckSensitiveOnPromptEnabled = true  // :6
// var CheckSensitiveOnCompletionEnabled  // :8  ← 已注释掉，说明「响应检测」上游曾有但被移除
var StopOnSensitiveEnabled = true         // :11
var StreamCacheQueueLength = 0            // :14 「流模式缓存队列长度，0表示无缓存」← 只在 model/option.go:173,588 读写，relay 链路无任何消费者（残留）
var SensitiveWords = []string{"test_sensitive"}  // :18
func SensitiveWordsToString() string      // :22
func SensitiveWordsFromString(s string)   // :26
func ShouldCheckPromptSensitive() bool    // :37  → CheckSensitiveEnabled && CheckSensitiveOnPromptEnabled
// func ShouldCheckCompletionSensitive()  // :41  ← 已注释
```
配置读写：`model/option.go:166,170-172`（导出）、`:362-373,580-581`（导入）。

### 6.3 唯一的实际检查点
`controller/relay.go:129-146`
```go
needSensitiveCheck := setting.ShouldCheckPromptSensitive()      // :129
needCountToken := constant.CountToken                            // :130
var meta *types.TokenCountMeta
if needSensitiveCheck || needCountToken {
	meta = request.GetTokenCountMeta()                           // :134  ← 构建 CombineText（较贵）
} else {
	meta = fastTokenCountMetaForPricing(request)                 // :136  ← 跳过 CombineText
}
if needSensitiveCheck && meta != nil {                           // :139
	contains, words := service.CheckSensitiveText(meta.CombineText)  // :140
	if contains {
		logger.LogWarn(c, fmt.Sprintf("user sensitive words detected: %s", strings.Join(words, ", ")))  // :142
		newAPIError = types.NewError(err, types.ErrorCodeSensitiveWordsDetected)  // :143
		return
	}
}
```
> ⚠️ **`:143` 有 bug**：`types.NewError(err, ...)` 传的是上一行 `GenRelayInfo` 的 `err`（此时为 `nil`），不是敏感词错误。所以 `NewAPIError.Err == nil`，`Error()` 回落到 `string(errorCode)` = `"sensitive_words_detected"`。功能上不崩，但错误信息里丢了命中词。

**结论：响应侧（completion）审核在上游被明确移除了，目前只有 prompt 侧，且没有任何"响应内容检测"机制。**

### 6.4 已有的「违规」相关基础设施（可复用/参考）
`C:\Users\Administrator\Desktop\qianye\qianye-newapi\service\violation_fee.go`
```go
const ViolationFeeCodePrefix     = "violation_fee."                    // :21
const CSAMViolationMarker        = "Failed check: SAFETY_CHECK_TYPE"   // :22
const ContentViolatesUsageMarker = "Content violates usage guidelines" // :23

func IsViolationFeeCode(code types.ErrorCode) bool                       // :26
func HasCSAMViolationMarker(err *types.NewAPIError) bool                 // :30
func WrapAsViolationFeeGrokCSAM(err *types.NewAPIError) *types.NewAPIError // :41
func NormalizeViolationFeeError(err *types.NewAPIError) *types.NewAPIError // :56  ← controller/relay.go:176,232 调用
func calcViolationFeeQuota(amount, groupRatio float64) int               // :84
func ChargeViolationFeeIfNeeded(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError) bool // :104 ← controller/relay.go:180 调用
```
这是一套「检测到上游返回违规标记 → 打 `violation_fee.*` 错误码 → skipRetry → 额外扣费 + 写 consume log」的完整流程。**新的违规检测功能可以完全照抄这个模式**（甚至复用 `ViolationFeeCodePrefix` 前缀 + `NormalizeViolationFeeError` 的 skipRetry 语义）。

上游违规信号的现有埋点（都写 `constant.ContextKeyAdminRejectReason`，最终进日志 `other["reject_reason"]`，见 `service/text_quota.go:407,480-482`）：
- `relay/channel/openai/relay-openai.go:258` `"openai_finish_reason=content_filter"`
- `relay/channel/claude/relay-claude.go:31` `"claude_stop_reason=refusal"`
- `relay/channel/gemini/relay-gemini.go:161,331,338`、`relay-gemini-native.go:39`、`relay_responses.go:38,45`

---

## 7. `RelayInfo` 关键字段

`C:\Users\Administrator\Desktop\qianye\qianye-newapi\relay\common\relay_info.go:83-192`

```go
type RelayInfo struct {
	// —— 身份 ——
	TokenId        int          // :84
	TokenKey       string       // :85
	TokenGroup     string       // :86
	UserId         int          // :87
	UsingGroup     string       // :88  使用的分组（auto 跨分组重试时会变）
	UserGroup      string       // :89
	TokenUnlimited bool         // :90
	UserSetting    dto.UserSetting // :113
	UserEmail      string       // :114
	UserQuota      int          // :115
	RequestId      string       // :140  ★ 幂等键

	// —— 时间 ——
	StartTime         time.Time  // :91
	FirstResponseTime time.Time  // :92
	isFirstResponse   bool       // :93 (私有)

	// —— 协议/模式 ——
	IsStream        bool                // :95   ★
	IsPlayground    bool                // :97
	RelayMode       int                 // :99   ★ relayconstant.RelayModeXxx
	OriginModelName string              // :100  ★ 用户请求的模型名
	RequestURLPath  string              // :101
	RequestHeaders  map[string]string   // :102  ★ 请求头快照（cloneRequestHeaders :527）
	RelayFormat     types.RelayFormat   // :116  ★ openai/claude/gemini/...
	IsChannelTest   bool                // :145
	RetryIndex      int                 // :146  ★ 当前重试次数
	LastError       *types.NewAPIError  // :147

	// —— 请求对象 ——
	Request dto.Request               // :171  ★ 完整 DTO，可 .GetTokenCountMeta()
	RequestConversionChain  []types.RelayFormat // :175
	FinalRequestRelayFormat types.RelayFormat   // :178

	UpstreamRequestBodySize int64      // :157

	// —— 计费 ——
	FinalPreConsumedQuota int          // :119
	ForcePreConsume       bool         // :123
	Billing               BillingSettler // :126
	BillingSource         string       // :129  "" | "wallet" | "subscription"
	SubscriptionId        int          // :131
	PriceData             hosttypes.PriceData // :159
	QuotaClamp            *common.QuotaClamp  // :164
	TieredBillingSnapshot *billingexpr.BillingSnapshot // :168

	StreamStatus *StreamStatus         // :180

	// —— 嵌入 ——
	ThinkingContentInfo                 // :185
	TokenCountMeta                      // :186  内含私有 estimatePromptTokens
	*ClaudeConvertInfo                  // :187
	*RerankerInfo                       // :188
	*ResponsesUsageInfo                 // :189
	*ChannelMeta                        // :190  ★ 渠道信息，InitChannelMeta 之后才非 nil
	*TaskRelayInfo                      // :191
}
```

`ChannelMeta`（`:58-76`）：`ChannelType` `ChannelId` `ChannelIsMultiKey` `ChannelMultiKeyIndex` `ChannelBaseUrl` `ApiType` `ApiVersion` `ApiKey` `Organization` `ChannelCreateTime` `ParamOverride` `HeadersOverride` `ChannelSetting`(`dto.ChannelSettings`) `ChannelOtherSettings` `UpstreamModelName` `IsModelMapped` `SupportStreamOptions`。

**关键方法**：
```go
func (info *RelayInfo) InitChannelMeta(c *gin.Context)          // :194  每次重试选完渠道后调用
func (info *RelayInfo) SetEstimatePromptTokens(int)             // :683
func (info *RelayInfo) GetEstimatePromptTokens() int            // :690
func (info *RelayInfo) GetFinalRequestRelayFormat() types.RelayFormat // :640
func (info *RelayInfo) SetFirstResponseTime()                   // :811
func (info *RelayInfo) HasSendResponse() bool                   // :818
func (info *RelayInfo) IncrSendResponseCount()                  // :773
func (info *RelayInfo) ToString() string                        // :251  已脱敏（ApiKey/Email）
```

**作为自定义数据载体**：`RelayInfo` 是 **struct 定义在原项目文件里**，加字段 = 改原文件。**推荐改用 `gin.Context` 传递**（`common.SetContextKey` / `GetContextKeyType[T]`，`common/gin.go:157,189`），新增自己的 `ContextKey` 常量放在新包里（`constant.ContextKey` 只是 `type ContextKey string`，可在包外用 `constant.ContextKey("qy_violation_result")` 构造，无需改 `constant/context_key.go`）。

---

## 【扩展点建议】

### (a) 转发前检查提示词 —— 两个方案

#### 方案 A1（**推荐，侵入最小：改 1 行**）：新增中间件，挂在 `relayV1Router`

- **新建**：`qianye/middleware/violation_prompt.go`（或 `middleware/qy_violation.go`，放在 `middleware` 包内可复用未导出的 `abortWithOpenAiMessage`）
- **改动原文件**：`router/relay-router.go` 在 `:73` 后插入 1 行 `relayV1Router.Use(qy.ViolationPromptCheck())`；若要覆盖 Gemini 原生端点，`:192` 后再插 1 行（共 2 行）。
- **中间件内部拿 prompt 的两种写法**：
  1. **协议无关（推荐）**：`common.UnmarshalBodyReusable(c, &raw)` 到 `map[string]any` / 或直接 `common.GetBodyStorage(c).Bytes()` + `gjson` 遍历，把所有字符串叶子拼起来。优点：不依赖 `relayFormat`，一套逻辑吃所有端点。
  2. **协议感知**：按 `c.Request.URL.Path` 推断 `types.RelayFormat`，调 `helper.GetAndValidateRequest(c, format)` → `request.GetTokenCountMeta().CombineText`。优点：文本干净（不含 base64 图片等）；缺点：与 `router` 的 path→format 映射重复，上游改路由会失配。
  > 由于 `UnmarshalBodyReusable` 会 seek 回 0 并重置 `c.Request.Body`，**中间件里读 body 完全不影响后续 `Distribute()` 和 `controller.Relay` 再读**。
- **拒绝写法**：
  ```go
  c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
      "message": common.MessageWithRequestId(msg, c.GetString(common.RequestIdKey)),
      "type":    "new_api_error",
      "code":    "qy_violation_prompt_blocked",
  }})
  c.Abort()
  ```
  Claude 端点（path 含 `/v1/messages`）改用 `gin.H{"type":"error","error": gin.H{"type":..., "message":...}}`，参考 `middleware/performance.go:20-27`。
- **命中检测**：直接 `service.AcSearch(strings.ToLower(text), myDict, true)`（`service/str.go:132`），`myDict` 从你的独立 MySQL 库加载并进程内缓存（`acCache` 会自动按词典哈希复用已编译自动机）。
- **落库**：命中记录写你自己的 MySQL 库（异步 `gopool.Go`），需要的字段全在 Context 里：`common.GetContextKeyInt(c, constant.ContextKeyUserId)`、`ContextKeyTokenId`、`ContextKeyUsingGroup`、`ContextKeyOriginalModel`（Distribute 之前该键尚未设置，可用 gjson 从 body 取 `model`）、`c.GetString(common.RequestIdKey)`、`c.ClientIP()`。

#### 方案 A2（备选：改 `controller/relay.go` 一处，约 8 行）
在 `controller/relay.go:146` 之后插入，复用已经构建好的 `meta.CombineText`（**零额外解析开销**，因为 `needSensitiveCheck` 分支已经付过 `GetTokenCountMeta()` 的代价），且此时 `relayInfo` 完整可用（user/token/group/model 全有），错误直接 `newAPIError = types.NewError(..., types.ErrOptionWithSkipRetry())` + `return`，格式适配由 `:92-110` 的 defer 自动完成。
- 优点：文本质量最好（`CombineText` 已归一化，含 tools/name/prompt/input）；能拿到 `meta.Files`（图片/音频来源）做多模态检测；错误处理最规范。
- 缺点：改 `controller/relay.go`（该文件是上游高频改动文件，合并冲突风险中等）。
- **折中**：只插入 `if err := qy.CheckPrompt(c, relayInfo, meta); err != nil { newAPIError = err; return }` 三行，冲突面极小。

> **我的建议：A1 + A2 组合** —— 主逻辑写在新包 `qy.CheckPrompt(c, info, meta)`，先用 A1 中间件上线（零 controller 改动）；若后续需要 `meta.Files` 多模态检测，再在 A2 位置补三行。

---

### (b) 响应后检测内容 —— 三个层次

#### 方案 B1（**推荐，零侵入原项目文件**）：包装 `c.Writer` 的响应捕获中间件
- **新建**：`qy/middleware/violation_response.go`，照抄 `middleware/audit.go:18-38` 的 `auditResponseWriter` 范式：
  ```go
  type respTap struct {
      gin.ResponseWriter
      buf     *bytes.Buffer
      maxSize int
  }
  func (w *respTap) Write(b []byte) (int, error)      // 边写边留副本
  func (w *respTap) WriteString(s string) (int, error)
  func (w *respTap) Unwrap() http.ResponseWriter      // ★ 让 http.NewResponseController 找到 SetWriteDeadline
  ```
  中间件里 `c.Writer = tap`，`c.Next()` 之后异步分析 `tap.buf`（SSE 需按 `data: ` 行切分并抽 `choices[].delta.content`；非流式直接抽 `choices[].message.content`）。
- **改动原文件**：与 (a) 复用**同一行** `relayV1Router.Use(...)` —— 一个中间件同时干 (a) 前置检查 + (b) 后置捕获（`c.Next()` 前检 prompt，`c.Next()` 后检响应）。**净改动 = `router/relay-router.go` 1 行。**
- **能力边界**：**只能事后检测，不能阻断**（字节已发出）。适合：记录违规样本、累计违规次数、触发封禁/扣费/告警。
- **优点**：一处覆盖流式+非流式、OpenAI/Claude/Gemini/Responses 全部 40+ adaptor，完全不碰 `relay/channel/**`。

#### 方案 B2（可阻断，但侵入 adaptor）：在 `OpenaiHandler` 写出前拦截
`relay/channel/openai/relay-openai.go:334` 的 `service.IOCopyBytesGracefully` 之前插入检测，命中则 `return nil, types.NewError(..., types.ErrOptionWithSkipRetry())`。
- 优点：**真正能阻断**，客户端收到的是错误而不是违规内容。
- 缺点：**只对非流式有效**；且要为每个协议改一处（`relay-openai.go:334`、`relay-claude.go:263`、`relay-gemini.go`、`relay_responses.go`…），侵入面大。
- 流式对应位置：`relay-openai.go:130` 的 `HandleStreamFormat` 调用前（延迟一帧，可以在发出前检查 `lastStreamData`），命中调 `sr.Stop(err)`（`relay/helper/stream_result.go`）终止流。工程量与风险都显著更高。

#### 方案 B3（补充：结算收口点做统计/惩罚）
`service.PostTextConsumeQuota`（`service/text_quota.go:397`）是所有文本 relay 成功后的统一收口（被 `compatible_handler.go:218,220`、`claude_handler.go:152,225`、`responses_handler.go:159,169` 调用）。它拿不到响应文本，但能拿到 `relayInfo` + `usage` + `constant.ContextKeyAdminRejectReason`。
- **配合 B1**：B1 在 `c.Next()` 后把检测结论写进 Context（`common.SetContextKey(c, constant.ContextKey("qy_violation"), result)`），或直接在 B1 里落自己的库 —— 因为 B1 的 `c.Next()` 后置逻辑本来就晚于 `PostTextConsumeQuota`，可以直接读 `ContextKeyAdminRejectReason` 把上游自带的违规信号（content_filter / refusal / gemini_block_reason）一并归档。
- **惩罚可照抄** `service/violation_fee.go:104` `ChargeViolationFeeIfNeeded` 的实现模式（`PostConsumeQuota` + `UpdateUserUsedQuotaAndRequestCount` + `RecordConsumeLog`），但写到你自己的库。

---

### 需要改动的原项目文件 —— **最小集合**

| 文件 | 改动 | 行数 |
|---|---|---|
| `C:\Users\Administrator\Desktop\qianye\qianye-newapi\router\relay-router.go` | `:73` 后加 `relayV1Router.Use(qy.ViolationGuard())`；（可选）`:192` 后为 Gemini 组加同一行 | **1~2 行** |

**仅此一处。** (a)(b) 全部功能都能挂在这一个中间件上。

若后续需要多模态（图片/音频）检测或更精确的归一化 prompt 文本，再追加：

| 文件 | 改动 | 行数 |
|---|---|---|
| `controller/relay.go` | `:146` 后加 3 行调用 `qy.CheckPrompt(c, relayInfo, meta)` | 3 行 |

若后续需要**真正阻断响应**（而非事后记录），才需要追加 `relay/channel/openai/relay-openai.go:334` / `relay/channel/claude/relay-claude.go:263` 等 per-adaptor 改动 —— 建议尽量避免。

### 可直接复用、无需改动的现成设施
- `service.AcSearch(text, dict, stopImmediately) (bool, []string)` — `service/str.go:132`，词典作为参数传入，自带 `sync.Map` 编译缓存
- `common.UnmarshalBodyReusable(c, v) error` — `common/gin.go:108`，可重复读 body
- `common.GetBodyStorage(c) (BodyStorage, error)` — `common/gin.go:86`
- `common.SetContextKey` / `GetContextKeyType[T]` — `common/gin.go:157,189`
- `types.NewError` / `ErrOptionWithSkipRetry` / `ToOpenAIError` / `ToClaudeError` — `relaykit/types/error.go`
- `dto.Request.GetTokenCountMeta() *types.TokenCountMeta`（含 `CombineText` + `Files`）— 全 relay format 通用
- `middleware/audit.go:18-38` 的 `auditResponseWriter` — 响应体捕获范式（照抄，不改）
- `service/violation_fee.go` 全套 — 违规错误码规范化 + 额外扣费 + 日志落库的参照实现
