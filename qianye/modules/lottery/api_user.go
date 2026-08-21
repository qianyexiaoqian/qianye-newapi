package lottery

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_user.go —— 用户侧接口。
//
// 全部用**显式 DTO**,绝不整行返回 model:Activity 里有 rules_text 之外的
// 运行期状态,Entry 里有资格快照与 IP 哈希。整行返回是本仓反复出现的泄漏形状,
// 而且下一个给表加列的人不会想起来这里。
//
// 接口只挂 UserAuth,不挂 TokenAuth:API Key 天然是给机器用的、可批量分发,
// 挂上去等于允许脚本化刷参与。

type activityBrief struct {
	ActNo   string `json:"act_no"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	Title   string `json:"title"`

	// CoverUrl / CoverRef 是卡片背景图的两种来源,至多一个非空。
	//
	// 下发**原始的两列**而不是一个拼好的 src:两者的信任级别不同,前端必须能
	// 分开对待 —— 外链要挂 referrerPolicy="no-referrer"(不把访客的来源地址
	// 送给管理员随手填的第三方主机),站内引用不需要。合成一个字符串之后
	// 这个区分就只能靠"它是不是以 / 开头"来猜。
	CoverUrl string `json:"cover_url"`
	CoverRef string `json:"cover_ref"`

	StakeQuota int64 `json:"stake_quota"`
	OpenAt     int64 `json:"open_at"`
	CloseAt    int64 `json:"close_at"`
	DrawAt     int64 `json:"draw_at"`

	ActiveCount int   `json:"active_count"`
	PoolQuota   int64 `json:"pool_quota"`
	// PrizeTotalQuota 让大厅卡片能显示"奖池"。抽奖没有奖池概念(平台出奖品),
	// 这里给的是奖品总额度 —— 对用户而言那才是"能赢多少"。
	PrizeTotalQuota int64 `json:"prize_total_quota"`
	// MyEntryCount 让大厅卡片直接回答"我参加过没有"。不给的话用户只能逐个点进
	// 详情页看,而"我到底报没报上"正是参与之后最想立刻确认的一件事。
	MyEntryCount int `json:"my_entry_count"`

	// ── 双色球(draw_mode=ball)的卡面 ──
	//
	// 大厅必须能把双色球与普通抽奖分开,而"分开"不是换个徽章就够了:同一张卡上
	// 三个数的**含义整个变了**。prize_total_quota 对双色球是错的 —— 浮动奖档的
	// amount_quota 恒为 0,Σ(amount×count) 会把一个滚了几期的大奖池显示成一个
	// 很小的数,而那正是用户用来决定要不要参与的那个数。所以这里另给
	// pool_open_quota(本期真正可派发的池子),并把号池四元组一起下发:
	// 有了它前端就能自己按组合数把头奖概率算出来,不必点进详情页。
	//
	// **不下发任何概率数字**:那是组合数的结果,后端给一个数就等于管理员在这件
	// 事上有了撒谎的接口,而"概率不需要相信平台"是双色球唯一但决定性的优势。
	DrawMode      string `json:"draw_mode"`
	SeriesNo      string `json:"series_no"`
	IssueNo       int    `json:"issue_no"`
	PoolOpenQuota int64  `json:"pool_open_quota"`
	BallRedPool   int    `json:"ball_red_pool"`
	BallRedPick   int    `json:"ball_red_pick"`
	BallBluePool  int    `json:"ball_blue_pool"`
	BallBluePick  int    `json:"ball_blue_pick"`
	BallResult    string `json:"ball_result"`
}

type specItem struct {
	// 抽奖:tier/name/amount_quota/count;竞猜:opt_no/label/is_catch_all。
	// 用一个联合结构而不是两个数组,是为了让前端与验证脚本只处理一种形状。
	Tier        int    `json:"tier,omitempty"`
	Name        string `json:"name,omitempty"`
	AmountQuota int64  `json:"amount_quota,omitempty"`
	Count       int    `json:"count,omitempty"`

	// PrizeType / WinPpm / TextDesc 是概率制与文本奖要展示给用户的三件事:
	// 这一档发什么、中的概率是多少、拿到之后怎么兑现。
	//
	// **参与之前就必须看得到**,而不是开奖之后才被告知规则 —— 概率是这套设计
	// 唯一向用户承诺的数字,把它藏起来等于没有承诺。
	PrizeType string `json:"prize_type,omitempty"`
	WinPpm    int    `json:"win_ppm,omitempty"`
	TextDesc  string `json:"text_desc,omitempty"`

	OptNo      int    `json:"opt_no,omitempty"`
	Label      string `json:"label,omitempty"`
	IsCatchAll bool   `json:"is_catch_all,omitempty"`
	BetQuota   int64  `json:"bet_quota,omitempty"`
	BetCount   int    `json:"bet_count,omitempty"`
	IsWinner   bool   `json:"is_winner,omitempty"`

	// 双色球奖级:命中门槛与占池比例。前端按红蓝池大小与这两个门槛用组合数
	// 自己算中奖概率 —— 后端**不下发**任何概率数字。
	RedMatch     int `json:"red_match,omitempty"`
	BlueMatch    int `json:"blue_match,omitempty"`
	PoolShareBps int `json:"pool_share_bps,omitempty"`
}

type activityDetail struct {
	ActNo   string `json:"act_no"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	Title   string `json:"title"`
	Intro   string `json:"intro"`
	// 详情页也给封面:从大厅点进来时,头图不接上会让人怀疑自己点错了活动。
	CoverUrl string `json:"cover_url"`
	CoverRef string `json:"cover_ref"`

	StakeQuota     int64 `json:"stake_quota"`
	BetMinQuota    int64 `json:"bet_min_quota"`
	BetMaxQuota    int64 `json:"bet_max_quota"`
	OpenAt         int64 `json:"open_at"`
	CloseAt        int64 `json:"close_at"`
	DrawAt         int64 `json:"draw_at"`
	SettleDeadline int64 `json:"settle_deadline"`

	// CommitHash / RulesText / Algo 让用户在**参与之前**就能拿到承诺,
	// 而不是等到开奖之后才被告知规则是什么。
	CommitHash string `json:"commit_hash"`
	RulesText  string `json:"rules_text"`
	Algo       string `json:"algo"`
	// NoWinnerPolicy 必须公示:事后无论怎么处理"全部猜错",
	// 没有事前公示都会被指控临时改规则。
	NoWinnerPolicy string `json:"no_winner_policy"`

	AllowMultiWin    bool  `json:"allow_multi_win"`
	FeeBps           int   `json:"fee_bps"`
	MinEntriesToHold int   `json:"min_entries_to_hold"`
	ActiveCount      int   `json:"active_count"`
	PoolQuota        int64 `json:"pool_quota"`

	// ── 定档方式与双色球期次 ──
	//
	// 号池下发的是**四个数**,不是各档中奖概率:概率是组合数的结果,前端自己算
	// 就行。后端下发一个数字等于把双色球唯一的优势(概率不需要相信平台)丢掉。
	DrawMode string `json:"draw_mode"`
	SeriesNo string `json:"series_no"`
	IssueNo  int    `json:"issue_no"`
	// PoolOpenQuota 是本期此刻可派发的池子(开局基数 + 已投注的入池部分),
	// 会随投注实时变大。
	PoolOpenQuota  int64  `json:"pool_open_quota"`
	PoolSeedQuota  int64  `json:"pool_seed_quota"`
	PoolCarryQuota int64  `json:"pool_carry_quota"`
	PoolShareBps   int    `json:"pool_share_bps"`
	BallRedPool    int    `json:"ball_red_pool"`
	BallRedPick    int    `json:"ball_red_pick"`
	BallBluePool   int    `json:"ball_blue_pool"`
	BallBluePick   int    `json:"ball_blue_pick"`
	BallResult     string `json:"ball_result"`

	// 频次与去重闸门。它们同样进了 rules_hash,这里单独下发只是为了让详情页
	// 不必解析 rules_text 就能把"每人几次、要不要等、同 IP 算不算一个人"
	// 摆在参与按钮旁边 —— 尤其是 IP 去重的代价必须在按下之前被看到。
	MaxEntriesPerUser int  `json:"max_entries_per_user"`
	CooldownSeconds   int  `json:"cooldown_seconds"`
	DedupIp           bool `json:"dedup_ip"`

	// 竞猜结果与它的公开依据。结果一经写入不可修改,依据对全部参与者可见。
	WinOptNo       int    `json:"win_opt_no"`
	ResultEvidence string `json:"result_evidence"`

	Spec         []specItem `json:"spec"`
	MyEntryCount int        `json:"my_entry_count"`
	// PayPasswordRequired 让前端知道要不要渲染支付密码输入框。判定用的是活动的
	// 基准参与费,即**这一场最小的一笔扣款**;竞猜自选更大的金额时,真正的闸门
	// 在 handleCreateEntry 里按本次金额重算(见那里的说明)。
	PayPasswordRequired bool `json:"pay_password_required"`
	// PayPasswordThresholdQuota 是阈值本身,让前端能在投注额输入框旁边直接说
	// "超过多少要验密码",而不是等提交失败之后才弹出一格。
	PayPasswordThresholdQuota int64 `json:"pay_password_threshold_quota"`
	// PlayOpen 表示这一场所属的玩法当前是否还受理新参与(见 play.go)。
	//
	// 玩法被隐藏之后详情页**仍然可达** —— 已参与的人必须还能查到自己那一票、
	// 还能看结果与证据链,所以这一页不能变 404。但"参与"按钮必须当场置灰并
	// 说明原因:留一个点下去才被 409 顶回来的按钮,与本仓一直在补的
	// "界面上点得到、后端不认"是同一种缺陷,只是方向反了。
	PlayOpen bool `json:"play_open"`
}

// hallPhase 是大厅一个分区的口径:它包含哪些状态、按什么排序。
//
// # 为什么排序不是创建顺序
//
// 大厅要回答的第一个问题是「我现在还能参加什么」。按 id 倒序排,一场今晚就
// 截止的活动会被今天刚建的、下周才截止的压到第二页去 —— 而"还剩多久"正是
// 用户唯一来不及补救的那个量。
//
//   - live 段:能参加的(published)整体排在只能等结果的(locked/settling)
//     前面,组内按**下一个关键时刻**升序 —— published 是 close_at(最后能报名
//     的那一刻),locked/settling 是 draw_at(结果揭晓的那一刻)。
//   - ended 段:按 settled_at 倒序。刻意不用 draw_at:一场在开奖前就被取消的
//     活动,draw_at 仍停在原定开奖时间(可能还在未来),拿它排序会让一场早就
//     退完款的活动插在昨天刚开的奖前面。settled_at 是活动真正落定的那一刻,
//     库里 64 条 finished 全部非零。
//
// CASE WHEN 是标准 SQL,SQLite / MySQL / PostgreSQL 三家同义;表达式里只有
// 常量与列名,不拼接任何用户输入。
type hallPhase struct {
	order    string
	statuses []string
}

var hallPhases = map[string]hallPhase{
	"live": {
		statuses: []string{StatusPublished, StatusLocked, StatusSettling},
		order: "CASE WHEN status = 'published' THEN 0 ELSE 1 END ASC, " +
			"CASE WHEN status = 'published' THEN close_at ELSE draw_at END ASC, id DESC",
	},
	// 已结束的活动必须永久可查 —— 那正是需求里的"历史公正查询"。
	"ended": {
		statuses: []string{StatusFinished},
		order:    "settled_at DESC, id DESC",
	},
}

// hallQuery 按大厅口径拼出活动查询:草稿与已下架永不下发、按玩法过滤、
// 按分区过滤并定序。
//
// 抽成一个吃 *gorm.DB 的函数是为了让这段口径**能被真的跑一遍数据库测到**:
// handler 走的是 db.Get() 那个只连 MySQL 的全局句柄,在测试里起不来,而
// "两张标签返回同一份列表"恰恰只在这段拼装里看得见 —— 上一版正是在这里
// 静默失效了一整个版本。
func hallQuery(gdb *gorm.DB, kind, phase string, set opSettings) (*gorm.DB, error) {
	// hidden_at > 0 是管理员的「下架」。它与草稿并列写在这一行,而不是散在
	// handler 里:大厅口径只有这一个执行点,加在别处迟早会有一条分支漏掉。
	q := gdb.Model(&Activity{}).Where("status <> ? AND hidden_at = ?", StatusDraft, 0)
	// 被隐藏的玩法同样不下发。它必须在**这里**而不是 handler 里过滤:
	// 前端只会按 kind 分两张标签,而"只隐藏双色球"落不到任何一个 kind 上,
	// 交给前端过滤等于把已经发出去的活动数据当成可以藏住的东西。
	if clause, args := playFilterClause(set); clause != "" {
		q = q.Where(clause, args...)
	}
	if kind != "" {
		q = q.Where("kind = ?", kind)
	}
	// 空串 = 不分区(全部非草稿)。未登记的取值一律 400,**绝不静默忽略**:
	// 上一版前端发的是 `status=open|done`、这里读的却是 `phase`,于是"进行中"
	// 与"已结束"两张标签返回同一份列表,而全链路没有任何一处报错 —— 项目方
	// 反馈的"当前已结束和进行中没有进行区分"就是它。参数名一旦再漂移,
	// 这一行会当场把它变成一次可见的失败。
	if phase == "" {
		return q.Order("id desc"), nil
	}
	p, ok := hallPhases[phase]
	if !ok {
		return nil, errBadPhase
	}
	return q.Where("status IN (?)", p.statuses).Order(p.order), nil
}

// handleListActivities 返回活动大厅。
//
// 草稿永不下发:它还没有承诺,内容随时可能变,提前泄漏等于让人看到一个
// 可能被改掉的规则。
//
// 被运营隐藏的玩法(见 play.go)同样不在大厅里,效果与下架同形:只影响大厅
// 可见性与新参与,详情、我的参与、证据链与后台结算一律照常。区别只在粒度 ——
// 下架针对一场,玩法开关针对一类,而且**可以作用于进行中的活动**:那一场会
// 照常封盘、开奖、派奖,只是不再收新的参与。
//
// 被管理员下架(hidden_at > 0)的场次同样不在大厅里。**这是下架的全部效果** ——
// 活动详情、我的参与与匿名证据链一律照常可达:公正性一旦公布过就不能被运营
// 收回,而参与过的人必须还能查到自己那一票。下架只能作用于已结束的场次
// (见 api_admin_retire.go),所以它不可能被用来悄悄提前截止一场进行中的活动。
func handleListActivities(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	q, err := hallQuery(gdb.WithContext(c.Request.Context()),
		c.Query("kind"), c.Query("phase"), effectiveCtx(c.Request.Context()))
	if err != nil {
		respondErr(c, err)
		return
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计活动", err))
		return
	}
	rows := make([]Activity, 0, size)
	if err := q.Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询活动", err))
		return
	}

	ids := actIds(rows)
	prizeTotals, err := prizeTotalsByAct(c.Request.Context(), ids)
	if err != nil {
		respondErr(c, err)
		return
	}
	mine, err := myEntryCounts(c.Request.Context(), c.GetInt("id"), ids)
	if err != nil {
		respondErr(c, err)
		return
	}
	items := make([]activityBrief, 0, len(rows))
	for i := range rows {
		a := rows[i]
		items = append(items, activityBrief{
			ActNo: a.ActNo, Kind: a.Kind, Status: a.Status, Outcome: a.Outcome,
			Title: a.Title, CoverUrl: a.CoverUrl, CoverRef: a.CoverRef,
			StakeQuota: a.StakeQuota,
			OpenAt:     a.OpenAt, CloseAt: a.CloseAt, DrawAt: a.DrawAt,
			ActiveCount: a.ActiveCount, PoolQuota: a.PoolQuota,
			PrizeTotalQuota: prizeTotals[a.Id],
			MyEntryCount:    mine[a.Id],
			DrawMode:        a.DrawMode,
			SeriesNo:        a.SeriesNo,
			IssueNo:         a.IssueNo,
			PoolOpenQuota:   ballPoolOpen(&rows[i]),
			BallRedPool:     a.BallRedPool,
			BallRedPick:     a.BallRedPick,
			BallBluePool:    a.BallBluePool,
			BallBluePick:    a.BallBluePick,
			BallResult:      a.BallResult,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleGetActivity 返回活动详情。
func handleGetActivity(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	if act.Status == StatusDraft {
		respondErr(c, errActivityNotFound)
		return
	}

	detail := activityDetail{
		ActNo: act.ActNo, Kind: act.Kind, Status: act.Status, Outcome: act.Outcome,
		Title: act.Title, Intro: act.Intro,
		CoverUrl: act.CoverUrl, CoverRef: act.CoverRef,
		StakeQuota: act.StakeQuota, BetMinQuota: act.BetMinQuota, BetMaxQuota: act.BetMaxQuota,
		OpenAt: act.OpenAt, CloseAt: act.CloseAt, DrawAt: act.DrawAt,
		SettleDeadline: act.SettleDeadline,
		CommitHash:     act.CommitHash, RulesText: act.RulesText, Algo: act.Algo,
		NoWinnerPolicy:      NoWinnerPolicy,
		AllowMultiWin:       act.AllowMultiWin,
		FeeBps:              act.FeeBps,
		MinEntriesToHold:    act.MinEntriesToHold,
		ActiveCount:         act.ActiveCount,
		PoolQuota:           act.PoolQuota,
		MaxEntriesPerUser:   act.MaxEntriesPerUser,
		CooldownSeconds:     act.CooldownSeconds,
		DedupIp:             act.DedupIp,
		ResultEvidence:      act.ResultEvidence,
		DrawMode:            act.DrawMode,
		SeriesNo:            act.SeriesNo,
		IssueNo:             act.IssueNo,
		PoolOpenQuota:       ballPoolOpen(act),
		PoolSeedQuota:       act.PoolSeedQuota,
		PoolCarryQuota:      act.PoolCarryQuota,
		PoolShareBps:        act.PoolShareBps,
		BallRedPool:         act.BallRedPool,
		BallRedPick:         act.BallRedPick,
		BallBluePool:        act.BallBluePool,
		BallBluePick:        act.BallBluePick,
		BallResult:          act.BallResult,
		Spec:                make([]specItem, 0, 8),
		PayPasswordRequired: PayPasswordRequired(act.StakeQuota),
		PlayOpen:            effectiveCtx(ctx).playShown(playOf(act.Kind, act.DrawMode)),

		PayPasswordThresholdQuota: config.Get().Lottery.PayPasswordThresholdQuota,
	}

	if act.Kind == KindDraw {
		prizes := make([]Prize, 0, 8)
		if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
			Order("tier asc").Find(&prizes).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("查询奖档", err))
			return
		}
		for _, p := range prizes {
			detail.Spec = append(detail.Spec, specItem{
				Tier: p.Tier, Name: p.Name, AmountQuota: p.AmountQuota, Count: p.Count,
				PrizeType: p.Type(), WinPpm: p.WinPpm, TextDesc: p.TextDesc,
				RedMatch: p.RedMatch, BlueMatch: p.BlueMatch, PoolShareBps: p.PoolShareBps,
			})
		}
	} else {
		options := make([]Option, 0, 8)
		if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
			Order("opt_no asc").Find(&options).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("查询选项", err))
			return
		}
		for _, o := range options {
			detail.Spec = append(detail.Spec, specItem{
				OptNo: o.OptNo, Label: o.Label, IsCatchAll: o.IsCatchAll,
				BetQuota: o.BetQuota, BetCount: o.BetCount, IsWinner: o.IsWinner,
			})
			if o.IsWinner {
				detail.WinOptNo = o.OptNo
			}
		}
	}

	var mine int64
	if err := gdb.WithContext(ctx).Model(&Entry{}).
		Where("act_id = ? AND user_id = ? AND status IN (?)",
			act.Id, c.GetInt("id"), []string{EntryPending, EntrySuccess}).
		Count(&mine).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计我的参与", err))
		return
	}
	detail.MyEntryCount = int(mine)
	respondOK(c, detail)
}

// handleGetEligibility 回答"我为什么不能参加"。
//
// **仅用于展示,绝不作为放行依据**:真正的判定在报名路径上重做一遍
// (锁外尽早报错 + 活动行锁内的频次判定 + 主库行锁内的三项复检)。
// 把这个只读结果当放行凭据,就等于给了一个 30 秒的 TOCTOU 窗口。
func handleGetEligibility(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	if act.Status == StatusDraft {
		respondErr(c, errActivityNotFound)
		return
	}
	rules, err := ParseRules(act.RulesText)
	if err != nil {
		respondErr(c, err)
		return
	}
	subject, err := LoadSubject(ctx, c.GetInt("id"), rules, act.CreatedBy)
	if err != nil {
		// fail-closed:读不到就说读不到,绝不回落成"你符合条件"。
		respondErr(c, err)
		return
	}
	missing := Evaluate(rules, subject, act.StakeQuota, common.GetTimestamp())
	respondOK(c, gin.H{"eligible": len(missing) == 0, "missing": missing})
}

// entryRequest 是报名/投注的请求体。
type entryRequest struct {
	// ClientRequestId 由前端在**打开确认弹窗时**生成并缓存,重试沿用同一个。
	// 点击时才生成会让每次重试变成一次新参与(还各扣一笔钱),幂等彻底失效。
	ClientRequestId string `json:"client_request_id"`
	OptNo           int    `json:"opt_no"`
	// Amount 只有竞猜可以自选;抽奖恒等于活动的参与费,用户不能自己指定金额。
	Amount int64 `json:"amount"`
	// Pick 是双色球的选号,格式 "03,05,12|08"(红球升序 ⋆ 竖线 ⋆ 蓝球升序)。
	// 非双色球活动必须留空 —— 后端对带号的非双色球请求是**拒绝**而不是忽略。
	//
	// 机选是纯前端按钮(crypto.getRandomValues),服务端不区分自选与机选:
	// 号码一旦进链两者的可验证性完全一样,而服务端多一条随机路径就多一处
	// 要证明其公正的地方。
	Pick        string `json:"pick"`
	PayPassword string `json:"pay_password"`
}

// entryInputOf 把请求体翻译成一次参与的输入。
//
// 它是**唯一**一处"用户在界面上填的东西变成会被扣钱、会进哈希链的那份输入"。
// 单拎出来是因为漏抄一个字段不会有任何编译错误或运行报错:双色球上线时
// entryRequest 就少了 pick,表现是每一张票都被判成"选号不合法",整个玩法
// 开得出来却玩不了,而没有任何一条日志说得清原因。有了这个函数,那条契约
// 才有一个能被测试直接盯住的落点(见 ball_entry_test.go)。
func entryInputOf(c *gin.Context, actNo string, req entryRequest) EntryInput {
	return EntryInput{
		ActNo:           actNo,
		UserId:          c.GetInt("id"),
		ClientRequestId: req.ClientRequestId,
		OptNo:           req.OptNo,
		Amount:          req.Amount,
		Pick:            req.Pick,
		ClientIp:        c.ClientIP(),
		UserAgent:       c.Request.UserAgent(),
	}
}

// entryReceipt 是报名回执 —— 用户事后举证的全部凭据。
//
// chain_hash 必须**立即**返回并由前端持久展示:事后插入/删除/改动任何一条,
// 该条之后所有用户手里的这个值全部对不上。管理员要伪造名单,
// 就必须同时改掉 N 个用户已经看到过并可截图的值。
type entryReceipt struct {
	EntryNo    string `json:"entry_no"`
	Seq        int    `json:"seq"`
	ChainHash  string `json:"chain_hash"`
	PrevHash   string `json:"prev_hash"`
	CommitHash string `json:"commit_hash"`
	UserRef    string `json:"user_ref"`
	Amount     int64  `json:"amount"`
	OptNo      int    `json:"opt_no"`
	// Pick 回执必须带**归一化之后**的选号,而不是让前端把自己提交的那串原样显示:
	// 进链的是归一化后的字节,用户手里那份凭据若与链上的不是同一串,他事后拿它
	// 去比对会得出"平台改了我的号"的错误结论。
	Pick      string `json:"pick,omitempty"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// handleCreateEntry 是唯一会动钱的用户入口,已挂 CriticalRateLimit。
func handleCreateEntry(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	var req entryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	// 验密闸门的位置有讲究:必须在解析请求体**之后**(密码随体来),
	// 且在 ChargeEntry **之前**(那里面就开始落单、扣钱了)。
	// 阈值判定读的是活动的参与费,所以要先把活动取出来。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	act, err := loadActivityByNo(ctx, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	in := entryInputOf(c, act.ActNo, req)
	// 阈值判定必须用**本次真正要扣的金额**,不是活动的基准参与费:竞猜的投注额
	// 由用户自选,按 stake_quota 判定等于让一个基准费 1000、上限 500 万的盘口
	// 完全绕过二次验证 —— 而这道闸门存在的理由正是"盗号者能用参与把余额烧光"。
	amount, err := acceptAmount(act, in)
	if err != nil {
		respondErr(c, err)
		return
	}
	if PayPasswordRequired(amount) {
		// Require 不通过时已写好响应并 Abort。它没有任何可以表达豁免的入参 ——
		// 想加豁免的人必须先改 paypass.Require 的签名,那是一次看得见的动作。
		if !paypass.Require(c, c.GetInt("id"), req.PayPassword) {
			return
		}
	}

	entry, err := ChargeEntry(ctx, in)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, entryReceipt{
		EntryNo:    entry.EntryNo,
		Seq:        entry.Seq,
		ChainHash:  entry.ChainHash,
		PrevHash:   entry.PrevHash,
		CommitHash: act.CommitHash,
		UserRef:    entry.UserRef,
		Amount:     entry.Amount,
		OptNo:      entry.OptNo,
		Pick:       entry.Pick,
		Status:     entry.Status,
		CreatedAt:  entry.CreatedAt,
	})
}

type wonView struct {
	Kind   string `json:"kind"`
	Tier   int    `json:"tier"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
	// PayoutNo 只在文本奖上下发。它是"我的参与"跳去看奖品内容的唯一入口 ——
	// 内容本身走 /lottery/my/prizes/:payout_no 逐条拉,**不在列表里返回**:
	// 一个列表接口返回全部正文,意味着一次越权 bug 就是全量泄漏。
	PayoutNo string `json:"payout_no,omitempty"`
	// Fulfilled 让列表能直接显示"待管理员履行 / 可查看",
	// 而不必为每一行各打一次详情接口。
	Fulfilled bool `json:"fulfilled,omitempty"`
}

type myEntryView struct {
	EntryNo   string `json:"entry_no"`
	ActNo     string `json:"act_no"`
	Title     string `json:"title"`
	Kind      string `json:"kind"`
	Seq       int    `json:"seq"`
	ChainHash string `json:"chain_hash"`
	UserRef   string `json:"user_ref"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"`
	OptNo     int    `json:"opt_no"`
	// Pick 是双色球那张票买的号(归一化格式)。非双色球恒为空串。
	//
	// 它必须在"我的参与"里长期看得见:选号是这张票唯一由用户决定的内容,
	// 而事后争议的第一句话永远是"我买的明明是那一组"。回执弹窗关掉就没了,
	// 这份列表才是留得住的那一份。
	Pick      string   `json:"pick,omitempty"`
	Won       *wonView `json:"won"`
	CreatedAt int64    `json:"created_at"`
}

// handleListMyEntries 返回"我的参与与派奖"。
func handleListMyEntries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ctx := c.Request.Context()
	me := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Entry{}).Where("user_id = ?", me)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计我的参与", err))
		return
	}
	rows := make([]Entry, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询我的参与", err))
		return
	}

	// 跨库不能 JOIN,同库跨表也刻意不 JOIN:两次点查比一次 JOIN 更容易解释,
	// 而这两张表都在主键/唯一键上。
	acts := make(map[int64]Activity, len(rows))
	if ids := actIdsOfEntries(rows); len(ids) > 0 {
		list := make([]Activity, 0, len(ids))
		if err := gdb.WithContext(ctx).Where("id IN (?)", ids).Find(&list).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("查询活动", err))
			return
		}
		for _, a := range list {
			acts[a.Id] = a
		}
	}
	payouts := make(map[int64]Payout, len(rows))
	if ids := entryIds(rows); len(ids) > 0 {
		list := make([]Payout, 0, len(ids))
		if err := gdb.WithContext(ctx).Where("entry_id IN (?)", ids).Find(&list).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("查询出款", err))
			return
		}
		for _, p := range list {
			// 一张票最多只有一类出款(uk(act_id, entry_id, kind) 之外,
			// 派奖与退款在业务上互斥)。真出现两条时以派奖为准展示。
			if old, ok := payouts[p.EntryId]; ok && old.Kind != PayoutRefund {
				continue
			}
			payouts[p.EntryId] = p
		}
	}

	items := make([]myEntryView, 0, len(rows))
	for _, e := range rows {
		v := myEntryView{
			EntryNo: e.EntryNo, Seq: e.Seq, ChainHash: e.ChainHash, UserRef: e.UserRef,
			Amount: e.Amount, Status: e.Status, OptNo: e.OptNo, Pick: e.Pick,
			CreatedAt: e.CreatedAt,
		}
		if a, ok := acts[e.ActId]; ok {
			v.ActNo, v.Title, v.Kind = a.ActNo, a.Title, a.Kind
		}
		if p, ok := payouts[e.Id]; ok {
			v.Won = &wonView{Kind: p.Kind, Tier: p.Tier, Amount: p.AmountQuota, Status: p.Status}
			if p.Kind == PayoutText {
				v.Won.PayoutNo, v.Won.Fulfilled = p.PayoutNo, p.FulfilledAt > 0
			}
		}
		items = append(items, v)
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// ─────────────────────────── 小工具 ───────────────────────────

func actIds(rows []Activity) []int64 {
	out := make([]int64, 0, len(rows))
	for _, a := range rows {
		out = append(out, a.Id)
	}
	return out
}

func actIdsOfEntries(rows []Entry) []int64 {
	seen := make(map[int64]bool, len(rows))
	out := make([]int64, 0, len(rows))
	for _, e := range rows {
		if seen[e.ActId] {
			continue
		}
		seen[e.ActId] = true
		out = append(out, e.ActId)
	}
	return out
}

func entryIds(rows []Entry) []int64 {
	out := make([]int64, 0, len(rows))
	for _, e := range rows {
		out = append(out, e.Id)
	}
	return out
}

// myEntryCounts 一次查出当前用户在这一页每场活动上的有效参与数。
//
// 一次分组查询而不是每张卡片各查一次:大厅一页 12 张卡,后者就是 12 次往返。
func myEntryCounts(ctx context.Context, userId int, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	if userId <= 0 || len(ids) == 0 {
		return out, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	rows := make([]struct {
		ActId int64
		Cnt   int
	}, 0, len(ids))
	err := gdb.WithContext(ctx).Model(&Entry{}).
		Select("act_id, COUNT(*) AS cnt").
		Where("user_id = ? AND act_id IN (?) AND status IN (?)",
			userId, ids, []string{EntryPending, EntrySuccess}).
		Group("act_id").Scan(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计我的参与", err)
	}
	for _, r := range rows {
		out[r.ActId] = r.Cnt
	}
	return out, nil
}

// prizeTotalsByAct 一次查出多场活动的奖品总额度。
func prizeTotalsByAct(ctx context.Context, ids []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	rows := make([]struct {
		ActId int64
		Total int64
	}, 0, len(ids))
	err := gdb.WithContext(ctx).Model(&Prize{}).
		Select("act_id, COALESCE(SUM(amount_quota * count), 0) AS total").
		Where("act_id IN (?)", ids).Group("act_id").Scan(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计奖品总额度", err)
	}
	for _, r := range rows {
		out[r.ActId] = r.Total
	}
	return out, nil
}
