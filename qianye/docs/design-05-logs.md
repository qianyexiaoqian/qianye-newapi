# 需求4:使用日志新增两列

# 需求 4 — 使用日志新增「推理强度」「缓存百分比」两列 · 设计

## 0. 结论摘要

| 项 | 结论 |
|---|---|
| 推荐路线 | **路线 A（改 `logs.other`）**，后端 **2 个文件 / 4 行**，全部"只增不改"单行 hook |
| 新建物理表 | **0 张**（路线 B 的表结构作为附录 A 保留，非本期实施） |
| 新增 HTTP 端点 | **0 个**（`controller/log.go` 无 DTO，`Log.Other` JSON 直出，新键自动下发） |
| 新增 Go 包 | `qianye/loghook`（叶子包，只 import `common`/`constant`/`relay/common`/`relaykit/dto`/`qianye/config`） |
| 前端改动 | 修改 4 个原文件 + 新建 1 个纯函数文件 + 7 个 locale JSON 追加 |
| 老日志处理 | 语义不可判别时渲染 `—`，**绝不显示可能错误的数值** |

---

## 1. 数据可得性结论表

### 1.1 推理强度

| 厂商 / 入口 | 参数 | 现在是否落库 | 落在哪 | 赋值代码位置 |
|---|---|---|---|---|
| OpenAI o系列 / GPT-5 (Chat) | `reasoning_effort` | ✅ 是 | `logs.other.reasoning_effort` | `relay/channel/openai/adaptor.go:355`（**仅 `isOModel \|\| isGPT5Model` 分支内**）→ `service/log_info_generate.go:84` |
| OpenAI Responses API | `reasoning.effort` | ✅ 是 | 同上 | `relay/channel/openai/adaptor.go:618` |
| DeepSeek V4（OpenAI 格式） | 模型名 `-max/-none` 后缀 | ✅ 是 | 同上 | `relay/channel/deepseek/adaptor.go:116` |
| DeepSeek V4（Claude 格式） | 同上 | ✅ 是 | 同上 | `relay/channel/deepseek/adaptor.go:147` |
| xAI Grok | 模型名 `-high/-low` 后缀 | ⚠️ 部分 | 同上 | `relay/channel/xai/adaptor.go:88`，**仅 `grok-3-mini` 前缀** |
| Gemini `thinkingLevel` | `thinkingConfig.thinkingLevel` | ⚠️ 部分 | 同上 | `relaykit/relayconvert/internal/shared/gemini/request.go:146`，**仅走 `reasoning.TrimEffortSuffix` 命中 level 分支** |
| **Claude 原生 `thinking`** | `thinking.budget_tokens` | ❌ **否** | — | `relaykit/dto/claude.go:227` / `:447-461` 有 DTO 与 `GetBudgetTokens()`，**全仓无任何 `SetReasoningEffort` 调用** |
| **Gemini 数值预算** | `thinkingConfig.thinkingBudget` | ❌ **否** | — | `relaykit/dto/gemini.go:163-167` `ThinkingBudget *int`，只有 `ThinkingLevel` 分支写日志 |
| **Qwen** | `enable_thinking` / `thinking_budget` | ❌ **否** | — | `relaykit/dto/openai_request.go:91-92`，`json.RawMessage` 透传，不进日志 |
| **通用 OpenAI 兼容渠道**（非 o系列/非 GPT-5） | `reasoning_effort` | ❌ **否** | — | `relaykit/dto/openai_request.go:38` 有字段，但 `adaptor.go:355` 的 `if` 不成立 |
| 错误日志 type=5 | — | ❌ 否 | — | `controller/relay.go:380-396` 构造的 `other` 完全不含 reasoning |

**覆盖率结论：约 4 类命中、5 类缺失。Claude / Gemini 数值预算 / Qwen 三大主流思考模型全部拿不到，纯前端方案会让这些行永远显示空白。** 这是本需求必须动后端的唯一硬理由。

脱敏检查：`model/log.go:116-132` `formatUserLogs` 只 delete `admin_info`/`audit_info`/`stream_status`，**`reasoning_effort` 与 `cache_tokens` 普通用户可见**，新键同理（只要不放进 `admin_info`）。

### 1.2 缓存 Token

| 数据项 | 来源字段 | 是否落库 | 落在哪 | 代码位置 |
|---|---|---|---|---|
| 缓存读取（分子） | `usage.PromptTokensDetails.CachedTokens` | ✅ | `other.cache_tokens` | `service/text_quota.go:261` → `log_info_generate.go:78` |
| 缓存写入合计 | 归一化后总量 | ✅ | `other.cache_write_tokens` | `text_quota.go:506-512` |
| Claude 缓存创建 | `cache_creation_input_tokens` | ✅ | `other.cache_creation_tokens` | `text_quota.go:496-499` / `log_info_generate.go:280` |
| Claude 5m/1h 分档 | `ClaudeCacheCreation5mTokens/1h` | ✅ | `other.cache_creation_tokens_5m` / `_1h` | `text_quota.go:500-505` |
| DeepSeek | `prompt_cache_hit_tokens` | ✅（归一化进 `CachedTokens`） | `other.cache_tokens` | `relaykit/dto/openai_response.go:226` |
| 归一化输入总量 | `billingUsage.InputTokens` | ⚠️ **条件写入** | `other.input_tokens_total` | `text_quota.go:513-519`，**仅当"最终格式非 Claude" 且 `UsageSource != ""` 且 `InputTokens > 0`** |
| 计费语义判别键 | — | ⚠️ **单向写入** | `other.usage_semantic = "anthropic"` | `text_quota.go:475`，**只在 Claude 语义时写，OpenAI 语义什么都不写** |

**分子 100% 可得；分母（输入总量）不可得——这正是 §2 的陷阱。**

前端类型缺口（已确认 `web/src/features/usage-logs/types.ts:115-244` 无声明）：`usage_semantic`、`cache_write_tokens`、`input_tokens_total` 三个键需补进 `LogOtherData`。

---

## 2. GAPS §四⑤ 语义陷阱与处理方案

### 2.1 陷阱的精确表述

`prompt_tokens` 是否已经包含 `cached_tokens`，取决于计费语义：

- **OpenAI / Gemini 语义**：`prompt_tokens` **包含** `cached_tokens`。证据 `service/text_quota.go:307-311`——只有在 `!IsClaudeUsageSemantic && !legacyClaudeDerived` 时才 `baseTokens.Sub(dCacheTokens)`，说明基数里本来就含缓存。
- **Anthropic 语义**：`prompt_tokens` = Claude `input_tokens`，**不含** cache read / cache creation。
- **OpenRouter + Claude 计费**（`text_quota.go:271-280`）：显式 `PromptTokens -= CacheTokens`，同属"不含缓存"，且 `IsClaudeUsageSemantic == true`，判别一致。

判别键 `other.usage_semantic` 由 `text_quota.go:475` 写入，**且只在 `IsClaudeUsageSemantic == true` 时写**。因此：

```
usage_semantic 缺失  ⟺  { 新的 OpenAI 语义日志 }  ∪  { 该键上线前的全部历史日志（含 Claude） }
```

**"缺失"是一个二义状态，无法用现有数据区分。** 对一条历史 Claude 日志误用 OpenAI 公式，会得到偏高甚至 >100% 的缓存率——比不显示更糟，因为用户会当真。

### 2.2 处理方案：正向标记 + 分级降级

**核心手段：写入一个由本扩展产生的、永远为正向的权威分母 `qy_input_total`，配合版本标记 `qy_ver`。**

后端 HOOK B 在 `service/text_quota.go` 中拿到确定无疑的 `summary.IsClaudeUsageSemantic` 后，**把分母算好写进 `other`**，前端不再做任何语义推断。

前端决策树（严格按序，命中即停）：

| 序 | 条件 | 分母 | 展示 | 置信 |
|---|---|---|---|---|
| 1 | `qy_input_total > 0` | `qy_input_total` | 精确百分比 | **权威**（本扩展上线后的全部日志） |
| 2 | `cache_read === 0 && cache_write === 0` | — | `0%` | **权威**（分子为 0，任何分母都得 0，唯一无歧义的历史情形） |
| 3 | `usage_semantic === 'anthropic'` | `prompt_tokens + cache_read + cache_write` | 精确百分比 | 高（正向键，绝不误报） |
| 4 | `claude === true` | 同上 | 精确百分比 | 中（`claude` 表示最终上游请求格式为 Claude Messages，非严格等价于计费语义，但足够） |
| 5 | `input_tokens_total > 0` | `input_tokens_total` | 精确百分比 | 中（`text_quota.go:513-519` 已保证只在可信时写） |
| 6 | 其余（有缓存但语义不明） | — | **`—`** + tooltip `qy_log_metric_unknown_tip` | — |

序 6 的文案（中文）：**"该日志早于统计能力上线，缺少计费语义标记，无法准确计算缓存占比"**。

**明确拒绝的两个替代方案：**
- ❌ 不做"OpenAI 公式 + 结果 >100% 就回退 Claude 公式"的启发式。它只能捕获 `cache_read > prompt_tokens` 的极端情形；当 Claude 的 `input_tokens` 恰好大于 `cache_read` 时（长 system prompt + 小缓存），公式错但结果落在 0-100% 区间，静默出错，无法察觉。
- ❌ 不做历史日志回填。`logs` 表可能是 ClickHouse（`model/main.go:400-403` 不走 AutoMigrate），`other` 是 JSON 文本列，全量 UPDATE 代价高且 ClickHouse 无高效行更新。用 `—` 表达"不可知"是诚实且零风险的。

**副产品建议**：HOOK B 顺便对非 Claude 也写 `qy_semantic: "openai"`，让判别键在两个方向上都是正向的，方便后续排障与统计。

---

## 3. 两种实现路线的抉择

### 3.1 路线 B（旁路中间件 + 新库 + 前端 merge）的否决理由

用户没要求筛选，所以 GAPS §一.8 的"跨库无法筛选/排序"本身**不是**决定性理由。真正致命的是下面这条：

> **旁路中间件（`c.Next()` 之后）拿不到 `summary.IsClaudeUsageSemantic`。**

`IsClaudeUsageSemantic` 是 `service/text_quota.go:247` 在 relay 内部由 `usageSemanticFromUsage(relayInfo, usage)` 算出的，依赖上游返回的 `usage.UsageSemantic` 和 `relayInfo.GetFinalRequestRelayFormat()`。中间件在请求外层，`usage` 早已被消费，只能自己重新推断——**结果就是它必须重新踩一遍 §2 的语义陷阱，而且比路线 A 更容易踩错**。为规避陷阱而选的方案反而无法规避陷阱，逻辑上自我否定。

其余劣势（按重要性）：

| 劣势 | 说明 |
|---|---|
| 数据完整性 | 同上，分母必须重算，正确性无法保证 |
| 时序窗口 | 中间件晚于 `RecordConsumeLog`（`text_quota.go:526` 同步调用），存在"日志已可见但扩展数据未落"的窗口，用户会看到 `—` 刷新后变数值 |
| 不可筛选排序 | 分页/排序在 SQL 层，跨库 merge 只对当前页生效；若将来要筛选必须推翻重来 |
| 工程量 | 1 中间件 + 1 张表 + AutoMigrate + 1 端点 + 前端二次请求 + 保留期清理任务 + 分布式租约锁 ≈ 路线 A 的 8 倍 |
| 新库故障面 | 挂在 `/v1` 全量流量上，即使 fail-open 也增加热路径风险 |
| 原项目改动 | 仍需改 `router/relay-router.go`（**GAPS 标为中风险，随新端点增加而变动**）+ 路由注册，**并不比路线 A 少** |

### 3.2 路线 A 的成本核算（在 D3 预算下）

后端 **2 个文件、4 行**，占 D3 预算（10 文件 / 40 行）的 **20% 文件、10% 行数**。两个文件的上游改动频率：`service/log_info_generate.go` 中低频（工具函数集合，改动多为追加新 `appendXxx`）、`service/text_quota.go` 中频（计费逻辑，但插入点在 `attachQuotaSaturation` 与 `RecordConsumeLog` 之间的稳定缝隙）。均为纯追加单行，冲突可 3 秒内人工消解。

### 3.3 推荐：**路线 A**

理由排序：① 是唯一能拿到权威计费语义的位置；② 覆盖 Claude/Gemini/Qwen 的推理强度缺口（纯前端做不到）；③ 数据进 `logs.other` 意味着未来若要加筛选，只需改 `model/log.go` 的 WHERE 条件，路径畅通；④ 成本比路线 B 低一个数量级。

**明确不做的事**：不给 `model.Log` 结构体加数据库列。`logs` 表可能是 ClickHouse，`migrateLOGDB()`（`model/main.go:399-403`）在 ClickHouse 分支不走 AutoMigrate，加列需手写 `ALTER TABLE` 且改结构体必与上游冲突。

---

## 4. 数据结构

### 4.1 物理表：本模块新建 **0 张**

### 4.2 逻辑契约：`logs.other` 新增 JSON 键

`Log.Other` 是 `string`（`model/log.go:80`，无 gorm type，MySQL 下默认 `longtext`），内容为 `common.MapToJsonStr(map[string]interface{})`。新增键如下。

| 键 | JSON 类型 | 写入条件 | 为什么存在 |
|---|---|---|---|
| `qy_ver` | `int` | 扩展启用时**无条件**写入（值 `1`） | 版本水位线。前端据此区分"本扩展上线后的日志"与历史日志，是 §2 降级决策树的锚点。比全局 epoch 时间戳更可靠（逐行、不受重启/回滚影响） |
| `qy_input_total` | `int` | HOOK B，`> 0` 时写 | **权威分母**。由后端在持有 `IsClaudeUsageSemantic` 时算好，彻底消除前端语义推断 |
| `qy_cache_read` | `int` | HOOK B，`> 0` 时写 | 分子快照。与 `cache_tokens` 同值，独立存放是为了让 `qy_*` 自成闭包——上游若改 `cache_tokens` 语义不会静默污染本模块 |
| `qy_cache_write` | `int` | HOOK B，`> 0` 时写 | 缓存写入合计（5m+1h 或 `cache_creation_tokens`）。参与分母计算，也用于详情弹窗展示 |
| `qy_semantic` | `string` | HOOK B，无条件写（`"anthropic"` / `"openai"`） | 正向双向判别键，补上游 `usage_semantic` 只写单边的缺口。用于排障与后续统计 |
| `qy_reasoning` | `object` | HOOK A，探测到任一信号时写 | 归一化推理强度，见下表 |

`qy_reasoning` 子结构：

| 子键 | 类型 | 说明 |
|---|---|---|
| `level` | `string` | 归一化档位，枚举：`none` / `minimal` / `low` / `medium` / `high` / `max` / `auto` |
| `raw` | `string` | 原始值（如 `"xhigh"`、`"budget:24576"`），tooltip 展示，避免归一化丢信息 |
| `budget` | `int` | 数值型思考预算（tokens）。无数值口径时为 `0`；Gemini 动态预算 `-1` 保留原值 |
| `src` | `string` | 来源：`relay_info` / `claude_thinking` / `gemini_budget` / `gemini_level` / `oai_reasoning` / `oai_effort` / `qwen` |

**体积评估**：`qy_reasoning` 约 70 字节，其余 5 键约 90 字节，单条日志 `other` 增长 **< 200 字节**。当前 `other` 典型体积 400-900 字节，增幅约 20-40%。对 MySQL `longtext` 无影响；ClickHouse 下 `other` 列压缩率高，可忽略。

**脱敏定位**：新键**全部平铺在 `other` 顶层，不放进 `admin_info`**，与现有 `reasoning_effort` / `cache_tokens` 的可见性保持一致（`model/log.go:116-132` 不会 delete 它们），普通用户在自己的日志里可见。所有键均为整数或枚举字符串，**不含任何请求内容、PII 或渠道信息**。

### 4.3 YAML 配置（`qianye.yaml` 片段）

```yaml
log_metrics:
  enabled: true                 # 总开关；false 或配置文件缺失 → 两个 hook 直接 return
  reasoning_probe:
    enabled: true               # 是否做 body 兜底探测（Claude/Gemini/Qwen 覆盖）
    max_body_kb: 256            # body 超过此大小直接跳过探测，防 128MB 多模态请求拖慢热路径
  budget_levels:                # 数值 budget → 档位阈值（tokens），左开右闭
    minimal: 1024
    low: 4096
    medium: 16384
    high: 32768                 # > high 即 max
```

**禁止**注册进 `setting/config.GlobalConfig`（会持久化到主库 `options` 表，违背独立库约束）。

---

## 5. API 清单

### 5.1 本期新增端点：**0 个**

`controller/log.go:13` `GetAllLogs` / `:35` `GetUserLogs` 直接把 `[]*model.Log` 塞进 `common.PageInfo.Items`，**无 DTO 层**。`Log.Other` 是 `string` 直出，新增 JSON 键随现有响应自动下发，前端零额外请求。

复用的现有端点（仅列出契约变化）：

| Method | Path | 权限 | 变化 |
|---|---|---|---|
| `GET` | `/api/log/self` | `UserAuth` | 响应 `data.items[].other` 内新增 §4.2 六个键（历史日志无） |
| `GET` | `/api/log/` | `AdminAuth` | 同上 |
| `GET` | `/api/log/token` | `TokenAuthReadOnly` | 同上 |

响应结构不变：`{ success, message, data: { page, page_size, total, items: Log[] } }`。

### 5.2 建议新增（可选，非必须）

**① 上线自检端点**

```
GET /api/qy/log-metrics/health
权限：AdminAuth
```

Response:
```jsonc
{
  "success": true,
  "data": {
    "enabled": true,
    "probe_enabled": true,
    "probe_max_body_kb": 256,
    "stats_1h": {
      "total":        128340,   // 经过 HOOK A 的请求数
      "from_relay":    91200,   // relayInfo.ReasoningEffort 非空，零开销命中
      "probe_hit":     12045,   // body 探测成功解析出推理参数
      "probe_skip":    24800,   // 跳过（body 未缓存 / 超过 max_body_kb / 无思考参数）
      "probe_error":     295    // 解析失败（已 recover，不影响日志）
    },
    "budget_levels": { "minimal": 1024, "low": 4096, "medium": 16384, "high": 32768 }
  }
}
```

**存在理由**：hook 是静默 fail-open 的，出错不会有任何用户可见症状。没有这个端点，"两列一直是空的"到底是"没人用思考模型"还是"hook 根本没生效"无法区分。计数器为进程内 `atomic.Int64` + 1 小时滑窗，不落库，多节点各报各的。

**② 阈值下发端点**

```
GET /api/qy/log-metrics/config
权限：UserAuth
```
返回 `budget_levels`，供前端与后端共用同一套阈值。**建议不做**——前端内置同一份常量即可，多一次请求换取的"配置一致性"不划算，除非产品明确要求阈值可运营配置。

---

## 6. 关键流程

### 流程 1 — 日志写入（relay 请求完成 → `logs` 落库）

```
1. relay 完成 → controller/relay.go 的 defer 调用 service.PostTextConsumeQuota
                （service/text_quota.go:397，同步，非事务）
2. calculateTextQuotaSummary 产出 summary（含 IsClaudeUsageSemantic / PromptTokens /
   CacheTokens / CacheCreationTokens{,5m,1h}）           [text_quota.go:408]
3. 分支构造 other：
   3a. IsClaudeUsageSemantic → GenerateClaudeOtherInfo   [text_quota.go:468]
   3b. 否则                  → GenerateTextOtherInfo     [text_quota.go:477]
   两者最终都进入 GenerateTextOtherInfo                   [log_info_generate.go:72]
       ├─ :83-85  写 other["reasoning_effort"]（若 relayInfo 有）
       └─ ★ HOOK A：qylog.AttachReasoning(ctx, relayInfo, other)   ← 见流程 2
4. 上游继续追加 cache_creation_* / cache_write_tokens /
   input_tokens_total / tiered / quota_saturation        [text_quota.go:496-524]
5. ★ HOOK B：qylog.AttachCacheBasis(other, summary.PromptTokens, summary.CacheTokens,
              cacheWriteTokens, summary.IsClaudeUsageSemantic)     ← 见流程 3
6. model.RecordConsumeLog(...) 写 LOG_DB                 [text_quota.go:526]
```

**事务边界**：步骤 1-5 全部在**事务之外**、同一 goroutine、同步执行；`RecordConsumeLog` 内部自管其单条 INSERT。两个 hook **不参与任何事务，不做任何 IO**。

**加锁点**：无。`other` map 在步骤 6 之前只被当前 goroutine 持有；步骤 6 之后的 `gopool.Go`（`text_quota.go:540`）不触碰 `other`，无数据竞争。

**幂等键**：不适用（纯内存 map 写入，无持久化副作用）。

**失败回滚**：无需回滚。两个 hook 均 `defer recover()`，任何 panic 被吞掉并计入 `probe_error`，`other` 保留步骤 4 的状态，日志照常落库，只是少两个键 → 前端渲染 `—`。**hook 永远不得让计费日志写入失败。**

### 流程 2 — HOOK A：推理强度归一化

```
1. cfg := qyconfig.LogMetrics(); if !cfg.Enabled → return              (fail-open)
2. defer func(){ recover(); }()
3. if relayInfo.ReasoningEffort != "" :
      other["qy_reasoning"] = normalize(raw=relayInfo.ReasoningEffort, src="relay_info")
      return                                                    ← 零开销主路径（约 70% 流量）
4. if !cfg.ReasoningProbe.Enabled → return
5. body 探测（严格护栏，任一不满足即 return）：
   5.1 v, ok := ctx.Get(common.KeyBodyStorage); if !ok → return
       ★ 绝不调用 common.GetBodyStorage / UnmarshalBodyReusable —— 它们在无缓存时会去读
         c.Request.Body，此时 body 已被 relay 消费完毕，会返回空值或产生非预期副作用。
         只接受"已经被缓存过"的 body。
   5.2 bs := v.(common.BodyStorage); if bs.Size() > cfg.MaxBodyBytes → return  (probe_skip)
   5.3 bs.Seek(0, io.SeekStart)
   5.4 用窄结构体流式 decode（只声明需要的 6 个字段，其余全部忽略）
   5.5 ★ decode 后必须再次 bs.Seek(0, io.SeekStart)
       （照抄 common.UnmarshalBodyReusable 的行为，防止破坏后续潜在读取者）
6. 按优先级取第一个命中的信号：
   ① thinking.budget_tokens            (Claude)      → src=claude_thinking, budget=N
   ② thinkingConfig.thinkingBudget     (Gemini)      → src=gemini_budget,   budget=N
   ③ thinkingConfig.thinkingLevel      (Gemini)      → src=gemini_level
   ④ reasoning.effort                  (OpenRouter)  → src=oai_reasoning
   ⑤ reasoning_effort                  (通用 OAI)    → src=oai_effort
   ⑥ thinking_budget / enable_thinking (Qwen)        → src=qwen
7. 全部未命中 → return（probe_skip，不写键）
8. other["qy_reasoning"] = normalize(...)                              (probe_hit)
```

**性能预算**：步骤 3 命中时开销 = 一次字符串比较 + 一次 map 写。步骤 5 触发时，窄结构体流式解码 256KB JSON 上限约 0.3-0.8ms，且仅发生在 `ReasoningEffort` 为空的请求上。**`max_body_kb` 是最关键的护栏——多模态请求 body 可达 128MB（`constant.MaxRequestBodyMB`），无护栏会直接把热路径打爆。**

### 流程 3 — HOOK B：缓存分母固化

```
输入：other, promptTokens int, cacheRead int, cacheWrite int, isClaudeSemantic bool

1. cfg 未启用 → return
2. defer recover()
3. other["qy_ver"] = 1
4. other["qy_semantic"] = isClaudeSemantic ? "anthropic" : "openai"
5. 用 int64 累加防溢出：
     var total int64
     if isClaudeSemantic {
         total = int64(promptTokens) + int64(cacheRead) + int64(cacheWrite)
     } else {
         total = int64(promptTokens)          // 已含 cached_tokens
     }
6. 边界钳制：
     if total < 0            → total = 0
     if total > MaxInt32     → total = MaxInt32      // 防写入畸形大值
     if int64(cacheRead) > total { total = int64(cacheRead) }
         ★ 上游返回畸形 usage 时保证 pct ≤ 100%，并写 other["qy_cache_anomaly"] = true
7. if total > 0    → other["qy_input_total"] = int(total)
   if cacheRead>0  → other["qy_cache_read"]  = cacheRead
   if cacheWrite>0 → other["qy_cache_write"] = cacheWrite
```

**为什么不在后端直接算好百分比**：存分子分母而非结果，让前端可以按需展示"12.3%"或"1,024 / 8,320"两种形态，也让未来的聚合统计（平均命中率）能正确加权求和。存结果会丢信息。

### 流程 4 — 前端渲染

```
1. 表格行渲染 → accessorFn 调用 memo 化的 getLogOther(row)   (WeakMap 缓存，见 §8.2)
2. if !isDisplayableLogType(log.type) → return null           (与相邻列行为一致)
3. 缓存率：按 §2.2 决策树取分母 → clamp → 渲染 pct / "0%" / "—"
4. 推理强度：other.qy_reasoning?.level
             ‖ normalizeEffort(other.reasoning_effort)   (历史日志回退)
             ‖ null → 渲染 "—"
5. 空态统一：<span className="text-muted-foreground/50">—</span>
             （与 usage-logs-mobile-card.tsx:236 现有空态写法一致）
```

---

## 7. 原项目改动清单

### 7.1 后端（计入 D3 预算：**2 文件 / 4 行**）

| # | 文件:行号 | 插入的确切代码行 | 位置说明 | 冲突风险 |
|---|---|---|---|---|
| 1 | `service/log_info_generate.go` **第 11 行后** | `	qylog "github.com/QuantumNous/new-api/qianye/loghook"` | import 块内。`pkg/billingexpr`(:11) < `qianye/loghook` < `relay/common`(:12)，gofmt 排序位置精确 | **低** |
| 2 | `service/log_info_generate.go` **第 85 行后** | `	qylog.AttachReasoning(ctx, relayInfo, other)` | 紧接 `if relayInfo.ReasoningEffort != "" { ... }` 闭合花括号之后、`if relayInfo.IsModelMapped {`(:86) 之前 | **低** |
| 3 | `service/text_quota.go` **第 15 行后** | `	qylog "github.com/QuantumNous/new-api/qianye/loghook"` | import 块内。`pkg/perf_metrics`(:15) < `qianye/loghook` < `relay/common`(:16) | **低** |
| 4 | `service/text_quota.go` **第 524 行后** | `	qylog.AttachCacheBasis(other, summary.PromptTokens, summary.CacheTokens, cacheWriteTokens, summary.IsClaudeUsageSemantic)` | 紧接 `attachQuotaSaturation(ctx, relayInfo, other)`(:524) 之后、`model.RecordConsumeLog(`(:526) 之前 | **中** |

**#4 风险说明**：`service/text_quota.go` 是计费核心，上游改动中频，但插入点位于两个稳定语句之间的空行处（:525），且 `cacheWriteTokens` 局部变量在 :506 定义、`summary` 在 :408 定义，作用域稳定。若上游重构导致 `cacheWriteTokens` 改名，编译期立刻报错，不会静默出错。

**Import cycle 安全性证明**：`qianye/loghook` 只 import `common`、`constant`、`relay/common`、`relaykit/dto`、`qianye/config`。前四者已被 `service` 包 import（`text_quota.go:10,11,16,18`），由 Go 的无环约束可知它们**不可能** import `service`。`qianye/config` 是纯叶子包（stdlib + `gopkg.in/yaml.v3`）。因此 `service → qianye/loghook` 不引入任何环。

**硬约束**：`qianye/loghook` **禁止 import `service`、`model`、`qianye`(root)、`qianye/db`、`qianye/service`**。配置由 `qianye/bootstrap.go` 通过 `qylog.SetConfig(cfg)` 单向注入，或由 loghook 直接读 `qianye/config` 的只读快照。

### 7.2 新建后端文件（不计入预算，纯新增）

| 文件 | 内容 |
|---|---|
| `qianye/loghook/loghook.go` | `AttachReasoning` / `AttachCacheBasis` / `SetConfig` / 内部计数器 |
| `qianye/loghook/normalize.go` | `normalizeEffort(raw string, budget int) (level, raw string)`、budget 分档 |
| `qianye/loghook/probe.go` | 窄结构体 + body 探测 |
| `qianye/loghook/loghook_test.go` | 归一化表驱动测试 + 分母边界测试 |
| `qianye/controller/log_metrics.go` | （可选）`/api/qy/log-metrics/health` handler |

### 7.3 前端（不计入 D3 后端预算，但须列出）

| 文件 | 新建/修改 | 改动内容 | 冲突风险 |
|---|---|---|---|
| `web/src/features/usage-logs/lib/qy-log-metrics.ts` | **新建** | 全部计算逻辑与映射表，纯函数 | 无 |
| `web/src/features/usage-logs/components/columns/common-logs-columns.tsx` | 修改 | 在 `columns.push(` 的 `prompt_tokens`(:645-691) 之后、`quota`(:693) 之前插入 2 个 `ColumnDef`；import 追加 1 行 | **中** |
| `web/src/features/usage-logs/types.ts` | 修改 | `LogOtherData` 追加 8 个可选键（3 个上游缺声明 + 5 个 `qy_*`） | **低**（纯追加） |
| `web/src/features/usage-logs/components/usage-logs-mobile-card.tsx` | 修改 | `MobileTokensField`(:190-240) 内追加百分比与 badge | **中** |
| `web/src/features/usage-logs/components/dialogs/details-dialog.tsx` | 修改（建议） | `TokenBreakdown`(:406) 加 1 行；reasoning 区块(:1016-1032) 改用统一归一化 | **中** |
| `web/src/i18n/locales/{en,zh,zh-TW,ja,fr,ru,vi}.json` ×7 | 修改 | 追加 12 个 `qy_` 扁平键 | **高频但纯追加，零语义冲突** |

**`data/schema.ts` 不需要改动**——两列都不是 `UsageLog` 顶层字段，用 `id` + `accessorFn` 定义即可，`usageLogSchema` 保持原样。

---

## 8. 推理强度归一化展示

### 8.1 归一化映射表（前后端共用同一份，后端为准）

**枚举口径 → 档位**

| 上游原始值 | 归一化 `level` | 出现于 |
|---|---|---|
| `none` | `none` | OpenAI GPT-5、DeepSeek V4 `-none` |
| `minimal` | `minimal` | OpenAI |
| `low` | `low` | OpenAI / xAI / Gemini |
| `medium` | `medium` | OpenAI / Gemini |
| `high` | `high` | OpenAI / xAI / Gemini |
| `xhigh` / `max` | `max` | OpenAI `-xhigh`、DeepSeek `-max` |
| 未知非空字符串 | `medium`（保守居中） + `raw` 保留原文 | 兜底 |

**数值口径（tokens）→ 档位**（阈值来自 YAML `budget_levels`，左开右闭）

| budget 区间 | `level` |
|---|---|
| `-1`（Gemini 动态） | `auto` |
| `0` | `none` |
| `(0, 1024]` | `minimal` |
| `(1024, 4096]` | `low` |
| `(4096, 16384]` | `medium` |
| `(16384, 32768]` | `high` |
| `> 32768` | `max` |

**布尔口径**：`enable_thinking: false` → `none`；`enable_thinking: true` 且无 budget → `medium`（各厂商默认值多落在此区间）。

**阈值设定依据**：Claude 官方最低 `budget_tokens` 为 1024（低于此值 API 直接拒绝），故 `minimal` 上界取 1024；Gemini 2.5 Flash 默认动态预算上限 24576、Pro 上限 32768，故 `high` 上界取 32768。跨厂商统一分档必然是近似的——**这正是必须同时保留 `raw` 和 `budget` 原值的原因**，tooltip 展示精确数字，档位只用于快速扫视与配色。

### 8.2 前端渲染

组件：复用 `@/components/status-badge` 的 `StatusBadge`（`size='sm'` / `copyable={false}`），外层 `Tooltip` 展示 `raw` + `budget`。

配色（`StatusVariant` 均取自 `web/src/components/status-badge.tsx:41-92` 已有色板，无新增 CSS）：

| `level` | `variant` | 语义 |
|---|---|---|
| `none` | `grey` | 未思考 |
| `minimal` | `light-blue` | 极低 |
| `low` | `green` | 低 |
| `medium` | `yellow` | 中（与 `details-dialog.tsx:610` 现有映射一致） |
| `high` | `orange` | 高（同 `:608`） |
| `max` | `red` | 最高，成本警示 |
| `auto` | `violet` | 动态预算，成本不可预估 |
| 无数据 | — | 渲染 `—`，`text-muted-foreground/50` |

**必须与 `details-dialog.tsx:607-612` 的现有映射统一**：把该处硬编码替换为调用 `qy-log-metrics.ts` 导出的 `reasoningVariant(level)`，避免表格与弹窗两处配色漂移。

---

## 9. 缓存百分比：公式与边界

### 9.1 公式

```
cacheRead  = other.qy_cache_read  ?? other.cache_tokens ?? 0
cacheWrite = other.qy_cache_write
             ?? (other.cache_creation_tokens_5m ?? 0) + (other.cache_creation_tokens_1h ?? 0)
             || other.cache_creation_tokens ?? 0

denominator = 按 §2.2 决策树取得（qy_input_total 优先）

pct = denominator > 0 ? (cacheRead / denominator) * 100 : null
```

**注意 `cacheWrite` 的 fallback 顺序**：必须先看 5m/1h 之和，只有两者都为 0 时才回退到 `cache_creation_tokens`。这与 `common-logs-columns.tsx:660-666` 和 `usage-logs-mobile-card.tsx:206-212` 的 `hasSplitCache` 逻辑完全一致，**直接复用其判断，不要另写一套**（Claude 同时写入 `cache_creation_tokens` 和 5m/1h 分档，简单相加会重复计数）。

### 9.2 边界处理

| 情形 | 处理 | 展示 |
|---|---|---|
| `denominator === 0` 或 `null` | 不计算 | `—` |
| `cacheRead === 0` 且分母可知 | `pct = 0` | `0%`（灰色，弱化） |
| `cacheRead === 0` 且分母不可知 | **特判**：分子为 0 时任何分母都得 0 | `0%`（权威，见 §2.2 序 2） |
| `pct < 0` | clamp 到 `0`，`console.warn` 一次 | `0%` |
| `pct > 100` | clamp 到 `100`，附 ⚠ 图标 + tooltip `qy_log_cache_over_tip` | `100% ⚠` |
| `NaN` / `Infinity` | 视为不可计算 | `—` |
| `other` 解析失败（`parseLogOther` 返回 `null`） | 不计算 | `—` |
| 非消费类日志（`type !== 2`，即充值/管理/错误/退款/登录） | `isDisplayableLogType(log.type)` 为 false | `null`（整格空白，与相邻列一致） |
| 语义不明的历史日志 | 见 §2.2 序 6 | `—` + tooltip |

**精度**：显示保留 **1 位小数**（`12.3%`），`< 0.1%` 且 `> 0` 时显示 `<0.1%`（避免显示 `0.0%` 误导为完全无缓存）。计算全程用 JS `number`（IEEE754 双精度），分子分母均为 `< 2^31` 的整数，无精度风险。

**后端计算**：全程 `int64` 整数运算，无浮点、无金额。本模块**不涉及任何 quota / 金额换算**，故 `common.QuotaFromFloat` / `QuotaRound` / `QuotaFromDecimal` 与 decimal 库均不适用（AGENTS.md 的强制约束针对金额路径）。唯一的数值风险是 int32 溢出，已在流程 3 步骤 6 用 `int64` 累加 + `MaxInt32` 钳制处理。

### 9.3 配色（缓存率热力）

| 区间 | 颜色 | 含义 |
|---|---|---|
| `>= 70%` | `text-success` | 缓存高效，成本显著降低 |
| `30% ~ 70%` | `text-warning` | 中等 |
| `> 0% ~ 30%` | `text-muted-foreground` | 低 |
| `0%` | `text-muted-foreground/60` | 未命中 |
| `—` | `text-muted-foreground/50` | 不可知 |

采用 `variant='text'` 的纯文本着色（`status-badge.tsx:69-91` 的 `textColorMap`），不用 pill——表格已有多个 badge，再加 pill 会视觉过载。字体 `font-mono tabular-nums`，与相邻 Tokens 列（`common-logs-columns.tsx:670`）对齐。

---

## 10. 并发与边界

### 10.1 后端

| 项 | 分析 |
|---|---|
| **热路径同步性** | 两个 hook 均在 `PostTextConsumeQuota` 的同步调用链上（`text_quota.go:526` 之前）。**禁止任何 DB 查询、Redis 访问、网络 IO、锁等待。** 违反此约束会把 relay P99 直接拖高 |
| **body 重读竞态** | `common.KeyBodyStorage`（`common/gin.go:21`）由 gin `Context` 持有，请求作用域内单 goroutine。但 `CleanupBodyStorage`（`common/gin.go:93`）可能已在 defer 中被调用并 `Set(KeyBodyStorage, nil)`——**必须做 `v != nil` 判空**，`c.Get` 返回 `(nil, true)` 而非 `(nil, false)` |
| **Seek 副作用** | 探测后未 `Seek(0)` 会让后续潜在读取者读到 EOF。虽然 relay 已完成，但 `controller/relay.go` 的 defer 链中可能还有读 body 的逻辑（如错误日志构造）。**无条件 Seek 回 0** |
| **panic 隔离** | 每个 hook 顶层 `defer recover()`。上游 usage 结构畸形、body 是非 JSON、类型断言失败——任一 panic 逃逸都会让计费日志丢失，这是不可接受的严重故障 |
| **map 并发写** | `other` 在 `RecordConsumeLog` 之前为单 goroutine 独占；`gopool.Go`（`text_quota.go:540`）之后不再触碰。无竞态 |
| **多节点** | 两个 hook 完全无状态（除进程内 atomic 计数器），无需分布式锁、无需 `IsMasterNode` gate |
| **配置热更** | `qyconfig.LogMetrics()` 返回不可变快照结构体的值拷贝；若支持热 reload，用 `atomic.Pointer[Config]` 交换，读侧 `Load()` 无锁 |
| **计数器溢出** | `atomic.Int64` 计数器 1 小时滑窗，QPS 10000 下 1 小时 = 3.6e7，距 `MaxInt64` 有 11 个数量级余量 |
| **降级** | 配置文件缺失 → `Enabled() == false` → 两 hook 首行 return，`other` 完全不变，行为与未安装扩展完全一致。**本模块不访问新库，因此不存在"新库连不上"的降级分支**——这是路线 A 相对路线 B 的又一优势 |

### 10.2 前端

| 项 | 分析 |
|---|---|
| **重复 JSON.parse** | `lib/format.ts:159` `parseLogOther` **无 memo**，每个 cell 独立调用。当前 `common-logs-columns.tsx` 已有 4 处调用（`is_stream`/`prompt_tokens`/`quota`/`content`），新增 2 列 → 每行 6 次。20 行/页 = 120 次 `JSON.parse`，单次约 500 字节，实测约 2-4ms/页。**建议**：在 `qy-log-metrics.ts` 内实现模块级 `WeakMap<UsageLog, LogOtherData \| null>` 缓存，新列走缓存版本，不改动 `format.ts`（零冲突）。若后续要优化全表，可把现有 4 处也切过来 |
| **View 菜单可见性（硬约束）** | `web/src/components/data-table/toolbar/view-options.tsx:44-49` 过滤条件是 `typeof column.accessorFn !== 'undefined' && column.getCanHide()`。**只有 `id` + `cell` 而无 `accessorFn` 的列不会出现在"Toggle columns"下拉里，用户永远无法隐藏它。** 因此两列都**必须**提供 `accessorFn`（返回 `number \| null` 与 `string`），这也顺带让排序在客户端可用（虽然本期 `manualPagination` 下不启用） |
| **列可见性默认值** | `use-data-table.ts:311-314` 的初始值是 `{ ...readColumnVisibility(key), ...initialColumnVisibility }`。localStorage 中的历史记录不含新列 id → 落入 tanstack 默认「可见」。**老用户升级后两列默认显示，符合预期**，无需迁移逻辑 |
| **列宽与横向溢出** | admin 视图当前列数已达 9 列。两个新列各 `size: 110`，共 +220px。`DataTablePage` 使用 `applyHeaderSize`（`usage-logs-table.tsx:184`），表格横向可滚动，不会破版。**但建议**：`meta.mobileHidden: true`，并在 admin 视图下考虑默认收起（见 §12 建议 4） |
| **移动端不自动继承** | `usage-logs-mobile-card.tsx:506` 对 `logCategory === 'common'` 走 `CommonLogsCard`(:311)，该组件用 `cells.get('model_name' \| 'quota' \| 'channel' \| 'user' \| 'token_name' \| 'content')` **手工枚举**，完全不读 `meta.mobileOrder/mobileHidden`。新列在移动端**必须手工挂载**，见 §11.3 |
| **空数组 / 空态** | 日志列表为空时走 `DataTablePage` 的 `emptyTitle`，与新列无关 |
| **i18n 未加载** | `t('qy_log_cache_hit_rate')` 在 key 缺失时 i18next 默认回显 key 本身（丑但不崩）。**7 个 locale 必须同批提交**，缺一个就会有语言显示原始 key |

---

## 11. 前端页面

### 11.1 新建：`web/src/features/usage-logs/lib/qy-log-metrics.ts`

全部逻辑集中于此纯函数模块，`common-logs-columns.tsx` 只做渲染。

```ts
export type ReasoningLevel =
  | 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'max' | 'auto'

export interface QyReasoning {
  level: ReasoningLevel
  raw: string
  budget: number
  src: string
}

/** WeakMap-memo 版 parseLogOther，避免新增两列带来的重复 JSON.parse */
export function getLogOtherCached(log: UsageLog): LogOtherData | null

/** null = 不可计算（应渲染 "—"）；数值单位为百分比 0-100 */
export function getCacheHitRate(
  log: UsageLog,
  other: LogOtherData | null
): { pct: number; anomaly: boolean } | null

/** null = 无推理数据（应渲染 "—"） */
export function getReasoning(other: LogOtherData | null): QyReasoning | null

/** level → StatusBadge variant，供表格与详情弹窗共用（消除 details-dialog:607-612 的硬编码） */
export function reasoningVariant(level: ReasoningLevel): StatusBadgeProps['variant']

/** 缓存率 → Tailwind 文本色 class */
export function cacheRateColorClass(pct: number): string

/** 历史日志的字符串 effort → 归一化（前后端同一份映射表） */
export function normalizeLegacyEffort(raw: string): QyReasoning | null
```

### 11.2 修改：`common-logs-columns.tsx`

在 `:600` 起的 `columns.push(...)` 参数列表中，`prompt_tokens` 列（`:645-691`）与 `quota` 列（`:693`）**之间**插入：

```tsx
{
  id: 'qy_reasoning',
  header: t('Reasoning Effort'),          // 复用上游已有 key（7 语言均已存在）
  accessorFn: (row: UsageLog) =>
    getReasoning(getLogOtherCached(row))?.level ?? '',
  cell: ({ row }) => { /* StatusBadge + Tooltip(raw/budget)，无数据渲染 "—" */ },
  meta: { label: t('Reasoning Effort'), mobileHidden: true },
  size: 110,
},
{
  id: 'qy_cache_rate',
  header: t('qy_log_cache_hit_rate'),
  accessorFn: (row: UsageLog) =>
    getCacheHitRate(row, getLogOtherCached(row))?.pct ?? null,
  cell: ({ row }) => { /* 着色文本 + 可选 ⚠ + Tooltip(分子/分母)，无数据渲染 "—" */ },
  meta: { label: t('qy_log_cache_hit_rate'), mobileHidden: true },
  size: 110,
},
```

**放在 `prompt_tokens` 之后**：两列都是"输入侧特征"，与 Tokens 列语义邻近；放在 `quota`（成本）之前，形成「用了多少 token → 缓存省了多少 → 推理花了多少 → 最终成本」的阅读动线。

`import` 追加 1 行：
```tsx
import { getCacheHitRate, getReasoning, getLogOtherCached, reasoningVariant, cacheRateColorClass } from '../../lib/qy-log-metrics'
```

### 11.3 修改：`usage-logs-mobile-card.tsx`（手工挂载，必做）

**不新增卡片格子**（`CommonLogsCard:331` 的 grid 已是 2 列 × 3 行 + 详情，再加格子会让卡片过高）。改动集中在 `MobileTokensField`（`:190-240`）：

1. 第一行 `{prompt} / {completion}` 右侧追加一个极小的推理档位圆点 + 字母（如 `● H`），`title` 属性给完整信息；
2. 第二行现有 `Cache↓ N ↑ M` 之后追加 `· 42.3%`，着色同 §9.3；
3. 无缓存无推理时保持现有 `—` 空态（`:236`），不引入新空行。

**理由**：移动端信息密度优先，不为两个辅助指标增加纵向空间；用户需要精确值时点开详情弹窗。

### 11.4 修改（建议）：`dialogs/details-dialog.tsx`

- `TokenBreakdown`（`:406-470`）在 `Cache Read`（`:429`）之后插入一行 `Cache Hit Rate`，展示 `42.3% (1,024 / 2,420)`；分母不可知时展示 `—` + 说明文案。
- 推理强度区块（`:1016-1032`）：把 `other.reasoning_effort` 换成 `getReasoning(other)`，展示归一化档位 badge + 一行 `qy_log_thinking_budget: 24,576 tokens`；`reasoningEffortVariant`（`:607-612`）的硬编码替换为 `reasoningVariant(level)`。

### 11.5 修改：`types.ts`

`LogOtherData` 追加（纯追加，位置任意，建议紧随 `reasoning_effort`（`:196`）之后）：

```ts
  // —— 上游已写入但此前未声明 ——
  usage_semantic?: string
  cache_write_tokens?: number
  input_tokens_total?: number
  // —— qianye 扩展（qianye/loghook 写入）——
  qy_ver?: number
  qy_semantic?: 'anthropic' | 'openai'
  qy_input_total?: number
  qy_cache_read?: number
  qy_cache_write?: number
  qy_cache_anomaly?: boolean
  qy_reasoning?: {
    level: 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'max' | 'auto'
    raw: string
    budget: number
    src: string
  }
```

---

## 12. i18n Key

**复用上游已有 key**（7 语言均已存在，零改动）：`Reasoning Effort`(:3644)、`Cache`(:676)、`Cache Read`(:689)、`Cache Write`(:693)、`Input Tokens`、`Output Tokens`。

**新增 12 个 `qy_` 前缀扁平键**（下划线，无点号，符合 i18next 默认 `keySeparator: '.'` 约束），7 个 locale 文件各追加一次：

| key | zh | en |
|---|---|---|
| `qy_log_cache_hit_rate` | 缓存命中率 | Cache Hit Rate |
| `qy_log_thinking_budget` | 思考预算 | Thinking Budget |
| `qy_log_reasoning_none` | 未启用 | Off |
| `qy_log_reasoning_minimal` | 极低 | Minimal |
| `qy_log_reasoning_low` | 低 | Low |
| `qy_log_reasoning_medium` | 中 | Medium |
| `qy_log_reasoning_high` | 高 | High |
| `qy_log_reasoning_max` | 最高 | Max |
| `qy_log_reasoning_auto` | 动态 | Dynamic |
| `qy_log_metric_unknown_tip` | 该日志早于统计能力上线，缺少计费语义标记，无法准确计算缓存占比 | This log predates cache metrics and lacks a billing-semantic marker, so the ratio cannot be computed accurately |
| `qy_log_cache_over_tip` | 上游返回的缓存 token 超过输入总量，已按 100% 显示 | Upstream reported more cached tokens than total input; clamped to 100% |
| `qy_log_cache_detail_tip` | 缓存读取 {{read}} / 输入总量 {{total}} | Cache read {{read}} / total input {{total}} |

**注意**：`qy_log_cache_detail_tip` 使用 i18next 插值 `{{}}`，与项目现有插值风格一致。

---

## 13. 我建议补充的

> 以下均为用户未提出、但工程上必要或高性价比的设计。

**建议 1 — 详情弹窗同步展示（强烈建议做）**
表格列受宽度限制只能给一个百分比。详情弹窗有空间展示分子/分母/写入量/思考预算原值，是用户排查"为什么这次贵"的主入口。成本：`details-dialog.tsx` 约 15 行。见 §11.4。

**建议 2 — CSV 导出：本期不做**
已 grep 确认项目 `web/src/components/data-table/` 与 `features/usage-logs/` **没有任何 CSV/导出能力**。为两列新增导出等于凭空开一个新功能，超出需求。若未来做导出，硬性要求：导出层必须调用 `qy-log-metrics.ts` 的同一组纯函数，**禁止重新实现公式**——两处实现必然漂移，而导出结果常被用于对账，错了后果更重。

**建议 3 — 上线自检端点（建议做）**
见 §5.2 ①。hook 静默 fail-open 是把双刃剑：出错时零症状。没有自检端点，"两列一直空白"无法区分是"没人用思考模型"还是"hook 没生效"。成本约 40 行。

**建议 4 — admin 视图默认收起推理强度列（建议）**
admin 视图当前已 9 列（含 channel/user），再加 2 列横向压力较大。建议在 `usage-logs-table.tsx` 传 `initialColumnVisibility: { qy_reasoning: !isAdmin }`——普通用户默认显示（他们的日志页只有 7 列），admin 默认隐藏但可从 View 菜单开启。缓存率列两侧都默认显示（它对成本分析价值更高）。**注意**：这需要给 `useDataTable` 传 `initialColumnVisibility`，且该值会被 localStorage 覆盖（`use-data-table.ts:311-314` 的展开顺序），即老用户的显式选择优先——行为正确。

**建议 5 — 统计卡片增加"平均缓存命中率"（建议排二期）**
`components/common-logs-stats.tsx` 已有统计区。加"本期平均缓存命中率"需要后端聚合 `SUM(qy_cache_read) / SUM(qy_input_total)`——但这两个值在 `other` JSON 文本里，MySQL 下要 `JSON_EXTRACT` 全表扫描，ClickHouse 下更麻烦。**必须先落地附录 A 的物化表**才有意义。本期不做，但设计上留出路径。

**建议 6 — 筛选/排序：明确不做，但保留路径**
需求只说"新增两列"。本期不实现筛选。若后续要做，路径是：`model/log.go:468/564` 的查询加 `WHERE JSON_EXTRACT(other, '$.qy_reasoning.level') = ?`——但这**无法走索引**且 ClickHouse 语法不同。真正可行的是附录 A 的物化表。**务必在需求文档里写死这一点**，避免上线后被追加要求筛选时才发现要重做。

**建议 7 — 探测器采样日志，禁止逐请求打日志**
`probe_error` 若逐次 `logger.LogWarn` 会在上游返回畸形数据时刷爆日志。采用：每 1000 次错误打一条汇总 warn，或仅在 `probe_error` 计数首次跨越 100/1000/10000 时打点。

**建议 8 — 归一化映射表的单元测试（必做）**
`qianye/loghook/loghook_test.go` 表驱动覆盖：7 个枚举值 + 6 个 budget 边界（0/1/1024/1025/32768/32769）+ `-1` 动态 + 空字符串 + 未知字符串 + int32 溢出 + `cacheRead > total` 异常钳制。前端 `qy-log-metrics.test.ts` 覆盖 §2.2 决策树 6 个分支 + §9.2 全部 9 种边界。这两组测试是本模块唯一的正确性保障（hook 出错静默，无运行时告警）。

**建议 9 — 文档化"为什么老日志显示 `—`"**
在日志页表头的 `meta.description`（`tanstack-table.d.ts:24` 已支持该字段）挂一句说明，用户 hover 列头即可看到。避免运营被反复追问"为什么有些行是横杠"。

---

## 附录 A — 路线 B / 未来筛选需求的物化表（本期不实施）

若后续确认需要**按推理强度筛选**或**按缓存率排序**，唯一可行方案是把指标物化到独立库的结构化列上。表结构如下（放 `qianye/model/log_metric.go`）：

```go
// QyLogMetric 是使用日志的结构化指标物化表，用于支持 SQL 层筛选与排序。
// 与主库/LOG_DB 的 logs 表通过 request_id 关联；跨库无法 JOIN，
// 因此启用此表时必须让它成为分页主表、反查 logs 明细。
type QyLogMetric struct {
	Id int64 `gorm:"primaryKey;autoIncrement" json:"id"`

	// RequestId 是与 model.Log.RequestId 的唯一关联键。
	// model/log.go:95 ensureLogRequestId 保证非空；唯一索引提供写入幂等。
	RequestId string `gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_lm_request" json:"request_id"`

	// UserId + CreatedAt 组成列表页主排序索引（与 logs 的 idx_created_at_id 对齐）。
	UserId    int   `gorm:"not null;index:idx_qy_lm_user_time,priority:1" json:"user_id"`
	CreatedAt int64 `gorm:"bigint;not null;index:idx_qy_lm_user_time,priority:2;index:idx_qy_lm_time" json:"created_at"`

	// ModelName 用于按模型下钻。191 是 utf8mb4 下 InnoDB 单列索引的安全上限（767B/4）。
	ModelName string `gorm:"type:varchar(191);not null;default:''" json:"model_name"`

	// —— 推理强度 ——
	// ReasoningLevel 是归一化档位，是筛选的目标列，故单独建索引。
	ReasoningLevel  string `gorm:"type:varchar(16);not null;default:'';index:idx_qy_lm_level" json:"reasoning_level"`
	// ReasoningRaw 保留厂商原值，归一化会丢信息，排障时必须能还原。
	ReasoningRaw    string `gorm:"type:varchar(32);not null;default:''" json:"reasoning_raw"`
	// ThinkingBudget 数值预算（tokens）；-1 表示动态预算，0 表示无数值口径。
	ThinkingBudget  int32  `gorm:"not null;default:0" json:"thinking_budget"`
	// ReasoningSource 记录数据来自哪条探测分支，用于评估各厂商覆盖率。
	ReasoningSource string `gorm:"type:varchar(24);not null;default:''" json:"reasoning_source"`

	// —— 缓存 ——
	// InputTotal 是已固化的权威分母（含语义修正），不可由前端重算。
	InputTotal       int32 `gorm:"not null;default:0" json:"input_total"`
	CacheReadTokens  int32 `gorm:"not null;default:0" json:"cache_read_tokens"`
	CacheWriteTokens int32 `gorm:"not null;default:0" json:"cache_write_tokens"`
	// CacheHitBp 是缓存命中率的万分比（0-10000）。
	// 用整数而非 DECIMAL/FLOAT：排序索引友好、无浮点误差、跨库比较结果稳定。
	// 万分之一精度足够（展示只到小数点后一位）。
	CacheHitBp int32 `gorm:"not null;default:0;index:idx_qy_lm_hit" json:"cache_hit_bp"`

	// UsageSemantic 冻结当时的计费语义，保证历史数据在上游语义演进后仍可解释。
	UsageSemantic string `gorm:"type:varchar(16);not null;default:''" json:"usage_semantic"`
}

func (QyLogMetric) TableName() string { return "qy_log_metric" }
```

**启用此表的额外代价（务必先评估）**：
1. 日志列表接口必须重写为"以 `qy_log_metric` 分页 → 用 `request_id` 批量反查 `logs`"，**这会推翻 `controller/log.go` 与 `model/log.go` 的全部现有分页/筛选逻辑**，远超 D3 预算；
2. 需要保留期清理任务 + 新库自建租约锁（GAPS §三.2(7)）；
3. 与 `logs` 表的数据一致性需要补偿对账；
4. 历史日志无法回填，筛选结果天然不完整。

**结论：除非产品明确要求筛选，否则不启用。**
