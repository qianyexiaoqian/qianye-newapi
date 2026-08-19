package withdraw

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 三把 base64 编码的 32 字节测试密钥。内容无所谓,长度必须正确 ——
// 配置校验会拒绝任何非 32 字节的密钥。
const (
	testPIIKeyA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	testPIIKeyB = "QkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkI="
	testPIIKeyC = "Q0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0NDQ0M="
)

// loadTestConfig 用一份临时 YAML 驱动 config 全局快照。
//
// 走真实的 Load 路径而不是直接塞结构体:密钥长度、方式枚举这些校验规则
// 本身就是被测行为的一部分,绕过它们的测试证明不了线上会怎么跑。
func loadTestConfig(t *testing.T, piiKey, digestKey string) {
	t.Helper()
	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota", "fiat"]
  pii_key: "`+piiKey+`"
  digest_key: "`+digestKey+`"
`)
}

func loadTestConfigYAML(t *testing.T, yaml string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())
}

// loadRotatedConfig 模拟一次完整的密钥轮换:新钥启用为 v2,旧钥登记进
// pii_keys_retired[1]。这正是运维按注释操作之后应有的配置形态。
func loadRotatedConfig(t *testing.T, activeKey string, activeVersion int, retired map[int]string) {
	t.Helper()
	yaml := `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota", "fiat"]
  pii_key: "` + activeKey + `"
  pii_key_version: ` + strconv.Itoa(activeVersion) + `
  digest_key: "digest-secret"
  pii_keys_retired:
`
	for v, k := range retired {
		yaml += "    " + strconv.Itoa(v) + `: "` + k + `"` + "\n"
	}
	loadTestConfigYAML(t, yaml)
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

	nonce, ciphertext, version, err := sealPayee(data, withdrawAAD("WD1"))
	require.NoError(t, err)
	require.Len(t, nonce, 12)
	require.NotEmpty(t, ciphertext)
	// 版本号必须由 sealPayee 一并返回:调用方另读一次配置的话,
	// 恰好落在热更新两侧就会把 v1 的密文标成 v2,那一行从此永远解不开。
	assert.Equal(t, 1, version)
	// 明文绝不能以任何形式出现在密文里。
	assert.NotContains(t, string(ciphertext), "6214830112345678")

	got, err := openPayee(nonce, ciphertext, withdrawAAD("WD1"), version)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// AAD 绑定是"密文换单"攻击的唯一防线:把 A 单的收款密文复制到 B 单上,
// 解密必须失败,而不是安静地解出一份属于别人的银行卡号。
func TestOpenPayee_RejectsForeignAAD(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	nonce, ciphertext, version, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)

	_, err = openPayee(nonce, ciphertext, withdrawAAD("WD2"), version)
	assert.ErrorIs(t, err, errPayeeUndecryptable)
}

func TestOpenPayee_RejectsTamperedCipher(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret")
	nonce, ciphertext, version, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)

	ciphertext[0] ^= 0xFF
	_, err = openPayee(nonce, ciphertext, withdrawAAD("WD1"), version)
	assert.ErrorIs(t, err, errPayeeUndecryptable)
}

// B8:密钥轮换是 KeyVersion 列存在的全部理由。
//
// 修复前 openPayee 永远只读"当前"那把钥匙:运维按注释把 pii_key 换成新的、
// pii_key_version 从 1 改成 2、重启,此后队列里【全部】待打款单的收款账号
// 一起变成不可解密 —— 钱打不出去,而佣金还锁在 frozen 里。
func TestOpenPayee_RotatedKeyStillOpensOldCiphertext(t *testing.T) {
	loadRotatedConfig(t, testPIIKeyA, 1, nil)
	nonce, ciphertext, version, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)
	require.Equal(t, 1, version)

	// 轮换:新钥启用为 v2,旧钥登记为 v1。
	loadRotatedConfig(t, testPIIKeyB, 2, map[int]string{1: testPIIKeyA})

	got, err := openPayee(nonce, ciphertext, withdrawAAD("WD1"), version)
	require.NoError(t, err)
	assert.Equal(t, samplePayee(), got)

	// 新写入的密文用新钥、标新版本,而且不能被旧钥解开。
	newNonce, newCipher, newVersion, err := sealPayee(samplePayee(), withdrawAAD("WD2"))
	require.NoError(t, err)
	assert.Equal(t, 2, newVersion)
	_, err = openPayee(newNonce, newCipher, withdrawAAD("WD2"), 1)
	assert.ErrorIs(t, err, errPayeeUndecryptable)
}

// 轮换时忘了把旧钥搬进 pii_keys_retired 是最典型的运维事故。
// 它必须与"密文坏了"区分开:前者要让人去补配置(500),后者才是
// "请联系用户重新提供"(400)。混成一个 code 会把配置疏漏包装成一批用户的锅。
func TestOpenPayee_MissingRetiredKeyIsAnOpsError(t *testing.T) {
	loadRotatedConfig(t, testPIIKeyA, 1, nil)
	nonce, ciphertext, _, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)

	loadRotatedConfig(t, testPIIKeyB, 2, nil) // 忘了登记 v1

	_, err = openPayee(nonce, ciphertext, withdrawAAD("WD1"), 1)
	assert.ErrorIs(t, err, errPIIKeyMissingVersion)
	assert.NotErrorIs(t, err, errPayeeUndecryptable)
}

// 版本选钥必须逐行生效:同一次请求里既有旧版本行也有新版本行是轮换期的常态
// (队列里的老单 + 新提交的单)。
func TestPiiKeyForVersion_PicksPerRow(t *testing.T) {
	loadRotatedConfig(t, testPIIKeyC, 3, map[int]string{1: testPIIKeyA, 2: testPIIKeyB})

	cases := []struct {
		name    string
		version int
		want    string
	}{
		{"当前版本", 3, testPIIKeyC},
		{"上一代", 2, testPIIKeyB},
		{"更早的一代", 1, testPIIKeyA},
		// 0 出现在 KeyVersion 列加上之前的历史行与脏数据上:按当前钥匙试是
		// 唯一有根据的猜测,解不开也只会回落到 errPayeeUndecryptable。
		{"未记录版本按当前钥匙试", 0, testPIIKeyC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := piiKeyForVersion(tc.version)
			require.NoError(t, err)
			want, err := decodePIIKey(tc.want)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}

	_, err := piiKeyForVersion(9)
	assert.ErrorIs(t, err, errPIIKeyMissingVersion)
}

// pii_key_version 未配置时必须与 applyDefaults、以及 KeyVersion 列的默认值
// 一起归一到 1。三者取值一旦不同,新写入的行就会被标成一个查不到密钥的版本。
func TestActiveKeyVersion_DefaultsToOne(t *testing.T) {
	loadTestConfig(t, testPIIKeyA, "digest-secret") // YAML 里没写 pii_key_version
	_, _, version, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	require.NoError(t, err)
	assert.Equal(t, 1, version)
	assert.Equal(t, 1, activeKeyVersion(config.Withdraw{}))
	assert.Equal(t, 1, activeKeyVersion(config.Withdraw{PIIKeyVersion: -3}))
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
func TestCanonicalPayeeRecord_OrderIndependent(t *testing.T) {
	a := map[string]string{"real_name": "张三", "account": "13800138000"}
	b := map[string]string{"account": "13800138000", "real_name": "张三"}
	assert.Equal(t, canonicalPayeeRecord(ChannelAlipay, a), canonicalPayeeRecord(ChannelAlipay, b))
}

// 分隔符必须无歧义:两组不同的字段值不能拼出同一个规范串。
func TestCanonicalPayeeRecord_NoDelimiterAmbiguity(t *testing.T) {
	a := canonicalPayeeRecord(ChannelAlipay, map[string]string{"account": "ab", "real_name": "cd"})
	b := canonicalPayeeRecord(ChannelAlipay, map[string]string{"account": "a", "real_name": "bcd"})
	assert.NotEqual(t, a, b)
}

func TestPayeeCrypto_FailsWithoutKeys(t *testing.T) {
	// 密钥缺失时法币方式在配置校验阶段就会被拒,这里模拟"只开 quota"的部署:
	// 此时任何 PII 操作都必须失败,绝不允许降级为明文落库。
	loadTestConfigYAML(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
withdraw:
  enabled: true
  methods: ["quota"]
`)

	_, _, _, err := sealPayee(samplePayee(), withdrawAAD("WD1"))
	assert.ErrorIs(t, err, errPIIKeyUnavailable)

	_, err = payeeDigest(ChannelBank, samplePayee())
	assert.ErrorIs(t, err, errDigestKeyMissing)
}
