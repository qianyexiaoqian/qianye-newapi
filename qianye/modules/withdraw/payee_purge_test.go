package withdraw

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// loadRetentionConfig 打开一份保留期为 180 天的配置。
func loadRetentionConfig(t *testing.T) {
	t.Helper()
	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota", "fiat"]
  pii_key: "`+testPIIKeyA+`"
  digest_key: "digest-secret"
  pii_retention_days: 180
`)
}

// forceCreatedAt 把 created_at 写成给定值。
//
// 必须绕过 Create:GORM 把任何名为 CreatedAt 的字段当自动时间戳,零值会被悄悄
// 填成当前时间。也就是说 `Create(&row{CreatedAt: 0})` 落库的根本不是 0 ——
// 用它来验证"时间戳异常行不该被当成无限久远"的断言会永远为真,是一条假测试。
// UpdateColumn 不触发 GORM 的时间戳钩子,是唯一能真的写进 0 的路径。
func forceCreatedAt(t *testing.T, gdb *gorm.DB, model any, id int64, createdAt int64) {
	t.Helper()
	require.NoError(t, gdb.Model(model).Where("id = ?", id).UpdateColumn("created_at", createdAt).Error)
}

// seedPayeeFor 给一张提现单挂上收款信息密文快照。
func seedPayeeFor(t *testing.T, gdb *gorm.DB, w *Withdrawal, createdAt int64) *Payee {
	t.Helper()
	p := &Payee{
		WithdrawalId: w.Id,
		WithdrawNo:   w.WithdrawNo,
		UserId:       w.UserId,
		Channel:      ChannelBank,
		CipherAlg:    "aes-256-gcm",
		KeyVersion:   1,
		Nonce:        []byte("000000000000"),
		Cipher:       []byte("ciphertext-" + w.WithdrawNo),
		Digest:       "d-" + w.WithdrawNo,
		Masked:       "招商银行 ****5678 / 张*",
		CreatedAt:    createdAt,
	}
	require.NoError(t, gdb.Create(p).Error)
	forceCreatedAt(t, gdb, &Payee{}, p.Id, createdAt)
	p.CreatedAt = createdAt
	return p
}

func loadPayee(t *testing.T, gdb *gorm.DB, no string) Payee {
	t.Helper()
	var p Payee
	require.NoError(t, gdb.Where("withdraw_no = ?", no).Take(&p).Error)
	return p
}

// D1:withdraw.pii_retention_days 此前没有任何消费方 ——
// 已打款一年的单据,收款人的银行卡号仍然原样躺在库里。
func TestPrunePii(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	now := common.GetTimestamp()
	old := now - 181*86400

	terminal := seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) { w.Status = StatusPaid })
	seedPayeeFor(t, gdb, terminal, old)

	// 还在打款流程里的单:提前抹掉密文就是把这笔钱变成打不出去的死单。
	active := seedWithdrawal(t, gdb, "WD-approved", func(w *Withdrawal) { w.Status = StatusApproved })
	seedPayeeFor(t, gdb, active, old)

	// 保留期内的单一律不动。
	recent := seedWithdrawal(t, gdb, "WD-recent", func(w *Withdrawal) { w.Status = StatusPaid })
	seedPayeeFor(t, gdb, recent, now-86400)

	// created_at = 0 的异常行不能被当成"无限久远"顺手清掉。
	broken := seedWithdrawal(t, gdb, "WD-zero", func(w *Withdrawal) { w.Status = StatusPaid })
	seedPayeeFor(t, gdb, broken, 0)

	prunePii(context.Background(), gdb, 100)

	purged := loadPayee(t, gdb, "WD-paid")
	assert.Empty(t, purged.Cipher, "到期终态单的密文必须被清空")
	assert.NotZero(t, purged.PurgedAt)
	// 脱敏串与风控指纹必须留着:前者是"当时打给了谁"的唯一凭据,
	// 后者是"同一账号被 N 个小号使用"的索引,清掉等于把历史线索一次性作废。
	assert.NotEmpty(t, purged.Masked)
	assert.NotEmpty(t, purged.Digest)

	for _, no := range []string{"WD-approved", "WD-recent", "WD-zero"} {
		kept := loadPayee(t, gdb, no)
		assert.NotEmpty(t, kept.Cipher, "%s 的密文不该被清除", no)
		assert.Zero(t, kept.PurgedAt, "%s 不该被标记为已清除", no)
	}
}

// 终态判定必须写进扫描条件。放在外面过滤的话,一批更老的非终态单(例如长期
// 挂着的人工裁决单)会永远占满每一轮的 batch,后面所有到期单据从此再也轮不到 ——
// 清理任务看起来在跑,实际一行都清不掉。
func TestPrunePii_StuckOrdersDoNotBlockTheBatch(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	old := common.GetTimestamp() - 200*86400

	// id 更小 = 更老,排在扫描的最前面。
	stuck := seedWithdrawal(t, gdb, "WD-stuck", func(w *Withdrawal) {
		w.Status = StatusPaying
		w.ReconcileState = ReconcileHold
	})
	seedPayeeFor(t, gdb, stuck, old)
	done := seedWithdrawal(t, gdb, "WD-done", func(w *Withdrawal) { w.Status = StatusPaid })
	seedPayeeFor(t, gdb, done, old)

	// batch = 1:如果扫描不带终态条件,这一轮只会取到 WD-stuck 然后什么都不做。
	prunePii(context.Background(), gdb, 1)

	assert.Empty(t, loadPayee(t, gdb, "WD-done").Cipher)
	assert.NotEmpty(t, loadPayee(t, gdb, "WD-stuck").Cipher)
}

// 保留期配负数表示关掉清理(填 0 会被 applyDefaults 补成 180,这是刻意的:
// 少配一个键不该让一批银行卡号永久留存);失去租约(ctx 取消)必须立刻停手,
// 否则会与接管节点双跑。
func TestPrunePii_SkipsWhenDisabledOrCancelled(t *testing.T) {
	gdb := newTestDB(t)
	old := common.GetTimestamp() - 400*86400
	w := seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) { w.Status = StatusPaid })
	seedPayeeFor(t, gdb, w, old)

	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
  pii_retention_days: -1
`)
	prunePii(context.Background(), gdb, 100)
	assert.NotEmpty(t, loadPayee(t, gdb, "WD-paid").Cipher, "保留期为负数时不该清理")

	loadRetentionConfig(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	prunePii(cancelled, gdb, 100)
	assert.NotEmpty(t, loadPayee(t, gdb, "WD-paid").Cipher, "失去租约后不该继续写库")
}

// seedPayeeAccount 插入一条用户保存的收款方式。deletedAt = 0 表示还在用。
func seedPayeeAccount(t *testing.T, gdb *gorm.DB, ref string, createdAt, deletedAt int64) *PayeeAccount {
	t.Helper()
	a := &PayeeAccount{
		Ref:        ref,
		UserId:     1,
		Channel:    ChannelBank,
		Label:      "工资卡",
		CipherAlg:  "aes-256-gcm",
		KeyVersion: 1,
		Nonce:      []byte("000000000000"),
		Cipher:     []byte("ciphertext-" + ref),
		Digest:     "d-" + ref,
		Masked:     "招商银行 ****5678 / 张*",
		DeletedAt:  deletedAt,
		CreatedAt:  createdAt,
		UpdatedAt:  createdAt,
	}
	require.NoError(t, gdb.Create(a).Error)
	forceCreatedAt(t, gdb, &PayeeAccount{}, a.Id, createdAt)
	a.CreatedAt = createdAt
	return a
}

func loadPayeeAccount(t *testing.T, gdb *gorm.DB, ref string) PayeeAccount {
	t.Helper()
	var a PayeeAccount
	require.NoError(t, gdb.Where("ref = ?", ref).Take(&a).Error)
	return a
}

// OLD-3:prunePii 只清了 Payee(每张单的快照),而 PayeeAccount 存着同一份银行卡号
// 密文。用户在前端"删除收款方式"只写了软删除标记,密文永久留在库里 ——
// pii_retention_days 对它完全无效,PII 面只清了一半。
func TestPrunePayeeAccounts(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	now := common.GetTimestamp()
	old := now - 181*86400

	seedPayeeAccount(t, gdb, "acc-purge", now-400*86400, old)
	// 删除时间还在保留期内:证据窗口没到,不能提前抹。
	seedPayeeAccount(t, gdb, "acc-recent-delete", now-400*86400, now-86400)
	// 从没删过的收款方式是用户当前在用的数据。抹掉密文之后 resolvePayee 解不开密,
	// 下一次引用它的提现申请再也提交不了 —— 保留期任务不能把自己变成功能故障。
	seedPayeeAccount(t, gdb, "acc-live", now-400*86400, 0)

	prunePayeeAccounts(context.Background(), gdb, 100)

	purged := loadPayeeAccount(t, gdb, "acc-purge")
	assert.Empty(t, purged.Cipher, "已删除且到期的收款方式密文必须被清空")
	assert.NotZero(t, purged.PurgedAt)
	// 与 Payee 同口径:脱敏串与风控指纹留着,历史线索不作废。
	assert.NotEmpty(t, purged.Masked)
	assert.NotEmpty(t, purged.Digest)

	for _, ref := range []string{"acc-recent-delete", "acc-live"} {
		kept := loadPayeeAccount(t, gdb, ref)
		assert.NotEmpty(t, kept.Cipher, "%s 的密文不该被清除", ref)
		assert.Zero(t, kept.PurgedAt, "%s 不该被标记为已清除", ref)
	}
}

// 保留期锚点必须是 deleted_at 而不是 created_at:一张绑了一年、用户还在用的卡
// 不该因为"创建得早"就进入到期集合。
func TestPrunePayeeAccounts_AnchoredOnDeletion(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	// 创建于两年前、昨天才被删除:按 created_at 算早就过期,按 deleted_at 算还差得远。
	seedPayeeAccount(t, gdb, "acc-old-created", now-730*86400, now-86400)

	prunePayeeAccounts(context.Background(), gdb, 100)

	kept := loadPayeeAccount(t, gdb, "acc-old-created")
	assert.NotEmpty(t, kept.Cipher, "保留期应从删除时刻起算,不是创建时刻")
	assert.Zero(t, kept.PurgedAt)
}

// deleted_at > 0 必须写进扫描条件。未删除的行永远不可清,捞进 batch 再丢掉的话,
// 一个长期挂着几张收款方式的老账号(id 更小,排在最前)就能把每一轮 batch 占满,
// 后面所有到期行从此再也轮不到 —— 与 prunePii 的终态判定是同一个坑。
func TestPrunePayeeAccounts_LiveAccountsDoNotBlockTheBatch(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	now := common.GetTimestamp()
	old := now - 200*86400

	// id 更小 = 更老,排在扫描的最前面。
	seedPayeeAccount(t, gdb, "acc-live", now-400*86400, 0)
	seedPayeeAccount(t, gdb, "acc-purge", now-400*86400, old)

	// batch = 1:如果扫描不带 deleted_at > 0,这一轮只会取到 acc-live 然后什么都不做。
	prunePayeeAccounts(context.Background(), gdb, 1)

	assert.Empty(t, loadPayeeAccount(t, gdb, "acc-purge").Cipher)
	assert.NotEmpty(t, loadPayeeAccount(t, gdb, "acc-live").Cipher)
}

func TestPrunePayeeAccounts_SkipsWhenDisabledOrCancelled(t *testing.T) {
	gdb := newTestDB(t)
	old := common.GetTimestamp() - 400*86400
	seedPayeeAccount(t, gdb, "acc-purge", old, old)

	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
  pii_retention_days: -1
`)
	prunePayeeAccounts(context.Background(), gdb, 100)
	assert.NotEmpty(t, loadPayeeAccount(t, gdb, "acc-purge").Cipher, "保留期为负数时不该清理")

	loadRetentionConfig(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	prunePayeeAccounts(cancelled, gdb, 100)
	assert.NotEmpty(t, loadPayeeAccount(t, gdb, "acc-purge").Cipher, "失去租约后不该继续写库")
}

// seedPiiAudit 插入一条明文访问审计。
func seedPiiAudit(t *testing.T, gdb *gorm.DB, reason string, createdAt int64) *PiiAudit {
	t.Helper()
	a := &PiiAudit{
		Resource:     "withdrawal",
		ResourceId:   1,
		TargetUserId: 1,
		AdminId:      9,
		AdminName:    "root",
		Action:       "view_plain",
		Fields:       "account_no,real_name",
		Reason:       reason,
		Ip:           "10.0.0.1",
		CreatedAt:    createdAt,
	}
	require.NoError(t, gdb.Create(a).Error)
	forceCreatedAt(t, gdb, &PiiAudit{}, a.Id, createdAt)
	a.CreatedAt = createdAt
	return a
}

func auditReasons(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	var out []string
	require.NoError(t, gdb.Model(&PiiAudit{}).Order("id asc").Pluck("reason", &out).Error)
	return out
}

// OLD-4:qy_pii_audits 记录每一次银行卡号明文访问,此前没有任何保留期与清理路径。
func TestPrunePiiAudits(t *testing.T) {
	loadRetentionConfig(t) // pii_retention_days = 180 → 审计保留 180 + 365 = 545 天
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	seedPiiAudit(t, gdb, "expired", now-546*86400)
	seedPiiAudit(t, gdb, "inside-window", now-544*86400)
	// created_at = 0 的异常行不能被当成"无限久远"顺手删掉 —— 审计行删了就没了。
	seedPiiAudit(t, gdb, "zero-ts", 0)

	prunePiiAudits(context.Background(), gdb, 100)

	assert.ElementsMatch(t, []string{"inside-window", "zero-ts"}, auditReasons(t, gdb))
}

// 审计凭据的保留期必须【严格长于】它所保护的密文:两者同期到期的话,银行卡号
// 被销毁的那一天,"谁看过这张卡"的证据也一起消失,day-1 就扒库的管理员反而最先
// 被洗白。这条钉住"不能直接复用 pii_retention_days"。
func TestPrunePiiAudits_OutlivesTheCiphertextItProtects(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	// 这个时间点上,同期创建的收款密文早已被 prunePii 清掉(超过 180 天),
	// 而访问痕迹必须还在。
	seedPiiAudit(t, gdb, "just-past-pii-retention", now-181*86400)
	seedPiiAudit(t, gdb, "one-day-before-audit-expiry", now-544*86400)

	prunePiiAudits(context.Background(), gdb, 100)

	assert.ElementsMatch(t,
		[]string{"just-past-pii-retention", "one-day-before-audit-expiry"},
		auditReasons(t, gdb),
		"审计行不能与它保护的密文同期到期")
}

func TestPrunePiiAudits_SkipsWhenDisabledOrCancelled(t *testing.T) {
	gdb := newTestDB(t)
	old := common.GetTimestamp() - 4000*86400
	seedPiiAudit(t, gdb, "ancient", old)

	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
  pii_retention_days: -1
`)
	prunePiiAudits(context.Background(), gdb, 100)
	assert.Equal(t, []string{"ancient"}, auditReasons(t, gdb), "保留期为负数时不该清理")

	loadRetentionConfig(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	prunePiiAudits(cancelled, gdb, 100)
	assert.Equal(t, []string{"ancient"}, auditReasons(t, gdb), "失去租约后不该继续写库")
}

// 一轮清理必须扫过全部四个 PII 面。少接一个,pii_retention_days 就是半个摆设 ——
// 这正是 OLD-3 的形状:清理任务看起来在跑,银行卡号密文却还在另一张表里躺着。
//
// 第四个面(凭证图片)是唯一一个 PII 不在数据库里的,也因此最容易在
// pruneExpiredPii 这一层被漏接 —— 断链的典型形状:pruneProofs 自己测得好好的,
// 调度入口没接上,线上一张图都没清。所以这里断言的是【入口】,不是 pruneProofs。
func TestPruneExpiredPii_CoversEveryPiiSurface(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	now := common.GetTimestamp()

	paid := seedWithdrawal(t, gdb, "WD-paid", func(w *Withdrawal) { w.Status = StatusPaid })
	seedPayeeFor(t, gdb, paid, now-181*86400)
	seedPayeeAccount(t, gdb, "acc-purge", now-400*86400, now-181*86400)
	seedPiiAudit(t, gdb, "expired", now-546*86400)
	proof := seedProof(t, gdb, paid, now-181*86400)

	pruneExpiredPii(context.Background(), gdb, 100)

	assert.Empty(t, loadPayee(t, gdb, "WD-paid").Cipher, "提现单收款快照未被清理")
	assert.Empty(t, loadPayeeAccount(t, gdb, "acc-purge").Cipher, "已删除收款方式未被清理")
	assert.Empty(t, auditReasons(t, gdb), "到期明文访问审计未被清理")
	assert.NotZero(t, reloadProof(t, gdb, proof.Ref).PurgedAt, "到期凭证未被清理")
	assert.NoFileExists(t, proofFullPath(t, proof.StoredName), "凭证元数据标记了已清理,文件却还在磁盘上")
}

// batch 是删除作用域的硬上界:审计行不可恢复,一轮扫掉整张表的风险不能只靠
// 调用方传对参数来避免。
func TestPrunePiiAudits_RespectsBatch(t *testing.T) {
	loadRetentionConfig(t)
	gdb := newTestDB(t)
	old := common.GetTimestamp() - 600*86400
	seedPiiAudit(t, gdb, "a", old)
	seedPiiAudit(t, gdb, "b", old)
	seedPiiAudit(t, gdb, "c", old)

	prunePiiAudits(context.Background(), gdb, 2)

	assert.Equal(t, []string{"c"}, auditReasons(t, gdb))
}
