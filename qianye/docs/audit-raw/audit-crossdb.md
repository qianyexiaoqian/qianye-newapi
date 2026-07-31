# crossdb

我已通读 `qianye/service/twophase/`、`modules/transfer/`、`modules/withdraw/`、`modules/commission/`、`modules/violation/` 的资金路径,以及 `model/qy_export.go` 的 outbox 探针。下面只列我能给出可复现交错序列的真实缺陷。

---

## 1. 【资损】提现对账任务会自动推翻 `holdForReview` 的人工裁决,把已到账的佣金退回可用池

**位置**:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\withdraw\reconcile.go:117-120`(以及扫描条件 `reconcile.go:75-77`)

**缺陷**:`settleOnePaying` 的 `case qymodel.StatusFailed` 分支直接调用 `failWithdrawal(w, ...)` 退回佣金,既不复查主库 outbox 探针,也不检查 `w.ReconcileState == ReconcileHold`。而 `credit.go:66-69` 在探针显示"主库已生效"时正是把单据留在 `paying` + `reconcile_state=hold` 等人工裁决——这个状态恰好落在 `settlePaying` 的扫描集合里。

**精确交错序列**(不需要进程崩溃,单节点即可复现):

| 时刻 | 线程 A(管理员 approve → creditQuota) | 线程 B(withdraw.reconcile,lease.Run 每 60s) |
|---|---|---|
| t=0 | `startPaying`:提现单 `approved → paying`,`settle_started_at=0`,佣金已在申请时冻结 | |
| t=0 | 主库事务:插入 `qy_fund_outbox` + `users.quota += 500000`,**服务端 commit 成功**,但客户端在读 OK 前连接被切断(`KILL` 掉这条 MySQL 连接 / 主库前面的 LB 抖动),GORM 返回 `invalid connection` | |
| t=0 | `twophase.Execute` → `markFailed` → `qy_fund_orders.status = Failed`(execute.go:108-112) | |
| t=0 | `creditQuota`:`order.Status==Failed` → `mainSideApplied()` 探针查到 outbox 行 → **true** → `holdForReview()`,提现单保持 `paying` + `reconcile_state=hold`(credit.go:66-69) | |
| t=60 | | `settlePaying` 扫到该单(`status=paying AND settle_started_at < now-60`,**无 reconcile_state 过滤**) |
| t=60 | | 读 `qy_fund_orders` → `Failed` → `failWithdrawal(w, ...)` |
| t=60 | | `applyTransition(paying → failed)` CAS 成功(状态确实还是 paying)→ `commission.UnfreezeForWithdraw` → `available_quota += 500000` |

**结果**:用户主库额度 +500000,同时佣金 500000 回到 `available_quota`,可以再发一单提现,循环套利。`credit.go:230-234` 的注释本身就点名了这是"可以被主动构造的套利面(在兑现瞬间打满主库连接让事务超时)"——防线写了,但下一个对账周期把它拆掉了。

**第二条触发路径(无需连接切断)**:主库事务长时间未提交(网络黑洞、`innodb_lock_wait_timeout` 调大)。默认配置下 `Compensate` 因为 `updated_at < now-60` 与 `next_probe_at` 双条件,探针实际节奏约为 60/120/180/240/300/360/424/552/808/1064 秒;第 9 次探针(t≈1064)时 `age > ManualReviewAfterSeconds(900)` 且 `attempts(9) < MaxProbeAttempts(10)`,于是 `compensate.go:124` 的 `finalizeFailed` 把在途单判成 `Failed`。此后主库事务才提交 → 同样落到上面的退佣分支。

**对照证据**:划转模块的同名逻辑做对了——`qianye\modules\transfer\reconcile.go:124-128` 在 `StatusFailed` 分支先调 `mainSideApplied(orderNo)`,探针为真就 `markUncertainAfterConflict` 而不是退还预占。提现模块缺了这一步,两者是同一个作者写的同一类收尾,属于明确的遗漏而非取舍。

**影响等级**:资损(可重复领取同一笔佣金)

**修复建议**:
1. `settlePaying` 的扫描条件加 `reconcile_state <> 'hold'`——已转人工的单只能由 `resolveHold` 推动;
2. `settleOnePaying` 的 `StatusFailed` 分支照抄 transfer 的写法:先 `if mainSideApplied(order.OrderNo) { holdForReview(...); return }`,探针失败按"可能已生效"处理,再调 `failWithdrawal`;
3. 顺带把 `failWithdrawal` 里的 `applyTransition` 加上 `reconcile_state` 清空,避免终态单残留 hold 标记。

---

## 2. 【风控失效 / 拒绝服务】提现的四项额度风控配置从未被任何代码读取

**位置**:
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\withdraw\validate.go:108-113`(`acceptCreate` 只校验 `MinQuota` 与 `common.MaxQuota`)
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\withdraw\create.go:65-92`(整个落单事务里只有 `checkDailyCount` 一道闸)
- 配置定义与默认值:`config\config.go:176-184`、`config\defaults.go:75-78`;`config\validate.go:203-209` 还专门校验了 `max_quota_per_order` 与 `min_quota` 的一致性

**缺陷**:`withdraw.max_quota_per_order`(默认 5e8)、`withdraw.daily_max_quota`(默认 1e9)、`withdraw.max_pending_orders`(默认 3)、`withdraw.cooldown_seconds`(默认 60)在 `qianye/modules/withdraw/` 下**零引用**(全仓 grep `MaxQuotaPerOrder|DailyMaxQuota|MaxPendingOrders|CooldownSecs` 只命中 config 包与 transfer 模块)。配置被 `validate()` 校验、被 `applyDefaults()` 赋默认值,因此运维完全无法察觉它们是空的。

**具体触发场景**:某邀请人 `qy_commission_balance.available_quota = 2,000,000,000`(长期返佣累积,扩展库是 int64 无上限)。他直接 `POST /api/qianye/withdraw`,body `{"method":"quota","quota":2000000000,"client_request_id":"..."}`。`acceptCreate` 只判 `quota <= common.MaxQuota (2147483647)` → 放行;`create` 只判当日笔数 → 放行;`FreezeForWithdraw` 只判 `available_quota >= quota` → 放行。一张 20 亿额度的提现单落库,`max_quota_per_order: 500000000` 完全没生效。同理,他可以在 60 秒内连发 3 单(`cooldown_seconds` 不生效),并在多天内累积任意多张未终态单(`max_pending_orders` 不生效),把人工审核队列淹掉。

`config.go:355-366` 自己写了"风控开关名拼错却被静默忽略,是本系统最危险的失败模式";这里是同一个失败模式的另一种形态——名字没拼错,但没有消费方。

**影响等级**:拒绝服务(审核队列被淹)+ 风控上限失效(单笔敞口从配置的 5e8 放大到 int32 上限)。注:因为申请即冻结,不构成直接超发。

**修复建议**:在 `acceptCreate` 中补 `cfg.MaxQuotaPerOrder > 0 && req.Quota > cfg.MaxQuotaPerOrder` 的拒绝;在 `create` 的落单事务里(与 `checkDailyCount` 同事务,保证串行化)补当日累计额度 `SUM(quota)`、未终态单计数、上一单 `created_at` 冷却三道 CAS 前置判断。另在 `handleGetConfig` 里下发这几个上限,前端才能给出正确的输入上界。

---

## 3. 【数据错误 → 潜在二次退款】违规扣费退款走了两阶段,但没注册 Resolver,补偿成功后业务侧状态永久错误

**位置**:
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\violation\api_admin.go:444-452`(退款成功才回写 `fee_status`)
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\violation\api_admin.go:427-429`(`RowsAffected == 0` 直接幂等返回,阻断了重入自愈)
- `C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\service\twophase\compensate.go:97-103`(无 resolver 时只推资金单状态,不做业务收尾)

**缺陷**:`KindViolationFee` 没有调用 `twophase.RegisterResolver`(全仓只有 `withdraw/module.go:30` 与 `transfer/module.go:26` 注册),`refundFee` 也没有 `LocalCommit`。`fee_status = refunded` / `refund_quota` 的回写完全依赖业务线程在 `revokeRecord` 里同步走完;而违规模块没有任何对账任务(`violation/tasks.go` 只有证据 GC 与封禁补偿)。

**精确交错序列**:
1. 管理员点"撤销 + 退款"。`revokeRecord` 的 `WHERE id=? AND status IN ('active','appealed')` CAS 成功,记录变 `revoked`,`fee_status` 仍是 `charged`。
2. `refundFee` → `twophase.Execute` → 主库事务提交成功(`users.quota += fee`,outbox 行落地)。
3. 进程在 `applyOnMainDB` 返回与 `markSuccess` 之间被 kill(滚动发布 / OOM)。资金单停在 `pending`。
4. 重启后 `twophase.Compensate` 探针查到 outbox → `resolveApplied`:`resolverRegistry["violation_fee"]` 不存在 → 直接把资金单标成 `Success`。
5. 但 `qy_violation_record.fee_status` 永远停在 `charged`、`refund_quota = 0`。
6. 管理员再点一次"撤销+退款":`revokeRecord` 的 CAS `RowsAffected == 0` → `return 0, nil`,`refundFee` 根本不会被调用,自愈路径被堵死。
7. 管理端与用户端(`api_user.go:47` 下发 `fee_quota`、`api_user.go:126` 汇总 `SUM(fee_quota)`)都显示这笔罚款仍在被收取。运营看到"已撤销但没退款",走人工额度补单 → **同一笔罚款退两次**。

**影响等级**:数据错误(对账口径错误),叠加人工介入后升级为资损

**修复建议**:在 `violation` 的 `InstallHooks()` 里 `twophase.RegisterResolver(qymodel.KindViolationFee, ...)`,回调按 `order.RefId`(记录 id)幂等地把 `fee_status` 置为 `refunded`、`refund_quota` 置为 `order.AmountQuota`;同时给 `refundFee` 补 `LocalCommit`,让正常路径的回写与资金单回写落在同一个扩展库事务里。

---

## 4. 【数据错误】`markFailed` 不校验 `RowsAffected`,会在补偿任务已推进单据后伪造终态与审计

**位置**:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\service\twophase\execute.go:262-277`

**缺陷**:`markFailed` 的 UPDATE 带了 `AND status = StatusPending` 的 CAS 条件,但拿到 `err == nil` 就无条件执行 `order.Status = StatusFailed` 并写一条 `ResultFail` 审计。`RowsAffected == 0`(即补偿任务已抢先把单据推成 `Uncertain`/`Failed`)与"CAS 成功"被当成同一件事。

**精确交错序列**:
1. 线程 A(`transfer.create` 或 `withdraw.creditQuota`)进入 `applyOnMainDB`,主库事务因锁等待/网络长时间不返回。
2. 线程 B(`Compensate`)在约 t=826s 时 `attempts` 达到 `MaxProbeAttempts(10)` → `markUncertain` → `qy_fund_orders.status = Uncertain`。这是系统里"我不知道钱动没动,禁止自动回滚"的唯一出口。
3. 线程 A 的主库事务最终以确定性错误返回(如 `errInsufficientQuota`)。`markFailed` 的 UPDATE 命中 0 行,但**内存里** `order.Status` 仍被改成 `Failed`,并向 `qy_audit_logs` 写入一条 `fund.transfer.failed` / `ResultFail`。
4. 调用方按内存里的 `Failed` 走回滚分支:`transfer.releaseOnFailure` 清 `risk_held` 并退还风控预占;`withdraw.creditQuota` 走 `failWithdrawal` 解冻佣金。
5. 最终状态:`qy_fund_orders` 里这张单是 `Uncertain`(等人工裁决),业务明细却已经是 `failed` 并且预占/冻结都已释放,审计日志还声称它转成了 `failed`。管理员在对账台看到的是三份互相矛盾的记录,而 `Uncertain` 单不会再被 `Compensate` 扫描(它只查 `status = pending`),这个矛盾永远不会自愈。

**影响等级**:数据错误(资金对账台自相矛盾 + 审计日志失真)。注:第 4 步的动作方向本身是正确的(主库确实没生效),所以不直接产生资损。

**修复建议**:`markFailed` 检查 `res.RowsAffected`;为 0 时不要改写 `order.Status`、不要写 `ResultFail` 审计,改为回读一次真实状态并 `common.SysError` 告警,让调用方按真实状态(`Uncertain` → 不做任何自动回滚)分支。同理,`markSuccess`(execute.go:243-245)在 `RowsAffected == 0` 时也应区分"对方推成了 Success"与"对方推成了 Failed/Uncertain",后者不应让 `Execute` 返回 `nil` 并写 `ResultOK` 审计。

---

**已核对但未发现问题的项**(供交叉复核参考):
- `QyClaimFundOutbox` 返回 false 时 `applyOnMainDB`(execute.go:214-220)确实在 `appliedNow = true` 之前 `return nil`,三个 `MainApply` 实现都不会被调用。
- 划转 `applyQuotaTransfer`(transfer/service.go:156-215):锁按 user id 升序、扣款 `WHERE quota >= ?` + `RowsAffected != 1`、加款 `WHERE quota <= MaxQuota-amount` + `RowsAffected != 1`,均正确。
- `FreezeForWithdraw` / `SettleFrozen` / `UnfreezeForWithdraw`:`qy_commission_freeze` 的 `(ref_no, action)` 唯一索引 + `lockBalance` 行锁 + 提现单状态机 CAS,三者叠加下同一单不可能既解冻又核销,也不可能重复解冻。
- `absorbAccruals`(commission/settle.go:320-347)用读到的 `settled_amount` 做 CAS 而非写列名,并发结算会整批回滚而不是"发一半"。
- `model.DB` 未接 `dbresolver`,`QyProbeFundOutbox` 读的是主库主节点,不存在读从库复制延迟导致探针误判的问题。
