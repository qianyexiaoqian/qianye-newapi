package invite

import (
	"context"
	"sync"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/shopspring/decimal"
)

// export.go —— 给星屑与推广佣金两个模块用的导出面。
//
// 依赖方向只有一个:stardust / commission → invite。本包**不** import 它们(否则 Go 编译期成环,
// 由 qianye/module_import_guard_test.go 钉住)。凡是星屑需要而又绑死在本包内部状态上
// 的东西(邀请人解析的缓存、singleflight、跨节点失效、日界),都从这里以函数的形式
// 导出;反过来本包需要它们回答的(按下线汇总的星屑与佣金、下线日消费计佣),以 func 变量的形式
// 留槽,由各自的 InstallHooks 赋值。

// AfterRedeemSuccessCommission 是余额兑换码兑换成功之后给推广佣金(modules/commission)
// 的转发槽。model.QyOnRedeemSuccess 是单槽变量,由本包独占(hook.go installHooks);
// 佣金要吃同一个事件,又不能让本包 import 它,于是在 onRedeemSuccess 里无条件调用本变量。
//
// 星屑侧曾经有一个并列的 AfterRedeemSuccess(兑换码返),已随「下线返只走佣金」下线。
// 由 commission.InstallHooks 赋值;依赖方向仍是 commission → invite。
var AfterRedeemSuccessCommission = func(userId int, redemptionId int, quota int) {}

// AfterSubscriptionGranted / AfterSubscriptionGrantedCommission 是套餐授予事件
// (model.QyOnSubscriptionGranted,四条购买路径的事务提交之后)的两个转发槽。
// 那个单槽变量由本包独占(hook.go installHooks),再无条件转发给两边:
//   - 星屑(modules/stardust):买家本人的套餐返;
//   - 推广佣金(modules/commission):上线按「在线 / 余额 / 兑换码」三档计的套餐佣金。
//
// 两边各自判自己的开关,依赖方向仍是 stardust / commission → invite。
var (
	AfterSubscriptionGranted           = func(g model.QySubscriptionGrant) {}
	AfterSubscriptionGrantedCommission = func(g model.QySubscriptionGrant) {}
)

// UserGroupOf 回答一个账号**自己**的分组(users.group),给星辉佣金按上线分组取档用。
//
// missing 为真表示主库里根本没有这一行(被删 / 被软删):它与"分组是空串"必须
// 在结构上分得开 —— 后者会被 groupname.Effective 折成 default 组,前者不该按任何
// 分组档发放。走的是与 InviteeEligible 同一份缓存,同一个人只回一次主库。
// 只能在后台 worker 或 HTTP 处理器里调用(会回主库)。
func UserGroupOf(ctx context.Context, userId int) (group string, missing bool, err error) {
	e, _, err := resolveInviter(ctx, userId)
	if err != nil {
		return "", false, err
	}
	return e.Group, e.Missing, nil
}

// KnownWithoutInviter 只查进程内缓存、绝不回源:这个人是否**已知**没有上线。
//
// 它是 relay 结算线程上唯一允许的邀请判定形式。绝大多数用户没有邀请人,负缓存
// 命中即到此为止 —— 全程一次 map 查找,不往异步队列里投任何东西。未命中缓存时
// 返回 false(交给 worker 去回源),不是"有上线"。
func KnownWithoutInviter(userId int) bool {
	e, ok := peekInviter(userId)
	return ok && e.InviterId == 0
}

// WarnUnknownInviter 是限频的"解析邀请关系失败"告警,给同样在 worker 里回源的
// 星辉佣金路径复用 —— 两个模块各打各的会把同一次主库故障喊成两倍音量。
func WarnUnknownInviter(userId int, err error) { warnUnknownInviter(userId, err) }

// InvalidateInviter 失效某个账号的邀请关系缓存(userId <= 0 = 全清)并广播。
// 与管理端 /admin/invite/cache/invalidate 同一个动作,给佣金侧的测试夹具与
// 需要"立刻按主库重读"的路径用。
func InvalidateInviter(userId int) { invalidateInviter(userId) }

// InviterCacheStats / CacheSyncStats 把邀请人缓存与跨节点失效通道的健康数给
// 佣金管理端的健康面板 —— 计佣按缓存里的关系与分组冻结费率,这两组数决定
// "这段时间的佣金要不要复核"。
func InviterCacheStats() map[string]any { return inviterCacheStats() }
func CacheSyncStats() map[string]any    { return cacheSyncStats() }

// PairRewardTotals 由星屑注入:按 (邀请人, 下线) 这一对汇总邀请人因该下线在**星屑账本**上
// 直接得到的星屑(注册奖 / 套餐返上线,以及已下线的星屑侧下线返留下的历史流水)。
// 佣金那一半由 PairCommissionTotals 回答,两者相加才是"这条关系一共带来多少"。
// 管理端关系列表与换绑/解绑的回显都靠它们回答
// "这条关系一共给过谁多少"。默认实现返回空表 —— 星屑没装上时列表照常出,只是
// 那一列全是 0。
var PairRewardTotals = func(ctx context.Context, pairs [][2]int) map[[2]int]int64 {
	return map[[2]int]int64{}
}

// PairCommission 是推广佣金按 (邀请人, 下线) 这一对的汇总。作废(voided)的行不计。
type PairCommission struct {
	// ConsumeBaseQuota 是下线消费的计佣基数(额度),只计 consume 来源。
	ConsumeBaseQuota int64
	// LastDay 是最近一次消费计佣的桶日 YYYYMMDD,空串 = 还没有过消费计佣。
	LastDay string
	// Gross 是全部来源(充值 / 兑换码 / 消费,含冲正)的计佣毛额,星屑,全精度。
	// **含尚未结算进星屑余额的部分**:它回答"这个下线一共给上线带来多少佣金",
	// 不是"已经到账多少"(到账由 commission_credit 流水回答)。
	Gross decimal.Decimal
}

// PairCommissionTotals 由推广佣金注入:按 (邀请人, 下线) 汇总佣金。下线带来的返还
// 只走佣金这一条线,所以用户端「我的下线」与管理端关系列表的"这个下线带来多少"
// 都以它为准,再加上星屑侧与佣金基数不重叠的注册奖 / 套餐返(PairRewardTotals)。
// 默认实现返回空表 —— 佣金没装上时那一列全是 0。读失败同样返回空表:调用方只是在
// 渲染一个回显数字,不该为它失败。
var PairCommissionTotals = func(ctx context.Context, pairs [][2]int) map[[2]int]PairCommission {
	return map[[2]int]PairCommission{}
}

// DayAccrual 是某个下线在某一段日子里的消费计佣汇总:基数与毛额(星屑,全精度)。
type DayAccrual struct {
	BaseQuota int64
	Gross     decimal.Decimal
}

// InviteAccrualByInvitee 由推广佣金注入:[startDay, endDay](YYYYMMDD,含首尾)内
// 每个下线的消费计佣汇总(作废行不计);inviterId > 0 时只看这个邀请人名下的。
// 下线日消费报表拿它算"消费了多少、其中多少计了佣金"。下线带来的返还只走佣金。
var InviteAccrualByInvitee = func(ctx context.Context, startDay, endDay string, inviterId int) (map[int]DayAccrual, error) {
	return map[int]DayAccrual{}, nil
}

// InviteAccrualByDay 由推广佣金注入:一个下线在 [startDay, endDay] 内逐日的消费计佣汇总,
// 按 YYYYMMDD 索引。按天下钻用。
var InviteAccrualByDay = func(ctx context.Context, inviteeId int, startDay, endDay string) (map[string]DayAccrual, error) {
	return map[string]DayAccrual{}, nil
}

// InviteMatch 是"这个下线现在归谁、能不能给他上线返东西"的答案。
type InviteMatch struct {
	// InviterId 是主库 users.inviter_id 解析出的上线;0 = 没有上线。
	InviterId int
	// InviterGroup 是**上线自己**的账号分组(D-02:比例按上线分组解析)。
	InviterGroup string
	// InviteeCreated 是下线的注册时刻(users.created_at),给绑定成熟期一类的
	// 风控判据用(星辉佣金的 min_invitee_age_hours)。
	InviteeCreated int64
	// Blocked 表示这对关系被管理端拉黑或被互邀环路自动 blocked,任何一侧都不该返。
	Blocked bool
}

// Eligible 表示可以返:有上线、不是自邀、没被拉黑。
func (m InviteMatch) Eligible() bool { return m.InviterId > 0 && !m.Blocked }

// InviteeEligible 解析一个下线的邀请关系:
// resolve → 自邀拒绝 → 首次回源时补建关系快照(保留互邀环路的自动拉黑)→ 拉黑判定。
//
// invite.enabled=false 时一律不合格且不建快照:关掉邀请功能的含义就是
// "不建关系、不发任何邀请返"。只能在后台 worker 里调用(会回主库)。
func InviteeEligible(ctx context.Context, inviteeId int) (InviteMatch, error) {
	if !config.Get().Invite.Enabled {
		return InviteMatch{}, nil
	}
	e, fromSource, err := resolveInviter(ctx, inviteeId)
	if err != nil {
		return InviteMatch{}, err
	}
	if e.InviterId == 0 || e.InviterId == inviteeId {
		return InviteMatch{}, nil
	}
	if fromSource {
		ensureRelation(ctx, e.InviterId, inviteeId, e.InviteeName, e.InviteeCreated)
	}
	m := InviteMatch{InviterId: e.InviterId, InviteeCreated: e.InviteeCreated}
	if blockedInvitees(ctx)[inviteeId] {
		m.Blocked = true
		return m, nil
	}
	inviter, _, err := resolveInviter(ctx, e.InviterId)
	if err != nil {
		return m, err
	}
	m.InviterGroup = inviter.Group
	return m, nil
}

var sharedInfraOnce sync.Once

// StartSharedInfra 启动被星屑复用的两样设施:跨节点失效通道(cachesync)与 logs 覆盖索引
// 的后台补建。星屑开着、邀请关着的部署里索引与通道同样要起 —— 前者是日结聚合的地基,
// 后者让换分组 / 换绑在其它节点上即时生效。
//
// 幂等:startCacheSync 自带 sync.Once;startLogsIndexMaintenance 没有,这里包一层,
// 因为 lease.Run 不按名字去重,同进程调两次会多一个探测协程与一个 ticker。
func StartSharedInfra() {
	sharedInfraOnce.Do(func() {
		startCacheSync()
		startLogsIndexMaintenance()
	})
}

// SharedInfraWanted 回答"这台机器上有没有人需要共享设施":邀请或星屑任一开着。
func SharedInfraWanted() bool {
	c := config.Get()
	return c.Invite.Enabled || c.Stardust.Enabled
}

// CacheSyncOn 供守卫测试与健康面板读取:跨节点失效通道是否已经启动。
func CacheSyncOn() bool { return cacheSyncOn.Load() }
