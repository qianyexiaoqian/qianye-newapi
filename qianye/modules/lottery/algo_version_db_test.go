package lottery

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// algo_version_db_test.go —— 协议版本在草稿/发布两条路径上的一致性。
//
// 这一组守的是同一件事:**algo 与 spec_hash 必须永远来自同一个版本**。
// 两者一旦错位,承诺照样能算出来、开奖也照常跑,只有拿着 verify.py 的用户会
// 算出一个对不上的哈希 —— 而那时活动已经开完了,没有任何补救余地。

// 草稿的 algo 必须与 spec_hash 同一次落库。
//
// 这是一个真实的错位形状:本轮之前建的草稿(algo=lot-v1)被改一次之后,
// buildActivity 会按 lot-v2 算 spec_hash,而更新语句如果不带上 algo 列,
// 库里就会存着"v2 形状的 spec_hash + algo=lot-v1"。
func TestDraftUpdatesCarryAlgoAlongsideSpecHash(t *testing.T) {
	fields := draftUpdates(&Activity{Algo: AlgoV2, SpecHash: "sh"})
	assert.Equal(t, AlgoV2, fields["algo"],
		"algo 必须与 spec_hash 同一次写进去 —— 两者错位时,只有用户的验证脚本会发现")
	assert.Equal(t, "sh", fields["spec_hash"])
}

// 发布用的是**草稿行上写着的那个版本**,而不是"当前最新版本"。
//
// 按最新版本发布会让一份躺了两天的草稿在发布瞬间换掉原像形状:
// 它的 spec_hash 是建草稿那天用旧版算的,而承诺会用新版的原像去覆盖 ——
// 两者从此永远对不上。
func TestPublishUsesTheAlgoFrozenOnTheDraft(t *testing.T) {
	assert.Equal(t, AlgoV1, publishAlgo(&Activity{Algo: AlgoV1}),
		"存量草稿必须按 lot-v1 发布,它的 spec_hash 就是用 v1 原像算的")
	assert.Equal(t, AlgoV2, publishAlgo(&Activity{Algo: AlgoV2}))
	assert.Equal(t, AlgoV1, publishAlgo(&Activity{Algo: ""}),
		"空串是更早的存量行,按 v1 处理")
}

// 每一个允许发布的定档方式,在 qianye/docs/lottery-verify.py 里都必须有
// 对应的复算分支。
//
// 这条断言是那份文档纪律的机器化:再加一种玩法时,**先补验证脚本再放开这里**。
// 一份验不了的证据链比没有证据链更糟,因为它看起来像是可验证的。
func TestOnlyVerifiableDrawModesArePublishable(t *testing.T) {
	verifiable := []string{"", DrawModeRank, DrawModeProb, DrawModeBall}
	for _, mode := range verifiable {
		act := &Activity{Algo: AlgoV2, Kind: KindDraw, DrawMode: mode, SeriesId: 1}
		require.NoErrorf(t, checkAlgoPublishable(act), "定档方式 %q 应当可发布", mode)
	}

	unknown := &Activity{Algo: AlgoV2, Kind: KindDraw, DrawMode: "roulette", SeriesId: 1}
	require.Error(t, checkAlgoPublishable(unknown),
		"没有验证脚本分支的玩法必须拒绝发布")

	bad := &Activity{Algo: "lot-v9", Kind: KindDraw}
	require.Error(t, checkAlgoPublishable(bad), "未知协议版本必须拒绝发布")
}

// spec 原像必须按版本分派,而且两版算出来的哈希必须不同。
func TestPrizeSpecLineFollowsTheActivityAlgo(t *testing.T) {
	p := Prize{Tier: 1, Name: "头奖", AmountQuota: 5000, Count: 1,
		PrizeType: PrizeTypeQuota, WinPpm: 1000}

	v1 := prizeSpecLineOf(AlgoV1, p)
	v2 := prizeSpecLineOf(AlgoV2, p)
	assert.NotEqual(t, v1, v2)
	assert.Equal(t, prizeSpecLineV1(1, "头奖", 5000, 1), v1,
		"v1 分派必须原样落到冻结的那个函数上")
	// v1 的行里根本没有概率这一列 —— 这正是"存量活动的原像一个字节都没变"。
	assert.NotContains(t, v1, "1000\x1f")
}
