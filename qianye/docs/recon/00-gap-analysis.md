# 缺口分析

---

## 一、信息缺口（按会导致返工的严重度排序）

### 1. 「提现」到底提的是什么——法币还是站内额度？(致命)
需求 3 明写「收款信息 + 打款时间 + 拒绝理由」，这三个词只在**线下法币出款**语境下成立。但所有报告都默认佣金是 `quota`（int32，500000 = $1）。两者不可调和：

- 若是法币：账本主键必须是 `decimal(18,2)` 金额 + 币种，`quota↔money` 需要 `QuotaPerUnit` + `USDExchangeRate=7.3`（后者是 `setting/operation_setting` 里的**可被管理员随时改的全局变量**，历史佣金必须冻结当时汇率，否则对账永远对不上）；且项目**没有任何出款能力**，全靠人工。
- 若是站内额度：那"提现"其实是 `TransferAffQuotaToQuota` 的加强版，"收款信息""打款时间"全是伪需求。

不问清楚，佣金表的字段类型、精度、审核状态机、与主库 quota 的边界会全部推翻重做。

### 2. 返佣的计费口径（三个子问题，全都没答案）
报告把"在哪 hook"研究透了，但**"返多少"完全没定义**：

- **充值返佣范围**：六条支付路径之外，兑换码充值（`redemption.go:184`）、余额购订阅（`subscription.go:833`）、管理员补单（`ManualCompleteTopUp`）算不算？管理员补单算的话，管理员送额度也会给邀请人分佣，是漏洞。
- **消费返佣基数**：`RecordConsumeLog.Quota` 在订阅计费下扣的是订阅额度而非钱包（`other.billing_source == "subscription"`，`wallet_quota_deducted` 可能为 0）。若按 `params.Quota` 返佣，用户用订阅套餐消费也触发返佣，平台重复出血。另外**违规扣费本身也走 `RecordConsumeLog`**（`service/violation_fee.go:150`），会被当成消费返佣。
- **退款/冲正**：`LogTypeRefund`、`Billing.Refund`、`RefundTaskQuota`、订单撤销全部存在，但没有任何一份报告讨论"已返的佣金怎么冲回"。没有冲正机制的佣金账本上线即穿。

### 3. 违规检测的「转发前拦截」与「模型价格倍数扣费」在架构上互斥
这是报告之间对撞出来的硬矛盾，但没人指出：

- relay-pipeline 推荐把拦截中间件挂在 `router/relay-router.go:73`（`Distribute()` **之前**），理由是省开销。
- 但需求 7 要求"模型价格倍数扣费"，`PriceData` 来自 `helper.ModelPriceHelper`（`controller/relay.go:156`），`relayInfo` 来自 `GenRelayInfo`（`:123`）——**都在 `Distribute()` 之后**。中间件在 `Distribute` 前根本拿不到模型单价、分组倍率、`relayInfo.Billing`，`PostConsumeQuota` 也调不了（它依赖 `relayInfo`）。

结论：**「转发前拦截」和「按模型价格倍数扣费」不能在同一个挂载点实现**。要么拦截点后移到 `controller/relay.go:146`（改原项目高频文件），要么固定金额扣费走中间件、倍数扣费走 relay 内部——方案会完全不同。

### 4. 「隐藏分组」这个概念在代码里根本不存在
model-plaza 已确认：全仓 grep 无任何分组可见性字段，分组只是散落在 3 个 map 里的字符串 key，没有分组实体表、没有 `hidden` 标记。所以用户说的"隐藏分组泄漏 BUG"只能是两种之一：

- (a) 指"**不在 `UserUsableGroups` 白名单里的分组名，被 `enable_groups` 泄漏出去了**"——那就是 `controller/pricing.go` 20 行的修复，1 天工作量；
- (b) 指"我想要一套新的**分组隐藏体系**（隐藏标记、显示名、白名单），顺便修泄漏"——那是新建 `setting/group_visibility` + 管理页 + 与 `GroupRatio`/`UserUsableGroups`/`GroupSpecialUsableGroup` 三套现有 map 做同步，10 倍工作量。

不问清楚，规模估算差一个数量级。

### 5. 新库不可用时的降级语义完全没定义
所有报告都只讨论了"**配置文件缺失** → 功能整体禁用"，没有任何一份讨论"**配置存在但 MySQL 连不上/超时**"。这直接决定 relay 主链路的可用性：

- 违规检测中间件挂在 `/v1` 全量流量上，新库挂了是**放行**还是**拒绝**？放行=风控形同虚设；拒绝=新库成为主业务的单点故障。
- 划转/返佣的跨库两阶段中，新库先写 pending 失败时，是否阻止主库扣款？

这个不定，中间件的错误处理、超时、熔断全部要重写。

### 6. 三份报告对「初始化与路由挂载点」给出互相矛盾的结论
- `init()` + blank import：user-quota 报告推荐（"连 main.go 都不用改"），config 报告明确**否决**（`init()` 早于 `main.go:287` 的 `godotenv.Load` 和 `:295` 的 `common.InitEnv()`，`IsMasterNode` 是零值），db-layer 报告说"懒加载可行但迁移必须在 Once 内"。**config 报告是对的**，user-quota 的建议会导致多节点部署下迁移行为错乱。
- 路由挂载：user-quota 说改 `router/api-router.go`（加 `registerQianyeRoutes`），model-plaza 明确说"`api-router.go` 是 300+ 行上游高频改动文件，**务必避开**"，config 说改 `router/main.go` 或 `main.go`。**必须统一到 `router/main.go` 新增一行 `SetQyRouter(router)`**，且注意 config 报告的硬约束：**必须在 `SetWebRouter` 之前**，否则被 gzip/Cache/static.Serve 全局中间件污染。
- YAML 路径：db-layer 推荐 `./config/qianye.yaml`（**`.gitignore` 未覆盖，含 DSN 密码会误提交**），config 推荐 `./data/qianye.yaml`（`.gitignore:29` 已覆盖）。用后者。

### 7. 「管理员审核」审核的是哪个对象
需求 3 的括号里"管理员审核"夹在返佣配置和用户查看之间，指向不明：审核**返佣记录本身**（每笔佣金要人工确认才入账）？还是审核**提现申请**？前者意味着佣金要有 `pending/approved/rejected` 三态且不能自动入账，后者只是提现单状态机。两者的表结构与前端页面完全不同。

### 8. 使用日志两列是否需要「筛选/排序」
logs 报告给了两条路：改 `service/log_info_generate.go`（数据进 `logs.other`）vs 旁路中间件写新库 + 前端按 `request_id` merge。**但只要用户要求按推理强度筛选或按缓存率排序，旁路方案直接不可行**——分页/排序在 SQL 层，跨库 merge 只能对当前页生效。需求只说"新增两列"，没说交互。

### 9. 违规规则的「可配置」到底配什么
现有可复用的只有 `service.AcSearch`（AC 自动机，**纯关键词，不支持正则/权重/上下文窗口**）。若用户预期的是正则、语义模型、外部审核 API、按模型/分组差异化规则，全部要新建，且热路径性能预算需要重新算。

### 10. 佣金比例的精度与「小额消费返佣归零」
`quota` 是 int32，单次消费常见几十到几百 quota。5% 佣金 = 1~10 quota，用 `int(quota * 0.05)` 会大量截断为 0。必须用 decimal 累积「未结算余数」按用户挂账，或改为按日/按笔聚合后结算。所有报告都没提这一点，直接实现会出现"用了一天没有佣金"的 bug。

### 11. 消费返佣的邀请人查询在热路径上
若在 `RecordConsumeLog` 挂 hook，每次消费都要查 `users.inviter_id`（主库）。`RecordConsumeLog` 是**同步调用**（`text_quota.go:526`），没有任何报告提到这里需要缓存。裸查会给主库加上与 relay QPS 等量的读压力。

---

## 二、架构风险：「独立库 + 不改原文件」在哪些地方做不到

### 结论先行：7 项需求里，只有 **3 项半**能真正独立；「钱的最终落点」和「用户身份」100% 在主库。

| 需求 | 能否独立库 | 原因 |
|---|---|---|
| 1 独立库+YAML | ✅ | — |
| 2 余额划转 | ❌ 一半 | `users.quota` 在主库，两条 UPDATE 必须在主库事务内；只有流水能进新库 |
| 3 返佣体系 | ❌ 一半 | 邀请关系 `users.inviter_id`、充值源 `top_ups`、消费源 `logs` 全在原库；佣金→额度的兑现要写主库 |
| 4 钱包 UI | ⚠️ | 纯前端，但 fork 成本见下 |
| 5 日志两列 | ❌ | 数据源在 `LOG_DB.logs.other`，只能读原库 |
| 6a 分组泄漏修复 | ❌ | 必须改 `controller/pricing.go`（这是修 bug，绕不开） |
| 6b 可用率监控 | ❌ | `perf_metrics` 表在**主库**，`logs` 在 LOG_DB |
| 7 违规检测 | ❌ 一半 | 扣费走主库 quota，封号写 `users.status` |

### 必须改动的原项目文件清单 + 冲突风险

**后端：**

| 文件 | 改动 | 行数 | 上游冲突风险 | 备注 |
|---|---|---|---|---|
| `main.go` `InitResources()` 尾部 | `qy.Init()` | 2~5 | **低** | 位置稳定；`defer` 内 `qy.Close()` 可省 |
| `router/main.go:19` 后 | `SetQyRouter(router)` | 1 | **低** | 该函数几乎不动；**必须在 `SetWebRouter` 前** |
| `controller/pricing.go:12-34` | 重写 `filterPricingByUsableGroups` | ~20 | **中** | 单函数替换；注意 `pricingMap` 是全局缓存，**必须新分配 slice** |
| `service/group.go:37` | `GetUserUsableGroups` return 前加 hook | 2 | **低** | 杠杆最大的一处，一次堵住 4 个 handler |
| `router/relay-router.go:73` 后 | 违规中间件 | 1~2 | **中** | 该文件随新端点增加而改；Gemini `/v1beta` 组要单独加 |
| `controller/relay.go:173-181` defer | 违规扣费 hook | 1~3 | **高** | 上游最高频改动文件之一；但这是"按模型价格倍数扣费"的唯一可行点 |
| `controller/relay.go:146` 后 | 提示词检查（若要 `meta.Files` 多模态） | 3 | **高** | 同上 |
| `model/log.go:343` | 消费返佣 hook | 1 | **中** | 必须插在 `:344` 的 `LogConsumeEnabled` 早退**之前** |
| `pkg/perf_metrics/metrics.go:76` 后 | 可用率采样 hook | 1 | **低** | 该包很新，改动少 |
| `controller/perf_metrics.go:22,76-82` | 按用户可用分组过滤 | ~15 | **中** | 泄漏修复 B |
| `model/subscription.go:150` | `varchar(255)` → `text` | 1 | **中** | 仅当套餐备注要长文本；可用新库扩展表规避 |

**"零冲突"技巧（务必用）：** 在 `model/` 包新增 `model/qy_export.go`（纯新文件，不改任何现有文件），导出 `lockForUpdate`、`cacheIncrUserQuota`/`cacheDecrUserQuota`。这是打通 `model` 包私有能力的唯一干净手段。**唯一风险**：上游若未来加同名符号会编译冲突——用 `QyLockForUpdate` 这种带前缀的名字规避。

**前端：**

| 文件 | 冲突风险 | 说明 |
|---|---|---|
| `web/src/routeTree.gen.ts` | **高**（但可自动重生成） | 必须在合并流程文档里写死"冲突时删掉重跑 build" |
| `web/src/i18n/locales/*.json` ×7 | **高频但纯追加** | 用 `qy_xxx_yyy` 下划线扁平键（**不能用点号**，`keySeparator` 默认 `.` 生效会被当嵌套） |
| `web/src/hooks/use-sidebar-data.ts` | **中** | 收敛成 1 行（一个工作区入口），其余子页面走 `SIDEBAR_VIEWS` |
| `web/src/features/wallet/index.tsx` | — | fork vs 直接改，见下 |

### 单独点名：钱包页 fork 是本项目最贵的"不改原文件"代价
wallet-ui 报告推荐 fork `features/wallet/index.tsx` 到 `wallet-ext`。但：

- `RechargeFormCard` 有 **28 个 props**，上游任何一次支付方式增改（该项目已有 epay/stripe/creem/waffo/waffo_pancake/balance 六种，明显在持续扩张）都会让 fork 的编排文件**编译失败**，且失败点在 fork 文件里，合并时不会有冲突提示。
- 钱包页是上游改动最频繁的业务页之一。fork = 永久放弃上游对该页的所有改进。

**这是"改 1 个文件 4 行"和"永久维护一个 390 行的分叉"之间的选择**，报告只算了前者的账。

---

## 三、跨库难题

### 3.1 报告事实**足以**解决的部分
- 划转的主库事务写法（`PurchaseSubscriptionWithBalance` 是正确模板）、双向按 id 升序加锁避死锁、`WHERE quota >= ?` + `RowsAffected` 兜底、提交后 `InvalidateUserCache`——这些都清楚了。
- 新库 pending → 主库事务 → 新库 success/failed 的两阶段模式清楚了。

### 3.2 报告**没有**解决的陷阱

**(1) 两阶段的中间态窗口无人对账。**
划转的正确顺序是「新库写 pending → 主库事务 → 新库置 success」。如果**主库成功但新库回写失败**（进程崩溃/新库断连），用户余额已经变了，流水永远停在 pending。补偿任务必须能从主库反查——但主库的 `logs` 里只有 `RecordLog` 的自由文本 content，**没有结构化的 `transfer_no`**。必须约定：`RecordLog` 的 content 或 `other` 里带上新库单号，否则对账无据可依。所有报告都没提这一点。

**(2) 返佣发放是「新库扣 → 主库加」，方向相反，风险更高。**
划转是"新库记账、主库动钱"；返佣兑现是"新库扣佣金余额、主库 `IncreaseUserQuota`"。主库加钱成功但新库扣减失败 = 佣金可以无限重复领取。必须在**新库事务内先扣并落 `claim_no`**，再调主库，且新库要有唯一约束 + 幂等键。

**(3) 违规封号跨库且没有可复用的原子函数。**
"达阈值自动禁用账户"需要写主库 `users.status = 2`。但：
- `user.Update()` 会 `Omit` quota 但会触发 `IncrementUserAuthVersionWithTx` → Redis fence → 会话吊销（这里**恰恰是想要的**，与划转的要求相反）；
- 但 `controller/user.go` 的 `ManageUser` 是 controller 层，含 `canManageTargetRole` 越权校验，无法复用；
- **没有任何导出的 `model.DisableUser(id, reason)` 函数**。新包必须自己拼「update status + bump auth_version + invalidate 缓存 + 写审计日志」四步，任何一步漏了都会出现"被禁用但旧 token 还能用"的安全洞。

**(4) 违规扣费可能把余额扣成负数。**
`PostConsumeQuota` 底层是 `DecreaseUserQuota`，**没有余额校验**（user-quota 报告已确认）。违规固定扣费遇到余额为 0 的用户会扣成负数。新功能需要自己定义"余额不足时的违规扣费行为"（截断到 0 / 记欠费 / 直接封号）。

**(5) 可用率统计的口径与数据源双重错配。**
- `perf_metrics` 在**主库**、`logs` 在 **LOG_DB**（可能是 ClickHouse），两者不能 join。
- `perf_metrics` 记的是**端到端可用率**（重试全失败才算失败），且**没有 `channel_id` 列**。若用户想看"某分组下某模型的渠道级健康度"，现有数据完全支撑不了，必须在 `processChannelError`（`controller/relay.go:360`）新增 attempt 级采样——**又一处改高频文件**。
- 若改用 `logs` 表：`ERROR_LOG_ENABLED` 默认 **false**（分母直接为 0），且 error log 的 `Group` 用的是 `user.Group` 而 consume log 用的是 `UsingGroup`，**两类日志的 group 语义对不上**，按分组统计必然算错。

**(6) 邀请返佣读主库 `top_ups` 的轮询游标不成立。**
`TopUp` 结构体只有 `CreateTime`/`CompleteTime`，**没有 `UpdatedAt`**；且 epay 路径从不写 `CompleteTime`（恒为 0）；订单是先 pending 插入后转 success，纯 `id > cursor` 游标必然漏单。可行的只有"低水位 id + 近 N 天全量重扫 + 新库 `trade_no` 唯一索引去重"，这是 O(N) 重复扫描，随订单量增长会越来越重。

**(7) 所有新库后台任务必须做分布式互斥。**
轮询、对账、佣金结算在多节点部署下会重复执行。`common.IsMasterNode` 只是环境变量，**不是租约**，多个节点都配 master 就会双跑。要么复用 `model.SystemTaskLock`（写主库）要么在新库自建锁表。报告只提了 `IsMasterNode` 门禁，不够。

---

## 四、被明显低估的复杂度

**① 需求 3 邀请返利体系 —— 实际是 7 项里最大的一项，约占总量 40%。**
报告把它描述成"选个 hook + 建几张表"。真实内容是一套**独立的财务子系统**：佣金账本（含未结算余数、精度处理）+ 三种触发源的幂等（充值 6 路径 + 兑换码 + 订阅 + 消费全路径）+ 退款冲正 + 审核状态机 + 提现单状态机（含收款信息这类 PII 存储、加密、审计）+ 跨库两阶段 + 对账补偿 + 4 个前端页面（用户返佣看板 / 已邀请列表 / 提现申请 / 提现历史）+ 2 个管理端页面（审核队列 / 佣金配置）。而项目里**提现/佣金相关代码 0 命中**，全部从零建。

**② 需求 7 违规检测 —— 被低估约 3 倍。**
被报告一笔带过、实际很重的部分：
- 「记录完整上下文供管理员审查」：body 上限 128MB，一条违规记录可能是几百 KB 的 prompt + 图片 base64。表结构、分表、保留期、体积估算、脱敏——**完全没人评估**。这单项就可能比检测逻辑本身大。
- 规则引擎：`AcSearch` 只支持关键词，正则/优先级/按模型分组差异化全要新建。
- 热路径性能：中间件跑在 `/v1` 全量流量上，同步 AC 匹配 + 异步落库 + 新库故障熔断。
- 违规计数与封号的并发正确性：多节点并发请求同时把计数推过阈值，会重复封号/重复扣费，需要新库的原子 upsert + CAS。
- 前面已说的：拦截点与扣费点互斥、封号无可复用原子函数、余额不足扣负。

**③ 需求 6b 可用率监控页 —— 被低估。**
"读 perf_metrics 画个图"看起来 2 天，但见 §3.2(5)：口径（端到端 vs 渠道级 vs attempt 级）、数据源（主库 vs LOG_DB）、空 group 污染 default、失败原因维度缺失。若用户想要的是"哪个渠道挂了"，现有数据**一条都用不上**，必须新增采样点 + 新表 + 新的 flush 机制，等于重做一个 mini perf_metrics。

**④ 需求 4 钱包 UI —— 表面最小，长期成本最高。**
见 §2 末尾的 fork 分析。另外 Base UI 的 `Tabs.Panel` **默认不 keepMounted**（全仓 0 先例），不加会导致每次切 Tab 重复打两个接口且 `SubscriptionPurchaseDialog` 被卸载；加了则要验证 Base UI 该版本是否真支持——**没人验证过**。

**⑤ 需求 5 日志两列 —— 只有在"不需要筛选"时才是低成本。**
缓存百分比的语义分支（Claude 语义 vs OpenAI 语义，且判别键 `usage_semantic` 是较新才写入的，**老日志无法判别，会算错**）、推理强度覆盖率缺口（Claude thinking / Gemini thinkingBudget / Qwen 全都不落库）、移动端卡片不自动继承新列需手工挂——加起来不小。

---

## 五、必须先问用户的 6 个决策点

**1. 提现是「线下法币打款」还是「站内额度兑换」？**
→ 推荐：**先做站内额度兑换**（佣金 → 平台余额，走 `model.IncreaseUserQuota`），把"收款信息 + 打款时间 + 拒绝理由"的字段作为**预留结构**建好但不启用出款流程。理由：法币出款涉及合规、汇率冻结、对账、人工运营，且平台无出款能力；先跑通额度闭环，法币作为二期。

**2. 「隐藏分组」指的是现有的"未授权分组"，还是要新建一套分组可见性体系？**
→ 推荐：**先按 (a) 理解**，即用 `UserUsableGroups` 白名单做交集裁剪，修 `controller/pricing.go` + `controller/perf_metrics.go` + `service/group.go` 三处（约 40 行）。这已经能消除泄漏。若确认要 (b)，再作为独立需求排期。

**3. 返佣的三个口径：充值算哪些来源？消费按什么基数？退款是否冲正？**
→ 推荐：充值**只算真实付款的 6 条路径**（排除兑换码和管理员补单）；消费**只算 `other.wallet_quota_deducted > 0` 的部分**（订阅额度消费不返佣）并排除 `other.violation_fee == true`；退款**必须冲正**，用新库负向记录 + 唯一键关联原返佣单。

**4. 违规检测：要「转发前真阻断」还是「事后扣费+累计封号」？两者的扣费方式必须分开定。**
→ 推荐：**分成两条独立能力**——提示词拦截走 `router/relay-router.go` 中间件（只做**固定金额**扣费或纯拦截不扣费，因为拿不到模型价格）；「模型价格倍数扣费」走 `controller/relay.go:180` 的 defer hook，照抄 `service/violation_fee.go` 模式。不要试图在一个点同时满足两者。

**5. 划转的风控边界：谁能转给谁？有无限额/手续费/冷却期？「按用户名查收款人」是否可接受用户名可枚举？**
→ 推荐：**收款人只允许用「用户 ID + 二次确认展示脱敏用户名」**，不提供用户名模糊搜索（否则和需求 3 的"用户名脱敏"自相矛盾，等于开放用户枚举）；设单笔上限、日累计上限、`CriticalRateLimit()`；明确规定**赠送额度是否可转**（不限制的话是套现通道）。

**6. 「尽量不改原项目文件」的硬度到底多少？具体是：能否接受改 `controller/relay.go`（高冲突）？能否接受 fork 钱包页而放弃上游该页的后续改进？**
→ 推荐：**给出明确预算——允许改 ≤ 10 个原项目后端文件、总计 ≤ 40 行，且全部是"只增不改"的单行 hook**；`controller/relay.go` 的 2 处必须改（否则需求 7 的倍数扣费做不了）。钱包页**建议直接改原文件而非 fork**（4 行 diff 的合并冲突，远比维护 390 行分叉便宜）。同时把「`routeTree.gen.ts` 冲突即重生成」「i18n 用 `qy_` 前缀扁平键」写进合并流程文档。
