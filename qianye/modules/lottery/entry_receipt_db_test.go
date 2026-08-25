package lottery

// entry_receipt_db_test.go —— 「买到手的票必须出现在回执里」这条契约。
//
// 一次买多注把两个新数摆进了响应:accepted(收下几注)与 total_quota(这次扣了
// 多少钱)。这两个数一旦与主库余额对不上,用户看到的就是"我付了三注的钱,
// 界面说只买成两注",而客服照着 failed_code 会按"没扣钱"处置一笔已扣的钱。
//
// 这里的两条用例各钉住一条把它们说错的路径:
//
//	调用方预算在钱动完之后失效  —— 回执不许因此丢掉一张已经成交的票
//	客户端自选的 crid 撞进派生位 —— 回执不许因此混进上一次提交买的票

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// receiptEnv 造一套"真扣钱"的环境:扩展库 + 主库 + 一场已发布的双色球。
func receiptEnv(t *testing.T, startQuota int) (*gorm.DB, *gorm.DB, *Activity) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeQuota:          5_000_000,
		MaxTotalPrizeQuota:     5_000_000,
		MaxActiveActivities:    16,
		MaxPrizeTiers:          8,
		MaxTotalEntriesHard:    1_000,
	})
	main := newBallMainDB(t, startQuota)
	return ext, main, seedBallActivity(t, ext, nil)
}

// buyerQuota 读主库余额。期望值一律独立算出来,不从响应里抄。
func buyerQuota(t *testing.T, main *gorm.DB) int {
	t.Helper()
	var u model.User
	require.NoError(t, main.Where("id = ?", ballE2EUserId).Take(&u).Error)
	return u.Quota
}

// 调用方预算在**钱已经动过之后**耗尽时,回执必须照样拿得到。
//
// ChargeEntry 的最后一步是回读那条参与明细(entry_no + seq + chain_hash 是用户
// 事后举证的全部凭据)。它原本用的是调用方那个会过期的 ctx,而它排在整条链路
// 最长的一段(主库事务 + 收尾)之后 —— 预算落在这里时,一张**已经买到手**的票
// 会被当成"这一注处理失败"抛出去:多注提交的 accepted / total_quota 因此少报
// 一笔真实扣款,而 failed_code 说的是"处理失败,请稍后重试"。
//
// 演示库上实测到过三次,失败原文逐字是「回读参与明细: context deadline exceeded」。
//
// 这里不靠计时去撞那个窗口(计时用例本仓明令禁止),而是在扩展库的 UPDATE
// 回调上挂一颗钩子:第一条打在 qy_lot_entry 上的 UPDATE 就是 markEntrySuccess,
// 它跑在主库扣款提交**之后**,且自己走的是 WithoutCancel 的收尾 ctx。在那一刻
// 掐掉调用方的预算,窗口就被确定性地打开了。
func TestEntryReceiptSurvivesACallerBudgetThatDiesAfterTheMoneyMoved(t *testing.T) {
	const startQuota = 100_000
	ext, main, act := receiptEnv(t, startQuota)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const hook = "qy_test_kill_caller_budget"
	require.NoError(t, ext.Callback().Update().After("gorm:update").
		Register(hook, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (Entry{}).TableName() {
				cancel()
			}
		}))
	t.Cleanup(func() { _ = ext.Callback().Update().Remove(hook) })

	entry, err := ChargeEntry(ctx, EntryInput{
		ActNo:           act.ActNo,
		UserId:          ballE2EUserId,
		ClientRequestId: "budget-1",
		Pick:            "01,02,03|01",
		ClientIp:        "127.0.0.1",
		UserAgent:       "go-test",
	})
	require.Error(t, ctx.Err(),
		"钩子没被触发,这条用例什么都没验到 —— 先修用例再看结论")
	require.NoError(t, err,
		"钱已经扣了、票已经进链,回执必须照样拿得到;报成失败等于把一张买到手的票说没买成")
	require.NotNil(t, entry)
	assert.Equal(t, EntrySuccess, entry.Status)
	assert.NotEmpty(t, entry.EntryNo, "entry_no 是用户事后举证的凭据,不能是空的")
	assert.NotEmpty(t, entry.ChainHash)
	assert.EqualValues(t, 1, entry.Seq)

	// 钱确实动了 —— 也就是说上面那句 NoError 不是因为这一注根本没成交。
	assert.EqualValues(t, startQuota-int(act.StakeQuota), buyerQuota(t, main),
		"主库余额必须正好少一注参与费")

	var stored Entry
	require.NoError(t, ext.Where("entry_no = ?", entry.EntryNo).Take(&stored).Error)
	assert.Equal(t, EntrySuccess, stored.Status, "库里那张票也必须是成交状态")
}

// 客户端自选的 client_request_id 不许撞进服务端的派生位。
//
// 多注提交给第 i(i ≥ 1)注派生的幂等键是 `<crid>#<i>`。客户端也用 `#` 时这个
// 映射就不再是单射:先用 `X#1` 买一注,再用 `X` 买两注,第二批第 1 注派生出的
// 正是 `X#1` —— 于是它幂等命中**上一次提交**买下的那张票,回执里混进一张不属于
// 这次提交的票,total_quota 也跟着多报一注。
//
// 堵法是把 `#` 挡在派生之前(handleCreateEntry 与长度同一道闸)。挡一个字符比
// 给键空间做转义便宜得多:转义要么撑破 idem_key 的列宽,要么改掉单注那一份的
// 取值,而后者会让旧客户端的重试不再命中原单。
func TestClientRequestIdCannotReachTheDerivedBatchKeyspace(t *testing.T) {
	const startQuota = 100_000
	ext, main, act := receiptEnv(t, startQuota)
	r := ballE2ERouter()
	path := "/lottery/activities/" + act.ActNo + "/entries"

	code, body := callJSON(t, r, http.MethodPost, path,
		entryBody(t, "col#1", []string{"01,02,03|01"}))
	require.Equalf(t, http.StatusBadRequest, code,
		"以 #1 结尾的 crid 必须在派生之前就被挡下: %s", body)
	assert.Equal(t, "qy_lot_bad_request_id", errorCode(t, body))

	var landed int64
	require.NoError(t, ext.Model(&Entry{}).Where("act_id = ?", act.Id).Count(&landed).Error)
	assert.Zero(t, landed, "被拒的提交不许落下任何一张票")
	assert.Equal(t, startQuota, buyerQuota(t, main), "被拒的提交不许扣一分钱")

	// 同一个用户随后用 `col` 买两注:这两注都必须是**这一次**买的。
	code, body = callJSON(t, r, http.MethodPost, path,
		entryBody(t, "col", []string{"04,05,06|02", "01,02,03|01"}))
	require.Equalf(t, http.StatusOK, code, "多注提交失败: %s", body)
	batch := decodeEntryBatch(t, body)
	require.Len(t, batch.Entries, 2)
	assert.Equal(t, 2, batch.Accepted)
	assert.EqualValues(t, 2*act.StakeQuota, batch.TotalQuota)
	assert.Equal(t, 1, batch.Entries[0].Seq)
	assert.Equal(t, 2, batch.Entries[1].Seq,
		"两份回执必须是本次新买的两张票,不能有一张来自上一次提交")
	assert.EqualValues(t, startQuota-int(2*act.StakeQuota), buyerQuota(t, main),
		"扣的钱必须与回执上写的总额逐字相等")
}

// ─────────────────────── 对账:读偏斜不是篡改 ───────────────────────

// 活动还在收报名时,对账不许把读偏斜报成篡改。
//
// runReconcile 与 auditFinishedChains 都是先一条 Find 把整批活动行读进内存、
// 再逐场对账,而各条聚合是各自独立的语句、各拿各的读视图。中间落定的每一条
// 参与都会让 pool_mismatch / count_drift / chain_drift 三条同时"漂移",漂移量
// 恰好等于这期间新落的条目数。演示库里抓到过一次:同一秒落了 9 条参与,三条
// flag 一起亮,而活动跑完之后同一行完全自洽。
//
// 这三个 code 是本模块**唯一**的事后篡改出口,落表之后不会自愈、只能人工关闭,
// 还会一直卡住这场活动的删除 —— 假阳会把真阳淹掉。
func TestMaterializedInvariantsDoNotFlagWhileEntriesAreStillLanding(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusPublished
		a.ChainHead = ""
	})
	const amount = int64(1000)
	const landed = 3
	for seq := 1; seq <= landed; seq++ {
		require.NoError(t, gdb.Create(&Entry{
			EntryNo:   newEntryNo(),
			ActId:     act.Id,
			IdemKey:   fmt.Sprintf("%s:k-%d", act.ActNo, seq),
			UserId:    900 + seq,
			Seq:       seq,
			Amount:    amount,
			Status:    EntrySuccess,
			ChainHash: fmt.Sprintf("hash-%d", seq),
			CreatedAt: common.GetTimestamp(),
		}).Error)
	}
	// 活动行与这三条参与完全自洽 —— 库里此刻没有任何漂移。
	require.NoError(t, gdb.Model(&Activity{}).Where("id = ?", act.Id).
		Updates(map[string]any{
			"entry_seq":    landed,
			"active_count": landed,
			"pool_quota":   landed * amount,
			"chain_head":   fmt.Sprintf("hash-%d", landed),
		}).Error)

	// 读偏斜:活动行被读进内存的那一刻只有两条参与,第三条是在聚合跑之前
	// 落定的。三条不变量会同时对不上,而一条都不是篡改。
	stale := *loadAct(t, gdb, act.Id)
	stale.EntrySeq = landed - 1
	stale.ActiveCount = landed - 1
	stale.PoolQuota = (landed - 1) * amount
	stale.ChainHead = fmt.Sprintf("hash-%d", landed-1)

	checkMaterializedInvariants(context.Background(), gdb, &stale)

	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ?", act.Id).Find(&flags).Error)
	assert.Emptyf(t, flags,
		"活动还在收报名,读偏斜不是篡改 —— 假阳会把真阳淹掉,实际落了: %+v", flags)

	// 反面:真的篡改照样查得出来。直接删掉一条参与,活动行的计数器一个字节
	// 都不会动,所以"再读一次"仍然与快照相等,这条修复不会让判据失灵。
	require.NoError(t, gdb.Where("act_id = ? AND seq = ?", act.Id, landed).
		Delete(&Entry{}).Error)
	checkMaterializedInvariants(context.Background(), gdb, loadAct(t, gdb, act.Id))

	require.NoError(t, gdb.Where("act_id = ?", act.Id).Find(&flags).Error)
	codes := make(map[string]bool, len(flags))
	for _, f := range flags {
		codes[f.Code] = true
	}
	assert.Truef(t, codes[FlagPoolMismatch], "删掉一条参与必须报奖池对不上: %+v", flags)
	assert.Truef(t, codes[FlagCountDrift], "删掉一条参与必须报有效条目对不上: %+v", flags)
	assert.Truef(t, codes[FlagChainDrift], "删掉一条参与必须报链断了: %+v", flags)
}
