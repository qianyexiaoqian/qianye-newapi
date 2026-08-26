package lottery

import (
	"context"
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// ball_admin.go —— 双色球活动在创建期与发布期的全部校验。
//
// 分成独立文件而不是塞进 buildPrizes:双色球的奖级表与抽奖的奖档表除了
// "都叫 tier"之外没有任何共同校验,混在一个函数里会长出一串
// "这个字段在这个模式下不看"的注释,而那正是最容易在重构里被忽略的一类约定。

// applyBallSpec 把双色球的奖级条件写进奖档行,并重算 spec 原像。
//
// 它跑在 buildPrizes 之后:那一步已经把通用校验(等级唯一、名称、数量、
// 单档与总额度闸门)做完了,这里只补 ball 独有的那几条,然后**整体重算**
// spec 原像的逐行 —— 因为 red_match / blue_match / pool_share_bps 也在原像里。
func applyBallSpec(ctx context.Context, act *Activity, in *activityInput, rows []Prize) ([]Prize, []string, error) {
	s, err := resolveBallSeries(ctx, act, in)
	if err != nil {
		return nil, nil, err
	}

	cfg := config.Get().Lottery
	entriesCap := act.MaxTotalEntries
	if entriesCap <= 0 {
		entriesCap = cfg.MaxTotalEntriesHard
	}

	byTier := make(map[int]prizeInput, len(in.Prizes))
	for _, p := range in.Prizes {
		byTier[p.Tier] = p
	}

	var shareSum int
	tiers := make([]BallTier, 0, len(rows))
	for i := range rows {
		src := byTier[rows[i].Tier]
		if rows[i].Type() != PrizeTypeQuota {
			// 文本奖在双色球里本轮不做:它与浮动奖池的"按比例摊薄"在语义上
			// 冲突(兑换码劈不开),而"固定份数的文本奖 + 跨期滚存池"是两套
			// 完全不同的库存模型。要发文本奖请用 rank 或 prob 模式。
			return nil, nil, errBadRequest("双色球奖级本轮只支持额度奖")
		}
		if err := checkBallTierInput(src, s, entriesCap); err != nil {
			return nil, nil, err
		}
		rows[i].RedMatch = src.RedMatch
		rows[i].BlueMatch = src.BlueMatch
		rows[i].PoolShareBps = src.PoolShareBps
		shareSum += src.PoolShareBps
		tiers = append(tiers, BallTier{
			Tier: rows[i].Tier, RedMatch: src.RedMatch, BlueMatch: src.BlueMatch,
			Count: rows[i].Count, Amount: rows[i].AmountQuota,
			PoolShareBps: src.PoolShareBps, PrizeType: rows[i].Type(),
		})
	}
	if shareSum > 10000 {
		return nil, nil, errBadRequest("各浮动奖级占池比例之和不得超过 10000 万分比")
	}
	if err := checkBallTierReachability(tiers); err != nil {
		return nil, nil, err
	}

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, prizeSpecLineOf(act.Algo, r))
	}
	return rows, lines, nil
}

// resolveBallSeries 绑定活动到一个期次系列,并把号池抄到活动行上。
//
// 号池在创建期就抄一份,是因为参与时要用它校验选号,而那时不该再去 JOIN 系列表:
// 系列表上的号池虽然不可变,但"活动依赖的那份值"必须与进承诺原像的那份是同一个。
func resolveBallSeries(ctx context.Context, act *Activity, in *activityInput) (*Series, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	// 句柄在传出去之前就绑上请求的预算:创建活动是管理端冷路径,
	// 一条没有上界的查询会占住扩展库连接。
	s, err := loadSeriesByNo(ctx, gdb.WithContext(ctx), in.SeriesNo)
	if err != nil {
		return nil, err
	}
	if s.Status != SeriesOpen {
		return nil, errSeriesClosed
	}
	if err := ValidateBallPool(s.RedPool, s.RedPick, s.BluePool, s.BluePick); err != nil {
		return nil, errBadRequest("该期次系列的号池配置不合法,无法开新一期")
	}
	act.SeriesId = s.Id
	act.SeriesNo = s.SeriesNo
	act.PoolShareBps = s.PoolShareBps
	act.BallRedPool = s.RedPool
	act.BallRedPick = s.RedPick
	act.BallBluePool = s.BluePool
	act.BallBluePick = s.BluePick
	return s, nil
}

// checkBallTierInput 校验一个奖级的定档条件与奖金形态。
func checkBallTierInput(p prizeInput, s *Series, entriesCap int) error {
	if p.RedMatch < 0 || p.RedMatch > s.RedPick || p.BlueMatch < 0 || p.BlueMatch > s.BluePick {
		return errBadRequest(fmt.Sprintf(
			"奖级 %d 的命中数必须落在 红 [0,%d]、蓝 [0,%d]", p.Tier, s.RedPick, s.BluePick))
	}
	if p.RedMatch+p.BlueMatch <= 0 {
		return errBadRequest(fmt.Sprintf("奖级 %d 至少要求命中一个号码,否则全场每个人都中奖", p.Tier))
	}
	if p.PoolShareBps < 0 || p.PoolShareBps > 10000 {
		return errBadRequest(fmt.Sprintf("奖级 %d 的占池比例必须落在 [0, 10000]", p.Tier))
	}
	if p.PoolShareBps > 0 {
		// 浮动奖与固定奖互斥:一个奖级同时写"每人 1000"和"占池 30%"时,
		// 到底按哪个发只能靠代码里的先后顺序回答,而那是最坏的一种规则来源。
		if p.AmountQuota != 0 {
			return errBadRequest(fmt.Sprintf("奖级 %d 是浮动奖(占池比例 > 0),额度必须为 0", p.Tier))
		}
		return nil
	}
	if p.AmountQuota <= 0 {
		return errBadRequest(fmt.Sprintf("奖级 %d 必须填写固定额度,或者改成占池比例的浮动奖", p.Tier))
	}
	// 与概率制同一条:中签人数超过份数时按预算均分,人均不足 1 额度会有人分到 0,
	// 而 PlanPayouts 会跳过 amount<=0 的计划 —— 一个真中了奖的人被静默漏发。
	if p.AmountQuota*int64(p.Count) < int64(entriesCap) {
		// 与概率制那一条**共用同一个构造器**,不再各写一份格式串。
		return tierBudgetShort(p.Tier, p.Count, p.AmountQuota, entriesCap)
	}
	return nil
}

// checkBallTierReachability 拒绝"永远开不出来"的奖级表。
//
// MatchTier 按 tier 升序命中即停,所以只要某个更高等级(tier 更小)的门槛
// **不严于**某个更低等级,后者就永远轮不到 —— 界面上明明写着五等奖,
// 实际上一个人都不可能中,而这种错配不会有任何报错。
func checkBallTierReachability(tiers []BallTier) error {
	ordered := append([]BallTier(nil), tiers...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Tier < ordered[j].Tier })
	for i := range ordered {
		for j := i + 1; j < len(ordered); j++ {
			if ordered[i].RedMatch <= ordered[j].RedMatch && ordered[i].BlueMatch <= ordered[j].BlueMatch {
				return errBadRequest(fmt.Sprintf(
					"奖级 %d 的门槛不严于奖级 %d,后者永远开不出来:请让等级数越小的奖级要求越多的命中数",
					ordered[i].Tier, ordered[j].Tier))
			}
		}
	}
	return nil
}

// seriesSnapshotOf 把活动行上的期次段投影成承诺原像里的那一段。
func seriesSnapshotOf(act *Activity) *SeriesSnapshot {
	return &SeriesSnapshot{
		SeriesNo:       act.SeriesNo,
		IssueNo:        act.IssueNo,
		PoolSeedQuota:  act.PoolSeedQuota,
		PoolCarryQuota: act.PoolCarryQuota,
		PoolOpenQuota:  act.PoolOpenQuota,
		PoolShareBps:   act.PoolShareBps,
		BallRedPool:    act.BallRedPool,
		BallRedPick:    act.BallRedPick,
		BallBluePool:   act.BallBluePool,
		BallBluePick:   act.BallBluePick,
	}
}

// checkBallPoolCovers 是"数学上不可能欠付"的发布期硬校验。
//
// 判据用的是**开局基数** pool_open_quota,不含本期投注的入池部分。这样是充分的:
// 固定奖档的支出与池子无关,浮动奖档的支出按比例随池子一起涨,
// 所以 fixed + floor(open × Σshare/10000) ≤ open 在 open 更大时只会更宽松。
//
// 校验通过之后,池子在数学上不可能不足 —— 于是**永远不需要截断,也永远不需要
// 向用户解释为什么中了拿不到**。
func checkBallPoolCovers(tx *gorm.DB, act *Activity) error {
	var prizes []Prize
	if err := tx.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error; err != nil {
		return err
	}
	entriesCap := int64(act.MaxTotalEntries)
	if entriesCap <= 0 {
		entriesCap = int64(config.Get().Lottery.MaxTotalEntriesHard)
	}
	open := act.PoolOpenQuota
	var fixed int64
	var shareBps int64
	for _, p := range prizes {
		if p.PoolShareBps > 0 {
			shareBps += int64(p.PoolShareBps)
			// 浮动奖的"人均至少 1 额度"下限。固定奖那一条(count × amount ≥
			// 全场参与上限)在创建期由 checkBallTierInput 守住,浮动奖的预算
			// 直到发布期取走池子才知道,所以它的同名判据只能落在这里。
			//
			// 没有它,一个门槛宽松的浮动奖档在超募时会把预算摊到人均 0:
			// ballSplitEven 会 fail-closed,整场卡在 settling —— 那比静默漏发好,
			// 但发布期就能算出来的事不该等到开奖才炸。
			if open*int64(p.PoolShareBps)/10000 < entriesCap {
				return errSeriesPoolShort
			}
			continue
		}
		fixed += p.AmountQuota * int64(p.Count)
	}
	if shareBps > 10000 {
		return errSeriesPoolShort
	}
	if fixed+open*shareBps/10000 > open {
		return errSeriesPoolShort
	}
	// 池子本身也必须落在单笔出款的容量之内:浮动奖独中时那一笔要过 twophase 的
	// amount ≤ common.MaxQuota。越界时活动会永远收不了尾,拦在发布期。
	//
	// 报 errSeriesPoolCeiling 而不是 errSeriesPoolShort:两者的处置**相反**。
	// pool_short 的文案是「请先注资或调低奖级」,而这一条越注资越糟,
	// 奖级也救不了它(这个判据无条件)。合成一个码的实测后果是运营照着文案
	// 反复注资,把一个已经开不出新期的系列推得更深。
	if open > int64(common.MaxQuota) {
		return errSeriesPoolCeiling
	}
	return nil
}
