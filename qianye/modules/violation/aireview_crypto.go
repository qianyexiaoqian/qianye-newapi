package violation

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// AI 审核渠道 api_key 的加密。
//
// # 为什么是独立的一把钥匙,而不是复用 withdraw.pii_key
//
// 复用最省事,也最危险:提现密钥是**要轮换**的(它护的是银行卡号,轮换步骤
// 写在 withdraw/crypto.go 的注释里),而轮换的那一刻,全部 AI 审核渠道的密钥
// 会同时变成不可解密。那时的表现不是报错,是「AI 审核突然全部放行」——
// 因为热路径对解密失败一律 fail-open(见 aireview.go 的失败方向)。
// 一个模块的例行运维动作静默关掉另一个模块的风控,这种耦合不能建立。
//
// # 与 withdraw 同规格的部分
//
// AES-256-GCM + 版本化轮换 + 版本号由 seal 返回而不是让调用方再读一次配置。
// 最后一条不是洁癖:config.Get() 每次返回的是当前快照指针,两次分开取恰好落在
// 热更新两侧时,会把 v1 的密文标成 v2 —— 那一行从此永远解不开,而且没有任何迹象。
//
// # 与 withdraw 不同的一条:AAD 绑定渠道 id
//
// 提现那边 AAD 绑的是单号。这里绑渠道 id,作用相同:密文若被搬到另一行上,
// GCM 校验直接失败,而不是安静地用 A 渠道的密钥去调 B 渠道的地址。

var (
	// errAIKeyUnavailable 表示 violation.ai_review_key 没配或配错。
	// 这不是"解不开某一行",是"这个功能的加密根本没准备好"。
	errAIKeyUnavailable = errors.New("violation.ai_review_key 未配置或不是合法的 32 字节 base64 密钥")
	// errAIKeyMissingVersion 表示密文行上的版本在 ai_review_keys_retired 里查不到。
	// 与"密钥不对"分开:这是运维轮换时漏了搬运,必须直指配置而不是让人重填密钥。
	errAIKeyMissingVersion = errors.New("该渠道密钥使用的版本未在 violation.ai_review_keys_retired 中登记")
	// errAIKeyUndecryptable 覆盖"密钥不对"与"密文被篡改"两种情况。
	// 两者对调用方是同一件事:这一行的密钥用不了,渠道降级成不可用。
	errAIKeyUndecryptable = errors.New("渠道密钥无法解密")
)

// aiKeyConfigured 回答"现在能不能存下一个 api_key"。
//
// 管理端在保存带 api_key 的渠道之前先问这一句,是为了把"运维少配了一行 YAML"
// 报成一句能照着做的话,而不是一个通用的 500。
func aiKeyConfigured() bool {
	_, err := decodeAIKey(config.Get().Violation.AIReviewKey)
	return err == nil
}

// sealAIKey 加密渠道密钥,返回 nonce、密文与本次使用的密钥版本。
//
// channelRef 是 AAD。新建渠道时行还没有 id,调用方传的是渠道名归一后的值;
// 更新时传 id —— 两者都由 aiChannelAAD 统一生成,调用方不自己拼。
func sealAIKey(plainKey, channelRef string) (nonce, ciphertext []byte, keyVersion int, err error) {
	key, keyVersion, err := activeAIKey()
	if err != nil {
		return nil, nil, 0, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, 0, err
	}
	nonce = make([]byte, gcm.NonceSize())
	// 必须是 crypto/rand:GCM 在同一密钥下 nonce 重用会直接泄漏明文异或值。
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, 0, err
	}
	return nonce, gcm.Seal(nil, nonce, []byte(plainKey), []byte(channelRef)), keyVersion, nil
}

// openAIKey 用密文行上记录的版本解密渠道密钥。
//
// 返回值是明文密钥,**只允许流向出站 HTTP 的 Authorization 头**。
// 它不进日志、不进审计快照、不进任何接口响应 —— 本文件之外唯一的调用点是
// 快照构建(aireview.go 的 buildAIChannels),而快照只活在进程内存里。
func openAIKey(nonce, ciphertext []byte, channelRef string, keyVersion int) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil // 渠道本来就没配密钥(自建的免鉴权审核服务),不是错误
	}
	key, err := aiKeyForVersion(keyVersion)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", errAIKeyUndecryptable
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(channelRef))
	if err != nil {
		return "", errAIKeyUndecryptable
	}
	return string(plain), nil
}

// aiChannelAAD 生成绑定业务标识的附加认证数据。
//
// 用渠道 id 而不是名字:名字可以改,改完之后旧密文就再也解不开了 ——
// 而"改个备注名导致密钥失效"是一种没有人会预料到的失效方式。
func aiChannelAAD(channelId int64) string {
	return fmt.Sprintf("qy_violation_ai_channel:%d", channelId)
}

// maskAIKey 生成界面上显示的掩码提示。
//
// 只留尾 4 位:够运营辨认"我填的是哪一把",又不足以拼回任何一段有效凭证。
// 短于 8 位的密钥一个字符都不露 —— 那种长度下"尾 4 位"已经是半把钥匙。
func maskAIKey(plainKey string) string {
	plainKey = strings.TrimSpace(plainKey)
	if plainKey == "" {
		return ""
	}
	if len(plainKey) < 8 {
		return "****"
	}
	return "****" + plainKey[len(plainKey)-4:]
}

// activeAIKey 返回当前启用的密钥及其版本号,两者取自同一份配置快照。
func activeAIKey() ([]byte, int, error) {
	v := config.Get().Violation
	key, err := decodeAIKey(v.AIReviewKey)
	if err != nil {
		return nil, 0, err
	}
	return key, activeAIKeyVersion(v.AIReviewKeyVersion), nil
}

// aiKeyForVersion 按密文行上的版本号选密钥。
//
// version <= 0 出现在两种行上:KeyVersion 列加上之前写入的历史行,以及被人手工
// 改过的脏数据。一律按当前密钥试 —— 那是唯一有根据的猜测,解不开也只是回落到
// errAIKeyUndecryptable(渠道降级成不可用),不会误解出别的渠道的密钥(AAD 兜住了)。
func aiKeyForVersion(version int) ([]byte, error) {
	v := config.Get().Violation
	if version <= 0 || version == activeAIKeyVersion(v.AIReviewKeyVersion) {
		return decodeAIKey(v.AIReviewKey)
	}
	raw, ok := v.AIReviewKeysRetired[version]
	if !ok {
		// 轮换时忘了把旧钥搬进 ai_review_keys_retired。表现是「AI 审核突然全部
		// 放行」而不是任何报错,所以这里必须主动喊出来并直指配置项。
		common.SysError(fmt.Sprintf(
			"qianye/violation: AI 审核渠道密钥使用的版本 %d 未在 violation.ai_review_keys_retired 中登记,"+
				"该渠道将被跳过(轮换 ai_review_key 时必须把旧密钥连同版本号一并保留)", version))
		return nil, errAIKeyMissingVersion
	}
	return decodeAIKey(raw)
}

// activeAIKeyVersion 归一化当前密钥版本。未填时视为 1,与 applyDefaults
// 以及 KeyVersion 列的默认值保持一致 —— 三者取值一旦不同,新写入的行就会被
// 标成一个查不到密钥的版本。
func activeAIKeyVersion(v int) int {
	if v <= 0 {
		return 1
	}
	return v
}

func decodeAIKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, errAIKeyUnavailable
	}
	return key, nil
}
