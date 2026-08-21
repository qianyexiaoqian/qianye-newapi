package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// user_username_case_test.go —— 用户名的大小写口径必须与库无关。
//
// 裸 `WHERE username = ?` 在三种受支持的主库上不是同一件事:MySQL 建库默认
// utf8mb4_0900_ai_ci(大小写**不**敏感),PostgreSQL 与 SQLite 逐字节比
// (大小写敏感)。也就是说 MySQL 的排序规则一直在替业务代码兜底,而这份兜底
// 在另外两种受支持部署上根本不存在。实测(两台同二进制、同请求的对照实例):
//
//	注册 qy-fals-a 之后再注册 QY-FALS-A → MySQL 拒绝、PostgreSQL success
//	用 QY-FALS-B 登录 qy-fals-b        → MySQL 成功、PostgreSQL「用户名或密码错误」
//
// 前者是冒名面(username 是工单、日志、审计、佣金关系、提现审核里的展示身份),
// 后者是迁库当天存量用户集体登不进来。两条同根同源,必须一起钉住。
//
// 用例跑在 sqlite 上,而 sqlite 恰好与 PostgreSQL 同侧(逐字节比)—— 也就是说
// 这条判据在默认的 `go test ./...` 里就能把缺陷打红,不需要任何 DSN。

func seedCaseUser(t *testing.T, username string) *User {
	t.Helper()
	hashed, err := common.Password2Hash("case-fold-pass-1")
	require.NoError(t, err)
	user := &User{
		Username: username, Password: hashed, Role: common.RoleCommonUser,
		Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1,
		AffCode:     "aff-" + username,
		DisplayName: username + "-display",
		Email:       username + "@example.test",
	}
	require.NoError(t, DB.Create(user).Error)
	return user
}

// 注册/建号的重名判定:只差大小写的用户名必须被认成"已存在"。
func TestCheckUserExistOrDeletedFoldsUsernameCase(t *testing.T) {
	truncateTables(t)
	// 库里存的是**混合大小写**:只把入参 ToLower 是不够的,列那一侧必须一起折叠。
	seedCaseUser(t, "Qy-Case-A")

	for _, probe := range []string{"qy-case-a", "QY-CASE-A", "Qy-Case-A", "  QY-CASE-A  "} {
		exists, err := CheckUserExistOrDeleted(probe, "")
		require.NoError(t, err)
		assert.Truef(t, exists,
			"只差大小写的用户名必须判成已存在,否则 PG/SQLite 上能注册出冒名账号(probe=%q)", probe)
	}

	exists, err := CheckUserExistOrDeleted("qy-case-b", "")
	require.NoError(t, err)
	assert.False(t, exists, "真正的新用户名不得被误判成已存在")
}

// 登录:用户名与邮箱两条入口都按大小写折叠,口令仍必须逐字相等。
func TestValidateAndFillFoldsUsernameAndEmailCase(t *testing.T) {
	truncateTables(t)
	seedCaseUser(t, "Qy-Case-Login")

	for _, tc := range []struct {
		name     string
		login    string
		password string
		wantErr  error
	}{
		{"用户名逐字相等", "Qy-Case-Login", "case-fold-pass-1", nil},
		{"用户名全大写", "QY-CASE-LOGIN", "case-fold-pass-1", nil},
		{"用户名全小写", "qy-case-login", "case-fold-pass-1", nil},
		{"邮箱大写", "QY-CASE-LOGIN@EXAMPLE.TEST", "case-fold-pass-1", nil},
		{"口令不折叠", "QY-CASE-LOGIN", "CASE-FOLD-PASS-1", ErrInvalidCredentials},
		{"不存在的账号", "qy-case-nobody", "case-fold-pass-1", ErrInvalidCredentials},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := &User{Username: tc.login, Password: tc.password}
			err := user.ValidateAndFill()
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "Qy-Case-Login", user.Username, "登录成功后必须填回库里那一行")
		})
	}
}

// requireCaseSensitiveLike 把 sqlite 的 LIKE 切成大小写敏感,让它在**检索**这条
// 判据上与 PostgreSQL 同侧。
//
// 这是一处必须写清楚的坑:sqlite 与 PG 只在 `=` 上同侧(都逐字节比),在 LIKE 上
// **不**同侧 —— sqlite 的 LIKE 对 ASCII 默认大小写不敏感。于是把 SearchUsers 的
// `LOWER(col) LIKE ?` 改回裸 `col LIKE ?`(即把缺陷放回去),sqlite 上的用例照样
// 全绿:那是一条什么也没验到的假测试。实测过这次变异,SURVIVED。
//
// 打开 `PRAGMA case_sensitive_like = ON` 之后,sqlite 的 LIKE 与 PG 逐字对齐,
// 同一次变异当场变红,而且不需要任何 DSN —— 默认的 `go test ./...` 就能守住。
// 连接池已被 TestMain 钉成一条连接(SetMaxOpenConns(1)),所以 PRAGMA 作用于
// 后续全部查询;用完在 Cleanup 里关回去,不污染同包其它用例。
func requireCaseSensitiveLike(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Exec("PRAGMA case_sensitive_like = ON").Error)
	t.Cleanup(func() { DB.Exec("PRAGMA case_sensitive_like = OFF") })

	// 自检:PRAGMA 没生效的话下面所有断言都会退化成「怎么写都过」,
	// 那正是这个 helper 要消灭的东西,所以先证明它真的生效了。
	var probe int64
	require.NoError(t, DB.Raw("SELECT CASE WHEN 'A' LIKE 'a' THEN 1 ELSE 0 END").Scan(&probe).Error)
	require.EqualValues(t, 0, probe,
		"PRAGMA case_sensitive_like 未生效,这组检索用例将无法区分「折叠了」与「没折叠」")
}

// 管理端检索:PG 上返回 200 + 空列表与"这个人不存在"不可分辨,必须折叠。
func TestSearchUsersFoldsKeywordCase(t *testing.T) {
	truncateTables(t)
	requireCaseSensitiveLike(t)
	seedCaseUser(t, "Qy-Case-Search")

	for _, keyword := range []string{"qy-case-search", "QY-CASE-SEARCH", "Qy-Case-Se"} {
		users, total, err := SearchUsers(keyword, "", nil, nil, 0, 10)
		require.NoError(t, err)
		assert.Equalf(t, int64(1), total, "检索关键字的大小写不得影响结果(keyword=%q)", keyword)
		require.Len(t, users, 1)
		assert.Equal(t, "Qy-Case-Search", users[0].Username)
	}

	// display_name 与 email 两列同样要折叠。
	for _, keyword := range []string{"QY-CASE-SEARCH-DISPLAY", "QY-CASE-SEARCH@EXAMPLE.TEST"} {
		_, total, err := SearchUsers(keyword, "", nil, nil, 0, 10)
		require.NoError(t, err)
		assert.Equalf(t, int64(1), total, "display_name / email 两列同样要折叠(keyword=%q)", keyword)
	}

	_, total, err := SearchUsers("qy-case-nobody", "", nil, nil, 0, 10)
	require.NoError(t, err)
	assert.Zero(t, total, "查不到的关键字仍然必须查不到")
}

// 渠道检索:与用户检索同一条判据,原先只有 QY_TEST_PG_DSN 门控的那份守着,
// 默认的 `go test ./...` 里是裸奔的。打开 case_sensitive_like 之后 sqlite 就能
// 替 PG 站岗,不需要任何 DSN。
// 密钥列刻意不折叠(那是凭据比对),所以这里顺带钉住:密钥必须逐字相等才命中。
func TestSearchChannelsFoldsKeywordCase(t *testing.T) {
	truncateTables(t)
	requireCaseSensitiveLike(t)

	channel := &Channel{
		Name:    "Qy-Case-Channel",
		Key:     "Qy-Case-Channel-Key",
		BaseURL: strPtr("https://Qy-Case-Host.example.test"),
		Models:  "QY-Case-Model,other-model",
		Group:   "default",
	}
	require.NoError(t, DB.Create(channel).Error)

	for _, keyword := range []string{"Qy-Case-Channel", "qy-case-channel", "QY-CASE-CHANNEL"} {
		channels, err := SearchChannels(keyword, "", "", true)
		require.NoError(t, err)
		require.Lenf(t, channels, 1, "渠道名检索的大小写不得影响结果(keyword=%q)", keyword)
		assert.Equal(t, channel.Id, channels[0].Id)
	}

	// base_url 与 models 两列同样折叠。
	for _, tc := range []struct{ keyword, model string }{
		{"QY-CASE-HOST", ""},
		{"qy-case-channel", "qy-case-model"},
		{"qy-case-channel", "QY-CASE-MODEL"},
	} {
		channels, err := SearchChannels(tc.keyword, "", tc.model, true)
		require.NoError(t, err)
		assert.Lenf(t, channels, 1, "keyword=%q model=%q", tc.keyword, tc.model)
	}

	// 密钥列不折叠:凭据比对必须逐字相等。
	byKey, err := SearchChannels("Qy-Case-Channel-Key", "", "", true)
	require.NoError(t, err)
	assert.Len(t, byKey, 1, "逐字相等的密钥必须命中")

	byWrongCaseKey, err := SearchChannels("QY-CASE-CHANNEL-KEY", "", "", true)
	require.NoError(t, err)
	assert.Empty(t, byWrongCaseKey,
		"密钥是凭据比对,不得做大小写折叠;这里命中说明有人把 commonKeyCol 也套上了 LOWER")

	none, err := SearchChannels("qy-case-nobody", "", "", true)
	require.NoError(t, err)
	assert.Empty(t, none, "查不到的关键字仍然必须查不到")
}

func strPtr(s string) *string { return &s }

// 日志页的按用户名 / 按模型名过滤:等值与 LIKE 两条分支都要折叠。
func TestGetAllLogsTextFilterFoldsCase(t *testing.T) {
	truncateTables(t)
	requireCaseSensitiveLike(t)
	require.NoError(t, LOG_DB.Create(&Log{
		Id: 880001, UserId: 1, CreatedAt: common.GetTimestamp(), Type: LogTypeConsume,
		Username: "Qy-Case-Log", ModelName: "QY-Case-Model",
	}).Error)

	for _, tc := range []struct {
		name      string
		username  string
		modelName string
	}{
		{"逐字相等", "Qy-Case-Log", "QY-Case-Model"},
		{"用户名大写", "QY-CASE-LOG", "QY-Case-Model"},
		{"模型名小写", "Qy-Case-Log", "qy-case-model"},
		{"两列都换大小写", "qy-case-log", "qy-CASE-model"},
		{"LIKE 分支也折叠", "QY-CASE-%", "qy-case-%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs, total, err := GetAllLogs(LogTypeConsume, 0, 0, tc.modelName, tc.username,
				"", 0, 10, 0, "", "", "", false)
			require.NoError(t, err)
			assert.Equal(t, int64(1), total)
			require.Len(t, logs, 1)
			assert.Equal(t, 880001, logs[0].Id)
		})
	}

	_, total, err := GetAllLogs(LogTypeConsume, 0, 0, "", "qy-case-nobody",
		"", 0, 10, 0, "", "", "", false)
	require.NoError(t, err)
	assert.Zero(t, total, "对不上的用户名仍然必须查不到")
}
