package lottery

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 文本奖的兑换码必须是真密文,不是明文直存。
//
// 它存的是管理员为中奖者填进去的**实际兑换码**,而同一个扩展库里性质相同的
// 两处(收款账号、AI 渠道密钥)都是 AES-256-GCM。此前这一列是明文直存
// (key_version 恒 0、nonce 恒 NULL):列名叫 cipher,而任何拿到库备份、
// 只读报表账号或离线 dump 的人都能直接读走 —— 在线侧那一整套控制
// (json:"-"、maskSecret、reveal 强制事由 + 双写审计)一条都拦不住他们。
func TestPrizeSecretIsCiphertextWhenAKeyIsConfigured(t *testing.T) {
	withPrizeSecretKey(t, newPrizeKey(t), 1, nil)

	const plain = "QY-CDK-8FN2-REALCODE"
	nonce, ciphertext, version, err := sealPrizeSecret(plain, "LP20260826-abc")
	require.NoError(t, err)

	assert.Equal(t, 1, version)
	assert.NotEmpty(t, nonce, "GCM 必须带 nonce")
	assert.NotContains(t, string(ciphertext), plain, "密文里不许出现明文")
	assert.NotContains(t, string(ciphertext), "QY-CDK", "前缀也不许漏出去")

	back, err := openPrizeSecret(nonce, ciphertext, "LP20260826-abc", version)
	require.NoError(t, err)
	assert.Equal(t, plain, back)
}

// aad 绑定 payout_no:密文被搬到另一条记录上时必须解不开,
// 而不是安静地解出一份属于别人的兑换码。
func TestPrizeSecretIsBoundToItsPayout(t *testing.T) {
	withPrizeSecretKey(t, newPrizeKey(t), 1, nil)

	nonce, ciphertext, version, err := sealPrizeSecret("code-a", "LP-owner")
	require.NoError(t, err)

	_, err = openPrizeSecret(nonce, ciphertext, "LP-someone-else", version)
	assert.ErrorIs(t, err, errPrizeSecretUnreadable)
}

// 库里不存在 v0(明文)行:写路径没有密钥就拒绝写入,也没有任何回填任务
// (D-11:不保留旧数据)。一行标着 v0 的密文只可能来自直接改库,读路径必须按
// "读不出来"处理 —— 把 cipher 列当明文吐给管理员,等于让一串被人塞进去的乱码
// 看起来像一个兑换码。
func TestPrizeSecretRefusesUnversionedRows(t *testing.T) {
	withPrizeSecretKey(t, newPrizeKey(t), 1, nil)

	_, err := openPrizeSecret(nil, []byte("FIXTURE-CODE-2ND-2"), "LP-any", 0)
	assert.ErrorIs(t, err, errPrizeSecretUnreadable, "v0 行没有合法来源,不能被当成明文读出")

	_, err = openPrizeSecret([]byte("123456789012"), []byte("x"), "LP-any", 0)
	assert.ErrorIs(t, err, errPrizeSecretUnreadable)
}

// 没配密钥时**拒绝写入**,绝不静默回落成明文。
//
// 这是本轮那处破坏性变更在代码侧的落点:此前同一个调用会安静地返回
// (nonce=nil, cipher=明文, version=0),接口照常 200,而库里那一列从此是明文,
// 没有任何迹象。启动校验现在拦得住缺配置,但热更新能在运行中把密钥抹掉,
// 那一刻正确的行为是报错而不是降级。
func TestPrizeSecretRefusesToWriteWithoutAKey(t *testing.T) {
	withPrizeSecretKey(t, "", 0, nil)

	nonce, ciphertext, version, err := sealPrizeSecret("plain-code", "LP-x")
	assert.ErrorIs(t, err, errPrizeSecretKeyMissing)
	assert.Empty(t, nonce)
	assert.Empty(t, ciphertext, "拒绝写入时一个字节的明文都不能回给调用方")
	assert.Zero(t, version)
}

// 轮换:旧版本的密文必须用 prize_secret_keys_retired 里的旧钥解开。
// 少了这一层,轮换那一刻已履行的兑换码会全部变成不可读。
func TestPrizeSecretRotationKeepsOldRowsReadable(t *testing.T) {
	oldKey := newPrizeKey(t)
	withPrizeSecretKey(t, oldKey, 1, nil)
	nonce, ciphertext, version, err := sealPrizeSecret("old-code", "LP-rot")
	require.NoError(t, err)
	require.Equal(t, 1, version)

	// 轮换:新钥 v2,旧钥搬进退役表。
	withPrizeSecretKey(t, newPrizeKey(t), 2, map[int]string{1: oldKey})

	back, err := openPrizeSecret(nonce, ciphertext, "LP-rot", 1)
	require.NoError(t, err)
	assert.Equal(t, "old-code", back)

	// 忘了搬运旧钥 = 最典型的运维事故,必须报错而不是解出垃圾。
	withPrizeSecretKey(t, newPrizeKey(t), 2, nil)
	_, err = openPrizeSecret(nonce, ciphertext, "LP-rot", 1)
	assert.ErrorIs(t, err, errPrizeSecretUnreadable)
}

// withPrizeKeyOnCurrentConfig 在**当前**配置快照上补一把兑换码密钥,其余项不动。
//
// 强制加密之后 sealPrizeSecret 没有密钥就写不进去,而好几个库测试只是顺手塞一串码
// 进去以便断言别的事(证据链里不许出现明文、再次履行不许覆盖上一串)。
// 用 withPrizeSecretKey 整份替换配置会连带把那些测试真正依赖的运行参数清成零值,
// 所以这里只补这一项。
func withPrizeKeyOnCurrentConfig(t *testing.T) {
	t.Helper()
	next := *config.Get() // 值拷贝:绝不就地改共享快照
	next.Lottery.PrizeSecretKey = newPrizeKey(t)
	next.Lottery.PrizeSecretKeyVersion = 1
	prev := qyConfig.Swap(&next)
	t.Cleanup(func() { qyConfig.Store(prev) })
}

func newPrizeKey(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

func withPrizeSecretKey(t *testing.T, key string, version int, retired map[int]string) {
	t.Helper()
	prev := qyConfig.Swap(&config.Config{
		Enabled: true,
		Lottery: config.Lottery{
			Enabled:                true,
			PrizeSecretKey:         key,
			PrizeSecretKeyVersion:  version,
			PrizeSecretKeysRetired: retired,
		},
	})
	t.Cleanup(func() { qyConfig.Store(prev) })
	require.Equal(t, strings.TrimSpace(key), strings.TrimSpace(config.Get().Lottery.PrizeSecretKey))
}
