package groupns

// usergroup_edit_test.go —— 「用户分组」那一张表上的编辑动作。
//
// 两件事各守一组:
//
//	充值倍率  它落在**上游 options**、直接乘进收款金额。"配了一个值"与"没配过"
//	          必须区分得开(前者按那个值收、后者按 1 收),而 **0 一律拒收** ——
//	          支付路径读到 0 之后按 1 收款,库里的 0 只会让配置值与收款值分家。
//	补登记    表上的清单是「观测值 ∪ 登记表」,所以一定会出现只在 users.group 里、
//	          还没有登记行的名字。对着这样一行按编辑必须能改,而不是收到一句
//	          「还没有登记」然后去别处找一个叫「回填」的按钮。

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storedTopupRatio(t *testing.T, name string) (float64, bool) {
	t.Helper()
	m, err := loadTopupRatios()
	require.NoError(t, err)
	v, ok := m[name]
	return v, ok
}

// TestUpdateUserGroupTopupRatioKeepsSetDistinctFromUnset 是这一组里最重要的一条。
//
// 上游 GetTopupGroupRatio 缺键时返回 **1** 并写一条 SysError,配了值时返回那个值。
// 「删掉这个键」与「配一个值」因此是两种不同的收款结果,而在一个 float64 零值上
// 它们完全同形 —— 请求体因此用 *float64 + 一个显式的 clear 布尔,两者同时给出报 400。
//
// ⚠ 这里刻意**不**拿 0 当"显式免费"来验:见 validateTopupRatio 的注释,
// 四条支付路径读到 0 之后一律按 1 收款,0 在这一侧不是一个能表达任何意图的值。
// 「0 是否落库」由 TestUpdateUserGroupRejectsAmbiguousAndUnsafeTopupRatio 守。
func TestUpdateUserGroupTopupRatioKeepsSetDistinctFromUnset(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"default": 1})
	seedOptionRatios(t, main, `{}`, `{"vip":0.9}`)
	seedUser(t, main, 1, "vip")
	require.NoError(t, gdb.Create(newUserGroup("vip", 1, now())).Error)

	t.Run("显式配值必须落库并进热路径", func(t *testing.T) {
		res := callGroupHandler(t, http.MethodPut, "/user-groups/vip",
			gin.Param{Key: "name", Value: "vip"}, `{"topup_ratio":0.5}`, adminUpdateUserGroup)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())

		v, ok := storedTopupRatio(t, "vip")
		require.True(t, ok)
		assert.Equal(t, 0.5, v)
		assert.Equal(t, 0.5, common.GetTopupGroupRatio("vip"),
			"热路径读的就是这一份内存快照,它必须跟着一起变")
	})

	t.Run("clear 才是删键", func(t *testing.T) {
		res := callGroupHandler(t, http.MethodPut, "/user-groups/vip",
			gin.Param{Key: "name", Value: "vip"}, `{"clear_topup_ratio":true}`, adminUpdateUserGroup)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())

		_, ok := storedTopupRatio(t, "vip")
		assert.False(t, ok, "clear_topup_ratio 必须真的把键删掉")
		assert.Equal(t, float64(1), common.GetTopupGroupRatio("vip"),
			"删键之后上游回落到 1 —— 这正是它与「配了一个 0.5」的区别")
	})

	t.Run("不给这两个字段就一个字节都不动", func(t *testing.T) {
		require.NoError(t, common.UpdateTopupGroupRatioByJSONString(`{"vip":0.7}`))
		res := callGroupHandler(t, http.MethodPut, "/user-groups/vip",
			gin.Param{Key: "name", Value: "vip"}, `{"note":"只改备注"}`, adminUpdateUserGroup)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())

		v, ok := storedTopupRatio(t, "vip")
		require.True(t, ok)
		assert.Equal(t, 0.7, v, "只改备注的那一次请求不得顺手动掉充值倍率")
	})
}

// TestUpdateUserGroupRejectsAmbiguousAndUnsafeTopupRatio 守两道入参闸门。
//
// 上游对充值倍率没有任何校验,而它被直接乘进 payMoney:负数是一张负价订单。
// 同时给出 topup_ratio 与 clear_topup_ratio 时服务端无法判断哪个是本意 ——
// 猜一个的表现是运营以为自己清掉了,而实际留了一个值(或者反过来)。
func TestUpdateUserGroupRejectsAmbiguousAndUnsafeTopupRatio(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"default": 1})
	seedOptionRatios(t, main, `{}`, `{"vip":1}`)
	seedUser(t, main, 1, "vip")
	require.NoError(t, gdb.Create(newUserGroup("vip", 1, now())).Error)

	for name, body := range map[string]string{
		"负倍率":  `{"topup_ratio":-1}`,
		"超过上限": `{"topup_ratio":100000}`,
		// 0 是这一组里唯一一个"看起来合法"的值,而它恰恰是最危险的那个:
		// controller/topup.go:159 与另外四处逐字相同的 `if ratio == 0 { ratio = 1 }`
		// 会把它抬回 1,于是界面写着 0、收款按 1,而这个偏差不出现在任何告警里。
		// 就算把那五处 if 删掉,payMoney < 0.01 那道闸门也只会让这一档人充不了值。
		// 在入口拒掉是唯一能让"界面上写的"与"实际收的"永远相等的做法。
		"零倍率":      `{"topup_ratio":0}`,
		"设值与清除同时给": `{"topup_ratio":0.5,"clear_topup_ratio":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := callGroupHandler(t, http.MethodPut, "/user-groups/vip",
				gin.Param{Key: "name", Value: "vip"}, body, adminUpdateUserGroup)
			require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())

			v, ok := storedTopupRatio(t, "vip")
			require.True(t, ok)
			assert.Equal(t, float64(1), v, "被拒绝的请求不得留下任何写入")
		})
	}
}

// TestUpdateUserGroupBackfillsObservedOnlyName 守"编辑时补登记"。
//
// 表上的清单是「观测值 ∪ 登记表」,只在 users.group 里的名字必须能直接编辑。
// 400 掉的表现是这一档人的备注永远改不了,而运营看到的理由
// (「还没有登记」)指向一个他不需要知道的内部区分。
func TestUpdateUserGroupBackfillsObservedOnlyName(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"default": 1})
	seedOptionRatios(t, main, `{}`, `{}`)
	seedUser(t, main, 1, "历史遗留档")

	res := callGroupHandler(t, http.MethodPut, "/user-groups/历史遗留档",
		gin.Param{Key: "name", Value: "历史遗留档"},
		`{"note":"终于能改了"}`, adminUpdateUserGroup)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())

	var row UserGroup
	require.NoError(t, gdb.Where("name = ?", "历史遗留档").Take(&row).Error)
	assert.Equal(t, "终于能改了", row.Note)
	assert.Equal(t, DefaultModeInherit, row.DefaultMode,
		"补出来的登记行必须是 inherit —— 补登记本身零行为变化")
}

// TestUpdateUserGroupRejectsNameNobodyUses 守补登记的边界。
//
// 一个既没登记、users.group 里也没人挂着的名字意味着请求里的名字拼错了
// (或者那一档刚被删掉)。此时建行等于把一个错字变成一个正式分组,
// 而它从此会出现在每一个下拉里。
func TestUpdateUserGroupRejectsNameNobodyUses(t *testing.T) {
	gdb := newTestDB(t)
	main := newMainDB(t)
	enableExtAPI(t, gdb)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"default": 1})
	seedOptionRatios(t, main, `{}`, `{}`)
	seedUser(t, main, 1, "vip")

	res := callGroupHandler(t, http.MethodPut, "/user-groups/vlp",
		gin.Param{Key: "name", Value: "vlp"}, `{"note":"拼错了"}`, adminUpdateUserGroup)
	require.Equal(t, http.StatusBadRequest, res.Code, res.Body.String())

	var n int64
	require.NoError(t, gdb.Model(&UserGroup{}).Where("name = ?", "vlp").Count(&n).Error)
	assert.Zero(t, n, "拼错的名字绝不能被顺手建成一个正式分组")
}
