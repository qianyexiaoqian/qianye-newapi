package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTemp 写一个临时配置文件并返回路径。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

const minimalValid = `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
`

func TestParseFile_AppliesDefaults(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, minimalValid))
	require.NoError(t, err)

	// 未写的字段必须补成默认值,否则连接池为 0 会直接不可用。
	assert.Equal(t, 20, c.Database.MaxIdleConns)
	assert.Equal(t, 100, c.Database.MaxOpenConns)
	assert.Equal(t, "warn", c.Database.LogLevel)
	assert.Equal(t, 60, c.Runtime.LeaseTTLSeconds)
	assert.Equal(t, 200, c.Withdraw.RemarkMaxRunes)

	// 三个"默认为 true"的开关在未显式配置时必须为 true。
	assert.True(t, c.Database.ShouldAutoMigrate())
	assert.True(t, c.Runtime.FailOpen())
	assert.True(t, c.TwoPhase.OutboxEnabled())
}

// 显式写 false 必须能覆盖"默认 true",否则用户根本关不掉这些开关。
func TestParseFile_ExplicitFalseOverridesDefaultTrue(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
  auto_migrate: false
two_phase:
  main_outbox_enabled: false
`))
	require.NoError(t, err)
	assert.False(t, c.Database.ShouldAutoMigrate())
	assert.False(t, c.TwoPhase.OutboxEnabled())
}

// 字段名拼错必须导致启动失败。风控开关静默失效是最危险的失败模式。
func TestParseFile_RejectsUnknownField(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
commission:
  enabled: true
  refund_clawbackk: true
`))
	require.Error(t, err)
}

func TestValidate_Database(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "空 DSN",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"\"\n",
			wantErr: "dsn 不能为空",
		},
		{
			// SQLite 的拒绝理由必须说出**为什么**,不能只说"不支持":
			// 只写"仅支持 MySQL"会被读成适配工作量问题,于是下一个人把 sqlite
			// 驱动接上去,而资金路径的行锁串行化在那一刻静默失效。
			name:    "SQLite 被拒,理由必须点到行锁",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"local\"\n",
			wantErr: "行锁",
		},
		{
			name:    "sqlite: 前缀被拒",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"sqlite:/data/qy.db\"\n",
			wantErr: "行锁",
		},
		{
			name:    "ClickHouse 被拒",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"clickhouse://h:9000/db\"\n",
			wantErr: "行锁",
		},
		{
			name:    "缺少库名",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(127.0.0.1:3306)\"\n",
			wantErr: "缺少库名",
		},
		{
			name:    "PostgreSQL URL 缺少库名",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"postgres://u:p@h:5432\"\n",
			wantErr: "缺少库名",
		},
		{
			name:    "PostgreSQL 关键字 DSN 缺少 dbname",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"host=127.0.0.1 port=5432 user=postgres\"\n",
			wantErr: "缺少 dbname=",
		},
		{
			name:    "max_open 小于 max_idle",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(h:3306)/d\"\n  max_idle_conns: 50\n  max_open_conns: 10\n",
			wantErr: "不得小于",
		},
		{
			name:    "非法日志级别",
			yaml:    "enabled: true\ndatabase:\n  dsn: \"u:p@tcp(h:3306)/d\"\n  log_level: verbose\n",
			wantErr: "log_level 取值非法",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseFile(writeTemp(t, tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// PostgreSQL 是扩展库受支持的第二种部署,两种 DSN 写法都必须过校验。
//
// 这条与上面那张拒绝表是一对:拒绝表只能证明"某些东西被挡住了",挡得太宽
// (比如把 host= 开头的合法 PG DSN 当成"缺库名"的 MySQL DSN)只会在真部署
// 那一刻才暴露 —— 而那一刻主程序是 FatalLog。
func TestValidate_DatabaseAcceptsPostgres(t *testing.T) {
	for _, dsn := range []string{
		"postgres://postgres:pw@127.0.0.1:5432/qy_ext?sslmode=disable",
		"postgresql://postgres@127.0.0.1:5432/qy_ext",
		"host=127.0.0.1 port=5432 user=postgres dbname=qy_ext sslmode=disable",
	} {
		t.Run(dsn, func(t *testing.T) {
			c, _, err := parseFile(writeTemp(t,
				"enabled: true\ndatabase:\n  dsn: \""+dsn+"\"\n"))
			require.NoError(t, err)
			assert.Equal(t, dsn, c.Database.DSN)
		})
	}
}

// 续租间隔必须显著快于过期,否则一次网络抖动就丢租约、任务反复易主。
func TestValidate_LeaseRenewMustBeFasterThanHalfTTL(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
runtime:
  lease_ttl_seconds: 60
  lease_renew_seconds: 30
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lease_renew_seconds")
}

// 启用法币提现却没配密钥必须报错:收款信息是个人敏感信息,不允许明文落库。
func TestValidate_FiatWithdrawRequiresKeys(t *testing.T) {
	base := `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
withdraw:
  enabled: true
  methods: ["quota", "fiat"]
`
	_, _, err := parseFile(writeTemp(t, base))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pii_key")

	// 只用站内额度兑换则不需要密钥。
	_, _, err = parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
withdraw:
  enabled: true
  methods: ["quota"]
`))
	require.NoError(t, err)
}

func TestValidate_PIIKeyMustBe32Bytes(t *testing.T) {
	// 16 字节的 base64,长度不足。
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
withdraw:
  enabled: true
  methods: ["fiat"]
  pii_key: "MDEyMzQ1Njc4OWFiY2RlZg=="
  digest_key: "whatever"
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "32 字节")
}

// 额度上限不得超过主库 users.quota 的 int32 容量,否则跨库写入必然溢出。
func TestValidate_QuotaCapBoundedByInt32(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
transfer:
  enabled: true
  max_per_tx_quota: 9999999999
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "超过主库额度上限")
}

func TestValidate_BpsRange(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
commission:
  enabled: true
  topup_rate_bps: 20000
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topup_rate_bps")
}

// 多级返佣尚未实现,必须直接报错而不是静默降级成一级 ——
// 静默降级会让运营以为二级佣金在发,实际没发。
func TestValidate_RejectsMultiLevelCommission(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
commission:
  enabled: true
  levels: 2
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "仅支持 1 级")
}

func TestValidate_ViolationPolicy(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
violation:
  enabled: true
  insufficient_balance_policy: "explode"
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient_balance_policy")
}

// 可用率的时间桶必须整除一小时,否则小时级汇总会跨桶错位算错。
func TestValidate_AvailabilityBucketDividesHour(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
availability:
  enabled: true
  bucket_seconds: 700
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "3600 的因数")

	_, _, err = parseFile(writeTemp(t, `
enabled: true
database:
  dsn: "u:p@tcp(h:3306)/d"
availability:
  enabled: true
  bucket_seconds: 300
`))
	require.NoError(t, err)
}

// 扩展未启用时不校验其余字段:用户可能只是把配置留着备用。
func TestValidate_SkippedWhenDisabled(t *testing.T) {
	c, _, err := parseFile(writeTemp(t, `
enabled: false
database:
  dsn: ""
commission:
  levels: 99
`))
	require.NoError(t, err)
	assert.False(t, c.Enabled)
}

// 示例配置必须始终能通过校验,否则用户照抄就起不来。
func TestExampleConfigIsValid(t *testing.T) {
	raw, err := os.ReadFile("qianye.example.yaml")
	require.NoError(t, err, "示例配置文件应当存在")

	dir := t.TempDir()
	p := filepath.Join(dir, "qianye.yaml")
	require.NoError(t, os.WriteFile(p, raw, 0o600))

	c, _, err := parseFile(p)
	require.NoError(t, err, "示例配置必须能通过严格解析与校验")
	assert.True(t, c.Enabled)
}

func TestResolvePath_ExplicitMissingIsAnError(t *testing.T) {
	t.Setenv(EnvConfigPath, filepath.Join(t.TempDir(), "nope.yaml"))
	p, found := resolvePath()
	assert.False(t, found)
	assert.NotEmpty(t, p, "显式指定却不存在时必须返回路径,调用方据此报错而非静默禁用")
}

// 没有任何配置文件时,Load 必须成功且扩展禁用 —— 这是"与上游行为一致"的保证。
func TestLoad_NoConfigDisablesExtensionWithoutError(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv(EnvConfigPath, "")

	require.NoError(t, Load())
	assert.False(t, Enabled())
	assert.NotNil(t, Get(), "Get 永不返回 nil")
}

func TestHasWithdrawMethod(t *testing.T) {
	w := Withdraw{Methods: []string{WithdrawMethodQuota}}
	assert.True(t, w.HasWithdrawMethod(WithdrawMethodQuota))
	assert.False(t, w.HasWithdrawMethod(WithdrawMethodFiat))
}
