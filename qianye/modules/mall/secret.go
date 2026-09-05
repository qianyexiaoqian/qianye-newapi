package mall

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
)

// secret.go —— 兑换码库存与收货地址的密文封装(形状照 lottery/text_prize.go)。
//
// # 为什么这几列必须是真密文
//
// qy_ml_code_stock.code_cipher 存的是**发给用户的实际第三方卡密**,
// qy_ml_order.address_cipher / contact_cipher 存的是收货地址与联系方式(PII)。
// 明文直存的话,任何拿到库备份、只读报表账号或离线 dump 的人都能直接读走,
// 而在线侧的全部控制(json:"-" 不下发、揭示强制验密 + 审计)一条都拦不住他们。
//
// # 没有密钥就写不进去,不回落明文
//
// mall.secret_key 是必填项(qianye/config/validate.go 的 validateMall),正常运行
// 走不到缺失分支;留着它是因为热更新配置与单测都能构造出"密钥没了"的快照,
// 那一刻正确的行为是**拒绝写入** —— 静默回落成明文是这类字段最常见的泄漏路径。
//
// # aad 绑定业务标识
//
// 兑换码绑 product_no,地址绑 order_no:密文若被搬到另一条记录上,GCM 校验
// 会直接失败,而不是安静地解出一份属于别人的码或地址。
//
// sealCode / openCode / sealAddress / openAddress 是**包内唯一**允许触碰那几列的
// 函数,由 secret_guard_test.go 的 AST 断言守住。

// sealCode 把一枚明文兑换码封进库存行。
func sealCode(row *CodeStock, plain, aad string) error {
	key, version, err := activeSecretKey()
	if err != nil {
		return err
	}
	nonce, ciphertext, err := sealAESGCM(key, []byte(plain), aad)
	if err != nil {
		return err
	}
	row.CodeNonce = nonce
	row.CodeCipher = ciphertext
	row.KeyVersion = version
	return nil
}

// openCode 取回一枚兑换码的明文。keyVersion 按**行上记录的版本**选密钥,
// 轮换 secret_key 时若少了这一层,已发出的码会全部变成不可读。
func openCode(row *CodeStock, aad string) (string, error) {
	key, err := secretKeyForVersion(row.KeyVersion)
	if err != nil {
		return "", err
	}
	plain, err := openAESGCM(key, row.CodeNonce, row.CodeCipher, aad)
	if err != nil {
		return "", errSecretUnreadable
	}
	return string(plain), nil
}

// sealAddress 把收货地址与联系方式封进订单行。两列各自一个 nonce,共用一个版本号
// (它们总是同一次写入的)。
func sealAddress(o *Order, address, contact string) error {
	key, version, err := activeSecretKey()
	if err != nil {
		return err
	}
	aad := o.OrderNo
	addrNonce, addrCipher, err := sealAESGCM(key, []byte(address), aad)
	if err != nil {
		return err
	}
	contactNonce, contactCipher, err := sealAESGCM(key, []byte(contact), aad)
	if err != nil {
		return err
	}
	o.AddressNonce, o.AddressCipher = addrNonce, addrCipher
	o.ContactNonce, o.ContactCipher = contactNonce, contactCipher
	o.AddressKeyVersion = version
	o.AddressSetAt = common.GetTimestamp()
	return nil
}

// openAddress 取回收货地址与联系方式的明文。
//
// 已被保留期清空的行(密文为空)回 errAddressPruned,与"解不开"分开:
// 前者是按规则发生的,后者是配置事故。
func openAddress(o *Order) (address, contact string, err error) {
	if o.AddressPrunedAt > 0 || len(o.AddressCipher) == 0 {
		return "", "", errAddressPruned
	}
	key, err := secretKeyForVersion(o.AddressKeyVersion)
	if err != nil {
		return "", "", err
	}
	addr, err := openAESGCM(key, o.AddressNonce, o.AddressCipher, o.OrderNo)
	if err != nil {
		return "", "", errSecretUnreadable
	}
	contactPlain, err := openAESGCM(key, o.ContactNonce, o.ContactCipher, o.OrderNo)
	if err != nil {
		return "", "", errSecretUnreadable
	}
	return string(addr), string(contactPlain), nil
}

// activeSecretKey 返回当前启用的密钥与版本。密钥缺失是硬错误。
//
// 密钥与版本取自**同一份**配置快照:两次分开取恰好落在热更新两侧时,
// 会把 v1 的密文标成 v2 —— 那一行从此永远解不开,而且没有任何迹象。
func activeSecretKey() ([]byte, int, error) {
	m := config.Get().Mall
	if strings.TrimSpace(m.SecretKey) == "" {
		common.SysError("qianye/mall: mall.secret_key 未配置,拒绝写入兑换码 / 收货地址 —— " +
			"这几列不允许明文落库,请生成密钥后重启(openssl rand -base64 32)")
		return nil, 0, errSecretKeyMissing
	}
	key, err := decodeSecretKey(m.SecretKey)
	if err != nil {
		return nil, 0, err
	}
	return key, m.ActiveSecretKeyVersion(), nil
}

// secretKeyForVersion 按密文行上的版本号选密钥。
func secretKeyForVersion(version int) ([]byte, error) {
	m := config.Get().Mall
	if version == m.ActiveSecretKeyVersion() {
		if strings.TrimSpace(m.SecretKey) == "" {
			common.SysError(fmt.Sprintf(
				"qianye/mall: 有 key_version=%d 的密文,但 mall.secret_key 未配置", version))
			return nil, errSecretUnreadable
		}
		return decodeSecretKey(m.SecretKey)
	}
	raw, ok := m.SecretKeysRetired[version]
	if !ok {
		// 轮换时忘了把旧钥搬进 secret_keys_retired 是最典型的运维事故:
		// 表现是"全部已发出的兑换码突然都看不到了"。错误必须直指配置。
		common.SysError(fmt.Sprintf(
			"qianye/mall: 密文使用的密钥版本 %d 未在 mall.secret_keys_retired 中登记,"+
				"该行无法解密(轮换 secret_key 时必须把旧密钥连同版本号一并保留)", version))
		return nil, errSecretUnreadable
	}
	return decodeSecretKey(raw)
}

// decodeSecretKey 解出 32 字节的 AES-256 密钥。规格与 withdraw.pii_key 相同。
func decodeSecretKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("qianye/mall: secret_key 必须是 base64 编码的 32 字节密钥")
	}
	return key, nil
}

func sealAESGCM(key, plain []byte, aad string) (nonce, ciphertext []byte, err error) {
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

func openAESGCM(key, nonce, ciphertext []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() || len(ciphertext) == 0 {
		return nil, errSecretUnreadable
	}
	return gcm.Open(nil, nonce, ciphertext, []byte(aad))
}
