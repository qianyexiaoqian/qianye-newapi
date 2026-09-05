package lottery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/mall"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// wheel.go —— 星屑转盘(kind=draw, draw_mode=wheel):lottery 的第四种定档方式,
// 也是唯一**即时开奖**的一种。
//
// # 与批次玩法的差别只在"什么时候开"
//
// rank / prob / ball 是封盘 → 冻结名单 → 揭示种子 → 统一摇号;转盘是每一次转动
// 在活动行锁内当场摇号、当场扣库存、当场派奖,封盘与揭示只剩"公开种子"这一步
// (lifecycle.go 的 revealWheel)。承诺-揭示的骨架照旧:种子在发布时承诺、揭示后
// 任何人都能按 WheelTicket 把每一转从头复算;不同的是票面**不混名单哈希**
// (即时开奖没有名单可冻结),于是能读到种子的人可以对自己的下一转离线枚举
// client_seed 挑一转必中 —— 项目方接受这一档(decisions.md D-13:管理员可参与,
// 与三种抽奖同档),协议只保证"服务端按公示公式算了票、不可抵赖地被检出"。
// 规则页对转盘因此只声称"可复算",不声称"任何人都无法预知"。
//
// # 一个扩展库事务(spinTx)
//
//	活动行条件 UPDATE 取锁(published 且在时间窗内,entry_seq+1)
//	→ 锁内奖档完整性:按 tier 读全部奖档重算 spec 原像与承诺比对,不一致 → 回滚拒绝 + 挂旗
//	→ checkCaps(与报名同一份闸门)
//	→ stardust.Debit(参与费;幂等键与报名同 uk)
//	→ 票面 HMAC(seed, act_no ‖ seq ‖ client_seed) → RollPpm → Bands 落档
//	→ 真实档条件递减库存,RowsAffected=0 ⇒ 记 exhausted_tier、结果落空
//	→ 插票(结果四列进链原像)→ 推链
//	→ 派奖:quota 档 stardust.Credit + 直接插入 status=paid 的出款行;text 档 granted
//	→ 全部真实档库存归零 ⇒ 同事务内当场封盘(与到点封盘同一段 lockActivityTx)
//
// 任何一步失败整笔回滚:seq 不占、本金不扣、奖不发。星屑不足与到账溢出都是
// "这一转没发生",不是一笔卡住的账。
//
// # 锁序(与 entry.go / payout.go 同一条)
//
// 活动行锁在最前,余额行锁(Debit / Credit)在它之后;奖档行的库存递减夹在两者
// 之间,只有本路径会写它。派奖 worker 对活动行的补计只在 finished 上,与进行中的
// 转盘不相交。
//
// # 幂等
//
// 键 = act_no:client_request_id(与报名同一个 uk),指纹 = 用户 + 金额 + client_seed。
// 原样重放拿回**原来那一转的结果**,不得再摇;换了 client_seed 重放 409。
// 重放先于一切(资格、验密、事务):一个已经付了钱的人在重试时若被别的理由顶回去,
// 那句话暗示什么都没发生,而钱已经扣了。

// wheelNoneName 是派生的「谢谢参与」档的名字。它进 spec 原像,发布后不可改。
const wheelNoneName = "谢谢参与"

// wheelStockOf 回答"这一档落草稿时的在线库存":转盘 = count,其余玩法恒 0。
func wheelStockOf(drawMode string, count int) int {
	if drawMode == DrawModeWheel {
		return count
	}
	return 0
}

// maxClientSeed 是 client_seed 的长度上限(qy_lot_entry.client_seed 的列宽)。
const maxClientSeed = 64

// validClientSeed 判定 client_seed 合规:≤ 64 字节、仅 [0-9a-zA-Z_-]。空串合法(空分量)。
//
// 字符集与幂等键同一档收紧:它进票面原像与链原像,"|" 尤其不能进 —— 那是
// WheelPick 编码的分隔符。刻意不 TrimSpace:进原像的字节就是落库的字节。
func validClientSeed(s string) bool {
	if len(s) > maxClientSeed {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// spinRequest 是一次转动的请求体。
type spinRequest struct {
	// ClientRequestId 由前端在打开转动弹窗时生成并缓存,重试沿用同一个(见 entryRequest)。
	ClientRequestId string `json:"client_request_id"`
	// ClientSeed 是用户自选的票面分量。前端默认随机生成并展示,用户可改;
	// 它是这一转里**唯一由用户决定**的输入,回执与证据链都原样带着它。
	ClientSeed  string `json:"client_seed"`
	PayPassword string `json:"pay_password"`
}

// SpinInput 是一次转动的全部输入。
type SpinInput struct {
	UserId     int
	ClientSeed string
	ClientIp   string
	UserAgent  string
}

// spinResult 是转动回执,也是幂等重放时原样返回的那一份。
type spinResult struct {
	EntryNo string `json:"entry_no"`
	Seq     int    `json:"seq"`
	// Ppm 是摇号量 ∈ [0, 999999];ResultTier 0 = 未中。
	Ppm        int64 `json:"ppm"`
	ResultTier int   `json:"result_tier"`
	// ExhaustedTier > 0 = 摇中了这一档但它已发完,结果落空。
	ExhaustedTier int `json:"exhausted_tier"`
	// Amount 是中奖档的单份星屑(text / product / none 恒 0);PrizeType 是
	// quota / text / product / none。
	Amount    int64  `json:"amount"`
	PrizeType string `json:"prize_type"`
	// ProductNo / MallOrderNo 只对 product 档非空:中的是哪件商品、生成了哪张商城订单。
	// 结果屏据此跳去「我的订单」看码 / 填地址 / 看订阅。
	ProductNo   string `json:"product_no"`
	MallOrderNo string `json:"mall_order_no"`
	// ChainHead 是这一转的 chain_hash,用户手里的那一环凭据。
	ChainHead string `json:"chain_head"`
	Replayed  bool   `json:"replayed"`
}

// spinOutcome 是转动事务算出的派奖结论,回执从它与票拼出来。
type spinOutcome struct {
	PrizeType string
	Amount    int64
	ProductNo string
	// PayoutNo / Grant 只对 product 档有值:事务提交之后据此挂旗 / 发订阅(afterProductGrant)。
	PayoutNo string
	Grant    *mall.GrantResult
}

// specDrift 是"锁内重算的奖档原像与承诺对不上"。它带着重算的细节出事务,
// 好让挂旗与审计写出"重算 X 已公开 Y"这种当场算出来的数字;对外仍是 errSpecDrift。
type specDrift struct{ detail string }

func (d specDrift) Error() string      { return d.detail }
func (specDrift) Is(target error) bool { return target == errSpecDrift }

// handleSpin 是转盘唯一会动钱的用户入口:POST /lottery/activities/:act_no/spins。
//
// 顺序是契约的一部分:幂等重放 → 玩法开关 → 资格 → 验密 → 事务。
// 重放排最前(理由见文件头);验密在解析请求体之后、开事务之前、事务之外
// (paypass.Require 的规矩)。
func handleSpin(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	var req spinRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	// 幂等键的归一化与报名逐字同一条(理由见 handleCreateEntry):大小写折叠、
	// 字符集收紧到 [a-z0-9_-]、长度 ≤ 64。转盘没有多注,不派生 `#i` 后缀。
	crid := strings.TrimSpace(req.ClientRequestId)
	if crid == "" || len(crid) > maxClientRequestID {
		respondErr(c, errBadRequestID)
		return
	}
	folded, ok := qymodel.NormalizeIdemClientKey(crid)
	if !ok {
		respondErr(c, errBadRequestID)
		return
	}
	if !validClientSeed(req.ClientSeed) {
		respondErr(c, errBadClientSeed)
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb = gdb.WithContext(ctx)
	// 不限状态地读活动(loadActivityAny 而不是只认 published 的 loadActivityByNo):
	// 幂等重放必须在**封盘之后**仍然拿得回原来那一转 —— 库存耗尽当场封盘的那一转,
	// 用户的重试恰恰会落在封盘之后;状态闸门在重放之后、事务之前再判。
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	if act.Status == StatusDraft {
		respondErr(c, errActivityNotFound)
		return
	}
	if act.DrawMode != DrawModeWheel {
		respondErr(c, errNotWheel)
		return
	}

	uid := c.GetInt("id")
	idemKey := buildIdemKey(act.ActNo, folded)
	// 指纹 = 用户 + 金额 + client_seed。复用报名的指纹口径,把 client_seed 放进
	// "用户请求要素"里 pick 的那一位:它正是这一转里唯一由用户决定的输入。
	fingerprint := entryFingerprint(act, uid, act.StakeQuota, 0, req.ClientSeed)

	// (1) 幂等重放先于一切,**不得再摇一次**。
	prior, err := loadEntryByIdemKey(ctx, gdb, act.Id, idemKey)
	if err != nil {
		respondErr(c, err)
		return
	}
	if prior != nil {
		res, err := replaySpin(ctx, gdb, act, prior, fingerprint, uid)
		if err != nil {
			respondErr(c, err)
			return
		}
		respondOK(c, res)
		return
	}
	// 没开始 / 已截止 / 已封盘(含库存耗尽当场封盘)三种在这里不区分,用户拿到的
	// 动作都一样(刷新页面);权威判定仍在事务里那条带时间窗的 CAS。
	if act.Status != StatusPublished {
		respondErr(c, errWheelClosed)
		return
	}
	// 玩法被隐藏时不再受理新的转动,其余一切照旧(见 play.go)。
	if !effectiveCtx(ctx).playShown(PlayWheel) {
		respondErr(c, errPlayHidden)
		return
	}

	// (2) 资格。D-13:转盘与三种抽奖同档,管理员与创建者可以参与(硬规则只对竞猜)。
	rules, err := ParseRules(act.RulesText)
	if err != nil {
		respondErr(c, err)
		return
	}
	subject, err := LoadSubject(ctx, uid, rules, act.CreatedBy)
	if err != nil {
		respondErr(c, err)
		return
	}
	if missing := Evaluate(rules, subject, act.StakeQuota, common.GetTimestamp(), PlayWheel); len(missing) > 0 {
		respondErr(c, ineligibleWith(missing))
		return
	}

	// (3) 验密:一转就是一笔参与费,阈值口径与报名同一条(pay_password_threshold_stardust)。
	if PayPasswordRequired(act.StakeQuota) {
		if !paypass.Require(c, uid, req.PayPassword) {
			return
		}
	}

	// (4) 事务。
	in := SpinInput{
		UserId: uid, ClientSeed: req.ClientSeed,
		ClientIp: common.ClientIP(c), UserAgent: c.Request.UserAgent(),
	}
	res, err := spinWheel(ctx, gdb, act, subject, in, idemKey, fingerprint)
	if err != nil {
		respondErr(c, err)
		return
	}
	respondOK(c, res)
}

// spinWheel 执行一次转动:拼票 → 一个扩展库事务(spinTx)→ 把结局翻译成回执或错误。
//
// 调用方已经做完幂等重放、资格与验密;这里只剩事务与它的三种非内部结局:
// 并发同键重放(回到读票)、星屑不足、到账溢出 —— 后两者整笔回滚,seq 不占。
func spinWheel(ctx context.Context, gdb *gorm.DB, act *Activity, subject *Subject,
	in SpinInput, idemKey, fingerprint string) (*spinResult, error) {
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
		Amount:              act.StakeQuota,
		ClientSeed:          in.ClientSeed,
		Status:              EntrySuccess,
		EligibilitySnapshot: SnapshotJSON(subject),
		IpHash:              hmacHex(salts.IpSalt, in.ClientIp),
		UaHash:              hmacHex(salts.IpSalt, in.UserAgent),
		CreatedAt:           common.GetTimestamp(),
	}

	var out spinOutcome
	err = gdb.Transaction(func(tx *gorm.DB) error {
		o, err := spinTx(ctx, tx, act, entry)
		out = o
		return err
	})
	switch {
	case err == nil:
		res := &spinResult{
			EntryNo: entry.EntryNo, Seq: entry.Seq, Ppm: entry.Ppm,
			ResultTier: entry.ResultTier, ExhaustedTier: entry.ExhaustedTier,
			Amount: out.Amount, PrizeType: out.PrizeType, ProductNo: out.ProductNo,
			ChainHead: entry.ChainHash,
		}
		if out.Grant != nil && out.Grant.Order != nil {
			res.MallOrderNo = out.Grant.Order.OrderNo
			// 事务已提交:码不够挂旗、套餐奖立即发订阅。放在回执之前 —— 用户看到
			// "中了套餐"的那一刻,订阅已经在他账上(或已经交给对账补发)。
			afterProductGrant(ctx, act.Id, out.PayoutNo, out.Grant)
		}
		return res, nil
	case errors.Is(err, errEntryReplayRace):
		// 同一个 crid 的另一路刚刚落定,本次事务已整体回滚:回到读票,拿回它那一转。
		prior, err := loadEntryByIdemKey(ctx, gdb, act.Id, idemKey)
		if err != nil {
			return nil, err
		}
		if prior == nil {
			return nil, wrapInternal("受理转动", errors.New("幂等键撞键却读不回已有票"))
		}
		return replaySpin(ctx, gdb, act, prior, fingerprint, in.UserId)
	case errors.Is(err, stardust.ErrInsufficient):
		return nil, errInsufficientQuota()
	case errors.Is(err, stardust.ErrOverflow):
		return nil, errPrizeOverflow()
	case errors.Is(err, errSpecDrift):
		noteSpecDrift(ctx, act, err.Error())
		return nil, errSpecDrift
	}
	if _, ok := AsBizError(err); ok {
		return nil, err
	}
	db.MarkFailure(err)
	return nil, wrapInternal("受理转动", err)
}

// spinTx 是一次转动在扩展库事务里的全部写入。**必须在调用方的事务内执行。**
//
// 步骤与锁序见文件头。种子只在这里、只经 loadSeedForSpin 读一次,读到的值只喂给
// WheelTicket —— seed_guard_test.go 钉住这是包内唯一的热路径读点。
func spinTx(ctx context.Context, tx *gorm.DB, act *Activity, e *Entry) (spinOutcome, error) {
	var out spinOutcome
	now := common.GetTimestamp()

	// 活动行条件 UPDATE:状态与时间窗复检、序号分配、计数与参与费累加、取 X 锁。
	// 不用 entry_close_grace_seconds:转盘没有 pending 阶段。时间窗读的是活动行
	// **此刻**的 open_at / close_at —— 排期不进转盘的承诺原像,运营可以在发布后改
	// (api_admin_schedule.go),改完下一转就按新窗口判。
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
		return out, res.Error
	}
	if res.RowsAffected != 1 {
		return out, errWheelClosed
	}
	var cur Activity
	if err := tx.Where("id = ?", act.Id).Take(&cur).Error; err != nil {
		return out, err
	}
	e.Seq = cur.EntrySeq

	// 锁内奖档完整性。批次玩法只在开奖那一瞬校验一次;转盘的钱是逐转付出的,
	// 发布后改一行 win_ppm 会立刻改变后续每一转,必须每转校验。spec 行用 count,
	// stock_left 不进原像(它每一转都在变)。
	prizes := make([]Prize, 0, 8)
	if err := tx.Where("act_id = ?", act.Id).Order("tier asc").Find(&prizes).Error; err != nil {
		return out, err
	}
	if err := checkSpecIntegrity(&cur, prizes); err != nil {
		return out, specDrift{detail: err.Error()}
	}
	if err := checkCaps(tx, &cur, e, 0); err != nil {
		return out, err
	}

	debit, err := stardust.Debit(tx, stardust.Posting{
		UserId:    e.UserId,
		Kind:      stardust.KindLotStake,
		Amount:    e.Amount,
		IdemScope: idemScopeStake,
		IdemKey:   e.IdemKey,
		RefType:   refTypeEntry,
		RefNo:     e.EntryNo,
		ActNo:     cur.ActNo,
		Remark:    "转盘转动扣除",
	})
	if err != nil {
		return out, err
	}
	if !debit.Inserted {
		return out, errEntryReplayRace
	}
	e.OrderNo = debit.LedgerNo
	e.QuotaBefore = debit.BalanceAfter + e.Amount
	e.QuotaAfter = debit.BalanceAfter

	// 票面与摇号。原像 = 域 ‖ act_no ‖ seq ‖ client_seed:没有 user_ref、没有任何
	// 服务端当场生成的量(理由见 WheelTicket)。
	seedHex, err := loadSeedForSpin(tx, act.Id)
	if err != nil {
		return out, err
	}
	ticket, err := WheelTicket(seedHex, cur.ActNo, e.Seq, e.ClientSeed)
	if err != nil {
		return out, err
	}
	e.Ppm = int64(RollPpm(ticket))

	bands, err := Bands(wheelTiersOf(prizes))
	if err != nil {
		return out, wrapInternal("转盘落档", err)
	}
	var hit *Band
	for i := range bands {
		if uint32(e.Ppm) >= bands[i].LoPpm && uint32(e.Ppm) < bands[i].HiPpm {
			hit = &bands[i]
			break
		}
	}
	if hit == nil {
		// 发布期断言过 Σwin_ppm == PpmDen(含派生的 none 档),摇号轴被铺满,
		// 这里落不进任何区间只可能是奖档表被改过 —— 而上面刚核过原像。
		return out, wrapInternal("转盘落档", fmt.Errorf("摇号量 %d 落在全部区间之外", e.Ppm))
	}

	var won *Prize
	if hit.PrizeType != PrizeTypeNone {
		// 真实档:条件递减库存。RowsAffected=0 = 这一档已发完,结果落空并记下
		// 摇中的是哪一档 —— "库存耗尽落空"是真的,这一位就是证据。
		upd := tx.Model(&Prize{}).
			Where("act_id = ? AND tier = ? AND stock_left > 0", cur.Id, hit.Tier).
			Update("stock_left", gorm.Expr("stock_left - 1"))
		if upd.Error != nil {
			return out, upd.Error
		}
		if upd.RowsAffected == 1 {
			e.ResultTier = hit.Tier
			for i := range prizes {
				if prizes[i].Tier == hit.Tier {
					won = &prizes[i]
				}
			}
		} else {
			e.ExhaustedTier = hit.Tier
		}
	}

	// 推链。结果四列经 WheelPick 编码进 lot-v2 的 pick 分量:事后改任何一转的
	// 结果,该转之后所有人手里的链环全部对不上。
	prev := cur.ChainHead
	if prev == "" {
		prev = cur.CommitHash
	}
	e.PrevHash = prev
	e.ChainHash = ChainNextFor(cur.Algo, prev, cur.ActNo, e.Seq, e.EntryNo, e.UserRef,
		0, e.Amount, WheelPick(e.ResultTier, e.Ppm, e.ExhaustedTier, e.ClientSeed))
	e.SettledAt = e.CreatedAt
	if err := tx.Create(e).Error; err != nil {
		if db.IsDuplicateKey(err) {
			return out, errEntryReplayRace
		}
		return out, err
	}
	if err := tx.Model(&Activity{}).Where("id = ?", act.Id).
		Update("chain_head", e.ChainHash).Error; err != nil {
		return out, err
	}

	out.PrizeType = PrizeTypeNone
	if won != nil {
		payoutNo, grant, err := grantWheelPrize(tx, &cur, e, won, now)
		if err != nil {
			return out, err
		}
		out.PrizeType, out.Amount, out.ProductNo = won.Type(), won.AmountQuota, won.ProductNo
		out.PayoutNo, out.Grant = payoutNo, grant
		// 库存耗尽:所有真实档 stock_left=0 → 同事务内当场封盘(与到点封盘同一段)。
		// 不做"耗尽后继续按未中开出"—— 那是收钱不给奖。close_at / draw_at 不动:
		// 封盘之后排期已经没有意义(名单已冻结、只等揭示),揭示时刻由
		// max(draw_at, locked_at + reveal_delay) 决定。
		var left int64
		if err := tx.Model(&Prize{}).
			Where("act_id = ? AND prize_type <> ? AND stock_left > 0", cur.Id, PrizeTypeNone).
			Count(&left).Error; err != nil {
			return out, err
		}
		if left == 0 {
			if _, err := lockActivityTx(ctx, tx, &cur, now, qymodel.ActorSystem, 0,
				map[string]any{"stock_exhausted": true}); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

// grantWheelPrize 在转动事务里当场派奖。
//
// quota 档:先生成 payout_no,stardust.Credit(幂等键 lotpay:<payout_no>),再**直接
// 插入** status=paid 的出款行 —— 不经 PlanPayouts、不经 planned/paying、不调
// markPayoutPaid(它的 CAS 刻意只认 paying,"先 planned 再标 paid"会静默 no-op 并把
// 行留给 worker)。payout_quota 逐转累加只服务进行中的实时显示,收尾以 SUM(paid)
// 整体覆盖为权威(finishIfDone / revealWheel)。
//
// text 档:落 granted 行并在同一事务里 text_grant_count + 1。批次模型里它是开奖一次
// 写入的期望值,finishIfDone 与 auditTextPrizes 靠它复核;转盘必须逐转累加,
// 不能在封盘时用 COUNT 回填 —— 回填会把"行被删了"与"当初就没登记"混成同一个数。
//
// product 档:先生成出款号,在同一事务里 mall.GrantPrizeTx 建一张 0 元商城订单
// (幂等键 lotprize:<payout_no>),再落 granted 行并把商城单号写进 mall_order_no。
// 两者同库,所以是一个事务;套餐奖的主库那一步由商城在提交后自己收敛。
// 返回出款号与商城那一侧的结论(非 product 档为 "", nil)。
func grantWheelPrize(tx *gorm.DB, cur *Activity, e *Entry, won *Prize, now int64) (string, *mall.GrantResult, error) {
	payout := Payout{
		PayoutNo: newPayoutNo(), ActId: cur.Id, EntryId: e.Id,
		UserId: e.UserId, Tier: won.Tier, DrawPos: e.Seq, CreatedAt: now,
	}
	switch won.Type() {
	case PrizeTypeQuota:
		credit, err := stardust.Credit(tx, stardust.Posting{
			UserId:    e.UserId,
			Kind:      stardust.KindLotPrize,
			Amount:    won.AmountQuota,
			IdemScope: idemScopePayout,
			IdemKey:   payoutIdemKey(payout.PayoutNo),
			RefType:   refTypePayout,
			RefNo:     payout.PayoutNo,
			ActNo:     cur.ActNo,
			Remark:    "转盘中奖到账",
		})
		if err != nil {
			return "", nil, err
		}
		payout.Kind, payout.Status = PayoutPrize, PayoutPaid
		payout.AmountQuota, payout.OrderNo, payout.SettledAt = won.AmountQuota, credit.LedgerNo, now
		if err := tx.Create(&payout).Error; err != nil {
			return "", nil, err
		}
		return payout.PayoutNo, nil, tx.Model(&Activity{}).Where("id = ?", cur.Id).
			UpdateColumn("payout_quota", gorm.Expr("payout_quota + ?", won.AmountQuota)).Error
	case PrizeTypeText:
		payout.Kind, payout.Status = PayoutText, PayoutGranted
		if err := tx.Create(&payout).Error; err != nil {
			return "", nil, err
		}
		return payout.PayoutNo, nil, tx.Model(&Activity{}).Where("id = ?", cur.Id).
			UpdateColumn("text_grant_count", gorm.Expr("text_grant_count + 1")).Error
	case PrizeTypeProduct:
		grant, err := mall.GrantPrizeTx(tx, mall.GrantPrizeInput{
			UserId: e.UserId, ProductNo: won.ProductNo,
			RefType: refTypePayout, RefNo: payout.PayoutNo, ActNo: cur.ActNo,
		})
		if err != nil {
			return "", nil, err
		}
		payout.Kind, payout.Status = PayoutProduct, PayoutGranted
		payout.MallOrderNo, payout.SettledAt = grant.Order.OrderNo, now
		if err := tx.Create(&payout).Error; err != nil {
			return "", nil, err
		}
		return payout.PayoutNo, grant, nil
	}
	return "", nil, fmt.Errorf("qianye/lottery: 转盘落到了不该派奖的档 %d(%s)", won.Tier, won.PrizeType)
}

// wheelTiersOf 把奖档行投影成摇号轴的输入(含派生的 none 行)。
func wheelTiersOf(prizes []Prize) []Tier {
	out := make([]Tier, 0, len(prizes))
	for _, p := range prizes {
		out = append(out, Tier{
			Tier: p.Tier, Count: p.Count, Amount: p.AmountQuota,
			PrizeType: p.Type(), WinPpm: p.WinPpm,
		})
	}
	return out
}

// loadSeedForSpin 是包内**唯一的热路径**种子读点,只在转动事务里被 spinTx 调用。
//
// 与 loadSeedForReveal 分开而不是复用:那一个的四个调用点全是冷路径或揭示后
// (发布 / 开奖 worker / 证据链 / 删除审计),这一个每一转都要读、由普通用户触发,
// 暴露面比现状大 —— 正因如此它只取 seed 一列、只在事务内、返回值只喂给 WheelTicket,
// 由 seed_guard_test.go 的三条 AST 断言钉住。
func loadSeedForSpin(tx *gorm.DB, actId int64) (string, error) {
	var row struct {
		Seed string `gorm:"column:seed"`
	}
	if err := tx.Model(&Seed{}).Select("seed").Where("act_id = ?", actId).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", wrapInternal("读取转盘随机源", errors.New("随机源不存在"))
		}
		return "", err
	}
	if row.Seed == "" {
		return "", wrapInternal("读取转盘随机源", errors.New("随机源为空"))
	}
	return row.Seed, nil
}

// noteSpecDrift 把一次被拒绝的转动落成对账异常 + 系统审计,与 suspendReveal 同一条
// 去重纪律:同一场活动的每一次转动尝试都会撞上同一条漂移,不去重会把审计表刷满。
func noteSpecDrift(ctx context.Context, act *Activity, detail string) {
	if !raiseFlag(ctx, act.Id, FlagSpecDrift, detail) {
		return
	}
	common.SysError(fmt.Sprintf("qianye/lottery: 转盘 %s 拒绝转动: %s", act.ActNo, detail))
	writeSystemAudit("lottery.spin", act.ActNo, qymodel.ResultFail, detail, "")
}

// replaySpin 把一张已落定的转动票当作原样重放的回执返回;指纹不一致一律 409。
//
// 结果从票上的四列读回,派奖形态从奖档表反查(result_tier > 0 时)。**不再摇一次**。
func replaySpin(ctx context.Context, gdb *gorm.DB, act *Activity, prior *Entry, fingerprint string, userId int) (*spinResult, error) {
	if prior.Fingerprint != fingerprint {
		common.SysError(fmt.Sprintf(
			"qianye/lottery: 用户 %d 用同一个 client_request_id 提交了要素不同的转动,已拒绝(票 %s)",
			userId, prior.EntryNo))
		return nil, errIdemConflict
	}
	res := &spinResult{
		EntryNo: prior.EntryNo, Seq: prior.Seq, Ppm: prior.Ppm,
		ResultTier: prior.ResultTier, ExhaustedTier: prior.ExhaustedTier,
		PrizeType: PrizeTypeNone, ChainHead: prior.ChainHash, Replayed: true,
	}
	if prior.ResultTier > 0 {
		var p Prize
		if err := gdb.WithContext(ctx).Where("act_id = ? AND tier = ?", act.Id, prior.ResultTier).
			Take(&p).Error; err != nil {
			db.MarkFailure(err)
			return nil, wrapInternal("读取转动结果", err)
		}
		res.PrizeType, res.Amount, res.ProductNo = p.Type(), p.AmountQuota, p.ProductNo
		if p.Type() == PrizeTypeProduct {
			var payout Payout
			if err := gdb.WithContext(ctx).Select("mall_order_no").
				Where("entry_id = ? AND kind = ?", prior.Id, PayoutProduct).Take(&payout).Error; err != nil {
				db.MarkFailure(err)
				return nil, wrapInternal("读取转动结果", err)
			}
			res.MallOrderNo = payout.MallOrderNo
		}
	}
	return res, nil
}

// mySpinView 是「我的转动」列表里的一行。
type mySpinView struct {
	EntryNo       string `json:"entry_no"`
	Seq           int    `json:"seq"`
	Ppm           int64  `json:"ppm"`
	ResultTier    int    `json:"result_tier"`
	ExhaustedTier int    `json:"exhausted_tier"`
	Amount        int64  `json:"amount"`
	PrizeType     string `json:"prize_type"`
	// ProductNo / MallOrderNo 只对 product 档非空(与 spinResult 同义)。
	ProductNo   string `json:"product_no"`
	MallOrderNo string `json:"mall_order_no"`
	ClientSeed  string `json:"client_seed"`
	// ChainHash 让这一行长期留着用户当初拿到的那一环凭据(回执弹窗关掉就没了)。
	ChainHash string `json:"chain_hash"`
	CreatedAt int64  `json:"created_at"`
}

// handleListMySpins 返回我在这一场转盘上的全部转动(分页,最近的在前)。
func handleListMySpins(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ctx := c.Request.Context()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	gdb = gdb.WithContext(ctx)
	act, err := loadActivityAny(ctx, gdb, c.Param("act_no"))
	if err != nil {
		respondErr(c, err)
		return
	}
	if act.Status == StatusDraft {
		respondErr(c, errActivityNotFound)
		return
	}
	if act.DrawMode != DrawModeWheel {
		respondErr(c, errNotWheel)
		return
	}
	me := c.GetInt("id")
	page, size := httpq.Paginate(c, listPaging)
	q := gdb.Model(&Entry{}).Where("act_id = ? AND user_id = ?", act.Id, me)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计我的转动", err))
		return
	}
	rows := make([]Entry, 0, size)
	if err := q.Order("seq desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询我的转动", err))
		return
	}
	// 奖档表一次读完(≤ 12 行),按 tier 反查每一转的派奖形态。
	prizes := make([]Prize, 0, 8)
	if err := gdb.Where("act_id = ?", act.Id).Find(&prizes).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询奖档", err))
		return
	}
	byTier := make(map[int]Prize, len(prizes))
	for _, p := range prizes {
		byTier[p.Tier] = p
	}
	// 商品奖那几转要带上商城单号:按这一页的票一次查出出款行。
	mallOrders := make(map[int64]string, len(rows))
	if ids := entryIds(rows); len(ids) > 0 {
		payouts := make([]Payout, 0, len(ids))
		if err := gdb.Select("entry_id, mall_order_no").
			Where("entry_id IN (?) AND kind = ?", ids, PayoutProduct).Find(&payouts).Error; err != nil {
			db.MarkFailure(err)
			respondErr(c, wrapInternal("查询商品奖出款", err))
			return
		}
		for _, p := range payouts {
			mallOrders[p.EntryId] = p.MallOrderNo
		}
	}
	items := make([]mySpinView, 0, len(rows))
	for _, e := range rows {
		v := mySpinView{
			EntryNo: e.EntryNo, Seq: e.Seq, Ppm: e.Ppm,
			ResultTier: e.ResultTier, ExhaustedTier: e.ExhaustedTier,
			PrizeType: PrizeTypeNone, ClientSeed: e.ClientSeed,
			ChainHash: e.ChainHash, CreatedAt: e.CreatedAt,
		}
		if p, ok := byTier[e.ResultTier]; ok && e.ResultTier > 0 {
			v.PrizeType, v.Amount, v.ProductNo = p.Type(), p.AmountQuota, p.ProductNo
			v.MallOrderNo = mallOrders[e.Id]
		}
		items = append(items, v)
	}
	respondOK(c, gin.H{"items": items, "total": total, "p": page, "page_size": size})
}
