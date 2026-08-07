package lottery

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ball_golden_test.go —— 双色球摇号原语的**跨实现**黄金向量。
//
// ══════════════════════ 为什么这个文件必须存在 ══════════════════════
//
// BallDraw 是整套双色球唯一的随机原语,开出的七个号码直接决定钱怎么发。
// 在此之前它一个测试都没有:go test 全绿、前端的概率测试也全绿(那算的是
// 组合数,与摇号实现无关),而只要有人动一动 sort.SliceStable 的平局分支、
// 或者把 deci(b) 换成两位补零,第三方拿 qianye/docs/lottery-verify.py 复算
// 就会得到**另一组开奖号** —— 而那时活动已经开完、钱已经发完,谁都举不出证。
//
// rank / prob 两支有 fairness_test.go / fairness_v2_test.go 的黄金向量兜着,
// ball 这一支不能是唯一没有的。
//
// ══════════════════════ 向量是怎么来的 ══════════════════════
//
// **不是从本实现里 dump 出来的**(那样只能证明它等于它自己)。下面每一行都由
// qianye/docs/lottery-verify.py 的 ball_draw 独立算出 —— 纯标准库 hmac/hashlib,
// 与 Go 侧没有共享一行代码。所以这张表钉死的是「Go 与验证脚本对同一个种子摇出
// 同一组号码」,也就是三方一致性里唯一会被人真正拿去复核的那一条。
//
// 覆盖:4 个 final_seed(含 "00" 这种奇数长度以外的边界与全零)× 2 个 act_no ×
// 5 组号池(含上界 36/8 与 16/2)× 红蓝双色 = 80 个向量。
func TestBallDrawMatchesTheVerifyScriptGoldenVectors(t *testing.T) {
	cases := []struct {
		seed  string
		actNo string
		color string
		poolN int
		pickK int
		want  string // 两位补零、逗号分隔、升序 —— 与 FormatPick 的单侧格式一致
	}{
		{"00", "LOT20260101ABCD", "red", 6, 1, "04"},
		{"00", "LOT20260101ABCD", "blue", 6, 1, "02"},
		{"00", "LOT20260101ABCD", "red", 12, 3, "04,07,09"},
		{"00", "LOT20260101ABCD", "blue", 12, 3, "08,11,12"},
		{"00", "LOT20260101ABCD", "red", 33, 6, "04,07,13,16,20,22"},
		{"00", "LOT20260101ABCD", "blue", 33, 6, "08,11,15,20,22,31"},
		{"00", "LOT20260101ABCD", "red", 36, 8, "04,07,13,16,20,22,35,36"},
		{"00", "LOT20260101ABCD", "blue", 36, 8, "08,11,15,20,22,31,35,36"},
		{"00", "LOT20260101ABCD", "red", 16, 2, "13,16"},
		{"00", "LOT20260101ABCD", "blue", 16, 2, "08,11"},
		{"00", "LOT20991231ZZZZ", "red", 6, 1, "01"},
		{"00", "LOT20991231ZZZZ", "blue", 6, 1, "03"},
		{"00", "LOT20991231ZZZZ", "red", 12, 3, "01,08,10"},
		{"00", "LOT20991231ZZZZ", "blue", 12, 3, "03,11,12"},
		{"00", "LOT20991231ZZZZ", "red", 33, 6, "01,08,19,21,22,26"},
		{"00", "LOT20991231ZZZZ", "blue", 33, 6, "03,11,12,13,28,29"},
		{"00", "LOT20991231ZZZZ", "red", 36, 8, "01,08,18,19,21,22,25,26"},
		{"00", "LOT20991231ZZZZ", "blue", 36, 8, "03,11,12,13,17,28,29,34"},
		{"00", "LOT20991231ZZZZ", "red", 16, 2, "01,08"},
		{"00", "LOT20991231ZZZZ", "blue", 16, 2, "03,12"},
		{"a3f1", "LOT20260101ABCD", "red", 6, 1, "02"},
		{"a3f1", "LOT20260101ABCD", "blue", 6, 1, "05"},
		{"a3f1", "LOT20260101ABCD", "red", 12, 3, "02,11,12"},
		{"a3f1", "LOT20260101ABCD", "blue", 12, 3, "05,07,10"},
		{"a3f1", "LOT20260101ABCD", "red", 33, 6, "02,09,11,12,19,26"},
		{"a3f1", "LOT20260101ABCD", "blue", 33, 6, "07,10,16,24,28,31"},
		{"a3f1", "LOT20260101ABCD", "red", 36, 8, "02,09,11,12,19,25,26,28"},
		{"a3f1", "LOT20260101ABCD", "blue", 36, 8, "07,10,16,23,24,28,31,32"},
		{"a3f1", "LOT20260101ABCD", "red", 16, 2, "11,12"},
		{"a3f1", "LOT20260101ABCD", "blue", 16, 2, "10,16"},
		{"a3f1", "LOT20991231ZZZZ", "red", 6, 1, "04"},
		{"a3f1", "LOT20991231ZZZZ", "blue", 6, 1, "05"},
		{"a3f1", "LOT20991231ZZZZ", "red", 12, 3, "04,07,11"},
		{"a3f1", "LOT20991231ZZZZ", "blue", 12, 3, "05,09,12"},
		{"a3f1", "LOT20991231ZZZZ", "red", 33, 6, "11,15,23,24,28,31"},
		{"a3f1", "LOT20991231ZZZZ", "blue", 33, 6, "05,09,12,17,23,27"},
		{"a3f1", "LOT20991231ZZZZ", "red", 36, 8, "04,11,15,23,24,27,28,31"},
		{"a3f1", "LOT20991231ZZZZ", "blue", 36, 8, "04,05,09,12,15,17,23,27"},
		{"a3f1", "LOT20991231ZZZZ", "red", 16, 2, "11,15"},
		{"a3f1", "LOT20991231ZZZZ", "blue", 16, 2, "05,12"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "red", 6, 1, "04"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "blue", 6, 1, "06"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "red", 12, 3, "01,04,09"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "blue", 12, 3, "04,06,09"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "red", 33, 6, "04,14,18,22,23,31"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "blue", 33, 6, "06,09,21,22,29,31"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "red", 36, 8, "04,14,18,22,23,25,31,36"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "blue", 36, 8, "04,06,09,21,22,29,31,32"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "red", 16, 2, "04,14"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20260101ABCD", "blue", 16, 2, "06,09"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "red", 6, 1, "05"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "blue", 6, 1, "06"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "red", 12, 3, "05,06,09"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "blue", 12, 3, "02,06,08"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "red", 33, 6, "05,15,16,17,27,29"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "blue", 33, 6, "14,24,27,30,32,33"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "red", 36, 8, "05,15,16,17,19,26,27,29"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "blue", 36, 8, "14,24,25,27,30,32,33,36"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "red", 16, 2, "05,16"},
		{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "LOT20991231ZZZZ", "blue", 16, 2, "13,14"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "red", 6, 1, "04"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "blue", 6, 1, "02"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "red", 12, 3, "04,07,09"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "blue", 12, 3, "08,11,12"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "red", 33, 6, "04,07,13,16,20,22"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "blue", 33, 6, "08,11,15,20,22,31"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "red", 36, 8, "04,07,13,16,20,22,35,36"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "blue", 36, 8, "08,11,15,20,22,31,35,36"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "red", 16, 2, "13,16"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20260101ABCD", "blue", 16, 2, "08,11"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "red", 6, 1, "01"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "blue", 6, 1, "03"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "red", 12, 3, "01,08,10"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "blue", 12, 3, "03,11,12"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "red", 33, 6, "01,08,19,21,22,26"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "blue", 33, 6, "03,11,12,13,28,29"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "red", 36, 8, "01,08,18,19,21,22,25,26"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "blue", 36, 8, "03,11,12,13,17,28,29,34"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "red", 16, 2, "01,08"},
		{"0000000000000000000000000000000000000000000000000000000000000000", "LOT20991231ZZZZ", "blue", 16, 2, "03,12"}}
	for _, tc := range cases {
		got := BallDraw(tc.seed, tc.actNo, tc.color, tc.poolN, tc.pickK)
		require.Len(t, got, tc.pickK, "seed=%s act=%s color=%s", tc.seed, tc.actNo, tc.color)
		assert.Equal(t, tc.want, strings.Join(pad2(got), ","),
			"seed=%s act=%s color=%s pool=%d pick=%d —— 与 lottery-verify.py 的 ball_draw 不一致",
			tc.seed, tc.actNo, tc.color, tc.poolN, tc.pickK)
	}
}

// TestBallDrawEdgesAreClosedNotWrapped 钉住三条边界:它们决定的是"摇不出来时
// 会发生什么",而那正是最容易被写成静默错误答案的地方。
func TestBallDrawEdgesAreClosedNotWrapped(t *testing.T) {
	const seed = "a3f1"
	// pickK > poolN:夹到 poolN 而不是 panic、也不是补重复号。
	assert.Equal(t, []int{1, 2, 3}, BallDraw(seed, "ACT", "red", 3, 9))
	// 号池 = 取数:恒等于全池,与哈希无关。
	assert.Equal(t, []int{1, 2, 3, 4, 5}, BallDraw(seed, "ACT", "red", 5, 5))
	// 非法入参一律返回空切片(不是 nil),调用方无需判空。
	assert.Equal(t, []int{}, BallDraw(seed, "ACT", "red", 0, 3))
	assert.Equal(t, []int{}, BallDraw(seed, "ACT", "red", 6, 0))
	// 换一个 act_no 必然换一组号:同一期里红蓝两色也必须互不相同,
	// 否则说明 color 没有真的进原像(那会让蓝球完全可由红球推出)。
	assert.NotEqual(t,
		BallDraw(seed, "ACT-A", "red", 33, 6),
		BallDraw(seed, "ACT-B", "red", 33, 6))
	assert.NotEqual(t,
		BallDraw(seed, "ACT", "red", 33, 6),
		BallDraw(seed, "ACT", "blue", 33, 6))
}

// TestMatchTierStopsAtTheFirstHit 钉住定档规则:tier 升序、命中即停、一票只中一档。
//
// 这条规则同时被前端 lib/ball.ts 的概率表与 lib/verify.ts 的名单复算重新实现了
// 一遍。写反成"取最后一个命中的档"或"取奖金最高的档",后端照样跑得通,而三方
// 会各自算出不同的中奖名单。
func TestMatchTierStopsAtTheFirstHit(t *testing.T) {
	drawReds, drawBlues := []int{3, 9, 12}, []int{5}
	tiers := []BallTier{
		{Tier: 1, RedMatch: 3, BlueMatch: 1},
		{Tier: 2, RedMatch: 3, BlueMatch: 0},
		{Tier: 3, RedMatch: 2, BlueMatch: 1},
		{Tier: 4, RedMatch: 1, BlueMatch: 0},
	}
	cases := []struct {
		name     string
		pick     string
		wantTier int
		wantRed  int
		wantBlue int
	}{
		{"全中走一等奖,不再往下落档", "03,09,12|05", 1, 3, 1},
		{"红全中蓝不中 → 二等奖", "03,09,12|02", 2, 3, 0},
		{"红中二蓝中一 → 三等奖", "03,09,07|05", 3, 2, 1},
		{"红中一 → 四等奖", "03,07,08|02", 4, 1, 0},
		{"一个都不中 → tier 0,是正常结果不是异常", "01,07,08|02", 0, 0, 0},
		{"蓝中但红一个不中,四等奖门槛是红>=1 → 不中", "01,07,08|05", 0, 0, 1},
		{"脏选号解不开一律按未中奖,绝不抛错", "not-a-pick", 0, 0, 0},
		{"乱序缺零的选号仍要能定档(splitPickLoose 的宽松侧)", "9,3,12|5", 1, 3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier, red, blue := MatchTier(drawReds, drawBlues, tc.pick, tiers)
			assert.Equal(t, tc.wantTier, tier)
			assert.Equal(t, tc.wantRed, red)
			assert.Equal(t, tc.wantBlue, blue)
		})
	}
}

// TestBallResultTextIsTheCanonicalPickFormat 钉住开奖号与选号是**同一个格式**。
//
// 详情页显示 activity.ball_result、「为什么是这个结果」显示本地复算值,用户要
// 拿这两串肉眼比对;前端 lib/verify.ts 现在也直接对这个串做相等判定。格式一旦
// 分叉(少补一位零、分隔符换掉),那条自动比对会在数据完全诚实时报红。
func TestBallResultTextIsTheCanonicalPickFormat(t *testing.T) {
	assert.Equal(t, "03,09,12|05", BallResultText([]int{12, 3, 9}, []int{5}))
	assert.Equal(t, "01,02|03,04", BallResultText([]int{1, 2}, []int{4, 3}))
	// 空侧保留分隔符,而不是塌成空串:塌掉之后 "01,02" 会被解析器读成"没有蓝球",
	// 与"蓝球池为空"混为一谈。
	assert.Equal(t, "|", BallResultText(nil, nil))
	assert.Equal(t, "05|", BallResultText([]int{5}, nil))
}
