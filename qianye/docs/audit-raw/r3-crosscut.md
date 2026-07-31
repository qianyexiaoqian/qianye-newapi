# crosscut

审计完成。工作树已复原(我做的 9 处变异全部回滚,`git diff` 中残留的 `config/*`、`guard.go` 改动与 `web/src/features/qy/pages/admin-group-pricing/` 是**另一个并行会话正在写的 group_pricing 模块**,不属本轮审计范围)。

---

# 一、真实缺陷

## D1（拒绝服务 / 后台任务成批漏跑)——**已在生产日志中实证**

**位置**:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\service\lease\lease.go:61`(INSERT)× `lease.go:68`(`isDuplicateKey` 判据)× `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\db\db.go:55`(`PrepareStmt: true`)

**一句话**:`lease.Acquire` 把「INSERT 撞主键报错」当成常规控制流,而 GORM v1.25.2 的 `PreparedStmtDB` 在**任何** Exec 错误上都会异步 `stmt.Close()` + 删缓存,导致同一时刻抢租约的其它任务拿到 `sql: statement is closed`,`isDuplicateKey` 不认它 → `Acquire` 返回 error → 该任务**整轮跳过**。

**机制(已逐行核对依赖源码)**:`$(go env GOMODCACHE)\gorm.io\gorm@v1.25.2\prepare_stmt.go` 的 `PreparedStmtDB.ExecContext`:
```go
result, err = stmt.ExecContext(ctx, args...)
if err != nil {                    // ← 任何错误,不只是 driver.ErrBadConn
    go stmt.Close()                // ← 异步关闭,与其它 goroutine 的 Exec 竞争
    delete(db.Stmts, query)
}
```
八个 lease 任务的 INSERT **SQL 文本完全相同**,共用同一个缓存 `*sql.Stmt`;而这条 INSERT 从第二轮起**必然**报 1062。

**实证(用户机器上正在跑的实例,非我构造)**:
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\run.log:456-473` —— 同一秒 `01:25:38`,`transfer.reconcile` / `violation.ban_compensate` / `withdraw.reconcile` / `twophase.compensate` 拿到 `Error 1062`,紧接着 line 472 一条 `lease.go:61 sql: statement is closed`。
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\run.err.log:5-10` —— `01:20:38`(进程启动后 t=600s,30/60/300s 三种周期在此对齐)**六个任务同时**「获取租约失败: sql: statement is closed」:`availability.rollup`、`violation.refund_reconcile`、`twophase.compensate`、`withdraw.reconcile`、`commission.topup_scan`、`transfer.reconcile`。

**复现**:扩展库为 MySQL、`runtime.background_enabled=true`、进程运行 ≥2 个 tick。周期对齐点(t = 60/300/600s 的公倍数)必现:N 个任务并发跑同一条已缓存的 INSERT,先拿到 1062 的那个触发 `go stmt.Close()`,其余全部收到 `sql: statement is closed`。

**影响**:每个对齐 tick 上 N−1 个任务丢一整轮 —— `twophase.compensate`(资金单探针补偿)、`withdraw.reconcile`(paying 单收尾 + PII 清理)、`commission.topup_scan`/`settle`、`transfer.reconcile`。下一轮自愈,但因为周期对齐会**系统性反复**发生;资金中间态的收尾延迟被无声拉长。另有常态成本:这条 INSERT 每一轮都要 PREPARE→EXECUTE→CLOSE 三次往返,永远无法命中缓存。

`db.MarkFailure` 不会因此打开熔断(`isConnLevelError` 的串表里没有 `statement is closed`),所以不会放大成全站丢弃 —— 这是唯一的好消息。

**为什么两轮审计都没抓到**:`qianye/` 下全部测试都用 `sqlite.Open(":memory:")` 且不开 `PrepareStmt`,这条路径在测试里根本不存在。

**归属**:`lease.go` 与 `PrepareStmt: true` 均来自地基提交 `80d5f07d`,**非本轮引入**,但本轮是第一次有运行期证据。

**修复建议**:
1. 不要用错误做控制流。把顺序倒过来:先跑那条条件 `UPDATE`(稳态下的唯一路径,`RowsAffected` 天然是判据),只有 `RowsAffected==0` 且回读确认无行时才 INSERT;或者干脆一条语句 `INSERT ... ON DUPLICATE KEY UPDATE holder = IF(lease_until < UNIX_TIMESTAMP(), VALUES(holder), holder), fence = fence + IF(...)`,让正常路径零错误。
2. 兜底:`Acquire` 把 `sql: statement is closed` 视作可重试(重试一次即可,GORM 会重新 prepare),而不是直接放弃本轮。
3. 顺带排查同形状的地方 —— `qianye/service/twophase/execute.go:240` 的 `isDuplicateKey(err)` 也是「靠唯一键报错识别幂等命中」,并发同键时同样可能收到 `statement is closed` 而被误判成硬错误(用户看到一次假失败,重试可自愈,不构成资损)。

---

## D2(测试质量 —— 划转分组限制的**权威判定点**没有行为级回归)

**位置**:被测代码 `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\transfer\service.go:239`;测试 `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\transfer\grouprule_test.go:277`

**一句话**:`TestGroupPolicyIsEnforcedAtBothStagesOfCreate` 是一个 **AST「函数名出现过」检查**,只确认 `applyQuotaTransfer` 里调用了 `enforceGroupPolicy`,完全不检查它的返回值有没有被消费 —— 而全包**没有任何一个测试驱动过 `applyQuotaTransfer`**。

**我实测的结果**:把 `service.go:239-241` 改成
```go
if err := enforceGroupPolicy(sender, receiver, rules); err != nil {
    _ = err   // 调用照做,结论丢弃
}
```
→ `go test ./qianye/modules/transfer/ -count=1` **全绿(ok 7.347s)**。锁内分组闸门被彻底废掉,测试毫无反应。

作为对照,我另外实测的 8 处变异**全部被捕捉**(见第三节),说明这一处是孤例。

**影响**:该测试守的正是它注释里写的那条 ——「提交之后、落账之前改分组的窗口」,也就是唯一会**让钱真的转到不该去的分组**的路径。现在它守的只是"这一行代码还在",而不是"这一行代码还有效"。这与前两轮总结的「纯函数层做对了不算数」是同一形状,只是上移了一层:调用点接上了,但**接上没接上不受回归保护**。

**修复建议**:补一个行为级测试 —— 用 sqlite 建 `users` 两行(vip / default 各一),规则设成 `vip → allow_list[@self]`,直接调 `applyQuotaTransfer(tx, acc, cfg, rules, &snap)`,断言 ① 返回 `errGroupTargetDenied`,② 两行 `quota` 一分未动。AST 检查可以保留作为廉价的第二道,但不能是唯一一道。

---

## D3(加固 —— 契约承诺了、实现没做)

**位置**:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\availability\sample.go:44`(`onRelaySample`)vs 契约 `C:\Users\Administrator\Desktop\qianye\qianye-newapi\pkg\perf_metrics\qy_export.go:28`

`qy_export.go` 明写实现方「自行吞掉 panic」,但 `onRelaySample` 没有 `defer recover()`。`guard.HotAsync` 内部的 `recoverHot` 只保护闭包体(`observe(s)`),同步段的 `buildSample(info, ...)` 是裸的。对照:`qianye/modules/violation/guard.go:60` 有 `defer recoverHot("pre_relay_guard")`,`qianye/modules/usergroup/settings.go:95` 有 `defer recover()` —— 三个热路径 hook 里只有这个漏了。

**诚实说明**:我逐个核对了 `buildSample` 读到的每个字段(`StreamStatus` 有 nil 判、`classifyError` 有 `err == nil` 判、`HasSendResponse` 在非 nil info 上),**当前没有可达的 panic 路径**,所以定级为加固而非缺陷。风险在于:`RelayInfo` 有多条构造路径,上游任何一次改动都可能让它变成"relay 请求 500"。

**修复建议**:`onRelaySample` 首行加 `defer recoverHot(...)`(与 violation 同款)。

---

## D4(其他 —— 低)

**位置**:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\commission\api_admin.go:540`(`adminInvalidateCache`)

该接口是运营手动刷缓存的出口,失效了 inviter / settings / blocked 三份,唯独漏了本轮**新增**的 `groupRateCache`(`qianye/modules/commission/grouprate.go:129` 的 `invalidateGroupRates`)。正常增删改路径会主动失效,所以只在「直接改库」或「多节点」场景下留下最长 60 秒的旧费率窗口,影响有界。但既然这个接口的语义就是"把本模块的缓存全清掉",漏一份会让运营在排查费率不生效时得到一个错误的排除结论。

---

# 二、逐项回答分配给我的问题

### 1. 本轮改动有没有破坏前两轮的修复?——**没有,抽查的 5 项全部仍然成立且有效**

| 修复 | 当前位置 | 状态 |
|---|---|---|
| A1 提现 hold 不被对账翻转 | `qianye/modules/withdraw/reconcile.go:113`(`reconcile_state <> ?`)+ `reconcile.go:154`(Failed 分支再探针) | ✅ 在,且变异可捕捉 |
| B4 幂等指纹校验 | `qianye/service/twophase/execute.go:263`(空值任一侧即跳过) | ✅ 在 |
| B7 `markFailed` 的 `RowsAffected` | `qianye/service/twophase/execute.go:382`(回读 + 不改内存 + 不写审计) | ✅ 在 |
| NEW-2 熔断不计 ctx 超时 | `qianye/db/db.go:362`(`DeadlineExceeded`/`Canceled` 一律 false) | ✅ 在 |
| NEW-2 异步预算独立 | `qianye/guard/guard.go:133`(`asyncBudget` 默认 3000ms)+ `guard.go:253`(`drainQueue` 用它) | ✅ 在 |
| C4 熔断只认真访问过库的作业 | `qianye/guard/guard.go:156`(`touchedDB()` 门控 `MarkSuccess`) | ✅ 在 |
| NEW-1 迁移专用连接去掉读写超时 | `qianye/db/db.go:306`(`migrationDSN`)+ `qianye/db/migrate.go:75` | ✅ 在 |
| NEW-4 指纹不含 FeeQuota | `qianye/service/twophase/execute.go:127`(`Digest` 只收请求要素) | ✅ 在 |
| OLD-2 `filter_group_api` 空开关 | 已从 `Config` 结构体整体删除,`groupvis.go` 包头写明理由 | ✅ 已收 |

### 2. 配置自检 `qianye/config/selfcheck.go`

- **对账逻辑正确**。`checkConsumers`(`selfcheck.go:269`)三类判定齐全(Unregistered / Stale / Unconsumed),`leafFields` 只对 `reflect.Struct` 递归、其余(`*bool`、`[]string`、`map[int]string`)一律当叶子,与 YAML 形状一致,无误报也无漏报。
- **本轮新增字段都登记了**:`commission.topup_rate_percent` / `consume_rate_percent`(→ `modules/commission/settings.go`)、两个已废弃的 `*_rate_bps`(→ `config/defaults.go`)、`runtime.hot_async_timeout_ms`、`database.read/write_timeout_seconds`、`withdraw.pii_keys_retired` 全部在表内。**划转分组规则与注册默认分组本轮刻意没有引入 YAML 键**(规则存扩展库 / `qy_settings`),因此不在自检覆盖面内 —— 这是对的,不是遗漏。
- **不会拖慢启动、不会 panic**。`bootstrap.go:50` 调用点在 `db.Init` **之前**,全程纯反射 + map 查找(约 150 个字段),无 IO;`reflect.TypeOf(Config{})` 恒为结构体,无 nil 解引用路径;三类问题都只 `SysError` 不阻断(`selfcheck.go:298` 注释与实现一致)。
- **它自己的回归是真的**。我把 `qianye/modules/transfer/reconcile.go:155` 改回包内常量 `days := 30`(即 OLD-1 原形),`TestFieldConsumers_ConsumerFilesReallyReferenceTheField` **立即失败**并准确点名 `transfer.lookup_log_retain_days`。
- 另外:并行会话正在写的 group_pricing 已经把 `group_pricing.*` 登记进表但模块目录还没建,此刻 `go test ./qianye/config/` 会因 `open ..\..\qianye\modules\grouppricing\hook.go: cannot find the file` 失败 —— 这**恰好证明**这道防线在岗(它拦住了"先合配置项、后接消费方"),不是缺陷。

### 3. 测试质量抽查(实测 9 处变异)

| # | 变异 | 结果 |
|---|---|---|
| 1 | `withdraw/reconcile.go` 去掉 `reconcile_state <> hold` | ❌ **失败** `TestScanStalePaying_SkipsHoldOrders`(实际扫出 `WD-hold`) |
| 2 | `twophase/execute.go` 去掉 `markFailed` 的 `RowsAffected==0` | ❌ **失败** `TestMarkFailed_DoesNotOverrideOtherPath` 全部子用例 |
| 3 | `twophase/execute.go` 去掉指纹不一致分支 | ❌ **失败** `TestResolveExisting_FingerprintMismatch` ×2、`TestRequestDigest_IgnoresServerDerivedFee` |
| 4 | `guard/guard.go` 去掉 `touchedDB()` 门控,无条件 `MarkSuccess` | ❌ **失败** `TestHotRunOnlyMarksSuccessWhenDBWasTouched/纯内存 hook 不得清零失败计数` |
| 5 | `db/db.go` 把 `DeadlineExceeded` 改判为连接级错误 | ❌ **失败** `TestContextDeadlineIsNotAConnectionFailure` |
| 6 | `model/user.go` 删掉 `prepareForInsert` 首行 hook(上游断链) | ❌ **失败** 4 个用例,含 `TestNewUserGroup_AppliesConfiguredGroup` |
| 7 | `commission/grouprate.go` 让 `resolveRate` 永不命中分组规则 | ❌ **失败** 9 个用例,含 `TestAccrueConsumeFreezesGroupRate`、`TestGroupRateCrudTakesEffectImmediately` |
| 8 | `transfer/reconcile.go` 把配置项换回包内常量(OLD-1 原形) | ❌ **失败** `TestFieldConsumers_ConsumerFilesReallyReferenceTheField` |
| 9 | `transfer/service.go:239` 保留调用但丢弃 `enforceGroupPolicy` 的结论 | ✅ **全绿 —— 见 D2** |

**结论**:9 挑 1 假。上一轮"修复回滚后测试照样全绿"的系统性问题这一轮基本被解决了,尤其第 6 项证明 `usergroup` 的测试真的验的是"上游那一行还在",而不是纯函数返回值。唯一漏网的是划转分组限制的锁内判定点(D2)。

### 4. 主题 CSS `C:\Users\Administrator\Desktop\qianye\qianye-newapi\web\src\styles\qy-steins-gate.css` ——**未发现泄漏**

- **全部 276 行规则都在 `[data-theme-preset='steins-gate']` 作用域内**。唯二例外:`@keyframes qy-grain`(line 57,keyframes 无法被选择器作用域限制,但已带 `qy-` 前缀,与上游无冲突)与 `@media (prefers-reduced-motion)`(line 72,其内部规则仍是限定的)。
- **`.dark [data-theme-preset='steins-gate']::after`(line 53)是有效的**:`data-theme-preset` 由 `web/src/context/theme-customization-provider.tsx:57-65` 写在 **`document.body`** 上,而 `.dark` 由 `web/src/context/theme-provider.tsx:97-98` 写在 **`document.documentElement`** 上 —— 祖先/后代关系成立,不是常见的"同元素后代选择器写死"错误。
- **`body::after` 颗粒层不影响布局/滚动/点击**:`position: fixed`(脱离文档流,不产生溢出)+ `pointer-events: none`(点击穿透)+ 仅动画 `transform`(可合成,不触发重排)。`z-index: 9999` 会盖在 Radix 弹层(z-50)之上,但 5%~7% 透明度且不吃事件,是有意的全屏叠加,不是缺陷。
- 主题未激活时(`data-theme-preset` 被移除或为别的值)整份文件零匹配,其他主题不受影响。

### 5. 上游侵入 ——**10 文件 24 行核对无误,只有一处不是纯增量语义**

逐行核对结果(全部为插入,无一行既有代码被改写):`controller/perf_metrics.go`(+2)、`controller/pricing.go`(+1)、`controller/relay.go`(+2)、`main.go`(+12,含注释与空行)、`model/log.go`(+2)、`model/redemption.go`(+1)、`model/user.go`(+1)、`pkg/perf_metrics/metrics.go`(+1)、`service/log_info_generate.go`(+1)、`service/text_quota.go`(+1)= 24 行 / 10 文件 ✅

三项安全属性核对:
- **panic 不冒泡**:`QyPreRelayGuard`→`violation/guard.go:60` 有 `defer recoverHot`(函数返回值无名,recover 后返回 nil = 放行);`QyResolveNewUserGroup`→`usergroup/settings.go:95` 有 `defer recover()` 且返回 `("",false)` 触发 fail-open;`QyOnConsumeLog`/`QyOnTaskBillingLog`/`QyOnRedeemSuccess` 同步段只做 map 读与原子操作,异步段由 `guard.hotRunWithBudget` 的 `recoverHot` 保护。**唯一缺口见 D3(`QyOnRelaySample`)**。
- **不阻塞**:所有查库动作都在 `guard.HotAsync` 的 worker 里;`QyResolveNewUserGroup` 是唯一跑在主库事务内的同步查库 hook,但它有 `guard.Available()` 前置、`context.WithTimeout(loadBudget())` 硬超时、60 秒进程内缓存,且读失败原样返回入参(`usergroup/resolve.go:20-39`)。
- **不修改传入参数**:`QyGroupVisFilterPricing` 经 `groupvis/filter.go:41-43` 新分配 `out` 与 `visible`,不复用底层数组,不污染全局定价缓存;`availability` 的 `buildSample` 把 `*RelayInfo` 压成纯值 `sample`,不把指针带出本次调用(`sample.go:19-29`);`QyLogMetricsAttach*` 写入 `other` map 是功能本意。
- **唯一的语义例外**:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\controller\relay.go:157`
  ```go
  priceData, err := helper.ModelPriceHelper(c, relayInfo, tokens, meta)
  err = service.QyPreRelayGuard(c, relayInfo, meta, err)   // ← 新增的这一行重绑定了 err
  if err != nil { ... }
  ```
  它是插入的一行,但**重写了既有变量的值**,后面那个既有的 `if err != nil` 分支消费的已不是上游原值。当前是安全的:默认实现直接 `return upstreamErr`(`service/qy_violation_export.go:26-28`),真实实现首行也是 `if upstreamErr != nil { return upstreamErr }`(`violation/guard.go:49-51`)。但这条约束只写在注释里,没有测试守 —— 建议在 violation 包补一条一行的表驱动测试:传入非 nil `upstreamErr` 时 `PreRelayGuard` 必须原样返回同一个 error 值(`assert.Same`)。

---

# 三、整体判断

本轮没有破坏前两轮的任何一项修复,新增的三块业务代码(分组费率、分组划转规则、注册默认分组)在**调用链接通**这件事上明显吸取了教训 —— usergroup 的测试直接插真实用户验证上游那一行还在,commission 的分组费率有端到端冻结验证,selfcheck 把"配置项没有消费方"变成了启动告警 + 可回归的测试。测试质量从上一轮的"大面积假绿"改善到"9 挑 1 假"。

**上线阻塞项:无。** 需要在上线前处理的两条是 **D1**(有生产日志实证、会周期性打掉后台任务整轮执行,虽自愈但会持续拉长资金中间态的收尾延迟)与 **D2**(唯一会让钱转到错误分组的闸门缺行为级回归,风险不在于它现在坏了,而在于下一次改动坏了没人知道)。D3/D4 可并入同一轮顺手收掉。
