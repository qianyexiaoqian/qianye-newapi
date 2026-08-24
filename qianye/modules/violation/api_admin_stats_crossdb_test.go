package violation

import (
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	qydb "github.com/QuantumNous/new-api/qianye/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// adminStats 的两个分布桶曾经把别名写成 MySQL 的反引号(`rule_name as ` + "`key`" + `)。
//
// 反引号是 MySQL / SQLite 的标识符引号,PostgreSQL 不认 —— 整条 SELECT 报
// `syntax error at or near "`"`(42601),**与表里有没有数据无关**。于是 PG 部署上
// GET /admin/violation/stats 恒 500,而这一份数据是"违规规则"页与"违规记录"页
// 顶部那条影子横幅的唯一来源:横幅走 `if (stats == null) return null`,
// 于是它**静默消失**,站点同时失去「N 条真实扣费 / M 条影子」这条唯一能回答
// "现在有没有规则在真实扣钱"的指示、熔断红条,以及唯一的解除熔断按钮。
//
// key 本身还是 PostgreSQL 的保留字,所以修法不是换一种引号而是换一个不需要
// 引号的列别名(bucket_key),再用 gorm 的 column 标签映回字段 —— json 契约
// (`key`)一个字不变,由本用例连同状态码一起钉住。
//
// MySQL / PostgreSQL 由环境变量开(不设就只跑 SQLite):
//
//	QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN
func TestViolationStatsRunsOnEveryDialect(t *testing.T) {
	for _, env := range []struct{ name, key string }{
		{"mysql", "QY_TEST_MYSQL_DSN"}, {"postgres", "QY_TEST_PG_DSN"},
	} {
		dsn := os.Getenv(env.key)
		if dsn == "" {
			t.Logf("跳过 %s:未设置 %s", env.name, env.key)
			continue
		}
		t.Run(env.name, func(t *testing.T) {
			d, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			gdb, err := gorm.Open(d, &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			for _, tbl := range []string{"qy_violation_record", "qy_violation_ban"} {
				require.NoError(t, gdb.Exec("DROP TABLE IF EXISTS "+tbl).Error)
			}
			t.Cleanup(func() {
				for _, tbl := range []string{"qy_violation_record", "qy_violation_ban"} {
					_ = gdb.Exec("DROP TABLE IF EXISTS " + tbl).Error
				}
			})
			require.NoError(t, gdb.AutoMigrate(&Record{}, &Ban{}))

			prevHandle := qyDBHandleForCtxTest.Swap(gdb)
			prevHealthy := qyDBHealthyForJSONTest.Swap(true)
			prevCfg := qyConfigForJSONTest.Swap(&config.Config{
				Enabled:   true,
				Violation: config.Violation{Enabled: true},
			})
			t.Cleanup(func() {
				qyDBHandleForCtxTest.Store(prevHandle)
				qyDBHealthyForJSONTest.Store(prevHealthy)
				qyConfigForJSONTest.Store(prevCfg)
			})

			// 空库先跑一遍:语法错误在计划阶段发生,零行同样会 500。
			data := readViolationData(t, "/api/qy/admin/violation/stats", adminStats)
			assertJSONArrayIsEmpty(t, data, "by_rule")
			assertJSONArrayIsEmpty(t, data, "by_model")

			now := common.GetTimestamp()
			require.NoError(t, gdb.Create(&Record{
				RecNo: "VR-PROBE-1", UserId: 7, RuleName: "probe-rule", ModelName: "probe-model",
				FeeQuota: 300, CreatedAt: now,
			}).Error)
			require.NoError(t, gdb.Create(&Record{
				RecNo: "VR-PROBE-2", UserId: 8, RuleName: "probe-rule", ModelName: "probe-model",
				FeeQuota: 200, CreatedAt: now,
			}).Error)

			// 独立算出的期望:两条同名同模型的记录 → 各一个桶,cnt=2,fee_quota=500。
			data = readViolationData(t, "/api/qy/admin/violation/stats?hours=24", adminStats)
			assert.JSONEq(t, `[{"key":"probe-rule","cnt":2,"fee_quota":500}]`, string(data["by_rule"]))
			assert.JSONEq(t, `[{"key":"probe-model","cnt":2,"fee_quota":500}]`, string(data["by_model"]))
		})
	}
}
