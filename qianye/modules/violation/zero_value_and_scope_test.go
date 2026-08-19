package violation

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// zero_value_and_scope_test.go —— 三条被实测出来的配置缺陷。
//
//  1. count_weight=0 / priority=0 / sort_order=0 在**新建**时被 GORM 的
//     `default:` 标签吞掉,由数据库填成 1 / 100 / 100。管理端表单回填的仍是 0,
//     而 POST 只回 {id},运营看不到自己的配置被改写过。count_weight 那一条
//     方向是**多封人**:一条显式配成"只拦截、不计数"的规则,建出来之后每次
//     命中都在推进两条线。
//  2. 用户分组作用域写 `*` 保存成功、汇总里显示成"已绑定",而分组是精确查表 ——
//     一个真实分组都匹配不到。AI 审核那一侧尤其重:那道闸正是"哪些用户的
//     请求正文会被发往第三方"的唯一入口,写 `*` 的人本意是"全站都审"。
//  3. 解封 / 重置计数只清账号总量线,不清类型线,而两条线是 OR。

func newRuleEnv(t *testing.T) *gorm.DB {
	t.Helper()
	useTestConfig(t, "  enabled: true\n")
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(
		&Rule{}, &Category{}, &Counter{}, &CategoryCounter{}, &qymodel.AuditLog{}))
	prevHandle := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevHandle)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

func ruleCtx(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest("POST", "/api/qy/admin/violation/rules", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Set("id", 1)
	c.Set("username", "qy-admin")
	return c, rec
}

// TestExplicitZeroSurvivesRuleCreation 走**真实 handler**,断言落库的是管理员填的数。
//
// 不测下层函数:GORM 跳过零值这件事只发生在 Create 那一刻,纯函数断言全绿而
// 库里躺着一条会封人的规则 —— 那正是这条缺陷能活下来的原因。
func TestExplicitZeroSurvivesRuleCreation(t *testing.T) {
	t.Run("显式 count_weight=0 / priority=0 必须原样落库", func(t *testing.T) {
		gdb := newRuleEnv(t)
		c, rec := ruleCtx(t, `{"name":"只拦不计数","enabled":false,"mode":"shadow",
			"priority":0,"phase":"prompt","match_type":"keyword","pattern":"probe",
			"action":"record","fee_mode":"none","count_weight":0}`)
		adminCreateRule(c)
		require.Equal(t, 200, rec.Code, rec.Body.String())

		var row Rule
		require.NoError(t, gdb.Order("id desc").Take(&row).Error)
		assert.Equal(t, 0, row.CountWeight,
			"0 是一档合法配置(只处置、不推进任何一条线);被填成 1 等于让这条规则开始把人推向封号")
		assert.Equal(t, 0, row.Priority, "0 是最高优先级,被填成 100 等于'最先判'静默失效")
	})

	t.Run("漏传字段仍然落出厂默认", func(t *testing.T) {
		gdb := newRuleEnv(t)
		c, rec := ruleCtx(t, `{"name":"没填权重","enabled":false,"mode":"shadow",
			"phase":"prompt","match_type":"keyword","pattern":"probe",
			"action":"record","fee_mode":"none"}`)
		adminCreateRule(c)
		require.Equal(t, 200, rec.Code, rec.Body.String())

		var row Rule
		require.NoError(t, gdb.Order("id desc").Take(&row).Error)
		assert.Equal(t, 1, row.CountWeight, "漏传与显式 0 必须分得开")
		assert.Equal(t, 100, row.Priority)
	})
}

// TestWildcardGroupScopeIsRejected 挡住"保存成功但永不生效"。
//
// 分组作用域是精确查表,`*` 会被当成一个名叫 `*` 的分组。模型作用域才走 matchGlob,
// 而两列在同一张表单上紧挨着。
func TestWildcardGroupScopeIsRejected(t *testing.T) {
	base := func(groupScope string) *Rule {
		return &Rule{
			Name: "scope", Mode: ModeShadow, Phase: PhasePrompt, MatchType: MatchKeyword,
			Pattern: "x", Action: ActionRecord, FeeMode: FeeNone, CountWeight: 1,
			GroupScope: groupScope, GroupScopeMode: GroupScopeInclude,
		}
	}
	for _, tc := range []struct {
		name, scope string
		wantErr     bool
	}{
		{"裸通配", "*", true},
		{"混在名单里", "vip,*", true},
		{"前缀通配也不支持", "vip*", true},
		{"留空表示全站生效,合法", "", false},
		{"逐个列出,合法", "vip,wholesale", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRule(base(tc.scope))
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "不支持通配符",
				"报错必须说清它为什么一个分组都匹配不到,而不是静默存下一条死规则")
		})
	}

	t.Run("AI 审核作用域走同一道闸", func(t *testing.T) {
		s := &AIScope{
			Name: "全站", Enabled: true, GroupScope: "*", GroupScopeMode: GroupScopeInclude,
			PreSampleRateBps: 100, AsyncSampleRateBps: 100,
		}
		err := validateAIScope(s)
		require.Error(t, err, "这道闸是'哪些用户的请求正文会被发往第三方'的唯一入口")
		assert.Contains(t, err.Error(), "不支持通配符")
	})
}

// TestUnbanClearsBothCounterLines 守住"解封 + 清零计数"给的是真正的重新开始。
//
// 封号判据是 OR(anyReached):账号总量线与单类型线任一越线都要求处置。只清
// 总量线的话,被类型线封掉的账号解封后类型计数仍然停在阈值上,而判据是
// `after >= threshold` —— 下一次同类命中必然再次被封。类型窗口配成 -1
// (不限期限)时那个计数永远不会自然滚出,而管理端没有任何页面显示这条线。
func TestUnbanClearsBothCounterLines(t *testing.T) {
	gdb := newRuleEnv(t)
	require.NoError(t, gdb.Create(&Counter{
		UserId: 77, WindowStart: 1000, HitCount: 3, TotalCount: 12, BanCycle: 1,
	}).Error)
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 77, CategoryId: 2904, WindowStart: 1000, HitCount: 2, TotalCount: 6,
	}).Error)

	require.NoError(t, openNewBanCycle(77, true))

	var acc Counter
	require.NoError(t, gdb.Where("user_id = ?", 77).Take(&acc).Error)
	assert.Equal(t, 0, acc.HitCount)
	assert.Equal(t, 2, acc.BanCycle, "周期必须 +1,否则下次封号会撞唯一键静默失效")

	var cat CategoryCounter
	require.NoError(t, gdb.Where("user_id = ?", 77).Take(&cat).Error)
	assert.Equal(t, 0, cat.HitCount,
		"类型线不清的话,解封给的是'再犯一次就立刻再封',而封他的那条线运营看不到")
	assert.EqualValues(t, 6, cat.TotalCount, "终身累计是运营信息,不得被清掉")

	t.Run("没勾清零时两条线都原样保留", func(t *testing.T) {
		require.NoError(t, gdb.Model(&Counter{}).Where("user_id = ?", 77).
			Update("hit_count", 3).Error)
		require.NoError(t, gdb.Model(&CategoryCounter{}).Where("user_id = ?", 77).
			Update("hit_count", 2).Error)

		require.NoError(t, openNewBanCycle(77, false))

		var acc2 Counter
		require.NoError(t, gdb.Where("user_id = ?", 77).Take(&acc2).Error)
		assert.Equal(t, 3, acc2.HitCount)
		var cat2 CategoryCounter
		require.NoError(t, gdb.Where("user_id = ?", 77).Take(&cat2).Error)
		assert.Equal(t, 2, cat2.HitCount, "不勾清零就一条线都不该动")
	})
}

// TestResetCounterReportsTheCategoryLines 管理端重置必须把类型线的清零前状态
// 一起交出来 —— 那条线是把人封掉的那一条,而管理端没有任何页面显示它。
func TestResetCounterReportsTheCategoryLines(t *testing.T) {
	gdb := newRuleEnv(t)
	require.NoError(t, gdb.Create(&Counter{
		UserId: 88, WindowStart: 1000, HitCount: 4, TotalCount: 20, BanCycle: 1,
	}).Error)
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 88, CategoryId: 2904, WindowStart: 1000, HitCount: 2, TotalCount: 5,
	}).Error)

	_, cats, ok, err := resetUserCounter(context.Background(), gdb, 88)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, cats, 1)
	assert.EqualValues(t, 2904, cats[0].CategoryId)
	assert.Equal(t, 2, cats[0].HitCount)

	// 审计快照里必须真的能序列化出来(map 里塞的是结构体的话,前端展开看到的
	// 是一串 Go 字段名而不是列名)。
	raw, err := json.Marshal(cats)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"category_id":2904`)
}
