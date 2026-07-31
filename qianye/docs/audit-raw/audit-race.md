# race

我逐文件读了 `qianye/` 全部代码与上游挂载点。下面只列我能给出精确交错序列的缺陷。

---

## 缺陷 1 — 扩展库短暂不健康时,已入队的热路径作业被静默丢弃(无计数、无日志)

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\guard\guard.go:96`**(`Hot` 开头的 `if !Available() { return }`),配套 `guard.go:135`、`guard.go:150-158`

**缺陷**:`HotAsync` 在**入队时**判 `Available()`,worker 在**出队时**由 `Hot` 再判一次;两次之间状态翻转的话,作业被直接丢弃,而 `dropped` 计数器只在 `select default`(队列满)分支自增,所以这条丢失路径既不计数也不告警。

**精确触发序列**(不需要熔断,一次网络抖动即可):

1. `t=0`,relay 正常跑,`onConsumeLog` 持续调 `HotAsync("commission.consume", …)`。`Available()==true`,作业入队。假设队列内积压了 3000 条待处理的返佣事件(4096 容量,未到 80% 水位,不触发同步降级)。
2. `t=1s`,`db.StartHealthLoop` 的 `probe()` 恰好执行,对扩展库 `PingContext` 因一次 3 秒网络抖动超时 → `qianye/db/db.go:166 healthy.Store(false)`。注意这里**不经过熔断阈值**,单次 ping 失败就置位。
3. `t=1s..16s`(下一次 probe 成功前的整整一个健康周期),2 个 worker 继续从 channel 取作业,每条都走到 `guard.go:96` 的 `!Available()` → `return`。3000 条 `commission.consume` 事件全部消失。
4. `accrueConsume` 从未执行 ⇒ `qy_commission_accrual` 的当日聚合桶不会被写入/累加。`consumeEvent` 只存在于内存闭包里,**没有任何补偿路径**:`repairStrandedAccruals` 只处理 `settled→accrued`,`runTopupScan` 只覆盖充值,消费返佣没有 outbox。
5. 管理端 `/admin/.../health` 读到的 `hot_queue.dropped` 仍是 0,日志里一条 `qianye: 热路径队列已满` 都没有。

同一窗口内 `violation.persist` 的作业也这样消失:违规记录、计数推进、封号判定全部无声跳过(`recordDrops` 只在 `persist` 入口的 `db.Available()` 检查里自增,入队之后再失效就不计)。

**这与已知取舍不符**:审计范围里声明的是"极端情况下可能丢事件,已有背压与告警"。该路径既无背压(队列没满)也无告警(不计数、不打日志),而且触发条件是一次 3 秒的 ping 超时,不是极端情况。`guard.go:152` 的注释"丢弃是'用户该拿的钱没拿到'的唯一路径"是错的。

**影响**:资损(邀请人佣金永久丢失)+ 可观测性失效

**修复建议**:worker 侧不要复用 `Hot` 的 `Available()` 短路。给 `Hot` 拆一个 `hotRun(name, fn)`(只做 panic 拦截 + 超时 + 错误处理,不判可用性),worker 循环里改调它;若确实要在不可用时跳过,必须走与 `dropped` 同级的计数器 + 限频告警(例如 `skipped` / `qianye: 扩展库不可用,已跳过 N 个已入队事件`)。更彻底的做法是把 `commission.consume` 事件先落一张扩展库的 pending 表再异步消费,但那超出本次改动预算,至少要先把丢失变成可见。

---

## 缺陷 2 — 违规计数"跨越阈值"的信号被消费掉却没有落任何痕迹,封号在整个滚动窗口内永久丢失

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules\violation\counter.go:186`**(`banRateExceeded()` 分支的 `return false`),同类问题在 `counter.go:189-191`(`claimBan` 出错/超时)与 `counter.go:173-178`(影子模式分支)

**缺陷**:`crossedThreshold`(`counter.go:97`)判的是"恰好跨越"——`after >= threshold && after-weight < threshold`,一个 ban_cycle 内**只会为真一次**。而 `maybeAutoBan` 在三个分支上直接 `return false`,不写 `qy_violation_ban`、不留任何待办;`runBanCompensate`(`tasks.go:90-97`)只扫 `qy_violation_ban` 里已存在的 `pending/failed` 行,因此对"从未认领成功"的跨越完全无能为力。

**精确触发序列 A(速率闸,确定性可复现)**:

1. 配置 `auto_ban_threshold=5`、`global_ban_rate_limit_per_hour=10`、`shadow_mode=false`。
2. 一小时内已有 10 个用户被自动封禁,`banWinCount==10`。
3. 用户 X 第 5 次违规:`bumpCounter` 事务提交,`hit_count=5`,`crossedThreshold(5,1,5)` → `true`。
4. `maybeAutoBan` → `banRateExceeded()` → `banWinCount(10) >= limit(10)` → 打一行日志"封号被推迟为人工处理" → `return false`。**没有插入 `qy_violation_ban`**。
5. 同时 `banRateExceeded` 内部调了 `tripShadow`(`breaker.go:128`),`forcedShadowUntil = now+1800`。
6. 用户 X 第 6 次违规:`hit_count=6`,`crossedThreshold(6,1,5)` = `6>=5 && 5<5` → **false**。此后在 24 小时窗口内(`auto_ban_window_hours` 默认 24)`Crossed` 再也不会为真。
7. 结果:X 在接下来 24 小时里可以无限违规,永不被自动封禁;`qy_violation_ban` 里查不到这条待办,补偿任务也不知道它存在。同时步骤 5 触发的 30 分钟强制影子模式,让**这段时间内所有跨过阈值的用户**都走 `counter.go:173` 分支,同样把各自的 `Crossed` 信号消耗掉,同样不可恢复。

`counter.go:180-181` 的注释明确写着"速率闸在认领之前:超限时连认领都不做,**这样恢复后仍能正常触发**"——实现与这句声明不符。

**精确触发序列 B(并发/超时,同一根因)**:

1. `persist` 的闭包跑在 `guard.Hot` 的 200ms ctx 下(`hot_path_timeout_ms` 默认 200)。
2. `guard.go:230` `bumpCounter(ctx, …)` 事务提交成功,`Crossed=true`,耗时 180ms(同一用户的并发违规在 `qy_violation_counter` 行锁上排队)。
3. `guard.go:234` 的 `gdb.WithContext(ctx).Model(&Record{}).Updates(...)` 在第 205ms 撞上 deadline → 返回 `context.DeadlineExceeded` → `persist` 的 fn 直接 `return err`,**`maybeAutoBan` 根本没被调用**。
4. 计数已经提交、跨越已经发生、封号永远不会到来,`qy_violation_ban` 无行,补偿任务无从下手。

**影响**:资损 / 风控失效(自动封号静默失效,违规用户继续消费)

**修复建议**:把"跨越"变成持久化事实而不是一次性内存信号 —— 在 `bumpCounter` 判定 `Crossed` 之后**无条件先 `claimBan` 落一行 `qy_violation_ban{status: pending}`**,再由速率闸/影子模式/执行失败决定把它标成 `deferred`/`skipped`/`banned`;`runBanCompensate` 的扫描集合加上 `deferred`。这样"是否封"是可撤销的决策,而"阈值被跨过"是不可丢失的事实。退一步的最小修复:把 `crossedThreshold` 改成"跨越 ∨(已达阈值 ∧ 本 cycle 无 ban 行)",让后续违规能重新触发认领。

---

## 缺陷 3 — `guard.Hot` 承诺的"200ms 超时即放弃"对非 ctx 感知的调用完全无效;高水位同步降级会把完整封号流程压到 relay 线程上

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\guard\guard.go:142`**(高水位同步降级),受害路径 **`qianye\modules\violation\ban.go:47`** 与 **`ban.go:80`**

**缺陷**:`Hot` 的文档保证 3 是"在 hot_path_timeout_ms 的 ctx 下执行,超时即放弃"。但 ctx 只能约束显式 `WithContext` 的调用。`disableUserForViolation` 全程不带 ctx:`model.DB.Transaction`(`ban.go:47`)、`PublishUserAuthCache`、`InvalidateUserTokensCache`、`RevokeAllUserSessions`(`ban.go:80`)、`RecordLogWithAdminInfo`(`ban.go:84`)。其中 `model.RevokeAllUserSessions` → `revokeUserSessions`(`model/user_session.go:712`)是一个**无上限的 `for` 批循环**,每批一次 `Find` + 逐行 Redis deny-fence 写 + 一个带 `FOR UPDATE` 的主库事务。

**精确触发序列**:

1. 站点 QPS 数百,`commission.enabled=true`。每条消费日志投一个 `commission.consume`(3~4 次 DB 往返),worker 只有 2 个(`hot_hook_workers` 默认 2),吞吐约 400-1000 job/s。
2. 扩展库出现一次慢查询(或跨机房 RTT 抖到 20ms),worker 吞吐跌到 ~100 job/s,队列水位在几秒内越过 3277(4096×80%)。
3. 此后每一个 relay goroutine 走 `guard.go:142` 的高水位分支 → `Hot(name, fn)` **同步执行**。
4. 恰好此时用户 X 的违规计数跨过阈值:X 自己的这次 relay 请求在自己的 goroutine 上依次执行 `Create(record)` → `Create(payload)` → `bumpCounter` 事务 → `claimBan` → **`disableUserForViolation`**:主库事务 + `IncrementUserAuthVersionWithTx` + Redis publish + token 缓存失效 + 遍历 X 的全部会话逐批撤销 + 写主库日志。
5. `Hot` 的 200ms ctx 在第 4 步中途早已到期,但没有任何一个调用会因此返回 —— 它们都不接 ctx。这条 relay 请求被阻塞的时长完全取决于主库和 Redis 的响应,没有上界。X 会话越多、主库越慢,阻塞越久。

也就是说"扩展绝不能拖垮主业务"这条核心原则在同步降级路径上被 `violation` 模块单方面破坏了。

**影响**:拒绝服务(relay 请求无界阻塞)

**修复建议**:两条一起做。(a) `guard.Hot` 的同步路径必须只承担"可被 ctx 打断"的工作;给 `HotAsync` 加一个 `NoSyncFallback` 语义(或按 name 白名单),让 `violation.persist` 这类含跨库副作用的 job 在高水位时宁可丢弃/降级为只写记录,也不进同步分支。(b) `disableUserForViolation` 全链路接 ctx:`model.DB.WithContext(ctx).Transaction(...)`,并把 `RevokeAllUserSessions` 这类不可控步骤移出封号的同步链(标记后由 `runBanCompensate` 补做),否则它在 worker 里同样会撑爆 200ms 预算并触发缺陷 2 的序列 B。

---

## 缺陷 4 — `QueueStats` 无同步读取 `queue`,与 `queueOnce.Do` 里的写构成数据竞争

**`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\guard\guard.go:186-187`**

**缺陷**:`queue` 是包级变量,唯一的写在 `startWorkers` 的 `queueOnce.Do`(`guard.go:172`)。`HotAsync` 读它之前必定先调 `startWorkers()`,由 `sync.Once` 建立 happens-before;但 `QueueStats()` **不调 `startWorkers`**,直接 `cap(queue)` / `len(queue)`。

**精确触发序列**:进程刚启动、还没有任何 relay 流量(`HotAsync` 未被调用过,`queueOnce` 未执行)。管理员打开健康面板 → `qianye/controller/admin.go:27` 的 `guard.QueueStats()` 在 goroutine A 上读 `queue`;与此同时第一个 relay 请求在 goroutine B 上进入 `HotAsync` → `startWorkers()` → `queueOnce.Do` 内执行 `queue = make(chan hotJob, size)`。两者对同一个变量的无同步读/写,`go test -race` 必报 `DATA RACE`。amd64 上实际后果只是面板偶尔显示 `capacity: 0, pending: 0`,但按 Go 内存模型这是未定义行为。

**影响**:代码质量(题目已说明 `-race` 未跑过,这是 `-race` 一定会报的一处)

**修复建议**:`QueueStats()` 开头加 `startWorkers()`;或把 `queue` 换成 `atomic.Pointer[chan hotJob]` / 在 `init` 里用固定容量创建。前者一行改动即可。

---

## 已按要求逐条核验、确认**不是**缺陷的几处

写在这里是因为任务点名要求验证,不作为缺陷计:

- **`qianye/modules/groupvis/filter.go:40-79` 的"不污染全局定价缓存"断言成立**。逐行核验:`for _, item := range pricing` 拿到的是 `model.Pricing` 结构体值拷贝;`visible := make([]string, 0, len(item.EnableGroup))`(`filter.go:43`)是全新分配,`cap` 恰为源长度,后续 `append` 只可能扩容到新数组,永远不会写进 `model.pricingMap` 共享的底层数组;`item.EnableGroup = visible`(`filter.go:75`)只改拷贝的 slice header;`sort.Strings(visible)` 排的是新数组。`filterGroupKeys`/`filterPerfGroups` 同样是 `make` + `append` 到新切片,不写入参。上游 `controller/pricing.go:20` 的 `filterPricingByUsableGroups` 也只做值拷贝 append。`model.Pricing` 的其余引用字段(`SupportedEndpointTypes`)虽然仍与全局缓存共享底层数组,但本文件没有任何路径写它。**无缺陷,断言准确。**
- **`qianye/modules/violation/counter.go:37-86` 的 `bumpCounter` 并发唯一性成立**。`INSERT ... ON DUPLICATE KEY UPDATE` 对该行取排他锁并持有到提交,后续同事务的 `Take` 必然读到本次推进后的值;两个并发 bump 从 4 推到 5、6 时,`crossedThreshold` 只对第一个为真。`claimBan` 的 `(user_id, ban_cycle)` 唯一索引也确实杜绝了跨节点重复封号。真正的问题是跨越信号丢失(缺陷 2),不是重复触发。
- **`qianye/modules/commission/settle.go:320-347` 的 `absorbAccruals` CAS 足以覆盖"后台 leased 结算"与"管理端 `POST /commission/settle` 立即结算"的并发**。两者都先 `lockBalance` 取行锁,后到者的 `WHERE id=? AND settled_amount=?` 必然 `RowsAffected==0` → 整批回滚,不会出现发一半。
- **`qianye/service/lease/lease.go` 的 fence 目前是"死参数"**:`Run` 的 `fn func(ctx context.Context)` 签名没有把 fence 交出去,`Acquire` 文档里"老持有者恢复后…写入都会失败"这句在实现上不成立,唯一的保护是 ctx 取消(窗口最长 `renewEvery`,默认 ttl/3 ≈ 20 秒)。但我逐个核验了全部 8 个 leased 任务(`twophase.Compensate`/`PruneOutbox`、`availability.rollup`/`cleanup`、`commission.settle`/`topup_scan`、`transfer.reconcile`、`withdraw.reconcile`、`violation.retention_gc`/`ban_compensate`),每一个的写入都是 CAS(`WHERE status=pending`)、覆盖语义幂等 upsert、或幂等删除,双跑窗口内不会写脏。因此不构成活跃缺陷,但 fence 给人的安全感是虚的 —— 将来新增 leased 任务时若假设"fence 会挡住老持有者",会直接出资损。建议要么把 fence 透传给 `fn` 并在关键写入上加 `AND fence = ?`,要么把注释改成如实描述。
- **`qianye/modules/commission/accrual.go:284-313` 的 `blockedInvitees()` 返回共享 map 但无原地修改**:新 map 在 `blockedSet = m` 之前完整构建,发布后只读;`invalidateBlocked` 只置 nil。同理 `qianye/modules/availability/query.go:356-372` 的 `groupOfferedModels`。均无竞争。
- **`config.Reload` 的多次 `Get()`**:我找过依赖"同一请求内配置不变"的逻辑。`transfer.create`(`service.go:29`)、`withdraw.create`(`create.go:33`)都在入口取了一次 `cfg` 并向下传递;`reserveRisk`(`risk.go:46`)与 `freezeRates`/`minFiatAmount` 会各自重读快照,理论上能在一次热重载中混用新旧值,但混的都是限额/汇率这类独立参数,不会破坏任何守恒关系(费用与金额始终来自同一次 `acceptCreate`)。**未发现会导致错误结果的实例。**
- **初始化顺序**:`main.go:375` 的 `qianye.Init()` 在 `InitResources()` 内,而 `InitResources()` 在 `main.go:55` 被调用,早于 `main.go:155` 的 `StartBackgroundTasks()` 和 `main.go:202` 的 `RegisterRoutes()`。hook 变量的赋值确实先于任何 HTTP 请求与后台协程,`bootstrap.go:92` 还有 `db.Get() == nil` 的兜底。**无未初始化窗口。**
