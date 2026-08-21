package lottery

import (
	"context"
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qyctl "github.com/QuantumNous/new-api/qianye/controller"
	"github.com/QuantumNous/new-api/qianye/module"
	"github.com/QuantumNous/new-api/qianye/service/lease"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/gin-gonic/gin"
)

// Mod 把娱乐功能接进扩展的模块注册表。
//
// 本模块对原项目后端的改动为 0 行:不加列、不改业务逻辑、不占任何 hook 槽位。
// 消费数据走独立的低水位扫描,绝不去抢 model.QyOnConsumeLog —— 那是单槽变量,
// commission 已经直接赋值,再赋一次会静默覆盖佣金结算的唯一入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "lottery" }

func (Mod) Tables() []any { return tables() }

// tables 是本模块交给 AutoMigrate 的全部模型。
//
// 单独成函数而不是内联在 Tables() 里:前七张是**永不清理的证据表**
// (activity/seed/prize/option/entry/payout/event),而"哪些表属于证据"
// 这件事需要一份能被测试直接引用的权威清单 —— 内联在方法里的字面量
// 只能被人眼核对,而人眼核对过的东西在这个仓库里已经漏过好几次。
func tables() []any {
	return []any{
		&Activity{}, &Seed{}, &Prize{}, &Option{},
		&Entry{}, &Payout{}, &Event{}, &SpendDaily{}, &Flag{},
		// PrizeSecretHist 同样是证据表:它存的是被顶替掉的兑换码明文,
		// 争议时唯一能回答"用户当初拿到的是哪一串"。不注册任何清理任务。
		&PrizeSecretHist{},
		// Series 不是证据表,但它是**资金闸门表**:删一行等于把一个双色球系列
		// 的累计发行上限抹掉。同样不注册任何清理任务。
		&Series{},
		// Cover 是本模块**唯一**可被清理的非派生表:它是磁盘上那张封面图的
		// 指针,而不是任何事实的证据。它的回收口径见 cover.go 的 pruneCovers。
		&Cover{},
	}
}

// InstallHooks 注册补偿回调与引导端点的展示开关。
//
// 补偿回调让 twophase 知道"确认主库已生效之后找谁收尾"。缺了它的后果在本仓的
// violation 上真实发生过:补偿任务把资金单推成 success,业务侧永远停在中间态。
//
// 展示开关必须接管:管理端「在前端显示娱乐入口」写的是 qy_settings,而引导端点
// 默认只读 YAML —— 不接管就会出现"运营关掉了入口,前台照旧显示"。
func (Mod) InstallHooks() {
	InstallResolvers()
	qyctl.QyLotteryEntryShown = func() bool { return effective().ShowEntry }
}

// RegisterPublicRoutes 挂载**匿名可访问**的证据链端点。
//
// 需要注册账号才能取证的公正性不叫公正性。这个端点就是需求里
// "结束的抽奖活动要保留,作为历史公正查询"的落点,也是 module.PublicRouter
// 这个通用扩展点存在的唯一理由。
func (Mod) RegisterPublicRoutes(g *gin.RouterGroup) {
	g.GET("/lottery/public/:act_no/proof", handleGetProof)
	// 卡片背景图。匿名的理由与代价见 api_admin_cover.go 的文件头:
	// `<img src>` 这条请求由浏览器发出、不带 Authorization 头,而封面本来就是
	// 面向所有访客的招贴。只回**已经用在某场活动上**的那些,未绑定的上传取不到。
	g.GET("/lottery/covers/:ref", handleGetCover)
}

// RegisterUserRoutes 挂载普通用户接口。传入的组已挂 UserAuth。
//
// 刻意只挂 UserAuth 不挂 TokenAuth:API Key 天然是给机器用的、可批量分发,
// 允许它调用等于允许脚本化刷参与。
func (Mod) RegisterUserRoutes(g *gin.RouterGroup) {
	g.GET("/lottery/activities", middleware.SearchRateLimit(), handleListActivities)
	g.GET("/lottery/activities/:act_no", handleGetActivity)
	// 预检只用于展示"我为什么不能参加",绝不是放行依据。它会打主库与扩展库
	// 各几次,因此挂按用户限流。
	g.GET("/lottery/activities/:act_no/eligibility", middleware.SearchRateLimit(), handleGetEligibility)
	// 唯一会动钱的入口。幂等键只防得住"同一次点击的重试",防不住脚本用不同
	// client_request_id 连续发单,所以还要挂关键操作限流 —— **两把桶都要挂**。
	//
	// CriticalRateLimit 按 IP 分桶,而这条路要防的正是"一个被盗的会话 + 一个
	// 代理池":同一账号换出口 IP 就换了桶,实测同一 token 打满 20 次转 429 后,
	// 只加一个 X-Forwarded-For 就继续 200,参与费照扣。活动 Rules 的
	// max_entries_per_user / max_attempts_per_user / cooldown_seconds 默认全为 0
	// (= 无上限),而 lottery.pay_password_threshold_quota 默认 100000,单笔低于
	// 它连支付密码都不触发 —— 于是这是全站唯一一条"会动钱且无任何账号级节流"
	// 的用户路径,余额可以沿它被烧光。
	// 划转/提现/兑换码都有账号桶(见 router/api-router.go 的 /topup),抽奖漏了。
	g.POST("/lottery/activities/:act_no/entries",
		middleware.CriticalRateLimit(),
		middleware.UserCriticalRateLimit("lottery_entry"),
		handleCreateEntry)
	g.GET("/lottery/my-entries", handleListMyEntries)
	// 文本奖**逐条**拉取,刻意不做批量列表:一个列表接口返回全部正文,
	// 意味着一次越权 bug 就是全量泄漏。payout_no 由 crypto/rand 生成,不可枚举。
	g.GET("/lottery/my/prizes/:payout_no", handleGetMyPrize)
	// 双色球期次详情:当前池子、号池、上期开奖号。各档中奖概率**不下发**,
	// 由前端按组合数自己算 —— 后端下发等于把"概率不需要相信平台"这条丢掉。
	g.GET("/lottery/series/:series_no", handleGetSeries)
}

// RegisterAdminRoutes 挂载管理端接口。传入的组已挂 AdminAuth(自带上游操作审计)。
//
// 注意这里**没有**"提前截止"与"立即开奖"两个端点,理由见 api_admin.go 顶部。
func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/lottery/activities", handleAdminListActivities)
	g.GET("/lottery/activities/:act_no", handleAdminGetActivity)
	g.GET("/lottery/activities/:act_no/entries", handleAdminListEntries)
	g.GET("/lottery/activities/:act_no/payouts", handleAdminListPayouts)
	g.GET("/lottery/activities/:act_no/events", handleAdminListEvents)
	g.GET("/lottery/flags", handleAdminListFlags)
	g.GET("/lottery/config", handleGetConfig)
	g.GET("/lottery/series", handleAdminListSeries)
	// 文本奖履行队列。列表只给掩码,**明文只走 reveal 那一个写审计的接口**。
	g.GET("/lottery/text-prizes", handleAdminListTextPrizes)

	// 全部写接口都挂关键操作限流:它们要么决定平台会发出去多少钱,
	// 要么决定一场活动的结局。
	crit := middleware.CriticalRateLimit()
	g.POST("/lottery/activities", crit, handleCreateActivity)
	g.PUT("/lottery/activities/:act_no", crit, handleUpdateActivity)
	// 发布是不可逆的:承诺哈希一经生成,条件/奖档/选项/四个时刻/费率全部只读。
	g.POST("/lottery/activities/:act_no/publish", crit, handlePublishActivity)
	g.POST("/lottery/activities/:act_no/cancel", crit, handleCancelActivity)
	// 「关闭」与「删除」是两个动作,与 cancel 都不一样:
	//   hide/unhide 只把已结束的场次从用户端大厅撤下/放回,不动一分钱、可逆;
	//   DELETE 彻底抹掉这一场的十一张表,不可逆,过六道硬闸门 + 服务端二次确认。
	// 三者的代价差异在管理端弹窗的第一行各写一遍(见 api_admin_retire.go)。
	g.POST("/lottery/activities/:act_no/hide", crit, handleHideActivity)
	g.POST("/lottery/activities/:act_no/unhide", crit, handleUnhideActivity)
	g.DELETE("/lottery/activities/:act_no", crit, handleDeleteActivity)
	// 封面。上传要落磁盘,是本模块唯一一条能消耗宿主机存储的入口 ——
	// 必须挂关键操作限流,而不是只靠 coverPendingMax 那道计数闸门。
	g.POST("/lottery/covers", crit, handleUploadCover)
	g.DELETE("/lottery/covers/:ref", crit, handleDiscardCover)
	// 换封面**不限活动状态**:它不进任何哈希原像,因此是 publish 之后仍然
	// 可写的极少数字段之一(理由见 api_admin_cover.go)。
	g.PUT("/lottery/activities/:act_no/cover", crit, handleSetActivityCover)
	// 设定开奖结果是本模块**唯一**提到超级管理员的动作。
	//
	// 抽奖(draw)与双色球的结果来自建活动时就落库的 commit-reveal 随机源,
	// 由定时任务在承诺过的时刻揭示,没有任何管理端入口能影响它 —— 这一页
	// 刻意没有"提前截止"、"立即开奖"、"重抽"。只有竞猜(guess)的结果是**链下
	// 事实**,必须由人录入,那就是全站唯一一处"管理员说了算"的开奖口。
	//
	// 其余全部照旧 role>=10:建活动、发布、封盘、取消、隐藏、删除、换封面、
	// 履行/撤销/揭示文本奖、解决对账标记、期次注资与关闭、改运营参数。
	// 项目方原话:「管理员可以开活动、参与等等,但不能设定或更改开奖结果。」
	g.POST("/lottery/activities/:act_no/guess-result", crit,
		middleware.RootActionGate(middleware.RootActionLotteryResultSet), handleSetGuessResult)
	g.POST("/lottery/activities/:act_no/payouts/:payout_no/retry", crit, handleRetryPayout)
	// 文本奖的履行 / 撤销 / 揭示。三个都写审计(成功与失败各一条)。
	//
	// 撤销只对 kind='text' 开放:额度奖的 paid 是资金终态,永远不可撤。
	// 撤销**不清空密文** —— 用户可能已经用掉了那串码,抹掉记录等于抹掉
	// 争议时唯一的证据。
	g.POST("/lottery/payouts/:payout_no/fulfill", crit, handleFulfillPrize)
	g.POST("/lottery/payouts/:payout_no/unfulfill", crit, handleUnfulfillPrize)
	g.POST("/lottery/payouts/:payout_no/reveal", crit, handleRevealPrizeSecret)
	// 关掉一条已经处理完的对账异常。没有它 qy_lot_flag 只写不改,而 raiseFlag
	// 按 (act_id, code, resolved=false) 去重 —— 第一条落下之后这场活动这一类的
	// 检出就永久哑火,运营只能去改库。写审计。
	g.POST("/lottery/flags/:id/resolve", crit, handleAdminResolveFlag)
	g.PUT("/lottery/config", crit, handlePutConfig)

	// 双色球期次系列。注资是平台唯一一处主动扩大发行上限的动作,
	// 因此三个写接口全部挂关键操作限流并强制写审计。
	g.POST("/lottery/series", crit, handleCreateSeries)
	g.POST("/lottery/series/:series_no/fund", crit, handleFundSeries)
	g.POST("/lottery/series/:series_no/close", crit, handleCloseSeries)
}

// StartTasks 启动后台任务。
//
// 全部走 lease.Run 而非裸 goroutine:common.IsMasterNode 只是一个环境变量,
// 多节点都配成 master 时出款 worker 会双跑 —— 而重入 Execute 虽然幂等,
// 双跑仍会让同一批行反复撞 CAS,把日志刷满并掩盖真正的异常。
//
// **本模块不注册任何针对七张证据表的清理任务**(activity/seed/prize/option/
// entry/payout/event)。可清理的只有消费日桶与已解决的异常标记。
func (Mod) StartTasks() {
	cfg := config.Get().Lottery
	if !cfg.Enabled {
		return
	}

	// ── 活动状态机的自动推进 ──
	//
	// 封盘与开奖**只由时间触发,管理端一个按钮都没有**。理由见 lifecycle.go:
	// 结果是 f(种子, 冻结名单, 规则) 的确定函数,与执行时刻无关,但"什么时候
	// 封盘/开奖"若由人按,管理员就能挑一个对自己有利的时刻按下去。
	lease.Run("lottery.lock", seconds(cfg.LockScanIntervalSeconds, 15), runLock)
	lease.Run("lottery.reveal", seconds(cfg.RevealScanIntervalSeconds, 15), runReveal)
	// 逾期流局防的是"管理员无限期扣着奖池不结算"—— 竞猜最现实的资损形状,
	// 而且它连"作弊"都算不上,只需要什么都不做。
	lease.Run("lottery.void", seconds(cfg.RevealScanIntervalSeconds, 15), runVoidExpired)

	// 出款驱动与活动收尾分成两个租约:出款按 10 秒的节奏逐笔重试,
	// 而收尾要扫全部 settling 活动;混在一起会让一笔卡住的出款拖慢所有活动
	// 的收尾判定。
	lease.Run("lottery.payout", seconds(cfg.PayoutIntervalSeconds, 10), DrivePayouts)
	lease.Run("lottery.settle", seconds(cfg.PayoutIntervalSeconds, 10), runSettle)

	// 对账跑得比补偿任务还快没有意义,只会空扫:它收拾的正是补偿任务推完
	// 终态之后的尾巴。它只告警不自愈 —— 一个会自己改数的对账任务,
	// 在数据真被篡改时会顺手把证据也抹平。
	reconcileEvery := 2 * twophase.Interval()
	if reconcileEvery < time.Minute {
		reconcileEvery = time.Minute
	}
	lease.Run("lottery.reconcile", reconcileEvery, runReconcile)
	lease.Run("lottery.spend_scan", seconds(cfg.SpendScanIntervalSeconds, 60), func(ctx context.Context) {
		runSpendScan(ctx)
	})
	// 重算道跑得慢得多:它按整个自然日聚合,稳态下每天只有一天要算。
	// 与清理放在同一个任务里 —— 两者都只碰日桶表,分成两个租约只是多一把锁。
	lease.Run("lottery.spend_recalc", 10*time.Minute, func(ctx context.Context) {
		runSpendRecalc(ctx)
		pruneSpendDaily(ctx)
	})

	// 封面回收。周期一小时:三条到期口径的粒度都是"小时"级(宽限期 24 小时),
	// 跑得更勤只会空扫。必须走 lease.Run —— common.IsMasterNode 只是个环境变量,
	// 多节点都配成 master 时回收会双跑,而双跑的表现是同一批文件被删两次
	// (第二次报"文件不存在"并把整批标记跳过)。
	lease.Run("lottery.cover_prune", time.Hour, pruneCovers)
}

func seconds(v, fallback int) time.Duration {
	if v <= 0 {
		v = fallback
	}
	return time.Duration(v) * time.Second
}

func init() { module.Register(Mod{}) }
