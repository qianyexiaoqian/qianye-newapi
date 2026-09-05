package stardust

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ledger.go —— 写账的唯一入口。
//
// 所有模块(lottery / mall / 本包的结算与 hook)都只经 Credit / Debit 动账。
// 协议(design §3.3)逐条落在 post 里;调用契约见 doc.go —— 一句话:
// 返回任何非 nil error 时调用方必须让整个扩展库事务回滚。

// Kind 是流水种类,取值见 model.go 的 Kind* 常量。
type Kind string

// Posting 是一次记账的全部参数。
type Posting struct {
	UserId int
	Kind   Kind
	// Amount 必须 > 0;符号由 Credit / Debit 决定,不由调用方给。
	Amount int64
	// IdemScope / IdemKey 必填,合起来是唯一幂等键(§3.2 表)。
	// IdemKey 超过 96 字节时自动 sha256 折叠,调用方不必自己截断 —— 截断会让
	// 两个不同的业务键撞成同一个幂等键,那意味着第二笔永远记不上。
	IdemScope string
	IdemKey   string
	// 关联单据、活动号、对端用户、冻结的比例 / 分组 / 基数、事由、操作人。
	// 除 OperatorId(系统 0)外都是可选的,超长字段按列宽截断。
	RefType    string
	RefNo      string
	ActNo      string
	PeerUserId int
	RateBps    int
	RateGroup  string
	BaseQuota  int64
	Remark     string
	OperatorId int
}

// Result 是一次记账的收据。
type Result struct {
	// Inserted 为 false 表示幂等重放:余额未动,其余字段来自已存在的那一行。
	Inserted     bool
	LedgerId     int64
	LedgerNo     string
	BalanceAfter int64
}

var (
	ErrInsufficient = errors.New("stardust: 星屑不足")
	ErrOverflow     = errors.New("stardust: 余额超出上界")
	ErrBadAmount    = errors.New("stardust: 金额非法")
	ErrBadKind      = errors.New("stardust: 未知的账本 kind")
	// ErrBadPosting 是 UserId / IdemScope / IdemKey 缺失或超宽:这些是调用方的编程
	// 错误,不是业务拒绝,所以不与上面四条混用 —— 一条 user_id=0 的记账落库之后
	// 就是一行谁也解释不了的余额。
	ErrBadPosting = errors.New("stardust: 记账参数缺失")
)

// Credit 入账:available += Amount,并按 kind 累加对应的累计列。
func Credit(tx *gorm.DB, p Posting) (Result, error) { return post(tx, p, false) }

// Debit 扣减:available −= Amount,并按 kind 累加对应的累计列(manual 为带符号净额)。
func Debit(tx *gorm.DB, p Posting) (Result, error) { return post(tx, p, true) }

// post 是 Credit / Debit 的共同实现,六步与 design §3.3 逐条对应。
func post(tx *gorm.DB, p Posting, debit bool) (Result, error) {
	if tx == nil {
		return Result{}, db.ErrNotReady
	}
	// (1) 参数校验。金额超出上界直接错,不截断:截断会让账本上的数与业务单据对不上。
	if p.Amount <= 0 || p.Amount > int64(common.MaxQuota) {
		return Result{}, fmt.Errorf("%w: %d", ErrBadAmount, p.Amount)
	}
	if p.UserId <= 0 {
		return Result{}, fmt.Errorf("%w: user_id=%d", ErrBadPosting, p.UserId)
	}
	if p.IdemScope == "" || len(p.IdemScope) > 32 || p.IdemKey == "" {
		return Result{}, fmt.Errorf("%w: idem_scope=%q idem_key 长度 %d", ErrBadPosting, p.IdemScope, len(p.IdemKey))
	}
	col, err := postingColumn(p.Kind, debit)
	if err != nil {
		return Result{}, err
	}

	// (2) 锁余额行。这是本包唯一的加锁点;锁序契约见 doc.go。
	bal, err := LockBalance(tx, p.UserId)
	if err != nil {
		return Result{}, err
	}

	// (3) 锁内快速失败:此时什么都没写,调用方回滚的是一个空事务。
	if debit {
		if bal.Available < p.Amount {
			return Result{}, ErrInsufficient
		}
	} else if bal.Available > int64(common.MaxQuota)-p.Amount {
		return Result{}, ErrOverflow
	}

	// (4) DoNothing 插入流水行,RowsAffected == 1 才是新建。
	//
	// 不用 db.UpsertHead:那是 DO UPDATE 的头部,而 DO UPDATE 命中时 MySQL 返回 2、
	// PostgreSQL 返回 1(与新插入同值),"是不是新建"在 PG 上分不开。DoNothing 那一档
	// 三家统一(命中 0)。
	signed := p.Amount
	if debit {
		signed = -p.Amount
	}
	after := bal.Available + signed
	now := common.GetTimestamp()
	row := Ledger{
		LedgerNo:     NewLedgerNo(),
		UserId:       p.UserId,
		Kind:         string(p.Kind),
		Amount:       signed,
		BalanceAfter: after,
		IdemScope:    p.IdemScope,
		IdemKey:      normalizeIdemKey(p.IdemKey),
		RefType:      clip(p.RefType, 32),
		RefNo:        clip(p.RefNo, 64),
		ActNo:        clip(p.ActNo, 32),
		PeerUserId:   p.PeerUserId,
		RateBps:      p.RateBps,
		RateGroup:    clip(p.RateGroup, 64),
		BaseQuota:    p.BaseQuota,
		Remark:       clip(p.Remark, 255),
		OperatorId:   p.OperatorId,
		CreatedAt:    now,
	}
	ins := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "idem_scope"}, {Name: "idem_key"}},
		DoNothing: true,
	}).Create(&row)
	if ins.Error != nil {
		db.MarkFailure(ins.Error)
		return Result{}, fmt.Errorf("stardust: 写入流水: %w", ins.Error)
	}
	if ins.RowsAffected != 1 {
		// 幂等重放:读回已有行,余额不再动。读不回来就是错(不是重放):MySQL 的
		// ON DUPLICATE KEY 对任意唯一键生效,ledger_no 撞键同样会走到这里,而那不是重放。
		var prior Ledger
		if err := tx.Where("idem_scope = ? AND idem_key = ?", row.IdemScope, row.IdemKey).
			Take(&prior).Error; err != nil {
			return Result{}, fmt.Errorf("stardust: 幂等命中却读不回已有流水(%s/%s): %w",
				row.IdemScope, row.IdemKey, err)
		}
		return Result{Inserted: false, LedgerId: prior.Id, LedgerNo: prior.LedgerNo, BalanceAfter: prior.BalanceAfter}, nil
	}

	// (5) 仅新建时做条件 UPDATE:available 与累计列在同一条语句里改。
	//
	// WHERE 里再判一次上下界不是多余:第 3 步的判断依赖行锁,而 SQLite(测试方言)
	// 没有行锁 —— 条件 UPDATE 是所有方言上都成立的最后一道闸。manual 的累计列是
	// 带符号净额,扣减时要减;其余累计列只记毛额,方向已由 postingColumn 钉死。
	colDelta := p.Amount
	if debit && col == colTotalAdjusted {
		colDelta = -p.Amount
	}
	set := map[string]any{
		"available":  gorm.Expr("available + ?", signed),
		col:          gorm.Expr(col+" + ?", colDelta),
		"updated_at": now,
	}
	var upd *gorm.DB
	if debit {
		upd = tx.Model(&Balance{}).Where("user_id = ? AND available >= ?", p.UserId, p.Amount).Updates(set)
	} else {
		upd = tx.Model(&Balance{}).Where("user_id = ? AND available <= ?", p.UserId, int64(common.MaxQuota)-p.Amount).Updates(set)
	}
	if upd.Error != nil {
		db.MarkFailure(upd.Error)
		return Result{}, fmt.Errorf("stardust: 更新余额: %w", upd.Error)
	}
	if upd.RowsAffected != 1 {
		return Result{}, fmt.Errorf("stardust: 余额条件更新影响 %d 行(user %d, kind %s, amount %d),事务必须回滚",
			upd.RowsAffected, p.UserId, p.Kind, signed)
	}
	// (6) balance_after 用的是锁内读到的 available ± amount:持锁期间没有别人能改它。
	return Result{Inserted: true, LedgerId: row.Id, LedgerNo: row.LedgerNo, BalanceAfter: after}, nil
}

// LockBalance 取得余额行的排他锁,不存在则先插入零值行。
//
// seed 一行(DoNothing)再 db.LockForUpdate。
// 这是本包与 lottery / mall 共同约定的唯一加锁点;锁序契约见 doc.go。
func LockBalance(tx *gorm.DB, userId int) (*Balance, error) {
	if tx == nil {
		return nil, db.ErrNotReady
	}
	if userId <= 0 {
		return nil, fmt.Errorf("%w: user_id=%d", ErrBadPosting, userId)
	}
	seed := Balance{UserId: userId, Carry: decimal.Zero, UpdatedAt: common.GetTimestamp()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&seed).Error; err != nil {
		db.MarkFailure(err)
		return nil, fmt.Errorf("stardust: 建余额行: %w", err)
	}
	var bal Balance
	if err := db.LockForUpdate(tx).Where("user_id = ?", userId).Take(&bal).Error; err != nil {
		db.MarkFailure(err)
		return nil, fmt.Errorf("stardust: 锁余额行: %w", err)
	}
	return &bal, nil
}

var ledgerSeq atomic.Uint64

// NewLedgerNo 生成流水号,形如 SD20260904T091533-3f2-9c1e4b7d20,最长 32 字符。
//
// 形状照 twophase.NewOrderNo:UTC 时间给人看、进程内序列抗同一秒内的高频碰撞、
// 随机段(common.GetUUID 走 crypto/rand)保证不可预测。序列取 36³ 的模,是为了
// 把总长钉在列宽 varchar(32) 之内:2 + 15 + 1 + 3 + 1 + 10 = 32。
// 单号里刻意不编码用户 id。碰撞由 uk_qy_sdl_no 兜底。
func NewLedgerNo() string {
	ts := time.Now().UTC().Format("20060102T150405")
	sq := strconv.FormatUint(ledgerSeq.Add(1)%46656, 36)
	rnd := common.GetUUID()
	if len(rnd) > 10 {
		rnd = rnd[:10]
	}
	return "SD" + ts + "-" + sq + "-" + rnd
}

// normalizeIdemKey 把任意长度的业务键压进 varchar(96)。
//
// 直接截断会让两个不同的单号撞成同一个幂等键 —— 那意味着第二笔永远记不上。
// 超长时改用哈希,保证单射。
func normalizeIdemKey(raw string) string {
	if len(raw) <= 96 {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "h:" + hex.EncodeToString(sum[:])[:64]
}

// clip 按字节上限截断,但不切开一个多字节字符(varchar(N) 在 MySQL 上按字符计,
// 按字节截断永远不会超宽)。
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	for len(string(r)) > max {
		r = r[:len(r)-1]
	}
	return string(r)
}

// UnitName 是当前生效的单位名(YAML 基线 + qy_settings 覆盖)。
func UnitName() string { return effective().Name }

// QuotaPerUnit 是刻度:1 星屑 = 多少额度。YAML 为 0 时取 common.QuotaPerUnit。
//
// 它被冻结进每一行日桶,改它不追溯。永远 ≥ 1:它是除数。
func QuotaPerUnit() int64 {
	if v := config.Get().Stardust.QuotaPerUnit; v > 0 {
		return v
	}
	if v := int64(common.QuotaPerUnit); v > 0 {
		return v
	}
	return 1
}
