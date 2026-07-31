package withdraw

import (
	"errors"
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
	nonce, ciphertext, err := sealPayee(data, accountAAD(ref))
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
		KeyVersion: cfg.PIIKeyVersion,
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
	data, err := openPayee(row.Nonce, row.Cipher, accountAAD(row.Ref))
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
	nonce, ciphertext, err := sealPayee(data, withdrawAAD(withdrawNo))
	if err != nil {
		return nil, err
	}
	return &Payee{
		WithdrawNo: withdrawNo,
		UserId:     userId,
		Channel:    channel,
		CipherAlg:  "aes-256-gcm",
		KeyVersion: config.Get().Withdraw.PIIKeyVersion,
		Nonce:      nonce,
		Cipher:     ciphertext,
		Digest:     digest,
		Masked:     truncate(maskPayee(channel, data), 128),
		CreatedAt:  common.GetTimestamp(),
	}, nil
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
