package model

// subscription_usergroup_rules_test.go —— 用户组商品的三条购买规则(项目方 2026-08-14 拍板)。
//
//	同一目标用户组 + 已有的那条是永久 → 拒绝
//	同一目标用户组 + 已有的那条有时效 → 延期,不新建
//	不同目标用户组                     → 旧的直接作废,剩余时间不折算不退款
//
// 外加第四条:管理员手动改用户组时,把"负责改组"的订阅摘出到期回退。
//
// 这里用源码断言 + 纯函数断言,不连库:这几条规则的实现散在一个带行锁的事务里,
// 要真跑一遍得建用户、建套餐、建订阅、模拟并发,而本文件真正要守住的是
// **规则本身有没有被写反或被删掉** —— 那是最容易在后续重构里悄悄丢掉的东西。
// 连库的端到端验证放在上线前的真实付费测试里做。

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func usergroupRulesSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("subscription.go")
	require.NoError(t, err)
	return string(raw)
}

// TestPurchaseRulesOnlyApplyToGroupProducts 普通套餐必须完全不受这套规则影响。
//
// 这是它能被安全加进既有购买路径的唯一理由:upgrade_group 为空就立刻返回,
// 站上所有现存的、只卖额度的套餐一个字节都不会变。
func TestPurchaseRulesOnlyApplyToGroupProducts(t *testing.T) {
	fn := extractFunc(t, usergroupRulesSource(t), "func applyUserGroupPurchaseRulesTx(")
	body := fn[:strings.Index(fn, "\n\tnow := ")]
	require.Contains(t, body, `if target == "" {`,
		"upgrade_group 为空必须立刻返回 —— 普通套餐不该被这套规则碰到")
	require.Contains(t, body, "return nil, nil")
}

// TestSameGroupPermanentIsRejected 已经永久拥有就不许再卖一次。
func TestSameGroupPermanentIsRejected(t *testing.T) {
	fn := extractFunc(t, usergroupRulesSource(t), "func applyUserGroupPurchaseRulesTx(")
	require.Contains(t, fn, "if existing.EndTime == 0 {",
		"必须先判永久(end_time = 0)")
	require.Contains(t, fn, "你已经永久拥有该用户组,无需重复购买")
}

// TestSameGroupTimedExtendsFromExistingEnd 同组续期必须从**原到期时间**往后接。
//
// 从"现在"往后接会把用户已经买过、还没用完的那一段时间白白吃掉 ——
// 那是直接的资损,而且用户几乎不可能发现(他只看到一个新的到期日)。
func TestSameGroupTimedExtendsFromExistingEnd(t *testing.T) {
	fn := extractFunc(t, usergroupRulesSource(t), "func applyUserGroupPurchaseRulesTx(")
	require.Contains(t, fn, "base := time.Unix(existing.EndTime, 0)",
		"续期的基准必须是原到期时间,不是当下")
	require.Contains(t, fn, "if existing.EndTime < now {",
		"只有已经过期的那种情况才从当下接")
}

// TestCrossGroupSupersedesWithoutRefund 跨组购买把旧的顶掉,且不触发它的回退。
//
// 不触发回退是刻意的:回退会把用户的组降回旧的 prev_user_group,而紧接着新订阅
// 又要把组改成 target —— 中间那一瞬用户处在一个谁都没配过的状态,而且两次写
// users.group 会在审计里留下一条看不懂的往返。
func TestCrossGroupSupersedesWithoutRefund(t *testing.T) {
	fn := extractFunc(t, usergroupRulesSource(t), "func applyUserGroupPurchaseRulesTx(")
	require.Contains(t, fn, "SubscriptionStatusSuperseded",
		"跨组必须把旧订阅标成 superseded")
	require.NotContains(t, fn, "downgradeUserGroupForSubscriptionTx",
		"顶替时不得触发到期回退 —— 那会让用户的组在一瞬间往返一次")
	require.Equal(t, "superseded", SubscriptionStatusSuperseded,
		"状态值一旦改动,存量行与新代码就对不上了")
}

// TestSupersededIsDistinctFromExpired 顶替与到期必须是两种状态。
//
// 客服要能回答「我上个月买的 VIP 去哪了」,而"时间到了"与"你自己买了别的组、
// 剩余时间作废"的答案完全不同。合并成一种就永远解释不清。
func TestSupersededIsDistinctFromExpired(t *testing.T) {
	require.NotEqual(t, "expired", SubscriptionStatusSuperseded)
	require.NotEqual(t, "active", SubscriptionStatusSuperseded)
}

// TestDetachClearsAllThreeGroupSnapshots 管理员改组时必须把三个快照一起清空。
//
// 只清 downgrade_group 是不够的:downgradeUserGroupForSubscriptionTx 的判据是
// 「downgrade_group 与 upgrade_group **都**空才什么都不做」,留着 upgrade_group
// 那条订阅仍然会参与回退,把管理员刚设的组改掉。
func TestDetachClearsAllThreeGroupSnapshots(t *testing.T) {
	fn := extractFunc(t, usergroupRulesSource(t), "func DetachUserGroupSubscriptionsTx(")
	for _, col := range []string{"upgrade_group", "downgrade_group", "prev_user_group"} {
		require.Contains(t, fn, `"`+col+`":`, "摘除时必须清空 %s", col)
	}
	require.NotContains(t, fn, `"status":`,
		"不得改动订阅状态 —— 它可能还带着用户花钱买的余额,不能一起作废")
	require.NotContains(t, fn, `"end_time":`,
		"不得改动到期时间 —— 订阅本身还活着,只是不再负责改组")
}

// TestPreviewWarnsAboutIrreversibleSupersede 跨组的不可逆后果必须在下单前说清楚。
func TestPreviewWarnsAboutIrreversibleSupersede(t *testing.T) {
	fn := extractFunc(t, usergroupRulesSource(t), "func PreviewUserGroupPurchase(")
	require.Contains(t, fn, "剩余时间直接作废、不折算也不退款,且不可撤销",
		"预览必须把不可逆的后果原话写出来")
	require.Contains(t, fn, `out.Action = "reject"`)
	require.Contains(t, fn, `out.Action = "extend"`)
	require.Contains(t, fn, `out.Action = "supersede"`)
	require.NotContains(t, fn, "lockForUpdate",
		"预览是只读的,绝不能加锁 —— 用户刷一下商品页就锁住自己所有订阅行")
}
