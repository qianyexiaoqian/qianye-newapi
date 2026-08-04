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
}

type specItem struct {
	// 抽奖:tier/name/amount_quota/count;竞猜:opt_no/label/is_catch_all。
	// 用一个联合结构而不是两个数组,是为了让前端与验证脚本只处理一种形状。
	Tier        int    `json:"tier,omitempty"`
	Name        string `json:"name,omitempty"`
	AmountQuota int64  `json:"amount_quota,omitempty"`
	Count       int    `json:"count,omitempty"`

	OptNo      int    `json:"opt_no,omitempty"`
	Label      string `json:"label,omitempty"`
	IsCatchAll bool   `json:"is_catch_all,omitempty"`
	BetQuota   int64  `json:"bet_quota,omitempty"`
	BetCount   int    `json:"bet_count,omitempty"`
	IsWinner   bool   `json:"is_winner,omitempty"`
}

type activityDetail struct {
	ActNo   string `json:"act_no"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	Title   string `json:"title"`
	Intro   string `json:"intro"`

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
}

// handleListActivities 返回活动大厅。
//
// 草稿永不下发:它还没有承诺,内容随时可能变,提前泄漏等于让人看到一个
// 可能被改掉的规则。
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
	q := gdb.WithContext(c.Request.Context()).Model(&Activity{}).
		Where("status <> ?", StatusDraft)
	if v := c.Query("kind"); v != "" {
		q = q.Where("kind = ?", v)
	}
	switch c.Query("phase") {
	case "ended":
		// 已结束的活动必须永久可查 —— 那正是需求里的"历史公正查询"。
		q = q.Where("status = ?", StatusFinished)
	case "live":
		q = q.Where("status IN (?)", []string{StatusPublished, StatusLocked, StatusSettling})
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计活动", err))
		return
	}
	rows := make([]Activity, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
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
	for _, a := range rows {
		items = append(items, activityBrief{
			ActNo: a.ActNo, Kind: a.Kind, Status: a.Status, Outcome: a.Outcome,
			Title: a.Title, StakeQuota: a.StakeQuota,
			OpenAt: a.OpenAt, CloseAt: a.CloseAt, DrawAt: a.DrawAt,
			ActiveCount: a.ActiveCount, PoolQuota: a.PoolQuota,
			PrizeTotalQuota: prizeTotals[a.Id],
			MyEntryCount:    mine[a.Id],
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
		Spec:                make([]specItem, 0, 8),
		PayPasswordRequired: PayPasswordRequired(act.StakeQuota),

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
	Amount      int64  `json:"amount"`
	PayPassword string `json:"pay_password"`
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
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
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
	in := EntryInput{
		ActNo:           act.ActNo,
		UserId:          c.GetInt("id"),
		ClientRequestId: req.ClientRequestId,
		OptNo:           req.OptNo,
		Amount:          req.Amount,
		ClientIp:        c.ClientIP(),
		UserAgent:       c.Request.UserAgent(),
	}
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
		Status:     entry.Status,
		CreatedAt:  entry.CreatedAt,
	})
}

type wonView struct {
	Kind   string `json:"kind"`
	Tier   int    `json:"tier"`
	Amount int64  `json:"amount"`
	Status string `json:"status"`
}

type myEntryView struct {
	EntryNo   string   `json:"entry_no"`
	ActNo     string   `json:"act_no"`
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	Seq       int      `json:"seq"`
	ChainHash string   `json:"chain_hash"`
	UserRef   string   `json:"user_ref"`
	Amount    int64    `json:"amount"`
	Status    string   `json:"status"`
	OptNo     int      `json:"opt_no"`
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
			Amount: e.Amount, Status: e.Status, OptNo: e.OptNo, CreatedAt: e.CreatedAt,
		}
		if a, ok := acts[e.ActId]; ok {
			v.ActNo, v.Title, v.Kind = a.ActNo, a.Title, a.Kind
		}
		if p, ok := payouts[e.Id]; ok {
			v.Won = &wonView{Kind: p.Kind, Tier: p.Tier, Amount: p.AmountQuota, Status: p.Status}
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
