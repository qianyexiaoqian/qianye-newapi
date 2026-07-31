# transfer

已完成:`go build ./...` 通过,`go test ./qianye/modules/transfer/... -count=1` 全绿。以下每条都对应我实际读过的代码。

---

# 确认缺陷

## 1. 管理端"谁能转给谁"矩阵会遗漏规则里引用的分组,把配错的规则显示成"没有效果"

`qianye/modules/transfer/grouprule.go:297`(`buildGroupMatrix` 的 `for _, to := range known`)+ `grouprule.go:312`(`knownGroups`)+ `api_group_rules.go:59`

`knownGroups()` 的取值集合只有三个来源:`defaultGroupName`、`ratio_setting.GetGroupRatioCopy()`、`setting.GetUserUsableGroupsCopy()`。**规则表自己的 `from_group` 与 `to_groups` 完全不参与**。而 `buildGroupMatrix` 的行与列都取自这个集合。

**复现**:
1. 运营历史上有一个分组 `agent`(users 里仍有账号带这个 group),后来把它从分组倍率表里删了 —— 这一情形在本轮的兄弟模块里被明确承认过(`qianye/modules/usergroup/resolve.go:31-33`:"运营完全可能在配置之后把它从倍率表里删掉")。
2. 管理员建规则 `vip → allow_list [agent]`,期望"vip 只能转给 agent"。
3. 打开 `GET /api/qy/admin/transfer/group-rules`:`known_groups` 里没有 `agent`,矩阵既没有 `agent` 这一列,也没有 `agent` 这一行。`vip` 行的 `to_groups` 是**空数组**。
4. 管理员在页面上看到"vip 谁都转不了",据此判断规则配错了,改成 `allow_all` —— 实际上原规则是生效的,vip→agent 一直放行,改完之后 vip 变成能转给任何人。

`grouprule.go:309-311` 的注释"少列一个分组不影响任何判定"只对**判定**成立,对**矩阵**不成立;而 `grouprule.go:283-288` 自己写过"矩阵一旦与真正的判定分家……那比没有矩阵更危险,因为它会让人放心地配错"。这里正是那个形状:判定没分家,取值域分家了。

**影响等级**:数据错误(误导管理员做出反向的策略变更,后果落在资金流向上)

**修复**:`knownGroups()` 收一个规则列表参数(或在 `handleAdminListGroupRules` 里并集):把每条规则的 `from_group`(排除 `*`)与 `parseGroupList(to_groups)` 的每一项(排除 `@self`)并进候选集,再排序。判定端不受影响,只是矩阵的取值域变完整。

---

## 2. 唯一索引按大小写不敏感比较,判定端按大小写敏感比较 —— 大小写不一致的规则静默失效

`qianye/modules/transfer/grouprule.go:123`、`grouprule.go:130`、`grouprule.go:180` vs `api_group_rules.go:229`、`grouprule.go:80`

判定端 `set.byGroup[normalizeGroupName(fromGroup)]` 是 Go map 精确匹配(大小写敏感),`matchGroupList` 的 `entry == to` 同样精确;而扩展库固定 MySQL 且 `qianye/db/db.go:285-286` 只在 DSN 里补 `charset=utf8mb4`、**不指定 collation**,服务器默认是 `utf8mb4_0900_ai_ci`/`utf8mb4_general_ci`,大小写不敏感。于是 `from_group` 的唯一索引与 `groupRuleTaken` 的 `Where("from_group = ?")` 与判定端口径相反。

**复现(闸门方向,危害更大)**:
1. 运营在管理端建规则 `from_group = "vip"`,`policy = deny_all`(vip 组禁止转出)。
2. 某个账号的 `users.group` 是 `"VIP"`(管理员在用户编辑页手输、或历史数据大小写不一)。
3. `ruleFor("VIP")` → `byGroup["VIP"]` 未命中 → 落到兜底规则;没有兜底规则就是**完全不限制**。这个账号照常转出,而管理端矩阵里只有 `vip` 一行(来自倍率表),显示 deny_all,看不出有账号漏网。

**复现(反向,误伤)**:规则 `vip → allow_list [Default]`,真实分组是 `default` → `matchGroupList` 不命中 → 所有 vip→default 的划转被 403 拒绝,而管理员在规则页看到的白名单确实写着 Default。

顺带一个可观测症状:已有 `vip` 规则时再建 `VIP` 会被 `groupRuleTaken` 判成重复(DB 侧不敏感)并回 409 `qy_transfer_group_rule_duplicate`,提示"请直接编辑它",但列表里根本没有叫 `VIP` 的行。

**影响等级**:越权 / 风控失效(一条本意是"限制"的规则变成完全不设防,且无任何迹象)

**修复**:在写入侧收敛,让两端口径一致 —— `validateGroupRule` 把 `r.FromGroup` 与每个 `to_groups` 条目做一次大小写归一(例如统一 `strings.ToLower`),并在 `normalizeGroupName` 里同样归一,使 map 键与 DB 比较语义对齐;若不接受改变分组名大小写语义,则相反地在建表时给 `from_group` 指定 `collate utf8mb4_bin`,并在 `validateGroupRule` 里增加"该分组名不在 `knownGroups()` 中"的显式告警(与 `usergroup.validateDefaultGroup` 同口径)。

---

## 3. 划转明细不冻结双方分组,事后无法回答"这笔划转当时是按什么放行的"

`qianye/modules/transfer/model.go:27-74`(`Order` 结构)+ `service.go:239`(权威判定点)+ `service.go:390-403`(`transferCreatedAudit`)

规则变更这一侧的审计是**做到了**的:`api_group_rules.go:267-280` 的 `writeGroupRuleAudit` 对 create/update/delete 都写了 `BeforeSnap`/`AfterSnap`,硬删也保留了删除前全文(`api_group_rules.go:204`),而 `qy_audit_logs` 全仓没有清理任务(`qianye/config/selfcheck.go:106` 把 `audit.retention_days` 明确标为无消费方)。所以"某时刻规则集是什么"可以重建。

但判定的**另一个输入 —— 双方那一刻的分组 —— 没有任何地方留痕**:
- `applyQuotaTransfer` 用的是行锁内读到的 `sender.Group` / `receiver.Group`,判完即丢;
- `Order` 冗余快照了 `FromUsername`/`ToUsername`(model.go:37-38)与四个余额快照(model.go:52-57),理由写在 model.go:25-26 与 52-53:"主库用户可以改名""主库 users 表没有历史版本,不留快照就没有任何证据",**但没有 group**;
- `transferCreatedAudit` 的 `Reason` 只写 `StatusName(order.Status)`。

**复现**:2026-08-01 建规则 `vip → deny_all`。8 月 10 日发现用户 5 在 8 月 3 日成功转出 100 万。仲裁时:审计表能证明 8 月 3 日 `vip` 确实是 deny_all;`users` 表现在显示用户 5 是 `vip`。无法判断 8 月 3 日他是不是 `vip`(管理员可能 8 月 5 日才把他调进 vip),也就无法区分"规则没生效(缺陷)"与"当时他不在 vip(正常)"。而这正是本模块设计里唯一会变、且"管理员随时会改"的字段(`grouprule.go:189-191` 自己点名了这一点)。

**影响等级**:数据错误 / 审计完整性

**修复**:`Order` 增 `FromGroup` / `ToGroup`(`varchar(64)`),在 `applyQuotaTransfer` 里从锁内的 `sender`/`receiver` 取值随 `quotaSnapshot` 一起带出,由 `settleDetailTx`(`settle.go:65-70` 已有写快照字段的位置)一并落库;`transferCreatedAudit` 的 `Reason` 可附带命中的 `rule_id`/`policy`。

---

## 4. `qy_transfer_group_rules` 缺表时,整个划转功能(创建 / 预览 / 额度页)全线 500

`qianye/modules/transfer/grouprule.go:209-222`(`loadGroupRules`)→ 消费方 `service.go:43`、`handler.go:125`、`handler.go:220`;成因 `qianye/db/migrate.go:47-54`

`loadGroupRules` 的 fail-closed 语义本身是对的("读失败一律向上抛,绝不回落成不限制"),但它把一张**本轮新增的表**变成了三个既有接口的硬前置。而迁移有两道跳过门:`!common.IsMasterNode` 直接 return,`database.auto_migrate=false` 直接 return —— 后者是 `qianye/config/selfcheck.go:74` 明确支持的部署方式("由 DBA 手工建表")。全仓没有任何"模块需要的表是否存在"的启动自检:`db.TableCount()` 只在 `qianye/controller/admin.go:34` 输出一个数字,不与 `allTables()` 的长度比对。

**复现**:
1. 现有部署 `database.auto_migrate: false`(DBA 手工建表),升级到本版本。DBA 没有拿到"本次新增 `qy_transfer_group_rules`"的清单。
2. 任意登录用户 `POST /api/qy/transfer` → `loadGroupRules` → `Error 1146: Table 'qy_transfer_group_rules' doesn't exist` → 走 `errors.go:122` 的 default 分支 → 500 `qy_internal_error` "处理失败,请稍后重试"。
3. `POST /api/qy/transfer/preview`、`GET /api/qy/transfer/limits` 同样 500 —— 钱包页连"我还能转多少"都渲染不出来。
4. 多节点滚动升级同理:从节点跳过迁移,主节点尚未重启的那段窗口内,新代码的从节点上划转全挂。

**影响等级**:拒绝服务(整个划转功能,持续到人工建表)

**修复**:二选一。(a) 启动时按 `allTables()` 逐张 `Migrator().HasTable()`,缺表则 `FatalLog` 并列出表名 —— 与 `bootstrap.go` 现有的"配置写错就该立刻炸"口径一致,且把故障从运行期挪到启动期;(b) 在 `loadGroupRules` 里单独识别 `1146`(表不存在)并按"空表 = 不限制"处理 + 限频 `SysError` 告警 —— 语义上它确实等价于全新部署,但会削弱 fail-closed,不如 (a)。

---

## 5. `@self` 令牌无转义,名为 `@self` 的真实分组无法被写进名单(加固)

`qianye/modules/transfer/grouprule.go:172-185`(`matchGroupList`)+ `grouprule.go:420-424`(`validateGroupRule` 对 `@self` 跳过 `checkGroupToken`)

`matchGroupList` 对 `entry == groupSelfToken` 一律解释成"与发起方同组",没有任何转义写法;`validateGroupRule` 对 `@self` 条目也不做分组名校验。若运营在倍率表里建了一个真的叫 `@self` 的分组,规则 `vip → allow_list [@self]` 的含义会从"只能转给 @self 组"变成"只能转给 vip 组",且矩阵与判定一致地错,无法被发现。`from_group = "@self"` 也能通过校验(`checkGroupToken` 只禁空白与分隔符),这条规则只会匹配 group 字面量为 `@self` 的账号。

其余边界都是对的:用户没有分组时 `normalizeGroupName("")` → `default`,发起方与收款方两侧都归一(`grouprule.go:145-146`),测试也钉住了(`grouprule_test.go:186-195`)。

**影响等级**:其他(需要运营把分组命名成 `@self`,概率极低,但没有任何拦截或提示)

**修复**:`checkGroupToken` 拒绝以 `@` 开头的分组名(`from_group` 与名单条目都校验),把 `@` 前缀保留为令牌命名空间;顺带为将来新增令牌留出空间。

---

# 逐条回答审计要点

1. **两处判定都在且都会执行**。受理侧 `service.go:190`(在 `loadParties` 里,排在收款人存在性/状态之后,避免枚举旁路);主库行锁内 `service.go:239`。`MainApply` 只在 `applyOnMainDB`(`twophase/execute.go:301`)探到 outbox 已认领时才跳过,而那只发生在补偿重跑同一 `order_no` 的场景 —— 新单的 `order_no` 由 `NewOrderNo` 现生成,不可能预先存在。补偿路径(`compensate.go:97` → `transfer/reconcile.go:20`)只做收尾,不重放资金变更,因此不构成绕过。`grouprule_test.go:277-303` 用 AST 解析 `service.go` 钉住了这两个调用点,删掉任一处测试会红 —— 这条不是假测试。(唯一的盲点:测试只检查"调用发生",不检查返回值被使用,`_ = enforceGroupPolicy(...)` 仍能通过。)

2. **快照时机自洽,但"白吃冷却"这半句成立**。规则在 `service.go:43` 冻结一次、两处共用,管理员在两次判定之间改规则**不会**造成"受理放行、锁内拒绝"。改**分组**会:此时锁内拒绝 → `markFailed` → `releaseOnFailure`(`service.go:118`)→ `settleDetail(statusFailed)` → `refundReservation`(`settle.go:99`)把日额度、日笔数、收款方 `DayInCount`、`PendingCount` **全部原路退还**,所以"白吃预占"不成立;但 `undoReservation`(`risk.go:130-133`)刻意不回滚 `LastOutAt`,冷却确实被消耗一次(已在注释里承认为保守取舍)。另外这一笔的 `client_request_id` 会被烧成 `StatusFailed`,同 id 重试拿到 400「该划转此前已失败」—— 这与余额不足等既有锁内拒绝同形,不是本轮引入。

3. **失败模式正确**。`loadGroupRules` 在 `db.Get()==nil` 时返回 `db.ErrNotReady`(→ 503 + Retry-After),查询报错时 `db.MarkFailure` 后原样上抛,没有任何回落分支;三个消费方(create/preview/limits)全部 `return`,没有一处吞掉错误。空表确实等于不限制(`buildGroupRuleSet(nil)` → `ruleFor` 返回 nil → `allowsGroup` 返回 nil),`grouprule_test.go:64-76` 钉住。**唯一的问题是缺表时也走这条 fail-closed 路径**,见缺陷 4。

4. **`@self` 解析正确**,边界见缺陷 5;"用户没有分组"这一边界处理正确。

5. **越权面干净**。四个规则接口都在 `RegisterAdminRoutes`(`module.go:41-49`),该组在 `qianye/router.go:44` 已挂 `middleware.AdminAuth()`,普通用户读不到规则表。用户侧只拿得到自己的策略(`handler.go:277` `describeGroupPolicy`),不含他人分组归属;`findRecipient`(`lookup.go:159`)虽然把 `group` 查出来参与判定,但 `previewResponse` 里没有该字段,不下发。preview 确实泄漏一位"对方在你转不到的组里"(`lookup.go:86-94` 已承认,并用 `SearchRateLimit` + `qy_transfer_lookup_logs` 兜底),这一位在提交时同样会暴露,不构成新增泄漏面。

6. **规则变更审计完整,单笔划转的判定依据缺失** —— 见缺陷 3。另有一处小瑕疵:`api_group_rules.go:204` 的删除审计把同一份快照同时写进 `BeforeSnap` 与 `AfterSnap`,读审计的人只能靠 action 名区分,建议 `AfterSnap` 置空。

7. **与佣金分组费率 / 注册默认分组的交互**:三者都以 `users.group` 为唯一事实源,`usergroup` 用 `ratio_setting.ContainsGroupRatio` 做存在性校验、`transfer.knownGroups()` 用同一个 `GetGroupRatioCopy()` 做候选集,口径一致,未发现分组变化后的一致性问题。唯一的耦合风险是缺陷 2 的大小写口径:`usergroup` 的 `groupExists` 走 `ratio_setting.ContainsGroupRatio`(map 精确匹配,大小写敏感),与 transfer 判定端一致,但都与 transfer 规则表的 DB 比较不一致 —— 修缺陷 2 时应统一到同一套归一函数。
