package lottery

// guess_bet_max_scope_db_test.go —— 单注上限**管的是一笔投注,不是一个人**。
//
// 管理端建活动的表单曾经给这一格自动填「单注额 × 20」,提示写着
// 「一个大户最多顶 20 个按单注额下注的普通参与者」。那句话是假的,而它错的
// 方向恰好是最坏的那一个:读到它的运营会认为大户问题已经解决,于是**不去设**
// 那个真正管用的每人参与上限。推荐值与那句话都已经删掉
// (web/src/features/qy/pages/admin-lottery/lib/advice.ts),这里钉住它们
// 之所以必须删的那个事实。
//
// 一个人的总押注上限 = bet_max_quota × max_entries_per_user。两格里任何一格
// 是 0(= 不限)时,乘积就是不限。
//
// 判据全部走真实路由(建活动 → 发布 → 报名),期望值独立算出、不从被测代码回读。
import (
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuessBetMaxCapsOneBetNotOnePerson(t *testing.T) {
	const (
		stake      = 1_000
		multiple   = 20
		betMax     = stake * multiple // 被删掉的那个「推荐值」
		startQuota = 500_000
	)

	_, main, r := guessE2EEnv(t, startQuota)
	now := common.GetTimestamp()

	// 每人参与上限刻意留空 —— 0 是后端的默认值,也是"运营没填"的样子。
	create := `{
		"kind":"guess","title":"单注上限只管一笔",
		"stake_quota":` + strconv.Itoa(stake) + `,
		"bet_max_quota":` + strconv.Itoa(betMax) + `,
		"fee_bps":500,
		"open_at":` + strconv.FormatInt(now-60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"rules":{"max_entries_per_user":0},
		"options":[
			{"opt_no":1,"label":"会涨"},
			{"opt_no":2,"label":"不会涨"}
		]}`
	code, body := guessCall(t, r, http.MethodPost, "/admin/lottery/activities", create)
	require.Equalf(t, http.StatusOK, code, "建竞猜失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")
	code, body = guessCall(t, r, http.MethodPost,
		"/admin/lottery/activities/"+actNo+"/publish", `{}`)
	require.Equalf(t, http.StatusOK, code, "发布竞猜失败: %s", body)

	bet := func(userId int, amount int64, reqId string) (int, []byte) {
		guessE2EActor = userId
		return guessCall(t, r, http.MethodPost, "/lottery/activities/"+actNo+"/entries",
			`{"client_request_id":"`+reqId+`","opt_no":1,"amount":`+
				strconv.FormatInt(amount, 10)+`}`)
	}

	// ① 上限确实在拦:超一个单位的**单笔**投注被拒。
	code, body = bet(guessE2EUserA, betMax+1, "qy-t4-over")
	assert.NotEqualf(t, http.StatusOK, code, "单注上限没有拦住超额的一笔: %s", body)

	// ② 同一个人连开三笔顶格投注,三笔全过 —— 上限对"一个人"毫无约束。
	before := quotaOf(t, main, guessE2EUserA)
	for i := 1; i <= 3; i++ {
		code, body = bet(guessE2EUserA, betMax, "qy-t4-whale-"+strconv.Itoa(i))
		require.Equalf(t, http.StatusOK, code, "第 %d 笔顶格投注被拒: %s", i, body)
	}
	after := quotaOf(t, main, guessE2EUserA)

	// 独立算出的期望:3 × 20000 = 60000,也就是 60 个按单注额(1000)下注的
	// 普通参与者。被删掉的那句文案说的是"最多顶 20 个"。
	assert.EqualValues(t, 3*betMax, before-after,
		"一个人押进去的总额不等于三笔顶格之和")
	assert.EqualValues(t, 60, (before-after)/stake,
		"单注上限 20 倍并没有把一个人限制在 20 个普通参与者的量级上")

	// ③ 真正封顶的是两格的乘积。把每人参与上限设成 1 之后,同一个人的第二笔
	// 就进不来了 —— 而这一格与单注上限是两个独立的开关。
	capped := `{
		"kind":"guess","title":"两格一起才封得住",
		"stake_quota":` + strconv.Itoa(stake) + `,
		"bet_max_quota":` + strconv.Itoa(betMax) + `,
		"fee_bps":500,
		"open_at":` + strconv.FormatInt(now-60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"rules":{"max_entries_per_user":1,"max_attempts_per_user":4},
		"options":[
			{"opt_no":1,"label":"会涨"},
			{"opt_no":2,"label":"不会涨"}
		]}`
	code, body = guessCall(t, r, http.MethodPost, "/admin/lottery/activities", capped)
	require.Equalf(t, http.StatusOK, code, "建第二场失败: %s", body)
	cappedNo := jsonString(t, body, "data", "act_no")
	code, body = guessCall(t, r, http.MethodPost,
		"/admin/lottery/activities/"+cappedNo+"/publish", `{}`)
	require.Equalf(t, http.StatusOK, code, "发布第二场失败: %s", body)

	guessE2EActor = guessE2EUserB
	code, body = guessCall(t, r, http.MethodPost, "/lottery/activities/"+cappedNo+"/entries",
		`{"client_request_id":"qy-t4-capped-1","opt_no":1,"amount":`+
			strconv.Itoa(betMax)+`}`)
	require.Equalf(t, http.StatusOK, code, "第一笔应当放行: %s", body)
	code, body = guessCall(t, r, http.MethodPost, "/lottery/activities/"+cappedNo+"/entries",
		`{"client_request_id":"qy-t4-capped-2","opt_no":1,"amount":`+
			strconv.Itoa(betMax)+`}`)
	assert.NotEqualf(t, http.StatusOK, code,
		"每人参与上限=1 没有拦住同一个人的第二笔: %s", body)
}
