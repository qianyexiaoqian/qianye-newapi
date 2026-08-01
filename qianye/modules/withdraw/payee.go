package withdraw

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
)

// AAD 前缀。加密时把业务标识绑进 GCM 的附加认证数据,密文被搬到另一条记录上
// 时解密会直接失败,而不是安静地解出一份属于别人的银行卡号。
func accountAAD(ref string) string { return "qy_payee_account:" + ref }
func withdrawAAD(no string) string { return "qy_withdrawal:" + no }

// payeeView 是收款方式对外的唯一形态:只有脱敏值,永不含明文。
type payeeView struct {
	Ref       string `json:"ref"`
	Channel   string `json:"channel"`
	Label     string `json:"label"`
	Masked    string `json:"masked"`
	CreatedAt int64  `json:"created_at"`
}

// listPayeeAccounts 返回用户已保存的收款方式(脱敏)。
func listPayeeAccounts(userId int) ([]payeeView, error) {
	var rows []PayeeAccount
	if err := db.Get().
		Where("user_id = ? AND deleted_at = 0", userId).
		Order("id desc").Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	views := make([]payeeView, 0, len(rows))
	for _, r := range rows {
		views = append(views, payeeView{
			Ref: r.Ref, Channel: r.Channel, Label: r.Label,
			Masked: r.Masked, CreatedAt: r.CreatedAt,
		})
	}
	return views, nil
}

// createPayeeAccount 保存一个新的收款方式。
//
// 数量上限存在的理由不是省存储,而是限制"一个账号挂十几张卡轮流试"这类行为,
// 同时让 digest 风控的信噪比保持可用。
func createPayeeAccount(userId int, channel string, data map[string]string, label string) (*payeeView, error) {
	digest, err := payeeDigest(channel, data)
	if err != nil {
		return nil, err
	}
	ref := strings.ReplaceAll(common.GetUUID(), "-", "")
	// 版本号取 sealPayee 返回值,不再另读一次配置:见 sealPayee 的说明。
	nonce, ciphertext, keyVersion, err := sealPayee(data, accountAAD(ref))
	if err != nil {
		return nil, err
	}

	cfg := config.Get().Withdraw
	now := common.GetTimestamp()
	row := &PayeeAccount{
		Ref:        ref,
		UserId:     userId,
		Channel:    channel,
		Label:      truncate(strings.TrimSpace(label), 64),
		CipherAlg:  "aes-256-gcm",
		KeyVersion: keyVersion,
		Nonce:      nonce,
		Cipher:     ciphertext,
		Digest:     digest,
		Masked:     truncate(maskPayee(channel, data), 128),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	err = db.Get().Transaction(func(tx *gorm.DB) error {
		// 计数与插入必须同事务:分开做的话并发提交会双双读到旧计数一起通过。
		var cnt int64
		if err := tx.Model(&PayeeAccount{}).
			Where("user_id = ? AND deleted_at = 0", userId).Count(&cnt).Error; err != nil {
			return err
		}
		if cfg.PayeeAccountMax > 0 && cnt >= int64(cfg.PayeeAccountMax) {
			return errPayeeLimit
		}
		return tx.Create(row).Error
	})
	if err != nil {
		db.MarkFailure(err)
		return nil, err
	}
	return &payeeView{
		Ref: row.Ref, Channel: row.Channel, Label: row.Label,
		Masked: row.Masked, CreatedAt: row.CreatedAt,
	}, nil
}

// deletePayeeAccount 软删除一个收款方式。
//
// 软删除而非物理删除:历史提现单的 digest 风控线索要留着,
// 且"这个账号曾经绑过某张卡"本身就是事后追查的证据。
func deletePayeeAccount(userId int, ref string) error {
	res := db.Get().Model(&PayeeAccount{}).
		Where("user_id = ? AND ref = ? AND deleted_at = 0", userId, ref).
		Updates(map[string]any{
			"deleted_at": common.GetTimestamp(),
			"updated_at": common.GetTimestamp(),
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errPayeeNotFound
	}
	return nil
}

// resolvePayee 取出本次申请要用的收款信息明文。
//
// 引用已保存的收款方式时会解密一次:提现单必须保存自己的独立快照,
// 而不是指向 payee_account —— 用户改一次收款账号,历史打款记录就全变了,
// 打款争议时拿不出"当时打给了谁"的证据。
func resolvePayee(userId int, acc acceptedRequest) (string, map[string]string, error) {
	if acc.PayeeRef == "" {
		return acc.PayeeChannel, acc.Payee, nil
	}
	var row PayeeAccount
	err := db.Get().
		Where("user_id = ? AND ref = ? AND deleted_at = 0", userId, acc.PayeeRef).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil, errPayeeNotFound
	}
	if err != nil {
		db.MarkFailure(err)
		return "", nil, err
	}
	data, err := openPayee(row.Nonce, row.Cipher, accountAAD(row.Ref), row.KeyVersion)
	if err != nil {
		return "", nil, err
	}
	// 保存时的规格可能早于一次字段调整,重新过一遍校验保证快照始终合规。
	return acceptPayee(row.Channel, data)
}

// buildPayeeSnapshot 生成提现单上的收款信息快照。
func buildPayeeSnapshot(withdrawNo string, userId int, channel string, data map[string]string) (*Payee, error) {
	digest, err := payeeDigest(channel, data)
	if err != nil {
		return nil, err
	}
	nonce, ciphertext, keyVersion, err := sealPayee(data, withdrawAAD(withdrawNo))
	if err != nil {
		return nil, err
	}
	return &Payee{
		WithdrawNo: withdrawNo,
		UserId:     userId,
		Channel:    channel,
		CipherAlg:  "aes-256-gcm",
		KeyVersion: keyVersion,
		Nonce:      nonce,
		Cipher:     ciphertext,
		Digest:     digest,
		Masked:     truncate(maskPayee(channel, data), 128),
		CreatedAt:  common.GetTimestamp(),
	}, nil
}

// prunePii 清除超过保留期的收款信息密文。
//
// withdraw.pii_retention_days 此前没有任何消费方:已打款一年的单据,收款人的
// 银行卡号仍然原样躺在库里。单据本身必须永久留存(法务/税务),但解密能力不该
// 跟着永久留存 —— 这正是 Payee 独立成表的理由。
//
// 只清 Cipher,保留 Masked 与 Digest:前者让管理员事后仍能核对"当时打给了谁",
// 后者是"同一收款账号被 N 个小号使用"的风控索引,清掉等于把历史线索一次性作废。
//
// 终态判定必须写进【扫描条件】而不是取回来再过滤:放在外面的话,一批更老的
// 非终态单(例如长期挂着的人工裁决单)会永远占满每一轮的 batch,后面所有到期
// 单据从此再也轮不到 —— 清理任务看起来在跑,实际一行都清不掉。
func prunePii(ctx context.Context, gdb *gorm.DB, batch int) {
	days := config.Get().Withdraw.PIIRetentionDays
	if days <= 0 || ctx.Err() != nil {
		return
	}
	if batch <= 0 {
		batch = 200
	}
	before := common.GetTimestamp() - int64(days)*86400

	// created_at > 0 是防御性条件:时间戳为 0 的异常行会被当成"无限久远",
	// 一次把还没打款的单据密文抹掉。
	settled := gdb.Model(&Withdrawal{}).Select("withdraw_no").Where("status IN ?", terminalStatuses)
	var ids []int64
	if err := gdb.Model(&Payee{}).
		Where("purged_at = 0 AND created_at > 0 AND created_at < ? AND withdraw_no IN (?)", before, settled).
		Order("id asc").Limit(batch).Pluck("id", &ids).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 扫描到期收款信息失败: " + err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}

	// 再带一次 purged_at = 0:两个节点的租约交接窗口里可能重叠执行一轮,
	// 条件写在 UPDATE 上才不会把 purged_at 刷成第二个时间戳。
	res := gdb.Model(&Payee{}).Where("id IN ? AND purged_at = 0", ids).
		Updates(map[string]any{"cipher": nil, "purged_at": common.GetTimestamp()})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/withdraw: 清除到期收款信息失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/withdraw: 已清除 %d 条到期收款信息密文(保留期 %d 天)",
			res.RowsAffected, days))
	}
}

// pruneExpiredPii 跑一轮保留期清理,覆盖全部四个 PII 面。
//
// 四个面收拢成一个入口,而不是让 reconcile 逐个调用:PII 面漏掉一个,
// pii_retention_days 就是半个摆设 —— 第一版正是这么翻车的(只清了 Payee 快照,
// 漏了 PayeeAccount 里那份一模一样的银行卡号密文,用户删了收款方式也白删)。
// 收拢之后"新增一张存 PII 的表却忘了接清理"能被一条测试钉住,而不是等下一次审计。
//
// 第四个面(凭证图片)是唯一一个 PII 不在数据库里的:它清的是磁盘文件。
// 正因为如此它最容易被漏 —— 一次 `DELETE FROM` 的审计习惯看不见磁盘。
func pruneExpiredPii(ctx context.Context, gdb *gorm.DB, batch int) {
	prunePii(ctx, gdb, batch)
	prunePayeeAccounts(ctx, gdb, batch)
	prunePiiAudits(ctx, gdb, batch)
	pruneProofs(ctx, gdb, batch)
}

// prunePayeeAccounts 清除已删除收款方式的银行卡号密文。
//
// prunePii 只覆盖了 Payee(每张提现单的快照),而 PayeeAccount 里装的是【同一份】
// 银行卡号密文。用户在前端点"删除收款方式",deletePayeeAccount 只写了一个软删除
// 标记,密文原样躺在库里 —— pii_retention_days 对它完全无效,PII 面只清了一半。
//
// 只清软删除过的行。未删除的收款方式是用户【当前在用】的数据:抹掉密文之后
// resolvePayee → openPayee 会直接失败,下一次引用它的提现申请再也提交不了。
// 保留期任务把自己变成功能故障,是比"密文留久了"严重得多的事故。
//
// 保留期锚点取 DeletedAt 而不是 CreatedAt:删除之前这份收款方式一直在服役,
// 从创建日起算等于把还在用的卡也算进保留期,一张绑了半年的常用卡会在用户
// 完全没有删除意图的情况下进入到期集合(只是暂时被 deleted_at > 0 挡住)。
//
// deleted_at > 0 必须写进【扫描条件】而不是取回来再过滤:未删除的行永远不可清,
// 捞进 batch 再丢掉的话,一个长期挂着几张收款方式的老账号(id 更小,排在最前)
// 就能把每一轮 batch 占满,后面所有到期行从此再也轮不到 —— 与 prunePii 的
// 终态判定是同一个坑。
func prunePayeeAccounts(ctx context.Context, gdb *gorm.DB, batch int) {
	days := config.Get().Withdraw.PIIRetentionDays
	if days <= 0 || ctx.Err() != nil {
		return
	}
	if batch <= 0 {
		batch = 200
	}
	before := common.GetTimestamp() - int64(days)*86400

	var ids []int64
	if err := gdb.Model(&PayeeAccount{}).
		Where("purged_at = 0 AND deleted_at > 0 AND deleted_at < ?", before).
		Order("id asc").Limit(batch).Pluck("id", &ids).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 扫描到期收款方式失败: " + err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}

	// 再带一次 purged_at = 0,理由同 prunePii:两个节点的租约交接窗口里可能
	// 重叠执行一轮,条件写在 UPDATE 上才不会把 purged_at 刷成第二个时间戳。
	res := gdb.Model(&PayeeAccount{}).Where("id IN ? AND purged_at = 0", ids).
		Updates(map[string]any{"cipher": nil, "purged_at": common.GetTimestamp()})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/withdraw: 清除到期收款方式密文失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/withdraw: 已清除 %d 条已删除收款方式的密文(保留期 %d 天)",
			res.RowsAffected, days))
	}
}

// piiAuditInvestigationDays 是密文销毁之后仍要保留访问痕迹的追查窗口。
//
// qy_pii_audits 与业务数据不是一类东西:它是【合规凭据】,记录"哪个管理员、
// 什么时候、拿什么事由看了谁的银行卡号"。所以它的保留期必须严格长于它所保护的
// 密文本身 —— 两者同期到期的话,银行卡号被销毁的那一天,"谁看过这张卡"的证据
// 也一起消失,day-1 就把整表扒走的管理员反而最先被洗白。这就是本函数没有直接
// 复用 pii_retention_days 的原因。
//
// 取"pii_retention_days + 一年"而不是另立一个与 PII 无关的固定值:一次明文访问
// 可能发生在密文生命周期里的任何一天,而审计行的时间锚点是访问时刻。以访问时刻
// 为锚再加满一年,才能保证【任何一次访问】在对应密文销毁之后仍有整整一年可追查。
// 一年同时覆盖了"网络日志留存不少于六个月"的合规下限,并给"用户投诉 → 立案 →
// 取证"这条现实链路留出余量。
//
// 没有做成 YAML 键是刻意的:审计凭据的保留期若能被运维随手调小,抹除痕迹就退化
// 成一次配置改动 —— 而这张表存在的全部意义就是防这件事。要放宽必须改代码走评审。
const piiAuditInvestigationDays = 365

// prunePiiAudits 删除超过保留期的明文访问审计。
//
// 这里是【物理删除】而不是像密文那样置空某一列:审计行本身就是证据,把 admin_id
// 与 ip 抹掉之后剩下的空壳既不能追查也不能统计,只是在占位。
//
// 先 Pluck 主键再按 id 删,不用一条带范围条件的 DELETE:审计行不可恢复,一次写错
// 的 WHERE 抹掉的是合规证据。分两步之后 DELETE 的作用域被限定在一组明确的主键上,
// 且天然受 batch 约束,不会一轮吃掉整张表。
//
// created_at > 0 与 prunePii 同理:时间戳为 0 的异常行会被当成"无限久远",
// 一轮就把它删干净。
func prunePiiAudits(ctx context.Context, gdb *gorm.DB, batch int) {
	days := config.Get().Withdraw.PIIRetentionDays
	if days <= 0 || ctx.Err() != nil {
		return
	}
	if batch <= 0 {
		batch = 200
	}
	before := common.GetTimestamp() - int64(days+piiAuditInvestigationDays)*86400

	var ids []int64
	if err := gdb.Model(&PiiAudit{}).
		Where("created_at > 0 AND created_at < ?", before).
		Order("id asc").Limit(batch).Pluck("id", &ids).Error; err != nil {
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 扫描到期明文访问审计失败: " + err.Error())
		return
	}
	if len(ids) == 0 {
		return
	}

	res := gdb.Where("id IN ?", ids).Delete(&PiiAudit{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		common.SysError("qianye/withdraw: 删除到期明文访问审计失败: " + res.Error.Error())
		return
	}
	if res.RowsAffected > 0 {
		common.SysLog(fmt.Sprintf("qianye/withdraw: 已删除 %d 条到期明文访问审计(保留期 %d 天)",
			res.RowsAffected, days+piiAuditInvestigationDays))
	}
}

// digestRiskFlag 判定该收款账号是否已被其他用户使用过。
//
// 不拒绝,只标红:家庭共用收款账号是真实场景,硬拒绝的误伤远大于收益。
// 但"N 个小号 → 同一个收款账号"是刷单党的典型特征,必须让审核的人看见。
func digestRiskFlag(userId int, digest string) string {
	if digest == "" {
		return ""
	}
	var cnt int64
	err := db.Get().Model(&Withdrawal{}).
		Where("payee_digest = ? AND user_id <> ?", digest, userId).
		Count(&cnt).Error
	if err != nil {
		// 风控查询失败不阻断申请:它是提示信息,不是准入条件。
		db.MarkFailure(err)
		common.SysError("qianye/withdraw: 收款指纹风控查询失败(已忽略): " + err.Error())
		return ""
	}
	if cnt > 0 {
		return "shared_payee"
	}
	return ""
}
