package violation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNearestThresholdLineTakesMinimumAcrossBothLines 守的是用户端"还差几次"。
//
// # 这个数字曾经是反向的
//
// 处置由 anyReached 的 OR 触发,所以用户真正会被处置的时点由**最先到达的那条线**
// 决定。而 my-summary 的 remaining 一度只算账号总量线,于是在"账号线 10、
// 某一类 3"这种再普通不过的配置下,违规记录页头条写着「距离封号还剩 8」,
// 同一屏下方的公示卡片写着「距离封号还差 1 次」,而用户下一次命中就被封了。
//
// 那不是少给信息,是给了一个**反向**的信息:少给会让人保守,给反了会让人放心。
//
// # 判据必须与 categoryReached / reachedThreshold 逐字同构
//
// 一条线"生效"的条件就是那两个函数里的条件。任何一处放宽,倒计时就会与真实处置
// 对不上,而对不上的方向恰好是"我以为还剩很多"。
func TestNearestThresholdLineTakesMinimumAcrossBothLines(t *testing.T) {
	cat := func(id int64, enabled bool, threshold int) Category {
		return Category{Id: id, Enabled: enabled, Threshold: threshold}
	}

	cases := []struct {
		name             string
		accountThreshold int
		accountHit       int
		cats             []Category
		hits             map[int64]int
		wantLine         string
		wantRemaining    int
		wantCategoryId   int64
	}{
		{
			name:             "两条线都没配 = 当前没有任何门槛",
			accountThreshold: 0,
			cats:             []Category{cat(2, true, 0)},
			wantLine:         ThresholdLineNone,
		},
		{
			name:             "只有账号总量线",
			accountThreshold: 10,
			accountHit:       2,
			wantLine:         ThresholdLineAccount,
			wantRemaining:    8,
		},
		{
			// 这就是那次事故的配置:账号线 10、某一类 3,用户已命中 2 次。
			// 只算账号线会说"还剩 8",而真实答案是 1。
			name:             "单类型线更近时必须报单类型线",
			accountThreshold: 10,
			accountHit:       2,
			cats:             []Category{cat(5, true, 3)},
			hits:             map[int64]int{5: 2},
			wantLine:         ThresholdLineCategory,
			wantRemaining:    1,
			wantCategoryId:   5,
		},
		{
			name:             "账号线关着、某一类开着 —— 这不是'不限'",
			accountThreshold: 0,
			cats:             []Category{cat(5, true, 3)},
			hits:             map[int64]int{5: 1},
			wantLine:         ThresholdLineCategory,
			wantRemaining:    2,
			wantCategoryId:   5,
		},
		{
			name:             "多个类型时取最近的那一个",
			accountThreshold: 20,
			accountHit:       1,
			cats:             []Category{cat(3, true, 10), cat(5, true, 5), cat(6, true, 8)},
			hits:             map[int64]int{3: 2, 5: 4, 6: 0},
			wantLine:         ThresholdLineCategory,
			wantRemaining:    1,
			wantCategoryId:   5,
		},
		{
			name:             "停用的类型不构成一条线(与 categoryReached 同构)",
			accountThreshold: 10,
			accountHit:       2,
			cats:             []Category{cat(5, false, 3)},
			hits:             map[int64]int{5: 2},
			wantLine:         ThresholdLineAccount,
			wantRemaining:    8,
		},
		{
			name:             "阈值 0 的类型不构成一条线",
			accountThreshold: 10,
			accountHit:       2,
			cats:             []Category{cat(5, true, 0)},
			hits:             map[int64]int{5: 99},
			wantLine:         ThresholdLineAccount,
			wantRemaining:    8,
		},
		{
			// 已达门槛的那一刻账号就已经被处置了,余量必须夹到 0 而不是负数:
			// 一个负的"还差几次"会被前端当成正常数字渲染出去。
			name:             "已经越线时余量夹到 0,绝不为负",
			accountThreshold: 10,
			accountHit:       2,
			cats:             []Category{cat(5, true, 3)},
			hits:             map[int64]int{5: 7},
			wantLine:         ThresholdLineCategory,
			wantRemaining:    0,
			wantCategoryId:   5,
		},
		{
			name:             "并列最近时报账号总量线(所有用户都有的那条,不需要额外解释)",
			accountThreshold: 10,
			accountHit:       8,
			cats:             []Category{cat(5, true, 3)},
			hits:             map[int64]int{5: 1},
			wantLine:         ThresholdLineAccount,
			wantRemaining:    2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nearestThresholdLine(tc.accountThreshold, tc.accountHit, 24, tc.cats, tc.hits)
			assert.Equal(t, tc.wantLine, got.Line)
			if tc.wantLine == ThresholdLineNone {
				return
			}
			assert.Equal(t, tc.wantRemaining, got.Remaining)
			assert.Equal(t, tc.wantCategoryId, got.CategoryId)
			// 这条线自己的三个数必须一起交出来 —— 否则界面上会出现「触发线:类型」
			// 配上账号总量线的窗口与阈值,一句话里混着两条线的数字。
			assert.Equal(t, got.Threshold-got.HitCount > 0,
				got.Remaining > 0, "remaining 必须由这条线自己的阈值与计数解释得通")
			if tc.wantLine == ThresholdLineCategory {
				assert.Equal(t, tc.hits[got.CategoryId], got.HitCount,
					"类型线报的必须是这一类自己的计数,不是账号总量线的")
			}
		})
	}
}

// TestNearestThresholdLineAgreesWithAnyReached 把倒计时与真实处置钉在一起。
//
// 这两件事必须同时为真,否则用户看到的数字就是一句谎话:
//   - remaining 归零 ⇔ anyReached 说"该处置了";
//   - remaining > 0 ⇔ anyReached 说"还不到"。
//
// 分别写两套判据的结果是它们各自都"对",合起来对不上 —— 而对不上的那一刻
// 用户屏幕上写的是"还剩 N 次",身份却已经是被封的。
func TestNearestThresholdLineAgreesWithAnyReached(t *testing.T) {
	cases := []struct {
		name             string
		accountThreshold int
		accountHit       int
		category         Category
		categoryHit      int
	}{
		{"都没到", 10, 2, Category{Id: 5, Enabled: true, Threshold: 3}, 1},
		{"类型线刚好到", 10, 2, Category{Id: 5, Enabled: true, Threshold: 3}, 3},
		{"账号线刚好到", 10, 10, Category{Id: 5, Enabled: true, Threshold: 3}, 1},
		{"两条一起到", 3, 3, Category{Id: 5, Enabled: true, Threshold: 3}, 3},
		{"类型线越过之后", 10, 2, Category{Id: 5, Enabled: true, Threshold: 3}, 9},
		{"类型停用,只剩账号线且未到", 10, 4, Category{Id: 5, Enabled: false, Threshold: 3}, 99},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 判定侧:与 bumpCounter 落到 counterState 上的口径一致。
			st := counterState{
				HitCount:    tc.accountHit,
				Reached:     reachedThreshold(tc.accountHit, tc.accountThreshold),
				Category:    tc.category,
				CatHitCount: tc.categoryHit,
				CatReached:  categoryReached(tc.category, tc.categoryHit),
			}
			reached, _ := anyReached(st)

			// 展示侧。
			line := nearestThresholdLine(
				tc.accountThreshold, tc.accountHit, 24,
				[]Category{tc.category}, map[int64]int{tc.category.Id: tc.categoryHit},
			)

			require.NotEqual(t, ThresholdLineNone, line.Line,
				"两条线里至少有一条是配着的,展示侧却说没有门槛")
			assert.Equal(t, reached, line.Remaining == 0,
				"倒计时归零与真实处置必须同时发生:anyReached=%v 而 remaining=%d",
				reached, line.Remaining)
		})
	}
}

// TestViolationErrorCodeCarriesNoRuleId 守的是**规则库不被在线试探**。
//
// 这个码会原样出现在被拦截请求的响应体里。它一度是 "qy_violation.<规则主键>",
// 于是用户可以反复改写 prompt、看码变不变,推断自己撞的是哪一条规则、
// 以及某一次改写有没有绕开它 —— 等于把规则库做成了一个免费的探测接口。
//
// 排障能力没有丢:规则 id / 规则名 / 命中词写在计费日志的 admin_info 里
// (formatUserLogs 会为普通用户删掉 admin_info)。
func TestViolationErrorCodeCarriesNoRuleId(t *testing.T) {
	code := violationErrorCode()
	assert.Equal(t, "qy_violation", code)
	// 这个断言的形式很重要:任何"把某个 id 拼进去"的写法都会带来数字。
	assert.NotContains(t, code, ".", "错误码里出现分隔符,通常意味着又拼了一个内部标识")
	for _, ch := range code {
		assert.False(t, ch >= '0' && ch <= '9',
			"错误码里出现了数字,用户可以据此枚举规则:%q", code)
	}
}
