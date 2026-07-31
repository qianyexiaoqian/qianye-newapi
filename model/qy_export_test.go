package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C5:outbox 清理必须真的按批执行。
//
// 旧实现是 DB.Where(...).Limit(batch).Delete(...)。GORM 只在方言把 "LIMIT"
// 列进 DeleteClauses 时才渲染它,而这只有 MySQL 驱动做了 —— postgres 与
// sqlite 的 DeleteClauses 里没有 LIMIT,会静默生成一条不带 LIMIT 的 DELETE
// 一次删光。本测试跑在 sqlite 上(TestMain 的库),因此旧实现会删掉全部行而
// 不是 batch 行,直接失败。
func TestQyPruneFundOutbox_BatchesAcrossDialects(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&QyFundOutbox{}))
	t.Cleanup(func() { DB.Exec("DELETE FROM qy_fund_outbox") })
	require.NoError(t, DB.Exec("DELETE FROM qy_fund_outbox").Error)

	// 10 行过期 + 3 行未过期。
	for i := 0; i < 10; i++ {
		require.NoError(t, DB.Create(&QyFundOutbox{
			OrderNo: "OLD-" + string(rune('a'+i)), Kind: "transfer", UserId: 1, Amount: 1,
			CreatedAt: 100,
		}).Error)
	}
	for i := 0; i < 3; i++ {
		require.NoError(t, DB.Create(&QyFundOutbox{
			OrderNo: "NEW-" + string(rune('a'+i)), Kind: "transfer", UserId: 1, Amount: 1,
			CreatedAt: 300,
		}).Error)
	}

	countAll := func() int64 {
		var n int64
		require.NoError(t, DB.Model(&QyFundOutbox{}).Count(&n).Error)
		return n
	}

	deleted, err := QyPruneFundOutbox(200, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 3, deleted, "一轮只能删 batch 行,否则会在业务库上产生一次删光的长事务")
	assert.EqualValues(t, 10, countAll())

	// 反复调用直到删完过期行,未过期行一行都不能少。
	total := deleted
	for {
		n, err := QyPruneFundOutbox(200, 3)
		require.NoError(t, err)
		total += n
		if n == 0 {
			break
		}
	}
	assert.EqualValues(t, 10, total)
	assert.EqualValues(t, 3, countAll(), "未到保留期的行必须原样保留")

	var left []QyFundOutbox
	require.NoError(t, DB.Find(&left).Error)
	for _, row := range left {
		assert.EqualValues(t, 300, row.CreatedAt)
	}
}

// batch 非正数时必须退化成默认批量,而不是退化成"不限"把表删光。
func TestQyPruneFundOutbox_NonPositiveBatchFallsBackToDefault(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&QyFundOutbox{}))
	require.NoError(t, DB.Exec("DELETE FROM qy_fund_outbox").Error)
	t.Cleanup(func() { DB.Exec("DELETE FROM qy_fund_outbox") })

	for i := 0; i < 5; i++ {
		require.NoError(t, DB.Create(&QyFundOutbox{
			OrderNo: "Z-" + string(rune('a'+i)), Kind: "transfer", UserId: 1, Amount: 1,
			CreatedAt: 100,
		}).Error)
	}

	deleted, err := QyPruneFundOutbox(200, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 5, deleted)

	deleted, err = QyPruneFundOutbox(200, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 0, deleted, "没有可删的行时不应报错也不应发出 DELETE")
}
