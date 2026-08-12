package violation

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// window_unlimited_test.go —— 统计窗口「不限期限」这一档的取值语义。
//
// # 这个文件守的是什么
//
// 项目方要的是「统计窗口可以设定为无限时间,即没有窗口期限,达到次数就封号」。
// 落地方式是一个哨兵值 WindowUnlimited(-1),而不是拿 0 顶上。这个选择的全部
// 风险都集中在一件事上:**存量数据里的 0 不能被静默改写**。
//
// 库里的 window_hours 历来只可能是正数(两个校验器都要求 >= 1,种子与 gorm 默认
// 都是 24),但读点从来都写着 `<= 0 → 24`,也就是"没配 ⇒ 按 24 小时算"。
// 如果哪天有人从库外插进一行 0,它必须继续按 24 小时判 —— 改成"无限"意味着
// 一年前的命中重新算数,而**用户会因此被封号**,库里却没有任何东西记录这件事。
// 下面第一张表就是这条不变量,它比"无限窗口能用"更重要。

// TestWindowFloorTreatsOnlyMinusOneAsUnlimited 是取值语义的总表。
//
// 三件事一次说清:
//   - 0 与无法识别的负数继续回落 24 小时(**存量语义不变**,这是迁移安全的全部依据);
//   - 只有 WindowUnlimited(-1)是"没有下界";
//   - 有限窗口的下界就是 now - hours*3600,没有别的修正。
//
// 期望值逐格写死而不是在测试里复算 `now - h*3600`:复算等于把同一个公式写两遍,
// 公式本身错了(比如把小时当成分钟)两边会一起错、一起绿。
func TestWindowFloorTreatsOnlyMinusOneAsUnlimited(t *testing.T) {
	const now int64 = 1_700_000_000

	cases := []struct {
		name string
		// hours 是库里那一列的原始值。
		hours int
		// wantFloor 是窗口的时间下界。负数表示"没有下界"(无限窗口)。
		wantFloor int64
		// wantEffective 是展示与回显用的生效值。
		wantEffective int
	}{
		{"1 小时", 1, 1_700_000_000 - 3600, 1},
		{"24 小时(出厂默认)", 24, 1_700_000_000 - 86400, 24},
		{"72 小时", 72, 1_700_000_000 - 3*86400, 72},
		{"上界 90 天", maxPolicyWindowHours, 1_700_000_000 - 90*86400, maxPolicyWindowHours},

		// 这一行是本轮最关键的一格:0 的语义在改造前后必须逐字节相同。
		{"0(库外插进来的历史行)→ 仍按 24 小时,绝不改写成无限", 0, 1_700_000_000 - 86400, 24},

		{"哨兵 -1 → 没有下界", WindowUnlimited, windowUnlimitedFloor, WindowUnlimited},

		// 其余负数不是哨兵。回落 24 小时是**保守方向**:窗口更短、更少人被封。
		// 把任意负数都当成无限的话,一次手滑 SQL 就是全站范围的窗口放大。
		{"-2(手滑写进来的负数)→ 回落 24 小时,不是无限", -2, 1_700_000_000 - 86400, 24},
		{"-9999 → 回落 24 小时,不是无限", -9999, 1_700_000_000 - 86400, 24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantFloor, windowFloor(now, tc.hours))
			assert.Equal(t, tc.wantEffective, effectiveWindowHours(tc.hours))
		})
	}

	t.Run("无限窗口的下界小于任何可能的窗口起点", func(t *testing.T) {
		// window_start 是 Unix 时间戳,恒 >= 0(GORM 零值也是 0)。计数重置的判据是
		// `window_start < winFrom`,所以下界必须严格小于 0,否则 window_start=0 的
		// 那一行会在无限窗口下被判成"窗口已滚过"并清零。
		assert.Less(t, windowFloor(now, WindowUnlimited), int64(0))
	})
}

// TestBumpCategoryCounterUnlimitedWindowKeepsAncientHits 是无限窗口的行为本体,
// 跑在真实 SQL 上(bumpCategoryCounter 刻意写成三家数据库都能执行的两条语句)。
//
// 同一份"一年前的计数行"喂给两种窗口配置,结果必须相反:
//   - 24 小时窗口:窗口早已滚过,计数从本次权重重新起算(既有语义,不能被改坏);
//   - 不限期限:那一次仍然算数,计数在它之上累加,并因此越过阈值。
//
// 一年前这个跨度是刻意的:它比任何合法的有限窗口(上界 90 天)都长,
// 所以"无限"确实是无限,而不是"上界那么长"。
func TestBumpCategoryCounterUnlimitedWindowKeepsAncientHits(t *testing.T) {
	const (
		userId     = 4201
		categoryId = 7
		threshold  = 3
	)
	// 一年前:比窗口上界(90 天)还远,任何有限窗口都覆盖不到。
	ancient := common.GetTimestamp() - 365*86400

	cases := []struct {
		name        string
		windowHours int
		// wantHit 是这次推进之后窗口内的计数。
		wantHit int
		// wantReached 是"是否已达这一类的阈值(3)"。
		wantReached bool
		// wantWindowStart 说明窗口有没有被重置。
		wantWindowStartMoved bool
	}{
		{
			name: "24 小时窗口:一年前那 2 次已经过期,从本次权重重新起算",
			// 2(旧) 被丢弃 → 1,离阈值 3 还差 2 次。
			windowHours: 24, wantHit: 1, wantReached: false, wantWindowStartMoved: true,
		},
		{
			name: "不限期限:一年前那 2 次仍然算数,第 3 次当场到线",
			// 2(旧) + 1 = 3 == 阈值 → 到线。这就是"没有窗口期限,达到次数就封号"。
			windowHours: WindowUnlimited, wantHit: 3, wantReached: true, wantWindowStartMoved: false,
		},
		{
			name:        "窗口列为 0 的历史行:按 24 小时读,与改造前逐字节一致",
			windowHours: 0, wantHit: 1, wantReached: false, wantWindowStartMoved: true,
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newCategoryDB(t)
			require.NoError(t, gdb.Create(&CategoryCounter{
				UserId: userId, CategoryId: categoryId,
				WindowStart: ancient, HitCount: 2, TotalCount: 2,
				LastHitAt: ancient, UpdatedAt: ancient,
			}).Error)

			cat := Category{
				Id: categoryId, Key: CatJailbreak, Enabled: true,
				WindowHours: tc.windowHours, Threshold: threshold,
			}
			hit, reached, err := bumpCategoryCounter(ctx, gdb, userId, cat, 1)
			require.NoError(t, err)
			assert.Equal(t, tc.wantHit, hit)
			assert.Equal(t, tc.wantReached, reached)

			var row CategoryCounter
			require.NoError(t, gdb.
				Where("user_id = ? AND category_id = ?", userId, categoryId).
				Take(&row).Error)
			assert.Equal(t, tc.wantHit, row.HitCount)
			// total_count 是终身累计,与窗口无关:两种配置下都是 2 + 1。
			assert.EqualValues(t, 3, row.TotalCount, "终身累计不受窗口口径影响")
			if tc.wantWindowStartMoved {
				assert.Greater(t, row.WindowStart, ancient, "窗口滚过必须把起点推到现在")
			} else {
				assert.Equal(t, ancient, row.WindowStart,
					"无限窗口永不重置,窗口起点必须原样保留 —— 它是这一行开始累计的时间")
			}
		})
	}
}

// TestWindowWidensRanksUnlimitedAboveEveryFiniteWindow 守的是二次确认那道闸。
//
// 判据曾经是裸的 `next.WindowHours > before.WindowHours`。哨兵是 -1,比任何正数
// 都小,于是"24 小时 → 不限期限"这个**最激进的一种改动**会被判成放宽:
// 不弹二次确认、不拉影响面预览,一批存量账号在管理员毫不知情的情况下当场越线。
//
// 方向不对称,所以两个方向都要钉:无限 → 有限必须判成放宽(不拦),
// 否则每一次"把无限收回 24 小时"都要多点一次确认,而那是一次纯粹的止损动作。
func TestWindowWidensRanksUnlimitedAboveEveryFiniteWindow(t *testing.T) {
	cases := []struct {
		name   string
		before int
		next   int
		want   bool
	}{
		{"24 → 72:变长", 24, 72, true},
		{"72 → 24:变短", 72, 24, false},
		{"24 → 24:没变", 24, 24, false},

		{"24 → 不限期限:这是最激进的一种放大,必须判成收紧", 24, WindowUnlimited, true},
		{"上界 90 天 → 不限期限:仍然是放大", maxPolicyWindowHours, WindowUnlimited, true},
		{"不限期限 → 24:止损,不该拦", WindowUnlimited, 24, false},
		{"不限期限 → 不限期限:没变", WindowUnlimited, WindowUnlimited, false},

		// 0 在两侧都按 24 小时读,与改造前一致。
		{"0 → 24:两边都是 24,没变", 0, 24, false},
		{"0 → 72:变长", 0, 72, true},
		{"0 → 不限期限:放大", 0, WindowUnlimited, true},
		{"72 → 0:等于回到 24,变短", 72, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, windowWidens(tc.before, tc.next))
		})
	}

	// 两个调用点必须都走这条判据。它们各自的表里已经有窗口的常规行,
	// 这里只补"无限"那一格 —— 那是裸比较唯一会判错的位置。
	t.Run("违规类型的二次确认认这条判据", func(t *testing.T) {
		before := Category{Enabled: true, WindowHours: 24, Threshold: 10}
		next := Category{Enabled: true, WindowHours: WindowUnlimited, Threshold: 10}
		assert.True(t, categoryTightens(true, before, next))
		assert.False(t, categoryTightens(true, next, before))
	})
	t.Run("处置策略档的二次确认认这条判据", func(t *testing.T) {
		before := BanPolicy{WindowHours: 24, Threshold: 10, Action: PolicyActionBan}
		next := BanPolicy{WindowHours: WindowUnlimited, Threshold: 10, Action: PolicyActionBan}
		assert.True(t, tightensBanPolicy(true, before, next))
		assert.False(t, tightensBanPolicy(true, next, before))
	})
}

// TestValidateWindowAcceptsUnlimitedSentinelOnly 固化写入口的取值域。
//
// 两张表的窗口列必须接受同一组值,否则管理端会出现"类型能配不限期限、策略档不能"
// 这种说不出理由的差别,而它落到用户端就是并排的两条线用两套时间口径说话。
//
// 0 与其余负数继续被拒:它们在读点会静默回落成 24 小时,而"保存成功但生效的
// 不是我填的数"是这张表上最难查的一类问题。
func TestValidateWindowAcceptsUnlimitedSentinelOnly(t *testing.T) {
	cases := []struct {
		name   string
		hours  int
		wantOK bool
	}{
		{"1 小时", 1, true},
		{"24 小时", 24, true},
		{"上界", maxPolicyWindowHours, true},
		{"不限期限哨兵", WindowUnlimited, true},
		{"0", 0, false},
		{"-2 不是哨兵", -2, false},
		{"超过上界", maxPolicyWindowHours + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := &Category{Key: "spam", Name: "垃圾", WindowHours: tc.hours, Threshold: 3}
			policy := &BanPolicy{UserGroup: "vip", WindowHours: tc.hours,
				Threshold: 3, Action: PolicyActionBan}
			if tc.wantOK {
				assert.NoError(t, validateCategory(cat))
				assert.NoError(t, validateBanPolicy(policy))
				return
			}
			require.Error(t, validateCategory(cat))
			assert.Contains(t, validateCategory(cat).Error(), "统计窗口必须在")
			require.Error(t, validateBanPolicy(policy))
			assert.Contains(t, validateBanPolicy(policy).Error(), "统计窗口必须在")
		})
	}
}

// TestUserFacingWindowIsThreeStateNotAWrongHourCount 守用户端公示的时间口径。
//
// 公示页写的是「你违规了【X】N 次,到 M 次封号(24 小时内累计)」。窗口改成不限
// 期限之后,那句话里的"24 小时内"必须换掉 —— 留着它是一句**错的**承诺:
// 用户会以为等一天就清零,而实际那些次数永远算数。
//
// 后端能做的是不撒谎:把 WindowUnlimited 原样下发,由前端换成"累计"的说法
// (前端那一半由 web/src/features/qy/pages/violations 的用例守)。
// 这里钉住的是"绝不折成一个具体小时数"——折成 24 是撒谎,折成 -1 小时是乱码。
func TestUserFacingWindowIsThreeStateNotAWrongHourCount(t *testing.T) {
	now := common.GetTimestamp()
	ancient := now - 365*86400

	cases := []struct {
		name string
		cat  Category
		ct   CategoryCounter
		// wantWindow 是下发给前端的 window_hours。
		wantWindow int
		wantHit    int
	}{
		{
			name:       "有限窗口 + 窗口内的计数 → 照常给小时数",
			cat:        Category{Id: 5, PublicTitle: "T", Enabled: true, WindowHours: 24, Threshold: 3},
			ct:         CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: now - 60},
			wantWindow: 24, wantHit: 2,
		},
		{
			name:       "有限窗口 + 一年前的计数 → 窗口已滚过,不展示旧计数",
			cat:        Category{Id: 5, PublicTitle: "T", Enabled: true, WindowHours: 24, Threshold: 3},
			ct:         CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: ancient},
			wantWindow: 24, wantHit: 0,
		},
		{
			name: "不限期限 + 一年前的计数 → 仍然算数,窗口原样下发哨兵",
			cat: Category{Id: 5, PublicTitle: "T", Enabled: true,
				WindowHours: WindowUnlimited, Threshold: 3},
			ct:         CategoryCounter{CategoryId: 5, HitCount: 2, WindowStart: ancient},
			wantWindow: WindowUnlimited, wantHit: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toUserCategoryView(tc.cat, tc.ct, now)
			assert.Equal(t, tc.wantWindow, got.WindowHours)
			assert.Equal(t, tc.wantHit, got.HitCount)
			assert.Equal(t, 3-tc.wantHit, got.Remaining)
		})
	}
}

// TestCountCategoryImpactMatchesTheJudgementUnderUnlimitedWindow 守影响面预览。
//
// 预览与判定必须同口径。预览按 24 小时算、判定按不限期限算的话,管理员看到
// 「0 个账号会受影响」就按下了保存,而下一波命中会把一批人收走 —— 这个预览的
// 全部价值就是不让那件事发生。
func TestCountCategoryImpactMatchesTheJudgementUnderUnlimitedWindow(t *testing.T) {
	gdb := newCategoryDB(t)
	ancient := common.GetTimestamp() - 365*86400
	// 一年前攒够 5 次的账号:任何有限窗口都看不见它,不限期限必须看见。
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 88, CategoryId: 9, WindowStart: ancient,
		HitCount: 5, TotalCount: 5, LastHitAt: ancient,
	}).Error)

	base := Category{Id: 9, Enabled: true, Threshold: 3}
	ctx := context.Background()

	finite := base
	finite.WindowHours = 24
	got, err := countCategoryImpact(ctx, gdb, finite)
	require.NoError(t, err)
	assert.Equal(t, 0, got.Matched, "24 小时窗口下这一行早已过期")
	assert.Equal(t, 24, got.WindowHours)

	unlimited := base
	unlimited.WindowHours = WindowUnlimited
	got, err = countCategoryImpact(ctx, gdb, unlimited)
	require.NoError(t, err)
	assert.Equal(t, 1, got.Matched, "不限期限下这一行已经越线,预览必须数出来")
	assert.Equal(t, []int{88}, got.UserIds)
	assert.Equal(t, WindowUnlimited, got.WindowHours,
		"回显必须是哨兵,折成 24 会让弹窗上的窗口与实际算法对不上")
}

// TestSuggestionImpactUsesTheSameWindowAsTheJudgement 补的是**建议阈值**那一条
// 影响面路径 —— 它与 countCategoryImpact 是两条独立的查询,收敛时漏了一条。
//
// # 为什么它值一条独立的测试
//
// 建议阈值的确认弹窗上写着"这一改会让 N 个账号立刻处在越线状态",而 N 就是
// 这个函数算出来的。它与判定用的是**两套**窗口口径时,那个数字是错的:
// 少算的方向是"看起来更安全",管理员照着一个 0 按下确认,一批账号当场被封。
//
// 这一处此前用的是裸的 `if windowHours <= 0 { windowHours = 24 }`,于是哨兵
// 会被折成 24 小时。今天走不到那一支(probe 的窗口来自代码里的建议表,全是
// 正数),所以它是一个**埋着的**陷阱而不是活着的缺陷 —— 但下一次有人给建议表
// 里加一行"不限期限",或者把这一段抄去别处,它就活了。这条测试直接喂哨兵,
// 把口径钉死在函数上,不依赖调用方今天恰好只传正数。
func TestSuggestionImpactUsesTheSameWindowAsTheJudgement(t *testing.T) {
	gdb := newCategoryDB(t)
	ancient := common.GetTimestamp() - 365*86400
	recent := common.GetTimestamp() - 3600
	// 88:一年前攒够 5 次 —— 有限窗口看不见,不限期限必须看见。
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 88, CategoryId: 9, WindowStart: ancient,
		HitCount: 5, TotalCount: 5, LastHitAt: ancient,
	}).Error)
	// 89:一小时前攒够 5 次 —— 两种窗口都必须看见,它是对照组。
	// 没有它,"不限期限数出 1 个"可能只是因为整条查询恰好没跑起来。
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 89, CategoryId: 9, WindowStart: recent,
		HitCount: 5, TotalCount: 5, LastHitAt: recent,
	}).Error)

	tests := []struct {
		name        string
		windowHours int
		want        int
		why         string
	}{
		{
			name: "24 小时:只数得到窗口内那一个", windowHours: 24, want: 1,
			why: "一年前那一行在有限窗口下已经过期,判定看不见它,预览也不该数",
		},
		{
			name: "不限期限:一年前那一行也算数", windowHours: WindowUnlimited, want: 2,
			why: "折成 24 会让预览说 1、而实际会封 2 —— " +
				"少算的方向正好是「看起来更安全」,管理员照着它按下确认",
		},
		{
			name: "0(没配):按 24 小时回落,与存量语义一致", windowHours: 0, want: 1,
			why: "0 是「没配」而不是「无限」,这一格钉住存量行的语义没有被改写",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, capped, err := countSuggestionAffectedUsers(
				context.Background(), gdb,
				[]Category{{Id: 9, Enabled: true, Threshold: 3, WindowHours: tc.windowHours}},
			)
			require.NoError(t, err)
			assert.False(t, capped)
			assert.Equal(t, tc.want, got, tc.why)
		})
	}
}
