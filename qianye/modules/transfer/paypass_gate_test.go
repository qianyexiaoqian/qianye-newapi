package transfer

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// paypass_gate_test.go —— 验密闸门**真的装在动钱路径上**。
//
// # 这条测试补的是什么缺口
//
// paypass 包自己的用例把闸门本身测得很扎实(密码对/错/未设置/锁定/并发计数、
// 以及一条禁止 `if isContact { skip }` 形状的 AST 断言)。但那些用例全都直接调
// paypass.Require —— 它们证明的是「门是好的」,证明不了「门装在门框上」。
//
// 实际发生过的正是后者:paypass 模块整套写完、transfer/settings.go 里留了一句
// 「划转的执行入口要调 paypass.Require」的注释,而 handleCreate 里**一行调用都没有**。
// 门造好了搁在旁边,任何人不带密码都能把钱转走,而全仓测试全绿。
// 这就是本项目累计出现十几次的头号缺陷形状:写对了,但没接上。
//
// 因此本条测试刻意**从 handleCreate 这个 HTTP 入口打进去**,而不是调 Require:
// 只有走真实入口,「有没有接上」才是被测行为的一部分。
//
// # 为什么断言"一分钱没动"而不只是"返回了 403"
//
// 「拒绝了」与「拒绝了**且没动钱**」是两回事。验密如果被放在 create() 之后,
// 响应码照样是 403,而额度已经占掉、单据已经落库。上一轮划转分组限制那条
// 回归就是这么抓出来的(判定挪到两条 UPDATE 之后,assert.Same 照样通过,
// 只有"额度一分未动"那几条变红)。所以这里必须回读订单表。

func newPayPassGateEnv(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	// 订单表是"钱有没有动"的观测面;支付密码表是闸门的判定依据。
	require.NoError(t, gdb.AutoMigrate(&Order{}, &paypass.PayPassword{}))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled:  true,
		Transfer: config.Transfer{Enabled: true},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})
	return gdb
}

// seedPayPassword 直接落一行支付密码。
//
// 不复用 paypass 的测试助手:那是另一个包的 _test 文件,跨包引不到。
// 这里只需要"这个用户设过密码"这个事实,哈希本身的正确性由 paypass 自己的用例负责。
func seedPayPassword(t *testing.T, gdb *gorm.DB, userId int, bcryptHash string) {
	t.Helper()
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&paypass.PayPassword{
		UserId: userId, Algo: "bcrypt", Hash: bcryptHash,
		SetAt: now, ChangedAt: now, UpdatedAt: now,
	}).Error)
}

func postCreate(t *testing.T, userId int, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/transfer", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", userId)
	handleCreate(c)
	return rec
}

// assertGateStopped 断言闸门拦下之后 handler **立刻返回了**。
//
// # 为什么需要这一条
//
// 第一版这条测试只断言了「状态码非 200」「body 含 qy_pay_pwd」「订单表为空」,
// 而回滚验证当场抓到它是**假回归**:把 `if !paypass.Require(...) { return }`
// 改成裸调用 `paypass.Require(...)`(接了闸门但把返回值丢掉),三条断言全都照样绿。
//
// 原因是 Require 内部已经写好错误响应,状态码与 body 前缀因此不变;而订单表为空
// 只是因为这个测试环境里没有主库,create() 走两步就自己失败了 —— 那条断言在这里
// 是**空的**,它测不到"钱有没有动",只测到"这个环境里 create 跑不通"。
//
// 真正可观测的差异是响应体:handler 没 return 的话 create() 会继续执行,
// 失败后再写一次响应,于是 body 里出现**第二段 JSON**:
//
//	{"code":"qy_pay_pwd_not_set",...}{"code":"qy_internal_error",...}
//
// 所以判据是「body 恰好是一个 JSON 对象」。多出来的那一段就是 create() 被执行过的铁证。
func assertGateStopped(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := rec.Body.String()
	assert.NotEqual(t, http.StatusOK, rec.Code,
		"没通过验密的划转必须被拒 —— 闸门要么没装,要么返回值被丢掉了")
	assert.Contains(t, body, "qy_pay_pwd",
		"拒绝的理由必须来自支付密码闸门,而不是碰巧被别的校验挡下")
	assert.Equal(t, 1, strings.Count(body, `"success"`),
		"闸门拦下后 handler 必须立刻 return —— 响应体里多出第二段 JSON,"+
			"说明 create() 仍然被执行了(裸调用 paypass.Require 而没判返回值)")
}

// TestCreateIsGatedByPayPassword 钉死:动钱入口必须过验密闸门,且被拦下时一分钱没动。
//
// 三个用例覆盖三种"闸门没装"的表现形态:
//   - 完全不带密码 → 若没接线,会一路走到 create() 去动钱
//   - 带错密码     → 若接线时把返回值丢掉(`_ = paypass.Require(...)`),同样会放行
//   - 用户没设过密码 → 首次使用强制设置这一条也走同一个闸门
func TestCreateIsGatedByPayPassword(t *testing.T) {
	// bcrypt("correct-horse") 的固定散列。用常量而不是现算:
	// 现算要引 paypass 的未导出 hashPassword,跨包拿不到。
	const hashOfCorrectHorse = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy" // bcrypt("secret")
	const userId = 9101

	t.Run("不带支付密码必须被拒且一分钱没动", func(t *testing.T) {
		gdb := newPayPassGateEnv(t)
		seedPayPassword(t, gdb, userId, hashOfCorrectHorse)

		rec := postCreate(t, userId, `{"to_user_id":9102,"amount":1000,"confirm":true,"client_request_id":"cr-1"}`)

		assertGateStopped(t, rec)

		var orders int64
		require.NoError(t, gdb.Model(&Order{}).Count(&orders).Error)
		assert.Zero(t, orders,
			"被验密拦下时不得留下任何单据 —— 「拒绝了」和「拒绝了且没动钱」是两回事")
	})

	t.Run("带错密码同样被拒", func(t *testing.T) {
		gdb := newPayPassGateEnv(t)
		seedPayPassword(t, gdb, userId, hashOfCorrectHorse)

		rec := postCreate(t, userId,
			`{"to_user_id":9102,"amount":1000,"confirm":true,"client_request_id":"cr-2","pay_password":"wrong-one"}`)

		assertGateStopped(t, rec)

		var orders int64
		require.NoError(t, gdb.Model(&Order{}).Count(&orders).Error)
		assert.Zero(t, orders)
	})

	t.Run("从未设置支付密码的用户也必须过闸门", func(t *testing.T) {
		gdb := newPayPassGateEnv(t)
		// 刻意不 seed:首次使用强制设置(裁决 1)走的是同一个闸门。
		rec := postCreate(t, userId+1,
			`{"to_user_id":9102,"amount":1000,"confirm":true,"client_request_id":"cr-3"}`)

		assertGateStopped(t, rec)
		assert.Contains(t, rec.Body.String(), "qy_pay_pwd_not_set",
			"没设过密码时要引导去设置,而不是放行")

		var orders int64
		require.NoError(t, gdb.Model(&Order{}).Count(&orders).Error)
		assert.Zero(t, orders)
	})
}

// TestPreviewIsNotGatedByPayPassword 反向约束:预览不动钱,不该要密码。
//
// 没有这一条,上面那三条会诱导出一个"把验密下沉进 create() 附近的公共校验链"
// 的实现 —— 那样确实能让上面全绿,同时把预览也一起挡住,而裁决 1 写的是
// 「验密点只有一处」。两条方向相反的断言合起来才把位置钉住。
func TestPreviewIsNotGatedByPayPassword(t *testing.T) {
	newPayPassGateEnv(t)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/transfer/preview",
		bytes.NewBufferString(`{"identifier":"someone@example.com"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 9201)
	handlePreview(c)

	assert.NotContains(t, rec.Body.String(), "qy_pay_pwd",
		"预览不动钱,不得被支付密码挡下 —— 验密点只有 handleCreate 一处")
}
