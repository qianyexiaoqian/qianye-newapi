package ticket

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/gin-gonic/gin"
)

// Mod 把工单接进扩展的模块注册表。
// 本模块对原项目后端的改动为 0 行:既不需要 hook,也不需要新的上游文件。
type Mod struct{ module.Base }

func (Mod) Name() string { return "ticket" }

func (Mod) Tables() []any {
	return []any{&Ticket{}, &Message{}, &Attachment{}}
}

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
//
// 静态段(/ticket/list、/ticket/images)与参数段(/ticket/:no)同级共存,
// gin 从 1.7 起支持这种混合树;withdraw 的 /withdraw/records 与 /withdraw/:id
// 已经是既成先例。
//
// 用户端按**业务单号**寻址(:no),管理端按自增 id(:id):用户视图刻意不下发
// id(见 view.go),那么用它做路径参数就等于要求前端拿一个恒为 0 的值去请求。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/ticket/config", handleGetConfig)
	// 建单与回复都会往数据库写长文本,并可能带上磁盘写入,必须挂关键操作限流 ——
	// cooldown_seconds 那道闸是按"上一条的时间戳"判的,拦不住并发同时进来的一批。
	g.POST("/ticket", middleware.CriticalRateLimit(), handleCreate)
	g.GET("/ticket/list", middleware.SearchRateLimit(), handleList)
	// 上传要落磁盘,是本模块唯一一条能消耗宿主机存储的用户入口 ——
	// 必须挂关键操作限流,而不是只靠 pendingUploadMax 那道计数闸门。
	g.POST("/ticket/images", middleware.CriticalRateLimit(), handleUploadImage)
	g.GET("/ticket/images/:ref", handleGetImage)
	// 丢弃一张【自己上传且尚未提交】的图片。没有它,用户在弹窗里移除一张图
	// 只会把本地那一项删掉,而服务端那条未绑定的行会一直占着 pendingUploadMax
	// 的名额 —— 两次"选了又不发"之后,他在 24 小时孤儿清理到期之前再也传不了图,
	// 而收到的提示是"请先完成当前这条消息",一个他根本无法执行的下一步。
	g.DELETE("/ticket/images/:ref", handleDiscardImage)
	g.GET("/ticket/:no", handleGet)
	g.POST("/ticket/:no/reply", middleware.CriticalRateLimit(), handleReply)
	g.POST("/ticket/:no/close", handleClose)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带上游操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/ticket", handleAdminList)
	g.GET("/ticket/stats", handleAdminStats)
	g.POST("/ticket/images", middleware.CriticalRateLimit(), handleAdminUploadImage)
	g.GET("/ticket/images/:ref", handleAdminGetImage)
	// 与用户端同一个处理器:丢弃的判定是"这张图是我传的且还没进任何一条消息",
	// 那是身份口径而不是角色口径,管理员并不因此获得删别人待提交图片的能力。
	g.DELETE("/ticket/images/:ref", handleDiscardImage)
	g.GET("/ticket/:id", handleAdminGet)
	g.POST("/ticket/:id/reply", middleware.CriticalRateLimit(), handleAdminReply)
	// 改状态 / 改等级 / 指派都是"事后要能解释"的动作,全部写审计(见 api_admin.go)。
	// 挂关键操作限流是为了让"脚本把全站工单批量关掉"这件事变得昂贵。
	crit := middleware.CriticalRateLimit()
	g.POST("/ticket/:id/status", crit, handleAdminSetStatus)
	g.POST("/ticket/:id/priority", crit, handleAdminSetPriority)
	g.POST("/ticket/:id/assign", crit, handleAdminAssign)
}

// StartTasks 启动维护任务(自动关闭 + 图片保留期清理)。
//
// 周期固定一小时:两件事的粒度都是"天",跑得更勤只会空扫。必须走 lease.Run ——
// common.IsMasterNode 只是个环境变量,多节点都配成 master 时清理会双跑,
// 而双跑的表现是同一批文件被删两次(第二次报"文件不存在"并把整批标记跳过)。
func (Mod) StartTasks() {
	if !config.Get().Ticket.Enabled {
		return
	}
	lease.Run("ticket.maintain", time.Hour, maintain)
}

func init() { module.Register(Mod{}) }
