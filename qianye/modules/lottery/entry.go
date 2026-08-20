package lottery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"gorm.io/gorm"
)

// entry.go —— 报名/投注的扣费链路。本模块唯一一条"主库减额度"的路径。
//
// # 幂等键为什么必须由客户端携带
//
// 报名是**用户发起、可能超时重发**的操作。服务端生成的键无法把"同一次意图的
// 两次请求"归并 —— 用户点一次按钮、网络超时、客户端重试,服务端会当成两次
// 参与各扣一笔。键可以被伪造,由指纹兜住(见 fundingFacts)。
// 这与 transfer 是同一取舍;出款那一侧方向相反(见 payout.go),
// 因为那里"谁在重试"是服务端自己。
//
// # 为什么敢让同一活动的报名完全串行化
//
// LocalDetail 里的第一条 UPDATE 会在活动行上取 X 锁,同一活动的并发报名因此
// 排队。这是**读了 twophase/execute.go 之后才敢下的结论**:阶段1(扩展库事务)
// 与阶段2(主库事务)是两个独立事务,活动行锁在阶段1 提交时就已释放,
// **不跨主库调用**。锁的持有时间是几毫秒,而不是一次跨库往返。
//
// 换来的是:序号分配、名额闸门、每人次数、冷却、邀请人配额、IP 去重全部在
// 同一把锁下完成,不存在任何 TOCTOU,也不需要一整张物化计数表 ——
// 而计数表必须写"失败时带非负保护地回退计数",那是本仓最容易写错的一类代码。
// 这里的 COUNT 条件是 status IN ('pending','success'),失败条目自然停止计数,
// 不需要任何回滚。

// idemScopeEntry 与 qy_fund_orders 的 (idem_scope, idem_key) 唯一索引配套。
const idemScopeEntry = "lottery_entry"

// perUserCapHard 同时是两件事:活动行锁内一次读回的本人条目上限,
// 以及 max_entries_per_user / max_attempts_per_user 允许配到的最大值。
//
// 必须是同一个数。锁内用一次读代替四次 COUNT 是刻意的性能取舍,但它成立的前提
// 是"窗口装得下要判定的全部条目";上限一旦能配得比窗口大,超出的那部分就会在
// 用户攒够条目之后静默失效 —— 而失效的是一道运营以为自己开着的闸门。
const perUserCapHard = 500

// maxClientRequestID 是 client_request_id 的长度上限。
//
// 超长直接 400 而不是静默哈希:静默哈希会让"两个不同的超长键"有极小概率
// 撞成同一笔参与,而那是用户永远查不出原因的一次丢单。
// 上限来自列宽反推:act_no(27) + ":"(1) + 64 = 92 ≤ qy_lot_entry.idem_key 的 96。
const maxClientRequestID = 64

// EntryInput 是一次参与请求的全部输入。
type EntryInput struct {
	ActNo           string
	UserId          int
	ClientRequestId string
	// OptNo 抽奖必须为 0,竞猜必须命中本活动的一个选项。
	OptNo int
	// Amount 只有竞猜可以自选(受单注上下限约束);抽奖恒等于 StakeQuota,
	// 用户不能自己指定金额。
	Amount int64
	// Pick 只有双色球(draw_mode=ball)必填,其余活动必须为空。
	// 机选是**纯前端行为**:服务端不区分自选与机选,因为号码一旦进链,
	// 两者的可验证性完全一样,而服务端多一条随机路径就多一处要证明其公正的地方。
	Pick      string
	ClientIp  string
	UserAgent string
}

// PayPasswordRequired 判断这一笔参与是否需要支付密码。
//
// 参与是不可逆消费:盗号者能用"参与抽奖"把余额烧光,而且不留下划转/提现
// 那样显眼的痕迹。阈值由 lottery.pay_password_threshold_quota 控制,
// 判定放在这里而不是 handler 里,是为了让"哪些操作需要二次验证"只有一处口径。
func PayPasswordRequired(stakeQuota int64) bool {
	threshold := config.Get().Lottery.PayPasswordThresholdQuota
	return threshold > 0 && stakeQuota >= threshold
}

// ChargeEntry 执行一次参与扣费。
//
// 返回的 Entry 就是用户的报名回执:entry_no + seq + chain_hash 三件套是他
// 事后举证"我确实在名单里、而且是在第 N 位"的全部凭据。
func ChargeEntry(ctx context.Context, in EntryInput) (*Entry, error) {
	crid := strings.TrimSpace(in.ClientRequestId)
	if crid == "" || len(crid) > maxClientRequestID {
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
	rules, err := ParseRules(act.RulesText)
	if err != nil {
		return nil, err
	}
	amount, err := acceptAmount(act, in)
	if err != nil {
		return nil, err
	}

	// 锁外的资格判定只为**尽早报错**,不是权威判定点(见 eligibility.go 的口径)。
	// 权威判定在活动行锁内(频次类)与主库行锁内(status/group/余额)。
	subject, err := LoadSubject(ctx, in.UserId, rules, act.CreatedBy)
	if err != nil {
		return nil, err
	}
	if missing := Evaluate(rules, subject, amount, common.GetTimestamp()); len(missing) > 0 {
		return nil, ineligibleWith(missing)
	}

	pick, err := acceptPick(act, in)
	if err != nil {
		return nil, err
	}

	salts, err := loadSalts(ctx, gdb, act.Id)
	if err != nil {
		return nil, err
	}

	entry := &Entry{
		EntryNo:             newEntryNo(),
		ActId:               act.Id,
		IdemKey:             buildIdemKey(act.ActNo, crid),
		UserId:              in.UserId,
		Username:            truncateRunes(subject.Username, 64),
		InviterId:           subject.InviterId,
		UserRef:             UserRef(salts.RefSalt, in.UserId),
		OptNo:               in.OptNo,
		Pick:                pick,
		Amount:              amount,
		Status:              EntryPending,
		EligibilitySnapshot: SnapshotJSON(subject),
		IpHash:              hmacHex(salts.IpSalt, in.ClientIp),
		UaHash:              hmacHex(salts.IpSalt, in.UserAgent),
		CreatedAt:           common.GetTimestamp(),
	}

	var snap quotaSnapshot
	req := fundingFacts(act, entry)
	req.LocalDetail = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		entry.OrderNo = o.OrderNo
		return reserveEntry(tx, act, rules, entry)
	}
	req.MainApply = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		return debitMainQuota(tx, rules, entry, &snap)
	}
	req.AfterCommit = func(o *qymodel.FundOrder) {
		afterDebit(entry, o.OrderNo, snap)
	}
	req.LocalCommit = func(tx *gorm.DB, o *qymodel.FundOrder) error {
		return markEntrySuccess(tx, entry.EntryNo, &snap)
	}

	order, execErr := twophase.Execute(ctx, req)
	if execErr != nil {
		// 指纹冲突必须在任何回滚之前拦掉:那条路径会拿本次的失败原因去结算
		// **原单**的明细行与计数,等于让攻击者用一个伪造请求污染别人已经成交
		// 的参与。只记日志不写审计 —— 审计表是事后仲裁的唯一凭据,
		// 不能让任何人凭一个被拒绝的请求往里塞行。
		if errors.Is(execErr, twophase.ErrIdemConflict) {
			common.SysError(fmt.Sprintf(
				"qianye/lottery: 用户 %d 用同一个 client_request_id 提交了要素不同的参与,已拒绝: %v",
				in.UserId, execErr))
			return nil, errIdemConflict
		}
		releaseEntryOnFailure(ctx, order, entry, execErr)
		return nil, execErr
	}

	// 从这里往下,entry.EntryNo 一律换成资金单指向的那一个,而且是**就地覆盖**
	// 而不是新起一个局部变量:后面每一处收尾都要用它,留着一个仍指向本次现摇值
	// 的字段,下一个人加一行代码就会再踩一次(那正是这个缺陷的形状)。
	// 覆盖之后 entry 这个对象只用于收尾,不再回写任何库。
	entry.EntryNo = settledEntryNo(order, entry)

	// **Execute 返回 nil 不代表业务侧已落定。** 主库已扣款而扩展库回写失败时
	// 它同样返回 (order, nil)。settleGuard 是全模块统一的收尾姿势。
	// 幂等命中时这次补做是安全的空转:原参与早已 success,markEntrySuccess 的
	// `WHERE status = pending` 命中 0 行即返回,而 snap.Applied 为 false,
	// quota_before/after 不会被本次的空快照覆盖。
	if err := settleGuard(ctx, order, func(tx *gorm.DB) error {
		return markEntrySuccess(tx, entry.EntryNo, &snap)
	}); err != nil {
		return nil, err
	}

	stored, err := reloadEntry(ctx, gdb, entry.EntryNo)
	if err != nil {
		return nil, err
	}
	switch stored.Status {
	case EntrySuccess:
		return stored, nil
	case EntryExcluded:
		// 扣费成功但已错过封盘。诚实回答,并把退款交给结算任务按资金单的
		// 终态驱动 —— 绝不在这里宣称已退。
		return nil, errEntryExcluded
	default:
		return nil, errNotSettled
	}
}

// fundingFacts 把受理后的参与翻译成 twophase 的资金要素并算好指纹。
//
// 指纹里必须收进 OptNo:用户提交后风向变了,用同一个 request_id 换个选项重发,
// 不纳入指纹就会幂等命中返回成功,而实际投注仍是旧选项 —— 这是竞猜特有的
// 换参重放。口径与 twophase.Digest 一致:只收用户请求要素,不收服务端派生量,
// 因此 act_id 用对外的 act_no 承载(RefId),而不是自增 id。
func fundingFacts(act *Activity, e *Entry) twophase.Request {
	req := twophase.Request{
		Kind:        qymodel.KindLotteryEntry,
		IdemScope:   idemScopeEntry,
		IdemKey:     e.IdemKey,
		UserId:      e.UserId,
		AmountQuota: e.Amount,
		RefType:     "lottery_entry",
		// RefId 指向 entry_no 而不是 act_no:补偿任务的 Resolver 要靠它精确
		// 找回**这一条**参与明细,活动号只能定位到一场。
		RefId: e.EntryNo,
	}
	// Pick 同样必须收进指纹:双色球下"换个号码用同一个 request_id 重发"与竞猜
	// 的换选项重放是同一种攻击 —— 不纳入指纹就会幂等命中返回成功,
	// 而实际投的仍是旧号码。
	//
	// 指纹里**必须**把 RefId 摘出去。它承载的是 entry_no,而 entry_no 由
	// newEntryNo() 每次请求现生成 —— 是服务端派生量里最极端的一种(随机数)。
	// 留着它,同一个 client_request_id 的两次提交必然算出两个不同指纹 →
	// ErrIdemConflict → 409「本次提交与此前同一请求标识的内容不一致」,
	// 也就是说这条幂等键**在结构上永远命中不了**,而 api_user.go 要求前端
	// 「打开弹窗时生成、重试沿用同一个」的那条契约完全失效:用户超时重试会
	// 收到一句不实的冲突提示,合理地以为第一次没成功,换个 crid 再投一注 ——
	// 幂等键存在的全部意义就是拦住这件事。附带损害是每次诚实重试都写一条
	// 「用户 N 提交了要素不同的参与」的安全日志,把真正的换参重放淹没。
	// 活动维度由 extra 里的 act_no 承载,不靠 RefId;RefId 本身照旧留在
	// Request 上,补偿任务的 Resolver 要靠它精确找回**这一条**参与明细。
	forDigest := req
	forDigest.RefId = ""
	req.Fingerprint = forDigest.Digest(act.ActNo, deci(e.OptNo), e.Pick)
	return req
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

// buildIdemKey 拼出资金单的幂等键。
//
// act_no 前缀是必需的:同一个 client_request_id 在两场活动上是两笔不同的参与,
// 不加前缀会让第二场的报名幂等命中第一场的单据直接返回"成功"。
func buildIdemKey(actNo, clientRequestId string) string {
	return actNo + ":" + clientRequestId
}

// acceptAmount 校验并定出本次要扣的额度。
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
	// 活动没填单注上限时兜到 **lottery.max_stake_quota**,不是 int32 上界。
	// 后者是溢出边界而不是运营闸门:兜到那里等于一个没填上限的竞猜可以让人
	// 一次扣掉 21 亿额度,而 max_stake_quota 恰恰是配置里写明"决定单笔扣款
	// 上限、不允许在线改"的那一项。
	if limit := config.Get().Lottery.MaxStakeQuota; limit > 0 && amount > limit {
		return 0, errBadAmount
	}
	if amount > int64(common.MaxQuota) {
		return 0, errBadAmount
	}
	return amount, nil
}

// ─────────────────────── 阶段一:扩展库事务内的预占 ───────────────────────

// reserveEntry 在资金单的同一扩展库事务内完成全部锁内判定与明细落库。
//
// 第一条 UPDATE 同时承担四件事:活动状态复检、时间窗复检、全场序号分配,
// 以及在活动行上取得 X 锁。之后的每一次 COUNT 都在这把锁的保护下,
// 因此"两个并发请求同时读到旧计数、同时通过上限校验"在结构上不可能发生。
func reserveEntry(tx *gorm.DB, act *Activity, rules Rules, e *Entry) error {
	now := common.GetTimestamp()
	grace := int64(config.Get().Lottery.EntryCloseGraceSeconds)

	res := tx.Model(&Activity{}).
		Where("id = ? AND status = ? AND open_at <= ? AND ? < close_at - ?",
			act.Id, StatusPublished, now, now, grace).
		Updates(map[string]any{
			"entry_seq":     gorm.Expr("entry_seq + 1"),
			"pending_count": gorm.Expr("pending_count + 1"),
			"updated_at":    now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		// 分不清"没开始""已结束""已取消"三种情况在这里是刻意的:再查一次去
		// 区分它们要多一次往返,而用户拿到的动作都一样(刷新页面)。
		return errClosingSoon
	}

	// X 锁已持有,回读到的必然是本次更新之后的值 —— 效果等同 FOR UPDATE
	// 且省一次往返。
	var cur Activity
	if err := tx.Where("id = ?", act.Id).Take(&cur).Error; err != nil {
		return err
	}
	e.Seq = cur.EntrySeq

	if err := checkCaps(tx, &cur, e); err != nil {
		return err
	}

	// 链在这里推进。prev 取活动行上的 chain_head,chain_0 = commit_hash。
	prev := cur.ChainHead
	if prev == "" {
		prev = cur.CommitHash
	}
	e.PrevHash = prev
	// lot-v2 的链末尾多一个选号分量(非双色球恒为空串)。按活动自己的 algo
	// 分派,存量活动的链因此一个字节都不变。
	e.ChainHash = ChainNextFor(cur.Algo, prev, cur.ActNo, e.Seq,
		e.EntryNo, e.UserRef, e.OptNo, e.Amount, e.Pick)

	if err := tx.Create(e).Error; err != nil {
		return err
	}
	if err := tx.Model(&Activity{}).Where("id = ?", act.Id).
		Update("chain_head", e.ChainHash).Error; err != nil {
		return err
	}

	if cur.Kind == KindGuess {
		// 选项上的投注聚合。失败时随整个事务回滚,与 pending_count 同生共死。
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

// checkCaps 是活动行 X 锁内的频次类判定。
//
// 全部用 COUNT 而不是物化计数表:条件是 status IN ('pending','success'),
// **失败的条目自然停止计数,不需要任何回滚**。这正是不建计数表的最大收益 ——
// 计数表必须写"失败时带非负保护地回退",而那段代码一旦写错,上限会永久失效。
func checkCaps(tx *gorm.DB, cur *Activity, e *Entry) error {
	live := []string{EntryPending, EntrySuccess}

	// 全场名额。pending_count 已经含本次,因此判定用 >。
	if cur.MaxTotalEntries > 0 && cur.ActiveCount+cur.PendingCount > cur.MaxTotalEntries {
		return errCapReached
	}
	// 竞猜奖池的上界。赔付是从池子里切出来的,而每一笔出款都要过 twophase 的
	// `amount ≤ MaxQuota`(int32)—— 池子一旦越过那条线,独中的那个人的赔付
	// 在出款入口就会被拒,活动永远收不了尾。上限在这里挡住,而不是等到结算时
	// 才发现钱发不出去。抽奖不受这条限制:它的池子只是参与费收入,不参与分配。
	if cur.Kind == KindGuess && cur.PoolQuota > int64(common.MaxQuota)-e.Amount {
		return errCapReached
	}
	// 双色球受同一条限制,而且理由更硬:它的参与费**有一部分要进期次奖池**
	// (pool_share_bps),没派出去的部分在收尾时滚存回系列。系列池一旦越过 int32,
	// checkBallPoolCovers 的 `open > MaxQuota` 分支会让这个系列**永久开不出新一期**,
	// 而且没有任何接口能把池子降下来(handleCloseSeries 只会把整池作废)。
	// 判据用"本期真正可派发的池子",与 ballPoolOpen / settleSeriesPool 同一个式子:
	// 它 ≤ MaxQuota 时,滚存回去的 carry 也一定 ≤ MaxQuota。
	if cur.DrawMode == DrawModeBall && cur.PoolShareBps > 0 {
		in := (cur.PoolQuota + e.Amount) * int64(cur.PoolShareBps) / 10000
		if cur.PoolOpenQuota > int64(common.MaxQuota)-in {
			return errCapReached
		}
	}
	// 全场**人数**上限与条目上限分开:允许多次参与时,人数上限拦不住一个人
	// 刷 1000 笔,反过来条目上限也拦不住 1000 个小号各来一笔。
	if cur.MaxTotalUsers > 0 {
		var users int64
		if err := tx.Model(&Entry{}).
			Where("act_id = ? AND status IN ? AND user_id <> ?", cur.Id, live, e.UserId).
			Distinct("user_id").Count(&users).Error; err != nil {
			return err
		}
		if users >= int64(cur.MaxTotalUsers) {
			return errCapReached
		}
	}

	// 本人的既有条目一次读出来,后面四项判定共用 —— 每项各查一次是四次往返,
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
	var liveCount, attempts int
	var lastAt int64
	for i := range mine {
		switch mine[i].Status {
		case EntryPending:
			// 未结算的参与会让余额与名单的差额无法归因到哪一笔。
			// 这是资金正确性条件,不是风控偏好。
			return errEntryInFlight
		case EntrySuccess, EntryExcluded, EntryRefunded:
			liveCount++
		}
		attempts++
		if mine[i].CreatedAt > lastAt {
			lastAt = mine[i].CreatedAt
		}
	}
	if cur.MaxEntriesPerUser > 0 && liveCount >= cur.MaxEntriesPerUser {
		return errUserCap
	}
	// 尝试数把**失败**也算进来:用"必然余额不足"的报名去膨胀哈希链与 proof
	// 体积是零成本的,而失败条目永久占一个 seq、永久留在证据里。
	if cur.MaxAttemptsPerUser > 0 && attempts >= cur.MaxAttemptsPerUser {
		return errAttemptCap
	}
	// 冷却。次数上限防不住"在 close 前 1 秒用脚本连发把全场名额吃光"。
	if cur.CooldownSeconds > 0 && lastAt > 0 &&
		common.GetTimestamp()-lastAt < int64(cur.CooldownSeconds) {
		return errCooldown
	}

	if cur.MaxPerInviter > 0 && e.InviterId > 0 {
		var n int64
		if err := tx.Model(&Entry{}).
			Where("act_id = ? AND inviter_id = ? AND status IN ? AND user_id <> ?",
				cur.Id, e.InviterId, live, e.UserId).
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
			Where("act_id = ? AND ip_hash = ? AND status IN ? AND user_id <> ?",
				cur.Id, e.IpHash, live, e.UserId).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return errIPCap
		}
	}
	return nil
}

// ─────────────────────── 阶段二:主库事务内的扣款 ───────────────────────

// quotaSnapshot 记录扣款前后的余额,用于回执与账本日志。
type quotaSnapshot struct {
	Before int64
	After  int64
	// Applied 区分"本次真的扣了"与"此前已生效(补偿重跑)"。
	// 没有它,AfterCommit 会为一次跳过的资金变更再写一条账本日志。
	Applied bool
}

// debitMainQuota 在主库事务内扣额度。
//
// 硬性约束(违反即资损,见包注释):绝不 DecreaseUserQuota、绝不 Save(&User{})、
// 绝不碰 auth_version;扣款必须带 WHERE quota >= ? 并断言 RowsAffected == 1 ——
// 主库可能是 SQLite,那里行锁是空操作,CAS 条件是唯一的正确性保障。
func debitMainQuota(tx *gorm.DB, rules Rules, e *Entry, snap *quotaSnapshot) error {
	var u model.User
	// First 自带软删除过滤。绝不 Unscoped:已注销账号不该还能参与。
	if err := model.QyLockForUpdate(tx).Where("id = ?", e.UserId).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errUserUnavailable
		}
		return err
	}
	// 锁内复检**三项且只有三项**:它们是扣钱的前提,不是资格的前提。
	// 资格用报名那一刻的值,开奖时不再复检(见 eligibility.go 的口径)。
	if u.Status != common.UserStatusEnabled {
		return errUserUnavailable
	}
	// 规则用受理时冻结的那一份,分组取锁内读到的最新值:分组是"这个人现在
	// 属于谁"的事实,必须取最新;规则是"提交那一刻的约定",中途被收紧不该
	// 让一笔已经在动钱的请求拿到与用户所见不同的结论。
	if !GroupAllowed(rules, u.Group) {
		return ineligibleWith([]Missing{{Code: MissGroup, Need: groupNeed(rules), Have: u.Group}})
	}
	// 余额门槛在锁内复检。它与下面那条"够不够扣"是两件事:前者是运营设的
	// 参与门槛,后者是扣款的前提。锁外读到的余额与此刻可能差了几秒的消费,
	// 而"报名那一刻余额满 N"这句承诺只有在真正扣款的那一刻复核才成立。
	//
	// 它挡不住"同一笔钱在多个小号之间循环转账,让每个号在自己报名那一秒都满足
	// 门槛"—— 那需要冻结或持有时长,不是一次复检能解决的。管理端表单必须写清
	// 这条门槛的真实含义是"报名瞬间持有",不是"长期持有"。
	if rules.MinQuota > 0 && int64(u.Quota) < rules.MinQuota {
		return ineligibleWith([]Missing{{Code: MissBalance, Need: rules.MinQuota, Have: int64(u.Quota)}})
	}
	if int64(u.Quota) < e.Amount {
		return errInsufficientQuota
	}

	res := tx.Model(&model.User{}).
		Where("id = ? AND quota >= ?", e.UserId, e.Amount).
		Update("quota", gorm.Expr("quota - ?", e.Amount))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errInsufficientQuota
	}

	*snap = quotaSnapshot{Before: int64(u.Quota), After: int64(u.Quota) - e.Amount, Applied: true}
	return nil
}

// afterDebit 处理主库提交之后的收尾。钱已经动了,任何失败都不能回滚。
func afterDebit(e *Entry, orderNo string, snap quotaSnapshot) {
	if !snap.Applied {
		return
	}
	// 整体失效而非增量更新:增量会与并发的消费扣费互相覆盖。
	if err := model.InvalidateUserCache(e.UserId); err != nil {
		common.SysError("qianye/lottery: 失效用户缓存失败(额度已扣): " + err.Error())
	}
	// 账本日志用 **LogTypeSystem**,不是 LogTypeConsume。
	//
	// 用 LogTypeConsume 会把参与费计进消费统计与"近 N 日消费",于是
	// "累计/近期消费满 N 才能参加"这道门槛可以被抽奖本身刷高:花 100 参与一场
	// 低门槛活动 → 消费 +100 → 满足高门槛活动。那是一个自举漏洞。
	// 用 LogTypeTopup 则会污染充值统计。
	//
	// 正文里的金额换算成站内余额,口径见 ledgerLogContent。
	model.QyRecordLedgerLog(e.UserId, model.LogTypeSystem,
		ledgerLogContent("参与活动扣除", e.Amount, e.EntryNo), orderNo,
		map[string]any{
			"qy_module":       "lottery",
			"qy_lot_entry_no": e.EntryNo,
			"qy_quota":        e.Amount,
			"qy_quota_after":  snap.After,
		})
}

// ─────────────────────── 收尾与回滚 ───────────────────────

// markEntrySuccess 把参与明细推进成功并落计数。
//
// **LocalCommit、post-Execute 补做、补偿 Resolver 三处共用同一个函数** ——
// 三份拷贝各自漂移是本仓反复出现的失败形状,而这里漂移的后果是奖池金额与
// 名单条数对不上。幂等锚点是 status 的 CAS:RowsAffected == 0 说明别的路径
// 已经收过尾(或这条已在封盘时被标 excluded),直接返回成功。
//
// snap 可以为 nil:补偿路径拿不到扣款前后的余额,那一列留空好过编一个。
func markEntrySuccess(tx *gorm.DB, entryNo string, snap *quotaSnapshot) error {
	var e Entry
	if err := tx.Where("entry_no = ?", entryNo).Take(&e).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 有资金单却没有明细,是不该出现的孤儿数据。返回 nil 让补偿任务把
			// 资金单推进终态而不是无限重试,异常本身由对账任务的 orphan_order
			// 与告警交给人。
			common.SysError("qianye/lottery: 找不到参与明细 " + entryNo + ",无法收尾")
			return nil
		}
		return err
	}

	now := common.GetTimestamp()
	updates := map[string]any{"status": EntrySuccess, "settled_at": now}
	if snap != nil && snap.Applied {
		updates["quota_before"] = snap.Before
		updates["quota_after"] = snap.After
	}
	res := tx.Model(&Entry{}).
		Where("entry_no = ? AND status = ?", entryNo, EntryPending).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}

	// 计数与 pool 必须与状态转移同事务。带非负保护:pending_count 被扣成负数
	// 意味着全场上限从此永久失效,那比少统计一笔严重得多。
	r := tx.Model(&Activity{}).
		Where("id = ? AND pending_count > 0", e.ActId).
		Updates(map[string]any{
			"pending_count": gorm.Expr("pending_count - 1"),
			"active_count":  gorm.Expr("active_count + 1"),
			"pool_quota":    gorm.Expr("pool_quota + ?", e.Amount),
			"updated_at":    now,
		})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		// 计数漂移。不阻断收尾 —— 钱已经扣了、明细已经成功,回滚只会更糟。
		// 交给对账任务重算(count_drift / pool_mismatch)。
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 活动 %d 的 pending_count 已为 0,参与 %s 的计数未能回落,交对账任务",
			e.ActId, entryNo))
	}
	return nil
}

// markEntryFailed 在主库**确定未生效**时回滚扩展库的预占。
//
// 注意 seq 与链环不回滚:失败条目永久留在链上占一个 seq,删除即破链。
// 这是 max_attempts_per_user 存在的理由 —— 把失败尝试也计入名额,
// 防止用"必然余额不足"的报名膨胀链与 proof 体积。
func markEntryFailed(tx *gorm.DB, entryNo, failCode string) error {
	var e Entry
	if err := tx.Where("entry_no = ?", entryNo).Take(&e).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	res := tx.Model(&Entry{}).
		Where("entry_no = ? AND status = ?", entryNo, EntryPending).
		Updates(map[string]any{
			"status":     EntryFailed,
			"fail_code":  truncateRunes(failCode, 48),
			"settled_at": common.GetTimestamp(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return nil
	}

	if err := tx.Model(&Activity{}).
		Where("id = ? AND pending_count > 0", e.ActId).
		Update("pending_count", gorm.Expr("pending_count - 1")).Error; err != nil {
		return err
	}
	if e.OptNo > 0 {
		// 选项聚合同样带非负保护。
		if err := tx.Model(&Option{}).
			Where("act_id = ? AND opt_no = ? AND bet_count > 0 AND bet_quota >= ?",
				e.ActId, e.OptNo, e.Amount).
			Updates(map[string]any{
				"bet_quota": gorm.Expr("bet_quota - ?", e.Amount),
				"bet_count": gorm.Expr("bet_count - 1"),
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

// releaseEntryOnFailure 在 Execute 返回错误时回滚预占。
//
// **只有 order.Status == Failed 才回滚**:pending / in_doubt / uncertain 都意味着
// 钱可能已经动了,回滚会让用户白拿一次参与。其中 in_doubt 正是"主库 COMMIT 已发出
// 但结局不明"那一档,twophase 保证它不会被写成 Failed。
//
// 回滚之前仍要用主库 outbox 探针复核一次:Failed 也可能来自存量单、补偿任务或
// 人工裁决,那几条来自另一套判据,而回滚是不可逆的。探针本身失败一律按"可能已生效"处理。
func releaseEntryOnFailure(ctx context.Context, order *qymodel.FundOrder, e *Entry, cause error) {
	if order == nil || order.Status != qymodel.StatusFailed {
		return
	}
	// 与 ChargeEntry 的收尾同源:要回滚的是**资金单指向的**那条明细。
	e.EntryNo = settledEntryNo(order, e)
	// 回滚预占等于宣布"这笔钱没扣过"。只有探针明确说主库没动才敢这么宣布,
	// 探针关掉 / 报错 / 行缺失一律按"可能已扣"处理:错判的代价是用户被白扣一笔。
	if twophase.ProbeMainSide(order) != twophase.MainNotApplied {
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 参与 %s 的资金单 %s 被判失败但无法排除主库已生效,不回滚,交对账任务",
			e.EntryNo, order.OrderNo))
		raiseFlag(ctx, e.ActId, FlagEntryStuck,
			"资金单被判失败但无法排除主库已扣款: "+order.OrderNo)
		return
	}
	code := "qy_lot_failed"
	if be, ok := AsBizError(cause); ok {
		code = be.ErrCode()
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	if err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return markEntryFailed(tx, e.EntryNo, code)
	}); err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/lottery: 回滚参与预占失败,交对账任务: " + err.Error())
	}
}

// postDebitFromOrder 是"主库已确认扣款生效"之后必须补做的账本行。
//
// afterDebit 那条路径捎带着进程内的余额快照,而 commit 断连与扩展库回写失败这两条
// 路径上它一次都不会跑 —— 用户的额度已经少了,账单里却没有任何一行解释这笔钱去哪了。
// 这里从资金单重建:参与号是 RefId,金额是 AmountQuota。
//
// 唯一丢掉的是 qy_quota_after(扣款后余额),它只存在于当时那个事务里。
// 少一个展示字段,好过整条账本行不存在。
func postDebitFromOrder(_ context.Context, order *qymodel.FundOrder) error {
	model.QyRecordLedgerLog(order.UserId, model.LogTypeSystem,
		ledgerLogContent("参与活动扣除", order.AmountQuota, order.RefId), order.OrderNo,
		map[string]any{
			"qy_module":       "lottery",
			"qy_lot_entry_no": order.RefId,
			"qy_quota":        order.AmountQuota,
		})
	return nil
}

// resolveEntryAfterCompensation 是补偿任务确认主库已生效后的收尾回调。
//
// 没有它的后果已经在本仓的 violation 上真实发生过:补偿任务把资金单推成
// success,业务侧永远停在中间态。必须幂等 —— 幂等由 markEntrySuccess 的 CAS 保证。
func resolveEntryAfterCompensation(ctx context.Context, order *qymodel.FundOrder) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, order.RefId, nil)
	})
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

// settledEntryNo 是**这张资金单真正结算的**那条参与明细的编号。
//
// 收尾(markEntrySuccess / markEntryFailed)与回读一律认它,绝不认本次请求现摇
// 的 e.EntryNo。二者只在一种情况下不同,而那恰好是幂等键唯一存在的理由:
// 同一个 client_request_id 原样重放时,twophase.Execute 走 resolveExisting 直接
// 返回原单并**跳过整个 LocalDetail 闭包**,所以 newEntryNo() 这次摇出来的串从未
// 落过库。拿它去回读必然 ErrRecordNotFound → wrapInternal → HTTP 500
// 「处理失败,请稍后重试」。用户在同一个弹窗里怎么重试都是 500(前端把 crid 钉在
// 打开弹窗那一刻),只能关掉重开换一个新 crid —— 于是真的多投一注、真的多扣一笔,
// 正是这条幂等键要拦住的那一注。
//
// order.RefId 在两条路径上都是权威值:新建单时 fundingFacts 就是拿 e.EntryNo
// 填的它。补偿任务的 resolveEntryAfterCompensation 用的也是它。
func settledEntryNo(order *qymodel.FundOrder, e *Entry) string {
	if order != nil && order.RefId != "" {
		return order.RefId
	}
	return e.EntryNo
}

func reloadEntry(ctx context.Context, gdb *gorm.DB, entryNo string) (*Entry, error) {
	var e Entry
	if err := gdb.WithContext(ctx).Where("entry_no = ?", entryNo).Take(&e).Error; err != nil {
		return nil, wrapInternal("回读参与明细", err)
	}
	return &e, nil
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
			// 没有种子行的活动不可能被发布(见 activity.go 的 createActivity)。
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
