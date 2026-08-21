package overdraft

import (
	"context"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// overdraft_test.go —— 负余额总览的四条契约。
//
// 这个包的全部作用是回答运营的一个问题:「现在有多少账号是负的、合计欠多少、
// 最深的是谁」。四条契约各自对应一种"答错了但看起来很正常"的失败:
//
//	① 合计欠额是正数,且等于全部负余额之和的绝对值。搞错符号的表现是界面上
//	   写着「合计欠额 -$1.23」,而运营看到负号会以为是平台欠用户钱。
//	② 排序是"欠得最深的在前"。quota 恒为负,所以是 ASC 不是 DESC ——
//	   写反了会把欠得最少的那个人排在第一行,而这张表的用途正是"先追谁"。
//	③ 软删除的账号不算。注销账号的欠款既追不回也没必要追,算进去会污染
//	   "本月欠款涨了多少"这个运营最常看的差值。
//	④ truncated 与 accounts 必须能区分"正好 N 个人欠钱"和"还有一大堆"。
//
// 余额为 0 的账号一律不算 —— 判据是 `quota < 0` 而不是 `<= 0`。零余额是
// "用完了",不是"欠费",两者的处置动作完全不同。

func newUsersDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.User{}))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// seedUser 落一个账号。deleted 为 true 时随即软删。
func seedUser(t *testing.T, gdb *gorm.DB, id int, username string, quota int, deleted bool) {
	t.Helper()
	// aff_code 与 access_token 都带唯一索引,空值在 SQLite 上会互撞。
	// 逐行给不同的值,免得测试红在与本包无关的地方。
	u := &model.User{
		Id:       id,
		Username: username,
		Password: "x",
		Quota:    quota,
		Group:    "default",
		Status:   1,
		AffCode:  fmt.Sprintf("qy-aff-%d", id),
	}
	require.NoError(t, gdb.Create(u).Error)
	if deleted {
		require.NoError(t, gdb.Delete(u).Error)
	}
}

func TestScanCountsSumsAndRanksOverdrawnAccounts(t *testing.T) {
	gdb := newUsersDB(t)

	seedUser(t, gdb, 1, "qy-positive", 5000, false)
	seedUser(t, gdb, 2, "qy-zero", 0, false)
	seedUser(t, gdb, 3, "qy-small-debt", -100, false)
	seedUser(t, gdb, 4, "qy-deep-debt", -140000, false)
	seedUser(t, gdb, 5, "qy-mid-debt", -7000, false)
	// 已注销但欠着钱:必须被排除在外。
	seedUser(t, gdb, 6, "qy-deleted-debt", -999999, true)

	report, err := Scan(context.Background(), 0)
	require.NoError(t, err)

	// 独立算出的期望:三个未删除的负余额账号,合计 100 + 140000 + 7000。
	assert.EqualValues(t, 3, report.Accounts)
	assert.EqualValues(t, 147100, report.TotalOwed,
		"合计欠额必须是正数,且等于负余额之和的绝对值")

	require.Len(t, report.Top, 3)
	assert.Equal(t, []string{"qy-deep-debt", "qy-mid-debt", "qy-small-debt"},
		[]string{report.Top[0].Username, report.Top[1].Username, report.Top[2].Username},
		"排序必须是欠得最深的在前(quota 恒为负 ⇒ ASC)")

	require.NotNil(t, report.Deepest)
	assert.Equal(t, "qy-deep-debt", report.Deepest.Username)
	assert.Equal(t, -140000, report.Deepest.Quota,
		"逐账号下发的是余额原值(负数),不是取过反的欠款额")
	assert.Equal(t, "default", report.Deepest.Group)

	assert.False(t, report.Truncated, "3 个账号全都在清单里,不该报截断")
}

func TestScanReportsNothingWhenAllBalancesAreNonNegative(t *testing.T) {
	gdb := newUsersDB(t)
	seedUser(t, gdb, 1, "qy-positive", 5000, false)
	// 余额恰好为 0:是"用完了",不是"欠费"。判据是 quota < 0。
	seedUser(t, gdb, 2, "qy-zero", 0, false)

	report, err := Scan(context.Background(), 0)
	require.NoError(t, err)

	assert.EqualValues(t, 0, report.Accounts)
	assert.EqualValues(t, 0, report.TotalOwed)
	assert.Nil(t, report.Deepest)
	assert.Empty(t, report.Top)
	assert.False(t, report.Truncated)
}

func TestScanTruncatesTopListAndSaysSo(t *testing.T) {
	gdb := newUsersDB(t)
	for i := 1; i <= 5; i++ {
		seedUser(t, gdb, i, fmt.Sprintf("qy-debtor-%d", i), -i*1000, false)
	}

	report, err := Scan(context.Background(), 2)
	require.NoError(t, err)

	// 合计与计数**不受 limit 影响** —— limit 只裁清单。这一条是本测试的重点:
	// 把聚合也接上 limit 的表现是"合计欠额随着页面参数变小",而运营会照着
	// 那个数字做决定。
	assert.EqualValues(t, 5, report.Accounts)
	assert.EqualValues(t, 1000+2000+3000+4000+5000, report.TotalOwed)

	require.Len(t, report.Top, 2)
	assert.Equal(t, -5000, report.Top[0].Quota)
	assert.Equal(t, -4000, report.Top[1].Quota)
	assert.True(t, report.Truncated, "5 个账号只列了 2 个,必须报截断")
}

func TestScanClampsLimitToMaxAndRejectsMissingDatabase(t *testing.T) {
	t.Run("limit 超上界被夹住", func(t *testing.T) {
		gdb := newUsersDB(t)
		for i := 1; i <= 3; i++ {
			seedUser(t, gdb, i, fmt.Sprintf("qy-debtor-%d", i), -i, false)
		}
		report, err := Scan(context.Background(), MaxTopLimit+10_000)
		require.NoError(t, err)
		assert.Len(t, report.Top, 3, "夹到上界之后仍然要返回全部命中行")
	})

	t.Run("主库未初始化是错误而不是空报告", func(t *testing.T) {
		prev := model.DB
		model.DB = nil
		t.Cleanup(func() { model.DB = prev })

		_, err := Scan(context.Background(), 0)
		// 「0 个账号欠 0 元」与「查不了」在界面上一模一样,而运营结论正好相反。
		require.ErrorIs(t, err, ErrNoDatabase)
	})
}
