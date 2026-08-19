package commission

// api_admin_balance.go —— 佣金余额总览,以及「已提现额度」的迁移编辑。
//
// # 这张表的四个额度列是什么关系
//
// qy_commission_balance 上的四个整数列不是四个独立的数字,它们受同一条恒等式约束:
//
//	可提现(available) + 冻结中(frozen) + 已提现(withdrawn)
//	    = 累计已结算(total_earned) − 累计冲正(total_clawback)
//
// 三条写入路径全都按这条恒等式成对改列:结算(settle.go)同时加 available 与
// total_earned;冲正同时减 available、加 total_clawback;提现三步
// (withdraw_api.go)在 available / frozen / withdrawn 之间搬运。
// 所以列表页必须把这条式子显式摊给运营看 —— 只给一个"可提现"数字,
// "为什么这个人有佣金却提不了"这个问题永远要靠翻代码回答。
//
// # 为什么编辑只开放「已提现」
//
// 项目方的诉求是把外挂佣金系统的历史数据搬进来,避免用户在这里把同一笔佣金
// 再领一次。那笔钱在旧系统里已经付掉了,对应到这张表就是:它不该再算在可提现里,
// 而该算在已提现里。也就是**把差额从 available 搬到 withdrawn**,恒等式两侧同时
// 不变 —— 这与提现成功(SettleFrozen)做的事在账面上是同一类搬运,只是没有单据。
//
// 直接改 available 或 total_earned 都被排除:前者是派生量,单独改它会让恒等式
// 立刻破,账本与 qy_commission_settlement 流水再也对不回去;后者等于凭空宣称
// 平台结算过一笔没有结算单的钱。
//
// # 语义是「设为绝对值」而不是「增量」
//
// 迁移场景天然是"这个人历史上已经领过 X",重复提交同一个 X 必须是空操作。
// 增量语义在网络重试/运营手滑双击时会把额度扣两遍,而扣掉的可提现额度
// 没有任何自动路径能还回来。

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 迁移编辑会被拒绝的三种情形。全部映射成 400 + 独立 code,
// 让前端能分辨"填大了"与"这行账本本来就是坏的"。
var (
	// errWithdrawnOverAvailable 是最常撞到的一条:差额要从可提现里划走,
	// 划不出来就说明这个数字对不上,硬写会让 available 变成负数。
	errWithdrawnOverAvailable = errors.New("已提现额度不能超过「当前已提现 + 当前可提现」")
	// errWithdrawnOverEarned 只会在余额行本身已经漂移时命中(恒等式成立时它被
	// 上一条完全覆盖)。留着是防御纵深:一行坏账本上,唯一比"改不了"更糟的
	// 结果是"改成了一个更坏的值"。
	errWithdrawnOverEarned = errors.New("已提现额度不能超过历史累计已结算净额(该余额行已存在账本漂移)")
	// errWithdrawnOverflow 守回退方向:调小已提现会把额度还回可提现,
	// 而这些额度最终要流向主库的 int32 额度列。
	errWithdrawnOverflow = errors.New("回退后的可提现额度会超过单账户额度上限")
)

// balanceErrCodes 把上面三个哨兵错误映射成前端可 i18n 的 code。
var balanceErrCodes = map[error]string{
	errWithdrawnOverAvailable: "qy_withdrawn_over_available",
	errWithdrawnOverEarned:    "qy_withdrawn_over_earned",
	errWithdrawnOverflow:      "qy_withdrawn_overflow",
}

// balanceSortOrders 是列表排序的白名单。
//
// 一律带 user_id 做次级排序:仅按金额排时,金额相同的行在 MySQL 里没有稳定
// 顺序,翻页会漏行也会重复行 —— 迁移时对着列表逐个改,漏掉的那个人就永远
// 少扣一笔。
var balanceSortOrders = map[string]string{
	"available": "available_quota desc, user_id asc",
	"withdrawn": "withdrawn_quota desc, user_id asc",
	"earned":    "total_earned_quota desc, user_id asc",
	"updated":   "updated_at desc, user_id asc",
	"user":      "user_id asc",
}

const defaultBalanceSort = "available"

// balanceView 是一行佣金余额对管理端的形状。
//
// DerivedAvailable / LedgerDrift 是刻意冗余的:它们能由前四列算出来,但让**后端**
// 算并下发,前端就不会出现第二份算法。这条恒等式已经在 settle / clawback /
// withdraw 三处各被实现了一遍,再让前端实现第四遍是在等它漂移。
type balanceView struct {
	UserId   int    `json:"user_id"`
	Username string `json:"username"`
	// UserResolved 为假表示主库里读不到这个 id(账号已删,或这一次主库读失败)。
	// 不谎称"用户名为空",迁移时对着一个空名字改钱是最不该发生的事。
	UserResolved bool `json:"user_resolved"`

	AvailableQuota     int64 `json:"available_quota"`
	FrozenQuota        int64 `json:"frozen_quota"`
	WithdrawnQuota     int64 `json:"withdrawn_quota"`
	TotalEarnedQuota   int64 `json:"total_earned_quota"`
	TotalClawbackQuota int64 `json:"total_clawback_quota"`

	// DerivedAvailable = 已结算 − 已冲正 − 冻结中 − 已提现,即恒等式给出的可提现。
	DerivedAvailable int64 `json:"derived_available_quota"`
	// LedgerDrift = 实际可提现 − 派生可提现。非 0 就是账本坏了,必须先查清楚再动。
	LedgerDrift int64 `json:"ledger_drift"`

	// 余数与法币一律用字符串下发:decimal(30,10) 到了 JS 的 Number 里会丢位。
	UnsettledAmount string `json:"unsettled_amount"`
	AvailableFiat   string `json:"available_fiat"`

	DebtBlocked bool `json:"debt_blocked"`
	// InviteeCount 一律由 hydrateBalanceViews 现算(qy_invite_relation 里
	// unbound_at = 0 的行数),与「佣金总表」页同一份口径。余额行上曾经有一个
	// 同名列,但它没有任何写入方 —— 下发它等于对所有真正有下线的推广人报 0。
	InviteeCount  int   `json:"invitee_count"`
	LastSettledAt int64 `json:"last_settled_at"`
	UpdatedAt     int64 `json:"updated_at"`
}

func newBalanceView(b Balance) balanceView {
	derived := b.TotalEarnedQuota - b.TotalClawbackQuota - b.FrozenQuota - b.WithdrawnQuota
	return balanceView{
		UserId:             b.UserId,
		AvailableQuota:     b.AvailableQuota,
		FrozenQuota:        b.FrozenQuota,
		WithdrawnQuota:     b.WithdrawnQuota,
		TotalEarnedQuota:   b.TotalEarnedQuota,
		TotalClawbackQuota: b.TotalClawbackQuota,
		DerivedAvailable:   derived,
		LedgerDrift:        b.AvailableQuota - derived,
		UnsettledAmount:    b.UnsettledAmount.String(),
		AvailableFiat:      b.AvailableFiat.Round(2).String(),
		DebtBlocked:        b.DebtBlocked,
		LastSettledAt:      b.LastSettledAt,
		UpdatedAt:          b.UpdatedAt,
	}
}

// adminListBalances 分页列出全平台佣金余额。
//
// 只按 user_id / username 精确定位,不做模糊搜索:模糊搜索要么在扩展库里
// LIKE 一个不存在的用户名列,要么再往主库里塞一份 LIKE 转义实现 —— 后者在本仓
// 已经有一份(qianye/controller/request_audit.go),复制第二份就是在等它漂移。
func adminListBalances(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)

	userId := httpq.Int(c, "user_id", 0)
	if name := strings.TrimSpace(c.Query("username")); name != "" {
		resolved, err := findUserIdByName(ctx, name)
		if err != nil {
			internalError(c, err)
			return
		}
		if resolved == 0 || (userId > 0 && userId != resolved) {
			// 用户名查无此人(或与同时给出的 user_id 互相矛盾)。回空页而不是
			// 忽略这个条件 —— 忽略掉筛选条件返回全表,看起来与"这个人排在第一页"
			// 一模一样,而迁移正是照着第一行改钱。
			respond(c, gin.H{
				"items": []balanceView{}, "total": 0, "p": page, "page_size": size,
				"totals": gin.H{"available_quota": 0, "withdrawn_quota": 0},
			})
			return
		}
		userId = resolved
	}

	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	q := gdb.WithContext(ctx).Model(&Balance{})
	if userId > 0 {
		q = q.Where("user_id = ?", userId)
	}
	if c.Query("debt_only") == "true" {
		q = q.Where("debt_blocked = ?", true)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	// 合计跟着同一组筛选走。迁移对账问的就是"这批人一共还挂着多少可提现、
	// 已经登记了多少已提现",逐页心算是不可行的。
	var sums struct {
		AvailableQuota int64
		WithdrawnQuota int64
	}
	if err := q.Session(&gorm.Session{}).
		Select("COALESCE(SUM(available_quota), 0) AS available_quota, " +
			"COALESCE(SUM(withdrawn_quota), 0) AS withdrawn_quota").
		Scan(&sums).Error; err != nil {
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	order := balanceSortOrders[c.Query("sort")]
	if order == "" {
		order = balanceSortOrders[defaultBalanceSort]
	}
	var rows []Balance
	if err := q.Order(order).Offset(httpq.Offset(page, size)).Limit(size).
		Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	respond(c, gin.H{
		"items":     hydrateBalanceViews(ctx, rows),
		"total":     total,
		"p":         page,
		"page_size": size,
		"totals": gin.H{
			"available_quota": sums.AvailableQuota,
			"withdrawn_quota": sums.WithdrawnQuota,
		},
	})
}

// findUserIdByName 精确解析用户名到主库 id。查无此人返回 0(不是错误)。
func findUserIdByName(ctx context.Context, name string) (int, error) {
	if model.DB == nil {
		return 0, db.ErrNotReady
	}
	var row struct{ Id int }
	err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id").Where("username = ?", name).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.Id, nil
}

// hydrateBalanceViews 回主库补用户名。
//
// 主库读失败时整页标 UserResolved=false 而不是把名字留空:迁移是"照着名字
// 改钱"的操作,分不清"这个账号没了"和"这次没读到"就等于在盲改。
func hydrateBalanceViews(ctx context.Context, rows []Balance) []balanceView {
	views := make([]balanceView, 0, len(rows))
	for _, r := range rows {
		views = append(views, newBalanceView(r))
	}
	if len(views) == 0 || model.DB == nil {
		return views
	}

	ids := make([]int, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.UserId)
	}
	var users []model.User
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		common.SysError("qianye/commission: 回读佣金余额对应主库用户失败: " + err.Error())
		return views
	}
	names := make(map[int]string, len(users))
	for _, u := range users {
		names[u.Id] = u.Username
	}
	downlines := downlineCounts(ctx, ids)
	for i := range views {
		if name, ok := names[views[i].UserId]; ok {
			views[i].Username = name
			views[i].UserResolved = true
		}
		views[i].InviteeCount = downlines[views[i].UserId]
	}
	return views
}

// resolvedBalanceView 是单行版的 hydrateBalanceViews。
//
// 手工增减佣金与迁移已提现额度那两条路(以及它们各自的审计前后快照)以前直接用
// newBalanceView:那是从**扩展库**的余额行造出来的形状,username 恒为空串、
// user_resolved 恒为 false。而 user_resolved=false 这一位有明确语义——"主库里
// 读不到这个 id(账号已删,或这一次主库读失败)"。于是每一条改钱审计的前后快照
// 都永久带着一个假的"账号不可解析"标记,事后仲裁时分不清"当时账号真的没了"
// 与"当时代码没去查";审计详情页是原样展开这段 JSON 的,管理员看得到它。
func resolvedBalanceView(ctx context.Context, b Balance) balanceView {
	views := hydrateBalanceViews(ctx, []Balance{b})
	if len(views) == 0 {
		return newBalanceView(b)
	}
	return views[0]
}

// downlineCounts 数每个上线名下还在绑的下线条数。
//
// 与 api_admin_users.go 同一份口径(qy_invite_relation 里 unbound_at = 0),
// 这是全仓唯一权威的算法 —— 余额行上那个同名列没有任何写入方。
// 读不到就返回空表:少一个展示数字远好于让整页余额打不开。
func downlineCounts(ctx context.Context, inviterIds []int) map[int]int {
	out := map[int]int{}
	gdb := db.Get()
	if gdb == nil || len(inviterIds) == 0 {
		return out
	}
	var rows []struct {
		InviterId int
		Cnt       int
	}
	if err := gdb.WithContext(ctx).Model(&InviteRelation{}).
		Select("inviter_id, COUNT(*) AS cnt").
		Where("inviter_id IN ? AND unbound_at = 0", inviterIds).
		Group("inviter_id").Scan(&rows).Error; err != nil {
		common.SysError("qianye/commission: 统计佣金余额对应下线数失败: " + err.Error())
		return out
	}
	for _, r := range rows {
		out[r.InviterId] = r.Cnt
	}
	return out
}

// adminSetWithdrawn 把某个用户的「已提现」额度设成给定的绝对值,差额从可提现划转。
//
// 这是全模块唯一一个不产生任何业务单据、直接改余额行的接口,因此按最高标准做:
//   - 事由必填且 ≥4 字符(与 withdraw 模块看 PII 明文同口径):没有事由的改账
//     事后无法与误操作区分;
//   - 成功与失败都写审计,带前后完整快照与**实际发生的差额**;
//   - 全程在行锁(lockBalance)内完成:与结算任务、提现冻结共用同一把锁,
//     否则并发下会丢更新 —— 结算刚加上的可提现会被这里的旧读值覆盖掉;
//   - 绝对值语义,重复提交是空操作。
func adminSetWithdrawn(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		UserId int `json:"user_id"`
		// 指针:0 是合法的目标值(把误填的已提现清零),用非指针就分不清
		// "设为 0" 与 "这个字段根本没传"。
		WithdrawnQuota *int64 `json:"withdrawn_quota"`
		Reason         string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.UserId <= 0 {
		badRequest(c, "qy_invalid_param", "必须指定 user_id")
		return
	}
	if req.WithdrawnQuota == nil {
		badRequest(c, "qy_invalid_param", "必须给出已提现额度的目标值")
		return
	}
	target := *req.WithdrawnQuota
	if target < 0 || target > int64(common.MaxQuota) {
		badRequest(c, "qy_invalid_param", "已提现额度必须是 0 到额度上限之间的整数")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len([]rune(reason)) < 4 {
		badRequest(c, "qy_reason_required", "必须填写迁移事由(至少 4 个字符)")
		return
	}

	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		internalError(c, db.ErrNotReady)
		return
	}

	var before, after Balance
	var delta int64
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		bal, err := lockBalance(tx, req.UserId)
		if err != nil {
			return err
		}
		before = *bal
		delta = target - bal.WithdrawnQuota
		if delta == 0 {
			// 绝对值语义下的重复提交。仍然要走到审计,那条记录回答的是
			// "运营在这一刻确认过这个人的已提现就是这个数"。
			after = *bal
			return nil
		}
		if delta > bal.AvailableQuota {
			return errWithdrawnOverAvailable
		}
		newAvail := bal.AvailableQuota - delta
		if newAvail > int64(common.MaxQuota) {
			return errWithdrawnOverflow
		}
		if target > bal.TotalEarnedQuota-bal.TotalClawbackQuota {
			return errWithdrawnOverEarned
		}
		// 法币按额度比例同步缩放,与提现冻结、冲正回收走同一个 scaleFiat ——
		// 只改额度不改法币,available_fiat 会与 available_quota 永久漂移,
		// 而提现模块按法币口径对账。
		if err := applyBalance(tx, req.UserId, map[string]any{
			"withdrawn_quota": target,
			"available_quota": newAvail,
			"available_fiat":  scaleFiat(bal.AvailableFiat, newAvail, bal.AvailableQuota),
		}); err != nil {
			return err
		}
		var reread Balance
		if err := tx.Where("user_id = ?", req.UserId).Take(&reread).Error; err != nil {
			return err
		}
		after = reread
		return nil
	})

	if err != nil {
		// 审计落在事务之外,否则它会跟着一起回滚 —— 而"有人在这一刻试图改某个人的
		// 已提现额度、失败了"正是最需要留痕的事实。AmountQuota 记 0:这一路
		// 资金侧什么都没发生,写上意图金额会让审计看起来像是真扣了。
		writeWithdrawnAudit(c, req.UserId, 0, qymodel.ResultFail,
			"迁移登记已提现额度失败(事务已回滚): "+err.Error()+" | 事由: "+reason,
			before, currentBalance(ctx, req.UserId))
		if code, ok := balanceErrCodes[err]; ok {
			badRequest(c, code, err.Error())
			return
		}
		db.MarkFailure(err)
		internalError(c, err)
		return
	}

	// 金额取事务内算出的真实差额,绝不用请求里的目标值:重复提交时差额是 0,
	// 而审计表是这套资金系统事后仲裁的唯一凭据。
	writeWithdrawnAudit(c, req.UserId, delta, qymodel.ResultOK,
		"迁移登记已提现额度: "+reason, before, after)
	respond(c, gin.H{
		"user_id":     req.UserId,
		"delta_quota": delta,
		"before":      resolvedBalanceView(c.Request.Context(), before),
		"after":       resolvedBalanceView(c.Request.Context(), after),
	})
}

// currentBalance 尽力回读余额行,读不到就回零值。
// 只用于失败路径的 after 快照:那条快照回答"库里现在到底是什么",
// 读不到本身也是一种回答,不该因此把审计整条丢掉。
func currentBalance(ctx context.Context, userId int) Balance {
	var bal Balance
	gdb := db.Get()
	if gdb == nil {
		return bal
	}
	if err := gdb.WithContext(ctx).Where("user_id = ?", userId).Take(&bal).Error; err != nil {
		return Balance{UserId: userId}
	}
	return bal
}

// writeWithdrawnAudit 落一条已提现迁移审计,成功与失败共用。
//
// 快照走 balanceView 而不是裸结构体:事后翻审计的是人,而 balanceView 里带着
// 派生可提现与漂移量 —— 那正是判断"这次改动是否把账本改坏了"要看的两个数。
func writeWithdrawnAudit(c *gin.Context, userId int, delta int64, result, reason string, before, after Balance) {
	beforeSnap, _ := common.Marshal(resolvedBalanceView(c.Request.Context(), before))
	afterSnap, _ := common.Marshal(resolvedBalanceView(c.Request.Context(), after))
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       "commission.balance.withdrawn.set",
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: userId,
		AmountQuota:  delta,
		Result:       result,
		Reason:       reason,
		BeforeSnap:   string(beforeSnap),
		AfterSnap:    string(afterSnap),
	})
}

// registerBalanceRoutes 挂载余额总览与迁移编辑两个管理端接口。
//
// 列表挂搜索限流(它做两次全表聚合),编辑挂关键操作限流(它直接改钱)。
func registerBalanceRoutes(g *gin.RouterGroup, crit gin.HandlerFunc) {
	g.GET("/commission/balances", middleware.SearchRateLimit(), adminListBalances)
	g.POST("/commission/balances/withdrawn", crit, adminSetWithdrawn)
}
