package withdraw

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 两把 base64 编码的 32 字节测试密钥。内容无所谓,长度必须正确 ——
// 配置校验会拒绝任何非 32 字节的密钥。
const (
	testPIIKeyA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testPIIKeyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
)

// loadTestConfig 用一份临时 YAML 驱动 config 全局快照。
//
// 走真实的 Load 路径而不是直接塞结构体:密钥长度、方式枚举这些校验规则
// 本身就是被测行为的一部分,绕过它们的测试证明不了线上会怎么跑。
func loadTestConfig(t *testing.T, piiKey, digestKey string) {
	t.Helper()
	yaml := `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota", "fiat"]
  pii_key: "` + piiKey + `"
  digest_key: "` + digestKey + `"
`
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

func samplePayee() map[string]string {
	return map[string]string{
		"real_name":  "张三",
		"bank_name":  "招商银行",
		"account_no": "6214830112345678",
	}
}

func TestSealOpenPayee_RoundTrip(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	data := samplePayee()

	nonce, ciphertext, err := sealPayee(data, withdrawAAD("WD1"))
	require.NoError(t, err)
	require.Len(t, nonce, 12)
	require.NotEmpty(t, ciphertext)
	// 明文绝不能以任何形式出现在密文里。
	assert.NotContains(t, string(ciphertext), "6214830112345678")

	got, err := openPayee(nonce, ciphertext, withdrawAAD("WD1"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// AAD 绑定是"密文换单"攻击的唯一防线:把 A 单的收款密文复制到 B 单上,
// 解密必须失败,而不是安静地解出一份属于别人的银行卡号。
func TestOpenPayee_RejectsForeignAAD(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	nonce, ciphertext, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)

	_, err = openPayee(nonce, ciphertext, withdrawAAD("WD2"))
	assert.ErrorIs(t, err, errPayeeUndecryptable)
}

func TestOpenPayee_RejectsTamperedCipher(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	nonce, ciphertext, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)

	ciphertext[0] ^= 0xFF
	_, err = openPayee(nonce, ciphertext, withdrawAAD("WD1"))
	assert.ErrorIs(t, err, errPayeeUndecryptable)
}

func TestOpenPayee_RejectsRotatedKey(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	nonce, ciphertext, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)

	loadTestConfig(t, testPIIKeyB, "digest-secret")
	_, err = openPayee(nonce, ciphertext, withdrawAAD("WD1"))
	// 换钥之后旧密文解不开是可预期的运维状态,必须是一个可识别的业务错误
	// (前端据此提示"请联系用户重新提供"),而不是 500。
	assert.ErrorIs(t, err, errPayeeUndecryptable)
}

// 指纹必须与加密密钥完全解耦:密钥轮换后历史指纹若失效,
// "同一收款账号被多个账号使用"这条刷单线索就整体作废了。
func TestPayeeDigest_SurvivesPIIKeyRotation(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	before, err := payeeDigest(ChannelBank, samplePayee())
	require.NoError(t, err)

	loadTestConfig(t, testPIIKeyB, "digest-secret")
	after, err := payeeDigest(ChannelBank, samplePayee())
	require.NoError(t, err)

	assert.Equal(t, before, after)
	assert.Len(t, before, 64) // hex(HMAC-SHA256)
}

func TestPayeeDigest_ChangesWithDigestKey(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "secret-one")
	a, err := payeeDigest(ChannelBank, samplePayee())
	require.NoError(t, err)

	loadTestConfig(t, testPIIKeyA, "secret-two")
	b, err := payeeDigest(ChannelBank, samplePayee())
	require.NoError(t, err)

	assert.NotEqual(t, a, b)
}

func TestPayeeDigest_DistinguishesChannelAndFields(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")

	base, err := payeeDigest(ChannelBank, samplePayee())
	require.NoError(t, err)

	// 同样的字段换个渠道必须是不同的指纹,否则跨渠道会误判成同一账号。
	otherChannel, err := payeeDigest(ChannelAlipay, samplePayee())
	require.NoError(t, err)
	assert.NotEqual(t, base, otherChannel)

	changed := samplePayee()
	changed["account_no"] = "6214830112345679"
	otherAccount, err := payeeDigest(ChannelBank, changed)
	require.NoError(t, err)
	assert.NotEqual(t, base, otherAccount)
}

// 规范化必须只依赖内容,不依赖 map 的插入顺序 —— Go 的 map 遍历顺序是随机的,
// 依赖它的话同一个账号每次算出的指纹都不一样。
func TestCanonicalPayee_OrderIndependent(t *testing.T) {
	a := map[string]string{"real_name": "张三", "account": "13800138000"}
	b := map[string]string{"account": "13800138000", "real_name": "张三"}
	assert.Equal(t, canonicalPayee(ChannelAlipay, a), canonicalPayee(ChannelAlipay, b))
}

// 分隔符必须无歧义:两组不同的字段值不能拼出同一个规范串。
func TestCanonicalPayee_NoDelimiterAmbiguity(t *testing.T) {
	a := canonicalPayee(ChannelAlipay, map[string]string{"account": "ab", "real_name": "cd"})
	b := canonicalPayee(ChannelAlipay, map[string]string{"account": "a", "real_name": "bcd"})
	assert.NotEqual(t, a, b)
}

func TestPayeeCrypto_FailsWithoutKeys(t *testing.T) {
	// 密钥缺失时法币方式在配置校验阶段就会被拒,这里模拟"只开 quota"的部署:
	// 此时任何 PII 操作都必须失败,绝不允许降级为明文落库。
	yaml := `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
`
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())

	_, _, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	assert.ErrorIs(t, err, errPIIKeyUnavailable)

	_, err = payeeDigest(ChannelBank, samplePayee())
	assert.ErrorIs(t, err, errDigestKeyMissing)
}
