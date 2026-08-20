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
