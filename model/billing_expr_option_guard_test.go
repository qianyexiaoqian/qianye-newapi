package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/billing_setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// billing_expr_option_guard_test.go —— 阶梯计费表达式必须在**落库之前**被校验。
//
// pkg/billingexpr/expr.md 承诺保存时会编译 + 做非负冒烟，但那个冒烟函数此前
// 一个调用者都没有。UpdateOption 的顺序是「先 DB.Save，后 updateOptionMap」，
// 所以一份 `p * 3 - 20000` 会先落库，随后每一笔小请求都结算成负额度 ——
// 扣费变成给用户充值，而且重启不自愈。controller/ratio_sync.go 还会把远端站点
// 推来的表达式原样写进同一个键，使这条路可被远端触发。

func TestNegativeBillingExprIsRejectedBeforeItReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)

	require.Error(t,
		UpdateOption(billing_setting.BillingExprOptionKey, `{"m":"tier(\"promo\", p * 3 - 20000)"}`),
		"结果可为负的表达式必须被拒:TryTieredSettle 之外没有任何一层会把它变正")

	var option Option
	assert.ErrorIs(t, db.Where(&Option{Key: billing_setting.BillingExprOptionKey}).First(&option).Error,
		gorm.ErrRecordNotFound,
		"被拒的表达式绝不能已经落库 —— 落库之后重启也不自愈")
}

func TestMalformedBillingExprIsRejectedBeforeItReachesTheDatabase(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)

	require.Error(t,
		UpdateOption(billing_setting.BillingExprOptionKey, `{"m":"tier(\"base\", p * )"}`),
		"编译不过的表达式必须被拒:它会让该模型的每一次请求都 400")

	var option Option
	assert.ErrorIs(t, db.Where(&Option{Key: billing_setting.BillingExprOptionKey}).First(&option).Error,
		gorm.ErrRecordNotFound)
}

// 对照组:闸门不能把正常保存一起挡掉。
func TestValidBillingExprStillPersists(t *testing.T) {
	db := useFrontendOptionMigrationDB(t)
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
		t.Cleanup(func() { common.OptionMap = nil })
	}

	const value = `{"m":"tier(\"base\", p * 3 + c * 15)"}`
	require.NoError(t, UpdateOption(billing_setting.BillingExprOptionKey, value))

	var option Option
	require.NoError(t, db.Where(&Option{Key: billing_setting.BillingExprOptionKey}).First(&option).Error)
	assert.Equal(t, value, option.Value)
}
