package withdraw

import (
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"

	"github.com/gin-gonic/gin"
)

// Mod 把提现接进扩展的模块注册表。
// 本模块对原项目后端的改动为 0 行:既不需要 hook,也不需要新的上游文件。
type Mod struct{ module.Base }

func (Mod) Name() string { return "withdraw" }

func (Mod) Tables() []any {
	return []any{&Withdrawal{}, &Payee{}, &PayeeAccount{}, &Event{}, &PiiAudit{}, &Proof{}}
}

// 本模块**不实现 InstallHooks**:提现改成人工发放之后,它对主库零写入,
// 既没有上游 hook 要注入,也不再需要向 twophase 登记补偿回调
// (那条回调服务的是自动到账的跨库资金单,已随 paying 状态一并下线)。

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/withdraw/config", handleGetConfig)
	// 唯一会动佣金的入口。幂等键只防得住"同一次点击的重试",防不住脚本用不同
	// client_request_id 连续发单,所以还要挂关键操作限流。
	g.POST("/withdraw", middleware.CriticalRateLimit(), handleCreate)
	g.GET("/withdraw/records", middleware.SearchRateLimit(), handleListRecords)
	g.GET("/withdraw/payees", handleListPayees)
	g.POST("/withdraw/payees", middleware.CriticalRateLimit(), handleCreatePayee)
	g.DELETE("/withdraw/payees/:ref", handleDeletePayee)
	// 上传要落磁盘,是本模块唯一一条能消耗宿主机存储的用户入口 ——
	// 必须挂关键操作限流,而不是只靠 proofPendingMax 那道计数闸门。
	g.POST("/withdraw/proofs", middleware.CriticalRateLimit(), handleUploadProof)
	g.GET("/withdraw/:id", handleGetRecord)
	// 凭证下载只对本人开放。挂在 /:id 下而不是用凭证 ref 直接寻址,是刻意的:
	// 越权判定因此天然落在"这张单是不是你的"这个已有的口径上
	// (loadUserWithdrawal 把 user_id 写进 WHERE),不必再造第二套。
	g.GET("/withdraw/:id/proof", handleGetProof)
	g.POST("/withdraw/:id/cancel", middleware.CriticalRateLimit(), handleCancel)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带上游操作审计)。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/withdraw", handleAdminList)
	g.GET("/withdraw/stats", handleAdminStats)
	g.GET("/withdraw/pii-audits", handleAdminPiiAudits)
	g.GET("/withdraw/:id", handleAdminGet)
	// 查看明文是被审计的高敏操作,挂关键操作限流可以让"批量扒库"变得昂贵。
	//
	// 档位与二次校验**同时**要:两者防的不是一件事 —— RootActionGate 回答
	// "谁有资格看",X-Security-Proof 回答"此刻坐在键盘前的是不是本人"。
	// 一张被偷走的 root 会话在没有第二因子时仍然扒不出任何一行明文;
	// 一个诚实的 role=10 即便刚做完 2FA 也看不到。去掉任何一道都只剩一半。
	//
	// 代价是**只有超管能实际打款**(打款要照着收款账号去银行/钱包转账),
	// 而四个人工决定仍是 role>=10 —— 这是项目方明确要的形状:role=10 负责
	// 审核与状态流转,出钱那一步收口到一个人。
	payeePii := middleware.RootActionGate(middleware.RootActionWithdrawPayeeReveal)
	// 三道闸门的**顺序**要紧:档位在最前。限流桶按 客户端 IP + 路由 计,与身份
	// 无关,被拒的越权尝试同样消耗它 —— 而这两条路由是线下打款时唯一的取数口,
	// 被一个 role=10 的重试脚本锁死 20 分钟等于打款停摆。闸门在前时被拒的尝试
	// 一格桶都不消耗,却仍然逐条写审计。
	g.GET("/withdraw/:id/payee", payeePii, middleware.CriticalRateLimit(), handleAdminRevealPayee)
	// 凭证图片与收款账号同属 PII,因此走同一套口径:必填事由 + 写 qy_pii_audits
	// + 关键操作限流 + 同一道档位。差别只在它没有"脱敏版"可看 —— 一张图要么
	// 看得到要么看不到。
	g.GET("/withdraw/:id/proof", payeePii, middleware.CriticalRateLimit(), handleAdminGetProof)

	// 人工决策一律挂关键操作限流:它们要么改佣金账本,要么终结一张单。
	//
	// 四个决定构成完整闭环:通过 → 待发放;驳回 / 发放失败 → 佣金退回;
	// 标记已发放 → 佣金核销。没有第五条边,也没有任何一条会自动出钱。
	crit := middleware.CriticalRateLimit()
	g.POST("/withdraw/:id/approve", crit, handleAdminApprove)
	g.POST("/withdraw/:id/reject", crit, handleAdminReject)
	g.POST("/withdraw/:id/mark-paid", crit, handleAdminMarkPaid)
	g.POST("/withdraw/:id/fail", crit, handleAdminFail)
}

// StartTasks 启动周期性收尾任务(待发放积压告警 + PII 保留期清理)。
//
// 十分钟一轮:两件事都是"到点做一次"的收尾,没有任何实时性要求。跑得更密只会
// 空扫,跑得更疏会让一批到期密文多留几个小时。必须走 lease.Run ——
// common.IsMasterNode 只是个环境变量,多节点都配成 master 时会双跑。
func (Mod) StartTasks() {
	if !config.Get().Withdraw.Enabled {
		return
	}
	lease.Run("withdraw.reconcile", 10*time.Minute, reconcile)
}

func init() { module.Register(Mod{}) }
