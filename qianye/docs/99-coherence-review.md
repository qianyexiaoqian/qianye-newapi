# 跨模块一致性审查裁定

---

## 1. 跨模块矛盾（逐条 + 裁定）

### 1.1 基础设施类（最严重，会导致编译失败或启动崩溃）

| # | 冲突点 | 各模块说法 | **裁定** |
|---|---|---|---|
| C1 | **租约表** | 00 `qy_task_leases`(name,holder,**fence**,lease_until,acquired_at)；01 `qy_task_leases`(task_key,owner,expires_at)；02 `qy_task_lease`(name,owner,expires_at,version)；06 `qy_task_lease`(name,holder,expire_at)；07 `qy_task_lease`(name,owner,expires_at) | **只保留 00 的 `qy_task_leases`**（复数 + fence + `UNIX_TIMESTAMP()` DB 时钟）。01/02/06/07 的定义全部删除，改为 `import qianye/service/lease`。任何模块不得再 `AutoMigrate` 租约表 |
| C2 | **两阶段主线被架空** | 00 强制 `qy_fund_orders` + 主库 `qy_fund_outbox` 为**唯一精确探针**；01 完全不用，自建 `qy_transfer_orders` 状态机 + `logs.request_id` 锚点；03 完全不用，自建 `claim_no` + `logs.content LIKE '%claim%'` 反查 | **强制走 00 的 twophase**。① `qy_fund_outbox` 是唯一探针；② 01 的 `qy_transfer_orders`、03 的 `qy_withdrawals` 降级为**业务明细表**，主状态机字段（status/attempts/next_probe_at/settled_at）迁到 `qy_fund_orders`，明细表以 `order_no` 外键关联；③ 03 的 `content LIKE` 反查**必须删除**——LOG_DB 若为 ClickHouse 这是全表扫，且无法区分"没做"和"日志没写" |
| C3 | **表前缀双重定义** | 00 `NamingStrategy{TablePrefix:"qy_"}` + 所有模块的 `TableName()` 都硬编码 `qy_` | 去掉 `NamingStrategy.TablePrefix`，**统一由 `TableName()` 硬编码**。config 的 `database.table_prefix` 字段删除（留着只会让人以为能改前缀，实际被 TableName 覆盖） |
| C4 | **hook 变量形态** | 00 `var QyOnX func(...)` + 调用点 `if QyOnX != nil { QyOnX(...) }`；02 `var QyOnX = func(...){}` + 调用点 `QyOnX(...)` | **用 02 的默认 no-op 形态**。调用点 1 行、无 nil panic 风险、diff 更小 |
| C5 | **叶子 hook 包重复** | 05 `qianye/loghook`（被 `service` import）；06 `qianye/hook`（被 `pkg/perf_metrics` import）；07 直接 `import qianye/service/violation` 进 `controller/relay.go` | **全部作废**。按 00 §0 的铁律：上游包内的 hook 一律用**同包变量注入**——`service/qy_export.go`、`pkg/perf_metrics/qy_export.go`、`controller/qy_export.go`（纯新增文件），调用点 0 import 改动。这直接省下 5 行 import（见 §2） |
| C6 | **`model/qy_export.go` 五份互不兼容的定义** | 00/01/02/03/06 各写一份，且 `QyLogDBIsMainDB`(00) vs `QyLogDBSharesMainDB`(01) 同功能双名 | 合并为**一份权威清单**（见 §3.6）。统一 `QyLogDBSharesMainDB`。删除 00 的 `QyDisableUser`（见 C7） |
| C7 | **封号实现两套，00 的那套有安全洞** | 00 `QyDisableUser` 只做 status+auth_version+`invalidateUserCache`+RecordLog；07 `disableUserForViolation` 额外做 `PublishUserAuthCache` + `InvalidateUserTokensCache` + `RevokeAllUserSessions` | **删除 00 的 `QyDisableUser`**。缺 `InvalidateUserTokensCache` 会让已缓存的 relay 令牌在 TTL 内继续可用 = 封号无效。以 07 的六步版本为唯一实现，放 `qianye/service/violation/ban.go` |
| C8 | **可用率采样挂载点** | 00 `pkg/perf_metrics/qy_export.go` 的 `QyOnSample(sample Sample)`（挂 `Record()`）；06 论证必须挂 `RecordRelaySample(info,...)`（`Sample` 只有裸 bool，拿不到 error） | **以 06 为准**。00 的 `QyOnSample` 签名改为 `QyOnRelaySample(info *relaycommon.RelayInfo, success bool, outputTokens int64)`，声明在 `pkg/perf_metrics/qy_export.go`（同包，省 import 行） |

### 1.2 命名与协议类

| # | 冲突点 | 各模块说法 | **裁定** |
|---|---|---|---|
| C9 | **单号格式与随机源** | 00 `TR20260730T091533-3f2a-9c1e4b7d20`（crypto/rand，**明令禁止 `common.GetRandomString`**）；01 `QYT{from}T{to}N{nano}{rand6}` 用 `common.GetRandomString`（内部 math/rand）；03 `QYW+yyyymmdd+12位 GetRandomString` | **统一 `twophase.NewOrderNo(kind)`**，kindCode = `TR/CM/WD/RV/VF`。**禁止 `common.GetRandomString`**（math/rand 可预测，资金单号必须 crypto）。01 的 `{from}T{to}` 把双方 user_id 编进单号，等于对外泄漏用户关系，也一并废除 |
| C10 | **客户端幂等键字段名，四个** | 00 header `X-Qy-Idempotency-Key`；01 body `request_id`；03 body `request_key`；08 body `client_request_id` | **统一 body 字段 `client_request_id`**（08 的前端契约已按此写）。后端映射为 `idem_key = "<userId>:<client_request_id>"` |
| C11 | **幂等键存储结构** | 00 `(idem_scope, idem_key)` 双列唯一；01 单列 `idempotency_key`；03 `(user_id, request_key)`；02 `(source_type, source_no)` | **统一 `(idem_scope, idem_key)` 双列唯一**（02 的 `source_type/source_no` 语义等价，改名对齐） |
| C12 | **状态机取值** | 00 int8 `{0 Pending,2 Success,3 Failed,4 Uncertain,5 Reversed}`；01 string `{pending,success,failed,**unknown**}`；03 string 7 态；02 string 6 态 | `qy_fund_orders.status` 用 00 的 int8。业务明细表允许 string，但**"结果不可判定"统一叫 `uncertain`**，01 的 `unknown` 废除。08 前端 `QyStatusBadge` 色板追加 `uncertain → warning+pulse` |
| C13 | **`withdraw.methods` 枚举值** | 00 `["balance","fiat"]` 且 `validate()` 硬校验 ⊆{balance,fiat}；03 `["quota","fiat"]` | **统一 `["quota","fiat"]`**（"quota"语义更准，与前端 `withdraw_quota` feature flag 一致）。**这条不裁定会导致 00 的 validate 直接 FatalLog，服务起不来** |
| C14 | **PII 密钥配置名** | 00 `withdraw.payout_account_aes_key`(64 hex)；03 `withdrawal.pii_key`(base64 32B) + `pii_key_version` | **统一 03 的 `pii_key`(base64) + `pii_key_version`**（支持轮换）。段名统一 `withdraw`（非 `withdrawal`） |
| C15 | **引导端点名** | 00 `GET /api/qy/status`；08 `GET /api/qy/config` | **统一 `/api/qy/config`**（08 的前端全部依赖它）。权限用 `TryUserAuth()`（06 的可用率页要求匿名可见） |
| C16 | **管理端路径前缀** | 00/02/06 `/api/qy/admin/<mod>/...`；03 `/api/qy/admin/withdrawal`；07 `/api/qy/violation/rules`（管理端不带 admin） | **统一 `/api/qy/admin/<module>/...`**。07 的规则/记录/封禁/申诉全部迁到 `/api/qy/admin/violation/*` |
| C17 | **降级 HTTP 语义** | 00 disabled→404 / unavailable→503；01 "transfer.enabled=false 返回 403"（同文档另一处又说 404）；08 前端把 403 判为 forbidden | **disabled / feature_off → 404；新库不可用 → 503 + `Retry-After`；权限不足 → 403**。01 的 403 说法废除 |
| C18 | **路由组与中间件注册** | 00 由 `RegisterRoutes` 统一建 `/api/qy` 组挂 gzip+RateLimit；01/03/07 各自 `server.Group(...)`，07 还额外 `middleware.CORS()` | **只有 `qianye/router.go` 能 `engine.Group`**。模块导出 `registerXxxRoutes(user, admin *gin.RouterGroup)`。CORS 不重复挂（引擎级已有） |
| C19 | **前端路由前缀** | 08 `/qy/*`；01 `/qy-transfer`；02 `/qy-affiliate`+`/qy-commission`；03 `/qy-withdrawal`+`/qy-withdrawal-review`；06 `/qy-availability`；07 `/qy-violation/$section` | **以 08 为准 `/qy/*` + `/qy/admin/*`**，01/02/03/06/07 的路由路径全部作废 |
| C20 | **侧边栏改动** | 01 +5 行；02 +2 项；03 +2 项；06 +5 行；07 +1 项+registry；08 统一 +2 行 | **只保留 08 的 2 行**（`use-sidebar-data.ts` import + spread）+ `sidebar-view-registry.ts` 2 行。其余模块的侧边栏改动全部作废 |
| C21 | **i18n 落盘位置** | 01/02/03/04/05/06/07 全说往 `i18n/locales/*.json` ×7 追加；08 说独立 `i18n/qy/*.json` + `addResourceBundle` | **以 08 为准**。禁止任何 qy 键进 `locales/`，禁止对 qy 改动跑 `bun run i18n:sync` |
| C22 | **i18n 键 domain 前缀** | 01 `qy_transfer_*`；07 `qy_violation_*`；05 `qy_log_*`；08 白名单是 `tr/vio/...` 且没有 log domain | **统一 08 白名单并补齐**：`nav common err tr aff wd cm vio avl cfg **log** plan`。01→`qy_tr_*`，07→`qy_vio_*`，04→`qy_plan_*`，05→`qy_log_*` |

### 1.3 数值与语义类

| # | 冲突点 | 各模块说法 | **裁定** |
|---|---|---|---|
| C23 | **佣金成熟期归属（会导致接口无法实现）** | 03 要求 `Commission.Withdrawable(userId, maturityDays)` 返回"available 中 `earned_at <= now-N` 的部分"；但 02 的 `qy_commission_balance.available_quota` 是**标量，没有任何时间维度** | **成熟期判定前移到返佣侧**：02 结算时只吸收 `accrual.mature_at <= now` 的行 → `available_quota` 天然全部可提。`Withdrawable()` 退化为 `return available_quota`。**删除 `withdraw.maturity_days`，统一用 `commission.holding_days`（默认 7）** |
| C24 | **佣金余额表名** | 02 `qy_commission_balance`；03 引用 `qy_commission_accounts` + `qy_commission_ledgers` | **以 02 为准**。`qy_commission_ledgers` **不建**（`qy_commission_accrual` 负额行 + `qy_audit_logs` 已覆盖），03 的 `CommissionPort` 三方法直接操作 `qy_commission_balance` |
| C25 | **quota 精确余数精度** | 02 `decimal(30,10)`；03 `decimal(24,8)` | **统一 `decimal(30,10)`** |
| C26 | **法币金额精度** | 00 audit `decimal(18,4)`；02 `decimal(18,6)`；03 `decimal(18,4)` | **统一 `decimal(18,6)`**，展示层 `Round(2)` |
| C27 | **汇率精度** | 00 `decimal(18,8)`；02 `decimal(12,6)`；03 `decimal(20,8)` | **统一 `decimal(18,8)`** |
| C28 | **比例表示** | 01 `fee_rate float64`；02 `rate_bps int`（万分比）；07 `FeeMultiple decimal(18,6)`；00 `*_percent float64` | **比例统一 `bps int`**（整数万分比，可复现、可入库比较）；**倍数统一 `decimal(18,6)`**。00 的所有 `*_percent float64` 改为 `*_bps int` |
| C29 | **跨库金额字段类型** | 00 `qy_fund_orders.amount_quota int64`；01 坚持 `int`(int32) 以"插入即失败" | `qy_fund_orders.amount_quota` 保持 **int64**（它还承载佣金聚合），但 `twophase.Execute` 入口**强制** `0 < amt <= common.MaxQuota` 才受理。01 的明细表可继续用 int |
| C30 | **时间戳写入方式** | 00/01/03/06/07 手工 `common.GetTimestamp()`；02 用 `gorm:"autoCreateTime"` | **统一手工赋值**，禁用 `autoCreateTime/autoUpdateTime`（GORM 对 int64 的单位推断跨版本不稳定） |
| C31 | **`config.Config` 结构体自相矛盾** | 00 §1.2 里 `HotPathFailOpen bool`，§1.3 又说这三个字段必须是 `*bool` | 以 `*bool` 为准，§1.2 的结构体定义按 §1.3 修正 |
| C32 | **YAML 段字段名整体不匹配** | 00 的 `Transfer/Commission/Withdraw/UsageLog/Availability/Violation` 六段字段，与 01/02/03/05/06/07 各自给的 YAML **几乎无一相同**（例：`min_quota` vs `min_amount_quota`；`usage_log` vs `log_metrics`） | **以各功能模块的 YAML 为准，重写 00 的 config 结构体**。段名固定为：`database/runtime/two_phase/audit/transfer/commission/withdraw/wallet/log_metrics/group_visibility/availability/violation`。00 的 `usage_log` 段改名 `log_metrics` |
| C33 | **钱包页入口卡三家抢同一行** | 01/02/03 都要在 `wallet/index.tsx:350` 插自己的卡；04 又把该区域重构成 Tabs，行号全变 | **合并为 08 的单个 `QyWalletEntryCard`**（内含划转/推广/提现三个跳转），插在 04 改造后的 `<AffiliateRewardsCard/>` 之后、**Tabs 之外**。钱包页净增 2 行（1 import + 1 JSX） |
| C34 | **`other` 里敏感字段归属** | 01 明确"单号不要放 admin_info（用户要看见）"；07 明确"命中词必须放 admin_info" | 两者不冲突，固化为规则：**用户应知（单号/原因文案/费用）放 `other` 顶层；仅管理员可见（命中词/rule_id/quota_saturation/内部 phase）放 `other.admin_info`** |

---

## 2. 原项目改动预算核算

### 2.1 汇总表（按各模块**原始声明**）

| # | 文件 | 行号 | 插入内容 | 行数 | 冲突风险 | 来源 |
|---|---|---|---|---|---|---|
| 1 | `main.go` | :31 | `"…/qianye"` import | 1 | 低 | 00 |
| 2 | `main.go` | :365 | `qianye.Init()` 错误处理块 | 4 | 低 | 00 |
| 3 | `main.go` | :195 | `qianye.RegisterRoutes(server)` | 1 | 低 | 00 |
| 4 | `main.go` | :152 | `qianye.StartBackgroundTasks()` | 1 | 低 | 00 |
| 5 | `model/log.go` | :343 | `QyOnConsumeLog(c,userId,params)` | 1 | 低 | 02 |
| 6 | `model/log.go` | :419 | `QyOnTaskBillingLog(params)` | 1 | 低 | 02 |
| 7 | `model/redemption.go` | :184 | `QyOnRedeemSuccess(...)` | 1 | 低 | 02 |
| 8 | `model/topup.go` | :386 | `QyOnManualTopUpCompleted(...)` | 1 | 低 | 02 |
| 9 | `service/log_info_generate.go` | :11 | `qylog` import | 1 | 低 | 05 |
| 10 | `service/log_info_generate.go` | :85 | `qylog.AttachReasoning(...)` | 1 | 低 | 05 |
| 11 | `service/text_quota.go` | :15 | `qylog` import | 1 | 低 | 05 |
| 12 | `service/text_quota.go` | :524 | `qylog.AttachCacheBasis(...)` | 1 | 中 | 05 |
| 13 | `controller/pricing.go` | :59 | 调用名替换 | 1 | 低 | 06 |
| 14 | `controller/perf_metrics.go` | :22 | 调用名替换 | 1 | 低 | 06 |
| 15 | `controller/perf_metrics.go` | :68 | 调用名替换 | 1 | 低 | 06 |
| 16 | `pkg/perf_metrics/metrics.go` | :11 | `qyhook` import | 1 | 低 | 06 |
| 17 | `pkg/perf_metrics/metrics.go` | :55 | `qyhook.OnRelaySample(...)` | 1 | 低 | 06 |
| 18 | `controller/relay.go` | :27 | `qyviolation` import | 1 | 高 | 07 |
| 19 | `controller/relay.go` | :160 | `PreRelayGuard` 拦截块 | 3 | 高 | 07 |
| 20 | `controller/relay.go` | :180 | `PostRelayGuard(...)` | 1 | 高 | 07 |
| 21 | `router/relay-router.go` | :8 | `qymw` import | 1 | 低 | 07 |
| 22 | `router/relay-router.go` | :73 | `Use(qymw.ViolationGuard())` | 1 | 中 | 07 |
| 23 | `router/relay-router.go` | :198 | 同上（Gemini 原生） | 1 | 中 | 07 |

**原始合计：11 个文件 / 28 行。**

- 行数 28 ≤ 40 ✅
- **文件数 11 > 10，超 1 个文件** ❌

### 2.2 削减方案（按优先级，执行前两条即达标）

| 削减项 | 效果 | 代价 |
|---|---|---|
| **S1（必做）：全部 hook 改同包变量注入** — 新增 `service/qy_export.go`、`pkg/perf_metrics/qy_export.go`、`controller/qy_export.go`、`router/qy_export.go`（纯新增，0 冲突），调用点直接调同包符号 | **省 5 行 import**（#9/#11/#16/#18/#21） | 无。这本来就是 00 §0 定的铁律，05/06/07 违规了 |
| **S2（必做）：砍掉 07 的 M3/M4 中间件层** — 07 自己标注为"可选加速层，默认关闭" | **省 1 个文件（`router/relay-router.go`）+ 2 行** | 失去"已封禁用户秒拒"与"响应体内容检测"。前者由 `middleware/auth.go` 现有的 `userEnabled` 检查覆盖；后者本期本就默认关闭 |
| S3（建议）：砍掉 `model/topup.go:386` 的管理员补单 hook | 省 1 文件 1 行 | 管理员补单的 `exclude_redemption_and_manual` 开关失效。02 的 GORM callback 已能捕获该 topup 写入，补单识别改用 `payment_provider=="" && payment_method=="manual"` 判定即可，**功能不丢** |
| S4（备选）：砍掉 `model/redemption.go:184` 兑换码 hook | 省 1 文件 1 行 | 兑换码充值不返佣（降级）。若运营接受可执行 |

### 2.3 削减后最终预算

| 方案 | 文件数 | 行数 | 达标 |
|---|---|---|---|
| S1+S2 | **10** | **21** | ✅ 刚好卡线 |
| S1+S2+S3 | **9** | **20** | ✅ 留 1 文件余量 |

**推荐执行 S1+S2+S3 → 9 文件 / 20 行**，为后续意外挂载点留缓冲。

最终 9 个文件：`main.go`、`model/log.go`、`model/redemption.go`、`service/log_info_generate.go`、`service/text_quota.go`、`controller/pricing.go`、`controller/perf_metrics.go`、`pkg/perf_metrics/metrics.go`、`controller/relay.go`。

### 2.4 前端改动（单独统计，不计入后端预算）

| 文件 | 行数 | 来源 |
|---|---|---|
| `web/src/hooks/use-sidebar-data.ts` | 2 | 08（01/02/03/06/07 的版本全废） |
| `web/src/components/layout/lib/sidebar-view-registry.ts` | 2 | 08 |
| `web/src/i18n/config.ts` | 2 | 08 |
| `web/src/features/wallet/index.tsx` | 2 | 08（合并 01/02/03 的三份入口卡） |
| `web/src/routes/_authenticated/wallet/index.tsx` | 4 | 04 |
| `web/src/features/wallet/constants.ts` | 追加 | 04 |
| `web/src/features/wallet/index.tsx`（Tabs 改造） | ~50 | 04（与上面 2 行同文件） |
| `web/src/features/wallet/components/subscription-plans-card.tsx` | ~20 | 04 |
| `web/src/features/subscriptions/components/dialogs/subscription-purchase-dialog.tsx` | ~50 | 04 |
| `web/src/features/subscriptions/components/subscriptions-columns.tsx` | 3 | 04 |
| `web/src/features/subscriptions/lib/{index.ts,plan-form.ts}` | 2 | 04 |
| `web/src/features/usage-logs/**` ×4 | ~40 | 05 |
| `web/src/routeTree.gen.ts` | 自动 | — |

**前端合计：约 13 个既有文件。** `i18n/locales/*.json` ×7 与 `static-keys.ts` **改动归零**（C21 裁定的直接收益）。

---

## 3. 表结构总览与统一清单

### 3.1 发现的重复造轮子

| 重复项 | 出现次数 | 处理 |
|---|---|---|
| 租约表 | 5 份（00/01/02/06/07） | 合 1：`qy_task_leases` |
| 资金单据状态机 | 3 份（`qy_fund_orders` / `qy_transfer_orders` / `qy_withdrawals`） | 状态机归一到 `qy_fund_orders`，另两张降为明细表 |
| 审计/事件流水 | 4 份（`qy_audit_logs` / `qy_withdrawal_events` / `qy_pii_audits` / 07 靠 `RecordOperationAuditLog`） | `qy_audit_logs` 为全局；`qy_withdrawal_events` 保留（UI 时间线必需）；`qy_pii_audits` 保留（合规隔离必需）；07 不再建第四张 |
| 游标/KV | 2 份（02 `qy_scan_cursor` / 06 rollup 游标） | 合 1：`qy_kv(k, v, updated_at)` |
| 运营 KV 配置 | 2 份（02 `qy_commission_setting` / 01 建议的 `qy_runtime_flags`） | 合 1：`qy_settings(scope, k, v, operator_id, updated_at)` |
| 佣金余额 | 2 名（`qy_commission_balance` / `qy_commission_accounts`） | 合 1：`qy_commission_balance` |

### 3.2 统一后的完整表清单（新库，共 **26 张** + 主库 1 张）

**地基（4）**
| 表 | 用途 |
|---|---|
| `qy_fund_orders` | 跨库两阶段状态机（kind: transfer/commission_settle/withdraw_quota/withdraw_fiat/reverse） |
| `qy_audit_logs` | 全局审计（人工决策 + 状态跃迁），只增不改 |
| `qy_task_leases` | 分布式租约（name/holder/**fence**/lease_until） |
| `qy_kv` | 游标与轻量运行期状态（充值低水位、rollup 游标） |
| `qy_settings` | 运营可改配置 KV（佣金策略、紧急熔断开关） |

（实际 5 张，`qy_kv`/`qy_settings` 由地基提供）

**划转（3）**：`qy_transfer_orders`（明细，去状态机）、`qy_transfer_user_state`、`qy_transfer_lookup_logs`

**返佣（6）**：`qy_commission_accrual`、`qy_commission_balance`、`qy_commission_settlement`、`qy_commission_rate`、`qy_invite_relation`、`qy_manual_topup_mark`
（`qy_commission_setting`→并入 `qy_settings`；`qy_scan_cursor`→并入 `qy_kv`）

**提现（4）**：`qy_withdrawals`（明细）、`qy_withdrawal_payees`、`qy_withdrawal_events`、`qy_pii_audits`

**可用率（3+1）**：`qy_avail_bucket`、`qy_avail_bucket_hour`、`qy_avail_error`、`qy_avail_channel_bucket`（默认不建）

**违规（7）**：`qy_violation_rule`、`qy_violation_rule_version`、`qy_violation_record`、`qy_violation_payload`、`qy_violation_counter`、`qy_violation_ban`、`qy_violation_appeal`

**主库（1，纯新增文件创建）**：`qy_fund_outbox`

### 3.3 全局字段类型规范（强制）

| 语义 | 类型 | 说明 |
|---|---|---|
| quota 整数 | `bigint`(int64) | 跨库落主库前强制 `0 < v <= common.MaxQuota` |
| quota 精确余数 | `decimal(30,10)` | 佣金累计、carry |
| 法币金额 | `decimal(18,6)` | 展示 `Round(2)` |
| 汇率 | `decimal(18,8)` | 产生时冻结，永不回算 |
| 比例 | `int` bps | 万分比，5% = 500 |
| 倍数 | `decimal(18,6)` | 违规扣费倍数 |
| 时间戳 | `bigint` unix 秒 | 手工 `common.GetTimestamp()`，禁 autoCreateTime |
| 单号 | `varchar(64)` | ≤64（`logs.request_id` 的上限） |
| 幂等 | `(idem_scope varchar(32), idem_key varchar(96))` UNIQUE | 全表统一 |
| 分组名 | `varchar(64)` | — |
| 模型名 | `varchar(128)` | 与上游 `perf_metrics` 对齐，避免索引长度爆 |

### 3.4 索引问题

- **06 `uk_qy_avail_dim`** = 8+64×4+128×4 = 776B，在 InnoDB DYNAMIC(3072B) 下安全，但如果 MySQL 5.7 建表时未启用 `innodb_large_prefix` 会失败 → **`db.Migrate` 需显式检查并在文档写明要求 MySQL 5.7.8+ / ROW_FORMAT=DYNAMIC**
- **02 `uk_qy_ca_src`** = `source_no varchar(128)` × 4 = 512B + 24B，安全；但 02 自己说 `trade_no > 128` 时用 sha256 前 64 —— 需固化为工具函数，禁止各处自行截断
- **01 `qy_transfer_orders`** 的 `idx_qy_tr_status_created` 在降级为明细表后应删除（扫 pending 的职责移交 `qy_fund_orders.idx_fund_probe`）
- **跨库无外键**：所有 `order_no` 关联均为软引用，实施时必须在 model 注释里写明，禁止有人加 `constraint:` tag

### 3.5 `model/qy_export.go` 统一清单（唯一权威）

```
// 私有能力导出
QyLockForUpdate(tx) *gorm.DB
QyCacheApplyUserQuotaDelta(uid int, delta int64) error
QyCommonGroupCol() / QyLogGroupCol() / QyCommonTrueVal() / QyCommonFalseVal() string
QyLogDB() *gorm.DB
QyLogDBSharesMainDB() bool          // 统一命名，废弃 QyLogDBIsMainDB
QyCreateLogWithTx(tx, *Log) error
QyGetPerfMetricsByGroup(startTs, endTs int64, groups []string) ([]QyPerfGroupBucket, error)   // 06
QyGetGroupEnabledModels(group string) []string                                                // 06

// 主库 outbox（唯一精确探针）
type QyFundOutbox
QyEnsureFundOutbox() / QyClaimFundOutbox(tx,row) (bool,error) / QyProbeFundOutbox(no) (bool,error) / QyPruneFundOutbox(before,batch)

// 账本日志
QyRecordLedgerLog(uid, logType int, content, orderNo string, other map[string]any)

// hook 变量（默认 no-op）
var QyOnConsumeLog           = func(c *gin.Context, uid int, p RecordConsumeLogParams) {}
var QyOnTaskBillingLog       = func(p RecordTaskBillingLogParams) {}
var QyOnRedeemSuccess        = func(uid, redemptionId, quota int) {}
```

**删除**：`QyDisableUser`（C7）、`QyOnUserQuotaChanged`（无人使用）、`QyOnManualTopUpCompleted`（S3 砍掉）。

---

## 4. 遗漏与风险

### 4.1 GAPS 十二条逐条核对

| # | GAPS 陷阱 | 处理模块 | 状态 |
|---|---|---|---|
| 1 | 提现方向风险（先扣佣金 vs 先加额度） | 03 §4.1/4.2 | ⚠️ **部分**。方向本身正确（申请即冻结 + S1 CAS 闸门 + hold 不自动判 failed），但探针用 `logs.content LIKE '%claim_no%'` —— ClickHouse 全表扫、且 `RecordLog` 失败即失去证据。**必须改用 `qy_fund_outbox`** |
| 2 | 封号四步 | 07 §5 | ✅ 完整（六步：条件 UPDATE + auth_version 同事务 + PublishUserAuthCache + InvalidateUserTokensCache + RevokeAllUserSessions + 审计）。**但 00 的 `QyDisableUser` 是残缺的第二实现，必须删** |
| 3 | 余额扣负 | 07 §6.3 | ⚠️ **有已知残留竞态**。读 `avail` 与 `PostConsumeQuota` 非原子，并发违规扣费仍可扣成小额负数。07 已明示并用 `fee_quota_want`/`fee_quota` 双列留痕，**接受但需在管理端 `/stats` 暴露"负余额用户数"告警** |
| 4 | 佣金精度归零 | 02 §2 | ✅ 三段式（decimal(30,10) 全精度 → 日聚合 upsert → floor + carry 回写）。数值走查完备 |
| 5 | 热路径查邀请人 | 02 §3 | ✅ 内存 LRU + 负缓存 + singleflight + 有界 chan。**但见 4.2-R1 的丢弃风险** |
| 6 | 充值轮询漏单 | 02 §5 | ✅ GORM callback（信号）+ 延迟重读（数据源）+ 低水位重扫（兜底）+ 唯一索引（去重）。**但见 4.2-R2** |
| 7 | 两阶段中间态对账 | 00 §4.6 | ⚠️ **地基做了，但 01/03 都没接入**，各造了一套补偿任务与 `unknown`/`hold` 状态。C2 裁定后须重做 |
| 8 | 多节点双跑 | 00 §5 | ✅ lease + fence + DB 时钟。**但 5 份表定义，C1 裁定后须统一** |
| 9 | Base UI `keepMounted` | 04 §0 | ✅ 三路交叉验证（bun.lock 版本 + 官方 API + 上游源码），结论可直接实施 |
| 10 | 缓存语义老日志 | 05 §2 | ✅ 正向标记 `qy_input_total`/`qy_ver` + 6 级决策树 + 不可判定显示 `—`，明确拒绝启发式猜测 |
| 11 | 可用率数据源错配 | 06 §6.1 | ✅ 自建采样为主 + `perf_metrics` 兜底/回填/对账。**明示不覆盖 MJ/Suno/视频异步任务**，需在 UI 文案声明 |
| 12 | 违规上下文体积 | 07 §7 | ✅ 先算账再定方案（base64 一律剥离为描述符 + 三层容量闸 + zstd + 分表 + 保留期） |

**结论：12 条中 9 条正面解决，3 条（#1 探针、#3 竞态、#7 未接入）需按本裁定返工。**

### 4.2 GAPS 之外，本轮审查新发现的风险

| # | 风险 | 严重度 | 处理 |
|---|---|---|---|
| R1 | **消费返佣事件队列满即丢弃 = 真实佣金丢失**。02 §3.3 `select default` 丢弃 + 进程退出最多丢 30s 聚合。这是唯一会造成"用户该拿的钱没拿到"的路径 | **高** | ① flush 间隔从 30s 降到 **5s**；② `events_dropped_total > 0` 必须触发 `SysError` 并在 `/health` 红字；③ 队列水位 >80% 时**同步降级为直写 accrual**（宁可加 relay 延迟也不丢钱） |
| R2 | **GORM 全局 Callback 挂在 `model.DB` 上，对每一次主库写入触发**（含 relay 热路径的 token 更新、user quota 更新）。02 只在 `Statement.Dest` 类型断言处早退，但 `Dest` 可能是 `map`/`[]*Token`，断言本身有反射开销 | 中 | 在 callback 首行加 `if tx.Statement.Table != "top_ups" { return }`（字符串比较，零反射），再做类型断言 |
| R3 | **`main.go:152` 与 `:365` 的先后顺序**。00 说 `StartBackgroundTasks` 挂 `:152`、`Init` 挂 `InitResources():365`。若 `InitResources()` 的调用点晚于 152，后台任务会在 `config.Load()`/`db.Init()` 之前启动 → nil panic | **高** | 实施第一步就要在真实 `main.go` 上核对调用顺序，并在 `StartBackgroundTasks` 首行加 `if !config.Enabled() \|\| db.Get()==nil { return }` 双保险 |
| R4 | **04 的钱包页 Tabs 改造与 01/02/03 的入口卡插入点冲突**。04 把 `:293-340` 整块换成 Tabs，行号全变 | 中 | C33 已裁定：合并为 `QyWalletEntryCard`，且**必须在 04 落地之后**再插入（实施顺序约束） |
| R5 | **`plan.subtitle` 后端无长度校验**。04 只在前端 zod 加 `.max(255)`，管理员直接打 API 仍会 DB 报错/静默截断 | 低 | 接受。或在 `controller/subscription.go` 加 1 行——但会吃预算，不建议 |
| R6 | **06 的行为变更会改变匿名用户看到的成功率数字**（`perf-metrics/summary` 分母口径从全站 GroupRatio 收窄到白名单）。可能引发"数字对不上"工单 | 中 | 与需求 6 的新看板同批上线 + 发布说明写明 |
| R7 | **07 缺少 shadow mode（07 自己列为"建议·最重要一条"）**。一个 `.*` 正则能在 30 秒内封掉全站 | **高** | 提升为**必做**：`shadow_mode` 全局开关 + 规则级 `dry_run` + `global_block_rate_limit`(5%)/`global_ban_rate_limit`(20/h) 自动熔断 |
| R8 | **03 的 `payee_digest` 用 HMAC，密钥来自 `pii_key`；密钥轮换后 digest 全部失效**，跨账户风控索引作废 | 低 | digest 用**独立的、不轮换的** `digest_key`，与加密密钥分离 |
| R9 | **05 的 body 探测在 `CleanupBodyStorage` 之后可能读到已释放存储**。05 已要求判 nil + 只接受已缓存 body，但 `c.Get` 返回 `(nil, true)` 的边界易漏 | 中 | 单测覆盖 `Set(KeyBodyStorage, nil)` 后的调用路径 |
| R10 | **`/api/qy/*` 未注册时的 NoRoute 返回体**。08 依赖 404 判 disabled，但 `RelayNotFound` 返回 `{"error":{...}}` 无 `success` 字段；若部署配了 `FRONTEND_BASE_URL` 重定向，可能返回 HTML | 中 | 08 的 `unwrap()` 已用 `isEnvelope()` 兜底为 `disabled` ✅，但需在集成测试里覆盖 HTML 响应场景 |
| R11 | **多模块各自实现进程内缓存**（02 邀请人 LRU、06 查询缓存、07 规则快照），无统一失效/指标基建 | 低 | 地基提供 `qianye/cache` 薄封装（`atomic.Pointer` 快照 + 命中率计数器），三模块复用 |
| R12 | **审计遗漏**：02 的费率变更、01 的紧急熔断开关、06 的口径开关变更，都会影响资金/SLA，但都没强制写 `qy_audit_logs` | 中 | 固化规则：**任何影响资金计算或对外承诺的配置变更，必须 `audit.Write(category=config)`** |

---

## 5. 实施顺序建议

| 阶段 | 内容 | 工作量占比 | 可独立验收的产出 | 前置依赖 |
|---|---|---|---|---|
| **P0 地基** | `qianye/config`（按 C32 重写全部段）、`qianye/db`（含 GET_LOCK 迁移）、`guard`、`lease`（唯一租约实现）、`twophase`（Execute + Compensator）、`audit`、`qy_kv`/`qy_settings`；4 个 `qy_export.go`（model/service/perf_metrics/controller）；`model.QyFundOutbox`；`main.go` 4 处挂载；`qianye/router.go` 骨架 + `/api/qy/config` | **22%** | ① 配置缺失时主程序行为与上游逐字节一致；② 配置存在时 `GET /api/qy/config` 返回 200 且 `/admin/health` 可看到租约与连接池；③ 单测：两阶段模拟主库提交后 kill，补偿任务收敛为 success；④ 多节点并发 AutoMigrate 无死锁 | 无 |
| **P0b 前端骨架** | 08 全套：`features/qy/{lib,hooks,components}`、`i18n/qy/`、路由 `qy/route.tsx` + `qy/admin/route.tsx`、侧边栏 2+2 行、`i18n/config.ts` 2 行、`QyPageBoundary`/`QyStatusBadge`/`QyAmountInput`/`QyFiatText` | **8%** | 扩展关闭时前端零痕迹（菜单不出现、无红色报错）；扩展开启时 `/qy` 工作区可进入、普通用户看不到任何管理入口名称 | P0 的 `/api/qy/config` |
| **P1 零风险快速交付** | 需求 5（分组泄漏，3 行 + 2 新文件）+ 需求 3（钱包 Tabs + 详情截断 + 订阅弹窗，纯前端）+ 需求 4 前端部分 | **10%** | 需求 5：匿名 `curl /api/pricing` 的 `enable_groups` 只含白名单；单测含 `-race` 下"未修改入参底层数组"断言。需求 3：切 Tab 不重复取数、订阅弹窗展示完整说明。**需求 5 可同步向上游提 PR-1/PR-2** | P0b（钱包入口卡位置） |
| **P2 划转** | 第一个走通 twophase 的资金模块，用它验证地基 | **14%** | 并发 100 笔 A↔B 互转：余额守恒、无死锁、无负数、无超发；同 `client_request_id` 提交 5 次只扣 1 次；主库提交后 kill 进程，补偿任务把 pending 正确收敛 | P0 |
| **P3 日志两列后端** | 需求 4 的 `service` 两处 hook + `qy_input_total` 固化 | **5%** | 新日志的缓存率精确、老日志显示 `—` 且不显示错误数值；`/log-metrics/health` 可区分"没人用思考模型"与"hook 没生效" | P0（可与 P2 并行） |
| **P4 返佣** | 账本 + 触发 + 结算（含 `model/log.go` 2 行 hook） | **16%** | 数值走查：rate 5% × 2000 次 × 10 quota → 恰好入账 1000 quota，零损耗；`events_dropped_total` 为 0；充值 callback 与低水位重扫双路不重复返佣 | P0；与 P2 并行（不同表） |
| **P5 提现** | 依赖 P4 的 `CommissionPort`（按 C23 简化后的 `Withdrawable`） | **11%** | 申请→冻结→审核→兑现→到账全链路；PII 加密后明文不可从 DB 直读；对账异常单进 hold 队列不自动判 failed | **强依赖 P4** |
| **P6 可用率** | 采样 + 预聚合 + 矩阵页 | **9%** | 上线流程：回填近 30 天 `perf_metrics` → 对账偏差 <2% → 开放入口；`state` 六态正确（`no_data` 不显示 0%） | P0；与 P4/P5 并行 |
| **P7 违规** | 最后做，因为要动 `controller/relay.go`（最高冲突风险） | **5%** | **必须先跑 shadow mode 一周**（R7）：只记录不扣费不封号，看命中分布；确认误判率后再切真实模式。验收：全局熔断在拦截率 >5% 时自动回落 shadow | P0；建议在其他模块全部合并上游一轮之后再改 relay.go |

**合计 100%。**

### 并行与串行约束

- **必须串行**：P0 → 全部；P4 → P5（`CommissionPort`）；P0b → P1 的钱包入口卡；**P1 的需求 3（Tabs 改造）→ 钱包入口卡插入**（否则行号冲突）
- **可并行**：P2 / P3 / P4 / P6 四条线互不依赖（表不重叠、hook 文件不重叠）
- **建议二期**：07 的 M3/M4 中间件与响应体检测、06 的 attempt 级渠道健康度、06 的 P95/P99 直方图、05 的物化表与筛选、01 的 `capped_by_topup` 策略、03 的 `paid` 后自动冲正（**明确不做**）

### 上游合并窗口建议

在 P7 之前安排一次完整的上游 rebase 演练，重点验证：`routeTree.gen.ts` 删除重建流程、`controller/relay.go` 的 3 行插入点是否仍存在、`service/text_quota.go:524` 的 `cacheWriteTokens` 变量名是否被重构。这三处是本项目仅有的"中/高冲突风险"改动。
