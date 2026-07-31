# 配置加载与 YAML 接入

勘察完成。以下是配置系统领域的完整报告。

---

# 【new-api 配置系统勘察报告】

## 一、main.go 启动顺序(精确行号)

文件:`C:\Users\Administrator\Desktop\qianye\qianye-newapi\main.go`(共 368 行)

### 1.1 embed 声明(main.go:42-46)
```go
//go:embed web/dist
var buildFS embed.FS

//go:embed web/dist/index.html
var indexPage []byte
```

### 1.2 `main()` 主流程(main.go:48-239)

| 行号 | 调用 | 说明 |
|---|---|---|
| 49 | `startTime := time.Now()` | |
| 50-53 | `kitutil.SetLogging` / `SetSystemErrorLogging` | relaykit 日志桥接 |
| **55** | **`err := InitResources()`** | **所有初始化都在这里面** |
| 61 | `common.SysLog("New API ... started")` | |
| 62-64 | `gin.SetMode(gin.ReleaseMode)` | |
| 71-76 | `defer model.CloseDB()` | 只关 `DB` 和 `LOG_DB` |
| 78-102 | 内存缓存 / `model.InitChannelCache()` | |
| 101 | `go model.SyncChannelCache(common.SyncFrequency)` | |
| 106 | `model.GetPricing()` | |
| **109** | **`go model.SyncOptions(common.SyncFrequency)`** | **配置热更新唯一入口** |
| 112 | `go authz.StartPolicySync(...)` | |
| 115 | `go model.UpdateQuotaData()` | |
| 117-123 | `CHANNEL_UPDATE_FREQUENCY` 定时任务 | |
| 126 | `service.StartCodexCredentialAutoRefreshTask()` | |
| 129 | `service.StartSubscriptionQuotaResetTask()` | |
| 133 | `service.StartSystemInstanceReporter()` | |
| 138-144 | `service.GetTaskAdaptorFunc = ...` | 函数变量注入解循环依赖(**值得模仿的解耦范式**) |
| 151 | `controller.RegisterScheduledSystemTasks()` | 定时任务注册点 |
| **152** | **`service.StartSystemTaskRunner()`** | **后台任务启动的最后一处** |
| 154-158 | `BATCH_UPDATE_ENABLED` | |
| 160-166 | `ENABLE_PPROF` | |
| 168 | `common.StartPyroScope()` | |
| **174** | **`server := gin.New()`** | |
| 175-178 | `middleware.ConfigureTrustedProxies(server)` | |
| 179-187 | `gin.CustomRecovery` | |
| 190-192 | `server.Use(RequestId/Version/I18n)` | 全局中间件链 |
| 193 | `middleware.SetUpLogger(server)` | |
| 194-195 | `InjectUmamiAnalytics()` / `InjectGoogleAnalytics()` | |
| **198-201** | **`router.SetRouter(server, router.WebAssets{...})`** | **唯一路由挂载点** |
| 202-205 | 端口解析 `PORT` → `*common.Port` | |
| 207-216 | `srv.ListenAndServe()` | |
| 220 | `common.LogStartupSuccess(startTime, port)` | |
| 222-233 | 信号 + `srv.Shutdown(ctx)`,超时用 `common.GetEnvOrDefault("SHUTDOWN_TIMEOUT_SECONDS", 120)` | |
| 235-237 | `model.SaveQuotaDataCache()` | |

### 1.3 `InitResources()`(main.go:284-368)——**核心插入区**

```
284  func InitResources() error {
287      err := godotenv.Load(".env")            ← .env 唯一加载点(CWD 相对)
295      common.InitEnv()                        ← flag.Parse() + 全部环境变量读取
297      logger.SetupLogger()                    ← 日志文件就绪(此后 SysLog 才写文件)
300      ratio_setting.InitRatioSettings()
302      service.InitHttpClient()
304      service.InitTokenEncoders()
307      err = model.InitDB()                    ← 主库(SQL_DSN)+ AutoMigrate
312      authz.Init(model.DB)                    ← casbin
317      model.CheckSetup()
320-324  model.MigrateRetiredFrontendOptions()   ← 仅 IsMasterNode
325      model.InitOptionMap()                   ← options 表 → 内存 OptionMap → GlobalConfig
328      common.CleanupOldCacheFiles()
331      err = model.InitLogDB()                 ← 日志库(LOG_SQL_DSN)
337      err = common.InitRedisClient()
342      perfmetrics.Init()
345      common.StartSystemMonitor()
348      err = i18n.Init()
356      i18n.SetUserLangLoader(model.GetUserLanguage)
359      err = oauth.LoadCustomProviders()
365      service.StartAuthArtifactCleanup()
367      return nil
368  }
```

**关键约束:`godotenv.Load(".env")` 在 main.go:287,晚于所有包的 `init()` 函数。** 所以任何依赖 `.env` 中变量的逻辑都不能放在 `init()` 里。

---

## 二、环境变量读取

文件:`common\env.go`(仅 39 行,三个函数):

```go
func GetEnvOrDefault(env string, defaultValue int) int          // common/env.go:9
func GetEnvOrDefaultString(env string, defaultValue string) string  // common/env.go:21
func GetEnvOrDefaultBool(env string, defaultValue bool) bool    // common/env.go:28
```
解析失败时调 `SysError` 并回退默认值,不 panic。

统一初始化入口 `common.InitEnv()` —— `common\init.go:32-137`:
- `common\init.go:33` `flag.Parse()`(flag 定义在 `common\init.go:18-23`:`Port`/`PrintVersion`/`PrintHelp`/`LogDir`,均为 `*T` 指针)
- `common\init.go:69-71` `SQLITE_PATH` → `common.SQLitePath`
- `common\init.go:87-89` `DebugEnabled` / `MemoryCacheEnabled` / `IsMasterNode = os.Getenv("NODE_TYPE") != "slave"`
- `common\init.go:136` → `initConstantEnv()`(`common\init.go:176-227`),把环境变量灌进 `constant` 包的导出变量

日志函数(`common\sys_log.go`):
```go
func SysLog(s string)              // :17
func SysError(s string)            // :24
func FatalLog(v ...any)            // :31 —— 内部 os.Exit(1)
```

---

## 三、.env / godotenv

- 加载点唯一:**main.go:287** `godotenv.Load(".env")`,**CWD 相对路径**,失败仅在 DEBUG 下打日志。
- `.env` 在 `.gitignore:13` 被忽略。
- `.env.example`(121 行)全部是注释形态的环境变量清单,分组:端口/调试(PYROSCOPE)/数据库(`SQL_DSN`、`LOG_SQL_DSN`、`SQLITE_PATH`、`SQL_MAX_IDLE_CONNS`、`SQL_MAX_OPEN_CONNS`、`SQL_MAX_LIFETIME`、`SQL_SLOW_THRESHOLD_MS`)/缓存(`REDIS_CONN_STRING`、`SYNC_FREQUENCY`)/超时/TLS/`TRUSTED_PROXIES`/Session/`NODE_TYPE`。**没有任何 YAML 相关项。**

**Docker 路径事实(重要):**
- `Dockerfile:40-41`:`WORKDIR /data` + `ENTRYPOINT ["/new-api"]`
- `docker-compose.yml`:`volumes: - ./data:/data`
- 即容器内 CWD = `/data` = 宿主机 `./data`。所以 `.env` 与任何 CWD 相对的配置文件天然落在挂载卷里。
- **`.gitignore:29` 已有 `/data/`** —— 本地开发时 `./data/xxx.yaml` 已自动被 git 忽略,不需要改 `.gitignore`。

---

## 四、setting/ 目录组织方式

### 4.1 核心:`setting\config\config.go`(306 行)

```go
type ConfigManager struct {          // :14
    configs map[string]interface{}
    mutex   sync.RWMutex
}
var GlobalConfig = NewConfigManager() // :19

func (cm *ConfigManager) Register(name string, config interface{})              // :28
func (cm *ConfigManager) Get(name string) interface{}                           // :35
func (cm *ConfigManager) LoadFromDB(options map[string]string) error            // :42
func (cm *ConfigManager) SaveToDB(updateFunc func(key, value string) error) error // :71
func (cm *ConfigManager) ExportAllConfigs() map[string]string                   // :286
func ConfigToMap(config interface{}) (map[string]string, error)                 // :276
func UpdateConfigFromMap(config interface{}, configMap map[string]string) error // :281
```

机制:反射 + **`json` tag 作为键名**,扁平 key 形如 `"模块名.json标签"`。标量用 `strconv`,`Map/Slice/Struct/Ptr` 用 `encoding/json` 序列化成字符串。

### 4.2 各模块注册范式(照抄即可)

`setting\console_setting\config.go`:
```go
var defaultConsoleSetting = ConsoleSetting{...}
var consoleSetting = defaultConsoleSetting

func init() {
    config.GlobalConfig.Register("console_setting", &consoleSetting)
}
func GetConsoleSetting() *ConsoleSetting { return &consoleSetting }
```
同样范式:`setting\system_setting\fetch_setting.go:27-30`(`"fetch_setting"`)、`setting\performance_setting\config.go:43-48`(`"performance_setting"`,init 里还调 `syncToCommon()` 把值推到 `common` 包)。

**`init()` 触发靠 blank import** —— `main.go:32`:
```go
_ "github.com/QuantumNous/new-api/setting/performance_setting"
```
这是项目里已有的"零业务代码挂载点"先例。

### 4.3 持久化 + 热更新链路

存储表:`model\option.go:18-21`
```go
type Option struct {
    Key   string `json:"key" gorm:"primaryKey"`
    Value string `json:"value"`
}
```

- `model.InitOptionMap()` (`model\option.go:30-187`):先把内存默认值写进 `common.OptionMap`,`model\option.go:180-183` 用 `config.GlobalConfig.ExportAllConfigs()` 自动导出所有注册模块,最后 `loadOptionsFromDatabase()`。
- `loadOptionsFromDatabase()` (`model\option.go:189-197`) → `updateOptionMap(key, value)`。
- `updateOptionMap()` (`model\option.go:271`) → `handleConfigUpdate(key, value)` (`model\option.go:601-636`):按 `.` 拆成 `configName`/`configKey`,`config.GlobalConfig.Get(configName)` 拿到指针,`config.UpdateConfigFromMap` 写回;`model\option.go:628-633` 有特判后处理(`performance_setting` → `UpdateAndSync()`;`billing_setting` → 失效定价缓存)。
- **热更新** = `SyncOptions(frequency)` (`model\option.go:199-205`):`for { sleep(frequency秒); loadOptionsFromDatabase() }`,由 main.go:109 起协程。**只有轮询数据库这一种热更新,没有文件监听**(全库无 `fsnotify`,已 grep 确认)。
- 写入:`UpdateOption(key, value)` (`model\option.go:214`)、`UpdateOptionsBulk(map)` (`model\option.go:238`)。
- 前端读取:`controller\option.go:79 GetOptions`,写入 `controller\option.go:124 UpdateOption`。

> ⚠️ **对本次需求的关键结论**:`config.GlobalConfig` 这套体系的持久化目标是**原项目主库的 `options` 表**。如果新功能的开关注册进 `GlobalConfig`,数据就落到了原库,**违背"独立 MySQL"约束**。所以新功能配置不要走 `GlobalConfig`,应走「YAML(静态) + 新库自有表(动态)」。

---

## 五、YAML 库现状 —— **已有,无需新增依赖**

- `go.mod:57`:`gopkg.in/yaml.v3 v3.0.1` —— **位于第一个 `require` 直接依赖块**,不是 indirect。
- `go.sum:3008-3009`:`gopkg.in/yaml.v3 v3.0.1 h1:...` + `/go.mod h1:...`,**h1 哈希齐全,可直接构建**。
- **没有** `goccy/go-yaml`,**没有** `spf13/viper`;`gopkg.in/yaml.v2` 只在 go.sum 里以传递依赖形式出现,go.mod 无。
- 唯一使用者:`i18n\i18n.go:11` import,`i18n\i18n.go:40`:
  ```go
  bundle.RegisterUnmarshalFunc("yaml", yaml.Unmarshal)
  ```
  也就是说 **项目里目前没有任何 `yaml.Unmarshal` 直接解析结构体的先例**,新代码是第一个。
- `relaykit\go.mod:22` 里 yaml.v3 是 indirect;按 `AGENTS.md:67-70`,`relaykit/` 必须独立可构建,**新代码不要放进 relaykit**。
- `AGENTS.md:72-79` 强制 JSON 走 `common/json.go` 包装(`common.Marshal`/`common.Unmarshal`/`common.UnmarshalJsonStr`/`common.DecodeJson`),**对 YAML 无此约束**,直接用 `yaml.Unmarshal` 符合 i18n 的既有做法。

---

## 六、外部文件读取先例 & 路径定位

全库 grep `os.ReadFile|os.Open|filepath.Join` 的结果,**没有"读取外部配置文件"的先例**,只有:
- `logger\logger.go:55`:`filepath.Join(*common.LogDir, "oneapi-<ts>.log")` —— `LogDir` 来自 flag(`common\init.go:22`,默认 `./logs`),`common\init.go:72-84` 会做 `filepath.Abs` + `os.Mkdir(0777)`。**这是项目里唯一的"路径解析 + 目录自建"范式。**
- `common\disk_cache.go:30/49/89/94`:磁盘缓存目录,路径来自 `common.GetDiskCachePath()`(`common\disk_cache_config.go:65`),空则用系统临时目录。
- `common\utils.go:104/128`:读 `/proc/1/cgroup`、`/proc/1/comm` 判断容器。
- `controller\performance.go:301/311/324`:读日志目录。

**结论:唯一的"配置来自外部文件"先例就是 `.env`(CWD 相对)。** 新 YAML 应沿用同一约定 + 环境变量覆盖。

---

## 七、embed 用法盘点(是否影响新增配置文件)

全库 4 处 `//go:embed`:

| 位置 | 指令 | 影响 |
|---|---|---|
| `main.go:42` | `//go:embed web/dist` | 构建期要求 `web/dist` 存在(`Dockerfile.dev:19-20` 用占位 html 绕过);与新 YAML 无关 |
| `main.go:45` | `//go:embed web/dist/index.html` | 同上 |
| `i18n\i18n.go:25` | `//go:embed locales/*.yaml` | ⚠️ **新 YAML 绝对不要放进 `i18n/locales/`**,否则会被这个 glob 一起编进二进制(虽然 `i18n\i18n.go:43` 的白名单 `[]string{"locales/zh-CN.yaml","locales/zh-TW.yaml","locales/en.yaml"}` 不会加载它,但无谓增大体积且语义混乱) |
| `common\limiter\limiter.go:13` | `//go:embed lua/rate_limit.lua` | 无影响 |

**结论:新增一个位于新包目录下(或仓库根)的 YAML,不会被任何现有 embed 捕获。** 若想把「默认/示例配置」编进二进制作为兜底,在新包里加自己的 `//go:embed qianye.example.yaml` 完全安全,和现有 embed 不冲突。

同时注意 `.dockerignore` 只排除 `.github/.git/*.md/.vscode/.gitignore/Makefile/docs/...`,`*.yaml` 会进构建上下文;但最终镜像(`Dockerfile:37`)只 `COPY /build/new-api /`,**不会带任何 yaml 进镜像**。所以运行时配置必须从 `/data` 卷提供。

---

## 八、新库连接需要注意的既有细节

- `newGormConfig(prepareStmt bool) *gorm.Config`(`model\gorm_logger.go:25`)**是未导出的**,新包用不了,必须自建 `&gorm.Config{}`。可参考它的做法:`PrepareStmt: true` + `logger.New(...)`,慢查询阈值来自 `SQL_SLOW_THRESHOLD_MS`(`model\gorm_logger.go:33`,默认常量 `defaultSlowThresholdMs = 200`,`model\gorm_logger.go:21`)。
- MySQL DSN 处理范式 `model\main.go:152-163`:自动补 `parseTime=true`
  ```go
  if !strings.Contains(dsn, "parseTime") {
      if strings.Contains(dsn, "?") { dsn += "&parseTime=true" } else { dsn += "?parseTime=true" }
  }
  db, err := gorm.Open(mysql.Open(dsn), newGormConfig(true))
  ```
- 连接池范式 `model\main.go:193-195`:`SetMaxIdleConns` / `SetMaxOpenConns` / `SetConnMaxLifetime`。
- 迁移 gating 范式 `model\main.go:197-199` 与 `:241-243`:`if !common.IsMasterNode { return nil }` 之后才 `AutoMigrate`。**新库迁移也应这样 gate**,否则多节点并发 DDL。
- 关闭:`model.CloseDB()`(`model\main.go:701`)只关 `DB`/`LOG_DB`,**新库需要自己在 main.go:71-76 的 defer 里加,或者干脆交给进程退出**(推荐后者,零改动)。
- `common.SetMainDatabaseType` / `SetLogDatabaseType`(`common\database.go:23/27`)是全局单例,**新库千万不要调用**,会污染 `initCol()`(`model\main.go:30-51`)对反引号/双引号列名的判断。

---

## 九、【扩展点建议】

### 9.1 推荐目录结构(全部为**新建**文件)

```
qianye/                          ← 新增顶层包,与 upstream 零重名
  config/
    config.go        // YAML 结构体定义 + Load() + 全局单例 + Get*()
    path.go          // 配置文件路径解析
    qianye.example.yaml
  db/
    db.go            // 独立 MySQL 连接 + AutoMigrate
  model/             // 新功能的 GORM 模型(全部绑到 qianye/db.DB)
  service/
  controller/
  router.go          // RegisterRoutes(r *gin.Engine)
  bootstrap.go       // Init() —— 唯一对外入口
```

### 9.2 YAML 加载的最干净实现

**依赖:零新增**(`gopkg.in/yaml.v3 v3.0.1` 已在 `go.mod:57`)。

路径解析(`qianye/config/path.go`),按优先级:
1. `os.Getenv("QIANYE_CONFIG")` —— 显式指定
2. `./qianye.yaml` —— Docker 下即 `/data/qianye.yaml` = 宿主 `./data/qianye.yaml`
3. `./data/qianye.yaml` —— 本地开发,**`.gitignore:29` 的 `/data/` 已覆盖,含密码也不会误提交**

用 `common.GetEnvOrDefaultString("QIANYE_CONFIG", "")` 读取(`common\env.go:21`),保持与项目一致。

建议的对外 API 形态:
```go
package config

type Config struct {
    Database DatabaseConfig `yaml:"database"`
    Features FeatureFlags   `yaml:"features"`
}
type DatabaseConfig struct {
    DSN             string `yaml:"dsn"`
    MaxIdleConns    int    `yaml:"max_idle_conns"`
    MaxOpenConns    int    `yaml:"max_open_conns"`
    MaxLifetimeSecs int    `yaml:"max_lifetime_seconds"`
}
type FeatureFlags struct {
    XxxEnabled bool `yaml:"xxx_enabled"`
    ...
}

var cfg atomic.Pointer[Config]        // 便于未来做 SIGHUP 重载

func Load() error                     // 读文件 → yaml.Unmarshal → 校验 → cfg.Store
func Get() *Config                    // 全局只读访问
func Enabled() bool                   // 配置文件缺失时整个新功能优雅降级
```
**强烈建议**:配置文件不存在时 `Load()` 返回 nil 并把 `Enabled()` 置 false,让整个新功能整体禁用而不是让主程序启动失败——这样 fork 与上游的默认行为完全一致,合并上游时也不会因缺文件而崩。

热更新:YAML 内容(DSN + 开关)属于启动级配置,**建议只在启动时加载一次**。若确需热更新,不要引入 fsnotify(会新增依赖),仿照 `model\option.go:199-205` 的轮询范式,在新包里起一个 goroutine 定期 `os.Stat` 比对 mtime 即可。

### 9.3 精确插入位置(**这是核心答案**)

#### 改动点 1 —— `main.go` import 块(第 3-40 行内)
新增一行:
```go
	"github.com/QuantumNous/new-api/qianye"
```
建议插在 `main.go:31`(`"github.com/QuantumNous/new-api/service/authz"`)之后、`main.go:32`(`_ ".../setting/performance_setting"`)之前,保持字母序、减少 gofmt 冲突面。

#### 改动点 2 —— `main.go` `InitResources()` 内,**在第 365 行之后、第 367 行 `return nil` 之前**插入:
```go
	if err := qianye.Init(); err != nil {
		common.SysError("failed to initialize qianye extension: " + err.Error())
		return err
	}
```

**为什么是这个位置(365 之后)——理由逐条:**
- 晚于 **main.go:287** `godotenv.Load(".env")` → `QIANYE_CONFIG` 等环境变量已可用(这也是**不能用 blank-import + `init()` 方案**的根本原因:`init()` 早于 godotenv,读不到 `.env` 里的变量)。
- 晚于 **main.go:295** `common.InitEnv()` → `common.IsMasterNode`、`DebugEnabled`、`SQLitePath` 等已就绪,新库迁移可以正确 gate。
- 晚于 **main.go:297** `logger.SetupLogger()` → `gin.DefaultWriter` 已切到日志文件,`common.SysLog` 输出可落盘。
- 晚于 **main.go:307/312/325/331/337** → 主库 `model.DB`、casbin、`common.OptionMap`、日志库、Redis 全部就绪,新功能可以自由读用户/令牌/Redis。
- 晚于 **main.go:348** `i18n.Init()` → 新功能可直接用 `i18n.Translate`。
- 早于 `return nil` → 出错时经 `main.go:56-59` 走 `common.FatalLog` 统一失败路径。

`qianye.Init()` 内部顺序建议:
```go
func Init() error {
    if err := config.Load(); err != nil { return err }   // 找不到文件 → 返回 nil 且 Enabled()=false
    if !config.Enabled() { common.SysLog("qianye extension disabled (no config)"); return nil }
    if err := db.Init(config.Get().Database); err != nil { return err }  // 独立 MySQL + AutoMigrate(仅 master)
    return nil
}
```

#### 改动点 3(可选)—— 路由挂载,二选一

**方案 A(推荐,改 `main.go`,+1 行):** 在 **main.go:195**(`InjectGoogleAnalytics()`)之后、**main.go:198**(`router.SetRouter(...)`)之前插入:
```go
	qianye.RegisterRoutes(server)
```

**方案 B(改 `router\main.go`,+1 行):** 在 `router\main.go:19`(`SetVideoRouter(router)`)之后插入 `SetQianyeRouter(router)`。

**⚠️ 无论哪个方案,都必须在 `SetWebRouter` 之前。** 依据 `router\web-router.go:25-28`:
```go
router.Use(gzip.Gzip(gzip.DefaultCompression))
router.Use(middleware.GlobalWebRateLimit())
router.Use(middleware.Cache())
router.Use(static.Serve("/", frontendFS))
```
Gin 的 `engine.Use()` 只影响**其后**注册的路由。原项目正是靠 `router\main.go:16-19` 先注册 API/Dashboard/Relay/Video、`:26` 最后注册 Web 来避开这些全局中间件(尤其 `static.Serve` 和 `Cache`)。新路由若注册在 `SetRouter` 之后,会被 gzip/限流/缓存/静态服务中间件全部套上,SSE 之类会直接坏掉。

#### 改动点 4(可选)—— 后台常驻任务
若新功能需要定时任务,在 **main.go:152**(`service.StartSystemTaskRunner()`)之后插入一行 `qianye.StartBackgroundTasks()`。此处所有依赖(缓存、定价、options 同步)均已就绪。

### 9.4 需要改动的原有文件 —— 最小集合

| 文件 | 改动量 | 必要性 |
|---|---|---|
| `main.go` | +1 import 行,+4 行(`qianye.Init()`),+1 行(`RegisterRoutes`),可选 +1 行(后台任务) | **必需** |
| `router\main.go` | +1 行(仅当选路由方案 B) | 可选,与 main.go 方案 A 二选一 |
| `go.mod` / `go.sum` | **0 改动**(yaml.v3 已是直接依赖) | 不需要 |
| `.gitignore` | **0 改动**(`/data/` 已在 `.gitignore:29`) | 不需要 |
| `.env.example` | 0(可选加一行注释说明 `QIANYE_CONFIG`) | 不需要 |
| `docker-compose.yml` | 0(`./data:/data` 卷已存在,配置文件放宿主 `./data/qianye.yaml` 即可) | 不需要 |
| `Dockerfile` | 0(配置运行时从卷提供,不进镜像) | 不需要 |

**合计:合并上游时的冲突面 = `main.go` 里 5~7 行 + 可选 `router/main.go` 1 行。** 且这些行都集中在 `InitResources()` 尾部和 import 块,是 upstream 极少改动的位置。

### 9.5 明确要避开的坑

1. **不要把新配置注册进 `config.GlobalConfig`**(`setting\config\config.go:19`)—— 它的持久化目标是原项目主库的 `options` 表(`model\option.go:214-231`),直接违背"数据必须进独立 MySQL"的约束。
2. **不要用 blank import + `init()` 的方式加载 YAML** —— `init()` 早于 `main.go:287` 的 `godotenv.Load(".env")`,读不到 `.env` 提供的环境变量;也早于 `common.InitEnv()`(main.go:295),`IsMasterNode` 等尚未赋值。`setting/performance_setting` 能用这套是因为它只注册结构体、不读环境变量。
3. **不要调用 `common.SetMainDatabaseType` / `common.SetLogDatabaseType`**(`common\database.go:23/27`)—— 全局单例,会破坏 `model\main.go:30-51 initCol()` 的列名引号选择,进而搞坏原项目所有 SQL。
4. **新 YAML 不要放在 `i18n/locales/`** —— 会被 `i18n\i18n.go:25` 的 `//go:embed locales/*.yaml` 捕获。
5. **新库的 `AutoMigrate` 必须 gate `common.IsMasterNode`**,照抄 `model\main.go:197-199`。
6. **路由必须在 `SetWebRouter` 之前注册**(见 9.3 的 ⚠️)。
