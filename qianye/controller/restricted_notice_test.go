package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 被测函数一律走 db.Get() 自取句柄(与生产代码一致),所以测试必须真的把测试库
// 接上去。链接到 db 包私有句柄的 qyDBHandle / qyDBHealthy 由同包的
// pagination_handler_test.go 声明,本文件直接复用 —— 一个包里 go:linkname
// 同一个符号两次会直接编译失败。

// restrictedContextKey 是 middleware 打给受限账号的上下文键。
//
// 它在 middleware 包里是私有常量,这里只能按字面量重建 —— 因此每一处用到它的
// 地方都紧跟一句 require(middleware.IsRestrictedUser(c)):键名一旦在 middleware
// 那边改掉,测试会当场变红并指出"上下文键对不上了",而不是悄悄退化成
// "所有用例都在测正常账号那条分支"。
const restrictedContextKey = "user_restricted"

// newNoticeEnv 接上一个只承载 qy_settings 的内存库,并加载一份可用配置。
func newNoticeEnv(t *testing.T) *gorm.DB {
	t.Helper()

	yaml := "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)/qy\"\n"
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	// 扩展库固定是 MySQL,sqlite 不认 FOR UPDATE。本仓既有做法。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(&qymodel.Setting{}, &qymodel.AuditLog{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

// seedNotice 直接往 qy_settings 写三行,绕过写接口。
//
// 绕过是刻意的:读取侧必须能独立于写入侧被验证 —— 库里的行可以由 DBA 手工改、
// 由上一版代码写下、由一次半成功的写入留下,而这些形状写接口造不出来。
func seedNotice(t *testing.T, gdb *gorm.DB, enabled, title, body string) {
	t.Helper()
	now := common.GetTimestamp()
	rows := []qymodel.Setting{
		{Scope: restrictedNoticeScope, K: restrictedNoticeKeyEnabled, V: enabled, OperatorId: 7, UpdatedAt: now},
		{Scope: restrictedNoticeScope, K: restrictedNoticeKeyTitle, V: title, OperatorId: 7, UpdatedAt: now},
		{Scope: restrictedNoticeScope, K: restrictedNoticeKeyBody, V: body, OperatorId: 7, UpdatedAt: now},
	}
	require.NoError(t, gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&rows).Error)
}

// callUserNotice 以「受限 / 正常」两种身份调一次用户端接口,返回状态码与响应体。
func callUserNotice(t *testing.T, restricted bool) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/qy/restricted-notice", nil)
	c.Set("id", 42)
	if restricted {
		c.Set(restrictedContextKey, true)
		require.True(t, middleware.IsRestrictedUser(c),
			"上下文键与 middleware.IsRestrictedUser 的判据已经对不上了")
	} else {
		require.False(t, middleware.IsRestrictedUser(c))
	}
	UserRestrictedNotice(c)
	return rec.Code, rec.Body.String()
}

// 开关三态 + 两种"配坏了"的降级,全部按**用户端接口的实际响应**断言。
//
// 三态的展示后果各不相同,而它们在库里的差别只是几行 KV:
//
//	没有任何行     → 从来没配过。回落固定文案。
//	enabled=false → 配过又关掉了。回落固定文案(而不是显示上一版内容)。
//	enabled=true  → 显示这一段。
//
// 后两条降级("开着但正文被清空"、"开关值是个没人认识的字符串")是这条链路上
// 真正会出事的地方:它们都能让受限用户的首屏出现一块**空白卡片** —— 一个刚被
// 封号的人打开控制台,看到一块什么都没写的区域,然后去发工单问"我这是怎么了",
// 而这段公告本来就是要替我们回答那个问题的。
func TestRestrictedNoticeStates(t *testing.T) {
	cases := []struct {
		name string
		// seed 为 nil 表示库里一行都没有。
		seed        func(t *testing.T, gdb *gorm.DB)
		wantEnabled bool
		wantBody    string
	}{
		{
			name:        "从未配置",
			seed:        nil,
			wantEnabled: false,
		},
		{
			name: "配过但关闭",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedNotice(t, gdb, "false", "申诉指引", "请联系 admin@example.com")
			},
			wantEnabled: false,
		},
		{
			name: "已启用",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedNotice(t, gdb, "true", "申诉指引", "请联系 admin@example.com")
			},
			wantEnabled: true,
			wantBody:    "请联系 admin@example.com",
		},
		{
			name: "开着但正文被手工清空:回落固定文案,不能是空白公告",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedNotice(t, gdb, "true", "申诉指引", "   \n\t ")
			},
			wantEnabled: false,
		},
		{
			name: "开着但标题被手工清空",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedNotice(t, gdb, "true", "", "请联系 admin@example.com")
			},
			wantEnabled: false,
		},
		{
			name: "开关值不是 true 的任何取值都按关闭处理",
			seed: func(t *testing.T, gdb *gorm.DB) {
				seedNotice(t, gdb, "1", "申诉指引", "请联系 admin@example.com")
			},
			wantEnabled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb := newNoticeEnv(t)
			if tc.seed != nil {
				tc.seed(t, gdb)
			}

			code, body := callUserNotice(t, true)
			assert.Equal(t, http.StatusOK, code,
				"这个端点永远 200:任何非 200 都会在受限落地页上变成一块红色报错")

			if tc.wantEnabled {
				assert.Contains(t, body, `"enabled":true`)
				assert.Contains(t, body, tc.wantBody)
				return
			}
			assert.Contains(t, body, `"enabled":false`)
			assert.Contains(t, body, `"title":""`)
			assert.Contains(t, body, `"body":""`)
			assert.NotContains(t, body, "admin@example.com",
				"关闭状态下正文一个字都不能下发")
		})
	}
}

// 正常账号读不到公告内容 —— 这条是"会不会漏给正常用户"的钉子。
//
// 这段文案的形状是「你的账号已被限制,申诉请…」。误发给一个没有任何问题的用户,
// 后果是他以为自己被封了,然后来发工单 —— 与这个功能的目的正好相反,而且是
// 全站规模的。前端在正常账号身上根本不渲染受限横幅,所以泄漏只可能从接口发生;
// 判据因此做在服务端,前端就算写错也漏不出去。
//
// 变异验证:把 UserRestrictedNotice 里的 `if !middleware.IsRestrictedUser(c)`
// 一段删掉,本用例立刻变红(正常账号会拿到 enabled:true 与正文)。
func TestRestrictedNoticeNotLeakedToNormalUser(t *testing.T) {
	gdb := newNoticeEnv(t)
	seedNotice(t, gdb, "true", "申诉指引", "请联系 admin@example.com")

	// 先证明这份数据对受限账号确实是可见的,否则下面那条断言可以靠
	// "公告根本没配上"而空绿。
	code, restrictedBody := callUserNotice(t, true)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, restrictedBody, "admin@example.com")

	code, normalBody := callUserNotice(t, false)
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, normalBody, `"enabled":false`)
	assert.NotContains(t, normalBody, "申诉指引")
	assert.NotContains(t, normalBody, "admin@example.com")
}

// 受限账号必须能**到达**这条路由。
//
// 白名单漏一条的表现是 403,而 403 在展示上与"没配置"不可区分:落地页照样
// 回落固定文案,运营在后台配好的申诉渠道一个字都不显示,而且没有任何一处报错。
// 路由树那一侧由 router/restricted_user_routes_test.go 逐条核对,这里钉的是
// 判据本身 —— 路径写错一个字母(:no/:id 那类)在那边同样会红,但在这里更早。
func TestRestrictedNoticeRouteIsWhitelisted(t *testing.T) {
	assert.True(t, middleware.RestrictedUserRouteAllowed(http.MethodGet, "/api/qy/restricted-notice"),
		"受限账号到不了公告接口,那块公告位会永远空着")
	// 写接口绝不能进白名单:它挂在 /api/qy/admin 下,受限账号连管理端都进不去。
	assert.False(t, middleware.RestrictedUserRouteAllowed(http.MethodPut, "/api/qy/admin/restricted-notice"))
}

// 写入侧的校验闸。表驱动,输入与期望逐条写死。
func TestValidateRestrictedNotice(t *testing.T) {
	cases := []struct {
		name    string
		in      restrictedNotice
		wantErr bool
	}{
		{"关闭且全空:允许(等于清空配置)", restrictedNotice{}, false},
		{"关闭但留着草稿:允许", restrictedNotice{Title: "草稿", Body: "回头再开"}, false},
		{"启用且内容完整", restrictedNotice{Enabled: true, Title: "申诉指引", Body: "联系我们"}, false},
		{"启用但标题为空", restrictedNotice{Enabled: true, Body: "联系我们"}, true},
		{"启用但正文为空", restrictedNotice{Enabled: true, Title: "申诉指引"}, true},
		{
			"标题正好到上限",
			restrictedNotice{Enabled: true, Title: strings.Repeat("标", RestrictedNoticeTitleMaxRunes), Body: "x"},
			false,
		},
		{
			"标题超一个汉字",
			restrictedNotice{Enabled: true, Title: strings.Repeat("标", RestrictedNoticeTitleMaxRunes+1), Body: "x"},
			true,
		},
		{
			"正文正好到上限",
			restrictedNotice{Enabled: true, Title: "t", Body: strings.Repeat("文", RestrictedNoticeBodyMaxRunes)},
			false,
		},
		{
			"正文超一个汉字",
			restrictedNotice{Enabled: true, Title: "t", Body: strings.Repeat("文", RestrictedNoticeBodyMaxRunes+1)},
			true,
		},
		{
			// 长度按 rune 计,不按字节:全中文的合法正文不能被判超长,
			// 否则运营写的每一段中文都会莫名其妙被顶回来。
			"三倍字节数的中文正文仍在上限内",
			restrictedNotice{Enabled: true, Title: "t", Body: strings.Repeat("请联系管理员", RestrictedNoticeBodyMaxRunes/6)},
			false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := validateRestrictedNotice(tc.in)
			if tc.wantErr {
				assert.NotEmpty(t, msg, "必须带可读理由,否则运营不知道该改什么")
				return
			}
			assert.Empty(t, msg)
		})
	}
}

// 超长内容必须在写库之前被拒,并且**一个字节都不落库**。
//
// 这一条守的是本仓踩过的那个坑:qy_settings.v 是 TEXT,MySQL 非严格模式下超长
// 是**静默截断**。截断之后接口照常 200、界面照常显示"已保存",而线上那段申诉
// 指引从中间没了 —— 用户会照着半句话去做。
func TestRestrictedNoticeRejectsOversizeBeforeWriting(t *testing.T) {
	gdb := newNoticeEnv(t)
	seedNotice(t, gdb, "true", "原标题", "原正文")

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	huge := `{"enabled":true,"title":"新标题","body":"` + strings.Repeat("超", RestrictedNoticeBodyMaxRunes+1) + `"}`
	c.Request = httptest.NewRequest(http.MethodPut, "/api/qy/admin/restricted-notice", strings.NewReader(huge))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 1)

	AdminPutRestrictedNotice(c)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var row qymodel.Setting
	require.NoError(t, gdb.Where("scope = ? AND k = ?", restrictedNoticeScope, restrictedNoticeKeyBody).
		Take(&row).Error)
	assert.Equal(t, "原正文", row.V, "被拒的写入不能留下任何痕迹,更不能留下截断后的半段")
}

// 写入 → 回读的闭环:管理端存进去的东西,受限账号必须原样读得到。
//
// 正文里刻意带 Markdown 与一段脚本标签:后端**一个字符都不改写**(与工单正文
// 同一口径),净化只发生在前端渲染那一步。这里断言的是"没被后端悄悄转义" ——
// 如果哪天有人在后端加一层 HTML 转义,前端那一层白名单净化就会拿到一份已经
// 被改过形状的输入,两层各自漂移,而"两处都以为对方会兜底"正是 XSS 最常见的来源。
func TestRestrictedNoticeRoundTrip(t *testing.T) {
	gdb := newNoticeEnv(t)

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	payload := `{"enabled":true,"title":"  申诉指引  ","body":"  **联系我们**\n<script>alert(1)</script>  "}`
	c.Request = httptest.NewRequest(http.MethodPut, "/api/qy/admin/restricted-notice", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 9)

	AdminPutRestrictedNotice(c)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	code, body := callUserNotice(t, true)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"enabled":true`)
	assert.Contains(t, body, `"title":"申诉指引"`, "首尾空白要被裁掉")
	assert.Contains(t, body, `**联系我们**`, "Markdown 源码必须原样保留")

	// 落库的那一份必须与运营输入逐字节相同。断言打在库行上而不是 JSON 上:
	// Go 的 JSON 编码器会把 `<` 写成 `<`,那是传输层转义,客户端解析回来
	// 仍然是 `<script>` —— 拿它当"后端做了消毒"会得出一个反过来的结论。
	var row qymodel.Setting
	require.NoError(t, gdb.Where("scope = ? AND k = ?", restrictedNoticeScope, restrictedNoticeKeyBody).
		Take(&row).Error)
	assert.Equal(t, "**联系我们**\n<script>alert(1)</script>", row.V,
		"后端不做任何 HTML 转义/过滤:净化只有前端 DOMPurify 那一处(与工单同档)")
}

// 长度上限必须能安全落进 qy_settings.v(MySQL TEXT = 65535 **字节**)。
//
// 上限是按 rune 定的,而列宽是按字节的 —— 两个口径之间必须留出最坏情况的余量,
// 否则一段全是 emoji 的正文会在通过校验之后被数据库静默截断。
func TestRestrictedNoticeLimitsFitTextColumn(t *testing.T) {
	const mysqlTextBytes = 65535
	const worstCaseBytesPerRune = 4 // utf8mb4 的码点上界
	worst := (RestrictedNoticeTitleMaxRunes + RestrictedNoticeBodyMaxRunes) * worstCaseBytesPerRune
	assert.Less(t, worst, mysqlTextBytes,
		"标题 + 正文的最坏字节数已经逼近 TEXT 列宽,超出部分在非严格模式下会被静默截断")
}
