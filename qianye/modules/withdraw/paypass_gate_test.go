package withdraw

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/modules/commission"
	"github.com/QuantumNous/new-api/qianye/modules/paypass"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// paypass_gate_test.go —— 提现的验密闸门**真的装在出钱路径上**。
//
// # 这条测试补的是什么缺口
//
// design-13-paypass.md §0 白纸黑字写着「佣金提现 ❌ 未接入」:支付密码只挡住了
// 站内划转,而提现是钱**离开站点**的那条路。拿到会话的攻击者绕开划转直接提现,
// 支付密码要防的那个威胁就完全没被防住。
//
// 与 transfer/paypass_gate_test.go 同形,但多做一件它做不到的事:**正向对照**。
// 那份测试自己在注释里承认,它的"订单表为空"是一条空断言 —— 环境里没有主库,
// create() 走两步就自己失败了,所以"没落单"证明不了是闸门拦的。
// 本文件先跑一条**带正确密码、完整落库成功**的用例,证明这套环境确实能造出一张
// 提现单并冻结佣金;此后再断言"不带密码时零单据、零冻结",那个零才有含义。
//
// # 为什么必须从 handleCreate 打进去
//
// paypass 包自己的用例把闸门本身测得很扎实,但它们全都直接调 paypass.Require ——
// 证明的是「门是好的」,证明不了「门装在门框上」。本仓累计出现十几次的头号缺陷
// 形状正是后者:写对了,但没接上。

// gatePassword 是本文件用的支付密码明文。只存在于测试进程内,
// 哈希在 fixture 里现算 —— 写死一个哈希常量会在换 cost 时静默失配。
const gatePassword = "wd-gate-7391"

const gateUserId = 7301

// newWithdrawGateEnv 造一套能**跑完整条提现申请**的环境。
//
// 三件东西缺一不可,这也正是 transfer 那份测试缺的:
//   - 扩展库:提现自己那几张表 + 佣金两张 + 支付密码表 + 审计表;
//   - 主库:create() 第一件事就是 model.GetUserById 判账号状态;
//   - 配置:guard.RequireAPI(FlagWithdraw) 与 acceptCreate 都要读它。
//
// 审计表与 qy_settings 必须建。audit.Write 默认是开的、paypass 的锁定策略也读
// qy_settings,两者任一读写失败都会走 db.MarkFailure 把扩展库标成不健康 ——
// 那会让**后续步骤**莫名其妙地 503,而失败现场看起来与闸门毫无关系。
//
// 返回的 sqlRecorder 记录扩展库上真正执行过的每一条语句 ——
// TestWithdrawGateRunsBeforeAnyFundingWork 靠它证明"验密失败时资金表一次都没被碰过"。
func newWithdrawGateEnv(t *testing.T) (*gorm.DB, *sqlRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := &sqlRecorder{Interface: logger.Discard}
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	// 扩展库固定是 MySQL,commission.LockBalance 无条件挂 FOR UPDATE,sqlite 不认。
	// 与 newTestDB 同一做法:被吞掉的只是 SQL 子句。真正的并发回归在 concurrency_test.go。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	tables := append(withdrawTables(),
		&paypass.PayPassword{}, &qymodel.AuditLog{}, &qymodel.Setting{})
	require.NoError(t, gdb.AutoMigrate(tables...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	prevCfg := qyConfig.Swap(&config.Config{
		Enabled: true,
		Withdraw: config.Withdraw{
			Enabled:  true,
			Methods:  []string{config.WithdrawMethodQuota},
			MinQuota: 1000,
		},
	})
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		qyConfig.Store(prevCfg)
		_ = sqlDB.Close()
	})

	mainDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	mainSQL, err := mainDB.DB()
	require.NoError(t, err)
	mainSQL.SetMaxOpenConns(1)
	require.NoError(t, mainDB.AutoMigrate(&model.User{}))
	require.NoError(t, mainDB.Create(&model.User{
		Id: gateUserId, Username: "gate-user", Status: common.UserStatusEnabled,
	}).Error)
	prevMain := model.DB
	model.DB = mainDB
	t.Cleanup(func() {
		model.DB = prevMain
		_ = mainSQL.Close()
	})

	// 佣金远大于单笔申请额:这条测试测的是验密,余额不足会用 errInsufficient
	// 把请求挡回去,那样即使闸门根本没装,"没落单"也照样成立。
	seedBalance(t, gdb, gateUserId, 10_000_000)
	return gdb, rec
}

// seedGatePayPassword 给用户落一行真实可验的支付密码。
//
// 哈希用 bcrypt 现算而不是抄一个常量:paypass.hashPassword 是另一个包的未导出
// 函数,跨包拿不到;而抄常量的下场是 hashCost 一改就静默失配,
// 测试会从"验密通过"悄悄变成"验密失败",而三条失败用例照样绿。
func seedGatePayPassword(t *testing.T, gdb *gorm.DB, userId int, password string) {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)
	now := common.GetTimestamp()
	require.NoError(t, gdb.Create(&paypass.PayPassword{
		UserId: userId, Algo: "bcrypt", Hash: string(h),
		SetAt: now, ChangedAt: now, UpdatedAt: now,
	}).Error)
}

func postWithdrawCreate(t *testing.T, userId int, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/withdraw", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", userId)
	handleCreate(c)
	return rec
}

// assertNothingMoved 断言这次请求在资金侧一点痕迹都没留下。
//
// 「拒绝了」与「拒绝了**且没动钱**」是两回事:验密若被放在 create() 之后,
// 响应码照样是 403,而单据已经落库、佣金已经冻结。
func assertNothingMoved(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	var orders int64
	require.NoError(t, gdb.Model(&Withdrawal{}).Count(&orders).Error)
	assert.Zero(t, orders, "被验密拦下时不得留下任何提现单")

	var bal commission.Balance
	require.NoError(t, gdb.Where("user_id = ?", gateUserId).Take(&bal).Error)
	assert.Zero(t, bal.FrozenQuota, "被验密拦下时不得冻结任何佣金")
}

// assertGateStopped 断言闸门拦下之后 handler **立刻返回了**。
//
// 只断言状态码与 code 前缀是不够的:把 `if !paypass.Require(...) { return }` 写成
// 裸调用 `paypass.Require(...)`(接了闸门却把返回值丢掉)时,Require 内部已经写好
// 错误响应,状态码与 code 都不会变。真正可观测的差异是响应体——handler 没 return
// 的话 create() 会继续跑并再写一次响应,于是 body 里出现**第二段 JSON**。
// 判据因此是「body 恰好是一个 JSON 对象」。
func assertGateStopped(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := rec.Body.String()
	assert.NotEqual(t, http.StatusOK, rec.Code,
		"没通过验密的提现必须被拒 —— 闸门要么没装,要么返回值被丢掉了")
	assert.Contains(t, body, "qy_pay_pwd",
		"拒绝的理由必须来自支付密码闸门,不能与「余额不足」这类失败混在一起")
	assert.Equal(t, 1, strings.Count(body, `"success"`),
		"闸门拦下后 handler 必须立刻 return —— 响应体里多出第二段 JSON,"+
			"说明 create() 仍然被执行了(裸调用 paypass.Require 而没判返回值)")
}

// TestWithdrawCreateIsGatedByPayPassword 钉死:提现提交必须过验密闸门。
//
// 第一条子用例是**正向对照**,它让后面三条的"零单据"成为有效断言:
// 撤掉 handleCreate 里那一行 paypass.Require,后三条会因为申请真的落库而全部变红。
func TestWithdrawCreateIsGatedByPayPassword(t *testing.T) {
	const okBody = `{"client_request_id":"wd-gate-0001","method":"quota","quota":500000,` +
		`"pay_password":"` + gatePassword + `"}`

	t.Run("带正确支付密码的申请必须真的落库", func(t *testing.T) {
		gdb, _ := newWithdrawGateEnv(t)
		seedGatePayPassword(t, gdb, gateUserId, gatePassword)

		rec := postWithdrawCreate(t, gateUserId, okBody)

		require.Equal(t, http.StatusOK, rec.Code,
			"正向对照跑不通,后面三条的「零单据」就证明不了任何事。响应:%s", rec.Body.String())

		var orders int64
		require.NoError(t, gdb.Model(&Withdrawal{}).Count(&orders).Error)
		assert.EqualValues(t, 1, orders)

		var bal commission.Balance
		require.NoError(t, gdb.Where("user_id = ?", gateUserId).Take(&bal).Error)
		assert.EqualValues(t, 500000, bal.FrozenQuota, "申请即冻结,这一步是钱真的动了的证据")
	})

	t.Run("不带支付密码必须被拒且一分钱没动", func(t *testing.T) {
		gdb, _ := newWithdrawGateEnv(t)
		seedGatePayPassword(t, gdb, gateUserId, gatePassword)

		rec := postWithdrawCreate(t, gateUserId,
			`{"client_request_id":"wd-gate-0002","method":"quota","quota":500000}`)

		assertGateStopped(t, rec)
		assert.Contains(t, rec.Body.String(), "qy_pay_pwd_required")
		assertNothingMoved(t, gdb)
	})

	t.Run("带错密码同样被拒", func(t *testing.T) {
		gdb, _ := newWithdrawGateEnv(t)
		seedGatePayPassword(t, gdb, gateUserId, gatePassword)

		rec := postWithdrawCreate(t, gateUserId,
			`{"client_request_id":"wd-gate-0003","method":"quota","quota":500000,`+
				`"pay_password":"definitely-not-it"}`)

		assertGateStopped(t, rec)
		assert.Contains(t, rec.Body.String(), "qy_pay_pwd_wrong")
		assertNothingMoved(t, gdb)
	})

	t.Run("从未设置支付密码的用户被拒并被引导去设置", func(t *testing.T) {
		gdb, _ := newWithdrawGateEnv(t)
		// 刻意不 seed。这条钉死的是「没设过密码 = 拒绝并提示去设置」这个决策:
		// 放行才是这里最容易被写出来的实现(「照顾老用户」),而那等于让盗号者
		// 挑一个没设过密码的账号就能把佣金提走。
		rec := postWithdrawCreate(t, gateUserId,
			`{"client_request_id":"wd-gate-0004","method":"quota","quota":500000,`+
				`"pay_password":"`+gatePassword+`"}`)

		assertGateStopped(t, rec)
		assert.Contains(t, rec.Body.String(), "qy_pay_pwd_not_set",
			"没设过密码时要引导去设置,而不是放行")
		assertNothingMoved(t, gdb)
	})
}

// TestWithdrawGateRunsBeforeAnyFundingWork 钉死验密跑在 create() **之前**,
// 因而必然在事务之外。
//
// # 为什么这条不能省
//
// 「加一行 paypass.Require」有一个看起来更"内聚"的写法:把它挪进 submitInTx。
// 那会同时踩两颗雷:
//
//   - 挪到 commission.LockBalance **之前** → 事务的第一条语句不再是行锁,
//     MySQL 的 REPEATABLE READ 快照被钉在加锁之前,四道风控闸门静默失效
//     (TestSubmitInTxLocksBeforeItReads 会红,但那条测试给出的线索是"锁的位置",
//     不会有人联想到是验密挤的);
//   - 挪到行锁**之后** → 每笔提现的行锁持有时间被抬高到一次 bcrypt 的量级,
//     而且 paypass 用的是自己的 db.Get() 句柄(它必须脱离请求 ctx,否则客户端
//     断连就能取消掉失败计数写入),在持锁事务里再借第二条连接写另一张表,
//     是连接池耗尽型死锁的标准形状。**这一条没有任何现成测试会红。**
//
// # 判据为什么是"资金表一次都没被查过"
//
// 第一版这里数的是 GORM 的 `gorm:begin_transaction` 回调,而回滚验证当场抓到
// 它是**假回归**:那个回调挂在 Create 的链上,每条 INSERT 都会触发一次,它数的
// 其实是"有没有写过库"。验密若被放在行锁**之后**,事务开了、锁也拿了,但因为
// 验密失败直接回滚,一条 INSERT 都没发生 —— 计数为 0,测试照样绿,而那正是
// 本条用例声称要挡的那一种写法。
//
// 换成"扩展库上有没有出现过针对资金表的查询"就没有这个盲区:
// create() 一旦被执行,`commission.Withdrawable` 的余额预读与事务里的
// `LockBalance` 行锁都会打在 qy_commission_balance 上。闸门在 create() 之前
// 拦下时,这张表一次都不会被碰。
func TestWithdrawGateRunsBeforeAnyFundingWork(t *testing.T) {
	gdb, sqls := newWithdrawGateEnv(t)
	seedGatePayPassword(t, gdb, gateUserId, gatePassword)

	// 建表与播种本身也会查 qy_commission_balance(AutoMigrate 的存在性探测、
	// seedBalance 的插入)。只看这次请求之后新增的那些语句。
	beforeRequest := len(sqls.selects())

	rec := postWithdrawCreate(t, gateUserId,
		`{"client_request_id":"wd-gate-0005","method":"quota","quota":500000,`+
			`"pay_password":"definitely-not-it"}`)

	assertGateStopped(t, rec)

	touched := []string{}
	for _, sql := range sqls.selects()[beforeRequest:] {
		if strings.Contains(sql, "qy_commission_balance") {
			touched = append(touched, sql)
		}
	}
	assert.Empty(t, touched,
		"验密失败,却已经查过佣金余额表 —— 说明 create() 被执行了,"+
			"闸门要么在 create() 内部、要么被挪进了 submitInTx。"+
			"放在行锁前会顶掉「行锁必须是事务第一条语句」这条不变量;"+
			"放在行锁后会让每笔提现的持锁时间跟着 bcrypt 走,并在持锁期间"+
			"再借一条连接写 qy_pay_passwords。它必须留在 create() 之前。")
}
