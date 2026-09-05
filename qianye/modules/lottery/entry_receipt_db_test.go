package lottery

// entry_receipt_db_test.go —— 「买到手的票必须出现在回执里」这条契约。
//
// 一次买多注把两个新数摆进了响应:accepted(收下几注)与 total_quota(这次扣了
// 多少钱)。这两个数一旦与星屑余额对不上,用户看到的就是"我付了三注的钱,
// 界面说只买成两注",而客服照着 failed_code 会按"没扣钱"处置一笔已扣的钱。
//
// 这里的两条用例各钉住一条把它们说错的路径:
//
//	调用方预算在事务中途失效    —— 钱与票要么都在、要么都不在,回执必须与之一致
//	客户端自选的 crid 撞进派生位 —— 回执不许因此混进上一次提交买的票

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// receiptEnv 造一套"真扣钱"的环境:扩展库(含星屑账本)+ 主库 + 一场已发布的双色球。
func receiptEnv(t *testing.T, startStardust int) (*gorm.DB, *Activity) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ext := newPayoutEnv(t, config.Lottery{
		Enabled:                true,
		PayoutMaxAttempts:      8,
		EntryCloseGraceSeconds: 0,
		RevealDelaySeconds:     0,
		MaxStakeStardust:       5_000_000,
		MaxTotalPrizeStardust:  5_000_000,
		MaxActiveActivities:    16,
		MaxPrizeTiers:          8,
		MaxTotalEntriesHard:    1_000,
	})
	newBallMainDB(t, startStardust)
	return ext, seedBallActivity(t, ext, nil)
}

// 调用方预算在参与事务**中途**失效时,钱与票必须同生共死,回执必须如实。
//
// 参与是一个扩展库事务:扣星屑、落票、推链要么全部提交、要么全部回滚。这条用例
// 在票的 INSERT 之后(扣款已经写进事务、还没提交)掐掉调用方的预算,然后只断言
// 一件事:**库里的钱与票一致,ChargeEntry 的回答与库里一致** —— 提交成功就必须
// 拿到回执且余额少一注,提交失败就必须一分钱没扣、一张票没有。两种落点都合法,
// 唯一不合法的形状是"钱扣了、票没有"或"报了失败、钱却扣了"。
//
// 不靠计时去撞窗口(计时用例本仓明令禁止),而是在扩展库的 CREATE 回调上挂钩子:
// 第一条打在 qy_lot_entry 上的 INSERT 就是那张票。
//
// 必须用落盘的 SQLite:预算中途失效时 database/sql 会把那条连接判成坏连接并关掉,
// 而 `:memory:` 库的生命周期就是那条连接的生命周期 —— 下一次取连接拿到的是一个空库。
func TestEntryReceiptAgreesWithTheLedgerWhenTheCallerBudgetDiesMidTransaction(t *testing.T) {
	const startStardust = 100_000
	gin.SetMode(gin.TestMode)
	ext := newFileBackedEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8,
		EntryCloseGraceSeconds: 0, RevealDelaySeconds: 0,
		MaxStakeStardust: 5_000_000, MaxTotalEntriesHard: 1_000,
	}, startStardust)
	act := seedBallActivity(t, ext, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const hook = "qy_test_kill_caller_budget"
	require.NoError(t, ext.Callback().Create().After("gorm:create").
		Register(hook, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == (Entry{}).TableName() {
				cancel()
			}
		}))
	t.Cleanup(func() { _ = ext.Callback().Create().Remove(hook) })

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

	var tickets []Entry
	require.NoError(t, ext.Where("act_id = ?", act.Id).Find(&tickets).Error)
	ledger := ledgerRowsOf(t, ext, ballE2EUserId, act.ActNo)
	balance := userStardust(t, ext)

	if err == nil {
		require.NotNil(t, entry)
		assert.Equal(t, EntrySuccess, entry.Status)
		assert.NotEmpty(t, entry.EntryNo, "entry_no 是用户事后举证的凭据,不能是空的")
		assert.NotEmpty(t, entry.ChainHash)
		assert.EqualValues(t, 1, entry.Seq)
		require.Len(t, tickets, 1, "回执说成交了,库里就必须有这张票")
		require.Len(t, ledger, 1, "票在,扣款流水就必须在")
		assert.Equal(t, ledger[0].LedgerNo, entry.OrderNo)
		assert.EqualValues(t, startStardust-act.StakeQuota, balance, "星屑余额必须正好少一注参与费")
		return
	}
	// 事务在提交前被掐断:整笔回滚,一分钱没扣、一张票没有、序号没占。
	assert.Empty(t, tickets, "报了失败就不许留下票 —— 那会是一张用户不知道自己买过的票")
	assert.Empty(t, ledger, "报了失败就不许扣钱 —— 那是一笔用户永远看不到的扣款")
	assert.EqualValues(t, startStardust, balance)
	assert.Zero(t, loadAct(t, ext, act.Id).EntrySeq, "回滚的尝试不许占序号")
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
	const startStardust = 100_000
	ext, act := receiptEnv(t, startStardust)
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
	assert.EqualValues(t, startStardust, userStardust(t, ext), "被拒的提交不许扣一分钱")

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
	assert.EqualValues(t, startStardust-2*act.StakeQuota, userStardust(t, ext),
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
