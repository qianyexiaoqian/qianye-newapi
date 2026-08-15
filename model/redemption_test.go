package model

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// withMultiConnDB 把包级 DB 换成一个允许多连接的库,用例结束后换回。
//
// ═══════════ 为什么发订阅的用例非换不可 ═══════════
//
// TestMain 的库是 `:memory:` + MaxOpenConns(1)。那个 1 不是保守,是必需的:
// SQLite 的 `:memory:` 每开一条连接就是**一个各自独立的空库**。
//
// 而发订阅这条路径上,CreateUserSubscriptionFromPlanTx 会调 GetDBTimestamp(),
// 后者走的是全局 DB 而不是传进来的 tx —— 也就是**在事务里再问连接池要一条连接**。
// 池里只有一条、还正被这个事务占着,于是它等一条永远不会还回来的连接。
//
// 换成文件库(连接之间天然共享同一个数据库)+ busy_timeout,这层嵌套才成立。
// 这也是 AdminBindSubscription / PurchaseSubscriptionWithBalance 至今没有用例的原因。
func withMultiConnDB(t *testing.T) {
	t.Helper()
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "redeem.db")) +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	require.NoError(t, db.AutoMigrate(
		&User{}, &Log{}, &Redemption{},
		&SubscriptionPlan{}, &UserSubscription{}, &SubscriptionOrder{},
	))

	previousDB, previousLogDB := DB, LOG_DB
	DB, LOG_DB = db, db
	t.Cleanup(func() {
		DB, LOG_DB = previousDB, previousLogDB
		_ = sqlDB.Close()
	})
}

func TestSearchRedemptionsFiltersAndPaginates(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	})

	now := common.GetTimestamp()
	redemptions := []Redemption{
		{Id: 1, Name: "alpha-active", Key: "00000000000000000000000000000001", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: 0},
		{Id: 2, Name: "alpha-future", Key: "00000000000000000000000000000002", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now + 3600},
		{Id: 3, Name: "alpha-expired", Key: "00000000000000000000000000000003", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now - 10},
		{Id: 4, Name: "beta-disabled", Key: "00000000000000000000000000000004", Status: common.RedemptionCodeStatusDisabled, ExpiredTime: 0},
		{Id: 5, Name: "beta-used", Key: "00000000000000000000000000000005", Status: common.RedemptionCodeStatusUsed, ExpiredTime: 0},
	}
	require.NoError(t, DB.Create(&redemptions).Error)

	tests := []struct {
		name      string
		keyword   string
		status    string
		startIdx  int
		num       int
		wantTotal int64
		wantIds   []int
	}{
		{
			name:      "no filters returns all rows",
			num:       10,
			wantTotal: 5,
			wantIds:   []int{5, 4, 3, 2, 1},
		},
		{
			name:      "keyword filters by name prefix",
			keyword:   "alpha",
			num:       10,
			wantTotal: 3,
			wantIds:   []int{3, 2, 1},
		},
		{
			name:      "enabled status excludes expired rows",
			status:    "1",
			num:       10,
			wantTotal: 2,
			wantIds:   []int{2, 1},
		},
		{
			name:      "expired status returns enabled expired rows",
			status:    "expired",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{3},
		},
		{
			name:      "disabled status",
			status:    "2",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{4},
		},
		{
			name:      "used status",
			status:    "3",
			num:       10,
			wantTotal: 1,
			wantIds:   []int{5},
		},
		{
			name:      "pagination keeps unpaged total",
			startIdx:  1,
			num:       2,
			wantTotal: 5,
			wantIds:   []int{4, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, total, err := SearchRedemptions(tt.keyword, tt.status, tt.startIdx, tt.num)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTotal, total)
			gotIds := make([]int, 0, len(rows))
			for _, row := range rows {
				gotIds = append(gotIds, row.Id)
			}
			assert.Equal(t, tt.wantIds, gotIds)
		})
	}
}

// setupRedeemFixture 建一个用户和一张码。redemption 由调用方填好商品类型那几个字段,
// 只有清库、建用户、补公共字段是共用的。
func setupRedeemFixture(t *testing.T, redemption *Redemption) (userId int, key string) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
		DB.Exec("DELETE FROM users")
		DB.Exec("DELETE FROM logs")
		DB.Exec("DELETE FROM user_subscriptions")
		DB.Exec("DELETE FROM subscription_plans")
	})

	user := &User{Username: "redeem-user", Password: "password", Status: common.UserStatusEnabled, Quota: 0, Group: "default"}
	require.NoError(t, DB.Create(user).Error)

	key = "10000000000000000000000000000001"
	redemption.Key = key
	if redemption.Name == "" {
		redemption.Name = "redeem-test"
	}
	redemption.Status = common.RedemptionCodeStatusEnabled
	redemption.CreatedTime = common.GetTimestamp()
	require.NoError(t, DB.Create(redemption).Error)
	return user.Id, key
}

// seedRedeemPlan 建一个套餐,并在用例结束时清掉套餐缓存。
//
// getSubscriptionPlanByIdTx 会把套餐写进进程内缓存,而各用例复用同一个进程;
// 不清的话,后一个用例用同一个 id 建的套餐会读到前一个用例的内容。
func seedRedeemPlan(t *testing.T, plan *SubscriptionPlan) *SubscriptionPlan {
	t.Helper()
	enabled := plan.Enabled
	require.NoError(t, DB.Create(plan).Error)
	// enabled 列带 `default:true`,GORM 会把 false 当零值整列略过,数据库再补回 true。
	// 想造一个"已停用"的套餐,只能在插入之后显式改一次。
	if !enabled {
		require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).
			Update("enabled", false).Error)
		plan.Enabled = false
	}
	t.Cleanup(func() { InvalidateSubscriptionPlanCache(plan.Id) })
	return plan
}

func TestRedeemCreditsQuotaExactlyOnce(t *testing.T) {
	userId, key := setupRedeemFixture(t, &Redemption{Quota: 500, ProductType: RedemptionProductQuota})

	result, err := Redeem(key, userId)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, RedemptionProductQuota, result.ProductType)
	assert.Equal(t, 500, result.Quota)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)

	var redemption Redemption
	require.NoError(t, DB.First(&redemption, "name = ?", "redeem-test").Error)
	assert.Equal(t, common.RedemptionCodeStatusUsed, redemption.Status)
	assert.Equal(t, userId, redemption.UsedUserId)

	// Redeeming the same code again must fail and must not credit quota.
	_, err = Redeem(key, userId)
	require.Error(t, err)
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)
}

// TestRedeemLegacyBlankProductTypeCreditsQuota 存量兑换码(product_type 是空串)必须按余额码兑换。
//
// 这一列是后加的,库里那批还没兑换的码全是空串。判定要是直接比较字段而不走 ProductKind(),
// 升级那一刻它们会一起变成"商品类型不认识",用户手里的码集体作废 ——
// 而且是在兑换的时候才发现,管理端看上去一切正常。
func TestRedeemLegacyBlankProductTypeCreditsQuota(t *testing.T) {
	userId, key := setupRedeemFixture(t, &Redemption{Quota: 700})

	// 必须显式改成空串:product_type 列带 `default:'quota'`,新插入的行拿不到空值,
	// 而库里那批**加列之前就存在**的行正是空的。这一行就是在还原那个状态。
	require.NoError(t, DB.Model(&Redemption{}).Where("`key` = ?", key).
		Update("product_type", "").Error)

	var stored Redemption
	require.NoError(t, DB.First(&stored, "name = ?", "redeem-test").Error)
	require.Empty(t, stored.ProductType, "fixture 必须真的落成空串,否则这条用例没测到东西")

	result, err := Redeem(key, userId)
	require.NoError(t, err)
	assert.Equal(t, RedemptionProductQuota, result.ProductType)
	assert.Equal(t, 700, result.Quota)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 700, user.Quota)

	var subCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userId).Count(&subCount).Error)
	assert.Zero(t, subCount, "余额码不该发出任何订阅")
}

// TestRedeemPlanIssuesSubscription 套餐码发一条订阅,而不是加余额。
//
// 断言落在 UserSubscription 的快照字段上:发放必须复用
// CreateUserSubscriptionFromPlanTx。自己拼一条 insert 的话,upgrade_group /
// prev_user_group / no_quota / allow_wallet_overflow 会一起漏掉,
// 表现是到期那天用户组不回退、纯商品变成无限余额 —— 都不在兑换这条链路上暴露。
func TestRedeemPlanIssuesSubscription(t *testing.T) {
	withMultiConnDB(t)
	plan := seedRedeemPlan(t, &SubscriptionPlan{
		Id: 7301, Title: "Pro 月付", DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
		TotalAmount: 12345, UpgradeGroup: "vip", DowngradeGroup: "default", Enabled: true,
	})
	// 刻意给一个非 0 的额度:套餐码的 quota 列是死数据,而且**真的不会是 0** ——
	// 该列带 `default:100`,GORM 会把建码时那个刻意的 0 换成列默认值。
	// 兑换必须只看 product_type,绝不能顺手把这一列的钱也发出去。
	userId, key := setupRedeemFixture(t, &Redemption{
		ProductType: RedemptionProductPlan, ProductId: plan.Id, Quota: 999,
	})

	result, err := Redeem(key, userId)
	require.NoError(t, err)
	assert.Equal(t, RedemptionProductPlan, result.ProductType)
	assert.Zero(t, result.Quota, "套餐码不加钱包额度")
	assert.Equal(t, plan.Id, result.PlanId)
	assert.Equal(t, "Pro 月付", result.PlanTitle)
	assert.Equal(t, "vip", result.UpgradeGroup)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Zero(t, user.Quota, "钱包余额必须一分未动 —— 哪怕这张码的 quota 列是 999")
	assert.Equal(t, "vip", user.Group, "套餐配了升级组就要真的改组")

	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userId).First(&sub).Error)
	assert.Equal(t, plan.Id, sub.PlanId)
	assert.Equal(t, RedemptionSubscriptionSource, sub.Source, "来路要能事后分辨")
	assert.Equal(t, "active", sub.Status)
	assert.EqualValues(t, 12345, sub.AmountTotal)
	assert.False(t, sub.NoQuota)
	assert.Equal(t, "vip", sub.UpgradeGroup)
	assert.Equal(t, "default", sub.PrevUserGroup, "购买前的原组要拍进快照,否则到期回退无处可退")
	assert.Equal(t, "default", sub.DowngradeGroup)
}

// TestRedeemUserGroupProductIssuesPureProductSubscription 用户组商品:永久 + 纯商品档。
//
// 它与 plan 走同一条发放路径,差别整个落在套餐本身 —— 这条用例钉的正是
// "那些差别确实被原样带进了订阅":永久档的 end_time 是 0(而不是某个未来时间),
// 纯商品的额度是 PureProductAmountTotal(而不是 0,0 的语义是不限额度)。
func TestRedeemUserGroupProductIssuesPureProductSubscription(t *testing.T) {
	withMultiConnDB(t)
	plan := seedRedeemPlan(t, &SubscriptionPlan{
		Id: 7302, Title: "永久 VIP 身份", DurationUnit: SubscriptionDurationPermanent,
		NoQuota: true, TotalAmount: 0, UpgradeGroup: "vip", Enabled: true,
	})
	userId, key := setupRedeemFixture(t, &Redemption{ProductType: RedemptionProductUserGroup, ProductId: plan.Id})

	result, err := Redeem(key, userId)
	require.NoError(t, err)
	assert.Equal(t, RedemptionProductUserGroup, result.ProductType)
	assert.Zero(t, result.Quota)
	assert.Equal(t, "vip", result.UpgradeGroup)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Zero(t, user.Quota)
	assert.Equal(t, "vip", user.Group)

	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userId).First(&sub).Error)
	assert.True(t, sub.NoQuota, "纯商品标记必须拍进订阅,否则它会被出资查询选中")
	assert.Equal(t, PureProductAmountTotal, sub.AmountTotal,
		"纯商品落 0 等于「不限额度」—— 必须是那个极小的正数")
	assert.Zero(t, sub.EndTime, "永久档的 end_time 必须是 0")
}

// TestRedeemPlanRollsBackWhenPlanDisabled 套餐停用时,码必须还能用。
//
// 这条守的是「发货与 CAS 同一事务」。拆成两步的话,码已经被标成已用,
// 而订阅因为套餐停用没发出去 —— 用户手里的码作废了,东西一样没拿到,
// 且没有任何一条路径能把它还原。
func TestRedeemPlanRollsBackWhenPlanDisabled(t *testing.T) {
	withMultiConnDB(t)
	plan := seedRedeemPlan(t, &SubscriptionPlan{
		Id: 7303, Title: "已停用套餐", DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
		TotalAmount: 100, Enabled: false,
	})
	userId, key := setupRedeemFixture(t, &Redemption{ProductType: RedemptionProductPlan, ProductId: plan.Id})

	_, err := Redeem(key, userId)
	require.Error(t, err)

	var stored Redemption
	require.NoError(t, DB.First(&stored, "name = ?", "redeem-test").Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, stored.Status, "发不出货的兑换不能把码标成已用")
	assert.Zero(t, stored.UsedUserId)

	var subCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userId).Count(&subCount).Error)
	assert.Zero(t, subCount)
}

// Exactly one of several concurrent redeems of the same code may win, and
// quota must be credited exactly once.
func TestRedeemConcurrentSingleSuccess(t *testing.T) {
	userId, key := setupRedeemFixture(t, &Redemption{Quota: 300, ProductType: RedemptionProductQuota})

	assert.Equal(t, 1, countConcurrentRedeemSuccesses(t, key, userId),
		"exactly one concurrent redeem should succeed")

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 300, user.Quota, "quota must be credited exactly once")
}

// TestRedeemPlanConcurrentIssuesExactlyOneSubscription 套餐码的并发保护与余额码一致。
//
// 单独测一遍是必要的:套餐分支在 CAS 之后多做了一件事(发订阅),
// 而重复发订阅的后果比重复加额度更难收拾 —— 多出来的那条订阅会一直占着名额、
// 挂着升级组,到期回退时还会互相压制。
func TestRedeemPlanConcurrentIssuesExactlyOneSubscription(t *testing.T) {
	withMultiConnDB(t)
	plan := seedRedeemPlan(t, &SubscriptionPlan{
		Id: 7304, Title: "并发套餐", DurationUnit: SubscriptionDurationMonth, DurationValue: 1,
		TotalAmount: 999, UpgradeGroup: "vip", Enabled: true,
	})
	userId, key := setupRedeemFixture(t, &Redemption{ProductType: RedemptionProductPlan, ProductId: plan.Id})

	assert.Equal(t, 1, countConcurrentRedeemSuccesses(t, key, userId),
		"并发兑换同一张套餐码只能成功一次")

	var subCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userId).Count(&subCount).Error)
	assert.EqualValues(t, 1, subCount, "订阅只能发出一条")
}

func countConcurrentRedeemSuccesses(t *testing.T, key string, userId int) int {
	t.Helper()
	const goroutines = 5
	successes := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			if _, err := Redeem(key, userId); err == nil {
				successes[idx] = true
			}
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, ok := range successes {
		if ok {
			successCount++
		}
	}
	return successCount
}
