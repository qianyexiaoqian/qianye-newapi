package commission

// api_admin_relation.go —— AFF(邀请)关系的管理端列表与手工绑定/解绑。
//
// # 权威字段:users.inviter_id,不是 qy_invite_relation
//
// 这套系统里有两处看起来都像"邀请关系"的数据,它们**不是**互为副本:
//
//	主库 users.inviter_id      ← 权威。注册时由 aff_code 解析出来一次性写入,
//	                             计佣链路唯一的判据(resolveInviter → peekInviter)。
//	主库 users.aff_code        ← 是这个人**自己的**推广码,不是绑定关系。
//	主库 users.aff_count/aff_quota/aff_history
//	                          ← 上游自带的另一套邀请奖励池,与本模块的佣金账本
//	                             完全无关。管理员手工绑定**绝不补发**它:那笔额度
//	                             是注册那一刻 inviteUser() 发的,事后补等于凭空造钱。
//	扩展库 qy_invite_relation  ← **懒建的展示快照**。ensureRelation 只在某个下线
//	                             第一次产生佣金时才写这一行。
//
// 最后一条是本文件所有设计的出发点:备份库实测 users 里有 375 条绑定,而
// qy_invite_relation 只有 8 行。拿快照表当 AFF 关系列表的数据源,管理员会看到
// 一张少了 98% 的表,而且没有任何报错。所以列表(绑定中)一律从主库出。
//
// # 改了一边要不要同步另一边
//
// 要,而且顺序是固定的:先写主库(权威),再补扩展库快照,最后失效
// invalidateInviter —— 邀请关系有最长 InviterCacheSecs 的进程内缓存,不失效的话
// 解绑之后的一整个 TTL 里仍然在给旧邀请人计佣,而库里已经查不出原因了。
//
// # 解绑之后已经产生的佣金怎么办
//
// **保留,不再产生新的**。这是本文件最重要的一条语义,理由不是保守:
//
//   - qy_commission_accrual 是 append-only 的账本,Σgross 与 Σsettled、
//     结算单、余额行三者互相咬合。删掉计佣行会让 Σ计佣 与 Σ结算 当场对不上,
//     而且没有任何一条流水能解释差额(上一轮实测已经踩过这个坑);
//   - 已结算的部分早就变成了 qy_commission_balance.available_quota,
//     甚至可能已经提现走了 —— 账面上"删掉"它只会把余额行改成负数。
//
// 要把已经发出去的钱要回来,必须单独走「冲正」(commission.clawback):
// 那是一个独立的决定,必须由人显式做出并填写理由,而不是"解绑"的副作用。

import (
	"context"
	"errors"
	"net/http"
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
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// 绑定/解绑会被拒绝的情形。全部映射成独立 code,让前端能分辨
// "换个人也许可以"与"这个动作本身就不该做"。
var (
	errRelSelfInvite  = errors.New("邀请人与被邀请人不能是同一个账号")
	errRelUserMissing = errors.New("邀请人或被邀请人不存在(或已被删除)")
	errRelAlreadyBd   = errors.New("该账号已经绑定了邀请人,请先解绑再重新绑定")
	errRelCycle       = errors.New("这条绑定会形成邀请环路(A 邀 B、B 又邀 A)")
	errRelNotBound    = errors.New("该账号当前没有绑定邀请人")
	errRelRaced       = errors.New("邀请关系已被另一次操作改动,请刷新后重试")
	// errRelSameInviter 挡住"换成他现在的这个人"。回 400 而不是当空操作回 200:
	// 换绑的语义是"从此以后佣金归另一个人",运营点了确认却什么都没变,
	// 却看到成功提示,下一步就会去账本里找那笔并不存在的变化。
	errRelSameInviter = errors.New("新的邀请人与当前邀请人是同一个账号,没有需要改动的地方")
)

var relationErrCodes = map[error]string{
	errRelSelfInvite:  "qy_rel_self_invite",
	errRelUserMissing: "qy_rel_user_not_found",
	errRelAlreadyBd:   "qy_rel_already_bound",
	errRelCycle:       "qy_rel_cycle",
	errRelNotBound:    "qy_rel_not_bound",
	errRelRaced:       "qy_rel_conflict",
	errRelSameInviter: "qy_rel_same_inviter",
}

// maxInviteChainDepth 是防环时向上追溯的层数上界。
//
// 上游的返佣只有一层(users.inviter_id 没有递归展开),但绑定关系本身可以串成
// 任意长的链。只挡 A↔B 两两互邀会漏掉 A→B→C→A 这种三人环 —— 它同样是自刷,
// 只是多拉了一个号。上界的存在是为了让一条已经损坏(成环)的历史数据不会把
// 这个循环变成死循环。
const maxInviteChainDepth = 64

// relationSortOrders 是绑定中列表的排序白名单(作用在**主库 users** 上)。
//
// 一律带 id 做次级排序:注册时间相同的行在 MySQL 里没有稳定顺序,翻页会漏行
// 也会重复行 —— 而这一页正是"逐个核对谁绑了谁"的地方。
var relationSortOrders = map[string]string{
	"newest":  "created_at desc, id desc",
	"oldest":  "created_at asc, id asc",
	"invitee": "id asc",
	"inviter": "inviter_id asc, id asc",
}

// unboundSortOrders 是"已解绑"列表的排序白名单(作用在**扩展库快照**上)。
// 键与 relationSortOrders 逐个对应,前端只有一个下拉。
var unboundSortOrders = map[string]string{
	"newest":  "unbound_at desc, invitee_id desc",
	"oldest":  "unbound_at asc, invitee_id asc",
	"invitee": "invitee_id asc",
	"inviter": "inviter_id asc, invitee_id asc",
}

const defaultRelationSort = "newest"

// relationView 是一条 AFF 关系对管理端的形状。
//
// 两侧都给 id + 用户名:管理员本就有权看到真实身份,脱敏只针对"邀请人看下线"
// 那个方向(见 mask.go)。
type relationView struct {
	InviteeId       int    `json:"invitee_id"`
	InviteeUsername string `json:"invitee_username"`
	InviteeResolved bool   `json:"invitee_resolved"`

	InviterId       int    `json:"inviter_id"`
	InviterUsername string `json:"inviter_username"`
	InviterResolved bool   `json:"inviter_resolved"`

	// BoundAt 是扩展库快照记下的绑定时刻。0 表示还没有快照行
	// (这个下线一次佣金都没产生过,ensureRelation 从没跑到),
	// 此时前端回落显示 InviteeCreatedAt 并标注"按注册时间推定"。
	BoundAt int64 `json:"bound_at"`
	// InviteeCreatedAt 是下线的注册时间。自动绑定发生在注册那一刻,
	// 所以它同时是绝大多数关系的真实绑定时间。
	InviteeCreatedAt int64 `json:"invitee_created_at"`
	// UnboundAt > 0 表示这条关系已被管理员解绑,只保留历史。
	UnboundAt int64 `json:"unbound_at"`

	// TotalCommission 是这条关系(这一对 inviter/invitee)累计产生的佣金全精度值,
	// 字符串下发:decimal(30,10) 到了 JS 的 Number 里会丢位。
	TotalCommission string `json:"total_commission"`
	// TotalCommissionQuota 是同一个数向下取整后的额度,给前端 formatQuota 用。
	// 向下取整而不是四舍五入:列表上的钱宁可略小于账本也不能虚报。
	TotalCommissionQuota int64 `json:"total_commission_quota"`
	TotalBaseQuota       int64 `json:"total_base_quota"`
	AccrualCount         int64 `json:"accrual_count"`

	// SnapshotPresent 为假表示扩展库里还没有这条关系的快照行。它不是异常 ——
	// 快照是懒建的 —— 但解释了为什么 BoundAt 是 0。
	SnapshotPresent bool   `json:"snapshot_present"`
	Blocked         bool   `json:"blocked"`
	RiskFlags       string `json:"risk_flags"`
}

// relationPair 是 (邀请人, 被邀请人) 这个复合键。
//
// 佣金聚合必须按**这一对**统计而不是只按 invitee:一个账号解绑后重新绑给另一个
// 邀请人时,老邀请人名下的历史计佣行仍然挂着同一个 invitee_id,只按 invitee 聚合
// 会把它算进新关系的"累计佣金"里 —— 新邀请人一分钱没挣,列表上却写着一大笔。
type relationPair struct {
	InviterId int
	InviteeId int
}

// adminListRelations 分页列出 AFF 关系。
//
// scope=bound(默认)从**主库 users** 出,因为那才是权威;scope=unbound 从扩展库
// 快照出,列的是"曾经绑过、已经解绑"的关系及其保留下来的历史佣金。
func adminListRelations(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)

	inviterId := httpq.Int(c, "inviter_id", 0)
	inviteeId := httpq.Int(c, "invitee_id", 0)
	// 用户名是"从任一侧反查"的入口:填一个名字,两边都匹配。
	// 精确匹配而不是 LIKE,理由与 adminListBalances 一致。
	eitherId := 0
	if name := strings.TrimSpace(c.Query("username")); name != "" {
		resolved, err := findUserIdByName(ctx, name)
		if err != nil {
			internalError(c, err)
			return
		}
		if resolved == 0 {
			// 查无此人必须回空页。忽略掉这个条件返回的是**未经筛选的全表**,
			// 而它看起来与"这个人排在第一页"一模一样 —— 而这一页上有解绑按钮。
			respond(c, gin.H{"items": []relationView{}, "total": 0, "p": page, "page_size": size})
			return
		}
		eitherId = resolved
	}

	// 下发给前端的数组一律显式初始化,理由见 qianye/json_array_guard_test.go:
	// 裸 var 声明的切片在两个分支都没赋值时会序列化成 null,前端对着 null
	// 调 .map 会整页白屏。两条分支自己也各用 make,这里是第二道。
	views := make([]relationView, 0, size)
	list := listBoundRelations
	if c.Query("scope") == "unbound" {
		list = listUnboundRelations
	}
	fetched, total, err := list(ctx, relationFilter{
		InviterId: inviterId, InviteeId: inviteeId, EitherId: eitherId,
		Sort: c.Query("sort"), Page: page, Size: size,
	})
	if err != nil {
		internalError(c, err)
		return
	}
	views = append(views, fetched...)
	respond(c, gin.H{"items": views, "total": total, "p": page, "page_size": size})
}

type relationFilter struct {
	InviterId int
	InviteeId int
	EitherId  int
	Sort      string
	Page      int
	Size      int
}

// listBoundRelations 从主库列出仍然生效的绑定关系。
func listBoundRelations(ctx context.Context, f relationFilter) ([]relationView, int64, error) {
	if model.DB == nil {
		return nil, 0, db.ErrNotReady
	}
	q := model.DB.WithContext(ctx).Model(&model.User{}).Where("inviter_id > ?", 0)
	if f.InviterId > 0 {
		q = q.Where("inviter_id = ?", f.InviterId)
	}
	if f.InviteeId > 0 {
		q = q.Where("id = ?", f.InviteeId)
	}
	if f.EitherId > 0 {
		// 括号是必须的:这个条件要与前面的 inviter_id > 0 用 AND 串起来,
		// 少了括号在部分方言下会退化成 "(A AND B) OR C",OR 右边那支
		// 直接绕过全部筛选返回额外的行。
		q = q.Where("(id = ? OR inviter_id = ?)", f.EitherId, f.EitherId)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := relationSortOrders[f.Sort]
	if order == "" {
		order = relationSortOrders[defaultRelationSort]
	}
	var rows []model.User
	if err := q.Session(&gorm.Session{}).
		Select("id", "username", "inviter_id", "created_at").
		Order(order).Offset(httpq.Offset(f.Page, f.Size)).Limit(f.Size).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}

	views := make([]relationView, 0, len(rows))
	for _, r := range rows {
		views = append(views, relationView{
			InviteeId:        r.Id,
			InviteeUsername:  r.Username,
			InviteeResolved:  true,
			InviterId:        r.InviterId,
			InviteeCreatedAt: r.CreatedAt,
			TotalCommission:  decimal.Zero.String(),
		})
	}
	if err := hydrateRelationViews(ctx, views); err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

// listUnboundRelations 从扩展库快照列出已解绑的关系。
//
// 这一路的数据源只能是快照:主库那边 inviter_id 已经被清零,"他曾经是谁的下线"
// 这个事实在主库里一个字都不剩了。
func listUnboundRelations(ctx context.Context, f relationFilter) ([]relationView, int64, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, 0, db.ErrNotReady
	}
	q := gdb.WithContext(ctx).Model(&InviteRelation{}).Where("unbound_at > ?", 0)
	if f.InviterId > 0 {
		q = q.Where("inviter_id = ?", f.InviterId)
	}
	if f.InviteeId > 0 {
		q = q.Where("invitee_id = ?", f.InviteeId)
	}
	if f.EitherId > 0 {
		q = q.Where("(invitee_id = ? OR inviter_id = ?)", f.EitherId, f.EitherId)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		return nil, 0, err
	}
	order := unboundSortOrders[f.Sort]
	if order == "" {
		order = unboundSortOrders[defaultRelationSort]
	}
	var rows []InviteRelation
	if err := q.Session(&gorm.Session{}).Order(order).
		Offset(httpq.Offset(f.Page, f.Size)).Limit(f.Size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, 0, err
	}

	views := make([]relationView, 0, len(rows))
	for _, r := range rows {
		views = append(views, relationView{
			InviteeId:       r.InviteeId,
			InviterId:       r.InviterId,
			BoundAt:         r.BoundAt,
			UnboundAt:       r.UnboundAt,
			SnapshotPresent: true,
			Blocked:         r.Blocked,
			RiskFlags:       r.RiskFlags,
			TotalCommission: decimal.Zero.String(),
		})
	}
	if err := hydrateRelationViews(ctx, views); err != nil {
		return nil, 0, err
	}
	return views, total, nil
}

// hydrateRelationViews 给一页关系补上两侧用户名、快照信息与累计佣金。
//
// 三次查询是刻意的(主库用户、扩展库快照、扩展库聚合):关系在主库、钱在扩展库,
// 没有任何一条 SQL 能同时读到两边。页长上限 100,三次都走主键或索引。
func hydrateRelationViews(ctx context.Context, views []relationView) error {
	if len(views) == 0 {
		return nil
	}
	ids := make([]int, 0, len(views)*2)
	inviteeIds := make([]int, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.InviteeId, v.InviterId)
		inviteeIds = append(inviteeIds, v.InviteeId)
	}

	names := map[int]model.User{}
	if model.DB != nil {
		var users []model.User
		if err := model.DB.WithContext(ctx).Model(&model.User{}).
			Select("id", "username", "created_at").Where("id IN ?", ids).
			Find(&users).Error; err != nil {
			// 主库读不到时不谎称"用户名为空":*_resolved 保持 false,
			// 前端会显示"账号不存在或读取失败"。这一页上有解绑按钮,
			// 分不清"这个号没了"和"这次没读到"就等于在盲改。
			common.SysError("qianye/commission: 回读邀请关系对应主库用户失败: " + err.Error())
		}
		for _, u := range users {
			names[u.Id] = u
		}
	}

	gdb := db.Get()
	snaps := map[relationPair]InviteRelation{}
	totals := map[relationPair]inviteePairAggregate{}
	if gdb != nil {
		var rels []InviteRelation
		if err := gdb.WithContext(ctx).Where("invitee_id IN ?", inviteeIds).
			Find(&rels).Error; err != nil {
			db.MarkFailure(err)
			return err
		}
		for _, r := range rels {
			snaps[relationPair{InviterId: r.InviterId, InviteeId: r.InviteeId}] = r
		}

		var aggs []inviteePairAggregate
		if err := gdb.WithContext(ctx).Model(&Accrual{}).
			Select("inviter_id, invitee_id, "+
				"COALESCE(SUM(gross_amount),0) AS gross, "+
				"COALESCE(SUM(base_quota),0) AS base_quota, "+
				"COUNT(*) AS rows_count").
			Where("invitee_id IN ? AND status <> ?", inviteeIds, StatusVoided).
			Group("inviter_id, invitee_id").Scan(&aggs).Error; err != nil {
			db.MarkFailure(err)
			return err
		}
		for _, a := range aggs {
			totals[relationPair{InviterId: a.InviterId, InviteeId: a.InviteeId}] = a
		}
	}

	for i := range views {
		v := &views[i]
		if u, ok := names[v.InviteeId]; ok {
			v.InviteeUsername = u.Username
			v.InviteeResolved = true
			v.InviteeCreatedAt = u.CreatedAt
		}
		if u, ok := names[v.InviterId]; ok {
			v.InviterUsername = u.Username
			v.InviterResolved = true
		}
		key := relationPair{InviterId: v.InviterId, InviteeId: v.InviteeId}
		if r, ok := snaps[key]; ok {
			v.SnapshotPresent = true
			v.BoundAt = r.BoundAt
			v.UnboundAt = r.UnboundAt
			v.Blocked = r.Blocked
			v.RiskFlags = r.RiskFlags
		}
		if a, ok := totals[key]; ok {
			v.TotalCommission = a.Gross.String()
			// Floor 而不是 QuotaFromDecimal 的四舍五入:列表上的钱宁可略小于
			// 账本也不能虚报。负数(净冲正)照样 Floor,方向不变。
			v.TotalCommissionQuota = int64(common.QuotaFromDecimal(a.Gross.Floor()))
			v.TotalBaseQuota = a.BaseQuota
			v.AccrualCount = a.RowsCount
		}
	}
	return nil
}

// inviteePairAggregate 是按 (邀请人, 被邀请人) 聚合出来的累计佣金。
type inviteePairAggregate struct {
	InviterId int             `gorm:"column:inviter_id"`
	InviteeId int             `gorm:"column:invitee_id"`
	BaseQuota int64           `gorm:"column:base_quota"`
	Gross     decimal.Decimal `gorm:"column:gross"`
	RowsCount int64           `gorm:"column:rows_count"`
}

// ───────────────────────── 绑定 ─────────────────────────

// adminBindRelation 手工建立一条邀请关系。
//
// 三道闸门,每一道都对应一种真实发生过的滥用或误操作:
//
//	自邀请   —— 计佣路径本来就会跳过它(accrueConsume 有一条 e.InviterId == inviteeId),
//	            但让这种行存在只会让列表上多一条永远不产生佣金的假关系;
//	已绑定   —— 直接改指向等于把未来佣金从一个人手里转给另一个人,而"解绑"这个
//	            决定必须被单独做出并单独留痕。所以这里拒绝,不覆盖;
//	环路     —— 向上追溯 inviter_id 链,撞到被邀请人就是环。只挡两两互邀会漏掉
//	            A→B→C→A。
func adminBindRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		InviterId int    `json:"inviter_id"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.InviteeId <= 0 || req.InviterId <= 0 {
		badRequest(c, "qy_invalid_param", "必须同时指定邀请人与被邀请人")
		return
	}
	reason, ok := requireReason(c, req.Reason)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	before := relationSnapshot(ctx, req.InviteeId)
	inviter, invitee, err := bindRelation(ctx, req.InviterId, req.InviteeId, reason)
	if err != nil {
		writeRelationAudit(c, "commission.relation.bind", req.InviteeId, qymodel.ResultFail,
			"手工绑定邀请关系失败: "+err.Error()+" | 事由: "+reason,
			before, relationSnapshot(ctx, req.InviteeId))
		respondRelationError(c, err)
		return
	}

	invalidateInviter(req.InviteeId)
	invalidateBlocked()
	writeRelationAudit(c, "commission.relation.bind", req.InviteeId, qymodel.ResultOK,
		"手工绑定邀请关系: "+invitee+" 的邀请人设为 "+inviter+
			"(不补发上游 aff_quota 邀请奖励)| 事由: "+reason,
		before, relationSnapshot(ctx, req.InviteeId))
	respond(c, gin.H{
		"invitee_id": req.InviteeId,
		"inviter_id": req.InviterId,
		"bound":      true,
	})
}

// bindRelation 写主库的权威字段,再补扩展库快照。
//
// 顺序不能反:快照先写、主库失败,列表上会出现一条并不生效的关系;
// 反过来最坏只是快照缺一行,而快照本来就是懒建的。
func bindRelation(ctx context.Context, inviterId, inviteeId int, reason string) (inviterName, inviteeName string, err error) {
	if inviterId == inviteeId {
		return "", "", errRelSelfInvite
	}
	if model.DB == nil {
		return "", "", db.ErrNotReady
	}
	var users []model.User
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "email", "inviter_id", "created_at").
		Where("id IN ?", []int{inviterId, inviteeId}).Find(&users).Error; err != nil {
		return "", "", err
	}
	byId := map[int]model.User{}
	for _, u := range users {
		byId[u.Id] = u
	}
	inviter, okInviter := byId[inviterId]
	invitee, okInvitee := byId[inviteeId]
	if !okInviter || !okInvitee {
		return "", "", errRelUserMissing
	}
	if invitee.InviterId != 0 {
		return "", "", errRelAlreadyBd
	}
	cyclic, err := invitePathReaches(ctx, inviterId, inviteeId)
	if err != nil {
		return "", "", err
	}
	if cyclic {
		return "", "", errRelCycle
	}

	// CAS:只有当被邀请人**仍然**没有邀请人时才写。两个管理员同时给同一个人
	// 绑不同的上线,没有这个条件会后到的那次静默覆盖先到的那次。
	res := model.DB.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND inviter_id = ?", inviteeId, 0).
		Update("inviter_id", inviterId)
	if res.Error != nil {
		return "", "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", "", errRelRaced
	}
	// 刻意不动 users.aff_count / aff_quota / aff_history:那是上游注册时
	// inviteUser() 发的另一套邀请奖励,事后补发等于凭空造钱。

	upsertRelationSnapshot(ctx, inviterId, invitee)
	return displayName(inviter), displayName(invitee), nil
}

// invitePathReaches 判断从 fromId 顺着 inviter_id 往上走能不能走到 targetId。
//
// 这是防环的全部内容。走的是主库的权威字段,不是快照 —— 快照可能压根没有这一行。
func invitePathReaches(ctx context.Context, fromId, targetId int) (bool, error) {
	if model.DB == nil {
		return false, db.ErrNotReady
	}
	seen := map[int]bool{}
	cur := fromId
	for depth := 0; depth < maxInviteChainDepth && cur > 0; depth++ {
		if cur == targetId {
			return true, nil
		}
		if seen[cur] {
			// 历史数据里已经存在的环。它本身是坏数据,但绝不能让这个循环转不出去。
			return false, nil
		}
		seen[cur] = true
		var row struct{ InviterId int }
		err := model.DB.WithContext(ctx).Model(&model.User{}).
			Select("inviter_id").Where("id = ?", cur).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		cur = row.InviterId
	}
	return false, nil
}

// upsertRelationSnapshot 建/更新扩展库的关系快照。
//
// 手工绑定的 bound_at 取**现在**而不是下线的注册时间:关系确实是此刻成立的。
// blocked/risk_flags 一并清空 —— 一条刚被管理员显式建立的关系如果还带着上一次的
// 拉黑标记,表现是"绑好了但永远不产生佣金",而界面上什么都看不出来。
// 清空这件事会连同 before/after 快照一起进审计。
//
// 已知取舍:本表的主键是 invitee_id,一个下线在这里只能留一条关系。因此
// "X 曾经绑过 A、解绑后又绑给 B" 时,A→X 那一行会被这次 Save 覆盖,
// 「已解绑」列表里从此看不到它。**账本不受影响** —— qy_commission_accrual 里
// A→X 的计佣行原样都在,佣金流水页按 inviter_id 一样查得到,审计的 before 快照
// 也完整记下了被覆盖的那一份。要让快照表保留多段历史需要换主键,那是一次
// 数据迁移,不在本次改动的预算内。
func upsertRelationSnapshot(ctx context.Context, inviterId int, invitee model.User) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	now := common.GetTimestamp()
	row := InviteRelation{
		InviteeId:  invitee.Id,
		InviterId:  inviterId,
		MaskedName: truncate(MaskUsername(displayName(invitee)), 64),
		InviteeRef: inviteeRef(invitee.Id, refSalt()),
		BoundAt:    now,
		RiskFlags:  "",
		Blocked:    false,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := gdb.WithContext(ctx).Save(&row).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/commission: 写入邀请关系快照失败(主库已生效): " + err.Error())
	}
}

// ───────────────────────── 换绑 ─────────────────────────

// adminRebindRelation 把一个账号的邀请人**换成另一个人**。
//
// # 为什么它是独立的一条路由,而不是给 bind 加个 force 参数
//
// adminBindRelation 对"已经有上线"一律拒绝(errRelAlreadyBd),那条拒绝本身是
// 对的:绑定是"这个人此前没有上线"这个前提下的动作,让它顺手覆盖等于把一个
// 需要单独决定的事情做成了副作用。换绑是**另一个决定** —— 它要回答的问题是
// "把这个人从 A 名下挪到 B 名下",而 bind 回答不了这个问题,因为它连"原来是谁"
// 都不需要知道。分成两条路由之后,审计里也天然分成两种 action。
//
// # 已经产生的佣金怎么办
//
// **历史保留、不再产生新的** —— 与解绑逐字相同的语义,理由见本文件开头:
// qy_commission_accrual 是 append-only 的账本,A 名下那些计佣行已经变成了
// A 的可提现余额、甚至已经提现走了。换绑只改"从下一笔开始算给谁",一个字节
// 都不动账本。响应里的 kept_commission_quota 就是这句话的量化形式:
// 那是**原邀请人**从这条关系上已经挣到、并且会继续留在他名下的钱。
//
// 要把 A 名下的那笔钱要回来,必须单独走冲正(commission.clawback)。
//
// # 三道闸门与绑定完全一致
//
// 自邀请、防环(向上追溯 inviter_id 链,任意长度)、目标账号必须存在。
// 多一道:新旧邀请人相同直接拒。
func adminRebindRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		InviterId int    `json:"inviter_id"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.InviteeId <= 0 || req.InviterId <= 0 {
		badRequest(c, "qy_invalid_param", "必须同时指定新的邀请人与被邀请人")
		return
	}
	reason, ok := requireReason(c, req.Reason)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	before := relationSnapshot(ctx, req.InviteeId)
	out, err := rebindRelation(ctx, req.InviterId, req.InviteeId)
	if err != nil {
		writeRelationAudit(c, "commission.relation.rebind", req.InviteeId, qymodel.ResultFail,
			"换绑邀请关系失败: "+err.Error()+" | 事由: "+reason,
			before, relationSnapshot(ctx, req.InviteeId))
		respondRelationError(c, err)
		return
	}

	invalidateInviter(req.InviteeId)
	invalidateBlocked()
	writeRelationAudit(c, "commission.relation.rebind", req.InviteeId, qymodel.ResultOK,
		"换绑邀请关系: "+out.InviteeName+" 的邀请人由 "+itoa(out.OldInviterId)+
			" 改为 "+out.NewInviterName+"(不补发上游 aff_quota 邀请奖励);"+
			"原邀请人名下已产生的佣金全部保留(额度 "+itoa64(out.KeptQuota)+
			"),只是从此不再产生新的;要收回已发放部分须单独走冲正 | 事由: "+reason,
		before, relationSnapshot(ctx, req.InviteeId))
	respond(c, gin.H{
		"invitee_id": req.InviteeId,
		// old/new 都回显:前端的确认框与成功提示都要写清楚"从谁挪到了谁"。
		"old_inviter_id": out.OldInviterId,
		"inviter_id":     req.InviterId,
		"rebound":        true,
		// 原邀请人从这条关系上已经挣到、并且继续留在他名下的钱。
		"kept_commission_quota": out.KeptQuota,
	})
}

// rebindOutcome 是一次换绑的结局,只用于回显与审计,不参与任何资金动作。
type rebindOutcome struct {
	OldInviterId   int
	NewInviterName string
	InviteeName    string
	KeptQuota      int64
}

// rebindRelation 把权威字段从旧邀请人改到新邀请人,再更新扩展库快照。
//
// 顺序与 bindRelation 一致:先写主库(权威),再补快照。反过来的话主库失败时
// 列表上会出现一条并不生效的关系。
func rebindRelation(ctx context.Context, newInviterId, inviteeId int) (*rebindOutcome, error) {
	if newInviterId == inviteeId {
		return nil, errRelSelfInvite
	}
	if model.DB == nil {
		return nil, db.ErrNotReady
	}
	var users []model.User
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "email", "inviter_id", "created_at").
		Where("id IN ?", []int{newInviterId, inviteeId}).Find(&users).Error; err != nil {
		return nil, err
	}
	byId := map[int]model.User{}
	for _, u := range users {
		byId[u.Id] = u
	}
	inviter, okInviter := byId[newInviterId]
	invitee, okInvitee := byId[inviteeId]
	if !okInviter || !okInvitee {
		return nil, errRelUserMissing
	}
	oldInviterId := invitee.InviterId
	if oldInviterId == 0 {
		// 没有上线的账号该走「绑定」。回 errRelNotBound 而不是顺手绑上:
		// 两个动作的确认框写的话不一样(换绑要说明原邀请人的佣金怎么办),
		// 悄悄替运营换一个动作执行是最不该做的事。
		return nil, errRelNotBound
	}
	if oldInviterId == newInviterId {
		return nil, errRelSameInviter
	}
	// 防环走的是主库权威字段,与绑定同一个实现:新邀请人顺着 inviter_id 往上
	// 能走到被邀请人,就说明这一改会成环(A→B→C→A 同样挡得住)。
	cyclic, err := invitePathReaches(ctx, newInviterId, inviteeId)
	if err != nil {
		return nil, err
	}
	if cyclic {
		return nil, errRelCycle
	}

	// CAS 到观察到的那个旧邀请人上:并发下别人刚改过指向时必须失败,
	// 而不是把别人刚写进去的绑定覆盖掉。
	res := model.DB.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND inviter_id = ?", inviteeId, oldInviterId).
		Update("inviter_id", newInviterId)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, errRelRaced
	}
	// 刻意不动 users.aff_count / aff_quota / aff_history:那是上游注册时
	// inviteUser() 发的另一套邀请奖励,换绑不是重新注册,补发等于凭空造钱。

	// 先把旧关系保留下来的佣金算出来,再覆盖快照 —— 快照主键是 invitee_id,
	// upsertRelationSnapshot 会把 (旧邀请人, 这个下线) 那一行覆盖掉。账本不受
	// 影响(计佣行原样都在),但这个数字必须在覆盖前读,否则读到的是新关系的 0。
	kept := pairCommissionQuota(ctx, oldInviterId, inviteeId)
	upsertRelationSnapshot(ctx, newInviterId, invitee)

	return &rebindOutcome{
		OldInviterId:   oldInviterId,
		NewInviterName: displayName(inviter),
		InviteeName:    displayName(invitee),
		KeptQuota:      kept,
	}, nil
}

// pairCommissionQuota 返回 (邀请人, 被邀请人) 这一对历史上累计产生的佣金额度。
//
// 向下取整:回显与审计里的钱宁可略小于账本,也不能虚报。读失败返回 0 而不是
// 中断整个动作 —— 主库那一侧已经改完了,为了一个回显数字把它回滚才是真的坏。
func pairCommissionQuota(ctx context.Context, inviterId, inviteeId int) int64 {
	return pairCommissionQuotas(ctx, [][2]int{{inviterId, inviteeId}})[[2]int{inviterId, inviteeId}]
}

// pairCommissionQuotas 是 pairCommissionQuota 的批量形式:一次查询回答一整页。
//
// # 为什么必须是同一份实现
//
// 这个数字出现在两个地方,而它们必须是同一个数:
//
//	换绑/解绑的响应与审计    kept_commission_quota  —— 动作**之后**的回显
//	用户佣金总表的每一行      inviter_commission_quota —— 动作**之前**的确认框
//
// 确认框上写"这笔钱会留在原邀请人名下",点下去之后的成功提示又念一遍同一个数。
// 两处若各算各的,迟早会出现"确认框说 0、点完说 13517"这种当着运营的面自相
// 矛盾的场面 —— 那正是本轮修掉的缺陷(前端原本渲染的是 total_earned_quota,
// 一个语义完全不同的数)。所以单数版本委托给复数版本,而不是各写一遍 SQL。
//
// 一次 GROUP BY 查完整页,与页长无关,不是 N+1。按 (inviter, invitee) 双列
// 分组而不是只按 invitee:一个下线换过几次上线时,账本里同一个 invitee 下挂着
// 几个不同 inviter 的计佣行,只按 invitee 求和会把别人名下的钱也算进来。
//
// 读失败返回空表(调用方拿到 0)而不是中断:调用方一侧要么已经改完了主库,
// 要么只是在渲染一个列表,都不该为一个回显数字失败。
func pairCommissionQuotas(ctx context.Context, pairs [][2]int) map[[2]int]int64 {
	out := make(map[[2]int]int64, len(pairs))
	if len(pairs) == 0 {
		return out
	}
	gdb := db.Get()
	if gdb == nil {
		return out
	}
	wanted := make(map[[2]int]bool, len(pairs))
	invitees := make([]int, 0, len(pairs))
	seen := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		wanted[p] = true
		if seen[p[1]] {
			continue
		}
		seen[p[1]] = true
		invitees = append(invitees, p[1])
	}

	var rows []struct {
		InviterId int
		InviteeId int
		Gross     string
	}
	if err := gdb.WithContext(ctx).Model(&Accrual{}).
		Select("inviter_id, invitee_id, COALESCE(SUM(gross_amount), 0) AS gross").
		Where("invitee_id IN ? AND status <> ?", invitees, StatusVoided).
		Group("inviter_id, invitee_id").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out
	}
	for _, r := range rows {
		key := [2]int{r.InviterId, r.InviteeId}
		if !wanted[key] {
			continue
		}
		d, err := decimal.NewFromString(r.Gross)
		if err != nil {
			continue
		}
		out[key] = int64(common.QuotaFromDecimal(d.Floor()))
	}
	return out
}

// ───────────────────────── 解绑 ─────────────────────────

// adminUnbindRelation 解除一条邀请关系。
//
// 语义(与 UI 文案逐字一致):**已产生的佣金全部保留,只是从此不再产生新的。**
// 要收回已经发出去的钱必须单独走冲正。理由见本文件开头。
func adminUnbindRelation(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	var req struct {
		InviteeId int    `json:"invitee_id"`
		Reason    string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, "qy_invalid_param", "请求格式错误")
		return
	}
	if req.InviteeId <= 0 {
		badRequest(c, "qy_invalid_param", "必须指定被邀请人")
		return
	}
	reason, ok := requireReason(c, req.Reason)
	if !ok {
		return
	}

	ctx := c.Request.Context()
	before := relationSnapshot(ctx, req.InviteeId)
	keptQuota, inviterId, err := unbindRelation(ctx, req.InviteeId)
	if err != nil {
		writeRelationAudit(c, "commission.relation.unbind", req.InviteeId, qymodel.ResultFail,
			"解绑邀请关系失败: "+err.Error()+" | 事由: "+reason,
			before, relationSnapshot(ctx, req.InviteeId))
		respondRelationError(c, err)
		return
	}

	invalidateInviter(req.InviteeId)
	invalidateBlocked()
	// 审计正文里写死"保留了多少":解绑之后主库的 inviter_id 已经没了,
	// "这条关系一共给过谁多少钱"事后只能靠这一句和账本行自己回答。
	writeRelationAudit(c, "commission.relation.unbind", req.InviteeId, qymodel.ResultOK,
		"解绑邀请关系(邀请人 "+itoa(inviterId)+"):已产生的佣金全部保留,不再产生新的佣金;"+
			"要收回已发放部分须单独走冲正 | 事由: "+reason,
		before, relationSnapshot(ctx, req.InviteeId))
	respond(c, gin.H{
		"invitee_id":            req.InviteeId,
		"inviter_id":            inviterId,
		"unbound":               true,
		"kept_commission_quota": keptQuota,
	})
}

// unbindRelation 清主库的权威字段,再把快照标成已解绑。
//
// 返回值是"这条关系历史上一共产生了多少佣金(向下取整的额度)",
// 它只用于回显与审计,不参与任何资金动作。
func unbindRelation(ctx context.Context, inviteeId int) (keptQuota int64, inviterId int, err error) {
	if model.DB == nil {
		return 0, 0, db.ErrNotReady
	}
	var invitee model.User
	err = model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "email", "inviter_id", "created_at").
		Where("id = ?", inviteeId).Take(&invitee).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, errRelUserMissing
	}
	if err != nil {
		return 0, 0, err
	}
	if invitee.InviterId == 0 {
		// 重复解绑走到这里。回 400 而不是 200:回 200 等于告诉运营
		// "这次解绑成功了",而实际上什么都没发生 —— 两次解绑之间可能有人重新绑过。
		return 0, 0, errRelNotBound
	}
	inviterId = invitee.InviterId

	// CAS 到观察到的那个邀请人上:并发下别人刚改过指向时,这里必须失败而不是
	// 把新的绑定一起清掉。
	res := model.DB.WithContext(ctx).Model(&model.User{}).
		Where("id = ? AND inviter_id = ?", inviteeId, inviterId).
		Update("inviter_id", 0)
	if res.Error != nil {
		return 0, 0, res.Error
	}
	if res.RowsAffected == 0 {
		return 0, 0, errRelRaced
	}

	keptQuota = markRelationUnbound(ctx, inviterId, invitee)
	return keptQuota, inviterId, nil
}

// markRelationUnbound 把快照标成已解绑并返回该关系保留下来的历史佣金。
//
// 快照不存在时补建一行:那意味着这个下线从没产生过佣金(ensureRelation 从没跑到),
// 补一行是为了让"已解绑"列表完整 —— 否则这条关系解绑之后在两个库里都查不到,
// 而主库的 inviter_id 已经被清零了。
func markRelationUnbound(ctx context.Context, inviterId int, invitee model.User) int64 {
	gdb := db.Get()
	if gdb == nil {
		return 0
	}
	now := common.GetTimestamp()
	res := gdb.WithContext(ctx).Model(&InviteRelation{}).
		Where("invitee_id = ? AND inviter_id = ?", invitee.Id, inviterId).
		Updates(map[string]any{"unbound_at": now, "updated_at": now})
	if res.Error != nil {
		db.MarkFailure(res.Error)
	}
	if res.Error == nil && res.RowsAffected == 0 {
		row := InviteRelation{
			InviteeId:  invitee.Id,
			InviterId:  inviterId,
			MaskedName: truncate(MaskUsername(displayName(invitee)), 64),
			InviteeRef: inviteeRef(invitee.Id, refSalt()),
			BoundAt:    invitee.CreatedAt,
			UnboundAt:  now,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := gdb.WithContext(ctx).Save(&row).Error; err != nil {
			db.MarkFailure(err)
		}
	}

	return pairCommissionQuota(ctx, inviterId, invitee.Id)
}

// ───────────────────────── 共用件 ─────────────────────────

// requireReason 校验事由并返回去空白后的值。
//
// 下限 4 个字符,与 adminSetWithdrawn、withdraw 模块看 PII 明文同口径:
// 改动资金归属的动作没有事由,事后无法与误操作区分。
func requireReason(c *gin.Context, raw string) (string, bool) {
	reason := strings.TrimSpace(raw)
	if len([]rune(reason)) < 4 {
		badRequest(c, "qy_reason_required", "必须填写事由(至少 4 个字符)")
		return "", false
	}
	return reason, true
}

func respondRelationError(c *gin.Context, err error) {
	if code, ok := relationErrCodes[err]; ok {
		status := http.StatusBadRequest
		if errors.Is(err, errRelRaced) {
			status = http.StatusConflict
		}
		respondFail(c, status, code, err.Error())
		return
	}
	db.MarkFailure(err)
	internalError(c, err)
}

// relationSnapshot 拼一条关系在此刻的完整形状,给审计的 before/after 用。
//
// 它跨两个库读:主库的 inviter_id 是权威,扩展库的快照带着 blocked/解绑时刻。
// 读不到就退化成只有 id 的那一份 —— 读不到本身也是一种回答,
// 不该因此把整条审计丢掉。
//
// 主库说"没有邀请人"时回落到快照里的 inviter_id,这一步不能省:解绑之后的
// after 快照正是这种情形,不回落的话累计佣金会算成 0(聚合按 inviter/invitee
// 这一对走),而这条审计要回答的恰恰是"解绑之后保留了多少"。
func relationSnapshot(ctx context.Context, inviteeId int) string {
	view := relationView{InviteeId: inviteeId, TotalCommission: decimal.Zero.String()}
	if model.DB != nil {
		var u model.User
		if err := model.DB.WithContext(ctx).Model(&model.User{}).
			Select("id", "username", "inviter_id", "created_at").
			Where("id = ?", inviteeId).Take(&u).Error; err == nil {
			view.InviteeUsername = u.Username
			view.InviteeResolved = true
			view.InviteeCreatedAt = u.CreatedAt
			view.InviterId = u.InviterId
		}
	}
	if view.InviterId == 0 {
		if gdb := db.Get(); gdb != nil {
			var rels []InviteRelation
			if err := gdb.WithContext(ctx).Where("invitee_id = ?", inviteeId).
				Limit(1).Find(&rels).Error; err == nil && len(rels) > 0 {
				view.InviterId = rels[0].InviterId
			}
		}
	}
	views := []relationView{view}
	if err := hydrateRelationViews(ctx, views); err != nil {
		common.SysError("qianye/commission: 拼装邀请关系审计快照失败: " + err.Error())
	}
	snap, _ := common.Marshal(views[0])
	return string(snap)
}

// writeRelationAudit 落一条关系变更审计,成功与失败共用。
func writeRelationAudit(c *gin.Context, action string, inviteeId int, result, reason, before, after string) {
	audit.Write(c, audit.Entry{
		Category:     qymodel.AuditCategoryCommission,
		Action:       action,
		ActorType:    qymodel.ActorAdmin,
		ActorUserId:  c.GetInt("id"),
		ActorName:    c.GetString("username"),
		TargetUserId: inviteeId,
		Result:       result,
		Reason:       reason,
		BeforeSnap:   before,
		AfterSnap:    after,
	})
}

// displayName 取用户的可展示名,用户名为空时回落邮箱(与 resolveInviter 同口径)。
func displayName(u model.User) string {
	if u.Username != "" {
		return u.Username
	}
	return u.Email
}

// registerRelationRoutes 挂载 AFF 关系列表与绑定/解绑。
//
// 列表挂搜索限流(它跨两个库做三次查询),两个写接口挂关键操作限流 ——
// 它们改的是"未来的佣金归谁",与直接改钱同一档。
func registerRelationRoutes(g *gin.RouterGroup, crit gin.HandlerFunc) {
	g.GET("/commission/relations", middleware.SearchRateLimit(), adminListRelations)
	g.POST("/commission/relations/bind", crit, adminBindRelation)
	g.POST("/commission/relations/rebind", crit, adminRebindRelation)
	g.POST("/commission/relations/unbind", crit, adminUnbindRelation)
}
