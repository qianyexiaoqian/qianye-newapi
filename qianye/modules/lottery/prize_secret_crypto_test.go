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

// 历史行(以及没配密钥时写下的行)是 v0 明文,必须照常读得出来 ——
// 加密不能把已经履行过的兑换码变成不可读。
func TestPrizeSecretStillReadsLegacyPlaintextRows(t *testing.T) {
	withPrizeSecretKey(t, newPrizeKey(t), 1, nil)

	back, err := openPrizeSecret(nil, []byte("FIXTURE-CODE-2ND-2"), "LP-any", 0)
	require.NoError(t, err)
	assert.Equal(t, "FIXTURE-CODE-2ND-2", back)

	// 有 nonce 却标 v0 说明数据被改过,不猜。
	_, err = openPrizeSecret([]byte("123456789012"), []byte("x"), "LP-any", 0)
	assert.ErrorIs(t, err, errPrizeSecretUnreadable)
}

// 没配密钥时保持明文,这是刻意的向后兼容:强制要求它会让每个现存部署在
// 升级那一刻 FATAL 退出。
func TestPrizeSecretFallsBackToPlaintextWithoutAKey(t *testing.T) {
	withPrizeSecretKey(t, "", 0, nil)

	nonce, ciphertext, version, err := sealPrizeSecret("plain-code", "LP-x")
	require.NoError(t, err)
	assert.Equal(t, 0, version)
	assert.Empty(t, nonce)
	assert.Equal(t, "plain-code", string(ciphertext))
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
