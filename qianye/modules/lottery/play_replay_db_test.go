package lottery

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// play_replay_db_test.go —— 玩法隐藏时,「原样重放」必须与「新参与」分开。
//
// 缺陷形状:玩法闸门放在事务前的读票**之前**,不区分新单与重放。
// 运营把某种玩法切到隐藏之后,一个已经扣过钱、票已进哈希链的用户如果还在
// 重试同一个 client_request_id,拿到的是 409「暂不受理新的参与」而不是原始回执。
// 那句话暗示什么都没发生,而钱已经扣了 —— 与本模块自己写下的口径
// (「幂等键存在的唯一理由就是让重放拿回原单」)直接冲突。
//
// 判据落在 loadEntryByIdemKey 上:这个幂等键在本场活动上已经有票 = 重放。
// 读票本身的行为(命中 / 未命中 / 另一场不命中)由 entry_replay_db_test.go 覆盖,
// 这里只钉接线的**顺序**。

// ChargeEntry 里那道闸门必须**先问是不是重放**再决定拒不拒。
// 这一条从源码层钉住接线:纯函数写对了、闸门没接上是本仓的头号形状。
func TestPlayGateAsksWhetherItIsAReplayBeforeRejecting(t *testing.T) {
	raw, err := os.ReadFile("entry.go")
	require.NoError(t, err)
	src := string(raw)

	start := strings.Index(src, "func ChargeEntry(")
	require.GreaterOrEqual(t, start, 0, "找不到 ChargeEntry")
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end]
	}

	const gate = "if !effectiveCtx(ctx).playShown(playOf(act.Kind, act.DrawMode)) {"
	gateAt := strings.Index(body, gate)
	require.GreaterOrEqual(t, gateAt, 0, "玩法闸门整块不见了")
	lookupAt := strings.Index(body, "loadEntryByIdemKey(")
	require.GreaterOrEqual(t, lookupAt, 0,
		"闸门之前必须先按幂等键读票;直接 return errPlayHidden 会把已经扣过钱的那一笔也挡掉")
	assert.Less(t, lookupAt, gateAt, "读票必须排在玩法闸门之前:重放拿回原票,只有新参与才被拒")
	assert.Contains(t, body[gateAt:], "errPlayHidden", "新参与仍然必须被拒 —— 只放行重放")
}
