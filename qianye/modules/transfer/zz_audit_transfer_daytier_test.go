package transfer

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zz_audit_transfer_daytier_test.go —— 资金审计:当日取严组合的自洽性。
//
// 本模块对「配置不自洽」有一条贯穿始终的契约:失败关闭(fail-closed),
// 返回 503(errSettingsInvalid / errGroupLimitInvalid),而不是让用户拿一份
// 谁都没批准过、自相矛盾的门槛去跑资金操作。effectiveCtx / transferFor 都遵守它:
// 两者在合并结果非法(min_quota > max_per_tx_quota 这类)时都会再跑一遍
// config.ValidateTransfer 并失败关闭。
//
// transferForSenderDay 曾是唯一一处例外:它把「当前档」与「今天那一档」逐项取严后
// **不再校验合并结果**。而逐项取严完全可能把两份各自自洽的门槛拼成一份不自洽的:
// min_quota 取两者里更大的、max_per_tx_quota 取两者里更小的,一大一小凑到一起就可能
// min_quota > max_per_tx_quota。
//
// 这不是资损(方向是把用户挡得更死),但它绕过了本模块自己的失败关闭契约:用户不会
// 拿到一个能解释的 503「门槛配置异常」,而是**每一个金额**都撞 errAmountOutOfRange
// (400)—— 一个既看不出根因、运营那边也零告警的静默死档。
//
// 修复:transferForSenderDay 在取严之后补跑一次 config.ValidateTransfer,不自洽即返回
// errGroupLimitInvalid(→503),与 transferFor / effectiveCtx 一致。本用例守住这条契约。

func daytierSettings(global config.Transfer, tiers ...GroupLimit) opSettings {
	return opSettings{Transfer: global, Tiers: buildTierSet(tiers)}
}

// TestDayTierStrictestCombineEscapesFailClosedContract 复现取严组合逃逸失败关闭。
//
//	current 档(hi): min_quota=5000, max_per_tx=0(不限)   —— 自洽
//	day     档(lo): min_quota=0(不限), max_per_tx=3000    —— 自洽
//	取严:min_quota=max(5000,0)=5000, max_per_tx=min(不限,3000)=3000 → 5000 > 3000
//
// 两档各自都能通过 ValidateTransfer,取严后的组合却过不了。而 transferForSenderDay
// 对这份组合返回 nil error,把它原样交给受理校验,于是任何金额都被 400 拒。
func TestDayTierStrictestCombineEscapesFailClosedContract(t *testing.T) {
	global := createGlobal()
	global.MinQuota = 0
	global.MaxPerTxQuota = 0 // 全站不限单笔
	global.DailyMaxQuota = 0
	global.CooldownSecs = 0
	global.NewAccountFreezeHours = 0
	require.NoError(t, config.ValidateTransfer(&global), "前置:全站门槛必须自洽")

	hi := GroupLimit{UserGroup: "hi", Enabled: true, MinQuota: i64(5000)}
	lo := GroupLimit{UserGroup: "lo", Enabled: true, MaxPerTxQuota: i64(3000)}
	s := daytierSettings(global, hi, lo)

	// 两档各自生效的门槛都必须自洽 —— 否则本用例证的就不是「取严把它拼坏了」。
	hiCfg, err := s.transferFor("hi")
	require.NoError(t, err)
	require.NoError(t, config.ValidateTransfer(&hiCfg), "hi 档单独看必须自洽")
	loCfg, err := s.transferFor("lo")
	require.NoError(t, err)
	require.NoError(t, config.ValidateTransfer(&loCfg), "lo 档单独看必须自洽")

	// 用户当前在 hi 档,但今天已经在 lo 档下转过账(DayOutGroup=lo)。
	state := &UserState{
		DayBucket:   dayBucket(common.GetTimestamp()),
		DayOutGroup: "lo",
		DayOutCount: 1,
	}
	got, err := s.transferForSenderDay("hi", state, dayBucket(common.GetTimestamp()))

	// 先把「取严确实拼出了不自洽组合」摆出来,作为下面那条契约断言的依据。
	assert.Greater(t, got.MinQuota, got.MaxPerTxQuota,
		"取严把 min_quota(5000) 拼到了 max_per_tx_quota(3000) 之上,这份组合任何金额都不合法")
	assert.Error(t, config.ValidateTransfer(&got),
		"取严后的组合过不了 ValidateTransfer —— 它就是本模块别处一律失败关闭的那种不自洽")

	// 契约断言:面对不自洽组合,transferForSenderDay 必须与 transferFor / effectiveCtx
	// 一样失败关闭(返回 error → 上层翻成 503),而不是返回 nil 让它冒充一份正常门槛。
	require.Error(t, err,
		"当日取严拼出不自洽门槛时必须失败关闭(返回 error → 503);修复前返回 nil,"+
			"用户会在每一个金额上收到 400 金额超范围,而不是可解释的 503,运营零告警")
}
