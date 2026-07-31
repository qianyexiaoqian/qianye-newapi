package config

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// audit_retention_test.go —— audit.retention_days 的下限守在**加载期**。
//
// 这一组全部走 parseFile 而不是直接调 validateAudit:要守的不是那个纯函数
// (它写对过很多次),而是"纯函数写对了、调度层没接上"这条链。validateAudit
// 一旦忘了挂进 validate(),直接测纯函数的用例照样全绿。

func auditYAML(retentionDays string) string {
	return fmt.Sprintf(`
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
audit:
  retention_days: %s
`, retentionDays)
}

// 0 是默认值,含义是永久保留 —— 必须原样通过,而且不能被 applyDefaults 补成别的值。
//
// 这条守的是升级安全:带清理任务的版本上线时,所有现存部署的 retention_days
// 都是 0(或压根没写),行为必须与升级前逐位一致,一行审计都不能少。
func TestValidateAudit_ZeroMeansForeverAndSurvivesDefaults(t *testing.T) {
	for _, y := range []string{auditYAML("0"), minimalValid} {
		c, _, err := parseFile(writeTemp(t, y))
		require.NoError(t, err)
		assert.Zero(t, c.Audit.RetentionDays,
			"0 表示永久保留,绝不能被 applyDefaults 补成一个会真的删数据的天数")
	}
}

// 低于硬下限必须拒绝启动,而且拒绝时不得改写配置值。
//
// 「不得改写」是这条用例的重点:静默夹到 365 会让运维以为自己配的是 7 天,
// 那是本扩展反复栽跟头的"以为改了其实没改",比直接报错危险得多。
func TestValidateAudit_RejectsBelowFloorWithoutSilentClamping(t *testing.T) {
	for _, days := range []int{1, 7, 30, 180, MinAuditRetentionDays - 1} {
		t.Run(fmt.Sprintf("%d天", days), func(t *testing.T) {
			_, _, err := parseFile(writeTemp(t, auditYAML(fmt.Sprint(days))))
			require.Error(t, err, "低于 %d 天的保留期必须拒绝启动", MinAuditRetentionDays)
			assert.Contains(t, err.Error(), "audit.retention_days",
				"错误信息必须点名字段,否则运维在 YAML 里搜不到")
			assert.Contains(t, err.Error(), fmt.Sprint(MinAuditRetentionDays),
				"错误信息必须给出下限,否则运维只知道错了不知道该填多少")

			// 校验失败时 parseFile 不返回配置,直接验证 validateAudit 不改写入参。
			a := Audit{RetentionDays: days}
			require.Error(t, validateAudit(&a))
			assert.Equal(t, days, a.RetentionDays, "校验器不得静默把取值夹到下限")
		})
	}
}

// 负数不是"永久保留"的另一种写法,必须单独报错。
//
// 若放它过去,Prune 里的 days <= 0 会把它当成永久保留而静默生效 ——
// 运维写 -1 的本意通常是"关掉",两者恰好同向,于是这个错永远不会被发现,
// 直到有人把 -1 改成 -1 以外的别的值。
func TestValidateAudit_RejectsNegative(t *testing.T) {
	_, _, err := parseFile(writeTemp(t, auditYAML("-1")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "audit.retention_days")
}

// 达到或超过下限必须放行,否则这个配置项就等于只能填 0,清理任务永远跑不起来。
func TestValidateAudit_AcceptsFloorAndAbove(t *testing.T) {
	for _, days := range []int{MinAuditRetentionDays, MinAuditRetentionDays + 1, 3650} {
		c, _, err := parseFile(writeTemp(t, auditYAML(fmt.Sprint(days))))
		require.NoError(t, err, "%d 天不低于下限,应当放行", days)
		assert.Equal(t, days, c.Audit.RetentionDays)
	}
}

// 下限本身必须真的覆盖那两条外部时限。
//
// 常量被人顺手改小(比如"30 天够用了")时,上面几条用例会跟着一起变绿 ——
// 它们都是拿 MinAuditRetentionDays 自身做的比较。这条是唯一能拦住那次改动的断言。
func TestMinAuditRetentionDays_CoversChargebackAndAnnualAudit(t *testing.T) {
	assert.GreaterOrEqual(t, MinAuditRetentionDays, 180,
		"下限必须活过拒付(chargeback)争议窗口,否则纠纷还没结束凭据就没了")
	assert.GreaterOrEqual(t, MinAuditRetentionDays, 365,
		"税务与审计留存按年计,下限不得短于一个完整会计年度")
}

// 示例 YAML 必须把三件事讲清楚:0 的含义、下限是多少、为什么。
//
// 这个配置项的危险不在代码里,在"运维照着示例填了一个自以为安全的值"。
// 示例注释是他唯一会读的文档。
func TestExampleYAML_ExplainsAuditRetentionSemantics(t *testing.T) {
	raw, err := os.ReadFile("qianye.example.yaml")
	require.NoError(t, err)
	block := sectionOf(string(raw), "audit:")
	require.NotEmpty(t, block, "示例配置里应当有 audit 段")

	assert.Contains(t, block, "永久保留", "必须写明 0 的含义")
	assert.Contains(t, block, fmt.Sprint(MinAuditRetentionDays), "必须写明硬下限的具体天数")
	assert.Contains(t, block, "拒绝启动", "必须写明低于下限是拒绝启动,而不是被夹到下限")
	assert.Contains(t, block, "retention.go", "必须写明消费方,否则又变回一个看不出谁在读的开关")
}

// sectionOf 截取顶层 YAML 段(从 header 行到下一个顶层键之前)。
func sectionOf(raw, header string) string {
	lines := strings.Split(raw, "\n")
	var out []string
	in := false
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, header):
			in = true
		case in && ln != "" && !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "#"):
			return strings.Join(out, "\n")
		}
		if in {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
