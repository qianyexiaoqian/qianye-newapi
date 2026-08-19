package controller

import (
	"bytes"
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

// redeem_log_leak_test.go —— 兑换失败时,兑换码明文不许落进日志。
//
// # 被测的缺陷(实测拿到过钱)
//
// 兑换失败走的是 `logger.LogError(c, "failed to redeem key %s ...", req.Key)`,
// 整串码写进 gin.DefaultErrorWriter,而它是 MultiWriter(stderr, logs/*.log)。
// 关键在于**兑换失败不消耗兑换码**:套餐停用、销售时间窗未开/已结束、超出单人
// 购买上限、DB 故障这几条分支走完之后,那张码仍然是 status=enabled、面值分文未动。
// 于是任何有日志读取权的人(运维、日志采集平台、日志备份)可以直接 grep 出一张
// 活码,用任意账号抢在合法持有者重试之前兑走 —— 上一轮审计正是这么拿到的钱:
// 从日志里 grep 出码 → 换一个账号调 /api/user/topup → 拿到订阅。
//
// 码本身是 crypto/rand 的 122 bit,不可枚举,这一行是它唯一的泄漏面。

func setupRedeemLogLeakTest(t *testing.T) *gorm.DB {
	t.Helper()
	prevDB, prevLogDB := model.DB, model.LOG_DB
	prevRedis := common.RedisEnabled
	prevMain, prevLog := common.MainDatabaseType(), common.LogDatabaseType()
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)

	dsn := fmt.Sprintf("file:%s_leak?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Redemption{}, &model.Log{},
		&model.SubscriptionPlan{}, &model.User{}))
	model.DB, model.LOG_DB = db, db

	payment := operation_setting.GetPaymentSetting()
	prevConfirmed, prevVersion := payment.ComplianceConfirmed, payment.ComplianceTermsVersion
	payment.ComplianceConfirmed = true
	payment.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	t.Cleanup(func() {
		payment.ComplianceConfirmed, payment.ComplianceTermsVersion = prevConfirmed, prevVersion
		model.DB, model.LOG_DB = prevDB, prevLogDB
		common.RedisEnabled = prevRedis
		common.SetDatabaseTypes(prevMain, prevLog)
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// 一张指向停用套餐的码:兑换必然失败,而码本身一个字节都不会被消耗。
func TestFailedRedeemNeverWritesTheCodeIntoTheLog(t *testing.T) {
	db := setupRedeemLogLeakTest(t)

	const liveKey = "deadbeef0000cafe0000feed00000001"
	require.NoError(t, db.Create(&model.User{Id: 7, Username: "redeemer"}).Error)
	require.NoError(t, db.Create(&model.SubscriptionPlan{Id: 61, Title: "停用的套餐"}).Error)
	// enabled 有 gorm 默认值 true,零值会被 Create 跳过,只能显式写回。
	require.NoError(t, db.Model(&model.SubscriptionPlan{}).Where("id = ?", 61).
		Update("enabled", false).Error)
	require.NoError(t, db.Create(&model.Redemption{
		Id: 1, UserId: 1, Key: liveKey, Name: "leak-probe",
		Status: common.RedemptionCodeStatusEnabled,
		// 套餐码:停用的套餐在 CAS 之前就报错,码留在 enabled 上。
		ProductType: model.RedemptionProductPlan, ProductId: 61,
	}).Error)

	var logs bytes.Buffer
	prevWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logs
	t.Cleanup(func() { gin.DefaultErrorWriter = prevWriter })

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/topup",
		strings.NewReader(`{"key":"`+liveKey+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 7)

	TopUp(c)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":false`)

	written := logs.String()
	require.Contains(t, written, "failed to redeem key",
		"失败仍然要留痕,否则这条修复变成了把日志删掉")
	assert.NotContains(t, written, liveKey,
		"兑换码明文进了日志 = 日志读取权等于兑换码提取权")
	assert.Contains(t, written, common.MaskCredential(liveKey),
		"末 4 位要留着,客服凭它把用户报的那张码对上库里那一行")

	// 这张码此刻仍然可兑 —— 泄漏的是活码,不是废码。
	var after model.Redemption
	require.NoError(t, db.First(&after, 1).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, after.Status)
	assert.Zero(t, after.RedeemedTime)
	assert.Zero(t, after.UsedUserId)
}

// 掩码的两条边界:短值一个字符都不露;长值只露末 4 位。
func TestMaskCredential(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret string
		want   string
	}{
		{"空值", "", "***"},
		{"短于 8 个字符的凭据撑不住部分泄漏", "abc1234", "***"},
		{"恰好 8 个字符", "abcd1234", "***1234"},
		{"32 位兑换码只露末 4 位", "deadbeef0000cafe0000feed00000001", "***0001"},
		{"首尾空白不算内容", "  deadbeef0000cafe0000feed00000001  ", "***0001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, common.MaskCredential(tc.secret))
		})
	}
}
