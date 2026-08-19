package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// balance_invitee_count_test.go —— 「佣金余额总览」下发的下线数必须是真的。
//
// qy_commission_balance 上曾经有一个 invitee_count 列,注释声称"计佣路径顺手
// 维护",而它**从来没有任何写入方**(全仓只有字段定义和这一处读取)。于是全库
// 每一个真正有下线的推广人在这个字段上都是 0,而同一个数字在用户端与「佣金总表」
// 页是现算的 —— 三个页面两套口径,其中一套恒为 0。
//
// 现在一律现算,与 api_admin_users.go 同一份口径:qy_invite_relation 里
// unbound_at = 0 的行数。

func TestBalanceViewCountsRealDownlines(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionConfig(1))
	mainDB := useMainDB(t, &model.User{})
	seedUser(t, mainDB, 100, "boss", 0, 1000)
	seedUser(t, mainDB, 101, "nobody", 0, 1000)

	now := common.GetTimestamp()
	// 两个还在绑的下线 + 一个已解绑的(解绑不算,但关系行会留着)。
	for _, r := range []InviteRelation{
		{InviteeId: 201, InviterId: 100, InviteeRef: "ref-201", BoundAt: now, CreatedAt: now},
		{InviteeId: 202, InviterId: 100, InviteeRef: "ref-202", BoundAt: now, CreatedAt: now},
		{InviteeId: 203, InviterId: 100, InviteeRef: "ref-203", BoundAt: now, UnboundAt: now, CreatedAt: now},
	} {
		require.NoError(t, gdb.Create(&r).Error)
	}
	require.NoError(t, gdb.Create(&Balance{UserId: 100, AvailableQuota: 5000}).Error)
	require.NoError(t, gdb.Create(&Balance{UserId: 101}).Error)

	views := hydrateBalanceViews(context.Background(),
		[]Balance{{UserId: 100, AvailableQuota: 5000}, {UserId: 101}})
	require.Len(t, views, 2)

	byUser := map[int]balanceView{}
	for _, v := range views {
		byUser[v.UserId] = v
	}
	assert.Equal(t, 2, byUser[100].InviteeCount,
		"两个在绑下线;恒为 0 会让运营对着一个有余额的人问「他哪来的下线」")
	assert.Equal(t, "boss", byUser[100].Username)
	assert.True(t, byUser[100].UserResolved)
	assert.Equal(t, 0, byUser[101].InviteeCount, "真的没有下线时才是 0")
}
