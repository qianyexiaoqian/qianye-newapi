package model

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ClickHouse 是 LOG_SQL_DSN 支持的第四种方言,而它的 logs 表不走 AutoMigrate,
// 是 model/main.go 里的一段手写 DDL。额度/用量三列此前是 Int32 ——
// 恰好等于 common.MaxQuota 的旧值 math.MaxInt32,于是「额度上界 == 列宽」
// 在这条路上是真的;MaxQuota 抬到 2^43 之后,这一列比它窄 4096 倍,
// 而越界是**静默回绕**(驱动走 reflect.Convert,int64→int32 截断且不报错):
// 2147483648 存成 -2147483648(一次扣费变成一笔等额退款)、
// MaxQuota 本身存成 0(饱和事件在日志里正好变成零消费)。
//
// 判据用 common.MaxQuota 现算,不抄常量:上界再动一次时这条断言跟着动。
func TestClickHouseLogQuotaColumnsAreWideEnoughForMaxQuota(t *testing.T) {
	ddl := clickHouseLogCreateTableSQL(0)

	for _, col := range []string{"quota", "prompt_tokens", "completion_tokens"} {
		re := regexp.MustCompile(`(?m)^\s*` + col + `\s+(Int\d+)`)
		m := re.FindStringSubmatch(ddl)
		require.NotNilf(t, m, "DDL 里找不到列 %s", col)
		width := m[1]
		assert.Equalf(t, "Int64", width,
			"logs.%s 建成 %s,而 common.MaxQuota = %d 装不进去 —— "+
				"ClickHouse 驱动对超宽整数是静默截断,不报错、不留日志",
			col, width, common.MaxQuota)
	}

	// 断言本身要有牙:同一条 DDL 里确实还有别的 Int32 列(它们与额度无关),
	// 所以上面的判据不是"整份 DDL 里没有 Int32"这种恒真的说法。
	assert.Contains(t, ddl, "user_id Int32", "对照列:与额度无关的列仍然是 Int32")

	// int64(MaxQuota) 必须真的装不进 int32 —— 这一条把两个常量的关系钉住。
	assert.Greater(t, int64(common.MaxQuota), int64(2147483647),
		"MaxQuota 已经超过 int32 上界,所以这三列不能是 Int32")
}

// 存量部署靠 CREATE TABLE IF NOT EXISTS 是加不宽的,必须另有一条幂等 ALTER。
func TestClickHouseMigrationWidensExistingQuotaColumns(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	require.NoError(t, err)
	src := string(raw)
	require.Contains(t, src, "func widenClickHouseLogQuotaColumns()",
		"建表语句是 IF NOT EXISTS,改 DDL 只对新部署生效;存量表必须另有一条加宽")
	for _, col := range []string{"quota", "prompt_tokens", "completion_tokens"} {
		assert.Truef(t, strings.Contains(src, `"`+col+`"`),
			"加宽清单里必须含 %s", col)
	}
	assert.Contains(t, src, "widenClickHouseLogQuotaColumns()",
		"加宽必须真的被 migrateClickHouseLogDB 调用,而不是一个没人叫的函数")
}
