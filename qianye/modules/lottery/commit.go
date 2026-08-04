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
const (
	domainRules  = "qylot-rules-v1"
	domainSpec   = "qylot-spec-v1"
	domainCommit = "qylot-commit-v1"
	domainChain  = "qylot-chain-v1"
	domainRoster = "qylot-roster-v1"
	domainFinal  = "qylot-final-v1"
	domainTicket = "qylot-ticket-v1"
	domainURef   = "qylot-uref-v1"
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

// PrizeSpecLine 是抽奖奖档在 spec 原像里的一行。
func PrizeSpecLine(tier int, name string, amountQuota int64, count int) string {
	return strings.Join([]string{deci(tier), name, dec(amountQuota), deci(count)}, SEP)
}

// OptionSpecLine 是竞猜选项在 spec 原像里的一行。
func OptionSpecLine(optNo int, label string, isCatchAll bool) string {
	return strings.Join([]string{deci(optNo), label, boolStr(isCatchAll)}, SEP)
}

// CommitHash 算出活动的承诺哈希。
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

// RosterLine 是有效名单里的一行。
type RosterLine struct {
	EntryNo string `json:"entry_no"`
	UserRef string `json:"user_ref"`
	OptNo   int    `json:"opt_no"`
	Amount  int64  `json:"amount"`
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
}

// Winner 是一个中奖位。
type Winner struct {
	Pos     int    `json:"pos"`
	Tier    int    `json:"tier"`
	EntryNo string `json:"entry_no"`
	UserRef string `json:"user_ref"`
	Amount  int64  `json:"amount"`
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
			winners = append(winners, Winner{
				Pos:     idx,
				Tier:    t.Tier,
				EntryNo: pool[idx].line.EntryNo,
				UserRef: pool[idx].line.UserRef,
				Amount:  t.Amount,
			})
			idx++
		}
	}
	return winners
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
	// 静默钳到 int32 上界的钱:quotaTruncate 里的饱和会把超出的部分吞掉,
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
