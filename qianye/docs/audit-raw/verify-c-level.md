# c-level

验证完成:`go build ./...` 通过,`go test ./qianye/... ./model/...` 全绿。以下判定全部基于修复后代码直接走原始复现路径,并对关键测试做了"回滚后是否会失败"的实证。

---

## C1 提现四项额度风控配置从未被读取 — **真堵住了**

四项逐个确认消费方(全部为新增):

| 配置 | 消费点 | 位置 |
|---|---|---|
| `max_quota_per_order` | `acceptCreate` | `qianye/modules/withdraw/validate.go:114` |
| `daily_max_quota` | `enforceCreateLimits` | `qianye/modules/withdraw/create.go:281` |
| `cooldown_seconds` | 同上 | `create.go:285-297` |
| `max_pending_orders` | 同上 | `create.go:299-309` |

**事务边界正确**:`create.go:77-81` 起扩展库事务 → `submitInTx`(`create.go:126`)→ `create.go:133` 调 `enforceCreateLimits(tx, ...)`,与 `tx.Create(w)`(138)、`FreezeForWithdraw`(147)同一个 `tx`。判定与写入之间没有提交间隙,与原 `checkDailyCount` 同级。

**顺序不变量也对**:`findByIdemKey`(129)排在闸门之前 —— 否则原单自己占住的冷却窗口会把它自己的重试判成违规。`create_limits_test.go:292-324` 专门钉住了这条(重放返回原单 / 换幂等键才被 `errCooldown` 拦)。

**撤销刷单循环已封死**,两道:
- `create.go:277` 新增 `usage.Submitted >= DailyMaxCount*dailySubmitFactor(4)` —— 含已撤销单,默认 `DailyMaxCount=3` 即 12 次/日封顶;
- `create.go:289-291` 冷却窗口刻意**不过滤 status**(注释在 287-288 行说明了理由),撤销后立刻重发同样被 60 秒拦住。

**回滚敏感性**:`enforceCreateLimits` 是新函数,回滚即编译失败;`TestAcceptCreate_EnforcesMaxQuotaPerOrder` 的 `{"超出单笔上限一个额度", 800001, 800000, errAmountOutOfRange}` 在旧 `acceptCreate` 下必然放行 → 失败。测试为真。

上限已下发前端:`api_user.go:47-50`。

*次要瑕疵(非缺陷)*:超单笔上限与超 int32 复用同一个 `errAmountOutOfRange`(`validate.go:109` / `115`),前端无法区分文案。`enforceCreateLimits` 的注释(`create.go:259-263`)诚实承认了"同事务≠串行化,并发可能多放行一笔",硬闸门仍是 `FreezeForWithdraw` 的余额 CAS —— 这个取舍成立,不构成资损。

---

## C2 `receiver_daily_max_in_count` 洗号闸门 — **真堵住了**

`qianye/modules/transfer/risk.go:87`:
```go
if cfg.ReceiverDailyMaxInCount > 0 && receiver.DayInCount+1 > cfg.ReceiverDailyMaxInCount {
    return errReceiverDailyInExceeded
}
```
**位置正确**:`evaluateRisk` 签名已改为 `(sender, receiver UserState, ...)`,调用在 `risk.go:48`,**排在 `applyReservation`(52)之前**。

**锁正确**:`risk.go:31/34` 两行都用 `db.LockForUpdate(tx)`(`db/db.go:210-212` → `clause.Locking{Strength:"UPDATE"}`,扩展库固定 MySQL,真发 `FOR UPDATE`),按 `user_id` 升序,与 `settle.go` 同序。receiver 行在同一事务内被锁住,不需要额外加锁。

**唯一创建入口**:`service.go:70` 在 `twophase` 的 `LocalDetail` 回调里调 `reserveRisk(tx, ...)`,与落单同事务;`validate.go:55` 已拒绝自转,不存在 `sender == receiver` 的别名塌缩。

**跨日/回滚语义正确**:`rollDay`(44)在判定前清零 `DayInCount`;`undoReservation`(136)在主库失败时原路退还,失败划转不会永久吃掉收款方名额。

**测试为真**:`transfer_test.go:402-419` `TestEvaluateRiskAccumulatesReceiverCountAcrossSenders` 直接复现"N 个小号 → 同一汇集账号"的形状,回滚后 `evaluateRisk` 签名不匹配即编译失败。

---

## C3 `guard.Hot` 超时对返佣/封号链路无效 + 高水位同步降级 — **只改了一半**

按子项拆开:

### (b) DSN 硬上界 — 真堵住了
`qianye/db/db.go:289-297` 补齐 `timeout` / `readTimeout` / `writeTimeout`(默认 5s/30s/30s),且"已在 DSN 里显式写过的一律不覆盖"。`secondsParam`(303)把非正值回落到默认 —— 0 在驱动语义里是"永不超时",正是要消灭的状态。`db_test.go:21+` 有覆盖。

### (d) 高水位同步降级 — 真堵住了
`guard.go:156-158` 新增 `syncSafeJobs` 白名单,只有 `availability.sample` 允许同步执行;`inlineAtHighWater`(165)默认拒绝。`commission.consume` / `violation.persist` / `commission.redeem` / `commission.task_refund` 永远不会跑在 relay 线程上。`guard_test.go:136-161` 逐个钉住,包括"未登记的新作业默认不允许"。**原复现步骤 5、6 已不可达。**

### (a) ctx 透传 — 主链路接通,但仍有 3 处裸调用
审计点名的全部已修:`hook.go:57/154/175/185` 闭包透传 ctx;`accrueConsume`(hook.go:90)、`accrueOneShot`(191)、`clawback`(clawback.go:35)、`resolveInviter`(inviter.go:81 → `model.DB.WithContext(ctx)` at 95)、`ensureRelation`(accrual.go:254 → `gdb.WithContext(ctx)` at 259)、`blockedInvitees`(accrual.go:298 → 313)、`writeAccrual`(accrual.go:157 → 162)。我用 sqlite + `registerOpProbe` 实测确认探针在 Query/Create/Raw/Transaction 四条路径上都能触发,ctx 确实到底。

**但热路径闭包里还有裸 GORM 调用:**

1. **`qianye/modules/commission/settings.go:114`** — `loadOverrides()` 的 `gdb.Where("scope = ?", settingScope).Find(&rows)` 无 `WithContext`。它由 `effective()` 调用,而 `effective()` 在热路径上有三个调用点:`hook.go:112`(accrueConsume)、`hook.go:211`(accrueOneShot)、**`accrual.go:238`(alertLargeAccrual,在 `writeAccrual` 末尾 229 行无条件调用 —— 也就是每一次成功计佣都会走一次)**。更糟的是 `settings.go:78-79` 的 `settingsMu.Lock()/defer Unlock()` **横跨了这条查询**(85 行才调 `loadOverrides`)。

   **残留复现**:`settingsCacheSeconds = 60`(settings.go:57)。缓存过期后第一条 `commission.consume` 进 `effective()` 拿住 `settingsMu` → 裸查 `qy_settings`。此时扩展库"可达但慢"(或该表撞上管理端 `writeSetting` 的 upsert 锁),这条语句**不看 ctx**,只能等到驱动层 `readTimeout=30s`。默认 `hot_hook_workers=2`,第二个 worker 进 `effective()` 卡在 `settingsMu` 上。两个 worker 全部占死 30 秒,`hot_path_timeout_ms=200` 完全没有约束力。队列在几秒内越过高水位 —— 这次不会同步降级(d 已修),但队列满后开始 `dropped`,消费返佣没有 outbox,**丢一条就是永久丢一条**。

2. **`qianye/modules/commission/settings.go:192 / 208 / 212`** — `refSalt()` 的三条查询全裸,且同样横跨 `saltOnce` 全局互斥锁(182-183)。调用路径:`ensureRelation` → `accrual.go:272` 的 `inviteeRef(inviteeId, refSalt())`。首次解析到某个下线时命中。查询失败时 `saltCache` 保持空,**每一次调用都会重试**,失败风暴下所有 worker 在这把锁上排队。

3. **`qianye/modules/violation/counter.go:271`** — `markBan` 的 `gdb.Model(&Ban{}).Where("id = ?", id).Updates(updates)` 无 ctx。调用方 `maybeAutoBan`(counter.go:250/253/257)就在 `guard.HotAsync("violation.persist")` 的闭包里(`violation/guard.go:239`)。封号执行完必然走这一步,即封号链路的**最后一步**仍是无界写。

### (c) `disableUserForViolation` — ctx 接了,但 `RevokeAllUserSessions` 没移出同步链
`ban.go:41` 已收 ctx,`ban.go:57` `model.DB.WithContext(ctx).Transaction(...)` —— 最重的带 `FOR UPDATE` 主库事务已受约束。但审计建议的"把 `RevokeAllUserSessions` 移出封号同步链,由 `runBanCompensate` 补做"**没有做**:`ban.go:90` 仍在同步链上,`ban.go:42` 的 `model.GetUserById` 也不带 ctx。代码注释(`ban.go:37-40`)承认了这一点,理由是"封号作业不在 syncSafeJobs 里,最坏只占一个后台 worker"。这个理由成立(我核对过 `syncSafeJobs` 确实只有 `availability.sample`),但结论是:**封号链路的无界 IO 仍在,只是从 relay 线程搬到了 worker 线程**,而 worker 只有 2 个。

### ⚠️ 修复引入的新风险(需要修复者回应)
`hotRun`(guard.go:115-119)用同一个 `hot_path_timeout_ms`(默认 **200ms**)给 `Hot` 和 `HotAsync` **共用**预算。修复前返佣链路一条 GORM 调用都不接 ctx,这 200ms 形同虚设、作业总能跑完;修复后它**真的生效了**,而 `accrueConsume` 一次要在这 200ms 内完成:`resolveInviter`(主库 users 查询,缓存未命中时)+ `ensureRelation`(再一次 `resolveInviter` + `refSalt` + insert)+ `blockedInvitees`(60 秒一次全表 Pluck)+ `effective()`(60 秒一次查询)+ `writeAccrual`(upsert)——**最多 5~6 次往返**。跨可用区部署(RTT 10~20ms)或任何负载尖峰都会超预算,而 `accrueConsume` 返回 err 后只走 `logThrottled` + `MarkFailure`,**没有重试、没有 outbox,这笔佣金永久丢失**。

建议:给异步作业单独一个更宽的预算(例如 `hot_async_timeout_ms`,1~3 秒),把 200ms 留给真正跑在 relay 线程上的 `Hot`。当前形态是拿"资损"换"worker 占用"。

**测试评估**:`guard_test.go` 的四个测试都是真的(回滚后 `drainQueue`/`hotRun`/`inlineAtHighWater`/`skipped` 均不存在,编译即失败)。但**没有任何测试覆盖"ctx 真的到了 GORM"** —— 三个残留裸调用不会被现有测试捕捉到。

---

## C4 熔断在"库可达但查询变慢"时永远打不开 — **真堵住了(两处都改了)**

**第一处**(纯内存 hook 清零失败计数):`guard.go:121` 新增 `ctx, touchedDB := db.WithOpProbe(ctx)`,`guard.go:131-133` 改为 `if touchedDB() { db.MarkSuccess() }`。探针实现在 `db/db.go:169-204`,通过 `registerOpProbe` 挂在六类 GORM 语句的 **After** 回调上(用 After 而非 Before 是对的:失败的语句同样算"访问过库")。`Init` 在 `db.go:74-76` 硬失败于注册错误。

我实测验证了探针链路可用(临时 sqlite 测试,已删除):Query/Create/Raw/Transaction 内的语句都能把 `touched` 置 true,**而不带 `WithContext(ctx)` 的裸调用不会** —— 这正好也是 C3 残留三处不会给熔断投健康票的原因,方向保守。

**第二处**(探测成功无条件清零):`db/health.go:70-78` 新增 `markProbeHealthy()`,只在 `healthy` 由 false 翻回 true 时才 `failStreak.Store(0)` + `openUntil.Store(0)`。git diff 确认原来的三行无条件 store 已被搬进这个守卫。

原复现路径("`availability.sample` 频率远高于失败频率 → failStreak 永远回不到 5")现在不成立:`observe` 是纯内存,`touchedDB()` 恒为 false,不再 `MarkSuccess`。

**测试为真**:`guard_test.go:82-111`,回滚 `touchedDB` 判断后 `assert.EqualValues(t, 1, failStreakNow(t))` 必然失败(变成 0)。

*残留(审计已预告)*:判据仍是"连续失败数"而非"窗口失败率"。任何一个成功的**碰库** hook 仍会把 `failStreak` 清零。审计原文把窗口失败率列为"更彻底"的方案,修复者选了最小方案,是可接受的取舍 —— 但在混合负载下(慢查询与快查询并存)熔断仍可能打不开。另外 `markProbeHealthy` 会在 `MarkFailure` 开熔断后的第一次 Ping 成功时把 `openUntil` 清零,实际熔断窗口被压到 ≤ `health_interval_seconds`(15s)而不是 `breaker_open_seconds`(30s)—— 这是 `StartHealthLoop` 原有的"自动恢复"语义,不是本次引入。

---

## C5 `QyPruneFundOutbox` 的 Limit 被静默忽略 — **真堵住了**

`model/qy_export.go:124-138` 改为"先 Pluck 主键再按主键删":
```go
DB.Model(&QyFundOutbox{}).Where("created_at < ?", before).Order("id").Limit(batch).Pluck("id", &ids)
...
DB.Where("id IN ?", ids).Delete(&QyFundOutbox{})
```
`SELECT` 上的 `LIMIT` 三种方言都渲染,`DELETE ... WHERE id IN (...)` 也是三方言通用 → **MySQL / PostgreSQL / SQLite 上行为一致**。`batch <= 0` 回落 200(127 行),不会退化成"不限"。

**调用方加了循环**:`qianye/service/twophase/compensate.go:254-270`,`maxPruneRounds = 50`(单轮上界 200×50 = 1 万行),每轮检查 `ctx.Err()`(257)并 `time.Sleep(50ms)`(269)限速,删不满一批即退出(266)。原"MySQL 上 800 行/天追不上净增长"的问题一并解决。

**测试为真 —— 我做了实证**。临时在 model 包跑旧写法 `DB.Where("created_at < ?", 200).Limit(3).Delete(&QyFundOutbox{})`,sqlite 下 `RowsAffected = 10`(应为 3),LIMIT 确实被静默忽略。因此 `model/qy_export_test.go:44` 的 `assert.EqualValues(t, 3, deleted)` 在旧实现下必然失败。测试跑在 sqlite 上(model 包的 TestMain),恰好是受害方言之一,选得对。

---

## D 级

**D1 `pii_retention_days` 清理任务 — 真堵住了**
`qianye/modules/withdraw/payee.go:189-227` 新增 `prunePii`,调用方 `reconcile.go:51`(与 `resumeApproved`/`settlePaying` 并列,共用 `withdraw.reconcile` 租约,`module.go` 里已注册)。实现质量高于建议:
- **终态判定写进扫描条件**(`payee.go:201-204` 的 `withdraw_no IN (?)` 子查询),不是取回来再过滤 —— 否则一批长期挂着的 hold 单会永远占满每轮 batch,任务看着在跑实际一行清不掉。`payee_purge_test.go:100-119` 用 `batch=1` 专门钉住了这条,这是个**有洞察力的测试**,不是凑数的。
- `created_at > 0` 防御异常行被当成"无限久远"(测试 `WD-zero` 覆盖)。
- UPDATE 上再带一次 `purged_at = 0`(216),防租约交接窗口重复刷时间戳。
- 只清 `Cipher`,保留 `Masked` / `Digest`(测试 87-88 行断言)。`Payee.Cipher` 是 `[]byte` 且无 `not null`(`model.go:120`),置 nil 安全。

**D2 `QueueStats` 数据竞争 — 真堵住了**
`guard.go:248` 开头加了 `startWorkers()`。`guard_test.go:168-175` 断言 `capacity` 为正且 `queue != nil`,回滚后在未跑过 `HotAsync` 的测试进程里 `cap(nil chan)` = 0 → 失败。

**D3 裁决理由长度 — 真堵住了**
`qianye/controller/admin.go:122` `maxResolveReasonRunes = 200`,`checkResolveReason`(134)按 rune 校验并返回 `qy_reason_too_long`,`AdminResolveFundOrder`(174-178)前置调用。写库前还有 rune 安全兜底:`admin.go:205` `audit.Truncate("人工裁决: "+reason, 512)`。`admin_test.go:31-34` 覆盖 201 汉字 / 201 ASCII / 10 万字节剪枝路径。

**D4 规则级 `fee_multiple` 上界 — 真堵住了**
`qianye/modules/violation/rules.go:284-286` 新增 `FeeMultiple > maxFeeMultiple(100)` 拒绝,与 YAML 的 `violation.fee_multiplier` 同口径。`rules_test.go:170-201` 覆盖边界(100 放行 / 100.000001 拒绝 / 1e9 拒绝)。

建议的第二半("`checkQuotaCap` 拒绝 0")**没做**,但**不构成残留**:`config/defaults.go:92` 是 `int64Default(&v.MaxFeeQuota, 5000000)`,而 `int64Default`(defaults.go:109-113)在值为 0 时覆盖成默认值 —— 也就是说 YAML 里写 `max_fee_quota: 0` 会被改写成 5000000,负数被 `checkQuotaCap` 拒绝。**"max_fee_quota = 0 = 不限"这个前置条件在配置层根本不可达**,D4 的叠加攻击面已经关闭。

**D5 单字符用户名脱敏 — 真堵住了**
`qianye/modules/commission/mask.go:47-52` 把 `case n <= 2` 拆成 `n == 1 → "**"` 与 `n == 2 → r[0]+"**"`,与 `transfer/validate.go` 口径统一。`mask_test.go` 同步改了期望值并新增 `{"单字符本地部分邮箱", "a@example.com", "**@***.com"}`。

---

## 汇总

| 缺陷 | 判定 |
|---|---|
| C1 | 真堵住了 |
| C2 | 真堵住了 |
| **C3** | **只改了一半** — (b)(d) 完整;(a) 三处裸调用残留;(c) `RevokeAllUserSessions` 未移出;**另引入 200ms 预算导致返佣静默丢失的新风险** |
| C4 | 真堵住了(两处都改了,探针链路实测有效) |
| C5 | 真堵住了(旧实现失效性已实证) |
| D1~D5 | 全部真堵住了 |

**C3 必须回炉的三行**:
- `qianye/modules/commission/settings.go:114`(`loadOverrides`,且横跨 `settingsMu`)
- `qianye/modules/commission/settings.go:192/208/212`(`refSalt`,且横跨 `saltOnce`)
- `qianye/modules/violation/counter.go:271`(`markBan`,封号链路最后一步)

三者都需要把 ctx 从各自的热路径闭包传下来。**外加一个设计决策需要修复者回应**:异步 worker 作业是否应该与同步 `Hot` 共用 200ms 预算 —— 当前形态下,一次超过 200ms 的计佣就是一笔永久丢失的佣金。
