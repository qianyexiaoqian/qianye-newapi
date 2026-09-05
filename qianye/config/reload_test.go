package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshotGlobals 保存并在测试结束时恢复包级快照状态,避免 Reload 测试
// 污染同包其他用例读到的 Get()/Enabled()。
func snapshotGlobals(t *testing.T) {
	t.Helper()
	prevCfg := current.Load()
	prevPath, _ := loadedPath.Load().(string)
	prevAt := loadedAt.Load()
	prevMod := loadedMod.Load()
	t.Cleanup(func() {
		if prevCfg != nil {
			current.Store(prevCfg)
		} else {
			current.Store(&Config{})
		}
		loadedPath.Store(prevPath)
		loadedAt.Store(prevAt)
		loadedMod.Store(prevMod)
	})
}

// Reload 拒绝热关停时会把 Enabled 翻回 true,而 parseFile 的 validate 在
// enabled: false 上是直接放行的 —— 翻转之后必须补跑校验,否则一份写着
// enabled: false 的非法配置(额度越界、密钥形状错、reveal 间隔为 0……)
// 会以未经任何检查的状态整份生效,直到下次重启。
func TestReload_DisableAttemptWithInvalidConfigKeepsOldSnapshot(t *testing.T) {
	snapshotGlobals(t)

	p := writeTemp(t, minimalValid)
	t.Setenv(EnvConfigPath, p)
	require.NoError(t, Load())
	require.True(t, Enabled())

	// 运维把 enabled 改成 false,同时夹带一个 enabled: true 下绝过不了校验的值。
	require.NoError(t, os.WriteFile(p, []byte(`
enabled: false
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
invite:
  day_offset_minutes: 99999
`), 0o600))

	err := Reload()
	require.Error(t, err, "翻回 enabled: true 之后必须重新校验,不能让非法配置整份生效")
	assert.Contains(t, err.Error(), "day_offset_minutes")

	// 旧快照必须原封不动。
	assert.True(t, Enabled())
	assert.Equal(t, 0, Get().Invite.DayOffsetMinutes)
}

// enabled: false 但其余内容合法时,热载仍拒绝关停:配置按 enabled: true
// 复核通过后生效,其余改动照常应用。
func TestReload_DisableAttemptWithValidConfigStaysEnabled(t *testing.T) {
	snapshotGlobals(t)

	p := writeTemp(t, minimalValid)
	t.Setenv(EnvConfigPath, p)
	require.NoError(t, Load())
	require.True(t, Enabled())

	require.NoError(t, os.WriteFile(p, []byte(`
enabled: false
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
invite:
  inviter_cache_seconds: 301
`), 0o600))

	require.NoError(t, Reload())
	assert.True(t, Enabled(), "热载不允许整体关停,Enabled 必须保持 true")
	assert.Equal(t, 301, Get().Invite.InviterCacheSecs, "其余改动应当照常生效")
}
