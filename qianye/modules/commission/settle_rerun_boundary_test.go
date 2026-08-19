package commission

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// settle_rerun_boundary_test.go —— 「重跑今天这一轮」的权限边界。
//
// # 这一版拍板:维持 ADMIN,不上收到 SUPER_ADMIN
//
// 理由不是"改起来麻烦",而是这条动作在**它自己模块里的相对位置**:
//
//	commission.settle          按人立即结算   ADMIN   真的把钱打进可提现余额
//	commission.clawback        手工冲正       ADMIN   真的写一条负额账目
//	commission.balances/adjust 手工增减佣金   ADMIN   真的改余额
//	commission.cache/invalidate 清缓存        ADMIN   改的是下一笔按什么费率算
//	commission.settle/rerun    重跑今天这一轮 ADMIN   ← 本条,一分钱都不动
//
// rearmDailyRun 只把 qy_commission_settle_run 里今天那一行改回"还要再跑",
// 金额、收款人、成熟期一个都不碰,发出去的仍然是定时任务本来就会发的那一批。
// 把这一条上收到 SUPER_ADMIN,而让上面四条真正动钱的留在 ADMIN,得到的是一个
// **说不出理由的**边界:同一个管理员照样可以对着每个人按一次 adminSettle 把
// 同样的钱发完,只是要按很多次。安全上什么也没换来,可用性上换掉的是当天那一跑
// 挂掉之后唯一的整轮补救入口 —— 而它按下去的时刻恰恰是出故障的时刻,那时候
// 要求"必须找到 root 账号"是把一次故障拖成两次。
//
// 已有的约束反而是对的那几条:CriticalRateLimit 防连点,audit.Write 记下是谁
// 在哪一天按的(成功与失败都记),两者由下面的断言与 audit_coverage_guard_test
// 一起守住。
//
// # 这条测试守什么
//
// 不是"它现在挂在哪个组"这种同义反复,而是两条会被无声破坏的事实:
//
//	① 它不得出现在用户端路由树上(用户能重排全站结算 = 谁都能催单);
//	② 它必须与真正动钱的那几条**同组同限流**——哪天有人把它单独拎出去,
//	   这条断言会逼他把上面那段理由重新读一遍再决定。
func TestDailySettleRerunSharesTheMoneyActionsTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	Mod{}.RegisterUserRoutes(e.Group("/api/qy"))
	Mod{}.RegisterAdminRoutes(e.Group("/api/qy/admin"))

	paths := map[string]bool{}
	for _, r := range e.Routes() {
		paths[r.Method+" "+r.Path] = true
	}

	const rerun = "POST /api/qy/admin/commission/settle/rerun"
	require.True(t, paths[rerun], "重跑必须挂在管理端组上")
	assert.False(t, paths["POST /api/qy/commission/settle/rerun"],
		"用户端出现重跑 = 任何登录用户都能把全站当天的结算队列重新排一遍")

	// 真正动钱的那几条必须仍在同一个管理端组里。它们是上面那段理由的前提:
	// 一旦哪条被上收,重跑留在 ADMIN 这个结论就该重新论证。
	for _, sibling := range []string{
		"POST /api/qy/admin/commission/settle",
		"POST /api/qy/admin/commission/clawback",
		"POST /api/qy/admin/commission/balances/adjust",
	} {
		assert.True(t, paths[sibling],
			"%s 不再与重跑同组 —— 重跑维持 ADMIN 的理由建立在它比这些动作更轻上", sibling)
	}
}

// TestSettleRerunKeepsCriticalRateLimit 守那道限流。
//
// 重跑不动钱,但它会让当天剩余队列立刻开跑;连点会把同一批人的结算反复
// 排进队列,浪费的是主库连接与一次日结的租约。CriticalRateLimit 是它与
// adminSettle / adminClawback 共用的那一道,不能只有它例外。
//
// 从源码文本上断言,而不是从 gin 的路由树:gin 只暴露链上最后一个 handler 的
// 名字,中间件挂没挂在运行时看不出来 —— 而"挂漏一道中间件"恰恰是无声的。
func TestSettleRerunKeepsCriticalRateLimit(t *testing.T) {
	raw, err := os.ReadFile("module.go")
	require.NoError(t, err)

	line := regexp.MustCompile(`(?m)^.*"/commission/settle/rerun".*$`).FindString(string(raw))
	require.NotEmpty(t, line, "module.go 里找不到重跑的注册行")
	assert.True(t, strings.Contains(line, "crit"),
		"重跑的注册行没挂 CriticalRateLimit:%s", line)
}
