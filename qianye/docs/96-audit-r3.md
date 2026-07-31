## 一、对抗性复核结论

我逐条打开了 file:line,并实跑了 4 处变异。**24 条里 0 条误报,21 条确认,3 条降级为"理论成立但当前不可达/需前置条件"。**

### commission

| 条目 | 判定 | 复核要点 |
|---|---|---|
| **F1** 费率取 `users.group` 而非计价分组 | **确认（最高优先级资损）** | `hook.go:54` 的 `consumeEvent{InviteeId,Quota,At}` 确实丢掉了 `params.Group`;`model/log.go:339` 该字段存在,`service/quota.go:256` 与 `service/text_quota.go:538` 都填的是 `relayInfo.UsingGroup`;`middleware/auth.go:473` 确实把令牌分组覆盖成 `UsingGroup`。`hook.go:120` 用的是 `e.InviteeGroup`,而它来自 `inviter.go:100` 的 `Select(...,"group")`。**更糟一层审计没提**:`InviteeGroup` 还被塞进 `inviterCache`,所以即便分组口径改对了,读到的也是缓存里的旧分组。 |
| **F2** `varchar(64)` 无 COLLATE | **确认,但触发需前置条件** | `grouprate.go:61` 确无 collation;扩展库固定 MySQL(`sql.Open("mysql",...)`),`db.go:49` 只补 `charset=utf8mb4` 不补 collation。`upsert` 静默改错行的机制成立。**前置条件是"平台同时存在只差大小写的两个分组名"** —— 不是自动触发。定级中,不是中高。 |
| **F3** 批量保存无事务 + 失败零审计 | **确认** | `api_admin.go:128-134` 是裸 for + `internalError` 早退,`audit.Write` 在 149 行之后。注释 95-96 行的承诺只挡住了 400。 |
| **F4** `effective()` 持锁裸查 | **确认** | `settings.go:99-100` `settingsMu.Lock()` + `defer`,第 107 行 `loadOverrides()`,`settings.go:136` 是 `gdb.Where(...)` 无 `WithContext`。同函数体里 `resolveRate`/`writeAccrual` 都接了 ctx —— 是漏,不是取舍。 |
| **F5** 静默降级 + `Matched` 零消费方 | **确认** | `grouprate.go:113-118` 三条返回路径全部无告警。`grep -rn "\.Matched"` 全仓只有 `grouprate.go:174` 赋值 + 2 处测试断言,**生产零读方**。 |
| **F6** decimal 走字符串绑定 | **确认(低)** | `accrual.go:222` 属实。当前量级无损,定级正确。 |
| **F7** 空分组逃逸 | **确认(低)** | `grouprate.go:140` 只 TrimSpace,`:167` 空串早退;而 `transfer/grouprule.go:337` 明确把空串归一成 `default` 并写了理由。同仓两套口径,出钱这一侧是宽松的那个。 |

补充核对:`calcGross`(`accrual.go:39-46`)全程 decimal,`capGross` 双向钳位正确,**没有裸 int 转换,不违反 AGENTS.md 的计费不变量**。`RateGroup` 在管理端 API 是原样下发的(`api_admin.go:415` 返回 `[]Accrual`,结构体带 `json:"rate_group"`),所以它不是"零消费方"——但 F5 说的"回落场景下 RateUnits 与全局默认恰好相等、判据自相矛盾"仍然成立。

### transfer

| 条目 | 判定 |
|---|---|
| **1** 矩阵取值域漏规则引用的分组 | **确认**。`buildGroupMatrix` 行列都取自 `knownGroups()`,而后者三个来源里没有规则表自身。判定不受影响,矩阵会把"生效中的限制"显示成"谁都转不了"。 |
| **2** collation 口径与判定相反 | **确认**。`GroupRule.FromGroup` 无 collation,`byGroup` 是 Go map 精确匹配。deny_all 失效那一侧比 commission F2 严重。同样需要"存在大小写变体的分组名"这个前置。 |
| **3** Order 不冻结双方分组 | **确认**。`grep -n "Group" model.go` 零命中;`Order` 冻结了用户名和四个余额快照,唯独没冻结判定用的那个字段。 |
| **4** 缺表全线 500 | **确认**。全仓无任何 `Migrator().HasTable()` 启动自检(只有一处在测试里)。`auto_migrate=false` 是 selfcheck 明确支持的部署方式。 |
| **5** `@self` 无转义 | **确认为加固**。`checkGroupToken`(`grouprule.go:444-451`)只禁空白与分隔符,不禁 `@` 前缀。需要运营把分组命名成 `@self`,概率极低。 |

### availability + usergroup

| 条目 | 判定 |
|---|---|
| **1** usergroup 从未 blank import | **确认,且情况比报告更糟**。`grep -rn "modules/usergroup" --include=*.go .` 退出码 1,零命中。**并且:并行会话在我复核期间刚改过 `qianye/modules.go`,加进了 `grouppricing` 和 `sitetheme` 两行 —— 仍然没有 `usergroup`。**同一个文件被第二次编辑、第二次漏掉,说明这不是一次疏忽,是缺一道结构性断言。 |
| **2** AutoMigrate 跑在业务池 | **确认**。`migrate.go:106` 是 `gdb.AutoMigrate(models...)`,`gdb` 来自第 55 行的 `Get()`;`migDB` 只在 84-89 行用于 `Conn`+`GET_LOCK`。且没有 `WithContext(ctx)`,30 分钟预算只约束了两条 LOCK 语句。注释里自己列的第 2 条理由(大表 ADD COLUMN)原封未解决。 |
| **3** `page` 溢出 panic | **确认**。`queryInt` 用 `strconv.Atoi`,`184467440737095518 < MaxInt64` 能解析;`(page-1)*50` 溢出为负;`start >= len(names)` 对负数不生效;`names[start:end]` 必 panic。`names` 为空也照样 panic。 |
| **4** `intersectGroups` 不去重 + `groups` 无上限 | **确认**。`api.go:57` 的 `splitCSV(..., 0)` 对照同行 `models` 的 `maxModelFilter`;`intersectGroups`(`api.go:382-397`)无 `seen` map;`api.go:84` 的 `make([]cell, 0, len(pageModels)*len(groups))` 在 `maxSeries()` 截断之前就分配完了。 |

### crosscut

| 条目 | 判定 |
|---|---|
| **D1** lease 用错误做控制流 × `PrepareStmt:true` | **确认,有生产实证**。`run.err.log:4-13` 十条 `获取租约失败: sql: statement is closed`;`run.log` 同一秒的 1062 与 statement-is-closed 交错。`lease.go:161-162` 是 `SysError` + `continue`,**整轮跳过**属实。 |
| **D2** AST 测试是假防线 | **确认,我实测复现,且范围比报告更大** —— 见下。 |
| **D3** `onRelaySample` 无 recover | **确认为加固**。`sample.go:44-50` 同步段 `buildSample` 确实裸奔,对照 `violation/guard.go:60` 有 `defer recoverHot`。当前无可达 panic 路径。 |
| **D4** `adminInvalidateCache` 漏 `groupRateCache` | **确认**。`api_admin.go:541-546` 三个 invalidate,无 `invalidateGroupRates()`。 |

---

## 二、"这个形状本轮又出现了吗" —— 出现了,5 次,其中 1 次是我新发现的

我按四种已知形状主动搜了一遍(结构体字段零读方、函数改了调用点没换、模块目录 vs 注册表、测试覆盖算术不覆盖调度),并跑了 4 组变异验证。

**变异实测结果:**

| 变异 | 结果 |
|---|---|
| `usergroup/resolve.go` 去掉 `groupExists` 复检 | ❌ 被捕捉(`TestNewUserGroup_AutoGroupIsNeverApplied` 等) |
| `availability/perf.go` 去掉 `SpeedCount < perfMinSamples` | ❌ 被捕捉(2 个子用例) |
| `transfer/service.go:239` 丢弃 `enforceGroupPolicy` 返回值(锁内) | ✅ **全绿** |
| `transfer/service.go:190` 丢弃 `enforceGroupPolicy` 返回值(受理) | ✅ **全绿** ← 新发现 |

**上一轮报告只测了锁内那处。我把受理那处也废掉,transfer 包同样全绿。也就是说:划转分组限制的两道闸门可以被同时彻底废除,而 `TestGroupPolicyIsEnforcedAtBothStagesOfCreate` 依然通过、整包依然 `ok`。**这条测试的名字里写着"IsEnforced",它验的只是"这个函数名还在源码里出现过"。

**本轮新增代码里,这个形状的 5 处实例:**

1. **模块级零消费方** —— `usergroup` 整个模块没被注册。这是形状的最严重变体:前四轮是"配置项没消费方",这次是"整个功能没消费方"。而且本轮专门为此新建的 `selfcheck.go` 只扫 YAML 字段,`usergroup` 的配置在 `qy_settings` 里,防线正好覆盖不到。
2. **字段级零消费方** —— `rateDecision.Matched`,注释写"用于日志与管理端解释",生产零读方,只有测试在断言它。
3. **改了函数没接调用链** —— `migrationDSN` 写对了、测试也只断言那个 DSN 字符串,而真正执行 DDL 的 `gdb.AutoMigrate` 根本没换连接。上一轮 NEW-1 的"修复"只落地了函数,没落地调用点。
4. **测试覆盖调用、不覆盖生效** —— transfer 两处 `enforceGroupPolicy`(上面实测)。
5. **取值域与判定分家** —— `knownGroups()` 与规则表分家,导致管理端矩阵与真实判定给出相反结论。这是同一形状换了个层次:判定接上了,但**喂给判定的取值集合**没接上。

结论:**形状没有消失,只是从"函数层"上移到了"接线层"和"取值域层"。**上一轮新建的两道防线(selfcheck、AST 调用点检查)各自都被本轮的新实例从旁边绕过去了 —— selfcheck 只认 YAML,AST 检查只认函数名。

---

## 三、最终判断:**不能上生产**

扣费相关的算术层这三轮下来是干净的(decimal 全程、`common.QuotaFromFloat` 系列、饱和审计、幂等键单射性),我抽查未发现问题。**问题全部在接线、口径来源和收尾。而这些恰恰是会真出钱的地方。**

### P0 —— 必须先修,否则不能上线

| # | 项 | 理由 |
|---|---|---|
| 1 | **`usergroup` 加进 `modules.go`,并补"模块目录 ⊆ 注册表"的结构性断言** | 需求 4 交付量 0%,管理端页面直接 404。断言必须补 —— 同一个文件已经被漏了两次(并行会话刚加了 grouppricing/sitetheme 仍没加它)。 |
| 2 | **AutoMigrate 改走 `migDB` 并 `WithContext(ctx)`** | 升级阻断。本轮给 `qy_avail_bucket`(百万级起)加 `speed_count`、给 `qy_fund_orders` 加 `fingerprint`,DDL 超 30 秒即 FatalLog 起不来,且 MySQL DDL 不回滚,重启会反复失败。测试要断言"Migrate 用的句柄 ≠ `db.Get()`",不是断言 DSN 字符串。 |
| 3 | **commission F1:`consumeEvent` 带上 `params.Group`,费率按计价分组解析** | **唯一可被用户主动构造的资损路径**:建一个低费率分组的令牌把流量走过去,平台按批发价收钱、按零售价付佣金,而流水行自洽、看不出异常。顺带处理 `InviteeGroup` 走缓存这个二级问题。 |
| 4 | **lease D1:把 INSERT-撞键 改成 `INSERT ... ON DUPLICATE KEY UPDATE` 或先 UPDATE 后 INSERT** | 有生产日志实证,每个周期对齐点打掉 N−1 个后台任务整轮,其中包括 `twophase.compensate` 和 `withdraw.reconcile` —— 资金中间态的收尾被无声拉长。兜底:`Acquire` 把 `statement is closed` 视作可重试。同形状的 `twophase/execute.go:240` 一并排查。 |

### P1 —— 上线前同批收掉

5. **transfer #4**:启动时按 `allTables()` 逐张 `HasTable()`,缺表即 FatalLog 列出表名。如果你的部署用 `auto_migrate=false`,这条升格为 P0(划转创建/预览/额度页全线 500)。
6. **collation 口径统一**(commission F2 + transfer #2):两张表的分组名列二选一 —— 要么 `COLLATE utf8mb4_bin` 让存储跟上代码,要么全链路 `ToLower` 让代码跟上存储。不能留"代码敏感、存储不敏感"的中间态。transfer 那一侧更急:一条 deny_all 可以静默变成完全不设防。
7. **commission F3**:`writeSetting` 循环包进事务,失败分支补 `ResultFail` 审计。
8. **D2 补行为级回归**:直接驱动 `applyQuotaTransfer` 和 `loadParties`,断言返回 `errGroupTargetDenied` 且两行 `quota` 一分未动。AST 检查保留作第二道,不能是唯一一道。
9. **availability #3/#4**:`pageParams` 给 page 加硬上界、`intersectGroups` 去重、`splitCSV(groups)` 给上限。同一个只读端点两处输入未加固,一起收。

### P2 —— 可上线后处理

F4(`effective`/`loadOverrides`/`refSalt` 接 ctx 并把查库移出临界区)、F5(降级告警 + `RiskFlags` 打标)、F6、F7、transfer #1/#3/#5、D3、D4。

### 另外两件与代码无关但必须做

- `run.log` / `run.err.log` 是未跟踪的运行日志,进 `.gitignore`。
- `qianye/modules/grouppricing` 目前编译不过(`go build ./...` 失败于 6 个 undefined),那是并行会话在写的东西 —— 但它现在已经被 `modules.go` blank import 了,**主程序在这个状态下构建不出来**。合并前必须确认那个会话收尾。

---

**一句话:算钱的算术是对的,决定按什么口径算钱的那一层不是。** F1 是唯一一条用户能主动构造、平台稳定亏钱、且流水上看不出来的路径 —— 它和另外三条 P0 一起,决定了这版现在不能上。
