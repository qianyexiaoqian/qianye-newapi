package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 兑换码面值必须有上界，而且上下界要在建码与改码两侧同时成立。
//
// quota 是 Go int（64 位），库里 redemptions.quota 与 users.quota 都是 bigint，
// 而全站的额度语义上界是 common.MaxQuota —— 那是一条**算术**上界（见
// common/quota_math.go 的推导），不是列宽。没有这道闸时一个 role=10 管理员可以铸出
// 面额 MaxInt64 的码，兑换后 users.quota 直接等于 9223372036854775807 ——
// 之后任意一次 Go 侧的 `user.Quota += x`（aff_transfer 实测）都会静默回绕成约
// -9.2e18 的负余额，接口仍返回 success。
func TestRedemptionQuotaIsBoundedOnBothWriteSides(t *testing.T) {
	t.Run("改码:超过 MaxQuota 必须拒", func(t *testing.T) {
		db := setupRedemptionAdminTest(t)
		seedRedemptionRow(t, db, &model.Redemption{
			Id: 990301, UserId: 4242, Key: "qyctlkey00000000000000000bound01",
			Status: common.RedemptionCodeStatusEnabled, Name: "qy-bound", Quota: 1_000,
			CreatedTime: common.GetTimestamp(), ProductType: model.RedemptionProductQuota,
		})

		rec := callUpdateRedemption(t, "", `{"id":990301,"name":"qy-bound","quota":9223372036854775807,"expired_time":0}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"success":false`,
			"面值超过钱包能承载的上限必须拒;放过去等于给一次静默 int64 回绕铺路")

		var stored model.Redemption
		require.NoError(t, db.Where("id = ?", 990301).First(&stored).Error)
		assert.Equal(t, 1_000, stored.Quota, "被拒的改码不得留下任何痕迹")
	})

	t.Run("改码:恰好等于 MaxQuota 仍然放行", func(t *testing.T) {
		db := setupRedemptionAdminTest(t)
		seedRedemptionRow(t, db, &model.Redemption{
			Id: 990302, UserId: 4242, Key: "qyctlkey00000000000000000bound02",
			Status: common.RedemptionCodeStatusEnabled, Name: "qy-bound2", Quota: 1_000,
			CreatedTime: common.GetTimestamp(), ProductType: model.RedemptionProductQuota,
		})

		// 面值直接由 common.MaxQuota 拼出来:写死一个数就等于把这条闸门的
		// 边界抄成常量,MaxQuota 改一次这个"恰好等于上界"的用例就变成了
		// "上界以内的随便一个数",而它本来要证的正是边界本身可用。
		rec := callUpdateRedemption(t, "", `{"id":990302,"name":"qy-bound2","quota":`+
			strconv.Itoa(common.MaxQuota)+`,"expired_time":0}`)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"success":true`, "上界本身必须可用,否则这是一道错位的闸")

		var stored model.Redemption
		require.NoError(t, db.Where("id = ?", 990302).First(&stored).Error)
		assert.Equal(t, common.MaxQuota, stored.Quota)
	})
}

// 套餐码与用户组商品码的面额是死数据,建码侧会把它归零 —— 面额的上下界因此
// 只对余额码成立,**不能提到商品类型判断之外**。
//
// 上游在 rc.26 加的是一道无差别的 Quota<=0,与本仓的归零逻辑正面冲突:
// 前端送 0 被入口判据打回,运营手填正数被归零后撞在 Insert() 那道同名判据上,
// 三种 payload 全失败、实际创建 0 行,而运营只看到一句英文报错。
// 这条用例走**真实的 AddRedemption + Insert()**,不用 DB.Create 绕过去 ——
// 现有的套餐兑换用例正是因为夹具直接写库,才让这个回归全程没有信号。
func TestPlanAndUserGroupRedemptionsAreNotRejectedByTheQuotaFloor(t *testing.T) {
	for _, tc := range []struct {
		name        string
		productType string
		bodyQuota   string
	}{
		{"套餐码:前端送 0", model.RedemptionProductPlan, "0"},
		{"套餐码:运营手填正数", model.RedemptionProductPlan, "500000"},
		{"用户组商品码", model.RedemptionProductUserGroup, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupRedemptionAdminTest(t)
			allowRedemptionCreation(t)
			require.NoError(t, db.AutoMigrate(&model.SubscriptionPlan{}))
			require.NoError(t, db.Create(&model.SubscriptionPlan{
				Id: 77, Title: "qy-plan", Enabled: true,
			}).Error)

			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := `{"name":"qy-plan-code","count":1,"quota":` + tc.bodyQuota +
				`,"expired_time":0,"product_type":"` + tc.productType + `","product_id":77}`
			c.Request = httptest.NewRequest(http.MethodPost, "/api/redemption/", strings.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("id", 4242)
			c.Set("role", common.RoleRootUser)
			c.Set("username", "redemption-operator")
			AddRedemption(c)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), `"success":true`, rec.Body.String())

			var stored model.Redemption
			require.NoError(t, db.Where("name = ?", "qy-plan-code").First(&stored).Error,
				"码必须真的落库 —— 接口回 success 但零行才是这个回归最难查的形态")
			assert.Equal(t, tc.productType, stored.ProductKind())
			assert.Equal(t, 77, stored.ProductId)
		})
	}

	t.Run("余额码的下界仍然挡着", func(t *testing.T) {
		setupRedemptionAdminTest(t)
		allowRedemptionCreation(t)
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/redemption/",
			strings.NewReader(`{"name":"qy-zero","count":1,"quota":0,"expired_time":0}`))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", 4242)
		c.Set("role", common.RoleRootUser)
		c.Set("username", "redemption-operator")
		AddRedemption(c)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"success":false`,
			"非正面额的余额码兑出去是一笔倒扣,这道闸不能被顺手拆掉")
	})
}

// 划转到钱包的加法必须饱和，不能是裸的 int64 `+=`。
func TestQuotaAddSaturatesInsteadOfWrapping(t *testing.T) {
	cases := []struct {
		name        string
		base, delta int
		want        int
		wantClamp   bool
	}{
		{"正常相加", 1000, 2000, 3000, false},
		{"顶到上界就饱和,绝不回绕", common.MaxQuota - 1, 1000, common.MaxQuota, true},
		{"下界同理", common.MinQuota + 1, -1000, common.MinQuota, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, clamp := common.QuotaAddChecked(tc.base, tc.delta)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantClamp, clamp != nil, "饱和事件必须可审计")
		})
	}
}

// 建码走的是本仓那道支付合规漏斗(三条发钱线共用),不确认就一律拒。
// 这里放行的是**合规门**,与本用例要测的面额判据无关。
func allowRedemptionCreation(t *testing.T) {
	t.Helper()
	payment := operation_setting.GetPaymentSetting()
	prevConfirmed, prevVersion := payment.ComplianceConfirmed, payment.ComplianceTermsVersion
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
	t.Cleanup(func() {
		payment.ComplianceConfirmed, payment.ComplianceTermsVersion = prevConfirmed, prevVersion
	})
}
