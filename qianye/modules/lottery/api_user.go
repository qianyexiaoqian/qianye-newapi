package lottery

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
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
	// MyEntriesRemaining 是"我在这一场还能买几注"。
	//
	// # 为什么是后端算而不是前端拿 max_entries_per_user 减 my_entry_count
	//
	// 那两个数已经在下发,前端确实减得出来 —— 但减法要选用哪一个计数口径,
	// 而 checkCaps 数的是 status IN (success/excluded/refunded) 而不是这里的
	// (pending/success)。两处口径本来就不同名同形,交给前端就等于把一条会漂移的
	// 约定写进注释。这里与 my_entry_count 出自同一次查询,两个数不可能各说各话。
	//
	// # 零值口径
	//
	//   - nil(JSON null)= 本场没有每人上限,想买几注买几注(仍受
	//     max_picks_per_request 的单次批量约束)。
	//   - 0 = 已经买满,一注都不能再买。
	//
	// **它只是提示,绝不是放行依据**:真正的判定在活动行锁内(见 checkCaps),
	// 拿它当凭据就等于给了一个刷新周期长的 TOCTOU 窗口。它存在的理由是让
	// "你还能买 3 注"出现在按下确认**之前**,而不是提交了十注之后才被顶回来。
	//
	// 它同时夹住**两道**每人闸门:max_entries_per_user 与 max_attempts_per_user。
	// 只报前者会让第二道在提交时突然冒出来,而这一行存在的全部理由就是不让
	// 任何一道闸门在按下确认之后才第一次露面。尝试上限数的是"含失败的全部条目",
	// 与 checkCaps 的 attempts 同一个方向。
	MyEntriesRemaining *int `json:"my_entries_remaining"`
	// TotalEntriesRemaining 是"这一场**全场**还剩几个名额"。
	//
	// # 为什么必须与每人上限分开下发
	//
	// 它们会在完全不同的时刻绑住同一次提交:每人上限只跟自己有关、刷新一次就
	// 不再变;全场名额是所有人共用的,可能在用户选号的这一分钟里被别人买光。
	// 合成一个数会让"你还能买 3 注"在两种原因下说同一句话,而用户能做的事
	// 恰好相反 —— 前者是"我买够了",后者是"手快有手慢无"。
	//
	// # 零值口径(与 MyEntriesRemaining 对齐)
	//
	//   - nil(JSON null)= 本场没有全场上限。存量活动才可能是这一档:
	//     buildActivity 会把没填的 max_total_entries 回填成硬上限,
	//     所以新建的活动一定是个正数。
	//   - 0 = 全场已满,一注都买不进去了。
	//
	// 同样**只是提示**:权威判定在活动行锁内(checkCaps 用的是活动行上的
	// active_count + pending_count,与这里读的是同一对计数器)。
	TotalEntriesRemaining *int `json:"total_entries_remaining"`
	// MaxPicksPerRequest 是这一场一次提交最多几注(picksCapOf,活动级可配)。
	// 下发它是为了让选号盘的"再加一注"按钮在到顶时当场置灰 —— 前端写死一个
	// 同名常量的下场,是后端调整之后界面上多出来的那一注恒被 400 顶回来;
	// 而这个数**每一场都可能不一样**,写死等于在别的场次上一定是错的。
	MaxPicksPerRequest int `json:"max_picks_per_request"`
	// MyTickets 是**我自己**在这一场买的票,只在 draw_mode=ball 下下发。
	//
	// # 为什么详情页必须拿到它
	//
	// 双色球唯一要回答的问题是「我中了没有」,而那句话的完整形式是
	// **「我的号 ⟷ 开奖号」**。改造前这两半从来没有在同一屏出现过:详情页有
	// 开奖号、没有我的号(公开名单那张表对 ball 的「选项」列恒显示 -);
	// 「我的参与」有我的号、没有开奖号(myEntryView 不带 ball_result)。
	// 唯一并排的地方是「为什么是这个结果」弹窗 —— 在另一张标签页的一个图标
	// 按钮后面,而且要先整份拉证据链再用 WebCrypto 复算。项目方反馈的
	// 「已开奖的抽奖为什么不显示双色球号码」就是这个形状。
	//
	// # 为什么不是复用 /lottery/my-entries
	//
	// 那条是**跨活动**的分页列表,详情页要的是"这一场我的全部票",按活动过滤
	// 要么加一个只有一处调用方的查询参数、要么让前端翻页翻到为止。这里一次
	// 点查,与 my_entry_count 同一次请求 —— 两个数出自同一个事务视图,不会
	// 出现"卡片说我买了 3 张、下面只列出 2 张"。
	//
	// 非 ball 活动恒为空数组:pick 在那些模式下恒为空串,列出来一格内容都没有。
	MyTickets []myTicketView `json:"my_tickets"`
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

// myTicketView 是详情页上「我买的那几张票」。
//
// # 零值口径(先定义再用)
//
//   - Pick == ""      非双色球,或这张票根本没选号(不可能:acceptPick 拒绝空号)。
//   - WonKind == ""   这张票**没有任何派奖行**。它是否等于"没中",取决于活动
//     开没开出号码:ball_result != "" 才说明这一期真的开过奖
//     (取消/流局的场次 reveal 从未执行,ball_result 恒为空串)。
//     前端据此才敢写「未中奖」三个字 —— 在一场已取消的活动上写"未中奖",
//     等于把退款说成了输钱。
//   - WonTier == 0    同上,没有派奖行。奖级本身从 1 起。
type myTicketView struct {
	EntryNo string `json:"entry_no"`
	Seq     int    `json:"seq"`
	Pick    string `json:"pick"`
	Status  string `json:"status"`
	Amount  int64  `json:"amount"`
	// WonKind / WonTier / WonAmount 与 myEntryView.Won 同一个来源(qy_lot_payout),
	// 口径也完全一致:一张票最多一类出款,真出现两条时以派奖为准。
	WonKind   string `json:"won_kind"`
	WonTier   int    `json:"won_tier"`
	WonAmount int64  `json:"won_amount"`
}

// myTicketsCap 是详情页最多列出多少张我自己的票。
//
// 每人参与上限的硬顶是 perUserCapHard(500),把 500 张票塞进一张详情页既没人
// 读得完,也让这个响应体从"一屏"变成"一份报表"。超出的部分在「我的参与」里
// 分页看得到,那才是留得住的那一份;这里只回答"我这几张中了没有"。
const myTicketsCap = 50

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
// 按选择夹(lane)与分区(phase)过滤并定序。
//
// lane 是用户端大厅那三张选择夹(抽奖 / 竞猜 / 双色球,见 play.go),空串 =
// 不限。它**不是** kind:`lane=draw` 恰好排除双色球。
//
// 抽成一个吃 *gorm.DB 的函数是为了让这段口径**能被真的跑一遍数据库测到**:
// handler 走的是 db.Get() 那个只连 MySQL 的全局句柄,在测试里起不来,而
// "两张标签返回同一份列表"恰恰只在这段拼装里看得见 —— 上一版正是在这里
// 静默失效了一整个版本。
func hallQuery(gdb *gorm.DB, lane, phase string, set opSettings) (*gorm.DB, error) {
	// 未登记的选择夹一律 400,与 phase 同一条纪律:静默忽略会让 `lane=Ball`
	// 这种大小写笔误退回"三张标签拿同一份列表",而全链路没有任何一处报错 ——
	// 那正是上一版 phase 参数漂移能活过一整个版本的形状。
	if lane != "" {
		if _, ok := hallLanes[lane]; !ok {
			return nil, errBadLane
		}
	}
	// hidden_at > 0 是管理员的「下架」。它与草稿并列写在这一行,而不是散在
	// handler 里:大厅口径只有这一个执行点,加在别处迟早会有一条分支漏掉。
	q := gdb.Model(&Activity{}).Where("status <> ? AND hidden_at = ?", StatusDraft, 0)
	// 玩法隐藏与选择夹归属**在同一句 WHERE 里**一起落到 SQL 上。
	//
	// 两者都不能交给前端做:"只隐藏双色球"落不到任何一个 kind 上,而按选择夹
	// 分页更是必须在数据库里分 —— 前端过滤会让「双色球」那张标签的第 1 页
	// 只剩零星几条(整页被过滤掉的那些不会被补上),分页总数也是错的。
	if clause, args := playFilterClause(set, lane); clause != "" {
		q = q.Where(clause, args...)
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
		c.Query("lane"), c.Query("phase"), effectiveCtx(c.Request.Context()))
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
		MyTickets:           make([]myTicketView, 0, 4),
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
	detail.MaxPicksPerRequest = picksCapOf(act)

	// 尝试上限是**第二道**每人闸门,而且是唯一一道会把失败条目也算进去的。
	// 它多打一次 COUNT 而不是复用上面那一次:两次数的是不同的集合,合成一次
	// 查询要么少判一道闸门,要么把 my_entry_count 的口径改掉(那是回执与
	// "我的参与"共用的数)。只有配了这道闸门才去数 —— 绝大多数活动不配。
	var attempts int64
	if act.MaxAttemptsPerUser > 0 {
		if err := gdb.WithContext(ctx).Model(&Entry{}).
			Where("act_id = ? AND user_id = ?", act.Id, c.GetInt("id")).
			Count(&attempts).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("统计我的尝试次数", err))
			return
		}
	}
	// 夹到 0:上限被在线调低之后计数可以大过它,而一个负的"还能买几注"会在
	// 界面上显示成 "还能买 -2 注",并让任何 `remaining > 0` 的判断照旧为假、
	// `remaining >= n` 的判断在 n 也为负时反而为真。
	clampRemaining := func(cap int, used int64) int {
		if remaining := cap - int(used); remaining > 0 {
			return remaining
		}
		return 0
	}
	if act.MaxEntriesPerUser > 0 {
		remaining := clampRemaining(act.MaxEntriesPerUser, mine)
		detail.MyEntriesRemaining = &remaining
	}
	if act.MaxAttemptsPerUser > 0 {
		// 两道闸门取更紧的那一条:提交时它们是**并列**判定的(checkCaps 里
		// 一个 return errUserCap、一个 return errAttemptCap),报宽的那个数
		// 等于把另一道留到按下确认之后才说。
		remaining := clampRemaining(act.MaxAttemptsPerUser, attempts)
		if detail.MyEntriesRemaining == nil || remaining < *detail.MyEntriesRemaining {
			detail.MyEntriesRemaining = &remaining
		}
	}
	if act.MaxTotalEntries > 0 {
		// 与 checkCaps 读的是同一对计数器,判据也同一条:那里用
		// `active_count + pending_count > max_total_entries` 拒绝(pending 已含
		// 本次),所以**下一注之前**还剩的名额正是 max - active - pending。
		remaining := clampRemaining(act.MaxTotalEntries, int64(act.ActiveCount+act.PendingCount))
		detail.TotalEntriesRemaining = &remaining
	}

	// 双色球才列票:pick 在别的模式下恒为空串,列出来一格内容都没有。
	// 未登录不会走到这里(整条路由挂在 UserAuth 之下),但 me <= 0 仍然显式挡一道 ——
	// 一个 user_id = 0 的查询会把**别人的**票当成"我的"发出去。
	if act.DrawMode == DrawModeBall && c.GetInt("id") > 0 {
		tickets, err := loadMyBallTickets(ctx, gdb, act.Id, c.GetInt("id"))
		if err != nil {
			respondErr(c, err)
			return
		}
		detail.MyTickets = tickets
	}
	respondOK(c, detail)
}

// loadMyBallTickets 取"这一场我自己的票"及其派奖结果。
//
// 两次点查而不是 JOIN:与 handleListMyEntries 同一条口径(两张表都在索引上,
// 而两次点查比一次 JOIN 更容易在跨库三家上解释)。
func loadMyBallTickets(ctx context.Context, gdb *gorm.DB, actId int64, userId int) ([]myTicketView, error) {
	rows := make([]Entry, 0, 8)
	if err := gdb.WithContext(ctx).
		Where("act_id = ? AND user_id = ? AND status IN (?)",
			actId, userId, []string{EntryPending, EntrySuccess}).
		Order("seq asc").Limit(myTicketsCap).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("查询我的选号", err)
	}
	out := make([]myTicketView, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}

	payouts := make(map[int64]Payout, len(rows))
	list := make([]Payout, 0, len(rows))
	if err := gdb.WithContext(ctx).
		Where("entry_id IN (?)", entryIds(rows)).Find(&list).Error; err != nil {
		db.MarkFailure(err)
		return nil, wrapInternal("查询我的出款", err)
	}
	for _, p := range list {
		// 与 handleListMyEntries 逐字同一条:一张票最多一类出款,
		// 真出现两条时以派奖为准展示。
		if old, ok := payouts[p.EntryId]; ok && old.Kind != PayoutRefund {
			continue
		}
		payouts[p.EntryId] = p
	}

	for _, e := range rows {
		v := myTicketView{
			EntryNo: e.EntryNo, Seq: e.Seq, Pick: e.Pick,
			Status: e.Status, Amount: e.Amount,
		}
		if p, ok := payouts[e.Id]; ok {
			v.WonKind, v.WonTier, v.WonAmount = p.Kind, p.Tier, p.AmountQuota
		}
		out = append(out, v)
	}
	return out, nil
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
	Pick string `json:"pick"`
	// Picks 是**一次买多注**的选号列表,每一项与 Pick 同格式。
	//
	// 只有双色球用得上它:一注就是一组号,N 注就是 N 组号、N × 单注参与费,
	// 每一注各自进哈希链、各自与开奖号比对、各自定档 —— 与用户连点 N 次
	// 完全同构,区别只在于这 N 次在服务端串行跑完、总额在按下确认之前就
	// 已经写在屏幕上。
	//
	// 零值口径:缺席或空数组 = 单注提交,选号取 Pick(旧客户端与"只买一注"
	// 走的都是这一条)。两个字段同时非空一律 400(errPickAndPicks),
	// 绝不静默择一 —— 择一意味着有一半的请求买到的不是它写的那组号。
	//
	// 允许**重号**:同一次提交里两注号码完全相同照常受理,它们是两张独立的票,
	// 中奖时各拿一份。真实彩票就是这么卖的,而拒绝重号会让"机选 5 注"在小号池
	// 上有可观的概率整批被顶回来。
	Picks       []string `json:"picks"`
	PayPassword string   `json:"pay_password"`
}

// acceptPickList 定出这一次提交到底要买哪几注。
//
// 返回的每一项都是**尚未归一化**的原始输入:归一化在 acceptPick 里,而那是
// 唯一一处能算出进链字节的地方,复制一份到这里就等于给自己留了一个会漂移的
// 第二口径。这里只回答"几注、哪几组"。
//
// cap 由调用方从**这一场活动**上取(picksCapOf),不再是一个包级常量:同一个
// 站点上一场配 10、另一场配 999 是正常的,而一个写死的上界会在其中一场上说谎。
func acceptPickList(cap int, req entryRequest) ([]string, error) {
	if len(req.Picks) == 0 {
		// 单注:选号仍旧走 Pick。空串在非双色球上是合法的(不带号),
		// 在双色球上会被 acceptPick 判成 errBadPickInput —— 判定点仍然只有一处。
		//
		// 注意这一支**不看 cap**:cap 最小是 1(picksCapOf 把 0 读成默认 10,
		// 负数同理),所以单注提交在任何配置下都过得去。把它也拿去比一次的话,
		// 一个被改库改成 0 的活动会连单注都买不了,而那正是"零值 = 没配过"
		// 这条口径要防的事。
		return []string{req.Pick}, nil
	}
	if strings.TrimSpace(req.Pick) != "" {
		return nil, errPickAndPicks
	}
	if len(req.Picks) > cap {
		return nil, tooManyPicks(cap)
	}
	return req.Picks, nil
}

// batchRequestId 派生第 i 注的幂等键。
//
// 第 0 注**原样沿用**客户端那一份:一次单注提交因此与改造前逐字节相同,
// 旧客户端的重试照旧命中原单。第 i(i ≥ 1)注加 `#i` 后缀,于是"同一批的
// 每一注各是一张独立资金单",而整批重放时每一张各自幂等命中、一分钱都不会
// 重复扣 —— 这正是把批量放在服务端而不是让前端连打 N 次的理由:前端每一次
// 点击都要自己造一个新 crid,重发就是真的多扣一笔。
//
// # 它成立的前提:客户端那一份不含 `#`
//
// 这个映射只有在 crid 不含 `#` 时才是单射。否则 (`X#1`, 0) 与 (`X`, 1) 派生出
// 同一个键,后者会幂等命中前者那一次提交买下的票。前提由 handleCreateEntry
// 在**派生之前**校验(与长度同一道闸),所以这里不再重复判 —— 判两次意味着
// 两处口径,而这一处没有 error 可回。
func batchRequestId(crid string, i int) string {
	if i == 0 {
		return crid
	}
	return crid + "#" + strconv.Itoa(i)
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
		ClientIp:        common.ClientIP(c),
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

// entryReceiptBatch 是一次提交的回执,**单注与多注同一个形状**。
//
// # 为什么不是"一注时回单个对象、多注时回数组"
//
// 那样前端要按自己发了什么去决定怎么解析响应,而"我发了几注"与"服务端收下了
// 几注"恰恰是这条链路上唯一会不一致的两个数(余额不足、撞上每人上限、时间预算
// 用完都会让后半批停下)。一个恒定的形状让"买成了几注、一共扣了多少"必须被读出来,
// 而不是被假设。
//
// # 零值口径
//
//   - Entries 恒非空。整批一注都没买成时走 respondErr,而不是回一个 200 空数组 ——
//     一次什么都没发生的提交对用户来说就是失败。
//   - FailedCode == "" 表示 Requested 注全部买成,此时 Accepted == Requested。
//     非空时 Accepted < Requested,而**没买成的那几注一分钱都没扣**:每一注是
//     一张独立资金单,停在哪一注,后面的就从来没有落过单。
//   - TotalQuota 是 Σ Entries[].Amount,也就是这次真正扣掉的钱。它不是
//     "单注 × Requested" —— 那个数在部分成交时是错的,而错的方向是多报。
type entryReceiptBatch struct {
	Entries []entryReceipt `json:"entries"`
	// Requested 是提交的注数,Accepted 是买成的注数。
	Requested int `json:"requested"`
	Accepted  int `json:"accepted"`
	// TotalQuota 是本次真正扣掉的总额,由每一注的回执逐笔累加而来。
	TotalQuota int64 `json:"total_quota"`
	// FailedCode / FailedMessage 是**后面那几注**停下的原因,原样取自那条业务错误。
	FailedCode    string `json:"failed_code,omitempty"`
	FailedMessage string `json:"failed_message,omitempty"`
}

// handleCreateEntry 是唯一会动钱的用户入口,已挂 CriticalRateLimit。
//
// # 一次买多注在这里是 N 次串行的 ChargeEntry,不是一张 N 倍金额的资金单
//
// 后者要把 twophase 的 RefId(指向**一条**参与明细)、markEntrySuccess 的
// 状态 CAS、releaseEntryOnFailure 的回滚、补偿任务的 Resolver 全部改成
// "一张单对多条明细",而那四处是本模块唯一保证"钱与名单对得上"的地方。
// 串行 N 次则让每一注与改造前的单注参与**逐字节相同**:一张独立资金单、
// 一条链环、一个 seq、一份可复算的回执,批量只是把 N 次点击搬到了服务端。
//
// 代价是它不是原子的:第 k 注余额不足时前 k-1 注已经成交。这不是缺陷,是彩票
// 本来的样子(买到哪注算哪注),而响应里的 accepted / total_quota / failed_code
// 三个数就是把这件事说清楚的全部手段。
func handleCreateEntry(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	var req entryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	// 客户端携带的那一份挡在 64。ChargeEntry 自己认的上界比它大 4(要装下
	// 最长 `#998` 的后缀),所以这道校验必须在**派生之前**做一次,否则一个
	// 66 字符的 crid 会以单注身份合法通过、以多注身份把派生键顶到 70。
	//
	// `#` 同时被挡掉:它是**服务端派生位**的分隔符(见 batchRequestId),
	// 客户端也用它就会撞进同一个键空间 —— 用 `X#1` 买一注、再用 `X` 买两注,
	// 第二批的第 1 注派生出的正是 `X#1`,于是它幂等命中**上一次提交**的那张票,
	// 回执里混进一张不属于这次提交的票、total_quota 也多报一注。
	// 挡一个字符比给键空间做转义便宜得多:转义要么撑破 idem_key 的列宽,
	// 要么改掉单注那一份的取值(那会让旧客户端的重试不再命中原单)。
	//
	// 规范化的结果必须**往下传**,不能只用来做校验。此前这里 TrimSpace 之后
	// 只拿 crid 判长度与 `#`,派生键却用的是 req.ClientRequestId 的**原值**:
	// 于是一个尾部带空格的 crid,第 0 注 TrimSpace 之后幂等命中原单,
	// 第 1..N 注派生出 `"x #1"` 这种全新的键 —— 同一次提交多一个尾空格就是
	// 真的再扣一遍 N-1 注的钱(真 MySQL 上实测过)。
	//
	// 同时做大小写折叠与字符集收紧,理由见 qymodel.NormalizeIdemClientKey:
	// 抽奖这条路此前只挡长度与 `#`,连非 ASCII 都放行,而 MySQL 的默认排序
	// 规则重音不敏感 —— 'café' 与 'cafe' 在库里是同一个键。
	crid := strings.TrimSpace(req.ClientRequestId)
	if len(crid) > maxClientRequestID {
		respondErr(c, errBadRequestID)
		return
	}
	if crid != "" {
		folded, ok := qymodel.NormalizeIdemClientKey(crid)
		if !ok {
			// `#` 落在字符集之外,所以这一条同时把它挡掉了(它是服务端派生位
			// 的分隔符,客户端也用就会撞进同一个键空间)。
			respondErr(c, errBadRequestID)
			return
		}
		crid = folded
	}
	req.ClientRequestId = crid
	// 活动**先取**,注数上限后判:单次批量上限现在是每一场各配的
	// (max_picks_per_request,见 picksCapOf),不取活动就没有可比的那个数。
	// 顺序换过来的可见差别只有一处:一个打向不存在活动的超量请求现在回 404
	// 而不是 400 —— 那本来就是更准的答案。
	//
	// 这一段仍旧用冷路径预算:它只是几次点查。真正与注数成正比的那一段预算
	// 在下面单独取(entryBatchContext)。
	loadCtx, cancelLoad := guard.ColdContext(context.Background())
	defer cancelLoad()

	act, err := loadActivityByNo(loadCtx, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	picks, err := acceptPickList(picksCapOf(act), req)
	if err != nil {
		respondErr(c, err)
		return
	}

	// 验密闸门的位置有讲究:必须在解析请求体**之后**(密码随体来),
	// 且在 ChargeEntry **之前**(那里面就开始落单、扣钱了)。
	in := entryInputOf(c, act.ActNo, req)
	// 阈值判定必须用**本次真正要扣的金额**,不是活动的基准参与费:竞猜的投注额
	// 由用户自选,按 stake_quota 判定等于让一个基准费 1000、上限 500 万的盘口
	// 完全绕过二次验证 —— 而这道闸门存在的理由正是"盗号者能用参与把余额烧光"。
	amount, err := acceptAmount(act, in)
	if err != nil {
		respondErr(c, err)
		return
	}
	// 多注时判的是**整批的总额**,不是单注。按单注判等于给出一条把余额烧光的
	// 绕路:阈值 10 万、单注 2 万时,一次十注买走 20 万而一次密码都不问。
	// amount ≤ MaxQuota(int32)且 len(picks) ≤ maxPicksPerRequestHard(999),
	// 乘积上界约 2.1e12,远在 int64 之内。
	if PayPasswordRequired(amount * int64(len(picks))) {
		// Require 不通过时已写好响应并 Abort。它没有任何可以表达豁免的入参 ——
		// 想加豁免的人必须先改 paypass.Require 的签名,那是一次看得见的动作。
		if !paypass.Require(c, c.GetInt("id"), req.PayPassword) {
			return
		}
	}

	// 预算按注数给。一批 N 注是 N 次串行的 twophase.Execute,拿一次冷路径操作的
	// 3 秒去装 N 次,999 注会在第 86 注上下被截断 —— 也就是"活动可配到 999"
	// 在预算这一侧根本不成立。理由与上界见 entryBatchContext。
	ctx, cancel := entryBatchContext(context.Background(), len(picks))
	defer cancel()

	out := entryReceiptBatch{
		Entries:   make([]entryReceipt, 0, len(picks)),
		Requested: len(picks),
	}
	var stopped error
	for i, pick := range picks {
		// 预算在这里显式看一眼,而不是等 ChargeEntry 里的语句超时:后者会
		// 冒出一条 500「处理失败,请稍后重试」,而此刻已经有几注成交了 ——
		// 用户需要知道的是"买成了几注、剩下的没扣钱",不是一句内部错误。
		if ctx.Err() != nil {
			stopped = errBatchBudget
			break
		}
		one := in
		one.Pick = pick
		one.BatchIndex = i
		one.ClientRequestId = batchRequestId(in.ClientRequestId, i)
		entry, err := ChargeEntry(ctx, one)
		if err != nil {
			stopped = err
			break
		}
		out.Entries = append(out.Entries, entryReceipt{
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
		out.TotalQuota += entry.Amount
	}
	budgetGone := ctx.Err() != nil
	if stopped != nil {
		if _, ok := AsBizError(stopped); !ok {
			// 非业务错误不回显原文(可能带 SQL 片段与表结构),但**必须**在
			// 服务端留下全文:accepted < requested 却说不出为什么,比说错更难查。
			common.SysError(fmt.Sprintf(
				"qianye/lottery: 多注提交停在第 %d/%d 注(整批预算已用完=%t): %v",
				len(out.Entries)+1, out.Requested, budgetGone, stopped))
		}
	}
	// 一注都没成交 = 这次提交什么都没发生,原样把那条错误报出去。多注不该
	// 把"余额不足"降级成一个 200 —— 前端据 code 决定说哪句话,而这条路径上
	// 单注与多注该说的是同一句。
	//
	// **预算在第一注上就用完时走的也是这里。** 那一支必须与"买成了几注之后被
	// 截断"说同一句话:两者对用户是同一件事(一分钱没扣,再提交一次即可),
	// 而不加这道翻译的话它会变成一句 500「处理失败,请稍后重试」——
	// 用户读完的下一个动作是去查自己有没有被扣钱。
	if len(out.Entries) == 0 {
		respondErr(c, batchStopError(stopped, budgetGone))
		return
	}
	out.Accepted = len(out.Entries)
	if stopped != nil {
		out.FailedCode, out.FailedMessage = batchStopReason(stopped, budgetGone)
	}
	respondOK(c, out)
}

// batchStopReason 把"整批停在这里"翻译成回执上的 (code, message)。
//
// # 为什么单拎出来
//
// 它是这条链路上唯一一处"把一个内部错误翻译成用户读得懂的一句话"的判定,而它
// 的三个分支在真实运行里**出现的概率极不均匀**:业务错误(余额不足、撞上限)
// 天天有,预算耗尽要压着截止时刻才出现,内部错误几乎不出现。挂在 handler 里
// 只能靠"恰好把预算跑干"的端到端用例去碰,而那条路径落在哪一支取决于截止时刻
// 落在两条语句之间还是一条语句中间 —— 也就是说它测不确定。单拎出来之后
// 三个分支各有一行确定的用例(见 ball_picks_cap_db_test.go)。
//
// # budgetGone 是**本批的截止时刻**,不是错误文本
//
// 循环顶上那次 ctx.Err() 只在"上一注跑完之后才跨过截止时刻"时命中;预算在
// ChargeEntry **内部**某条语句上到期时,冒出来的是驱动自己包装的一个错误
// (各家包装方式还不一样,有的原样返回 context.DeadlineExceeded、有的换成
// 自己的字符串)。靠错误文本判断的结果是:同一件事在两条路径上被说成两句话,
// 其中一句是「处理失败,请稍后重试」—— 而用户此刻最需要知道的恰恰是
// "买成的那几注真的买成了、没买成的一分钱都没扣"。
func batchStopReason(stopped error, budgetGone bool) (code, message string) {
	if be, ok := AsBizError(batchStopError(stopped, budgetGone)); ok {
		return be.ErrCode(), be.Message()
	}
	// 非业务错误的原文可能带 SQL 片段与表结构,一律不回显;全文由调用方写进
	// 服务端日志。**但必须留下一个码**:accepted < requested 却说不出为什么,
	// 比说错更难查。
	return "qy_internal_error", "处理失败,请稍后重试"
}

// batchStopError 把"整批停在这里"归一成**一条业务错误**(归一不了就原样带出)。
//
// 两个调用方共用它,而它们是同一件事的两种落点:
//
//	accepted == 0  —— 这次提交什么都没发生,原样 respondErr;
//	accepted > 0   —— 200 + 信封,把码填进 failed_code。
//
// 共用一处是硬要求。分开写的表现是:预算在**第一注**上就用完时报一句 500
// 「处理失败,请稍后重试」,而在第五注上用完时报「只买成了前面几注,剩下的没有
// 扣费」—— 对用户是同一件事(一分钱没扣、再提交一次即可),却被说成了两句话,
// 其中一句会让他去查自己有没有被扣钱。
//
// 业务错误最优先:它自己就是最准的那句话,哪怕此刻预算也恰好用完了
// (余额不足就是余额不足,不该被改口成"超时了" —— 那会让用户去重试一次
// 必然再失败的提交)。
func batchStopError(stopped error, budgetGone bool) error {
	if _, ok := AsBizError(stopped); ok {
		return stopped
	}
	if budgetGone {
		return errBatchBudget
	}
	return stopped
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
	Pick string `json:"pick,omitempty"`
	// DrawMode / BallResult 让这张表能把「我的号」与「开奖号」放在同一行上。
	//
	// # 这两个字段此前刻意不在这里,现在为什么必须在
	//
	// 原口径是「玩法与开奖号都从证据链里现算,列表行上再放一份就多一个会漂移
	// 的取值」。方向是对的,代价却落在了唯一要紧的那句话上:这张表是用户查
	// 「我中了没有」的地方,而改造前它只有我的号、没有开奖号 —— 想知道中没中,
	// 得逐行点开「为什么是这个结果」,那里会整份拉一次证据链再用 WebCrypto 复算。
	// 一页 20 行就是 20 次下载与 20 次复算,实际结果是没有人会去点。
	//
	// 漂移风险由**位置**而不是由缺席来控制:这里的 ball_result 与详情页、
	// 证据链读的是同一行活动记录(qy_lot_activity.ball_result,开奖那一刻写入、
	// 此后只读),不存在第二个写入点;而「为什么是这个结果」弹窗仍然一个数字
	// 都不信后端 —— 它照旧从公开种子当场重摇。两条路算出不同的号,正是那个
	// 弹窗要抓的东西,把号摆在列表上只会让它更容易被发现,而不是更难。
	//
	// 非双色球恒为空串,omitempty 让老前端与脚本客户端的响应体一个字节都不变。
	DrawMode   string   `json:"draw_mode,omitempty"`
	BallResult string   `json:"ball_result,omitempty"`
	Won        *wonView `json:"won"`
	CreatedAt  int64    `json:"created_at"`
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
			v.DrawMode, v.BallResult = a.DrawMode, a.BallResult
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
