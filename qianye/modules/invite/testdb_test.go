package invite

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	_ "unsafe" // //go:linkname 需要

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 本文件是 invite 包的数据库测试脚手架。
//
// 邀请关系的几条契约(绑定的 CAS、快照懒建、拉黑名单缓存的代次)全都**不在算术里,
// 而在 WHERE 条件与 ON CONFLICT 语义里**。只测纯函数的结果是:把 CAS 条件删掉、
// 把 DoNothing 换成 DO UPDATE,测试照样全绿。要真正捕捉这类缺陷,必须让数据库把
// 每一条语句真跑一遍。扩展库受支持的部署方言是 MySQL 与 PostgreSQL,这里用 sqlite
// 当测试方言,断言一律只依赖跨库通用语义。

// qyDBHandle 指向 qianye/db 包里的连接句柄。
//
// db.Get() 读的是包内未导出的 atomic.Pointer,而本包全部函数都通过 db.Get() 自取
// 句柄、不接受注入。用 //go:linkname 把那个句柄借出来,是在**不改动任何生产代码**
// 的前提下让这些函数跑在测试库上的唯一办法。
//
//go:linkname qyDBHandle github.com/QuantumNous/new-api/qianye/db.handle
var qyDBHandle atomic.Pointer[gorm.DB]

// qyConfig 同理指向 qianye/config 的当前配置快照。
//
//go:linkname qyConfig github.com/QuantumNous/new-api/qianye/config.current
var qyConfig atomic.Pointer[config.Config]

// qyDBHealthy 是熔断的健康标志。guard.RequireAPI 要求它为 true,
// 而它只有真的 db.Init 过才会被置上。
//
//go:linkname qyDBHealthy github.com/QuantumNous/new-api/qianye/db.healthy
var qyDBHealthy atomic.Bool

// extTables 是本包逻辑会碰到的全部扩展库表。
//
// qy_settings 必须一起建:refSalt 每次都会查它;qy_audit_logs 承接管理端写动作的审计。
func extTables() []any {
	return []any{
		&InviteRelation{}, &CacheInvalidation{},
		&qymodel.Setting{}, &qymodel.KV{}, &qymodel.AuditLog{},
	}
}

// newTestDB 建一个承载本包全部表的测试库,并把它接到 db.Get()。
//
// 用 t.TempDir() 下的文件库而不是 ":memory:":refSalt / blockedInvitees 走 db.Get()
// 拿到的是**另一条**连接,":memory:" 按连接隔离会各看到一个空库。WAL 让事务
// 持写锁期间另一条连接仍能读。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "qy_ext.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	gdb, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)

	// sqlite 不认 FOR UPDATE 语法,把 FOR 子句渲染成空。测试是单协程的,
	// 行锁在这里本来也没有语义。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}

	require.NoError(t, gdb.AutoMigrate(extTables()...))

	prev := qyDBHandle.Swap(gdb)
	resetInviteCaches()
	t.Cleanup(func() {
		qyDBHandle.Store(prev)
		resetInviteCaches()
		_ = sqlDB.Close()
	})
	return gdb
}

// resetInviteCaches 清掉本包所有跨测试残留的进程内缓存。
//
// blockedInvitees 缓存 60 秒、refSalt 与邀请关系一旦解析就常驻。不清的话上一个
// 测试的库内容会渗进下一个测试,而且渗进来的往往正好是让断言变成永真的那一份。
func resetInviteCaches() {
	invalidateBlocked()
	invalidateInviter(0)
	saltOnce.Lock()
	saltCache = ""
	saltOnce.Unlock()
}

// inviteConfig 是一份开着邀请功能的最小配置。
func inviteConfig(dayOffsetMinutes int) *config.Config {
	return &config.Config{
		Enabled: true,
		Invite:  config.Invite{Enabled: true, DayOffsetMinutes: dayOffsetMinutes, InviterCacheSecs: 300},
	}
}

// useConfig 临时替换扩展的全局配置快照。
func useConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	prev := qyConfig.Swap(cfg)
	t.Cleanup(func() { qyConfig.Store(prev) })
}

// useAdminAPI 把扩展置成"库健康",让 guard.RequireAPI 放行。
func useAdminAPI(t *testing.T) {
	t.Helper()
	prev := qyDBHealthy.Swap(true)
	t.Cleanup(func() { qyDBHealthy.Store(prev) })
}

// useMainDB 临时替换主库句柄,只建被测到的表。
func useMainDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(models...))

	prev := model.DB
	model.DB = gdb
	t.Cleanup(func() {
		model.DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// useLogDB 临时替换日志库句柄。
//
// 与 useMainDB 分开:生产里 LOG_DB 可以是与主库完全不同的一个库(甚至 ClickHouse),
// 把它们塞进同一个 sqlite 会让"我查错了库"这类错误在测试里看不见。
func useLogDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&model.Log{}))

	prev := model.LOG_DB
	model.LOG_DB = gdb
	t.Cleanup(func() {
		model.LOG_DB = prev
		_ = sqlDB.Close()
	})
	return gdb
}

// useRewardTotals 把星屑那一侧的 (邀请人, 下线) 汇总换成一张固定表:本包不能
// import stardust,用例只断言"这个数原样流到了列表与审计上"。
func useRewardTotals(t *testing.T, totals map[[2]int]int64) {
	t.Helper()
	prev := PairRewardTotals
	PairRewardTotals = func(context.Context, [][2]int) map[[2]int]int64 { return totals }
	t.Cleanup(func() { PairRewardTotals = prev })
}

// useInviteAccruals 把星屑那一侧的下线消费返汇总换成固定表(按下线 / 按天各一张)。
func useInviteAccruals(t *testing.T, byInvitee map[int]DayAccrual, byDay map[string]DayAccrual) {
	t.Helper()
	prevInvitee, prevDay := InviteAccrualByInvitee, InviteAccrualByDay
	InviteAccrualByInvitee = func(context.Context, string, string, int) (map[int]DayAccrual, error) {
		return byInvitee, nil
	}
	InviteAccrualByDay = func(context.Context, int, string, string) (map[string]DayAccrual, error) {
		return byDay, nil
	}
	t.Cleanup(func() {
		InviteAccrualByInvitee, InviteAccrualByDay = prevInvitee, prevDay
	})
}

// callAdminHandler 以 id=7 / role=10 的管理员身份跑一条管理端处理器。
//
// middleware.AdminAuth() 在生产里必然同时写入 id / username / role;role 不能省:
// 改关系接口的越级判据(guard.ManageableTarget)读的就是它。
func callAdminHandler(t *testing.T, method, target, body string, h gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 7)
	c.Set("username", "admin7")
	c.Set("role", common.RoleAdminUser)
	h(c)
	return rec
}

// newDailyCtx 造一个只带查询串的 gin 上下文,给纯解析函数用。
func newDailyCtx(t *testing.T, rawQuery string, userId int) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/x?"+rawQuery, nil)
	c.Set("id", userId)
	c.Set("username", "u"+itoa(userId))
	return c
}

// seedLog 插入一条日志。
func seedLog(t *testing.T, gdb *gorm.DB, userId int, at int64, quota int, logType int) {
	t.Helper()
	require.NoError(t, gdb.Create(&model.Log{
		UserId: userId, CreatedAt: at, Type: logType, Quota: quota, ModelName: "qy-test",
	}).Error)
}

// dayTs 返回 yyyymmdd 那一天起点之后 offset 秒的时刻。
func dayTs(t *testing.T, day string, offset int64) int64 {
	t.Helper()
	start, ok := dayKeyStart(day)
	require.True(t, ok, "日键不合法: %s", day)
	return start + offset
}

// seedUser 往主库插一个账号。inviterId 为 0 表示没有上线。
//
// aff_code 必须逐个不同:主库那一列带 uniqueIndex,留空会让第二个账号撞唯一约束。
func seedUser(t *testing.T, mainDB *gorm.DB, id int, name string, inviterId int, createdAt int64) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.User{
		Id: id, Username: name, InviterId: inviterId, CreatedAt: createdAt,
		AffCode: "aff" + strconv.Itoa(id),
	}).Error)
}

// seedGateUser 往主库插一个指定角色的账号。
func seedGateUser(t *testing.T, mainDB *gorm.DB, id int, role int) {
	t.Helper()
	require.NoError(t, mainDB.Create(&model.User{
		Id: id, Username: "gate" + strconv.Itoa(id), Role: role,
		AffCode: "gateaff" + strconv.Itoa(id),
	}).Error)
}

// seedBoundPair 造一对已绑定的关系:主库权威字段 + 扩展库快照(未拉黑)。
func seedBoundPair(t *testing.T, mainDB *gorm.DB, gdb *gorm.DB, inviterId, inviterRole, inviteeId int) {
	t.Helper()
	seedGateUser(t, mainDB, inviterId, inviterRole)
	require.NoError(t, mainDB.Create(&model.User{
		Id: inviteeId, Username: "down" + strconv.Itoa(inviteeId), InviterId: inviterId,
		AffCode: "downaff" + strconv.Itoa(inviteeId),
	}).Error)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&InviteRelation{
		InviteeId: inviteeId, InviterId: inviterId, InviteeRef: "ref" + strconv.Itoa(inviteeId),
		Blocked: true, BoundAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func relationAuditLogs(t *testing.T, gdb *gorm.DB, action string) []qymodel.AuditLog {
	t.Helper()
	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", action).Order("id asc").Find(&rows).Error)
	return rows
}

func inviterIdOf(t *testing.T, mainDB *gorm.DB, id int) int {
	t.Helper()
	var u model.User
	require.NoError(t, mainDB.Where("id = ?", id).Take(&u).Error)
	return u.InviterId
}

func relationRowOf(t *testing.T, gdb *gorm.DB, inviteeId int) *InviteRelation {
	t.Helper()
	var rows []InviteRelation
	require.NoError(t, gdb.Where("invitee_id = ?", inviteeId).Find(&rows).Error)
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

func bindBody(inviteeId, inviterId int, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) +
		`,"inviter_id":` + strconv.Itoa(inviterId) +
		`,"reason":"` + reason + `"}`
}

func rebindBody(inviteeId, inviterId int, reason string) string {
	return bindBody(inviteeId, inviterId, reason)
}

func unbindBody(inviteeId int, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) + `,"reason":"` + reason + `"}`
}

func blockBody(inviteeId int, blocked bool, reason string) string {
	return `{"invitee_id":` + strconv.Itoa(inviteeId) +
		`,"blocked":` + strconv.FormatBool(blocked) +
		`,"reason":"` + reason + `"}`
}

// listRelations 调列表接口并解出 items/total。
func listRelations(t *testing.T, query string) ([]relationView, int64) {
	t.Helper()
	rec := callAdminHandler(t, http.MethodGet,
		"/api/qy/admin/invite/relations?"+query, "", adminListRelations)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Data struct {
			Items []relationView `json:"items"`
			Total int64          `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.Data.Items, resp.Data.Total
}

// blockedRelationOf 回读一条关系此刻的 blocked 标记。关系行不存在时报错:
// 这批用例里"没有快照行"永远意味着前置步骤没做成,而不是一个待断言的取值。
func blockedRelationOf(t *testing.T, gdb *gorm.DB, inviteeId int) bool {
	t.Helper()
	rel := relationRowOf(t, gdb, inviteeId)
	require.NotNil(t, rel, "下线 %d 的关系快照行不存在", inviteeId)
	return rel.Blocked
}

func deniedAuditsOf(t *testing.T, gdb *gorm.DB, action string) []qymodel.AuditLog {
	t.Helper()
	return relationAuditLogs(t, gdb, action)
}
