package lottery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"gorm.io/gorm"
)

// entry.go —— 报名/投注的扣费链路。本模块唯一一条"扣星屑"的路径。
//
// # 一个扩展库事务
//
// 参与费与派奖全部走扩展库的星屑账本(qianye/modules/stardust),不再碰主库
// users.quota,也就不再有跨库两阶段:没有资金单、没有 pending 票、没有探针、
// 没有补偿。一次参与就是一个扩展库事务:
//
//	reserveEntry(活动行条件 UPDATE 取锁 + 序号 + 锁内闸门 + 链环)
//	→ stardust.Debit(kind=lot_stake,余额行锁 + 条件扣减 + 幂等流水)
//	→ 票以 success 直接落库、推进 chain_head、竞猜选项聚合
//
// 任何一步失败整笔回滚:票不落库、seq 不占、流水不留。**失败的尝试从此不在链上
// 留痕**(design-15 §5.2 e),链上每一个 seq 都对应一张真扣了钱的票。
//
// # 锁序(design-15 §3.3,与 stardust/doc.go 的契约同一条)
//
// 同一事务里**活动行锁必须先于余额行锁**:reserveEntry 在 stardust.Debit 之前,
// 绝不能反过来。派奖 worker 那一侧对活动行的 UPDATE 同样排在 Credit 之前
// (payout.go 的 drivePayout),两条路径在结构上就不可能形成反向顺序。
//
// # 幂等键为什么必须由客户端携带
//
// 报名是**用户发起、可能超时重发**的操作。服务端生成的键无法把"同一次意图的
// 两次请求"归并 —— 用户点一次按钮、网络超时、客户端重试,服务端会当成两次
// 参与各扣一笔。键可以被伪造,由指纹兜住(见 entryFingerprint)。
// 出款那一侧方向相反(见 payout.go),因为那里"谁在重试"是服务端自己。
//
// # 为什么敢让同一活动的报名完全串行化
//
// reserveEntry 的第一条 UPDATE 会在活动行上取 X 锁,同一活动的并发报名因此
// 排队。锁只持有一次扩展库事务的几毫秒,不跨任何主库调用。换来的是:序号分配、
// 名额闸门、每人次数、冷却、邀请人配额、IP 去重全部在同一把锁下完成,不存在
// 任何 TOCTOU,也不需要一整张物化计数表。

// perUserCapHard 同时是两件事:活动行锁内一次读回的本人条目上限,
// 以及 max_entries_per_user / max_attempts_per_user 允许配到的最大值。
//
// 必须是同一个数。锁内用一次读代替四次 COUNT 是刻意的性能取舍,但它成立的前提
// 是"窗口装得下要判定的全部条目";上限一旦能配得比窗口大,超出的那部分就会在
// 用户攒够条目之后静默失效 —— 而失效的是一道运营以为自己开着的闸门。
const perUserCapHard = 500

// maxClientRequestID 是**客户端可以携带的** client_request_id 长度上限。
//
// 超长直接 400 而不是静默哈希:静默哈希会让"两个不同的超长键"有极小概率
// 撞成同一笔参与,而那是用户永远查不出原因的一次丢单。
// 上限来自列宽反推:act_no(27) + ":"(1) + 64 = 92 ≤ qy_lot_entry.idem_key 的 96。
const maxClientRequestID = 64

// defaultPicksPerRequest 是**没配过的活动**一次提交最多买几注(双色球)。
//
// 0 在这里的意思是"没配过"(见 picksCapOf),不是"不限"也不是"一注都不能买"。
const defaultPicksPerRequest = 10

// maxPicksPerRequestHard 是这一格能配到的最大值,由项目方直接给定(999)。
//
// 它必须有限而且不大:一次 N 注提交在服务端是 N 次串行的扩展库事务(每一注
// 一条流水、一条链环),而那正是"每一注各自可复算"的实现方式,不能为了快而合并。
const maxPicksPerRequestHard = 999

// picksCapOf 回答"这一场一次提交最多买几注"。**全模块唯一口径。**
//
// 上界在读的时候再夹一次,不是多余:硬顶是常量而列是数据,一行由更宽松的
// 旧代码(或直接改库)写进来的 5000 会让下面的时间预算算出一个荒唐的截止时刻。
// 夹在这里意味着无论那一列是什么,受理端永远认同一个上界。
func picksCapOf(act *Activity) int {
	n := act.MaxPicksPerRequest
	if n <= 0 {
		return defaultPicksPerRequest
	}
	if n > maxPicksPerRequestHard {
		return maxPicksPerRequestHard
	}
	return n
}

// measuredMsPerPick 是一注在**实测**里的平均耗时(毫秒)。
//
// 本机 MySQL 8.0(127.0.0.1:3307)999 注串行:总 36.07 秒、每注均值 36.1 毫秒
// (那是跨库两阶段时代的数;单事务之后只会更快,数字留作上界)。
//
// 它只用来**说人话**:管理端把 N × 它印在这一格旁边,让运营在填 999 之前就
// 知道这是一次三十几秒的请求。真正的截止时刻走 batchPerPickBudgetMs。
const measuredMsPerPick = 36

// batchPerPickBudgetMs 是每一注在总预算里分到的毫秒数。
//
// 取实测均值(measuredMsPerPick)的约三倍。给足倍数是因为预算耗尽的表现是
// **后半批被截断**,而截断虽然安全(前面每一注都已各自落定、后面的一分钱没扣),
// 用户看到的仍然是一次"只买成一半"。
const batchPerPickBudgetMs = 100

// entryBatchContext 给一次 N 注提交定一个与 N 成正比的截止时刻。
//
// # 为什么不能沿用 guard.ColdContext
//
// 那是**一次**冷路径操作的预算(默认 3 秒)。一批 N 注是 N 次操作,拿一次的预算
// 去装 N 次,后果是 999 注在第 86 注左右被截断 —— 也就是这一格根本配不到 999,
// 配了也只是让用户每次都收到一份"买成了 86 注"的回执。
//
// # 为什么仍然要有上界
//
// 一个不封顶的预算 = 一个不封顶的 HTTP 请求。请求在反向代理的读超时那一刻被切断时
// 用户看到的是 504,而服务端此刻仍在逐注扣钱 —— 那是这条链路上最糟的形状:
// 钱扣了、回执没送到。上界由 lottery.entry_batch_max_ms 给,默认 45 秒,
// 刻意留在常见反代默认读超时(60 秒)之下;部署方的反代更严格时把它调小。
func entryBatchContext(parent context.Context, picks int) (context.Context, context.CancelFunc) {
	base := config.Get().Runtime.ColdPathTimeoutMs
	if base <= 0 {
		base = 3000
	}
	if picks < 1 {
		picks = 1
	}
	budget := int64(base) + int64(picks)*batchPerPickBudgetMs
	if max := int64(config.Get().Lottery.EntryBatchMaxMs); max > 0 && budget > max {
		budget = max
	}
	// 下界兜住一个被配得比冷路径预算还小的 entry_batch_max_ms:单注提交拿到的
	// 预算不能比 guard.ColdContext 少。
	if budget < int64(base) {
		budget = int64(base)
	}
	return context.WithTimeout(parent, time.Duration(budget)*time.Millisecond)
}

// maxEntryRequestID 是 ChargeEntry 允许的 ClientRequestId 长度上限。
//
// 它比 maxClientRequestID 大 4:多注提交给第 i(i ≥ 1)注派生的幂等键是
// `<客户端的 crid>#<i>`,i 至多三位十进制(maxPicksPerRequestHard = 999 →
// 最大下标是 998,后缀 `#998` 占 4 字节)。
// 列宽仍然对得上,而且是**刚好**对上:act_no(27) + ":"(1) + 64 + "#998"(4)
// = 96 = qy_lot_entry.idem_key 的列宽。再抬高硬顶必须先加宽这一列。
//
// 两个常量分开是因为它们约束的是两件事:客户端传进来的那一份由 handler 挡在
// 64(用户输入的边界),而 ChargeEntry 自己认的是列宽反推出来的那一份 ——
// 合成一个数就必然要么拒掉一个合法的派生键、要么把用户输入的上界悄悄放宽。
const maxEntryRequestID = maxClientRequestID + 4

// idemScopeStake 是参与费在 qy_sd_ledger 上的幂等作用域;幂等键就是票的 idem_key
// (act_no:crid),不加前缀:`act_no(27)+':'+crid(64)+'#998'` 已顶满 96。
const idemScopeStake = "lot_stake"

// refTypeEntry 是参与费流水的关联单据类型(qy_sd_ledger.ref_type)。
const refTypeEntry = "lot_entry"

// EntryInput 是一次参与请求的全部输入。
type EntryInput struct {
	ActNo           string
	UserId          int
	ClientRequestId string
	// OptNo 抽奖必须为 0,竞猜必须命中本活动的一个选项。
	OptNo int
	// Amount 只有竞猜可以自选(受单注上下限约束);抽奖恒等于 StakeQuota,
	// 用户不能自己指定金额。单位是星屑整数。
	Amount int64
	// Pick 只有双色球(draw_mode=ball)必填,其余活动必须为空。
	// 机选是**纯前端行为**:服务端不区分自选与机选,因为号码一旦进链,
	// 两者的可验证性完全一样,而服务端多一条随机路径就多一处要证明其公正的地方。
	Pick      string
	ClientIp  string
	UserAgent string
	// BatchIndex 是这一注在**同一次提交**里的下标,0 = 第一注(或单注提交)。
	//
	// 它只被冷却闸门读:一次买 N 注在服务端是 N 次串行的 ChargeEntry,相邻两注
	// 之间只隔几毫秒,而 cooldown_seconds 判的是"距上一条参与多久"—— 不区分的话
	// 任何配了冷却的活动都买不了第二注,而失败发生在第二注扣费之前、用户已经
	// 看过总额了。冷却拦的是"在 close 前用脚本连发把名额吃光",一次提交是**一个
	// 动作**,批内不再互相计时;下一次提交仍然要等,因为 lastAt 已经推到了本批
	// 最后一注上。
	BatchIndex int
}

// PayPasswordRequired 判断这一笔参与是否需要支付密码(D-12:星屑路径保留验密)。
//
// 星屑经商城能换成第三方卡密、实物与文本奖兑换码,是可变现出平台的:盗号者能用
// "参与抽奖"把星屑烧光,而且不留下划转/提现那样显眼的痕迹。阈值由
// lottery.pay_password_threshold_stardust 控制(单位星屑),判定放在这里而不是
// handler 里,是为了让"哪些操作需要二次验证"只有一处口径。
func PayPasswordRequired(stakeQuota int64) bool {
	threshold := config.Get().Lottery.PayPasswordThresholdStardust
	return threshold > 0 && stakeQuota >= threshold
}

// ChargeEntry 执行一次参与扣费。
//
// 返回的 Entry 就是用户的报名回执:entry_no + seq + chain_hash 三件套是他
// 事后举证"我确实在名单里、而且是在第 N 位"的全部凭据。
//
// 幂等语义:同一个 (act_no, client_request_id) 原样重放拿回原票、账本上只有一行
// 流水;换了要素(选项 / 号码 / 金额 / 用户)重放 409。
func ChargeEntry(ctx context.Context, in EntryInput) (*Entry, error) {
	crid := strings.TrimSpace(in.ClientRequestId)
	if crid == "" || len(crid) > maxEntryRequestID {
		return nil, errBadRequestID
	}
	// 幂等键必须落在三方言比较一致的字符集里。
	//
	// 判据放在这里而不是只放在 handler:ChargeEntry 是本模块唯一的扣费入口,
	// handler 只是它的一个调用方。放过一个大写字母或一个重音字母,
	// MySQL 的默认排序规则(0900_ai_ci)就会把它和另一个键判成同一个,
	// 而 PostgreSQL / SQLite 不会 —— 同一份代码在两种方言上给出相反的
	// 扣钱结果。`#` 在允许集里,它是服务端派生多注键的分隔符(batchRequestId)。
	if !qymodel.IsCollationNeutralIdemKey(crid) {
		return nil, errBadRequestID
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	// 句柄一次性绑上调用方的预算。逐条 WithContext 漏一条就等于在这条链路上
	// 开了一个没有上界的口子:语句级预算只对 WithContext 的语句生效,
	// 而"等一个空闲连接"这一段在 Background 下无界。
	gdb = gdb.WithContext(ctx)

	act, err := loadActivityByNo(ctx, in.ActNo)
	if err != nil {
		return nil, err
	}
	// 转盘只经转动接口参与(wheel.go 的 spinTx):它的每一张票都必须带着摇号结果
	// 进链,一张经这里落下的"普通票"会让整条链在验证者那里断在它那一环。
	if act.DrawMode == DrawModeWheel {
		return nil, errWheelUseSpins
	}
	rules, err := ParseRules(act.RulesText)
	if err != nil {
		return nil, err
	}
	amount, err := acceptAmount(act, in)
	if err != nil {
		return nil, err
	}
	pick, err := acceptPick(act, in)
	if err != nil {
		return nil, err
	}
	idemKey := buildIdemKey(act.ActNo, crid)
	fingerprint := entryFingerprint(act, in.UserId, amount, in.OptNo, pick)

	// (a) 事务前按 uk(act_id, idem_key) 读票。
	//
	// **原样重放不是新参与。** 同一个 client_request_id 重发时,那一笔已经扣过钱、
	// 票已经进了哈希链;让它拿回原始回执正是幂等键存在的唯一理由。这一步排在玩法
	// 闸门与资格判定**之前**:一个已付费的用户在重试(移动网络、双击、页面刷新)时
	// 若被 409「暂不受理新的参与」顶回去,那句话暗示什么都没发生,而钱已经扣了。
	if prior, err := loadEntryByIdemKey(ctx, gdb, act.Id, idemKey); err != nil {
		return nil, err
	} else if prior != nil {
		return replayEntry(prior, fingerprint, in.UserId)
	}

	// 玩法被隐藏时不再受理**新的**参与,但这一场的其余一切照旧:已经收下的
	// 参与照常封盘、开奖、派奖、退款,已参与的人照常查票与领奖(见 play.go)。
	//
	// 闸门放在这里而不是 handler 里:这是"新参与"的唯一执行点,放到 HTTP 层
	// 意味着日后任何一条新入口都要记得再抄一遍。
	//
	// 读不到配置时 effectiveCtx 回落到基线 = 全部显示,即**失败放行**。
	// 这是刻意的:这一项是展示口径,不是资金或资格闸门(那两类在下面的活动
	// 行锁与余额行锁里,一条都没有被绕过),而失败拒绝会让扩展库抖一下就变成
	// 全站报名中断。
	if !effectiveCtx(ctx).playShown(playOf(act.Kind, act.DrawMode)) {
		return nil, errPlayHidden
	}

	// 锁外的资格判定只为**尽早报错**,不是权威判定点(见 eligibility.go 的口径)。
	// 频次类的权威判定在活动行锁内(checkCaps);"够不够扣"由 stardust.Debit 的
	// 条件 UPDATE 承担。status / group / min_quota 三项没有锁内复检的落点,
	// 接受 LoadSubject → Debit 之间的窗口(理由见 eligibility.go 文件头)。
	subject, err := LoadSubject(ctx, in.UserId, rules, act.CreatedBy)
	if err != nil {
		return nil, err
	}
	if missing := Evaluate(rules, subject, amount, common.GetTimestamp(), playOf(act.Kind, act.DrawMode)); len(missing) > 0 {
		return nil, ineligibleWith(missing)
	}

	salts, err := loadSalts(ctx, gdb, act.Id)
	if err != nil {
		return nil, err
	}

	entry := &Entry{
		EntryNo:             newEntryNo(),
		ActId:               act.Id,
		IdemKey:             idemKey,
		Fingerprint:         fingerprint,
		UserId:              in.UserId,
		Username:            truncateRunes(subject.Username, 64),
		InviterId:           subject.InviterId,
		UserRef:             UserRef(salts.RefSalt, in.UserId),
		OptNo:               in.OptNo,
		Pick:                pick,
		Amount:              amount,
		Status:              EntrySuccess,
		EligibilitySnapshot: SnapshotJSON(subject),
		IpHash:              hmacHex(salts.IpSalt, in.ClientIp),
		UaHash:              hmacHex(salts.IpSalt, in.UserAgent),
		CreatedAt:           common.GetTimestamp(),
	}

	// (b) 一个扩展库事务:锁活动行 → 扣星屑 → 落票。
	err = gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, act, rules, entry, in.BatchIndex)
	})
	switch {
	case err == nil:
		entry.SettledAt = entry.CreatedAt
		return entry, nil
	case errors.Is(err, errEntryReplayRace):
		// (c) 并发重放:同一个 crid 的另一路刚刚落定,本次事务已整体回滚
		// (seq 不占、流水不留),回到 (a) 拿回它那张票。
		prior, err := loadEntryByIdemKey(ctx, gdb, act.Id, idemKey)
		if err != nil {
			return nil, err
		}
		if prior == nil {
			return nil, wrapInternal("受理参与", errors.New("幂等键撞键却读不回已有票"))
		}
		return replayEntry(prior, fingerprint, in.UserId)
	case errors.Is(err, stardust.ErrInsufficient):
		// (d) 星屑不足:整笔回滚,票不落库、seq 不占。
		return nil, errInsufficientQuota()
	}
	if _, ok := AsBizError(err); ok {
		return nil, err
	}
	db.MarkFailure(err)
	return nil, wrapInternal("受理参与", err)
}

// errEntryReplayRace 是"票在本事务提交之前被另一路同键请求落下了"的内部信号。
// 它只在 ChargeEntry 里流转,永远不会回给用户。
var errEntryReplayRace = errors.New("qianye/lottery: 幂等键并发撞键")

// settleEntryTx 是一次参与在扩展库事务里的全部写入。**必须在调用方的事务内执行。**
//
// 顺序就是锁序:活动行(reserveEntry)→ 余额行(stardust.Debit)。Debit 返回任何
// 非 nil error 都必须让整个事务回滚 —— 包括 ErrInsufficient。吞掉它继续提交的
// 后果写在 stardust/doc.go:那一行幂等流水会残留,用户下一次重试被判成"重放"
// 而余额一分没动。
func settleEntryTx(tx *gorm.DB, act *Activity, rules Rules, e *Entry, batchIndex int) error {
	cur, err := reserveEntry(tx, act, rules, e, batchIndex)
	if err != nil {
		return err
	}
	res, err := stardust.Debit(tx, stardust.Posting{
		UserId:    e.UserId,
		Kind:      stardust.KindLotStake,
		Amount:    e.Amount,
		IdemScope: idemScopeStake,
		IdemKey:   e.IdemKey,
		RefType:   refTypeEntry,
		RefNo:     e.EntryNo,
		ActNo:     act.ActNo,
		Remark:    "参与活动扣除",
	})
	if err != nil {
		return err
	}
	if !res.Inserted {
		// 幂等流水已存在而票不存在:只可能是另一路同键请求刚提交。回滚本事务,
		// 让调用方回到事务前的读票分支。
		return errEntryReplayRace
	}
	e.OrderNo = res.LedgerNo
	e.QuotaBefore = res.BalanceAfter + e.Amount
	e.QuotaAfter = res.BalanceAfter
	e.SettledAt = e.CreatedAt

	if err := tx.Create(e).Error; err != nil {
		if db.IsDuplicateKey(err) {
			return errEntryReplayRace
		}
		return err
	}
	if err := tx.Model(&Activity{}).Where("id = ?", act.Id).
		Update("chain_head", e.ChainHash).Error; err != nil {
		return err
	}
	if cur.Kind == KindGuess {
		// 选项上的投注聚合。失败时随整个事务回滚,与票同生共死。
		r := tx.Model(&Option{}).
			Where("act_id = ? AND opt_no = ?", act.Id, e.OptNo).
			Updates(map[string]any{
				"bet_quota": gorm.Expr("bet_quota + ?", e.Amount),
				"bet_count": gorm.Expr("bet_count + 1"),
			})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return errBadOption
		}
	}
	return nil
}

// replayEntry 把一张已成交的票当作原样重放的回执返回。
//
// 指纹不一致就是换参重放(用户提交后风向变了,用同一个 request_id 换个选项重发),
// 一律 409。只记日志不写审计 —— 审计表是事后仲裁的唯一凭据,不能让任何人凭一个
// 被拒绝的请求往里塞行。
func replayEntry(prior *Entry, fingerprint string, userId int) (*Entry, error) {
	if prior.Fingerprint != fingerprint {
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 用户 %d 用同一个 client_request_id 提交了要素不同的参与,已拒绝(票 %s)",
			userId, prior.EntryNo))
		return nil, errIdemConflict
	}
	return prior, nil
}

// entryFingerprint 是一次参与的**用户请求要素**摘要,落在 qy_lot_entry.fingerprint。
//
// 只收用户这次请求说了什么(用户、金额、活动、选项、号码),不收任何服务端
// 派生量 —— entry_no 由 newEntryNo() 每次请求现摇,
// 一旦进指纹,同一个 client_request_id 的两次提交必然算出两个不同指纹,
// 幂等键就在结构上永远命中不了。
//
// 选项与号码必须在:换个选项 / 换组号用同一个 request_id 重发,不纳入指纹就会
// 幂等命中返回成功,而实际投的仍是旧选项 / 旧号码。
func entryFingerprint(act *Activity, userId int, amount int64, optNo int, pick string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		refTypeEntry, idemScopeStake,
		strconv.Itoa(userId), strconv.FormatInt(amount, 10),
		act.ActNo, strconv.Itoa(optNo), pick,
	}, SEP)))
	return hex.EncodeToString(sum[:])
}

// acceptPick 校验并归一化本次的选号。
//
// 归一化之后的字节就是落库、进链、进名单的那一份 —— 绝不读出来重序列化。
// 非双色球活动一律拒绝携带选号,而不是静默忽略:静默忽略会让一个填了号码的
// 请求照常成功,用户以为自己买的是那组号。
func acceptPick(act *Activity, in EntryInput) (string, error) {
	if act.Kind != KindDraw || act.DrawMode != DrawModeBall {
		if strings.TrimSpace(in.Pick) != "" {
			return "", errPickNotAllowed
		}
		return "", nil
	}
	_, _, normalized, err := ParsePick(in.Pick,
		act.BallRedPool, act.BallRedPick, act.BallBluePool, act.BallBluePick)
	if err != nil {
		return "", errBadPickInput
	}
	return normalized, nil
}

// loadEntryByIdemKey 回答「这个幂等键在本场活动上已经有票了吗」,有就把票读回来。
//
// 票与流水是同一个扩展库事务写下的,所以一个真正全新的请求在这里必然查不到行。
// 读失败一律报错而不是当作"没有":这里放行的是**新参与**,一次查询故障若被
// 当成"没有票",一笔已成交的参与就会被再扣一遍。
func loadEntryByIdemKey(ctx context.Context, gdb *gorm.DB, actId int64, idemKey string) (*Entry, error) {
	var e Entry
	err := gdb.WithContext(ctx).Where("act_id = ? AND idem_key = ?", actId, idemKey).Take(&e).Error
	if err == nil {
		return &e, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	db.MarkFailure(err)
	return nil, wrapInternal("读取参与明细", err)
}

// buildIdemKey 拼出票与流水共用的幂等键。
//
// act_no 前缀是必需的:同一个 client_request_id 在两场活动上是两笔不同的参与,
// 不加前缀会让第二场的报名幂等命中第一场的票直接返回"成功"。
func buildIdemKey(actNo, clientRequestId string) string {
	return actNo + ":" + clientRequestId
}

// acceptAmount 校验并定出本次要扣的星屑数。
func acceptAmount(act *Activity, in EntryInput) (int64, error) {
	if act.Kind == KindDraw {
		// 抽奖不允许用户指定金额:那等于让用户自己定价。
		if in.OptNo != 0 {
			return 0, errBadOption
		}
		return act.StakeQuota, nil
	}
	if in.OptNo <= 0 {
		return 0, errBadOption
	}
	amount := in.Amount
	if amount < 0 {
		// 显式的负数与"没填"在 wire 上完全可区分,不能合并成同一条回落分支。
		// 参与是不可逆消费:把一个明确写着 -5 的请求静默当成"按单注额下注",
		// 等于替用户下了一笔他没打算下的注并真的扣钱。与本函数对超上限、
		// 对非法选项一律 400 的口径保持一致。
		return 0, errBadAmount
	}
	if amount == 0 {
		// 0 是 int64 的零值,也就是"请求里没有 amount 字段"——竞猜不填金额时
		// 按单注额,与抽奖同形。这是前端当前唯一在走的路径。
		amount = act.StakeQuota
	}
	if act.BetMinQuota > 0 && amount < act.BetMinQuota {
		return 0, errBadAmount
	}
	if act.BetMaxQuota > 0 && amount > act.BetMaxQuota {
		return 0, errBadAmount
	}
	// 活动没填单注上限时兜到 **lottery.max_stake_stardust**,不是算术上界。
	// 后者是溢出边界而不是运营闸门:兜到那里等于一个没填上限的竞猜可以让人
	// 一次扣掉天文数字的星屑,而 max_stake_stardust 恰恰是配置里写明"决定单笔扣款
	// 上限、不允许在线改"的那一项。
	if limit := config.Get().Lottery.MaxStakeStardust; limit > 0 && amount > limit {
		return 0, errBadAmount
	}
	if amount > int64(common.MaxQuota) {
		return 0, errBadAmount
	}
	return amount, nil
}

// ─────────────────────── 活动行锁内的预占 ───────────────────────

// reserveEntry 在参与事务内完成活动行上的全部锁内判定,并给票配好序号与链环。
//
// 第一条 UPDATE 同时承担五件事:活动状态复检、时间窗复检、全场序号分配、
// 有效计数与奖池累加,以及在活动行上取得 X 锁。之后的每一次 COUNT 都在这把锁的
// 保护下,因此"两个并发请求同时读到旧计数、同时通过上限校验"在结构上不可能发生。
//
// 时间窗判 `now < close_at`,**不用** entry_close_grace_seconds:grace 的用途是给
// 两阶段的 pending 单留收敛窗口,单事务没有 pending;close_at 进承诺原像,
// 任何更早的截止都不影响验证。那个配置键仍由创建校验消费(close_at − open_at
// 必须大于它)。
//
// 有效计数与奖池在这里就累加而不是等票落库之后:票在同一个事务里以 success
// 落库,事务失败时这两个数随之回滚,不需要任何"失败时回退计数"的代码。
func reserveEntry(tx *gorm.DB, act *Activity, rules Rules, e *Entry, batchIndex int) (*Activity, error) {
	now := common.GetTimestamp()
	res := tx.Model(&Activity{}).
		Where("id = ? AND status = ? AND open_at <= ? AND ? < close_at",
			act.Id, StatusPublished, now, now).
		Updates(map[string]any{
			"entry_seq":    gorm.Expr("entry_seq + 1"),
			"active_count": gorm.Expr("active_count + 1"),
			"pool_quota":   gorm.Expr("pool_quota + ?", e.Amount),
			"updated_at":   now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		// 分不清"没开始""已结束""已取消"三种情况在这里是刻意的:再查一次去
		// 区分它们要多一次往返,而用户拿到的动作都一样(刷新页面)。
		return nil, errClosingSoon
	}

	// X 锁已持有,回读到的必然是本次更新之后的值 —— 效果等同 FOR UPDATE
	// 且省一次往返。
	var cur Activity
	if err := tx.Where("id = ?", act.Id).Take(&cur).Error; err != nil {
		return nil, err
	}
	e.Seq = cur.EntrySeq

	if err := checkCaps(tx, &cur, e, batchIndex); err != nil {
		return nil, err
	}

	// 链在这里推进。prev 取活动行上的 chain_head,chain_0 = commit_hash。
	prev := cur.ChainHead
	if prev == "" {
		prev = cur.CommitHash
	}
	e.PrevHash = prev
	// lot-v2 的链末尾多一个选号分量(非双色球恒为空串)。按活动自己的 algo
	// 分派。金额数字现在就是星屑整数,原像形状一个字节都没变。
	e.ChainHash = ChainNextFor(cur.Algo, prev, cur.ActNo, e.Seq,
		e.EntryNo, e.UserRef, e.OptNo, e.Amount, e.Pick)
	return &cur, nil
}

// checkCaps 是活动行 X 锁内的频次类判定。
//
// 全部用 COUNT 而不是物化计数表,而且判据里没有任何"失败条目":单事务下失败的
// 尝试整笔回滚,库里的每一行票都是真扣了星屑的 —— 这正是不建计数表的最大收益,
// 计数表必须写"失败时带非负保护地回退",而那段代码一旦写错,上限会永久失效。
func checkCaps(tx *gorm.DB, cur *Activity, e *Entry, batchIndex int) error {
	// 全场名额。active_count 已经含本次,因此判定用 >。
	if cur.MaxTotalEntries > 0 && cur.ActiveCount > cur.MaxTotalEntries {
		return errCapReached
	}
	// 竞猜奖池的上界。赔付是从池子里切出来的,而每一笔入账都要过 stardust.Credit
	// 的 `amount ≤ MaxQuota` —— 池子一旦越过那条线,独中的那个人的赔付在入账
	// 入口就会被拒,活动永远收不了尾。上限在这里挡住,而不是等到结算时才发现
	// 钱发不出去。抽奖不受这条限制:它的池子只是参与费收入,不参与分配。
	// pool_quota 已经含本次。
	if cur.Kind == KindGuess && cur.PoolQuota > int64(common.MaxQuota) {
		return errCapReached
	}
	// 双色球受同一条限制,而且理由更硬:它的参与费**有一部分要进期次奖池**
	// (pool_share_bps),没派出去的部分在收尾时滚存回系列。系列池一旦越过 common.MaxQuota,
	// checkBallPoolCovers 的 `open > MaxQuota` 分支会让这个系列**永久开不出新一期**,
	// 而且没有任何接口能把池子降下来(handleCloseSeries 只会把整池作废)。
	// 判据用"本期真正可派发的池子",与 ballPoolOpen / settleSeriesPool 同一个式子:
	// 它 ≤ MaxQuota 时,滚存回去的 carry 也一定 ≤ MaxQuota。
	if cur.DrawMode == DrawModeBall && cur.PoolShareBps > 0 {
		in := cur.PoolQuota * int64(cur.PoolShareBps) / 10000
		if cur.PoolOpenQuota > int64(common.MaxQuota)-in {
			return errCapReached
		}
	}
	// 全场**人数**上限与条目上限分开:允许多次参与时,人数上限拦不住一个人
	// 刷 1000 笔,反过来条目上限也拦不住 1000 个小号各来一笔。
	if cur.MaxTotalUsers > 0 {
		var users int64
		if err := tx.Model(&Entry{}).
			Where("act_id = ? AND user_id <> ?", cur.Id, e.UserId).
			Distinct("user_id").Count(&users).Error; err != nil {
			return err
		}
		if users >= int64(cur.MaxTotalUsers) {
			return errCapReached
		}
	}

	// 本人的既有条目一次读出来,后面几项判定共用 —— 每项各查一次是几次往返,
	// 而它们全都在同一把锁下,读一次就够。
	//
	// 窗口大小与 perUserCapHard 是**同一个数**:创建活动时两个每人上限都不许
	// 超过它,所以窗口里一定装得下判定所需的全部条目。两者一旦脱钩,超出窗口的
	// 那部分上限就会静默失效(界面写着"每人 1000 次",实际是无上限)。
	var mine []Entry
	if err := tx.Where("act_id = ? AND user_id = ?", cur.Id, e.UserId).
		Order("id desc").Limit(perUserCapHard).Find(&mine).Error; err != nil {
		return err
	}
	var lastAt int64
	for i := range mine {
		if mine[i].CreatedAt > lastAt {
			lastAt = mine[i].CreatedAt
		}
	}
	if cur.MaxEntriesPerUser > 0 && len(mine) >= cur.MaxEntriesPerUser {
		return errUserCap
	}
	// 尝试上限。它在两阶段时代把**失败**的尝试也算进来(失败条目永久占一个 seq);
	// 单事务下失败的尝试整笔回滚、不留任何行,于是它能数到的只有成功的票 ——
	// 与每人参与上限是同一个集合,只是运营可以把它配得更紧。规则字段保留是因为
	// 它进 rules_hash;管理端说明照实写明这一点。
	if cur.MaxAttemptsPerUser > 0 && len(mine) >= cur.MaxAttemptsPerUser {
		return errAttemptCap
	}
	// 冷却。次数上限防不住"在 close 前 1 秒用脚本连发把全场名额吃光"。
	//
	// batchIndex > 0 = 这一注是**同一次提交**里的第 2..N 注。批内不计时:
	// 相邻两注只隔几毫秒,不豁免的话任何配了冷却的活动都买不了第二注,
	// 而用户是在看过总额、按下确认之后才被顶回来的。豁免的边界很窄 ——
	// 单次批量由 maxPicksPerRequest 封顶、每人总数仍受 max_entries_per_user
	// 约束,而**下一次**提交照旧要等,因为 lastAt 已经推到本批最后一注上。
	if batchIndex == 0 && cur.CooldownSeconds > 0 && lastAt > 0 &&
		common.GetTimestamp()-lastAt < int64(cur.CooldownSeconds) {
		return errCooldown
	}

	if cur.MaxPerInviter > 0 && e.InviterId > 0 {
		var n int64
		if err := tx.Model(&Entry{}).
			Where("act_id = ? AND inviter_id = ? AND user_id <> ?",
				cur.Id, e.InviterId, e.UserId).
			Distinct("user_id").Count(&n).Error; err != nil {
			return err
		}
		if n >= int64(cur.MaxPerInviter) {
			return errInviterCap
		}
	}

	// IP 去重默认关闭,理由写在 Activity.DedupIp 上:它会误伤共用出口的真实
	// 用户,而 X-Forwarded-For 可被伪造 —— 防御方向恰好反了。
	if cur.DedupIp && e.IpHash != "" {
		var n int64
		if err := tx.Model(&Entry{}).
			Where("act_id = ? AND ip_hash = ? AND user_id <> ?",
				cur.Id, e.IpHash, e.UserId).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return errIPCap
		}
	}
	return nil
}

// ─────────────────────────── 读取辅助 ───────────────────────────

func loadActivityByNo(ctx context.Context, actNo string) (*Activity, error) {
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	var a Activity
	if err := gdb.WithContext(ctx).Where("act_no = ?", actNo).Take(&a).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errActivityNotFound
		}
		db.MarkFailure(err)
		return nil, wrapInternal("读取活动", err)
	}
	if a.Status != StatusPublished {
		return nil, errNotOpen
	}
	return &a, nil
}

// activitySalts 只取盐,**不取种子**。
//
// 报名路径需要 ref_salt 与 ip_salt,但绝不需要 seed。用一个独立的投影结构体
// 而不是把整行 Seed 读出来,是为了让"包内只有两个函数触碰种子"这条约束
// 在语法层面成立,而不是靠调用方自觉不去读那个字段。
type activitySalts struct {
	RefSalt string `gorm:"column:ref_salt"`
	IpSalt  string `gorm:"column:ip_salt"`
}

func loadSalts(ctx context.Context, gdb *gorm.DB, actId int64) (*activitySalts, error) {
	var s activitySalts
	err := gdb.WithContext(ctx).Model(&Seed{}).
		Select("ref_salt", "ip_salt").
		Where("act_id = ?", actId).Take(&s).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 没有种子行的活动不可能被发布(见 api_admin.go 的 handleCreateActivity)。
			// 走到这里说明数据被手工改过,一律拒绝而不是补一个新盐 ——
			// 补新盐会让同一个人在同一场活动里拿到两个不同的 user_ref。
			return nil, wrapInternal("读取活动随机量", gorm.ErrRecordNotFound)
		}
		db.MarkFailure(err)
		return nil, wrapInternal("读取活动随机量", err)
	}
	return &s, nil
}

// hmacHex 对 IP / UA 做每活动加盐的 HMAC。空输入返回空串(不落一个"空值的哈希",
// 那会让所有没有 IP 的请求互相撞成同一个"用户")。
func hmacHex(salt, msg string) string {
	if msg == "" || salt == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// truncateRunes 按 rune 边界截断。
//
// 裸字节切会把中文切出非法尾巴,MySQL 的 utf8mb4 列会整条 UPDATE 报 1366
// 而不是截断 —— 那会让一次本该成功的写入整个失败。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
