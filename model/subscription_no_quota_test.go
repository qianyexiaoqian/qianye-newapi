package model

// subscription_no_quota_test.go —— 纯商品(只改用户组、不带余额)绝不参与出资。
//
// ═══════════════════════ 它防的是一个资损口子 ═══════════════════════
//
// PreConsumeUserSubscription 里判余额的那一句是 `if sub.AmountTotal > 0`,
// 也就是 **AmountTotal == 0 的语义是「不限额度」**,直接跳过检查。
//
// 于是如果拿 TotalAmount = 0 来表达「这个商品没有余额」,每一个买家换来的
// 不是"没有余额",而是一份**无限订阅余额** —— 他的每一次请求都会优先从这条
// 永远花不完的订阅里出资,钱包一分不扣。
//
// 所以「纯商品」必须是独立字段 NoQuota,而且必须在**出资查询的 SQL 里**就排除,
// 不能等取出来再 continue(那条查询带 FOR UPDATE,把纯商品行一起锁进来还会让
// 改用户组的商品和扣钱互相排队)。

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPureProductIsExcludedFromFundingQuery 钉住出资查询里那条 no_quota 过滤。
//
// 用源码断言而不是跑一次真实预扣:预扣要连库、要建用户、要造请求上下文,
// 而这里要守的其实是一行 WHERE —— 它一旦被删掉,任何"纯商品"套餐都会立刻
// 变成无限余额,而且不会有任何一条现有用例变红(现有用例里没有纯商品)。
func TestPureProductIsExcludedFromFundingQuery(t *testing.T) {
	src := readSubscriptionSource(t)

	fn := extractFunc(t, src, "func PreConsumeUserSubscription(")
	require.Contains(t, fn, "no_quota = ?",
		"出资查询必须在 SQL 里排除纯商品 —— AmountTotal 为 0 的语义是「不限额度」,"+
			"漏掉这条过滤等于给每个买家一份无限订阅余额")
}

// TestPureProductFlagIsSnapshotOnPurchase 纯商品标记必须在购买那一刻拍快照。
//
// 不拍快照(每次回查套餐)的后果:运营事后把套餐从"纯商品"改成"带余额",
// 已经卖出去的那批订阅会凭空长出一份余额;反过来同理。这与本表其它几个
// 快照字段(upgrade_group / downgrade_group / allow_wallet_overflow)同一口径。
func TestPureProductFlagIsSnapshotOnPurchase(t *testing.T) {
	src := readSubscriptionSource(t)
	require.Contains(t, src, "NoQuota:             plan.NoQuota,",
		"购买时必须把套餐的 NoQuota 拍进 UserSubscription")
}

// TestPureProductStoresTinyAmountNotZero 纯商品落库的额度必须是 1,不是 0。
//
// 这是第二道闸:出资查询里那条 no_quota 过滤是第一道。只留第一道的话,
// 将来任何一条绕过它的路径拿到的都是 AmountTotal=0 —— 而 0 的语义是**不限额度**,
// 那条路径会安静地给出无限余额。落成 1 之后,最坏情况也只是一个 quota 单位。
func TestPureProductStoresTinyAmountNotZero(t *testing.T) {
	pure := &SubscriptionPlan{NoQuota: true, TotalAmount: 0}
	require.Equal(t, PureProductAmountTotal, planAmountTotal(pure),
		"纯商品必须落成一个极小的正数,而不是 0(0 = 不限额度)")
	require.Positive(t, PureProductAmountTotal,
		"必须严格大于 0,否则预扣那句 `if AmountTotal > 0` 会整段跳过余额检查")

	unlimited := &SubscriptionPlan{NoQuota: false, TotalAmount: 0}
	require.Zero(t, planAmountTotal(unlimited), "不限额度的套餐仍然落 0")

	limited := &SubscriptionPlan{NoQuota: false, TotalAmount: 500}
	require.EqualValues(t, 500, planAmountTotal(limited), "有限额度原样落库")
}

// TestNoQuotaIsDistinctFromUnlimited 三态必须可区分。
//
// NoQuota=true            纯商品,不参与出资
// NoQuota=false, Total=0  不限额度
// NoQuota=false, Total>0  有限额度
//
// 少了 NoQuota 这个字段,前两者在数据上无法区分,而它们的计费行为正好相反。
func TestNoQuotaIsDistinctFromUnlimited(t *testing.T) {
	pure := &SubscriptionPlan{NoQuota: true, TotalAmount: 0}
	unlimited := &SubscriptionPlan{NoQuota: false, TotalAmount: 0}
	limited := &SubscriptionPlan{NoQuota: false, TotalAmount: 500}

	require.True(t, pure.NoQuota)
	require.False(t, unlimited.NoQuota)
	require.False(t, limited.NoQuota)
	require.Equal(t, unlimited.TotalAmount, pure.TotalAmount,
		"纯商品与不限额度在 TotalAmount 上确实无法区分 —— 这正是需要独立字段的理由")
}

func readSubscriptionSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("subscription.go")
	require.NoError(t, err)
	return string(raw)
}

// extractFunc 取出一个函数的**函数体**,到它顶格的收尾大括号为止。
//
// 切在顶格 `}` 而不是下一个 `func `:后者会把下一个函数的**文档注释**一起吃进来,
// 于是「本函数不得出现 X」这类断言,会被隔壁注释里顺口提到的 X 误判成失败 ——
// 这条测试自己先踩过一次(TestCrossGroupSupersedesWithoutRefund)。
func extractFunc(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	require.GreaterOrEqual(t, start, 0, "找不到函数 %s", signature)
	rest := src[start+len(signature):]
	if end := strings.Index(rest, "\n}\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
