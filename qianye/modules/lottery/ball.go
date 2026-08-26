package lottery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ball.go —— 双色球(draw_mode=ball)的全部纯函数。零 IO,可被第三方逐字节复现。
//
// # 它与概率模式(draw_mode=prob)是两件事,但共用同一个随机原语
//
//	prob:每张票各摇一次,阈值判定。随机量的作用域 = 每一条 entry。
//	ball:全场共摇一次,匹配判定。随机量的作用域 = 整个活动。
//
// 区别只在**作用域**,不在随机源:两者都是
// HMAC(final_seed, 域前缀 ⋆ act_no ⋆ 标签) → 排序或阈值 → 确定性结论。
// 说成一件事会在实现时把两套语义搅在一起;说成两套随机源则凭空多一个
// 可被操纵的面。
//
// # 为什么摇号用"给每个球号算一个 HMAC 再排序取前 k",而不是 `1 + h(i) % R`
//
// PickWinners 的注释里已经就这件事立过法:**可复现性本身就是公正性的一部分**。
// 取模 + 撞重重取需要第三方精确复现计数器 i 的推进规则、蓝球的起始下标、
// 撞到重复时是否消耗随机数 —— 每一处细微差异都会算出**完全不同的号码**,
// 而验证者无法判断是谁错了。选排序法零实现自由度、零模偏置、任何语言十行以内,
// 而且与 PickWinners 是同一个原语:验证脚本里两处都是 sorted(key=(hash, 号码))。
//
// # 界面上那七颗球必须是产生结果的**原因**
//
// 一种更省事的做法是:先用票面定出中奖等级,再反向构造一组"与该等级相容的号码"
// 拿去展示。那样界面上的球就不是因果机制,而是结果产生之后编出来的一个自洽故事,
// 用户看到的因果链是假的 —— 而且它与"用户自己选号"在数学上不相容。
// 这里做的是真摇号:开奖号只由 final_seed 决定,与任何一张票无关;
// 一张票中不中,由它自己选的号与开奖号的匹配数决定。

// 球色标签。它进摇号的 HMAC 原像,红蓝两组因此互不相关。
const (
	BallColorRed  = "red"
	BallColorBlue = "blue"
)

// 号池的硬上界。**刻意写死在代码里,不进 YAML**:它只防手滑,不是运营旋钮,
// 而 config 那几个文件正被别的工作流占用。
//
// 上界之所以定得这么小,是因为真正的 33 选 6 + 16 选 1 是 1772 万分之一 ——
// 在一个 API 网关的用户体量下一等奖永远不会开出、初始池子永远派不出去,
// 池子只涨不发在用户眼里就是骗局。推荐区间 red 10~15 选 3~4、blue 3~5 选 1,
// 一等奖概率落在千分之一到万分之一:**可能开出,也可能连滚三期**,
// 这正是"不是说必须要有中奖人"。
const (
	MaxBallRedPool  = 36
	MaxBallRedPick  = 8
	MaxBallBluePool = 16
	MaxBallBluePick = 2
)

// pickSep / pickGroupSep 是选号的规范化格式:红球两位补零、升序、逗号分隔,
// 竖线,蓝球同理。例 "03,05,12|08"。
//
// **归一化之后的字节就是进链的字节**,绝不读出来重序列化 —— 否则平台可以在
// 开奖后把某个人的号改成中奖号,而链尾、计数、roster 重算全部照常通过。
const (
	pickSep      = ","
	pickGroupSep = "|"
)

var (
	// ErrBadPick 是选号不合法。它是用户输入错误,不是内部错误。
	ErrBadPick = errors.New("qianye/lottery: 选号不合法")
	// ErrBallPoolInvalid 是号池配置不合法(创建期就该被挡住)。
	ErrBallPoolInvalid = errors.New("qianye/lottery: 双色球号池配置不合法")
)

// ValidateBallPool 校验号池四元组。
//
// 号池在 series 上定死、期与期之间不可变,而且四个值同时进每期的 commit 原像:
// 运营不能靠悄悄调号码空间来改概率,而"各档中奖概率"是由组合数算出来的,
// 前端自己就能算 —— 在这件事上管理员根本没有撒谎的接口。
func ValidateBallPool(redPool, redPick, bluePool, bluePick int) error {
	if redPool < 2 || redPool > MaxBallRedPool {
		return ErrBallPoolInvalid
	}
	if redPick < 1 || redPick > MaxBallRedPick || redPick > redPool {
		return ErrBallPoolInvalid
	}
	if bluePool < 1 || bluePool > MaxBallBluePool {
		return ErrBallPoolInvalid
	}
	if bluePick < 1 || bluePick > MaxBallBluePick || bluePick > bluePool {
		return ErrBallPoolInvalid
	}
	return nil
}

// FormatPick 把两组号码编成规范化选号串。调用方保证两组各自已去重。
func FormatPick(reds, blues []int) string {
	r := append([]int(nil), reds...)
	b := append([]int(nil), blues...)
	sort.Ints(r)
	sort.Ints(b)
	return strings.Join(pad2(r), pickSep) + pickGroupSep + strings.Join(pad2(b), pickSep)
}

func pad2(ns []int) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		s := strconv.Itoa(n)
		if len(s) < 2 {
			s = "0" + s
		}
		out = append(out, s)
	}
	return out
}

// ParsePick 校验并归一化用户的选号。
//
// 返回的 normalized 就是要落库、要进 ChainNext 与 RosterLine 的那份字节。
// 机选与自选在协议上完全等价:机选是纯前端按钮(crypto.getRandomValues),
// 服务端不区分 —— 服务端多一条随机路径就多一处要证明其公正的地方,
// 而 pick 一旦进链,两者的可验证性一模一样。
func ParsePick(pick string, redPool, redPick, bluePool, bluePick int) ([]int, []int, string, error) {
	if err := ValidateBallPool(redPool, redPick, bluePool, bluePick); err != nil {
		return nil, nil, "", err
	}
	parts := strings.Split(strings.TrimSpace(pick), pickGroupSep)
	if len(parts) != 2 {
		return nil, nil, "", ErrBadPick
	}
	reds, err := parseBallGroup(parts[0], redPick, redPool)
	if err != nil {
		return nil, nil, "", err
	}
	blues, err := parseBallGroup(parts[1], bluePick, bluePool)
	if err != nil {
		return nil, nil, "", err
	}
	return reds, blues, FormatPick(reds, blues), nil
}

// parseBallGroup 解析一组号码:恰好 want 个、互不相同、全部落在 [1, pool]。
func parseBallGroup(raw string, want, pool int) ([]int, error) {
	fields := strings.Split(strings.TrimSpace(raw), pickSep)
	if len(fields) != want {
		return nil, ErrBadPick
	}
	out := make([]int, 0, want)
	seen := make(map[int]bool, want)
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 || n > pool || seen[n] {
			return nil, ErrBadPick
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

// splitPickLoose 解析一份**已经归一化过**的选号串,不做池子边界校验。
//
// 用在开奖判定上:那时号池早已冻结在活动行与 commit 原像里,再校一次边界
// 只会在"运营改了号池"这种本该被承诺校验拦下的场景里制造第二个报错点。
func splitPickLoose(pick string) ([]int, []int, bool) {
	parts := strings.Split(pick, pickGroupSep)
	if len(parts) != 2 {
		return nil, nil, false
	}
	reds, ok := looseGroup(parts[0])
	if !ok {
		return nil, nil, false
	}
	blues, ok := looseGroup(parts[1])
	if !ok {
		return nil, nil, false
	}
	return reds, blues, true
}

func looseGroup(raw string) ([]int, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	fields := strings.Split(raw, pickSep)
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// BallDraw 摇出一组开奖号。
//
//	t[b]  = HMAC_SHA256(key=unhex(final_seed), "qylot-ball-v2" ⋆ act_no ⋆ color ⋆ dec(b))
//	order = 按 (t[b], b) 升序排序的球号            ← 平局用号码本身定死,不留自由度
//	返回    order 的前 k 个,**升序**展示
//
// 零取模、零拒绝采样、零状态。任何语言十行以内可复现。
//
// key 取 final_seed 的**字节**而不是它的十六进制文本:这一条必须与验证脚本
// 逐字节一致,verify.py 里写的是 hmac(unhex(final), ...)。
func BallDraw(finalSeed, actNo, color string, poolN, pickK int) []int {
	if poolN <= 0 || pickK <= 0 {
		return make([]int, 0)
	}
	if pickK > poolN {
		pickK = poolN
	}
	key, err := hex.DecodeString(finalSeed)
	if err != nil {
		// final_seed 恒由 sha256Hex 产出。解不开只可能是调用方传错了东西,
		// 而回落成"把文本本身当密钥"会**静默**摇出另一组号码 —— 与 Ticket 同口径。
		key = []byte(finalSeed)
	}

	type ball struct {
		n int
		h string
	}
	all := make([]ball, 0, poolN)
	for b := 1; b <= poolN; b++ {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(strings.Join([]string{domainBall, actNo, color, deci(b)}, SEP)))
		all = append(all, ball{n: b, h: hex.EncodeToString(mac.Sum(nil))})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].h != all[j].h {
			return all[i].h < all[j].h
		}
		return all[i].n < all[j].n
	})

	out := make([]int, 0, pickK)
	for i := 0; i < pickK; i++ {
		out = append(out, all[i].n)
	}
	sort.Ints(out)
	return out
}

// BallResultText 把一次开奖结果编成落库与展示用的规范化串,与选号同格式。
func BallResultText(reds, blues []int) string { return FormatPick(reds, blues) }

// BallTier 是一个双色球奖级。
//
// 与 Tier 分开是因为定档条件完全不同:抽奖按名次或概率,双色球按红蓝命中数。
// 合并成一个结构会让"这几个字段在这个模式下没有意义"变成一句只能靠注释表达
// 的约定,而那正是最容易在重构里被忽略的一类约定。
type BallTier struct {
	Tier int `json:"tier"`
	// RedMatch / BlueMatch 是**下界**:命中数达到即可定档。
	RedMatch  int `json:"red_match"`
	BlueMatch int `json:"blue_match"`
	// Count / Amount 是固定奖:最多发 Count 份,每份 Amount。
	// 中签人数超过 Count 时按 Count × Amount 的预算均分(见 PickWinnersBall)。
	Count  int   `json:"count"`
	Amount int64 `json:"amount_quota"`
	// PoolShareBps > 0 时本档是浮动奖:恒为 pool_open × bps / 10000 均分,
	// 与 Count / Amount 互斥。
	PoolShareBps int    `json:"pool_share_bps"`
	PrizeType    string `json:"prize_type"`
}

// MatchTier 判定一张票中了哪一档。
//
// 按 tier 升序(1 = 最高奖)遍历,**命中即停**:一张票只中一档。
// 返回 tier == 0 表示未中奖 —— 这是一等公民结果,不是异常分支。
func MatchTier(drawReds, drawBlues []int, pick string, tiers []BallTier) (int, int, int) {
	myReds, myBlues, ok := splitPickLoose(pick)
	if !ok {
		return 0, 0, 0
	}
	redMatch := countMatch(drawReds, myReds)
	blueMatch := countMatch(drawBlues, myBlues)

	ordered := append([]BallTier(nil), tiers...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Tier < ordered[j].Tier })
	for _, t := range ordered {
		if redMatch >= t.RedMatch && blueMatch >= t.BlueMatch {
			return t.Tier, redMatch, blueMatch
		}
	}
	return 0, redMatch, blueMatch
}

func countMatch(a, b []int) int {
	set := make(map[int]bool, len(a))
	for _, n := range a {
		set[n] = true
	}
	n := 0
	for _, x := range b {
		if set[x] {
			n++
		}
	}
	return n
}

// PickWinnersBall 按开奖号算出中奖名单与逐笔金额。
//
// # 封顶不封人
//
// 固定奖档在中签人数超过 Count 时**不截断名单**,而是把 Count × Amount 的预算
// 交给全部中签者均分。截断制下一张票的实际中奖概率会变成
// 组合数概率 × min(1, Count/W) —— 依赖当期人数,也就是说规则页上印的那个概率
// 在超募时是**假的**。整套设计的立身之本是公示的数字为真,所以概率恒等于
// 组合数给出的那个值,让金额去浮动。附带收益:消掉了"我中了却因为排第 7 名
// 拿不到"这个必然引发争议的坏情形。
//
// 逐笔向零截断、残差归 entry_no 字节序最大者,与 SplitPool 逐字节同一套口径 ——
// 不新增任何舍入约定。
//
// roster 必须已按 EntryNo 字节序升序(与 RosterHash 同一份输入)。
func PickWinnersBall(drawReds, drawBlues []int, roster []RosterLine, tiers []BallTier, poolOpen int64) ([]Winner, error) {
	if poolOpen < 0 {
		return nil, ErrPoolNotConserved
	}
	ordered := append([]BallTier(nil), tiers...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Tier < ordered[j].Tier })

	byTier := make(map[int][]RosterLine, len(ordered))
	for _, l := range roster {
		tier, _, _ := MatchTier(drawReds, drawBlues, l.Pick, ordered)
		if tier == 0 {
			continue
		}
		byTier[tier] = append(byTier[tier], l)
	}

	winners := make([]Winner, 0, len(roster))
	var total int64
	pos := 0
	for _, t := range ordered {
		hits := byTier[t.Tier]
		if len(hits) == 0 {
			continue
		}
		amounts, err := ballTierAmounts(t, hits, poolOpen)
		if err != nil {
			return nil, err
		}
		for i, l := range hits {
			total += amounts[i]
			if total > poolOpen {
				// 池子在发布期就被校验成"数学上不可能不足",走到这里说明奖级表
				// 或池子被事后改动过。整场停手,绝不发一笔对不上账的钱。
				return nil, ErrPoolNotConserved
			}
			// PrizeType 必须原样带出去:revealActivity 按它把中奖位分成
			// 额度腿与文本腿。留空会让一个文本奖中奖位落成 amount=0 的额度腿,
			// 被 PlanPayouts 整批丢掉且不报错 —— 中奖者什么都拿不到,
			// 而验证脚本会把他算进中奖名单并判 FAIL。
			// 本轮 applyBallSpec 只放行 quota,但那道闸门是一行 if,而这里是结构。
			winners = append(winners, Winner{
				Pos: pos, Tier: t.Tier, EntryNo: l.EntryNo, UserRef: l.UserRef,
				Amount: amounts[i], PrizeType: t.PrizeType,
			})
			pos++
		}
	}
	return winners, nil
}

// ballTierAmounts 算出一个奖级里每一位中签者拿多少。
func ballTierAmounts(t BallTier, hits []RosterLine, poolOpen int64) ([]int64, error) {
	if t.PoolShareBps > 0 {
		if t.PoolShareBps > 10000 {
			return nil, ErrPoolNotConserved
		}
		// poolOpen ≤ common.MaxQuota(2^43),乘 10000 之后是 2^43×10^4 ≈ 8.8e16,
		// 仍在 int64 之内(那正是 MaxQuota 推导里留的那道余量)。
		return ballSplitEven(t.Tier, poolOpen*int64(t.PoolShareBps)/10000, hits)
	}
	if len(hits) <= t.Count {
		out := make([]int64, len(hits))
		for i := range out {
			out[i] = t.Amount
		}
		return out, nil
	}
	if t.Count < 0 || (t.Amount != 0 && t.Count > 0 && t.Amount > (1<<62)/int64(t.Count)) {
		return nil, ErrPoolNotConserved
	}
	return ballSplitEven(t.Tier, t.Amount*int64(t.Count), hits)
}

// ballSplitEven 把预算均分给中签者:逐笔向零截断,残差归 entry_no 字节序最大者。
//
// 与 SplitPool 的残差口径完全一致(那里是"最后一笔",而 winners 已按 entry_no
// 升序,末位即最大)。这里显式找最大值而不是取末位,是因为调用方传进来的顺序
// 由 roster 决定 —— 依赖调用方的顺序等于把一条会被静默改掉的约定藏进注释。
//
// 均分唯一的新失败态是"预算 < 人数时有人分到 0",而 PlanPayouts 会跳过
// amount <= 0 的计划(静默漏发)。固定奖档在创建期被一条结构性校验堵死
// (count × amount ≥ 全场参与上限),浮动奖档在发布期被 checkBallPoolCovers
// 的同名判据堵死(open × bps/10000 ≥ 全场参与上限)。
//
// 但**这里仍然 fail-closed**,与概率制的 prizeShares 完全同一个口径:
// 走到人均 0 说明奖级表或池子被事后改动过。静默发 0 的后果不只是漏发 ——
// PlanPayouts 会连 payout 行都不落,而证据链的 winners 是从 payout 表拼出来的,
// 于是验证脚本会算出一批"平台没发的中奖位"并判 FAIL,平台看起来像在作弊。
// 宁可整场停在 settling 等人查,也绝不发一份算不平的名单。
func ballSplitEven(tier int, budget int64, hits []RosterLine) ([]int64, error) {
	out := make([]int64, len(hits))
	if len(hits) == 0 {
		return out, nil
	}
	if budget < int64(len(hits)) {
		return nil, fmt.Errorf("%w: 奖级 %d 预算 %d 摊给 %d 人后有人分到 0",
			ErrPoolNotConserved, tier, budget, len(hits))
	}
	base := budget / int64(len(hits))
	for i := range out {
		out[i] = base
	}
	maxIdx := 0
	for i := range hits {
		if hits[i].EntryNo > hits[maxIdx].EntryNo {
			maxIdx = i
		}
	}
	out[maxIdx] += budget - base*int64(len(hits))
	return out, nil
}
