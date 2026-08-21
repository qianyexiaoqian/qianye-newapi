package lottery

// retire_db_test.go —— 「下架」与「彻底删除」的闸门、清除范围、审计遗物与可逆性。
//
// # 为什么这一组必须真的跑一遍数据库
//
// 删除的正确性全部住在 WHERE 条件与行数上:六道闸门是六条 COUNT、清除范围是
// 十一条 DELETE 的**并集**、审计是事务里第一条写语句。mock 掉 GORM 等于把被测
// 对象换成测试自己写的假设,而这里假设一次错的代价是一场还欠着钱的活动被抹掉、
// 或者删完之后库里留下一堆用户还能查到的孤儿行。
//
// # 这里锁住的五条
//
//  1. 六道闸门逐条生效(表驱动,每一条都单独造出触发条件);
//  2. 删完之后十一张表全部归零 —— 尤其是挂在 payout_no 上而不是 act_id 上的
//     qy_lot_prize_secret_hist,它是最容易漏的那一张(漏了就是一堆无主的兑换码明文);
//  3. 审计行**删完还在**,且带着 commit_hash / seed / roster_hash 与全部资金口径;
//  4. 审计关闭时拒绝删除(删完之后审计是唯一的遗物);
//  5. 下架只遮住大厅、可逆、且**不动任何人的钱** —— 这是它与 cancel 的全部区别。

import (
	"context"
	"net/http"
	"reflect"
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

const retireAdminId = 4001

func retireRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := func(h gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) { c.Set("id", retireAdminId); h(c) }
	}
	r.DELETE("/admin/lottery/activities/:act_no", admin(handleDeleteActivity))
	r.POST("/admin/lottery/activities/:act_no/hide", admin(handleHideActivity))
	r.POST("/admin/lottery/activities/:act_no/unhide", admin(handleUnhideActivity))
	return r
}

// newRetireEnv 建一个装好扩展库句柄、配置与审计表的环境。
//
// 审计表必须一起迁移:这一组用例里"审计行删完还在"本身就是被测的不变量,
// 少了它测的就只是"删除没报错"。
func newRetireEnv(t *testing.T) *gorm.DB {
	t.Helper()
	ext := newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		MaxStakeQuota: 5_000_000, MaxTotalPrizeQuota: 5_000_000,
		MaxActiveActivities: 16, MaxPrizeTiers: 8, MaxOptions: 8,
		MaxTotalEntriesHard: 1_000,
	})
	require.NoError(t, ext.AutoMigrate(&qymodel.AuditLog{}))
	return ext
}

// seedFinishedActivity 造一场"已结束、账全清"的活动:每一张表都至少有一行,
// 而且每一行都处在允许删除的终态。各个闸门用例在它上面**只改一处**,
// 于是"这一条闸门到底拦的是什么"在用例里一眼可见。
func seedFinishedActivity(t *testing.T, gdb *gorm.DB) *Activity {
	t.Helper()
	now := common.GetTimestamp()
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.Algo = AlgoV2
		a.RosterHash = "r0ster"
		a.RosterCount = 2
		a.ChainHead = "cha1nhead"
		a.RulesHash = "rul3s"
		a.SpecHash = "5pec"
		a.PoolQuota = 2000
		a.PayoutQuota = 1500
		a.PlatformFeeQuota = 100
		a.TextGrantCount = 1
		a.EntrySeq = 2
		a.SettledAt = now
		a.RevealedAt = now - 10
	})
	require.NoError(t, gdb.Create(&Seed{
		ActId: act.Id, Seed: "5eedhex", RefSalt: "r5alt", IpSalt: "ip5alt", CreatedAt: now,
	}).Error)
	require.NoError(t, gdb.Create(&Prize{
		ActId: act.Id, Tier: 1, Name: "头奖", AmountQuota: 1500, Count: 1,
	}).Error)
	require.NoError(t, gdb.Create(&Option{ActId: act.Id, OptNo: 1, Label: "甲"}).Error)
	require.NoError(t, gdb.Create(&Event{
		ActId: act.Id, FromStatus: StatusSettling, ToStatus: StatusFinished,
		Action: ActionFinish, CreatedAt: now,
	}).Error)
	// 已解决的异常照样要被删掉:它挂在 act_id 上,留下就是一条指向不存在活动的孤儿。
	require.NoError(t, gdb.Create(&Flag{
		ActId: act.Id, Code: FlagPayoutStuck, Resolved: true, CreatedAt: now,
	}).Error)

	for i := 0; i < 2; i++ {
		entryNo := newEntryNo()
		require.NoError(t, gdb.Create(&Entry{
			EntryNo: entryNo, ActId: act.Id, IdemKey: buildIdemKey(act.ActNo, entryNo),
			Seq: i + 1, UserId: 700 + i,
			UserRef: "ref", Amount: 1000, Status: EntrySuccess,
			OrderNo: "TR-" + strconv.Itoa(i), CreatedAt: now,
		}).Error)
	}
	// 一笔已到账的额度奖 + 一笔已履行的文本奖:两个允许删除的终态各一。
	seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.EntryId = 1
		p.Status = PayoutPaid
		p.AmountQuota = 1500
		p.SettledAt = now
	})
	textPayout := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.EntryId = 2
		p.Kind = PayoutText
		p.Status = PayoutGranted
		p.AmountQuota = 0
		p.FulfilledAt = now
		p.FulfilledBy = retireAdminId
	})
	// 被顶替过的兑换码履历挂在 payout_no 上,不在 act_id 上 —— 这是清除范围里
	// 最容易漏的一张表。
	require.NoError(t, gdb.Create(&PrizeSecretHist{
		PayoutNo: textPayout.PayoutNo, Seq: 1, SupersededAt: now,
	}).Error)
	return act
}

func deleteBody(actNo string) string {
	return `{"confirm_act_no":"` + actNo + `","reason":"项目方要求清理历史记录"}`
}

// ─────────────────────────── 闸门 ───────────────────────────

// 六道硬闸门逐条生效。每一格只在"账全清的已结束活动"上改一处。
func TestDeleteActivityGates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(t *testing.T, gdb *gorm.DB, act *Activity)
		want    *bizError
	}{
		{
			name: "草稿不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Model(act).Update("status", StatusDraft).Error)
			},
			want: errDeleteNotFinished,
		},
		{
			name: "开放中不许删:用户投的钱会不知去向",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Model(act).Update("status", StatusPublished).Error)
			},
			want: errDeleteNotFinished,
		},
		{
			name: "结算中不许删:payout 计划还没执行完",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Model(act).Update("status", StatusSettling).Error)
			},
			want: errDeleteNotFinished,
		},
		{
			name: "转人工的出款没落定不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				seedPayout(t, gdb, act.Id, func(p *Payout) {
					p.EntryId = 91
					p.Status = PayoutHeld
					p.AmountQuota = 800
				})
			},
			want: errDeleteFundsOpen,
		},
		{
			name: "退款还在计划里不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				seedPayout(t, gdb, act.Id, func(p *Payout) {
					p.EntryId = 92
					p.Kind = PayoutRefund
					p.Status = PayoutPlanned
					p.AmountQuota = 1000
				})
			},
			want: errDeleteFundsOpen,
		},
		{
			name: "文本奖还没履行不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				seedPayout(t, gdb, act.Id, func(p *Payout) {
					p.EntryId = 93
					p.Kind = PayoutText
					p.Status = PayoutGranted
					p.FulfilledAt = 0
				})
			},
			want: errDeleteTextPending,
		},
		{
			name: "还有在途参与不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Create(&Entry{
					EntryNo: newEntryNo(), ActId: act.Id, Seq: 9, UserId: 777,
					Amount: 1000, Status: EntryPending, CreatedAt: common.GetTimestamp(),
				}).Error)
			},
			want: errDeleteEntryOpen,
		},
		{
			name: "封盘后才落定的参与还没收敛不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Create(&Entry{
					EntryNo: newEntryNo(), ActId: act.Id, Seq: 10, UserId: 778,
					Amount: 1000, Status: EntryExcluded, CreatedAt: common.GetTimestamp(),
				}).Error)
			},
			want: errDeleteEntryOpen,
		},
		{
			name: "对账异常没处理完不许删",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				require.NoError(t, gdb.Create(&Flag{
					ActId: act.Id, Code: FlagPoolMismatch, Resolved: false,
					CreatedAt: common.GetTimestamp(),
				}).Error)
			},
			want: errDeleteFlagOpen,
		},
		{
			name: "双色球:系列还开着,后面还会有期次靠它的滚存开局",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				s := &Series{
					SeriesNo: newSeriesNo(), Title: "系列", Status: SeriesOpen,
					RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1,
					IssueCapQuota: 1_000_000,
				}
				require.NoError(t, gdb.Create(s).Error)
				require.NoError(t, gdb.Model(act).Updates(map[string]any{
					"series_id": s.Id, "series_no": s.SeriesNo, "issue_no": 1,
					"draw_mode": DrawModeBall,
				}).Error)
			},
			want: errDeleteSeriesLive,
		},
		{
			name: "双色球:系列已关闭但还有后续期次依赖它的结转",
			arrange: func(t *testing.T, gdb *gorm.DB, act *Activity) {
				s := &Series{
					SeriesNo: newSeriesNo(), Title: "系列", Status: SeriesClosed,
					RedPool: 33, RedPick: 6, BluePool: 16, BluePick: 1,
					IssueCapQuota: 1_000_000,
				}
				require.NoError(t, gdb.Create(s).Error)
				require.NoError(t, gdb.Model(act).Updates(map[string]any{
					"series_id": s.Id, "series_no": s.SeriesNo, "issue_no": 1,
					"draw_mode": DrawModeBall,
				}).Error)
				seedActivity(t, gdb, func(a *Activity) {
					a.Status = StatusFinished
					a.SeriesId = s.Id
					a.SeriesNo = s.SeriesNo
					a.IssueNo = 2
					a.DrawMode = DrawModeBall
				})
			},
			want: errDeleteSeriesLive,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newRetireEnv(t)
			act := seedFinishedActivity(t, ext)
			tc.arrange(t, ext, act)

			// 事务外那一遍:它负责给运营一句精确的话。
			fresh := loadAct(t, ext, act.Id)
			err := checkActivityDeletable(context.Background(), ext, fresh)
			require.Error(t, err)
			be, ok := AsBizError(err)
			require.True(t, ok, "闸门必须回可展示的业务错误,而不是 500")
			assert.Equal(t, tc.want.ErrCode(), be.ErrCode())

			// 接口那一遍:真的打一次,并确认一行都没被删掉。
			code, body := callJSON(t, retireRouter(), http.MethodDelete,
				"/admin/lottery/activities/"+fresh.ActNo, deleteBody(fresh.ActNo))
			assert.Equal(t, tc.want.HTTPStatus(), code, string(body))
			assert.Equal(t, tc.want.ErrCode(), jsonString(t, body, "code"))

			var n int64
			require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
			assert.EqualValues(t, 1, n, "被闸门拦下之后活动行必须原样还在")
			require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&n).Error)
			assert.Greater(t, n, int64(0), "被闸门拦下之后参与明细必须原样还在")

			// 失败也要留痕:「有人正在试图删掉一场还欠着钱的活动」正是最需要查到的形状。
			var audits int64
			require.NoError(t, ext.Model(&qymodel.AuditLog{}).
				Where("action = ? AND result = ?", "lottery.activity.delete", qymodel.ResultFail).
				Count(&audits).Error)
			assert.Greater(t, audits, int64(0))
		})
	}
}

// 二次确认在**服务端**校验:活动号回填不上就一行都不删。
func TestDeleteActivityRequiresServerSideConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{"活动号回填错了", `{"confirm_act_no":"LA-WRONG","reason":"清理"}`, "qy_lot_delete_confirm"},
		{"活动号没填", `{"reason":"清理"}`, "qy_lot_delete_confirm"},
		{"理由没填", `{"confirm_act_no":"%s"}`, "qy_lot_bad_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext := newRetireEnv(t)
			act := seedFinishedActivity(t, ext)
			body := tc.body
			if body == `{"confirm_act_no":"%s"}` {
				body = `{"confirm_act_no":"` + act.ActNo + `"}`
			}

			code, resp := callJSON(t, retireRouter(), http.MethodDelete,
				"/admin/lottery/activities/"+act.ActNo, body)
			assert.NotEqual(t, http.StatusOK, code)
			assert.Equal(t, tc.code, jsonString(t, resp, "code"))

			var n int64
			require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
			assert.EqualValues(t, 1, n)
		})
	}
}

// 审计关掉时一律拒绝删除:删完之后审计行是唯一还能证明这一场存在过的东西。
func TestDeleteActivityRefusedWhenAuditDisabled(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)

	off := false
	prev := qyConfig.Load()
	next := *prev
	next.Audit = config.Audit{Enabled: &off}
	qyConfig.Store(&next)
	t.Cleanup(func() { qyConfig.Store(prev) })

	code, body := callJSON(t, retireRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, deleteBody(act.ActNo))
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_delete_audit_off", jsonString(t, body, "code"))

	var n int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}

// ─────────────────────────── 清除范围与遗物 ───────────────────────────

// 删干净:十一张表一起走,一行都不能留;审计行删完还在,且带着证据链指纹。
func TestDeleteActivityPurgesEveryTableAndLeavesAudit(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)
	// 同一个库里再放一场无关的活动:删除必须**只**动目标那一场。
	other := seedFinishedActivity(t, ext)

	code, body := callJSON(t, retireRouter(), http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, deleteBody(act.ActNo))
	require.Equalf(t, http.StatusOK, code, "删除失败: %s", body)

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
		assert.EqualValuesf(t, 0, n,
			"%s 还留着行 —— 留半截的表现就是用户端还能查到孤儿记录", tc.table)
	}
	// 兑换码履历挂在 payout_no 上而不是 act_id 上,是最容易漏的那一张。
	var hist int64
	require.NoError(t, ext.Model(&PrizeSecretHist{}).Count(&hist).Error)
	assert.EqualValues(t, 1, hist,
		"只该剩下另一场活动那一行:本场的兑换码明文必须跟着走")

	// 只动目标那一场。
	var others int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", other.Id).Count(&others).Error)
	assert.EqualValues(t, 1, others)
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", other.Id).Count(&others).Error)
	assert.EqualValues(t, 2, others)

	// 审计:删完还在,且装着足够复盘的东西。
	var row qymodel.AuditLog
	require.NoError(t, ext.Where("action = ? AND result = ? AND trace_no = ?",
		"lottery.activity.delete", qymodel.ResultOK, act.ActNo).Take(&row).Error)
	assert.Equal(t, retireAdminId, row.ActorUserId)
	assert.Contains(t, row.Reason, "清理历史记录")
	for _, want := range []string{
		act.ActNo, "c0mm1t", "5eedhex", "r0ster", "cha1nhead", "rul3s", "5pec",
		`"entry_success":2`, `"distinct_users":2`, `"bet_total_quota":2000`,
		`"payout_quota":1500`, `"platform_fee_quota":100`, `"paid_total_quota":1500`,
	} {
		assert.Containsf(t, row.BeforeSnap, want,
			"审计正文缺了 %s —— 删完之后它是唯一还能复盘这一场的东西", want)
	}
}

// 事务性:清除范围里任何一步失败,已经删掉的行必须整体回滚,审计也不能留下。
//
// 制造失败的方式是把活动行的状态在事务开始前改成非 finished —— 那会让
// purgeActivityRows 最后那条带状态条件的 DELETE 影响 0 行。
// 这条用例证明的是"最后一步失败,前面十张表的删除全部撤回"。
func TestPurgeActivityRowsRollsBackAsOneUnit(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)

	stale := *act
	stale.Status = StatusFinished
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
		Update("status", StatusSettling).Error)

	err := ext.Transaction(func(tx *gorm.DB) error { return purgeActivityRows(tx, &stale) })
	require.ErrorIs(t, err, errStatusConflict)

	for _, tc := range []struct {
		label string
		model any
		want  int64
	}{
		{"参与明细", &Entry{}, 2},
		{"出款", &Payout{}, 2},
		{"事件流", &Event{}, 1},
		{"奖档", &Prize{}, 1},
		{"选项", &Option{}, 1},
		{"异常", &Flag{}, 1},
		{"种子", &Seed{}, 1},
	} {
		var n int64
		require.NoError(t, ext.Model(tc.model).Where("act_id = ?", act.Id).Count(&n).Error)
		assert.EqualValuesf(t, tc.want, n, "%s 没有跟着回滚", tc.label)
	}
	var hist int64
	require.NoError(t, ext.Model(&PrizeSecretHist{}).Count(&hist).Error)
	assert.EqualValues(t, 1, hist, "兑换码履历没有跟着回滚")
}

// 清除范围必须覆盖**每一张以 act_id 为键的表**,包括以后新加的。
//
// 这条用反射而不是抄一份表名清单:抄下来的清单在被抄的当天是对的,而下一个
// 给活动加从表的人不会想到回来改它 —— 那张新表会在每一次删除之后留下一批
// 指向不存在活动的孤儿行,而所有既有用例照常绿。
//
// 它**抓不到**不以 act_id 为键的从表(例如挂在 payout_no 上的兑换码履历,
// 以及封面图那张按 ref 引用的表)。那两张各有自己的用例与说明。
func TestPurgeCoversEveryActIdKeyedTable(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)

	scanned := 0
	for _, model := range tables() {
		typ := reflect.TypeOf(model).Elem()
		if typ == reflect.TypeOf(Activity{}) {
			continue
		}
		if _, ok := typ.FieldByName("ActId"); !ok {
			continue
		}
		scanned++
		row := reflect.New(typ)
		row.Elem().FieldByName("ActId").SetInt(act.Id)
		// Take/Create 对已存在主键的行会撞唯一键,那说明 seedFinishedActivity
		// 已经给这张表铺过行 —— 那正是我们要的初始状态,忽略即可。
		_ = ext.Create(row.Interface()).Error

		var before int64
		require.NoError(t, ext.Model(model).Where("act_id = ?", act.Id).Count(&before).Error)
		require.Greaterf(t, before, int64(0), "%T 没有铺上待删的行,这一格测了个空", model)
	}
	require.Greater(t, scanned, 5, "扫到的从表太少,反射八成写错了")

	require.NoError(t, ext.Transaction(func(tx *gorm.DB) error {
		return purgeActivityRows(tx, act)
	}))

	for _, model := range tables() {
		typ := reflect.TypeOf(model).Elem()
		if typ == reflect.TypeOf(Activity{}) {
			continue
		}
		if _, ok := typ.FieldByName("ActId"); !ok {
			continue
		}
		var n int64
		require.NoError(t, ext.Model(model).Where("act_id = ?", act.Id).Count(&n).Error)
		assert.EqualValuesf(t, 0, n,
			"%T 没有被清除范围覆盖 —— 删完之后它会留下指向不存在活动的孤儿行", model)
	}
}

// 允许删除的一场,先下架再删同样走得通;而删除本身不依赖下架。
func TestDeleteWorksOnHiddenActivity(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)
	r := retireRouter()

	code, body := callJSON(t, r, http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/hide", `{"reason":"先从大厅撤下"}`)
	require.Equalf(t, http.StatusOK, code, "下架失败: %s", body)

	code, body = callJSON(t, r, http.MethodDelete,
		"/admin/lottery/activities/"+act.ActNo, deleteBody(act.ActNo))
	require.Equalf(t, http.StatusOK, code, "删除失败: %s", body)

	var n int64
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).Count(&n).Error)
	assert.EqualValues(t, 0, n)
}

// ─────────────────────────── 下架 ───────────────────────────

// 下架 = 只从大厅撤下,可逆,且**一分钱都不动**。这是它与 cancel 的全部区别。
func TestHideRemovesFromHallOnlyAndIsReversible(t *testing.T) {
	ext := newRetireEnv(t)
	act := seedFinishedActivity(t, ext)
	r := retireRouter()

	inHall := func() int64 {
		q, err := hallQuery(ext, "", "", allPlaysShown())
		require.NoError(t, err)
		var n int64
		require.NoError(t, q.Where("id = ?", act.Id).Count(&n).Error)
		return n
	}
	require.EqualValues(t, 1, inHall(), "下架之前它就在大厅里")

	code, body := callJSON(t, r, http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/hide", `{"reason":"运营决定不再展示"}`)
	require.Equalf(t, http.StatusOK, code, "下架失败: %s", body)

	assert.EqualValues(t, 0, inHall(), "下架之后不该再出现在大厅里")
	after := loadAct(t, ext, act.Id)
	assert.Greater(t, after.HiddenAt, int64(0))
	assert.Equal(t, retireAdminId, after.HiddenBy)
	assert.Equal(t, "运营决定不再展示", after.HiddenReason)

	// 与 cancel 的分水岭:状态、结局、三个资金口径一个都不许变,
	// 也不许多出任何一条退款计划。运营把"关闭"当成"取消"的代价就在这几行上。
	assert.Equal(t, StatusFinished, after.Status, "下架不是取消,状态机一步都不许走")
	assert.Equal(t, OutcomeDrawn, after.Outcome)
	assert.Equal(t, act.PoolQuota, after.PoolQuota)
	assert.Equal(t, act.PayoutQuota, after.PayoutQuota)
	assert.Equal(t, act.RefundQuota, after.RefundQuota)
	var refunds int64
	require.NoError(t, ext.Model(&Payout{}).
		Where("act_id = ? AND kind = ?", act.Id, PayoutRefund).Count(&refunds).Error)
	assert.EqualValues(t, 0, refunds, "下架绝不能像 cancel 那样登记退款")

	// 证据与参与照常可查:公正性一旦公布过就不能被运营收回。
	var entries, payouts, events int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&entries).Error)
	require.NoError(t, ext.Model(&Payout{}).Where("act_id = ?", act.Id).Count(&payouts).Error)
	require.NoError(t, ext.Model(&Event{}).Where("act_id = ?", act.Id).Count(&events).Error)
	assert.EqualValues(t, 2, entries)
	assert.EqualValues(t, 2, payouts)
	assert.EqualValues(t, 1, events)

	// 重复下架回 409,而不是静默成功。
	code, body = callJSON(t, r, http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/hide", `{"reason":"再来一次"}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_hide_already", jsonString(t, body, "code"))

	// 可逆。
	code, body = callJSON(t, r, http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/unhide", `{}`)
	require.Equalf(t, http.StatusOK, code, "上架失败: %s", body)
	assert.EqualValues(t, 1, inHall(), "重新上架之后必须回到大厅")
	assert.EqualValues(t, 0, loadAct(t, ext, act.Id).HiddenAt)

	code, body = callJSON(t, r, http.MethodPost,
		"/admin/lottery/activities/"+act.ActNo+"/unhide", `{}`)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "qy_lot_hide_not_hidden", jsonString(t, body, "code"))
}

// 只有已结束的场次能下架。下架一场进行中的活动等于一次隐蔽的提前截止:
// 没打开过它的人再也找不到入口,而手里有链接的人照常可以参与。
func TestHideRefusedBeforeFinished(t *testing.T) {
	for _, status := range []string{StatusDraft, StatusPublished, StatusLocked, StatusSettling} {
		t.Run(status, func(t *testing.T) {
			ext := newRetireEnv(t)
			act := seedFinishedActivity(t, ext)
			require.NoError(t, ext.Model(&Activity{}).Where("id = ?", act.Id).
				Update("status", status).Error)

			code, body := callJSON(t, retireRouter(), http.MethodPost,
				"/admin/lottery/activities/"+act.ActNo+"/hide", `{"reason":"试图提前藏起来"}`)
			assert.Equal(t, http.StatusConflict, code)
			assert.Equal(t, "qy_lot_hide_not_finished", jsonString(t, body, "code"))
			assert.EqualValues(t, 0, loadAct(t, ext, act.Id).HiddenAt)
		})
	}
}

// 大厅口径:草稿与已下架是并列的两条,任何一条漏掉都会让不该露面的场次露面。
func TestHallQueryExcludesDraftAndHidden(t *testing.T) {
	ext := newRetireEnv(t)
	visible := seedFinishedActivity(t, ext)
	hidden := seedFinishedActivity(t, ext)
	require.NoError(t, ext.Model(&Activity{}).Where("id = ?", hidden.Id).
		Update("hidden_at", common.GetTimestamp()).Error)
	draft := seedActivity(t, ext, func(a *Activity) { a.Status = StatusDraft })

	q, err := hallQuery(ext, "", "", allPlaysShown())
	require.NoError(t, err)
	rows := make([]Activity, 0, 4)
	require.NoError(t, q.Find(&rows).Error)

	got := make(map[int64]bool, len(rows))
	for _, a := range rows {
		got[a.Id] = true
	}
	assert.True(t, got[visible.Id])
	assert.False(t, got[hidden.Id], "下架的场次不该出现在大厅里")
	assert.False(t, got[draft.Id], "草稿永不下发")
}
