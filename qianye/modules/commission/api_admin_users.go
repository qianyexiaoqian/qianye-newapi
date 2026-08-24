package commission

// api_admin_users.go —— **以用户为中心**的佣金总表。
//
// # 为什么要有这张表,而不是让运营在原来三张之间跳
//
// 模块此前有三张管理端列表,每一张的"一行"都不是一个人:
//
//	佣金流水(adminListRecords)     一行 = 一笔计佣
//	AFF 关系(adminListRelations)   一行 = 一条邀请关系
//	佣金余额(adminListBalances)     一行 = 一行余额账
//
// 项目方的原话是「我需要的是新增一个用户佣金列表,我可以查看/编辑用户的佣金,
// 以及查看拉了多少用户,以及编辑/移除/添加这个用户的佣金绑定关系」。这句话里
// 的每一个主语都是**这个人**,而上面三张表没有一张能回答"关于这个人的全部佣金
// 事务":要知道老王拉了几个人得开关系页按 inviter_id 数行,要知道他有多少钱得
// 开余额页搜他的 id,要知道他上线是谁得回关系页再搜一次 invitee_id。
//
// 本文件给出的是 **一行 = 一个人**。行的形状刻意是 balanceView 的**超集**
// (内嵌,不是抄一遍字段):余额那五列与恒等式派生的两列在两张表上必须是同一个
// 数、同一套算法,而手工增减佣金的弹窗因此不需要任何适配层就能接收本表的行。
//
// # 覆盖范围
//
// 有上线的人 ∪ 有下线的人 ∪ 有过佣金账的人。三者的并集就是"与返佣有关的全部
// 账号":少了第一个会漏掉"被谁拉进来的",少了第二个会漏掉"拉了人但一分钱还没
// 产生"(刚起量的推广号,恰恰是运营最想提前看到的),少了第三个会漏掉"关系已经
// 解绑、但历史佣金还挂在账上"。全站账号里绝大多数与返佣无关,把他们一起列出来
// 只会让运营翻不到要找的人。
//
// # 跨两个库的聚合,以及为什么它不是 N+1
//
// 难点在于数据分居两个库:人与邀请关系在**主库**(users.inviter_id 是权威),
// 钱在**扩展库**(qy_commission_balance / qy_commission_accrual)。没有任何一条
// SQL 能同时读到两边,而"每行去扩展库查一次余额"就是教科书式的 N+1 ——
// 一页 100 行 = 101 次查询,而且每一次都跨库。
//
// 这里走的是**有界的内存 join**,查询次数恒定 6 次、与页长和总行数都无关:
//
//	① 主库  SELECT inviter_id, COUNT(*) FROM users WHERE inviter_id>0 GROUP BY inviter_id
//	        —— 一次拿到全部"谁拉了几个人";它同时也是"有下线的人"这个集合;
//	② 扩展库 SELECT * FROM qy_commission_balance   —— 全部余额行(= 有过佣金账的人);
//	③ 扩展库 SELECT invitee_id, inviter_id FROM qy_invite_relation WHERE blocked = 1
//	        —— 被拉黑的关系;按 invitee 用于"他自己被拉黑没有",按 inviter 数出
//	        "他名下有几条被拉黑";
//	④ 主库  SELECT ... FROM users WHERE (inviter_id>0 OR id IN (①∪②)) [+ 关键词]
//	        —— 候选行本体;
//	⑤ 主库  SELECT id, username, display_name, email FROM users WHERE id IN (本页的上线 id)
//	        —— 只给**本页**补上线名;
//	⑥ 扩展库 SELECT inviter_id, invitee_id, SUM(gross_amount) ... GROUP BY 两列
//	        —— 只给**本页**补"当前上线从这个人身上挣到了多少"(管理关系确认框
//	        上那个金额,与换绑/解绑响应里的 kept_commission_quota 同一份实现)。
//
// 筛选、排序、分页、合计在 ④ 的结果上于内存中完成。这不是偷懒,是唯一可行的
// 做法:项目方要的排序里有「谁拉的人最多」与「可提现最多」,前者的数在主库、
// 后者的数在扩展库,任何一条 SQL 都排不动它们两个。在数据库里排 = 只能排一半,
// 而"只排了一半的排序"比不提供排序更糟 —— 它看起来是对的。
//
// 代价是 ④ 会把候选集整个读进来,所以候选集必须有界:maxCandidateUsers 一到就
// 明确报错。**不截断** —— 截断出来的是一张看起来正常、实际少了人的表,而这一页
// 上有改钱和改关系的按钮。真到了那个规模,该换的是分页锚点(把余额行做成
// 带用户名的物化视图再从扩展库出),而不是在这里加缓存:缓存只会让"刚改完关系
// 刷新看不到"变成新的投诉。
//
// # 这张表不碰钱
//
// 本文件只有一条 GET。所有写动作(手工增减、绑定/换绑/解绑、拉黑)都复用既有
// 接口,一行代码都不重复 —— 手工增减的幂等键、下界校验、三条恒等式、审计,
// 全部只有 api_admin_adjust.go 那一份实现。下钻(逐笔计佣、结算、提现、下线列表)
// 同理复用 /commission/records 与 /commission/relations。

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
)

// maxCandidateUsers 是"与返佣有关的账号"这个候选集的规模上界。
//
// 它同时是安全阀和体检项:超过说明这张表的分页锚点该换实现了,而在那之前
// 宁可明确报错也不能截断。20000 行 × 每行约 200 字节 ≈ 4MB,这条路由挂着
// 搜索限流,是可以承受的;再往上就不是了。
const maxCandidateUsers = 20000

// userCommissionView 是一行"一个人的全部佣金事务"。
//
// 内嵌 balanceView 而不是把那十几个字段抄一遍:同一个数在两张表上必须是同一个
// 名字、同一套算法。派生可提现与账本漂移尤其如此 —— 那条恒等式在后端已经被
// 结算/冲正/提现三条路径各实现了一遍,再抄第四遍就是在等它漂移。
type userCommissionView struct {
	balanceView

	// DisplayName 是主库的展示名,可能为空;前端为空时回落 Username。
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	// UserGroup 不叫 group:那是 SQL 保留字,也是前端 `QyCommissionBalance`
	// 超集里已经定好的名字。
	UserGroup string `json:"user_group"`

	// ── 上线(他自己的邀请人)──
	// InviterId 取自主库 users.inviter_id,也就是权威字段。0 = 没有上线,
	// 它是"这一行有没有邀请关系"唯一的判据,前端三个关系动作的可用性全看它。
	InviterId       int    `json:"inviter_id"`
	InviterUsername string `json:"inviter_username"`
	// InviterResolved 为假表示主库里查不到这个上线 id(账号已删,或这次读失败)。
	InviterResolved bool `json:"inviter_resolved"`
	// InviterBlocked 说的是"**他作为下线**的这条关系被拉黑了",即他的消费不再
	// 给上线计佣。它不是"这个人被封号了"。
	InviterBlocked bool `json:"inviter_blocked"`
	// InviterCommissionQuota 是**当前这个上线从这个人身上**已经挣到的佣金额度,
	// 也就是"把这条关系换掉或解掉之后,会留在原邀请人名下的那笔钱"。
	//
	// 它与 TotalEarnedQuota 是两个方向完全相反的数,绝不能互相顶替:前者是
	// 别人从**他**身上挣的,后者是**他**从自己所有下线身上挣的。管理关系的确认框
	// 要回答的是"改了之后那笔钱怎么办",答案只能是前者。渲染成后者的后果实测过:
	// 397 号的 total_earned_quota 是 0(他自己没有下线),而他上线 391 从他身上
	// 已经挣到 13517 —— 确认框会写"保留 0",点完的成功提示写"保留 13517"。
	//
	// 与换绑/解绑响应里的 kept_commission_quota 共用同一份实现
	// (pairCommissionQuotas),因此两处不可能算出不同的数。
	// 没有上线(InviterId = 0)时恒为 0:那时没有任何既有关系可言。
	InviterCommissionQuota int64 `json:"inviter_commission_quota"`

	// ── 下线 ──
	// BlockedInviteeCount 是他名下已被拉黑、不再产生新佣金的下线条数。
	BlockedInviteeCount int `json:"blocked_invitee_count"`

	// HasBalanceRow 为假表示扩展库里还没有这个人的余额行(他一分佣金都没产生过)。
	// 不是异常;此时内嵌的那几个数字全是 0,而"0"与"没有这一行"含义不同。
	HasBalanceRow bool `json:"has_balance_row"`
}

// userCommissionSorters 是排序白名单。
//
// 全部作用在**内存里已经 join 好的行**上,所以主库列与扩展库列可以混排 ——
// 「谁拉的人最多」(主库)与「可提现最多」(扩展库)在同一个下拉里。
//
// 每一个都以 user_id 做最终的次级键:比较值相同的行必须有稳定顺序,
// 否则翻页会漏行也会重复行,而这一页正是"逐个核对谁挣了多少"的地方。
var userCommissionSorters = map[string]func(a, b userCommissionView) bool{
	"available": func(a, b userCommissionView) bool { return a.AvailableQuota > b.AvailableQuota },
	"earned":    func(a, b userCommissionView) bool { return a.TotalEarnedQuota > b.TotalEarnedQuota },
	"updated":   func(a, b userCommissionView) bool { return a.UpdatedAt > b.UpdatedAt },
	"user":      func(a, b userCommissionView) bool { return false },
	"invitees":  func(a, b userCommissionView) bool { return a.InviteeCount > b.InviteeCount },
}

const defaultUserCommissionSort = "available"

// adminListUserCommissions 分页列出"一行一个人"的佣金总表。
func adminListUserCommissions(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCommission) {
		return
	}
	if model.DB == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	if db.Get() == nil {
		internalError(c, db.ErrNotReady)
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)

	rows, err := buildUserCommissionRows(ctx, c)
	if err != nil {
		internalError(c, err)
		return
	}

	// 合计跟着当前筛选走。运营问的是"命中这批条件的人一共挂着多少可提现、
	// 已经提走了多少、一共拉了多少人",逐页心算是不可行的。
	totals := gin.H{
		"user_count": len(rows), "available_quota": int64(0),
		"withdrawn_quota": int64(0), "invitee_count": 0,
	}
	var availableSum, withdrawnSum int64
	inviteeSum := 0
	for _, r := range rows {
		availableSum += r.AvailableQuota
		withdrawnSum += r.WithdrawnQuota
		inviteeSum += r.InviteeCount
	}
	totals["available_quota"] = availableSum
	totals["withdrawn_quota"] = withdrawnSum
	totals["invitee_count"] = inviteeSum

	less, ok := userCommissionSorters[c.Query("sort")]
	if !ok {
		less = userCommissionSorters[defaultUserCommissionSort]
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if less(rows[i], rows[j]) {
			return true
		}
		if less(rows[j], rows[i]) {
			return false
		}
		return rows[i].UserId < rows[j].UserId
	})

	total := int64(len(rows))
	pageRows := httpq.Slice(rows, page, size)
	if err := attachInviterNames(ctx, pageRows); err != nil {
		internalError(c, err)
		return
	}
	attachInviterCommission(ctx, pageRows)
	respond(c, gin.H{
		"items": pageRows, "total": total, "p": page, "page_size": size,
		"totals": totals,
	})
}

// buildUserCommissionRows 把两个库的数据 join 成"一行一个人",并应用搜索与筛选。
//
// 返回的是**全部命中行**(排序与分页在调用方做):跨库排序只有在内存里才做得到,
// 见本文件开头的说明。
func buildUserCommissionRows(ctx context.Context, c *gin.Context) ([]userCommissionView, error) {
	gdb := db.Get()

	// ① 谁拉了几个人。权威口径:主库 users.inviter_id 指着他的账号数。
	// 刻意不数扩展库的 qy_invite_relation:那张表是懒建的,数出来会少 97%。
	var counts []struct {
		InviterId int
		Cnt       int
	}
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("inviter_id, COUNT(*) AS cnt").Where("inviter_id > ?", 0).
		Group("inviter_id").Scan(&counts).Error; err != nil {
		return nil, err
	}
	downlines := make(map[int]int, len(counts))
	for _, r := range counts {
		downlines[r.InviterId] = r.Cnt
	}

	// ② 全部余额行。
	var balances []Balance
	if err := gdb.WithContext(ctx).Find(&balances).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	byUser := make(map[int]balanceView, len(balances))
	for _, b := range balances {
		byUser[b.UserId] = newBalanceView(b)
	}

	// ③ 被拉黑的关系。两个方向都要:按 invitee 回答"他自己被拉黑没有",
	// 按 inviter 回答"他名下有几条被拉黑"。
	var blockedRels []InviteRelation
	if err := gdb.WithContext(ctx).Select("invitee_id", "inviter_id").
		Where("blocked = ?", true).Find(&blockedRels).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	blockedInvitee := make(map[int]bool, len(blockedRels))
	blockedByInviter := make(map[int]int, len(blockedRels))
	for _, r := range blockedRels {
		blockedInvitee[r.InviteeId] = true
		blockedByInviter[r.InviterId]++
	}

	// ④ 候选行。
	candidates := make(map[int]bool, len(downlines)+len(balances))
	for id := range downlines {
		candidates[id] = true
	}
	for _, b := range balances {
		candidates[b.UserId] = true
	}
	ids := make([]int, 0, len(candidates))
	for id := range candidates {
		ids = append(ids, id)
	}

	q := model.DB.WithContext(ctx).Model(&model.User{})
	if len(ids) == 0 {
		q = q.Where("inviter_id > ?", 0)
	} else {
		q = q.Where("inviter_id > ? OR id IN ?", 0, ids)
	}
	// 按 id / 用户名 / 邮箱找人,一个输入框全包。纯数字优先当 id 精确匹配,
	// 同时仍然 OR 上用户名/邮箱的前缀匹配 —— 用户名叫 "123" 的账号确实存在,
	// 只按 id 找会让运营以为查无此人。
	//
	// id 走 httpq.Int 而不是 strconv.Atoi:上界是解析的一部分。运营粘进来一串
	// 超长数字时 strconv.Atoi 会给出一个 int64 量级的 id 送进 WHERE,
	// 而 httpq.Int 直接判定"这不是一个 id",回落到按名字搜。
	//
	// 检索走 httpq.SearchLike:前缀匹配、转义 % 与 _、并且**折叠大小写**。
	// 折叠是必须的 —— PostgreSQL 的 LIKE 大小写敏感,MySQL(ci 排序规则)与
	// SQLite 不敏感,不折叠的话同一个关键词在 PG 部署上搜不到人而两边都返回 200,
	// 而这一页上有余额调整、佣金冲正、绑定/解绑上下线的按钮。理由全文见该函数。
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		expr, pattern := httpq.SearchLike(kw, httpq.MatchPrefix, "username", "email")
		if id := httpq.Int(c, "keyword", 0); id > 0 {
			q = q.Where("id = ? OR "+expr, id, pattern, pattern)
		} else {
			q = q.Where(expr, pattern, pattern)
		}
	}

	var count int64
	if err := q.Count(&count).Error; err != nil {
		return nil, err
	}
	if count > maxCandidateUsers {
		// 明确报错而不是截断:截断出来的是一张缺了人的表,运营看不出来,
		// 而这一页上有改钱和改关系的按钮。
		return nil, errors.New("commission: 与返佣有关的账号已达 " +
			strconv.FormatInt(count, 10) + " 个,超过本表一次能安全 join 的上界 " +
			strconv.Itoa(maxCandidateUsers) + ";请先用搜索缩小范围。" +
			"要长期支持这个规模,需要把分页锚点从主库 users 换成扩展库侧的物化视图")
	}

	var users []model.User
	if err := q.Select("id", "username", "display_name", "email", "group",
		"status", "inviter_id", "created_at").Find(&users).Error; err != nil {
		return nil, err
	}

	hasInvitees := c.Query("has_invitees") == "true"
	hasBalance := c.Query("has_balance") == "true"
	onlyBlocked := c.Query("blocked") == "true"

	out := make([]userCommissionView, 0, len(users))
	for _, u := range users {
		bv, hasRow := byUser[u.Id]
		if !hasRow {
			// 没有余额行时给一个全零的形状,而不是让前端去分辨 null。
			// 走 newBalanceView 而不是手搭 balanceView{}:那几个字符串字段
			// (unsettled_amount / available_fiat)手搭出来是**空串**而不是 "0",
			// 于是"有余额"筛选里 `unsettled_amount == "0"` 恒不成立,一个佣金账
			// 都没有的人会被当成"账上有钱"筛出来。
			bv = newBalanceView(Balance{UserId: u.Id})
		}
		// 用户名与解析标记一律以**这一次**主库读到的为准:balanceView 是从
		// 扩展库的余额行造出来的,它那两个字段本来就是等着被主库补上的。
		bv.Username = u.Username
		bv.UserResolved = true
		// 下线数一律现算。余额行上曾经有一个 invitee_count 列,注释声称"计佣路径
		// 顺手维护",而它**从来没有任何写入方** —— 全库每一个真正有下线的推广人
		// 在那一列上都是 0。该列已从模型里摘掉,这里保留覆盖是因为 newBalanceView
		// 造出来的是零值。
		bv.InviteeCount = downlines[u.Id]

		row := userCommissionView{
			balanceView:         bv,
			DisplayName:         u.DisplayName,
			Email:               u.Email,
			UserGroup:           u.Group,
			InviterId:           u.InviterId,
			InviterBlocked:      blockedInvitee[u.Id],
			BlockedInviteeCount: blockedByInviter[u.Id],
			HasBalanceRow:       hasRow,
		}

		// 三个筛选互相独立、可以同时成立(「有下线且被拉黑」正是运营最想一眼
		// 看到的那一批),所以是三个 AND 而不是一个下拉。
		if hasInvitees && row.InviteeCount == 0 {
			continue
		}
		// "有余额"刻意不含已提现:那笔钱已经走了。按"有余额"筛出一个实际可动
		// 余额为 0 的人,运营会对着他研究半天。未结算余数算在内 —— 它是欠账或
		// 待发的零头,两者都需要人看一眼。
		if hasBalance && row.AvailableQuota == 0 && row.FrozenQuota == 0 &&
			row.UnsettledAmount == "0" {
			continue
		}
		// "被拉黑"筛的是**这一行自己**那条上线关系被拉黑,与列上的
		// `inviter_blocked` 逐字对应。刻意不把"名下有下线被拉黑"也算进来:
		// 那是另一个问题(「谁手下有自刷」),混进同一个开关会让筛出来的一批人
		// 里一半是嫌疑人、一半是嫌疑人的上线,而运营分不出哪个是哪个。
		if onlyBlocked && !row.InviterBlocked {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// attachInviterNames 只给**本页**补上线用户名。
//
// 放在分页之后是刻意的:候选集可能有上万行,给每一行都解析上线名等于把主库
// 再读一遍,而列表上看得见的只有这一页。
func attachInviterNames(ctx context.Context, rows []userCommissionView) error {
	ids := make([]int, 0, len(rows))
	seen := map[int]bool{}
	for _, r := range rows {
		if r.InviterId > 0 && !seen[r.InviterId] {
			seen[r.InviterId] = true
			ids = append(ids, r.InviterId)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var users []model.User
	if err := model.DB.WithContext(ctx).Model(&model.User{}).
		Select("id", "username", "email").Where("id IN ?", ids).
		Find(&users).Error; err != nil {
		return err
	}
	names := make(map[int]string, len(users))
	for _, u := range users {
		names[u.Id] = displayName(u)
	}
	for i := range rows {
		if name, ok := names[rows[i].InviterId]; ok {
			rows[i].InviterUsername = name
			rows[i].InviterResolved = true
		}
	}
	return nil
}

// attachInviterCommission 给**本页**补上"当前上线从这个人身上挣到了多少"。
//
// 这是管理关系那个确认框唯一该念的金额:改掉/解掉这条关系之后,留在原邀请人
// 名下的就是这笔钱。它跟 total_earned_quota 是反方向的两个数,见
// userCommissionView.InviterCommissionQuota 上的说明。
//
// 与 attachInviterNames 一样放在分页之后,而且刻意用同一份 pairCommissionQuotas
// —— 换绑/解绑响应里的 kept_commission_quota 走的就是它,两处永远算得出同一个数。
//
// 读扩展库失败不让整张表 500:这一列是确认框上的提示,而这一页还要用来看余额、
// 看下线数、改钱。缺了它前端只会少显示一个金额,前端在没有上线时本来也不显示。
func attachInviterCommission(ctx context.Context, rows []userCommissionView) {
	pairs := make([][2]int, 0, len(rows))
	for _, r := range rows {
		if r.InviterId > 0 {
			pairs = append(pairs, [2]int{r.InviterId, r.UserId})
		}
	}
	if len(pairs) == 0 {
		return
	}
	quotas := pairCommissionQuotas(ctx, pairs)
	for i := range rows {
		rows[i].InviterCommissionQuota = quotas[[2]int{rows[i].InviterId, rows[i].UserId}]
	}
}

// registerUserCommissionRoutes 挂载以用户为中心的佣金总表。
//
// 只读,挂搜索限流(它一次要跨两个库做五次查询并在内存里 join)。写动作与全部
// 下钻查询都不在本文件里:它们复用 relations/* 、balances/adjust 、records 与
// withdrawals,那些路由各自已经挂了该挂的限流。
func registerUserCommissionRoutes(g *gin.RouterGroup) {
	g.GET("/commission/users", middleware.SearchRateLimit(), adminListUserCommissions)
}
