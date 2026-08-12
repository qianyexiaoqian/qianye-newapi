package violation

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// banpolicy_test.go —— 按用户分组的处置策略档。
//
// 这里守的不是"函数返回值对不对",是三件一旦错掉就会直接改变谁被封号的事:
//   - 没配专属档的分组必须落到兜底档,兜底档必须永远存在;
//   - 阈值/窗口必须来自**用户所在分组**的那一档,不是全局 YAML;
//   - 「仅记录」档不得处置账号,但必须留下可查的行。

// usePolicySnapshot 直接装配一份策略快照,绕开数据库。
//
// 解析逻辑(分组 → 档)与加载逻辑(库 → 快照)是两件事,混在一起测会让
// "分组名大小写归一"这类判据必须先建一张表才能验,而那张表与判据无关。
// 加载本身由 TestReloadBanPoliciesSplitsDefault 单独覆盖。
func usePolicySnapshot(t *testing.T, s *banPolicySnapshot) {
	t.Helper()
	prev := policySnap.Load()
	prevNext := policyNextAt.Load()
	policySnap.Store(s)
	// 推到未来,避免 banPolicies() 在用例中途去查一个不存在的库把快照冲掉。
	policyNextAt.Store(common.GetTimestamp() + 3600)
	t.Cleanup(func() {
		policySnap.Store(prev)
		policyNextAt.Store(prevNext)
	})
}

func newPolicyDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&BanPolicy{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// TestResolveBanPolicyPicksGroupThenFallback 是"按分组取值 + 兜底"的核心判据。
func TestResolveBanPolicyPicksGroupThenFallback(t *testing.T) {
	fallback := BanPolicy{IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10, Action: PolicyActionBan}
	snap := &banPolicySnapshot{
		fromDB:   true,
		fallback: fallback,
		byGroup: map[string]BanPolicy{
			"vip": {UserGroup: "vip", Enabled: true, WindowHours: 72, Threshold: 3, Action: PolicyActionRestrict},
			// 停用档:等价于"没配",必须回落兜底,而不是"这个分组免罚"。
			"svip": {UserGroup: "svip", Enabled: false, WindowHours: 1, Threshold: 1, Action: PolicyActionBan},
		},
	}
	usePolicySnapshot(t, snap)

	cases := []struct {
		name      string
		group     string
		threshold int
		window    int
		action    string
	}{
		{"专属档命中", "vip", 3, 72, PolicyActionRestrict},
		{"分组名大小写不敏感", "VIP", 3, 72, PolicyActionRestrict},
		{"分组名两侧空白被归一", "  vip  ", 3, 72, PolicyActionRestrict},
		{"没配专属档 → 兜底", "enterprise", 10, 24, PolicyActionBan},
		{"停用的专属档 → 兜底,不是免罚", "svip", 10, 24, PolicyActionBan},
		{"空分组 → 兜底", "", 10, 24, PolicyActionBan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBanPolicy(tc.group)
			assert.Equal(t, tc.threshold, got.Threshold, "阈值取错档 = 这个分组的人在错误的次数上被处置")
			assert.Equal(t, tc.window, got.WindowHours)
			assert.Equal(t, tc.action, got.Action)
		})
	}
}

// TestResolveBanPolicyFallsBackToYAMLWhenSnapshotEmpty 守的是第三道锁。
//
// 快照从未加载成功(扩展库不可达、表刚建)时,阈值必须回落到 YAML 的两个旧字段,
// 也就是**改造之前的行为**。回落到硬编码默认值等于在事故当天悄悄改变封号阈值。
func TestResolveBanPolicyFallsBackToYAMLWhenSnapshotEmpty(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 7\n  auto_ban_window_hours: 48\n")
	usePolicySnapshot(t, nil)
	// usePolicySnapshot(nil) 之后 banPolicies() 会走到"快照为 nil"的那条路径。
	policyNextAt.Store(common.GetTimestamp() + 3600)

	got := resolveBanPolicy("anything")
	assert.Equal(t, 7, got.Threshold, "扩展库不可达时阈值必须仍是 YAML 里那个已经被运维设定过的值")
	assert.Equal(t, 48, got.WindowHours)
	assert.Equal(t, PolicyActionBan, got.Action)
}

// TestReloadBanPoliciesSplitsDefault 验证加载:兜底行进 fallback,其余进 byGroup。
func TestReloadBanPoliciesSplitsDefault(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 99\n")
	gdb := newPolicyDB(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&BanPolicy{
		IsDefault: true, Enabled: true, WindowHours: 12, Threshold: 5,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&BanPolicy{
		UserGroup: "vip", Enabled: true, WindowHours: 6, Threshold: 2,
		Action: PolicyActionRecord, CreatedAt: now, UpdatedAt: now,
	}).Error)

	prev := policySnap.Load()
	t.Cleanup(func() { policySnap.Store(prev) })
	require.NoError(t, reloadBanPolicies(context.Background(), gdb))

	snap := policySnap.Load()
	require.NotNil(t, snap)
	assert.True(t, snap.fromDB, "库里有兜底行时必须标成 fromDB,否则管理端会以为自己改的兜底没生效")
	assert.Equal(t, 5, snap.fallback.Threshold, "兜底档必须来自库,而不是 YAML 的 99")
	require.Contains(t, snap.byGroup, "vip")
	assert.Equal(t, PolicyActionRecord, snap.byGroup["vip"].Action)
	assert.NotContains(t, snap.byGroup, "", "兜底行不得同时出现在 byGroup 里")
}

// TestEnsureDefaultBanPolicyIsIdempotentAndNeverOverwrites 是"兜底档不可删"的第二道锁。
//
// 两条断言缺一不可:
//   - 重复调用不得插第二行(多节点同时启动);
//   - **绝不覆盖**已有兜底档。写成 Upsert 的话,管理员把兜底阈值从 10 改成 3,
//     下一次重启就被 YAML 里的 10 静默改回去 —— 而重启是运维动作,没有人会把
//     "阈值变回去了"和它联系起来。
func TestEnsureDefaultBanPolicyIsIdempotentAndNeverOverwrites(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 10\n  auto_ban_window_hours: 24\n")
	gdb := newPolicyDB(t)
	ctx := context.Background()

	require.NoError(t, ensureDefaultBanPolicy(ctx, gdb))
	var rows []BanPolicy
	require.NoError(t, gdb.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, 10, rows[0].Threshold, "种子必须取 YAML 的现网值")
	assert.True(t, rows[0].IsDefault)

	// 管理员把兜底阈值改窄。
	require.NoError(t, gdb.Model(&BanPolicy{}).Where("id = ?", rows[0].Id).
		Update("threshold", 3).Error)

	require.NoError(t, ensureDefaultBanPolicy(ctx, gdb))
	require.NoError(t, ensureDefaultBanPolicy(ctx, gdb))
	rows = nil
	require.NoError(t, gdb.Find(&rows).Error)
	require.Len(t, rows, 1, "重复补建不得插出第二个兜底档:两个兜底档等于没有兜底档")
	assert.Equal(t, 3, rows[0].Threshold,
		"补建绝不能覆盖管理员改过的兜底阈值,否则每次重启都会把封号线悄悄放宽回去")
}

func TestValidateBanPolicy(t *testing.T) {
	cases := []struct {
		name   string
		policy BanPolicy
		wantOK bool
	}{
		{"合法的分组档", BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: 5, Action: PolicyActionBan}, true},
		{"阈值 0 合法(不处置)", BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: 0, Action: PolicyActionRecord}, true},
		{"兜底档不带分组名", BanPolicy{IsDefault: true, WindowHours: 24, Threshold: 5, Action: PolicyActionBan}, true},
		{"非法动作", BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: 5, Action: "delete"}, false},
		{"动作为空", BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: 5}, false},
		{"窗口为 0", BanPolicy{UserGroup: "vip", WindowHours: 0, Threshold: 5, Action: PolicyActionBan}, false},
		// -1 是「不限期限」哨兵,合法;其余负数一律拒绝(它们在读点会静默回落
		// 24 小时,而"保存成功但生效的不是我填的数"是这张表上最难查的一类问题)。
		// 取值域的完整表在 window_unlimited_test.go。
		{"窗口为 -1(不限期限哨兵)", BanPolicy{UserGroup: "vip", WindowHours: WindowUnlimited, Threshold: 5, Action: PolicyActionBan}, true},
		{"窗口为负(不是哨兵)", BanPolicy{UserGroup: "vip", WindowHours: -2, Threshold: 5, Action: PolicyActionBan}, false},
		{"窗口超上界", BanPolicy{UserGroup: "vip", WindowHours: maxPolicyWindowHours + 1, Threshold: 5, Action: PolicyActionBan}, false},
		{"阈值为负", BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: -1, Action: PolicyActionBan}, false},
		{"阈值超上界", BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: maxPolicyThreshold + 1, Action: PolicyActionBan}, false},
		{"非兜底档缺分组名", BanPolicy{WindowHours: 24, Threshold: 5, Action: PolicyActionBan}, false},
		{"非兜底档分组名只有空白", BanPolicy{UserGroup: "   ", WindowHours: 24, Threshold: 5, Action: PolicyActionBan}, false},
		{"兜底档带了分组名", BanPolicy{IsDefault: true, UserGroup: "vip", WindowHours: 24, Threshold: 5, Action: PolicyActionBan}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.policy
			err := validateBanPolicy(&p)
			if tc.wantOK {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}

// TestValidateBanPolicyRemarkCountsRunes 钉住计长口径。
//
// varchar(512) 在 MySQL/PostgreSQL 上是 512 个**字符**。按 byte 判会让一段
// 200 字的中文(600 字节)被拒,而它在数据库里完全合法 —— 那是一条在正确数据上
// 误报的校验,下一个人只会把它删掉。
func TestValidateBanPolicyRemarkCountsRunes(t *testing.T) {
	base := BanPolicy{UserGroup: "vip", WindowHours: 24, Threshold: 5, Action: PolicyActionBan}

	ok := base
	ok.Remark = repeatRune('中', banPolicyRemarkLimit)
	assert.NoError(t, validateBanPolicy(&ok),
		"512 个中文字符 = 1536 字节,但 varchar(512) 数的是字符,必须放行")

	tooLong := base
	tooLong.Remark = repeatRune('中', banPolicyRemarkLimit+1)
	assert.Error(t, validateBanPolicy(&tooLong))
}

func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

// TestBanPolicyRemarkLimitMatchesColumnTag 把生产侧的长度校验钉在 gorm tag 上。
//
// 与 TestRuleVarcharLimitsMatchColumnTags 同形,防的是同一次事故:两份事实漂移,
// 校验放过数据库拒绝的行,MySQL 用一句 Error 1406 把整条 INSERT 打掉,
// 而接口照常返回 200。
func TestBanPolicyRemarkLimitMatchesColumnTag(t *testing.T) {
	f, ok := reflect.TypeOf(BanPolicy{}).FieldByName("Remark")
	require.True(t, ok)
	m := regexp.MustCompile(`(?i)\btype:\s*varchar\s*\(\s*(\d+)\s*\)`).FindStringSubmatch(f.Tag.Get("gorm"))
	require.NotNil(t, m, "BanPolicy.Remark 必须是一个有声明长度的 varchar 列")
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	assert.Equal(t, n, banPolicyRemarkLimit,
		"校验上限与列宽不一致:改窄了列却没改校验 → 管理员照旧存得进去,MySQL 报 Error 1406")
}

// TestTightensBanPolicyRequiresConfirmation 覆盖"什么时候需要二次确认"。
//
// 方向错了的后果是不对称的:漏判(该拦没拦)= 管理员在毫无提示的情况下
// 处置了一批存量账号;误判(不该拦却拦)= 多点一次确认。所以判据必须保守。
func TestTightensBanPolicyRequiresConfirmation(t *testing.T) {
	base := BanPolicy{WindowHours: 24, Threshold: 10, Action: PolicyActionRestrict}
	cases := []struct {
		name      string
		hasBefore bool
		next      BanPolicy
		want      bool
	}{
		{"新建一档一律算收紧", false, base, true},
		{"阈值不变", true, base, false},
		{"阈值变小", true, BanPolicy{WindowHours: 24, Threshold: 3, Action: PolicyActionRestrict}, true},
		{"阈值变大(放宽)", true, BanPolicy{WindowHours: 24, Threshold: 30, Action: PolicyActionRestrict}, false},
		{"从不处置改成处置", true, BanPolicy{WindowHours: 24, Threshold: 5, Action: PolicyActionRestrict}, true},
		{"改成不处置(放宽)", true, BanPolicy{WindowHours: 24, Threshold: 0, Action: PolicyActionRestrict}, false},
		{"窗口变长", true, BanPolicy{WindowHours: 72, Threshold: 10, Action: PolicyActionRestrict}, true},
		{"窗口变短(放宽)", true, BanPolicy{WindowHours: 6, Threshold: 10, Action: PolicyActionRestrict}, false},
		{"动作变重 restrict→ban", true, BanPolicy{WindowHours: 24, Threshold: 10, Action: PolicyActionBan}, true},
		{"动作变轻 restrict→record", true, BanPolicy{WindowHours: 24, Threshold: 10, Action: PolicyActionRecord}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := base
			if !tc.hasBefore {
				before = BanPolicy{}
			}
			assert.Equal(t, tc.want, tightensBanPolicy(tc.hasBefore, before, tc.next))
		})
	}
}

// TestRecordOnlyPolicyLeavesTraceButNeverBans 是「仅记录」档的核心判据。
//
// 两半都必须成立,少一半这一档就没有意义:
//   - **不返回 Ban** → 调用方不会去主库执行任何处置;
//   - **留下 observed 行** → 管理员能看见"如果切成封号,现在会封掉谁"。
//     只打日志不落行的话,这份名单不可筛选、不可分页,等于不存在。
func TestRecordOnlyPolicyLeavesTraceButNeverBans(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	isolateBreaker(t)
	gdb := newBanDB(t)

	st := counterState{
		HitCount: 5, BanCycle: 0, Reached: true,
		Policy: BanPolicy{UserGroup: "vip", Enabled: true, WindowHours: 24,
			Threshold: 5, Action: PolicyActionRecord},
	}
	ban := resolveBanClaim(context.Background(), gdb, banRecord(42), st)
	assert.Nil(t, ban, "「仅记录」档必须不把执行权交出去,否则这一档与封号档没有区别")

	var rows []Ban
	require.NoError(t, gdb.Find(&rows).Error)
	require.Len(t, rows, 1, "「仅记录」档必须留下一行,那份名单是这一档存在的全部理由")
	assert.Equal(t, BanObserved, rows[0].Status)
	assert.Equal(t, "vip", rows[0].PolicyGroup, "策略档必须冻结进封禁行,事后才答得出「按哪一档判的」")
	assert.Equal(t, PolicyActionRecord, rows[0].PolicyAction)
	assert.Equal(t, 5, rows[0].Threshold)

	// 同一周期内再违规不得刷出第二行。
	require.Nil(t, resolveBanClaim(context.Background(), gdb, banRecord(42), st))
	rows = nil
	require.NoError(t, gdb.Find(&rows).Error)
	assert.Len(t, rows, 1, "越线之后每一次违规都会走到这里,不认领就会刷出一串重复行")
}

// TestObservedBanIsPromotedWhenPolicyTightens 是收紧策略时的存量账号回归。
//
// 缺了这条提升路径,「仅记录」→「封号」的切换对**已经越线的账号完全无效**:
// 他们本周期的唯一键早被 observed 行占住,后续每一次违规都撞冲突返回。
// 而这批人恰恰是影响面预览承诺会处置的那批 —— 预览说会封、实际不封,
// 是这个功能最坏的失效方式。
func TestObservedBanIsPromotedWhenPolicyTightens(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	isolateBreaker(t)
	gdb := newBanDB(t)
	ctx := context.Background()

	recordOnly := counterState{HitCount: 5, Reached: true,
		Policy: BanPolicy{UserGroup: "vip", Threshold: 5, Action: PolicyActionRecord}}
	require.Nil(t, resolveBanClaim(ctx, gdb, banRecord(42), recordOnly))

	// 管理员把 vip 这一档改成封号。
	tightened := counterState{HitCount: 6, Reached: true,
		Policy: BanPolicy{UserGroup: "vip", Threshold: 5, Action: PolicyActionBan}}
	ban := resolveBanClaim(ctx, gdb, banRecord(42), tightened)
	require.NotNil(t, ban, "observed 行必须可以被提升,否则收紧策略对存量越线账号无效")
	assert.Equal(t, BanPending, ban.Status)
	assert.Equal(t, PolicyActionBan, ban.PolicyAction,
		"提升时必须把动作改写成新档,否则执行的是 ban、行里冻结的却是 record")

	var rows []Ban
	require.NoError(t, gdb.Find(&rows).Error)
	require.Len(t, rows, 1, "提升是改写同一行,不是插新行")
	assert.Equal(t, BanPending, rows[0].Status)
	assert.Equal(t, PolicyActionBan, rows[0].PolicyAction)
}

// TestTerminalBanStatusesAreNotPromoted 守的是提升路径的边界。
//
// 只有 deferred 与 observed 是"还没有结论"。把 banned/unbanned/skipped 也放进来
// 会让一个已经被管理员解封的账号在下一次违规时被同一行重新封掉,
// 而封禁列表上看不出任何异常 —— 那一行的状态会从 unbanned 变回 pending。
func TestTerminalBanStatusesAreNotPromoted(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")
	isolateBreaker(t)
	st := counterState{HitCount: 9, Reached: true,
		Policy: BanPolicy{UserGroup: "vip", Threshold: 5, Action: PolicyActionBan}}

	for _, status := range []string{BanBanned, BanUnbanned, BanSkipped, BanPending, BanFailed} {
		t.Run(status, func(t *testing.T) {
			gdb := newBanDB(t)
			require.NoError(t, gdb.Create(&Ban{
				UserId: 42, BanCycle: 0, Status: status, CreatedAt: common.GetTimestamp(),
			}).Error)
			assert.Nil(t, resolveBanClaim(context.Background(), gdb, banRecord(42), st),
				"%s 不是「还没有结论」,不得被提升成一次新的处置", status)
		})
	}
}

// TestCountBanPolicyImpactFiltersByGroupAndStatus 覆盖影响面预览的三道过滤。
//
// 这个数字是管理员按下保存之前唯一的依据,虚报会让人不敢改、少报会让人误以为
// 无害。三道过滤各自对应一种少报/虚报:
//   - 分组不匹配的账号不该算(虚报);
//   - 已经受限的账号不会被"再处置一次"(虚报);
//   - root 永远不会被自动处置(虚报);
//   - 窗口已经滚过的计数不算数(虚报)。
func TestCountBanPolicyImpactFiltersByGroupAndStatus(t *testing.T) {
	ext := newCounterOnlyDB(t)
	main := newUsersOnlyDB(t)
	now := common.GetTimestamp()

	// 计数行:1..5 都已达 5 次且窗口新鲜;6 的窗口已经滚过;7 没到阈值。
	for _, c := range []Counter{
		{UserId: 1, HitCount: 5, WindowStart: now},
		{UserId: 2, HitCount: 9, WindowStart: now},
		{UserId: 3, HitCount: 5, WindowStart: now},
		{UserId: 4, HitCount: 5, WindowStart: now},
		{UserId: 5, HitCount: 5, WindowStart: now},
		{UserId: 6, HitCount: 99, WindowStart: now - 100*3600},
		{UserId: 7, HitCount: 4, WindowStart: now},
	} {
		require.NoError(t, ext.Create(&c).Error)
	}
	// 用户:1/2 是 vip 且可用;3 是 vip 但已受限;4 是 vip 但 root;5 是 default。
	seedImpactUser(t, main, 1, "vip", common.UserStatusEnabled, common.RoleCommonUser)
	seedImpactUser(t, main, 2, "vip", common.UserStatusEnabled, common.RoleCommonUser)
	seedImpactUser(t, main, 3, "vip", common.UserStatusDisabled, common.RoleCommonUser)
	seedImpactUser(t, main, 4, "vip", common.UserStatusEnabled, common.RoleRootUser)
	seedImpactUser(t, main, 5, "default", common.UserStatusEnabled, common.RoleCommonUser)
	seedImpactUser(t, main, 6, "vip", common.UserStatusEnabled, common.RoleCommonUser)
	seedImpactUser(t, main, 7, "vip", common.UserStatusEnabled, common.RoleCommonUser)

	ctx := context.Background()

	got, err := countBanPolicyImpact(ctx, ext, main, "vip", 5, 24, PolicyActionBan)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Matched, "只有 1 与 2 会被处置:3 已受限、4 是 root、5 不是 vip、6 窗口过期、7 没到阈值")
	assert.ElementsMatch(t, []int{1, 2}, got.UserIds)

	t.Run("兜底档按全部分组算", func(t *testing.T) {
		all, err := countBanPolicyImpact(ctx, ext, main, "", 5, 24, PolicyActionBan)
		require.NoError(t, err)
		assert.Equal(t, 3, all.Matched, "空分组 = 兜底档 = 不加分组过滤,1/2/5 都算")
	})

	t.Run("阈值 0 表示不处置,影响面恒为 0", func(t *testing.T) {
		none, err := countBanPolicyImpact(ctx, ext, main, "vip", 0, 24, PolicyActionBan)
		require.NoError(t, err)
		assert.Equal(t, 0, none.Matched)
		assert.NotNil(t, none.UserIds, "空结果也必须是 [] 而不是 null,否则前端要为它单写一条分支")
	})

	t.Run("窗口拉长会把过期计数重新算进来", func(t *testing.T) {
		wide, err := countBanPolicyImpact(ctx, ext, main, "vip", 5, 200, PolicyActionBan)
		require.NoError(t, err)
		assert.Equal(t, 3, wide.Matched, "窗口 200 小时覆盖了用户 6 的 window_start,他重新算数")
	})
}

func newCounterOnlyDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Counter{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// newUsersOnlyDB 建一张最小的 users 表。
//
// 刻意不 AutoMigrate model.User:那会把主库全部关联一起拖进来,而这里要验的
// 只有三列(group / status / role)与一条保留字列名的引用是否正确。
func newUsersOnlyDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.Exec(
		"CREATE TABLE users (id INTEGER PRIMARY KEY, `group` TEXT, status INTEGER, role INTEGER)").Error)
	// countBanPolicyImpact 的 WHERE 用 model.QyCommonGroupCol() 拼列名,而那是一个
	// 由 InitCol() 填的包级变量。不调它,列名是空串,SQL 变成 ` = ?` —— 语法错误。
	// 这一步同时也是断言:忘了初始化的话这里会直接红,而不是在生产上红。
	model.InitCol()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

func seedImpactUser(t *testing.T, gdb *gorm.DB, id int, group string, status, role int) {
	t.Helper()
	require.NoError(t, gdb.Exec(
		"INSERT INTO users (id, `group`, status, role) VALUES (?, ?, ?, ?)",
		id, group, status, role).Error)
}

// TestRevokesSessionsSeparatesRestrictFromBan 钉住 restrict 与 ban 唯一的行为差别。
//
// 主库只有一个非删除停用态(common.UserStatusDisabled,语义是"受限账号"),
// 所以两档落到 users.status 上是同一个值。差别只有一处:ban 会把用户踢出控制台。
// 这条断言存在的理由就是让这个差别不至于在某次重构里被"统一一下"抹平 ——
// 抹平的方向多半是"两档都吊销",那样 restrict 这一档就彻底没有意义了。
func TestRevokesSessionsSeparatesRestrictFromBan(t *testing.T) {
	cases := []struct {
		action string
		want   bool
		why    string
	}{
		{PolicyActionBan, true, "封号档要把人踢出控制台"},
		{PolicyActionRestrict, false, "受限档保留会话:relay 已被 status 挡死,用户要能立刻提工单"},
		{PolicyActionRecord, false, "仅记录档根本走不到这里,真走到了也不该把人踢出去"},
		{"", true, "本列出现之前的历史行当时就是吊销会话的,未知取值必须回落到旧行为"},
		{"future_action", true, "新增动作却忘了改这里时,必须落在更严的一侧"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			assert.Equal(t, tc.want, revokesSessions(tc.action), tc.why)
		})
	}
}

// TestPersistRecordUsesFrozenGroupForPolicy 是一条 AST 锁,不是行为测试。
//
// 它锁的是 persistRecord 传给 bumpCounter 的**那一个参数**:必须是记录里冻结的
// rec.UsingGroup(命中当时用户实际在用的分组),不能是空串、也不能是"现在再查一次"。
//
// 为什么只能用 AST:bumpCounter 走的是 MySQL 专属的
// `INSERT ... ON DUPLICATE KEY UPDATE`(扩展库按设计只支持 MySQL,见 qianye/db 包注释),
// 在 SQLite 内存库上跑不起来,所以这条链路没有可用的行为测试入口。
// 而传错分组的后果是完全静默的:用户会按**另一个分组**的阈值被处置,
// 记录、日志、界面上没有任何异常。
func TestPersistRecordUsesFrozenGroupForPolicy(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "guard.go", nil, 0)
	require.NoError(t, err)

	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "persistRecord" {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || ident.Name != "bumpCounter" {
				return true
			}
			found = true
			require.Len(t, call.Args, 5, "bumpCounter 的签名变了,这条锁必须跟着更新")
			sel, ok := call.Args[4].(*ast.SelectorExpr)
			require.Truef(t, ok, "bumpCounter 的分组参数必须是 rec.UsingGroup,当前是 %T", call.Args[4])
			recv, ok := sel.X.(*ast.Ident)
			require.True(t, ok)
			assert.Equal(t, "rec", recv.Name)
			assert.Equal(t, "UsingGroup", sel.Sel.Name,
				"分组必须取记录里冻结的那一个:异步 worker 跑到这里时用户分组可能已经被改,"+
					"按新分组的阈值去判一次几秒前的违规是错的,而且完全静默")
			return false
		})
		return false
	})
	require.True(t, found, "persistRecord 里找不到 bumpCounter 调用 —— 违规计数整条断了")
}
