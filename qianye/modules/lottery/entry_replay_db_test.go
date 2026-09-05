package lottery

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/modules/stardust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// entry_replay_db_test.go —— 原样重放一次已成交的参与,必须拿回原来那张回执。
//
// 这是幂等键存在的**唯一**理由。用户点一次按钮、网络超时、客户端重试,服务端
// 若当成两次参与各扣一笔,那正是把批量放在服务端也挡不住的资损形状。
//
// 单事务之下重放有两个落点,两个都要钉住:
//
//	事务前:按 uk(act_id, idem_key) 读到票 → 比指纹 → 原票 / 409;
//	事务内:两路同键请求并发,晚到的那一路在 Debit 的幂等行或票的唯一键上撞回
//	        errEntryReplayRace,整笔回滚(seq 不占、流水不留)后回到事务前的分支。

// 指纹一致的重放拿回原票;换了要素的重放 409 —— 两者都不再动钱。
func TestReplayEntry_SameFingerprintReturnsOriginalAndDifferentOneConflicts(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	original := seedSuccessEntry(t, gdb, act, 4242, 1000)
	require.NotEmpty(t, original.Fingerprint, "落库的票必须带指纹,否则重放无从比对")

	prior, err := loadEntryByIdemKey(context.Background(), gdb, act.Id, original.IdemKey)
	require.NoError(t, err)
	require.NotNil(t, prior)

	same := entryFingerprint(act, 4242, 1000, 0, "")
	got, err := replayEntry(prior, same, 4242)
	require.NoError(t, err, "原样重放必须拿回原票,而不是 409 或 500")
	assert.Equal(t, original.EntryNo, got.EntryNo)
	assert.Equal(t, original.Seq, got.Seq)
	assert.Equal(t, original.ChainHash, got.ChainHash)
	// 首次扣费时记下的余额前后值不能被重放冲掉。
	assert.EqualValues(t, 1000, got.QuotaBefore)
	assert.EqualValues(t, 0, got.QuotaAfter)

	for name, fp := range map[string]string{
		"换金额": entryFingerprint(act, 4242, 9000, 0, ""),
		"换选项": entryFingerprint(act, 4242, 1000, 3, ""),
		"换号码": entryFingerprint(act, 4242, 1000, 0, "01,02,03|01"),
		"换用户": entryFingerprint(act, 4243, 1000, 0, ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := replayEntry(prior, fp, 4242)
			require.ErrorIs(t, err, errIdemConflict, "换参重放必须 409")
		})
	}

	// 全新的幂等键在本场没有票;同一个 crid 在**另一场**也是新参与(键带 act_no 前缀)。
	none, err := loadEntryByIdemKey(context.Background(), gdb, act.Id, buildIdemKey(act.ActNo, "fresh"))
	require.NoError(t, err)
	assert.Nil(t, none)
	other := seedActivity(t, gdb, nil)
	none, err = loadEntryByIdemKey(context.Background(), gdb, other.Id, original.IdemKey)
	require.NoError(t, err)
	assert.Nil(t, none, "同一个 crid 在另一场活动上是另一笔参与")
}

// 同一个幂等键第二次走进参与事务:整笔回滚,seq 不占、流水不留、余额不动。
//
// 这是并发重放的落点。两路同键请求里晚到的那一路,要么在 Debit 撞上账本的幂等行
// (Inserted=false),要么在票的唯一键上撞 IsDuplicateKey —— 两种形状都必须变成
// errEntryReplayRace,而不是一张多出来的票或一笔多扣的钱。
func TestSettleEntryTx_SecondRunWithSameKeyRollsBackCleanly(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	first := seedSuccessEntry(t, gdb, act, 77, 1000)
	seedStardust(t, gdb, 77, 5000)

	dup := &Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: first.IdemKey,
		UserId: 77, UserRef: first.UserRef, Amount: 1000, Status: EntrySuccess,
		CreatedAt: common.GetTimestamp(), Fingerprint: first.Fingerprint,
	}
	cur := loadAct(t, gdb, act.Id)
	err := gdb.Transaction(func(tx *gorm.DB) error {
		return settleEntryTx(tx, cur, Rules{}, dup, 0)
	})
	require.ErrorIs(t, err, errEntryReplayRace)

	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 1, after.EntrySeq, "撞键的那次不许占序号 —— 占了就是链上一个无主的空洞")
	assert.Equal(t, 1, after.ActiveCount)
	assert.EqualValues(t, 1000, after.PoolQuota)
	assert.Equal(t, first.ChainHash, after.ChainHead)
	assert.EqualValues(t, 5000, stardustOf(t, gdb, 77), "撞键的那次一分钱都不许扣")

	var stakes []stardust.Ledger
	require.NoError(t, gdb.Where("user_id = ? AND kind = ?", 77, string(stardust.KindLotStake)).
		Find(&stakes).Error)
	assert.Len(t, stakes, 1, "账本上同一个幂等键只可能有一行")
	var tickets int64
	require.NoError(t, gdb.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&tickets).Error)
	assert.EqualValues(t, 1, tickets)
}

// 同一个 (act_id, code) 的第二次检出必须刷新 detail,而不是被静默丢弃。
//
// qy_lot_flag 是本模块唯一的事后篡改出口,detail 里写的是当场算出来的数字。
// 只去重不更新,运营看到的是一个早已不成立的旧值;而 auditFinishedChains 让
// finished 活动也进入持续复核,finished 是永久态,这条 flag 也就永久停在那个旧值上。
func TestUpsertFlagDedupesWithoutFreezingTheDetail(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	first := "重算奖池 11500 与物化 3000 不一致"
	changed, err := upsertFlag(gdb, act.Id, FlagPoolMismatch, first)
	require.NoError(t, err)
	assert.True(t, changed, "首次检出必须报告为「新的」,调用方据此决定要不要打日志/审计")
	var seeded Flag
	require.NoError(t, gdb.Where("act_id = ?", act.Id).Take(&seeded).Error)

	// 同一条消息重复检出:不刷屏,也不产生无谓的写。
	changed, err = upsertFlag(gdb, act.Id, FlagPoolMismatch, first)
	require.NoError(t, err)
	assert.False(t, changed,
		"逐字相同的重复检出必须报告为「不是新的」——suspendReveal 靠这个判据才不会每 15 秒往审计表追加一条一模一样的 fail 行")
	// 第二次篡改,重算值变了。
	refreshed := "重算奖池 10277 与物化 3000 不一致"
	changed, err = upsertFlag(gdb, act.Id, FlagPoolMismatch, refreshed)
	require.NoError(t, err)
	assert.True(t, changed, "detail 变了就是新情况,必须重新告警")

	var rows []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagPoolMismatch).Find(&rows).Error)
	require.Len(t, rows, 1, "去重仍然成立:同一类异常不刷屏")
	assert.Equal(t, refreshed, rows[0].Detail,
		"第二次检出算出的新数字必须落地,否则红点上写的是一个已经不成立的值")
	assert.Equal(t, seeded.CreatedAt, rows[0].CreatedAt,
		"首次检出时刻不能被后来的刷新抹掉")
	assert.False(t, rows[0].Resolved)

	// 另一类异常照旧独立成行。
	_, err = upsertFlag(gdb, act.Id, FlagChainDrift, "链尾对不上")
	require.NoError(t, err)
	var n int64
	require.NoError(t, gdb.Model(&Flag{}).Where("act_id = ?", act.Id).Count(&n).Error)
	assert.Equal(t, int64(2), n)
}

// 已处理的异常必须让位给同一类的**新**检出。
//
// 去重条件是 (act_id, code, resolved=false)。若 resolved=true 的历史行也参与
// 去重,运营关掉一条异常之后这场活动这一类就永久哑火 —— 而 finished 是永久态,
// 历史公正查询的全部内容都在那里。
func TestResolvedFlagDoesNotSuppressTheNextDetection(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	_, err := upsertFlag(gdb, act.Id, FlagChainDrift, "第一次")
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&Flag{}).Where("act_id = ?", act.Id).
		Updates(map[string]any{"resolved": true, "resolved_by": 1301, "resolved_at": common.GetTimestamp()}).Error)

	changed, err := upsertFlag(gdb, act.Id, FlagChainDrift, "处理完之后又出事了")
	require.NoError(t, err)
	assert.True(t, changed, "已处理之后的新检出必须重新告警")

	var rows []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagChainDrift).
		Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "已处理的行必须让位,否则关掉一条异常之后这一类就永久哑火")
	assert.True(t, rows[0].Resolved)
	assert.False(t, rows[1].Resolved)
	assert.Equal(t, "处理完之后又出事了", rows[1].Detail)
}
