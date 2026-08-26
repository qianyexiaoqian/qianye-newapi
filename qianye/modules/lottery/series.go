package lottery

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// series.go —— 双色球的期次系列:初始池子的入账、封顶、跨期滚存与结算。
//
// # 池子是发行上限计数器,不是托管账户
//
// new-api 里额度是**发行**出来的,不存在一个持有余额的平台账户;派奖对
// users.quota 本来就是净增发。给池子做真实托管要引入平台主体、跨库资金归属、
// 一条全新的"平台→用户"资金方向,并在 qy_fund_orders 里造出对不上任何真实
// 主体的假记录。所以注资 = 在系列行上原子累加一个计数器 + 写事件 + 写审计,
// **不触碰任何用户的 quota**。
//
// # 平台的累计损失上界(这是本文件存在的主要理由)
//
// 单期闸门 max_total_prize_quota 拦不住滚存:管理员每期注资到上限、连开 N 期
// 无人中奖,池子滚到 N 倍,某一期一次性发出去 —— 而每一期看起来都守住了。
//
// 解法是把上限**冻结在系列行上**:创建系列时管理员填 issue_cap_quota,
// 校验它 ≤ 当时生效的 max_total_prize_quota;此后每一次注资都走条件 UPDATE
//
//	WHERE id = ? AND seed_total_quota + ? <= issue_cap_quota
//
// 并断言 RowsAffected == 1。冻结在行上而不是每次读配置,是因为后者意味着
// "改一次运营端设置就能追认已经发生的超发"。
//
// 由此可证:
//
//	pool_carry 恒 ≥ 0
//	每期净增发 = paid(n) − stake_total(n) ≤ pool_open(n) − stake_total(n)
//	跨全部期次累加,滚存项两两抵消
//	⇒ Σ净增发 ≤ Σ pool_seed(n) ≤ issue_cap_quota ≤ max_total_prize_quota
//
// 即:一整个系列、无论开多少期、无论运气多差,平台的累计净增发不超过管理员
// 显式注资的总和,而这个总和不超过一场**普通抽奖**已经被允许的额度。
// 这比现状严格更紧 —— 今天管理员可以并发办 max_active_activities 场活动,
// 每场各吃一个 max_total_prize_quota。
//
// **零新增 YAML 键**:上面这条论证不需要任何新配置项。

// Series 是一个双色球系列(号池 + 跨期滚存的奖池 + 累计发行闸门)。永不清理。
//
// 它不是证据表,但它是资金闸门表:删一行等于把一个系列的累计注资上限抹掉。
type Series struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// SeriesNo 由 crypto/rand 生成,与 act_no 同形。它进每期的 commit 原像。
	SeriesNo string `json:"series_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_lot_series_no"`
	Title    string `json:"title" gorm:"type:varchar(120);not null;default:''"`

	// 号池四元组,期与期之间**不可变**:可变的号码空间等于可变的中奖概率,
	// 而"各档概率是组合数算出来的、不用相信平台"正是双色球唯一但决定性的优势。
	RedPool  int `json:"red_pool" gorm:"not null;default:0"`
	RedPick  int `json:"red_pick" gorm:"not null;default:0"`
	BluePool int `json:"blue_pool" gorm:"not null;default:0"`
	BluePick int `json:"blue_pick" gorm:"not null;default:0"`
	// PoolShareBps 是本期投注额进入奖池的万分比,其余是平台手续费。
	// 每期发布时冻结进活动行与承诺原像。
	PoolShareBps int `json:"pool_share_bps" gorm:"not null;default:0"`

	// PoolQuota 是**当前尚未被任何一期取走**的池子。发布一期时整块取走并清零,
	// 结算那一期时把未派出的部分加回来。用乐观锁 CAS 更新。
	PoolQuota int64 `json:"pool_quota" gorm:"not null;default:0"`
	// PendingSeedQuota 是自上一期发布以来的新注资,仅用于把 pool_open 拆成
	// "本期新注资 / 历史滚存"两个可公示的数。发布时随 PoolQuota 一起清零。
	PendingSeedQuota int64 `json:"pending_seed_quota" gorm:"not null;default:0"`

	// SeedTotalQuota 是本系列**累计注资**,只增不减,是封顶判定的左值。
	SeedTotalQuota int64 `json:"seed_total_quota" gorm:"not null;default:0"`
	// PaidTotalQuota 是本系列累计派出(按计划口径,见 settleSeriesPool)。
	PaidTotalQuota int64 `json:"paid_total_quota" gorm:"not null;default:0"`
	// IssueCapQuota 在创建时冻结,此后**任何接口都不能改它**。
	IssueCapQuota int64 `json:"issue_cap_quota" gorm:"not null;default:0"`

	IssueSeq int    `json:"issue_seq" gorm:"not null;default:0"`
	Status   string `json:"status" gorm:"type:varchar(12);not null;default:''"`

	CreatedBy int   `json:"created_by" gorm:"not null;default:0"`
	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Series) TableName() string { return "qy_lot_series" }

// 系列状态。closed 之后不能再注资、不能再发新期,滚存余额作废并公示。
const (
	SeriesOpen   = "open"
	SeriesClosed = "closed"
)

const prefixSeries = "LS"

func newSeriesNo() string { return newNo(prefixSeries) }

var (
	errSeriesNotFound = newBizError(http.StatusNotFound, "qy_lot_series_not_found", "期次系列不存在")
	errSeriesClosed   = newBizError(http.StatusConflict, "qy_lot_series_closed",
		"该期次系列已关闭,不能再注资或开新一期")
	errSeriesCap = newBizError(http.StatusBadRequest, "qy_lot_series_cap",
		"本次注资会让本系列的累计注资超过创建时冻结的发行上限")
	errSeriesBusy = newBizError(http.StatusConflict, "qy_lot_series_busy",
		"该期次系列正在被另一次操作修改,请刷新后重试")
	errSeriesPoolShort = newBizError(http.StatusBadRequest, "qy_lot_series_pool_short",
		"奖级表的最坏支出超过本期开局池子,请先注资或调低奖级")
	// errSeriesPoolCeiling 与 errSeriesPoolShort 是**相反**的两件事,必须分开报。
	//
	// pool_short 说的是"池子不够大",处置是注资;这一条说的是"池子已经太大",
	// 而注资只会让它更糟。合成一个码的代价实测过:一个滚存已经越过上界的系列
	// 发布下一期时报 pool_short,文案写着「请先注资或调低奖级」—— 运营照做,
	// 池子再涨一截,而任何奖级配置都救不了(判据 open > MaxQuota 是无条件的)。
	errSeriesPoolCeiling = newBizError(http.StatusBadRequest, "qy_lot_series_pool_ceiling",
		"本系列的滚存池已达额度上界,不能再注资 —— 再注资会让这个系列永久开不出新一期。"+
			"请先开出几期把池子发下去,或关闭本系列")
	errBadPickInput = newBizError(http.StatusBadRequest, "qy_lot_bad_pick",
		"选号不合法:号码个数、取值范围或重复号有误")
	errPickNotAllowed = newBizError(http.StatusBadRequest, "qy_lot_pick_not_allowed",
		"本场活动不接受选号")
	// errPickAndPicks 挡住"同时带 pick 与 picks"。
	//
	// 不做静默择一:两个字段说的是同一件事,而择一意味着有一半的请求买到的
	// 不是它写的那组号 —— 这与 acceptPick 对"非双色球带号"一律拒绝是同一条口径。
	errPickAndPicks = newBizError(http.StatusBadRequest, "qy_lot_pick_conflict",
		"pick 与 picks 不能同时提交,多注请只用 picks")
	// errBatchBudget 是多注提交被时间预算截断时的诚实回答。
	//
	// 它只可能出现在"前面几注已经买成"的响应里(accepted > 0),因此文案的
	// 第一件事是让用户知道**没买成的那几注一分钱都没扣** —— 否则他会以为
	// 自己被扣了全额却只拿到一半的票。
	errBatchBudget = newBizError(http.StatusConflict, "qy_lot_batch_budget",
		"本次提交在处理超时前只买成了前面几注,剩下的没有扣费,可以再提交一次")
)

// ─────────────────────────── 池子的三个原子动作 ───────────────────────────

// fundSeriesPool 给系列注资。**这是平台累计发行上限的唯一执行点。**
//
// 条件 UPDATE 把封顶判定与写入放在同一条语句里:先 SELECT 再判断再 UPDATE
// 会在两个管理员同时注资时各自读到旧值、各自通过校验,而那正好是这道闸门
// 唯一需要防的场景。
func fundSeriesPool(tx *gorm.DB, seriesId, amount int64) error {
	now := common.GetTimestamp()
	// 滚存上界与发行上限一样必须写进**同一条**条件 UPDATE:先 SELECT 再判断
	// (checkFundable)只负责给出精确的错误文案,两个管理员同时注资时各自读到
	// 的都是旧值,只有这条语句里的条件才是执行点。
	res := tx.Model(&Series{}).
		Where("id = ? AND status = ? AND seed_total_quota + ? <= issue_cap_quota AND pool_quota + ? <= ?",
			seriesId, SeriesOpen, amount, amount, int64(common.MaxQuota)).
		Updates(map[string]any{
			"seed_total_quota":   gorm.Expr("seed_total_quota + ?", amount),
			"pool_quota":         gorm.Expr("pool_quota + ?", amount),
			"pending_seed_quota": gorm.Expr("pending_seed_quota + ?", amount),
			"updated_at":         now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		// 分不清"已关闭"与"超上限"在这里是**故意**的反面:两者的处置完全不同,
		// 所以调用方在这一步之前已经读过一次行并给出了精确的错误;走到这里
		// 说明是并发,统一报冲突让人重试。
		return errSeriesBusy
	}
	return nil
}

// claimSeriesPool 把系列池整块取走冻结成一期的开局池,并分配期号。
//
// 必须在 publish 的同一个事务里:pool_open / pool_seed / pool_carry / issue_no
// 全部进 commit 原像,取走之后承诺才算得出来,而承诺一旦落库就不可再改。
//
// 取走之后系列池归零,发布期间的新注资照常累加,滚进**下一期** ——
// 这样"本期池子是多少"在开放期是一个不会变的数,用户看到的和承诺里的是同一个。
func claimSeriesPool(tx *gorm.DB, act *Activity) error {
	var s Series
	if err := tx.Where("id = ?", act.SeriesId).Take(&s).Error; err != nil {
		return errSeriesNotFound
	}
	if s.Status != SeriesOpen {
		return errSeriesClosed
	}

	seed := s.PendingSeedQuota
	if seed > s.PoolQuota || seed < 0 {
		// 只可能来自直接改库。取保守的一边:全部计入滚存,不虚报"本期新注资"。
		seed = 0
	}
	now := common.GetTimestamp()
	res := tx.Model(&Series{}).
		Where("id = ? AND status = ? AND pool_quota = ? AND pending_seed_quota = ? AND issue_seq = ?",
			s.Id, SeriesOpen, s.PoolQuota, s.PendingSeedQuota, s.IssueSeq).
		Updates(map[string]any{
			"pool_quota":         0,
			"pending_seed_quota": 0,
			"issue_seq":          s.IssueSeq + 1,
			"updated_at":         now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errSeriesBusy
	}

	act.SeriesNo = s.SeriesNo
	act.IssueNo = s.IssueSeq + 1
	act.PoolSeedQuota = seed
	act.PoolCarryQuota = s.PoolQuota - seed
	act.PoolOpenQuota = s.PoolQuota
	act.PoolShareBps = s.PoolShareBps
	act.BallRedPool = s.RedPool
	act.BallRedPick = s.RedPick
	act.BallBluePool = s.BluePool
	act.BallBluePick = s.BluePick
	return nil
}

// settleSeriesPool 把一期没派出去的池子还回系列。**必须在活动收尾的同一个事务里。**
//
// 幂等靠调用方:它只在 settling → finished 那次 CAS 成功之后被调用,
// 而那次 CAS 在整个活动生命周期里最多成功一次。
//
// committed 用的是**计划口径**(Σ 已登记的派奖计划)而不是"已经真的发出去了
// 多少":一笔转人工的 held 出款事后仍可能被管理员重试成功,若按已付口径把它
// 还回池子,那笔钱就会被发两次 —— 一次给中奖者,一次留在池子里等着发给下一期
// 的人。按计划口径还回去的方向是安全的:最坏情况是池子少了一点,平台的累计
// 净增发只会更小。
func settleSeriesPool(tx *gorm.DB, act *Activity, committed int64) error {
	if act.DrawMode != DrawModeBall || act.SeriesId == 0 {
		return nil
	}
	open := act.PoolOpenQuota + ballPoolIn(act)
	if committed < 0 || committed > open {
		// 守恒式不成立就整个事务回滚 —— 宁可让活动停在 settling 等人,
		// 也绝不把一个对不上的数写回系列池。
		return ErrPoolNotConserved
	}
	carry := open - committed
	now := common.GetTimestamp()
	res := tx.Model(&Series{}).Where("id = ?", act.SeriesId).
		Updates(map[string]any{
			"pool_quota":       gorm.Expr("pool_quota + ?", carry),
			"paid_total_quota": gorm.Expr("paid_total_quota + ?", committed),
			"updated_at":       now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errSeriesBusy
	}
	return nil
}

// ballPoolIn 是本期投注额进入奖池的部分。
//
// 全额退款的四种收场下恒为 0:那时用户的本金原样退回,从来没有进过池子。
func ballPoolIn(act *Activity) int64 {
	if act.PoolShareBps <= 0 || act.PoolQuota <= 0 || isFullRefundOutcome(act.Outcome) {
		return 0
	}
	return act.PoolQuota * int64(act.PoolShareBps) / 10000
}

// ballPoolOpen 是本期真正可派发的池子:开局基数 + 本期投注入池部分。
//
// 验证脚本按同一个式子从公开数据(pool_open_quota、pool_quota、pool_share_bps)
// 复算,因此这三个数必须原样出现在证据链里。
func ballPoolOpen(act *Activity) int64 { return act.PoolOpenQuota + ballPoolIn(act) }

// ─────────────────────────── 管理端接口 ───────────────────────────

type seriesInput struct {
	Title    string `json:"title"`
	RedPool  int    `json:"red_pool"`
	RedPick  int    `json:"red_pick"`
	BluePool int    `json:"blue_pool"`
	BluePick int    `json:"blue_pick"`
	// PoolShareBps 是投注入池比例,0 表示投注全部归平台、池子只靠注资。
	PoolShareBps int `json:"pool_share_bps"`
	// IssueCapQuota 是本系列**累计注资**的上限,创建时冻结,此后不可修改。
	IssueCapQuota int64 `json:"issue_cap_quota"`
	// SeedQuota 是创建时的首笔注资,可为 0(之后再注)。
	SeedQuota int64 `json:"seed_quota"`
}

// handleCreateSeries 创建一个双色球系列。
func handleCreateSeries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	var in seriesInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "lottery.series.create", "", qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	s, err := buildSeries(ctx, &in, c.GetInt("id"))
	if err != nil {
		writeAdminAudit(c, "lottery.series.create", "", qymodel.ResultFail,
			auditReason(err), "", snapText(seriesSnapshotMap(nil, &in)))
		respondErr(c, err)
		return
	}

	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(s).Error; err != nil {
			return err
		}
		if in.SeedQuota > 0 {
			if err := fundSeriesPool(tx, s.Id, in.SeedQuota); err != nil {
				return err
			}
			s.PoolQuota = in.SeedQuota
			s.PendingSeedQuota = in.SeedQuota
			s.SeedTotalQuota = in.SeedQuota
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     s.SeriesNo,
			Category:    auditCategory,
			Action:      "lottery.series.create",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			AfterSnap:   snapText(seriesSnapshotMap(s, &in)),
		})
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("创建期次系列", err)
		}
		writeAdminAudit(c, "lottery.series.create", "", qymodel.ResultFail,
			auditReason(err), "", snapText(seriesSnapshotMap(s, &in)))
		respondErr(c, err)
		return
	}
	respondOK(c, seriesView(s))
}

func buildSeries(ctx context.Context, in *seriesInput, createdBy int) (*Series, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || utf8.RuneCountInString(title) > 60 {
		return nil, errBadRequest("系列标题必填且不超过 60 个字")
	}
	if err := ValidateBallPool(in.RedPool, in.RedPick, in.BluePool, in.BluePick); err != nil {
		return nil, errBadRequest("号池配置不合法:红球池 2..36 选 1..8、蓝球池 1..16 选 1..2,且选数不超过池大小")
	}
	if in.PoolShareBps < 0 || in.PoolShareBps > 10000 {
		return nil, errBadRequest("投注入池比例必须落在 [0, 10000]")
	}
	// 发行上限必须为正:它是本系列累计注资的**唯一**封顶,fundSeriesPool 的
	// 条件 UPDATE 只认它。0 在这里不是"不限",而是"这个系列一分钱都注不进去" ——
	// 与 max_stake_quota / max_total_prize_quota 那三项的 0 语义相反,
	// 因为它不是一道站点级的闸门,而是运营为这一个系列亲手写下的预算。
	if in.IssueCapQuota <= 0 {
		return nil, errBadRequest("发行上限必须大于 0 —— 它是本系列累计注资的唯一封顶")
	}
	// 站点自选的硬顶,0 = 不限(默认)。原先这里无条件夹进 max_total_prize_quota,
	// 理由是"复用一个已经存在的闸门";那个闸门现在默认不设,复用也就落空了。
	set := effectiveCtx(ctx)
	if set.MaxTotalPrizeQuota > 0 && in.IssueCapQuota > set.MaxTotalPrizeQuota {
		return nil, prizeCapExceeded(in.IssueCapQuota, set.MaxTotalPrizeQuota)
	}
	// 系列这一侧**不需要**二次确认:它的累计注资被下面那条 int32 夹死在
	// common.MaxQuota 以内,而奖品档是 amount × count,count 才是让它变成
	// 无上界的那个乘数。这里的手滑最多是一个 int32 上界的数,量级封顶。
	// 再夹一次额度上界:池子最终要过 twophase 的单笔 amount ≤ MaxQuota,而
	// checkBallPoolCovers 对 open > MaxQuota 的处置是**拒绝发布新一期** ——
	// 一个配得过大的 issue_cap 会让系列在注满之后永久开不出新期,且没有任何
	// 接口能把池子降回来。拦在创建期,那是唯一还能改的时刻。
	if in.IssueCapQuota > int64(common.MaxQuota) {
		return nil, errBadRequest(quotaColumnCeilingText("发行上限") +
			"。越过它的系列在注满之后会永久开不出新一期,而且没有任何接口能把池子降回来")
	}
	if in.SeedQuota < 0 || in.SeedQuota > in.IssueCapQuota {
		return nil, errSeriesCap
	}

	now := common.GetTimestamp()
	return &Series{
		SeriesNo:      newSeriesNo(),
		Title:         title,
		RedPool:       in.RedPool,
		RedPick:       in.RedPick,
		BluePool:      in.BluePool,
		BluePick:      in.BluePick,
		PoolShareBps:  in.PoolShareBps,
		IssueCapQuota: in.IssueCapQuota,
		Status:        SeriesOpen,
		CreatedBy:     createdBy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

type fundRequest struct {
	Amount int64  `json:"amount"`
	Note   string `json:"note"`
}

// handleFundSeries 给系列注资。**平台唯一一处主动扩大发行上限的动作。**
func handleFundSeries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	seriesNo := c.Param("series_no")
	var req fundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.series.fund", seriesNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	s, err := loadSeriesByNo(ctx, gdb, seriesNo)
	if err != nil {
		writeAdminAudit(c, "lottery.series.fund", seriesNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	if err := checkFundable(s, req.Amount); err != nil {
		writeAdminAudit(c, "lottery.series.fund", seriesNo, qymodel.ResultFail, auditReason(err),
			snapText(seriesSnapshotMap(s, nil)), "")
		respondErr(c, err)
		return
	}

	before := snapText(seriesSnapshotMap(s, nil))
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := fundSeriesPool(tx, s.Id, req.Amount); err != nil {
			return err
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     s.SeriesNo,
			Category:    auditCategory,
			Action:      "lottery.series.fund",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			Reason:      audit.Truncate(strings.TrimSpace(req.Note), 500),
			BeforeSnap:  before,
			AfterSnap: snapText(map[string]any{
				"series_no":  s.SeriesNo,
				"amount":     req.Amount,
				"seed_total": s.SeedTotalQuota + req.Amount,
				"issue_cap":  s.IssueCapQuota,
			}),
		})
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("系列注资", err)
		}
		writeAdminAudit(c, "lottery.series.fund", seriesNo, qymodel.ResultFail,
			auditReason(err), before, "")
		respondErr(c, err)
		return
	}

	fresh, err := loadSeriesByNo(ctx, gdb, seriesNo)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, seriesView(fresh))
}

// checkFundable 在条件 UPDATE 之前给出**精确**的失败原因。
//
// 条件 UPDATE 自己只能回答"没生效",而"已关闭"与"超上限"对运营的处置完全不同。
// 它不是执行点(并发下仍以那条 UPDATE 为准),只是错误信息的来源。
func checkFundable(s *Series, amount int64) error {
	if amount <= 0 {
		return errBadRequest("注资额度必须大于 0")
	}
	if amount > int64(common.MaxQuota) {
		return errBadRequest(quotaColumnCeilingText("注资额度"))
	}
	if s.Status != SeriesOpen {
		return errSeriesClosed
	}
	if s.SeedTotalQuota+amount > s.IssueCapQuota {
		return errSeriesCap
	}
	// 发行上限管的是**平台注资**,管不住滚存。
	//
	// pool_quota 除了注资还会从 settleSeriesPool 收回用户投注的入池部分
	// (carry = open − committed),所以它可以在 seed_total 远低于 issue_cap
	// 的情况下越过 common.MaxQuota。越过之后这个系列**永久开不出新一期**:
	// checkBallPoolCovers 的 `open > MaxQuota` 是无条件的,任何奖级配置都救不了;
	// 而唯一还能按的按钮(关闭系列)会把整池作废 —— 其中包含真实的用户投注。
	// 实测复现过:issue_cap 顶格 + 一注真实投注滚存之后再注资一次,
	// pool_quota = MaxQuota + 1,此后第 2 期发布恒定 400,没有任何接口能把池子降回来。
	//
	// 所以闸门必须落在**注资**这一刻 —— 那是这条链上唯一还能改的时刻。
	if s.PoolQuota+amount > int64(common.MaxQuota) {
		return errSeriesPoolCeiling
	}
	return nil
}

type closeSeriesRequest struct {
	Reason string `json:"reason"`
}

// handleCloseSeries 关闭系列。滚存余额作废,**这一条必须写在活动规则页上**。
//
// 它从来不是任何用户的钱:每一期的投注在当期就已完成再分配(入池部分归池子、
// 其余是平台手续费),滚存只是平台尚未发出去的发行额度。但用户不会自己想到
// 这一点,所以规则页要事前写清,而不是等争议时才解释。
func handleCloseSeries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	seriesNo := c.Param("series_no")
	var req closeSeriesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeAdminAudit(c, "lottery.series.close", seriesNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > 200 {
		writeAdminAudit(c, "lottery.series.close", seriesNo, qymodel.ResultFail, "未填写关闭原因", "", "")
		respondErr(c, errBadRequest("必须填写关闭原因,滚存余额会作废并对参与者公示"))
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	s, err := loadSeriesByNo(ctx, gdb, seriesNo)
	if err != nil {
		writeAdminAudit(c, "lottery.series.close", seriesNo, qymodel.ResultFail, auditReason(err), "", "")
		respondErr(c, err)
		return
	}
	before := snapText(seriesSnapshotMap(s, nil))

	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Series{}).
			Where("id = ? AND status = ?", s.Id, SeriesOpen).
			Updates(map[string]any{"status": SeriesClosed, "updated_at": common.GetTimestamp()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errSeriesClosed
		}
		return audit.WriteTx(tx, audit.Entry{
			TraceNo:     s.SeriesNo,
			Category:    auditCategory,
			Action:      "lottery.series.close",
			ActorType:   qymodel.ActorAdmin,
			ActorUserId: c.GetInt("id"),
			ActorName:   c.GetString("username"),
			Result:      qymodel.ResultOK,
			Reason:      audit.Truncate(reason, 500),
			BeforeSnap:  before,
			AfterSnap: snapText(map[string]any{
				"series_no": s.SeriesNo, "status": SeriesClosed,
				"forfeited_pool_quota": s.PoolQuota,
			}),
		})
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("关闭期次系列", err)
		}
		writeAdminAudit(c, "lottery.series.close", seriesNo, qymodel.ResultFail,
			auditReason(err), before, "")
		respondErr(c, err)
		return
	}
	respondOK(c, gin.H{"series_no": s.SeriesNo, "status": SeriesClosed})
}

// handleAdminListSeries 列出全部系列,附三个运营最需要的数。
func handleAdminListSeries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	page, size := httpq.Paginate(c, listPaging)
	var total int64
	if err := gdb.WithContext(ctx).Model(&Series{}).Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计期次系列", err))
		return
	}
	rows := make([]Series, 0, size)
	err := gdb.WithContext(ctx).Order("id desc").
		Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error
	if err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询期次系列", err))
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		items = append(items, seriesView(&rows[i]))
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}

// handleGetSeries 是用户侧的期次详情:当前池子、号池、上期开奖号。
//
// **不下发任何"各档中奖概率"的数字**:那是组合数的结果,前端自己按红蓝池
// 大小算就行。后端下发等于把双色球唯一的优势(概率不需要相信平台)丢掉。
func handleGetSeries(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	s, err := loadSeriesByNo(ctx, gdb, c.Param("series_no"))
	if err != nil {
		respondErr(c, err)
		return
	}

	var last Activity
	lastView := map[string]any{}
	err = gdb.WithContext(ctx).
		Where("series_id = ? AND ball_result <> ?", s.Id, "").
		Order("issue_no desc").Limit(1).Take(&last).Error
	if err == nil {
		lastView = map[string]any{
			"act_no":      last.ActNo,
			"issue_no":    last.IssueNo,
			"ball_result": last.BallResult,
			"revealed_at": last.RevealedAt,
		}
	}

	var current Activity
	currentView := map[string]any{}
	err = gdb.WithContext(ctx).
		Where("series_id = ? AND status IN ?", s.Id,
			[]string{StatusPublished, StatusLocked}).
		Order("issue_no asc").Limit(1).Take(&current).Error
	if err == nil {
		currentView = map[string]any{
			"act_no":          current.ActNo,
			"issue_no":        current.IssueNo,
			"status":          current.Status,
			"close_at":        current.CloseAt,
			"draw_at":         current.DrawAt,
			"pool_open_quota": ballPoolOpen(&current),
			"pool_seed_quota": current.PoolSeedQuota,
			"pool_carry":      current.PoolCarryQuota,
			"pool_share_bps":  current.PoolShareBps,
		}
	}

	respondOK(c, gin.H{
		"series_no":  s.SeriesNo,
		"title":      s.Title,
		"status":     s.Status,
		"red_pool":   s.RedPool,
		"red_pick":   s.RedPick,
		"blue_pool":  s.BluePool,
		"blue_pick":  s.BluePick,
		"pool_quota": s.PoolQuota,
		"issue_seq":  s.IssueSeq,
		"current":    currentView,
		"last":       lastView,
	})
}

func loadSeriesByNo(ctx context.Context, gdb *gorm.DB, seriesNo string) (*Series, error) {
	if strings.TrimSpace(seriesNo) == "" {
		return nil, errSeriesNotFound
	}
	var s Series
	err := gdb.WithContext(ctx).Where("series_no = ?", seriesNo).Take(&s).Error
	if err != nil {
		if gorm.ErrRecordNotFound == err {
			return nil, errSeriesNotFound
		}
		db.MarkFailure(err)
		return nil, wrapInternal("查询期次系列", err)
	}
	return &s, nil
}

// seriesView 把系列投影成管理端要的形状,含"本系列剩余可注资"。
func seriesView(s *Series) map[string]any {
	return map[string]any{
		"series_no":        s.SeriesNo,
		"title":            s.Title,
		"status":           s.Status,
		"red_pool":         s.RedPool,
		"red_pick":         s.RedPick,
		"blue_pool":        s.BluePool,
		"blue_pick":        s.BluePick,
		"pool_share_bps":   s.PoolShareBps,
		"pool_quota":       s.PoolQuota,
		"seed_total_quota": s.SeedTotalQuota,
		"paid_total_quota": s.PaidTotalQuota,
		"issue_cap_quota":  s.IssueCapQuota,
		"headroom_quota":   s.IssueCapQuota - s.SeedTotalQuota,
		"issue_seq":        s.IssueSeq,
		"created_at":       s.CreatedAt,
		"updated_at":       s.UpdatedAt,
	}
}

func seriesSnapshotMap(s *Series, in *seriesInput) map[string]any {
	m := map[string]any{}
	if s != nil {
		m["series_no"] = s.SeriesNo
		m["title"] = s.Title
		m["status"] = s.Status
		m["issue_cap_quota"] = s.IssueCapQuota
		m["seed_total_quota"] = s.SeedTotalQuota
		m["pool_quota"] = s.PoolQuota
		m["issue_seq"] = s.IssueSeq
	}
	if in != nil {
		m["red_pool"] = in.RedPool
		m["red_pick"] = in.RedPick
		m["blue_pool"] = in.BluePool
		m["blue_pick"] = in.BluePick
		m["pool_share_bps"] = in.PoolShareBps
		m["seed_quota"] = in.SeedQuota
	}
	return m
}
