`go build ./...` 通过,`go vet ./qianye/...` 通过,`go test ./qianye/... ./model/... -count=1` 全绿。以下是两个问题的答复。

---

# 一、修复引入的新缺陷

## NEW-1（阻断级)新增的 `read_timeout_seconds=30` 会切断扩展库自动迁移 —— 这批修复自己的升级路径

**位置**:`qianye/db/db.go:293`(`readTimeout` 默认 30s)× `qianye/db/migrate.go:19`(`migrateLockTimeoutSeconds = 30`)× `qianye/db/migrate.go:49`(迁移申请了 **5 分钟** ctx 预算)

`normalizeDSN` 现在无条件往 DSN 里写 `readTimeout=30s`。go-sql-driver 的 `readTimeout` 是**每一次结果包读取**的硬 deadline,与 ctx 无关、与"这条语句是不是 DDL"无关。

**复现 A(与表大小无关,多节点必现)**:
`migrate.go:60` `SELECT GET_LOCK('qy_schema_migrate', 30)` —— 从节点抢锁时这条读会阻塞**最多 30 秒**,而 readTimeout 也是 **30 秒**,两者相等,谁先触发不确定。readTimeout 先到 → `conn.QueryRowContext` 返回 i/o timeout → `Migrate` 返回 error → `bootstrap.Init:49` 返回 error → 主程序 FatalLog。**旧行为是打一行"另一节点正在执行扩展库迁移,本节点跳过"然后正常启动,新行为是启动失败。**

**复现 B(大表必现)**:
本批给 `qy_fund_orders` 加了 `fingerprint varchar(64)`,给 `qy_violation_record` 加了 `billing_source` / `subscription_id`。后者按请求量增长。MySQL 5.7 上 `ADD COLUMN` 即使走 INPLACE 也要重建全表,千万行超过 30 秒是常态。readTimeout 一到,驱动判连接坏死 → `AutoMigrate` 报 `invalid connection` → 启动失败,而 MySQL 的 DDL 不可回滚,表停在半迁移态。

`db.go:270-276` 的注释自己写的是"**必须显著大于最慢的一次合法后台操作**",而代码选的默认值恰好等于迁移锁超时、只有迁移预算的十分之一。这正是原审计总结的那类"注释承诺了、代码没做到"。

**建议**:迁移专用连接用独立 DSN(`readTimeout=0`),或最低限度把 `read_timeout_seconds` 默认值提到 120,并在 `validateDatabase`(`qianye/config/validate.go:92`)加一条 `read_timeout_seconds > migrateLockTimeoutSeconds` 的硬校验。

---

## NEW-2(高)200ms 热路径预算首次真正生效 → 返佣静默丢失,并经熔断放大成批量丢弃

**位置**:`qianye/guard/guard.go:115-131`(`hotRun` 让 `Hot` 与 `HotAsync` 共用 `hot_path_timeout_ms`,默认 **200ms**)

这批修复把 ctx 真正接到了 GORM 上(`commission/hook.go:57`、`inviter.go:95`、`accrual.go:162/259/313`、`violation/ban.go:57`)。修复前返佣链路一条 GORM 调用都不接 ctx,200ms 形同虚设、作业总能跑完;**修复后它真的生效了**。

- **后果 1(资损)**:`accrueConsume` 一次冷缓存轮要在 200ms 内完成 `resolveInviter`(回主库)+ `ensureRelation` + `blockedInvitees`(60s 一次全表 Pluck)+ `effective()` + `writeAccrual`,最多 5~6 次往返。超时后 `hotRun` 只 `MarkFailure` + 限频日志,**没有重试、没有 outbox**,这笔佣金永久丢失。

- **后果 2(放大 —— 两位复核员都没提到的一环)**:`qianye/db/db.go:326` 的 `isConnLevelError` 把 `context.DeadlineExceeded` **判为连接级错误**。默认 `breaker_failure_threshold=5` —— **连续 5 次 200ms 超时就把整个扩展熔断 30 秒**(`breaker_open_seconds=30`),熔断期间全部 `HotAsync` 走 `recordSkip`(`guard.go:181`)直接丢弃。而 C4 的探针化修复让纯内存的 `availability.sample` **不再** `MarkSuccess`,失败计数从此不会被抹掉。一次尾延迟抖动因此被放大成"30 秒内全站消费返佣 + 违规记录全部丢弃"。这条放大链是本批两处修复叠加出来的。

- **后果 3**:`violation.persist` 里的 `disableUserForViolation` 现在也吃这 200ms,而它内含主库 `FOR UPDATE` 事务 + 逐批会话吊销。超时 → `markBan(gdb, id, BanFailed, ...)`,即时自动封号退化成"等 5 分钟由 `runBanCompensate` 补做"。能自愈,但每次刷一条 SysError,且即时封禁语义没了。

**建议**:给异步作业单列 `hot_async_timeout_ms`(1~3 秒),200ms 只留给真正跑在 relay 线程的 `Hot`;并在 `hotRun` 里把 `context.DeadlineExceeded` 排除出 `MarkFailure` —— 超时是我们自己设的预算,不是"库坏了",拿它喂熔断等于让熔断判据被自己的预算污染。

---

## NEW-3(中)违规撤销的自愈路径只建了一半:第二次点击会报一笔并不存在的退款

**位置**:`qianye/modules/violation/api_admin.go:444-456`

修复删掉了旧代码中 `refundFee` 成功后那一行无条件的 `Updates({fee_status: refunded, refund_quota})`(理由:交给 LocalCommit 与 Resolver),同时 `claimRevoke` 新开了"第二次点击继续往下走"的自愈入口。两者在**遗留数据**上对不齐:

对升级前已被旧 `resolveApplied`(当时没有 Resolver)推成 `Success`、而 `fee_status` 仍是 `charged` 的退款单,管理员再点一次"撤销+退款":
1. `claimRevoke:479-507` 回读 → revoked / charged → 进入退款分支;
2. `refundFee` → `twophase.Execute` → 幂等命中 `execute.go:255` `case StatusSuccess: return order, nil`,**LocalCommit 在这条路径上根本不执行**;
3. `api_admin.go:454` `refunded = rec.FeeQuota` → 接口返回 200 `refunded_quota=<金额>`,`api_admin.go:458` 写一条 `records.revoke` 成功审计;
4. 库里 `fee_status` 与 `refund_quota` 纹丝不动。**点几次写几条假审计。**

旧代码里这条路走不到(`RowsAffected == 0` 直接早退),所以这个误报面是**修复新引入的**。

**建议**:让 `refundFee` 回传"记录是否真的被标成 refunded",或在 `revokeRecord` 里 `refundFee` 成功后回读一次 `fee_status`,`== FeeStatusRefunded` 才置 `refunded`;另外为 `qy_fund_orders.kind='violation_fee' AND status=Success` 但 `fee_status IN ('charged','truncated')` 的历史行做一次性修复。

---

## NEW-4(低)划转指纹把服务端派生的手续费也算了进去

**位置**:`qianye/service/twophase/execute.go:126`(`FeeQuota` 进 Digest)+ `qianye/modules/transfer/service.go:117`(`fundingFacts` 取 `acc.Fee`)

`acc.Fee` 由受理时的 config 算出,不是"用户这次请求说了什么"。用户首次提交超时、运营在这中间调了 `transfer.fee_*`,同一个 `client_request_id` 的客户端重试会算出不同指纹 → `ErrIdemConflict` → 409「请到划转记录中核对」,而原单其实根本没成功。

口径与 `clawback.go:210` 刻意**不比** `GrossAmount`(理由完全相同:被服务端削过的派生量不能当指纹分量)不一致。建议 Digest 去掉 `FeeQuota`。

---

## 关于你点名的其余各项:逐个确认无问题

| 检查项 | 结论 |
|---|---|
| **签名变更调用点** | `writeAccrual`(2 处全改)、`markSuccess`(唯一调用点 `execute.go:178`)、`markFailed`/`createOrLoadOrder`/`resolveExisting`(全部内部调用,`gdb` 统一来自 `execute.go:152` 的一次 `db.Get()` 且已判 nil)、`normalizeDSN`(唯一调用点 `db.go:52`)、`disableUserForViolation`(2 处)、`markBan`(4 处)、`evaluateRisk`(1 处)、`claimBan`(1 处)。**无漏改**。`qianye/` 下 8 处 `context.Background()` 全是后台任务/脱离 HTTP 的资金操作根 ctx,**没有哪个调用点拿 Background 顶替真实 ctx** |
| **指纹空值(你特别点名)** | `execute.go:249` `want != "" && order.Fingerprint != ""`,两侧任一为空即跳过。历史行由 `model/fund_order.go:35` 的 `default:''` 补空,`violation/refundFee` 未接入也是空。**升级瞬间不会把幂等重放变成 409,确认正确** |
| `markFailed` 的 `RowsAffected==0` | 回读 + 不改内存 + 不写审计;`transfer/service.go:286` 与 `withdraw/credit.go:73` 两个回滚闸门都只认 `StatusFailed`,`Uncertain` 不触发回滚。**无误伤** |
| 提现四项新限额 | 幂等重放判定排在闸门之前(`create.go:132-136`),撤销单只进 `Submitted`(×4)不进 `Active`。**"填错→撤销→重填"不会被误杀**,仅 60 秒冷却窗口会拦(刻意,注释写明) |
| 划转收款方日限 | `risk.go:87` 判定在 `applyReservation` 之前、两行已按 user_id 升序 `FOR UPDATE`,失败由 `undoReservation:136` 原路退还。**不会永久吃掉名额** |
| 熔断新判据 | `markProbeHealthy` 只在 false→true 时清零,`touchedDB()` 只给真访问过库的作业投票,方向保守。**本身不误伤** —— 但见 NEW-2,与 200ms 叠加后放大 |
| PII 清理任务(问题 4) | 与 `withdraw.reconcile` 共用租约(`reconcile.go:51`),`ctx` 由 `lease.runOnce`(`lease.go:173-195`)在租约丢失时 cancel;终态判定写进扫描条件的子查询;只清 `Cipher`。**有租约保护,不会误删在用数据** |
| `PruneOutbox` 循环的租约注释 | 注释说"每轮检查租约",代码只有 `ctx.Err()` —— 但 `lease.runOnce` 确实会在租约丢失时 cancel 该 ctx,**注释与代码一致** |
| AutoMigrate 新列(问题 5) | `fingerprint varchar(64) not null default ''`、`billing_source varchar(16)`、`subscription_id int` 都是窄列,MySQL 8.0 走 INSTANT、5.7 走 INPLACE,**理论上不阻塞 DML**。真正的风险不是锁表,是 **NEW-1 的 30 秒 readTimeout 把 DDL 掐断** |

---

# 二、本轮新发现的同类缺陷(原审计漏网)

## OLD-1 `transfer.lookup_log_retain_days` 定义了、有默认值、写进了 YAML —— 零消费方

`qianye/config/config.go:129` + `qianye/config/defaults.go:49`(默认 30)。唯一的清理任务用的是**包内常量**:

```go
// qianye/modules/transfer/reconcile.go:15
const lookupLogRetainDays = 30
// reconcile.go:156
before := common.GetTimestamp() - int64(lookupLogRetainDays)*86400
```

`config.Transfer.LookupLogRetainDays` 全仓只出现在 config.go 与 defaults.go。运维改成 7(合规)或 90(风控)完全无效,而 `qy_transfer_lookup_logs` 记录的是"谁查过谁的收款人",是可关联到个人的行为日志。**与 C1(提现四项限额)是一模一样的形状。**

## OLD-2 `group_visibility.filter_group_api` 访问器零调用方

`qianye/config/config.go:228` → `GroupAPIOn()`(`config.go:305`)全仓 **0 个调用点**。`qianye/modules/groupvis/groupvis.go:47-49` 只注入了 3 个 hook,消费的是 `PricingOn()` 与 `PerfMetricsOn()`;分组 API 那一路从来没接上。而 `qianye.example.yaml:209` 明明白白写着 `filter_group_api: true`。这是一个信息泄漏修复(包头注释自称修 CWE-200)只做了三分之二,YAML 却声称做完了。

## OLD-3 `withdraw.pii_retention_days` 只覆盖了一半 PII 面

本批新增的 `prunePii`(`qianye/modules/withdraw/payee.go:189`)只清 `Payee`(每张单的快照)。而 `PayeeAccount`(`qianye/modules/withdraw/model.go:134`,表 `qy_withdrawal_payee_accounts`)存着同样的银行卡号密文,有 `Cipher` 列、有 `DeletedAt` 软删除,**没有 `PurgedAt` 列,也没有任何清理路径**(全仓只有 `payee.go:85/109` 两处写入)。用户在前端"删除收款方式"之后,银行卡号密文永久留在库里,保留期配置对它完全无效。**D1 只做了一半。**

## OLD-4 `qy_pii_audits` 无保留期

`qianye/modules/withdraw/model.go:192`,记录每一次收款信息明文访问(管理员 id、IP)。只有写入(`api_admin.go:350`)与查询接口,没有任何清理任务。与 `qy_transfer_lookup_logs` 同形状,但那张至少还有个(读错配置的)清理任务。

## OLD-5 `BanDeferred` 的注释承诺了不存在的人工出口

`qianye/modules/violation/tasks.go:82-85` 写"deferred 行由管理员在封禁列表里处理",但 `adminUnban`(`api_admin.go:731-732`)只认 `BanBanned/BanPending/BanFailed`,前端无 deferred 入口。唯一出口是"该用户下次再违规"。确认 B 级复核员的结论,属于"注释承诺了、代码没实现"。

---

# 三、最终结论

**不能上生产。**

理由:NEW-1 是这批修复自己的**升级阻断** —— 新增的 `read_timeout_seconds=30` 默认值恰好等于 `migrateLockTimeoutSeconds`、只有 `Migrate` 自己申请的 5 分钟预算的十分之一,多节点部署的从节点会在抢迁移锁时被驱动切断而 FatalLog,给已有大表加 `fingerprint` / `billing_source` 列的 DDL 也会在 30 秒被掐断且不可回滚;NEW-2 则把"把 ctx 接通"这个正向改动变成 200ms 预算下的返佣静默丢失,并因 `isConnLevelError` 把 `DeadlineExceeded` 计入熔断,放大成 30 秒全站热路径丢弃。这两条必须先修。

修完之后可以上,其余(NEW-3、NEW-4、OLD-1~5,以及 A5/A2/B1/B5 的测试盲区)建议在同一轮里一并收掉,但不构成上线阻塞。
