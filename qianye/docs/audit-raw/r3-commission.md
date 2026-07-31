# commission

## 审计结论：佣金百分比改造 + 分组差异化费率

先说结论：**百分比↔整数的换算层、calcGross/computeSettlement 的算术层、旧 bps 兼容层都是对的**，数值走查（5% × 2000 × 10 = 1000 零损耗、10.25%、0.01%、混合分组）全部成立，费率与分组确实在 `writeAccrual` 的 `Create` 里冻结进了 `rate_bps`/`rate_group` 两列，日聚合幂等键带上费率也确实防住了"一行混两套费率"。管理端增删改都写了审计。

缺陷全部落在**口径来源、存储层语义、收尾层**这三处 —— 与前两轮同一个形状。

---

### F1 —— 分组费率取的是 `users.group`，不是这次请求真正计价的分组；下线可用令牌分组绕开低费率档 【资损｜高】

`qianye/modules/commission/hook.go:19-23` + `hook.go:54` + `hook.go:120` + `qianye/modules/commission/inviter.go:105-126`

`grouprate.go:14-18` 把口径的第一条理由写成"**费率要跟着毛利走** —— 平台从这笔额度上赚多少，取决于下线所在分组的倍率"。但实现取的是 `users.group`（`inviter.go:105` 的 `Select(... "group")` → `InviteeGroup`），而真正决定这次请求倍率的是 `relayInfo.UsingGroup`：

- `middleware/auth.go:459-474`：`token.Group != "" && 用户有权访问` → `userGroup = tokenGroup`，写进 `ContextKeyUsingGroup`；
- `relay/helper/price.go:52-56`：`auto_group` 命中时再覆盖一次 `relayInfo.UsingGroup`；
- `service/quota.go:256`、`service/text_quota.go:538`：`Group: relayInfo.UsingGroup` → 就是 `RecordConsumeLogParams.Group`（`model/log.go:339`）。

**这个字段已经原样递到 `onConsumeLog(c, userId, params)` 手里，`hook.go:54` 把它丢掉了**，`consumeEvent` 只带走 `InviteeId/Quota/At`。任务计费那一路同样（`hook.go:176`，`RecordTaskBillingLogParams.Group` 见 `model/log.go:415`）。

**触发场景（可主动构造）**：运营给 `wholesale`（group_ratio 低、毛利薄）配 1.5% 消费返佣，全局默认 5%。下线 U 的 `users.group` 保持 `default`，但 `wholesale` 在他的 usable groups 里 —— 他建一个 `group: "wholesale"` 的令牌，把全部流量走这个令牌。`middleware/auth.go:473` 把 `UsingGroup` 换成 `wholesale`，按薄毛利计价；而 `accrueConsume` 仍按 `users.group = default` 解析出 5%，并把 `rate_group="default"` 冻结进流水。平台按批发价收钱、按零售价付佣金，且**流水上看不出异常**——那一行自洽（base × 5% = gross），只是分组记错了。

反向同样成立（`users.group=vip` 12%、令牌走 `default`）。令牌分组填 `auto` 时更彻底：`UsingGroup` 逐请求变化，冻结下来的 `rate_group` 系统性地不是计价分组。

**修复**：`consumeEvent` 增加 `Group string`，`onConsumeLog`/`onTaskBillingLog` 填 `params.Group`；`accrueConsume` 用它调 `resolveRate`，为空时才回落 `e.InviteeGroup`。充值/兑换码没有请求分组，继续用 `users.group` 是对的，但要在 `grouprate.go` 的口径说明里写清这个不对称。副作用是 auto 分组下日聚合行数变成"分组数 × 天"，有界，可接受。

---

### F2 —— `group_name` 唯一索引在 MySQL 默认排序规则下大小写不敏感，而查表是大小写敏感的：给 "VIP" 配费率会静默改掉 "vip" 的费率 【数据错误 + 资损｜中高】

`qianye/modules/commission/grouprate.go:61` + `grouprate.go:136-140` + `grouprate.go:198-204` + `grouprate.go:220`

`normalizeGroup` 只 TrimSpace，注释明写"刻意不做大小写折叠……在这里折叠会让 'VIP' 的规则悄悄套到 'vip' 头上，那是一笔谁都没批准的费率"，`grouprate_test.go:123` 也把这条固化成断言。但落库的 `GroupName string gorm:"type:varchar(64);...uniqueIndex:uk_qy_cgr_group"` **没有指定排序规则**，AutoMigrate 建表继承库默认（5.7 = `utf8mb4_general_ci`，8.0 = `utf8mb4_0900_ai_ci`），两者都大小写不敏感。

**触发场景**：
1. 运营在管理端为分组 `vip` 配 8% 消费费率 → 落一行 `group_name='vip'`。
2. 平台另有分组 `VIP`，运营在同一个表单点"新增"，填 `VIP` / 50%。
3. `upsertGroupRate` 的 `clause.OnConflict{Columns: group_name, DoUpdates:[topup, consume, enabled, remark, operator_id, updated_at]}` 在 MySQL 上渲染成 `ON DUPLICATE KEY UPDATE`；`'VIP'` 撞上 `'vip'` 的唯一键 → **更新了 `vip` 那一行的费率，`group_name` 不在 DoUpdates 里所以仍是 `vip`**。
4. `api_admin.go:334` 把内存里的 `row`（GroupName="VIP"）回给前端，审计的 `afterSnap` 也记成 "VIP"。运营看到"VIP 已保存 50%"。
5. `groupRates` 的 map 键是 `vip` → **`vip` 分组的用户按 50% 返佣**（原本 8%），`VIP` 分组的用户回落全局默认。费率被放大 6 倍，审计日志指向一个库里根本不存在的分组名。

`deleteGroupRate`（`grouprate.go:220` 的 `Where("group_name = ?", "VIP")`）同理会删掉 `vip` 那一行。

**为什么测试挡不住**：`testdb_test.go:74-78` 用 SQLite，`varchar` 默认 BINARY 排序，大小写敏感 —— 这条差异永远不会在 CI 里出现。

**修复**：`gorm:"type:varchar(64) COLLATE utf8mb4_bin;not null;uniqueIndex:uk_qy_cgr_group"`（需要一次 `ALTER TABLE` 迁移，现存重复行要先人工合并）；或者放弃大小写敏感，`normalizeGroup` 统一 `strings.ToLower` 并同步改注释与 `grouprate_test.go:123`。两者选一，不能留现在这种"代码敏感、存储不敏感"的中间态。

---

### F3 —— 费率批量保存不在事务里，中途写库失败会留下"一半生效的费率组合"且**一条审计都没有** 【数据错误 / 审计完整性｜中】

`qianye/modules/commission/api_admin.go:129-134` + `api_admin.go:149-159`，与 `api_admin.go:95-96` 的注释直接矛盾

```go
// api_admin.go:95-96
// 先把全部取值校验并规范化,再统一落库:一半写进去一半 400,
// 会留下一个谁都没批准的中间费率组合。
...
for k, v := range normalized {
    if err := writeSetting(k, v, operatorId); err != nil {
        internalError(c, err)   // ← 直接 return
        return
    }
}
```

注释承诺挡住"一半写进去"，但只挡住了 **400**；`writeSetting` 每次自取 `db.Get()`、逐条 upsert，**没有事务**。

**触发场景**：运营在管理端同时改 `consume_rate_percent`（5% → 8%）和 `min_settle_quota`，前端一次 PUT 两个键。`normalized` 是 map，Go 的 map 迭代顺序随机；`consume_rate_percent` 先写成功，第二条撞上扩展库死锁 / 锁等待 / 连接抖动（`db.MarkFailure` 里 `isConnLevelError` 不认死锁，熔断不会开）→ 接口返回 500 "处理失败,请稍后重试"。结果：

- 库里 consume 费率已经是 8%，60 秒内所有节点开始按 8% 计佣；
- 本节点连 `invalidateSettings()` 都没走，管理端刷新还显示 5%；
- `audit.Write` 在 `api_admin.go:149` 之后才执行，**`qy_audit_logs` 里没有任何记录**。"谁在什么时候把 3% 改成 8%"这句注释里的承诺，在最该生效的那一次彻底失效。

运营看到 500 会重试，于是同一次调价可能落两条审计或零条。

**修复**：把 `for k, v := range normalized` 包进 `gdb.Transaction`（`writeSetting` 接受 `tx *gorm.DB`），失败即整体回滚；同时在失败分支补一条 `Result: qymodel.ResultFail` 的审计，把 `normalized` 快照写进 `AfterSnap` —— 部分写入的事实本身就必须留痕。

---

### F4 —— `effective()` 在持锁状态下跑一条不带 ctx 的查询；返佣 worker 的 3 秒预算对它完全无效 【拒绝服务｜中】

`qianye/modules/commission/settings.go:100-119` + `settings.go:130-145`，对照 `grouprate.go:113` 与 `accrual.go:173`

同一个函数体里的不对称非常刺眼：

```go
// hook.go:112-120
s := effective()                                   // ← 里面查库,不接 ctx
rate := resolveRate(ctx, e.InviteeGroup, ...)      // ← 接了
_, err = writeAccrual(ctx, accrualInput{...})      // ← 接了
```

`effective()` 在 `settings.go:100` 拿到 `settingsMu`（`defer` 到函数结束），第 107 行调 `loadOverrides()`，而 `settings.go:136` 是裸的 `gdb.Where("scope = ?", settingScope).Find(&rows)` —— **没有 `WithContext(ctx)`**，GORM 用 `context.Background()`。

**触发场景**：结算任务在 `settle.go:279` 的 `lockBalance` 上持 `qy_commission_balance` 行锁，同一时刻 `qy_settings` 所在实例负载抖动。缓存过期后第一个 worker 进 `effective()` → 拿住 `settingsMu` → 那条 SELECT 一直等到 DSN 的 `readTimeout=30s`（`db/db.go:293`）才被驱动切断。这 30 秒内：

- 另一个 worker 走到 `hook.go:112` 阻塞在 `settingsMu` 上 —— 两个 worker 全占死，队列涨到 4096 后开始 `dropped`；
- `getSummary`（`api_user.go:47`）、`adminHealth`（`api_admin.go:566`）、`pendingInviters`（`settle.go:139`）全部串在同一把锁上，用户端"我的推广"页面整体挂住。

上一轮的修复报告在"签名变更调用点"一栏声明"**无漏改**"，但 `effective` / `loadOverrides` / `refSalt`（`settings.go:277`、`settings.go:297`，同样裸查、同样持 `saltOnce`）三个都不在那张表里。新写的 `groupRates` 反倒接对了 ctx，说明这不是统一取舍，是漏了。

**修复**：`effective(ctx)` / `loadOverrides(ctx)` / `refSalt(ctx)` 接 ctx 并 `WithContext(ctx)`；查库那一步移出 `settingsMu` 的临界区（先解锁查、拿到结果再加锁写缓存，或改用 `singleflight`，与 `inviter.go:95` 的做法一致）。

---

### F5 —— 分组费率读库失败时静默按全局费率计佣并**冻结进账本**，无告警、无标记 【数据错误｜中低】

`qianye/modules/commission/grouprate.go:113-119` + `grouprate.go:150-151` + `hook.go:120-138`

```go
if err := gdb.WithContext(ctx).Where("enabled = ?", true).Find(&rows).Error; err != nil {
    db.MarkFailure(err)
    if groupRateCache != nil { return groupRateCache }
    return map[string]GroupRate{}          // ← 回落全局费率,一个字都不打
}
```

**触发场景**：进程刚启动（`groupRateCache == nil`）、或缓存刚过期，这一次 `Find` 撞上 3 秒 async 预算超时 / 扩展库瞬时抖动。`resolveRate` 拿到空表 → `Matched=false` → `d.Units` 保持全局默认。`wholesale` 分组（配 1.5%）的下线这一笔按 **5%** 计佣，`rate_group` 仍然写成 `wholesale`，`rate_bps=500`。因为幂等键带费率，这一天会多出一行标着 `wholesale` 却按全局费率的桶，且**没有任何修复路径**（`repairStrandedAccruals` 只管 settled 回退）。

事后翻账时无法区分"这行是回落"还是"当时配的就是 5%" —— `model.go:80` 的注释建议"看 RateUnits 与当时的全局默认是否一致"，而回落场景下这两者恰好相等，判据自相矛盾。

`rateDecision.Matched`（`grouprate.go:150-151`）注释写"只用于日志与管理端解释"，但全仓 grep 只有 `grouprate.go:174` 赋值和两处测试断言 —— **生产代码零消费方**，正是本项目反复出现的那个形状。

**修复**：`groupRates` 在"缓存为空且读库失败"这条路上必须 `warnf` + 计数器；`resolveRate` 把 `Matched` 与"是否走了降级路径"透出来，`accrueConsume` 在降级时写进 `RiskFlags`（例如 `rate_fallback`），让这类行事后可筛。更彻底的做法是这一条直接返回 error 让作业失败（可观测），但那会丢佣金，取舍要显式写下来而不是默认静默。

---

### F6 —— 日聚合累加把 decimal 当字符串参数传给 MySQL，`DECIMAL + '字符串'` 在 MySQL 里按 DOUBLE 求值 【其他｜低（当前量级无损，但违反项目硬约定）】

`qianye/modules/commission/accrual.go:217-224`

```go
"gross_amount": gorm.Expr("qy_commission_accrual.gross_amount + ?", in.Gross),
```

`in.Gross` 是 `decimal.Decimal`，其 `Value()`（`shopspring/decimal@v1.4.0/decimal.go:1867-1869`）返回 `d.String()` —— 绑定参数是**字符串**。MySQL 的 `Item_num_op::find_num_type()` 对 STRING_RESULT 操作数取 `numeric_context_result_type() = REAL_RESULT`，整个表达式按 DOUBLE 计算，再回写 `decimal(30,10)` 列。也就是说 `model.go:5` 的第一条铁律"佣金金额一律用 decimal 全精度累计"在**唯一一条累加语句**上不成立。

当前量级下无损：`calcGross` 的结果最多 4 位小数（`base × units / 10000`），gross 远低于 1e11，double 的 ~15.9 位有效数字足够，每步回写 decimal(30,10) 又把误差吃掉。所以定级为低。但这是一条无人看守的边界 —— `testdb_test.go:34-37` 明确写了"本文件的测试刻意不覆盖那条分支"，SQLite 也是 REAL 运算，两边都验证不了。

**修复**：`gorm.Expr("qy_commission_accrual.gross_amount + CAST(? AS DECIMAL(30,10))", in.Gross.String())`，并在 MySQL 上补一条集成断言（2000 次 ×0.001 累加后精确等于 2）。

---

### F7 —— 空 `users.group` 的账号逃出"default"分组规则 【其他｜低】

`qianye/modules/commission/grouprate.go:140` + `grouprate.go:167-169`

`normalizeGroup` 只 TrimSpace，`resolveRate` 对空分组直接早退回落全局默认。而同一轮新增的划转分组规则 `qianye/modules/transfer/grouprule.go:335-341` 明确写着相反的事实：

> 空串归一成 default：主库 users.group 的列默认值就是 'default'，但历史行、以及被直接改过库的账号可能留着空串。不归一的话，一条"default 只能转给 vip"的白名单对这些账号完全不生效 —— **而它们恰恰是最可疑的那批账号**。

**触发场景**：运营给 `default` 分组配 2% 消费返佣（低于全局 5%，因为默认分组毛利最薄）。一批历史账号 `users.group = ''`，`resolveRate` 早退 → 按 5% 返。同一个仓库里两个分组规则模块对同一个数据缺陷给出相反处理，而返佣这一侧是直接出钱的那一侧。

**修复**：`normalizeGroup` 与 transfer 对齐（空串 → `default`），或在 `grouprate.go` 的口径说明里写明"空分组不参与分组费率"是刻意选择并说明理由。

---

## 已核对无问题（避免下一轮重复走查）

- `RatePercentUnits`（`config/validate.go:61-82`）：全程 decimal，负数/超 100/超两位小数/空串/`10%`/科学计数法均拒绝，`int(scaled.IntPart())` 前已钳在 0..10000，窄化不可能溢出；`FormatRatePercent` 往返无损（`rate_percent_test.go:67-78` 覆盖全部 10001 个取值）。
- `bpsToPercent`（`config/defaults.go:46-58`）：只做整数除法与取余，0..10000 全量往返无损（`rate_percent_test.go:81-91`）。
- `adoptDeprecatedRates` + `checkRatePair`（`defaults.go:24-40` / `validate.go:367-388`）：`*int` 指针正确区分"写了 0"与"没写"；只在新字段为空时采纳旧值；旧字段先按自己的口径报错（字段名可搜）；新旧矛盾直接 FatalLog。`applyDefaults` 里 adopt 排在 `strDefault` 之前，顺序正确。
- 费率冻结：`writeAccrual`（`accrual.go:186-208`）把 `RateUnits`/`RateGroup` 写进 `Create`；`Accumulate` 分支的 `DoUpdates` 只改 `base_quota`/`gross_amount`/`updated_at`，而 `consumeIdemKey`（`accrual.go:373-376`）把分组与费率都编进幂等键，冲突时两者必然相同 —— 不存在"一行混两套费率"。冲正（`clawback.go:83-84`、`clawback.go:164-165`）复制原单的 `RateUnits`/`RateGroup`/`UsdRate`，方向正确。
- 幂等键单射性：group 与 units 之间的 `:` 分隔在 units 恒为纯数字的前提下不可能撞键；超 96 字节走 sha256（`accrual.go:76-82`）。
- 升级当天旧 `consume:<id>:<day>` 桶与新键并存只会多一行，不会重复发钱。
- `rateOverride`（`settings.go:184-202`）：百分比键优先、旧 bps 键回落、越界一律丢弃而非钳到 100%，`grouprate_test.go:279-331` 四个子用例真跑库。
- 路由与表：`module.go:26` 注册了 `GroupRate` 表，`module.go:47-48` 两个写接口挂在 `admin` 组（`router.go:44` 的 `AdminAuth`）+ `CriticalRateLimit`。
- 缓存失效：`upsertGroupRate`/`deleteGroupRate` 都调 `invalidateGroupRates()`，`TestGroupRateCrudTakesEffectImmediately` 真跑库验证（多节点仍有 60 秒窗口，与 `blockedInvitees`/`effective` 同一约定）。
- 本轮测试**不是假的**：`grouprate_test.go:132-204` 让 `accrueConsume` 真落库再回读冻结的费率与分组，把 `hook.go:120` 的 `rate.Units` 改回 `s.ConsumeRateUnits` 会立刻红；`TestAccrueConsumeSplitsRowWhenRateChanges` 断言每一行 `base × rate == gross`。`selfcheck_test.go:90-114` 真去 AST 解析登记表指向的源文件，commission 段 8 条登记全部核对通过。
