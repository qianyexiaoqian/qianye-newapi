package violation

import (
	"context"
	"net/http"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// aireview_nofallback_test.go —— 全局抽样率下线之后的三条不变量。
//
// 项目方的原话:「移除兜底策略,策略要手动添加,绑定分组。才能生效。
// 策略可以选择指定的审核渠道不填写则默认审核渠道。全局审核抽样率移除一下」。
//
// 这三条各自挡住一种"改完之后看起来一样、实际上完全不同"的失效:
//
//  1. **一条策略都没有 ⇒ 连抽样函数都不调用。** 这是零开销契约在"空表"这一
//     格上的形态。删掉兜底之后 scopeFor 返回 0/0,而 `preBps > 0 &&` 的短路
//     才是真正挡住 sampleAI 的那道闸 —— 把它写成 `sampleAI(preBps) && preBps > 0`
//     行为完全相同、外部一次调用都不多发,只有 aiSampleRolls 会变。
//     没有这条断言,那次改写会静默通过。
//  2. **指定的渠道说了算,而且失效时绝不回落。** 回落是静默降级:内容被发去
//     一个运营明确没有选的端点,而界面上什么都没变。
//  3. **存量的全局抽样率不会被静默丢弃。** 它转成一条策略行,启用与否取决于
//     它当时是不是真的在生效。

// ─────────────── 一、没有任何策略 = 零开销 ───────────────

// TestAIReviewWithoutMatchingScopeIsFreeOfCharge 钉住"没有策略命中 ⇒ 零开销"。
//
// 断言的量是 **aiSampleRolls 的增量**,不是"发了几次外部调用"。两者的区别就是
// 这条测试存在的全部理由:一个"先摇骰子、抽中之后再看在不在作用域内"的实现
// 同样一次调用都不会发,而它会让界面上那个百分比彻底失真 —— 运营填的 10%
// 变成"命中作用域的 10% 乘以一个谁也说不出来的数"。
//
// 三格覆盖删掉兜底之后新出现的两种"空":策略表整个是空的(全新站点、以及
// 迁移之后决定不留那条兜底档的站点),以及有策略但这次请求谁都不匹配。
func TestAIReviewWithoutMatchingScopeIsFreeOfCharge(t *testing.T) {
	useGenerousScanBudget(t)
	rule, err := compile(Rule{
		Id: 9401, Enabled: true, Mode: ModeEnforce, Phase: PhasePrompt,
		MatchType: MatchAIReview, Action: ActionBlock,
	})
	require.NoError(t, err)
	asyncRule, err := compile(Rule{
		Id: 9402, Enabled: true, Mode: ModeEnforce, Phase: PhasePostAsync,
		MatchType: MatchAIReview, Action: ActionRecord,
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		scopes    []*aiScopeRT
		group     string
		wantRolls int64
		why       string
	}{
		{
			name: "策略表是空的:一次骰子都不摇", scopes: nil, group: "default",
			wantRolls: 0,
			why: "全局抽样率与兜底档已下线 —— 空表就是「不审核」," +
				"任何一个非 0 的开销都说明还藏着一条隐含的兜底",
		},
		{
			name: "有策略但这次请求谁都不匹配", group: "default",
			scopes: []*aiScopeRT{
				scopeRT("自助注册", "", "selfserve", GroupScopeInclude, 10000, 10000),
			},
			wantRolls: 0,
			why:       "作用域闸排在抽样之前,这个顺序是契约不是优化",
		},
		{
			name: "命中的那一档两个时机都是 0(免审名单)", group: "internal",
			scopes: []*aiScopeRT{
				scopeRT("内部免审", "", "internal", GroupScopeInclude, 0, 0),
			},
			wantRolls: 0,
			why:       "两个都填 0 是免审名单的字面写法,它同样要零开销",
		},
		{
			// 对照组。没有它,上面三个 0 可能只是因为整条 AI 路径根本没接上。
			name: "命中一档 100% 的策略:两个时机各摇一次", group: "selfserve",
			scopes: []*aiScopeRT{
				scopeRT("自助注册", "", "selfserve", GroupScopeInclude, 10000, 10000),
			},
			wantRolls: 2,
			why:       "两个时机各摇各的骰子(同时抽中时只发一次调用)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
				_, _ = w.Write([]byte(okVerdict(false, "none", 0.1, 1, 1)))
			})
			rt := rtForServer(srv.URL, 2000)
			rt.Scopes = tc.scopes
			snap := &snapshot{
				promptRules: []*compiledRule{rule}, asyncRules: []*compiledRule{asyncRule},
				hasAIPrompt: true, hasAIAsync: true, ai: rt,
			}
			c, info := newRelayTestCtx(t)
			in := scanInput{Model: "gpt-4o", Group: tc.group, Text: "内容"}

			before := aiSampleRolls.Load()
			assert.NoError(t, aiPreReview(c, info, snap, in, in.Text))
			assert.Equal(t, tc.wantRolls, aiSampleRolls.Load()-before,
				"抽样函数被调用的次数:%s", tc.why)
		})
	}
}

// ─────────────── 二、指定审核渠道 ───────────────

// TestPickAIChannelsHonoursScopeChannel 钉住"指定了就只用那一个,失效就不发"。
//
// 加权随机那一半也一起断言:留空的准确含义是「按权重在全部启用渠道里随机」,
// 不是「用某一个固定渠道」。界面上这两句话给运营的预期完全不同。
func TestPickAIChannelsHonoursScopeChannel(t *testing.T) {
	rt := &aiRuntime{
		Channels: []*aiChannelRT{
			{Id: 1, Name: "主用", URL: "https://a.invalid/v1/chat/completions", Model: "m", Weight: 9},
			{Id: 2, Name: "备用", URL: "https://b.invalid/v1/chat/completions", Model: "m", Weight: 1},
		},
		totalWeight: 10,
	}

	tests := []struct {
		name      string
		scope     *aiScopeRT
		wantNames []string
		wantAny   bool // 结果是随机的,只断言条数与"来自渠道池"
		why       string
	}{
		{
			name:  "留空:按权重随机,最多试 maxAIAttempts 个",
			scope: &aiScopeRT{ChannelId: 0}, wantAny: true,
			why: "权重是运营表达主备的唯一方式;恒定顺序会让备用渠道永远不被验证",
		},
		{
			name:  "指定备用渠道:只用它,不带第二次尝试",
			scope: &aiScopeRT{ChannelId: 2}, wantNames: []string{"备用"},
			why: "指定的字面意思就是只发给它;要故障转移请留空走加权随机池",
		},
		{
			name:  "指定的渠道已停用/已删除:一个都不返回,绝不回落到随机池",
			scope: &aiScopeRT{ChannelId: 404}, wantNames: []string{},
			why: "回落会把用户内容发去一个运营明确没有选的端点 —— " +
				"而「只能发给这一个」往往正是指定渠道的全部理由",
		},
		{
			name:  "sc 为 nil(没有任何策略命中,理论上到不了这里)也要安全",
			scope: nil, wantAny: true,
			why: "热路径上一次 nil 解引用就是一次 500,而这条路是 relay 主干",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pickAIChannels(rt, tc.scope)
			if tc.wantAny {
				assert.Len(t, got, maxAIAttempts, tc.why)
				for _, ch := range got {
					assert.Contains(t, []string{"主用", "备用"}, ch.Name, tc.why)
				}
				return
			}
			names := make([]string, 0, len(got))
			for _, ch := range got {
				names = append(names, ch.Name)
			}
			assert.Equal(t, tc.wantNames, names, tc.why)
		})
	}
}

// TestAIReviewPinnedChannelDownYieldsNoChannel 把上面那条推到端到端。
//
// pickAIChannels 返回空之后的**处置方向**才是真正要钉的东西:必须是
// OutcomeNoChannel(不审核 + 放行 + 明细留痕),而不是"随便找一个发出去"。
// 这条断言的是 runAIReview 的结局,所以任何在它之内加回落的改动都会红。
func TestAIReviewPinnedChannelDownYieldsNoChannel(t *testing.T) {
	srv := newFakeReviewServer(t, func(w http.ResponseWriter, _ string) {
		_, _ = w.Write([]byte(okVerdict(true, "jailbreak", 0.9, 1, 1)))
	})
	rt := rtForServer(srv.URL, 2000) // 夹具里那个渠道的 id 是 1

	t.Run("指定一个不在快照里的渠道 → no_channel,一次调用都不发", func(t *testing.T) {
		out := runAIReview(context.Background(), rt, &aiScopeRT{ChannelId: 77}, "内容", 1000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeNoChannel, out.Outcome,
			"指定的渠道不可用时必须是「无可用渠道」,绝不是悄悄换一个发出去")
		assert.False(t, out.decided(), "未判定 = 放行,与本模块其余失败方向一致")
		assert.Equal(t, int64(0), srv.calls.Load(),
			"回落到随机池的实现会在这里发出一次调用 —— 内容就出境了")
	})

	t.Run("指定快照里那个渠道 → 正常送审", func(t *testing.T) {
		out := runAIReview(context.Background(), rt, &aiScopeRT{ChannelId: 1}, "内容", 2000)
		require.NotNil(t, out)
		assert.Equal(t, OutcomeViolation, out.Outcome)
		assert.Equal(t, int64(1), srv.calls.Load())
		assert.Equal(t, int64(1), out.ChannelId, "明细上要留下真正用了哪个渠道")
	})
}

// TestValidateAIScopeChannelId 是渠道那一格的纯校验闸。
//
// 渠道**存不存在、启没启用**由 adminUpsertAIScope 挡(那里才有库句柄);
// 这里只挡纯粹的脏数据 —— 负数 id 永远解析不到任何渠道,而它的表现是这一档
// 从此每次都走 no_channel,只有成本页上看得出来。
func TestValidateAIScopeChannelId(t *testing.T) {
	tests := []struct {
		name      string
		channelId int64
		wantErr   bool
	}{
		{"不指定(0)是合法的,含义是按权重随机", 0, false},
		{"指定一个正数 id 合法(存不存在由写入闸查库确认)", 12, false},
		{"负数 id 非法", -1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := AIScope{Name: "自助注册", GroupScope: "selfserve",
				GroupScopeMode: GroupScopeInclude, AsyncSampleRateBps: 1000,
				ChannelId: tc.channelId}
			err := validateAIScope(&row)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.channelId, row.ChannelId)
		})
	}
}

// TestBuildAIScopesCarriesChannelId 钉住这一列真的进了快照。
//
// 少了这一步,库里配得好好的、汇总表上也显示得好好的,而热路径读到的是 0 ——
// 于是这一档静默地回到了加权随机,内容被发去运营没选的端点。
// 这正是本模块反复出现的"保存成功、界面正常、线上不是那么回事"。
func TestBuildAIScopesCarriesChannelId(t *testing.T) {
	gdb := newAIWiringDB(t)
	require.NoError(t, gdb.Create(&AIScope{
		Id: 1, Name: "自助注册", Enabled: true, Priority: 10,
		GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude,
		AsyncSampleRateBps: 5000, ChannelId: 3,
	}).Error)
	scopes, err := buildAIScopes(gdb)
	require.NoError(t, err)
	require.Len(t, scopes, 1)
	assert.Equal(t, int64(3), scopes[0].ChannelId)
}

// ─────────────── 三、存量全局抽样率的迁移 ───────────────

// newAILegacySettingDB 建一张**带 sample_rate_bps 列**的设置表,复现现网形状。
//
// 结构体上已经没有这一列了,所以只能用 ADD COLUMN 追加 —— 而这恰好就是升级
// 上来的库的真实形状(单空格、不带引号,glebarez 的 DropColumn 正则匹配不上
// 的那一种,见 severity_drop_test.go)。
func newAILegacySettingDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, gdb.AutoMigrate(&AISetting{}, &AIScope{}))
	require.NoError(t, gdb.Exec(
		"ALTER TABLE qy_violation_ai_setting ADD COLUMN sample_rate_bps integer NOT NULL DEFAULT 0").Error)
	return gdb
}

// TestMigrateAISampleRateToScope 是"不要静默丢弃一个正在生效的配置"的可执行版本。
//
// 全局抽样率曾经是**作用域都不命中时的兜底**。直接删列等于把那部分流量从
// "按 X% 送审"改成"一律不送审"—— 一次没有任何症状的风控收缩。所以要先把值
// 搬成一条覆盖全部分组的策略。
//
// 表里那四格分别对应四种现网状态,判据是"这条策略建不建、建出来开不开"。
func TestMigrateAISampleRateToScope(t *testing.T) {
	tests := []struct {
		name        string
		legacyBps   int
		aiEnabled   bool
		wantCreated bool
		wantEnabled bool
		why         string
	}{
		{
			name: "值为 0:什么都不建", legacyBps: 0, aiEnabled: true,
			wantCreated: false,
			why: "这一列的 0 有明确定义(0 = 不抽,出厂就是 0)—— " +
				"不存在「零值与未设置不可分」,0 就是不抽,没有东西要搬",
		},
		{
			name: "值非 0 且总开关开着:建一条**启用**的策略", legacyBps: 500, aiEnabled: true,
			wantCreated: true, wantEnabled: true,
			why: "那 5% 正在把用户内容发往第三方 —— 建成停用的等于删掉一条" +
				"正在生效的风控,而且没有任何症状",
		},
		{
			name: "值非 0 但总开关关着:建一条**停用**的策略,值原样留着", legacyBps: 500, aiEnabled: false,
			wantCreated: true, wantEnabled: false,
			why: "那个数字一次都没生效过。建成启用的话,管理员下次打开总开关" +
				"就会突然全站送审,而他按新语义的预期是「我还没建任何策略,所以不会审」",
		},
		{
			name: "100% 也照搬", legacyBps: 10000, aiEnabled: true,
			wantCreated: true, wantEnabled: true, why: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newAILegacySettingDB(t)
			require.NoError(t, gdb.Create(&AISetting{
				Id: 1, Enabled: tc.aiEnabled, PreTimeoutMs: 1500,
				AsyncTimeoutMs: 8000, MaxInputChars: defaultAIMaxInputChars,
			}).Error)
			require.NoError(t, gdb.Exec(
				"UPDATE qy_violation_ai_setting SET sample_rate_bps = ? WHERE id = 1",
				tc.legacyBps).Error)

			created, bps, err := migrateAISampleRateToScope(context.Background(), gdb)
			require.NoError(t, err)
			assert.Equal(t, tc.wantCreated, created, tc.why)

			var rows []AIScope
			require.NoError(t, gdb.Order("id asc").Find(&rows).Error)
			if !tc.wantCreated {
				assert.Empty(t, rows, tc.why)
				return
			}
			require.Len(t, rows, 1)
			got := rows[0]
			assert.Equal(t, tc.legacyBps, bps)
			assert.Equal(t, migratedAIScopeName, got.Name)
			assert.Equal(t, tc.wantEnabled, got.Enabled, tc.why)
			assert.Equal(t, tc.legacyBps, got.PreSampleRateBps,
				"旧兜底对两个时机给的是同一个数,必须原样搬过来")
			assert.Equal(t, tc.legacyBps, got.AsyncSampleRateBps)
			assert.Empty(t, got.GroupScope, "作用域全空 = 匹配一切,这就是「兜底」的字面写法")
			assert.Empty(t, got.ModelScope)
			assert.Equal(t, 10000, got.Priority,
				"它是「别的都不匹配时」的那一档,必须排在最后 —— "+
					"既有策略的默认优先级是 100,任何现存策略都排在它前面")
			assert.Contains(t, got.Remark, "全局审核抽样率",
				"备注要说清这条是从哪来的,否则三个月后没人知道能不能删")

			t.Run("重复执行是幂等的", func(t *testing.T) {
				again, _, err := migrateAISampleRateToScope(context.Background(), gdb)
				require.NoError(t, err)
				assert.False(t, again, "第二次不该再建一条")
				var n int64
				require.NoError(t, gdb.Model(&AIScope{}).Count(&n).Error)
				assert.EqualValues(t, 1, n)
			})
		})
	}

	t.Run("设置行还不存在(全新部署)时是 no-op", func(t *testing.T) {
		gdb := newAILegacySettingDB(t)
		created, bps, err := migrateAISampleRateToScope(context.Background(), gdb)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Zero(t, bps)
	})

	t.Run("列已经不在时是 no-op(已迁过 / 全新建的库)", func(t *testing.T) {
		gdb := newAIWiringDB(t) // 按当前结构体建表:根本没有这一列
		require.NoError(t, gdb.Create(&AISetting{Id: 1, Enabled: true}).Error)
		created, _, err := migrateAISampleRateToScope(context.Background(), gdb)
		require.NoError(t, err)
		assert.False(t, created)
	})
}

// TestDropLegacyAISampleRateColumnIsIdempotent 是删列那一步。
//
// 判据必须是**迁移之后 HasColumn 为 false**,而不是"迁移没报错":
// GORM 的 Migrator().DropColumn 在 glebarez/sqlite 上对"单空格不带引号"的列
// (也就是 ADD COLUMN 追加出来的、现网那张表的形状)会**静默失败**并返回 nil。
// 完整实测见 severity_drop_test.go —— 这里共用同一段 dropLegacyColumn。
func TestDropLegacyAISampleRateColumnIsIdempotent(t *testing.T) {
	gdb := newAILegacySettingDB(t)
	require.NoError(t, gdb.Create(&AISetting{
		Id: 1, Enabled: true, PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars,
	}).Error)
	require.True(t, gdb.Migrator().HasColumn(&AISetting{}, legacyAISampleRateColumn),
		"夹具本身要真的造出这一列,否则这条测试测的是空气")

	dropped, err := dropLegacyAISampleRateColumn(context.Background(), gdb)
	require.NoError(t, err)
	assert.True(t, dropped)
	assert.False(t, gdb.Migrator().HasColumn(&AISetting{}, legacyAISampleRateColumn),
		"必须真的删掉 —— 一条「报告成功却什么都没做」的迁移比不写更糟")

	again, err := dropLegacyAISampleRateColumn(context.Background(), gdb)
	require.NoError(t, err)
	assert.False(t, again, "第二遍必须是 no-op:每个节点启动时各跑一次")

	t.Run("删列不动设置行的其余列", func(t *testing.T) {
		var row AISetting
		require.NoError(t, gdb.Where("id = ?", 1).Take(&row).Error)
		assert.Equal(t, 1500, row.PreTimeoutMs)
	})
}

// TestMigrateThenDropKeepsTheNumberSomewhere 是两步合起来的那条不变量:
// **那个数字在任何时刻都至少存在于一个地方。**
//
// 顺序反过来(先删列再建行)时,任何在两步之间崩掉的进程都会把它永久丢掉,
// 而丢掉之后没有任何地方能告诉运营"原来配的是多少"。这条测试先跑完整流程,
// 再确认值确实落在了策略行上、而列已经不在。
func TestMigrateThenDropKeepsTheNumberSomewhere(t *testing.T) {
	gdb := newAILegacySettingDB(t)
	require.NoError(t, gdb.Create(&AISetting{
		Id: 1, Enabled: true, PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars,
	}).Error)
	require.NoError(t, gdb.Exec(
		"UPDATE qy_violation_ai_setting SET sample_rate_bps = 500 WHERE id = 1").Error)

	created, _, err := migrateAISampleRateToScope(context.Background(), gdb)
	require.NoError(t, err)
	require.True(t, created)
	dropped, err := dropLegacyAISampleRateColumn(context.Background(), gdb)
	require.NoError(t, err)
	require.True(t, dropped)

	var row AIScope
	require.NoError(t, gdb.Where("name = ?", migratedAIScopeName).Take(&row).Error)
	assert.Equal(t, 500, row.PreSampleRateBps, "那 5% 必须还在,只是换了个地方")
	assert.False(t, gdb.Migrator().HasColumn(&AISetting{}, legacyAISampleRateColumn))

	// 而且它真的能被装配进快照并生效 —— 迁移出来一条"存在但不生效"的策略
	// 与丢掉它是同一件事。
	useAIReviewKey(t)
	require.NoError(t, gdb.AutoMigrate(&AIChannel{}))
	require.NoError(t, gdb.Create(&AIChannel{
		Name: "fake", BaseUrl: "https://example.invalid/v1", Model: "m",
		Weight: 1, Enabled: true,
	}).Error)
	rt, err := buildAIRuntime(gdb, true, seedAIVocabulary())
	require.NoError(t, err)
	require.NotNil(t, rt, "迁移出来的策略必须能让整份配置继续生效")
	_, pre, async := rt.scopeFor("gpt-4o", "谁都没配过的分组")
	assert.Equal(t, 500, pre, "旧兜底覆盖的正是这种「谁都不匹配」的请求")
	assert.Equal(t, 500, async)
}
