package planentitlement

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// hardening_audit_test.go —— 审计一轮里确认的四个缺陷的回归守卫。
// 每个用例断言的都是**修复后**的正确行为:对应缺陷若被改回来,这里会红。

// TestNoQuotaSubscriptionIsUnlockedButNeverFunded 钉住 HIGH-1。
//
// no_quota 纯商品套餐的 AmountTotal 恒为 0,而「0 == 不限量」这条口径会把它误判成
// 「有无限余额」→ funded=true → groupns 钱包出资闸门的 `!funded` 恒假,运营在别的
// 套餐上设的 allow_wallet_overflow=0 被架空(钱包为 0 倍率分组无限出资)。
// 上游 PreConsumeUserSubscription 的候选查询本就带 `no_quota = false`,永不用它出资。
//
// 修复:loadActivePlanIds 仍把 no_quota 计入**解锁集合**(它可能就是解锁包,不该
// 丢掉访问权),但**排除出 funded**。这里同时钉住两件事:解锁保留、出资排除。
func TestNoQuotaSubscriptionIsUnlockedButNeverFunded(t *testing.T) {
	ext := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0}`) // pro 是 0 倍率补贴分组,白嫖后果最重
	seedPlan(t, mainDB, 1, "解锁包(纯商品)")
	putGrant(t, ext, 1, "pro")

	now := common.GetTimestamp()
	require.NoError(t, mainDB.Create(&model.UserSubscription{
		Id: 40, UserId: 7, PlanId: 1, Status: statusActive,
		StartTime: now - 60, EndTime: now + 30*86400,
		AmountTotal: 0, AmountUsed: 0,
		NoQuota:             true,
		AllowWalletOverflow: false, // 运营明确不许钱包续付
	}).Error)

	resetCaches()
	require.NoError(t, reload())

	// 解锁集合仍含 plan 1(访问权保留)。
	ids, ok := activePlanIds(7)
	require.True(t, ok)
	assert.Equal(t, []int{1}, ids, "no_quota 套餐仍应算作活跃解锁套餐(不丢访问权)")

	// 但它绝不能出现在 funded 里。
	funded, ok := activeFundedPlanIds(7)
	require.True(t, ok)
	assert.NotContains(t, funded, 1, "no_quota 纯商品永不用自己的余额出资,不得被计入 funded(HIGH-1)")

	// 契约层:UnlockFundingState 报 unlocked=true、funded=false,
	// 于是钱包出资闸门会正确地去看 allow_wallet_overflow(此处为 false → 应被挡)。
	unlocked, funded2, allowOverflow := UnlockFundingState(7, "pro", false)
	assert.True(t, unlocked)
	assert.False(t, funded2, "no_quota 套餐不得让闸门误以为「还有套餐能付这一笔」(HIGH-1)")
	assert.False(t, allowOverflow, "运营在这张 no_quota 套餐上设了不许钱包续付")
}

// TestAdminCancelOrDeleteSubscriptionInvalidatesUnlockCache 钉住 HIGH-2。
//
// 管理员作废/硬删单条订阅(风控/退款路径)此前不失效 planentitlement 的 per-user
// 缓存,被作废的用户在本节点仍持有解锁的模型分组最长一个新鲜期(默认 60s,异步刷新
// 持续失败可达 300s)。修复:上游两条路径提交后调 QyOnUserSubscriptionInvalidated,
// 由本模块的 InvalidateUser 接线。生产接线在 InstallHooks;测试里手动接同一实现体。
func TestAdminCancelOrDeleteSubscriptionInvalidatesUnlockCache(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(subId int) error
	}{
		{"作废", func(id int) error { _, err := model.AdminInvalidateUserSubscription(id); return err }},
		{"硬删除", func(id int) error { _, err := model.AdminDeleteUserSubscription(id); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newExtDB(t)
			mainDB := newMainDB(t)
			setGroupRatios(t, `{"default":1,"pro":0}`)
			seedPlan(t, mainDB, 1, "套餐A")
			putGrant(t, ext, 1, "pro")

			prev := model.QyOnUserSubscriptionInvalidated
			model.QyOnUserSubscriptionInvalidated = InvalidateUser
			t.Cleanup(func() { model.QyOnUserSubscriptionInvalidated = prev })

			resetCaches()
			require.NoError(t, reload())
			sub := seedSubscription(t, mainDB, 41, 7, 1, 30*86400, 1000, 0)

			// 预热:此刻用户 7 的 per-user 缓存里解锁 pro。
			ids, ok := activePlanIds(7)
			require.True(t, ok)
			require.Equal(t, []int{1}, ids)
			_, found, _ := getUserCache().Get(7)
			require.True(t, found, "预热后缓存里应有条目")

			require.NoError(t, tc.revoke(sub.Id))

			// 修复点:缓存必须被失效。修复前这里 found 恒为 true(旧解锁残留)。
			_, found, _ = getUserCache().Get(7)
			assert.False(t, found, "作废/删除单条订阅后 per-user 缓存必须失效(HIGH-2)")

			// 端到端:重新解析已不再解锁 pro。
			ids, ok = activePlanIds(7)
			require.True(t, ok)
			assert.Empty(t, ids, "订阅已失效,不应再算作活跃解锁套餐")
		})
	}
}

// TestLongNoteDoesNotDefeatNoOpAuditShortCircuit 钉住 MEDIUM-2。
//
// note 超过 maxNoteLen 时,before.Note(从库读回的截断值)与未截断的 req.Note 永远
// 判不等 → sameEntitlement 恒为 false → 每次一模一样的保存都重新落库、写空改动审计,
// 并 InvalidateUser(0) 清空全站 per-user 缓存。修复:比较前对 next.Note 做同样截断。
func TestLongNoteDoesNotDefeatNoOpAuditShortCircuit(t *testing.T) {
	ext := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"pro":0.8}`)
	seedPlan(t, mainDB, 1, "套餐A")

	longNote := strings.Repeat("配", 200) // 600 字节,远超 maxNoteLen(255)
	require.Greater(t, len(longNote), maxNoteLen)
	body := `{"unlock_groups":["pro"],"balance_scope":"universal","note":"` + longNote + `"}`

	code, _ := putEntitlement(t, 1, body)
	require.Equal(t, http.StatusOK, code)
	code, _ = putEntitlement(t, 1, body) // 一模一样再存一次
	require.Equal(t, http.StatusOK, code)

	// 值没变 ⇒ 第二次必须短路:全程只应有第一次那一条审计。
	// 修复前:>255 的 note 让每次保存都写一条 ok 审计。
	assert.Equal(t, []string{"subscription.plan_entitlement.update:ok"}, auditActions(t, ext),
		"超长 note 的重复保存必须短路,不得每次都落库写审计(MEDIUM-2)")
}

// TestReloadDiscardsStaleInFlightSnapshot 钉住 MEDIUM-1(丢失更新)。
//
// reloadCtx 把两条 SELECT 挪进事务(消撕裂,MySQL RR / SQLite 串行)之后,
// 「读完、还没写回 current」之间仍有一个窗口:一次在途的旧回源会把管理端刚发布的
// 新快照静默盖掉。修复:InvalidateAndReload 自增代次,reloadCtx 写回前校验代次未变。
//
// 手法(全同步,不占事务、不写库,避免 SQLite 连接池死锁):用 After-query 回调在
// 一次回源读完 grants 之后、写回 current 之前,**在内存里**发布一份更新的快照并自增
// 代次(等价于「回源在途期间管理端又写了一次」)。该回源随后应发现代次已变而丢弃
// 自己那份陈旧结果,不把刚发布的新快照盖掉。
func TestReloadDiscardsStaleInFlightSnapshot(t *testing.T) {
	ext := newExtDB(t)
	mainDB := newMainDB(t)
	setGroupRatios(t, `{"default":1,"G":1,"H":1}`)
	seedPlan(t, mainDB, 1, "套餐A")
	putGrant(t, ext, 1, "G")

	resetCaches()
	require.NoError(t, reload()) // 基线 current = {G}
	require.True(t, Current().Binds(1, "G"))

	// 更新的快照(只绑定 H),代表「回源在途期间管理端发布的新版本」。
	newer := buildSnapshot([]PlanGrant{{PlanId: 1, ModelGroup: "H", UpdatedAt: 2}}, nil)

	var once sync.Once
	require.NoError(t, ext.Callback().Query().After("gorm:query").
		Register("probe:publish_newer", func(tx *gorm.DB) {
			if tx.Statement.Table != (PlanGrant{}).TableName() {
				return
			}
			// 本次回源已把 grants 读成 {G};此刻发布更新快照 + 自增代次。
			once.Do(func() {
				current.Store(newer)
				snapshotEpoch.Add(1)
			})
		}))
	t.Cleanup(func() { _ = ext.Callback().Query().Remove("probe:publish_newer") })

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	require.NoError(t, reloadCtx(ctx))

	// 这次回源基于旧 grants({G})构建,但代次已被上面那步改过 → 必须丢弃写回,
	// 不得把「更新的 {H}」退回成陈旧的 {G}。
	s := Current()
	assert.True(t, s.Binds(1, "H"), "在途旧回源不得盖掉更新的快照(MEDIUM-1 丢失更新)")
	assert.False(t, s.Binds(1, "G"), "陈旧的 {G} 结果必须被代次校验丢弃")
}
