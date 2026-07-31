# a-level

复核完成。以下是 A 级 5 条的独立判定。

---

## A1 提现 hold 被对账任务翻转 —— **真堵住了**

**(a) 扫描条件排除 hold:已接上。**
`qianye/modules/withdraw/reconcile.go:106-113`
```go
Where("status = ? AND reconcile_state <> ? AND settle_started_at > 0 AND settle_started_at < ?",
    StatusPaying, ReconcileHold, stale)
```
`ReconcileState` 列是 `not null;default:''`(`model.go:94`),不存在 `NULL <> 'hold'` 三值逻辑把正常单一起过滤掉的坑。调用链完整:`reconcile:50 → settlePaying:82 → scanStalePaying`。

**(b) Failed 分支再探一次 outbox:已接上,且两个入口都补了。**
- 对账侧:`reconcile.go:137-161` 新抽的纯函数 `decideSettle`,`StatusFailed` 分支 `if probe() { return actionHold }`;`reconcile.go:181` 真正被 `settleOnePaying` 调用,probe 惰性传入 `mainSideApplied(w.OrderNo)`。
- 业务线程侧:`credit.go:73-79`,`order.Status == StatusFailed` 时先 `mainSideApplied(order.OrderNo)`,为真走 `holdForReview` 并 `return`,不再落到 `failWithdrawal`。

**(c) 与 transfer 一致。** `transfer/reconcile.go:124-128` 的 `StatusFailed` 分支同样先探针再 `markUncertainAfterConflict`。两边口径已对齐(机制不同——transfer 推资金单到 Uncertain、withdraw 打 `reconcile_state=hold`——但语义一致:都不自动退)。

**hold 单的出口收敛正确**:`resolveHold`(`review.go:230-279`,人工)、`finishPaid`(补偿任务经 `resolveAfterCompensation`,`withdraw/module.go:30` 已注册 Resolver)。没有第三条路径能让 hold 单回到 `failWithdrawal`——`creditQuota` 的准入闸门是 `startPaying` 的 `From: StatusApproved`,hold 单是 paying,进不去。

**回归测试是真的。** `reconcile_test.go:19` 直接调生产函数 `scanStalePaying`,去掉 WHERE 条件即失败;`reconcile_test.go:61` 的 `TestDecideSettle` 显式断言了 probe 的**调用时机**(`wantProbed`),把 Failed 分支必须探针这件事钉死。

**唯一残留(既有取舍,非新引入)**:`TwoPhase.main_outbox_enabled: false` 时 `mainSideApplied` 恒返回 false(`credit.go:90`),Failed 分支退化回自动退款。默认值是 true,且 transfer 有同样行为,不算本次修复的漏。

---

## A2 违规退款只还钱包 —— **真堵住了(代码);但冻结那一步没有任何测试**

**冻结确实发生了,而且落在 `Create` 之前。** 这是我重点查的"改了一半"点:
- `fee.go:237-239` 在 `service.PostConsumeQuota` **之前**写 `rec.BillingSource = info.BillingSource` / `rec.SubscriptionId = info.SubscriptionId`;
- `guard.go:174` 调 `chargeFee(...)` → `guard.go:193` 才 `persist(rec, payload)` → `guard.go:213` 整行 `Create(rec)`。所以两个字段真的进了库,不是只改了内存。
- `TokenId` 本来就在 `newRecord`(`guard.go:263`)里冻结。

**回冲的是同一个池、同一张令牌 —— 逐项对到了 `PostConsumeQuota` 的对侧:**

| 扣费侧 (`service/quota.go:411-446`) | 退款侧 (`violation/api_admin.go:605-635`) |
|---|---|
| `BillingSource==subscription` → `PostConsumeUserSubscriptionDelta(SubscriptionId, +q)`(`model/subscription.go:1500`,锁行改 `amount_used`) | `refundToSubscription`(`:654`)`QyLockForUpdate` 同一 `id AND user_id`,`amount_used - q`,下界夹 0 —— 与 `subscription.go:1515` 同口径 |
| 否则 → `DecreaseUserQuota(userId, q)`(只动 `users.quota`) | `creditWallet`(`:638`)`quota + q`,带 `quota <= MaxQuota-amount` 溢出闸 |
| `DecreaseTokenQuota(TokenId, TokenKey, q)` → `remain_quota - q`,`used_quota + q`(`model/token.go:431`) | `:628-634` 按 `id = rec.TokenId AND user_id = rec.UserId`,`remain_quota + q`、`used_quota - q` |
| `chargeFee:248` 额外 `UpdateUserUsedQuotaAndRequestCount(+q)` | `:617-621` `users.used_quota - q`(CASE WHEN 夹下界,跨库安全) |

三处全在**同一个主库事务**里(`MainApply`),受 outbox 探针的"执行且只执行一次"覆盖。缓存侧 `AfterCommit:566-573` 用 `InvalidateUserCache` + `InvalidateUserTokensCache` 整体失效,比扣费侧的 `cacheDecrTokenQuota` 增量更稳。

`IsPlayground` 的不对称也没漏:`chargeable()`(`guard.go:305`)排除 playground,所以不会出现"扣费跳过令牌、退款却给令牌加钱"。

**测试判定:退款路由部分是真的,冻结部分是假的(未覆盖)。**
`refund_test.go:93` 的两个子用例直接跑生产函数 `applyRefundOnMainDB`,退回旧的"只加钱包"必失败,两条断言正好卡住"订阅池必须归还"和"钱包不得净增发"。

但 **`chargeFee` 全仓没有任何测试触及**(`grep chargeFee qianye/modules/violation/*_test.go` 只有注释)。`refund_test.go:57` 的 `chargedRecord()` 是手工把 `BillingSource` 填好的。把 `fee.go:237-239` 那两行删掉,全部测试照样绿,而线上每一笔订阅用户的违规退款都会静默退进钱包 —— 也就是 A2 原缺陷的一半原样复活。这条链路上最容易被后人误删的两行,恰恰是零覆盖的。

**低优先残留**:`chargeFee:248` 同时把 `request_count + 1`,退款不回冲(测试 `:104` 把这个行为固化了);`chargeFee:250` 的 `UpdateChannelUsedQuota` 也不回冲。都不是用户资金,可接受,但渠道用量统计会永久虚高。

---

## A3 已入队作业出队后被静默丢弃 —— **真堵住了**

`guard/guard.go:112` 新增 `hotRun(name, fn)`(只做 panic 拦截 + 超时 ctx + 错误处理),`guard.go:223-227` 的 `drainQueue` 改调 `hotRun`,`Hot`(`:97`)保留可用性判断但只在入队前用。可用性挡掉的路径不再静默:`recordSkip`(`:233-240`)自增 `skipped` 并限频告警,`QueueStats`(`:249-255`)把 `skipped` 与 `dropped` 并列暴露。

包头注释也同步改了(`guard.go:5-8`):"跳过只发生在入队之前;已经进了队列的作业出队后一律执行"。

**测试是真的。** `guard_test.go:34` 直接把两个 job 塞进 chan 后调 `drainQueue`,前置断言 `require.False(t, Available())` 精确复现"出队那一刻扩展不可用"。把 `drainQueue` 改回 `Hot` 即失败。`guard_test.go:59` 覆盖 skipped 计数。

顺带确认:`violation/guard.go:201` 的 `persist` 仍有自己的 `!db.Available() → recordDrops.Add(1)` 前置,那条路计入 `record_drops`(`violation/breaker.go:170` 已暴露),不会变成第三种静默丢失。

---

## A4 充值返佣游标越过失败订单 —— **真堵住了**

**`accrueTopUp` 真的返回 error 了:**`topup_scan.go:162-173`,签名 `func accrueTopUp(ctx context.Context, t *model.TopUp) error`,末尾 `return accrueOneShot(...)`。`accrueOneShot`(`hook.go:191-231`)把 `resolveInviter` 与 `writeAccrual` 的 error 都往上抛,不是吞掉后返回 nil。

**游标真的会被卡住:**`scanBatch`(`:101-126`)记录 `MinFailed`(取**首个**失败 id);`lowWaterAfter`(`:138-147`)取 `min(MaxScanned, MinPending-1, MinFailed-1)`;`runTopupScan:63-66` 真的用了它 —— `next := lowWaterAfter(out)`,不是 `maxScanned`。并且 `:78-82` 在有失败时直接结束本轮,不再往后批推进。

**按你给的构造走一遍**(id 100-104,101 失败):`MaxScanned=104`、`MinFailed=101` → `next = 100`。游标停在失败单之前一位,下一轮 `WHERE id > 100` 重新扫到 101。测试 `topup_scan_test.go:80` 就是这个用例,断言 `lowWaterAfter(out) == 100`。

**反向约束也测了**:`topup_scan_test.go:103` 确认"口径排除(余额支付)"与"零基数"返回 nil 而不是 error —— 否则一笔永远返不了佣的订单会把整条扫描线永久钉死,那是把资损换成了停滞。

停滞可观测:`topupHeld` 计数(`topup_scan.go:65`)已进 `metricsSnapshot`(`metrics.go:43` `topup_cursor_held`)并挂在 `api_admin.go:313` 的健康接口上。

**测试覆盖的小缺口**:`runTopupScan` 这个调度函数本身没有测试。两个纯函数(`scanBatch`/`lowWaterAfter`)覆盖到位,但"调度层真的调了 `lowWaterAfter` 而不是 `maxScanned`"这件事靠人眼。相比 A5 这属于轻微,因为接线只有 3 行且就在函数内。

---

## A5 被封顶削掉的 carry 发不出去 —— **代码两半都改了;但回归测试对这条缺陷完全无效(实测确认)**

**代码判定:真堵住了。**
- `pendingInviters`(`settle.go:119-146`)确实并上了第二路:`gdb.Model(&Balance{}).Where("unsettled_amount >= ?", carryFloor(effective().MinSettleQuota))`,再经 `mergeInviterIds` 去重、升序、**合并后**截断。
- `settleUser`(`settle.go:250-268`)确实**不再早退**:`len(rows)==0` 时先 `peek` Balance,`ErrRecordNotFound` 或 `UnsettledAmount < 1` 才 return,否则带 `delta=0` 继续往下走一遍 `computeSettlement`。
- 配套的两个坑也补上了:`settleNeeded`(`:202`)让 carry-only 且 net≠0 时照样落结算单;`batchRate`(`:189`)在 `delta==0` 时退回 `currentUsdRate()` 而不是零 —— 否则 carry-only 轮会"加了额度不加法币",`AvailableFiat` 与 `AvailableQuota` 永久漂移,提现按法币折算会少给钱。这是修 A5 时新暴露的次生问题,修复者看到了。

**测试判定:假的。这一条正是你担心的形态。**

我把两半修复**同时回滚**(`pendingInviters` 去掉 carry 那一路 + `settleUser` 恢复 `len(rows)==0 → return nil`),然后跑:

```
go test ./qianye/modules/commission/... -count=1
ok   github.com/QuantumNous/new-api/qianye/modules/commission   6.552s
```

**全绿。** 原因:
- `scheduling_test.go:20` `TestMergeInviterIdsKeepsCarryOnlySource` 只测 `mergeInviterIds` 这个新增的纯 helper,从不检查 `pendingInviters` 有没有真去查 `qy_commission_balance`;
- `scheduling_test.go:87` `TestSettleNeededFlushesCarryWithoutNewAccruals` 只测 `computeSettlement` + `settleNeeded` 的算术循环,从不调 `settleUser`;
- commission 包**根本没有 DB 测试脚手架**(withdraw / violation 都有 `sqlite :memory:` 的 `newTestDB`,commission 一个都没有,`grep sqlite.Open qianye/modules/commission` 零命中)。

原审计的原话是"`TestComputeSettlementDailyCap` 注释写'明天继续发',但纯函数是对的、调度层没接上。**测试覆盖了算术,没覆盖调度**"。修复后的测试**换了个函数名,重复了同一个错误**:`TestSettleNeededFlushesCarryWithoutNewAccruals` 仍然只覆盖算术。这次代码接上了,但没有任何东西能防止它下一次被断开。

**建议**:给 commission 补一个和 `withdraw/testdb_test.go` 同规格的 sqlite 脚手架,写两条 DB 级断言:①`Balance{unsettled_amount: 4000}` 且无任何 accrual 行时,`pendingInviters` 必须返回该 user;②同样状态下 `settleUser(id)` 必须产生一张 `GrantedQuota>0` 的 Settlement 且 `unsettled_amount` 下降。这两条各自能独立杀死一半的回滚。

**次要观察(不是缺陷,是新引入的调度形状)**:两路来源共享同一个 `settleInviterBatch=500` 且合并后按 user_id 升序截断。若有 ≥500 个低 id 用户的 carry 长期被日封顶卡住(carry 远大于 `daily_cap_quota`),他们会长期占满名额,把高 id 的正常待结算用户挤到后面。旧实现只有一路来源时也有同样的升序饥饿,所以不是回归,但修复放大了它。碰到大额 carry + 小日封顶的部署值得改成轮转游标。

---

## 附:复核过程中发现的一条 flaky 测试(不在 A 级范围,但会污染 CI)

首次全量 `go test ./qianye/...` 时 `TestMatchTypes/keyword_不区分大小写且只回报本规则的命中词` 失败(`rules_test.go:35`,`v.Terms` 为 nil),重跑 8 次均通过。

根因不是 flake 掩盖的缺陷,是测试本身依赖墙钟:`rules.go:377-427` 的 `scan` 有 20ms 预算(`config/defaults.go:100` `ScanTimeoutMs=20`),超时会 `return &verdict{Timeout: true}` —— 一个 `Rule == nil`、`Terms == nil` 的 verdict。测试的 `require.NotNil(t, v)` 会通过,下一行 `assert.Equal(t, []string{"违禁词a"}, v.Terms)` 才炸。并发跑满包时首次 `service.AcSearch` 要现建 AC 自动机(`service/str.go:98` `getOrBuildAC` 冷缓存),偶发超过 20ms。

生产侧无碍(`PreRelayGuard:79` 显式判了 `v.Rule == nil`),但这条测试应该在断言前加 `require.False(t, v.Timeout)`,或者在 `useTestConfig` 里把 `scan_timeout_ms` 调大。

---

## 汇总

| 缺陷 | 代码判定 | 回归测试判定 |
|---|---|---|
| A1 提现 hold 被翻转 | 真堵住了(a/b/c 三点全中) | 真的,两条都直击生产函数 |
| A2 违规退款退错池 | 真堵住了,三个池 + 令牌逐项对上 | **半真**:退款路由有真测试;**冻结 `BillingSource` 那两行零覆盖**,删掉全绿 |
| A3 出队后静默丢弃 | 真堵住了 | 真的 |
| A4 游标越过失败订单 | 真堵住了 | 真的(调度接线未覆盖,轻微) |
| A5 carry 发不出去 | 真堵住了(SQL 与早退**两半都改了**) | **假的 —— 已实测:两半同时回滚,全部测试仍通过** |

没有发现"只改了一半"的代码,也没有发现"改错了"引入新资损。唯一的实质问题在测试层:A5 的两处调度改动和 A2 的冻结改动没有任何东西守着,而这三处恰好就是原审计总结的"调度层 / 收尾层断链"的落点。
