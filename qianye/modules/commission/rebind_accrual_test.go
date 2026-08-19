package commission

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRebindSendsTheRestOfTheDayToTheNewInviter 钉住换绑之后的钱归谁。
//
// 消费日聚合的幂等键原来不含 inviter_id,而 ON CONFLICT 的 DoUpdates 只累加
// base_quota / gross_amount —— 也不含 inviter_id。于是换绑当天只要费率、分组、
// 法币比例都没变(换绑最典型的场景恰恰不改这三样),新上线的消费会撞上旧上线
// 那一行的唯一索引,金额被原子累加进去而 inviter_id 保持旧值:钱结结实实发给了
// 前一个上线,三条恒等式却全部成立,没有任何降级计数器会响。
//
// 断言分两层,缺一不可:
//   - 行数与归属:换绑之后必须为新上线新落一行,旧行一分不再增长;
//   - 金额:换绑之前那段仍归旧上线(他确实挣到了),之后那段归新上线。
func TestRebindSendsTheRestOfTheDayToTheNewInviter(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, commissionRateConfig("10", "5"))
	useMoneyGlobals(t, 6, 500000)

	at := common.GetTimestamp()
	const invitee, oldInviter, newInviter = 900, 42, 43
	// 两个上线同分组:法币折算比例按上线分组解析,同分组才能把
	// "比例不同所以落新行"这条侥幸路径排除掉,让唯一的变量是 inviter_id。
	cacheUser(oldInviter, 0, "promoter")
	cacheUser(newInviter, 0, "promoter")

	cacheUser(invitee, oldInviter, "vip")
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: invitee, Quota: 10000, At: at}))

	// 换绑:同一个下线改挂到另一个上线名下,费率/分组/比例一律不动。
	cacheUser(invitee, newInviter, "vip")
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: invitee, Quota: 10000, At: at}))

	var rows []Accrual
	require.NoError(t, gdb.Where("invitee_id = ?", invitee).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "换绑之后必须为新上线落新的一行,不能并进旧上线那一行")

	assert.Equal(t, oldInviter, rows[0].InviterId)
	assert.Equal(t, int64(10000), rows[0].BaseQuota, "换绑之前那一行不许再增长")
	assert.Equal(t, "500", rows[0].GrossAmount.String())

	assert.Equal(t, newInviter, rows[1].InviterId, "换绑之后的消费必须记在新上线名下")
	assert.Equal(t, int64(10000), rows[1].BaseQuota)
	assert.Equal(t, "500", rows[1].GrossAmount.String())

	// 每一行仍然自洽,而且两行加起来正好是两笔消费应得的佣金 —— 换绑既不吞钱
	// 也不多发,只是把归属切开。
	for _, r := range rows {
		assert.Equal(t, calcGross(r.BaseQuota, r.RateUnits).String(), r.GrossAmount.String())
	}

	// 再消费一次,必须继续并进**新上线**那一行,而不是每次都新开一行。
	require.NoError(t, accrueConsume(context.Background(),
		consumeEvent{InviteeId: invitee, Quota: 10000, At: at}))
	require.NoError(t, gdb.Where("invitee_id = ?", invitee).Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "上线没变时同一天仍必须聚合成一行")
	assert.Equal(t, int64(20000), rows[1].BaseQuota)
}
