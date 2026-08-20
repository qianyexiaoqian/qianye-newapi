package transfer

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// Mod 把划转接进扩展的模块注册表。
// 本模块对原项目后端的改动为 0 行:它既不需要 hook,也不需要新的路由文件。
type Mod struct{ module.Base }

func (Mod) Name() string { return "transfer" }

func (Mod) Tables() []any {
	return []any{&Order{}, &UserState{}, &LookupLog{}, &GroupRule{}, &GroupLimit{}, &Contact{}}
}

// InstallHooks 注册补偿回调。
// 这里没有上游 hook 要注入,只有 twophase 需要知道"确认主库生效之后找谁收尾"。
//
// 刻意**不注册 PostCommit**:本模块的账本行由 Resolver 里的 backfillLedger 补,
// 它有自己的 ledger_written CAS 做"只写一次"。再注册一个 PostCommit 等于在同一件事
// 上叠第二把幂等锁 —— 两把锁一旦对不齐(比如 PostCommit 抢到了 after_commit_at
// 而 backfillLedger 那一侧还没置位),账本行就会漏写。用户缓存由 twophase 统一失效。
func (Mod) InstallHooks() {
	twophase.RegisterResolver(qymodel.KindTransfer, resolveAfterCompensation)
}

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	// 唯一会动钱的入口,挂关键操作限流:幂等键只能防住"同一次点击的重试",
	// 防不住脚本用不同 request_id 连续发起。
	g.POST("/transfer", middleware.CriticalRateLimit(), handleCreate)
	// 解析接口天然可被用来枚举,SearchRateLimit 按用户 ID 限流,比 IP 限流抗代理轮换。
	g.POST("/transfer/preview", middleware.SearchRateLimit(), handlePreview)
	g.GET("/transfer/records", handleListRecords)
	g.GET("/transfer/limits", handleGetLimits)

	// 联系人簿(需求 3-C)。它**只**用来把收款人字段填好,不是信任凭据:
	// 不豁免支付密码、不豁免分组限制、不豁免任何限额。见 contacts.go 顶部
	// 的不变量说明,以及 contacts_isolation_test.go 的双向 AST 断言。
	g.GET("/transfer/contacts", handleListContacts)
	// 添加走的是与 /transfer/preview 同一套收款人解析,因此必须挂同一个
	// 限流器:「输入一个标识看它存不存在」在这里和在预览接口是同一个动作,
	// 只给其中一个入口设防等于没设防。
	g.POST("/transfer/contacts", middleware.SearchRateLimit(), handleAddContact)
	// 改备注与删除都只作用于调用者自己的行(WHERE 带 owner_user_id),
	// 不解析任何标识,因此不构成枚举面,不需要 SearchRateLimit。
	g.PUT("/transfer/contacts/:id", handleRenameContact)
	g.DELETE("/transfer/contacts/:id", handleDeleteContact)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/transfer/records", handleAdminListRecords)

	// 划转门槛(qy_settings, scope=transfer)。写接口挂关键操作限流:
	// 一次保存就能把全站的日额度放大一个数量级,不该允许被脚本高频改写。
	g.GET("/transfer/config", adminGetTransferConfig)
	g.PUT("/transfer/config", middleware.CriticalRateLimit(), adminPutTransferConfig)

	// 门槛的**按用户分组分档**(qy_transfer_group_limits)。限流档次与全站门槛
	// 一致:一次保存就能把某一档人的日额度放大一个数量级。
	// 删除走 query 参数而不是路径参数,理由见 adminDeleteGroupLimit。
	g.GET("/transfer/group-limits", adminListGroupLimits)
	g.PUT("/transfer/group-limits", middleware.CriticalRateLimit(), adminPutGroupLimit)
	g.DELETE("/transfer/group-limits", middleware.CriticalRateLimit(), adminDeleteGroupLimit)

	// 分组划转限制。写接口都挂关键操作限流:一条规则能瞬间放开或掐断
	// 整个分组的资金流向,不该允许被脚本高频改写。
	g.GET("/transfer/group-rules", handleAdminListGroupRules)
	g.POST("/transfer/group-rules", middleware.CriticalRateLimit(), handleAdminCreateGroupRule)
	g.PUT("/transfer/group-rules/:id", middleware.CriticalRateLimit(), handleAdminUpdateGroupRule)
	g.DELETE("/transfer/group-rules/:id", middleware.CriticalRateLimit(), handleAdminDeleteGroupRule)
}

// StartTasks 启动对账任务。
//
// 周期取两阶段补偿间隔的两倍:本任务只负责收拾补偿任务推进完终态之后的尾巴,
// 跑得比它还快没有意义,只会空扫。
func (Mod) StartTasks() {
	interval := 2 * twophase.Interval()
	if interval < time.Minute {
		interval = time.Minute
	}
	lease.Run("transfer.reconcile", interval, reconcile)
}

func init() { module.Register(Mod{}) }
