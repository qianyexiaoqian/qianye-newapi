package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// redemption_lookup_and_delivery_test.go —— 兑换码这条链上两个"接口回 success、
// 事实却不是那样"的缺陷。
//
//  1. 掩码把线索也一起掩掉了。失败日志里只剩末 4 位(common.MaskCredential),
//     而后台的搜索只认名称和 id —— 用户报来的那 4 位在产品里根本搜不到,
//     "客服凭末 4 位对上库里那一行"这句话没有落点。
//
//  2. 余额码的加额度只看 Error、不看 RowsAffected。收款人那一行不存在时
//     (会话还在、用户已被软删)UPDATE 匹配 0 行且不报错,事务照常提交:
//     码被 CAS 消耗、状态写成 used,面值一分钱没进任何人的钱包,而接口回 success。
//     用户手里的码作废了、东西没拿到,而且没有任何一处会报错。

func TestSearchRedemptionsFindsACodeByItsMaskedSuffix(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	})

	// 三张码的末 4 位互不相同,其中两张共享同一个前缀,防止用例被"随便命中一行"蒙混过去。
	rows := []Redemption{
		{Id: 8801, Name: "batch-a", Key: "0f1e2d3c4b5a69788796a5b4c3d2e1a4", Status: common.RedemptionCodeStatusEnabled},
		{Id: 8802, Name: "batch-a", Key: "0f1e2d3c4b5a69788796a5b4c3d2beef", Status: common.RedemptionCodeStatusEnabled},
		{Id: 8803, Name: "batch-b", Key: "99887766554433221100aabbccdde1a4", Status: common.RedemptionCodeStatusUsed},
	}
	for i := range rows {
		require.NoError(t, DB.Create(&rows[i]).Error)
	}

	for _, tc := range []struct {
		name    string
		keyword string
		wantIds []int
	}{
		{
			// 客服拿到的就是这一串:common.MaskCredential("...beef") == "***beef"。
			name:    "末 4 位定位到唯一一行",
			keyword: "beef",
			wantIds: []int{8802},
		},
		{
			// 16 bit 的后缀会撞车,这是掩码本身的代价:两行都要返回,由人按名称/时间分辨。
			name:    "末 4 位撞车时两行都要给出来",
			keyword: "e1a4",
			wantIds: []int{8803, 8801},
		},
		{
			name:    "报得更长就收敛回唯一一行",
			keyword: "c3d2e1a4",
			wantIds: []int{8801},
		},
		{
			// 用户抄码时大小写不可控,而生成的码恒为小写十六进制。
			// 三个数据库的 LIKE 大小写规则并不一致,判据必须自己统一到小写。
			name:    "大写输入同样能对上",
			keyword: "C3D2E1A4",
			wantIds: []int{8801},
		},
		{
			// 前缀不是掩码留下的东西,拿它当后缀搜必须搜不到 —— 否则这条判据
			// 退化成了"包含即命中",等于把整张 key 列变成模糊搜索面。
			name:    "前缀不算后缀",
			keyword: "0f1e2d3c",
			wantIds: []int{},
		},
		{
			// LIKE 的单字符通配符。判据只收十六进制,`_` 进不了 key 那一支 ——
			// 少了这层过滤,`_1a4` 会被拼成 `LIKE '%_1a4'` 而同时命中 8801 与 8803,
			// 也就是把 key 列变成一个可以从搜索框自由构造的模糊匹配面。
			// (名称那一支的通配符是上游行为,这里的关键字对任何名称都不匹配。)
			name:    "LIKE 通配符不能从搜索框进到 key 上",
			keyword: "_1a4",
			wantIds: []int{},
		},
		{
			// 3 位在 4096 的空间里命中太多,给不出可用的答案;名称仍然照常匹配。
			name:    "短于 4 位不进后缀判据",
			keyword: "1a4",
			wantIds: []int{},
		},
		{
			name:    "名称搜索没有被后缀判据挤掉",
			keyword: "batch-b",
			wantIds: []int{8803},
		},
		{
			// 纯数字关键字同时是 id 候选,两条判据必须都在。
			name:    "数字关键字仍然按 id 命中",
			keyword: "8801",
			wantIds: []int{8801},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := SearchRedemptions(0, tc.keyword, "", 0, 50)
			require.NoError(t, err)
			gotIds := make([]int, 0, len(got))
			for _, row := range got {
				gotIds = append(gotIds, row.Id)
			}
			assert.Equal(t, tc.wantIds, gotIds)
			assert.Equal(t, int64(len(tc.wantIds)), total, "总数必须与明细同源")
		})
	}
}

// 收款人不存在时,兑换必须整笔失败,而不是"码被吃掉、钱没到账、接口回 success"。
func TestRedeemQuotaCodeStaysLiveWhenTheCreditCannotLand(t *testing.T) {
	userId, key := setupRedeemFixture(t, &Redemption{Quota: 500, ProductType: RedemptionProductQuota})

	// 会话仍然握着这个 id,用户行已经被软删 —— UPDATE 的 WHERE 里带
	// `deleted_at IS NULL`,于是匹配 0 行、不报错。
	require.NoError(t, DB.Delete(&User{}, "id = ?", userId).Error)

	result, err := Redeem(key, userId)
	require.Error(t, err, "收不到钱的兑换不能算成功")
	assert.Nil(t, result)

	var stored Redemption
	require.NoError(t, DB.First(&stored, "`key` = ?", key).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, stored.Status,
		"发不出货就不能消耗兑换码 —— 否则用户手里那张码作废了,东西没拿到")
	assert.Zero(t, stored.RedeemedTime)
	assert.Zero(t, stored.UsedUserId)

	// 码还活着这件事必须是可用的,不只是状态列好看:用户恢复之后原样能兑。
	require.NoError(t, DB.Unscoped().Model(&User{}).Where("id = ?", userId).
		Update("deleted_at", nil).Error)
	result, err = Redeem(key, userId)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 500, result.Quota)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)
}
