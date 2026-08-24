package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/twophase"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// payout_adjudicate_db_test.go —— 人工落账那条最终裁决口。
//
// 它守的是一个此前**没有任何人能处置**的现场:出款 held + 本代次资金单 failed +
// 主库探针说"可能已生效"。三条自动链路(出款 worker 只扫 planned/paying/failed、
// 补偿任务只扫 pending/in_doubt、复判只扫 uncertain)全都不碰这一笔,而
// RetryPayout 的换代次分支被探针挡死 —— 于是那笔钱永久挂在冻结中,那一场活动
// 因此永远过不了删除闸门②。
//
// 三组用例分别钉住:
//
//  1. 分支表:哪些前置状态允许落账,哪些必须拒绝(其中"资金单未落定"那一档
//     就是与补偿任务的互斥判据);
//  2. 两种结论各自落成什么(paid / 换代次重排),以及钱有没有被多动一次;
//  3. 落账之后那一场**真的删得掉** —— 这是整件事的目的,不是副产品。

// newAdjudicateEnv 在出款测试环境之上补齐主库那两张表与补偿回调注册表。
//
// 三件事缺一不可:
//
//   - users / logs:credited 那一支走与补偿任务逐字相同的收尾链路,其中包含
//     "给用户补一行账本日志"。不建这两张表,那一步会在 runPostCommit 的
//     recover 里被吞掉,于是"钱到账了但账单里没有任何一行"这个真实缺陷在测试里
//     永远看不见。
//   - common.RedisEnabled:包级默认是 true 而 common.RDB 是 nil,账本日志那一步
//     会去读用户名缓存并当场空指针 —— 同样被 recover 吞掉。
//   - InstallResolvers():落账靠的正是**生产上那一份**补偿回调
//     (resolvePayoutAfterCompensation)。测试自己写一份等于绕开被测的接线,
//     而"回调没注册"恰恰是本仓 violation 上真实发生过的事故形状。
func newAdjudicateEnv(t *testing.T, lot config.Lottery) *gorm.DB {
	t.Helper()
	gdb := newPayoutEnv(t, lot)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Log{}))
	prevLog, prevRedis := model.LOG_DB, common.RedisEnabled
	model.LOG_DB = model.DB
	common.RedisEnabled = false
	t.Cleanup(func() {
		model.LOG_DB, common.RedisEnabled = prevLog, prevRedis
	})
	InstallResolvers()
	return gdb
}

// seedHeldPayoutWithOrder 造出那个死角现场:出款 held、本代次资金单 failed。
//
// applied 决定要不要落主库 outbox 探针行 —— 落了,探针就答"已生效",
// RetryPayout 从此拒绝这一笔,那正是本端点存在的前提。
func seedHeldPayoutWithOrder(
	t *testing.T, gdb *gorm.DB, actId int64, orderStatus int8, applied bool,
) (*Payout, *qymodel.FundOrder) {
	t.Helper()
	p := seedPayout(t, gdb, actId, func(p *Payout) {
		p.Status = PayoutHeld
		p.Attempts = 8
		p.LastError = "资金单被判失败但主库是否已生效无法排除,需人工核对"
	})
	order := &qymodel.FundOrder{
		OrderNo:     "LP-adj-" + p.PayoutNo,
		Kind:        qymodel.KindLotteryPayout,
		Status:      orderStatus,
		IdemScope:   idemScopePayout,
		IdemKey:     payoutIdemKey(p),
		UserId:      p.UserId,
		AmountQuota: p.AmountQuota,
		RefType:     "lottery_payout",
		RefId:       p.PayoutNo,
		CreatedAt:   common.GetTimestamp(),
	}
	require.NoError(t, gdb.Create(order).Error)
	if applied {
		seedOutboxRow(t, model.DB, order.OrderNo)
	}
	return p, order
}

// 前置状态分支表。
//
// 最要紧的是 pending / in_doubt / uncertain 三行:那三种状态下补偿任务
// (Compensate 扫 UnsettledStatuses、reprobeUncertain 扫 Uncertain)随时会
// 改同一张单,人此刻插手就是双写。互斥必须在这里被拒绝,而不是靠"运营应该
// 等一会儿再点"。
func TestAdjudicatePayout_RejectsEveryStateThatIsNotAStuckFailure(t *testing.T) {
	cases := []struct {
		name string
		// payoutStatus 是出款行的前置状态。
		payoutStatus string
		// orderStatus 是本代次资金单的状态;-1 表示压根没有单据。
		orderStatus int8
		wantErr     *bizError
	}{
		{"held + failed → 允许(基线,单独在下面断言落账)", PayoutHeld, qymodel.StatusFailed, nil},
		{"held + 没有单据 → 拒绝,该走「重试」", PayoutHeld, -1, errAdjudicateNoOrder},
		{"held + success → 拒绝,钱其实到账了", PayoutHeld, qymodel.StatusSuccess, errAdjudicateOrderSettled},
		{"held + pending → 拒绝,补偿任务还在处理", PayoutHeld, qymodel.StatusPending, errAdjudicateOrderOpen},
		{"held + in_doubt → 拒绝,补偿任务还在处理", PayoutHeld, qymodel.StatusInDoubt, errAdjudicateOrderOpen},
		{"held + uncertain → 拒绝,复判还在处理", PayoutHeld, qymodel.StatusUncertain, errAdjudicateOrderOpen},
		{"paying + failed → 拒绝,worker 还在扫它", PayoutPaying, qymodel.StatusFailed, errAdjudicateNotHeld},
		{"failed + failed → 拒绝,自动重试还没走完", PayoutFailed, qymodel.StatusFailed, errAdjudicateNotHeld},
		{"planned + failed → 拒绝", PayoutPlanned, qymodel.StatusFailed, errAdjudicateNotHeld},
		{"paid + failed → 拒绝,已经落定", PayoutPaid, qymodel.StatusFailed, errAdjudicateNotHeld},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAdjudicateEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
			act := seedActivity(t, gdb, nil)

			p := seedPayout(t, gdb, act.Id, func(p *Payout) {
				p.Status = tc.payoutStatus
				p.Attempts = 8
			})
			if tc.orderStatus >= 0 {
				require.NoError(t, gdb.Create(&qymodel.FundOrder{
					OrderNo: "LP-br-" + p.PayoutNo, Kind: qymodel.KindLotteryPayout,
					Status: tc.orderStatus, IdemScope: idemScopePayout, IdemKey: payoutIdemKey(p),
					UserId: p.UserId, AmountQuota: p.AmountQuota, RefId: p.PayoutNo,
					CreatedAt: common.GetTimestamp(),
				}).Error)
			}

			_, err := AdjudicatePayout(context.Background(), p.PayoutNo, VerdictCredited, "核对了主库 logs")
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
			// 被拒绝时**一个字节都不许动**:状态留在原处,后续自动链路照旧接手。
			after := reloadPayout(t, gdb, p.PayoutNo)
			assert.Equal(t, tc.payoutStatus, after.Status)
			assert.Equal(t, 0, after.Epoch, "被拒绝的裁决绝不能换代次")
		})
	}
}

// 核对结论非法时不能落成任何一种账。
//
// 这一条与上面那张表分开:它是**在读库之前**就该被拒的,一个拼错的结论字符串
// 落到默认分支上就是"按没发放处理" —— 那一支会让主库再加一次钱。
func TestAdjudicatePayout_RejectsUnknownVerdict(t *testing.T) {
	gdb := newAdjudicateEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, nil)
	p, _ := seedHeldPayoutWithOrder(t, gdb, act.Id, qymodel.StatusFailed, true)

	for _, verdict := range []string{"", "yes", "CREDITED", "not-credited", "paid"} {
		_, err := AdjudicatePayout(context.Background(), p.PayoutNo, verdict, "核对过了")
		require.ErrorIs(t, err, errAdjudicateVerdict, "结论 %q 必须被拒", verdict)
	}
	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Equal(t, PayoutHeld, after.Status)
	assert.Equal(t, 0, after.Epoch)
}

// 结论「确实已发放」:把账做平,一分钱都不再动。
//
// 三件事必须同时发生,少一件这笔钱就仍然说不清:
//   - 资金单 failed → success(它是账本口径的权威);
//   - 出款行 held → paid(用户端与删除闸门看的都是它);
//   - 主库账本补一行(钱早就到账了,而账单里此前一行都没有 —— 用户第一反应
//     是系统出错,客服也查不出)。
func TestAdjudicatePayout_CreditedSettlesWithoutMovingMoneyAgain(t *testing.T) {
	gdb := newAdjudicateEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, func(a *Activity) { a.Status = StatusFinished })
	p, order := seedHeldPayoutWithOrder(t, gdb, act.Id, qymodel.StatusFailed, true)

	after, err := AdjudicatePayout(context.Background(), p.PayoutNo,
		VerdictCredited, "核对主库 logs 与 users.quota,这笔 500 确实已入账")
	require.NoError(t, err)

	assert.Equal(t, PayoutPaid, after.Status)
	assert.Equal(t, 0, after.Epoch, "已发放的结论绝不换代次 —— 换了就是发第二次")
	assert.NotZero(t, after.SettledAt)

	var got qymodel.FundOrder
	require.NoError(t, gdb.Where("order_no = ?", order.OrderNo).Take(&got).Error)
	assert.Equal(t, qymodel.StatusSuccess, got.Status)

	// 账本行:钱是什么时候、凭哪张单到的账。
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", order.OrderNo).Find(&logs).Error)
	require.Len(t, logs, 1, "主库已生效却没有账本行 = 用户账上凭空多出一笔钱")
	assert.Equal(t, p.UserId, logs[0].UserId)
	assert.Contains(t, logs[0].Content, p.PayoutNo)

	// 活动合计要跟上:held_quota 是实时 SUM,补发成功后它归零,合计不补
	// 就会让同一笔钱从两个口子同时消失(见 addPaidPayoutToActivityTotals)。
	var reloaded Activity
	require.NoError(t, gdb.Where("id = ?", act.Id).Take(&reloaded).Error)
	assert.EqualValues(t, p.AmountQuota, reloaded.PayoutQuota)

	// 幂等:再落一次账不能再改任何东西,也不能再写第二行账本。
	_, err = AdjudicatePayout(context.Background(), p.PayoutNo, VerdictCredited, "重复点了一次")
	require.ErrorIs(t, err, errAdjudicateNotHeld)
	require.NoError(t, model.LOG_DB.Where("request_id = ?", order.OrderNo).Find(&logs).Error)
	assert.Len(t, logs, 1, "重复裁决写出了第二行账本 = 用户以为被发了两次")
	require.NoError(t, gdb.Where("id = ?", act.Id).Take(&reloaded).Error)
	assert.EqualValues(t, p.AmountQuota, reloaded.PayoutQuota, "合计被重复累加")
}

// 结论「确实没发放」:换代次重排,让钱真的出手。
//
// 代次一变幂等键就变,下一轮 DrivePayouts 才可能对主库再加一次钱 —— 沿用同一个
// 键的"重排"是纯粹的空转(Execute 会幂等命中那张 Failed 单直接返回)。
// 资金单本身**保持 failed**:那是事实,账本 append-only,不改写历史行。
func TestAdjudicatePayout_NotCreditedBumpsEpochAndRequeues(t *testing.T) {
	gdb := newAdjudicateEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, func(a *Activity) { a.Status = StatusFinished })
	p, order := seedHeldPayoutWithOrder(t, gdb, act.Id, qymodel.StatusFailed, true)

	after, err := AdjudicatePayout(context.Background(), p.PayoutNo,
		VerdictNotCredited, "核对主库 logs 与 users.quota,这笔 500 确实没到账")
	require.NoError(t, err)

	assert.Equal(t, PayoutPlanned, after.Status)
	assert.Equal(t, 1, after.Epoch, "不换代次的话重排只会再撞同一张死单")
	assert.Equal(t, 0, after.Attempts)
	assert.EqualValues(t, 0, after.NextAttemptAt)
	assert.NotEqual(t, payoutIdemKey(p), payoutIdemKey(after), "幂等键必须真的变了")

	var got qymodel.FundOrder
	require.NoError(t, gdb.Where("order_no = ?", order.OrderNo).Take(&got).Error)
	assert.Equal(t, qymodel.StatusFailed, got.Status, "历史资金单不许被改写")

	// 这一支不写账本:钱还没出去,写了就是凭空造一条到账记录。
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("request_id = ?", order.OrderNo).Find(&logs).Error)
	assert.Empty(t, logs)

	// 合计也不许动:活动的 payout_quota 只统计真的 paid 出去的钱。
	var reloaded Activity
	require.NoError(t, gdb.Where("id = ?", act.Id).Take(&reloaded).Error)
	assert.EqualValues(t, 0, reloaded.PayoutQuota)
}

// 这一整件事的目的:落账之后那一场**真的删得掉**。
//
// 删除闸门②要求全部出款落到 paid/granted,而 held 恰恰不在其中;闸门⑤要求没有
// 未解决的对账异常,而转人工时 holdPayout 落过一条 payout_stuck。所以这里逐段
// 断言:落账之前被②挡住;落账之后②放行、只剩⑤;把那条异常关掉之后整场可删。
//
// 「关掉对账异常」刻意不由裁决顺手做:一条 flag 按 (act_id, code) 去重,同一场
// 活动可能还有别的卡单挂在同一条上,自动关掉等于替运营把别人的红点也灭了。
func TestAdjudicatePayout_UnblocksActivityDeletion(t *testing.T) {
	gdb := newAdjudicateEnv(t, config.Lottery{Enabled: true, PayoutMaxAttempts: 8})
	act := seedActivity(t, gdb, func(a *Activity) { a.Status = StatusFinished })
	p, _ := seedHeldPayoutWithOrder(t, gdb, act.Id, qymodel.StatusFailed, true)
	// 转人工那一刻真的落过一条对账异常,夹具必须复刻它,否则这条用例证明的是
	// 一个生产上不存在的更宽松的现场。
	require.True(t, raiseFlag(context.Background(), act.Id, FlagPayoutStuck,
		p.PayoutNo+": 资金单被判失败但主库是否已生效无法排除,需人工核对"))

	ctx := context.Background()
	require.ErrorIs(t, checkActivityDeletable(ctx, gdb, act), errDeleteFundsOpen,
		"落账之前必须被资金闸门挡住 —— 那正是这一场此前删不掉的原因")

	_, err := AdjudicatePayout(ctx, p.PayoutNo, VerdictCredited, "核对主库确认已发放")
	require.NoError(t, err)

	require.ErrorIs(t, checkActivityDeletable(ctx, gdb, act), errDeleteFlagOpen,
		"资金闸门应当已经放行,此刻只该剩下那条对账异常")

	require.NoError(t, gdb.Model(&Flag{}).
		Where("act_id = ? AND code = ?", act.Id, FlagPayoutStuck).
		Update("resolved", true).Error)
	require.NoError(t, checkActivityDeletable(ctx, gdb, act),
		"落账 + 关掉对账异常之后,这一场必须能被正常删除")
}

// credited 落账之后的**回读闸门**必须被覆盖。
//
// 资金单被改判成 success 之后,出款行推进 paid 靠的是注册进 twophase 的那份
// 补偿回调(InstallResolvers)。回调没注册、或者接的是一份陈旧实现时,
// twophase.resolveApplied 里那句 `if r, ok := resolverRegistry[order.Kind]; ok`
// 会**直接跳过**、不报错,资金单照样落 success —— 于是单据变了、业务侧永远停在
// 中间态。此刻对管理员宣布"落账完成"会让他关掉页面,而那一场活动仍然过不了
// 删除闸门②(要求全部出款 paid/granted),钱继续挂在「冻结中」。
//
// 这道闸门此前**零测试覆盖**:把 `if after.Status != PayoutPaid` 改成
// `if false`,整包 go test 全绿(实测)。它同时也是并发那一路里唯一还在响的
// 东西(markPayoutPaid 在 RowsAffected == 0 时返回 nil,不报错)。
func TestAdjudicatePayout_CreditedRefusesToClaimSuccessWhenBusinessSideDidNotSettle(t *testing.T) {
	gdb := newAdjudicateEnv(t, config.Lottery{Enabled: true})

	// 把 payout 的 resolver 换成一个**什么都不做**的实现,模拟"回调没接上 /
	// 接的是陈旧实现"。这不是手写一份判定,而是把生产接线摘掉一根线。
	twophase.RegisterResolver(qymodel.KindLotteryPayout,
		func(context.Context, *qymodel.FundOrder) error { return nil })
	t.Cleanup(InstallResolvers)

	act := seedActivity(t, gdb, func(a *Activity) { a.Status = StatusFinished })
	p, order := seedHeldPayoutWithOrder(t, gdb, act.Id, qymodel.StatusFailed, true)

	after, err := AdjudicatePayout(context.Background(), p.PayoutNo, VerdictCredited, "已在主库核对过,确认到账")

	require.Error(t, err,
		"业务侧没落定就绝不能报成功 —— 管理员会照着这句话关掉页面,"+
			"而那一场活动仍然删不掉,钱还挂在冻结中")
	assert.ErrorIs(t, err, errNotSettled)
	assert.Nil(t, after)

	// 资金单确实已经被改判成功(所以这不是"整件事都没发生"),
	// 而出款行仍停在 held —— 正是那道闸门要报出来的中间态。
	var gotOrder qymodel.FundOrder
	require.NoError(t, gdb.Where("order_no = ?", order.OrderNo).Take(&gotOrder).Error)
	assert.Equal(t, qymodel.StatusSuccess, gotOrder.Status,
		"前置条件:资金单这一侧确实落定了,闸门守的正是业务侧没跟上的那一半")

	var gotPayout Payout
	require.NoError(t, gdb.Where("payout_no = ?", p.PayoutNo).Take(&gotPayout).Error)
	assert.Equal(t, PayoutHeld, gotPayout.Status,
		"回调没接上时出款行推不到 paid,这就是闸门要报出来的中间态")
}

// 操作人判据的两个失败方向必须给出**两句不同的话**。
//
// 这条路由挂着 RootActionGate,操作人恒为 role=100,而 guard.ManageableTarget
// 对 root 恒返回 true —— 所以 ErrTargetNotLower 在本端点上永远走不到,实际发生的
// 只可能是 ErrTargetMissing(硬删用户不级联清理扩展库里的出款行)。
// 把两者合并成一句"不能给同级或更高权限账号名下的出款落账",运营会照着它去找
// 一个并不存在的"更高权限账号",而真正该做的是先确认这个账号的去向。
func TestAdjudicatePayout_MissingTargetSaysSoInsteadOfBlamingPrivilege(t *testing.T) {
	assert.NotEqual(t, errAdjudicateTargetMissing.Code, errAdjudicateTargetHigher.Code,
		"两个方向必须有各自的错误码,否则前端也分不开")
	assert.NotContains(t, errAdjudicateTargetMissing.Msg, "权限",
		"目标账号查不到时不许把原因说成权限等级 —— 那会把人引向一次查不到结果的排查")
	assert.Contains(t, errAdjudicateTargetMissing.Msg, "查不到")
	assert.Contains(t, errAdjudicateTargetHigher.Msg, "权限")
}
