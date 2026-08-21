package lottery

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// play_replay_db_test.go —— 玩法隐藏时,「原样重放」必须与「新参与」分开。
//
// 缺陷形状:玩法闸门放在 twophase.Execute **之前**,不区分新单与重放。
// 运营把某种玩法切到隐藏之后,一个已经扣过钱、票已进哈希链的用户如果还在
// 重试同一个 client_request_id,拿到的是 409「暂不受理新的参与」而不是原始回执。
// 那句话暗示什么都没发生,而钱已经扣了 —— 与本模块自己写下的口径
// (「幂等键存在的唯一理由就是让重放拿回原单」)直接冲突。
//
// 判据落在 entryExistsByIdemKey 上:这个幂等键在本场活动上已经有票 = 重放。

func newReplayTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := newFundTestDB(t)
	require.NoError(t, gdb.AutoMigrate(&qymodel.FundOrder{}))
	return gdb
}

func TestEntryIdemKeyLookupSeparatesReplayFromNewEntry(t *testing.T) {
	gdb := newReplayTestDB(t)
	act := seedActivity(t, gdb, nil)
	ctx := context.Background()

	const crid = "qy-replay-crid-1"
	key := buildIdemKey(act.ActNo, crid)

	// 全新的 crid:库里没有票,必须判成「新参与」。
	replay, err := entryExistsByIdemKey(ctx, gdb, key)
	require.NoError(t, err)
	assert.False(t, replay, "全新的 crid 不能被当成重放放行,否则闸门等于没有")

	// 落一张票(与线上同形:参与行与 pending 资金单同一个扩展库事务)。
	require.NoError(t, gdb.Create(&Entry{
		EntryNo: newEntryNo(), ActId: act.Id, IdemKey: key,
		UserId: 1001, Amount: act.StakeQuota, Status: EntrySuccess,
		CreatedAt: common.GetTimestamp(),
	}).Error)

	replay, err = entryExistsByIdemKey(ctx, gdb, key)
	require.NoError(t, err)
	assert.True(t, replay, "同一个 crid 已经有票了,这就是重放")

	// 同一个 crid、**另一场**活动仍然是新参与:幂等键带 act_no 前缀。
	other := seedActivity(t, gdb, nil)
	replay, err = entryExistsByIdemKey(ctx, gdb, buildIdemKey(other.ActNo, crid))
	require.NoError(t, err)
	assert.False(t, replay, "同一个 crid 在另一场活动上是另一笔参与")

	// 读失败一律按「不是重放」:宁可让重试再报一次 409,
	// 也不能因为一次查询故障把闸门整个放开。
	require.NoError(t, gdb.Migrator().DropTable(&Entry{}))
	replay, err = entryExistsByIdemKey(ctx, gdb, key)
	assert.Error(t, err)
	assert.False(t, replay)
}

// ChargeEntry 里那道闸门必须**先问是不是重放**再决定拒不拒。
// 这一条从源码层钉住接线:纯函数写对了、闸门没接上是本仓的头号形状。
func TestPlayGateAsksWhetherItIsAReplayBeforeRejecting(t *testing.T) {
	raw, err := os.ReadFile("entry.go")
	require.NoError(t, err)
	src := string(raw)

	const gate = "if !effectiveCtx(ctx).playShown(playOf(act.Kind, act.DrawMode)) {"
	idx := strings.Index(src, gate)
	require.GreaterOrEqual(t, idx, 0, "玩法闸门整块不见了")
	end := idx + 1400
	if end > len(src) {
		end = len(src)
	}
	body := src[idx:end]

	assert.Contains(t, body, "entryExistsByIdemKey",
		"闸门必须先问「这是不是原样重放」;直接 return errPlayHidden 会把已经扣过钱的那一笔也挡掉")
	assert.Contains(t, body, "errPlayHidden",
		"新参与仍然必须被拒 —— 只放行重放")
}
