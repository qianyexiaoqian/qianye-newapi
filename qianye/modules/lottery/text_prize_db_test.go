package lottery

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// text_prize_db_test.go —— 文本奖那条腿在真实库上的三条不变量:
//
//  1. 混合奖档开奖后,planned + granted 的行数必须等于中奖位数(不能被静默丢掉);
//  2. granted 天然不在出款 worker 与收尾判定的扫描集合里(默认安全,而不是靠过滤);
//  3. 履行是 CAS 幂等的,撤销**不清空密文**。

func textEnv(t *testing.T) *gorm.DB {
	t.Helper()
	return newPayoutEnv(t, config.Lottery{
		Enabled: true, PayoutMaxAttempts: 8, MaxStakeQuota: 5_000_000,
	})
}

// 文本奖的金额恒为 0。PlanPayouts 原本的 `amount <= 0 就跳过` 会把它们
// **整批丢掉且不报错** —— 中奖者永远看不到自己的奖品,而系统零告警。
//
// 这是本轮最容易漏、后果最静默的一处,所以单独钉一条。
func TestPlanPayoutsKeepsZeroAmountTextPrizes(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, nil)

	plans := []PayoutPlan{
		{EntryId: 1, UserId: 11, Kind: PayoutPrize, Tier: 1, DrawPos: 0, Amount: 5000},
		{EntryId: 2, UserId: 12, Kind: PayoutText, Tier: 2, DrawPos: 1, Amount: 0},
		{EntryId: 3, UserId: 13, Kind: PayoutText, Tier: 2, DrawPos: 2, Amount: 0},
		// 额度腿上金额为 0 的计划仍然必须被跳过:twophase 的入口要求 amount > 0,
		// 而 0 元出款在账面上也不表达任何事实。
		{EntryId: 4, UserId: 14, Kind: PayoutPrize, Tier: 3, DrawPos: 3, Amount: 0},
	}
	require.NoError(t, gdb.Transaction(func(tx *gorm.DB) error {
		return PlanPayouts(tx, act.Id, plans)
	}))

	var rows []Payout
	require.NoError(t, gdb.Where("act_id = ?", act.Id).Order("draw_pos asc").Find(&rows).Error)
	require.Len(t, rows, 3, "两个文本奖中奖位一个都不能少;0 元的额度计划仍然要被跳过")

	assert.Equal(t, PayoutPlanned, rows[0].Status, "额度奖落 planned,交给出款 worker")
	for _, p := range rows[1:] {
		assert.Equal(t, PayoutText, p.Kind)
		assert.Equal(t, PayoutGranted, p.Status,
			"文本奖落库即终态 —— 它不动钱、不跨库,没有任何东西需要驱动")
		assert.Zero(t, p.FulfilledAt)
	}
}

// granted 的全部价值:它天然不在任何一个已有的扫描集合里。
//
// 落成 planned 的话,文本奖会**默认被出款 worker 捡走**,只能靠在
// DrivePayouts 与 finishIfDone 两处各补一个 kind 过滤来挡 —— 漏一处就是
// 一笔文本奖被当成资金单驱动。这条断言守的是"安全性来自结构而不是来自
// 某个人记得写了 if"。
func TestGrantedTextPrizeIsInvisibleToPayoutWorkerAndSettleGuard(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusSettling
		a.Outcome = OutcomeDrawn
		a.TextGrantCount = 1
	})
	seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Kind = PayoutText
		p.Status = PayoutGranted
		p.AmountQuota = 0
		p.EntryId = 77
	})

	// 出款 worker 扫的是 planned / paying / failed。
	var driveable int64
	require.NoError(t, gdb.Model(&Payout{}).
		Where("act_id = ? AND status IN ?", act.Id,
			[]string{PayoutPlanned, PayoutPaying, PayoutFailed}).
		Count(&driveable).Error)
	assert.Zero(t, driveable, "granted 绝不能出现在出款 worker 的扫描集合里")

	// 跑一轮真实的出款驱动:它一笔都不该动。
	DrivePayouts(context.Background())
	after := reloadPayout(t, gdb, seedPayoutNoOf(t, gdb, act.Id))
	assert.Equal(t, PayoutGranted, after.Status)
	assert.Zero(t, after.Attempts, "文本奖不该被尝试出款,一次都不该")

	// 收尾判定同样看不见它 —— 人工履行没有 SLA,拿它卡收尾会让 finished
	// 永远到不了,连带永久占用一个并发活动名额。
	finishIfDone(context.Background(), gdb, loadAct(t, gdb, act.Id))
	assert.Equal(t, StatusFinished, loadAct(t, gdb, act.Id).Status,
		"未履行的文本奖不应阻塞活动收尾")
}

// 反过来:文本奖的中奖位**少了一行**时,这一轮绝不收尾。
//
// 收了尾就再也没人扫这场活动,那些人的奖会永久消失且零告警 ——
// 与全额退款的覆盖度复核完全对称。
func TestFinishIsBlockedWhenTextGrantsAreMissing(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusSettling
		a.Outcome = OutcomeDrawn
		a.TextGrantCount = 2 // 开奖时算出两位,但库里只有一行
	})
	seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Kind = PayoutText
		p.Status = PayoutGranted
		p.AmountQuota = 0
	})

	finishIfDone(context.Background(), gdb, loadAct(t, gdb, act.Id))
	assert.Equal(t, StatusSettling, loadAct(t, gdb, act.Id).Status,
		"文本奖的中奖位没登记齐,这一轮不能收尾")

	// 对账必须把它喊出来,而且**只告警不自愈**:一个会自己改数的对账任务,
	// 在数据真被篡改时会顺手把证据也抹平。
	auditTextPrizes(context.Background(), gdb)
	var flags []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagTextGrantMissing).
		Find(&flags).Error)
	assert.NotEmpty(t, flags, "少一位文本奖中奖者是真事故,必须落异常标记")
}

// 文本奖的对账必须扫得到**已经 finished** 的活动。
//
// granted 不阻塞收尾,所以一场有文本奖的活动通常几秒内就走到 finished。
// 把这两条告警挂在"只扫 published/locked/settling"的那个循环里,
// 它们会一次都不响 —— 而那正是它们唯一要抓的场景。
func TestTextPrizeAuditReachesFinishedActivities(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, func(a *Activity) {
		a.Status = StatusFinished
		a.Outcome = OutcomeDrawn
		a.TextGrantCount = 1
	})
	seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Kind = PayoutText
		p.Status = PayoutGranted
		p.AmountQuota = 0
		// 两周前登记,至今没人履行。
		p.CreatedAt = common.GetTimestamp() - textPrizeStaleSeconds - 60
	})

	auditTextPrizes(context.Background(), gdb)

	var stale []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagTextPrizeStale).
		Find(&stale).Error)
	assert.NotEmpty(t, stale,
		"已结束活动上挂了两周的文本奖必须告警 —— 没有它,文本奖会静默烂掉,"+
			"而用户会以为是抽奖作弊")

	// 登记数对得上时不该误报。
	var missing []Flag
	require.NoError(t, gdb.Where("act_id = ? AND code = ?", act.Id, FlagTextGrantMissing).
		Find(&missing).Error)
	assert.Empty(t, missing)
}

// 履行是 CAS 幂等的:第二次提交不会覆盖第一次填进去的内容。
//
// 覆盖会让用户先看到 A 再看到 B,而争议时没人说得清他到底用掉了哪一个。
func TestFulfillIsIdempotentAndUnfulfillKeepsTheCiphertext(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Kind = PayoutText
		p.Status = PayoutGranted
		p.AmountQuota = 0
	})

	nonce, cipher, keyVersion, err := sealPrizeSecret("CDK-ABCD-1234", p.PayoutNo)
	require.NoError(t, err)
	now := common.GetTimestamp()

	fulfill := func(secretNonce, secretCipher []byte, kv int, note string) int64 {
		res := gdb.Model(&Payout{}).
			Where("payout_no = ? AND kind = ? AND fulfilled_at = 0", p.PayoutNo, PayoutText).
			Updates(map[string]any{
				"fulfilled_at": now, "fulfilled_by": 7, "fulfill_note": note,
				"secret_nonce": secretNonce, "secret_cipher": secretCipher,
				"secret_key_version": kv,
			})
		require.NoError(t, res.Error)
		return res.RowsAffected
	}

	require.EqualValues(t, 1, fulfill(nonce, cipher, keyVersion, "第一次"))
	require.EqualValues(t, 0, fulfill(nil, []byte("CDK-OVERWRITTEN"), 0, "第二次"),
		"已履行的行必须被 CAS 挡住,绝不覆盖")

	got := reloadPayout(t, gdb, p.PayoutNo)
	plain, err := openPrizeSecret(got.SecretNonce, got.SecretCipher, got.PayoutNo, got.SecretKeyVersion)
	require.NoError(t, err)
	assert.Equal(t, "CDK-ABCD-1234", plain)
	assert.Equal(t, "第一次", got.FulfillNote)
	assert.Equal(t, PayoutGranted, got.Status,
		"履行**不改 Status** —— Status 的语义严格保持「资金终态」")

	// 撤销:清账面,**不清密文**。用户可能已经看到并用掉了那串码,
	// 抹掉记录等于抹掉争议时唯一的证据。
	res := gdb.Model(&Payout{}).
		Where("payout_no = ? AND kind = ? AND fulfilled_at > 0", p.PayoutNo, PayoutText).
		Updates(map[string]any{"fulfilled_at": 0, "fulfilled_by": 0, "fulfill_note": ""})
	require.NoError(t, res.Error)
	require.EqualValues(t, 1, res.RowsAffected)

	after := reloadPayout(t, gdb, p.PayoutNo)
	assert.Zero(t, after.FulfilledAt)
	assert.NotEmpty(t, after.SecretCipher,
		"撤销绝不能清空密文:明文本身撤不回来,撤销只是账面纠错")
}

// 额度奖必须被这三个接口明确拒绝:它的 paid 是资金终态,永远不可撤。
func TestTextPrizeEndpointsRejectQuotaPayouts(t *testing.T) {
	gdb := textEnv(t)
	act := seedActivity(t, gdb, nil)
	p := seedPayout(t, gdb, act.Id, func(p *Payout) {
		p.Kind = PayoutPrize
		p.Status = PayoutPaid
	})

	_, err := loadTextPayout(context.Background(), p.PayoutNo)
	require.ErrorIs(t, err, errNotTextPrize,
		"对额度奖必须回 403 而不是 404 —— 404 会让人以为只是单号打错了,再去试一个")

	_, err = loadTextPayout(context.Background(), "LP-does-not-exist")
	require.ErrorIs(t, err, errPayoutNotFound)
}

// 掩码**一个字符都不能露**。
//
// 它出现在 handleAdminListTextPrizes 里,而那个接口只需要只读管理权限、
// 不写任何审计。曾经的首 2 + 末 2 对短码等于泄漏:6 位数字码露出首末各一段后
// 剩余搜索空间只有 100,写审计的 reveal 那条路被完全绕过。
// "认出是哪一条"由同一行里的 payout_no 负责,它由 crypto/rand 生成、不可枚举。
func TestMaskSecretRevealsNoCharacterOfTheCode(t *testing.T) {
	assert.Equal(t, "", maskSecret(""), "没填内容时不该凭空造出一串星号")
	assert.Equal(t, "****", maskSecret("abcd"))
	assert.Equal(t, "******", maskSecret("482913"), "六位数字码曾经会露出 48**13,剩余空间只有 100")
	assert.Equal(t, "************", maskSecret("CDK-ABCD-1234"))
	for _, s := range []string{"CDK-ABCD-1234", "482913", "abcd"} {
		assert.Equal(t, strings.Repeat("*", utf8.RuneCountInString(maskSecret(s))), maskSecret(s),
			"掩码里除了星号不能有任何别的字符")
	}
}

// seedPayoutNoOf 取出该活动下唯一一条出款的单号。
func seedPayoutNoOf(t *testing.T, gdb *gorm.DB, actId int64) string {
	t.Helper()
	var p Payout
	require.NoError(t, gdb.Where("act_id = ?", actId).Take(&p).Error)
	return p.PayoutNo
}
