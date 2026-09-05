package violation

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newCyberTestDB 造一个只含 CyberSetting 表的内存库。
func newCyberTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 内存库按连接隔离
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, gdb.AutoMigrate(&CyberSetting{}))
	return gdb
}

// TestMatchesCyberTrigger 守"哪些上游拒绝算 cyber 命中"的判据。
//
// 判据是子串、大小写不敏感,且同时看错误码与错误正文 —— 上游的 cyber_policy
// 更常落在正文里而不是我们内部分类出来的码上(与内置的 cybersecurity_refusal
// 规则同一个道理)。这条直接钉住:改判据不能悄悄放宽成"任何 4xx 都算"。
func TestMatchesCyberTrigger(t *testing.T) {
	codes := []string{"cyber_policy"}

	inBody := types.NewErrorWithStatusCode(
		errors.New(`{"error":{"code":"cyber_policy","message":"blocked"}}`),
		types.ErrorCode("bad_response_status_code"), http.StatusForbidden)
	assert.Equal(t, "cyber_policy", matchesCyberTrigger(codes, inBody), "正文里出现 cyber_policy 必须命中并回传规则")

	inCode := types.NewErrorWithStatusCode(
		errors.New("upstream refused"), types.ErrorCode("Cyber_Policy"), http.StatusForbidden)
	assert.Equal(t, "cyber_policy", matchesCyberTrigger(codes, inCode), "错误码里出现(大小写不敏感)必须命中")

	unrelated := types.NewErrorWithStatusCode(
		errors.New("rate limit exceeded"), types.ErrorCode("rate_limit"), http.StatusTooManyRequests)
	assert.Equal(t, "", matchesCyberTrigger(codes, unrelated), "无关错误不得命中")

	assert.Equal(t, "", matchesCyberTrigger(nil, inBody), "触发码为空时一律不命中")
	assert.Equal(t, "", matchesCyberTrigger(codes, nil), "无错误时不命中")
}

// TestCyberSessionHashIsolatesByUser 守拉黑的账号隔离。
//
// 同一个 session id 在两个账号下必须得到不同的哈希 —— 否则 A 用户被封的会话 id
// 会把 B 用户碰巧相同的会话一起挡掉。同账号同会话必须稳定得到同一个键。
func TestCyberSessionHashIsolatesByUser(t *testing.T) {
	a := cyberSessionHash(1, "sess-abc")
	b := cyberSessionHash(2, "sess-abc")
	assert.NotEqual(t, a, b, "不同账号的同名会话必须落在不同的键上")
	assert.Equal(t, a, cyberSessionHash(1, "sess-abc"), "同账号同会话必须稳定")
}

// TestCyberSessionRawKeyPrefersBodyThenHeader 守会话身份的取值顺序。
func TestCyberSessionRawKeyPrefersBodyThenHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("请求体 prompt_cache_key 优先", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("session_id", "from-header")
		c.Set(common.KeyRequestBody, []byte(`{"model":"gpt-5","prompt_cache_key":"from-body"}`))
		assert.Equal(t, "from-body", cyberSessionRawKey(c))
	})

	t.Run("没有请求体字段时回落会话头", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Request.Header.Set("conversation_id", "conv-9")
		c.Set(common.KeyRequestBody, []byte(`{"model":"claude"}`))
		assert.Equal(t, "conv-9", cyberSessionRawKey(c))
	})

	t.Run("两者都没有时为空", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		c.Set(common.KeyRequestBody, []byte(`{"model":"gpt-5"}`))
		assert.Equal(t, "", cyberSessionRawKey(c))
	})
}

// TestCyberLocalStoreBlocksThenExpires 守进程内兜底存储的拉黑与到期。
//
// Redis 不可用时(测试环境 RDB=nil)走的就是这条兜底路径:标记后命中,
// 过期时刻起不再命中,从未标记过的键一律不命中。
func TestCyberLocalStoreBlocksThenExpires(t *testing.T) {
	const h = "hash-under-test"
	cyberMarkLocal(h, 1000)
	assert.True(t, cyberBlockedLocal(h, 999), "过期前必须命中")
	assert.False(t, cyberBlockedLocal(h, 1000), "到期时刻起不再命中")
	assert.False(t, cyberBlockedLocal("never-marked", 999), "没标记过的键不命中")
}

// ctxWithBody 造一个带请求体的测试 gin.Context。
func ctxWithBody(body string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.KeyRequestBody, []byte(body))
	return c
}

// TestCyberSessionRoundTrip 走两个挂载点的核心逻辑:第一条 cyber_policy 请求照常
// 把上游错误回给用户、同时拉黑该会话,同会话的下一条请求在转发前判定里直接 403。
//
// 它钉住那条被点名的语义:命中过 cyber_policy 的会话**不能**继续无限重试。
// 用直接构造的 cyberRuntime(空作用域 = 全部模型分组)驱动,不经快照/DB ——
// 快照装配另由 TestBuildCyberRuntime 覆盖。
func TestCyberSessionRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useTestConfig(t, "  enabled: true\n") // 触发码走默认 ["cyber_policy"]

	rt := &cyberRuntime{ttlSeconds: 3600, scope: compileScope("", "", GroupScopeInclude),
		triggers: parseCyberTriggers("")}
	info := &relaycommon.RelayInfo{UserId: 4242, UsingGroup: "default", OriginModelName: "gpt-5", RequestId: "req-1"}
	const body = `{"model":"gpt-5","prompt_cache_key":"sess-rt-42"}`

	// 第一条:尚未拉黑,转发前判定放行。
	c1 := ctxWithBody(body)
	require.NoError(t, cyberSessionPrecheck(c1, info, rt), "第一条请求必须放行")

	// 上游以 cyber_policy 拒绝这一条 → 事后拉黑本会话。
	apiErr := types.NewErrorWithStatusCode(
		errors.New(`upstream error: {"code":"cyber_policy"}`),
		types.ErrorCode("bad_response_status_code"), http.StatusForbidden)
	maybeBlockCyberSession(c1, info, apiErr, rt)

	// 第二条:同账号、同会话 id,转发前判定必须直接 403,不再打上游。
	c2 := ctxWithBody(body)
	err := cyberSessionPrecheck(c2, info, rt)
	require.Error(t, err, "命中过 cyber_policy 的会话必须被本地拦下")

	var apiErr2 *types.NewAPIError
	require.True(t, errors.As(err, &apiErr2), "返回的必须是可被上游原样保留的 NewAPIError")
	assert.Equal(t, http.StatusForbidden, apiErr2.StatusCode)
	assert.Equal(t, types.ErrorCode(cyberBlockErrorCode), apiErr2.GetErrorCode())
	assert.True(t, types.IsSkipRetryError(apiErr2), "拉黑拦截必须跳过重试")

	// 另一条会话不受影响:同账号、不同 session id,照常放行。
	c3 := ctxWithBody(`{"model":"gpt-5","prompt_cache_key":"sess-rt-other"}`)
	assert.NoError(t, cyberSessionPrecheck(c3, info, rt), "另一条会话不该被连坐")
}

// TestCyberSessionRespectsGroupScope 守"哪个模型分组受屏蔽"这道闸。
//
// 作用域只 include vip:走 vip 的流量命中 cyber_policy 会被拉黑,而走 free 的
// 同样命中却一个字节都不写黑名单,后续照常放行。
func TestCyberSessionRespectsGroupScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useTestConfig(t, "  enabled: true\n")

	rt := &cyberRuntime{ttlSeconds: 3600, scope: compileScope("", "vip", GroupScopeInclude),
		triggers: parseCyberTriggers("")}
	apiErr := types.NewErrorWithStatusCode(
		errors.New(`{"code":"cyber_policy"}`), types.ErrorCode("x"), http.StatusForbidden)

	// free 不在作用域内:命中也不拉黑,后续放行。
	free := &relaycommon.RelayInfo{UserId: 7, UsingGroup: "free", RequestId: "r-free"}
	cFree := ctxWithBody(`{"prompt_cache_key":"sess-free"}`)
	require.NoError(t, cyberSessionPrecheck(cFree, free, rt))
	maybeBlockCyberSession(cFree, free, apiErr, rt)
	assert.NoError(t, cyberSessionPrecheck(ctxWithBody(`{"prompt_cache_key":"sess-free"}`),
		&relaycommon.RelayInfo{UserId: 7, UsingGroup: "free"}, rt),
		"作用域外的分组不该被拉黑")

	// vip 在作用域内:命中即拉黑,后续 403。
	vip := &relaycommon.RelayInfo{UserId: 7, UsingGroup: "vip", RequestId: "r-vip"}
	cVip := ctxWithBody(`{"prompt_cache_key":"sess-vip"}`)
	require.NoError(t, cyberSessionPrecheck(cVip, vip, rt))
	maybeBlockCyberSession(cVip, vip, apiErr, rt)
	assert.Error(t, cyberSessionPrecheck(ctxWithBody(`{"prompt_cache_key":"sess-vip"}`), vip, rt),
		"作用域内的分组命中后必须被拉黑")
}

// TestCyberBlockWritesAuditWithContext 守"拉黑一次会话必须落一条带上下文的审计"。
//
// 这是运营做人工复核与针对性配置防御的唯一入口:审计里要能查到谁/哪个模型/
// 哪个分组/命中了哪条触发规则/上游原话。
func TestCyberBlockWritesAuditWithContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	useTestConfig(t, "  enabled: true\n") // audit 默认开

	// 扩展库 + 审计表接到 db.Get(),否则 audit.Write 因 db 不可用直接返回、断言假绿。
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}))
	prevH := qyDBHandleForCtxTest.Swap(gdb)
	prevHealthy := qyDBHealthyForJSONTest.Swap(true)
	t.Cleanup(func() {
		qyDBHandleForCtxTest.Store(prevH)
		qyDBHealthyForJSONTest.Store(prevHealthy)
		_ = sqlDB.Close()
	})

	rt := &cyberRuntime{ttlSeconds: 600, scope: compileScope("", "", GroupScopeInclude),
		triggers: parseCyberTriggers("")}
	info := &relaycommon.RelayInfo{UserId: 77, UsingGroup: "default", OriginModelName: "gpt-5",
		TokenId: 9, RequestId: "r-audit"}

	c := ctxWithBody(`{"prompt_cache_key":"sess-audit"}`)
	c.Set("username", "alice")
	c.Set("token_name", "tk-a")
	require.NoError(t, cyberSessionPrecheck(c, info, rt)) // 先算出会话哈希塞进 ctx

	apiErr := types.NewErrorWithStatusCode(
		errors.New(`{"error":{"code":"cyber_policy","message":"blocked"}}`),
		types.ErrorCode("x"), http.StatusForbidden)
	maybeBlockCyberSession(c, info, apiErr, rt)

	var rows []qymodel.AuditLog
	require.NoError(t, gdb.Where("action = ?", "cyber_session_blocked").Find(&rows).Error)
	require.Len(t, rows, 1, "拉黑一次会话必须落且只落一条审计")
	assert.Equal(t, qymodel.ActorUser, rows[0].ActorType, "触发者是用户的这次请求")
	assert.Equal(t, 77, rows[0].ActorUserId)
	for _, want := range []string{"cyber_policy", "using_group", "gpt-5", "session_hash", "matched_trigger"} {
		assert.Contains(t, rows[0].AfterSnap, want, "审计上下文必须含 %s", want)
	}
}

// TestBuildCyberRecordFeedsBanCounter 守"计入自动封号计数"那条记录的形状:
// 绑定的类型、权重 1、非影子、稳定的 RecNo。这条记录进 persist 后会推进计数、
// 达阈值触发 maybeAutoBan —— 与规则命中完全相同的链路。
func TestBuildCyberRecordFeedsBanCounter(t *testing.T) {
	rc := recordCtx{UserId: 77, Username: "alice", TokenId: 9, TokenName: "tk-a",
		ModelName: "gpt-5", UsingGroup: "vip", RequestId: "r-1", Ip: "1.2.3.4"}
	cat := Category{Id: 42, Name: "网络攻击", PublicTitle: "安全策略"}
	apiErr := types.NewErrorWithStatusCode(errors.New(`cyber_policy`), types.ErrorCode("x"), 403)

	rec := buildCyberRecord(rc, "abcdef0123456789", cat, "cyber_policy", apiErr)

	assert.Equal(t, int64(42), rec.CategoryId, "计到绑定的违规类型上")
	assert.Equal(t, "网络攻击", rec.CategoryName)
	assert.Equal(t, 1, rec.CountWeight, "一次命中算一次")
	assert.False(t, rec.Shadow, "不是影子:确实拦了")
	assert.True(t, rec.Blocked)
	assert.Equal(t, PhaseCyberBlock, rec.Phase)
	assert.Equal(t, 77, rec.UserId)
	assert.Equal(t, "cyber_abcdef0123456789", rec.RecNo, "RecNo 用会话哈希,天然幂等")
	assert.Contains(t, rec.MatchedTerms, "cyber_policy")
}

// TestBuildCyberRuntime 守 DB 设置行 → 运行期形态的装配:关闭/缺行为 nil,
// 启用为非 nil 且 TTL 按边界回落。
func TestBuildCyberRuntime(t *testing.T) {
	gdb := newCyberTestDB(t)

	assert.Nil(t, buildCyberRuntime(gdb), "设置行还没建时按未启用处理")

	require.NoError(t, gdb.Create(&CyberSetting{Id: 1, Enabled: false, TTLSeconds: 100}).Error)
	assert.Nil(t, buildCyberRuntime(gdb), "总开关关着时不生效")

	require.NoError(t, gdb.Save(&CyberSetting{Id: 1, Enabled: true, TTLSeconds: 0,
		GroupScope: "vip", GroupScopeMode: GroupScopeInclude}).Error)
	rt := buildCyberRuntime(gdb)
	require.NotNil(t, rt)
	assert.Equal(t, cyberDefaultTTLSeconds, rt.ttlSeconds, "TTL=0 必须回落默认值")
	assert.True(t, rt.groupManaged("vip"))
	assert.False(t, rt.groupManaged("free"), "include 名单外的分组不受管")
	assert.Equal(t, []string{cyberDefaultTriggerText}, rt.triggers,
		"没配触发规则时,运行期回落默认(不是空 = 谁都不拦)")

	require.NoError(t, gdb.Save(&CyberSetting{Id: 1, Enabled: true, TTLSeconds: cyberMaxTTLSeconds + 1}).Error)
	assert.Equal(t, cyberDefaultTTLSeconds, buildCyberRuntime(gdb).ttlSeconds, "超上限的 TTL 也回落默认值")
}

// TestParseCyberTriggers 守触发过滤规则的解析:换行/逗号切、折小写去重、
// **空文本回落默认**(避免"清空 = 谁都不拦"这种静默失效)。
func TestParseCyberTriggers(t *testing.T) {
	assert.Equal(t, []string{cyberDefaultTriggerText}, parseCyberTriggers(""),
		"空文本必须回落默认")
	assert.Equal(t, []string{cyberDefaultTriggerText}, parseCyberTriggers("   \n , "),
		"只有分隔符/空白也回落默认")
	assert.Equal(t, []string{"cyber_policy", "policy_violation"},
		parseCyberTriggers("Cyber_Policy\npolicy_violation\ncyber_policy,POLICY_VIOLATION"),
		"折小写去重,保留首次出现顺序")
}

// TestValidateCyberSetting 守写入闸:方向合法性、通配符拒绝、TTL 边界。
func TestValidateCyberSetting(t *testing.T) {
	require.NoError(t, validateCyberSetting(&CyberSetting{GroupScopeMode: ""}),
		"空方向归一成 include,不报错")

	require.Error(t, validateCyberSetting(&CyberSetting{GroupScopeMode: "sideways"}),
		"非法方向必须拒绝")
	require.Error(t, validateCyberSetting(&CyberSetting{GroupScope: "vip*", GroupScopeMode: GroupScopeInclude}),
		"分组名单里的通配符必须拒绝(分组是精确查表)")
	require.Error(t, validateCyberSetting(&CyberSetting{TTLSeconds: -1}), "负数 TTL 必须拒绝")
	require.Error(t, validateCyberSetting(&CyberSetting{TTLSeconds: cyberMaxTTLSeconds + 1}), "超上限 TTL 必须拒绝")

	ok := &CyberSetting{GroupScope: " vip , svip ", GroupScopeMode: GroupScopeInclude, TTLSeconds: 600}
	require.NoError(t, validateCyberSetting(ok))
	assert.Equal(t, "vip , svip", ok.GroupScope, "两侧空白被去掉(内部归一保留原样)")
}
