package violation

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_banpolicy_test.go —— 策略档管理接口的回归。
//
// 这里必须走 HTTP handler 而不是直接调下层函数:被测的两条不变量
// (兜底档删不掉、收紧要二次确认)**只存在于 handler 里**。测下层函数会得到
// 一组全绿的断言,而删除接口照样能把兜底档删掉。
//
// 接测试库的做法与本仓既有模块一致(见 modules/ticket/testdb_test.go):
// 生产代码一律走 db.Get() 自取句柄,把 *gorm.DB 传进被测函数等于测一条
// 生产里不存在的调用形态。

// 句柄与健康位复用同包已有的两份 linkname(rules_ctx_test.go 的
// qyDBHandleForCtxTest、nil_array_json_test.go 的 qyDBHealthyForJSONTest)。
// 同一个包里对同一个符号声明两次会在链接期报 "symbol redeclared"。

// newBanPolicyEnv 接上一个只承载策略表与计数表的内存库。
func newBanPolicyEnv(t *testing.T) *gorm.DB {
	t.Helper()
	useTestConfig(t, "  enabled: true\n  auto_ban_threshold: 10\n  auto_ban_window_hours: 24\n")
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	// Ban 与审计表一并建出来:两条不变量("保存不处置任何人"、"删兜底档留痕")
	// 分别靠这两张表证伪。表不存在时写入只记一行日志就返回,断言会退化成假绿。
	require.NoError(t, gdb.AutoMigrate(&BanPolicy{}, &Counter{}, &Ban{}, &qymodel.AuditLog{}))
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

func banPolicyCtx(t *testing.T, method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
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

// TestDefaultBanPolicyCannotBeDeleted 是这一整块最硬的一条不变量。
//
// 删掉兜底档 = 让所有没有专属策略的用户分组落进一个不存在的策略。本仓在
// default 用户分组上踩过同一个坑,所以这条断言必须打在**接口**上:
// 只在模型层加一个 is_default 布尔不会阻止任何一次 DELETE。
func TestDefaultBanPolicyCannotBeDeleted(t *testing.T) {
	gdb := newBanPolicyEnv(t)
	now := common.GetTimestamp()
	def := BanPolicy{IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, gdb.Create(&def).Error)
	vip := BanPolicy{UserGroup: "vip", Enabled: true, WindowHours: 24, Threshold: 3,
		Action: PolicyActionRestrict, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, gdb.Create(&vip).Error)

	t.Run("兜底档拒绝删除", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "DELETE", "/violation/ban-policies/1", "")
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(def.Id, 10)}}
		adminDeleteBanPolicy(c)
		assert.Equal(t, 400, rec.Code)
		var still BanPolicy
		require.NoError(t, gdb.Where("id = ?", def.Id).Take(&still).Error,
			"兜底档必须还在:它是全部没配分组的唯一落点")
	})

	t.Run("普通档可以删除", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "DELETE", "/violation/ban-policies/2", "")
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(vip.Id, 10)}}
		adminDeleteBanPolicy(c)
		assert.Equal(t, 200, rec.Code)
		var n int64
		require.NoError(t, gdb.Model(&BanPolicy{}).Where("id = ?", vip.Id).Count(&n).Error)
		assert.Zero(t, n)
	})

	t.Run("删完之后兜底档仍然是解析的落点", func(t *testing.T) {
		invalidateBanPolicies()
		got := resolveBanPolicy("vip")
		assert.True(t, got.IsDefault, "专属档删掉之后必须回落兜底,而不是落空")
		assert.Equal(t, 10, got.Threshold)
	})
}

// TestTighteningBanPolicyRequiresExplicitConfirm 守二次确认。
//
// 阈值一降,滚动窗口里早就攒好的计数会**立刻**把一批存量账号推过线。
// 没有这道闸,管理员按下保存时手上唯一的信息是"我把 10 改成了 3"。
func TestTighteningBanPolicyRequiresExplicitConfirm(t *testing.T) {
	gdb := newBanPolicyEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&BanPolicy{
		IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}).Error)
	// 两个已经攒到 5 次的账号:阈值降到 3 会立刻把他们收走。
	require.NoError(t, gdb.Create(&Counter{UserId: 101, HitCount: 5, WindowStart: now}).Error)
	require.NoError(t, gdb.Create(&Counter{UserId: 102, HitCount: 7, WindowStart: now}).Error)
	seedMainUsersForImpact(t, 101, 102)

	body := `{"user_group":"vip","enabled":true,"window_hours":24,"threshold":3,"action":"ban"}`

	t.Run("不带 confirm 被 409 挡下,且什么都没写", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies", body)
		adminUpsertBanPolicy(c, false)
		assert.Equal(t, 409, rec.Code)
		assert.Contains(t, rec.Body.String(), "confirm_required")
		assert.Contains(t, rec.Body.String(), `"matched":2`,
			"影响面必须随 409 一起回去,否则前端要再发一次请求,而两次之间计数还会变")
		var n int64
		require.NoError(t, gdb.Model(&BanPolicy{}).Where("user_group = ?", "vip").Count(&n).Error)
		assert.Zero(t, n, "被确认闸挡下时绝不能已经写进去了")
	})

	t.Run("带 confirm 才落库", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies",
			`{"user_group":"vip","enabled":true,"window_hours":24,"threshold":3,"action":"ban","confirm":true}`)
		adminUpsertBanPolicy(c, false)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var row BanPolicy
		require.NoError(t, gdb.Where("user_group = ?", "vip").Take(&row).Error)
		assert.Equal(t, 3, row.Threshold)
		assert.False(t, row.IsDefault, "普通档绝不能被写成第二个兜底档")
	})

	t.Run("放宽不需要确认", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies",
			`{"user_group":"vip","enabled":true,"window_hours":24,"threshold":50,"action":"record"}`)
		adminUpsertBanPolicy(c, false)
		assert.Equal(t, 200, rec.Code,
			"放宽不会立刻处置任何人,再要一次确认只会训练管理员无脑点确认")
	})
}

// TestUpsertDefaultBanPolicyStaysDefault 守的是"兜底档只有一行、且改不成普通档"。
//
// 路径决定身份:请求体里带什么都不该改变这一点。能把兜底档降级成普通档,
// 与能把它删掉是同一件事。
func TestUpsertDefaultBanPolicyStaysDefault(t *testing.T) {
	gdb := newBanPolicyEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&BanPolicy{
		IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}).Error)

	seedMainUsersForImpact(t)

	// 请求体里塞一个分组名、并把 enabled 关掉 —— 两者都必须被忽略。
	c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies/default",
		`{"user_group":"vip","enabled":false,"window_hours":48,"threshold":20,"action":"restrict","confirm":true}`)
	adminUpsertBanPolicy(c, true)
	require.Equal(t, 200, rec.Code, rec.Body.String())

	var rows []BanPolicy
	require.NoError(t, gdb.Find(&rows).Error)
	require.Len(t, rows, 1, "兜底档写入不得插出第二行")
	assert.True(t, rows[0].IsDefault)
	assert.Equal(t, "", rows[0].UserGroup, "请求体里的分组名必须被忽略,兜底档恒为空分组")
	assert.True(t, rows[0].Enabled, "兜底档没有停用的概念:它没有可回落的下一级")
	assert.Equal(t, 48, rows[0].WindowHours)
	assert.Equal(t, 20, rows[0].Threshold)
}

// seedMainUsersForImpact 往主库句柄上建一张最小 users 表并塞入可用账号。
//
// 影响面预览要跨库(计数在扩展库、分组与状态在主库),不接主库的话
// countBanPolicyImpact 会直接报错,而 409 那条断言就会变成"因为报错所以没写入",
// 与"因为确认闸所以没写入"完全无法区分。
func seedMainUsersForImpact(t *testing.T, ids ...int) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.Exec(
		"CREATE TABLE users (id INTEGER PRIMARY KEY, `group` TEXT, status INTEGER, role INTEGER)").Error)
	for _, id := range ids {
		require.NoError(t, gdb.Exec("INSERT INTO users (id, `group`, status, role) VALUES (?, 'vip', ?, ?)",
			id, common.UserStatusEnabled, common.RoleCommonUser).Error)
	}
	// model.DB 是导出的包级句柄,直接换掉并在用例结束时还原。
	prev := model.DB
	model.DB = gdb
	// 列名走 model.QyCommonGroupCol(),它由 InitCol() 填;不调它列名是空串。
	model.InitCol()
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
}

// TestUpsertBanPolicyStillRequiresConfirmWhenImpactCannotBeComputed
// 守的是"评估失败"这一条分支的方向。
//
// 主库查不动时,两种写入必须走相反的路:
//   - 收紧仍然要人确认(评估不出来不等于没有影响);
//   - 放宽直接放行 —— 事故当天最需要能执行的就是放宽,而事故当天恰恰是
//     评估最容易失败的时候。把两者绑成同一个 500,等于在最需要放宽的时刻锁死放宽。
func TestUpsertBanPolicyStillRequiresConfirmWhenImpactCannotBeComputed(t *testing.T) {
	gdb := newBanPolicyEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&BanPolicy{
		IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}).Error)
	require.NoError(t, gdb.Create(&BanPolicy{
		UserGroup: "vip", Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}).Error)

	// 必须有越线的计数行,主库那一步才会真的被执行到 —— 没有候选账号时
	// countBanPolicyImpact 根本不查主库,也就观察不到"评估失败"这条分支。
	require.NoError(t, gdb.Create(&Counter{UserId: 101, HitCount: 8, WindowStart: now}).Error)

	// 主库句柄换成一个没有 users 表的库 —— 影响面查询必然失败。
	broken, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	prev := model.DB
	model.DB = broken
	model.InitCol()
	t.Cleanup(func() { model.DB = prev })

	t.Run("收紧仍然要确认", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies",
			`{"user_group":"vip","enabled":true,"window_hours":24,"threshold":2,"action":"ban"}`)
		adminUpsertBanPolicy(c, false)
		assert.Equal(t, 409, rec.Code)
		assert.Contains(t, rec.Body.String(), "impact_error")
	})

	t.Run("放宽照常放行", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies",
			`{"user_group":"vip","enabled":true,"window_hours":24,"threshold":99,"action":"record"}`)
		adminUpsertBanPolicy(c, false)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var row BanPolicy
		require.NoError(t, gdb.Where("user_group = ?", "vip").Take(&row).Error)
		assert.Equal(t, 99, row.Threshold)
	})
}

// TestUpsertBanPolicyNeverDisposesAnyoneAndTheCopySaysSo 守的是**报文与行为一致**。
//
// 保存策略档只写策略表:全程不调 disableUserForViolation(它只有两个调用点 ——
// counter.go 的一次新命中之后、tasks.go 的 pending 行重试,而保存不创建任何 Ban 行,
// 所以也没有可重试的对象)。已越线的存量账号要等**各自下一次违规命中**。
//
// 二次确认曾经写着"该策略会立刻处置已越线的存量账号"。管理员照这句话点完确认,
// 会去封禁列表里找一批永远不会出现的行,然后得出"功能坏了"或者反过来
// "不敢按这个按钮"。一道会说谎的护栏比没有护栏更坏,所以这条用例同时钉住
// 两件事:行为(没人被处置)与报文(不许再说"立刻处置")。
func TestUpsertBanPolicyNeverDisposesAnyoneAndTheCopySaysSo(t *testing.T) {
	gdb := newBanPolicyEnv(t)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&BanPolicy{
		IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}).Error)
	// 两个已经攒到 5/7 次的账号:阈值降到 3 会让他们当场处在越线状态。
	require.NoError(t, gdb.Create(&Counter{UserId: 101, HitCount: 5, WindowStart: now}).Error)
	require.NoError(t, gdb.Create(&Counter{UserId: 102, HitCount: 7, WindowStart: now}).Error)
	seedMainUsersForImpact(t, 101, 102)

	t.Run("409 的报文不许宣称保存会立刻处置", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies",
			`{"user_group":"vip","enabled":true,"window_hours":24,"threshold":3,"action":"ban"}`)
		adminUpsertBanPolicy(c, false)
		require.Equal(t, 409, rec.Code)
		body := rec.Body.String()
		assert.NotContains(t, body, "立刻处置",
			"保存不处置任何人,报文不许这么说")
		assert.Contains(t, body, "下一次违规命中",
			"必须说清楚处置发生在什么时候,否则管理员无从判断该盯哪里")
		assert.Contains(t, body, "2", "已越线的账号数要如实给出")
	})

	t.Run("确认保存之后,一个账号都没有被处置", func(t *testing.T) {
		c, rec := banPolicyCtx(t, "PUT", "/violation/ban-policies",
			`{"user_group":"vip","enabled":true,"window_hours":24,"threshold":3,"action":"ban","confirm":true}`)
		adminUpsertBanPolicy(c, false)
		require.Equal(t, 200, rec.Code, rec.Body.String())

		var bans int64
		require.NoError(t, gdb.Model(&Ban{}).Count(&bans).Error)
		assert.Zero(t, bans, "保存不创建封禁行 —— 处置发生在下一次命中时")

		for _, id := range []int{101, 102} {
			var status int
			require.NoError(t, model.DB.Raw("SELECT status FROM users WHERE id = ?", id).Scan(&status).Error)
			assert.Equal(t, common.UserStatusEnabled, status,
				"保存策略档不得改动任何账号状态")
		}
	})
}

// TestDeleteDefaultBanPolicyLeavesAuditTrail —— 被挡下的破坏性动作也要留痕。
//
// "谁试过删兜底档"只能靠失败审计回答:成功的那次会留下一行,而被拒的那次
// 曾经什么都不留。同模块的 bind/unbind/adjust 三条路径失败都留痕,口径必须一致。
// 纯入参 4xx(id 非法)仍然不写 —— 那一类没有指向库里任何一行。
func TestDeleteDefaultBanPolicyLeavesAuditTrail(t *testing.T) {
	gdb := newBanPolicyEnv(t)
	now := common.GetTimestamp()
	def := BanPolicy{IsDefault: true, Enabled: true, WindowHours: 24, Threshold: 10,
		Action: PolicyActionBan, CreatedAt: now, UpdatedAt: now}
	require.NoError(t, gdb.Create(&def).Error)

	c, rec := banPolicyCtx(t, "DELETE", "/violation/ban-policies/1", "")
	c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(def.Id, 10)}}
	adminDeleteBanPolicy(c)
	require.Equal(t, 400, rec.Code)

	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "ban_policies.delete").Find(&rows).Error)
	require.Len(t, rows, 1, "被拒的删除必须留下恰好一条审计")
	assert.Equal(t, qymodel.ResultFail, rows[0].Result)
	assert.Equal(t, 1, rows[0].ActorUserId)
	assert.Equal(t, "qy-admin", rows[0].ActorName)
	assert.Contains(t, rows[0].Reason, "兜底策略不可删除")
	assert.Contains(t, rows[0].BeforeSnap, `"is_default":true`,
		"审计要指得出被试图删掉的是哪一行")

	// 非法 id 那一档仍然不写审计:它一条库都没碰。
	c2, rec2 := banPolicyCtx(t, "DELETE", "/violation/ban-policies/0", "")
	c2.Params = gin.Params{{Key: "id", Value: "0"}}
	adminDeleteBanPolicy(c2)
	require.Equal(t, 400, rec2.Code)
	var n int64
	require.NoError(t, gdb.Model(&qymodel.AuditLog{}).Where("action = ?", "ban_policies.delete").Count(&n).Error)
	assert.EqualValues(t, 1, n, "入参级拒绝不该产生审计噪音")
}
