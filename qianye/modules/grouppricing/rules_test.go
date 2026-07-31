package grouppricing

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/groupname"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func enabledRule(group, modelName, mode, value string) Rule {
	return Rule{GroupName: group, ModelName: modelName, Mode: mode, Value: dec(value), Enabled: true}
}

// TestLookupPrecedence 锁定匹配优先级:精确 > 最长前缀 > "*"。
//
// 与扫描顺序无关是硬要求 —— 一笔请求按什么价扣钱不能取决于数据库返回行的次序。
// 用两种插入顺序各跑一遍,顺序敏感的实现必然在其中一种下失败。
func TestLookupPrecedence(t *testing.T) {
	rows := []Rule{
		enabledRule("vip", modelWildcard, ModeRatio, "0.9"),
		enabledRule("vip", "gpt-4*", ModeRatio, "0.8"),
		enabledRule("vip", "gpt-4o*", ModeRatio, "0.7"),
		enabledRule("vip", "gpt-4o", ModeRatio, "0.6"),
	}
	reversed := make([]Rule, len(rows))
	for i, r := range rows {
		reversed[len(rows)-1-i] = r
	}

	cases := []struct {
		name  string
		model string
		want  string
		hit   bool
	}{
		{"精确命中赢过一切", "gpt-4o", "0.6", true},
		{"最长前缀赢过短前缀", "gpt-4o-mini", "0.7", true},
		{"短前缀命中", "gpt-4-turbo", "0.8", true},
		{"都不匹配时落到通配", "claude-3", "0.9", true},
	}
	for _, order := range []struct {
		name string
		rows []Rule
	}{{"正序", rows}, {"逆序", reversed}} {
		s := buildSnapshot(order.rows, 1)
		for _, tc := range cases {
			t.Run(order.name+"/"+tc.name, func(t *testing.T) {
				got, ok := s.lookup("vip", tc.model)
				require.Equal(t, tc.hit, ok)
				assert.Equal(t, tc.want, got.Value.String())
			})
		}
	}

	// 规则只作用于自己那个分组,不会渗到别的分组去。
	s := buildSnapshot(rows, 1)
	_, ok := s.lookup("default", "gpt-4o")
	assert.False(t, ok, "vip 的规则不得影响 default 分组")
}

// TestLookupEmptyTableMeansNoOverride 是向后兼容的回归。
//
// 升级到本版本时规则表必然是空的。空表若被判成"有覆盖",全站价格会在升级
// 瞬间变成一个不确定的值 —— 这是本次改动最不能出的事故。
func TestLookupEmptyTableMeansNoOverride(t *testing.T) {
	for _, s := range []*snapshot{
		buildSnapshot(nil, 0),
		buildSnapshot([]Rule{}, 0),
		// 停用的规则视同不存在(reload 的 WHERE enabled 已过滤,这里再锁一层)。
		buildSnapshot([]Rule{{GroupName: "vip", ModelName: "gpt-4o", Mode: ModeRatio, Value: dec("0.5")}}, 0),
	} {
		_, ok := s.lookup("vip", "gpt-4o")
		assert.False(t, ok)
	}
}

// TestValidateValue 锁定取值边界。
//
// 这个值会被直接乘进每一笔账单,所以负数、超界、超精度三类都必须在
// 写入与快照编译两处被同一份判定挡住。0 是合法的("这个分组免费用这个模型"),
// 但阶梯乘数的 0 例外 —— 它看起来太像"没填"。
func TestValidateValue(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		value   string
		wantErr bool
	}{
		{"price 正常值", ModePrice, "0.03", false},
		{"price 零表示免费", ModePrice, "0", false},
		{"price 负数被拒", ModePrice, "-0.01", true},
		{"price 超上界被拒", ModePrice, "100001", true},
		{"price 上界本身允许", ModePrice, "100000", false},
		{"ratio 正常值", ModeRatio, "1.5", false},
		{"ratio 零表示免费", ModeRatio, "0", false},
		{"ratio 负数被拒", ModeRatio, "-1", true},
		{"ratio 超上界被拒", ModeRatio, "1000001", true},
		{"ratio 极大值被拒", ModeRatio, "1e30", true},
		{"tiered 正常乘数", ModeTiered, "0.5", false},
		{"tiered 零被拒", ModeTiered, "0", true},
		{"tiered 超上界被拒", ModeTiered, "101", true},
		{"未知口径被拒", "completion", "1", true},
		{"空口径被拒", "", "1", true},
		{"超过 10 位小数被拒", ModeRatio, "0.00000000001", true},
		{"正好 10 位小数允许", ModeRatio, "0.0000000001", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateValue(tc.mode, dec(tc.value))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// TestBuildSnapshotSkipsInvalidRows 锁定"非法行只能被跳过,不能被勉强采用"。
//
// 手改数据库、迁移脚本回填、旧版本遗留的行都会绕过管理接口直达这里。
// 跳过等于无覆盖(安全),勉强采用等于按一个不确定的价格扣钱(资损)。
// 而且一条坏行绝不能拖垮整份快照 —— 那等于一次手滑让全站分组价失效。
func TestBuildSnapshotSkipsInvalidRows(t *testing.T) {
	s := buildSnapshot([]Rule{
		enabledRule("vip", "good", ModeRatio, "0.5"),
		enabledRule("vip", "negative", ModeRatio, "-1"),
		enabledRule("vip", "huge", ModeRatio, "1e40"),
		enabledRule("vip", "bad-mode", "completion", "1"),
		{GroupName: "vip", ModelName: "", Mode: ModeRatio, Value: dec("1"), Enabled: true},
		enabledRule("vip", "after-bad", ModeRatio, "0.4"),
	}, 1)

	good, ok := s.lookup("vip", "good")
	require.True(t, ok)
	assert.Equal(t, "0.5", good.Value.String())

	// 坏行之后的好行仍然在:一条坏行不得截断整份快照。
	after, ok := s.lookup("vip", "after-bad")
	require.True(t, ok)
	assert.Equal(t, "0.4", after.Value.String())

	for _, m := range []string{"negative", "huge", "bad-mode"} {
		_, ok := s.lookup("vip", m)
		assert.False(t, ok, "非法规则 %s 必须按无覆盖处理", m)
	}
}

// TestNormalizeGroupMatchesUsersDefault 锁定空分组名归一成 default。
// 主库 users.group 的列默认值就是 default,不归一会让默认分组永远匹配不到规则。
//
// 大小写拼写一并纳入:原用例只有 {"default","","  ","  default  "},
// 对"把折叠大小写改回 TrimSpace"这个变异完全免疫。
func TestNormalizeGroupMatchesUsersDefault(t *testing.T) {
	s := buildSnapshot([]Rule{enabledRule("default", "gpt-4o", ModeRatio, "0.5")}, 1)
	for _, g := range []string{"default", "", "  ", "  default  ", "DEFAULT", "Default", "  DeFaUlT  "} {
		_, ok := s.lookup(g, "gpt-4o")
		assert.True(t, ok, "分组 %q 应当归一到 default", g)
	}
}

// TestLookupFoldsGroupNameCase —— 判定侧的大小写折叠回归。
//
// 生产形态:规则配在 "vip",而 relayInfo.UsingGroup 来自主库 users.group 或
// 令牌指定分组,两者都原样保留运营填进去的大小写。判定不折叠时
// rules.go 的精确匹配落空 → 按全局价扣钱,而规则列表里那条折扣好端端摆着、
// 管理端回显"已保存"。这是全仓第三张以分组名为键的 money 表,
// commission / transfer 两张已各有回归,这条补上第三张。
func TestLookupFoldsGroupNameCase(t *testing.T) {
	s := buildSnapshot([]Rule{enabledRule("vip", "gpt-4o", ModeRatio, "0.5")}, 1)
	for _, g := range []string{"vip", "VIP", "Vip", "  vIp  "} {
		r, ok := s.lookup(g, "gpt-4o")
		require.True(t, ok, "分组 %q 必须命中配在 vip 上的那条规则,否则这笔请求按全局价扣钱", g)
		assert.Equal(t, "0.5", r.Value.String(), "分组 %q 命中的必须是同一条规则", g)
	}
}

// TestGroupNameFoldedOnWrite —— 写入侧的大小写折叠回归。
//
// 两条写入路径都要钉住:管理端 ruleUpsertReq.apply 决定落库的那个字符串,
// compile 决定快照 map 的键。任一处漏折叠,写入与判定就会落在两个不同的桶里。
func TestGroupNameFoldedOnWrite(t *testing.T) {
	var dst Rule
	req := &ruleUpsertReq{GroupName: "  VIP  ", ModelName: " gpt-4o ", Mode: ModeRatio, Value: "0.5", Enabled: true}
	require.NoError(t, req.apply(&dst))
	assert.Equal(t, "vip", dst.GroupName,
		"管理端写入必须折叠大小写后再落库:扩展库列是 ci 排序规则,VIP 会写进 vip 那一行,而热路径按 VIP 精确查")
	assert.Equal(t, "gpt-4o", dst.ModelName)

	cr, err := compile(Rule{GroupName: " VIP ", ModelName: "gpt-4o", Mode: ModeRatio, Value: dec("0.5"), Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, "vip", cr.GroupName, "快照编译侧必须与写入侧同口径,否则手改数据库的历史行永远查不到")
}

// TestGroupPricingGroupKeyFollowsSharedContract 把本模块的分组名口径钉在共享实现上。
//
// 存在理由与 commission / transfer 的同名断言一致:同仓一度有三份各自演化的
// normalizeGroup,而出钱的那几份恰好是最宽松的。三个模块现在都只转调
// qianye/groupname,这条逐输入断言保证的是"没有人把它悄悄换回一份私有实现"——
// 共享包本身不构成防御,这条断言才是。
//
// 本模块用的是 Effective 而不是 Normalize:分组名为空在这里的语义是
// "默认分组的规则"(与主库 users.group 的列默认值一致),不是"没填分组名"。
func TestGroupPricingGroupKeyFollowsSharedContract(t *testing.T) {
	for _, in := range []string{"vip", "VIP", "Vip", "  vIp  ", "", "   ", "default", "DEFAULT", "内部测试组"} {
		assert.Equal(t, groupname.Effective(in), normalizeGroup(in),
			"分组名口径必须与 qianye/groupname 一致(输入 %q)", in)
	}
	assert.Equal(t, "vip", normalizeGroup("  VIP  "),
		"不折叠大小写就等于:配了折扣、界面显示已保存、实际按原价扣钱")
	assert.Equal(t, "default", normalizeGroup("  "))
}

// ─────────────────── 缓存:读库失败只能回落成「无覆盖」 ───────────────────

// TestColdStartLoadFailureMeansNoOverride:扩展库不可用时**从未**加载成功过,
// 查找必须返回"无覆盖",而不是任何形式的默认覆盖。
func TestColdStartLoadFailureMeansNoOverride(t *testing.T) {
	useConfig(t, true, true)
	resetCaches()
	detachDB(t)

	require.Error(t, reload(true), "库不可用时 reload 必须报错")
	_, ok := lookupOverride("vip", "gpt-4o")
	assert.False(t, ok, "冷启动加载失败必须回落成无覆盖")
	assert.Nil(t, activeSnapshot())
}

// TestStaleSnapshotFallsBackToNoOverride:快照曾经加载成功,但刷新持续失败,
// 陈旧超过 max_stale_seconds 之后必须主动丢弃,回落成"无覆盖"。
//
// 这条是本模块最容易被回滚掉的一行(把 activeSnapshot 的陈旧判断删掉,
// 其余测试全绿)。删掉之后节点会拿着一份不知道多久以前的价格一直扣钱。
func TestStaleSnapshotFallsBackToNoOverride(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	rule, ok := lookupOverride("vip", "gpt-4o")
	require.True(t, ok)
	require.Equal(t, "0.5", rule.Value.String())

	// 把上一次成功加载的时间推回到陈旧上限之外,等价于"刷新连续失败了这么久"。
	loadedAt.Store(common.GetTimestamp() - maxStaleSeconds() - 1)

	_, ok = lookupOverride("vip", "gpt-4o")
	assert.False(t, ok, "快照陈旧超限后必须回落成无覆盖")
	assert.Equal(t, int64(1), staleDrops.Load(), "回落必须被计数,否则这段时间完全不可观测")
	assert.Nil(t, activeSnapshot())

	// 边界另一侧:刚好等于上限仍然可用,避免把"到期"写成"提前一秒失效"。
	loadedAt.Store(common.GetTimestamp() - maxStaleSeconds())
	_, ok = lookupOverride("vip", "gpt-4o")
	assert.True(t, ok)
}

// TestReloadKeepsLastGoodSnapshotOnError:刷新失败时不得把已有快照清空,
// 也不得把它换成一份半成品 —— 那会让价格在每次数据库抖动时跳变。
// 陈旧上限才是它的时效闸门(见上一条)。
func TestReloadKeepsLastGoodSnapshotOnError(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	require.NoError(t, gdb.Migrator().DropTable(&Rule{}))
	assert.Error(t, reload(true))

	rule, ok := lookupOverride("vip", "gpt-4o")
	require.True(t, ok, "一次读失败不应立刻清空已有快照")
	assert.Equal(t, "0.5", rule.Value.String())
	assert.Equal(t, int64(1), refreshFails.Load())
}

// TestReloadSkipsWhenVersionUnchanged 锁定版本号短路,并锁定它**必须**刷新
// loadedAt —— 否则版本长期不变的正常站点会被陈旧上限误判成"库坏了",
// 分组价每 max_stale_seconds 失效一次。
func TestReloadSkipsWhenVersionUnchanged(t *testing.T) {
	useConfig(t, true, true)
	gdb := newTestDB(t)
	seedRule(t, gdb, "vip", "gpt-4o", ModeRatio, "0.5")

	// 直接改行但不推进版本号:短路生效时快照应当保持旧值。
	require.NoError(t, gdb.Model(&Rule{}).Where("model_name = ?", "gpt-4o").
		Update("value", dec("0.1")).Error)
	loadedAt.Store(0)
	require.NoError(t, reload(false))

	rule, _ := lookupOverride("vip", "gpt-4o")
	assert.Equal(t, "0.5", rule.Value.String(), "版本未变时不应重新拉规则")
	assert.NotZero(t, loadedAt.Load(), "版本未变也必须刷新 loadedAt,否则快照会被误判为陈旧")

	// 推进版本号之后必须拉到新值。
	require.NoError(t, bumpVersion(gdb))
	require.NoError(t, reload(false))
	rule, _ = lookupOverride("vip", "gpt-4o")
	assert.Equal(t, "0.1", rule.Value.String())
}

// TestReloadTruncatesAtMaxRules 锁定规则总数上限真的会截断,
// 而不是一个定义了却没有消费方的配置项。
func TestReloadTruncatesAtMaxRules(t *testing.T) {
	useConfig(t, true, true)
	cfg := *qyConfig.Load()
	cfg.GroupPricing.MaxRules = 2
	qyConfig.Store(&cfg)

	gdb := newTestDB(t)
	for _, m := range []string{"m1", "m2", "m3"} {
		require.NoError(t, gdb.Create(&Rule{
			GroupName: "vip", ModelName: m, Mode: ModeRatio, Value: dec("0.5"), Enabled: true,
		}).Error)
	}
	require.NoError(t, reload(true))

	assert.Len(t, current.Load().rules, 2, "超过 max_rules 的规则必须被截断")
	_, ok := lookupOverride("vip", "m3")
	assert.False(t, ok)
}
