package controller

// setup.go —— 引导部署向导里"配置扩展数据库"那一步的后端。
//
// # 它与上游 /api/setup 的分工
//
// 上游那一步**配不了主库**,也不该配:向导页本身要读主库才渲染得出来
// (constant.Setup 从库里读、root 用户往库里写)。主库只能来自 SQL_DSN。
//
// 扩展库不同,它有一条主库没有的性质:**配置文件缺失 = 扩展静默禁用,主程序
// 行为与上游完全一致**。也就是说进程可以在"扩展没配"的状态下正常起来、正常
// 进向导,然后由向导把第一份配置写出来。这是本文件成立的全部前提。
//
// # 为什么必须重启,以及为什么这不是偷懒
//
//   - qianye/router.go 在 config.Enabled() 为 false 时**一条路由都不注册**,
//     而 Gin 的路由表在服务起来之后不能再加(RegisterRoutes 必须在 SetRouter 之前)。
//   - module.InstallHooks 写的是上游包的包级变量。bootstrap.go 说明了它安全的
//     前提是"早于任何 HTTP 请求与后台协程" —— 运行期再写就是 data race。
//   - config.Reload 明确把 Database 段排除在热载之外,DSN 不能热切。
//
// 三条各自独立,补任何一条都不够。所以这一步的终点是"配置写好了,请重启"。
// 好在只有这一次:模块开关是请求时才查的,以后开功能改 YAML 即可。
//
// # 只配 MySQL
//
// 扩展库也支持 PostgreSQL,但部署基线(qianye/docs/deploy-database.md)是全 MySQL。
// 向导实现基线,PG 部署请手工编辑 YAML —— 让向导多支持一种方言,等于让它多背
// 一套连不上时的诊断措辞,而那份收益归零(基线不推荐 PG)。

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// setupProbeTimeout 是一次连接探测的总预算。
//
// 比 database.connect_timeout_seconds 的默认值(5 秒)略宽:探测除了 dial
// 还要跑四条查询,而它跑在一个人正盯着 loading 转圈的界面上 —— 卡到 30 秒
// 那个人会以为页面死了,然后重复点击。
const setupProbeTimeout = 8 * time.Second

// setupProbeTable 是权限探测用的表名。带 qy_ 前缀是为了万一没删掉,
// 它看起来也属于扩展而不是一张来历不明的表。
const setupProbeTable = "qy_setup_probe"

type setupDatabaseRequest struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Database string `json:"database"`
}

func (r setupDatabaseRequest) params() config.BootstrapParams {
	return config.BootstrapParams{
		Host:     r.Host,
		Port:     r.Port,
		User:     r.User,
		Password: r.Password,
		Database: r.Database,
	}
}

// GetSetupStatus 回答"扩展这一步走到哪了"。匿名可访问,永远返回 200。
//
// 刻意只返回布尔,不返回配置文件路径:向导靠这几个布尔就能决定渲染哪一屏,
// 而路径是匿名面上没必要泄露的部署信息。写入成功时那条路径由 PostSetupApply
// 返回,那一步有超管身份。
func GetSetupStatus(c *gin.Context) {
	configured := config.Path() != "" || configFileExists()
	enabled := config.Enabled()
	ok(c, gin.H{
		"configured": configured,
		"enabled":    enabled,
		"connected":  enabled && db.Available(),
		// configured 但没 enabled,就是"写完了还没重启"这个唯一有意义的中间态。
		"needs_restart": configured && !enabled,
		"in_container":  inContainer(),
	})
}

// PostSetupTestDatabase 试连一个候选扩展库,不写任何配置。
//
// 探测的四件事都是"不测就要等到重启之后才炸"的那一类:
//
//	版本      —— 5.7 与 8.0 的迁移行为差别很大(INSTANT DDL)
//	字符集    —— 不是 utf8mb4 的话中文会在写入时截断或报错,而主库有
//	             checkMySQLChineseSupport 兜底,扩展库没有
//	建表权限  —— 缺 CREATE 的表现是重启后 AutoMigrate 失败,那时界面已经关了
//	已有表数  —— 不为空说明这不是一个干净的库,值得让人再看一眼
func PostSetupTestDatabase(c *gin.Context) {
	var req setupDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_setup_bad_request", "请求参数有误")
		return
	}
	if !refuseWhenConfigured(c) {
		return
	}
	dsn, err := req.params().MySQLDSN()
	if err != nil {
		badRequest(c, "qy_setup_bad_dsn", err.Error())
		return
	}
	report, err := probeDatabase(dsn)
	if err != nil {
		// 连不上不是服务端错误,是这个人填错了或者网络不通。用 400 让前端
		// 把原文直接显示在表单下面,而不是弹一个"服务器开小差"。
		badRequest(c, "qy_setup_unreachable", err.Error())
		return
	}
	ok(c, report)
}

// PostSetupApplyDatabase 生成并写入第一份配置。
func PostSetupApplyDatabase(c *gin.Context) {
	var req setupDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_setup_bad_request", "请求参数有误")
		return
	}
	if !refuseWhenConfigured(c) {
		return
	}
	params := req.params()
	dsn, err := params.MySQLDSN()
	if err != nil {
		badRequest(c, "qy_setup_bad_dsn", err.Error())
		return
	}
	// 再探一次而不是信任前端刚才那次"测试连接":两次调用之间参数可能被改过,
	// 而写进文件的那一份必须是验证过的那一份。这条重复的代价是几百毫秒。
	if _, err := probeDatabase(dsn); err != nil {
		badRequest(c, "qy_setup_unreachable", err.Error())
		return
	}
	raw, err := config.RenderBootstrap(params)
	if err != nil {
		badRequest(c, "qy_setup_render_failed", err.Error())
		return
	}
	path, err := config.WriteBootstrap(raw)
	if err != nil {
		badRequest(c, "qy_setup_write_failed", err.Error())
		return
	}
	common.SysLog("qianye: 引导向导已写入扩展配置 " + path + ",重启后生效")
	ok(c, gin.H{
		"config_path":  path,
		"in_container": inContainer(),
	})
}

// PostSetupRestart 结束进程,由容器编排拉起。
//
// 只在"配置文件已写好、但本进程还没加载扩展"这个窗口里可用。窗口之外
// 一律拒绝 —— 一个能随时重启网关的接口,不该因为它在这一步方便就长期留着。
//
// 退出码用 0:这是一次有意的退出,不是崩溃。compose 的 restart: always 与
// unless-stopped 都会拉起它;restart: no 不会,前端因此必须同时给出手工命令。
func PostSetupRestart(c *gin.Context) {
	if !configFileExists() {
		badRequest(c, "qy_setup_not_configured",
			"扩展配置尚未写入,此时重启不会有任何变化")
		return
	}
	if config.Enabled() {
		badRequest(c, "qy_setup_already_live",
			"扩展已在本进程生效,无需重启。要重启服务请用容器编排的正常手段")
		return
	}
	if !inContainer() {
		fail(c, http.StatusConflict, "qy_setup_not_container",
			"当前不在容器中运行,自动重启不可用 —— 请手工重启进程")
		return
	}
	common.SysLog("qianye: 引导向导请求重启以加载扩展配置")
	ok(c, gin.H{"restarting": true})
	// 先把响应写回去再退出。直接 os.Exit 会让这次请求的连接被重置,
	// 前端看到的是一次网络错误而不是"正在重启"。
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
}

// refuseWhenConfigured 是三个写侧端点共用的窗口闸门。
//
// 已经有配置文件之后,向导一律不再插手:那份文件里有加密密钥,而向导
// 唯一会做的事是生成一把新的。改配置请编辑文件。
func refuseWhenConfigured(c *gin.Context) bool {
	if configFileExists() {
		fail(c, http.StatusConflict, "qy_setup_already_configured",
			"扩展配置文件已存在,向导不再改动它 —— 要调整请手工编辑该文件")
		return false
	}
	// 上游初始化都没做完就来配扩展,说明请求不是从向导发出来的。
	if !constant.Setup {
		fail(c, http.StatusConflict, "qy_setup_main_not_initialized",
			"请先完成系统初始化,再配置扩展数据库")
		return false
	}
	return true
}

func configFileExists() bool {
	for _, p := range []string{
		common.GetEnvOrDefaultString(config.EnvConfigPath, ""),
		"./qianye.yaml",
		"./data/qianye.yaml",
	} {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// inContainer 判断进程是否跑在容器里,决定前端显示按钮还是显示命令。
//
// /.dockerenv 是 Docker 自己放的标记;cgroup 那一路兜住 podman 与部分
// containerd 场景。判错的代价是对称且轻的:判成容器而其实不是,按钮点下去
// 进程退出不再起来(前端已经写明这一点);判成不是而其实是,只是少一个按钮。
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	raw, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(raw)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") ||
		strings.Contains(s, "kubepods")
}

// probeDatabase 建一次性连接并回答四个问题。连接用完即关。
func probeDatabase(dsn string) (gin.H, error) {
	dialector, err := db.DialectorFor(dsn)
	if err != nil {
		return nil, err
	}
	gdb, err := gorm.Open(dialector, &gorm.Config{
		Logger:                 gormlogger.Discard,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("连接失败:%v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("获取连接池失败:%v", err)
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), setupProbeTimeout)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("连接失败(请检查主机、端口、账号密码与网络):%v", err)
	}
	tx := gdb.WithContext(ctx)

	var version string
	if err := tx.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		return nil, fmt.Errorf("读取数据库版本失败:%v", err)
	}
	var charset string
	if err := tx.Raw("SELECT @@character_set_database").Scan(&charset).Error; err != nil {
		return nil, fmt.Errorf("读取数据库字符集失败:%v", err)
	}
	if !strings.EqualFold(charset, "utf8mb4") {
		return nil, fmt.Errorf("该库的字符集是 %s,必须是 utf8mb4 —— "+
			"否则中文会在写入时被截断。请执行:"+
			"ALTER DATABASE 库名 CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci", charset)
	}
	var tableCount int64
	if err := tx.Raw(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()",
	).Scan(&tableCount).Error; err != nil {
		return nil, fmt.Errorf("读取表清单失败:%v", err)
	}
	// 建表权限:缺它的唯一症状是重启后 AutoMigrate 失败,而那时向导已经关了。
	if err := tx.Exec(
		"CREATE TABLE IF NOT EXISTS " + setupProbeTable + " (id INT PRIMARY KEY)",
	).Error; err != nil {
		return nil, fmt.Errorf("该账号没有建表权限:%v —— "+
			"扩展需要在这个库里创建约 77 张 qy_ 前缀的表", err)
	}
	if err := tx.Exec("DROP TABLE " + setupProbeTable).Error; err != nil {
		// 建得出来却删不掉是极罕见的权限组合。不拦,但要让人看见 ——
		// 那张空表会一直留在库里。
		common.SysError("qianye: 引导探测表 " + setupProbeTable + " 未能清理: " + err.Error())
	}
	return gin.H{
		"version":     version,
		"charset":     charset,
		"table_count": tableCount,
		// 库里已经有表时前端要提示一句。这里不拦:复用一个已有 schema
		// (与主库同库、只靠 qy_ 前缀区分)是官方支持的部署形态。
		"empty": tableCount == 0,
	}, nil
}
