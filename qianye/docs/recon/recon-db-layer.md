# 数据库层与多数据库支持

# 数据库初始化与多数据库支持机制 — 勘察报告

## 一、总览:现有两套数据库的完整链路

### 1.1 全局 DB 变量(共 2 个)

| 变量 | 定义位置 | 类型 | 说明 |
|---|---|---|---|
| `DB` | `model/main.go:53` | `*gorm.DB` | 主库,承载除 logs 外的全部业务表 |
| `LOG_DB` | `model/main.go:55` | `*gorm.DB` | 日志库;未配置 `LOG_SQL_DSN` 时**直接赋值为 `DB`**(`model/main.go:214`) |

两者都是 `package model` 的**导出**包级变量,外部通过 `model.DB` / `model.LOG_DB` 访问(例如 `main.go:312` 的 `authz.Init(model.DB)`)。

### 1.2 数据库"类型"状态机 —— `common/database.go`(全文仅 45 行,是整套机制的核心)

```go
// common/database.go:3-10
type DatabaseType string
const (
    DatabaseTypeMySQL      DatabaseType = "mysql"
    DatabaseTypeSQLite     DatabaseType = "sqlite"
    DatabaseTypePostgreSQL DatabaseType = "postgres"
    DatabaseTypeClickHouse DatabaseType = "clickhouse"
)
// common/database.go:12-13
var mainDatabaseType = DatabaseTypeSQLite
var logDatabaseType  = DatabaseTypeSQLite
```

完整函数签名(全部在 `common/database.go`):
- `func MainDatabaseType() DatabaseType` — :15
- `func LogDatabaseType() DatabaseType` — :19
- `func SetMainDatabaseType(databaseType DatabaseType)` — :23
- `func SetLogDatabaseType(databaseType DatabaseType)` — :27
- `func SetDatabaseTypes(mainType DatabaseType, logType DatabaseType)` — :31
- `func UsingMainDatabase(databaseType DatabaseType) bool` — :36
- `func UsingLogDatabase(databaseType DatabaseType) bool` — :40
- `var SQLitePath = "one-api.db?_busy_timeout=30000"` — :44(由 `common/init.go:69-71` 从 `SQLITE_PATH` 覆盖)

**注意:这是两个全局可变变量,没有加锁**,只在启动期写一次。新增第三个库时若沿用此模式,应新增自己的 `xxxDatabaseType` 变量(不要复用这两个,否则会污染主库/日志库的方言分支判断)。

### 1.3 环境变量读取点(精确位置)

| 环境变量 | 读取位置 | 默认值 |
|---|---|---|
| `SQL_DSN` | `model/main.go:172` → `chooseDB("SQL_DSN", false)` → `os.Getenv(envName)` @ `model/main.go:128` | 空 → SQLite |
| `LOG_SQL_DSN` | `model/main.go:175`(判空)、`model/main.go:213`(判空)、`model/main.go:219` → `chooseDB("LOG_SQL_DSN", true)` | 空 → `LOG_DB = DB` |
| `SQLITE_PATH` | `common/init.go:69-71` | `one-api.db?_busy_timeout=30000` |
| `SQL_MAX_IDLE_CONNS` | `model/main.go:193`(主库)、`model/main.go:237`(日志库) | 100 |
| `SQL_MAX_OPEN_CONNS` | `model/main.go:194`、`model/main.go:238` | 1000 |
| `SQL_MAX_LIFETIME` | `model/main.go:195`、`model/main.go:239`(单位秒) | 60 |
| `SQL_SLOW_THRESHOLD_MS` | `model/gorm_logger.go:33` | 200(上限 `maxSlowThresholdMs = 3600000`,`model/gorm_logger.go:22`) |
| `LOG_SQL_CLICKHOUSE_TTL_DAYS` | `model/main.go:415` | 0(不删) |
| `DEBUG` | `common/init.go:87` → `common.DebugEnabled`,在 `model/main.go:179-181`/`223-225` 决定是否 `db.Debug()` |
| `NODE_TYPE` | `common/init.go:89` → `common.IsMasterNode`,`model/main.go:197`/`241` 决定是否执行 AutoMigrate |

`.env.example:20-38` 已文档化了上述数据库变量。`docker-compose.yml:29-33` 有 `SQL_DSN`/`LOG_SQL_DSN` 示例。

**关键点:连接池参数是全局共享的三个环境变量,主库和日志库用完全相同的值**(两段代码逐字重复)。新增第三库应当引入自己的池参数(来自 YAML),不要复用这三个 env。

### 1.4 `chooseDB` —— DSN 方言探测(model/main.go:127-169)

```go
func chooseDB(envName string, isLog bool) (*gorm.DB, common.DatabaseType, error)
```

逻辑分支顺序(严格按此顺序):
1. `dsn := os.Getenv(envName)`;若为空 → 直接 SQLite(`model/main.go:165-168`)
2. `isClickHouseDSN(dsn)`(`model/main.go:107-112`,前缀 `clickhouse://` / `tcp://` / `http://` / `https://`)→ 若 `!isLog` 直接返回错误;否则 `clickhouse.Open(normalizeClickHouseDSN(dsn))` + `newGormConfig(false)`(**注意 PrepareStmt=false**)
3. 前缀 `postgres://` / `postgresql://` → `postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true})` + `newGormConfig(true)`
4. 前缀 `local` → SQLite
5. 兜底 → MySQL,**自动补 `parseTime=true`**(`model/main.go:155-161`:有 `?` 就 `&parseTime=true`,否则 `?parseTime=true`),`mysql.Open(dsn)` + `newGormConfig(true)`

辅助:`func normalizeClickHouseDSN(dsn string) string`(`model/main.go:114-125`,https 时自动补 `secure=true`)。

### 1.5 `newGormConfig` —— GORM 配置工厂(model/gorm_logger.go:25-30)

```go
func newGormConfig(prepareStmt bool) *gorm.Config {
    return &gorm.Config{
        PrepareStmt: prepareStmt,
        Logger:      newGormLogger(os.Stdout),
    }
}
func newGormLogger(w io.Writer) logger.Interface   // gorm_logger.go:32-47
```

**`newGormConfig` 和 `newGormLogger` 都是小写不导出**,只能在 `package model` 内使用。日志脱敏逻辑在 `sanitizedLogWriter`(`model/gorm_logger.go:51-64`)与 `sanitizeDBError`(`:68-86`,把 MySQL/PG/ClickHouse/SQLite 驱动错误收敛为错误码)。

### 1.6 `InitDB` 完整流程(model/main.go:171-210)

```go
func InitDB() (err error)
```
1. `chooseDB("SQL_DSN", false)` — :172
2. `common.SetMainDatabaseType(dbType)` — :174
3. **若 `LOG_SQL_DSN` 为空,顺手把 log 类型也设成主库类型** — :175-177
4. `initCol()` — :178(初始化列名引号常量,见 §3)
5. `DebugEnabled` → `db.Debug()` — :179-181
6. `DB = db` — :182
7. MySQL 时 `checkMySQLChineseSupport(DB)`,失败 **panic** — :184-188
8. `DB.DB()` 取 `*sql.DB`,设 `SetMaxIdleConns / SetMaxOpenConns / SetConnMaxLifetime` — :189-195
9. `if !common.IsMasterNode { return nil }` — :197-199(**从节点不迁移**)
10. `migrateDB()` — :204
11. 失败路径:`common.FatalLog(err)`(`common/sys_log.go:31-37`,内部 `os.Exit(1)`)— :207

### 1.7 `InitLogDB` 完整流程(model/main.go:212-251)

```go
func InitLogDB() (err error)
```
1. **`LOG_SQL_DSN` 为空 → `LOG_DB = DB`;`SetLogDatabaseType(common.MainDatabaseType())`;`initCol()`;return** — :213-218(**这一段就是"第三库回落到主库"的现成模板**)
2. 否则 `chooseDB("LOG_SQL_DSN", true)` — :219
3. `SetLogDatabaseType(dbType)` → `initCol()` → Debug → `LOG_DB = db` — :221-226
4. MySQL 时字符集检查 — :228-232
5. 同一套连接池参数 — :233-239
6. `if !common.IsMasterNode { return nil }` — :241-243
7. `migrateLOGDB()` — :245

### 1.8 AutoMigrate 按库分别执行

**主库**(`migrateDB()`,`model/main.go:253-315`),`DB.AutoMigrate(...)` 注册的 **34 张表**(:261-295,按源码顺序):
`Channel, Token, User, UserSession, AuthFlow, ExternalIdentityClaim, PasskeyCredential, Option, Redemption, Ability, Log, Midjourney, TopUp, QuotaData, Task, Model, Vendor, PrefillGroup, Setup, TwoFA, TwoFABackupCode, Checkin, SubscriptionOrder, UserSubscription, SubscriptionPreConsumeRecord, CustomOAuthProvider, UserOAuthBinding, PerfMetric, SystemInstance, SystemTask, SystemTaskLock, CasbinRule, AuthzRole`

AutoMigrate **之前**先跑两个手写 DDL 迁移:
- `migrateSubscriptionPlanPriceAmount()` — `model/main.go:255`,定义在 `:634-690`(float→decimal(10,6),SQLite 跳过)
- `migrateTokenModelLimitsToText()` — `:257`,定义在 `:581-630`(varchar→text)

AutoMigrate **之后**:
- `InitializeUserAuthVersions()` — :299
- `InitializeExternalIdentityClaims()` — :302
- `SubscriptionPlan`:SQLite 走手写 `ensureSubscriptionPlanTableSQLite()`(`:500-577`),其余走 `DB.AutoMigrate(&SubscriptionPlan{})` — :305-313

**日志库**(`migrateLOGDB()`,`model/main.go:399-404`):
```go
func migrateLOGDB() error {
    if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
        return migrateClickHouseLogDB()
    }
    return LOG_DB.AutoMigrate(&Log{})
}
```
ClickHouse 分支:`migrateClickHouseLogDB()`(:406-412)→ 手写 `CREATE TABLE IF NOT EXISTS logs`(`clickHouseLogCreateTableSQL`,:437-464)+ `syncClickHouseLogTTL`(:466-480)。TTL 辅助:`clickHouseLogTTLDays`(:414)、`clickHouseLogTTLExpression`(:422)、`clickHouseLogTTLClause`(:429)、`clickHouseLogTableHasTTL`(:482)、`clickHouseCreateTableHasTTL`(:490)。

**已有 quirk**:`&Log{}` **同时**出现在 `migrateDB` 和 `migrateLOGDB` 中 —— 分库时主库里也会建一张空的 `logs` 表。

**死代码**:`migrateDBFast()`(`model/main.go:317-397`,并发 AutoMigrate 版本)**全仓无任何调用点**,已确认。

### 1.9 关闭 / 优雅退出

```go
// model/main.go:692-699
func closeDB(db *gorm.DB) error   // 不导出

// model/main.go:701-709
func CloseDB() error {
    if LOG_DB != DB {          // 指针比较,避免重复 Close
        if err := closeDB(LOG_DB); err != nil { return err }
    }
    return closeDB(DB)
}
```
调用点唯一:`main.go:71-76`
```go
defer func() {
    err := model.CloseDB()
    if err != nil { common.FatalLog("failed to close database: " + err.Error()) }
}()
```
注意:该 `defer` 在 `main()` 内,在 `srv.Shutdown(ctx)`(`main.go:231`)之后、函数返回时执行。**没有任何"关闭钩子注册表"** —— 想让第三个库在此处一起关闭,必须改 `model/main.go:701` 的 `CloseDB` 或 `main.go:71`。

### 1.10 健康检查 / Ping / 重连

- `model/main.go:803-806`:`var lastPingTime time.Time` + `var pingMutex sync.Mutex`
- `model/main.go:808-831`:`func PingDB() error` —— 加锁、**10 秒节流**(`time.Since(lastPingTime) < time.Second*10` 直接返回 nil)、`DB.DB()` → `sqlDB.Ping()`,成功后 `common.SysLog("Database pinged successfully")`
- **只 ping `DB`,不 ping `LOG_DB`**
- 唯一调用点:`controller/misc.go:26`(`TestStatus`),路由 `router/api-router.go:27` → `GET /api/status/test`,带 `middleware.AdminAuth()`
- **没有任何自动重连 / 断线重试逻辑**,完全依赖 `database/sql` 连接池自身的坏连接剔除

### 1.11 MySQL 字符集校验

```go
// model/main.go:714
func checkMySQLChineseSupport(db *gorm.DB) error
```
接收任意 `*gorm.DB`(已经是可复用的通用函数,但不导出)。检查 ① `information_schema.SCHEMATA` 的 `DEFAULT_CHARACTER_SET_NAME`/`DEFAULT_COLLATION_NAME`;② 所有 BASE TABLE 的 `TABLE_COLLATION`。允许集合 `utf8mb4/utf8/gbk/big5/gb18030`(`model/main.go:726-732`)。调用方在失败时 **panic**(`:186`、`:230`)。

---

## 二、`lockForUpdate`

`model/locking.go:20-25`:
```go
func lockForUpdate(tx *gorm.DB) *gorm.DB {
    if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
        return tx
    }
    return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}
```
- **不导出**,只能在 `package model` 内用。
- **硬编码判断的是主库类型 `UsingMainDatabase`** —— 若在第三个库上复用它,SQLite 判断会串味。新库需要自己的 `lockForUpdateX(tx)`。
- `AGENTS.md:86` 强制要求:`model/` 下所有 `SELECT ... FOR UPDATE` 必须走这个 helper,禁止 GORM v1 的 `tx.Set("gorm:query_option", "FOR UPDATE")`。
- 测试:`model/locking_test.go`。

---

## 三、`commonGroupCol` / `commonKeyCol` / `commonTrueVal` / `commonFalseVal`

定义:`model/main.go:22-25`(均为**包级不导出 `string` 变量**),另有 `logKeyCol`、`logGroupCol` 在 `model/main.go:27-28`。

赋值:`func initCol()`,`model/main.go:30-51`
```go
if common.UsingMainDatabase(common.DatabaseTypePostgreSQL) {
    commonGroupCol = `"group"`;  commonKeyCol = `"key"`
    commonTrueVal  = "true";     commonFalseVal = "false"
} else {
    commonGroupCol = "`group`";  commonKeyCol = "`key`"
    commonTrueVal  = "1";        commonFalseVal = "0"
}
switch common.LogDatabaseType() {
case common.DatabaseTypePostgreSQL: logGroupCol = `"group"`; logKeyCol = `"key"`
default:                            logGroupCol = "`group`"; logKeyCol = "`key`"
}
```

`initCol()` 的调用点共 3 处:`model/main.go:178`(InitDB)、`:216`(InitLogDB 回落分支)、`:222`(InitLogDB 独立库分支)。

使用点(部分):`model/ability.go:46,68,94,95,101`、`model/channel.go:142,144,400,926`、`model/channel_satisfy.go:48,59`、`model/model_meta.go:178`、`model/perf_metric.go:56,90,108`、`model/subscription.go:434`、`model/token.go:176,282`;`logGroupCol` 在 `model/log.go:501,591,651,652`。

`logKeyCol` 目前**已定义但无任何使用点**(死变量)。

---

## 四、其他相关事实

- **YAML 依赖已就绪**:`go.mod` 直接依赖 `gopkg.in/yaml.v3 v3.0.1`。当前全仓 Go 代码只有 `i18n/i18n.go:11` 用它(用于 embed 的 locales)。**新增独立 YAML 配置无需加新依赖**。
- **`.env` 加载**:`main.go:287` `godotenv.Load(".env")` 在 `InitResources()` 最开头,失败仅打日志。
- **`InitResources()` 调用顺序**(`main.go:284-368`,这是唯一的启动装配点):
  ```
  godotenv.Load(".env")            :287
  common.InitEnv()                 :295   ← 所有 env 常量在此落位
  logger.SetupLogger()             :297
  ratio_setting.InitRatioSettings():300
  service.InitHttpClient()         :302
  service.InitTokenEncoders()      :304
  model.InitDB()                   :307   ← 主库
  authz.Init(model.DB)             :312   ← 子系统接收 *gorm.DB 的现成范例
  model.CheckSetup()               :317
  model.InitOptionMap()            :325
  common.CleanupOldCacheFiles()    :328
  model.InitLogDB()                :331   ← 日志库
  common.InitRedisClient()         :337
  perfmetrics.Init()               :342
  common.StartSystemMonitor()      :345
  i18n.Init()                      :348
  oauth.LoadCustomProviders()      :359
  service.StartAuthArtifactCleanup():365
  ```
- **`authz.Init(db *gorm.DB) error`**(`service/authz/enforcer.go:33`)是"独立子系统持有自己的 DB 句柄"的现成范例:它接收 `*gorm.DB`、内部 `if common.IsMasterNode` 做 seeding、把 enforcer 存在包级变量 + `sync.RWMutex`(`:14-17`)。**这个模式可以直接照抄给第三个库。**
- **已有的"零改动注册表"模式**:`setting/config` 的 `config.GlobalConfig.Register("console_setting", &consoleSetting)`,在各 setting 包的 `init()` 里调用(如 `setting/console_setting/config.go:31-34`)。但它把配置存进主库 `options` 表,与"独立 YAML"目标不符,只能作为**思路参考**。
- **Docker**:`Dockerfile` 最后 `WORKDIR /data` + `ENTRYPOINT ["/new-api"]`。所以相对路径配置文件解析后会落在 `/data/` 下,天然是用户挂载的持久卷 —— YAML 默认路径建议 `./config/xxx.yaml`(即容器内 `/data/config/xxx.yaml`)。
- **`.gitignore`** 已忽略 `.env`、`*.db`、`/data/`,但**没有**忽略 `*.yaml`/`config/`。新增的 YAML 若含密码,需自行加忽略规则(这是要改的原有文件之一,或改用 `config/*.yaml` + 新增 `config/.gitignore`)。
- **测试基线**:`model/` 下的测试用 `gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})` 直接建 DB 并覆盖全局(`model/frontend_option_migration_test.go:18-25`、`model/task_cas_test.go:18` 等),并用 `common.SetMainDatabaseType` + defer 还原。新库的测试可照此写。

---

## 五、【扩展点建议】

### 5.1 结论先行

**可以做到"几乎全部新增文件"。必须改动的原有文件最少 1 个(`main.go`,+1~2 行);推荐改动 2 个(`main.go` + `.env.example` 文档)。若接受"进程退出时不显式 Close 第三库",则连 `model/main.go` 都不用碰。**

### 5.2 推荐方案:新建独立 package,完全不进 `model` 包

新增文件(全部是新文件,零冲突):

```
qydb/config.go      // YAML 结构体 + 加载
qydb/db.go          // 连接建立 / 连接池 / 方言状态 / 关闭
qydb/migrate.go     // 独立 AutoMigrate 注册表
qydb/locking.go     // 本库专用 lockForUpdate
qydb/models/*.go    // 新功能的所有 GORM 模型
config/qianye.yaml.example
```

**为什么不放进 `model` 包**:放进 `model/` 虽然能复用 `newGormConfig` / `closeDB` / `checkMySQLChineseSupport`(都不导出),但会让新模型和 34 张上游表混在同一个包里,`migrateDB()` 的表清单一旦上游变更就容易冲突,且 `lockForUpdate` / `commonGroupCol` 语义被绑死在主库类型上。独立包更干净,代价只是复制 ~15 行的 gorm config 工厂。

### 5.3 各环节的照抄映射(从 `LOG_DB` 模板 → 第三库)

| 环节 | 上游模板 | 新库对应做法 |
|---|---|---|
| 全局句柄 | `var LOG_DB *gorm.DB` @ `model/main.go:55` | `qydb.DB`(建议加 `sync.RWMutex` 或用 `atomic.Pointer`,或干脆 `func Get() *gorm.DB`) |
| 方言状态 | `common/database.go:13` `logDatabaseType` + `UsingLogDatabase` | 新包内 `var dialect common.DatabaseType` + `func Using(t common.DatabaseType) bool`,**复用 `common.DatabaseType` 常量,但不要复用 `common` 里那两个全局变量** |
| DSN 来源 | `os.Getenv("LOG_SQL_DSN")` @ `model/main.go:213,219` | YAML 字段(见 5.4) |
| 驱动选择 | `chooseDB` @ `model/main.go:127-169` | 只需 MySQL 分支即可(必须保留 `parseTime=true` 自动补全逻辑,`model/main.go:155-161`) |
| GORM 配置 | `newGormConfig(true)` @ `model/gorm_logger.go:25` | 复制 15 行;`PrepareStmt: true`,Logger 可直接 `logger.New(log.New(os.Stdout,...), ...)`,或简化为 `logger.Default.LogMode(logger.Warn)` |
| 连接池 | `model/main.go:237-239` | 从 YAML 读 `max_idle_conns` / `max_open_conns` / `max_lifetime_seconds`,默认值沿用 100/1000/60 |
| 字符集校验 | `checkMySQLChineseSupport` @ `model/main.go:714` | 该函数不导出。要么复制,要么**跳过**(新库是自己建的,可在 DSN/建库时直接要求 utf8mb4)。**不建议**为此改 `model/main.go` 去导出它 |
| 迁移 | `migrateLOGDB()` @ `model/main.go:399` | `qydb.Migrate()`,内部 `DB.AutoMigrate(&A{}, &B{}, ...)`;**同样要加 `if !common.IsMasterNode { return nil }` 门禁**(照抄 `model/main.go:241-243`) |
| 行锁 | `lockForUpdate` @ `model/locking.go:20` | 新包自己的版本,判断条件换成新库自己的 dialect |
| 列名引号 | `initCol()` @ `model/main.go:30` | 新库固定 MySQL,直接用常量 `` "`group`" `` 即可,无需 initCol |
| 关闭 | `CloseDB()` @ `model/main.go:701` | 见 5.5 |
| 健康检查 | `PingDB()` @ `model/main.go:808` | 照抄节流式 Ping(10s + mutex),暴露 `qydb.Ping() error` 给新功能自己的 status 接口 |

### 5.4 YAML 配置文件建议形态

因为 `gopkg.in/yaml.v3` 已在 `go.mod` 里,直接:
```go
// qydb/config.go
type Config struct {
    DSN                string `yaml:"dsn"`
    MaxIdleConns       int    `yaml:"max_idle_conns"`
    MaxOpenConns       int    `yaml:"max_open_conns"`
    MaxLifetimeSeconds int    `yaml:"max_lifetime_seconds"`
    AutoMigrate        bool   `yaml:"auto_migrate"`
    Debug              bool   `yaml:"debug"`
}
```
路径解析优先级建议:`QIANYE_CONFIG` 环境变量 → `./config/qianye.yaml` → 内置默认。Docker 下 `WORKDIR /data`,故相对路径落在挂载卷内,可直接被用户覆盖。**文件不存在时应"降级为功能关闭"而非 `FatalLog`**,这样上游镜像未挂配置也能正常启动(这一点与上游 `LOG_SQL_DSN` 为空时回落到主库的设计哲学一致)。

### 5.5 必须改动的原有文件 —— 最小集合

**方案 A(推荐,改 1 个文件 1 行):**

`main.go` 的 `InitResources()`,建议插在 `model.InitLogDB()` 之后(`main.go:334` 之后):
```go
// +1 import: "github.com/QuantumNous/new-api/qydb"
if err := qydb.Init(); err != nil {
    common.SysError("failed to init qianye database: " + err.Error())  // 不 return,功能降级
}
```
放在 `InitLogDB` 之后的理由:此时 `common.InitEnv()`、logger、`IsMasterNode` 都已就绪。

**方案 B(改 0 个原有文件,但有代价):**
在新包里用 `sync.Once` 做**懒初始化** —— 第一次调用 `qydb.Get()` 时才连库。因为配置来自 YAML 而非 `.env`,不依赖 `godotenv.Load()`,理论上甚至可以放在新包的 `init()` 里。代价:① 启动时不报配置错误,首个请求才暴露;② `common.IsMasterNode` 在 `init()` 阶段尚未赋值(`common/init.go:89` 在 `main.go:295` 才执行),**所以 `init()` 里绝不能做 AutoMigrate**;③ `go test` 会意外触发连库。**结论:懒加载可行且安全的前提是必须在 `qydb.Get()` 内部完成、且迁移也在同一个 Once 内做(此时 `IsMasterNode` 已就绪)。** 如果架构师追求"零改动 main.go",这是唯一可行路径,但仍推荐方案 A。

**关于优雅关闭**:`model.CloseDB()`(`model/main.go:701`)没有钩子注册表,`main.go:71-76` 的 defer 也只调它一个。要显式关闭第三库,只能在 `main.go` 的 defer 里再加一行 `_ = qydb.Close()`(这是 `main.go` 的第 2 处改动,仍在同一个文件内)。**不加也可接受** —— 进程退出时 OS 回收连接,且上游对 `LOG_DB == DB` 的场景本身就跳过 Close。

### 5.6 明确的"不要做"清单

1. **不要把新表加进 `model/main.go:261-295` 的 `DB.AutoMigrate(...)` 列表** —— 这是上游合并冲突最高发的一行区域。
2. **不要复用 `common.SetLogDatabaseType` / `UsingLogDatabase`** 来表达第三库的方言 —— 它们驱动着 `initCol()` 里的 `logGroupCol`(`model/main.go:43-50`)和 `model/log.go` 的 ClickHouse 分支,污染后会引发日志查询错误。
3. **不要复用 `SQL_MAX_IDLE_CONNS` 等三个 env** —— 与主库/日志库共享,调优会互相牵连;放进 YAML。
4. **不要在新库上调用 `model.lockForUpdate`**(它也不可见)—— 它判的是 `UsingMainDatabase`。
5. **不要为了复用而去导出 `newGormConfig` / `closeDB` / `checkMySQLChineseSupport`** —— 导出会修改上游文件签名区,复制这几十行的成本远低于合并冲突成本。
6. **不要在新包 `init()` 里做 AutoMigrate** —— `common.IsMasterNode` 那时是零值 `false`,会导致多节点部署下**所有**节点都跳过迁移(或反之,若逻辑写反则全都迁移)。

### 5.7 给架构师的一句话总结

> 上游的 `LOG_DB` 就是一个完整的、可直接照抄的"第二数据库"实现:`chooseDB(env, isLog) → SetXxxDatabaseType → initCol → 赋全局句柄 → SetMaxIdle/Open/Lifetime → IsMasterNode 门禁 → migrateXxxDB → CloseDB 里指针比较后关闭`。把其中的 `os.Getenv("LOG_SQL_DSN")` 换成 YAML 加载、把 `common` 里的全局方言变量换成新包内的私有变量、把 `migrateLOGDB` 换成自己的表清单,即得第三库。整个改造**只需要在 `main.go:334` 后插入一行 `qydb.Init()`**(可选再加一行 `defer` 内的 `qydb.Close()`),其余全部是新增文件。
