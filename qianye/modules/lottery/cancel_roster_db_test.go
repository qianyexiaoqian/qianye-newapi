package lottery

// cancel_roster_db_test.go —— 整场取消必须与封盘一样冻结名单快照。
//
// 缺陷原型:handleCancelActivity 从 published 直接跳到 settling,补做了封盘的
// pending→excluded 清扫,却漏了封盘的第三件事 —— 写 roster_hash / roster_count。
// 于是活动行上的名单快照永远是空串,而证据链下发的条目仍然完整:任何第三方
// (含本仓自带的 qianye/docs/lottery-verify.py)按公开条目重算出的都是一个
// 非空哈希,第 3 步当场 FAIL 并中止,连"本应中奖名单"那段用来判断
// "管理员是不是看了结果才决定不开"的材料都算不出来。
//
// 一个参与者都没有的场次同样会 FAIL:空名单的哈希是
// H(域, act_no, commit_hash, "0", "") 而不是空串,所以这里两种规模都要覆盖。

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func cancelRosterRouter() *gin.Engine {
	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EAdminId); h(c) }
	}
	user := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", ballE2EUserId); h(c) }
	}
	r.POST("/admin/lottery/activities", admin(handleCreateActivity))
	r.POST("/admin/lottery/activities/:act_no/publish", admin(handlePublishActivity))
	r.POST("/admin/lottery/activities/:act_no/cancel", admin(handleCancelActivity))
	r.POST("/lottery/activities/:act_no/entries", user(handleCreateEntry))
	return r
}

func TestCancelFreezesRosterLikeLock(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries int
	}{
		{"有人参加", 1},
		{"一个人都没有", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ext := newPayoutEnv(t, config.Lottery{
				Enabled: true, PayoutMaxAttempts: 8,
				EntryCloseGraceSeconds: 0, RevealDelaySeconds: 60,
				MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
				MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
				MaxTotalEntriesHard: 1_000,
			})
			require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
			newBallMainDB(t, 100_000)
			r := cancelRosterRouter()

			now := common.GetTimestamp()
			create := `{"kind":"draw","title":"取消场","stake_quota":1000,
				"open_at":` + strconv.FormatInt(now-60, 10) + `,
				"close_at":` + strconv.FormatInt(now+3600, 10) + `,
				"draw_at":` + strconv.FormatInt(now+7200, 10) + `,
				"settle_deadline":` + strconv.FormatInt(now+86400, 10) + `,
				"prizes":[{"tier":1,"name":"头奖","amount_quota":2000,"count":1}]}`
			code, body := callJSON(t, r, http.MethodPost, "/admin/lottery/activities", create)
			require.Equalf(t, http.StatusOK, code, "建活动失败: %s", body)
			actNo := jsonString(t, body, "data", "act_no")
			code, body = callJSON(t, r, http.MethodPost,
				"/admin/lottery/activities/"+actNo+"/publish", `{}`)
			require.Equalf(t, http.StatusOK, code, "发布失败: %s", body)

			for i := 0; i < tc.entries; i++ {
				code, body = callJSON(t, r, http.MethodPost,
					"/lottery/activities/"+actNo+"/entries",
					`{"client_request_id":"cancel-roster-`+strconv.Itoa(i)+`"}`)
				require.Equalf(t, http.StatusOK, code, "报名失败: %s", body)
			}

			code, body = callJSON(t, r, http.MethodPost,
				"/admin/lottery/activities/"+actNo+"/cancel", `{"reason":"临时下架"}`)
			require.Equalf(t, http.StatusOK, code, "取消失败: %s", body)

			var act Activity
			require.NoError(t, ext.Where("act_no = ?", actNo).Take(&act).Error)
			require.Equal(t, OutcomeCancelled, act.Outcome)

			// 第三方拿到 proof 之后做的就是这一步:按公开条目重算名单哈希。
			roster, err := loadRoster(context.Background(), ext, act.Id)
			require.NoError(t, err)
			want, count := RosterHashFor(act.Algo, act.ActNo, act.CommitHash, rosterLines(roster))
			assert.Equal(t, want, act.RosterHash,
				"取消同样要冻结名单快照,否则每一场被取消的活动都会被自家验证脚本判 FAIL")
			assert.Equal(t, count, act.RosterCount)
			assert.NotEmpty(t, act.RosterHash, "空名单的哈希也是一个真实哈希,不是空串")
		})
	}
}

// 已经封过盘的活动被取消时,绝不能覆盖那份可能早已被人抓走的快照。
func TestCancelKeepsAlreadyFrozenRoster(t *testing.T) {
	gdb := newFundTestDB(t)
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusLocked
		a.LockedAt = now - 10
		a.RosterHash = "封盘时公开的那一份"
		a.RosterCount = 3
	})

	err := gdb.Transaction(func(tx *gorm.DB) error {
		_, _, err := freezeRosterOnCancel(context.Background(), tx, act, now)
		return err
	})
	require.NoError(t, err)

	var got Activity
	require.NoError(t, gdb.Where("id = ?", act.Id).Take(&got).Error)
	assert.Equal(t, "封盘时公开的那一份", got.RosterHash)
	assert.Equal(t, 3, got.RosterCount)
}
