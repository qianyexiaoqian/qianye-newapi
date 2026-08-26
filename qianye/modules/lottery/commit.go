package lottery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
)

// commit.go —— 公正性协议 lot-v1 的全部纯函数。零 IO,可被第三方逐字节复现。
//
// # 承诺什么
//
// 只公布种子哈希是不够的。commit_hash 必须同时覆盖:随机源、参与条件、
// 奖档/选项集合、四个时刻、算法版本,以及影响结果的每一个布尔与数值
// (allow_multi_win / no_winner_policy / min_entries_to_hold / fee_bps)。
// 漏掉任何一项,管理员都能在不碰种子的前提下算出想要的结果,
// 而验证者只会看到"对不上"却举证不出是哪一边改的。
//
// # 编码约定(改一个字节就是不兼容变更,必须升 algo 版本号)
//
//   - SEP = 0x1F(单元分隔符),与 twophase.Digest 同口径。它不会出现在业务串里,
//     避免 "a|b" 与 "a"+"|b" 撞出同一个哈希。
//   - dec(n) = 十进制无前导零。
//   - 规范化 JSON = 键按字节序升序、无空白(由 encoding/json 对 map 的排序保证)。
//   - RulesText / SpecText 哈希的就是**落库的那份字节**,绝不读出来重序列化:
//     Go struct 的序列化顺序与第三方不一致,重序列化一次就会让所有外部验证者
//     算出不同的哈希。

// SEP 是全部哈希原像的字段分隔符。
const SEP = "\x1f"

// 域分隔前缀。每一种哈希用不同的前缀,防止 A 的原像被当成 B 的原像重放。
//
// domainRules / domainFinal / domainTicket / domainURef 在 lot-v2 里**刻意保持
// v1**:少改一处就少一处让存量证据链失效的可能;而"票面推导一个字节都没变"
// 本身就是"概率制没有引入新随机源"的最好证据 —— 概率制只是换了一把尺子去读
// 同一张票,而不是重新发了一张票。
const (
	domainRules  = "qylot-rules-v1"
	domainSpec   = "qylot-spec-v1"
	domainCommit = "qylot-commit-v1"
	domainChain  = "qylot-chain-v1"
	domainRoster = "qylot-roster-v1"
	domainFinal  = "qylot-final-v1"
	domainTicket = "qylot-ticket-v1"
	domainURef   = "qylot-uref-v1"

	domainSpecV2   = "qylot-spec-v2"
	domainCommitV2 = "qylot-commit-v2"
	domainChainV2  = "qylot-chain-v2"
	domainRosterV2 = "qylot-roster-v2"
	// domainBall 是双色球摇号的域前缀。协议位在 v2 里已经冻结(见
	// CommitHashV2 的 SeriesSnapshot 分量与 PrizeSpecLineV2 的 red_match /
	// blue_match),摇号算法本身由双色球那一路实现。
	domainBall = "qylot-ball-v2"
)

// sha256Hex 把若干分量按 SEP 拼接后取 SHA-256 十六进制。
func sha256Hex(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, SEP)))
	return hex.EncodeToString(sum[:])
}

func dec(n int64) string { return strconv.FormatInt(n, 10) }
func deci(n int) string  { return strconv.Itoa(n) }
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Canonical 把一组键值对编码成规范化 JSON:键按字节序升序、无空白。
//
// 走 map 而不是 struct 是刻意的:encoding/json 对 map 的键排序是有明确文档
// 保证的字节序升序,而 struct 的顺序是字段声明顺序 —— 后者在有人重排字段时
// 会静默改变哈希,而那正是"看起来无害的重构"最容易做的事。
func Canonical(m map[string]any) (string, error) {
	b, err := common.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RulesHash 对参与条件的规范化 JSON 取哈希。
func RulesHash(rulesText string) string { return sha256Hex(domainRules, rulesText) }

// SpecHash 对奖档/选项集合取哈希。调用方保证 lines 已按稳定顺序排好
// (抽奖按 tier 升序,竞猜按 opt_no 升序)。
func SpecHash(lines []string) string {
	return sha256Hex(append([]string{domainSpec}, lines...)...)
}

// SpecHashV2 对 lot-v2 的奖档/选项集合取哈希。
//
// 与 v1 唯一的差别是域前缀:同一组 lines 在两个版本下必须算出不同的哈希,
// 否则一份 v2 的奖档表可以被当作 v1 的原像重放。
func SpecHashV2(lines []string) string {
	return sha256Hex(append([]string{domainSpecV2}, lines...)...)
}

// SpecHashFor 按算法版本分派 spec 哈希。
func SpecHashFor(algo string, lines []string) string {
	if algo == AlgoV2 {
		return SpecHashV2(lines)
	}
	return SpecHash(lines)
}

// prizeSpecLineV1 是抽奖奖档在 lot-v1 的 spec 原像里的一行。**已冻结**。
//
// 任何改动都会让所有已开完活动的历史公正查询集体变成 FAIL。
// fairness_test.go 里钉了黄金向量。
func prizeSpecLineV1(tier int, name string, amountQuota int64, count int) string {
	return strings.Join([]string{deci(tier), name, dec(amountQuota), deci(count)}, SEP)
}

// PrizeSpec 是一个奖档在 lot-v2 的 spec 原像里的全部分量。
//
// 它是**承诺层**的形状,不是数据库行:凡是能改变"谁中什么"的量都必须在这里
// 出现,而中奖者具体拿到的那串兑换码**不在这里**(它在开奖之后才由管理员填入,
// 发布时根本不存在,见 text_prize.go 顶部的诚实边界说明)。
type PrizeSpec struct {
	Tier      int
	Name      string
	PrizeType string
	// AmountQuota 恒为 0(text 档)或 > 0(quota 档)。
	AmountQuota int64
	Count       int
	// WinPpm 是本档的中奖概率(百万分比)。rank 模式下恒为 0。
	WinPpm int
	// TextDesc 是**公开**的履行说明。它本来就要展示给所有人看,
	// 因此明文进原像 —— 放一个可被离线爆破的哈希在这里只会让人误以为它是保密的。
	TextDesc string
	// RedMatch / BlueMatch / PoolShareBps 是双色球奖级的定档条件与池子份额。
	// 非 ball 模式恒为 0,但**必须出现在原像里** —— 少一个占位就等于允许
	// 管理员把一场普通抽奖悄悄改成双色球。
	RedMatch     int
	BlueMatch    int
	PoolShareBps int
}

// PrizeSpecLineV2 是抽奖奖档在 lot-v2 的 spec 原像里的一行。
//
// 恒等式(创建时强制,验证脚本复查):
//   - quota 档:text_desc == ""
//   - text 档: amount_quota == 0
//   - rank 模式:win_ppm == 0
//   - 非 ball 模式:red_match / blue_match / pool_share_bps == 0
func PrizeSpecLineV2(p PrizeSpec) string {
	return strings.Join([]string{
		deci(p.Tier), p.Name, p.PrizeType,
		dec(p.AmountQuota), deci(p.Count), deci(p.WinPpm), p.TextDesc,
		deci(p.RedMatch), deci(p.BlueMatch), deci(p.PoolShareBps),
	}, SEP)
}

// OptionSpecLine 是竞猜选项在 spec 原像里的一行。
func OptionSpecLine(optNo int, label string, isCatchAll bool) string {
	return strings.Join([]string{deci(optNo), label, boolStr(isCatchAll)}, SEP)
}

// CommitHash 算出 lot-v1 活动的承诺哈希。**已冻结**,新活动一律走 CommitHashV2。
//
// seedHex 是唯一在揭示前保密的分量;其余每一个分量在揭示后都可被任何人复算,
// 这正是 ref_salt **不进原像**的理由(见 Seed.RefSalt 的说明)。
func CommitHash(a *Activity, seedHex string) string {
	return sha256Hex(
		domainCommit,
		a.ActNo,
		a.Kind,
		a.Algo,
		a.RulesHash,
		a.SpecHash,
		dec(a.StakeQuota),
		dec(a.OpenAt),
		dec(a.CloseAt),
		dec(a.DrawAt),
		dec(a.SettleDeadline),
		boolStr(a.AllowMultiWin),
		deci(a.FeeBps),
		NoWinnerPolicy,
		deci(a.MinEntriesToHold),
		seedHex,
	)
}

// SeriesSnapshot 是双色球期次在 lot-v2 承诺原像里的那一段。
//
// 单独成结构而不是直接读 Activity 的列,是为了让"概率制"与"双色球"两路能各自
// 独立落地:原像的**位置与顺序**在这里冻结,值由双色球那一路填。
// 非 ball 活动传 nil,全部分量取零值 —— 但零值仍然逐个进原像。
type SeriesSnapshot struct {
	SeriesNo       string
	IssueNo        int
	PoolSeedQuota  int64
	PoolCarryQuota int64
	PoolOpenQuota  int64
	PoolShareBps   int
	BallRedPool    int
	BallRedPick    int
	BallBluePool   int
	BallBluePick   int
}

// CommitHashV2 算出 lot-v2 活动的承诺哈希。
//
// 相对 v1,在 min_entries_to_hold 之后、seed 之前追加了 draw_mode 与整段期次快照。
// **非 ball 活动这些位恒为空串/0,但必须出现在原像里** —— 少一个占位就等于允许
// 管理员把一场普通抽奖悄悄改成双色球,或者把 rank 悄悄改成 prob。
//
// 每档的 win_ppm / prize_type / text_desc 是通过 a.SpecHash 间接进原像的
// (见 PrizeSpecLineV2)。概率表事后被改一个数字,revealActivity 那道完整原像
// 校验会**直接拒绝开奖**,而不是"以种子为准"继续 —— 这正是"公示的概率为真"
// 这条主张的执行点,而且它是现成代码,一行都不用新写。
func CommitHashV2(a *Activity, s *SeriesSnapshot, seedHex string) string {
	if s == nil {
		s = &SeriesSnapshot{}
	}
	return sha256Hex(
		domainCommitV2,
		a.ActNo,
		a.Kind,
		a.Algo,
		a.RulesHash,
		a.SpecHash,
		dec(a.StakeQuota),
		dec(a.OpenAt),
		dec(a.CloseAt),
		dec(a.DrawAt),
		dec(a.SettleDeadline),
		boolStr(a.AllowMultiWin),
		deci(a.FeeBps),
		NoWinnerPolicy,
		deci(a.MinEntriesToHold),
		a.DrawMode,
		s.SeriesNo,
		deci(s.IssueNo),
		dec(s.PoolSeedQuota),
		dec(s.PoolCarryQuota),
		dec(s.PoolOpenQuota),
		deci(s.PoolShareBps),
		deci(s.BallRedPool),
		deci(s.BallRedPick),
		deci(s.BallBluePool),
		deci(s.BallBluePick),
		seedHex,
	)
}

// CommitHashFor 按活动自己的算法版本分派承诺哈希。
//
// 分派点只有这一个:存量活动的 algo 列保持 lot-v1 且**永不回填**,
// 它们的历史公正查询必须永远算出与当年一模一样的值。
func CommitHashFor(a *Activity, s *SeriesSnapshot, seedHex string) string {
	if a.Algo == AlgoV2 {
		if s == nil {
			s = seriesSnapshotOf(a)
		}
		return CommitHashV2(a, s, seedHex)
	}
	return CommitHash(a, seedHex)
}

// ChainNext 推进逐条哈希链。chain_0 = commit_hash。
//
// 每条 entry 在插入那一刻算出 chain_hash 并**立即返回给用户**(报名回执)。
// 事后插入/删除/改动任何一条,该条之后所有用户手里的凭据全部对不上 ——
// 管理员要伪造,必须同时改掉 N 个用户已经看到过并可截图的值。
//
// 链刻意**不含 status**:否则 MainApply 每失败一次就破链。分层是干净的 ——
// 链保证"条目集合与不可变要素没被改",status 的真实性由资金单 + 用户自己的
// 额度流水交叉佐证。
func ChainNext(prev, actNo string, seq int, entryNo, userRef string, optNo int, amount int64) string {
	return sha256Hex(domainChain, prev, actNo, deci(seq), entryNo, userRef, deci(optNo), dec(amount))
}

// ChainNextV2 是 lot-v2 的链推进,末尾追加用户选号。
//
// pick 必须进链:否则平台可以在开奖之后把某个人的号改成中奖号,而现有的全部
// 校验(链尾、条目计数、名单重算)会**照常全部通过**。
// 非 ball 活动 pick 恒为空串,但那个空串仍然占一个分量位。
func ChainNextV2(prev, actNo string, seq int, entryNo, userRef string, optNo int, amount int64, pick string) string {
	return sha256Hex(domainChainV2, prev, actNo, deci(seq), entryNo, userRef, deci(optNo), dec(amount), pick)
}

// ChainNextFor 按算法版本分派链推进。
func ChainNextFor(algo, prev, actNo string, seq int, entryNo, userRef string, optNo int, amount int64, pick string) string {
	if algo == AlgoV2 {
		return ChainNextV2(prev, actNo, seq, entryNo, userRef, optNo, amount, pick)
	}
	return ChainNext(prev, actNo, seq, entryNo, userRef, optNo, amount)
}

// RosterLine 是有效名单里的一行。
//
// Pick 只在 lot-v2 的双色球模式下非空,且**只有 RosterLineV2 会读它** ——
// v1 的原像一个字节都没变,存量活动的证据链因此永远不受影响。
type RosterLine struct {
	EntryNo string `json:"entry_no"`
	UserRef string `json:"user_ref"`
	OptNo   int    `json:"opt_no"`
	Amount  int64  `json:"amount"`
	Pick    string `json:"pick,omitempty"`
}

// RosterHash 算出冻结名单的哈希与条数。调用方保证 lines 已按 EntryNo 字节序升序。
//
// 只有链是不够的:链不含 status,而抽签只用 status='success' 的集合。
// roster_hash 在揭示种子之前先行公开,把"到底哪些票有效"这个集合也钉死。
// 两者防的是不同的攻击。
func RosterHash(actNo, commitHash string, lines []RosterLine) (string, int) {
	rows := make([]string, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, strings.Join([]string{l.EntryNo, l.UserRef, deci(l.OptNo), dec(l.Amount)}, SEP))
	}
	h := sha256Hex(domainRoster, actNo, commitHash, deci(len(lines)), strings.Join(rows, "\n"))
	return h, len(lines)
}

// RosterHashV2 是 lot-v2 的名单哈希,行末追加选号。
func RosterHashV2(actNo, commitHash string, lines []RosterLine) (string, int) {
	rows := make([]string, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, strings.Join(
			[]string{l.EntryNo, l.UserRef, deci(l.OptNo), dec(l.Amount), l.Pick}, SEP))
	}
	h := sha256Hex(domainRosterV2, actNo, commitHash, deci(len(lines)), strings.Join(rows, "\n"))
	return h, len(lines)
}

// RosterHashFor 按算法版本分派名单哈希。
func RosterHashFor(algo, actNo, commitHash string, lines []RosterLine) (string, int) {
	if algo == AlgoV2 {
		return RosterHashV2(actNo, commitHash, lines)
	}
	return RosterHash(actNo, commitHash, lines)
}

// FinalSeed 把种子与冻结名单绑定成最终随机源。
//
// 这一步是本协议对朴素 commit-reveal 的关键修正:若票面只依赖 seed 与自己的
// entry_no,知道种子的人(管理员/DBA)每报一次名就能立刻算出自己的名次并锁定,
// 可以在开放期从容 grinding。把 roster_hash 混进随机源后,任何后来者的加入
// 都会重排全部票面,他无法在封盘前锁定任何结果 ——
// 除非他能保证自己是最后一个报名的人。
func FinalSeed(actNo, seedHex, rosterHash string, count int, algo string) string {
	return sha256Hex(domainFinal, actNo, seedHex, rosterHash, deci(count), algo)
}

// Ticket 算出单张票的票面值。
//
// key 是 final_seed 的字节而不是它的十六进制文本:这一条必须与验证脚本
// 逐字节一致,verify.py 里写的是 hmac(unhex(final), ...)。
func Ticket(finalSeed, actNo, entryNo string) string {
	key, err := hex.DecodeString(finalSeed)
	if err != nil {
		// final_seed 恒由 sha256Hex 产出,解不开只可能是调用方传错了东西。
		// 回落成把文本本身当密钥会**静默**算出一份不同的名单,那比报错危险得多。
		key = []byte(finalSeed)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.Join([]string{domainTicket, actNo, entryNo}, SEP)))
	return hex.EncodeToString(mac.Sum(nil))
}

// UserRef 算出公开名单里的脱敏稳定标识,取前 16 字节(32 个十六进制字符)。
func UserRef(refSalt string, userId int) string {
	full := sha256Hex(domainURef, refSalt, deci(userId))
	return full[:32]
}

// ─────────────────────────── 抽取算法 ───────────────────────────

// Tier 是一个奖档在抽取时的形态。
type Tier struct {
	Tier   int   `json:"tier"`
	Count  int   `json:"count"`
	Amount int64 `json:"amount_quota"`
	// PrizeType 是 quota(额度)或 text(文本兑换)。空串按 quota 处理,
	// 让存量调用点不必改。
	PrizeType string `json:"prize_type,omitempty"`
	// WinPpm 是本档的中奖概率(百万分比)。rank 模式下恒为 0。
	WinPpm int `json:"win_ppm,omitempty"`
}

// isText 判断这一档发的是文本奖而不是额度。
func (t Tier) isText() bool { return t.PrizeType == PrizeTypeText }

// Winner 是一个中奖位。
type Winner struct {
	Pos     int    `json:"pos"`
	Tier    int    `json:"tier"`
	EntryNo string `json:"entry_no"`
	UserRef string `json:"user_ref"`
	Amount  int64  `json:"amount"`
	// PrizeType 决定这一位走哪条派奖腿:quota 走 twophase 的资金链路,
	// text 只落一行 granted 记录、等管理员手工履行。
	PrizeType string `json:"prize_type,omitempty"`
	// RollPpm 是概率制下这张票的摇号结果,rank 模式下无意义(恒 0)。
	// 它是"我为什么没中"那句解释里唯一需要的数字。
	RollPpm uint32 `json:"roll_ppm,omitempty"`
}

// PickWinners 按 lot-v1 的选排序法抽出中奖名单。
//
// 步骤:
//  1. 每张票算 ticket
//  2. 按 (ticket 十六进制字典序升序, entry_no 字节序升序) 排序 —— 平局用
//     entry_no 定死,不留实现自由度
//  3. !allowMultiWin 时按此顺序遍历,每个 user_ref 只保留首次出现
//     (跳过的不消耗任何随机性,因此不需要公布跳票列表)
//  4. 按 tier 升序切片。票不够则该档空缺,如实公布,**不补抽**
//
// # 为什么不用 Fisher–Yates + 拒绝采样
//
// 可复现性本身就是公正性的一部分。FY 需要第三方精确复现计数器编码、字节序、
// limit 的算法、拒绝重取的边界、跳票时是否消耗随机数 —— 每一处细微差异都会
// 算出完全不同的名单,而验证者无法判断是谁错了。排序法无状态、零模偏置
// (256 位输出下碰撞可忽略)、任何语言十行以内。
//
// roster 必须已按 EntryNo 字节序升序(与 RosterHash 同一份输入)。
func PickWinners(finalSeed, actNo string, roster []RosterLine, tiers []Tier, allowMultiWin bool) []Winner {
	type ranked struct {
		line   RosterLine
		ticket string
	}
	all := make([]ranked, 0, len(roster))
	for _, l := range roster {
		all = append(all, ranked{line: l, ticket: Ticket(finalSeed, actNo, l.EntryNo)})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ticket != all[j].ticket {
			return all[i].ticket < all[j].ticket
		}
		return all[i].line.EntryNo < all[j].line.EntryNo
	})

	pool := all
	if !allowMultiWin {
		seen := make(map[string]bool, len(all))
		deduped := make([]ranked, 0, len(all))
		for _, r := range all {
			if seen[r.line.UserRef] {
				continue
			}
			seen[r.line.UserRef] = true
			deduped = append(deduped, r)
		}
		pool = deduped
	}

	ordered := make([]Tier, len(tiers))
	copy(ordered, tiers)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Tier < ordered[j].Tier })

	winners := make([]Winner, 0, len(pool))
	idx := 0
	for _, t := range ordered {
		for n := 0; n < t.Count; n++ {
			if idx >= len(pool) {
				// 票不够,该档空缺。绝不补抽 —— 补抽等于用一个没被承诺的规则
				// 决定谁中奖。
				break
			}
			// PrizeType 必须原样带出去(与 PickWinnersProb、PickWinnersBall 同口径)。
			// 留空会让一个文本奖中奖位落成 amount=0 的额度腿,被 PlanPayouts
			// 「amount<=0 跳过」整批丢掉且零告警:中奖者永远收不到兑换码、
			// text_grant_count 落成 0 让 finishIfDone 与 auditTextPrizes 的复核
			// 双双退化成 0>=0 恒真,而外部验证者按种子重算会多出这几位、
			// 判定整场 FAIL —— 平台明明是诚实的,证据链却自证造假。
			winners = append(winners, Winner{
				Pos:       idx,
				Tier:      t.Tier,
				EntryNo:   pool[idx].line.EntryNo,
				UserRef:   pool[idx].line.UserRef,
				Amount:    t.Amount,
				PrizeType: t.PrizeType,
			})
			idx++
		}
	}
	return winners
}

// ─────────────────────────── 概率制(draw_mode=prob)───────────────────────────

// PpmDen 是概率的分母:全部概率都以百万分比(ppm)表达。
//
// 为什么是百万分之一而不是万分之一:0.0001% 是运营真会用到的量级
// (十万分之一的一等奖),而万分比会把它逼成 0 —— 一个被静默取整成
// "永远不可能中"的奖档,是这套系统最不能出的那类事。
const PpmDen = 1_000_000

// ErrPpmOverflow 表示各档概率之和超过了 100%。
//
// 超过 100% 意味着有两档的摇号区间重叠,而"一张票同时中两档"在派奖层
// 会撞 uk(act_id, entry_id, kind)。创建时拒绝、揭示时复核、验证脚本再复核一次:
// 三处都对不上就直接 FAIL,**绝不猜一个"大概是这个意思"的解释**。
var ErrPpmOverflow = errors.New("qianye/lottery: 各档中奖概率之和超过 100%")

// RollPpm 把票面折算成一个六位摇号结果 r ∈ [0, 999999]。
//
// # 为什么是"票面前 64 位缩放"而不是"全 256 位阈值比较"
//
// 两者都零模偏置。取 64 位截断是因为 r 必须是一个**能念给用户听的数字**:
// 概率制相对名次制唯一需要额外证明的东西,就是"我为什么没中",而
// 「你的摇号结果 384217,二等奖区间是 [1000, 11000)」是一句人话,
// 一个 64 位十六进制串念出来等于没解释。可读性在这里是可验证性的一部分。
//
// 偏差是可忽略且被公示的:2^64 个均匀值分进 10^6 个桶,桶大小最多相差 1,
// 相对偏差 < 2^-44。这一条写进规则页,不掩饰。
//
// 实现用 bits.Mul64 取 128 位乘积的高 64 位 —— 等价于 floor(u × 10^6 / 2^64),
// 精确、无除法、无大整数依赖。Python 是 (u * 10**6) >> 64,
// TypeScript 是 Number((BigInt('0x'+hex) * 1000000n) >> 64n)。
//
// 票面恒由 Ticket 产出(64 个十六进制字符)。解不开只可能是调用方传错了东西,
// 此时返回 PpmDen —— 它落在**全部**中奖区间之外,即"这张票不中"。
// 回落成 0 会让一张畸形的票直接中一等奖,那是方向完全错误的失败。
func RollPpm(ticket string) uint32 {
	if len(ticket) < 16 {
		return PpmDen
	}
	u, err := strconv.ParseUint(ticket[:16], 16, 64)
	if err != nil {
		return PpmDen
	}
	hi, _ := bits.Mul64(u, PpmDen)
	return uint32(hi)
}

// Band 是一个奖档在摇号轴上占据的左闭右开区间。
type Band struct {
	Tier      int    `json:"tier"`
	LoPpm     uint32 `json:"lo_ppm"`
	HiPpm     uint32 `json:"hi_ppm"`
	Count     int    `json:"count"`
	Amount    int64  `json:"amount_quota"`
	PrizeType string `json:"prize_type"`
}

// Bands 把各档概率按 tier 升序累加成互不相交的区间 [Σ_{<k}, Σ_{≤k})。
//
// # 为什么是一根轴上的累加区间,而不是每档独立摇一次
//
// 独立摇需要 N 个随机量,并让"同时中两档"成为可能(届时派奖层撞唯一键,
// 一张票的第二个奖会静默消失)。一根轴上一次摇号,天然保证一张票至多中一档,
// 且第三方只需要复算一个数。
//
// win_ppm == 0 的档得到一个空区间 [lo, lo),它永远不可能被命中 ——
// 这是正确的:公示 0% 就该是 0%,而不是"偶尔也能中"。
func Bands(tiers []Tier) ([]Band, error) {
	ordered := make([]Tier, len(tiers))
	copy(ordered, tiers)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Tier < ordered[j].Tier })

	out := make([]Band, 0, len(ordered))
	var acc uint32
	for _, t := range ordered {
		if t.WinPpm < 0 {
			return nil, fmt.Errorf("%w: 第 %d 档的概率为负", ErrPpmOverflow, t.Tier)
		}
		if uint64(acc)+uint64(t.WinPpm) > PpmDen {
			return nil, fmt.Errorf("%w: 累计到第 %d 档已达 %d ppm", ErrPpmOverflow, t.Tier, uint64(acc)+uint64(t.WinPpm))
		}
		lo := acc
		acc += uint32(t.WinPpm)
		out = append(out, Band{
			Tier: t.Tier, LoPpm: lo, HiPpm: acc,
			Count: t.Count, Amount: t.Amount, PrizeType: t.PrizeType,
		})
	}
	return out, nil
}

// PickWinnersProb 按 lot-v2 的概率制抽出中奖名单。
//
// 步骤(与 verify.py 的 v2 分支逐字对应):
//  1. 每张票算 ticket(与 v1 **完全相同**的推导,一个字节都没变)
//  2. r = RollPpm(ticket)
//  3. 各档按 tier 升序累加 win_ppm 得到互不相交的区间
//  4. r 落进哪个区间就中哪一档;落在全部区间之外 = 未中奖
//  5. 某档中签人数 W 超过预算份数 count 时,该档预算 count×amount 由全部
//     W 人**均分**(逐笔向零截断,残差归 entry_no 字节序最大者)
//
// # 第 4 步为什么是一等公民而不是异常分支
//
// "不是说必须要有中奖人"正是项目方要的语义。更要紧的是可验证性:
// 落选者与中奖者走的是**同一行代码**、用的是**同一组公开输入**,
// 平台无法制造一个只有失败者看不到的暗门。落选的证明因此是一个正数
// (「r=384217,各档区间是 [0,1000)、[1000,11000),落在全部区间之外」),
// 而不是"你不在名单里"这种无法自证的否定式。
//
// # 第 5 步为什么是均分而不是按票面顺序截断前 count 名
//
// 截断制下一张票的**实际**中奖概率是 win_ppm × min(1, count/W),它依赖当期人数
// —— 也就是说卡片上印的"中奖概率 1%"在超募时是**假的**,而这套设计的立身之本
// 就是公示的数字为真。均分制下 P(命中) 严格等于 win_ppm,浮动的是金额,
// 而"超募时按比例摊薄"是真实彩票就有的浮动奖语义,可以事前写进规则页。
// 附带收益:消掉了"我摇中了却因为排第 7 名拿不到"这个必然引发争议的坏情形。
//
// 两种做法的最坏支出都恒为 Σ(count × amount),与 rank 模式一模一样 ——
// 概率模式因此**不引入任何新的发行风险**,创建期那道
// Σ(count×amount) ≤ MaxTotalPrizeQuota 的校验一个字都不用改。
//
// # 文本奖档不摊薄
//
// 兑换码没法劈成两半。text 档的 count 在概率制下退化为"预计份数",
// 全部命中者都中 —— 这是"公示概率为真"这条原则的直接推论。
// 它的代价(最坏履行份数 = 全场参与上限)在创建向导里显式摆出来。
//
// roster 必须已按 EntryNo 字节序升序(与 RosterHash 同一份输入)。
func PickWinnersProb(finalSeed, actNo string, roster []RosterLine, tiers []Tier) ([]Winner, error) {
	bands, err := Bands(tiers)
	if err != nil {
		return nil, err
	}

	type hitLine struct {
		line RosterLine
		roll uint32
	}
	hits := make(map[int][]hitLine, len(bands))
	for _, l := range roster {
		r := RollPpm(Ticket(finalSeed, actNo, l.EntryNo))
		for _, b := range bands {
			// 空区间(win_ppm=0)天然不成立,不需要额外分支。
			if r >= b.LoPpm && r < b.HiPpm {
				hits[b.Tier] = append(hits[b.Tier], hitLine{line: l, roll: r})
				break
			}
		}
	}

	winners := make([]Winner, 0, len(roster))
	pos := 0
	for _, b := range bands {
		h := hits[b.Tier]
		if len(h) == 0 {
			continue
		}
		amounts, err := prizeShares(b, len(h))
		if err != nil {
			return nil, err
		}
		for i, x := range h {
			winners = append(winners, Winner{
				Pos: pos, Tier: b.Tier, EntryNo: x.line.EntryNo, UserRef: x.line.UserRef,
				Amount: amounts[i], PrizeType: b.PrizeType, RollPpm: x.roll,
			})
			pos++
		}
	}
	return winners, nil
}

// prizeShares 算出某一档 W 个中签者各自拿多少。
//
// 未超募时每人拿 amount;超募时按 SplitPool **逐字节同一套口径**均分本档预算:
// 逐笔向零截断,残差归最后一位(调用方保证按 entry_no 字节序升序,
// 因此那是 entry_no 最大的那一位)。不新增任何舍入约定 ——
// 验证脚本里已经有一份现成的实现。
func prizeShares(b Band, w int) ([]int64, error) {
	out := make([]int64, w)
	if b.PrizeType == PrizeTypeText {
		// 文本奖没有金额,也无从摊薄。
		return out, nil
	}
	if w <= b.Count {
		for i := range out {
			out[i] = b.Amount
		}
		return out, nil
	}

	budget := b.Amount * int64(b.Count)
	var acc int64
	for i := 0; i < w; i++ {
		var pay int64
		if i == w-1 {
			pay = budget - acc
		} else {
			pay = quotaTruncate(decimal.NewFromInt(budget).Div(decimal.NewFromInt(int64(w))))
		}
		if pay <= 0 {
			// 预算摊到人均不足 1 额度。创建期的
			// `count × amount ≥ 全场参与上限` 已经把这一步堵死,
			// 走到这里说明校验被绕过了 —— 宁可整场结算失败挂起等人,
			// 也绝不让 PlanPayouts 静默跳过一个真中了奖的人(它跳过 amount<=0)。
			return nil, fmt.Errorf("%w: 第 %d 档预算 %d 摊给 %d 人后有人分到 %d",
				ErrPoolNotConserved, b.Tier, budget, w, pay)
		}
		acc += pay
		out[i] = pay
	}
	if acc != budget {
		return nil, fmt.Errorf("%w: 第 %d 档 Σ份额(%d) != 预算(%d)", ErrPoolNotConserved, b.Tier, acc, budget)
	}
	return out, nil
}

// ─────────────────────────── 竞猜奖池分配 ───────────────────────────

// Share 是一笔待出款的份额。
//
// 名字与 model.go 的 Payout 刻意区分:那是状态机行,这是纯计算结果。
// 让计算层认识状态机行会诱使有人在这里塞状态。
type Share struct {
	EntryNo string `json:"entry_no"`
	UserRef string `json:"user_ref"`
	Amount  int64  `json:"amount"`
	// Refund 表示这一笔是退还本金而不是赔付。
	Refund bool `json:"refund"`
}

// ErrPoolNotConserved 表示分配结果与奖池对不上。绝不发出一笔对不上账的钱。
var ErrPoolNotConserved = errors.New("qianye/lottery: 奖池分配不守恒")

// quotaTruncate 是"向零截断 + 走仓库统一的额度饱和转换"的组合。
//
// 先 Truncate(0) 再交给 common.QuotaFromDecimal:后者内部是 Round(0)
// (四舍五入),而奖池分配必须逐笔**截断**,残差单独归给最后一笔 ——
// 逐笔四舍五入会让 Σpay 时而大于 net(平台倒贴)、时而小于 net(黑掉用户的钱)。
// 不自己写 int64() 裸转换:额度的溢出饱和与告警全在那个helper 里。
func quotaTruncate(d decimal.Decimal) int64 {
	return int64(common.QuotaFromDecimal(d.Truncate(0)))
}

// SplitPool 按奖池制分配竞猜赔付。
//
// # 全部猜错 / 无输家 / 无对手盘 → 全额退回本金,手续费一分不收
//
// 手续费的对价是"平台撮合了一次真实发生的再分配"。无人猜中(或无人猜错)时
// 没有任何再分配发生,收费没有对价。更要紧的是激励层面:**只要平台在
// "没人猜中"时有收益,平台就有动机去设置不可能达成的选项** —— 这个漏洞
// 任何审计、任何证据链都补不上,因为竞猜的 winning_option 本来就是管理员
// 手工指定的。把最弱的一环和最大的收益绑在一起是设计事故。
//
// # 为什么残差归最后一笔而不是各自四舍五入
//
// 各自 round 会让 Σpay > net(平台倒贴)或 < net(黑掉用户的钱),两者都不能
// 接受。逐笔截断 + 残差归一人是唯一同时满足"守恒"与"可复现"的做法。
// 残差上界 < 赢家人数(以 quota 为单位,50 万 quota ≈ $1),归 entry_no
// 字节序最大的赢家 —— 顺序确定,第三方能复算出是谁。归平台会在守恒式里多一个
// 不可解释的项,并且给平台一个(哪怕极小的)偏向。
//
// all / winners 都必须已按 EntryNo 字节序升序。
func SplitPool(pool int64, feeBps int, all, winners []RosterLine) (int64, []Share, error) {
	// 池子必须落在单笔出款的容量之内。越界时**整个分配失败**而不是发一笔被
	// 静默钳到 common.MaxQuota 的钱:quotaTruncate 里的饱和会把超出的部分吞掉,
	// 而残差归最后一笔的写法又会把吞掉的量全部推给那一个人 —— 两者叠加的结果
	// 是"守恒式在计算层成立、在出款层完全落不了地",而且与第三方复算不一致。
	// 报名路径已经挡住了池子越界(见 checkCaps),这里是第二道。
	if pool < 0 || pool > int64(common.MaxQuota) {
		return 0, nil, fmt.Errorf("%w: 奖池 %d 超出单笔出款容量 %d", ErrPoolNotConserved, pool, common.MaxQuota)
	}

	var winSum int64
	for _, w := range winners {
		winSum += w.Amount
	}

	if len(winners) == 0 || winSum == pool {
		shares := make([]Share, 0, len(all))
		for _, e := range all {
			shares = append(shares, Share{EntryNo: e.EntryNo, UserRef: e.UserRef, Amount: e.Amount, Refund: true})
		}
		return 0, shares, nil
	}

	fee := quotaTruncate(
		decimal.NewFromInt(pool).Mul(decimal.NewFromInt(int64(feeBps))).Div(decimal.NewFromInt(10000)))
	if fee < 0 {
		fee = 0
	}
	if fee > pool {
		fee = pool
	}
	net := pool - fee

	shares := make([]Share, 0, len(winners))
	var acc int64
	for i, w := range winners {
		var pay int64
		if i == len(winners)-1 {
			pay = net - acc
		} else {
			pay = quotaTruncate(
				decimal.NewFromInt(net).Mul(decimal.NewFromInt(w.Amount)).Div(decimal.NewFromInt(winSum)))
		}
		if pay < 0 {
			return 0, nil, fmt.Errorf("%w: 赢家 %s 的份额为负(net=%d win=%d)", ErrPoolNotConserved, w.EntryNo, net, winSum)
		}
		acc += pay
		shares = append(shares, Share{EntryNo: w.EntryNo, UserRef: w.UserRef, Amount: pay})
	}

	// 守恒式必须精确成立。不成立就整个结算事务回滚 —— 宁可挂起等人,
	// 也绝不发出一笔对不上账的钱。
	if acc+fee != pool {
		return 0, nil, fmt.Errorf("%w: Σpay(%d) + fee(%d) != pool(%d)", ErrPoolNotConserved, acc, fee, pool)
	}
	return fee, shares, nil
}
