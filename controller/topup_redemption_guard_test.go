package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// topup_redemption_guard_test.go —— 下单上界与兑换码写接口的两道闸。
//
// 上界这一条的形状是"用户付了钱、额度一分没到":结算侧的额度换算超过
// common.MaxQuota 会整笔回滚,订单永远停在 pending,网关对着 fail 无限重投,
// 管理员补单走同一个换算同样失败。所以下单侧的上界必须与结算侧**同源**,
// 差一个单位就是一笔全额丢失的充值。
//
// 兑换码那两条的形状是"接口回 success,账目对不上":已兑换的码被翻回启用
// 就能再兑一次(佣金幂等键 redemption:<id> 只算第一次,钱却发了两遍),
// 面额被改成负数则是直接从用户余额里倒扣。

func useQuotaDisplayType(t *testing.T, displayType string, quotaPerUnit float64) {
	t.Helper()
	general := operation_setting.GetGeneralSetting()
	prevType := general.QuotaDisplayType
	prevQPU := common.QuotaPerUnit
	general.QuotaDisplayType = displayType
	common.QuotaPerUnit = quotaPerUnit
	t.Cleanup(func() {
		general.QuotaDisplayType = prevType
		common.QuotaPerUnit = prevQPU
	})
}

// getMaxTopup 与结算侧必须逐单位对齐:上界这一格允许下单,上界 +1 那一格
// 必须在结算侧换算失败 —— 否则要么白挡住合法充值,要么放进一笔永远结不掉的订单。
func TestGetMaxTopupMatchesSettlementCeiling(t *testing.T) {
	for _, tc := range []struct {
		name         string
		displayType  string
		quotaPerUnit float64
		want         int64
	}{
		{"USD 展示按单位数", operation_setting.QuotaDisplayTypeUSD, 500000, 4294},
		{"CNY 展示同口径", operation_setting.QuotaDisplayTypeCNY, 500000, 4294},
		{"TOKENS 展示按 tokens", operation_setting.QuotaDisplayTypeTokens, 500000, 4294 * 500000},
		{"额度单位更小时上界更高", operation_setting.QuotaDisplayTypeUSD, 1000, 2147483},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useQuotaDisplayType(t, tc.displayType, tc.quotaPerUnit)
			maxAmount := getMaxTopup()
			require.Equal(t, tc.want, maxAmount)

			// 落库的 Amount:TOKENS 模式下前端传的是 tokens,要先换成单位数。
			storedAt := func(amount int64) int64 {
				if tc.displayType != operation_setting.QuotaDisplayTypeTokens {
					return amount
				}
				return int64(float64(amount) / tc.quotaPerUnit)
			}

			atLimit := &model.TopUp{PaymentProvider: model.PaymentProviderEpay, Amount: storedAt(maxAmount)}
			_, err := atLimit.CreditQuota()
			assert.NoError(t, err, "上界这一格必须结算得掉,否则是在白挡合法充值")

			overLimit := &model.TopUp{PaymentProvider: model.PaymentProviderEpay, Amount: storedAt(maxAmount) + 1}
			_, err = overLimit.CreditQuota()
			assert.Error(t, err, "上界 +1 必须结算失败,否则这道闸拦错了地方")
		})
	}
}

func setupRedemptionAdminTest(t *testing.T) *gorm.DB {
	t.Helper()
	prevDB, prevLogDB := model.DB, model.LOG_DB
	prevRedis := common.RedisEnabled
	prevMainType, prevLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	dsn := fmt.Sprintf("file:%s_rdm?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Redemption{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db

	t.Cleanup(func() {
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.RedisEnabled = prevRedis
		common.SetDatabaseTypes(prevMainType, prevLogType)
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func callUpdateRedemption(t *testing.T, query string, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/redemption/"+query, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 4242)
	c.Set("role", common.RoleRootUser)
	c.Set("username", "redemption-operator")
	UpdateRedemption(c)
	return rec
}

func seedRedemptionRow(t *testing.T, db *gorm.DB, r *model.Redemption) {
	t.Helper()
	require.NoError(t, db.Create(r).Error)
	// quota 列带 `default:100`,零值会被 GORM 略过交给数据库补默认值。
	require.NoError(t, db.Model(&model.Redemption{}).Where("id = ?", r.Id).
		Update("quota", r.Quota).Error)
}

func TestUpdateRedemptionRefusesToResurrectAUsedCode(t *testing.T) {
	db := setupRedemptionAdminTest(t)
	seedRedemptionRow(t, db, &model.Redemption{
		Id: 990001, UserId: 1, Key: "qyctlkey00000000000000000000used",
		Status: common.RedemptionCodeStatusUsed, Name: "qy-used", Quota: 1_000_000,
		CreatedTime: common.GetTimestamp(), RedeemedTime: common.GetTimestamp(), UsedUserId: 555,
		ProductType: model.RedemptionProductQuota,
	})

	rec := callUpdateRedemption(t, "?status_only=true", `{"id":990001,"status":1}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`,
		"已兑换是终态:翻回启用等于把一张已核销的码重新变成现金")

	var stored model.Redemption
	require.NoError(t, db.Where("id = ?", 990001).First(&stored).Error)
	assert.Equal(t, common.RedemptionCodeStatusUsed, stored.Status)
	assert.Equal(t, 555, stored.UsedUserId, "核销痕迹不得被覆盖")
}

func TestUpdateRedemptionStatusOnlyAcceptsOnlyEnabledOrDisabled(t *testing.T) {
	db := setupRedemptionAdminTest(t)
	seedRedemptionRow(t, db, &model.Redemption{
		Id: 990002, UserId: 1, Key: "qyctlkey0000000000000000000live0",
		Status: common.RedemptionCodeStatusEnabled, Name: "qy-live", Quota: 1_000_000,
		CreatedTime: common.GetTimestamp(), ProductType: model.RedemptionProductQuota,
	})

	t.Run("停用是允许的", func(t *testing.T) {
		rec := callUpdateRedemption(t, "?status_only=true", `{"id":990002,"status":2}`)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"success":true`)
		var stored model.Redemption
		require.NoError(t, db.Where("id = ?", 990002).First(&stored).Error)
		assert.Equal(t, common.RedemptionCodeStatusDisabled, stored.Status)
	})

	t.Run("直接写成已使用是不允许的", func(t *testing.T) {
		rec := callUpdateRedemption(t, "?status_only=true", `{"id":990002,"status":3}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"success":false`,
			"状态机只在启用/停用之间走,已使用只能由真实兑换写出来")
		var stored model.Redemption
		require.NoError(t, db.Where("id = ?", 990002).First(&stored).Error)
		assert.Equal(t, common.RedemptionCodeStatusDisabled, stored.Status)
	})
}

func TestUpdateRedemptionRefusesNonPositiveQuota(t *testing.T) {
	db := setupRedemptionAdminTest(t)
	seedRedemptionRow(t, db, &model.Redemption{
		Id: 990003, UserId: 1, Key: "qyctlkey00000000000000000000quo0",
		Status: common.RedemptionCodeStatusEnabled, Name: "qy-quota", Quota: 1_000_000,
		CreatedTime: common.GetTimestamp(), ProductType: model.RedemptionProductQuota,
	})

	for _, tc := range []struct{ name, body string }{
		{"负面额", `{"id":990003,"name":"qy-neg","quota":-500000,"expired_time":0}`},
		{"零面额", `{"id":990003,"name":"qy-zero","quota":0,"expired_time":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callUpdateRedemption(t, "", tc.body)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), `"success":false`,
				"建码拦得住、改码拦不住,同一个业务不变量只守住一半")

			var stored model.Redemption
			require.NoError(t, db.Where("id = ?", 990003).First(&stored).Error)
			assert.Equal(t, 1_000_000, stored.Quota, "被拒绝的改动不得落库")
		})
	}
}

// 改兑换码是一条发钱通道,必须留下码 id 与前后值。
// 在补上这一条之前,它只落一条路由级兜底日志(只有 method/path/status),
// 事后无从判断哪张码被改过面额、哪张被翻回启用。
func TestUpdateRedemptionWritesManageAudit(t *testing.T) {
	db := setupRedemptionAdminTest(t)
	seedRedemptionRow(t, db, &model.Redemption{
		Id: 990004, UserId: 1, Key: "qyctlkey000000000000000000audit0",
		Status: common.RedemptionCodeStatusEnabled, Name: "qy-audit", Quota: 1_000_000,
		CreatedTime: common.GetTimestamp(), ProductType: model.RedemptionProductQuota,
	})

	rec := callUpdateRedemption(t, "?status_only=true", `{"id":990004,"status":2}`)
	require.Contains(t, rec.Body.String(), `"success":true`)

	var logs []model.Log
	require.NoError(t, db.Where("type = ?", model.LogTypeManage).Find(&logs).Error)
	require.Len(t, logs, 1, "一次成功的兑换码改动必须且只留一行管理审计")
	assert.Contains(t, logs[0].Other, `"redemption.update"`)
	assert.Contains(t, logs[0].Other, `"redemption_id":990004`)
	assert.Contains(t, logs[0].Other, `"status_before":1`)
	assert.Contains(t, logs[0].Other, `"status_after":2`)
}

// 下单侧必须真的把上界这道闸挂上。
//
// getMaxTopup 算得再对,只要 RequestEpay 不调用它,一笔 ≥ 上界的订单照样能建出来,
// 而它一旦被支付就是全额丢失:结算换算触顶 → 整笔回滚 → 订单永远 pending。
func TestRequestEpayRejectsAmountsAboveSettlementCeiling(t *testing.T) {
	useQuotaDisplayType(t, operation_setting.QuotaDisplayTypeUSD, 500000)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/pay",
		strings.NewReader(`{"amount":4295,"payment_method":"alipay"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 4242)

	RequestEpay(c)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "充值数量不能大于 4294",
		"上界必须在下单这一步就挡住,再往后任何一步都救不回来")
}
