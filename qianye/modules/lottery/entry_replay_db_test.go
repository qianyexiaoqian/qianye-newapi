package lottery

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// entry_replay_db_test.go —— 原样重放一次已成交的参与,必须拿回原来那张回执。
//
// 这是幂等键存在的**唯一**理由。它在 HTTP 层的失效形态换过两次:
//
//	改动前:entry_no 进指纹 ⇒ 两次请求必然算出不同指纹 ⇒ 409「内容不一致」。
//	把 entry_no 摘出指纹之后:指纹命中了,但收尾与回读仍拿本次现摇的 entry_no,
//	  而幂等命中分支根本不执行 LocalDetail、那个串从未落库 ⇒ record not found
//	  ⇒ HTTP 500。
//
// 两种形态对用户是同一件事:同一个弹窗里怎么重试都失败(前端把 client_request_id
// 钉在打开弹窗那一刻),于是关掉重开换一个新 crid —— 真的多投一注、真的多扣一笔。
// 所以这里断言的不是"没报 409",而是**回读确实落在原单指向的那条明细上**。

// seedSuccessEntry 落一条已成交的参与,并返回它对应的资金单。
func seedSuccessEntry(t *testing.T, gdb *gorm.DB, act *Activity, userId int, amount int64) (*Entry, *qymodel.FundOrder) {
	t.Helper()
	e := seedPendingEntry(t, gdb, act, userId, amount)
	snap := quotaSnapshot{Applied: true, Before: 90000, After: 90000 - amount}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, e.EntryNo, &snap)
	}))
	return e, &qymodel.FundOrder{
		OrderNo: e.OrderNo,
		Status:  qymodel.StatusSuccess,
		// fundingFacts 就是拿 e.EntryNo 填的 RefId,新建单时两者相等。
		RefId: e.EntryNo,
	}
}

func TestSettledEntryNoFollowsTheFundOrderNotTheFreshlyMintedNo(t *testing.T) {
	fresh := &Entry{EntryNo: "LE-fresh-never-persisted"}

	cases := []struct {
		name  string
		order *qymodel.FundOrder
		want  string
	}{
		{
			name:  "幂等命中:必须认原单上的 entry_no",
			order: &qymodel.FundOrder{RefId: "LE-original-0001"},
			want:  "LE-original-0001",
		},
		{
			name:  "新建单:RefId 本来就等于本次的 entry_no",
			order: &qymodel.FundOrder{RefId: fresh.EntryNo},
			want:  fresh.EntryNo,
		},
		{
			name: "历史单没有 RefId:回落本次的 entry_no,不能返回空串",
			// 空 RefId 若被原样返回,收尾会去 CAS 一条 entry_no='' 的行,
			// 那是一条永远命中 0 行的语句 —— 静默失败比报错更糟。
			order: &qymodel.FundOrder{RefId: ""},
			want:  fresh.EntryNo,
		},
		{
			name:  "order 为 nil:回落本次的 entry_no",
			order: nil,
			want:  fresh.EntryNo,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, settledEntryNo(tc.order, fresh))
		})
	}
}

// 幂等重放的完整收尾:回读命中原单那条明细,且不被本次的空快照污染。
func TestReplayOfASettledEntryReturnsTheOriginalReceipt(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)
	original, order := seedSuccessEntry(t, gdb, act, 4242, 1000)

	// 重放:同一个 client_request_id,同样的要素,但 entry_no 是新摇的。
	// 幂等命中时 LocalDetail 不执行,所以这个串从未落库,snap 也是零值。
	replay := &Entry{
		EntryNo: newEntryNo(),
		ActId:   act.Id,
		IdemKey: original.IdemKey,
		UserId:  original.UserId,
		Amount:  original.Amount,
	}
	require.NotEqual(t, original.EntryNo, replay.EntryNo,
		"两次请求的 entry_no 本来就不同,否则这条用例什么都没测")

	entryNo := settledEntryNo(order, replay)
	var zero quotaSnapshot
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return markEntrySuccess(tx, entryNo, &zero)
	}), "对一条已成交的参与补做收尾必须是安全的空转")

	stored, err := reloadEntry(context.Background(), gdb, entryNo)
	require.NoError(t, err,
		"原样重放回读失败 —— 用户拿到的是 500,只能换一个 crid 再投一注")
	assert.Equal(t, original.EntryNo, stored.EntryNo)
	assert.Equal(t, EntrySuccess, stored.Status)
	assert.Equal(t, original.Amount, stored.Amount)
	// 本次的零值快照绝不能覆盖首次扣费时记下的余额前后值。
	assert.Equal(t, int64(90000), stored.QuotaBefore)
	assert.Equal(t, int64(90000-1000), stored.QuotaAfter)

	// 这条活动的计数没有因为重放而二次记账。
	after := loadAct(t, gdb, act.Id)
	assert.Equal(t, 1, after.ActiveCount)
	assert.Equal(t, original.Amount, after.PoolQuota)

	// 反向对照:拿本次现摇的 entry_no 回读一定失败。没有它,上面那条
	// require.NoError 可能只是因为这个测试库里恰好什么都查得到。
	_, err = reloadEntry(context.Background(), gdb, replay.EntryNo)
	require.Error(t, err, "本次现摇的 entry_no 从未落库,回读必须失败")
}

// ChargeEntry 的收尾**必须**先把 entry_no 换成资金单指向的那一个。
//
// 这是一条源码级断言,不是行为断言 —— ChargeEntry 走包级 db.Get()(扩展库固定
// MySQL,无测试注入点),在单测里跑不起来。而上面那些行为用例只覆盖
// settledEntryNo 本身:把 ChargeEntry 里的调用换回 entry.EntryNo,它们**全部照绿**
// (实测确认过)。e01021b70 当初的测试缺口正是这个形状 —— 只断言指纹稳定,没有
// 任何用例走到幂等命中之后的回读分支 —— 所以这一条专门守住调用点。
func TestChargeEntryResolvesTheEntryNoFromTheFundOrder(t *testing.T) {
	src, err := os.ReadFile("entry.go")
	require.NoError(t, err)
	body := string(src)

	start := strings.Index(body, "func ChargeEntry(")
	require.Positive(t, start, "找不到 ChargeEntry")
	tail := body[start:]
	if end := strings.Index(tail[1:], "\nfunc "); end > 0 {
		tail = tail[:end]
	}

	assign := strings.Index(tail, "entry.EntryNo = settledEntryNo(order, entry)")
	require.Positive(t, assign,
		"ChargeEntry 必须在 Execute 之后把 entry.EntryNo 换成资金单指向的那一个,否则原样重放回 500")
	settle := strings.Index(tail, "settleGuard(")
	require.Positive(t, settle)
	reload := strings.Index(tail, "reloadEntry(")
	require.Positive(t, reload)
	assert.Less(t, assign, settle, "换值必须排在 settleGuard 之前")
	assert.Less(t, assign, reload, "换值必须排在 reloadEntry 之前")

	// releaseEntryOnFailure 是同一条链上的失败分支,同样不能认现摇的 entry_no。
	assert.Contains(t, body, "e.EntryNo = settledEntryNo(order, e)",
		"失败回滚也必须回滚资金单指向的那条明细,否则原单的预占永远留在 pending")
}

// 同一个 (act_id, code) 的第二次检出必须刷新 detail,而不是被静默丢弃。
//
// qy_lot_flag 是本模块唯一的事后篡改出口,detail 里写的是当场算出来的数字。
// 只去重不更新,运营看到的是一个早已不成立的旧值;而 auditFinishedChains 让
// finished 活动也进入持续复核,finished 是永久态,这条 flag 也就永久停在那个旧值上。
func TestUpsertFlagDedupesWithoutFreezingTheDetail(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	first := "重算奖池 11500 与物化 3000 不一致"
	changed, err := upsertFlag(gdb, act.Id, FlagPoolMismatch, first)
	require.NoError(t, err)
	assert.True(t, changed, "首次检出必须报告为「新的」,调用方据此决定要不要打日志/审计")
	var seeded Flag
	require.NoError(t, gdb.Where("act_id = ?", act.Id).Take(&seeded).Error)

	// 同一条消息重复检出:不刷屏,也不产生无谓的写。
	changed, err = upsertFlag(gdb, act.Id, FlagPoolMismatch, first)
	require.NoError(t, err)
	assert.False(t, changed,
		"逐字相同的重复检出必须报告为「不是新的」——suspendReveal 靠这个判据才不会每 15 秒往审计表追加一条一模一样的 fail 行")
	// 第二次篡改,重算值变了。
	refreshed := "重算奖池 10277 与物化 3000 不一致"
	changed, err = upsertFlag(gdb, act.Id, FlagPoolMismatch, refreshed)
	require.NoError(t, err)
	assert.True(t, changed, "detail 变了就是新情况,必须重新告警")

	var rows []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagPoolMismatch).Find(&rows).Error)
	require.Len(t, rows, 1, "去重仍然成立:同一类异常不刷屏")
	assert.Equal(t, refreshed, rows[0].Detail,
		"第二次检出算出的新数字必须落地,否则红点上写的是一个已经不成立的值")
	assert.Equal(t, seeded.CreatedAt, rows[0].CreatedAt,
		"首次检出时刻不能被后来的刷新抹掉")
	assert.False(t, rows[0].Resolved)

	// 另一类异常照旧独立成行。
	_, err = upsertFlag(gdb, act.Id, FlagChainDrift, "链尾对不上")
	require.NoError(t, err)
	var n int64
	require.NoError(t, gdb.Model(&Flag{}).Where("act_id = ?", act.Id).Count(&n).Error)
	assert.Equal(t, int64(2), n)
}

// 已处理的异常必须让位给同一类的**新**检出。
//
// 去重条件是 (act_id, code, resolved=false)。若 resolved=true 的历史行也参与
// 去重,运营关掉一条异常之后这场活动这一类就永久哑火 —— 而 finished 是永久态,
// 历史公正查询的全部内容都在那里。
func TestResolvedFlagDoesNotSuppressTheNextDetection(t *testing.T) {
	gdb := newFundTestDB(t)
	act := seedActivity(t, gdb, nil)

	_, err := upsertFlag(gdb, act.Id, FlagChainDrift, "第一次")
	require.NoError(t, err)
	require.NoError(t, gdb.Model(&Flag{}).Where("act_id = ?", act.Id).
		Updates(map[string]any{"resolved": true, "resolved_by": 1301, "resolved_at": common.GetTimestamp()}).Error)

	changed, err := upsertFlag(gdb, act.Id, FlagChainDrift, "处理完之后又出事了")
	require.NoError(t, err)
	assert.True(t, changed, "已处理之后的新检出必须重新告警")

	var rows []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagChainDrift).
		Order("id asc").Find(&rows).Error)
	require.Len(t, rows, 2, "已处理的行必须让位,否则关掉一条异常之后这一类就永久哑火")
	assert.True(t, rows[0].Resolved)
	assert.False(t, rows[1].Resolved)
	assert.Equal(t, "处理完之后又出事了", rows[1].Detail)
}
