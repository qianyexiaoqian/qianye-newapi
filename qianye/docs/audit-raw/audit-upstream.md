# upstream

以下为「上游影响与降级正确性」视角的审计结果。每条均已通过阅读源码 + 核对依赖库源码确认，`go vet ./qianye/...` 通过（无编译/vet 问题）。

---

## 1. 熔断器在「扩展库可达但查询变慢」这一唯一重要场景下永远不会打开，导致 relay 全站被拖慢

**位置**：`qianye/guard/guard.go:113`（`db.MarkSuccess()`）、`qianye/db/health.go:63`（`failStreak.Store(0)`）、`qianye/db/db.go:130`（`failStreak.Add(1) >= threshold`）、`qianye/guard/guard.go:142-145`（高水位同步降级）

**缺陷**：`MarkFailure` 要求**连续** N 次失败才开熔断，但有两条与「扩展库查询是否健康」完全无关的路径会把 `failStreak` 清零——`guard.Hot` 对任何返回 nil 的 hook 都调用 `db.MarkSuccess()`（包括纯内存 hook），以及 `probe()` 每 15 秒 Ping 成功就 `failStreak.Store(0)`。

**触发场景（可复现）**：
1. 配置 `availability.enabled: true`、`violation.enabled: true`（`hot_hook_workers: 2`、`hot_hook_queue_size: 4096`、`hot_path_timeout_ms: 200`，均为示例默认值）。
2. 让扩展 MySQL 处于「TCP 可达、写入慢」状态（例如对 `qy_violation_counter` / `qy_avail_bucket` 制造行锁堆积，或把该实例 IO 打满），使单次写入 > 200ms，但 `Ping` 仍在 3 秒内返回。
3. `violation.persist` 使用了 `gdb.WithContext(ctx)`，超时返回 `context.DeadlineExceeded` → `isConnLevelError` 命中 → `failStreak = 1`。
4. 队列中紧接着的 `availability.sample` 任务执行 `observe()`（`qianye/modules/availability/aggregate.go:87-94`，纯内存、必然返回 nil）→ `db.MarkSuccess()` → `failStreak = 0`。availability 采样是每次 relay 一条，频率远高于失败频率，`failStreak` 永远回不到 5。
5. 结果：`openUntil` 从未被设置，`healthy` 因 Ping 成功保持 true，`Available()` 恒为 true。队列消费速率被钉在 `2 workers / 200ms = 10 job/s`；当 relay QPS > 10 时 `len(queue)` 在数分钟内到达 80%，此后 **每一次** `HotAsync`（每条消费日志、每次采样）都走 `guard.go:144` 的 `Hot(name, fn)` **同步执行在 relay goroutine 上**，全站每个请求额外 +200ms 起。

**影响等级**：拒绝服务

**修复建议**：`db.MarkSuccess()` 只允许由真正执行了扩展库 IO 的调用点触发（给 `Hot` 加一个「本次是否访问了 DB」的信号，或让各 hook 显式调用 `MarkSuccess`）；`probe()` 里改为只在 `healthy` 由 false 翻回 true 时才重置 `failStreak`，Ping 成功不应清除「查询连续失败」的证据。另可把熔断判据从「连续失败数」改为窗口失败率。

---

## 2. 热路径 hook 的 `ctx` 超时形同虚设：commission / rule_refresh 的所有 DB 调用都没有带 ctx，且 DSN 没有读写超时

**位置**：
- `qianye/guard/guard.go:105-108`（创建 200ms ctx 并传给 fn）
- `qianye/modules/commission/accrual.go:205`（`gdb.Clauses(conflict).Create(&row)`）、`accrual.go:266`、`accrual.go:299`
- `qianye/modules/commission/inviter.go:89`（`model.DB.Model(&model.User{})...`）
- `qianye/modules/violation/rules.go:123`、`rules.go:133`（`reload` 内两处查询）
- `qianye/db/db.go:200-215`（`normalizeDSN` 只补 `parseTime`/`charset`）

**缺陷**：`guard.Hot` 的文档保证是「3. 在 hot_path_timeout_ms 的 ctx 下执行,超时即放弃」，但上述 hook 实现体拿到 `ctx` 后一次都没用（没有 `gdb.WithContext(ctx)`）。同时 `connect_timeout_seconds` 只用于 `db.Init` 的那一次 `PingContext`（`db.go:82-89`），没有写进 DSN，因此运行期连接的 dial/read/write 全部无超时。

**触发场景（可复现）**：
1. `commission.enabled: true`。后台结算任务 `qianye/modules/commission/settle.go:164` 开启事务，在 `settle.go:329-331` 按 id 更新 `qy_commission_accrual` 行、`settle.go:290` 对 `Balance` 行加 `FOR UPDATE`，事务期间持有这些行锁。
2. 同一时刻某条 relay 的消费日志触发 `commission.consume` → `accrueConsume` → `writeAccrual` → 对**同一个 `(idem_scope, idem_key)` 日聚合行**执行 `INSERT ... ON DUPLICATE KEY UPDATE`，阻塞在行锁上。
3. 因为没有 `WithContext(ctx)`，也没有 DSN 级 `readTimeout`，这条语句会一直等到 MySQL 的 `innodb_lock_wait_timeout`（默认 **50 秒**）。期间 `probe()` 走的是另一条连接、Ping 正常，`Available()` 保持 true。
4. 两个 worker 被这样占满 50 秒 → 4096 队列迅速填到 80% → 触发 `guard.go:142` 高水位同步降级 → 此后每个 relay 请求在 `RecordConsumeLog` 里**同步等待最长 50 秒**。网关实际不可用。

**影响等级**：拒绝服务

**修复建议**：(a) commission / violation 的所有扩展库与主库查询一律改用 `gdb.WithContext(ctx)` / `model.DB.WithContext(ctx)`，`resolveInviter` / `blockedInvitees` / `ensureRelation` / `writeAccrual` / `reload` 全部加上 ctx 参数；(b) `normalizeDSN` 追加 `timeout=<connect_timeout_seconds>s&readTimeout=<hot 上限>&writeTimeout=...`，让驱动层本身有硬上界；(c) 在 `guard.Hot` 里对超时做二次防御（例如把 fn 放到独立 goroutine + select ctx.Done()），使「hook 绝不阻塞 relay 超过 hot_path_timeout_ms」成为结构性保证而不是约定。

---

## 3. 扩展库启动期短暂不可达会让整个 new-api 拒绝启动（与 fail-open 设计直接冲突）

**位置**：`qianye/bootstrap.go:46-59` → `qianye/db/db.go:86-89`（Ping 失败 return error）→ `main.go:375-377` → `main.go:56-59`（`common.FatalLog` → `common/sys_log.go:36` `os.Exit(1)`）

**缺陷**：`qianye.Init()` 把「配置写错」和「扩展 MySQL 此刻连不上 / 迁移失败」这两类完全不同的事件都映射成同一个 fatal error。前者炸掉是合理的（代码注释也是这么说的），后者不是：扩展库是可选附加组件，`guard` 包开篇写的核心原则是「扩展绝不能拖垮主业务」，而这里扩展成了主业务的**启动期硬依赖**。

**触发场景（可复现）**：
1. `qianye.yaml` 存在且 `enabled: true`，`connect_timeout_seconds: 5`（默认）。
2. docker-compose / k8s 中扩展 MySQL 容器晚于 new-api 就绪，或该实例正在做 5 分钟的 crash recovery / 例行重启。
3. `db.Init` 的 `PingContext` 5 秒超时失败 → `Init()` 返回 error → `InitResources()` 返回 error → `FatalLog` → `os.Exit(1)`。
4. 容器进入 CrashLoopBackOff。**整个 API 网关（relay、计费、管理端）在扩展库恢复之前全部不可用**——而这恰恰是热路径 fail-open 设计要避免的情形。同理，`model.QyEnsureFundOutbox()`（`bootstrap.go:55-59`）在主库账号没有 CREATE 权限时也会让主程序无法启动。

**影响等级**：拒绝服务

**修复建议**：区分两类失败。`config.Load()` 的解析/校验错误保持 fatal；`db.Init` 的 Ping 失败、`db.Migrate` 失败、`QyEnsureFundOutbox` 失败改为记 `SysError` + 保持 `healthy=false` 后返回 nil，让 `StartHealthLoop` 在后台重试并在恢复后自动闭合（`Available()` 为 false 期间扩展 API 返回 503、热路径全部跳过，这正是既有的降级语义）。可另加一个 `runtime.fail_fast_on_boot`（默认 false）供确实想要 fail-fast 的部署显式开启。

---

## 4. `QyPruneFundOutbox` 的 `Limit` 在 PostgreSQL / SQLite 主库上被 GORM 静默忽略；在 MySQL 上又只能删 200 行/6 小时

**位置**：`model/qy_export.go:115-118`，调用方 `qianye/service/twophase/compensate.go:246`，任务注册 `qianye/bootstrap.go:103`（`6*time.Hour`）、批量大小 `qianye/config/defaults.go:32`（`BatchSize` 默认 200）

```go
res := DB.Where("created_at < ?", before).Limit(batch).Delete(&QyFundOutbox{})
```

**缺陷**：`Delete` 上的 `Limit` 是 MySQL 方言特性。已核对依赖源码：`gorm.io/driver/mysql@v1.4.3/mysql.go:52` 的 `DeleteClauses` 含 `"LIMIT"`，而 `gorm.io/driver/postgres@v1.5.2/postgres.go:50` 是 `{"DELETE","FROM","WHERE"}`、`github.com/glebarez/sqlite@v1.9.0/sqlite.go:62` 是 `{"DELETE","FROM","WHERE","RETURNING"}`、`gorm.io/gorm@v1.25.2/callbacks/callbacks.go:11` 默认也无 LIMIT——**`Limit` 子句被直接丢弃**。这违反 AGENTS.md「Do not use database-specific features without cross-DB fallback」。

**触发场景（可复现）**：
- **主库为 PostgreSQL/SQLite**：平台运行 30 天以上、`qy_fund_outbox` 累积到数十万行且所有资金单已终态时，`PruneOutbox` 生成一条不带 LIMIT 的 `DELETE FROM qy_fund_outbox WHERE created_at < $1`，一次性删除全部过期行。在主库（全平台业务库）上产生长事务、大量 WAL / 表膨胀与锁等待；日志里 `已清理 %d 行` 会打印一个远大于 200 的数字，正是「分批」语义失效的直接证据。
- **主库为 MySQL**：`Limit` 生效但每次只删 200 行，任务 6 小时跑一次 = 800 行/天。只要平台每天的划转+提现+违规退款笔数超过 800，`qy_fund_outbox` 在主库里**净增长**，保留期配置永远追不上。

**影响等级**：拒绝服务（PG/SQLite：主库长事务锁）/ 数据错误（MySQL：保留策略失效、主库表无限增长）

**修复建议**：改为按主键分批的跨库写法——先 `SELECT id FROM qy_fund_outbox WHERE created_at < ? ORDER BY id LIMIT ?` 取一批 id，再 `DELETE WHERE id IN (...)`；同时在 `PruneOutbox` 内加循环（带 `ctx.Err()` 检查与 sleep 限速，参照 `qianye/modules/availability/flush.go:283-300` 的 `deleteBefore` 写法），直到本轮删不满一批为止。

---

## 已核对且确认无问题的项（供复核参考，不算缺陷）

- `QyOnConsumeLog` / `QyOnTaskBillingLog` 确实挂在 `common.LogConsumeEnabled` 早退之前（`model/log.go:344`、`model/log.go:421`）。
- `QyPreRelayGuard` 的降级正确：默认实现原样返回 `upstreamErr`；扩展禁用时 `module.All().InstallHooks()` 根本不会执行（`qianye/bootstrap.go:42`），hook 保持 no-op；`violation.Mod.InstallHooks` 二次判 `Violation.Enabled`；扫描超时返回 `verdict{Rule:nil}` 走放行分支（`guard.go:80-84`）；`defer recoverHot` + 无名返回值使 panic 后返回 nil（fail-open）。`types.NewError` 通过 `errors.As` 原样保留 `*NewAPIError`（`relaykit/types/error.go:247`），错误码/skip-retry 不丢。
- 分组过滤返回值恒非 nil：`filterGroupKeys` 用 `make([]string, 0, ...)`；空切片被 `model.GetPerfMetricsSummaryBucketsAll`（`model/perf_metric.go:104-109`）与 `allowedGroupSet`（`pkg/perf_metrics/metrics.go:256-259`）解读为「全部过滤掉」而非「不过滤」，方向安全。`filterPricing` 对每条 `EnableGroup` 新分配切片，未写入 `model.GetPricing()` 的共享底层数组。
- 违规扣费产生的消费日志已被返佣硬排除（`qianye/modules/commission/hook.go:65-74` 读 `other["violation_fee"]`，由 `qianye/modules/violation/fee.go:222` 写入），不存在「下线违规、上线获利」。
- `GenerateClaudeOtherInfo` 内部调用 `GenerateTextOtherInfo`（`service/log_info_generate.go:279`），Claude 语义路径同样经过 `QyLogMetricsAttachReasoning`，不存在两列对 Claude 永久空白的问题。
- 划转/提现的主库改动后都调用了 `model.InvalidateUserCache`，而 quota 就存在同一个 `user:<id>` hash 里（`model/user_cache.go:151`），缓存不会残留旧余额。
