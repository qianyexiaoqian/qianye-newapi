验证完毕（`go build ./...` 通过，全部条目均已打开源码逐行核对）。

# 一、确认缺陷（去重后，按影响排序）

---

## A 级 — 资损

### A1. 提现对账任务会推翻 `holdForReview` 的人工裁决，把已到账的佣金退回可用池
`qianye/modules/withdraw/reconcile.go:76` + `reconcile.go:117-120`（配套 `credit.go:66-71`、`credit.go:235-243`）
（合并 crossdb#1、idem#1）

**一句话**：`holdForReview` 只写 `reconcile_state='hold'` 不改 status，而 `settlePaying` 的扫描条件里没有排除 hold，60 秒后同一张单被重新裁决并走 `failWithdrawal` 退款。

**复现**（单节点，无需崩溃）：
1. quota 方式提现单 W 审核通过 → `creditQuota` → `startPaying` 置 `paying`、`settle_started_at=T`。
2. 主库事务服务端 commit 成功（`qy_fund_outbox` 有行、`users.quota += N`），客户端在收到 OK 前连接断开，GORM 返回 error。
3. `twophase.Execute` → `markFailed`（`execute.go:262`）→ 资金单 `failed`。
4. `credit.go:66-70`：`order.Status==Failed` → `mainSideApplied()` 探到 outbox → true → `holdForReview`。W 仍是 `paying` + `reconcile_state=hold`。
5. 下一轮 `reconcile`（`interval = max(1min, 2×30s)`）：`settlePaying` 的 `Where("status = ? AND settle_started_at > 0 AND settle_started_at < ?", StatusPaying, stale)` 命中 W → `settleOnePaying` 读到资金单 `Failed` → 直接 `failWithdrawal` → `applyTransition(paying→failed)` CAS 成功 → `commission.UnfreezeForWithdraw` → `available_quota += N`。
6. 用户主库额度 +N，佣金 N 也回到可用池，可再提一次。

**为什么现有防护挡不住**：三道防线全部失效。①`holdForReview` 的 CAS 只挡"重复打 hold"，不阻止别的路径改 status；②`failWithdrawal` 的 `applyTransition` 条件是 `From: StatusPaying`，而 hold 单确实还是 paying，CAS 必然成功；③`reconcile.go:118` 的注释"资金单被判失败意味着补偿任务已通过 outbox 探针确认主库未生效"是**错的**——`twophase.markFailed` 在业务线程 commit 报错时也会置 failed，那条路径从未探过针。

**对照证据**：划转模块 `qianye/modules/transfer/reconcile.go:123-128` 在同一分支**显式再探一次** `mainSideApplied(orderNo)`，为真就 `markUncertainAfterConflict`。提现缺这一步，是遗漏不是取舍。

**修复**：
```go
// reconcile.go:75-77
Where("status = ? AND reconcile_state <> ? AND settle_started_at > 0 AND settle_started_at < ?",
    StatusPaying, ReconcileHold, stale)

// reconcile.go:117 —— 照抄 transfer 的写法
case qymodel.StatusFailed:
    if mainSideApplied(w.OrderNo) {
        holdForReview(w, "资金单被判失败但主库探针显示已生效,需人工核对")
        return
    }
    failWithdrawal(w, errors.New(fallbackReason(order.LastError)))
```

---

### A2. 违规扣费退款只还钱包，不还令牌额度、不还订阅池
`qianye/modules/violation/api_admin.go:474-497`（`refundFee`）vs `qianye/modules/violation/fee.go:180`

**一句话**：扣费经 `service.PostConsumeQuota` 同时动了 `users.quota`（或订阅池）**和** `tokens.remain_quota`；退款只 `users.quota + amount`。

**复现**：
1. 用户建一个非无限额度令牌 `remain_quota=1000000`。
2. 该令牌请求命中 `block_and_charge` 规则，`computeFee` 得 `Charged=500000`。
3. `chargeFee` → `service.PostConsumeQuota(info, 500000, 0, true)`：`service/quota.go:425-435` 扣 `users.quota`；`service/quota.go:437-446` 因 `!IsPlayground` 走 `model.DecreaseTokenQuota` → `model/token.go:431-440` 无条件 `remain_quota - 500000`、`used_quota + 500000`。
4. 管理员判误报，`POST /admin/violation/records/:id/revoke {"refund":true}`。
5. `refundFee` 的 `MainApply` 只有一条 `Update("quota", gorm.Expr("quota + ?", amount))`。令牌永久少 500000 可用额度。

订阅用户更严重：`service/quota.go:414-424` 在 `BillingSource==subscription` 时扣的是 `UserSubscription.AmountUsed`，退款一律加回钱包 → 订阅池消耗从未归还，钱包凭空多出等额额度。

**为什么现有防护挡不住**：`refundFee` 的注释只关注"跨库中间态"，两阶段 + outbox 探针保证的是"这一笔加款执行且只执行一次"，完全不覆盖"加款的账户对不对"。`qy_violation_record` 里也没有冻结 `token_id`/`billing_source`，事后连补都补不准。

**修复**：`Record` 增列 `token_id`、`token_key`、`billing_source`（扣费时从 `info` 冻结进去）；`refundFee` 的 `AfterCommit` 里补 `model.IncreaseTokenQuota(rec.TokenId, rec.TokenKey, amount)`；`MainApply` 按冻结的 `billing_source` 分支决定回冲钱包还是 `UserSubscription.AmountUsed`；同时 `model.UpdateUserUsedQuotaAndRequestCount(rec.UserId, -amount)` 回冲 `used_quota`。

---

### A3. 扩展库短暂不健康时，已入队的热路径作业被静默丢弃（无计数、无日志）
`qianye/guard/guard.go:96`（`Hot` 的 `if !Available() { return }`）+ `guard.go:174`（worker 复用 `Hot`）+ `qianye/db/health.go:49-54`

**一句话**：`HotAsync` 入队时判一次 `Available()`，worker 出队再判一次；两次之间状态翻转，作业直接消失，且 `dropped` 计数器只在队列满分支自增。

**复现**：
1. relay 正常跑，队列积压 3000 条 `commission.consume`（4096 容量，未到 80% 水位）。
2. `db/health.go:49` 的 `probe()` 一次 `PingContext` 3 秒超时 → `health.go:52 healthy.Store(false)`。**这不经过熔断阈值，单次 ping 失败即置位**。
3. 直到下一次 probe 成功（默认 `health_interval_seconds=15`），2 个 worker 每取一条就在 `guard.go:96` `return`，3000 条事件全部消失。
4. `accrueConsume` 从未执行 → `qy_commission_accrual` 无记录。`consumeEvent` 只存在于内存闭包，**没有任何补偿路径**：`repairStrandedAccruals` 只处理 `settled→accrued`，`runTopupScan` 只覆盖充值，消费返佣没有 outbox。
5. 管理端 `hot_queue.dropped` 仍是 0，日志一条没有。

同一窗口 `violation.persist` 也这样消失（`recordDrops` 只在 `guard.go:201` 的入队前检查里自增）。

**为什么现有防护挡不住**：`guard.go:152` 的注释"丢弃是'用户该拿的钱没拿到'的唯一路径,必须显式告警"只覆盖了 `select default` 分支。既无背压（队列没满）也无告警，触发条件是一次 3 秒 ping 超时。

**修复**：把 `Hot` 拆成 `hotRun(name, fn)`（只做 panic 拦截 + 超时 + 错误处理，不判可用性），`startWorkers` 的循环改调 `hotRun`；若确实要跳过，必须加与 `dropped` 同级的 `skipped atomic.Int64` + 限频告警。

---

### A4. 充值返佣扫描：单笔计佣失败后游标照样前进，该笔佣金永久丢失
`qianye/modules/commission/topup_scan.go:81`（`next := maxScanned`）+ `topup_scan.go:122`

**一句话**：低水位只由 pending 订单决定，`accrueTopUp` 的失败只 `warnf` 不影响游标推进，而 `WHERE id > low` 保证它再也不会被扫到。

**复现**：
1. 扩展库出现一次非连接级错误（`Error 1213 Deadlock` / `Lock wait timeout exceeded`——结算事务并发持 `qy_commission_balance` 行锁时很常见；`db/db.go:230` 的 `isConnLevelError` 不认这两类，熔断不开，扫描继续跑）。
2. 本批含成功订单 id=1000（trade_no T1）至 1050，窗口内无 pending。
3. `accrueOneShot` 对 1000 返回 error → `topup_scan.go:122` 只打 `warnf("...(下轮重扫会重试)")`；`writeAccrual` 什么都没写。
4. `next = maxScanned = 1050` → `saveTopupCursor(1050)`。
5. T1 的邀请人永远拿不到这笔返佣。

**为什么现有防护挡不住**：`topupIdemKey(t.TradeNo)` 的唯一索引只防重复，不防遗漏。`topup_scan.go:122` 的注释"下轮重扫会重试"与实际行为**相反**。管理端 `/commission/health` 也看不出来。

**修复**：循环里记录 `minFailed`（首个计佣失败的 id），`next = min(maxScanned, minPending-1, minFailed-1)`：
```go
case common.TopUpStatusSuccess:
    if err := accrueTopUp(r); err != nil && (minFailed == 0 || id < minFailed) {
        minFailed = id
    }
```
（`accrueTopUp` 需要改成返回 error。）

---

### A5. 被日封顶／结算门槛削掉的 carry 永远发不出去
`qianye/modules/commission/settle.go:111-126`（`pendingInviters`）+ `settle.go:172`

**一句话**：结算调度的唯一入口只按"还有未被吸收的 accrual 行"选人，carry 不在选人条件里；而 `absorbAccruals` 会把本批**全部** accrual 的 `settled_amount` 写成 `gross_amount`。

**复现**：
1. 管理员设 `max_daily_quota_per_inviter = 1000`（`settings.go:28`）。
2. 邀请人 A 有已成熟计佣行合计 5000。
3. 第一轮：`computeSettlement`（`settle.go:61-64`）得 `net=1000`、`Clipped=4000`、`CarryAfter=4000`；`absorbAccruals`（`settle.go:329-336`）把整批 `settled_amount = gross_amount`。
4. 第二轮：`pendingInviters` 的 `WHERE ... settled_amount <> gross_amount` 对 A 不再命中；管理端 `POST /commission/settle?user_id=A` 走 `settleUser`，`len(rows)==0` 在 `settle.go:172` 直接 `return nil`。
5. 4000 佣金停在 `unsettled_amount`，只有 A 名下再产生新计佣行才会顺带发出。下线停止消费 = 永久拿不到。

同一根因覆盖：`net < minSettle`（默认 1000）全额进 carry；`computeSettlement` 触顶 int32 时超出部分进 carry。`repairStrandedAccruals` 只处理 `settled_amount <> gross_amount`，覆盖不到 carry。

**为什么现有防护挡不住**：`TestComputeSettlementDailyCap` 注释写"明天继续发,不是作废"，但纯函数是对的、调度层没接上。**测试覆盖了算术，没覆盖调度**。

**修复**：`pendingInviters` 并上一路来源：
```sql
SELECT user_id FROM qy_commission_balance
 WHERE unsettled_amount >= 1 AND debt_blocked = 0
```
并且 `settleUser` 在 `len(rows)==0` 时不早退，带 `delta=0` 走一遍 `computeSettlement` 把 carry 刷出去。

---

## B 级 — 数据错误 / 审计完整性

### B1. 违规扣费退款走了两阶段但没注册 Resolver，补偿成功后业务状态永久错误，且重入自愈被堵死
`qianye/modules/violation/api_admin.go:427-429`（`RowsAffected==0` 幂等返回）+ `api_admin.go:444-452` + `qianye/service/twophase/compensate.go:97-103`

**复现**：
1. 管理员点"撤销+退款"。`revokeRecord` 的 `WHERE id=? AND status IN ('active','appealed')` CAS 成功，记录变 `revoked`，`fee_status` 仍是 `charged`。
2. `refundFee` → `twophase.Execute` → 主库事务提交成功（outbox 行落地）。
3. 进程在 `applyOnMainDB` 返回与 `markSuccess` 之间被 kill（滚动发布 / OOM）。资金单停 `pending`。
4. 重启后 `Compensate` 探到 outbox → `resolveApplied`（`compensate.go:98`）→ `resolverRegistry["violation_fee"]` 不存在（全仓只有 `withdraw/module.go:30`、`transfer/module.go:26` 注册）→ 直接把资金单标 `Success`。
5. `qy_violation_record.fee_status` 永远停在 `charged`、`refund_quota=0`。
6. 管理员再点一次：`revokeRecord` 的 CAS `RowsAffected==0` → `return 0, nil`，`refundFee` 根本不会被调用。**自愈路径被堵死**。
7. 用户端 `api_user.go` 的 `SUM(fee_quota)` 仍显示罚款在被收取 → 运营走人工补单 → 同一笔退两次。

**为什么现有防护挡不住**：`refundFee` 也没有 `LocalCommit`，`violation/tasks.go` 只有证据 GC 与封禁补偿，没有任何对账任务。

**修复**：`violation` 的 `InstallHooks()` 里
```go
twophase.RegisterResolver(qymodel.KindViolationFee, func(ctx context.Context, o *qymodel.FundOrder) error {
    return db.Get().WithContext(ctx).Model(&Record{}).
        Where("rec_no = ? AND fee_status IN ?", o.IdemKey, []string{FeeStatusCharged, FeeStatusTruncated}).
        Updates(map[string]any{"fee_status": FeeStatusRefunded, "refund_quota": o.AmountQuota}).Error
})
```
并给 `refundFee` 补同语义的 `LocalCommit`，让正常路径的回写与资金单回写同事务。

---

### B2. 违规扣费的"余额判定池"与"实际扣款池"不一致 → 订阅用户首次违规即被误封号
`qianye/modules/violation/fee.go:166-170`（读 `model.GetUserQuota`）vs `service/quota.go:414-424`（按 `BillingSource` 路由）

**复现**：
1. 用户 U 开通订阅（`AmountTotal` 充足），钱包 `users.quota = 0`（订阅用户的典型状态）。
2. `PostRelayGuard`（`controller/relay.go:182`，在 defer 里）触发，此时 `PreConsumeBilling` 已把 `relayInfo.BillingSource` 置为 `subscription`（`service/billing_session.go:321`）。
3. `chargeFee` 读到 `available = 0`：
   - 默认 `clamp`（`fee.go:139-142`）：`Charged=0`、`FeeStatus=insufficient`。订阅用户**永远罚不到款**，且记录显示"余额不足"误导管理员。
   - 配 `ban`（`fee.go:129-135`）：`available(0) < Want` → `ForceBanWeight=true` → `guard.go:187-189` 把 `weight` 顶成 `AutoBanThreshold` → **首次违规即自动封号**，尽管订阅池里有钱。
4. 反向：钱包 100000、订阅只剩 100 → 判定通过 → `PostConsumeUserSubscriptionDelta` 返回错误 → `FeeStatus=failed`，罚款丢失。

**为什么现有防护挡不住**：`PreRelayGuard` 路径下 `BillingSource` 还没被赋值（`relay.go:157` 早于 `PreConsumeBilling`），走钱包分支，一致；**只有 post 阶段不一致**，所以单看 prompt 阶段的测试永远发现不了。

**修复**：`chargeFee` 里按 `info.BillingSource` 取对应池：
```go
available := int64(0)
if info.BillingSource == service.BillingSourceSubscription && info.SubscriptionId > 0 {
    if sub, err := model.GetUserSubscriptionById(info.SubscriptionId); err == nil {
        available = sub.AmountTotal - sub.AmountUsed
    }
} else if q, err := model.GetUserQuota(info.UserId, false); err == nil {
    available = int64(q)
}
```
或者显式构造 `BillingSource=wallet` 的 relayInfo 副本传给 `PostConsumeQuota`。两者选一，但判定池与扣款池必须一致。

---

### B3. 违规计数"跨越阈值"的信号被消费掉却不落痕迹，封号在整个滚动窗口内永久丢失
`qianye/modules/violation/counter.go:186`（速率闸 `return false`）、`counter.go:173-178`（影子模式）、`counter.go:189-191`（claimBan 出错）

**复现（速率闸，确定性）**：
1. `auto_ban_threshold=5`、`global_ban_rate_limit_per_hour=10`、`shadow_mode=false`。
2. 一小时内已有 10 个用户被自动封禁，`banWinCount==10`。
3. 用户 X 第 5 次违规：`bumpCounter` 提交，`hit_count=5`，`crossedThreshold(5,1,5)` → true。
4. `maybeAutoBan` → `banRateExceeded()`（`breaker.go:127`）→ 打日志 → `return false`。**没有插入 `qy_violation_ban`**。同时 `breaker.go:128` 的 `tripShadow` 让 `forcedShadowUntil = now+1800`。
5. 用户 X 第 6 次违规：`crossedThreshold(6,1,5)` = `6>=5 && 5<5` → **false**。此后在 24 小时窗口内（`auto_ban_window_hours` 默认 24）`Crossed` 再也不为真。
6. X 可以无限违规不被封；步骤 4 的 30 分钟强制影子模式让**这段时间内所有跨阈值的用户**同样消耗掉各自的信号。

**并发/超时路径（同根因）**：`guard.go:230` `bumpCounter` 事务提交成功（`Crossed=true`），`guard.go:234` 的 `Updates` 撞上 200ms deadline → `persist` 的 fn 直接 `return err`，`maybeAutoBan` 根本没被调用。

**为什么现有防护挡不住**：`runBanCompensate`（`tasks.go:90-97`）只扫 `qy_violation_ban` 里已存在的 `pending/failed` 行，对"从未认领成功"的跨越完全无能为力。`counter.go:180-181` 的注释"速率闸在认领之前:超限时连认领都不做,这样恢复后仍能正常触发"与实现不符。

**修复**：把"跨越"变成持久化事实：`bumpCounter` 判定 `Crossed` 之后**无条件先 `claimBan` 落一行 `{status: pending}`**，再由速率闸/影子模式/执行失败决定标成 `deferred`/`skipped`/`banned`；`runBanCompensate` 的扫描集合加 `BanDeferred`。最小修复：`crossedThreshold` 改成"跨越 ∨（已达阈值 ∧ 本 cycle 无 ban 行）"。

---

### B4. 幂等命中不校验请求指纹：同一 `client_request_id` 换金额/换收款人可刷出任意金额的"划转成功"审计
`qianye/modules/transfer/service.go:109`（`writeCreateAudit(c, order, acc)`）+ `service.go:321-334`，根因 `qianye/service/twophase/execute.go:179-183`

**复现**：
1. 用户 5 提交 `{to_user_id:7, amount:100, client_request_id:"abc", confirm:true}` → 成功，`qy_fund_orders` 落 `(transfer, "5:abc")`。
2. 同一用户再提交 `{to_user_id:9, amount:50000000, client_request_id:"abc", confirm:true}`。
3. `createOrLoadOrder` 撞 `uk_qy_fund_idem` → 加载原单 → `resolveExisting` 命中 `StatusSuccess` → `return order, nil`，**没有比对 amount/fee/to_user_id**。
4. `execErr == nil` → `writeCreateAudit` 用的是**本次请求的** `acc.Amount=50000000`、`acc.ToUserId=9`，而 `TraceNo` 是**原单的真实单号**、`Result=ResultOK`。
5. `qy_audit_logs` 多出一条 `transfer.create` / `ok` / `amount_quota=50000000` / `target_user_id=9` / `trace_no=<第一笔真实单号>`。资金侧毫无痕迹。

**为什么现有防护挡不住**：`buildIdemKey`（`validate.go:130-146`）只校验字符集与长度，`client_request_id` 完全由前端生成；唯一索引只保证"不重复执行"，不保证"重放的是同一个请求"。响应体因为 `buildCreateResponse` 回读原单是正确的，所以**只有审计表被污染**——而审计表是这套资金系统事后仲裁的唯一凭据。

**修复**：`twophase.Request` 加 `Fingerprint string`（`sha256(kind|userId|peerUserId|amount|fee|refId)`），落 `qy_fund_orders` 新增列；`resolveExisting` 命中时比对指纹，不一致返回 409 `qy_idem_key_conflict`，调用方不写审计、不返回 200。`withdraw.loadByIdemKey` 同样比对 `quota`/`method`。

---

### B5. 管理端人工冲正：同一 `client_request_id` 换 `accrual_id`/`quota` 重放，返回旧单并写下金额虚高的成功审计
`qianye/modules/commission/clawback.go:160-164` + `qianye/modules/commission/api_admin.go:206-218`

**复现**：
1. 管理员 A（id=3）POST `/commission/clawback` `{accrual_id:100, quota:500, client_request_id:"x"}` → 生成负额行 CA-1（-500）。
2. 同一弹窗改金额重提（前端按裁定 C10 在打开弹窗时生成并缓存 `client_request_id`，重试沿用同一个）：`{accrual_id:200, quota:9999, client_request_id:"x"}`。
3. `writeAccrual` 的 `clause.OnConflict{DoNothing:true}`（`accrual.go:186-189`）冲突不报错；`clawback.go:161-165` 按同一个键回读，拿到 **CA-1** 并当作"本次新建"返回。
4. `api_admin.go:206-218` 写审计：`TraceNo=CA-1`、`AmountQuota=req.Quota=9999`、`Result=ok`。

**为什么现有防护挡不住**：`writeAccrual` 不返回"本次是否真的插入"，`clawbackCreated.Add(1)`（`clawback.go:158`）在 no-op 时也自增。响应体的 `gross_amount` 是 `-500`（真值），但审计表记的是 9999。

**修复**：`writeAccrual` 返回 `(inserted bool, err error)`（`res.RowsAffected == 1`）；`manualClawback` 在幂等命中且 `origin.Id != accrualId` 或金额不等时返回 409；`adminClawback` 的审计一律用回读行的 `created.GrossAmount` 而不是 `req.Quota`。

---

### B6. 审计写入按字节截断会切断 UTF-8 字符，导致整条审计行被数据库拒绝
`qianye/service/audit/audit.go:113-122`

```go
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max { return s }
	const mark = "...[truncated]"
	if max <= len(mark) { return s[:max] }
	return s[:max-len(mark)] + mark   // 裸字节切,不看 rune 边界
}
```

**复现**：管理员在提现审核页填中英混排拒绝理由，≤200 rune（通过 `review.go:114` 的 `checkRunes(rawReason, maxReasonRunes=200)`），但 UTF-8 字节 > 512 → `writeDecisionAudit`（`review.go:284`）→ `build()` 调 `truncate(reason, 512)` → `s[:498]`。第 498 字节不是 rune 起始位（混排文本下概率约 2/3）→ 尾部残缺的非法 UTF-8 → 扩展库 DSN 强制 `charset=utf8mb4`（`db/db.go:211`），MySQL `STRICT_TRANS_TABLES` 下报 1366 → `audit.Write`（`audit.go:50-53`）只 `SysError` 一行，**"谁在什么时候拒了这笔提现、理由是什么"的审计记录彻底丢失**，而 `qy_audit_logs.reason` 是 `varchar(512)`（按字符计，`model/audit_log.go:38`），原文本来完全放得下。

同路径覆盖 `withdraw.fail`、`withdraw.resolve.*`（同 200 rune 上限）与 `qianye/controller/admin.go:180` 的 `fund.resolve`（理由无长度上限）。

**为什么现有防护挡不住**：同仓其他三处截断都是安全的——`transfer/service.go` 用 `utf8.RuneStart` 回退、`withdraw/mask.go` 按 rune 计数、`violation/rules.go` 的 `safeCut` 也回退。**只有 audit 这一处漏了**。另外 `audit.WriteTx` 全仓零调用，所以不会连带回滚事务（唯一的好消息）。

**修复**：改成 rune 安全：
```go
func truncate(s string, max int) string {
    if max <= 0 || len(s) <= max { return s }
    const mark = "...[truncated]"
    if max <= len(mark) { return s[:safeCut(s, max)] }
    cut := max - len(mark)
    for cut > 0 && !utf8.RuneStart(s[cut]) { cut-- }
    return s[:cut] + mark
}
```
顺带把 `qianye/service/twophase/execute.go:258-261` 与 `compensate.go:171-173`、`compensate.go:188-190` 的 `msg[:512]` 一并改掉——同一类裸字节切，落的也是 `varchar(512)`。

---

### B7. `markFailed` 不校验 `RowsAffected`，在补偿任务已推进单据后伪造终态与审计
`qianye/service/twophase/execute.go:262-277`

**复现**：
1. 线程 A（`transfer.create` / `withdraw.creditQuota`）进入 `applyOnMainDB`，主库事务因锁等待长时间不返回。
2. 线程 B（`Compensate`）`attempts` 达到 `MaxProbeAttempts` → `backoff` → `markUncertain` → 资金单 `Uncertain`。这是系统里"我不知道钱动没动,禁止自动回滚"的唯一出口。
3. 线程 A 最终以确定性错误返回。`markFailed` 的 UPDATE（`WHERE order_no=? AND status=StatusPending`）命中 0 行，但 `err == nil` → **内存里** `order.Status = StatusFailed`，并写一条 `ResultFail` 审计。
4. 调用方按内存里的 `Failed` 走回滚分支：`transfer.releaseOnFailure` 清 `risk_held`、`withdraw.creditQuota` 走 `failWithdrawal` 解冻佣金。
5. 最终：`qy_fund_orders` 是 `Uncertain`（等人工），业务明细已 `failed` 且预占/冻结已释放，审计日志声称转成了 `failed`。三份互相矛盾，而 `Uncertain` 单不会再被 `Compensate` 扫（它只查 `status = pending`），矛盾永不自愈。

**为什么现有防护挡不住**：`markSuccess`（`execute.go:243-245`）正确检查了 `RowsAffected == 0` 并让出，`markFailed` 少了同样的检查——同一个文件里的不对称，属遗漏。

**修复**：
```go
res := db.Get().Model(&qymodel.FundOrder{}).
    Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
    Updates(...)
if res.Error != nil { ...; return }
if res.RowsAffected == 0 {
    // 别的路径已推进,回读真实状态,不改内存、不写审计
    var cur qymodel.FundOrder
    if e := db.Get().Where("order_no = ?", order.OrderNo).Take(&cur).Error; e == nil {
        order.Status = cur.Status
    }
    common.SysError(...)
    return
}
```
`markSuccess` 的 `RowsAffected == 0` 分支同样应区分"对方推成 Success"与"推成 Failed/Uncertain"，后者不该让 `Execute` 返回 nil 并写 `ResultOK`。

---

### B8. 密钥轮换是空承诺：换 `pii_key` 会让全部历史收款信息永久不可解密
`qianye/modules/withdraw/crypto.go:128-138`（`piiKey()`）

```go
func piiKey() ([]byte, error) {
	raw := strings.TrimSpace(config.Get().Withdraw.PIIKey)  // 永远只读"当前"这一把
```

`sealPayee`（`crypto.go:31`）/ `openPayee`（`crypto.go:60`）都无条件调 `piiKey()`，**从不读密文行上的 `KeyVersion`**；`config.Withdraw` 也只有单个 `PIIKey string` + `PIIKeyVersion int`（`config/config.go:169-175`）。`KeyVersion` 的全部用途只是写进去（`payee.go:70`、`payee.go:165`），从没被读出来选钥。

而 `model.go:116` 明确承诺"KeyVersion 支持密钥轮换:解密时按版本选密钥,旧密文不会因为换钥而全部作废"，`crypto.go:21` 也写"pii_key 用于 AES-256-GCM 加解密,可以轮换"。

**复现**：运维按注释轮换 `withdraw.pii_key`、把 `pii_key_version` 从 1 改成 2、重启。此后 `payee.go:142` `resolvePayee` 对所有已保存收款方式解密失败（`errPayeeUndecryptable`）；`api_admin.go:294` `handleAdminRevealPayee` 对**队列里全部待打款单**解密失败——管理员拿不到收款账号，这些单打不了款，佣金又已在 `frozen` 里。

**修复**：二选一，不留中间态。(a) 真做轮换：配置改 `pii_keys: {1: "...", 2: "..."}` + `pii_key_active_version`，`openPayee(nonce, ct, aad, keyVersion)` 按行上的版本选钥；(b) 明确不支持：删掉 `pii_key_version` 与 `KeyVersion` 列，把 `model.go:116`、`crypto.go:21` 的注释改成"密钥一经启用不得更换"。

---

## C 级 — 风控失效 / 拒绝服务

### C1. 提现的四项额度风控配置从未被任何代码读取
`qianye/modules/withdraw/validate.go:108-113`（`acceptCreate`）、`qianye/modules/withdraw/create.go:65-92`
（合并 arith#4、crossdb#2）

全仓 grep `MaxQuotaPerOrder|DailyMaxQuota|MaxPendingOrders|CooldownSecs` 在 `qianye/modules/withdraw/` 下**零命中**（只有 `config/config.go:176-184`、`config/defaults.go:75-78`、`config/validate.go:203-209`，以及 transfer 模块的同名字段）。

`acceptCreate` 只校验 `0 < Quota <= common.MaxQuota` 与 `>= cfg.MinQuota`；`create` 的事务里只有 `checkDailyCount`（笔数）。

**复现**：某邀请人 `available_quota = 2,000,000,000`（扩展库 int64，长期累积）。`POST /api/qy/withdraw {"method":"quota","quota":2000000000,...}` → `acceptCreate` 放行（2e9 < MaxInt32）→ `checkDailyCount` 放行 → `FreezeForWithdraw` 只判 `available_quota >= quota` 放行。一张 20 亿的提现单落库，`max_quota_per_order: 500000000` 是声明上界的 4 倍。同理 60 秒内可连发 3 单（`cooldown_seconds` 无效），未终态单不受限（撤销单还不计入 `checkDailyCount`，可无限循环申请-撤销淹掉审核队列）。

**为什么现有防护挡不住**：配置被 `validate()` 校验（`config/validate.go:203-209` 甚至专门校验了 `max_quota_per_order` 与 `min_quota` 的一致性）、被 `applyDefaults()` 赋默认值，**运维完全无法察觉它们是空的**。`config.go:181-184` 的注释原文正是"不设的话单笔上界只有主库 int32 容量,一次异常申请就会占满整个佣金池"。因为申请即冻结，不构成直接超发。

**修复**：`acceptCreate` 加 `cfg.MaxQuotaPerOrder > 0 && req.Quota > cfg.MaxQuotaPerOrder → errAmountOutOfRange`；`create` 的**同一个事务内**（与 `checkDailyCount` 并列，否则和它一样有 TOCTOU）补：当日 `SUM(quota)` 校验、未终态单计数、`MAX(created_at)` 冷却校验。另在 `handleGetConfig` 下发这几个上限。

---

### C2. `transfer.receiver_daily_max_in_count` 声明的洗号闸门从未实现
`qianye/modules/transfer/risk.go:61-77`（`evaluateRisk`）

`evaluateRisk` 只判发起方四项（`PendingCount`/冷却/日笔数/日额度），签名里根本没有 receiver。`UserState.DayInCount` 在 `applyReservation`（`risk.go:107`）老老实实累加，但没有任何地方读它做判定；全仓对 `ReceiverDailyMaxInCount` 的引用只在 `config/config.go:117-120` 与 `config/defaults.go:46`。

**复现**：准备 200 个过了 `new_account_freeze_hours` 的账号，各向同一汇集账号 R 转 1 笔。每笔只受发起方限额约束，全部通过。R 的 `day_in_count` 涨到 200，远超配置的 50。

**修复**：`reserveRisk` 已经在同一事务内按 user id 升序锁住了 receiver 行（`risk.go:31-36`），把判定插在 `applyReservation` 之前即可，无需额外加锁：
```go
if err := evaluateRisk(*sender, cfg, req.Total, now); err != nil { return err }
if cfg.ReceiverDailyMaxInCount > 0 && receiver.DayInCount+1 > cfg.ReceiverDailyMaxInCount {
    return errReceiverDailyInExceeded
}
```

---

### C3. `guard.Hot` 承诺的 200ms 超时对返佣/封号链路完全无效；高水位同步降级把无界 IO 搬回 relay 线程
`qianye/guard/guard.go:105-108`（造 ctx）+ `guard.go:141-145`（高水位同步降级）；受害路径 `qianye/modules/commission/hook.go:56/147/169/179`、`commission/accrual.go:205/266/299`、`commission/inviter.go:89`、`violation/ban.go:47/80`
（合并 arith#6、race#3、idem#4、upstream#2）

**缺陷**：`Hot` 文档保证"③ 在 hot_path_timeout_ms 的 ctx 下执行,超时即放弃"。但：
- 返佣的四个闭包**直接丢弃 ctx**：`func(ctx context.Context) error { return accrueConsume(ev) }`——`accrueConsume`/`accrueOneShot`/`clawback`/`resolveInviter` 根本没有 ctx 形参，下游全是裸 `gdb.Clauses(...).Create(...)`、`model.DB.Model(...)`。
- `disableUserForViolation`（`ban.go:47`）全程不带 ctx：`model.DB.Transaction`、`PublishUserAuthCache`、`InvalidateUserTokensCache`、`RevokeAllUserSessions`（后者是**无上限的 for 批循环**，每批一次 Find + 逐行 Redis 写 + 一个带 `FOR UPDATE` 的主库事务）、`RecordLogWithAdminInfo`。
- `db/db.go:200-215` 的 `normalizeDSN` 只补 `parseTime`/`charset`，**不设 `readTimeout`/`writeTimeout`**；`connect_timeout_seconds` 只用于 `db.Init` 的那一次 `PingContext`。

对照 `violation/guard.go:213`、`counter.go:55` 用的是 `gdb.WithContext(ctx)`，说明这不是统一约定，是漏了。

**复现**：
1. `commission.enabled=true`，`hot_hook_workers` 默认 2、队列 4096。
2. 后台结算任务 `settle.go:290` 对 `Balance` 行加 `FOR UPDATE`、`settle.go:329` 更新 accrual 行，事务期间持锁。
3. 同一时刻某条 relay 的消费日志触发 `commission.consume` → `writeAccrual` 对同一日聚合行执行 `INSERT ... ON DUPLICATE KEY UPDATE`，阻塞在行锁上。因为没有 `WithContext(ctx)` 也没有 DSN 级 `readTimeout`，这条语句一直等到 `innodb_lock_wait_timeout`（默认 **50 秒**）。
4. `probe()` 走另一条连接、Ping 正常，`Available()` 保持 true。两个 worker 被占满 50 秒 → 队列几秒内越过 3277（4096×80%）。
5. 此后**每一个** relay goroutine 走 `guard.go:144` 的高水位分支 → `Hot(name, fn)` **同步执行在 relay 结算线程上**（`model.RecordConsumeLog` 在 `PostTextConsumeQuota` 的同步链路上，`model/log.go:344`）。200ms 保护形同虚设。
6. 若此时恰好有用户跨过封号阈值，这条 relay 请求还要在自己的 goroutine 上跑完 `disableUserForViolation` 的全流程（主库事务 + Redis publish + 遍历全部会话逐批撤销 + 写主库日志），阻塞时长没有上界。

**为什么现有防护挡不住**：熔断认不出这种状态——`db.MarkFailure` 的 `isConnLevelError` 只认连接类错误，锁等待/慢查询不算；`healthy` 因 Ping 成功保持 true。

**修复**：
(a) `accrueConsume`/`accrueOneShot`/`clawback`/`resolveInviter`/`ensureRelation`/`blockedInvitees` 全部接收 ctx，所有 GORM 调用加 `WithContext(ctx)`（照抄 `violation/counter.go:55`）；
(b) `normalizeDSN` 追加 `timeout=<connect_timeout>s&readTimeout=<hot上限×2>&writeTimeout=...`，让驱动层有硬上界；
(c) `disableUserForViolation` 接 ctx，并把 `RevokeAllUserSessions` 移出封号同步链（标记后由 `runBanCompensate` 补做）；
(d) `HotAsync` 加 `NoSyncFallback` 语义（或按 name 白名单），让含跨库副作用的 job 在高水位时宁可丢弃/降级，也不进同步分支。

---

### C4. 熔断器在"扩展库可达但查询变慢"这一唯一重要场景下永远打不开
`qianye/guard/guard.go:113`（`db.MarkSuccess()`）+ `qianye/db/health.go:63`（`failStreak.Store(0)`）+ `qianye/db/db.go:130`

**缺陷**：`MarkFailure` 要求**连续** N 次（默认 5）失败才开熔断，但两条与"扩展库查询是否健康"无关的路径会把 `failStreak` 清零：
- `guard.Hot` 对任何返回 nil 的 hook 都调 `db.MarkSuccess()`——包括**纯内存 hook**。已核实 `availability.sample` 的闭包是 `func(ctx) error { observe(s); return nil }`（`modules/availability/sample.go:52-55`），`observe`（`aggregate.go:87-94`）注释明写"纯内存 O(1),无锁无 IO"，**必然返回 nil**。
- `probe()` 每 15 秒 Ping 成功就 `failStreak.Store(0)`。

**复现**：`availability.enabled: true` + `violation.enabled: true`，扩展 MySQL 处于"TCP 可达、写入慢"状态。`violation.persist` 超时 → `failStreak = 1`；队列里紧接着的 `availability.sample` → `MarkSuccess()` → `failStreak = 0`。availability 采样频率（每次 relay 一条）远高于失败频率，`failStreak` 永远回不到 5 → `openUntil` 从不设置 → `Available()` 恒为 true → 直通 C3 的高水位同步降级。

**修复**：`Hot` 加"本次是否访问了 DB"的信号（或改由各 hook 显式调 `MarkSuccess`）；`probe()` 改成只在 `healthy` 由 false 翻回 true 时才重置 `failStreak`。更彻底：熔断判据从"连续失败数"改为窗口失败率。

---

### C5. `QyPruneFundOutbox` 的 `Limit` 在 PostgreSQL / SQLite 主库上被 GORM 静默忽略
`model/qy_export.go:115-118`，调用方 `qianye/service/twophase/compensate.go:246`，任务注册 `qianye/bootstrap.go:103`（6 小时），批量 `qianye/config/defaults.go:32`（200）

```go
res := DB.Where("created_at < ?", before).Limit(batch).Delete(&QyFundOutbox{})
```

**已核对依赖源码**：
- `gorm.io/driver/mysql@v1.4.3/mysql.go:52` — `DeleteClauses = []string{"DELETE","FROM","WHERE","ORDER BY","LIMIT"}`
- `gorm.io/driver/postgres@v1.5.2/postgres.go:50` — `{"DELETE","FROM","WHERE"}`
- `github.com/glebarez/sqlite@v1.9.0/sqlite.go:62` — `{"DELETE","FROM","WHERE","RETURNING"}`

**触发**：
- **主库 PG/SQLite**：`qy_fund_outbox` 累积到数十万行且资金单已全部终态时，生成一条**不带 LIMIT** 的 `DELETE FROM qy_fund_outbox WHERE created_at < $1`，一次删光。在全平台业务库上产生长事务、大量 WAL / 表膨胀与锁等待。日志里 `已清理 %d 行` 打印一个远大于 200 的数字就是直接证据。
- **主库 MySQL**：`Limit` 生效但每次只删 200 行、6 小时一次 = 800 行/天。日划转+提现+违规退款笔数超过 800 就净增长，保留期永远追不上。

违反 AGENTS.md「Do not use database-specific features without cross-DB fallback」。

**修复**：改成按主键分批的跨库写法 + 循环：
```go
func QyPruneFundOutbox(before int64, batch int) (int64, error) {
    var ids []int64
    if err := DB.Model(&QyFundOutbox{}).Where("created_at < ?", before).
        Order("id").Limit(batch).Pluck("id", &ids).Error; err != nil { return 0, err }
    if len(ids) == 0 { return 0, nil }
    res := DB.Where("id IN ?", ids).Delete(&QyFundOutbox{})
    return res.RowsAffected, res.Error
}
```
`PruneOutbox` 里加循环（带 `ctx.Err()` 与 sleep 限速，参照 `availability/flush.go` 的 `deleteBefore`），直到删不满一批。

---

## D 级 — 代码质量 / 加固

### D1. `withdraw.pii_retention_days` 没有任何清理任务
`qianye/config/config.go:185-187`；`qianye/modules/withdraw/module.go:70-79` 只注册了 `withdraw.reconcile`，`reconcile.go` 只做 `resumeApproved`/`settlePaying`。全仓对 `PIIRetentionDays` 的引用只在 `config.go` 与 `defaults.go:79`。

`model.go:120` 的注释"Cipher 在保留期到期后置空,Masked 与 Digest 保留"已经预设了语义，只是没人实现。已 `paid` 180 天的单据，`Payee.Cipher` 仍在库里。

**修复**：`reconcile` 里加一步 `prunePii(ctx)`：`WHERE created_at < now - PIIRetentionDays*86400 AND purged_at = 0` 且对应提现单为终态的 `Payee` 行，把 `cipher` 置空、写 `purged_at`，分批 Limit 执行（扩展库固定 MySQL，`Limit` 有效）。

### D2. `QueueStats` 无同步读取 `queue`，与 `queueOnce.Do` 里的写构成数据竞争
`qianye/guard/guard.go:186-187`；入口 `qianye/controller/admin.go:27`（另有 `availability/api.go`、`commission/api_admin.go` 的 adminHealth）

`queue` 只在 `startWorkers()` 的 `queueOnce.Do`（`guard.go:172`）里赋值，而 `startWorkers` 只由 `HotAsync` 调用；`QueueStats` 绕开了这个 once。进程刚启动、尚无 relay 流量时管理员打开健康面板，与第一个 relay 请求的 `queue = make(...)` 构成无同步读/写。amd64 上实际后果只是面板显示 `capacity: 0`，但按 Go 内存模型是未定义行为，`-race` 必报。

**修复**：`QueueStats()` 开头加一行 `startWorkers()`。

### D3. `fund.resolve` 的人工裁决理由无长度上限
`qianye/controller/admin.go:135`（只校验非空）+ `admin.go:169`（`"人工裁决: " + req.Reason` 直写 `varchar(512)`，`qianye/model/fund_order.go:43`）

理由 > 506 字符 → MySQL 严格模式 1406 `Data too long` → `serverError` 只回"处理失败,请稍后重试"，不提示是理由太长。管理员原样重试，这笔资金单永远停在 uncertain。

**修复**：与提现审核对齐，加 `checkRunes(req.Reason, 200)` 前置校验并返回 `qy_reason_too_long`；写库前再用 rune 安全的 truncate 兜底。

### D4. 规则级 `fee_multiple` 无上界，可绕过全局 YAML 的 0..100 限制
`qianye/modules/violation/rules.go:270-272`（只校验非负）vs `qianye/config/validate.go:244-246`（严格 0..100）

**注意：这是加固建议，不是可直接触发的缺陷**。默认 `violation.max_fee_quota = 5000000`（`config/defaults.go:90`）会 clamp 住 `computeFee` 的 `want`。要放大成"一次扣光余额"必须叠加第二处误配——把 `max_fee_quota` 设为 0（`checkQuotaCap` 允许 0，含义是"不限"）。

**修复**：`ValidateRule` 对 `FeeMultiple` 加与 YAML 一致的 `> 100 → 报错`；`checkQuotaCap("violation.max_fee_quota")` 拒绝 0，强制运维给出有限的全局兜底。

### D5. commission 的用户名脱敏对单字符名原样回显
`qianye/modules/commission/mask.go:45`（`case n <= 2: return string(r[0]) + "**"`）

单字符用户名（`model/user.go` 的 `validate:"max=20"` 没有下限）或单字符邮箱本地部分 → `masked_name` 返回 `"王**"` / `"a**@***.com"`，星号之外就是完整原文。

**但审计员"这是遗漏"的判断不成立**：`mask.go:41-43` 的注释明写"短名字(1~2 字符,中文昵称的常见形态)不能只遮中间 —— 所以统一只保留首字符"，`mask_test.go:104` 也把 `{"单个中文", "王", "王**"}` 固化成期望值。这是**刻意选择**，与 `transfer/validate.go:174` 口径不一致而已。泄漏面确实存在（1 字符）。

**修复（如果要统一口径）**：`maskCore` 加 `case n == 1: return "**"`，同步改 `mask_test.go`。

---

# 二、被判为误报 / 不成立的条目

| 来源 | 条目 | 判定与理由 |
|---|---|---|
| upstream#3 | 扩展库启动期不可达导致整个 new-api 拒绝启动 | **误报（属明示设计取舍）**。`qianye/bootstrap.go:34-36` 的注释原文：「返回 error 的场景仅限配置错误与启动期连不上数据库,此时主程序会 FatalLog —— 配置写错就该立刻炸,而不是带着一个永远不可用的扩展跑起来」。作者已经权衡过并写下了结论。运维风险真实存在（k8s 启动顺序），但这是"要不要改设计"，不是缺陷。**建议**：加一个 `runtime.fail_fast_on_boot`（默认保持现状）供部署方选择。 |
| arith#7 | 规则级 `fee_multiple` 一次扣光用户余额 | **降级为加固建议（见 D4）**。默认 `max_fee_quota=5000000` 已 clamp，需叠加第二处误配（`max_fee_quota=0`）才能到"扣光余额"。原判"配置事故放大器 / 资损"过重。 |
| authz#4 | commission 单字符脱敏是"遗漏不是取舍" | **判断错误（见 D5）**。注释与测试都固化了这个行为，是刻意的，不是漏写。泄漏本身成立但轻微。 |
| race#4 / authz#3 | `QueueStats` 数据竞争定性为「资损/DoS」 | 竞争本身**确认**（见 D2），但影响是面板偶显 0，非资损。原文也定为"代码质量"，此处仅确认定级。 |
| crossdb#4 | `markSuccess` 在 `RowsAffected == 0` 时也有问题 | **部分成立**。`markSuccess`（`execute.go:243-245`）确实检查了并 `return nil` 让出——这是**正确的**（补偿任务抢先推成 Success 时不该重复执行 LocalCommit）。只有"对方推成 Failed/Uncertain 时不该写 ResultOK 审计"这半条成立，且概率远低于 `markFailed`。已并入 B7 的修复建议。 |
| race 报告 | `lease` 的 fence 是"死参数" | **不构成活跃缺陷**，我逐个核对了 8 个 leased 任务的写入，全部是 CAS / 幂等 upsert / 幂等删除，双跑窗口内不会写脏。但审计员的提醒是对的：注释给的安全感是虚的，将来新增 leased 任务时若假设"fence 会挡住老持有者"会直接出资损。**建议**把注释改成如实描述，或把 fence 透传给 `fn`。 |
| 各报告的"已核对无问题"清单 | groupvis 不污染全局定价缓存、`bumpCounter` 并发唯一性、`absorbAccruals` CAS、`applyQuotaTransfer` 加锁顺序、`FreezeForWithdraw` 三重保证、`QyClaimFundOutbox` 早退、初始化顺序、接口权限与数据归属、AES-GCM nonce、错误信息脱敏、限流挂载 | **抽查复核一致**，无异议。 |

---

# 三、总评

**结论：不能按现状上生产。**

这套代码的工程素养明显在平均线以上——两阶段 + 主库 outbox 探针的设计是对的，`computeSettlement` 抽成纯函数、`lockBalance` 单点加锁约定、`absorbAccruals` 写读到的 gross 而非列名、`crossedThreshold` 用"跨越"而非"达到"，这些都是在真正难的地方做对了决定，注释也基本诚实。

但缺陷有一个共同的形状：**纯函数层、单事务层做得非常干净，一到"调度层 / 收尾层 / 配置消费层"就断链**。
- carry 算对了，调度不选它（A5）
- hold 打对了，对账任务不认它（A1）
- 探针在划转里补了，提现里忘了（A1）
- 幂等键防住了重复执行，没防住重放换参（B4/B5）
- 四项风控配置定义好、校验好、给了默认值，就是没有消费方（C1/C2）
- `guard.Hot` 造了 ctx，下游没人接（C3）
- `KeyVersion` 写进去了，从没读出来（B8）

`config.go:355-366` 自己写过"风控开关名拼错却被静默忽略,是本系统最危险的失败模式"——C1/C2 正是同一失败模式的另一种形态：名字没拼错，但没有消费方。这类断链单元测试永远发现不了，只能靠集成测试或本次这种全链路走查。

**最该先修的三条：**

1. **A1 — 提现 hold 被对账任务翻转**（`withdraw/reconcile.go:76,117`）。唯一一条能被主动构造、可重复领取同一笔佣金的路径，攻击成本就是"在兑现瞬间打满主库连接"，`credit.go:230-234` 的注释自己点名了这个套利面。修复量：两处，各三行。

2. **C3 + C4 — 热路径 ctx 与熔断双双失效**（`guard/guard.go:113,141`、`commission/hook.go:56`、`db/health.go:63`、`db/db.go:200`）。这是唯一一条能把扩展变成主业务单点故障的路径，且两个防护互为掩护：熔断因为纯内存 hook 不断 `MarkSuccess` 而打不开，超时因为闭包丢 ctx 而不生效，最后高水位同步降级把无界 IO 直接搬到 relay 结算线程上。它违反了模块自己写在包头的核心原则。修复量：加 ctx 参数是机械改动，DSN 补 `readTimeout` 是一行。

3. **A2 + B1 — 违规扣费的退款链路整体不可信**（`violation/api_admin.go:427,444,474`）。退错账户（不还令牌额度、不还订阅池）+ 没注册 Resolver + `RowsAffected==0` 堵死重入自愈，三个问题叠在同一条路径上，结果是"撤销了但没退款"且**管理员点第二次也修不好**，必然走人工补单 → 同一笔退两次。这条不修，违规扣费功能就不该开。

修完这三条之后，A3（入队后静默丢弃）、A4（游标越过失败订单）、A5（carry 停滞）、B4/B5（审计可伪造）应作为第二批一起清掉——它们都是"钱少发了/账记错了"，不会立刻炸，但会在上线两三个月后变成对不上的账。

C1/C2 的两处死配置至少要先在 `config.Load()` 里加一条显式告警（"以下配置项当前无消费方"），否则运维会一直以为闸门是关着的。
