package violation

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// category_suggest_test.go —— 建议阈值:建议表本身、三态、只补空、影响面去重。
//
// 这里守的四件事都直接决定谁会被封号:
//
//   - 建议表里出现兜底类型 = 给"任何一条还没归类的规则"配上封号线;
//   - 三态塌成两态 = 管理端把"还没配"显示成一个看起来像 0 次就封的 0;
//   - 覆盖已配置的线 = 一次静默收紧,管理员手填的 10 次被 3 次顶掉;
//   - 影响面逐类相加 = 虚报越线人数,而那是管理员按下确认之前唯一会读的数。

// newSuggestEnv 接上一个承载类型、类型计数、策略档与审计表的内存库。
//
// 审计表必须建出来:apply 的"逐类留痕"是本文件断言的不变量之一,表不存在时
// audit.Write 只记一行日志就返回,断言会退化成假绿。
func newSuggestEnv(t *testing.T) *gorm.DB {
	t.Helper()
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 0\n  auto_ban_window_hours: 24\n")
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&Category{}, &CategoryCounter{}, &Rule{},
		&BanPolicy{}, &qymodel.AuditLog{}))
	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	prevSnap := policySnap.Load()
	prevNext := policyNextAt.Load()
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		policySnap.Store(prevSnap)
		policyNextAt.Store(prevNext)
		_ = sqlDB.Close()
	})
	return gdb
}

func suggestCtx(t *testing.T, method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set("id", 1)
	c.Set("username", "qy-admin")
	return c, rec
}

// seedSuggestCategories 造出与出厂种子同形的六行:全部 threshold=0。
func seedSuggestCategories(t *testing.T, gdb *gorm.DB) map[string]int64 {
	t.Helper()
	now := common.GetTimestamp()
	ids := make(map[string]int64, len(seedCategories))
	for i, s := range seedCategories {
		fallback := s.Key == FallbackCategoryKey
		row := Category{
			Key: s.Key, Name: s.Name, Remark: s.Desc,
			PublicTitle: s.Title, PublicDesc: s.Pub,
			Published: !fallback, Enabled: !fallback,
			WindowHours: 24, Threshold: 0, SortOrder: (i + 1) * 10,
			IsFallback: fallback, CreatedAt: now, UpdatedAt: now,
		}
		require.NoError(t, gdb.Create(&row).Error)
		ids[s.Key] = row.Id
	}
	return ids
}

// ─────────────────────── 建议表本身 ───────────────────────

// TestSuggestedThresholdsShape 守住建议表的四条硬约束。
//
// 最重的一条是"兜底类型没有建议值"。「未分类」是所有**还没被归类**的规则的落点
// (categoryForRule 把 category_id=0 折进它),给它一条线等于给一批没人知道内容
// 是什么的规则配上封号门槛 —— 而现网这一类下面确实挂着规则。
func TestSuggestedThresholdsShape(t *testing.T) {
	seeded := make(map[string]bool, len(seedCategories))
	for _, s := range seedCategories {
		seeded[s.Key] = true
	}

	t.Run("兜底类型绝不能有建议值", func(t *testing.T) {
		_, ok := suggestedCategoryThresholds[FallbackCategoryKey]
		assert.False(t, ok,
			"「未分类」是还没归类的规则的落点,给它配线等于给一批内容未知的规则配封号门槛")
	})

	for key, sug := range suggestedCategoryThresholds {
		t.Run(key, func(t *testing.T) {
			assert.Truef(t, seeded[key],
				"建议表里的 %q 在 seedCategories 里不存在:它永远匹配不到任何一行,"+
					"这一类会静默地一直没有建议值", key)
			assert.Positive(t, sug.Threshold, "建议值为 0 等于没有建议,应该直接从表里删掉这一行")
			assert.LessOrEqual(t, sug.Threshold, maxCategoryThreshold)
			assert.GreaterOrEqual(t, sug.WindowHours, 1)
			assert.LessOrEqual(t, sug.WindowHours, maxCategoryWindowHours)
			assert.NotEmpty(t, sug.Why,
				"理由会原样进确认弹窗:没有理由的数字只能被全盘照抄或全盘不用")
		})
	}

	// 严重度排序:意图越明确、误伤面越小的类型线越低。这不是审美 ——
	// 把 upstream(判据是上游的拒绝结论,混着大量误拒)配得比 jailbreak 还严,
	// 会让一批只是被上游偶尔拒绝的正常用户先被封掉。
	t.Run("线的高低跟着误伤面走", func(t *testing.T) {
		order := []string{CatJailbreak, CatReverse, CatPressure, CatDistill, CatUpstream}
		for i := 1; i < len(order); i++ {
			prev := suggestedCategoryThresholds[order[i-1]]
			cur := suggestedCategoryThresholds[order[i]]
			assert.LessOrEqualf(t, prev.Threshold, cur.Threshold,
				"%s 的建议线不该比 %s 更宽松:误伤面小的类型应该收得更紧",
				order[i-1], order[i])
		}
	})
}

// TestEverySeededCategoryHasASuggestedLine 是**反向**的那条约束:
// TestSuggestedThresholdsShape 保证"建议表里的键都是真类型",这里保证
// "真类型都在建议表里"。
//
// # 这不是补全癖,是实测抓到的一次静默失效
//
// 出厂种子最初是六类,建议表配了其中五类(兜底不配)。后来种子补进九条上游合规
// 类型(cyber_attack / minor_safety / … / fraud_spam),而建议表没跟着长。
// 后果不是"少了几个建议",而是:
//
//   - 「应用建议阈值」按下去之后,十四个公示类型里仍有九个 threshold=0;
//   - 管理端那九行显示"未配阈值 · 不计门槛",看起来与"配好了"只差一句措辞;
//   - **用户端那九行显示「这一类仅记录,不计封号门槛」** —— 项目方报的
//     「到多少次封号」在这九类上仍然没有答案,而它们恰恰是内容风险最高的九类。
//
// 也就是说:加一条种子类型而忘了加建议线,失效是完全静默的,页面上一切正常。
// 所以这条断言必须挂在种子表上,而不是挂在一份手抄的键清单上。
func TestEverySeededCategoryHasASuggestedLine(t *testing.T) {
	for _, s := range seedCategories {
		t.Run(s.Key, func(t *testing.T) {
			sug, ok := suggestedCategoryThresholds[s.Key]
			if s.Key == FallbackCategoryKey {
				assert.False(t, ok,
					"兜底类型必须留在建议表之外:它是所有还没归类的规则(以及 AI 判了违规却给不出类型的那一票)的落点")
				return
			}
			require.Truef(t, ok,
				"种子类型 %q 没有建议线:「应用建议阈值」跳过它之后,它在用户端会永远显示"+
					"「这一类仅记录,不计封号门槛」,而管理端看起来只是措辞不同", s.Key)
			assert.Positive(t, sug.Threshold)
		})
	}
}

// TestCategoryThresholdStateSeparatesUnsetFromDisabled 是"threshold=0 的语义"
// 在代码里的落点。
//
// 判定侧 unset 与 disabled 完全等价(categoryReached 两者都返回 false),正因为
// 等价,才容易被写成同一个分支 —— 而那一塌就是项目方看到的现象:六个类型全显示
// 一个 0,分不出"还没配"与"配了但关着",于是"到多少次封号"在界面上等于不存在。
func TestCategoryThresholdStateSeparatesUnsetFromDisabled(t *testing.T) {
	cases := []struct {
		name    string
		cat     Category
		want    string
		reached bool // 同一组输入在**判定侧**的结论,用来证明三态只影响界面
	}{
		{"出厂态:开关开着、线是 0", Category{Enabled: true, Threshold: 0}, thresholdUnset, false},
		{"开关关着、线也是 0", Category{Enabled: false, Threshold: 0}, thresholdUnset, false},
		{"负数线(脏数据)同样算没配", Category{Enabled: true, Threshold: -3}, thresholdUnset, false},
		{"配了线但开关关着", Category{Enabled: false, Threshold: 3}, thresholdDisabled, false},
		{"线正在生效", Category{Enabled: true, Threshold: 3}, thresholdActive, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, categoryThresholdState(tc.cat))
			assert.Equal(t, tc.reached, categoryReached(tc.cat, 999),
				"三态只用于界面表达,不能改变封号判定")
		})
	}
}

// ─────────────────────── 只补空,不覆盖 ───────────────────────

// TestApplySuggestedThresholdsOnlyFillsUnset 是这个动作最重的一条不变量。
//
// 覆盖一条管理员手填的线是**静默收紧**:界面上只是一个数字变了,而下一批命中
// 就会按新线把人封掉。这个用例把三种存量状态摆在一起,断言只有"还没配"那一档
// 被写,另外两档一个字节都不动。
func TestApplySuggestedThresholdsOnlyFillsUnset(t *testing.T) {
	gdb := newSuggestEnv(t)
	ids := seedSuggestCategories(t, gdb)

	// 破限:管理员已经手填了 10 次 —— 建议值 3 绝不能顶掉它。
	require.NoError(t, gdb.Model(&Category{}).Where("id = ?", ids[CatJailbreak]).
		Updates(map[string]any{"threshold": 10, "enabled": true}).Error)
	// 逆向:阈值开关被关掉了,线仍然是 0 —— 写进去也不生效,所以不写。
	require.NoError(t, gdb.Model(&Category{}).Where("id = ?", ids[CatReverse]).
		Update("enabled", false).Error)

	t.Run("没有 confirm 一律 409,并把影响面带回去", func(t *testing.T) {
		c, rec := suggestCtx(t, "POST", "/violation/categories/apply-suggested", `{"confirm":false}`)
		adminApplySuggestedThresholds(c)
		require.Equal(t, 409, rec.Code)
		assert.Contains(t, rec.Body.String(), "confirm_required")

		var still Category
		require.NoError(t, gdb.Where("id = ?", ids[CatDistill]).Take(&still).Error)
		assert.Zero(t, still.Threshold, "被 409 挡下的那次绝不能已经写了一半")
	})

	t.Run("confirm 之后只补空", func(t *testing.T) {
		c, rec := suggestCtx(t, "POST", "/violation/categories/apply-suggested", `{"confirm":true}`)
		adminApplySuggestedThresholds(c)
		require.Equal(t, 200, rec.Code, rec.Body.String())

		want := map[string]struct {
			threshold int
			why       string
		}{
			CatJailbreak:        {10, "管理员手填的线不能被建议值顶掉"},
			CatReverse:          {0, "阈值开关关着的类型不写:写进去也不生效,界面却会显示一个像在生效的数字"},
			CatPressure:         {suggestedCategoryThresholds[CatPressure].Threshold, "还没配过线,应该被补上"},
			CatDistill:          {suggestedCategoryThresholds[CatDistill].Threshold, "还没配过线,应该被补上"},
			CatUpstream:         {suggestedCategoryThresholds[CatUpstream].Threshold, "还没配过线,应该被补上"},
			FallbackCategoryKey: {0, "兜底类型没有建议值,永远不该被这个动作碰到"},
		}
		for key, exp := range want {
			var got Category
			require.NoError(t, gdb.Where("id = ?", ids[key]).Take(&got).Error)
			assert.Equalf(t, exp.threshold, got.Threshold, "%s: %s", key, exp.why)
		}
	})

	t.Run("逐类留痕,且快照是整行不是拼出来的几列", func(t *testing.T) {
		// 期望条数由建议表算出来,不写死:上面刻意把 jailbreak 手填成 10、
		// 把 reverse 的开关关掉,所以被补上的正好是"有建议线的类型"减去这两个。
		// 写死一个数字会在种子表长出新类型时变成一次假失败,而那次失败与本用例
		// 要守的"只补空"毫无关系。
		wantAudited := len(suggestedCategoryThresholds) - 2
		var logs []qymodel.AuditLog
		require.NoError(t, gdb.Where("action = ?", "categories.apply_suggested").Find(&logs).Error)
		require.Len(t, logs, wantAudited,
			"每个被补上的类型各一行:一条汇总行答不出「哪几类被配上了、当时写的是几次」")
		for _, l := range logs {
			assert.Equal(t, qymodel.ResultOK, l.Result)
			require.NotEmpty(t, l.AfterSnap)
			// published 必须是库里那一行的真值。拿 suggestionView 上那几列拼一个
			// Category 出来会让它恒为 false,于是审计把一个**正在对用户公示**的
			// 类型记成"未公示"—— 而事后要回答的问题正是"当时这一类对用户可见吗"。
			// 少几列的快照比没有快照更坏:它看起来是可信的。
			for _, snap := range []string{l.BeforeSnap, l.AfterSnap} {
				var got map[string]any
				require.NoError(t, common.Unmarshal([]byte(snap), &got))
				assert.Equalf(t, true, got["published"],
					"审计快照把一个已公示的类型记成未公示: %s", snap)
			}
		}
	})

	t.Run("补完之后再点一次:已经没有可应用的类型", func(t *testing.T) {
		c, rec := suggestCtx(t, "POST", "/violation/categories/apply-suggested", `{"confirm":true}`)
		adminApplySuggestedThresholds(c)
		assert.Equal(t, 400, rec.Code,
			"重复点击必须是一次明确的拒绝,而不是一次什么都没做的 200")
	})
}

// ─────────────────────── 影响面 ───────────────────────

// TestSuggestionPreviewAffectedUsersDeduplicates 守的是确认弹窗上那个数字。
//
// 逐类相加会把"同时越过破限线与高压线"的同一个人算两次。虚报的方向是"看起来
// 更吓人",于是要么吓得不敢点(功能白做),要么发现虚报之后不再信任这个数
// (下次直接点过去)—— 两种都比不给数更糟。
func TestSuggestionPreviewAffectedUsersDeduplicates(t *testing.T) {
	gdb := newSuggestEnv(t)
	ids := seedSuggestCategories(t, gdb)
	now := common.GetTimestamp()

	// 8801 同时在两类上越线;8802 只在一类上越线;8803 的计数在窗口外。
	rows := []CategoryCounter{
		{UserId: 8801, CategoryId: ids[CatPressure], HitCount: 9, WindowStart: now - 60},
		{UserId: 8801, CategoryId: ids[CatDistill], HitCount: 9, WindowStart: now - 60},
		{UserId: 8802, CategoryId: ids[CatDistill], HitCount: 9, WindowStart: now - 60},
		{UserId: 8803, CategoryId: ids[CatDistill], HitCount: 99, WindowStart: now - 72*3600},
	}
	for i := range rows {
		require.NoError(t, gdb.Create(&rows[i]).Error)
	}

	preview, err := buildSuggestionPreview(context.Background(), gdb)
	require.NoError(t, err)

	assert.Equal(t, 2, preview.AffectedUsers,
		"8801 同时越两条线只能算一个人;8803 的计数落在窗口外,不该算进来")

	byKey := make(map[string]suggestionView, len(preview.Items))
	for _, it := range preview.Items {
		byKey[it.Key] = it
	}
	// 逐类的数仍然是逐类的:去重只发生在总数上。两个数并存是刻意的 ——
	// 管理员既要知道"总共多少人",也要知道"是哪一类把人推过去的"。
	assert.Equal(t, 1, byKey[CatPressure].Impact.Matched)
	assert.Equal(t, 2, byKey[CatDistill].Impact.Matched)
	// 本夹具里每一类都还没配线,所以"可应用"就等于"建议表里有几条"。
	// 用建议表的长度算,而不是写死一个数:种子长出新类型时这个数会跟着长,
	// 而写死的那一份会在一次与本用例无关的改动上变红。
	assert.Equal(t, len(suggestedCategoryThresholds), preview.ApplicableCount,
		"每一个有建议线的内置类型都该可应用;兜底类型没有建议值,不在其中")

	// 处置动作必须一并给出:类型线只决定"几次",越线之后是记录/限制/封号
	// 由用户所在分组的策略档决定。不带上它,确认弹窗只能说"会触发处置"。
	assert.NotEmpty(t, preview.AccountAction)
	assert.Equal(t, thresholdSemanticsAnyLine, preview.ThresholdSemantics)
}

// TestSuggestionPreviewSkipReasonAlwaysExplains 守的是"为什么这一行是灰的"。
//
// 一个只灰不给理由的行会被当成 bug,而管理员的下一步就是绕过界面直接改库。
func TestSuggestionPreviewSkipReasonAlwaysExplains(t *testing.T) {
	gdb := newSuggestEnv(t)
	ids := seedSuggestCategories(t, gdb)
	require.NoError(t, gdb.Model(&Category{}).Where("id = ?", ids[CatJailbreak]).
		Update("threshold", 10).Error)
	require.NoError(t, gdb.Model(&Category{}).Where("id = ?", ids[CatReverse]).
		Update("enabled", false).Error)

	preview, err := buildSuggestionPreview(context.Background(), gdb)
	require.NoError(t, err)
	require.NotEmpty(t, preview.Items)
	for _, it := range preview.Items {
		if it.Applicable {
			assert.Empty(t, it.SkipReason)
			continue
		}
		assert.NotEmptyf(t, it.SkipReason, "%s 被跳过却没有给理由", it.Key)
	}
}
