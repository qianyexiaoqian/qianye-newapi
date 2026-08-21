package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 兑换码明文只对**发码人自己**可见（root 例外）。
//
// 兑换码是可直接兑成余额的 bearer 凭据：拿到那 32 位明文就等于拿到了钱。
// 管理端列表/搜索原样吐明文、不分创建者、读取路径一条审计都不写，于是任意
// role=10 账号可以把别的管理员或 root 已经发行、准备分给客户的整批在售码一次性
// 读走再自己兑掉，事后只剩 used_user_id 一个间接线索。
//
// 收窄到发码人而不是遮挡明文：发码/补发流程（含前端批量复制）本来就需要明文，
// 遮挡会把正常业务一起掐掉。
func TestRedemptionListingIsScopedToItsCreator(t *testing.T) {
	truncateTables(t)
	mine := &Redemption{UserId: 4101, Name: "qy-mine", Key: "aaaaaaaabbbbbbbbccccccccdddd1111",
		Quota: 100, Status: common.RedemptionCodeStatusEnabled, CreatedTime: common.GetTimestamp()}
	theirs := &Redemption{UserId: 4102, Name: "qy-theirs", Key: "aaaaaaaabbbbbbbbccccccccdddd2222",
		Quota: 100, Status: common.RedemptionCodeStatusEnabled, CreatedTime: common.GetTimestamp()}
	require.NoError(t, mine.Insert())
	require.NoError(t, theirs.Insert())

	t.Run("列表:非 root 只看得到自己发的码", func(t *testing.T) {
		rows, total, err := GetAllRedemptions(4101, 0, 50)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		require.Len(t, rows, 1)
		assert.Equal(t, "qy-mine", rows[0].Name)
	})

	t.Run("搜索:按末 4 位也搜不到别人的码", func(t *testing.T) {
		rows, total, err := SearchRedemptions(4101, "2222", "", 0, 50)
		require.NoError(t, err)
		assert.Zero(t, total, "搜得到就等于拿得到明文,末 4 位是客服拿到的唯一线索")
		assert.Empty(t, rows)

		rows, total, err = SearchRedemptions(4101, "1111", "", 0, 50)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total, "自己的码必须仍然搜得到,否则发码流程被掐断")
		require.Len(t, rows, 1)
		assert.Equal(t, "qy-mine", rows[0].Name)
	})

	t.Run("root(scope=0)看全量", func(t *testing.T) {
		_, total, err := GetAllRedemptions(0, 0, 50)
		require.NoError(t, err)
		assert.Equal(t, int64(2), total)
	})
}

// 「删除失效」此前是分桶上的一个洞:GetAll / Search / 按 id 改删都收在了发码人
// 那一桶里,唯独这一条整表扫。它删掉的正好是 used / disabled / 已过期那些行 ——
// 「这张码最后给了谁、什么时候被兑掉」的唯一记录 —— 于是任意 role=10 一按,
// 超管发出去的整批码的去向就没了,而列表里看不出少了什么。
func TestDeleteInvalidRedemptionsIsScopedToItsCreator(t *testing.T) {
	truncateTables(t)
	now := common.GetTimestamp()
	rows := []*Redemption{
		{UserId: 4201, Name: "qy-mine-used", Key: "aaaaaaaabbbbbbbbcccccccc00001111",
			Quota: 100, Status: common.RedemptionCodeStatusUsed, CreatedTime: now},
		{UserId: 4201, Name: "qy-mine-live", Key: "aaaaaaaabbbbbbbbcccccccc00002222",
			Quota: 100, Status: common.RedemptionCodeStatusEnabled, CreatedTime: now},
		{UserId: 4202, Name: "qy-theirs-used", Key: "aaaaaaaabbbbbbbbcccccccc00003333",
			Quota: 100, Status: common.RedemptionCodeStatusUsed, CreatedTime: now},
		{UserId: 4202, Name: "qy-theirs-expired", Key: "aaaaaaaabbbbbbbbcccccccc00004444",
			Quota: 100, Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now - 1, CreatedTime: now},
	}
	for _, row := range rows {
		require.NoError(t, row.Insert())
	}

	deleted, err := DeleteInvalidRedemptions(4201)
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted, "只该删掉 4201 自己那一张已用码")

	var names []string
	require.NoError(t, DB.Model(&Redemption{}).Order("name asc").Pluck("name", &names).Error)
	assert.Equal(t, []string{"qy-mine-live", "qy-theirs-expired", "qy-theirs-used"}, names)

	// root(scope=0)仍然横扫全量,这是它与普通管理员唯一的差别。
	deleted, err = DeleteInvalidRedemptions(0)
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)
	require.NoError(t, DB.Model(&Redemption{}).Order("name asc").Pluck("name", &names).Error)
	assert.Equal(t, []string{"qy-mine-live"}, names)
}
