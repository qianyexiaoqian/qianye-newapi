# 使用日志(推理强度/缓存比例)

## 【使用日志】领域勘察报告

---

## 一、后端数据模型

### 1.1 `Log` 结构体 — `model/log.go:59-81`（**已有**）

```go
type Log struct {
	Id                int    `json:"id" gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
	UserId            int    `json:"user_id" gorm:"index;index:idx_user_id_id,priority:1"`
	CreatedAt         int64  `json:"created_at" gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
	Type              int    `json:"type" gorm:"index:idx_created_at_type"`
	Content           string `json:"content"`
	Username          string `json:"username" ...`
	TokenName         string `json:"token_name" ...`
	ModelName         string `json:"model_name" ...`
	Quota             int    `json:"quota" gorm:"default:0"`
	PromptTokens      int    `json:"prompt_tokens" gorm:"default:0"`
	CompletionTokens  int    `json:"completion_tokens" gorm:"default:0"`
	UseTime           int    `json:"use_time" gorm:"default:0"`
	IsStream          bool   `json:"is_stream"`
	ChannelId         int    `json:"channel" gorm:"index"`
	ChannelName       string `json:"channel_name" gorm:"->"`   // 只读，运行时 join 填充
	TokenId           int    `json:"token_id" gorm:"default:0;index"`
	Group             string `json:"group" gorm:"index"`
	Ip                string `json:"ip" gorm:"index;default:''"`
	RequestId         string `json:"request_id,omitempty" gorm:"type:varchar(64);..."`
	UpstreamRequestId string `json:"upstream_request_id,omitempty" gorm:"type:varchar(128);..."`
	Other             string `json:"other"`     // ← JSON 字符串，无 gorm type，默认 longtext/text
}
```

**关键点：`Other` 是 `string`（JSON 序列化后的 `map[string]interface{}`）**，序列化/反序列化用 `common.MapToJsonStr`（`common/str.go:39`）和 `common.StrToMap`（`common/str.go:47`）。

日志类型常量 `model/log.go:84-93`：`LogTypeUnknown=0 / Topup=1 / Consume=2 / Manage=3 / System=4 / Error=5 / Refund=6 / Login=7`。

### 1.2 返回 DTO：**没有独立 DTO**
`controller/log.go:13`（`GetAllLogs`）和 `:35`（`GetUserLogs`）直接把 `[]*model.Log` 塞进 `common.PageInfo.Items`（`common/page_info.go:9-15`），JSON 直出 `Log` 结构体。**新增字段只要加在 `Log` 结构体或写进 `Other`，前端立刻能拿到。**

### 1.3 独立日志库能力（**已有，对"独立 MySQL 库"需求很重要**）
- `model/main.go:55` `var LOG_DB *gorm.DB`
- `model/main.go:211` `func InitLogDB() (err error)`：环境变量 `LOG_SQL_DSN` 为空 → `LOG_DB = DB`；否则 `chooseDB("LOG_SQL_DSN", true)` 建独立连接，`migrateLOGDB()`（`model/main.go:399`）只对 `&Log{}` 做 AutoMigrate。
- 支持 ClickHouse：`common.UsingLogDatabase(common.DatabaseTypeClickHouse)`，此时走 `clickHouseLogCreateTableSQL`，**ClickHouse 分支不走 AutoMigrate**（`model/main.go:400-403`）——若考虑给 `logs` 表加列，ClickHouse 路径要单独处理。

---

## 二、`Other` 字段里现在塞了什么（完整 key 清单）

### 2.1 写入点

| 文件:行 | 场景 |
|---|---|
| `service/log_info_generate.go:72` `GenerateTextOtherInfo` | 文本 relay 主入口 |
| `service/log_info_generate.go:248` `GenerateWssOtherInfo` | Realtime WSS |
| `service/log_info_generate.go:260` `GenerateAudioOtherInfo` | 音频 |
| `service/log_info_generate.go:272` `GenerateClaudeOtherInfo` | Claude 语义 |
| `service/log_info_generate.go:293` `GenerateMjOtherInfo` | Midjourney |
| `service/log_info_generate.go:307` `InjectTieredBillingInfo` | 阶梯计费 |
| `service/text_quota.go:466-519` | 消费日志补充键 |
| `service/task_billing.go:40-52,125-139,183-184,257-259` | 异步任务 |
| `service/violation_fee.go:139-147` | 违规计费 |
| `service/billing_usage.go:61-71` | `admin_info.usage_billing_path` |
| `controller/relay.go:380-396` | 错误日志（type=5） |
| `model/log.go:176,208,235-238,264` | 审计/登录/充值日志 |

### 2.2 完整 key 列表

**`GenerateTextOtherInfo` 精确签名（`service/log_info_generate.go:72-73`）：**
```go
func GenerateTextOtherInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo,
    modelRatio, groupRatio, completionRatio float64,
    cacheTokens int, cacheRatio float64, modelPrice float64, userGroupRatio float64) map[string]interface{}
```
写入：`model_ratio`、`group_ratio`、`completion_ratio`、**`cache_tokens`**、`cache_ratio`、`model_price`、`user_group_ratio`、`frt`、**`reasoning_effort`**（仅当 `relayInfo.ReasoningEffort != ""`）、`is_model_mapped`、`upstream_model_name`、`is_system_prompt_overwritten`、`admin_info`、`request_path`、`request_conversion`、`claude`、`po`、`billing_source`/`billing_preference`/`subscription_*`/`wallet_quota_deducted`、`stream_status`。

**其余：** `usage_semantic`（仅 Claude 语义分支，`text_quota.go:475`）、`reject_reason`、`image`/`image_ratio`/`image_output`、`tool_surcharges`、`audio_input_seperate_price`/`audio_input_token_count`/`audio_input_price`、`cache_creation_tokens`/`cache_creation_ratio`、`cache_creation_tokens_5m`/`_ratio_5m`、`cache_creation_tokens_1h`/`_ratio_1h`、**`cache_write_tokens`**（`text_quota.go:511`）、**`input_tokens_total`**（`text_quota.go:518`）、`billing_mode`/`expr_b64`/`matched_tier`、`ws`/`audio`/`audio_input`/`audio_output`/`text_input`/`text_output`/`audio_ratio`/`audio_completion_ratio`、`is_task`/`task_id`/`reason`/`pre_consumed_quota`/`actual_quota`、`violation_fee`/`violation_fee_code`/`violation_fee_marker`/`fee_quota`、`op`/`audit_info`/`admin_info`、`error_type`/`error_code`/`status_code`/`channel_id`/`channel_name`/`channel_type`。

`admin_info` 子键：`use_channel`、`is_multi_key`、`multi_key_index`、`local_count_tokens`、`channel_affinity`、`usage_billing_path`、`quota_saturation`、`payment_method`、`caller_ip`、`server_ip`、`version`、`node_name`、`admin_username`/`admin_id`/`admin_role`/`auth_method`。

### 2.3 普通用户脱敏 — `model/log.go:116-132`
```go
func formatUserLogs(logs []*Log, startIdx int) {
	...
	delete(otherMap, "admin_info")
	delete(otherMap, "audit_info")
	delete(otherMap, "stream_status")
	...
}
```
**`reasoning_effort` 和 `cache_tokens` 都不在脱敏名单里 → 普通用户也能看到。**

---

## 三、推理强度（reasoning effort）—— **后端已记录，但覆盖不全**

### 3.1 已记录
`service/log_info_generate.go:83-85`：
```go
if relayInfo.ReasoningEffort != "" {
    other["reasoning_effort"] = relayInfo.ReasoningEffort
}
```
字段定义：`relay/common/relay_info.go:112` `ReasoningEffort string`；访问器 `GetReasoningEffort()` `:738`、`SetReasoningEffort(effort string)` `:745`。接口约定在 `relaykit/relayconvert/convmeta/meta.go:27-30`，值容器 `convmeta/meta.go:81`。

### 3.2 赋值点（**穷举，全部在 relay 适配器里**）

| file:line | 覆盖范围 |
|---|---|
| `relay/channel/openai/adaptor.go:355` `info.ReasoningEffort = request.ReasoningEffort` | **仅在 `if isOModel \|\| isGPT5Model` 分支内**（o系列/GPT-5） |
| `relay/channel/openai/adaptor.go:618` `info.ReasoningEffort = request.Reasoning.Effort` | Responses API，`request.Reasoning.Effort != ""` |
| `relay/channel/deepseek/adaptor.go:116` | DeepSeek V4 `-max/-none` 后缀（OpenAI 格式） |
| `relay/channel/deepseek/adaptor.go:147` | DeepSeek V4（Claude 格式） |
| `relay/channel/xai/adaptor.go:88` | **仅 `grok-3-mini` 前缀**，从 `-high/-low` 模型后缀推导 |
| `relaykit/relayconvert/internal/shared/gemini/request.go:146` `info.SetReasoningEffort(level)` | Gemini：仅走 `reasoning.TrimEffortSuffix(modelName)` 命中 `thinkingLevel` 分支时 |

模型名后缀解析器：`relaykit/relayconvert/reasoning/suffix.go`
- `EffortSuffixes = []string{"-max","-xhigh","-high","-medium","-low","-minimal"}` (:9)
- `OpenAIEffortSuffixes = []string{"-high","-minimal","-low","-medium","-none","-xhigh"}` (:11)
- `func ParseOpenAIReasoningEffortFromModelSuffix(modelName string) (string, string)` (:30)
- `func TrimEffortSuffix(modelName string) (string, string, bool)` (:16)

### 3.3 **未记录的场景（缺口）**
1. **Claude 原生 `thinking`**：`relaykit/dto/claude.go:227` `Thinking *Thinking \`json:"thinking,omitempty"\``，结构体 `:447-455`（`Type`、`BudgetTokens *int`、`Display`），`GetBudgetTokens() int` `:457`。**全项目无任何地方把它写入 `relayInfo.ReasoningEffort`**。
2. **Gemini 数值型 `thinkingConfig.thinkingBudget`**：`relaykit/dto/gemini.go:163-167` `GeminiThinkingConfig{IncludeThoughts, ThinkingBudget *int, ThinkingLevel string}`，挂在 `GeminiGenerationConfig.ThinkingConfig`（`gemini.go:348`）。只有 `ThinkingLevel` 分支会 SetReasoningEffort，`ThinkingBudget` 分支不会。
3. **Qwen `enable_thinking` / `thinking_budget`**：`relaykit/dto/openai_request.go:91-92`（`json.RawMessage`），不写日志。
4. **通用 OpenAI 兼容渠道**上非 o系列/非 GPT-5 模型带 `reasoning_effort`（`relaykit/dto/openai_request.go:38` `ReasoningEffort string \`json:"reasoning_effort,omitempty"\``）—— 落不到日志。
5. **错误日志（type=5）**：`controller/relay.go:380-396` 构造的 `other` 完全不含 reasoning。

### 3.4 **补记录的最佳单点**
`service/log_info_generate.go:72` `GenerateTextOtherInfo` 同时持有 `ctx *gin.Context` 和 `relayInfo`，是**唯一被所有文本/音频/WSS/Claude 路径共用**的收敛点。在这里可以：
- 读 `relayInfo.ReasoningEffort`（已有）
- 读 `relayInfo.ClaudeConvertInfo` / 通过 `common.UnmarshalBodyReusable(c, &v)`（`common/gin.go:104`）或 `common.GetBodyStorage(c)`（`:83`）**重新读取原始请求体**（body 已被缓存在 `c` 的 `KeyBodyStorage` 里，可重复读，`common/gin.go:20-21`）解析 `thinking.budget_tokens` / `thinkingConfig` / `reasoning_effort`。

如果不想改 `log_info_generate.go`，也可以在 relay 完成后由**自建中间件**（挂在 relay 路由链尾）读取 `c.GetString(common.RequestIdKey)` + body，把 reasoning 信息写进新库并按 `request_id` 关联（见第七节）。

---

## 四、缓存 token —— **数据已有，可前端直接算**

### 4.1 Usage DTO — `relaykit/dto/openai_response.go:223-243`
```go
type Usage struct {
	PromptTokens         int    `json:"prompt_tokens"`
	CompletionTokens     int    `json:"completion_tokens"`
	TotalTokens          int    `json:"total_tokens"`
	PromptCacheHitTokens int    `json:"prompt_cache_hit_tokens,omitempty"`
	UsageSemantic        string `json:"usage_semantic,omitempty"`
	UsageSource          string `json:"usage_source,omitempty"`
	BillingUsage         *BillingUsage `json:"billing_usage,omitempty"`
	PromptTokensDetails    InputTokenDetails  `json:"prompt_tokens_details"`
	CompletionTokenDetails OutputTokenDetails `json:"completion_tokens_details"`
	InputTokens            int  `json:"input_tokens"`
	OutputTokens           int  `json:"output_tokens"`
	InputTokensDetails     *InputTokenDetails `json:"input_tokens_details"`
	ClaudeCacheCreation5mTokens int `json:"claude_cache_creation_5_m_tokens"`
	ClaudeCacheCreation1hTokens int `json:"claude_cache_creation_1_h_tokens"`
	Cost any `json:"cost,omitempty"`
}
```
`relaykit/dto/openai_response.go:256-266`：
```go
type InputTokenDetails struct {
	CachedTokens         int `json:"cached_tokens"`
	CachedCreationTokens int `json:"cached_creation_tokens,omitempty"`
	CacheWriteTokens     int `json:"cache_write_tokens,omitempty"`
	TextTokens           int `json:"text_tokens"`
	AudioTokens          int `json:"audio_tokens"`
	ImageTokens          int `json:"image_tokens"`
}
func (d InputTokenDetails) CacheCreationTokensTotal() int   // :275
```
Claude 侧：`relaykit/dto/claude.go:558` `CacheCreationInputTokens int \`json:"cache_creation_input_tokens"\``、`CacheReadInputTokens`。

### 4.2 落库路径
`service/text_quota.go:260-263`：
```go
summary.CacheTokens         = usage.PromptTokensDetails.CachedTokens
summary.CacheCreationTokens = usage.PromptTokensDetails.CacheCreationTokensTotal()
summary.CacheCreationTokens5m = usage.ClaudeCacheCreation5mTokens
summary.CacheCreationTokens1h = usage.ClaudeCacheCreation1hTokens
```
→ `text_quota.go:477` 传入 `GenerateTextOtherInfo(..., summary.CacheTokens, summary.CacheRatio, ...)` → `other["cache_tokens"]`（`log_info_generate.go:78`）。
Claude 分支 `text_quota.go:468` → `GenerateClaudeOtherInfo`，额外写 `cache_creation_tokens*`。
`text_quota.go:511` 另写 `other["cache_write_tokens"]`（归一化后的写入量总计）。

日志行的 `prompt_tokens` 来自 `text_quota.go:528` `PromptTokens: summary.PromptTokens`。

### 4.3 **缓存百分比能否算出来 —— 能，但必须区分语义**

**这是本次需求最关键的坑：`prompt_tokens` 是否包含 `cache_tokens`，取决于计费语义。**

- **OpenAI / Gemini 语义**：`prompt_tokens` **包含** `cached_tokens`。证据：`service/text_quota.go:307-310`
  ```go
  if !summary.IsClaudeUsageSemantic && !legacyClaudeDerived {
      baseTokens = baseTokens.Sub(dCacheTokens)   // 要减掉才不重复计费
  }
  ```
- **Anthropic 语义**（`summary.IsClaudeUsageSemantic == true`，即 `other.usage_semantic === 'anthropic'`，`text_quota.go:475`）：`prompt_tokens` = Claude 的 `input_tokens`，**不包含** cache read / cache creation。证据：上面那段 `if` 不成立时不减；以及 `relaykit/relayconvert/internal/claude_messages/to_oai_chat_resp.go:222`
  ```go
  totalInputTokens := usage.PromptTokens + usage.PromptTokensDetails.CachedTokens + cacheCreationTokens
  ```
- **OpenRouter + Claude 计费**（`text_quota.go:273,280`）：显式 `summary.PromptTokens -= summary.CacheTokens` 再 `-= summary.CacheCreationTokens`，同样属于"不含缓存"，且该分支 `IsClaudeUsageSemantic` 为 true，判别一致。

**推荐前端公式（纯展示，零后端改动）：**
```ts
const cacheRead  = other.cache_tokens ?? 0
const cacheWrite = (other.cache_creation_tokens_5m ?? 0) + (other.cache_creation_tokens_1h ?? 0)
                   || (other.cache_creation_tokens ?? 0)
const isAnthropic = other.usage_semantic === 'anthropic'   // 老日志可回退判断 other.claude === true
const totalInput = isAnthropic
  ? log.prompt_tokens + cacheRead + cacheWrite   // Claude: prompt_tokens 不含缓存
  : log.prompt_tokens                            // OpenAI/Gemini: 已含 cached_tokens
const cachePct = totalInput > 0 ? cacheRead / totalInput : null
```
后端还有一个现成的归一化字段可优先使用：`other.input_tokens_total`（`text_quota.go:513-519`，仅在非 Claude 最终格式且 `billingUsage.UsageSource != "" && InputTokens > 0` 时写入）。

**注意：`usage_semantic`、`cache_write_tokens`、`input_tokens_total` 三个 key 目前在前端 `LogOtherData` 类型里都没有声明**（已 grep 确认 `web/src` 无匹配），需要补类型定义。

---

## 五、前端日志页面

### 5.1 文件地图（`web/src/features/usage-logs/`）
| 文件 | 作用 |
|---|---|
| `index.tsx` | 页面壳（`UsageLogs`），Tabs 切 common/drawing/task |
| `section-registry.tsx` | 三个 section 注册表 |
| `data/schema.ts:26-50` | `usageLogSchema` zod + `export type UsageLog` |
| `types.ts:115-244` | **`LogOtherData` 接口 — `other` 的 TS 类型** |
| `api.ts:73-85` | `getAllLogs` / `getUserLogs` / `getLogStats` |
| `lib/columns.ts:33` | `useColumnsByCategory(logCategory, isAdmin): ColumnDef<any>[]` 分发 |
| `components/columns/common-logs-columns.tsx:286` | **`useCommonLogsColumns(isAdmin: boolean): ColumnDef<UsageLog>[]` — 列定义主体** |
| `components/usage-logs-table.tsx:75` | 表格容器 + react-query |
| `components/usage-logs-mobile-card.tsx:310` | `CommonLogsCard` 移动端卡片 |
| `components/dialogs/details-dialog.tsx` | **详情弹窗（点"Details"列打开）** |
| `lib/format.ts:159` | `parseLogOther(other: string): LogOtherData \| null` |
| `lib/utils.ts:173` | `buildApiParams(...)` 构造查询参数 |
| `constants.ts:54-63,92-101` | `LOG_TYPE_ENUM` / `LOG_TYPES` |

### 5.2 列定义方式 — `common-logs-columns.tsx:286-793`
一个 `ColumnDef<UsageLog>[]` **数组，用 push 顺序拼装**：

```
:288  const columns: ColumnDef<UsageLog>[] = [ { accessorKey:'created_at', header:t('Time'), ... size:180 } ]
:322  if (isAdmin) columns.push( {id:'channel',...}, {id:'user',...} )
:538  columns.push({ accessorKey:'token_name', header:t('Token'), ..., size:160 })
:600  columns.push(
:602      { accessorKey:'model_name', header:t('Model'), meta:{mobileTitle:true} },
:622      { accessorKey:'is_stream', header:t('Stream'), meta:{label:t('Stream')} },
:646      { accessorKey:'prompt_tokens', header:'Tokens', ... },       ← 缓存 token 现在展示的地方
:693      { accessorKey:'quota', header:t('Cost') },
:706      { accessorKey:'use_time', header:t('Timing') },
:727      { accessorKey:'content', header:t('Details'), size:180, maxSize:200 }
      )
:792  return columns
```

`ColumnMeta` 扩展声明在 `web/src/tanstack-table.d.ts:22-32`：`label` / `description` / `className` / `pinned` / `mobileTitle` / `mobileBadge` / `mobileHidden` / `mobileOrder`。

**"Tokens" 列现有渲染（`:646-691`）已在读缓存字段：**
```tsx
const cacheReadTokens = other?.cache_tokens || 0            // :660
const cacheWrite5m    = other?.cache_creation_tokens_5m || 0
const cacheWrite1h    = other?.cache_creation_tokens_1h || 0
const cacheWriteTokens = hasSplitCache ? cacheWrite5m + cacheWrite1h : other?.cache_creation_tokens || 0
// 渲染 "prompt / completion" + 第二行 "Cache↓ N  ↑ M"
```

### 5.3 `other` 的解析与展示
- 解析：`lib/format.ts:159` `parseLogOther(log.other)` → `JSON.parse`，失败返回 `null`。每个 cell 各自调用（**未做 memo，每列重复 parse**）。
- **有"详情"展开**：`content` 列（`:727-789`）渲染一个 button，`onClick` → `setDialogOpen(true)` → `<DetailsDialog log={log} isAdmin={isAdmin} open onOpenChange />`。
- 详情弹窗里已有的相关区块：
  - `details-dialog.tsx:406` `function TokenBreakdown(props: { log: UsageLog; other: LogOtherData })`，`:412` `const cacheRead = other.cache_tokens || 0`，逐行 `DetailRow`：Input Tokens / Output Tokens / **Cache Read** / Cache Write / Cache Write (5m) / Cache Write (1h) / Image Tokens…
  - `details-dialog.tsx:1016-1032` **推理强度已展示**：
    ```tsx
    {other?.reasoning_effort && (
      <DetailRow label={t('Reasoning Effort')}
        value={<StatusBadge label={other.reasoning_effort} variant={reasoningEffortVariant} size='sm' copyable={false} />} />
    )}
    ```
    颜色映射 `:607-612`：`high`→`orange`，`medium`→`yellow`，其它→`green`。
- 移动端：`usage-logs-mobile-card.tsx:190` `MobileTokensField`（`:207` 同样读 `other?.cache_tokens`）、`:310` `CommonLogsCard` 用 `cells.get('model_name'|'quota'|'channel'|'user'|'token_name'|'content')` 手工布局 —— **移动端不会自动继承新列，需要手动挂**。

### 5.4 i18n（**已有 key**）
`web/src/i18n/locales/zh.json:3644` `"Reasoning Effort": "推理强度"`；`:676` `"Cache": "缓存"`；`:689` `"Cache Read": "缓存读取"`。翻译文件为扁平 `translation` 对象，key 即英文原文。新 key（如 `"Cache Hit Rate"`）需要往 7 个 locale 文件（en/zh/zh-TW/ja/fr/ru/vi）各加一条。

### 5.5 列可见性持久化
`usage-logs-table.tsx:59-64`：
```ts
function getColumnVisibilityStorageKey(logCategory, isAdmin) {
  return `usage-logs:${logCategory}:${isAdmin ? 'admin' : 'user'}:column-visibility`
}
```
传给 `useDataTable({ columnVisibilityStorageKey })`（`:163`）。新增列默认可见，用户可通过 DataTable 工具栏自行隐藏。

---

## 六、日志接口的分页 / 筛选参数

### 6.1 路由（`router/api-router.go:271-278`）
```go
logRoute := apiRouter.Group("/log")
logRoute.GET("/",           middleware.AdminAuth(), controller.GetAllLogs)
logRoute.GET("/stat",       middleware.AdminAuth(), controller.GetLogsStat)
logRoute.GET("/self/stat",  middleware.UserAuth(),  controller.GetLogsSelfStat)
logRoute.GET("/self",       middleware.UserAuth(),  controller.GetUserLogs)
logRoute.GET("/search",     middleware.AdminAuth(), controller.SearchAllLogs)      // 已废弃
logRoute.GET("/self/search",middleware.UserAuth(), middleware.SearchRateLimit(), controller.SearchUserLogs)  // 已废弃
```
另有 `:305` `logRoute.GET("/token", middleware.TokenAuthReadOnly(), controller.GetLogByKey)`。

### 6.2 Query 参数（`controller/log.go:13-33`）
`p`、`page_size`（`common.GetPageQuery`，`common/page_info.go:41`）、`type`、`start_timestamp`、`end_timestamp`、`username`(admin only)、`token_name`、`model_name`、`channel`(admin only)、`group`、`request_id`、`upstream_request_id`。

### 6.3 Model 层签名
```go
// model/log.go:468
func GetAllLogs(logType int, startTimestamp int64, endTimestamp int64, modelName string,
    username string, tokenName string, startIdx int, num int, channel int, group string,
    requestId string, upstreamRequestId string) (logs []*Log, total int64, err error)

// model/log.go:564
func GetUserLogs(userId int, logType int, startTimestamp int64, endTimestamp int64,
    modelName string, tokenName string, startIdx int, num int, group string,
    requestId string, upstreamRequestId string) (logs []*Log, total int64, err error)
```
- `model_name` / `username` 支持 `%` 通配（`applyExplicitLogTextFilter`，`model/log.go:19`）
- 排序：普通库 `logs.created_at desc, logs.id desc`；ClickHouse `created_at desc, request_id desc`（`clickHouseLogOrder`，`:106`）
- 用户侧 count 上限 `logSearchCountLimit = 10000`（`:562`）
- 返回：`common.ApiSuccess(c, pageInfo)` → `{ success, message, data: { page, page_size, total, items } }`

前端参数构造：`lib/utils.ts:173` `buildApiParams`，`lib/utils.ts:97` `buildQueryParams`（过滤 `undefined/null/''`，保留 0）。

---

## 七、结论：这两列的数据从哪来

### 模型推理强度
**≈ 90% 是"已有数据只需前端展示"**。
- `other.reasoning_effort` 已经在写、已经下发给普通用户、前端类型（`types.ts:196`）和详情弹窗（`details-dialog.tsx:1017`）都已经支持，**只差一个表格列**。
- **缺口在覆盖率**：Claude 原生 `thinking.budget_tokens`、Gemini 数值 `thinkingBudget`、Qwen `thinking_budget`、通用 OpenAI 兼容渠道的 `reasoning_effort` 都不落库（第 3.3 节）。若产品要求"Claude 请求也要显示推理强度"，**后端必须补记录**。

### 模型缓存百分比
**100% 是"已有数据只需前端展示"**，无需任何后端改动：
- 分子 `other.cache_tokens`（`log_info_generate.go:78`）
- 分母 `log.prompt_tokens`（+ Claude 语义时加 `cache_tokens` + `cache_creation_tokens*`）
- 语义判别键 `other.usage_semantic`（`text_quota.go:475`）/ `other.claude`
- 唯一要做的后端侧"改善"是给 `other` 补一个已算好的分母（可选，不必要）

### 如果确实要后端补记录（reasoning 覆盖率），能否避开原项目文件？
**能，有两条路：**

1. **旁路新库方案（零侵入原文件，推荐）**
   在 relay 路由链上挂一个**新增的中间件**，`c.Next()` 之后从 `c.GetString(common.RequestIdKey)` 取 request_id，用 `common.UnmarshalBodyReusable(c, &probe)`（`common/gin.go:104`，body 已缓存在 `KeyBodyStorage`，可重复读）解析 `reasoning_effort` / `thinking` / `thinkingConfig` / `enable_thinking`，写进**你的独立 MySQL 库**的 `log_extra` 表（`request_id` 唯一键 + `reasoning_effort` + `thinking_budget` + 需要的缓存字段）。
   前端在日志列表加载后按当前页 `request_id` 批量查你的新接口，两边 merge 渲染。
   **原项目改动 = 1 行中间件注册**（`router/api-router.go` relay 路由组）+ 前端列定义。

2. **单点改 `GenerateTextOtherInfo`（1 个文件、约 5 行）**
   在 `service/log_info_generate.go:85` 之后追加一段：`relayInfo.ReasoningEffort` 为空时，从 `ctx` 重读 body 兜底解析并写入 `other["reasoning_effort"]` / `other["thinking_budget"]`。
   优点：数据直接进 `logs.other`，前端一行代码就能读；缺点：改了上游文件（但只增不改，冲突风险极低——这个函数上游改动频率中等）。

**不建议**给 `model.Log` 加数据库列：`logs` 表可能是 ClickHouse（`model/main.go:400-403` 不走 AutoMigrate，需要手写 `ALTER TABLE`），且改结构体一定和上游冲突。

---

## 【扩展点建议】

### 前端（**改动最小、收益最大的挂载点**）

**P0 — 唯一必改文件：`web/src/features/usage-logs/components/columns/common-logs-columns.tsx`**
在 `:600` 的 `columns.push(...)` 参数列表里插入两个新 `ColumnDef`（建议插在 `prompt_tokens` 之后、`quota` 之前）：
```tsx
{ id: 'reasoning_effort', header: t('Reasoning Effort'),
  accessorFn: (row) => parseLogOther(row.other)?.reasoning_effort ?? '',
  cell: ({row}) => {...}, meta: { label: t('Reasoning Effort'), mobileHidden: true }, size: 110 },
{ id: 'cache_hit_rate', header: t('Cache Hit Rate'),
  cell: ({row}) => {...}, meta: { label: t('Cache Hit Rate'), mobileHidden: true }, size: 110 },
```
用 `id` 而非 `accessorKey`（数据不在 `UsageLog` 顶层字段上），这样 zod schema `data/schema.ts` 完全不用动。

**P1 — 计算逻辑放新文件，避免污染 `lib/format.ts`**
新建 `web/src/features/usage-logs/lib/cache-metrics.ts`，导出：
```ts
export function getCacheHitRate(log: UsageLog, other: LogOtherData | null): number | null
export function getReasoningEffortDisplay(other: LogOtherData | null): { label: string; variant: StatusBadgeProps['variant'] } | null
```
（effort → variant 的映射逻辑可从 `details-dialog.tsx:607-612` 抄，避免两处不一致）

**P2 — 类型补声明：`web/src/features/usage-logs/types.ts`**
在 `LogOtherData`（`:115-244`）里补 3 个缺失 key：`usage_semantic?: string`、`cache_write_tokens?: number`、`input_tokens_total?: number`。

**P3 — 移动端（可选）：`usage-logs-mobile-card.tsx:190` `MobileTokensField`**
在 `Cache↓ N ↑ M` 那一行后面追加 `(xx%)`；`:310` `CommonLogsCard` 布局不需要动。

**P4 — i18n**：7 个 locale 文件补 `"Cache Hit Rate"`（`Reasoning Effort` 已存在）。

**受影响原文件最小集（前端）：`common-logs-columns.tsx`、`types.ts`、7 个 locale JSON**（后两者是纯追加，合并零冲突）。

---

### 后端

**方案 A（零侵入，配合独立 MySQL 库需求）**
- 新增：`middleware/<你的前缀>_log_extra.go` —— relay 后置中间件，从 `common.GetBodyStorage(c)` 解析推理参数 → 写你的独立库
- 新增：`controller/<你的前缀>_log_extra.go` + `router` 里注册一条 `GET /api/<prefix>/log-extra?request_ids=a,b,c`
- **原项目改动：`router/api-router.go` 里 relay 路由组加 1 行 `.Use(middleware.XxxLogExtra())`，以及 API 路由组加 1 行 GET 注册**
- 关联键：`Log.RequestId`（`model/log.go:78`，`varchar(64)`，有索引 `idx_logs_request_id`），由 `ensureLogRequestId`（`model/log.go:95`）保证非空
- 注意：`RecordConsumeLog` 是同步调用（`text_quota.go:526`），而中间件在 `c.Next()` 后执行，**两者时序上中间件更晚**，用 request_id 关联安全

**方案 B（1 文件微创，只为补 reasoning 覆盖率）**
- 唯一改动点：`service/log_info_generate.go:83-85` 那个 `if` 后面追加兜底解析
- 这个函数是**所有文本/音频/WSS/Claude 消费日志的唯一收敛点**，改一处即可覆盖全部 relay 路径（`text_quota.go:468,477`、`quota.go:239,362` 都间接经过它）
- 若同时要把数据写进独立库，可在此函数尾部调用你自建 package 的 `logextra.Record(ctx, relayInfo, other)`

**不要碰的地方**：`model/log.go` 的 `Log` 结构体（ClickHouse 迁移分支不自动同步）、`model/log.go:116-132` `formatUserLogs`（脱敏白名单，改了会影响权限语义）、`controller/log.go`（无 DTO，`Log` 直出，加字段不需要动它）。
