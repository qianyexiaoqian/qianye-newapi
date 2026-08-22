package lottery

import (
	"context"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_proof.go —— 历史公正查询(证据链)。
//
// # 为什么匿名可访问
//
// 需要注册账号才能取证的公正性不叫公正性。验证者第一步得先能**拿到** proof,
// 而这正是需求里"结束的抽奖活动要保留,作为历史公正查询"的落点。
// 它挂在 module.PublicRouter 上,与 /api/qy/config 同一档:限流与请求台账
// 都已生效,但没有认证。
//
// # 这套证据链保证什么、不保证什么(诚实版,原样写进活动规则页)
//
//	保证:任何人都能在自己的机器上重算出中奖名单,并核对它与平台公布的一致。
//	保证:名单在种子公开**之前**就已冻结(roster_hash 先于 seed 公开),
//	      且每个参与者在报名成功那一刻就已持有自己那一环(chain_hash)。
//	保证:承诺覆盖随机源、参与条件、奖档/选项、四个时刻与每一个影响结果的开关 ——
//	      管理员无法在不碰种子的前提下算出想要的结果而不被发现。
//
//	不保证:物理不可篡改。有数据库写权限的人能同时改掉种子与承诺哈希;
//	      他改不掉的是**已经分发给 N 个用户的报名回执**、独立表里的发布审计、
//	      以及揭示前就已公开的 roster_hash。任何一处对不上,验证脚本立刻报错
//	      且举证方向明确(用户手里的副本是平台自己签发的)。
//	      commit-reveal 保证的是**不可抵赖地被检出**,不是不可篡改。
//	不保证:user_ref 与真人的对应关系可被外部证明 —— 盐永不公开(user_id 空间
//	      只有几万到几十万,公开盐就能全量枚举反查身份)。这只影响
//	      allow_multi_win=false 那一条约束,不影响每张票中签概率严格相等。
//	不保证:竞猜的 winning_option 是真的。那是链下事实,任何密码学都证不了
//	      世界杯谁赢了。能做的只有:选项与费率进承诺、结果强制附依据并公开、
//	      结果一经写入不可修改。作弊面被压缩到"一次性地公开撒谎"。

// proofEntry 是证据链里的一条参与记录。
//
// 字段名与验证脚本逐字一致 —— 改任何一个名字都会让所有已发布的离线脚本失效。
type proofEntry struct {
	Seq       int    `json:"seq"`
	EntryNo   string `json:"entry_no"`
	UserRef   string `json:"user_ref"`
	OptNo     int    `json:"opt_no"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"`
	PrevHash  string `json:"prev_hash"`
	ChainHash string `json:"chain_hash"`
	// OrderNo 让非 success 的条目可以被交叉复核:链刻意不含 status,
	// "这一条到底成没成"由资金单与用户自己的额度流水佐证。
	OrderNo string `json:"order_no"`
	// Pick 是双色球的选号。它进 lot-v2 的链与名单原像,因此**必须**下发 ——
	// 少了它验证者连 chain_hash 都推不出来。
	Pick string `json:"pick,omitempty"`
}

type proofWinner struct {
	Pos     int    `json:"pos"`
	Tier    int    `json:"tier"`
	EntryNo string `json:"entry_no"`
	UserRef string `json:"user_ref"`
	Amount  int64  `json:"amount"`

	// PrizeType 让验证者能把两条派奖腿分开复算:quota 档比金额,
	// text 档只比"这一位中了哪一档"。少了它,一场混合奖档的活动会被算成
	// "有一堆 0 元中奖者",而那与真正的漏发在验证结果上无法区分。
	PrizeType string `json:"prize_type,omitempty"`
	// Fulfilled 只说"管理员标记履行了没有",**绝不下发任何内容,连哈希都不下发**。
	//
	// proof 是匿名可访问的;兑换码的熵不高,公开一个可复现原像的哈希等于给了
	// 离线爆破面。这一层能公开的只有"有没有这件事",不是"这件事是什么"。
	Fulfilled bool `json:"fulfilled,omitempty"`
}

type proofPayout struct {
	EntryNo string `json:"entry_no"`
	Kind    string `json:"kind"`
	Amount  int64  `json:"amount"`
	Status  string `json:"status"`
}

type proofSpecItem struct {
	Tier        int    `json:"tier,omitempty"`
	Name        string `json:"name,omitempty"`
	AmountQuota int64  `json:"amount_quota,omitempty"`
	Count       int    `json:"count,omitempty"`
	OptNo       int    `json:"opt_no,omitempty"`
	Label       string `json:"label,omitempty"`
	IsCatchAll  bool   `json:"is_catch_all,omitempty"`

	// ── lot-v2 追加的奖档分量,全部进 spec 原像 ──
	//
	// 少下发任何一个,验证者就算不出 spec_hash,而 spec_hash 又进 commit_hash ——
	// 于是整份证据链从第一步就验不了。omitempty 只影响 JSON 体积:
	// 验证脚本对缺失键取零值,与原像里的零值编码完全一致。
	PrizeType    string `json:"prize_type,omitempty"`
	WinPpm       int    `json:"win_ppm,omitempty"`
	TextDesc     string `json:"text_desc,omitempty"`
	RedMatch     int    `json:"red_match,omitempty"`
	BlueMatch    int    `json:"blue_match,omitempty"`
	PoolShareBps int    `json:"pool_share_bps,omitempty"`
}

type proofDocument struct {
	Algo    string `json:"algo"`
	ActNo   string `json:"act_no"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
	Title   string `json:"title"`

	// ── 承诺原像的每一个分量 ──
	RulesText        string          `json:"rules_text"`
	RulesHash        string          `json:"rules_hash"`
	Spec             []proofSpecItem `json:"spec"`
	SpecHash         string          `json:"spec_hash"`
	StakeQuota       int64           `json:"stake_quota"`
	OpenAt           int64           `json:"open_at"`
	CloseAt          int64           `json:"close_at"`
	DrawAt           int64           `json:"draw_at"`
	SettleDeadline   int64           `json:"settle_deadline"`
	AllowMultiWin    bool            `json:"allow_multi_win"`
	FeeBps           int             `json:"fee_bps"`
	NoWinnerPolicy   string          `json:"no_winner_policy"`
	MinEntriesToHold int             `json:"min_entries_to_hold"`

	// ── lot-v2 追加的承诺分量 ──
	//
	// 非 ball 活动这些位恒为空串/0,但它们**逐个进原像**,因此必须原样下发:
	// 少一个占位,验证者就无法区分"一场普通抽奖"与"一场被改成双色球的抽奖"。
	DrawMode       string `json:"draw_mode"`
	SeriesNo       string `json:"series_no"`
	IssueNo        int    `json:"issue_no"`
	PoolSeedQuota  int64  `json:"pool_seed_quota"`
	PoolCarryQuota int64  `json:"pool_carry_quota"`
	// PoolOpenQuota 是**开局基数**(注资 + 滚存),进承诺。
	// 本期真正可派发的池子 = pool_open_quota + floor(pool_quota × pool_share_bps / 10000),
	// 验证脚本按这个式子自己算,平台无法在这里报一个更大的数。
	PoolOpenQuota int64 `json:"pool_open_quota"`
	PoolShareBps  int   `json:"pool_share_bps"`
	BallRedPool   int   `json:"ball_red_pool"`
	BallRedPick   int   `json:"ball_red_pick"`
	BallBluePool  int   `json:"ball_blue_pool"`
	BallBluePick  int   `json:"ball_blue_pick"`
	// BallResult 是平台公布的开奖号。它**不进承诺**,而是被验证脚本从种子重新
	// 摇一遍再比对 —— 这正是"界面上那七颗球是产生结果的原因"这条主张的检验点。
	BallResult string `json:"ball_result"`

	CommitHash string `json:"commit_hash"`
	// Seed 在揭示之前是空串。空串不是"没有种子",是"还不该给你" ——
	// 验证脚本据此知道现在只能验到第 3 步(名单已冻结),验不了第 5 步(名单)。
	Seed string `json:"seed"`

	// ── 冻结名单 ──
	ChainHead   string `json:"chain_head"`
	RosterHash  string `json:"roster_hash"`
	RosterCount int    `json:"roster_count"`
	PoolQuota   int64  `json:"pool_quota"`

	// ── 结果 ──
	WinOptNo       int           `json:"win_opt_no"`
	ResultEvidence string        `json:"result_evidence"`
	FeeQuota       int64         `json:"fee_quota"`
	Winners        []proofWinner `json:"winners"`
	Payouts        []proofPayout `json:"payouts"`

	// ── 实际发生的时刻 ──
	//
	// open_at/close_at/draw_at 是**计划**时刻,而协议最核心的一条主张是
	// "名单先于种子公开"。不下发实际的封盘与揭示时刻,这条主张就无法从证据文件
	// 本身被证明或证伪 —— 验证者只能靠自己恰好在那个窗口里抓过一次快照。
	LockedAt   int64 `json:"locked_at"`
	RevealedAt int64 `json:"revealed_at"`
	SettledAt  int64 `json:"settled_at"`

	// ── 条目(分页)──
	Entries  []proofEntry `json:"entries"`
	Total    int64        `json:"total"`
	Page     int          `json:"p"`
	PageSize int          `json:"page_size"`

	// Notice 是给人看的边界说明。放进 JSON 而不是只写在页面上:
	// 离线拿到 proof 文件的人也必须读到它。
	Notice string `json:"notice"`
}

const proofNotice = "commit-reveal 保证的是「篡改会被不可抵赖地检出」,不是「物理不可篡改」。" +
	"请把你在开奖前抓到的 roster_hash、以及你自己报名时收到的 chain_hash," +
	"与本文件中的对应值逐一核对。" +
	"概率制(draw_mode=prob):每张票的摇号结果只依赖 final_seed、act_no 与自己的 entry_no," +
	"落选者与中奖者用的是同一组公开输入、走的是同一段复算 —— " +
	"「我为什么没中」因此是可以自己算出来的,而不是平台的一面之词。" +
	"摇号量取票面前 64 位缩放到 [0,1000000),相对偏差小于 2^-44,这一点不掩饰。" +
	"文本奖:奖品的名称、公开说明与份数在发布时就已进入承诺哈希、事后不可更改;" +
	"但中奖者收到的那串**具体内容**是开奖之后由管理员填入的,**它没有任何承诺**," +
	"因为在承诺那一刻它还不存在。本文件因此只公布 fulfilled 这个布尔," +
	"不公布内容、也不公布内容的哈希。" +
	"双色球(draw_mode=ball):开奖号**不进承诺**,它由公开的种子摇出来 —— " +
	"请自己按 ball_red_pool / ball_red_pick 重摇一遍再与 ball_result 比对," +
	"这一步才是「界面上那几颗球是产生结果的原因、而不是事后编的动画」的检验点。" +
	"本期可派发的池子 = pool_open_quota + floor(pool_quota × pool_share_bps / 10000)," +
	"其中 pool_open_quota(本期注资 + 上期滚存)在发布时就已进承诺、事后不可改;" +
	"没有派出去的部分滚进下一期,而系列一旦被关闭,滚存余额作废。"

// handleGetProof 返回一场活动的完整证据链。**匿名可访问。**
func handleGetProof(c *gin.Context) {
	// 刻意不走 guard.RequireAPI(FlagLottery):功能被临时关停之后,
	// 已经开完的活动仍然必须可验证 —— 那正是"历史公正查询"的全部意义。
	// 只判扩展是否启用与库是否可用。
	if !guard.Enabled() {
		respondErr(c, errProofDisabled)
		return
	}
	if !config.Get().Lottery.ProofOpen() {
		respondErr(c, errProofDisabled)
		return
	}
	if !db.Available() {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "证据链暂不可用,请稍后重试",
		})
		return
	}

	ctx := c.Request.Context()
	gdb := db.Get()
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	// 封盘之前不下发:此时名单还在变,一份会变的"证据"只会制造误解。
	if act.Status == StatusDraft || act.Status == StatusPublished {
		respondErr(c, errProofNotReady)
		return
	}

	doc, err := buildProof(ctx, gdb, act, c)
	if err != nil {
		respondErr(c, err)
		return
	}

	if c.Query("format") == "ndjson" {
		streamProof(c, gdb, act, doc)
		return
	}
	respondOK(c, doc)
}

// buildProof 组装证据链文档(条目部分分页)。
func buildProof(ctx context.Context, gdb *gorm.DB, act *Activity, c *gin.Context) (*proofDocument, error) {
	page, size := httpq.Paginate(c, proofPaging)

	doc := &proofDocument{
		Algo: act.Algo, ActNo: act.ActNo, Kind: act.Kind,
		Status: act.Status, Outcome: act.Outcome, Title: act.Title,
		RulesText: act.RulesText, RulesHash: act.RulesHash, SpecHash: act.SpecHash,
		StakeQuota: act.StakeQuota,
		OpenAt:     act.OpenAt, CloseAt: act.CloseAt, DrawAt: act.DrawAt,
		SettleDeadline: act.SettleDeadline,
		AllowMultiWin:  act.AllowMultiWin, FeeBps: act.FeeBps,
		NoWinnerPolicy: NoWinnerPolicy, MinEntriesToHold: act.MinEntriesToHold,
		DrawMode: act.DrawMode,
		SeriesNo: act.SeriesNo, IssueNo: act.IssueNo,
		PoolSeedQuota: act.PoolSeedQuota, PoolCarryQuota: act.PoolCarryQuota,
		PoolOpenQuota: act.PoolOpenQuota, PoolShareBps: act.PoolShareBps,
		BallRedPool: act.BallRedPool, BallRedPick: act.BallRedPick,
		BallBluePool: act.BallBluePool, BallBluePick: act.BallBluePick,
		BallResult: act.BallResult,
		CommitHash: act.CommitHash,
		ChainHead:  act.ChainHead, RosterHash: act.RosterHash, RosterCount: act.RosterCount,
		PoolQuota: act.PoolQuota, ResultEvidence: act.ResultEvidence,
		FeeQuota: act.PlatformFeeQuota,
		LockedAt: act.LockedAt, RevealedAt: act.RevealedAt, SettledAt: act.SettledAt,
		Spec:    make([]proofSpecItem, 0, 8),
		Winners: make([]proofWinner, 0, 8),
		Payouts: make([]proofPayout, 0, 8),
		Entries: make([]proofEntry, 0, size),
		Page:    page, PageSize: size,
		Notice: proofNotice,
	}

	if seedShouldBeRevealed(act) {
		seed, err := loadSeedForReveal(ctx, gdb, act.Id)
		if err != nil {
			return nil, err
		}
		doc.Seed = seed
	}

	if err := fillProofSpec(ctx, gdb, act, doc); err != nil {
		return nil, err
	}

	var total int64
	q := gdb.WithContext(ctx).Model(&Entry{}).Where("act_id = ?", act.Id)
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("统计证据链条目", err)
	}
	doc.Total = total

	// 按 seq 升序:验证者要逐条推进哈希链,而链的顺序就是 seq。
	rows := make([]Entry, 0, size)
	err := q.Order("seq asc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("查询证据链条目", err)
	}
	for _, e := range rows {
		doc.Entries = append(doc.Entries, toProofEntry(e))
	}

	if err := fillProofOutcome(ctx, gdb, act, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// seedShouldBeRevealed 判断此刻是否可以公开种子。
//
//	抽奖:必须等 revealed_at 落库。提前一秒都不行 —— 揭示之前公开种子,
//	     知道种子的人就能在封盘前算出自己的名次并决定要不要再报一次。
//	竞猜:抽签根本用不到种子,但承诺原像里有它,不公开就没人能复算
//	     commit_hash。结果一旦录入(status ≥ settling)即可公开。
//	取消/流局的抽奖:结局已定、不会再抽,公开种子只是让 commit_hash 可复算。
func seedShouldBeRevealed(act *Activity) bool {
	if act.RevealedAt > 0 {
		return true
	}
	return act.Status == StatusSettling || act.Status == StatusFinished
}

func toProofEntry(e Entry) proofEntry {
	return proofEntry{
		Seq: e.Seq, EntryNo: e.EntryNo, UserRef: e.UserRef, OptNo: e.OptNo,
		Amount: e.Amount, Status: e.Status,
		PrevHash: e.PrevHash, ChainHash: e.ChainHash, OrderNo: e.OrderNo,
		Pick: e.Pick,
	}
}

func fillProofSpec(ctx context.Context, gdb *gorm.DB, act *Activity, doc *proofDocument) error {
	if act.Kind == KindDraw {
		prizes := make([]Prize, 0, 8)
		if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
			Order("tier asc").Find(&prizes).Error; err != nil {
			db.MarkFailure(err)
			return wrapInternal("查询奖档", err)
		}
		for _, p := range prizes {
			doc.Spec = append(doc.Spec, proofSpecItem{
				Tier: p.Tier, Name: p.Name, AmountQuota: p.AmountQuota, Count: p.Count,
				PrizeType: p.Type(), WinPpm: p.WinPpm, TextDesc: p.TextDesc,
				RedMatch: p.RedMatch, BlueMatch: p.BlueMatch, PoolShareBps: p.PoolShareBps,
			})
		}
		return nil
	}
	options := make([]Option, 0, 8)
	if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
		Order("opt_no asc").Find(&options).Error; err != nil {
		db.MarkFailure(err)
		return wrapInternal("查询选项", err)
	}
	for _, o := range options {
		doc.Spec = append(doc.Spec, proofSpecItem{
			OptNo: o.OptNo, Label: o.Label, IsCatchAll: o.IsCatchAll,
		})
		if o.IsWinner {
			doc.WinOptNo = o.OptNo
		}
	}
	return nil
}

// fillProofOutcome 填入中奖名单与出款。
//
// 中奖名单不落库单独一张表:它由 payout 行 + entry 反查得到,
// 而且验证者本来就要自己重算一遍。少一张表就少一处可被事后改动的地方。
func fillProofOutcome(ctx context.Context, gdb *gorm.DB, act *Activity, doc *proofDocument) error {
	if act.Status != StatusSettling && act.Status != StatusFinished {
		return nil
	}
	payouts := make([]Payout, 0, 64)
	if err := gdb.WithContext(ctx).Where("act_id = ?", act.Id).
		Order("id asc").Find(&payouts).Error; err != nil {
		db.MarkFailure(err)
		return wrapInternal("查询出款", err)
	}
	if len(payouts) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(payouts))
	for _, p := range payouts {
		ids = append(ids, p.EntryId)
	}
	entries := make([]Entry, 0, len(ids))
	if err := gdb.WithContext(ctx).Where("id IN (?)", ids).Find(&entries).Error; err != nil {
		db.MarkFailure(err)
		return wrapInternal("查询出款对应的参与明细", err)
	}
	byId := make(map[int64]Entry, len(entries))
	for _, e := range entries {
		byId[e.Id] = e
	}

	for _, p := range payouts {
		e, ok := byId[p.EntryId]
		if !ok {
			// 一条出款指不到任何参与明细。
			//
			// 代码里造不出这种行(四个 PlanPayouts 调用方的 EntryId 全部取自
			// 刚读出来的 roster,开奖与竞猜结算两处在反查不到时硬回滚整事务;
			// 删活动时 payout 与 entry 同事务按 act_id 一起清),所以它只可能
			// 来自直接改库 —— 而这套 commit-reveal 从一开始就声明「有数据库写
			// 权限的人能改掉任何东西,协议保证的是**不可抵赖地被检出**」。
			//
			// 此前这里走的是 `e := byId[p.EntryId]`,map 取不到就拿到 Entry 零值,
			// 于是这条出款会在**公开证据链**里变成一位 entry_no='' / user_ref=''
			// 的中奖者,照常带着金额和 tier。备份库实测:活动
			// LT20260810-ab1f9f74c67f5452 的公开 proof 里就多了这么一位
			// (amount=3333),第三方按种子重算只会得到 4 位,当场判 FAIL ——
			// 而那正是"平台确实有问题"的时刻,却没有任何一处告警。
			//
			// 现在:落一条对账异常(管理端看得见、且它会挡住这一场被删掉),
			// 并且**不把这条出款写进 winners** —— 一位空 entry_no 的中奖者在
			// 公开文档里既证明不了什么,也无法被任何人核对。它仍然进 payouts
			// (那是"平台实际付了哪些钱"的如实记账,不能瞒),但 entry_no 留空
			// 本身就是它对不上账的证据。
			raiseFlag(ctx, act.Id, FlagPayoutOrphan,
				"出款 "+p.PayoutNo+" 指向的参与明细 id="+strconv.FormatInt(p.EntryId, 10)+" 不存在")
			doc.Payouts = append(doc.Payouts, proofPayout{
				Kind: p.Kind, Amount: p.AmountQuota, Status: p.Status,
			})
			continue
		}
		doc.Payouts = append(doc.Payouts, proofPayout{
			EntryNo: e.EntryNo, Kind: p.Kind, Amount: p.AmountQuota, Status: p.Status,
		})
		// 文本奖同样是中奖位,必须进 winners —— 否则一场混合奖档活动的
		// 复算名单会比公布的名单多出几位,验证脚本报 FAIL,而真实情况是
		// 平台完全诚实。它的 amount 恒为 0,prize_type 告诉验证者别去比金额。
		if p.Kind == PayoutPrize || p.Kind == PayoutText {
			prizeType := PrizeTypeQuota
			if p.Kind == PayoutText {
				prizeType = PrizeTypeText
			}
			doc.Winners = append(doc.Winners, proofWinner{
				Pos: p.DrawPos, Tier: p.Tier, EntryNo: e.EntryNo,
				UserRef: e.UserRef, Amount: p.AmountQuota,
				PrizeType: prizeType, Fulfilled: p.FulfilledAt > 0,
			})
		}
	}
	return nil
}

// streamProof 以 NDJSON 下发**完整且自洽**的证据链。
//
// 第一行是文档头(承诺、名单哈希、种子、结果……,entries 为空),之后每行一条
// 参与记录。这个形状是刻意的:
//
//   - 分页的 JSON 版**验不了**链与名单(少一条链就断),而 >200 人的活动
//     一页装不下 —— 只给分页版等于所有值得验的活动都没有一份可验的文件。
//   - 只吐条目的 NDJSON 同样验不了任何一步:没有 commit_hash / roster_hash /
//     seed / spec,连第一步都算不了。
//
// 两者都不能单独交付,所以这里把头与条目拼进同一份流:验证脚本读第一行拿到
// 全部承诺分量,再边下边推链,不必把整份读进内存。
func streamProof(c *gin.Context, gdb *gorm.DB, act *Activity, doc *proofDocument) {
	c.Header("Content-Type", "application/x-ndjson; charset=utf-8")
	c.Status(http.StatusOK)

	// 头一行里的 entries 必须是空数组而不是那一页:验证脚本会把后续的行当作
	// 全量条目,头里再带一页就成了重复。page/page_size 同理清零,NDJSON 是全量。
	header := *doc
	header.Entries = make([]proofEntry, 0)
	header.Page = 0
	header.PageSize = 0
	line, err := common.Marshal(header)
	if err != nil {
		c.Writer.WriteString(`{"error":"encode_failed"}` + "\n")
		return
	}
	c.Writer.Write(line)
	c.Writer.WriteString("\n")

	const batch = 1000
	lastSeq := 0
	for {
		rows := make([]Entry, 0, batch)
		err := gdb.WithContext(c.Request.Context()).
			Where("act_id = ? AND seq > ?", act.Id, lastSeq).
			Order("seq asc").Limit(batch).Find(&rows).Error
		if err != nil {
			db.MarkFailure(err)
			// 已经开始写响应体,改不了状态码了。写一行显式的错误标记而不是
			// 静默截断:截断会让验证脚本以为"名单就这么长"并算出一个
			// 对不上的 roster_hash,却不知道是下载出了问题。
			c.Writer.WriteString(`{"error":"stream_failed"}` + "\n")
			return
		}
		if len(rows) == 0 {
			return
		}
		for _, e := range rows {
			line, mErr := common.Marshal(toProofEntry(e))
			if mErr != nil {
				c.Writer.WriteString(`{"error":"encode_failed"}` + "\n")
				return
			}
			c.Writer.Write(line)
			c.Writer.WriteString("\n")
			lastSeq = e.Seq
		}
		c.Writer.Flush()
		if len(rows) < batch {
			return
		}
	}
}
