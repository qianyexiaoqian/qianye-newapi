package subscription

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 名额闸门的口径由项目方钉死:占用 = 持有 status='active' 订阅的**去重人数**,
// 0 或未配置 = 不限。这张表逐条覆盖那个口径的边界。
//
// 用例都跑真库,而且断言的是 gateSeat 的返回值而不是内部计数:把
// `Distinct("user_id")` 换成普通 Count、把 `status = 'active'` 换成不带状态的
// 全量统计,都必须让其中至少一行变红。
func TestGateSeatBoundaries(t *testing.T) {
	const planId = 7

	cases := []struct {
		name string
		// capacity < 0 表示"扩展库里根本没有这一行"。
		capacity int
		// subs 是预置的订阅:userId → status。
		subs      []subSeed
		buyerId   int
		wantBlock bool
		why       string
	}{
		{
			name: "未配置名额即不限", capacity: -1,
			subs:      []subSeed{{1, "active"}, {2, "active"}, {3, "active"}},
			buyerId:   9,
			wantBlock: false,
			why:       "没有配置行时必须与接入本闸门之前逐字一致",
		},
		{
			name: "配置为 0 即不限", capacity: 0,
			subs:      []subSeed{{1, "active"}, {2, "active"}},
			buyerId:   9,
			wantBlock: false,
			why:       "0 与未配置等价,是取消限量的正常表达",
		},
		{
			name: "还差一个位置时放行", capacity: 2,
			subs:      []subSeed{{1, "active"}},
			buyerId:   9,
			wantBlock: false,
		},
		{
			name: "恰好满员时拒绝", capacity: 2,
			subs:      []subSeed{{1, "active"}, {2, "active"}},
			buyerId:   9,
			wantBlock: true,
			why:       "used >= capacity 就该拒绝,不能等到超出才拦",
		},
		{
			name: "已经超员时继续拒绝", capacity: 2,
			subs:      []subSeed{{1, "active"}, {2, "active"}, {3, "active"}},
			buyerId:   9,
			wantBlock: true,
			why:       "并发溢出之后必须继续拒绝,否则溢出会持续扩大",
		},
		{
			name: "同一个人的两条 active 只占一个名额", capacity: 2,
			subs:      []subSeed{{1, "active"}, {1, "active"}},
			buyerId:   9,
			wantBlock: false,
			why:       "口径是去重人数;按行数算的话这里会误判为满员",
		},
		{
			name: "expired 与 cancelled 不占名额", capacity: 1,
			subs:      []subSeed{{1, "expired"}, {2, "cancelled"}},
			buyerId:   9,
			wantBlock: false,
			why:       "到期或取消后名额必须自动回收",
		},
		{
			name: "满员时老用户续订仍然放行", capacity: 1,
			subs:      []subSeed{{5, "active"}},
			buyerId:   5,
			wantBlock: false,
			why:       "他本来就占着这个位置,续订不消耗新名额;拦掉的话限量套餐会变成到期即出局",
		},
		{
			name: "满员时曾经到期的用户不再享有优待", capacity: 1,
			subs:      []subSeed{{5, "expired"}, {6, "active"}},
			buyerId:   5,
			wantBlock: true,
			why:       "expired 已经把名额还回去了,再买就要重新排队",
		},
		{
			name: "别的套餐占满不影响本套餐", capacity: 1,
			subs:      []subSeed{{1, "active-other"}},
			buyerId:   9,
			wantBlock: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext := newExtDB(t)
			main := newMainDB(t)
			plan := seedPlan(t, main, planId, "限量套餐")
			seedPlan(t, main, planId+1, "另一个套餐")
			for _, s := range tc.subs {
				if s.status == "active-other" {
					seedSubscription(t, main, s.userId, planId+1, "active")
					continue
				}
				seedSubscription(t, main, s.userId, planId, s.status)
			}
			if tc.capacity >= 0 {
				putSeat(t, ext, planId, tc.capacity)
			}

			err := gateSeat(main, plan, tc.buyerId, "balance", nil)
			if tc.wantBlock {
				require.Error(t, err, tc.why)
				assert.Contains(t, err.Error(), "名额已满")
				return
			}
			assert.NoError(t, err, tc.why)
		})
	}
}

type subSeed struct {
	userId int
	status string
}

// 闸门的两问必须同口径:第一问("这个人本来就在里面吗")放行的人,必须是
// 第二问(activeHolders)数得到的人。
//
// 这条测试盯的是两问分家时出现的**绕过路径**,而不是名额统计本身。曾经第一问
// 只看 status='active':一条 end_time 已过、status 仍是 active 的僵尸行
// (没有 master 节点的部署里清扫任务压根不跑,这种行永久存在)从来不被
// activeHolders 数到,却能让它的持有人每一次购买都在第一问就 return —— 名额
// 判定被完整跳过。溢出量等于历史到期人数,没有上界,也不会自愈。
//
// 判据:把第一问的 `end_time > ?` 去掉,这里必须变红。
func TestLapsedHolderCannotBypassTheSeatLimit(t *testing.T) {
	const planId = 7
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, planId, "限量套餐")
	putSeat(t, ext, planId, 1)

	// 老用户 100 的订阅已经到期,但清扫任务没跑到,行还停在 active。
	seedLapsedSubscription(t, main, 100, planId)
	// 名额因此是空的,新用户 200 买走了唯一的那一个。
	require.NoError(t, gateSeat(main, plan, 200, "balance", nil),
		"前置条件:僵尸行不占名额,新用户买得进来")
	seedSubscription(t, main, 200, planId, "active")

	// 老用户回来续订。他此刻并不在里面(那条订阅上游已经判定不可用),
	// 必须和其他人一样受名额约束。
	err := gateSeat(main, plan, 100, "balance", nil)

	require.Error(t, err, "僵尸行的持有人不能凭它跳过名额判定,否则 capacity=1 的套餐会有 2 个真实在用人")
	assert.Contains(t, err.Error(), "名额已满")

	// 反面:真正未到期的持有人续订照常放行,名额没有变成"到期即出局"。
	assert.NoError(t, gateSeat(main, plan, 200, "balance", nil),
		"还在有效期内的人续订不消耗新名额")
}

// 入参 err 非 nil 时闸门必须原样透传,并且一条语句都不发。
//
// 这条契约不是洁癖:调用点是 `err = QyGateSubscriptionSeat(tx, ..., err)`,
// 上游那些错误(套餐不存在、订阅周期算不出来)语义上先于名额判定。吞掉或替换
// 它们会把"套餐不存在"变成"名额已满",报错直接指错方向。
func TestGateSeatPassesThroughIncomingError(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 3, "限量套餐")
	putSeat(t, ext, 3, 1)
	seedSubscription(t, main, 1, 3, "active") // 已满员,若真去判定必然拒绝

	upstream := errors.New("套餐不存在")
	got := gateSeat(main, plan, 42, "balance", upstream)
	require.ErrorIs(t, got, upstream, "上游错误必须原封不动地返回")

	var queried atomic.Bool
	main.Callback().Query().After("gorm:query").Register("t:probe", func(*gorm.DB) { queried.Store(true) })
	_ = gateSeat(main, plan, 42, "balance", upstream)
	assert.False(t, queried.Load(), "带着上游错误进来时不该再查库")
}

// 扩展库不可用时必须放行。
//
// 这条是资金安全线而不是可用性偏好:闸门也跑在支付回调
// (CompleteSubscriptionOrder)的事务里,那时用户的钱已经付掉了。fail-closed
// 会让扩展库的一次抖动把一批已付款订单永久卡在 pending —— 钱收了、订阅发不出。
func TestGateSeatFailsOpenWhenExtensionDBIsDown(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 3, "限量套餐")
	putSeat(t, ext, 3, 1)
	seedSubscription(t, main, 1, 3, "active")

	require.Error(t, gateSeat(main, plan, 42, "balance", nil), "前置条件:配置读得到时应当拒绝")

	resetCache()
	qyDBHealthy.Store(false) // 熔断打开 → guard.Available() 为 false
	assert.NoError(t, gateSeat(main, plan, 42, "balance", nil),
		"扩展库不可用时必须放行,否则已付款的支付回调会被打挂")
	qyDBHealthy.Store(true)
}

// 支付回调(source="order")即便判定出满员也必须放行。
//
// 这是本闸门最反直觉的一条,也是唯一一条"判定正确却必须不执行"的规则:
// 满员看起来正是该拒绝的时候,但 CompleteSubscriptionOrder 是在把订单写成
// success **之前**创建订阅的,这里返回错误会把整个事务回滚 —— 钱已经收了、
// 订阅发不出、订单永久停在 pending,网关每次重试都撞同一条死路,没有退款路径。
//
// 用例刻意与"余额购买同样满员时必须拒绝"摆在一起:两者只差一个 source,
// 把这个分支删掉的话,下面那半会绿、上面这半必红。
func TestGateSeatNeverBlocksAlreadyPaidOrders(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 3, "限量套餐")
	putSeat(t, ext, 3, 1)
	seedSubscription(t, main, 1, 3, "active") // 名额已满

	assert.NoError(t, gateSeat(main, plan, 42, "order", nil),
		"钱已经付了,这里拒绝会把订单永久卡在 pending —— 比多卖一个名额严重得多")
	assert.Error(t, gateSeat(main, plan, 42, "balance", nil),
		"钱还没出的路径必须照常拒绝,否则名额形同虚设")
	assert.Error(t, gateSeat(main, plan, 42, "admin", nil))
}

// 下单前的预检模式:没有事务句柄,回落到主库,判定口径与强一致模式完全一致。
//
// 这条模式承担着"别让用户付完钱才被告知没名额"的全部责任 —— 因为回调那一档
// 已经无条件放行了(见上一条)。它跑在四个支付网关的 handler 里,tx 为 nil。
func TestGateSeatPrecheckModeWithoutTransaction(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 3, "限量套餐")
	putSeat(t, ext, 3, 1)

	require.NoError(t, gateSeat(nil, plan, 42, sourcePrecheck, nil), "还有位置时必须放行")

	seedSubscription(t, main, 1, 3, "active")
	assert.Error(t, gateSeat(nil, plan, 42, sourcePrecheck, nil),
		"预检是名额在支付链路上唯一的实际拦截点,满员必须在这里就拦住")
}

// 预检模式下,已停用的套餐一律放行,让上游自己去报"套餐未启用"。
//
// 调用点是 `plan, err := GetSubscriptionPlanById(...)` 的下一行,而上游的
// `if !plan.Enabled` 检查紧随其后。这里抢先报"名额已满"的话,一个已下架且恰好
// 卖满的套餐会让用户和客服去追一个根本不存在的名额问题,真实原因被盖住。
func TestGateSeatPrecheckDefersToUpstreamDisabledPlanError(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 3, "已下架的限量套餐")
	plan.Enabled = false
	require.NoError(t, main.Model(&model.SubscriptionPlan{}).Where("id = ?", 3).
		Update("enabled", false).Error)
	putSeat(t, ext, 3, 1)
	seedSubscription(t, main, 1, 3, "active") // 已满员

	assert.NoError(t, gateSeat(nil, plan, 42, sourcePrecheck, nil),
		"停用套餐的报错优先级高于名额,必须让上游那句「套餐未启用」跑出来")
	assert.Error(t, gateSeat(main, plan, 42, "admin", nil),
		"强一致模式不看 Enabled:管理员绑定停用套餐是上游允许的动作,名额该拦还是要拦")
}

// 闸门必须看得见**同一个事务里尚未提交**的订阅行。
//
// 这是"名额表在扩展库、订阅表在主库"这套设计的核心约束:计数必须走调用方的 tx。
// 换成 model.DB 或任何其他连接,这个用例就会红 —— 而线上的表现是同一笔购买里
// "先插后数"数不到自己,或者重试链路上的重复放行。
func TestGateSeatSeesUncommittedRowsInTheCallerTransaction(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 3, "限量套餐")
	putSeat(t, ext, 3, 1)

	err := main.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, gateSeat(tx, plan, 42, "balance", nil), "空库时应当放行")
		require.NoError(t, tx.Create(&model.UserSubscription{
			UserId: 1, PlanId: 3, Status: "active", StartTime: 1, EndTime: 1 << 40,
		}).Error)
		// 同一事务内,名额已经被这条未提交的行占掉了。
		assert.Error(t, gateSeat(tx, plan, 42, "balance", nil),
			"闸门必须用调用方的 tx 计数,否则看不见同事务内未提交的写入")
		return nil
	})
	require.NoError(t, err)
}

// 闸门装到上游唯一收口点之后,钱还没出的那两条创建路径必须被它挡住。
//
// 这条用例刻意走**模块自己的 InstallHooks()** 而不是在测试里手工把
// model.QyGateSubscriptionSeat 赋成 gateSeat:后者只能证明"闸门函数写对了",
// 证明不了生产环境的接线。InstallHooks 里那行赋值一旦被删或写错,进程起来后
// hook 保持上游的恒等默认实现,限量套餐彻底失效 —— 而它没有任何调用者依赖它,
// 是重构里最容易被当成死代码清掉的一行。手工赋值的写法会让这种缺陷全绿通过。
//
// 同时它也保护上游那个调用点:model/subscription.go 里的一行在合并时被冲掉,
// 这里同样变红。
func TestUpstreamCreatePathIsGatedBySeatLimit(t *testing.T) {
	ext := newExtDB(t)
	main := newMainDB(t)
	plan := seedPlan(t, main, 11, "限量套餐")
	putSeat(t, ext, 11, 1)
	seedSubscription(t, main, 1, 11, "active")

	prev := model.QyGateSubscriptionSeat
	t.Cleanup(func() { model.QyGateSubscriptionSeat = prev })
	Mod{}.InstallHooks()

	err := main.Transaction(func(tx *gorm.DB) error {
		_, err := model.CreateUserSubscriptionFromPlanTx(tx, 42, plan, model.PaymentMethodBalance)
		return err
	})
	require.Error(t, err, "名额已满时上游的订阅创建必须失败")
	assert.Contains(t, err.Error(), "名额已满")

	var rows int64
	require.NoError(t, main.Model(&model.UserSubscription{}).
		Where("plan_id = ?", 11).Count(&rows).Error)
	assert.EqualValues(t, 1, rows, "被拒绝的那一笔不允许留下任何订阅行")

	// 同一条上游路径,换成支付回调就必须放行并真的落库 —— 钱已经付了。
	require.NoError(t, main.Transaction(func(tx *gorm.DB) error {
		_, err := model.CreateUserSubscriptionFromPlanTx(tx, 43, plan, "order")
		return err
	}), "已付款的回调不许被名额闸门打回,否则订单永久卡在 pending")
	require.NoError(t, main.Model(&model.UserSubscription{}).
		Where("plan_id = ?", 11).Count(&rows).Error)
	assert.EqualValues(t, 2, rows)
}

// 五条管理端路由的**路径与方法**必须与前端调用的字符串逐字一致。
//
// 这两侧此前没有任何测试:后端把路由改成别的字符串、或前端拼错一个词,
// 表现都是 404,而 qy 前端客户端会把「没有 code 的 404」一律归类成"扩展未启用"
// 并**静默隐藏入口** —— 于是运营看到的是"这个功能没有",排查方向直接指反。
//
// 断言写死完整路径而不是引用常量:引用常量的话,把常量和路由一起改掉就照样全绿,
// 而这条锁要防的正是"两侧一起漂移"。
func TestAdminRoutesMatchTheContractTheFrontendCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Mod{}.RegisterAdminRoutes(engine.Group("/api/qy/admin"))

	got := make(map[string]bool)
	for _, r := range engine.Routes() {
		got[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /api/qy/admin/subscription/plans/:plan_id/usage",
		"GET /api/qy/admin/subscription/plans/:plan_id/holders",
		"GET /api/qy/admin/subscription/plans-usage",
		"PUT /api/qy/admin/subscription/plans/:plan_id/seat-limit",
		"POST /api/qy/admin/subscription/plans/:plan_id/delete",
	} {
		assert.True(t, got[want],
			"%s 未注册 —— 前端会收到 404,并把它当成「扩展未启用」而静默隐藏入口", want)
	}
}
