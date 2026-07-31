# 计费扣费与消费日志写入

I have the complete picture. Here is my survey.

---

# 计费 / 扣费 / 消费日志写入 — 勘察报告

## 1. 消费结算完整链路（预扣 → 实际消费 → 结算/退差额）

链路总入口是 `controller/relay.go` 的 `relayRequest`（重试循环所在函数）。

### 1.1 预扣费（pre-consume）

统一入口在 `service/billing.go:20`：

```go
func PreConsumeBilling(c *gin.Context, preConsumedQuota int, relayInfo *relaycommon.RelayInfo) *types.NewAPIError
```

调用点：`controller/relay.go:166`（`priceData.FreeModel` 为 false 时）。它内部：
1. 先检查 `relayInfo.QuotaClamp != nil`（额度饱和则 400 直接拒绝，`service/billing.go:21-28`）；
2. `NewBillingSession(c, relayInfo, preConsumedQuota)`（`service/billing_session.go:342`）根据 `UserSetting.BillingPreference`（`subscription_only` / `wallet_only` / `wallet_first` / `subscription_first`）选资金源并回退；
3. 会话赋给 `relayInfo.Billing`。

实际预扣在 `service/billing_session.go:186`：

```go
func (s *BillingSession) preConsume(c *gin.Context, quota int) *types.NewAPIError
```

顺序：信任额度旁路检查 `shouldTrust`（`billing_session.go:282`）→ 令牌预扣 `PreConsumeTokenQuota` → 资金源预扣 `funding.PreConsume`；任一失败原子回滚。

令牌层预扣在 `service/quota.go:387`：

```go
func PreConsumeTokenQuota(relayInfo *relaycommon.RelayInfo, quota int) error
```

资金源抽象 `FundingSource` 接口在 `service/funding_source.go:14`，两个实现 `WalletFunding`（:29）和 `SubscriptionFunding`（:69）。

请求中途还可追加预扣：`(*BillingSession).Reserve(targetQuota int) error`（`service/billing_session.go:152`）。

### 1.2 实际消费计算 + 结算

三条 Post 路径，均以「算 quota → 更新已用统计 → SettleBilling → 构造 other → RecordConsumeLog」为骨架：

| 路径 | 函数 | 位置 |
|---|---|---|
| 文本/主路径 | `PostTextConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent []string)` | `service/text_quota.go:397` |
| 音频 | `PostAudioConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, usage *dto.Usage, extraContent string)` | `service/quota.go:282` |
| Realtime WSS | `PostWssConsumeQuota(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, modelName string, usage *dto.RealtimeUsage, extraContent string)` | `service/quota.go:158` |

统一结算函数 `service/billing.go:51`：

```go
func SettleBilling(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, actualQuota int) error
```

有 `relayInfo.Billing` 时走 `(*BillingSession).Settle(actualQuota int) error`（`service/billing_session.go:41`，`delta = actualQuota - preConsumedQuota`，正数补扣、负数退还，资金源与令牌两步提交）；没有 session 时回退到旧路径 `PostConsumeQuota(relayInfo, quotaDelta, relayInfo.FinalPreConsumedQuota, true)`。

底层旧接口 `service/quota.go:411`：

```go
func PostConsumeQuota(relayInfo *relaycommon.RelayInfo, quota int, preConsumedQuota int, sendEmail bool) (err error)
```

**这是「独立扣一笔钱」最有用的函数**：`quota > 0` 扣、`< 0` 退；自动区分 wallet / subscription（依据 `relayInfo.BillingSource`，常量在 `service/billing.go:13-16`：`BillingSourceWallet = "wallet"` / `BillingSourceSubscription = "subscription"`）；同时同步扣减令牌额度（除非 `IsPlayground`）；`sendEmail=true` 时触发余额告警 `checkAndSendQuotaNotify`（`service/quota.go:457`）。

### 1.3 退款

- 请求失败：`controller/relay.go:173-181` 的 defer 中调用 `relayInfo.Billing.Refund(c)`（`service/billing_session.go:82`，幂等、异步 gopool）。
- 异步任务：`service/task_billing.go:166` `RefundTaskQuota(ctx, task, reason)`；差额结算 `service/task_billing.go:210` `RecalculateTaskQuota(ctx, task, actualQuota, reason, clamps ...*common.QuotaClamp)`。

底层额度操作签名：
- `model.DecreaseUserQuota(id int, quota int, db bool) error` — `model/user.go:1257`
- `model.IncreaseUserQuota(id int, quota int, db bool) error` — `model/user.go:1232`
- `model.UpdateUserUsedQuotaAndRequestCount(id int, quota int)` — `model/user.go:1309`
- `model.UpdateChannelUsedQuota(id int, quota int)` — `model/channel.go:861`
- `model.DecreaseTokenQuota(id int, key string, quota int) error` — `model/token.go:412`
- `model.IncreaseTokenQuota(tokenId int, key string, quota int) error` — `model/token.go:382`

---

## 2. 消费日志是怎么写的（`service/log_info_generate.go` 全文理解）

### 2.1 `other` 字段的构造

`service/log_info_generate.go:72`：

```go
func GenerateTextOtherInfo(ctx *gin.Context, relayInfo *relaycommon.RelayInfo,
    modelRatio, groupRatio, completionRatio float64,
    cacheTokens int, cacheRatio float64, modelPrice float64, userGroupRatio float64) map[string]interface{}
```

固定写入：`model_ratio`、`group_ratio`、`completion_ratio`、`cache_tokens`、`cache_ratio`、`model_price`、`user_group_ratio`、`frt`（首字节延迟 ms）；条件写入 `reasoning_effort`、`is_model_mapped` + `upstream_model_name`、`is_system_prompt_overwritten`。

然后组装 `admin_info` 子对象（`use_channel`、`is_multi_key`、`multi_key_index`、`local_count_tokens`、渠道亲和信息），写入 `other["admin_info"]`。随后依次调用：`appendRequestPath`（:53）、`appendRequestConversionChain`（:209）、`appendFinalRequestFormat`（:237）、`appendBillingInfo`（:155，写 `billing_source` / `subscription_*` 等）、`appendParamOverrideInfo`（:121）、`appendStreamStatus`（:128，写 `stream_status.status = "ok"|"error"`、`end_reason`、`error_count`、`errors`）。

变体：
- `GenerateWssOtherInfo(...)` — :248
- `GenerateAudioOtherInfo(...)` — :260
- `GenerateClaudeOtherInfo(...)` — :272
- `GenerateMjOtherInfo(relayInfo, priceData hosttypes.PriceData)` — :293
- `InjectTieredBillingInfo(other map[string]interface{}, relayInfo *relaycommon.RelayInfo, result *billingexpr.TieredResult)` — :307

**额度饱和审计（AGENTS.md 强制）**：
- `attachQuotaSaturationToOther(other map[string]interface{}, clamp *common.QuotaClamp)` — :25
- `attachQuotaSaturation(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, other map[string]interface{})` — :40，必须在 `RecordConsumeLog` 之前调用；marker 落在 `other.admin_info.quota_saturation`。

**关键机制**：`model.formatUserLogs`（`model/log.go:116`）在普通用户查询时会 `delete(otherMap, "admin_info")`、`delete(otherMap, "audit_info")`、`delete(otherMap, "stream_status")`。所以**想让某字段只给管理员看，塞进 `admin_info` 即可，零额外成本**。注意 `reject_reason` 那行是被注释掉的（`model/log.go:126`），即违规原因目前**对普通用户可见**。

### 2.2 `RecordConsumeLog` 完整签名

`model/log.go:328-404`：

```go
type RecordConsumeLogParams struct {
    ChannelId        int                    `json:"channel_id"`
    PromptTokens     int                    `json:"prompt_tokens"`
    CompletionTokens int                    `json:"completion_tokens"`
    ModelName        string                 `json:"model_name"`
    TokenName        string                 `json:"token_name"`
    Quota            int                    `json:"quota"`
    Content          string                 `json:"content"`
    TokenId          int                    `json:"token_id"`
    UseTimeSeconds   int                    `json:"use_time_seconds"`
    IsStream         bool                   `json:"is_stream"`
    Group            string                 `json:"group"`
    Other            map[string]interface{} `json:"other"`
}

func RecordConsumeLog(c *gin.Context, userId int, params RecordConsumeLogParams)
```

行为要点：
- 开头 `if !common.LogConsumeEnabled { return }`（`common/constants.go:93`，默认 `true`）。
- `Username` 取自 `c.GetString("username")`，`RequestId` / `UpstreamRequestId` 取自 context key。
- IP 只在 `GetUserSetting(userId).RecordIpLog` 为真时记录。
- `common.DataExportEnabled`（默认 true）时额外调 `LogQuotaData(QuotaDataLogParams{...})`（`model/usedata.go:28`）写统计表。
- **必须有 `*gin.Context`**。无 context 的场景（异步任务）用 `model.RecordTaskBillingLog(params RecordTaskBillingLogParams)`（`model/log.go:419`，结构体 :406），它自己按 `UserId` 查 username、按 `TokenId` 查 token name。

其他写日志函数：
- `RecordLog(userId int, logType int, content string)` — `model/log.go:144`
- `RecordLogWithAdminInfo(userId int, logType int, content string, adminInfo map[string]interface{})` — :163
- `RecordErrorLog(c *gin.Context, userId, channelId int, modelName, tokenName, content string, tokenId, useTimeSeconds int, isStream bool, group string, other map[string]interface{})` — :282
- `RecordOperationAuditLog(logUserId int, content, ip, action string, params, adminInfo, auditInfo map[string]interface{})` — :229
- `RecordTopupLog(userId int, content, callerIp, paymentMethod, callbackPaymentMethod string)` — :254

底层统一 `createLog(log *Log) error`（:101）→ `LOG_DB.Create(log)`；`ensureLogRequestId` 自动补 `request_id`。

---

## 3. 如何独立地「扣一笔费 + 写一条日志」

**项目里已经有一份现成的、几乎逐字对应你需求的实现**：`service/violation_fee.go`（全文 165 行）。它就是「违规时给用户写一条计费记录说明扣费原因」。

核心函数 `service/violation_fee.go:104`：

```go
func ChargeViolationFeeIfNeeded(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError) bool
```

它的完整动作序列（这就是**最小复用集**）：

```go
// 1) 算钱（用 decimal，禁止裸 cast）—— service/violation_fee.go:84 calcViolationFeeQuota
feeQuota := calcViolationFeeQuota(settings.ViolationDeductionAmount, groupRatio)

// 2) 扣钱（自动区分 wallet/subscription + 同步扣令牌 + 余额告警）
if err := PostConsumeQuota(relayInfo, feeQuota, 0, true); err != nil { ... }

// 3) 更新用户/渠道已用统计
model.UpdateUserUsedQuotaAndRequestCount(relayInfo.UserId, feeQuota)
model.UpdateChannelUsedQuota(relayInfo.ChannelId, feeQuota)

// 4) 写日志
model.RecordConsumeLog(ctx, relayInfo.UserId, model.RecordConsumeLogParams{...})
```

`other` map 的现有约定（`service/violation_fee.go:138-148`）：

```go
other := map[string]any{
    "violation_fee":        true,
    "violation_fee_code":   string(types.ErrorCodeViolationFeeGrokCSAM),
    "fee_quota":            feeQuota,
    "base_amount":          settings.ViolationDeductionAmount,
    "group_ratio":          groupRatio,
    "status_code":          apiErr.StatusCode,
    "upstream_error_type":  oai.Type,
    "upstream_error_code":  fmt.Sprintf("%v", oai.Code),
    "violation_fee_marker": CSAMViolationMarker,
}
```

配套的错误归一化：`NormalizeViolationFeeError(err *types.NewAPIError) *types.NewAPIError`（:56）、`IsViolationFeeCode(code types.ErrorCode) bool`（:26）、`HasCSAMViolationMarker(err) bool`（:30）、`WrapAsViolationFeeGrokCSAM(err)`（:41）、私有 `shouldChargeViolationFee(err) bool`（:73）。

常量（:20-24）：
```go
ViolationFeeCodePrefix     = "violation_fee."
CSAMViolationMarker        = "Failed check: SAFETY_CHECK_TYPE"
ContentViolatesUsageMarker = "Content violates usage guidelines"
```

配置来源 `setting/model_setting/grok.go:6`：`GrokSettings{ ViolationDeductionEnabled bool; ViolationDeductionAmount float64 }`，默认 `true` / `0.05`。

### 挂载点（只改了 2 行原文件）

`controller/relay.go:173-181`：

```go
defer func() {
    if newAPIError != nil {
        newAPIError = service.NormalizeViolationFeeError(newAPIError)   // :176
        if relayInfo.Billing != nil {
            relayInfo.Billing.Refund(c)
        }
        service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)   // :180
    }
}()
```

注意执行顺序：**先退预扣费，再扣违规费**，这是正确的（避免 Refund 把违规费一起退掉）。

### 结论 (a)：违规扣费 + 写日志最少复用哪些函数

有 `*gin.Context` 和 `*relaycommon.RelayInfo` 时（同步 relay 路径），**最少 4 个调用**：

1. `common.QuotaFromDecimal(d decimal.Decimal) int` — 算 quota（或 `QuotaFromDecimalChecked` 拿 clamp）
2. `service.PostConsumeQuota(relayInfo, feeQuota, 0, true)` — 扣钱（钱包/订阅 + 令牌，全包）
3. `model.UpdateUserUsedQuotaAndRequestCount(userId, feeQuota)` +（可选）`model.UpdateChannelUsedQuota(channelId, feeQuota)` — 已用统计
4. `model.RecordConsumeLog(ctx, userId, model.RecordConsumeLogParams{...})` — 写日志

如果还要保持 clamp 审计规范，插一句 `service.attachQuotaSaturation(ctx, relayInfo, other)`（同包内）或对外用 `attachQuotaSaturationToOther`。

**没有 gin.Context 的场景**（后台任务/异步判定违规）：把 3+4 换成
`model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{ UserId, LogType: model.LogTypeConsume, Content, ChannelId, ModelName, Quota, TokenId, Group, Other, NodeName })`；扣钱则需自己拼 `model.DecreaseUserQuota` + `model.DecreaseTokenQuota`（因为 `PostConsumeQuota` 依赖 `relayInfo`）。

---

## 4. 计费安全约束（`common/quota_math.go`，AGENTS.md 强制）

```go
const (
    MaxQuota = math.MaxInt32   // :15
    MinQuota = math.MinInt32   // :16
)

type QuotaClampKind string
const (
    QuotaClampOverflow  QuotaClampKind = "overflow"    // :24
    QuotaClampUnderflow QuotaClampKind = "underflow"   // :25
    QuotaClampNaN       QuotaClampKind = "nan"         // :26
)

type QuotaClamp struct {                                // :33
    Op       string         `json:"op"`       // "QuotaFromFloat" | "QuotaRound" | "QuotaFromDecimal"
    Kind     QuotaClampKind `json:"kind"`
    Original float64        `json:"original"`
    Clamped  int            `json:"clamped"`
}
func (c *QuotaClamp) Error() string                     // :42
func (c *QuotaClamp) AuditMap() map[string]interface{}  // :52
```

三组转换助手（每组三个变体）：

| 用途 | 基础 | Checked（返回 clamp） | Strict（clamp 变 error） |
|---|---|---|---|
| float 乘积，向零截断 | `QuotaFromFloat(value float64) int` :98 | `QuotaFromFloatChecked(value float64) (int, *QuotaClamp)` :105 | `QuotaFromFloatStrict(value float64) (int, error)` :111 |
| 四舍五入（half-away-from-zero） | `QuotaRound(value float64) int` :119 | `QuotaRoundChecked(value float64) (int, *QuotaClamp)` :126 | `QuotaRoundStrict(value float64) (int, error)` :132 |
| decimal 乘积 | `QuotaFromDecimal(d decimal.Decimal) int` :138 | `QuotaFromDecimalChecked(d decimal.Decimal) (int, *QuotaClamp)` :145 | — |

AGENTS.md 明确禁止：`int(float64(quota) * ratio)`、`int(math.Round(...))`、`int(decimal.IntPart())` 等裸转换，禁止重新引入本地转换助手。

`common.QuotaPerUnit = 500 * 1000.0`（`common/constants.go:22`），即 1 美元 = 500000 quota。

⚠️ 注意：现有的 `calcViolationFeeQuota`（`service/violation_fee.go:91-99`）用的是 `decimal...Round(0).IntPart()` + `int(quota)`，**违反了 AGENTS.md 的约定**（应该用 `common.QuotaFromDecimal`）。新代码不要照抄这一段。

---

## 5. 模型可用率统计的数据基础

### 5.1 `Log` 表字段（`model/log.go:59-81`）

```go
type Log struct {
    Id                int    `gorm:"index:idx_created_at_id,priority:2;index:idx_user_id_id,priority:2"`
    UserId            int    `gorm:"index;index:idx_user_id_id,priority:1"`
    CreatedAt         int64  `gorm:"bigint;index:idx_created_at_id,priority:1;index:idx_created_at_type"`
    Type              int    `gorm:"index:idx_created_at_type"`
    Content           string
    Username          string `gorm:"index;index:index_username_model_name,priority:2;default:''"`
    TokenName         string `gorm:"index;default:''"`
    ModelName         string `gorm:"index;index:index_username_model_name,priority:1;default:''"`
    Quota             int    `gorm:"default:0"`
    PromptTokens      int    `gorm:"default:0"`
    CompletionTokens  int    `gorm:"default:0"`
    UseTime           int    `gorm:"default:0"`
    IsStream          bool
    ChannelId         int    `json:"channel" gorm:"index"`
    ChannelName       string `gorm:"->"`               // 非持久化，查询时 join 填充
    TokenId           int    `gorm:"default:0;index"`
    Group             string `gorm:"index"`
    Ip                string `gorm:"index;default:''"`
    RequestId         string `gorm:"type:varchar(64);index:idx_logs_request_id;default:''"`
    UpstreamRequestId string `gorm:"type:varchar(128);index:idx_logs_upstream_request_id;default:''"`
    Other             string
}
```

日志类型常量（`model/log.go:84-93`，注释明确要求不要用 iota、不要改值）：

```go
LogTypeUnknown = 0
LogTypeTopup   = 1
LogTypeConsume = 2
LogTypeManage  = 3
LogTypeSystem  = 4
LogTypeError   = 5
LogTypeRefund  = 6
LogTypeLogin   = 7
```

### 5.2 成功/失败的可辨识度

- **成功** → `Type = LogTypeConsume(2)`，且 `channel_id` / `group` / `model_name` 都有。
- **失败** → `Type = LogTypeError(5)`，通过 `RecordErrorLog`（`model/log.go:282`）写入，`other` 里有 `error_type` / `error_code` / `status_code` / `channel_id` / `channel_name` / `channel_type` / `request_path`（构造在 `controller/relay.go:377-397`）。
- **软失败** → 成功的 consume 日志里 `other.stream_status.status == "error"`（`service/log_info_generate.go:128-153`），以及 `total tokens is 0` 的「上游超时」情况（`quota=0`、content 追加 `"（可能是上游超时）"`，见 `service/quota.go:222` / `service/text_quota.go:445`）。
- 违规扣费日志目前也是 `LogTypeConsume`，靠 `other.violation_fee == true` 区分。

### 5.3 三个致命缺口

1. **`ErrorLogEnabled` 默认 `false`**
   `common/init.go:196`：`constant.ErrorLogEnabled = GetEnvOrDefaultBool("ERROR_LOG_ENABLED", false)`。
   `controller/relay.go:370` 的 `if constant.ErrorLogEnabled && types.IsRecordErrorLog(err)` 是唯一的 error log 写入闸门。**默认部署下失败请求根本不落 Log 表**，分母全丢。

2. **部分错误被显式排除**
   `types.ErrOptionWithNoRecordErrorLog()`（`relaykit/types/error.go:387`）+ `types.IsRecordErrorLog(e)`（:408）。所有额度不足类错误（`service/billing_session.go:201, 219, 247, 276, 359, 365`）都带这个 option，不写 error log。这类其实也不该算「模型不可用」，属于合理排除，但要知道。

3. **error log 的 `Group` 语义与 consume log 不一致**
   - consume log：`Group: relayInfo.UsingGroup`（真实使用的分组，auto 跨组重试会变，`relay/common/relay_info.go:88`）
   - error log：`Group: c.GetString("group")`（`controller/relay.go:375, 402`），这是 `middleware/auth.go:199` 设的 **`user.Group`（用户所属分组）**，不是 UsingGroup。
   → **直接按 `logs.group` 做「分组 × 模型」成功率，两类日志的 group 含义对不上，统计会错。**

   另外 error log 的 `ModelName` 取 `c.GetString("original_model")`，而 consume log 用 `summary.ModelName`（经过 gizmo 归一化 `gpt-4-gizmo-*` 等，`service/text_quota.go:459-466`），也存在轻微口径差。

### 5.4 但项目已经有更好的现成方案：`pkg/perf_metrics`

**这套系统的维度正好就是 (model, group, 时间桶)，且已经在算成功率**。

采样：`pkg/perf_metrics/metrics.go:27`
```go
func RecordRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64)
// 内部取 Model: info.OriginModelName, Group: info.UsingGroup
```

调用点仅 3 处：
- `controller/relay.go:249` — `RecordRelaySample(relayInfo, false, 0)`（整个重试循环结束仍失败）
- `service/text_quota.go:541` — `RecordRelaySample(relayInfo, true, int64(summary.CompletionTokens))`
- `service/quota.go:383` — 音频路径 success=true

聚合结构 `pkg/perf_metrics/types.go:10`：
```go
type Sample struct {
    Model, Group string
    LatencyMs, TtftMs int64
    HasTtft, Success  bool
    OutputTokens, GenerationMs int64
}
```

落库表 `model/perf_metric.go:11`（表名 `perf_metrics`，**注意这张表在主 `DB` 上，不是 `LOG_DB`**）：
```go
type PerfMetric struct {
    Id             int
    ModelName      string `gorm:"size:128;uniqueIndex:idx_perf_model_group_bucket,priority:1"`
    Group          string `gorm:"column:group;size:64;uniqueIndex:idx_perf_model_group_bucket,priority:2"`
    BucketTs       int64  `gorm:"uniqueIndex:idx_perf_model_group_bucket,priority:3;index:idx_perf_bucket_ts"`
    RequestCount   int64
    SuccessCount   int64
    TotalLatencyMs int64
    TtftSumMs      int64
    TtftCount      int64
    OutputTokens   int64
    GenerationMs   int64
}
```

查询 API：
- `model.GetPerfMetrics(modelName, group string, startTs, endTs int64) ([]PerfMetric, error)` — `model/perf_metric.go:51`
- `model.GetPerfMetricsSummaryAll(startTs, endTs int64, groups []string) ([]PerfMetricSummary, error)` — :81
- `model.GetPerfMetricsSummaryBucketsAll(...)` — :99
- `model.DeletePerfMetricsBefore(cutoffTs int64) error` — :118
- `perfmetrics.Query(params QueryParams) (QueryResult, error)` — `pkg/perf_metrics/metrics.go:79`（返回按 group 拆分的 `[]GroupResult`，每个含 `SuccessRate` 和 `Series []BucketPoint`）
- `perfmetrics.QuerySummaryAll(hours int, groups []string) (SummaryAllResult, error)` — :125

内存热桶 `sync.Map hotBuckets`（key = `bucketKey{model, group, bucketTs}`），`flushLoop`（`pkg/perf_metrics/flush.go:13`）按 `FlushInterval` 分钟落库（`UpsertPerfMetric` 用 `clause.OnConflict` 做累加 upsert）。Redis 可选做多节点合并（`recordRedis` :380，key 格式 `perf:{model}:{group}:{bucketTs}`）。

配置 `setting/perf_metrics_setting/config.go`：
```go
type PerfMetricsSetting struct {
    Enabled       bool   `json:"enabled"`        // 默认 true
    FlushInterval int    `json:"flush_interval"` // 默认 5 (分钟)
    BucketTime    string `json:"bucket_time"`    // "minute"|"5min"|"hour"，默认 "hour"
    RetentionDays int    `json:"retention_days"` // 默认 0 = 不清理
}
```
注册名 `"perf_metrics_setting"`（`config.GlobalConfig.Register`）。

初始化：`perfmetrics.Init()` → `go flushLoop()`（`pkg/perf_metrics/metrics.go:23`）。

### 5.5 结论 (b)：现有日志数据能否支撑「按分组统计每个模型可用率」？

**用 `logs` 表：不能直接支撑，缺 3 样东西。**
1. 分母缺失 —— `ERROR_LOG_ENABLED` 默认关闭，失败请求不落表。
2. group 口径不一致 —— consume 用 `UsingGroup`，error 用 `user.Group`。
3. 没有 status/success 布尔列 —— 只能靠 `type=2 vs type=5` 推断，且软失败（stream_status=error、totalTokens=0）混在 `type=2` 里；要判别必须 JSON 解析 `other`，无法走索引。

**用 `perf_metrics` 表：完全够用，而且这就是模型广场「可用性」的现成数据源。**
`(model_name, group, bucket_ts) → request_count / success_count` 唯一索引齐备，`SuccessRate = success_count / request_count * 100`。

**仍需补的缺口（如果要更严格的可用率）：**
- `RecordRelaySample` 在**整个重试循环结束后**才记一次失败（`controller/relay.go:249`），而成功是在最终成功的那次记的 → 统计的是「用户视角端到端可用率」，**不是「单渠道单次尝试成功率」**。若要按渠道粒度统计，需要新增按 attempt 的采样（`perf_metrics` 表没有 `channel_id` 列）。
- 失败样本的 `info.UsingGroup` 在渠道选择就失败时可能为空，`Record` 会兜底成 `"default"`（`pkg/perf_metrics/metrics.go:62-64`），会污染 default 分组。
- 没有失败原因维度（无 error_code / status_code 分布）。
- `Group == ""` 的行会被 `filterActiveGroups`（`controller/perf_metrics.go:76`）过滤掉，只保留 `ratio_setting.GetGroupRatioCopy()` 里存在的分组 + `"auto"`。
- 重试成功的请求只算 1 次成功，中间失败的 attempt 不计入分母 → 可用率偏高。

---

## 6. 渠道可用性 / 健康检查机制

### 6.1 主动测试（`controller/channel-test.go`，1064 行）

- `testChannel(ctx context.Context, channel *model.Channel, testUserID int, testModel string, endpointType string, isStream bool) testResult` — :76
- `TestChannel(c *gin.Context)` — :828（单渠道手测）
- `TestAllChannels(c *gin.Context)` — :1035
- `performChannelTests(ctx context.Context, channels []*model.Channel, testUserID int, allowDisable bool, report func(processed, total int)) channelTestSummary` — :907
- `runChannelTestTask(ctx context.Context, mode string, notify bool, report func(processed, total int)) (channelTestSummary, error)` — :997
- `selectChannelsForAutomaticTest(channels []*model.Channel, mode string) []*model.Channel` — :1018

测试本身**也会真实计费并写一条 consume log**（`controller/channel-test.go:499-510`，`TokenName: "模型测试"`、`Content: "模型测试"`），quota 计算在 `settleTestQuota`（:534），other 构造在 `buildTestLogOther`（:556，直接复用 `service.GenerateTextOtherInfo`）。

### 6.2 自动禁用/启用（`service/channel.go`）

```go
func DisableChannel(channelError types.ChannelError, reason string)         // :19
func EnableChannel(channelId int, usingKey string, channelName string)      // :36
func ShouldDisableChannel(err *types.NewAPIError) bool                      // :45
func ShouldEnableChannel(newAPIError *types.NewAPIError, status int) bool   // :67
```

判定依据：`common.AutomaticDisableChannelEnabled` 开关 → `types.IsChannelError(err)` → `types.IsSkipRetryError(err)` → `operation_setting.ShouldDisableByStatusCode(err.StatusCode)` → AC 自动机关键词匹配 `operation_setting.AutomaticDisableKeywords`。
状态常量：`common.ChannelStatusEnabled` / `common.ChannelStatusAutoDisabled`。
触发点：`processChannelError`（`controller/relay.go:360-368`，`gopool.Go` 异步）和 `performChannelTests`（:940-966）。

**注意：渠道禁用状态与 `perf_metrics` 完全解耦**，模型广场的「可用性」只来自 `perf_metrics`，跟渠道 enabled/disabled 无关。

### 6.3 模型广场「可用性」数据来源

后端 `controller/perf_metrics.go`：
- `GetPerfMetricsSummary(c *gin.Context)` — :14 → `perfmetrics.QuerySummaryAll(hours, activeGroups)`，`activeGroups = append(lo.Keys(ratio_setting.GetGroupRatioCopy()), "auto")`
- `GetPerfMetrics(c *gin.Context)` — :38 → `perfmetrics.Query(QueryParams{Model, Group, Hours})`
- `filterActiveGroups(groups []perfmetrics.GroupResult) []perfmetrics.GroupResult` — :76

路由 `router/api-router.go:35-40`：
```go
perfMetricsRoute := apiRouter.Group("/perf-metrics")
perfMetricsRoute.Use(middleware.HeaderNavModulePublicOrUserAuth("pricing"))
{
    perfMetricsRoute.GET("/summary", controller.GetPerfMetricsSummary)
    perfMetricsRoute.GET("", controller.GetPerfMetrics)
}
```

前端：`web/src/features/performance-metrics/api.ts`（`getPerfMetricsSummary(hours)` / `getPerfMetrics(modelName, hours)`）、`types.ts`（`PerformanceGroup.success_rate`、`PerfModelSummary.success_rate` / `recent_success_rates`）。消费方：
- `web/src/features/pricing/components/model-perf-badge.tsx`（模型广场卡片上的可用性徽章）
- `web/src/features/pricing/components/model-details-performance.tsx`
- `web/src/features/pricing/components/model-card-grid.tsx`
- `web/src/features/dashboard/components/models/performance-overview.tsx`
- `web/src/features/dashboard/components/overview/performance-health-panel.tsx`
- 设置面板：`web/src/features/system-settings/operations/section-registry.tsx`

**另有一个独立的外部健康源**：`apiRouter.GET("/uptime/status", controller.GetUptimeKumaStatus)`（`router/api-router.go:25`），对接 Uptime Kuma，与本项目内部统计无关。

---

## 7. 日志表索引情况

`logs` 表索引（全部由 GORM tag 声明，`model/log.go:60-79`，无独立 migration）：

| 索引名 | 列（顺序） |
|---|---|
| `idx_created_at_id` | `created_at`, `id` |
| `idx_user_id_id` | `user_id`, `id` |
| `idx_created_at_type` | `created_at`, `type` |
| `index_username_model_name` | `model_name`, `username` |
| `idx_logs_request_id` | `request_id` |
| `idx_logs_upstream_request_id` | `upstream_request_id` |
| 单列 | `user_id`, `username`, `token_name`, `model_name`, `channel_id`, `token_id`, `group`, `ip` |

`Quota` / `PromptTokens` / `CompletionTokens` / `UseTime` / `IsStream` / `Content` / `Other` **无索引**。

### 对统计查询的影响

- **`(group, model_name, created_at, type)` 这个组合没有覆盖索引**。要做「按分组统计每个模型可用率」，MySQL 只能选 `group` 或 `model_name` 单列索引，剩下的回表过滤 → 大表上会很慢。
- `idx_created_at_type` 对「某时间段内 type=5 的数量」友好，但**列顺序是 (created_at, type)**，对「type=5 且 created_at 范围」是可用的（range on leading col），但没有 group/model 维度。
- `Other` 是 TEXT，任何基于 `other.stream_status` / `other.violation_fee` 的过滤都是全表扫。
- 用户侧查询 `GetUserLogs` 用 `Limit(logSearchCountLimit)`（= 10000，`model/log.go:562`）限制 count，说明作者已经意识到 count 慢。
- ClickHouse 分支：`common.UsingLogDatabase(common.DatabaseTypeClickHouse)` 时排序换成 `created_at desc, request_id desc`（`clickHouseLogOrder`，`model/log.go:106`），建表 SQL 在 `model/main.go:408` 的 `clickHouseLogCreateTableSQL(ttlDays)`。

主要查询函数：
- `GetAllLogs(logType int, startTimestamp, endTimestamp int64, modelName, username, tokenName string, startIdx, num, channel int, group, requestId, upstreamRequestId string) ([]*Log, int64, error)` — `model/log.go:468`
- `GetUserLogs(userId, logType int, startTimestamp, endTimestamp int64, modelName, tokenName string, startIdx, num int, group, requestId, upstreamRequestId string) ([]*Log, int64, error)` — :564
- `SumUsedQuota(logType int, startTimestamp, endTimestamp int64, modelName, username, tokenName string, channel int, group string) (Stat, error)` — :618
- `SumUsedToken(...)` — :674
- `CountOldLog` / `DeleteOldLogBatch` — :695 / :703

`group` 列名因方言不同需转义，用 `model/main.go:33-48` 的 `commonGroupCol` / `logGroupCol` 变量（`` `group` `` for MySQL，`"group"` for PG）。

### 日志库已支持独立 DB（对你的「独立 MySQL」约束很重要）

`model/main.go:212-235` `InitLogDB()`：`LOG_SQL_DSN` 为空则 `LOG_DB = DB`，否则 `chooseDB("LOG_SQL_DSN", true)` 建独立连接。`model/main.go:702-703` 关闭时判断 `LOG_DB != DB`。
即：**项目已有「第二个数据库句柄」的先例和模式，新增第三个（你的独立 MySQL）完全同构。**

---

## 【扩展点建议】

### A. 违规扣费 + 写日志 —— 已有干净扩展点，只需改 1 个文件的 1~2 行

**最推荐路线：新增 `service/xxx_fee.go`，仿 `service/violation_fee.go`，挂在同一个 defer 里。**

改动原文件的最小集合：**只有 `controller/relay.go` 一处 defer 块（:173-181）**。

```go
defer func() {
    if newAPIError != nil {
        newAPIError = service.NormalizeViolationFeeError(newAPIError)
        if relayInfo.Billing != nil {
            relayInfo.Billing.Refund(c)
        }
        service.ChargeViolationFeeIfNeeded(c, relayInfo, newAPIError)
        // ↓ 新增 1 行即可挂载你全部的违规扣费逻辑
        yourpkg.ChargeCustomViolationFee(c, relayInfo, newAPIError)
    }
}()
```

**更零侵入的做法**：不改 `controller/relay.go`，而是在你的新包里定义一个 hook 注册表，然后**只在 `service/violation_fee.go` 的 `ChargeViolationFeeIfNeeded` 末尾加一行 hook 调用**——但这仍是改原文件。两者改动量相当，直接改 `controller/relay.go` 更直白、冲突面更小（那个 defer 块很稳定）。

**新建文件清单：**
- `service/`（或新包 `extension/violation/`）：`ChargeXxxFee(ctx *gin.Context, relayInfo *relaycommon.RelayInfo, apiErr *types.NewAPIError) bool`
- 你的独立 DB 层：违规规则表 + 违规扣费明细表

**复用（不要重写）：**
- `service.PostConsumeQuota` — 钱包/订阅/令牌一把梭
- `model.RecordConsumeLog` + `model.UpdateUserUsedQuotaAndRequestCount` + `model.UpdateChannelUsedQuota`
- `common.QuotaFromDecimal` / `QuotaFromDecimalChecked`
- `service.GenerateTextOtherInfo`（如果想让日志 other 结构跟正常消费记录一致）
- `attachQuotaSaturationToOther`（跨包用的话需要 export 一个包装，或直接写 `other["admin_info"]["quota_saturation"] = clamp.AuditMap()`）

**双写策略**：违规明细写你的独立 MySQL（完整原因、规则 id、证据），同时调 `model.RecordConsumeLog` 往原 `logs` 表写一条给用户看（`other` 里放 `violation_fee: true` + 你的 `reason` + 独立库的记录 id 作为外键）。这样用户在原生日志页就能看到扣费原因，不用改前端日志页。
若原因不想给普通用户看，塞进 `other["admin_info"]`（`model/log.go:122` 会自动为普通用户剥离）。

**非 relay 路径**（后台批量判违规）：用 `model.RecordTaskBillingLog`（不需要 gin.Context）。

### B. 模型可用率统计 —— 两条路，强烈建议走 perf_metrics 而非 logs

**路线 1（推荐）：复用 `pkg/perf_metrics` 的采样，把数据同时写进你的独立 MySQL。**

唯一需要改的原文件是 3 个采样调用点之一——但更好的做法是**在 `pkg/perf_metrics/metrics.go:57` 的 `Record(sample Sample)` 里加一行 hook**：

```go
func Record(sample Sample) {
    ...
    actual.(*atomicBucket).add(sample)
    recordRedis(key, sample)
    yourpkg.OnSample(sample)   // ← 唯一改动，1 行
}
```

一处改动即可拿到全部 (model, group, success, latency, ttft, tokens) 采样流，随后在你的独立库里自建 `(group, model, bucket, request_count, success_count, error_code)` 表，维度想加多少加多少。

**路线 2：完全不改原文件，自己在 relay 层做采样。** 需要 `controller/relay.go` 的一个挂载点，改动量与 A 合并（同一个 defer + 成功路径），但成功路径没有现成的统一 defer，需要改 `service/text_quota.go` / `service/quota.go` 三处，成本更高。**不推荐。**

**必须补齐的缺口（写进实施方案）：**
1. 若坚持用 `logs` 表，**必须** 设 `ERROR_LOG_ENABLED=true`，否则分母为 0。
2. `RecordErrorLog` 的 `Group` 传的是 `c.GetString("group")`（user.Group），而 consume log 传 `relayInfo.UsingGroup`。要统一，最小改法是把 `controller/relay.go:375` 的 `userGroup := c.GetString("group")` 换成 `common.GetContextKeyString(c, constant.ContextKeyUsingGroup)`——**这是唯一必须改的原文件 1 行**。若走路线 1（perf_metrics）则完全不需要，因为 `RecordRelaySample` 用的就是 `info.UsingGroup`。
3. 决定「可用率」口径：端到端（现有 perf_metrics 语义）还是每次 attempt。要 attempt 级就必须在 `processChannelError`（`controller/relay.go:360`）里加采样——那里有 `channelError.ChannelId`，是拿渠道维度的唯一位置。
4. 失败样本 group 为空时会被兜底成 `"default"`（`pkg/perf_metrics/metrics.go:62`），你的新表要单独处理空 group，别污染 default。

### C. 独立数据库的接入模式 —— 照抄 `LOG_DB`

`model/main.go:212-235` 的 `InitLogDB()` 就是现成模板：一个包级 `*gorm.DB` 变量、一个 `chooseDB` 风格的连接函数、独立的 `AutoMigrate`、关闭时判断句柄不同才 Close（:702）。
你新增的包只需 `var EXT_DB *gorm.DB` + `InitExtDB(cfg YourYamlConfig) error`，然后在 `main.go` 里加 1 行 `yourpkg.InitExtDB(...)`。
**改动原文件：`main.go` 1 行。**

### D. 需要改动的原有文件 —— 最小集合汇总

| 文件 | 改动 | 行数 | 必要性 |
|---|---|---|---|
| `main.go` | 初始化独立 DB + 加载 YAML 配置 | 1~2 行 | 必须 |
| `controller/relay.go` | defer 块中挂违规扣费 hook（:180 后） | 1 行 | 必须 |
| `pkg/perf_metrics/metrics.go` | `Record()` 末尾挂采样 hook（:76 后） | 1 行 | 走路线 1 时必须 |
| `controller/relay.go` | `:375` userGroup 改用 `ContextKeyUsingGroup` | 1 行 | 仅当基于 `logs` 表统计 |
| `router/api-router.go` | 注册你的新 API 路由组 | 1~3 行 | 必须（前端要读数据） |

合计 **4 个原文件、5~8 行**。所有实质逻辑放新文件，上游合并冲突面极小。

**其余全部新建**：违规规则/明细的 model 层、可用率聚合的 model 层、service 层、controller 层、前端页面。

**AGENTS.md 硬约束提醒**：新增的任何 quota 计算必须用 `common.QuotaFromFloat` / `QuotaRound` / `QuotaFromDecimal`（或 `*Checked` 变体 + `attachQuotaSaturation`），禁止裸 `int()` 转换；预扣与结算两端都要保证饱和值不会静默回绕。
