package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bootstrapParams() BootstrapParams {
	return BootstrapParams{
		Host:     "127.0.0.1",
		Port:     "3306",
		User:     "qy_user",
		Password: "s3cr3t!#pass",
		Database: "qianye",
	}
}

// TestRenderBootstrapPassesTheRealStartupCheck 是本文件存在的理由。
//
// 向导写出的配置若过不了 parseBytes,进程重启就起不来,而那一刻界面已经关了。
// 模板里任何一个键名拼错、任何一个必填项漏掉,都必须在这里红,不能等到部署现场。
func TestRenderBootstrapPassesTheRealStartupCheck(t *testing.T) {
	raw, err := RenderBootstrap(bootstrapParams())
	require.NoError(t, err)

	cfg, err := parseBytes(raw)
	require.NoError(t, err, "生成的配置必须能过启动时那条判据")

	assert.True(t, cfg.Enabled)
	assert.Contains(t, cfg.Database.DSN, "qy_user")
	assert.Equal(t, 20, cfg.Database.MaxIdleConns)

	// 业务模块必须全关:向导只负责接库,开功能是运维之后的决定。
	assert.False(t, cfg.Transfer.Enabled)
	assert.False(t, cfg.Invite.Enabled)
	assert.False(t, cfg.Commission.Enabled)
	assert.False(t, cfg.Ticket.Enabled)
	assert.False(t, cfg.Availability.Enabled)
	assert.False(t, cfg.Violation.Enabled)
	assert.False(t, cfg.Lottery.Enabled)
	assert.False(t, cfg.Mall.Enabled)
}

// TestBootstrapShipsUsableAESKeys 钉住"生成即可用"这条契约。
//
// 密钥是在模块关着的时候写进去的,校验对关闭的模块整段跳过 —— 也就是说
// parseBytes 过关**并不能**证明这两把钥匙是合法的。必须直接问 checkAESKey,
// 否则运维开启商城那天才会在启动失败里发现钥匙是坏的。
func TestBootstrapShipsUsableAESKeys(t *testing.T) {
	raw, err := RenderBootstrap(bootstrapParams())
	require.NoError(t, err)
	cfg, err := parseBytes(raw)
	require.NoError(t, err)

	require.NotEmpty(t, cfg.Mall.SecretKey)
	require.NotEmpty(t, cfg.Lottery.PrizeSecretKey)
	assert.NoError(t, checkAESKey("mall.secret_key", cfg.Mall.SecretKey))
	assert.NoError(t, checkAESKey("lottery.prize_secret_key", cfg.Lottery.PrizeSecretKey))
	assert.NotEqual(t, cfg.Mall.SecretKey, cfg.Lottery.PrizeSecretKey,
		"两个模块必须各用各的钥匙,共用一把等于任一模块泄露就同时打穿两边")
}

// TestMySQLDSNRejectsFieldsThatSmuggleDSNSyntax 守住回环校验。
//
// 用例分两半,分界线是实测出来的(不是猜的):密码里的 / 与 @ 驱动处理得了,
// 库名/主机/用户名里的分隔字符会真的串场。两半都必须钉住 —— 只钉后一半的话,
// 某天有人"顺手"把密码里的 / 也拦掉,而那会让一批合法密码无法配置。
func TestMySQLDSNRejectsFieldsThatSmuggleDSNSyntax(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*BootstrapParams)
		wantErr bool
	}{
		{"普通密码", func(p *BootstrapParams) { p.Password = "s3cr3t" }, false},
		{"密码含感叹号井号", func(p *BootstrapParams) { p.Password = "a!b#c$d%e" }, false},
		{"密码含斜杠——驱动处理得了", func(p *BootstrapParams) { p.Password = "pa/ss" }, false},
		{"密码含 @——驱动处理得了", func(p *BootstrapParams) { p.Password = "pa@ss" }, false},
		{"空密码", func(p *BootstrapParams) { p.Password = "" }, false},

		// 这一条是回环校验真正的价值:密码想改写地址,但驱动按最后一个 @ 切,
		// Addr 仍是我们拼进去的那个,因此**不该**被拒。
		{"密码里写了个假地址", func(p *BootstrapParams) {
			p.Password = "p@tcp(evil.com:3306)/other"
		}, false},

		{"库名含问号会改掉字符集", func(p *BootstrapParams) { p.Database = "qianye?charset=latin1" }, true},
		{"库名含斜杠", func(p *BootstrapParams) { p.Database = "qianye/x" }, true},
		{"主机含 DSN 语法", func(p *BootstrapParams) { p.Host = "evil.com:1)/db?a=b&(" }, true},
		{"用户名含冒号与 @", func(p *BootstrapParams) { p.User = "u:x@y" }, true},
		{"端口不是数字", func(p *BootstrapParams) { p.Port = "3306/x" }, true},
		{"端口越界", func(p *BootstrapParams) { p.Port = "70000" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := bootstrapParams()
			tc.mutate(&p)
			dsn, err := p.MySQLDSN()
			if tc.wantErr {
				assert.Error(t, err)
				assert.Empty(t, dsn)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, dsn, "charset=utf8mb4")
		})
	}
}

func TestMySQLDSNRequiresEveryField(t *testing.T) {
	for _, tc := range []struct{ name, field string }{
		{"缺主机", "Host"}, {"缺端口", "Port"}, {"缺用户", "User"}, {"缺库名", "Database"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := bootstrapParams()
			switch tc.field {
			case "Host":
				p.Host = "  "
			case "Port":
				p.Port = ""
			case "User":
				p.User = ""
			case "Database":
				p.Database = ""
			}
			_, err := p.MySQLDSN()
			assert.Error(t, err)
		})
	}
}

// TestWriteBootstrapRefusesToOverwrite 守住"向导不覆盖已有配置"这条。
//
// 覆盖的代价不是丢一份配置,是丢一把**已经加密过数据**的钥匙:
// RenderBootstrap 每次都生成新的 mall.secret_key / lottery.prize_secret_key,
// 覆盖之后旧密文再也解不开。所以这里必须是拒绝,不能是备份后覆盖。
func TestWriteBootstrapRefusesToOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	t.Setenv(EnvConfigPath, path)

	raw, err := RenderBootstrap(bootstrapParams())
	require.NoError(t, err)

	written, err := WriteBootstrap(raw)
	require.NoError(t, err)
	require.Equal(t, path, written)

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, onDisk)

	// 第二次必须拒绝,且必须不改动磁盘上那一份。
	second, err := RenderBootstrap(bootstrapParams())
	require.NoError(t, err)
	_, err = WriteBootstrap(second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "已存在")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, onDisk, after, "被拒绝的写入不得改动已有文件")
}

// TestWriteBootstrapProducesALoadableConfig 把写盘与加载接起来。
//
// 单独断言 RenderBootstrap 过 parseBytes 是不够的:那走的是内存里的字节,
// 而生产路径是"写到磁盘 → 重启 → config.Load() 从磁盘读"。文件编码、权限、
// 路径解析里任何一处出错,都只会在重启那一刻才暴露。
func TestWriteBootstrapProducesALoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qianye.yaml")
	t.Setenv(EnvConfigPath, path)

	raw, err := RenderBootstrap(bootstrapParams())
	require.NoError(t, err)
	_, err = WriteBootstrap(raw)
	require.NoError(t, err)

	require.NoError(t, Load())
	assert.True(t, Enabled(), "写出来的配置必须让扩展在重启后真的启用")
	assert.Equal(t, path, Path())
	assert.Contains(t, Get().Database.DSN, "qy_user")
}
