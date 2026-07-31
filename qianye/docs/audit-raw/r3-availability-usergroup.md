# availability-usergroup

# 审计结果：可用率新维度 + 注册默认分组

`go build ./...` 通过，`go test ./qianye/modules/availability/... ./qianye/modules/usergroup/... ./model/...` 全绿。以下四条均为逐行读过源码并（前两条）实际运行验证过的缺陷。

---

## 1. 【上线阻断 / 功能完全失效】`usergroup` 模块从未被 blank import，需求 4 在生产中根本不存在

**文件**：`C:\Users\Administrator\Desktop\qianye\qianye-newapi\qianye\modules.go:10-18`

`qianye/modules/` 下有 8 个模块目录，`modules.go` 只 import 了 7 个 —— **`usergroup` 缺失**。全仓 `grep -rn "modules/usergroup" --include=*.go .` **零命中**。

于是 `usergroup` 包的 `init()` 永远不执行 → `module.Register(Mod{})` 不执行 → `module.All()` 里没有它 → `InstallHooks()` / `RegisterAdminRoutes()` 全部不执行。

**实测证据**（我临时建包跑了一次，已删除）：
```
registered: logmetrics / availability / commission / transfer / violation / withdraw / groupvis
hook identity? true      ← model.QyResolveNewUserGroup 仍是恒等函数
```

**具体触发场景**（不需要任何构造，默认部署即命中）：
1. 运营打开管理端「新用户默认分组」页（`web/src/features/qy/nav.ts:121` 已经把入口挂上了，路由 `web/src/routes/_authenticated/qy/admin/user-group/index.tsx` 也生成了）。
2. 页面请求 `GET /api/qy/admin/user-group/config` → 该路由从未注册 → **404**。
3. 即使绕过前端直接往 `qy_settings` 写一行 `scope='usergroup', k='default_group', v='vip'`，注册新用户后 `users.group` 仍是 `default` —— 因为 `model/user.go:541` 调的是 `model/qy_usergroup_export.go:37` 的默认恒等实现。

**为什么本轮的自检与测试全都挡不住**：
- `qianye/config/selfcheck.go` 是本轮专门为「定义了却没有消费方」建的防线，但它只反射展开 `Config` 结构体、只对账 YAML 字段。`usergroup` 的配置存在 `qy_settings` 而不是 YAML，**整个模块不在自检的视野里**。第五次同形状缺陷，恰好从新建的防线旁边走过去了。
- `qianye/modules/usergroup/testdb_test.go:124-129` 的 `installHook` 直接调 `Mod{}.InstallHooks()`，`api_admin_test.go:34` 直接调 `adminPutConfig(c)`。两处都跳过了「模块有没有被注册」这一步，所以 8 个测试全绿而功能是死的。注释里写的「注入这一步本身也是被测对象」只覆盖到 `InstallHooks`，没覆盖 `init()→Register`。

**影响等级**：功能完全失效 +（配好之后的）数据错误预期落空。属上线阻断 —— 需求 4 交付了 0%。

**修复**：
```go
// qianye/modules.go
_ "github.com/QuantumNous/new-api/qianye/modules/usergroup"
```
并补一条结构性断言，防止下一个模块再漏（这类缺陷靠 review 抓不住）：
```go
// qianye/modules_test.go
func TestEveryModuleDirIsRegistered(t *testing.T) {
    dirs, _ := os.ReadDir("modules")
    names := map[string]bool{}
    for _, m := range module.All() { names[m.Name()] = true }
    for _, d := range dirs {
        require.Truef(t, names[d.Name()], "modules/%s 未在 modules.go 中 blank import", d.Name())
    }
}
```

---

## 2. 【上线阻断 / 拒绝服务】迁移专用连接只用于 GET_LOCK，`AutoMigrate` 仍跑在带 `readTimeout=30s` 的业务连接池上

**文件**：`qianye/db/migrate.go:106`（`gdb.AutoMigrate(models...)`）vs `migrate.go:63-75` 的注释与 `migrate.go:28-37` `openMigrationConn`

本轮为修 NEW-1 新开了 `migDB`（`migrationDSN` 把 `readTimeout`/`writeTimeout` 置 0，`qianye/db/db.go:306-311`），但它只被用来 `Conn(ctx)` 拿 `GET_LOCK`。真正执行 DDL 的是第 55 行的 `gdb := Get()` —— 那是 `Init()` 用 `normalizeDSN` 建的**业务池**，DSN 里带 `readTimeout=30s`（`db.go:292-294`）。

注释自己列了两条必须绕开 readTimeout 的理由，**只解决了第 1 条（GET_LOCK），第 2 条（大表 ADD COLUMN）原封不动**：

> `// 2. 大表 ADD COLUMN —— 千万行的 DDL 超过 30 秒是常态。被驱动掐断后 MySQL 的 DDL 不可回滚,表会停在半迁移态。`

顺带：`migrateTimeout`（30 分钟）的 ctx 也没有传给 AutoMigrate（不是 `gdb.WithContext(ctx).AutoMigrate`），所以那 30 分钟预算只约束了 GET_LOCK/RELEASE_LOCK 两条语句。DDL 实际的唯一上界就是 30 秒的驱动 readTimeout。

**具体触发场景**（这正是本轮 `speed_count` 的升级路径）：
1. 已有部署，`availability.enabled=true` 跑了一段时间。`qy_avail_bucket` 是全扩展行数最大的表：`(bucket_ts, group_name, model_name)` 三元组 × 5 分钟桶 × `retention_days`（默认 15），(group,model) 基数 500 时约 216 万行，`hot_series_limit` 顶格时上限 8600 万行。
2. 升级重启 → `db.Migrate` → `gdb.AutoMigrate` 对 `qy_avail_bucket` 与 `qy_avail_bucket_hour` 各发一条 `ALTER TABLE ... ADD COLUMN speed_count bigint NOT NULL DEFAULT 0`。MySQL 5.7 上 ADD COLUMN 走 INPLACE 仍需重建全表，百万行级超过 30 秒是常态（本轮同时还要给 `qy_fund_orders` 加 `fingerprint`、`qy_violation_record` 加 `billing_source`/`subscription_id`）。
3. 30 秒到 → go-sql-driver 触发 `SetReadDeadline` → 返回 `invalid connection` / `i/o timeout` → `AutoMigrate` 报错 → `Migrate` 返回 error → `qianye.Init()` 返回 error → 主程序 **FatalLog，起不来**。
4. MySQL 侧 DDL 不因客户端断开而回滚，服务端继续跑；下次重启撞上「表已在迁移中」，反复失败。

**为什么现有测试挡不住**：`qianye/db/db_test.go:196-210` 只断言 `migrationDSN(cfg)` 这个**字符串**里含 `readTimeout=0`，从不验证 `AutoMigrate` 走的是哪条连接。把 `migDB` 整个删掉、退回 `sqlDB, _ := gdb.DB()`，这条测试照样全绿 —— 与前两轮结论里「纯函数层做对了不算数」完全同形。

**影响等级**：拒绝服务（升级阻断）+ 半迁移态数据风险。

**修复**：让 AutoMigrate 真的跑在 `migDB` 上：
```go
migGorm, err := gorm.Open(mysql.New(mysql.Config{Conn: migDB}), &gorm.Config{})
if err != nil { return fmt.Errorf("qianye: 迁移句柄创建失败: %w", err) }
if err := migGorm.WithContext(ctx).AutoMigrate(models...); err != nil { ... }
```
（`migDB.SetMaxOpenConns(1)` 需要放宽到 2，或 GET_LOCK 用另一条独立 `sql.DB`；GORM 会自己从池里取连接。）测试要断言的不是 DSN 字符串，而是「Migrate 使用的 gorm 句柄 ≠ `db.Get()`」。

---

## 3. 【拒绝服务 · 低】`page` 参数整数溢出让 `/availability/matrix` panic

**文件**：`qianye/modules/availability/api.go:553-563`（`paginate`）+ `api.go:565-575`（`pageParams`）

`pageParams` 只做 `if page < 1 { page = 1 }`，没有上界；`paginate` 的 `start := (page - 1) * pageSize` 是 `int` 乘法，溢出后变负数，而 `start >= len(names)` 这道闸门对负数不生效。

**具体触发场景**（任意登录用户，一条 GET 即可）：
```
GET /api/qy/availability/matrix?page=184467440737095518
```
`pageSize` 默认 50 → `(page-1)*50 = -9223372036854775766` → `names[-9223372036854775766 : -9223372036854775716]`。

我实际跑过同一段代码：
```
start= -9223372036854775766
PANIC: runtime error: slice bounds out of range [:-9223372036854775716]
```
`names` 为空也照样 panic（负数永远 `< 0`），所以不需要库里有任何数据。`main.go:182` 的 `gin.CustomRecovery` 会兜住，结果是 500 + 一条完整栈日志；`SearchRateLimit` 允许每用户 10 次/60 秒。

**影响等级**：拒绝服务（低，能被 Recovery 兜住，只污染日志/返回 500）。属既有代码，本轮未改动，但既然接口面向全体登录用户就该收掉。

**修复**：`pageParams` 里给 page 加硬上界（例如 `if page > 100000 { page = 100000 }`），或 `paginate` 改用 int64 并显式判 `start < 0`。`TestPaginate`（`query_test.go:143-149`）需补一个溢出用例。

---

## 4. 【拒绝服务 · 中低】`intersectGroups` 不去重，`groups` 参数无长度上限 → 攻击者可控的内存放大

**文件**：`qianye/modules/availability/api.go:57`、`api.go:84`、`api.go:382-398`（`intersectGroups`）

`splitCSV(c.Query("models"), maxModelFilter)` 给模型清单设了 200 的上限，**同一行的 `splitCSV(c.Query("groups"), 0)` 却传 0 = 不限**（`api.go:57`、`api.go:159-160`）。`intersectGroups` 逐个校验成员资格但**不去重**，重复的合法分组名会原样保留。

**具体触发场景**（任意登录用户，只需 1 个自己有权的分组名）：
```
GET /api/qy/availability/matrix?groups=default,default,default,...   ← 重复 60000 次(约 470KB 查询串)
```
1. `intersectGroups` 返回 60000 个 `"default"`（全部通过权限校验）。
2. `queryCells` → `Where("group_name IN ?", groups)` 生成 60002 个占位符（MySQL 上限 65535，恰好通过），配合 `PrepareStmt: true` 还会在 GORM 语句缓存与 MySQL `max_prepared_stmt_count`（默认 16382）里各留一份 —— 把重复次数从 1 递增到 16382 即可耗尽后者。
3. `api.go:84`：`out := make([]cell, 0, len(pageModels)*len(groups))` = `50 × 60000` = 300 万个 `cell`。我实测 `unsafe.Sizeof(cell{}) == 160`，即**单次请求预分配约 480 MB**。内层循环虽有 `maxSeries()`（默认 2000）截断，但 `make` 的容量在截断之前就已经分配下去了。
4. `groupInfos(groups, visible)`（`api.go:107`、`400-406`）再按同一个长度生成 60000 条重复的 `{group, desc}` 进响应体。

**影响等级**：拒绝服务（中低 —— 需登录账号，`SearchRateLimit` 10 次/60 秒，但 10 × 480MB 足以 OOM）。**不构成分组泄漏**：权限裁剪本身是正确的（见下方「已核对无问题」）。

**修复**：
```go
// api.go:382 intersectGroups —— 用 map 去重
seen := make(map[string]struct{}, len(visible))
for _, g := range requested {
    if _, ok := visible[g]; !ok { continue }
    if _, dup := seen[g]; dup { continue }
    seen[g] = struct{}{}
    out = append(out, g)
}
```
并把 `api.go:57` / `api.go:159-160` 的 `splitCSV(..., 0)` 改成带上限（分组基数天然远小于模型，给 `maxModelFilter` 同级或更小的值即可）。

---

# 已逐条核对、判定无问题的项

| 我被点名要查的 | 结论 |
|---|---|
| **分组泄漏（问题 1）** | **无泄漏，未开倒车**。`visibleGroups`(`api.go:377-379`) → `service.GetUserUsableGroups(c.GetString("group"))`，`group` 由 `middleware/auth.go:199` `setDashboardAuthContext` 写入。构造「用户只有 A 组、请求 B 组」：`intersectGroups(["B"], {A})` 返回空 → `getMatrix` 走 `api.go:58-67` 返回空矩阵（不是 403，侧信道也堵了）；`getSeries` 走 `querySeriesPoints` 的 `len(groups)==0` 早退（`query.go:130`）。`queryCells`/`querySeriesPoints`/`mergeHotCells`/`groupInfos`/`modelNames`/`overallCell`/`groupOfferedModels` 全部只在裁剪后的 `groups` 上工作，`hotBuckets.Range` 两处都有 `allowed[k.group]` 过滤（`query.go:164`、`query.go:203`）。不做裁剪的 `allGroupsInRange`/`adminStats` 只挂在 `/api/qy/admin`（`qianye/router.go:42-44` + `AdminAuth`），`query_test.go:30-46` 有结构性断言钉死这条边界。 |
| **speed_count 的三处消费（问题 2）** | **全部跟上**。聚合：`aggregate.go:65/146/197/223/248`（counters/record/drain/snapshot/restore 五处齐全）；落库：`model.go:116` 进 `counterMap`，`flush.go:144-159` 的 `upsertBucket` 由它派生；rollup：`flush.go:180` `counterColumns()` → `SUM(speed_count)` + `VALUES(speed_count)` 覆盖语义（`flush.go:231-255`）；查询：`query.go:229-237` `selectSums` 同源。`model_test.go:38` + `query_test.go:158` 两条列清单一致性断言。历史行 `speed_count=0` 的处理是**正确的**：`tpsOf`(`perf.go:77`) 先判 `SpeedCount < perfMinSamples` 才做除法，老数据返回 null 而不是荒唐数字；跨升级点的时间范围里 `tps = ΣtokenS / Σms` 仍是一个正确的加权速率，不会被历史行污染。 |
| **除零与溢出（问题 3）** | **安全**。`avgMs`(`perf.go:64-70`) 三重闸门 `count < 5 || count <= 0 || sum <= 0`；`tpsOf`(`perf.go:76-82`) 判 `GenerationMs <= 0`，杜绝 +Inf（注释里点名的 JSON 序列化 500）。`record`(`aggregate.go:143`) 要求 `OutputTokens > 0 && GenerationMs > 0` 才三个量同进同出。drain 逐字段 Swap 的竞态即使让 count 变负，`< perfMinSamples` 也会兜住。token 极大时 `float64` 结果仍是有限值，不会 NaN/Inf。`timings`(`sample.go:104-126`) 对 `StartTime` 零值、负延迟、负 TTFT 都有归零分支。 |
| **样本下限（问题 4）** | **三个指标逐一执行，不足一律 null**。延迟/首字共用 `avgMs` 的 `perfMinSamples=5`，速度用 `tpsOf` 的 `SpeedCount < perfMinSamples`。三个样本数 `LatencySamples/TtftSamples/SpeedSamples` 原样下发，`PerfMinSamples` 随 `Definition` 下发（`outcome.go:263`、`outcome.go:297`）。`perf_test.go:15-95` 的表驱动测试逐格钉死了「不足即 nil」，且覆盖了「历史行缺 speed_count」与「generation_ms 为 0」两个真实边界。 |
| **hook 位置覆盖全部建号路径（问题 5）** | **位置正确**（但见缺陷 1，整条链路是死的）。我把全仓建号点走了一遍：`controller/user.go:275` 密码注册 → `Insert`；`controller/user.go:1039` 管理员建号 → `InsertWithTx`；`controller/oauth.go:380`、`:406` OAuth 两个分支 → `InsertWithTx`；`controller/wechat.go:97` 微信 → `Insert`。四条全部经 `model/user.go:540-552` `prepareForInsert`，hook 在其首行。**绕过 prepareForInsert 的只有 `model/main.go:75`（root 初始化）与 `controller/setup.go:113`（安装向导建首个管理员）两处裸 `DB.Create`** —— 这两处不该套用「新注册用户默认分组」（把 root 塞进受限分组是更坏的结果），判为正确取舍而非遗漏。无批量导入路径；Telegram 只登录不建号。 |
| **分组事后被删（问题 6）** | **两道校验都在**。保存时 `validateDefaultGroup`(`groups.go:60-75`) 经 handler `api_admin.go:75` 调用（测试从 HTTP 入口进，`api_admin_test.go:53`）；应用时 `resolveNewUserGroup`(`resolve.go:34-37`) **再校验一次** `groupExists` → `ratio_setting.ContainsGroupRatio`（纯内存 RWMap，可以在主库事务里跑），失败即回落上游默认并限频告警。`resolve_test.go:62-72` 从真实 `InsertWithTx` 入口验证了「配置时存在、之后被删」这个场景。`auto` 伪分组在保存与应用两侧都被挡（`groups.go:50`、`groups.go:68`）。 |
| **向后兼容（问题 7）** | **成立**。未配置时 `resolveNewUserGroup` 原样返回空串 → GORM 省掉零值列 → `model/user.go:98` 的 `gorm:"default:'default'"` 兜底。`resolve_test.go:19-26` 断言的是**数据库里真正存下来的值**而不是函数返回值，这一点做对了。扩展库不可用时 `readDefaultGroup`(`settings.go:102-108`) 先判 `guard.Available()` 直接早退，绝不阻塞注册；`resolve_test.go:91-106` 覆盖。 |
| **配置接口越权（问题 8）** | **管理员限定**。`RegisterAdminRoutes`(`usergroup.go:68-72`) 挂在 `qianye/router.go:42-44` 的 `/api/qy/admin` 组，该组 `Use(middleware.AdminAuth())`；PUT 额外挂 `CriticalRateLimit()`，且必写审计（`api_admin.go:96-106`，`api_admin_test.go:158` 验证）。—— 前提是模块被注册，见缺陷 1。 |
| `settings.go:71` `resetCache` 注释说「配置重载使用」但生产零调用点 | 不构成缺陷：缓存 60 秒自然过期，且它缓存的是扩展库里的一行设置、与 YAML 热更新无关。属注释与代码不符，不报。 |
| `groups.go:65` `len(name) > 64` 按字节判、`users.group` 是 varchar(64) 按字符 | 只会**过严**（中文长分组名被误拒），不会让超长名字漏过去造成截断。不报。 |
| `currentDefaultGroup` 持 `cacheMu` 期间做扩展库读（`settings.go:44-58`） | 上界 200ms（`loadBudget` 复用 `hot_path_timeout_ms`），且 `withNormalizedEmailLock` 是按 email 的行锁不是全局锁。有放大但幅度可接受，不报。 |

# 总评（本次分工范围内）

**不能上生产**，两条阻断：

1. **缺陷 1** —— 需求 4 交付量为 0，管理端页面直接 404。而且它的失败形状比前两轮更严重：前四次是「配置项没有消费方」，这次是**整个模块没有消费方**。本轮新建的 `selfcheck.go` 只覆盖 YAML 字段，恰好覆盖不到；本轮新写的 8 个 usergroup 测试全部从 `Mod{}.InstallHooks()` 之后切入，也恰好覆盖不到。这两道新防线一起从旁边走过去了。
2. **缺陷 2** —— 上一轮 NEW-1 的修复只落地了一半：DSN 函数写对了、测试也只测了那个字符串，而真正执行 DDL 的调用点没换连接。`speed_count` 这一列的加列动作正是踩它的那一脚。

缺陷 3、4 是同一个只读端点上的两处输入未加固，可以同批收掉，不构成阻塞。

可用率的**数据口径与算术层这次做得是干净的** —— 三个指标各自独立计数、独立下限、一律 null 不显示 0，列清单三处同源派生，权限裁剪有结构性回归测试。问题仍然全部落在「接线」上：模块没接进注册表，迁移连接没接进 AutoMigrate。
