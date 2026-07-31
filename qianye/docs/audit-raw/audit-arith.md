# arith

## 审计结论(视角:扣费算术与金额转换)

---

### 1. `qianye/modules/commission/settle.go:111-125` + `settle.go:172`

**缺陷**:被日封顶 / 结算门槛削掉、留在 `unsettled_amount`(carry)里的佣金,永远不会被再次结算 —— 结算调度的唯一入口 `pendingInviters` 只按"还有未被吸收的 accrual 行"选人,而 carry 不在选人条件里。

**触发场景(可复现)**:
1. 管理员通过 `PUT /api/qy/admin/commission/config` 设 `max_daily_quota_per_inviter = 1000`。
2. 邀请人 A 有已成熟的计佣行合计 5000(单条或多条,`mature_at <= now`)。
3. 第一轮 `runSettle`:`computeSettlement` 得 `net=1000`、`Clipped=4000`、`CarryAfter=4000`(settle.go:59-66);随后 `absorbAccruals` 把这一批**全部** accrual 的 `settled_amount` 写成 `gross_amount`。
4. 第二轮及以后:`pendingInviters` 的 `WHERE ... settled_amount <> gross_amount` 对 A 不再命中 → A 不会被选中;管理端 `POST /commission/settle?user_id=A` 也走 `settleUser`,`len(rows)==0` 在 settle.go:172 直接 `return nil`。
5. 结果:4000 佣金永久停在 `qy_commission_balance.unsettled_amount`,只有当 A 名下**再产生新的计佣行**时才会被顺带发出。下线全部停止消费 = 永久拿不到。

同一根因还覆盖两条路径:`net < minSettle` 时(默认 `min_settle_quota=1000`)全额进 carry;`computeSettlement` 触顶 int32 时超出部分进 carry(`settle_test.go:TestComputeSettlementSaturates` 明确断言"留在余数里")。测试 `TestComputeSettlementDailyCap` 的注释写"明天继续发,不是作废",但调度层并未实现这一点 —— **实现与声明不符**。

**影响等级**:资损(用户少拿佣金,金额上界 = 日封顶差额)

**修复建议**:`pendingInviters` 增加一路来源 —— `SELECT user_id FROM qy_commission_balance WHERE unsettled_amount >= 1 AND debt_blocked = 0`(或 `unsettled_amount` 绝对值非零),与 accrual 来源并集;同时 `settleUser` 在 `len(rows)==0` 时不要早退,应带 `delta=0` 走一遍 `computeSettlement` 把 carry 刷出去。

---

### 2. `qianye/modules/violation/api_admin.go:474-497` vs `qianye/modules/violation/fee.go:180-190`

**缺陷**:违规扣费与退款走的是**两条不对称的账**。扣费经 `service.PostConsumeQuota` 同时扣了 `users.quota`(或订阅池)**和** `tokens.remain_quota`;退款 `refundFee` 只对 `users.quota` 做 `quota + amount`,既不还令牌额度,也不还订阅池。

**触发场景(可复现)**:
1. 用户创建一个非无限额度令牌,`remain_quota = 1000000`。
2. 该令牌的请求命中一条 `action=block_and_charge` 的规则,`computeFee` 得 `Charged = 500000`。
3. `chargeFee` → `service.PostConsumeQuota(info, 500000, 0, true)`:`users.quota -= 500000`,并且因为 `!IsPlayground` 走 `model.DecreaseTokenQuota` → `tokens.remain_quota = 500000`、`used_quota += 500000`(model/token.go:412-440)。
4. 管理员判定误报,调 `POST /admin/violation/records/:id/revoke {"refund": true}`。
5. `refundFee` 只执行 `UPDATE users SET quota = quota + 500000`,`tokens.remain_quota` 仍是 500000。
   → 用户账户余额恢复了,但这条令牌永久少了 500000 可用额度,且 `users.used_quota`(`UpdateUserUsedQuotaAndRequestCount`,fee.go:187)也没有回冲。

订阅用户更严重:`PostConsumeQuota` 在 `BillingSource == subscription` 时扣的是 `UserSubscription.AmountUsed`,而退款一律加回**钱包** → 订阅池的消耗从未归还,钱包却凭空多出等额额度。

**影响等级**:资损 / 数据错误

**修复建议**:退款走与扣费镜像的入口 —— 在 `MainApply` 之外补 `model.IncreaseTokenQuota(rec.TokenId, tokenKey, amount)`(需把 `TokenId`/`TokenKey` 落进 `Record`),并按扣费当时的 `billing_source` 决定回冲钱包还是订阅池(把 `billing_source` 一并冻结进 `qy_violation_record`)。同时回冲 `users.used_quota`。

---

### 3. `qianye/modules/violation/fee.go:166-176`

**缺陷**:余额策略判定读的是**钱包额度**(`model.GetUserQuota`),而实际扣款由 `service.PostConsumeQuota` 按 `relayInfo.BillingSource` 路由到**订阅池**(service/quota.go:414-424)。两者是不同的钱包。

**触发场景(可复现)**:
1. 用户 U 开通订阅(`UserSubscription.AmountTotal` 充足),钱包 `users.quota = 0`(订阅用户的典型状态)。
2. `PostRelayGuard` 在 defer 里触发,此时 `PreConsumeBilling` 已把 `relayInfo.BillingSource` 置为 `subscription`(service/billing_session.go:321)。
3. `chargeFee` 读到 `available = 0`。
   - 默认 `insufficient_balance_policy = clamp`:`available <= 0` → `Charged=0`,`FeeStatus=insufficient`。订阅用户**永远罚不到款**,而记录显示"余额不足",管理员据此排查会被误导。
   - 配 `insufficient_balance_policy = ban`(fee.go:132-138):`available(0) < Want` → `ForceBanWeight = true` → `handleHit` 把 `weight` 顶成 `AutoBanThreshold` → **首次违规即自动封号**,尽管该用户订阅池里有钱。
4. 反向场景:钱包 100000、订阅只剩 100,`available` 判定通过 → `PostConsumeUserSubscriptionDelta` 因 `newUsed > AmountTotal` 返回错误 → `FeeStatus=failed`,罚款丢失。

**影响等级**:数据错误(订阅用户被误封号 / 罚款静默失效且状态标记错误)

**修复建议**:`chargeFee` 按 `info.BillingSource` 取对应池的可用量:订阅时读 `AmountTotal - AmountUsed`,钱包时读 `GetUserQuota`;或直接把违规罚款固定走钱包分支(显式构造一份 `BillingSource=wallet` 的 relayInfo 副本传给 `PostConsumeQuota`),两者选一但必须让"判定的池"与"扣款的池"一致。

---

### 4. `qianye/modules/withdraw/validate.go:97-113`(`acceptCreate`)

**缺陷**:提现的四项声明限额**完全没有实现**:`max_quota_per_order`(默认 500,000,000)、`daily_max_quota`(默认 1,000,000,000)、`cooldown_seconds`(默认 60)、`max_pending_orders`(默认 3)。`acceptCreate` 只校验了 `0 < Quota <= common.MaxQuota` 与 `>= cfg.MinQuota`;`create` 里只有 `checkDailyCount`(笔数)。全仓库对这四个字段的引用只出现在 `config/config.go`、`config/defaults.go`、`config/validate.go`,业务代码零引用。

**触发场景(可复现)**:某用户佣金 `available_quota = 2,000,000,000`(或被 bug/人工调整放大)。他 `POST /api/qy/withdraw {"quota": 2000000000, ...}` —— 校验全部通过(2e9 < MaxInt32),单笔提现是声明上界的 4 倍。`config.go:182-184` 的注释原文正是"不设的话单笔上界只有主库 int32 容量,一次异常申请就会占满整个佣金池",而这道闸门就是没接上。同理,一天可以发 3 笔(`daily_max_count`)× MaxInt32,`daily_max_quota` 无效;两次申请之间无冷却;未终态单数不受限(撤销单还不计入 `checkDailyCount`,可无限循环申请-撤销)。

**影响等级**:资损(风控闸门缺失)

**修复建议**:在 `acceptCreate` 里加 `cfg.MaxQuotaPerOrder > 0 && req.Quota > cfg.MaxQuotaPerOrder → errAmountOutOfRange`;在 `create` 的扩展库事务内(与 `checkDailyCount` 同处、同事务)补当日 `SUM(quota)` 校验、`MAX(created_at)` 冷却校验与未终态单计数校验 —— 必须在事务内,否则和 `checkDailyCount` 一样存在 TOCTOU。

---

### 5. `qianye/modules/transfer/risk.go:61-77`(`evaluateRisk`)

**缺陷**:`transfer.receiver_daily_max_in_count`(默认 50)声明为"防止一个账号被无数小号集中打款"的洗号闸门,但 `evaluateRisk` 只判发起方的四项(`PendingCount`/冷却/日笔数/日额度),从不读 `receiver`。`UserState.DayInCount` 在 `applyReservation`(risk.go:106)里被老老实实累加,却没有任何地方读它做判定;全仓库对 `ReceiverDailyMaxInCount` 的引用只在 config 三件套里。

**触发场景(可复现)**:准备 200 个新账号(过了 `new_account_freeze_hours`),各向同一个汇集账号 R 转 1 笔。每笔都只受发起方限额约束,全部通过。R 的 `day_in_count` 涨到 200,远超配置的 50,没有任何一笔被拒。

**影响等级**:资损(批量套现/洗号路径未被拦截)

**修复建议**:在 `evaluateRisk` 增加 receiver 参数,`cfg.ReceiverDailyMaxInCount > 0 && receiver.DayInCount+1 > cfg.ReceiverDailyMaxInCount` 时返回新错误码;`reserveRisk` 已经在同一事务内按升序锁住了 receiver 行(risk.go:47-51),把判定插在 `applyReservation` 之前即可,无需额外加锁。

---

### 6. `qianye/guard/guard.go:95-115` 与 `guard.go:141-146`

**缺陷**:`Hot` 文档声明"③ 在 `hot_path_timeout_ms` 的 ctx 下执行,超时即放弃",但返佣模块的四个热路径 hook 全部**丢弃了传入的 ctx**(`hook.go:56/147/169/179` 里闭包直接调 `accrueConsume(ev)` / `accrueOneShot(...)` / `clawback(...)`,这些函数根本没有 ctx 形参),内部的 `model.DB` 与 `db.Get()` 调用均未 `WithContext`。因此超时约束不存在。叠加 `HotAsync` 在队列水位 ≥80% 时降级为**同步执行**(guard.go:141-146),这段无界耗时被直接搬到 relay 结算线程上。

**触发场景(可复现)**:进程刚启动,`inviterCache` 全空 → 每条消费日志都走 `resolveInviter` 回主库 →`HotAsync` 提交速率 = relay QPS,而 `hot_hook_workers` 默认只有 2、队列 4096。队列在几秒内达到 80% 水位后,**每一次 `RecordConsumeLog`**(在 `PostTextConsumeQuota` 同步链路上)都会在 relay 线程里同步执行"主库 SELECT users + 扩展库 upsert"。若扩展库此时处于慢查询状态(慢但不报连接级错误,`db.MarkFailure`/熔断不会触发,`isConnLevelError` 只认连接类错误),relay 结算被无上限阻塞。

**影响等级**:拒绝服务

**修复建议**:让 `accrueConsume` / `accrueOneShot` / `clawback` / `resolveInviter` 接受 `ctx` 并在所有 GORM 调用上 `WithContext(ctx)`(violation 模块已经这么做,可照抄 `counter.go:55`、`guard.go:213`);同步降级路径应使用比异步更短的独立超时,或改为"高水位直接丢弃并计数",而不是把无界 IO 放回热路径。

---

### 7. `qianye/modules/violation/rules.go:267-272`(`ValidateRule`)

**缺陷**:规则级 `fee_multiple` 只校验"非负",没有上界;而同语义的全局 YAML 参数 `violation.fee_multiplier` 在 `config/validate.go:244-246` 被严格限制在 `0..100`。规则级覆盖因此可以绕过全局上界。

**触发场景(可复现)**:管理员在规则编辑页把 `fee_multiple` 误填成 `100000`(多打三个零),且 `fee_max_quota` 留 0(不限)。若部署把 `violation.max_fee_quota` 设为 0(`checkQuotaCap` 允许 0,含义是"不限"),`computeFee` 的 `want` 会一路取到 `common.MaxQuota`(clamp 生效但不是拒绝),`applyBalancePolicy` 在默认 `clamp` 策略下把用户余额**一次性扣光**(`res.Charged = available`)。两道上限都被绕开,只留下一条 `quota_saturation` 审计。

**影响等级**:资损(配置事故放大器)

**修复建议**:`ValidateRule` 对 `FeeMultiple` 加与 YAML 一致的 `> 100 → 报错`,对 `FeeFixed` 加合理上界(例如不超过 `max_fee_quota / QuotaPerUnit`);另建议 `checkQuotaCap("violation.max_fee_quota")` 拒绝 0,强制运维显式给出一个有限的全局兜底。

---

### 8. `qianye/config/config.go:185-187`(`PIIRetentionDays`)

**缺陷**:`withdraw.pii_retention_days`(默认 180)声明"到期后清除密文只保留脱敏串",但没有任何清理任务实现它。`withdraw` 模块的 `StartTasks`(`module.go:79-88`)只注册了 `withdraw.reconcile`,`reconcile.go` 只做 `resumeApproved`/`settlePaying`。全仓库对 `PIIRetentionDays` 的引用只在 `config.go` 与 `defaults.go`。

**触发场景(可复现)**:用户提交一笔 fiat 提现并填入银行卡号/真实姓名 → `Payee.CipherText` 落库 → 单据 180 天前就已 `paid`。今天去查 `qy_withdraw_payees`,密文仍在,`KeyVersion` 仍是旧版本。配置承诺的到期清除从未发生。

**影响等级**:信息泄漏(PII 无限期留存,与配置注释"不应在提现完成后无限期留存"直接冲突)

**修复建议**:在 `withdraw.reconcile` 里加一步 `prunePii(ctx)`:`WHERE created_at < now - PIIRetentionDays*86400 AND withdrawal 状态为终态` 的 `Payee` 行,把 `cipher_text` 置空、保留 `masked`/`digest`(`model.go:67` 的注释已经预设了这个语义),分批 Limit 执行。

---

**说明**:以下项已按要求跳过 —— 违规扣费的余额扣负竞态(`fee.go:158-170`,已显式接受)、`guard.HotAsync` 队列满丢弃(已有背压与告警)。此外确认:`calcGross`/`computeFee`(transfer)/`computeFiat`/`applyFiat`/`scaleFiat` 的 bps 换算全部是"先乘后除"且用 decimal,除零分母(`weighted.Div(delta)`、`applyFiat` 的 `qpu`、`availabilityOf`/`tpsOf`/`avgOf`)均有前置判零;`qianye/` 内没有 `int(float64*r)` / `int(math.Round)` / `int(d.IntPart())` 形式的裸金额转换,`int(...)` 的 5 处均作用在已被 `common.MaxQuota` 收敛过的量或纯展示串上。
