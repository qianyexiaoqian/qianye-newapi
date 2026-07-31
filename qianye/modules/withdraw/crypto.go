package withdraw

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// sealPayee 加密收款信息。
//
// aad 绑定业务标识(提现单号或收款方式 ref):密文若被搬到另一条记录上,
// GCM 校验会直接失败,而不是安静地解出一份属于别人的银行卡号。
func sealPayee(data map[string]string, aad string) (nonce, ciphertext []byte, err error) {
	key, err := piiKey()
	if err != nil {
		return nil, nil, err
	}
	plain, err := common.Marshal(data)
	if err != nil {
		return nil, nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	// 必须是 crypto/rand:GCM 在同一密钥下 nonce 重用会直接泄漏明文异或值。
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plain, []byte(aad)), nil
}

// openPayee 解密收款信息。
//
// 解密失败不区分"密钥不对"与"密文被篡改":两者都是 errPayeeUndecryptable,
// 调用方向管理员展示脱敏值并提示联系用户重新提供,而不是回一个 500。
func openPayee(nonce, ciphertext []byte, aad string) (map[string]string, error) {
	key, err := piiKey()
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

// piiKey 解析 base64 编码的 32 字节密钥。
//
// 每次调用重新解析而不做缓存:提现是冷路径,一次 base64 解码的开销远小于
// 维护一份"配置热更新后要记得失效"的缓存所带来的出错概率。
func piiKey() ([]byte, error) {
	raw := strings.TrimSpace(config.Get().Withdraw.PIIKey)
	if raw == "" {
		return nil, errPIIKeyUnavailable
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, errPIIKeyUnavailable
	}
	return key, nil
}
