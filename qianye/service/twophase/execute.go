// Package twophase 实现跨库两阶段资金操作。
//
// 要解决的根本问题:记账在扩展库、钱在主库,两者不能放进同一个事务。
// 中间必然存在一个窗口 —— 主库已提交但扩展库还没回写。进程在这一刻崩溃,
// 钱已经动了却没有任何记录,这在资金系统里是不可接受的。
//
// 解法是主库 outbox:把单号连同资金变更写进同一个主库事务。
// 之后无论进程怎么崩,补偿任务都能通过查询 outbox 精确判定"主库到底动没动"。
//
// 划转、佣金兑现、提现到账、佣金冲正全部走这里,不要各写一套。
package twophase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"gorm.io/gorm"
)

// maxErrBytes 是 qy_fund_orders.last_error 的列宽(varchar(512))。
const maxErrBytes = 512

var (
	// ErrAmountOutOfRange 表示金额超出 common.MaxQuota 这条全站额度换算上界。
	// 绝不静默截断:那会让用户凭空少钱或多钱。
	ErrAmountOutOfRange = errors.New("qianye: 金额超出允许范围")
	// ErrInProgress 表示同一幂等键的单据正在处理中。
	ErrInProgress = errors.New("qianye: 该请求正在处理中,请稍候")
	// ErrOrderFailed 表示单据此前已明确失败。
	ErrOrderFailed = errors.New("qianye: 该请求此前已失败")
	// ErrIdemConflict 表示同一幂等键上一次携带的资金要素与本次不同。
	//
	// 这不是"重复提交",是"用同一个键提交了另一笔请求"。必须让调用方按 409 处理:
	// 既不能返回原单的成功,更不能拿本次请求的参数去写成功审计。
	ErrIdemConflict = errors.New("qianye: 幂等键与原请求的资金要素不一致")
	// ErrOrderDiverged 表示回写时发现单据已被别的路径推进到非成功状态。
	// 钱可能已经动了,调用方绝不能按"本次成功"收尾。
	ErrOrderDiverged = errors.New("qianye: 单据已被其他路径推进")
)

// Request 描述一次跨库资金操作。
type Request struct {
	Kind      string
	IdemScope string
	// IdemKey 与 IdemScope 组成唯一键。重复提交、支付回调重放、补偿重扫
	// 都靠它归并,绝不能省略。
	IdemKey string

	// Fingerprint 是本次请求资金要素的摘要,用 Digest() 生成。
	//
	// 唯一键只保证"不重复执行",保证不了"重放的是同一个请求" —— 幂等键往往由
	// 前端生成,换金额、换收款人复用同一个键时,幂等命中会返回原单成功。
	// 填了它,resolveExisting 会在不一致时返回 ErrIdemConflict。
	//
	// 可选:留空表示不参与校验(历史单据的列值也是空)。空值一律跳过校验,
	// 不是判为不匹配 —— 否则升级瞬间全部历史重放都会变成冲突。
	Fingerprint string

	UserId     int
	PeerUserId int

	AmountQuota int64
	FeeQuota    int64

	RefType string
	RefId   string

	// LocalDetail 在创建 pending 单的同一扩展库事务内插入业务明细行。
	// 明细与单据必须同生共死,否则会出现有单无明细的孤儿数据。
	LocalDetail func(tx *gorm.DB, order *qymodel.FundOrder) error

	// MainApply 在主库事务内执行真正的资金变更。
	//
	// 实现约定(违反会出资损):
	//   - 扣款一律用 WHERE quota >= ? 并检查 RowsAffected == 1,
	//     绝不使用 DecreaseUserQuota(它没有余额校验,会扣成负数)
	//   - 加款前校验不超过 common.MaxQuota
	//   - 涉及多个用户时按 user id 升序加锁,避免 A→B / B→A 死锁
	//   - 禁止调用 user.Update() 或 IncrementUserAuthVersionWithTx(会吊销用户会话)
	MainApply func(tx *gorm.DB, order *qymodel.FundOrder) error

	// AfterCommit 在主库事务提交后执行:缓存失效、写账本日志。
	// 失败只记日志,不影响资金结果 —— 钱已经动了,不能因为日志没写就回滚。
	AfterCommit func(order *qymodel.FundOrder)

	// LocalCommit 在扩展库回写 success 的同一事务内执行业务副作用,
	// 例如扣减佣金余额。
	LocalCommit func(tx *gorm.DB, order *qymodel.FundOrder) error
}

// Digest 按**用户这次请求说了什么**算出指纹,供调用方填进 Fingerprint:
//
//	r := twophase.Request{...}
//	r.Fingerprint = r.Digest()          // 需要额外要素时 r.Digest(payeeDigest)
//
// 抽成方法而不是让各模块自己拼字符串:字段顺序或分隔符只要有一处不一致,
// 同一笔请求就会算出不同指纹,幂等重放会被误判成冲突 —— 那是把"防伪造审计"
// 变成"正常重试打不通"。extra 用于承载不在 Request 里但决定资金去向的要素
// (例如提现的收款方式与账号摘要)。
//
// 口径:只收"请求要素",不收"服务端派生量"。
//
// 因此刻意**不含 FeeQuota**:手续费是受理时按当时的 transfer.fee_* 配置算出来的,
// 用户在请求里根本说不了它。用户首次提交撞上超时、运营恰好在这中间调了费率,
// 客户端拿同一个 client_request_id 重试就会算出另一个指纹 → ErrIdemConflict → 409
// 「请到划转记录中核对」,而原单其实压根没成功,用户既转不成也不敢重发。
// 这与 commission/clawback.go 的 sameClawbackRequest 刻意不比 GrossAmount 是同一条
// 理由(被服务端削过/派生的量不能当指纹分量),两处口径必须一致。
//
// AmountQuota 必须留下:它就是用户请求里写的金额,换金额重放正是指纹要识破的攻击。
// 手续费由金额与配置唯一决定,漏掉它也不会放过任何一种换参重放。
//
// 用 \x1f(单元分隔符)而不是竖线做分隔:它不会出现在业务字符串里,
// 避免 "a|b" 与 "a" + "|b" 撞出同一个指纹。
func (r Request) Digest(extra ...string) string {
	parts := make([]string, 0, 7+len(extra))
	parts = append(parts,
		r.Kind,
		r.IdemScope,
		strconv.Itoa(r.UserId),
		strconv.Itoa(r.PeerUserId),
		strconv.FormatInt(r.AmountQuota, 10),
		r.RefType,
		r.RefId,
	)
	parts = append(parts, extra...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	// sha256 的十六进制正好 64 字符,与 qy_fund_orders.fingerprint 的列宽一致。
	return hex.EncodeToString(sum[:])
}

// Execute 执行一次跨库资金操作。
//
// 步骤:
//  1. 【扩展库事务】落 pending 单 + 业务明细。幂等键冲突则返回已有单的结果。
//  2. 【主库事务】登记 outbox → 执行资金变更。outbox 冲突说明此前已生效,跳过。
//  3. 提交后:缓存失效 + 写账本日志(失败不影响结果)。
//  4. 【扩展库事务】回写 success + 业务副作用。
//
// 若步骤 2 提交成功但步骤 4 失败,单据停在 pending,由补偿任务通过 outbox 探针修复。
//
// ctx 的分工必须逐步骤区分,不能一路直传到底(见 settleContext):
//   - 步骤 1、2 是**正向**路径,还没有任何结果定局,一律用调用方的 ctx。
//     调用方专门造了 guard.ColdContext(3 秒)的预算,漏接就只剩主库
//     innodb_lock_wait_timeout(50 秒)与扩展库 DSN readTimeout(30 秒)兜底,
//     而"等一个空闲连接"这一段在 context.Background() 下根本没有上界。
//   - 步骤 4(以及步骤 2 失败后的 markFailed)是**收尾**路径,主库那一侧已经定局。
//     它们必须切断调用方的取消链,否则一次超时会连带把收尾也打掉,单据永远停在
//     pending —— 那正是把"慢一点"升级成"人工单"的路径。
func Execute(ctx context.Context, req Request) (*qymodel.FundOrder, error) {
	if !db.Available() {
		return nil, db.ErrNotReady
	}
	if err := validateAmount(req.AmountQuota); err != nil {
		return nil, err
	}
	if req.IdemScope == "" || req.IdemKey == "" {
		return nil, errors.New("qianye: 缺少幂等键")
	}
	gdb := db.Get()
	if gdb == nil {
		return nil, db.ErrNotReady
	}
	// 正向路径的每一条扩展库语句都必须带上调用方的预算。绑在句柄上而不是
	// 逐条 WithContext:createOrLoadOrder 拿到的是这个句柄,事务内的 tx 也从它派生,
	// 漏一条就等于这条链路上开了一个没有上界的口子。
	gdb = gdb.WithContext(ctx)

	order, existing, err := createOrLoadOrder(gdb, req)
	if err != nil {
		return nil, err
	}
	if existing {
		return resolveExisting(order, req.Fingerprint)
	}

	// ── 阶段二:主库事务 ──
	applied, phase, mainErr := applyOnMainDB(ctx, req, order)
	if mainErr != nil {
		settleAfterMainFailure(ctx, gdb, order, phase, mainErr)
		return order, mainErr
	}

	// ── 阶段三:提交后的非事务收尾 ──
	// 认领成功才执行:补偿任务的 PostCommit 是同一份收尾的另一条入口,
	// 账本行只能写一次。认领失败(扩展库不可用)时交给补偿任务,不抢跑。
	//
	// 认领语句走 settleContext:主库已经提交,钱已经动了。沿用调用方那个可能刚好
	// 耗尽的 ctx 会让认领必然失败,于是每一笔慢操作的账本行都要等补偿任务补 ——
	// 与 markSuccess 同一条理由。补偿任务侧的认领**不能**这么做:那里 ctx 就是租约,
	// 丢了租约还继续写就是双跑。
	if req.AfterCommit != nil {
		settleCtx, cancel := settleContext(ctx)
		if claimAfterCommit(settleCtx, gdb, order) {
			safeAfterCommit(req.AfterCommit, order)
		}
		cancel()
	}

	// ── 阶段四:回写扩展库 ──
	won, err := markSuccess(ctx, gdb, order, req.LocalCommit)
	if err != nil {
		// 主库已经生效,这里失败不能回滚。留 pending 交给补偿任务。
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 主库已生效但扩展库回写失败,已交由补偿任务处理: %v", order.OrderNo, err))
		return order, nil
	}
	if !won {
		// CAS 没命中:补偿任务或人工裁决已经推进了这张单,order 里已是回读到的真实状态。
		// 这条转移不是我们做的,审计留给赢下 CAS 的那一方写,这里绝不能补一条 ResultOK ——
		// 对方可能推成的是 Failed/Uncertain。
		return order, divergedError(order)
	}

	auditTransition(order, qymodel.ResultOK, transitionReason(applied))
	return order, nil
}

// createOrLoadOrder 落 pending 单;幂等键冲突时加载已有单。
func createOrLoadOrder(gdb *gorm.DB, req Request) (*qymodel.FundOrder, bool, error) {
	now := common.GetTimestamp()
	order := &qymodel.FundOrder{
		OrderNo:     NewOrderNo(req.Kind),
		Kind:        req.Kind,
		Status:      qymodel.StatusPending,
		IdemScope:   req.IdemScope,
		IdemKey:     req.IdemKey,
		Fingerprint: req.Fingerprint,
		UserId:      req.UserId,
		PeerUserId:  req.PeerUserId,
		AmountQuota: req.AmountQuota,
		FeeQuota:    req.FeeQuota,
		RefType:     req.RefType,
		RefId:       req.RefId,
		NodeName:    common.NodeName,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if req.LocalDetail != nil {
			return req.LocalDetail(tx, order)
		}
		return nil
	})
	if err == nil {
		return order, false, nil
	}
	if !isDuplicateKey(err) {
		db.MarkFailure(err)
		return nil, false, err
	}

	// 幂等命中:加载已有单。
	var found qymodel.FundOrder
	if e := gdb.Where("idem_scope = ? AND idem_key = ?", req.IdemScope, req.IdemKey).
		First(&found).Error; e != nil {
		// 也可能是单号碰撞而非幂等键冲突,此时重试一次即可。
		return nil, false, fmt.Errorf("qianye: 单据冲突且无法加载已有单据: %w", e)
	}
	return &found, true, nil
}

// resolveExisting 决定幂等命中时返回什么。
//
// want 是本次请求的指纹。指纹校验必须排在状态分支之前:参数对不上时,无论原单
// 是成功还是失败,本次都不是"同一个请求的重放",调用方拿原单的结果去回应本次请求
// 就是在伪造记录(资金侧毫无痕迹,审计表却多出一条金额虚高的成功记录)。
func resolveExisting(order *qymodel.FundOrder, want string) (*qymodel.FundOrder, error) {
	// 任一侧为空都跳过:历史行的列值是空,尚未接入指纹的调用方传的也是空。
	// 把空当成"不匹配"会让升级瞬间所有正常重放变成 409。
	if want != "" && order.Fingerprint != "" && want != order.Fingerprint {
		return order, fmt.Errorf("%w: 单号 %s", ErrIdemConflict, order.OrderNo)
	}
	switch order.Status {
	case qymodel.StatusSuccess, qymodel.StatusReversed:
		return order, nil // 幂等命中,不再执行任何副作用
	case qymodel.StatusFailed:
		if order.LastError != "" {
			return order, fmt.Errorf("%w: %s", ErrOrderFailed, order.LastError)
		}
		return order, ErrOrderFailed
	default:
		// Pending / InDoubt / Uncertain 一律 ErrInProgress。
		//
		// InDoubt 尤其不能翻译成 ErrOrderFailed:那会让调用方把一笔可能已经
		// 生效的资金当成"确定没发生"去回滚 —— 正是本轮要修的那个歧义。
		// Uncertain 同理,它等的是人,不是本次请求。
		grace := int64(config.Get().TwoPhase.PendingGraceSeconds)
		if common.GetTimestamp()-order.CreatedAt < grace {
			return order, ErrInProgress
		}
		// 超过宽限期的未定局单交给补偿任务,不在请求线程里重试:
		// 重试需要主库探针,那是慢操作,不该阻塞用户请求。
		return order, ErrInProgress
	}
}

// mainPhase 记录主库事务停在哪一步。
//
// 这是"从没开始 / 主库明确回滚 / 结局不明"三者唯一可靠的判据,也是本轮修复的
// 核心:**只要 COMMIT 从来没有发出去过,主库就不可能提交**。反过来,COMMIT 一旦
// 发出,客户端拿到的错误里不含任何关于服务端结局的信息 —— 连接在 COMMIT 期间断掉时
// InnoDB 仍可能把事务提交下去,database/sql 在 ctx 恰好于此刻取消时也会返回错误。
//
// 旧实现用 gorm 的 Transaction(fc) 包住整段,拿不到这个分界:fc 返回的错误、
// Begin 的错误、Commit 的错误在返回值里长得一模一样,于是 Execute 只能一律 markFailed,
// Failed 从此同时意味着"没开始"与"结局不明"。四个业务模块随后都得在自己那一侧
// 补一次 ProbeMainSide 才敢回滚,而任何一个忘记补的调用点就是一次超发或错扣。
type mainPhase int8

const (
	// phaseBegin:连事务都没开起来。主库确定没动。
	phaseBegin mainPhase = iota
	// phaseBody:事务体内的语句失败或返回业务错误,COMMIT 未发出。主库确定没动。
	phaseBody
	// phaseCommit:COMMIT 已发出。err == nil 表示确定提交;err != nil 表示**结局不明**。
	phaseCommit
)

// applyOnMainDB 在主库事务内登记 outbox 并执行资金变更。
//
// 返回 (本次是否真正执行了资金变更, 停在哪一步, 错误)。
// 第一个返回值 false 表示此前已生效(outbox 认领落空)。
//
// 不用 gorm 的 Transaction 包装器:那个包装器把 Begin / 事务体 / Commit 三种错误
// 揉成同一个返回值,而这三者对资金系统来说是完全不同的结论(见 mainPhase)。
// 手写 Begin/Rollback/Commit 是为了留住这个分界,不是为了绕开框架。
//
// 用调用方的 ctx:这一步还在"正向"一侧,预算耗尽时事务回滚 = 主库没动,
// 是一个确定的、安全的结局(outbox 探针随后也能复核出来)。反过来若不接 ctx,
// 同一个收款账号上的并发划转会在行锁上一直等到 innodb_lock_wait_timeout,
// 整段时间里一条主库连接被钉住。
func applyOnMainDB(ctx context.Context, req Request, order *qymodel.FundOrder) (bool, mainPhase, error) {
	tx := model.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return false, phaseBegin, tx.Error
	}
	// panic 与任何提前 return 都必须回滚。committed 之后 Rollback 是空操作,
	// 但仍用标志位挡住:让"提交成功"这条路径上不再对主库多发一条语句。
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
		}
	}()

	appliedNow := true
	if config.Get().TwoPhase.OutboxEnabled() {
		claimed, err := model.QyClaimFundOutbox(tx, &model.QyFundOutbox{
			OrderNo: order.OrderNo,
			Kind:    order.Kind,
			UserId:  order.UserId,
			PeerId:  order.PeerUserId,
			Amount:  order.AmountQuota,
		})
		if err != nil {
			return false, phaseBody, err
		}
		// 认领落空说明此前已生效(补偿任务重跑场景),必须跳过资金变更否则重复扣款。
		appliedNow = claimed
	}
	if appliedNow && req.MainApply != nil {
		if err := req.MainApply(tx, order); err != nil {
			return false, phaseBody, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		// **结局不明**:COMMIT 已经发出去了。appliedNow 照实返回,但它只描述
		// "本进程打算做什么",不描述"主库到底做成了什么"。
		return appliedNow, phaseCommit, err
	}
	committed = true
	return appliedNow, phaseCommit, nil
}

// markSuccess 把 pending 单回写成 success 并执行业务副作用。
//
// 返回值 won 表示本次是否赢下了 CAS。false 意味着补偿任务或人工裁决已抢先推进
// 这张单:此时 order 已被刷成回读到的真实状态,LocalCommit 不执行(赢家的
// Resolver 负责补做),调用方也不能按"本次成功"收尾。
//
// 这一步走 settleContext:主库已经提交,钱已经动了。沿用调用方那个可能刚好耗尽的
// ctx 会让回写立刻 DeadlineExceeded,单据停在 pending 等补偿任务 —— 用户看到的是
// 一笔"没成功"的划转,而钱已经扣了。
func markSuccess(ctx context.Context, gdb *gorm.DB, order *qymodel.FundOrder, localCommit func(*gorm.DB, *qymodel.FundOrder) error) (bool, error) {
	ctx, cancel := settleContext(ctx)
	defer cancel()

	now := common.GetTimestamp()
	won := false
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&qymodel.FundOrder{}).
			Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
			Updates(map[string]any{
				"status":     qymodel.StatusSuccess,
				"settled_at": now,
				"updated_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		won = true
		order.Status = qymodel.StatusSuccess
		order.SettledAt = now
		order.UpdatedAt = now
		if localCommit != nil {
			return localCommit(tx, order)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if !won {
		reloadStatus(ctx, gdb, order)
	}
	return won, nil
}

// settleContext 返回**收尾回写**应使用的 ctx:切断调用方的取消链,另配一个独立预算。
//
// 为什么不能直传调用方的 ctx:markFailed 恰恰常常是因为调用方预算耗尽才被触发的
// (applyOnMainDB 超时 → mainErr)。用同一个已过期的 ctx 去写 failed,第一条语句就
// DeadlineExceeded,单据留在 pending;而 transfer/service.go 的 releaseOnFailure 只在
// order.Status == Failed 时才回滚风控预占 —— 于是每一笔慢划转都变成一张人工单。
// markSuccess 同理:那时钱已经动了。
//
// 为什么也不能干脆不接 ctx:那等于把上界交给主库 innodb_lock_wait_timeout(50 秒)
// 与扩展库 DSN readTimeout(30 秒),而"等一个空闲连接"这一段在 Background 下无界。
// 收尾要的是"不被调用方取消",不是"永远等下去"。
func settleContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return guard.ColdContext(context.WithoutCancel(ctx))
}

// settleAfterMainFailure 在主库事务失败后给单据落一个**与证据相符**的状态。
//
// 这是本轮修复的落点。旧实现(markFailed)对任何 mainErr 一律置 Failed,于是
// Failed 同时承载两件完全不同的事:"从没开始"与"结局不明"。四个业务模块都按
// order.Status == Failed 做不可逆动作(退风控预占、解冻佣金、换代次重发出款),
// 拿一个歧义值去做不可逆动作,超发与错扣就是必然。
//
// 现在按 COMMIT 有没有发出去分流:
//
//	phaseBegin / phaseBody → Failed    COMMIT 未发出,主库不可能提交,回滚是安全的
//	phaseCommit            → InDoubt   COMMIT 已发出,结局只有 outbox 探针说了算
//
// InDoubt 不是终态:它留在补偿任务的收敛范围内(见 qymodel.UnsettledStatuses),
// 探针给出确定判据后自动落 Success 或 Failed,判不出来才转 Uncertain 交人工。
// 因此这条路径**不**要求调用方做任何事 —— 它们对非 Failed 一律不回滚。
//
// WHERE 里的 status = Pending 不是可有可无的乐观锁,而是这张单终态的唯一归属判定:
// 补偿任务在探针耗尽时会把单推成 Uncertain —— 那是系统里"我不知道钱动没动,禁止
// 自动回滚"的唯一出口。业务线程随后拿到一个确定性错误并不能推翻它(主库事务可能
// 在超时后才提交)。若这里无视 RowsAffected == 0 仍在内存里改成 Failed 并写审计,
// 调用方会据此走回滚分支(清预占、解冻佣金),最终 qy_fund_orders 是 Uncertain
// 等人工、业务明细已 failed 且预占已释放、审计声称转成了 failed —— 三份记录互相
// 矛盾,而 Uncertain 单不在 Compensate 的收敛范围内(它只扫未定局态),矛盾永不自愈。
//
// 所以 CAS 落空时必须回读真实状态交还给调用方(它们都按 order.Status == Failed
// 才回滚),并且不写审计:这条转移不是我们做的。
//
// 走 settleContext 而不是调用方的 ctx:见 settleContext 的说明,这一步最常见的
// 触发原因就是调用方预算已经耗尽。
func settleAfterMainFailure(ctx context.Context, gdb *gorm.DB, order *qymodel.FundOrder, phase mainPhase, cause error) {
	ctx, cancel := settleContext(ctx)
	defer cancel()

	target, result := qymodel.StatusFailed, qymodel.ResultFail
	if phase == phaseCommit {
		// 结局不明。审计结果记 pending 而不是 fail:这条转移没有宣布任何结论,
		// 写成 fail 会让事后仲裁把一笔可能已经生效的资金当成没发生。
		target, result = qymodel.StatusInDoubt, qymodel.ResultPending
	}

	now := common.GetTimestamp()
	msg := audit.Truncate(cause.Error(), maxErrBytes)
	res := gdb.WithContext(ctx).Model(&qymodel.FundOrder{}).
		Where("order_no = ? AND status = ?", order.OrderNo, qymodel.StatusPending).
		Updates(map[string]any{
			"status":     target,
			"last_error": msg,
			"updated_at": now,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError(fmt.Sprintf("qianye: 单号 %s 标记 %s 态时出错: %v",
			order.OrderNo, qymodel.StatusName(target), res.Error))
		return
	}
	if res.RowsAffected == 0 {
		reloadStatus(ctx, gdb, order)
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 标记 %s 态时发现单据已被其他路径推进为 %s(原始错误: %v),本次不改写状态、不写审计",
			order.OrderNo, qymodel.StatusName(target), qymodel.StatusName(order.Status), cause))
		return
	}
	order.Status = target
	order.LastError = msg
	order.UpdatedAt = now
	auditTransition(order, result, msg)
	if target == qymodel.StatusInDoubt {
		// 主库 COMMIT 断连是资金系统里最需要被人看见的一类事件:钱可能已经动了。
		// 补偿任务随后会自动收敛,但在它收敛之前必须先在日志里留一条。
		common.SysError(fmt.Sprintf(
			"qianye: 单号 %s 的主库事务在 COMMIT 阶段断连,结局不明(用户 %d,金额 %d),已置 in_doubt 交补偿任务复判: %s",
			order.OrderNo, order.UserId, order.AmountQuota, msg))
	}
}

// reloadStatus 在 CAS 落空后回读单据的真实状态。
//
// 回读失败时刻意保留内存里的 pending:调用方对 pending 的处理是"不回滚,交给补偿
// 任务",这是所有分支里最安全的那个。绝不能猜一个终态。
//
// ctx 由调用方(markSuccess / markFailed)传入,那两处都已经换成 settleContext:
// 回读是收尾判定的一部分,不该被调用方的取消打断。
func reloadStatus(ctx context.Context, gdb *gorm.DB, order *qymodel.FundOrder) {
	var cur qymodel.FundOrder
	if err := gdb.WithContext(ctx).Where("order_no = ?", order.OrderNo).Take(&cur).Error; err != nil {
		db.MarkFailure(err)
		common.SysError(fmt.Sprintf("qianye: 单号 %s 回读状态失败,按 pending 交给补偿任务: %v",
			order.OrderNo, err))
		return
	}
	order.Status = cur.Status
	order.LastError = cur.LastError
	order.SettledAt = cur.SettledAt
	order.UpdatedAt = cur.UpdatedAt
}

// divergedError 描述"回写时 CAS 落空"的结局。
//
// 对方推成 Success/Reversed 时返回 nil:主库确实生效了,业务收尾由赢家的
// Resolver 负责,这与幂等重放同义,不该让用户看到失败。
// 推成 Failed/Uncertain/InDoubt 时必须返回错误 —— 主库这一侧已经动过钱,而单据说没有,
// 调用方需要走各自的冲突处理(探针复核 → 转人工),绝不能当成功返回。
func divergedError(order *qymodel.FundOrder) error {
	if order.Status == qymodel.StatusSuccess || order.Status == qymodel.StatusReversed {
		return nil
	}
	return fmt.Errorf("%w: 单号 %s 当前状态 %s", ErrOrderDiverged, order.OrderNo,
		qymodel.StatusName(order.Status))
}

// safeAfterCommit 执行提交后收尾。此时钱已经动了,任何 panic 都不能冒泡。
func safeAfterCommit(fn func(*qymodel.FundOrder), order *qymodel.FundOrder) {
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf("qianye: 单号 %s 的提交后处理发生 panic(已拦截): %v",
				order.OrderNo, r))
		}
	}()
	fn(order)
}

func auditTransition(order *qymodel.FundOrder, result, reason string) {
	audit.Write(nil, audit.Entry{
		TraceNo:      order.OrderNo,
		Category:     qymodel.AuditCategoryFund,
		Action:       "fund." + order.Kind + "." + qymodel.StatusName(order.Status),
		ActorType:    qymodel.ActorSystem,
		ActorUserId:  order.UserId,
		TargetUserId: order.PeerUserId,
		AmountQuota:  order.AmountQuota,
		Result:       result,
		Reason:       reason,
	})
}

func transitionReason(appliedNow bool) string {
	if appliedNow {
		return ""
	}
	return "主库侧此前已生效,本次跳过资金变更"
}

// validateAmount 校验金额落在 common.MaxQuota 这条全站额度换算上界之内。
//
// 扩展库用 int64 承载聚合与中间量,而 common.MaxQuota 是**算术**上界而非列宽
// (额度列在 SQLite/MySQL/PostgreSQL 上都是 64 位,见 common/quota_math.go)。
// 跨库前必须显式拒绝越界值,绝不能让它静默溢出成负数。
func validateAmount(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: 金额必须大于 0", ErrAmountOutOfRange)
	}
	if amount > int64(common.MaxQuota) {
		return fmt.Errorf("%w: 金额 %d 超过单笔上限 %d", ErrAmountOutOfRange, amount, common.MaxQuota)
	}
	return nil
}

// isDuplicateKey 判断错误是否为唯一索引冲突。
//
// 判据统一收在 db.IsDuplicateKey:三家方言的报错文本互不相同,而这里把"撞键"
// 当作幂等命中(同一 idem_key 已落过单),漏判一家会让幂等重试变成真失败。
func isDuplicateKey(err error) bool { return db.IsDuplicateKey(err) }
