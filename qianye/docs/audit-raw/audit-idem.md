# idem

我已通读 `qianye/` 全部资金相关代码与上游 hook 挂载点，重点复核了幂等键构造、幂等命中返回、重放路径与唯一索引。以下是确认的真实缺陷。

---

## 1. 提现「转人工裁决」标记会被自己的对账任务推翻，自动把已到账的佣金退回可用池（**资损，无需崩溃即可复现**）

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\withdraw\reconcile.go:117`**（配套 `reconcile.go:76`、`credit.go:68`）

`holdForReview()` 只写 `reconcile_state='hold'`，**不改 status**（`credit.go:238-243`，单据仍是 `paying`）。而 `settlePaying()` 的查询条件是 `status='paying' AND settle_started_at < stale`，**完全没有排除 `reconcile_state='hold'`**。于是下一轮对账会重新裁决同一张单，并在 `order.Status == StatusFailed` 分支直接 `failWithdrawal()` → `UnfreezeForWithdraw()`——正是 `creditQuota` 刚刚明确拒绝执行的动作。

该分支的注释「资金单被判失败意味着补偿任务已通过 outbox 探针确认主库未生效」是错的：`twophase.markFailed`（`execute.go:256`）在业务线程 commit 报错时也会把资金单置为 `failed`，那条路径**从未探针过**。

**复现步骤（全自动，1–2 分钟内完成）：**
1. 提现单 W（quota 方式）审核通过 → `creditQuota` → `startPaying` 把 W 置为 `paying`，`settle_started_at = T`。
2. 主库事务实际提交成功（`qy_fund_outbox` 有行、`users.quota += N` 已生效），但 commit 应答阶段连接被切断，GORM 返回 error。
3. `twophase.Execute` → `markFailed` → 扩展库中 **FO.status = failed**。
4. 回到 `credit.go:66-70`：`mainSideApplied()` 探到 outbox 行存在 → 返回 true → `holdForReview(w, "资金单被判失败但主库探针显示已生效,需人工核对")`。此时 W 仍是 `paying`，只多了 `reconcile_state='hold'`。
5. 60 秒后（`PendingGraceSeconds` 默认 60，`withdraw.reconcile` 周期 `max(1min, 2×30s)`）对账任务运行：`settlePaying` 把 W 捞出来（hold 未被排除）→ `settleOnePaying` 读到 FO 是 `failed` → `failWithdrawal(w, …)` → CAS `paying→failed` 成功 → `commission.UnfreezeForWithdraw` 把 N 额度退回 `available_quota`。

**错误结果：** 用户主库额度已 `+N`，佣金账户的 N 也被退回可用池，可以再提一次。全程只有一条 `SysError` 日志，管理端 `reconcile_hold` 计数也被清掉，人工再也看不到这张单。

补充：即使不发生第 2 步的模糊 commit，只要 `holdForReview` 自身的扩展库写入失败（`credit.go:258-261` 只 `MarkFailure` 后静默 return，hold 标记根本没落库），同样会走到第 5 步。

**对照证据：** 划转模块的同名逻辑 `qianye/modules/transfer/reconcile.go:124-128` 在 `StatusFailed` 分支**显式再探一次 outbox**（`if mainSideApplied(orderNo) { markUncertainAfterConflict(...) }`）。提现这里少了这一步，是遗漏而非取舍。

**影响等级：资损**

**修复建议：**
- `settlePaying` 的 WHERE 增加 `AND reconcile_state <> 'hold'`；
- `settleOnePaying` 的 `case qymodel.StatusFailed` 分支照抄 transfer 的写法，先 `if mainSideApplied(w.OrderNo) { holdForReview(...); return }` 再 `failWithdrawal`；
- `holdForReview` 写入失败时不能静默 return，应返回错误让调用方保持 `paying` 且不退款（当前行为已经是不退，但要确保不会被下一轮翻转）。

---

## 2. 幂等命中不校验请求指纹：同一 `client_request_id` 提交不同金额/不同收款人，接口返回成功并写下一条金额与收款人全都是攻击者指定值的「划转成功」审计（**数据错误 / 审计污染**）

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\transfer\service.go:109`**（根因在 `qianye\service\twophase\execute.go:179-183`）

`buildIdemKey`（`validate.go:130-146`）拼出的键是 `<userId>:<client_request_id>`，`client_request_id` 完全由前端生成、用户任意可控。`resolveExisting` 在命中 `StatusSuccess` 时直接 `return order, nil`，**没有把本次请求的 amount / fee / to_user_id 与原单比对**。回到 `create()`，`execErr == nil`，于是：

- `service.go:109` `writeCreateAudit(c, order, acc)` —— `writeCreateAudit`（`service.go:321-334`）取的是 **本次请求的 `acc.Amount` 与 `acc.ToUserId`**，而 `TraceNo` 是**原单的真实单号**，`Result` 是 `ResultOK`。
- `service.go:110` `buildCreateResponse` 回读原单，HTTP 200 返回的是原单的 100 额度、原收款人。

**复现步骤：**
1. 用户 5 提交 `{to_user_id:7, amount:100, client_request_id:"abc", confirm:true}` → 成功，单号 `TR2026…`，`qy_fund_orders` 落 `(transfer, "5:abc")`。
2. 同一用户再提交 `{to_user_id:9, amount:50000000, client_request_id:"abc", confirm:true}`。
3. `tx.Create(order)` 撞 `uk_qy_fund_idem` → 加载原单 → `StatusSuccess` → 无错误返回。
4. **错误结果 A：** HTTP 200，客户端/集成方按「成功」处理，但这 5000 万额度一分没转（用户 9 也没收到）。响应体里的 `amount`/`to_user_id` 是第一笔的值，任何只判 HTTP 状态码的调用方都会误判。
5. **错误结果 B：** `qy_audit_logs` 多出一条 `transfer.create` / `result=ok` / `amount_quota=50000000` / `target_user_id=9` / `trace_no=<第一笔真实单号>` 的记录。攻击者可用一个真实单号无限刷出任意金额、任意收款人的「成功划转」审计，而资金侧毫无痕迹——审计表是这套资金系统事后仲裁的唯一凭据。

同类站点（同一根因，行为略轻）：
- `qianye\modules\withdraw\create.go:97` `loadByIdemKey`：同 `client_request_id` 换 `quota`/`method` 会返回原单，用户以为新单已提交（此处未写假审计）。

**影响等级：数据错误（审计完整性）**

**修复建议：** 在 `twophase.Request` 上增加一个请求指纹字段（如 `sha256(kind|userId|peerUserId|amount|fee|refId)`），落进 `qy_fund_orders` 新增列；`resolveExisting` 命中时比对指纹，不一致直接返回 409 `qy_idem_key_conflict`，调用方不得写审计、不得返回 200。`withdraw.loadByIdemKey` 同样比对 `quota`/`method`。

---

## 3. 充值返佣扫描：单笔计佣失败后游标照样前进，该笔充值的佣金永久丢失（**资损，方向是少发给用户**）

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\commission\topup_scan.go:81`**（错误吞掉点在 `topup_scan.go:122`）

`runTopupScan` 的低水位只由 **pending 订单** 决定：

```go
case common.TopUpStatusSuccess:
    accrueTopUp(r)                 // 错误只 warnf,不影响下面
case common.TopUpStatusPending:
    if r.CreateTime >= lookback && (minPending == 0 || id < minPending) { minPending = id }
}
next := maxScanned
if minPending > 0 { next = minPending - 1 }
```

`accrueTopUp` → `accrueOneShot` 的返回值在 `topup_scan.go:122` 只打了一句 `warnf("充值订单 %s 计佣失败(下轮重扫会重试)")`——**这句注释与实际行为相反**：游标已经推过该订单，`id > low` 的条件保证它再也不会被扫到。

**复现步骤：**
1. 扩展库出现一次非连接级错误（`Error 1213 Deadlock`、`Lock wait timeout exceeded`——这在结算事务并发持有 `qy_commission_balance` 行锁时很常见；`db.isConnLevelError` 不认这两类，熔断不会打开，扫描继续跑）。或者主库瞬时抖动让 `resolveInviter`（`inviter.go:98-100`）返回 error。
2. 本批 `top_ups` 含成功订单 id=1000（trade_no T1）与 id 至 1050，窗口内无 pending 单。
3. `accrueOneShot` 对 1000 返回 error → 仅 warn；`writeAccrual` 什么都没写。
4. `next = maxScanned = 1050` → `saveTopupCursor(1050)`。
5. **错误结果：** T1 的邀请人永远拿不到这笔充值返佣。`qy_commission_accrual` 里没有 `(topup, "topup:T1")` 这一行，任何重扫都不会再产生它。只留下一条 warn 日志，管理端 `/commission/health` 也看不出来。

**影响等级：资损**

**修复建议：** 与 `minPending` 同机制——在循环里记录 `minFailed`（首个计佣失败的 id），`next = min(maxScanned, minPending-1, minFailed-1)`；或把失败的 `trade_no` 落一张重试表由结算任务补做。两者都比「只打日志」可靠。

---

## 4. 热路径队列高水位降级为同步执行时，返佣的库操作完全不受 `hot_path_timeout_ms` 约束，relay 线程会被扩展库拖死（**拒绝服务**）

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\guard\guard.go:144`** 与 **`qianye\modules\commission\accrual.go:205`**

`HotAsync` 在队列水位 ≥80% 时改为在**调用方线程**同步执行（`guard.go:142-145`），调用方就是 `model.RecordConsumeLog` 所在的 relay 结算线程（`model/log.go:344`）。`Hot()` 虽然造了一个带 `HotPathTimeoutMs`（默认 200ms）的 ctx 并传给 `fn(ctx)`，但返佣侧的闭包 `func(ctx context.Context) error { return accrueConsume(ev) }`（`hook.go:56-58`）**把 ctx 丢掉了**，其下游全部使用裸句柄：

- `accrual.go:205` `res := gdb.Clauses(conflict).Create(&row)` —— 无 `WithContext`
- `accrual.go:266` `ensureRelation` 的 Create —— 无 `WithContext`
- `accrual.go:299` `blockedInvitees` 的 Pluck —— 无 `WithContext`
- `inviter.go:89-92` `resolveInviter` 打主库 —— 无 `WithContext`

对照 `qianye/modules/violation/guard.go:213` 是 `gdb.WithContext(ctx).Clauses(...)`，说明这不是统一约定。同时 `db.normalizeDSN`（`db/db.go:200-215`）只补 `parseTime`/`charset`，**不设 `readTimeout`/`writeTimeout`**。

**触发场景：** 扩展 MySQL 半死（网络黑洞、`Lock wait timeout` 前的长时间等待，TCP 不返回 RST）。此时 `db.Available()` 仍为 true（`MarkFailure` 只认连接级错误字符串，且要连续 5 次才开熔断），队列因 worker 卡住迅速堆到 80%，之后**每一个有邀请人的用户的每一次 relay 请求**都在结算阶段同步执行 1~4 次无超时的 DB 往返。200ms 的保护形同虚设，relay 线程池被耗尽。这与模块自称的「热路径永不阻塞、扩展绝不能成为 relay 的单点故障」直接矛盾。

**影响等级：拒绝服务**

**修复建议：** 让 `accrueConsume` / `accrueOneShot` / `resolveInviter` / `blockedInvitees` 接收并使用 ctx（`gdb.WithContext(ctx)`、`model.DB.WithContext(ctx)`）；同时在 `normalizeDSN` 里补 `readTimeout`/`writeTimeout` 兜底。

---

## 5. 管理端人工冲正：同一 `client_request_id` 换 `accrual_id`/`quota` 重放，返回旧单并写下一条金额虚高的成功审计（**数据错误**）

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\commission\clawback.go:160`**（配套 `api_admin.go:201`、`api_admin.go:215`）

`manualClawback` 的幂等键是 `clawback:manual:<operatorId>:<client_request_id>`（`clawback.go:141`），`writeAccrual` 用 `OnConflict{DoNothing}`。冲突时**不报错**，随后 `clawback.go:160-164` 按同一个键回读，拿到的是**上一次那条**计佣行并当作「本次新建」返回。

**复现步骤：**
1. 管理员 A（id=3）POST `/commission/clawback` `{accrual_id:100, quota:500, reason:"拒付", client_request_id:"x"}` → 生成负额行 CA-1（-500）。
2. 管理员 A 在同一个弹窗里改了金额重提（前端按裁定 C10 在打开弹窗时生成并缓存 `client_request_id`，重试沿用同一个）：`{accrual_id:200, quota:9999, reason:"…", client_request_id:"x"}`。
3. `writeAccrual` DoNothing，**一分钱没冲**；`clawback.go:161` 回读到 CA-1 → 返回 `{accrual_no: "CA-1", gross_amount: "-500"}`。
4. `api_admin.go:207-218` 写审计：`TraceNo = CA-1`、`AmountQuota = req.Quota = 9999`、`Result = ok`。

**错误结果：** 接口 200，管理员以为 9999 已冲正；`qy_commission_accrual` 里没有这条；审计表却记录了一笔「已成功冲正 9999」并挂在 CA-1 上。事后按审计对账会与账本差 9999。

**影响等级：数据错误（审计完整性）**

**修复建议：** `writeAccrual` 增加「本次是否真的插入」的返回值（`res.RowsAffected == 1`）；`manualClawback` 在幂等命中且 `origin.Id != 入参 accrual_id` 或 `amount` 不等时返回 409；`adminClawback` 的审计一律用回读行的真实 `GrossAmount` 而不是 `req.Quota`。

---

### 已复核并确认**没有**问题的路径（供复核者节省时间）

- `qy_fund_orders` 的 `uk_qy_fund_idem(idem_scope, idem_key)`、`qy_withdrawals` 的 `uk_qy_wd_idem`、`qy_commission_accrual` 的 `uk_qy_ca_idem`、`qy_commission_freeze` 的 `uk_qy_cf(ref_no, action)`、`qy_violation_ban` 的 `uk_qy_vban_user_cycle`、主库 `qy_fund_outbox` 的 `uk_qy_outbox_no` —— 索引定义与代码假设一致。
- 提现审核按钮重复点击：`applyTransition` 是 `WHERE id=? AND status=?` 的 CAS（`status.go:123-131`），并发只有一个 `RowsAffected==1`，`creditQuota` 不会跑两次。
- `creditQuota` 重入：`(withdraw_credit, WithdrawNo)` 命中后 `resolveExisting` 按 success/pending/failed 三分支返回，`MainApply` 不会二次执行。
- 兑换码：`QyOnRedeemSuccess` 挂在事务提交之后（`model/redemption.go:185`），且 `redemption:<id>` + 主库 status CAS 双重保证。
- `top_ups.trade_no` 有 `unique` 约束，`topup:<trade_no>` 键不会塌缩；超长走 sha256（`accrual.go:67-73`）。
- `Record.RecNo = "vr_" + requestId(≤40) + "_" + ruleId`：`common.NewRequestId()` 恒为 39 字符，不会被截断碰撞。
- `bumpCounter` 的「upsert 后同事务裸 SELECT」在 InnoDB 下确实读到本事务的写入且持 X 锁，注释成立。
- `lease` 的 fence + `Renew` 失败即 cancel ctx，各后台任务在批次边界检查 `ctx.Err()`，未见双跑写库。
