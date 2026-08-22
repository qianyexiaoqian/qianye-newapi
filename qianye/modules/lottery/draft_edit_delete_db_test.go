package lottery

// draft_edit_delete_db_test.go —— 草稿的「改」与「删」。
//
// # 这一组存在的理由
//
// 项目方原话:「草稿活动为什么改不了删不了,你弄一下,超级管理员拥有最高权限的。」
//
// 「改不了」的一半在前端(PUT 接口从来没有调用方,由 draft-edit-ui.test.ts 守),
// 另一半在这里:那条接口从来没被真的打过一遍,连"它到底能改哪几列"都只有
// draftUpdates 那张 map 自己说了算。
//
// 「删不了」整条在后端,而且不是一条设计取舍,是两道闸门被套用在了它们从没打算
// 覆盖的状态上(理由写在 checkActivityDeletable 的注释里)。
//
// # 这里钉住的六条
//
//  1. 草稿期**每一列**都真的改得动 —— 判据是"表列清单"与 draftUpdates 双向对齐,
//     而不是遍历 draftUpdates 自己的键(那样删掉一个键测试会跟着不检查它);
//  2. 非法值逐条被拒,且被拒之后活动行**逐列**与请求前一模一样;
//  3. 发布之后一个字都改不了,而且回的是"这件事从此不可能"而非"刷新重试";
//  4. 草稿删得掉,九张表归零、封面只解绑不删行、审计行删完还在;
//  5. 草稿的确认强度与代价匹配:不要求回填编号、不强制填理由 ——
//     但"脏草稿"(挂着参与/出款/承诺痕迹)一律拒绝;
//  6. 已结束那一档的强确认一个字节都没被放松。

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const draftAdminId = 4101

func draftRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", draftAdminId); h(c) }
	}
	r.POST("/admin/lottery/series", admin(handleCreateSeries))
	r.POST("/admin/lottery/activities", admin(handleCreateActivity))
	r.PUT("/admin/lottery/activities/:act_no", admin(handleUpdateActivity))
	r.POST("/admin/lottery/activities/:act_no/publish", admin(handlePublishActivity))
	r.DELETE("/admin/lottery/activities/:act_no", admin(handleDeleteActivity))
	r.POST("/admin/lottery/activities/:act_no/cancel", admin(handleCancelActivity))
	return r
}

// newDraftEnv 与 newRetireEnv 用同一份配置,外加把 RevealDelaySeconds 归零 ——
// 这一组不测"封盘到开奖的强制间隔",让它挡住每一个请求只会把失败原因搅浑。
func newDraftEnv(t *testing.T) *gorm.DB {
	t.Helper()
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
		MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
		MaxTotalEntriesHard: 1_000,
		// 竞猜手续费的上下界要显式给:零值等于"费率只能是 0",
		// 而这一组要证明的正是 fee_bps 在草稿期改得动。
		DefaultGuessFeeBps: 500, MaxGuessFeeBps: 2_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	return ext
}

// activityRow 把活动行读成 map[列名]字面量。
//
// 走 map 而不是 struct:被测的问题恰恰是"某一列没被写到",而 struct 断言只能
// 逐个字段手写,漏写哪一个就永远测不到哪一个。
func activityRow(t *testing.T, gdb *gorm.DB, actNo string) map[string]string {
	t.Helper()
	var raw map[string]any
	require.NoError(t, gdb.Table("qy_lot_activity").Where("act_no = ?", actNo).Take(&raw).Error)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// ─────────────────────────── 改 ───────────────────────────

// draftEditableColumns 是"草稿期必须真的改得动"的列清单。
//
// 它是**手写**的,而且必须手写:遍历 draftUpdates 自己的键去断言,等于让被测对象
// 决定自己被测哪几项 —— 有人从那张 map 里删掉一列,循环就跟着少检查一列,
// 而那一列从此在草稿期改不动,所有用例照常绿。下面 TestDraftUpdatesMapMatchesEditableColumns
// 把两边**双向**对上,少一列多一列都会红。
var draftEditableColumns = []string{
	"kind", "title", "intro",
	"cover_url", "cover_ref",
	"stake_quota", "bet_min_quota", "bet_max_quota",
	"open_at", "close_at", "draw_at", "settle_deadline",
	"rules_text", "rules_hash", "spec_text", "spec_hash",
	"allow_multi_win", "fee_bps", "min_entries_to_hold",
	"max_entries_per_user", "max_attempts_per_user",
	"max_total_entries", "max_total_users", "max_per_inviter",
	"cooldown_seconds", "dedup_ip",
	"draw_mode", "series_id", "series_no", "pool_share_bps",
	"ball_red_pool", "ball_red_pick", "ball_blue_pool", "ball_blue_pick",
}

// draftUpdatesNonComparable 是 draftUpdates 里**写了、但两次提交之间不保证不同**
// 的两列,它们不参加"值必须变"的断言:
//
//	algo:       buildActivity 恒写 AlgoV2,两次提交必然相同。它仍然必须留在
//	            draftUpdates 里 —— 它要与 spec_hash 同一次写进去(见那里的注释),
//	            所以下面单独断言它等于 AlgoV2。
//	updated_at: 秒级时间戳,同一秒内的两次提交会相等,只断言不回退。
var draftUpdatesNonComparable = map[string]bool{"algo": true, "updated_at": true}

// draftUpdates 那张 map 与上面那份清单必须**双向**对齐。
//
// 单向对不住:只查"清单里的列都在 map 里",新增一列进 map 而不进清单时,
// 那一列的可改性从此无人守;只查反向,则删一列进不了任何断言。
func TestDraftUpdatesMapMatchesEditableColumns(t *testing.T) {
	got := make([]string, 0, 40)
	for col := range draftUpdates(&Activity{}) {
		if draftUpdatesNonComparable[col] {
			continue
		}
		got = append(got, col)
	}
	want := append([]string(nil), draftEditableColumns...)
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got,
		"draftUpdates 与 draftEditableColumns 对不上 —— 新增的列没进清单等于没人守它的可改性,"+
			"删掉的列意味着草稿期从此改不动它")
}

// 草稿期**每一列都改得动**:一份双色球草稿被整体改写成一场竞猜之后,
// 上面那份清单里的每一列都必须换了值。
//
// 用"双色球 → 竞猜"这条跨度最大的改法,是因为只有它能同时把三组列都推到另一边:
// 期次与号池那七列从非零回到零(其余改法碰不到它们)、竞猜独有的费率与单注上下限
// 从零变成非零、kind 与 draw_mode 同时翻面。
func TestDraftUpdateRewritesEveryEditableColumn(t *testing.T) {
	ext := newDraftEnv(t)
	r := draftRouter()
	now := common.GetTimestamp()

	code, body := callJSON(t, r, http.MethodPost, "/admin/lottery/series", `{
		"title":"草稿改写系列","red_pool":12,"red_pick":3,
		"blue_pool":4,"blue_pick":1,"pool_share_bps":7000,
		"issue_cap_quota":1000000,"seed_quota":60000}`)
	require.Equalf(t, http.StatusOK, code, "建系列失败: %s", body)
	seriesNo := jsonString(t, body, "data", "series_no")

	// 先备一张已上传、还没落到任何活动上的封面。
	//
	// 没有它,cover_ref 那一格在前后两次提交里都是空串,而空串等于空串 ——
	// 那一列的可改性就成了一个测了个空的断言。两种封面来源是**互斥**的一对,
	// 所以这次改写正好走"从上传图换成外链"这条最容易漏写一列的路。
	const coverRef = "cov-draft-rewrite"
	require.NoError(t, ext.Create(&Cover{
		Ref: coverRef, UserId: draftAdminId, StoredName: "before.png",
		MimeType: "image/png", Size: 12, CreatedAt: now,
	}).Error)

	create := `{
		"kind":"draw","draw_mode":"ball","title":"改之前:双色球",
		"intro":"改之前的说明","cover_ref":"` + coverRef + `",
		"stake_quota":1000,
		"open_at":` + strconv.FormatInt(now-60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"series_no":"` + seriesNo + `",
		"min_entries_to_hold":3,
		"rules":{"max_entries_per_user":2,"max_attempts_per_user":4,
			"max_total_entries":100,"max_total_users":50,"max_per_inviter":5,
			"cooldown_seconds":30,"dedup_ip":false},
		"prizes":[
			{"tier":1,"name":"一等奖","count":1,"red_match":3,"blue_match":1,"pool_share_bps":6000},
			{"tier":2,"name":"二等奖","count":1,"red_match":3,"blue_match":0,"pool_share_bps":2500}
		]}`
	code, body = callJSON(t, r, http.MethodPost, "/admin/lottery/activities", create)
	require.Equalf(t, http.StatusOK, code, "建草稿失败: %s", body)
	actNo := jsonString(t, body, "data", "act_no")
	before := activityRow(t, ext, actNo)
	// 号池那一组必须真的是非零,否则下面"从非零变成零"那一半是空测。
	require.NotEqual(t, "0", before["ball_red_pool"], "夹具没继承到号池,这一格测了个空")
	require.NotEqual(t, "0", before["series_id"])
	require.Equal(t, coverRef, before["cover_ref"], "封面没认领上,这一格测了个空")
	require.Equal(t, "", before["cover_url"])

	update := `{
		"kind":"guess","title":"改之后:竞猜",
		"intro":"改之后的说明","cover_url":"https://example.invalid/after.png",
		"stake_quota":2000,"bet_min_quota":500,"bet_max_quota":9000,
		"open_at":` + strconv.FormatInt(now-30, 10) + `,
		"close_at":` + strconv.FormatInt(now+5400, 10) + `,
		"draw_at":` + strconv.FormatInt(now+9000, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+90000, 10) + `,
		"allow_multi_win":false,"fee_bps":300,"min_entries_to_hold":7,
		"rules":{"max_entries_per_user":1,"max_attempts_per_user":3,
			"max_total_entries":200,"max_total_users":80,"max_per_inviter":9,
			"cooldown_seconds":60,"dedup_ip":true,"min_quota":1234},
		"options":[
			{"opt_no":1,"label":"甲赢"},
			{"opt_no":2,"label":"乙赢"},
			{"opt_no":3,"label":"以上都不是","is_catch_all":true}
		]}`
	code, body = callJSON(t, r, http.MethodPut, "/admin/lottery/activities/"+actNo, update)
	require.Equalf(t, http.StatusOK, code, "改草稿失败: %s", body)
	after := activityRow(t, ext, actNo)

	for _, col := range draftEditableColumns {
		require.Containsf(t, before, col, "活动表上没有 %s 这一列,清单写错了", col)
		assert.NotEqualf(t, before[col], after[col],
			"%s 改完之后还是原值(%s)—— 草稿期这一列改不动", col, before[col])
	}
	// algo 不参加"值必须变",但它必须跟着 spec_hash 一起被写:一份 lot-v1 的
	// 老草稿被改一次之后,库里会存着 v2 形状的 spec_hash 却标着 lot-v1。
	assert.Equal(t, AlgoV2, after["algo"])
	assert.GreaterOrEqual(t, after["updated_at"], before["updated_at"])
	assert.Equal(t, StatusDraft, after["status"], "改草稿不许推动状态机")
	assert.Equal(t, "", after["commit_hash"], "草稿期不许出现承诺")

	// 奖档与选项是**整体替换**的两张从表,不在活动行上 —— 它们同样要跟着翻面。
	var prizes, options int64
	require.NoError(t, ext.Model(&Prize{}).Where("act_id > 0").Count(&prizes).Error)
	require.NoError(t, ext.Model(&Option{}).Where("act_id > 0").Count(&options).Error)
	assert.EqualValues(t, 0, prizes, "改成竞猜之后旧的两条奖档必须被删掉")
	assert.EqualValues(t, 3, options, "三个竞猜选项必须落库")
}

// 非法值逐条被拒,而且被拒之后活动行**逐列**与请求前一模一样。
//
// 只断言"回了 400"是不够的:buildActivity 在校验通过之后才拼出 next,
// 但封面换绑、奖档删除与选项重建都在同一个事务里 —— 一次半途失败若不回滚,
// 表现是奖档已经被删光、活动行还是旧的,而运营收到的只是一句"参数不合法"。
func TestDraftUpdateRejectsIllegalValuesAndLeavesRowIntact(t *testing.T) {
	now := common.GetTimestamp()
	ts := func(delta int64) string { return strconv.FormatInt(now+delta, 10) }
	// base 是一份合法的抽奖草稿请求体,每一格只把其中一处换掉。
	base := map[string]string{
		"kind":            `"draw"`,
		"title":           `"合法草稿"`,
		"stake_quota":     "1000",
		"open_at":         ts(-60),
		"close_at":        ts(3600),
		"draw_at":         ts(7200),
		"settle_deadline": ts(86400),
		"prizes":          `[{"tier":1,"name":"头奖","amount_quota":2000,"count":1}]`,
	}
	bodyWith := func(patch map[string]string) string {
		merged := make(map[string]string, len(base)+len(patch))
		for k, v := range base {
			merged[k] = v
		}
		for k, v := range patch {
			merged[k] = v
		}
		keys := make([]string, 0, len(merged))
		for k := range merged {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, `"`+k+`":`+merged[k])
		}
		return "{" + strings.Join(parts, ",") + "}"
	}

	for _, tc := range []struct {
		name  string
		patch map[string]string
		code  string
	}{
		{"活动类型不是 draw/guess", map[string]string{"kind": `"lottery"`}, "qy_lot_bad_request"},
		{"标题为空", map[string]string{"title": `"   "`}, "qy_lot_bad_request"},
		{"标题超过 60 字", map[string]string{"title": `"` + strings.Repeat("长", 61) + `"`}, "qy_lot_bad_request"},
		{"标题带控制字符(会污染 spec 原像)", map[string]string{"title": `"标题"`}, "qy_lot_bad_request"},
		{"参与费为 0(v1 没有免费场)", map[string]string{"stake_quota": "0"}, "qy_lot_bad_request"},
		{"参与费为负", map[string]string{"stake_quota": "-1"}, "qy_lot_bad_request"},
		{"参与费超过运营上限", map[string]string{"stake_quota": "5000001"}, "qy_lot_bad_request"},
		{"截止早于开始", map[string]string{"close_at": ts(-120)}, "qy_lot_bad_request"},
		{"截止已经过去", map[string]string{"open_at": ts(-7200), "close_at": ts(-60)}, "qy_lot_bad_request"},
		{"开奖早于截止", map[string]string{"draw_at": ts(1800)}, "qy_lot_bad_request"},
		{"结算截止早于开奖", map[string]string{"settle_deadline": ts(7000)}, "qy_lot_bad_request"},
		{"截止时间顶到 int64 上界(那条加法会溢出)",
			map[string]string{"close_at": "9223372036854775807"}, "qy_lot_bad_request"},
		{"最低成场人数为负", map[string]string{"min_entries_to_hold": "-1"}, "qy_lot_bad_request"},
		{"一档奖品都没有", map[string]string{"prizes": "[]"}, "qy_lot_bad_request"},
		{"两档撞同一个等级",
			map[string]string{"prizes": `[{"tier":1,"name":"甲","amount_quota":100,"count":1},
				{"tier":1,"name":"乙","amount_quota":100,"count":1}]`}, "qy_lot_bad_request"},
		{"奖档等级不是正整数",
			map[string]string{"prizes": `[{"tier":0,"name":"甲","amount_quota":100,"count":1}]`}, "qy_lot_bad_request"},
		{"奖品额度为 0",
			map[string]string{"prizes": `[{"tier":1,"name":"甲","amount_quota":0,"count":1}]`}, "qy_lot_bad_request"},
		{"奖品数量为 0",
			map[string]string{"prizes": `[{"tier":1,"name":"甲","amount_quota":100,"count":0}]`}, "qy_lot_bad_request"},
		{"奖品总额超上限(唯一能拦住多写一个零的闸门)",
			map[string]string{"prizes": `[{"tier":1,"name":"甲","amount_quota":5000000,"count":2}]`}, "qy_lot_prize_cap"},
		{"单档乘出来就溢出上限",
			map[string]string{"prizes": `[{"tier":1,"name":"甲","amount_quota":4000000,"count":3}]`}, "qy_lot_prize_cap"},
		{"定档方式不认识", map[string]string{"draw_mode": `"roulette"`}, "qy_lot_bad_request"},
		{"抽奖填了手续费(填了却不生效是最坏的界面谎言)",
			map[string]string{"fee_bps": "100"}, "qy_lot_bad_request"},
		{"名次制填了中奖概率",
			map[string]string{"prizes": `[{"tier":1,"name":"甲","amount_quota":100,"count":1,"win_ppm":5000}]`},
			"qy_lot_bad_request"},
		{"概率制的概率合计超过 100%",
			map[string]string{"draw_mode": `"prob"`, "max_total_entries": "10",
				"prizes": `[{"tier":1,"name":"甲","amount_quota":100000,"count":1,"win_ppm":600000},
					{"tier":2,"name":"乙","amount_quota":100000,"count":1,"win_ppm":600000}]`},
			"qy_lot_bad_request"},
		{"概率制某一档的概率为 0",
			map[string]string{"draw_mode": `"prob"`,
				"prizes": `[{"tier":1,"name":"甲","amount_quota":100000,"count":1,"win_ppm":0}]`},
			"qy_lot_bad_request"},
		{"全场参与上限超过系统硬上限",
			map[string]string{"rules": `{"max_total_entries":100000}`}, "qy_lot_bad_request"},
		{"双色球指向一个不存在的期次系列",
			map[string]string{"draw_mode": `"ball"`, "series_no": `"SR-does-not-exist"`,
				"prizes": `[{"tier":1,"name":"甲","count":1,"red_match":3,"blue_match":1,"pool_share_bps":6000}]`},
			"qy_lot_series_not_found"},
		{"双色球没指定期次系列",
			map[string]string{"draw_mode": `"ball"`,
				"prizes": `[{"tier":1,"name":"甲","count":1,"red_match":3,"blue_match":1,"pool_share_bps":6000}]`},
			"qy_lot_series_not_found"},
		{"竞猜只有一个选项",
			map[string]string{"kind": `"guess"`, "prizes": "[]",
				"options": `[{"opt_no":1,"label":"甲"}]`}, "qy_lot_bad_request"},
		{"竞猜两个选项撞同一个文案",
			map[string]string{"kind": `"guess"`, "prizes": "[]",
				"options": `[{"opt_no":1,"label":"甲"},{"opt_no":2,"label":"甲"}]`}, "qy_lot_bad_request"},
		{"竞猜没填结算截止(管理员可以无限期扣着奖池)",
			map[string]string{"kind": `"guess"`, "prizes": "[]", "settle_deadline": "0",
				"options": `[{"opt_no":1,"label":"甲"},{"opt_no":2,"label":"乙"}]`}, "qy_lot_bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newDraftEnv(t)
			r := draftRouter()

			code, body := callJSON(t, r, http.MethodPost, "/admin/lottery/activities", bodyWith(nil))
			require.Equalf(t, http.StatusOK, code, "建基准草稿失败: %s", body)
			actNo := jsonString(t, body, "data", "act_no")
			before := activityRow(t, ext, actNo)
			var prizesBefore int64
			require.NoError(t, ext.Model(&Prize{}).Where("act_id > 0").Count(&prizesBefore).Error)

			code, body = callJSON(t, r, http.MethodPut,
				"/admin/lottery/activities/"+actNo, bodyWith(tc.patch))
			assert.NotEqualf(t, http.StatusOK, code, "这一格本该被拒: %s", body)
			assert.Equalf(t, tc.code, jsonString(t, body, "code"), "错误 code 不对: %s", body)

			assert.Equal(t, before, activityRow(t, ext, actNo),
				"被拒之后活动行必须逐列原样 —— 半途写入的表现是奖档没了、活动行还是旧的")
			var prizesAfter int64
			require.NoError(t, ext.Model(&Prize{}).Where("act_id > 0").Count(&prizesAfter).Error)
			assert.Equal(t, prizesBefore, prizesAfter, "被拒之后奖档表必须跟着回滚")
		})
	}
}

// ─────────────────────────── 删 ───────────────────────────

// seedDraftActivity 造一份"每张从表都有行"的草稿。
//
// 事件与封面刻意都铺上:草稿期确实会有它们(创建那一刻写一条 create 事件、
// 向导里传过一张图),而"草稿一定是空的所以清不清都一样"正是这一组要否掉的假设。
func seedDraftActivity(t *testing.T, gdb *gorm.DB, mutate func(*Activity)) *Activity {
	t.Helper()
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusDraft
		a.Outcome = OutcomeNone
		a.Algo = AlgoV2
		a.CommitHash = ""
		a.RulesHash = "rul3s"
		a.SpecHash = "5pec"
		a.CoverRef = newSecret()[:16]
		if mutate != nil {
			mutate(a)
		}
	})
	require.NoError(t, gdb.Create(&Seed{
		ActId: act.Id, Seed: "5eedhex", RefSalt: "r5alt", IpSalt: "ip5alt", CreatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&Prize{
		ActId: act.Id, Tier: 1, Name: "头奖", AmountQuota: 1500, Count: 1,
	}).Error)
	require.NoError(t, gdb.Create(&Option{ActId: act.Id, OptNo: 1, Label: "甲"}).Error)
	require.NoError(t, gdb.Create(&Event{
		ActId: act.Id, FromStatus: "", ToStatus: StatusDraft,
		Action: ActionCreate, CreatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&Cover{
		Ref: act.CoverRef, UserId: draftAdminId, ActId: act.Id, ActNo: act.ActNo,
		StoredName: "abc.png", MimeType: "image/png", Size: 10,
		CreatedAt: now, BoundAt: now,
	}).Error)
	return act
}

// 草稿删得掉,而且删得干净:九张表归零、封面只解绑不删行、审计行删完还在。
//
// 请求体里**既没有 confirm_act_no 也没有 reason** —— 那正是这一档确认强度的
// 全部内容,写死在这条用例里。
func TestDeleteDraftActivity(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, nil)
	// 同库里再放一份无关草稿:删除必须只动目标那一份。
	other := seedDraftActivity(t, ext, nil)

	code, body := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, `{}`)
	require.Equalf(t, http.StatusOK, code, "删草稿失败: %s", body)

	for _, tc := range []struct {
		table string
		model any
	}{
		{"qy_lot_activity", &Activity{}},
		{"qy_lot_seed", &Seed{}},
		{"qy_lot_prize", &Prize{}},
		{"qy_lot_option", &Option{}},
		{"qy_lot_entry", &Entry{}},
		{"qy_lot_payout", &Payout{}},
		{"qy_lot_event", &Event{}},
		{"qy_lot_flag", &Flag{}},
	} {
		var n int64
		col := "act_id = ?"
		if tc.table == "qy_lot_activity" {
			col = "id = ?"
		}
		require.NoError(t, ext.Model(tc.model).Where(col, act.Id).Count(&n).Error)
		assert.EqualValuesf(t, 0, n, "%s 还留着指向已删草稿的行", tc.table)
	}
	// 封面是唯一一张只解绑不删行的从表:库行是磁盘那张图的唯一指针,
	// 删掉它文件就永远留在盘上。
	var cover Cover
	require.NoError(t, ext.Where("ref = ?", act.CoverRef).Take(&cover).Error)
	assert.EqualValues(t, 0, cover.ActId, "封面必须解绑")
	assert.Greater(t, cover.DetachedAt, int64(0), "封面必须打上待回收标记,而不是直接删行")

	var others int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", other.Id).Count(&others).Error)
	assert.EqualValues(t, 1, others, "只该动目标那一份")

	var row qymodel.AuditLog
	require.NoError(t, ext.Where("action = ? AND result = ? AND trace_no = ?",
		"lottery.activity.delete", qymodel.ResultOK, act.ActNo).Take(&row).Error)
	assert.Equal(t, draftAdminId, row.ActorUserId)
	assert.Equal(t, draftDeleteReason, row.Reason,
		"没填理由时审计里要写清「这次是运营主动没写」,而不是留一个像埋点漏了字段的空串")
	assert.Contains(t, row.BeforeSnap, act.ActNo,
		"删完之后这条审计是唯一还能证明这份草稿存在过的东西")
}

// 草稿这一档**不要求**回填活动编号、也不强制填理由;填了的理由照样进审计。
func TestDeleteDraftConfirmationStrength(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		wantReason string
	}{
		{"什么都不填", `{}`, draftDeleteReason},
		{"只填理由", `{"reason":"配错了参与费,重开一份"}`, "配错了参与费,重开一份"},
		{"回填了一个对不上的编号也不拦(草稿本来就不要求它)",
			`{"confirm_act_no":"LT-WRONG","reason":"随手清理"}`, "随手清理"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newDraftEnv(t)
			act := seedDraftActivity(t, ext, nil)

			code, body := callJSON(t, draftRouter(), http.MethodDelete,
				"/admin/lottery/activities/"+act.ActNo, tc.body)
			require.Equalf(t, http.StatusOK, code, "删草稿失败: %s", body)

			var n int64
			require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
			assert.EqualValues(t, 0, n)

			var row qymodel.AuditLog
			require.NoError(t, ext.Where("action = ? AND result = ?",
				"lottery.activity.delete", qymodel.ResultOK).Take(&row).Error)
			assert.Equal(t, tc.wantReason, row.Reason)
		})
	}
}

// 已结束那一档的强确认一个字节都没被放松 —— 放开草稿最容易顺手带塌的就是它。
func TestDeleteFinishedStillRequiresActNoAndReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{"不填编号", `{"reason":"清理"}`, "qy_lot_delete_confirm"},
		{"编号回填错", `{"confirm_act_no":"LT-WRONG","reason":"清理"}`, "qy_lot_delete_confirm"},
		{"不填理由", `{}`, "qy_lot_bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newRetireEnv(t)
			act := seedFinishedActivity(t, ext)

			code, body := callJSON(t, draftRouter(), http.MethodDelete,
				"/admin/lottery/activities/"+act.ActNo, tc.body)
			assert.NotEqual(t, http.StatusOK, code)
			assert.Equal(t, tc.code, jsonString(t, body, "code"))

			var n int64
			require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
			assert.EqualValues(t, 1, n, "被确认闸门拦下之后一行都不许动")
		})
	}
}

// 脏草稿一律拒绝:三条正向断言各自触发一次。
//
// 这三种情况在库结构正常时一条都不可能成立(报名带着 status='published'、
// 出款只在揭示事务里登记、承诺三列只在 publish 那一次写),所以它们等价于
// "库被直接改过" —— 而那时更不该抹掉现场。
func TestDeleteDraftRefusesDirtyDraft(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, gdb *gorm.DB, act *Activity)
	}{
		{
			name: "草稿上竟然挂着参与明细",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Create(&Entry{
					EntryNo: newEntryNo(), ActId: act.Id, Seq: 1, UserId: 701,
					Amount: 1000, Status: EntrySuccess, CreatedAt: common.GetTimestamp(),
				}).Error)
			},
		},
		{
			// 已结束那一支的 ④ 只数 pending/excluded,草稿这一支**一条都不许有**:
			// 一份收得到报名的草稿本身就是异常,它是哪个终态无关紧要。
			name: "草稿上挂着一条已经结清的参与",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Create(&Entry{
					EntryNo: newEntryNo(), ActId: act.Id, Seq: 1, UserId: 702,
					Amount: 1000, Status: EntryRefunded, CreatedAt: common.GetTimestamp(),
				}).Error)
			},
		},
		{
			name: "草稿上挂着一笔已到账的出款",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				seedPayout(t, gdb, act.Id, func(p *Payout) {
					p.Status = PayoutPaid
					p.SettledAt = common.GetTimestamp()
				})
			},
		},
		{
			name: "草稿上竟然有承诺哈希",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Model(act).Update("commit_hash", "c0mm1t").Error)
			},
		},
		{
			name: "草稿上竟然有发布时间",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Model(act).Update("published_at", common.GetTimestamp()).Error)
			},
		},
		{
			// issue_no 由 claimSeriesPool 在 publish 那一刻分配。它非零意味着
			// 这份"草稿"已经从系列池里取走过钱,后续期次的结转链上有它的位置。
			name: "草稿上竟然有期号(说明它取走过系列池)",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Model(act).Update("issue_no", 3).Error)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newDraftEnv(t)
			act := seedDraftActivity(t, ext, nil)
			tc.arrange(t, ext, act)

			code, body := callJSON(t, draftRouter(), http.MethodDelete,
				"/admin/lottery/activities/"+act.ActNo, `{}`)
			assert.Equal(t, http.StatusConflict, code, string(body))
			assert.Equal(t, "qy_lot_delete_draft_dirty", jsonString(t, body, "code"))

			var n int64
			require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
			assert.EqualValues(t, 1, n, "被拦下之后一行都不许动")
			require.NoError(t, ext.Model(&Prize{}).Where("act_id = ?", act.Id).Count(&n).Error)
			assert.EqualValues(t, 1, n, "从表也不许动")

			// 失败也要留痕:「有人正在试图删掉一份挂着参与记录的草稿」正是最需要查到的形状。
			var audits int64
			require.NoError(t, ext.Model(&qymodel.AuditLog{}).
				Where("action = ? AND result = ?", "lottery.activity.delete", qymodel.ResultFail).
				Count(&audits).Error)
			assert.Greater(t, audits, int64(0))
		})
	}
}

// 绑了期次系列的双色球草稿删得掉 —— 哪怕系列还开着、而且已经开出过后续期次。
//
// 这是"草稿删不了"里最隐蔽的一半:第六道闸门数的是 `issue_no > act.IssueNo`,
// 而草稿的 issue_no 恒为 0,于是系列里任何一个已发布期次都让它成立。
// 表现是一份从没取走过一分钱池子的草稿,报出"它的滚存仍被后续期次依赖"。
func TestDeleteBallDraftBoundToLiveSeries(t *testing.T) {
	ext := newDraftEnv(t)
	series := &Series{
		SeriesNo: newSeriesNo(), Title: "还开着的系列", Status: SeriesOpen,
		RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1,
		IssueCapQuota: 1_000_000, IssueSeq: 2,
	}
	require.NoError(t, ext.Create(series).Error)
	// 已经开出去的两期,它们的 issue_no 都 > 0。
	for issue := 1; issue <= 2; issue++ {
		seedActivity(t, ext, func(a *Activity) {
			a.Status = StatusFinished
			a.DrawMode = DrawModeBall
			a.SeriesId = series.Id
			a.SeriesNo = series.SeriesNo
			a.IssueNo = issue
		})
	}
	draft := seedDraftActivity(t, ext, func(a *Activity) {
		a.DrawMode = DrawModeBall
		a.SeriesId = series.Id
		a.SeriesNo = series.SeriesNo
		a.IssueNo = 0
	})

	code, body := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+draft.ActNo, `{}`)
	require.Equalf(t, http.StatusOK, code,
		"一份从没取走过系列池的草稿被判成「滚存被后续期次依赖」: %s", body)

	var n int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", draft.Id).Count(&n).Error)
	assert.EqualValues(t, 0, n)
	// 系列与已发布的两期一个字节都不许动:草稿从来没在这条结转链上。
	var reloaded Series
	require.NoError(t, ext.Where("id = ?", series.Id).Take(&reloaded).Error)
	assert.Equal(t, 2, reloaded.IssueSeq)
	require.NoError(t, ext.Model(&Activity{}).Where("series_id = ?", series.Id).Count(&n).Error)
	assert.EqualValues(t, 2, n)
}

// 草稿上挂着一条未处理的对账异常照样删得掉,而且那条异常跟着一起走。
//
// auditSpecDrift 扫的是 `kind='draw' AND spec_hash<>”`,**不看状态**,而草稿在
// 创建那一刻就有 spec_hash —— 它读活动行与读奖档行之间没有事务,一次并发的
// 草稿保存就足以让它落下一条 spec_drift。用第五道闸门去挡,等于让一条误报把
// 这份草稿永久钉死。
func TestDeleteDraftWithUnresolvedFlag(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, nil)
	require.NoError(t, ext.Create(&Flag{
		ActId: act.Id, Code: FlagSpecDrift, Resolved: false,
		CreatedAt: common.GetTimestamp(),
	}).Error)

	code, body := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, `{}`)
	require.Equalf(t, http.StatusOK, code, "删草稿失败: %s", body)

	var n int64
	require.NoError(t, ext.Model(&Flag{}).Where("act_id = ?", act.Id).Count(&n).Error)
	assert.EqualValues(t, 0, n, "异常行必须跟着走,否则留下一条指向不存在活动的孤儿")
}

// 读不出种子的草稿照样删得掉。
//
// errDeleteEvidenceBroken 的理由是"连当初公布的 commit_hash 是不是这个种子算出来的
// 都没人能验",而它的前提是**有一个公布过的 commit_hash**。草稿恒为空串,
// 没有任何一份承诺需要这个种子去佐证 —— 卡在这里换来的不是一份被保住的证据,
// 而是一份谁都清不掉的草稿。
func TestDeleteDraftWithMissingSeed(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, nil)
	require.NoError(t, ext.Where("act_id = ?", act.Id).Delete(&Seed{}).Error)

	code, body := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, `{}`)
	require.Equalf(t, http.StatusOK, code, "删草稿失败: %s", body)

	var n int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
	assert.EqualValues(t, 0, n)
}

// 反向钉子:已结束那一支读不出种子时**仍然**拒绝删除。
func TestDeleteFinishedStillRefusedWhenSeedUnreadable(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)
	require.NoError(t, ext.Where("act_id = ?", act.Id).Delete(&Seed{}).Error)

	code, body := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, deleteBody(act.ActNo))
	assert.Equal(t, http.StatusConflict, code, string(body))
	assert.Equal(t, "qy_lot_delete_evidence_broken", jsonString(t, body, "code"))

	var n int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

// 审计关掉时连草稿也不许删:删完之后那一行审计是唯一的遗物,
// 而这条规则不该因为"草稿没人见过"就打折。
func TestDeleteDraftRefusedWhenAuditDisabled(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, nil)

	off := false
	prev := qyConfig.Load()
	next := *prev
	next.Audit = config.Audit{Enabled: &off}
	qyConfig.Store(&next)
	t.Cleanup(func() { qyConfig.Store(prev) })

	code, body := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, `{}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_delete_audit_off", jsonString(t, body, "code"))

	var n int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

// 一份在"读出来"与"拿到锁"之间被发布掉的草稿必须在锁内停住。
//
// 没有这一条,它会带着草稿口径的宽松闸门被删掉 —— 而那时用户的参与费已经
// 真的扣走了。purgeActivityRows 最后那条 DELETE 的 WHERE 也跟着状态走,
// 所以这里同时钉住了"状态对不上就整体回滚"。
func TestPurgeRefusesWhenStatusMovedUnderneath(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, nil)

	stale := *act // 调用方手里那一份仍然写着 draft
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Update("status", StatusPublished).Error)

	err := ext.Transaction(func(tx *gorm.DB) error { return purgeActivityRows(tx, &stale) })
	require.ErrorIs(t, err, errStatusConflict)

	for _, tc := range []struct {
		label string
		model any
	}{{"奖档", &Prize{}}, {"选项", &Option{}}, {"事件流", &Event{}}, {"种子", &Seed{}}} {
		var n int64
		require.NoError(t, ext.Model(tc.model).Where("act_id = ?", act.Id).Count(&n).Error)
		assert.EqualValuesf(t, 1, n, "%s 没有跟着回滚", tc.label)
	}
}

// TestCancelRefusesDraft 守「草稿从没对外公布过」这条不变式。
//
// 取消曾经是草稿唯一的处置路径(那时既改不了也删不了),于是 cancel 的状态 CAS
// 名单里一直列着 StatusDraft。改与删都做出来之后,它留在草稿上只剩伤害 ——
// 一份从没对任何人公布过的活动被 cancel 之后 status=finished / outcome=cancelled:
//
//	① 永久出现在用户端大厅的「已结束」里(大厅口径是 status <> 'draft',
//	   状态一旦离开 draft 就再没有第二道判据);
//	② 匿名证据链开始下发它的 rules_text、spec_hash 与**随机种子**
//	   (seedShouldBeRevealed 只看 settling/finished),而 commit_hash 恒为空串
//	   —— 一份"公开了种子、却从来没有过任何承诺"的公开记录,自带的
//	   lottery-verify.py 当场判 FAIL;
//	③ 它从此不再是草稿,零仪式的草稿删除对它失效。
//
// 而换来的止损是零:草稿上不可能有参与(报名的原子 UPDATE 带着
// status='published')、不可能有出款、没有任何要退的钱。
//
// 判据分三层:HTTP code、库里一个字节没动、审计留下一条 fail。
func TestCancelRefusesDraft(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, nil)
	before := activityRow(t, ext, act.ActNo)

	code, body := callJSON(t, draftRouter(), http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/cancel",
		`{"reason":"参与费写错了"}`)
	require.Equal(t, http.StatusConflict, code, string(body))
	assert.Contains(t, string(body), "qy_lot_cancel_draft",
		"错误码必须与「状态已变化,请刷新后重试」分开:那句话在说再试一次,"+
			"而这里的真相是这个动作对草稿根本不该存在")

	after := activityRow(t, ext, act.ActNo)
	assert.Equal(t, before, after, "被拒的取消不得改动活动行的任何一列")
	assert.Equal(t, StatusDraft, after["status"])
	assert.Equal(t, OutcomeNone, after["outcome"])

	var fails int64
	require.NoError(t, ext.Model(&qymodel.AuditLog{}).
		Where("action = ? AND result = ? AND trace_no = ?",
			"lottery.activity.cancel", qymodel.ResultFail, act.ActNo).
		Count(&fails).Error)
	assert.EqualValues(t, 1, fails, "被拒的管理动作也要留痕")

	// 被拒之后草稿仍然是草稿 —— 零仪式删除对它照常有效,那才是运营真正要的动作。
	delCode, delBody := callJSON(t, draftRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, `{}`)
	require.Equalf(t, http.StatusOK, delCode, "拒了取消之后必须还能直接删掉: %s", delBody)
}

// TestCancelStillWorksOnPublished 是上面那条的对照组。
//
// 少了它,「草稿被拒」可能只是因为取消整个坏掉了。取消对**已发布**的活动
// 必须照常工作 —— 那是它唯一真正的用途:止损并全额退款。
func TestCancelStillWorksOnPublished(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, func(a *Activity) {
		a.Status = StatusPublished
		a.CommitHash = "c0mm1t"
		a.PublishedAt = common.GetTimestamp()
	})

	code, body := callJSON(t, draftRouter(), http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/cancel",
		`{"reason":"上游价格变了,本场作废"}`)
	require.Equalf(t, http.StatusOK, code, "已发布的活动必须还能整场取消: %s", body)

	after := activityRow(t, ext, act.ActNo)
	assert.Equal(t, StatusSettling, after["status"])
	assert.Equal(t, OutcomeCancelled, after["outcome"])
}

// TestProofNeverInventsABlankWinner 守公开证据链的诚实性。
//
// 一条 payout 指不到任何参与明细时,fillProofOutcome 此前拿 map 零值兜底,于是
// 它会在**公开**的证据链里变成一位 entry_no=” / user_ref=” 的中奖者,照常带着
// 金额和 tier。备份库里真的有这么一行(手工插进去的探针,payout_no
// 'LP-XACT-PROBE-0001',指向不存在的 entry_id=987654321,挂在一场真实的历史
// 活动上),它让那一场的公开 proof 对任何第三方都判 FAIL:按种子重算得到 4 位,
// 公布的是 5 位,多出来的那位没有 entry_no,谁都无法核对。
//
// 三条判据:
//
//	① 不许把它写进 winners —— 一位空 entry_no 的中奖者证明不了任何事,
//	   却会让所有诚实开出的活动在验证脚本里与真正的漏发无法区分;
//	② 仍然要进 payouts —— 那是"平台实际付了哪些钱"的如实记账,不能瞒;
//	③ 必须落一条对账异常 —— 这正是"平台确实有问题"的那一刻,而此前它最安静。
func TestProofNeverInventsABlankWinner(t *testing.T) {
	ext := newDraftEnv(t)
	act := seedDraftActivity(t, ext, func(a *Activity) {
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.CommitHash = "c0mm1t"
		a.PublishedAt = common.GetTimestamp()
		a.SettledAt = common.GetTimestamp()
	})
	// 一条正常出款(指得到条目)与一条孤儿出款,共处一场。
	entry := &Entry{
		ActId: act.Id, Seq: 1, EntryNo: "LE-REAL-1", UserId: 7, UserRef: "ref7",
		Amount: 1000, Status: EntrySuccess, CreatedAt: common.GetTimestamp(),
	}
	require.NoError(t, ext.Create(entry).Error)
	require.NoError(t, ext.Create(&Payout{
		ActId: act.Id, PayoutNo: "LP-REAL-1", EntryId: entry.Id, UserId: 7,
		Kind: PayoutPrize, Tier: 1, DrawPos: 1, AmountQuota: 1000,
		Status: PayoutPaid, CreatedAt: common.GetTimestamp(),
	}).Error)
	require.NoError(t, ext.Create(&Payout{
		ActId: act.Id, PayoutNo: "LP-ORPHAN-1", EntryId: 987654321, UserId: 9,
		Kind: PayoutPrize, Tier: 1, DrawPos: 2, AmountQuota: 3333,
		Status: PayoutPaid, CreatedAt: common.GetTimestamp(),
	}).Error)

	var doc proofDocument
	require.NoError(t, fillProofOutcome(context.Background(), ext, act, &doc))

	require.Len(t, doc.Winners, 1,
		"孤儿出款绝不能变成一位中奖者:它没有 entry_no,谁都无法核对,"+
			"而它的存在会让整份证据链在第三方那里判 FAIL")
	assert.Equal(t, "LE-REAL-1", doc.Winners[0].EntryNo)

	require.Len(t, doc.Payouts, 2, "实际付出去的钱必须如实记账,不能瞒")
	assert.Equal(t, "", doc.Payouts[1].EntryNo, "对不上账这件事本身就是它的证据")
	assert.EqualValues(t, 3333, doc.Payouts[1].Amount)

	var flags []Flag
	require.NoError(t, ext.Where("act_id = ? AND code = ?", act.Id, FlagPayoutOrphan).
		Find(&flags).Error)
	require.Len(t, flags, 1, "这一刻平台确实有问题,必须有人被告知")
	assert.Contains(t, flags[0].Detail, "LP-ORPHAN-1")
}
