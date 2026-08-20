package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redemption_used_immutable_test.go —— 已兑换是终态,面值同样不可改。
//
// status_only 那一支已经把"已兑换不能翻回启用"锁死,但非 status 的那一支完全
// 不看状态:同一个接口换一个 query 参数就能改一张已核销的码的 quota。钱在兑换
// 那一刻按旧面值发完了,改这一列不退不补,只会让三处记录彼此打架 —— 兑换码行上
// 写着 B、充值日志里记的是 A、用户钱包里进的是 A。事后对账时无从判断哪个是事实,
// 而 status 这道闸看上去还好端端地立着。

func TestUpdateRedemptionRefusesToRewriteAUsedCodesQuota(t *testing.T) {
	db := setupRedemptionAdminTest(t)
	seedRedemptionRow(t, db, &model.Redemption{
		Id: 990101, UserId: 1, Key: "qyctlkey00000000000000000000imm0",
		Status: common.RedemptionCodeStatusUsed, Name: "qy-imm", Quota: 1_000_000,
		CreatedTime: common.GetTimestamp(), RedeemedTime: common.GetTimestamp(), UsedUserId: 777,
		ProductType: model.RedemptionProductQuota,
	})

	for _, tc := range []struct {
		name        string
		body        string
		wantSuccess bool
		wantQuota   int
		wantName    string
	}{
		{
			name:        "改大面额被拒",
			body:        `{"id":990101,"name":"qy-imm","quota":9000000,"expired_time":0}`,
			wantSuccess: false,
			wantQuota:   1_000_000,
			wantName:    "qy-imm",
		},
		{
			name:        "改小面额同样被拒",
			body:        `{"id":990101,"name":"qy-imm","quota":1,"expired_time":0}`,
			wantSuccess: false,
			wantQuota:   1_000_000,
			wantName:    "qy-imm",
		},
		{
			// 这道闸只锁面值。改名是运营给批次打标记的正常动作,锁掉它等于
			// 把一条与钱无关的操作也一起禁了 —— 面值原样送回时必须放行。
			name:        "原样送回面值时改名仍然可以",
			body:        `{"id":990101,"name":"qy-imm-renamed","quota":1000000,"expired_time":0}`,
			wantSuccess: true,
			wantQuota:   1_000_000,
			wantName:    "qy-imm-renamed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := callUpdateRedemption(t, "", tc.body)
			require.Equal(t, http.StatusOK, rec.Code)
			if tc.wantSuccess {
				assert.Contains(t, rec.Body.String(), `"success":true`)
			} else {
				assert.Contains(t, rec.Body.String(), `"success":false`,
					"已核销的码的面值是历史事实,不是一个可编辑的字段")
			}

			var stored model.Redemption
			require.NoError(t, db.Where("id = ?", 990101).First(&stored).Error)
			assert.Equal(t, tc.wantQuota, stored.Quota)
			assert.Equal(t, tc.wantName, stored.Name)
			assert.Equal(t, common.RedemptionCodeStatusUsed, stored.Status)
			assert.Equal(t, 777, stored.UsedUserId, "核销痕迹不得被覆盖")
		})
	}
}

// 未兑换的码照常可以改面值 —— 这道闸挡的是终态,不是"兑换码不许编辑"。
func TestUpdateRedemptionStillAllowsQuotaEditsOnALiveCode(t *testing.T) {
	db := setupRedemptionAdminTest(t)
	seedRedemptionRow(t, db, &model.Redemption{
		Id: 990102, UserId: 1, Key: "qyctlkey00000000000000000000live",
		Status: common.RedemptionCodeStatusEnabled, Name: "qy-live", Quota: 1_000_000,
		CreatedTime: common.GetTimestamp(), ProductType: model.RedemptionProductQuota,
	})

	rec := callUpdateRedemption(t, "", `{"id":990102,"name":"qy-live","quota":2000000,"expired_time":0}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"success":true`)

	var stored model.Redemption
	require.NoError(t, db.Where("id = ?", 990102).First(&stored).Error)
	assert.Equal(t, 2_000_000, stored.Quota)
}
