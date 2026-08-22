package lottery

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

// rows_affected_db_test.go —— 把 MySQL 的「改变行数」口径搬进测试里。
//
// # 为什么必须有这一份
//
// 本模块有几十处 `if res.RowsAffected != 1 { return errXxx }` 形状的 CAS。
// 它们在 SQLite 上永远成立,在 MySQL 上却不一定:go-sql-driver 默认回报的是
// **改变行数**而不是匹配行数,一条 SET 的每一列都恰好等于原值的 UPDATE 回 0 行。
//
// 后果是一整类**只在生产库出现、测试永远绿**的缺陷:
//
//   - 删除活动的状态闸门只写 updated_at,同一秒内有过任何一次管理端写
//     (换封面、下架、上一次失败后立刻重试)就回 409「活动状态已变化」;
//   - 换封面的 CAS 的 WHERE 与 SET 是同三列,一次完全相同的重发(双击、
//     客户端重试、网关重投)回 409「封面刚刚被改过」,而没有第二个人改过;
//   - 保存一份没改动过的草稿同样回 409。
//
// 三处的共同点是:**失败方向安全,文案却指向一个不存在的并发冲突**,
// 而且用普通用例抓不到。所以这里不写第二套断言,而是把口径本身改掉 ——
// 让被测代码在测试里也面对 MySQL 的那份 RowsAffected。
//
// 实现只用公开的 GORM 回调:UPDATE 之前抓一次全表快照,UPDATE 之后再抓一次,
// 把 RowsAffected 改写成"真的变了的行数"。测试库里每张表只有个位数行,
// 没有随机、没有 sleep、没有计时,结果完全确定。

// useMySQLChangedRowsSemantics 让 table 上的 UPDATE 按 MySQL 默认口径回报行数。
//
// 只对指定的一张表生效:全表快照的代价随行数线性增长,而每一组用例真正关心的
// 只有一张表;把范围放宽只会让无关的 seed 语句一起变慢,并不多守住什么。
//
// # ignored 是干什么的
//
// GORM 认得 UpdatedAt 这个字段名,会给**每一条** Updates 自动补上
// `updated_at = <当前秒>`。于是线上那个缺陷是有窗口的:两次请求落在同一秒
// 才会一列都没改到,秒针一跳就自己好了。把 "updated_at" 传进 ignored 等于
// 把这个窗口固定住 —— 否则用例的成败取决于两次 ServeHTTP 之间秒针有没有跳,
// 那是一条会偶发假绿的用例,比没有还糟。
func useMySQLChangedRowsSemantics(t *testing.T, gdb *gorm.DB, table string, ignored ...string) {
	t.Helper()
	skip := make(map[string]bool, len(ignored))
	for _, c := range ignored {
		skip[c] = true
	}
	snapshot := func(tx *gorm.DB) map[string]string {
		var rows []map[string]any
		if err := tx.Session(&gorm.Session{NewDB: true}).Table(table).Find(&rows).Error; err != nil {
			return nil
		}
		out := make(map[string]string, len(rows))
		for _, r := range rows {
			keys := make([]string, 0, len(r))
			for k := range r {
				if !skip[k] {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			var b strings.Builder
			for _, k := range keys {
				fmt.Fprintf(&b, "%s=%v;", k, r[k])
			}
			out[fmt.Sprint(r["id"])] = b.String()
		}
		return out
	}

	const snapKey = "qytest:rows_affected_snapshot"
	require.NoError(t, gdb.Callback().Update().Before("gorm:update").
		Register("qytest:changed_rows_before", func(tx *gorm.DB) {
			if tx.Statement.Table != table {
				return
			}
			tx.InstanceSet(snapKey, snapshot(tx))
		}))
	require.NoError(t, gdb.Callback().Update().After("gorm:update").
		Register("qytest:changed_rows_after", func(tx *gorm.DB) {
			if tx.Statement.Table != table {
				return
			}
			v, ok := tx.InstanceGet(snapKey)
			if !ok {
				return
			}
			before, _ := v.(map[string]string)
			var changed int64
			for id, after := range snapshot(tx) {
				if before[id] != after {
					changed++
				}
			}
			tx.RowsAffected = changed
		}))
	t.Cleanup(func() {
		_ = gdb.Callback().Update().Remove("qytest:changed_rows_before")
		_ = gdb.Callback().Update().Remove("qytest:changed_rows_after")
	})
}

// TestMySQLChangedRowsHarnessIsRealisticallyStrict 是这套替身自己的钉子。
//
// 没有它,一个"什么都不改写"的坏替身会让下面两组用例白白全绿 —— 那正是
// 它们要防的失败形状本身。
func TestMySQLChangedRowsHarnessIsRealisticallyStrict(t *testing.T) {
	gdb := newRetireEnv(t)
	useMySQLChangedRowsSemantics(t, gdb, "qy_lot_activity", "updated_at")
	act := seedActivity(t, gdb, func(a *Activity) { a.Title = "原标题" })

	res := gdb.Model(&Activity{}).Where("id = ?", act.Id).Update("title", "原标题")
	require.NoError(t, res.Error)
	assert.Zero(t, res.RowsAffected, "值没变,MySQL 回 0 行")

	res = gdb.Model(&Activity{}).Where("id = ?", act.Id).Update("title", "新标题")
	require.NoError(t, res.Error)
	assert.Equal(t, int64(1), res.RowsAffected, "值变了才算 1 行")
}

// TestDeleteActivitySurvivesUnchangedUpdatedAt —— 同一秒内刚被写过的活动照样能删。
//
// 触发条件在生产上极其常见:管理员换完封面顺手点删除,两次请求落在同一秒,
// 于是删除闸门那条 `SET updated_at = now` 一列都没改到。
func TestDeleteActivitySurvivesUnchangedUpdatedAt(t *testing.T) {
	gdb := newRetireEnv(t)
	useMySQLChangedRowsSemantics(t, gdb, "qy_lot_activity", "updated_at")
	act := seedFinishedActivity(t, gdb)
	// 这一行就是全部的触发条件:updated_at 已经等于闸门要写进去的那个值。
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("updated_at", common.GetTimestamp()).Error)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/admin/lottery/activities/"+act.ActNo,
		strings.NewReader(deleteBody(act.ActNo)))
	req.Header.Set("Content-Type", "application/json")
	retireRouter().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"闸门要判的是状态,不是「这条 UPDATE 改到了几列」;回 409 指向一个不存在的并发冲突")

	var left int64
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).Count(&left).Error)
	assert.Zero(t, left)
}

// TestDeleteActivityStillRefusesWhenStatusMoved 是上一条的反向钉子:
// 放宽"改到了几行"的判据之后,**状态**这一条必须仍然拦得住。
func TestDeleteActivityStillRefusesWhenStatusMoved(t *testing.T) {
	gdb := newRetireEnv(t)
	useMySQLChangedRowsSemantics(t, gdb, "qy_lot_activity", "updated_at")
	act := seedFinishedActivity(t, gdb)
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Update("status", StatusSettling).Error)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/admin/lottery/activities/"+act.ActNo,
		strings.NewReader(deleteBody(act.ActNo)))
	req.Header.Set("Content-Type", "application/json")
	retireRouter().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
	var left int64
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).Count(&left).Error)
	assert.Equal(t, int64(1), left, "被拒绝的删除一行都不许动")
}

// TestUpdateDraftIsIdempotent —— 保存一份没改动过的草稿不是"状态已变化"。
//
// 这是同一个根因的第三处:draftUpdates 写的四十来列在一次未编辑的保存里
// 全部等于原值(双击保存、客户端重投、向导里来回翻页都会发出这一次请求),
// 于是 MySQL 回 0 行,而管理员收到的是「活动状态已变化,请刷新后重试」——
// 他刷新之后看到的还是那份草稿,再点一次还是同一句话。
func TestUpdateDraftIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
		MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
		MaxTotalEntriesHard: 1_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	useMySQLChangedRowsSemantics(t, ext, "qy_lot_activity", "updated_at")

	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EAdminId); h(c) }
	}
	r.POST("/admin/lottery/activities", admin(handleCreateActivity))
	r.PUT("/admin/lottery/activities/:act_no", admin(handleUpdateActivity))

	now := common.GetTimestamp()
	body := `{"kind":"draw","title":"草稿场","stake_quota":1000,
		"open_at":` + strconv.FormatInt(now+60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"prizes":[{"tier":1,"name":"头奖","amount_quota":2000,"count":1}]}`
	code, raw := callJSON(t, r, http.MethodPost, "/admin/lottery/activities", body)
	require.Equalf(t, http.StatusOK, code, "建草稿失败: %s", raw)
	actNo := jsonString(t, raw, "data", "act_no")

	// 一字未改地再提交一次 —— 活动行的每一列都等于原值。
	code, raw = callJSON(t, r, http.MethodPut, "/admin/lottery/activities/"+actNo, body)
	assert.Equalf(t, http.StatusOK, code, "保存一份没改动过的草稿被拒了: %s", raw)

	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	assert.Equal(t, StatusDraft, act.Status)
	assert.Equal(t, "草稿场", act.Title)
}

// TestUpdateDraftStillRefusedAfterPublish 是反向钉子:放宽"改到了几行"之后,
// 「只有草稿能改」这条(它是承诺不可篡改的唯一执行点)必须仍然成立。
func TestUpdateDraftStillRefusedAfterPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
		MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
		MaxTotalEntriesHard: 1_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	useMySQLChangedRowsSemantics(t, ext, "qy_lot_activity", "updated_at")

	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EAdminId); h(c) }
	}
	r.POST("/admin/lottery/activities", admin(handleCreateActivity))
	r.PUT("/admin/lottery/activities/:act_no", admin(handleUpdateActivity))

	now := common.GetTimestamp()
	body := `{"kind":"draw","title":"草稿场","stake_quota":1000,
		"open_at":` + strconv.FormatInt(now+60, 10) + `,
		"close_at":` + strconv.FormatInt(now+3600, 10) + `,
		"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
		"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
		"prizes":[{"tier":1,"name":"头奖","amount_quota":2000,"count":1}]}`
	code, raw := callJSON(t, r, http.MethodPost, "/admin/lottery/activities", body)
	require.Equalf(t, http.StatusOK, code, "建草稿失败: %s", raw)
	actNo := jsonString(t, raw, "data", "act_no")
	require.NoError(t, ext.Model(&Activity{}).Where("act_no = ?", actNo).
		Update("status", StatusPublished).Error)

	code, raw = callJSON(t, r, http.MethodPut, "/admin/lottery/activities/"+actNo,
		strings.Replace(body, "草稿场", "改过的标题", 1))
	assert.Equal(t, http.StatusConflict, code)
	// 不是 qy_lot_status_conflict:那句话在说"刷新一下再试",而真相是
	// "这件事从此不可能做到"。塌成前者的表现是运营反复刷新、反复重试。
	assert.Equal(t, "qy_lot_update_not_draft", jsonString(t, raw, "code"))

	var act Activity
	require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
	assert.Equal(t, "草稿场", act.Title, "发布之后的活动内容已经进了 commit_hash,一个字都不许改")
}

func coverRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/admin/lottery/activities/:act_no/cover", func(c *gin.Context) {
		c.Set("id", retireAdminId)
		handleSetActivityCover(c)
	})
	return r
}

func putCover(t *testing.T, actNo, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/lottery/activities/"+actNo+"/cover",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	coverRouter().ServeHTTP(rec, req)
	return rec
}

// TestSetActivityCoverIsIdempotent —— 一次完全相同的重发不是"撞车"。
//
// 换封面的 CAS 的 WHERE 与 SET 写的是同三列,重发时一列都没改到。把 0 行
// 直接翻译成 errCoverRaced 会让前端双击、客户端重试、网关重投都变成
// 「该活动的封面刚刚被改过,请刷新后重试」—— 一个凭空捏造的并发冲突。
func TestSetActivityCoverIsIdempotent(t *testing.T) {
	gdb := newCoverEnv(t, "")
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}))
	useMySQLChangedRowsSemantics(t, gdb, "qy_lot_activity", "updated_at")
	act := seedActivity(t, gdb, nil)

	const body = `{"cover_url":"https://cdn.example.com/hero.png"}`
	first := putCover(t, act.ActNo, body)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())

	second := putCover(t, act.ActNo, body)
	assert.Equal(t, http.StatusOK, second.Code,
		"同一个地址再发一次不该被当成并发换图")

	var row Activity
	require.NoError(t, gdb.Where("id = ?", act.Id).Take(&row).Error)
	assert.Equal(t, "https://cdn.example.com/hero.png", row.CoverUrl)
	assert.Empty(t, row.CoverRef)
}

// TestSetActivityCoverStillDetectsRealRace 是反向钉子:幂等重发放行之后,
// **真正的**并发换图必须仍然被 CAS 拦住。
//
// 线上的形状是:handler 把活动读出来时封面是 A,读到写之间另一个管理员把它
// 改成了 C,于是这一次带着 A 的 CAS 条件不再成立。用例直接把那条 CAS 语句
// 按 handler 里的原样发一次 —— 它是这条防线的全部,而被挤掉的那张图不会有
// 任何人给它打 detached_at,从此永远留在磁盘上。
func TestSetActivityCoverStillDetectsRealRace(t *testing.T) {
	gdb := newCoverEnv(t, "")
	require.NoError(t, gdb.AutoMigrate(&qymodel.AuditLog{}))
	useMySQLChangedRowsSemantics(t, gdb, "qy_lot_activity", "updated_at")
	act := seedActivity(t, gdb, func(a *Activity) {
		a.CoverUrl = "https://cdn.example.com/a.png"
	})

	// 另一个管理员抢先把它改成了 c.png。
	require.Equal(t, http.StatusOK,
		putCover(t, act.ActNo, `{"cover_url":"https://cdn.example.com/c.png"}`).Code)

	// 我们手里那份快照仍然是 a.png。
	res := gdb.Model(&Activity{}).
		Where("id = ? AND cover_url = ? AND cover_ref = ?", act.Id,
			"https://cdn.example.com/a.png", "").
		Updates(map[string]any{
			"cover_url": "https://cdn.example.com/b.png", "cover_ref": "",
			"updated_at": common.GetTimestamp(),
		})
	require.NoError(t, res.Error)
	assert.Zero(t, res.RowsAffected, "拿着过期的 cover_url 去写必须一行都命中不了")

	var row Activity
	require.NoError(t, gdb.Where("id = ?", act.Id).Take(&row).Error)
	assert.Equal(t, "https://cdn.example.com/c.png", row.CoverUrl, "先到的那一次不许被挤掉")
}
