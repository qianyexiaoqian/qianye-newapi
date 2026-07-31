package withdraw

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// 收款信息的加密与指纹。
//
// 两把密钥刻意分离:
//   - pii_key   用于 AES-256-GCM 加解密,可以轮换(KeyVersion 记录版本);
//   - digest_key 用于 HMAC 风控指纹,永不轮换。
//
// 合成一把的后果是:密钥一轮换,历史指纹全部失效,"同一个收款账号被 N 个小号
// 使用"这条刷单线索直接归零 —— 而那正是提现场景最有价值的风控信号。

// sealPayee 加密收款信息,并返回本次实际使用的密钥版本。
//
// aad 绑定业务标识(提现单号或收款方式 ref):密文若被搬到另一条记录上,
// GCM 校验会直接失败,而不是安静地解出一份属于别人的银行卡号。
//
// 版本号由本函数返回而不是让调用方自己再读一次 config.Get().Withdraw.PIIKeyVersion:
// config.Get() 每次返回的是当前快照指针,两次分开取恰好落在热更新两侧时,
// 会把 v1 的密文标成 v2 —— 那一行从此永远解不开,而且没有任何迹象。
func sealPayee(data map[string]string, aad string) (nonce, ciphertext []byte, keyVersion int, err error) {
	key, keyVersion, err := activePIIKey()
	if err != nil {
		return nil, nil, 0, err
	}
	plain, err := common.Marshal(data)
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
	return nonce, gcm.Seal(nil, nonce, plain, []byte(aad)), keyVersion, nil
}

// openPayee 用密文行上记录的密钥版本解密收款信息。
//
// keyVersion 是 KeyVersion 列存在的全部理由。此前它只被写进去、从没被读出来:
// 运维一旦按注释轮换 pii_key,队列里【全部】待打款单的收款账号会同时变成
// 不可解密 —— 钱打不出去,而佣金还锁在 frozen 里。
//
// 解密失败不区分"密钥不对"与"密文被篡改":两者都是 errPayeeUndecryptable,
// 调用方向管理员展示脱敏值并提示联系用户重新提供,而不是回一个 500。
// 但"这个版本的密钥根本没配"是另一回事:那是运维事故,必须回 500 让人去补配置,
// 而不是让管理员去找用户重新要一遍银行卡号。
func openPayee(nonce, ciphertext []byte, aad string, keyVersion int) (map[string]string, error) {
	key, err := piiKeyForVersion(keyVersion)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() || len(ciphertext) == 0 {
		return nil, errPayeeUndecryptable
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, errPayeeUndecryptable
	}
	data := map[string]string{}
	if err := common.Unmarshal(plain, &data); err != nil {
		return nil, errPayeeUndecryptable
	}
	return data, nil
}

// payeeDigest 计算收款信息的风控指纹。
func payeeDigest(channel string, data map[string]string) (string, error) {
	secret := strings.TrimSpace(config.Get().Withdraw.DigestKey)
	if secret == "" {
		return "", errDigestKeyMissing
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonicalPayee(channel, data)))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// canonicalPayee 把收款信息规范化成稳定字符串。
//
// 刻意不用 JSON 序列化:指纹要在多年、跨版本之间保持一致,而 JSON 库的键序、
// 转义与空白策略都可能随依赖升级而改变。手工拼接的分隔符形式没有这个风险。
// 分隔符取 US(0x1f)/RS(0x1e) 这两个控制字符,收款信息里不可能出现,
// 因此不存在"张三|李"与"张|三李"撞成同一指纹的歧义。
func canonicalPayee(channel string, data map[string]string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("channel\x1f")
	b.WriteString(strings.TrimSpace(channel))
	for _, k := range keys {
		b.WriteString("\x1e")
		b.WriteString(k)
		b.WriteString("\x1f")
		b.WriteString(strings.TrimSpace(data[k]))
	}
	return b.String()
}

// activePIIKey 返回当前启用的加密密钥及其版本号,两者取自同一份配置快照。
func activePIIKey() ([]byte, int, error) {
	w := config.Get().Withdraw
	key, err := decodePIIKey(w.PIIKey)
	if err != nil {
		return nil, 0, err
	}
	return key, activeKeyVersion(w), nil
}

// piiKeyForVersion 按密文行上的版本号选密钥。
//
// version <= 0 出现在两种行上:KeyVersion 列加上之前写入的历史行,以及被人手工
// 改过的脏数据。一律按当前密钥试 —— 那是唯一有根据的猜测,解不开也只是回落到
// errPayeeUndecryptable,不会误解出别人的数据(AAD 与 GCM 标签兜住了)。
func piiKeyForVersion(version int) ([]byte, error) {
	w := config.Get().Withdraw
	if version <= 0 || version == activeKeyVersion(w) {
		return decodePIIKey(w.PIIKey)
	}
	raw, ok := w.PIIKeysRetired[version]
	if !ok {
		// 轮换时忘了把旧钥搬进 pii_keys_retired,是最典型的一种运维事故:
		// 表现是"全部待打款单突然都看不到收款账号了"。错误必须直指配置。
		common.SysError(fmt.Sprintf(
			"qianye/withdraw: 收款信息使用的密钥版本 %d 未在 withdraw.pii_keys_retired 中登记,"+
				"该行无法解密(轮换 pii_key 时必须把旧密钥连同版本号一并保留)", version))
		return nil, errPIIKeyMissingVersion
	}
	return decodePIIKey(raw)
}

// activeKeyVersion 归一化当前密钥版本。
// 配置未填时视为 1,与 applyDefaults 以及 KeyVersion 列的默认值保持一致 ——
// 三者取值一旦不同,新写入的行就会被标成一个查不到密钥的版本。
func activeKeyVersion(w config.Withdraw) int {
	if w.PIIKeyVersion <= 0 {
		return 1
	}
	return w.PIIKeyVersion
}

// decodePIIKey 解析 base64 编码的 32 字节密钥。
//
// 每次调用重新解析而不做缓存:提现是冷路径,一次 base64 解码的开销远小于
// 维护一份"配置热更新后要记得失效"的缓存所带来的出错概率。
func decodePIIKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, errPIIKeyUnavailable
	}
	return key, nil
}
