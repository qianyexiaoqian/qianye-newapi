package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedOutbox(t *testing.T, orderNo string, createdAt int64) {
	t.Helper()
	require.NoError(t, DB.Create(&QyFundOutbox{
		OrderNo: orderNo, Kind: "transfer", UserId: 1, Amount: 1, CreatedAt: createdAt,
	}).Error)
}

func resetOutbox(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&QyFundOutbox{}))
	require.NoError(t, DB.Exec("DELETE FROM qy_fund_outbox").Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM qy_fund_outbox") })
}

// C5:outbox 清理必须真的按批执行。
//
// 旧实现是 DB.Where(...).Limit(batch).Delete(...)。GORM 只在方言把 "LIMIT"
// 列进 DeleteClauses 时才渲染它,而这只有 MySQL 驱动做了 —— postgres 与
// sqlite 的 DeleteClauses 里没有 LIMIT,会静默生成一条不带 LIMIT 的 DELETE
// 一次删光。本测试跑在 sqlite 上(TestMain 的库),因此不按批的实现会一次
// 捞回全部行而不是 batch 行,直接失败。
func TestQyScanFundOutbox_BatchesAcrossDialects(t *testing.T) {
	resetOutbox(t)
	for i := 0; i < 10; i++ {
		seedOutbox(t, "OLD-"+string(rune('a'+i)), 100)
	}
	for i := 0; i < 3; i++ {
		seedOutbox(t, "NEW-"+string(rune('a'+i)), 300)
	}

	rows, err := QyScanFundOutbox(200, 0, 3)
	require.NoError(t, err)
	require.Len(t, rows, 3, "一轮只能捞 batch 行,否则会在业务库上产生一次删光的长事务")
	for _, row := range rows {
		assert.EqualValues(t, 100, row.CreatedAt, "未到保留期的行不该进入候选")
	}

	// 游标推进:整段过期行走完为止,未过期的一行都不能出现。
	// 轮次显式设上界 —— 游标一旦失效,这个循环会永远捞回同一批行,
	// 让用例挂死而不是报错。宁可判"轮次超了"也不要一个跑不完的测试。
	var seen int
	cursor := int64(0)
	for round := 0; round < 8; round++ {
		batch, err := QyScanFundOutbox(200, cursor, 3)
		require.NoError(t, err)
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			assert.EqualValues(t, 100, row.CreatedAt)
		}
		seen += len(batch)
		require.Greater(t, batch[0].Id, cursor, "游标必须真的推进,否则清理会永远停在原地")
		cursor = batch[len(batch)-1].Id
	}
	assert.Equal(t, 10, seen)
}

// batch 非正数时必须退化成默认批量,而不是退化成"不限"。
func TestQyScanFundOutbox_NonPositiveBatchFallsBackToDefault(t *testing.T) {
	resetOutbox(t)
	for i := 0; i < 5; i++ {
		seedOutbox(t, "Z-"+string(rune('a'+i)), 100)
	}

	rows, err := QyScanFundOutbox(200, 0, 0)
	require.NoError(t, err)
	assert.Len(t, rows, 5)

	rows, err = QyScanFundOutbox(50, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, rows, "没有到期的行时不该捞回任何东西")
}

// 删除只认主键列表:调用方必须先逐行确认过"这一行的证据使命已经结束"。
// 空列表绝不能退化成一条无 WHERE 的 DELETE 把整张表清空。
func TestQyDeleteFundOutbox_ByIdsOnly(t *testing.T) {
	resetOutbox(t)
	for i := 0; i < 4; i++ {
		seedOutbox(t, "D-"+string(rune('a'+i)), 100)
	}

	deleted, err := QyDeleteFundOutbox(nil)
	require.NoError(t, err)
	assert.EqualValues(t, 0, deleted)
	var n int64
	require.NoError(t, DB.Model(&QyFundOutbox{}).Count(&n).Error)
	assert.EqualValues(t, 4, n, "空列表必须是零操作,绝不能删光整张表")

	rows, err := QyScanFundOutbox(200, 0, 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	deleted, err = QyDeleteFundOutbox([]int64{rows[0].Id, rows[1].Id})
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)

	require.NoError(t, DB.Model(&QyFundOutbox{}).Count(&n).Error)
	assert.EqualValues(t, 2, n)
}
