package withdraw

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestDB 建一个只承载提现相关表的内存库。
//
// 这几条缺陷("hold 单被重新扫到""风控闸门没有消费方""到期密文不清")的共同点
// 是它们全都住在 WHERE 条件里 —— 只有真的让数据库执行一遍那条查询才能验证。
// mock 掉 GORM 等于把被测对象换成了测试自己写的假设。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Withdrawal{}, &Payee{}, &PayeeAccount{}, &Event{}, &PiiAudit{}, &Proof{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// seedWithdrawal 插入一张提现单。唯一索引(withdraw_no、idem_scope+idem_key)
// 都从 no 派生,调用方只要给不同的单号就不会互相冲突。
func seedWithdrawal(t *testing.T, gdb *gorm.DB, no string, mutate func(*Withdrawal)) *Withdrawal {
	t.Helper()
	now := common.GetTimestamp()
	w := &Withdrawal{
		WithdrawNo: no,
		IdemScope:  idemScope,
		IdemKey:    idemKeyOf(1, "seed-"+no),
		UserId:     1,
		Method:     "quota",
		Status:     StatusPending,
		Quota:      500000,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if mutate != nil {
		mutate(w)
	}
	require.NoError(t, gdb.Create(w).Error)
	return w
}

func withdrawNos(rows []Withdrawal) []string {
	out := make([]string, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].WithdrawNo)
	}
	return out
}

func idOf(i int) string { return "WD" + strconv.Itoa(i) }
