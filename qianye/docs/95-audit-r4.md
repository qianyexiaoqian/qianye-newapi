# 95 — 第四轮复核汇总(r4)

复核对象:工作区当前状态(HEAD = `01f3d7cd`,叠加约 30 个已修改文件 + 16 个未跟踪文件的未提交改动)。
五个视角(money / wiring / concurrency / regression / testquality)共提出 21 条,经两名独立怀疑者逐条反驳后存活 19 条,去重合并为 **15 条**。

复核基线(本文所有结论都建立在这三条实测之上):

```
go build ./...              → 通过,无输出
go test ./qianye/... -count=1 → 18 个包全部 ok(sitetheme 为 [no test files])
git status                   → 无遗留探针文件,前几轮各 agent 的临时 *_probe_test.go 均已清理
```

> **诚实前置**:本轮**没有发现任何正在造成资金损失的活缺陷**。15 条里只有 1 条 P1,而它需要管理员主动发起一次绕过前端的 API 调用才能触发。其余多为断链、测试缺口、缓存口径漂移。定级按"是否需要在上线前同批收掉"给出,不按"听起来多严重"给出。

---

## 一、去重结果

原始 19 条经交叉比对,有 4 组是同一缺陷的不同视角描述,合并后为 15 条:

| 合并后 | 原始条目 | 合并理由 |
|---|---|---|
| **M5** | wiring-A2 + concurrency-A5 | 同一行代码(`sitetheme/store.go:72` 无条件 `snapshot.Store`),前者从"读库失败"入手、后者从"熔断打开"入手,是同一个缺陷的两条触发路径 |
| **M8** | concurrency-A2 + regression-A2 | 同一行代码(`commission/settings.go:146` 写缓存无代次校验),前者定性为并发缺陷、后者定性为 r3 修复引入的回归,两者都对 |
| **M9** | concurrency-A3 + testquality-A5 | 同一形状(临界区内做扩展库 I/O),前者点 `usergroup/currentDefaultGroup`、后者点 `commission/groupRates`;复核中另外查出第三份 `commission/blockedInvitees`,三份合并 |
| **M10** | concurrency-A6 + regression-A1 | 同一行代码(`db/migrate.go:106` 从节点返回裸 `nil` 而非 `errMigrationInProgress`) |

被两名怀疑者共同推翻、**本轮不予采纳**的 2 条(下轮不要重提):

- `guard.Feature` / `withdraw.isTerminal` / `qymodel.IsTerminal` 零消费方 —— 三条支撑论据全部被源码推翻:`Feature` 是**调用** `featureOn` 而非重写它,展开后与 `RequireAPI` 的谓词逐项等价;`isTerminal` 是从 `terminalStatuses` 遍历派生的,结构上不可能漂移;`status_test.go:53` 的 `TestTerminalDefinitionMatchesTransitionTable` 正是它的漂移守卫。按此发现动手会删掉一个真实的状态机不变量守卫。
- `StatusRiskHold` / `AppealWithdrawn` / `AuditCategoryAdmin` 三个常量零消费方 —— 取证 grep 显式排除了 `./web` 却据此下"全仓零消费方"的结论。三个值在前端都有真实消费方(状态下拉、徽章映射、i18n 词条),后端也有按它筛选的查询路径(`commission/api_admin.go:442`)。剩余事实仅为"后端尚未写入这三个预留枚举值",属设计预留。

---

## 二、存活发现清单

### P1(必须在上线前修)

#### M1 — `markPaid` 对提现方式无服务端校验,`quota` 单被它核销后主库额度一分未加

- 文件:`qianye/modules/withdraw/review.go:151`
- 形状:前端校验未在后端复做

`markPaid` 只做 `approved → paid` 的状态机校验,全文没有任何 `w.Method` 判断(实测 `review.go` 里唯一出现 `Method` 的地方是 299 行的审计快照)。对 `method=quota` 的单执行时,它会调 `commission.SettleFrozen` 把佣金从 `frozen` 转成 `withdrawn`,而这条路径**全程不碰主库 `users.quota`**。

两名怀疑者各自从真实 HTTP handler(`handleAdminMarkPaid`,不是内部函数)复现:HTTP 200、单据 `method=quota` → `status=paid`、`qy_commission_balance` 的 `frozen 500000→0 / withdrawn 0→500000`、主库 `users.quota` 全程未动。

为什么这是硬约束:

- `paid` 是终态,`allowedTransitions` 无出边,无反向接口;`reconcile` 只扫 `approved` 与 `paying`,一张 `order_no` 为空的 `paid` 单不会被任何对账逻辑再看一眼 —— **静默、不可逆、无检测**。
- `creditQuota` 的调用点只有 `credit.go:37`(定义)、`reconcile.go:78`、`review.go:102`,`markPaid` 不在其中。
- 唯一的方法闸在前端:`review-dialog.tsx:311` 的 `{withdrawal.method === 'fiat' && (...)}`。而 `withdraw/validate.go:96` 自己写着「前端的每一条校验后端都要再做一遍:前端校验只是体验,绕过它只需要一个 curl」。
- 不对称很说明问题:quota 侧有 `shouldAutoCredit` 的 method 闸,fiat 侧一个都没有。
- 设计文档 `design-03-withdraw.md:329` 明写 T10 `approved → paid` 的触发者是「管理员(fiat)」——这是**违反设计文档**,不是有意为之。

**触发难度(诚实评估)**:需要管理员凭证 + 一个带非空 `payout_ref` 的请求(空 ref 被 `errPayoutRefMissing` 挡掉),不是误点能产生的;产品自己的前端永远发不出这个请求。默认配置(`AutoCredit()` 默认 `true`)下 `method=quota ∧ status=approved` 是瞬态。现实的三条到达路径:(a) 运维脚本遍历 `approved` 队列批量"标记已打款"而不带 method 过滤;(b) 前端重构掉那个 `method === 'fiat'` 条件;(c) **把 `auto_credit_on_approve` 设为 false(即 M2)——此时 quota 单永久卡在 approved,mark-paid 成为管理端唯一看起来可用的"正向终态"按钮**。

**定为 P1 而非 P2 的理由**:修复是一行,而后果是不可逆、无检测的资金核销;M2 会把它从"需刻意误操作"变成"善意运维也会踩"。

**修法**:

```go
if w.Method != config.WithdrawMethodFiat {
    return nil, errIllegalTransition   // 与 acceptCreate 同口径
}
```

`markFailed` 不需要同样的断言(它的 `UnfreezeForWithdraw` 语义对两种方式都正确)。回归测试直接驱动 `markPaid`,断言 `method=quota` 时 `Balance` 一分未动。

---

### P2(建议同批收掉;逐条注明是否阻断上线)

#### M2 — `auto_credit_on_approve: false` 时 quota 提现没有任何**正确的**完成路径

- 文件:`qianye/modules/withdraw/reconcile.go:60`
- 形状:`*bool` 配置项的 `false` 分支零实现(断链)
- 触发前提:**运维显式改非默认值**

`approved → paying` 是 quota 单到 `paid` 的唯一入边,穷举 `applyTransition` 的 8 个调用点后确认:进入 `paying` 的边只有 `credit.go:107` 一条,只能经 `creditQuota` 到达。而 `creditQuota` 的两个调用点(`review.go:102` 的 `shouldAutoCredit`、`reconcile.go:60` 的 `resumeApproved`)都被 `config.Withdraw.AutoCredit()` 门住。开关置 false 后,quota 提现整条链路静默失效。

`twophase` 补偿救不回来:`resolveAfterCompensation → finishPaid` 要求 `status=paying`,而 `creditQuota` 从未被调用则资金单根本不存在,resolver 永不触发。

对原始报告的两处措辞修正(不影响结论):

- 「没有任何完成路径」字面不成立 —— `markPaid` 也能点,只是它只 `SettleFrozen` 不加额度,即 M1。准确表述是「没有**正确的**完成路径」。
- 「佣金永久冻结」也不严格 —— `markFailed` 会 `UnfreezeForWithdraw` 退回佣金,且管理端 UI 对 `approved` 单**无条件**渲染这个按钮。资金有出口。

**不阻断上线**(默认值为 true,`data/qianye.yaml` 与 `data/qianye-prod.yaml` 都没覆盖它)。但 `config/validate.go:263` 的 `validateWithdraw` 校验了「fiat 必须配 pii_key」这类跨字段矛盾,却没有校验 `methods:["quota"] + auto_credit_on_approve:false` 这个组合;`qianye.example.yaml:167` 也只是裸列一个布尔项,无任何反向提示。

**修法**(二选一,并写进配置文档):(a) 增加管理端「立即兑现」接口,对 approved 的 quota 单显式触发 `creditQuota`(它本身幂等,`startPaying` 的 CAS 就是准入闸);(b) 若该开关的语义就是「不支持 quota 方式」,则在 `validateWithdraw` 里把这个组合判成非法并 `FatalLog`,而不是让它静默变成一个吃钱的稳态。

#### M3 — 退款冲正复制的是「该下线最近一条正额计佣行」的费率,不是被退款那笔消费的费率

- 文件:`qianye/modules/commission/clawback.go:46`
- 形状:注释与代码脱节
- 触发前提:**运营开启 `commission.refund_clawback`(默认 false)**

```go
gdb.Where("invitee_id = ? AND gross_amount > 0", inviteeId).Order("id desc").Take(&origin)
amount := calcGross(refundQuota, origin.RateUnits)
```

选行条件里没有 `source_type`、没有 `bucket_date`。默认配置下充值 10%、消费 5%(`config/defaults.go:12-13`),所以一笔任务退款会按 10% 冲正一笔当初只按 5% 发出去的佣金 —— **正好 2 倍超额冲正**;反过来若消费费率配得高于充值费率,则是冲正不足,平台永久少收回一部分。

两名怀疑者独立复现(seed `SourceConsume/RateUnits=500/gross=500`(id=1)+ `SourceTopup/RateUnits=1000/gross=1000`(id=2),调 `clawback(ctx, 900, 10000, ...)`):落出 `gross=-1000 rate_units=1000 ref_accrual_id=2`;只有消费行的对照组落出 `-500`;把两行顺序对调后落出 `-500` —— 证明结果纯粹由 id 大小决定、与来源无关。

加重情节:

- **暴露窗口是数小时级而非瞬时竞态**。消费行走 `accrual.go:214-224` 的日聚合 `DoUpdates`,当天首次创建后 id 固定不变,同日之后落的任何充值行 id 必然更大,并保持「最大正额 id」直到次日首个消费桶落行。
- **触发频次比原文更高**。不只任务失败退款(`task_polling.go:86/307/598`),**任务成功路径同样发退款日志** —— `task_polling.go:651/656 → RecalculateTaskQuota`,`task_billing.go:252-255` 在 `quotaDelta<0`(预扣多于实际,视频/MJ 常态)时也打 `model.LogTypeRefund`。
- **审计链路也被污染**:`RefAccrualId` 挂到了无关的充值行,管理端「这笔被冲了多少」的溯源是错的。
- `remaining` 不兜底(充值 1000 + 消费 500 = 1500 > 1000,实测未被削减);`clawback` 行 `gross<0` 被 `gross_amount>0` 排除,不会级联。
- 注释 `clawback.go:80-84` 写着「冲正原样复制原单冻结的费率与分组,绝不用当前值」—— 它确实没用当前值,但复制的是**另一单**的值。**r3 审过这一行并判「方向正确」,正是被这句注释误导。**

设计文档自相矛盾:`design-02-commission.md:846-847` 写「查该 invitee 最近的可冲正 accrual」(§8.4 抄了近路),但同文档 :584 写「min(原单已发佣金, 按退款基数×当时费率)」、`design-00-foundation.md:729` 写「RefType/RefId 是佣金冲正找原单的依据」。设计写了不等于 2 倍冲正正确。

**不阻断上线**(`refund_clawback` 默认 false,且是 `yaml_readonly`)。但一旦运营为防「充值→拿佣金→退款」套利而打开它,就是普通用量下系统性的、append-only 不可更正的账本错误。

**修法**:把 `SourceType` 纳入选行条件(`AND source_type = SourceConsume`),并优先按 `bucket_date` 命中被退款那一天的日聚合行。若取不到消费行,宁可回落到 `resolveRate(ctx, e.InviteeGroup, SourceConsume, s)` 的当前消费费率,也不能拿 topup 行的费率。回归测试直接钉住「消费退款的冲正费率 == 消费费率」,而不是只测 `calcGross`。

#### M4 — sitetheme 管理端接口零前端消费方,「站点默认主题可后台设置」在界面上根本不存在

- 文件:`qianye/modules/sitetheme/sitetheme.go:29`
- 形状:**断链的镜像方向 —— 写端零生产方**
- 触发前提:**100% 的部署必然如此**,无需任何配置或竞态

后端完整可达:`router.go:58` 遍历 `module.All()` 把 GET/PUT `/api/qy/admin/site-theme` 挂在 `/admin` 下并套 `middleware.AdminAuth()`;`modules.go:16` 有 blank import;`api.go` 有完整校验、`ErrUnknownPreset`、`audit.Write`。

前端零写入(我复核了整个 `web/` 目录,不限 `web/src`,排除 `node_modules`):`site-theme|site_theme` 只有 7 处命中,全是**读侧**(`__root.tsx:33`、`theme-customization-provider.tsx:45`、`qy-site-theme-sync.tsx:1`、`site-theme.ts:18,19` 的 localStorage 键、`use-qy-site-theme.ts:14,20`)。

决定性佐证:

- `allowed_presets` / `upstream_default` 在整个 `web/src` **零命中** —— 而这两个字段只存在于 `handleGetSiteTheme` 的响应里。**等于证明管理端 GET 也是死的,不止 PUT。**
- `web/src/routes/_authenticated/qy/admin/` 恰 13 个页面目录,`nav.ts:79-129` 的 `ADMIN_PAGES` 恰 13 项,均无主题项。
- 排除的所有旁路:无 YAML 开关(`qianye.example.yaml` / `data/qianye.yaml` / `data/qianye-prod.yaml` grep `theme|preset` 全空);无通用 `qy_settings` 编辑页(其他 scope 都有专属页);未塞进上游个性化设置页(`GET /api/qy/config` 的消费者只有读侧);`qianye/docs/` 全库无 `site-theme` 字样。
- `git show --name-only 01f3d7cd -- web/`:同一个 commit 为 group-pricing 交付了完整的 20+ 文件管理端(api.ts / form-sheet / table / 路由 / nav 项 / i18n),而 site-theme 只有读取侧文件。**同一次提交、两个功能、只有一个写入 UI。**
- 不是有意延后的运维口:`store.go` 的注释原文是「AllowedPresets 返回全部合法预设,**供管理端下拉与校验共用同一份事实**」「改不改由管理员在后台点」;commit 01f3d7cd 正文写的是「现在由超级管理员在后台设置」。下拉框是设计过的,只是没建。

后果:`qy_settings` 的 `site_theme.default_preset` 恒为未配置,`Current()` 恒返回 `"default"`,已接好的读取链(`__root.tsx → QySiteThemeSync → useQySiteTheme → /api/qy/config → qyCacheSiteTheme → localStorage → qyResolveDefaultPreset`)永远只拿到上游默认。整个消费半边是对着一个任何 UI 都无法写入的生产半边建的。

**不阻断上线**(退化行为恰好等于文档承诺的降级,零资金/安全/数据影响,存在绕行:一次 PUT 或一行 `qy_settings`)。但 commit 把它作为标题功能宣告,写入侧交付率为 0%,运营会误以为该功能已存在。

**修法**:补一个 `/qy/admin/site-theme` 管理页(下拉用 GET 返回的 `allowed_presets`,保存打 PUT),在 `nav.ts` 的 `ADMIN_PAGES` 加一项;或若确认本版不上前端,把这两条路由与 `store.save` 一并移除,别留一个只有后端的半截功能。

#### M6 — `twophase.Execute` 收下 ctx 却一次都没用,整条跨库资金链路没有任何语句级超时

- 文件:`qianye/service/twophase/execute.go:153`
- 形状:**断链(ctx 专门构造、三处调用、零消费)**
- 触发前提:扩展库或主库进入病态(行锁排队 / 连接池排队)

实测:`grep -n "ctx" qianye/service/twophase/execute.go` **只输出签名那一行**。把签名改成 `func Execute(_ context.Context, req Request)` 后 `go build ./qianye/...` 退出码 0 —— 未使用参数编译通过,证明 ctx 零引用。`qianye/service/twophase/` 与 `qianye/modules/transfer/` 两棵树里 `WithContext` 零命中。

三个调用方都专门造了预算:`transfer/service.go:75+105`、`violation/api_admin.go:563+566`、`withdraw/credit.go:66`,`guard.ColdContext` 取 `ColdPathTimeoutMs`(默认 3000ms)。`compensate.go` 的 43/105/126/175/190/232/284 七处同样把 lease ctx 只用于 `ctx.Err()` 循环检查,GORM 调用一条都没接。

**这是有约定、有先例、资金主路径独漏的断链**,不是被误读的注释:全仓 39 处非测试 `WithContext` 调用点都透传了 ctx;`commission/accrual.go:166-167` 原文即为「ctx 必须一路传到 GORM 调用上:热路径 worker 的 200ms 上界只对 `WithContext(ctx)` 的语句生效,漏接就会一直等到 `innodb_lock_wait_timeout`」。

对原始报告的三处修正:

- 标题「没有任何语句级超时」不成立 —— 扩展库有 DSN `readTimeout=30s`(`db.go:293`),主库行锁有 `innodb_lock_wait_timeout` 50 秒。危害是「30-50 秒而非 3 秒」,不是无界。
- 真正无界的是**连接池取连接的等待**:`database/sql` 等待空闲连接是在 ctx 上 select 的,`context.Background()` 下永不触发,`readTimeout` 只在取到连接**之后**才生效。
- Go 是 goroutine-per-request、无固定 worker 池,「gin worker 被钉住」被夸大;真实成本是数据库连接。主库等待期间扩展库连接也未被钉住(`createOrLoadOrder` 的事务已提交并归还)。

**不阻断上线**(无资金正确性损失,两阶段本身有 `pending → 补偿`收敛,上界被 MySQL 默认兜住)。

**修法与一个必须避开的新坑**:正向路径 `gdb.WithContext(ctx)` + `model.DB.WithContext(ctx).Transaction`;但**失败落库路径不能用同一个已过期的 ctx** —— 若 `applyOnMainDB` 正是因预算耗尽而失败,`markFailed`/`markSuccess`/`reloadStatus` 用同一个 ctx 会立即 `DeadlineExceeded`,单据停在 `Pending`,而 `transfer/service.go:118` 的 `releaseOnFailure` 只在 `order.Status == Failed` 时回滚风控预占,每笔慢划转都会变成人工单。收尾写必须换一个新建的短 ctx 或 `context.WithoutCancel`。`transfer/service.go:74` 自己也写着「不该中断一笔已经在动钱的操作」。

#### M7 — 两个规则热刷新把 guard worker 的 ctx 整个丢掉

- 文件:`qianye/modules/violation/rules.go:116` + `qianye/modules/grouppricing/rules.go:178`(逐字相同的第二份拷贝)
- 形状:断链 + 第 N 份拷贝
- 触发前提:扩展库进入病态

```go
guard.HotAsync("violation.rule_refresh", func(ctx context.Context) error {
    return reload(false)          // ctx 收到即丢
})
...
func reload(force bool) error     // 签名里没有 context —— ctx 在语法上不可能到达 GORM
```

实测两处 `reload` 签名确认无 context 参数(`violation:123` / `grouppricing:189`),函数体内 `violation:130/:140` 与 `grouppricing:197/:210` 均为裸 `db.Get()` 句柄。`guard.HotAsync` 承诺的 `hot_async_timeout_ms`(3 秒)对这两条语句完全无效。

默认值实测吻合:`HotAsyncTimeoutMs=3000`、`HotHookWorkers=2`、`HotHookQueueSize=4096`、`ReadTimeoutSeconds=30`;两模块的 `RuleCacheSeconds` 均为 60 —— 同周期、同由 relay 流量触发、天然相位锁定,容易同时占满仅有的 2 个 worker。

次生问题:这两条语句不带 ctx,`db.WithOpProbe` 的 `noteOp` 只读 `tx.Statement.Context`,认不到它们 —— `hotRunWithBudget` 只会在失败时 `MarkFailure`、成功时永不 `MarkSuccess`,熔断的健康票被单向截断。(这一子问题部分属既定设计:`WithOpProbe` 注释明写「没接 ctx 的调用……也不该有资格给熔断投健康票」,真正的缺陷只是 `reload` 本就该带 ctx。)

无兜底:两模块都没有 `lease.Run` 的 `rule_refresh` 任务(注释明写是刻意不用,否则其余节点规则永远陈旧),`HotAsync` 是稳态下唯一的周期刷新路径;`commission` 无 outbox,`guard.go:228` 那句「丢弃是用户该拿的钱没拿到的唯一路径」的注释与代码一致。

尾部危害的现实边界(诚实):要真正走到 `default:` 丢弃分支,需在 30 秒内填满 4096 槽,即持续约 137 req/s 的带邀请关系流量;低于此只是延迟入账。且 `Violation.Enabled` / `GroupPricing.Enabled` 是裸 bool(非 `boolOr(...,true)` 那批),默认 false。真实停顿下健康探测(15s 周期)也会先把 `healthy` 置 false,把事件转到 `recordSkip` 分支。

**不阻断上线**。**修法**:`func reload(ctx context.Context, force bool) error` + `gdb = gdb.WithContext(ctx)`,闭包直传;四个冷路径调用点(`violation/api_admin.go:214`、`violation/module.go:51`、`grouppricing/api_admin.go:344`、`grouppricing/grouppricing.go:114`)改传 `guard.ColdContext`。注意 `grouppricing/rules_test.go:173-263` 与 `hook_test.go:223-237` 直接调 `reload(true)`,需同步改签名;这些测试跑 SQLite(无 readTimeout),**现有测试无法覆盖该回归**。

#### M9 — 互斥锁临界区内做扩展库 I/O 的第 2/3/4 份拷贝

- 文件:`qianye/modules/usergroup/settings.go:45`、`qianye/modules/commission/grouprate.go:107`、`qianye/modules/commission/accrual.go:311`
- 形状:**同一修复在同模块/同概念上只落地了一份**
- 触发前提:扩展库「可达但慢」(单条 SELECT 打爆预算)

r3 的 F4 只把 `commission` 的 `effectiveCtx` 与 `refSalt` 挪出了临界区,并各配了运行时探针测试(`TestEffectiveQueriesOutsideSettingsLock` / `TestRefSaltQueriesOutsideItsLock`,我实跑均 PASS)。**同一结论在另外三处都没有落地,且没有任何测试探测那三把锁**:

| 拷贝 | 位置 | 是否持锁查库 | 失败时是否推进时间戳(负缓存) | 严重度 |
|---|---|---|---|---|
| `currentDefaultGroup` | `usergroup/settings.go:45-57` | 是 | **否** —— 失败路径直接 return,下一次注册必然再查一次 | P2 |
| `groupRates` | `commission/grouprate.go:107-135` | 是 | 沿用旧快照 / 空表 | P3 |
| `blockedInvitees` | `commission/accrual.go:311-337` | 是 | 沿用旧快照 / 空 map | P3 |

`usergroup` 那一份是最严重的:它挂在 `model/user.go:541` 的 `prepareForInsert`,位于 `DB.Transaction(user.go:594)` + `withNormalizedEmailLock` **内部**。慢查询时全进程所有注册在 `cacheMu` 上排成一队,每个约 200ms(≈5 次/秒上限),每一个都攥着一个已打开的主库事务,外加 MySQL 上对尚不存在邮箱的 `SELECT ... FOR UPDATE` 间隙锁。

实测(临时探针,已删):(1) 在 `qy_settings` 的 `Before("gorm:query")` 回调里 `cacheMu.TryLock()` 失败 —— 持锁查库证实;(2) 查询失败时 4 次调用产生 4 次回库 —— `cachedAt` 不推进证实;(3) 慢(120ms)且失败时 5 个并发调用耗时 617ms、5 次查询 —— 串行化证实。`groupRates` / `blockedInvitees` 用同一手法探测,`groupRateMu` / `blockedMu` 同样被占。

**没有自愈出口**:`db.go:362-364` 的 `isConnLevelError` 显式排除 `context.DeadlineExceeded`,所以「可达但慢」时 `failStreak` 永远不涨;`health.go` 又只做 Ping(可达即成功),`Available()` 恒真,循环自我维持。

对原始报告的两处修正:

- 「扩展库连接池也在同步被吃掉」自相矛盾 —— 正因为有 `cacheMu`,这条路径全进程同一时刻最多一条扩展库查询在飞。它抱怨的锁恰恰是防止 ext 池被吃的东西。
- 「ctx 取消不释放底层 MySQL 连接」对 `go-sql-driver/mysql` 不成立(ctx 取消会走 watcher → `mc.cleanup()` 关闭 net.Conn)。`db.go:266-268` 的注释里有同样的误解,**属于继承的错误前提,不能当证据**。
- 健康路径下这把锁其实起**合并**作用(5 个并发只产生 1 次查询),缺陷只在「查询慢到打爆预算」这一窄带内成立。
- `commission` 那两份的争用方只有 2 个 HotAsync worker + `topup_scan` 定时任务,且已接调用方 ctx;但 `topup_scan` 的 ctx 来自 `lease.go:183` 的 `context.WithCancel(context.Background())` —— **无 deadline**,持锁方可以是一个没有超时的扫描协程。

**不阻断上线**(fail-open 语义保住,不丢钱、不脏数据)。**修法**:三处统一改成与 `effectiveCtx` 同形(持锁只读/写快照,SELECT 放临界区外),`usergroup` 那份**必须同时补负缓存**(失败也推进 `cachedAt`,例如 5 秒)——那才是真正的放大器。并在 `settings_ctx_test.go` 用现成的 `probeDuringQuery` 补三条锁的探针测试。

#### M10 — 从节点缺表自检直接 FatalLog 整个网关,而「另一节点正在迁移」的豁免只对主节点开

- 文件:`qianye/db/migrate.go:106`
- 形状:**本轮修复引入的新缺陷**
- 触发前提:显式配置 `NODE_TYPE=slave` + 一次新增 `qy_` 表的发布

```go
func autoMigrate(gdb *gorm.DB, models []any) error {
    if !common.IsMasterNode {
        common.SysLog("qianye: 从节点,跳过扩展库自动迁移")
        return nil                     // ← 裸 nil,不是 errMigrationInProgress
    }
```

`Migrate` 的豁免判据是 `errors.Is(err, errMigrationInProgress)`,只有 `migrate.go:146-148` 抢锁失败那条会返回它。从节点在 106 行就 return,**从未打开迁移连接、从未执行 GET_LOCK**,所以 `migrate.go:80-87` 的宽限分支对它**结构性不可达** —— 而从节点恰恰是永远依赖别的节点建表的那一类。

冒泡链:`verifyTables` 返回 error → `bootstrap.go:60` `return err` → `main.go:375-377` → `main.go:56-58` `common.FatalLog` → `os.Exit(1)`,**整个 new-api 进程(含全部上游 relay 流量)退出**。

确认是本轮回归:`git diff HEAD -- qianye/db/migrate.go` 显示 `verifyTables`、`errMigrationInProgress`、`Migrate/autoMigrate` 拆分全部是未提交的新增代码;`git show 6095e95c:qianye/db/migrate.go` 里 `Migrate` 第一行就是从节点 `return nil`,改前从节点走不到任何自检。

反向激励:**不设 `NODE_TYPE`(默认 master)抢锁失败能拿豁免正常启动,显式设 `slave`(`.env.example:115` 与 `qianye.example.yaml:45` 都推荐)反而崩。**

一名怀疑者判 refuted,理由是「这正是 r3 拍板的修复本身」。**部分成立但不能据此驳回**:r3 的 `audit-raw/r3-transfer.md:79` 确实把「多节点滚动升级同理:从节点跳过迁移,主节点尚未重启的那段窗口内,新代码的从节点上划转全挂」列在**问题**一侧;`96-audit-r3.md:91` 采纳的是「把故障从运行期挪到启动期」,不是「让从节点比主节点更容易崩」。**主从不对称不是拍板过的决定**。另,`verifyTables:216-219` 的原文把 fail-open 限定在「自检**自身失败**」,「确认缺表阻断启动」是它写明的例外 —— 所以「破例了第一原则」这半句是误读,应从论据里删掉。

场景里有一条腿站不住,应移除:「`auto_migrate=false` 且 DBA 未建表 → 从节点无限 crash-loop」**并非从节点特有** —— 主节点走 `autoMigrate` 时 `ShouldAutoMigrate()=false` 同样 `return nil` 后进严格 `verifyTables` 同样 FatalLog。那是 `bootstrap.go:59` 明写的刻意设计。

**默认部署不受影响**(`IsMasterNode = os.Getenv("NODE_TYPE") != "slave"`,`.env.example` 里 `NODE_TYPE=master` 是注释掉的),但**若计划以 slave 形态部署,这条阻断上线**。

**修法**:不要回退 fail-fast。给从节点(以及 `auto_migrate=false`)加一次迁移锁探测(`SELECT IS_USED_LOCK('qy_schema_migrate')`),锁被持有时按「表清单是中间态」处理,与主节点豁免同口径;或给一个有界重试窗口(30 秒内每 3 秒重查)。**注意**:让从节点去 `openMigrationConn + 阻塞式 GET_LOCK` 会撞上 `97-fix-verification.md:14` 记录过的 NEW-1 升级阻断(`readTimeout 30s` 与 `migrateLockTimeoutSeconds 30s` 相等),所以用非阻塞的 `IS_USED_LOCK` 探测而不是抢锁。

#### M11 — 分组定价第 4 个挂载点(结算侧 `applyTaskRatio`)零测试:整段掏空全绿

- 文件:`qianye/modules/grouppricing/hook.go:96`
- 形状:**假回归**
- 触发前提:需要日后有人改坏这段代码

链路完整存在且正确:`grouppricing.go:112 service.QyGroupTaskRatio = applyTaskRatio` → `service/qy_pricing_export.go:42` → `service/task_billing.go:305 modelRatio = QyGroupTaskRatio(...)`。

但**全仓 `*_test.go` 对 `TaskRatio` / `QyGroupTaskRatio` 零命中**(我复核:`grep -rn "TaskRatio" --include=*_test.go .` 无输出)。实测三个环节都没人守:

- 把 `hook.go:109` 的 `return rule.ValueFloat` 改成 `_ = rule; return ratio` → `go test ./qianye/modules/grouppricing/... ./service/...` 全绿。
- 把 `grouppricing.go:112` 的赋值行删掉 → 全绿。
- 把 `task_billing.go:305` 的调用整行删掉 → 全绿(`hookpoint_test.go:38` 的 AST 只解析 `relay/helper/price.go`,够不到 `service/task_billing.go`)。

对照:`pipeline_test.go` 的文件头明写它存在的理由就是「InstallHooks 赋值那一段谁都没守」,并为另外三个挂载点各写了端到端用例。**四个挂载点有三个被守,唯独漏了钱走的第四个。**

生产后果(若日后被改坏):给任务类模型(视频/MJ)配了 ratio 分组折扣时,预扣走 `ModelPriceHelperPerCall` 按折扣价,结算 `RecalculateTaskQuotaByTokens` 按全局倍率重算,差额以**追扣**形式补上 —— 正是 AGENTS.md「预扣与结算必须同口径」直指的情形。可达性已核实:`AdjustBillingOnComplete` 全仓只有 `taskcommon.BaseBilling` 一个实现且恒返回 0,`TotalTokens>0` 时必落到 `RecalculateTaskQuotaByTokens`;`TaskInfo.TotalTokens` 有两个真实赋值方(`kling/adaptor.go:366`、`doubao/adaptor.go:334`)。

**不阻断上线**(今天代码是对的,且 `group_pricing.enabled` 在三份 yaml 里都是 false、`shadow_mode` 默认 true)。**但在打开 group_pricing 真实模式之前必须补上。**

**修法**:在 `pipeline_test.go` 补一条与 `TestTieredHookVariableIsWiredAndApplies` 同形的用例(备份/还原 `service.QyGroupTaskRatio`,走真 `InstallHooks`,断言真实模式 == `rule.ValueFloat`、影子/关闭态 == 入参、`ModePrice` 规则 == 入参);另在 `hookpoint_test.go` 加一条 AST 断言,锁住 `service/task_billing.go` 的 `RecalculateTaskQuotaByTokens` 里存在 `QyGroupTaskRatio` 调用点。

#### M12 — grouppricing 的分组名大小写折叠没有回归测试:同一修复三个模块只钉住了两个

- 文件:`qianye/modules/grouppricing/rules.go:364`
- 形状:**假回归 + 同一概念第 N 份拷贝**

本轮把 commission / transfer / grouppricing 三张「以分组名为键的 money 表」统一改成 `groupname.Effective`。前两者各自补了显式回归(`grouprate_test.go:389 TestCommissionGroupKeyFollowsSharedContract`、`grouprule_test.go:594 TestTransferGroupNameFollowsSharedContract`),**grouppricing 一条都没有**。

实测把 `rules.go:364` 改回旧实现(`TrimSpace` / `""→"default"` / 原样返回)后,`go build ./...` 通过,`go test ./qianye/... -count=1` **18 个包全绿**。探针 `s.lookup(g,"gpt-4o")` 对 seeded 于 `"vip"` 的规则:变异态下 `"VIP"/"Vip"/"  VIP  "` 全部 miss(→ 按全局价扣钱),还原后全部命中 0.5。

一处证据缺陷需记录:原报告的 `grep -rn "normalizeGroup|VIP|..."` 没加 `-E`,BRE 下 `|` 是字面量,「无输出」是命令写坏的产物。改用 `-riE` 后可见 `rules_test.go:156 TestNormalizeGroupMatchesUsersDefault` 确实存在 —— 但它的用例是 `{"default","","  ","  default  "}`,**一个大写输入都没有**,对本变异完全免疫。所以修复量比原文暗示的更小:给现有测试补几个大写拼写即可。

**生产可达性弱于其他条目**(不阻断上线):grouppricing 的写入侧是闭合下拉(`rule-form-sheet.tsx` 的 `<Select>`,选项来自 `/api/pricing` 的 `group_ratio` keys),而拿到测试的那两个模块恰恰是自由文本(commission 是裸 `<Input>`,transfer 是 `<input list=...>`)——这个差异是「那两个更容易打错」的真实理由。读取侧同样闭合(`users.group` 走 `<Select>`;`token.Group` 被 `middleware/auth.go:466` 的 `ContainsGroupRatio` 精确命中 + `GetUserUsableGroups` 双重卡死)。且真出现错配时,`GetGroupRatio` 每一笔请求都会打 `group ratio not found: VIP` 并按分组倍率 1 计费 —— 在 grouppricing 介入之前全站就已经算错价且日志刷屏了。残余可达路径:脚本/导入器直接 POST `/admin/group-pricing/rules`(`ruleUpsertReq.apply` 只校验长度)、直接改库、配置里把分组重命名成另一种拼写。

**修法**:补 `TestGroupPricingGroupKeyFollowsSharedContract`,逐输入断言 `normalizeGroup(in) == groupname.Effective(in)`,防止再被换回私有实现;并给 `lookupOverride` 补 `vip/VIP/Vip/"  vIp  "` 命中同一条规则的断言。

#### M13 — httpq 的 AST「解析锁」漏掉 `c.DefaultQuery`,自然写法的第八份拷贝三道锁全过

- 文件:`qianye/httpq_guard_test.go:130`(判定在 :151)
- 形状:**防御自身的盲区**

```go
if sel.Sel.Name == "Query" {      // ← 只匹配等值 "Query"
    query = true
}
```

`c.DefaultQuery` / `c.GetQuery` / `c.QueryArray` / `c.Param` / `c.PostForm` 全部漏过。名字锁(`forbiddenFuncNames`)是固定 11 项黑名单;offset 锁只管 `.Offset(...)`,管不到切片分页。**三道锁同时失守。**

隔离复现(新增独立探针文件,不改 `api.go`):`strconv.Atoi(c.DefaultQuery("page","1"))` + 手写 `start := (page-1)*size; names[start:end]` → 三条锁全部 PASS。
正对照排除「遍历没扫到」:把同一函数改名为 `pageParams`(在黑名单里)后,名字锁报错并给出该文件路径 —— 证明文件确实被 `forEachQianyeFile` 解析到;而**同一次运行中解析锁仍然 PASS**,而此时函数体内明明白白有 `c.DefaultQuery + strconv.Atoi`。缺口精确锁定在选择器名匹配上。

绕过形态不是假想,而是本仓库的既有写法,就在 `controller/channel.go:360-379`:

```go
page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
startIdx := (page - 1) * pageSize
if startIdx > total { startIdx = total }   // 负数通过此检查
pagedData := channelData[startIdx:endIdx]  // 负下标 panic
```

**拷贝源自身就是无上界且可 panic 的**,照本仓风格写的第八份拷贝会正好落进盲区。溢出算术核对无误:`184467440737095518` 被 `Atoi` 成功解析,`(page-1)*50` 回绕为 `-9223372036854775766`,`start < len(names)` 对负数成立 → 切片 panic。

一处措辞需修正:`126-129` 行注释原文明确写了 `c.Query(...)`,其「与函数叫什么名字无关」指的是**函数名**,这一点锁确实做到了。真正过度承诺的是 `39-40` 行的「这条与名字无关,第八份拷贝叫什么都会被抓住」,把第三道锁包装成了兜底网。

同轴逃逸的既有实例(今天无害,但证明形态已在锁的视野外):`violation/http.go:43`、`grouppricing/api_admin.go:438`、`transfer/api_group_rules.go:242`、`withdraw/api_user.go:238` 的 `strconv.ParseInt(c.Param(...))`,其中 `pathInt64` 已经是三份拷贝(`grouppricing/api_admin.go:437`、`violation/http.go:42`、`violation/api_admin.go:1070` 的 `pathIntParam`)。

**不阻断上线**(qianye 现网无实际漏洞,所有调用点都走 httpq)。**修法**:把等值判断换成集合 `{Query, DefaultQuery, GetQuery, QueryArray, Param, PostForm, DefaultPostForm}`;并补第四条锁 —— 禁止 httpq 之外出现 `(<ident> - <lit>) * <ident>` 用作切片下标,或要求所有列表 handler 的页内切片只能来自 `httpq.Slice`。顺带把 `availability/api_test.go` 改成从 `getMatrix` 驱动,让「handler 有没有接上 httpq」本身成为断言。

---

### P3(可上线后处理)

#### M5 — sitetheme 把降级兜底值永久缓存,一次瞬时故障就让站点主题回不去

- 文件:`qianye/modules/sitetheme/store.go:71`

```go
func Current() (preset string, force bool) {
	if s := snapshot.Load(); s != nil { return s.Preset, s.Force }
	s := loadFromDB()
	snapshot.Store(s)          // ← 失败回落值也被存进来
	return s.Preset, s.Force
}
```

`loadFromDB` 的两条失败分支(`gdb == nil || !db.Available()`、SELECT 报错)都返回同一个 `{Preset: DefaultPreset, Force: false}` 兜底对象。全包只有 `:72`(Current)与 `save()` 的 `:131` 两处 `Store`,**无 TTL、无 loadedAt**;`health.go` 的 `markProbeHealthy` 只收敛熔断、不碰 snapshot;`module.Module` 没有预热钩子。

实测复现:`snapshot.Store(nil)` + 无 DB 句柄下调 `Current()`,返回 `("default", false)` 且随后 `snapshot.Load()` 非 nil;二次调用不回源。

前端放大:`use-qy-site-theme.ts:44` 无条件 `qyCacheSiteTheme`,`site-theme.ts:73-79` 覆写两个 localStorage 键。`force=false` 部署下,后端恢复也不会再纠正已被污染的访客。(但 `theme-customization-provider.tsx:176-183` 的 `setPreset` 会写 `theme_preset` cookie 且优先级更高,所以此前被应用过非 default 预设的访客不受影响,真正受影响的是全新访客。)

**触发窗口比原报告描述的窄得多**:`bootstrap.go:52` 的 `db.Init` Ping 失败会 `FatalLog`,进程根本起不来,所以「滚动重启期间库不可用」这条主路径不成立。真实窗口只有「进程启动 → 第一次 `Current()`」之间,而 `/api/qy/config` 是每次页面加载都打的引导端点,首调通常在监听后数秒内。只有低流量实例窗口才明显变宽。
**但触发比原报告说的更容易的一面**:`health.go:49-53` 单次 Ping 失败就无阈值置 `healthy=false`(15 秒探测周期),不需要攒够 5 次 `MarkFailure`;`:83` 的 Find 单次瞬时报错也会返回兜底并钉死。

反讽点:`store.go:85-87` 的注释专门论证「绝不能回落成上次缓存的值」,而代码犯的是**反向**的错 —— 把兜底值缓存成了下一次的「上次」。

同概念的兄弟实现把这件事做对了:`usergroup/settings.go:52-55` 在 `!ok` 时 `return cachedGroup` **不写缓存**,用 `cachedAt==0` 表示「从未成功填充」以便每次重试,还有 `warmDefaultGroup()` 在 Init 预热。sitetheme 三样都没有。

**修法**:`loadFromDB` 返回 `(settings, ok)`,`Current()` 仅在 `ok` 时 `Store`;失败时返回兜底值但不写缓存。若担心故障期间每次请求都查库,补一个「失败后 N 秒内不重试」的负缓存时间戳。

#### M8 — `effectiveCtx` 的缓存失效会被在途加载的旧快照吃掉(r3 把查库移出临界区引入的回归)

- 文件:`qianye/modules/commission/settings.go:146`
- 形状:**修复自身引入新缺陷**

我**亲自核对了当前文件**(见下方"一条无效反驳"):`settings.go:124-130` 确实是 `Lock` → 判缓存 → `Unlock`,`loadOverrides(ctx)` 在**锁外**,`:145-148` 回来后**无条件** `settingsCache = &base; settingsLoaded = now`,没有代次校验。任何发生在 SELECT 返回与写缓存之间的 `invalidateSettings()` 都会被静默覆盖。

`git show f745d19d` 证实 r3 之前是 `Lock + defer Unlock` 全程持锁,`invalidateSettings` 无法插进来;r3 的 F4(`audit-raw/r3-commission.md:105`)明文要求「先解锁查、拿到结果再加锁写缓存」——窗口正是这么开的。

两名怀疑者分别用受迫探针(GORM `After("gorm:query")` 回调模拟提交+失效)复现「库里 300、`effective()` 返回 800」;其中一人另做**不注入任何回调的自然并发探针**(8 协程加载 + 管理端同刻改价,300 轮),命中 1 次。另一人做 16 协程 × 40 次真实 `adminPutConfig` 的探针,0/40 命中。

**降为 P3 的理由**(两名怀疑者独立给出同一结论):

1. `invalidateSettings` 是**进程内**的,commission 配置没有任何跨节点失效机制(对照:grouppricing 专门加了 `RuleVersion` 单行表轮询)。而多节点是本项目的一等假设。所以每次改费率,**其余 N-1 个节点本来就会按旧费率计佣最长 60 秒并冻结进账本**,`audit-raw/r3-commission.md:171` 原话就是「多节点仍有 60 秒窗口,与 blockedInvitees/effective 同一约定」。此缺陷只是让管理员所在那个节点退化成和同伴一样。
2. 被覆盖回去的是一份完整的、此前已被批准的配置(管理端一个事务写全部键,`loadOverrides` 一条 SELECT 读全部键),不会产生「谁都没批准的中间费率组合」。
3. 账目仍自洽:`consumeIdemKey` 把 `RateUnits+RateGroup` 编进幂等键、`writeAccrual` 冻结两者,每行 `base × rate == gross` 仍成立。
4. 两条 PUT 路径都**自愈**:`api_admin.go:181-182` 与 `167-169` 在 `invalidateSettings()` 之后立即 `effectiveCtx(ctx)` 重新查库回填。在途读者只剩 `applyOverrides`(微秒级)却要赢过管理端一整个 DB 往返。
5. 「更糟的那半条」(审计与接口返回声称费率没变)被夸大:要让 `after` 读到旧值,在途写必须精确落进 `:181` 与 `settings.go:125` 之间的亚微秒窗口,比造成 60 秒陈旧的窗口还窄约三个数量级。绝大多数交错下 `after` 会发现 `settingsCache == nil` 而自行回库。
6. 「失效缓存按钮同样成立」**不实**:`adminInvalidateCache` 本身不写库,旧读者需要 SELECT 早于运营的库外改动、写缓存又晚于人点按钮,是人类时间尺度,不可达。

**修法**:`invalidateSettings()` 时 `settingsGen++`;`effectiveCtx` 在 `Unlock` 前记下 `gen`,写回缓存时若 `settingsGen != gen` 则只返回 `base` 不写缓存。顺带把 M9 的三份改成同一形状,避免再次分叉。

> **一条无效反驳必须记录**:有一名怀疑者判定本条 refuted,理由是「`effectiveCtx` 全程持锁、`settingsMu` 不可重入、会自死锁」,并附了一段 `sync.Mutex.Lock` 的挂死栈。**这个反驳是错的。** 我直接读了当前 `settings.go:124-130`:`Lock` → 判缓存 → `Unlock` → `loadOverrides`,锁确实先放了;`go test ./qianye/modules/commission/ -count=1` 实跑 30.6s 全绿,不存在自死锁。该怀疑者读到的是并发 agent 写文件过程中的中间快照。**下一轮不要据此把 `effectiveCtx` 改成"修自死锁"。**

#### M14 — `groupRateDegrade` 的两处生产调用点无覆盖,健康接口那条测试是自证式的

- 文件:`qianye/modules/commission/grouprate.go:124` 与 `:133`
- 形状:**假回归**

本轮 F5 给分组费率加了降级留痕。用 coverprofile 直接证明(比变异更硬):`hook.go` 同文件的 `applyModelPrice/applyModelRatio/applyTieredQuota/resolve` 覆盖计数为 1,而 `grouprate.go` 的 `96.69/97.80/100.2-106.35/106.35/109.2-110.14` 五个基本块**计数全为 0**。删掉两行 `note()`(grep 计数 2→0)后 `go test ./qianye/modules/commission/` 全绿。

唯一断言它的 `TestAdminHealth_ExposesDegradeCounters` 是在测试里**自己调** `groupRateDegrade.note(...)` 再断言自己刚写进去的值,从不驱动 `groupRates()`。对照:同一轮的 `settingsDegrade` 被真正驱动(`settings_ctx_test.go` 的 `TestLoadOverridesHonorsContext` 用已取消 ctx 走 `effectiveCtx` 再断言 `count==1`)。**两个计数器只有一个有真防线。**

场景描述有一处夸大需修正:「这批佣金会被冻结进账本行」只对 `:133` 成立(`gdb` 非 nil、仅本次 SELECT 失败,后续 INSERT 仍可能成功);`:124` 时 `db.Get()` 为 nil,`writeAccrual` 用同一个 nil 句柄立刻返回 `ErrNotReady`,一行账本都写不出来。且 `:124` 的前提在活进程里基本不可达(`db.Init` 失败即 FatalLog,`handle.Store(nil)` 只在 `Close()` 里)。

**该路径可测**:用已取消的 ctx 一次就能驱动到 `:133`,日志打出「配置降级…(累计 1 次): 读取分组费率失败: context canceled」。

**修法**:补驱动式用例 —— 先 `newTestDB` 建好缓存,再 `invalidateGroupRates()` 并用已取消 ctx 调 `groupRates(ctx)`,断言返回空表且 `groupRateDegrade.stats()["count"]` 从 0 变 1。`TestAdminHealth_ExposesDegradeCounters` 可保留,但它守的是「计数器被健康接口吐出来」,守不了「计数器真的会被记上」。

#### M15 — grouppricing 包注释仍宣称 Task 结算未覆盖,并承诺一个不存在的管理端告警

- 文件:`qianye/modules/grouppricing/grouppricing.go:48`
- 形状:注释与代码脱节

`38-57` 行的「已知不覆盖范围」仍写着:

> `service/task_billing.go RecalculateTaskQuotaByTokens` …… 重算这一步仍按全局倍率 …… ⚠ 这是唯一一处"预扣与结算口径不一致",切真实模式前必须确认没有给 Task 类模型配 ratio 覆盖;api_admin.go 的写入校验会对这类规则返回显式告警。

两句都与代码不符:

1. 同文件 `:112` 本轮已经挂上了 `service.QyGroupTaskRatio = applyTaskRatio`,链路完整可达。真实模式下 ratio 口径**已覆盖**,连「以下三处」的计数也过时(实剩两处)。
2. `grep -rniE "task|告警|warn" qianye/modules/grouppricing/api_admin.go` **无输出**。告警机制确实存在但在 `effective.go:120` 的 `modeMismatchWarning`(经 `api_admin.go:86/289` 以 `effective.warning` 返回),且**只判「规则口径 vs 模型全局计费口径」不匹配**;而 `RecalculateTaskQuotaByTokens` 恰恰只在 `hasRatioSetting` 且无按次价时才触发 —— 这类模型配 ratio 规则**不会产生任何告警**。即注释承诺的「显式告警」恰恰在它让运维依赖的那个场景下不存在。

运维照这段注释操作会:(a) 上线前刻意不给任务类模型配 ratio 分组价,白白放弃一个已做好的能力;(b) 配了之后等待那条不存在的告警,把「没有告警」误读成「这条规则没问题」。注释位于扩展的对外契约文档位置,是运维唯一会读的东西。

**修法**:把 48-56 行从「已知不覆盖范围」挪到覆盖清单,写明它经 `service.QyGroupTaskRatio` 覆盖、且只认 `ModeRatio`;删掉那句不存在的 `api_admin.go` 告警承诺,或把它补实现出来(给 Task 类模型写 `ModePrice` 规则时返回显式告警 —— `applyTaskRatio` 刻意不处理 `ModePrice`,那一档仍然是预扣按次价、结算按全局倍率重算)。
**注意不要连带改错**:`hook.go:125-126` 的同类表述(「配了却不生效的规则由管理端写入校验负责告警」)是有 `modeMismatchWarning` 支撑的,那一句是对的。

---

## 三、能不能上生产

**结论:修完 M1 之后可以上,但必须同时确认三项部署前提。**

### 必须先修(阻断)

| 条目 | 修复量 | 为什么阻断 |
|---|---|---|
| **M1** `markPaid` 无 method 校验 | **一行 if** | 唯一一处「核销佣金而不产生任何支付」的代码路径;静默、不可逆(`paid` 无出边)、无对账检测;后端零校验且违反项目自己写在 `validate.go:96` 的铁律 |

### 必须确认的部署前提(不改代码则必须锁死配置)

1. **`withdraw.auto_credit_on_approve` 保持默认 true**(三份 yaml 都没覆盖它 —— 保持现状即可)。设为 false 会让 quota 提现整条链路静默失效(M2),并把 M1 那条吃佣金的 mark-paid 变成运维唯一看起来可用的按钮。
2. **`commission.refund_clawback` 保持 false**(`qianye.example.yaml:146` 即为 false)。打开它会启用 M3 的 2 倍超额冲正。
3. **不要以 `NODE_TYPE=slave` 形态部署**,或先修 M10。默认(不设 `NODE_TYPE` = master)不受影响,多 master 并发启动走 `errMigrationInProgress` 豁免,正常。

### 打开 group_pricing 真实模式之前必须先修

- **M11**(第 4 个挂载点零测试)+ **M15**(注释宣称 Task 未覆盖并承诺不存在的告警)。这两条一起构成了一个陷阱:注释叫运维「别给 Task 模型配 ratio」,代码其实已经支持;而支持它的那段代码没有任何测试守护。
- **M12**(大小写折叠零测试)成本极低,同批一起补。

### 建议在下一个迭代内收掉(不阻断本次上线)

M6 / M7(ctx 断链,3 处)、M9(持锁 I/O,3 份)、M4(sitetheme 管理页)、M13(AST 锁盲区)。

### 建议排到 backlog

M5 / M8 / M14 / M15(修法部分)。

---

## 四、专项回答:那 4 种反复出现的缺陷形状,这一轮又出现了吗?

**四种全部又出现了,合计 14 次。**下表按形状归类(一条发现可能同时属于两种形状):

### 形状 1 — 断链(纯函数写对了,调度层/收尾层/配置消费层没接上):**5 次**

| # | 实例 | 断在哪一层 |
|---|---|---|
| 1 | M6 `twophase.Execute` 收 ctx 零引用 | 三个调用方专门构造 `guard.ColdContext`,零消费 |
| 2 | M7 `violation/rules.go` `reload` 无 ctx 参数 | HotAsync 闭包收到 ctx 但函数签名接不住 |
| 3 | M7 `grouppricing/rules.go` 同上(逐字拷贝) | 同上 |
| 4 | M4 sitetheme 管理端 API 零前端消费方 | **断链的镜像方向:写端零生产方**;`allowed_presets`/`upstream_default` 零命中证明连 GET 也是死的 |
| 5 | M2 `auto_credit_on_approve` 的 false 分支零实现 | `*bool` 配置项存在的全部意义就是能填 false,而那个取值没有任何完成路径 |

**这是本轮出现次数最多的形状,且全部集中在"ctx/配置的消费层"。**

### 形状 2 — 假回归(把修复回滚后测试照样全绿):**4 次,全部实测确认**

| # | 实例 | 变异后 |
|---|---|---|
| 1 | M11 `applyTaskRatio` 返回值改回入参 | 全绿(而且赋值行删掉、调用行删掉也全绿 —— 三个环节都没人守) |
| 2 | M12 `normalizeGroup` 改回私有实现 | **18 个包全绿**(我亲自跑的) |
| 3 | M13 第八份分页拷贝用 `c.DefaultQuery` | 三道 AST 锁全过 |
| 4 | M14 删掉两行 `groupRateDegrade.note()` | 全绿(coverprofile 显示那五个基本块计数为 0) |

### 形状 3 — 修复自身引入新缺陷:**2 次**

| # | 上一轮的修复 | 本轮引入的新缺陷 |
|---|---|---|
| 1 | r3-F4「把查库移出 `settingsMu` 临界区」 | M8 在途旧快照覆盖 `invalidateSettings()`(旧的全程持锁版本结构上不可能出现这个交错) |
| 2 | 本轮把 `Migrate` 拆成 `autoMigrate + verifyTables` | M10 从节点返回裸 `nil` 拿不到 `errMigrationInProgress` 豁免 → 缺表即 FatalLog 整个网关 |

比例上比"上一轮 4 个修复引入 4 个新缺陷"好了很多,但仍然是 2/N。

### 形状 4 — 同一概念的第 N 份拷贝各自漂移:**3 组**

| 组 | 份数 | 漂移点 |
|---|---|---|
| **缓存回落口径** | **5 份** | `usergroup`(失败不写缓存 + TTL + Init 预热,**最完整**)、`commission/settings`(失败退旧快照 + TTL,查库已在锁外)、`commission/groupRates`(退旧快照,持锁查库)、`commission/blockedInvitees`(退旧快照,持锁查库)、`sitetheme`(**失败值写进永久缓存,无 TTL,无预热 —— 最差,且犯的是自己注释警告的反向错误**) |
| **`reload` 热刷新** | 2 份 | violation / grouppricing 逐字相同,两份同样漏接 ctx |
| **`pathInt64` 路径参数解析** | 3 份 | `grouppricing/api_admin.go:437`、`violation/http.go:42`、`violation/api_admin.go:1070`(叫 `pathIntParam`)。**今天无害**(`ParseInt` 天然限界、调用方一律 `!ok → 404`),但正是下一个漂移候选 |

---

## 五、现有防御:哪些真起作用,哪些是摆设

### 真正起作用

**1. httpq 共享包 + 三道 AST 锁 —— 本轮最有效的防御,ROI 最高**

它不只收敛了 7 份拷贝(比任务书列的 4 份多 3 份),还**顺带挖出一个正在线上的静默失效缺陷**:`controller/config.go` 那份"已修好"的 `intQuery` 上界是 100 万,而它同时被非分页参数复用 ——

```go
if v := intQuery(c, "start_ts", 0); v > 0 { q = q.Where("created_at >= ?", v) }
```

任何真实 Unix 时间戳(约 1.75e9)都 > 100 万 → 解析恒回落 0 → `v > 0` 恒不成立 → **WHERE 从来没被拼上去**。资金单列表与审计日志列表的时间范围筛选一直是死的,`user_id > 100 万` 同理(管理员按用户筛,拿回的是所有人的单)。**没有任何报错。**

这条同时也是形状 3 的一个新实例(给分页加上界那次修复波及同一个 helper 的其他调用点),但因为它已在同一轮内被 httpq 收敛工作发现并修复,不单列为存活发现。

`R1..R8` 八条回滚验证全部实测变红,且中途抓到一次真实的假回归(R1 第一次是绿的,因为 `httpq.Offset` 自己也夹页码把 `Paginate` 的夹取盖住了)—— **这说明这套锁本身是被检验过的,不是自证式的。**

**2. `qianye/config/selfcheck.go` 登记表(24 条)+ `selfcheck_test.go` 的五条断言**

它不是靠自觉维护的注释:`TestFieldConsumers_ConsumerFilesReallyReferenceTheField` 会真的解析 `file` 指向的源文件确认引用存在。这条设计正确,是本仓最好的防御形状之一。

**但本轮 0 命中**,原因是射程外(见下)。

**3. `modules_test.go` 的两条结构断言**

`TestEveryModuleDirectoryIsRegistered` / `TestNoRegisteredModuleWithoutDirectory` —— sitetheme 已在 `modules.go:16` 正确注册并通过。**它守的范围内本轮没出问题**,不是摆设,只是射程小。

**4. `groupname` 共享包 —— 半个防御**

包本身接上了(三个模块都调 `groupname.Effective`),但只有 commission / transfer 配了对照测试。**grouppricing 这份可以被整个改回私有实现而 18 个包全绿**(M12)。共享包本身不构成防御,**"逐输入断言 == 共享实现"的那条测试才是**。

**5. `grouppricing/pipeline_test.go` + `hookpoint_test.go` —— 3/4 有效**

`pipeline_test.go` 的文件头明写它存在的理由就是「InstallHooks 赋值那一段谁都没守」,并为三个挂载点各写了端到端用例。这是正确的设计意图。但第 4 个(结算侧)漏了,而 `hookpoint_test.go` 的 AST 只解析 `relay/helper/price.go`,`grep` 不到 `service/task_billing.go`(M11)。

### 射程外 / 有盲区

**6. selfcheck 登记表对本轮 0 命中,不是失效,是射程外**

- **M2 是"分支级断链"**:`auto_credit_on_approve` 有真实消费方(`reconcile.go:60` 与 `credit.go:303` 都读它),登记表判定它"已消费"是对的。**登记表看不到"某个取值下没有实现"**。
- **M4(sitetheme)根本不是 YAML 配置项**,是 `qy_settings` 的 DB 设置,不在 `Config` 结构体里,反射展开扫不到。
- 补法:登记表可以扩一类 —— 对 `*bool` 类型的配置项额外要求登记"false 分支的行为在哪里实现";`validateWithdraw` 这类跨字段校验里补组合非法判定。

**7. AST 解析锁有一个明确盲区 —— `c.DefaultQuery`(M13)**

且盲区正对着本仓库既有的、最惯用的写法(`controller/channel.go:360`)。修复是把等值判断换成集合,一行。这是本轮唯一一条"防御自身被证明有洞"的发现。

### 完全缺失的防御(**下一轮最该新增的三条**)

**8. 没有任何"ctx 必须透传到 GORM"的机器校验 —— 本轮 3 处逃逸(M6 × 1、M7 × 2)**

这条铁律在项目里被写了至少 5 遍(`commission/hook.go:90-92`、`accrual.go:166-167`、`settings.go:95-98/107-114`、`db/db.go:266-268`、`violation/ban.go:33-35`),全仓 39 处非测试 `WithContext` 调用点都遵守了 —— **唯独资金主路径 `twophase.Execute` 和两份 `reload` 漏接,而这三处恰恰是最需要它的**。有约定、有先例、有 5 处注释,却零机器校验。

建议新增(与 `httpq_guard_test.go` 同形):qianye/** 下,凡在**能拿到 `context.Context` 的作用域**内用 `db.Get()` 取到的句柄发起 GORM 终结调用(`Find/Take/First/Create/Updates/Delete/Pluck/Transaction`),必须先 `.WithContext(ctx)`;白名单单列真正不该受调用方取消影响的收尾写(并要求它们显式用 `context.WithoutCancel`)。**这一条能同时抓住 M6 和 M7,是三条里 ROI 最高的。**

**9. 没有"后端管理接口 ↔ 前端消费方"对账 —— M4 逃逸**

已有一次人工对账(把全部前端 qy 请求路径抽出来共 60 条与后端路由表比对,反向缺口只有 `/admin/site-theme` 加几个纯运维排障口)。这个对账应该固化成脚本或测试,并维护一份"有意只有后端"的白名单(`/admin/commission/health`、`/admin/commission/cache/invalidate`、`/admin/log-metrics/health`、`/admin/withdraw/pii-audits`)。

**10. 没有"缓存回落口径"的共享包 —— 5 份各自实现(M5 / M8 / M9)**

这是本轮漂移最严重的一组,且最好的那份(`usergroup`)和最差的那份(`sitetheme`)差了三个特性:失败是否写缓存、有无 TTL、有无 Init 预热。建议抽一个 `qianye/cachex`(`pkg/cachex` 已存在,可复用)的 `Snapshot[T]`:统一「持锁只读/写快照,I/O 在锁外」「失败不写正快照 + 可选负缓存」「代次校验」三件事,五处全部改为调用它,并配一条"qianye/** 下不得出现新的 `xxxMu.Lock(); defer Unlock()` 包住 `db.Get()` 调用"的 AST 锁。

**11. `qianye/modules/sitetheme` 包零测试文件**

`go test ./qianye/...` 输出里它是 `[no test files]`。本轮 15 条发现里有 2 条出自这个包(M4 / M5),占比与它的代码量完全不成比例。

---

## 六、复核过程中需要留档的三件事

1. **一条无效反驳(已在 M8 内详述)**:有怀疑者判 M8 refuted,理由是 `effectiveCtx` 全程持锁会自死锁,并附了挂死栈。**该反驳基于并发 agent 写文件过程中的中间快照**;我实读当前文件确认锁在 `loadOverrides` 前已释放,`go test ./qianye/modules/commission/` 30.6s 全绿。下一轮不要据此去"修自死锁"。

2. **工作区状态**:本轮全部结论基于**未提交的工作区**(约 30 改 + 16 新增),不是 HEAD。`verifyTables`(M10)、`service/qy_pricing_export.go`(M11)、`groupname/`、`httpq/` 均为未提交新增。提交时需注意区分本轮修复与既有改动。

3. **复核期间多 agent 并发改同一批文件**曾导致至少三次读到陈旧快照(`settings.go`、`grouprate.go`、`availability/api.go`)。若后续仍以多 agent 并行方式做审计,建议按包分片、禁止跨片写。当前工作区已确认干净:无遗留 `zz_*_probe_test.go` / `qy_r4_probe_test.go` 等临时文件,`go build ./...` 与 `go test ./qianye/...` 全通过。
