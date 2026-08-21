package violation

import (
	"context"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/QuantumNous/new-api/common"
	qydb "github.com/QuantumNous/new-api/qianye/db"
)

// 保留期清理里有两处曾经是 MySQL 专有写法,而它们**都只把错误写进日志**:
//
//	DELETE ... ORDER BY created_at LIMIT ?      —— DELETE 上的 ORDER BY / LIMIT
//	                                              是 MySQL 扩展,PostgreSQL 语法错误
//	SET has_payload = 0 WHERE has_payload = 1   —— PostgreSQL 的 boolean 不接受
//	                                              整数字面量(operator does not exist)
//
// 后果不是报错而是**证据表永远不清理**:qy_violation_payload 是本模块体积最大、
// 隐私风险最高的表,保留期形同虚设;而详情页会一直显示 has_payload=true 却打不开。
// 所以判据要同时看三件事:过期证据没了、未过期证据还在、记录行的 has_payload
// 被正确翻成 false。
func TestRetentionGCIsIdenticalAcrossDialects(t *testing.T) {
	useTestConfig(t, "  enabled: true\n  evidence_retention_days: 1\n")

	type fixture struct {
		name string
		open func(*testing.T) *gorm.DB
	}
	fixtures := []fixture{{
		name: "sqlite",
		open: func(t *testing.T) *gorm.DB {
			g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			sqlDB, err := g.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = sqlDB.Close() })
			return g
		},
	}}
	for _, env := range []struct{ name, key string }{
		{"mysql", "QY_TEST_MYSQL_DSN"}, {"postgres", "QY_TEST_PG_DSN"},
	} {
		dsn := os.Getenv(env.key)
		if dsn == "" {
			continue
		}
		fixtures = append(fixtures, fixture{name: env.name, open: func(t *testing.T) *gorm.DB {
			d, err := qydb.DialectorFor(dsn)
			require.NoError(t, err)
			g, err := gorm.Open(d, &gorm.Config{Logger: gormlogger.Discard})
			require.NoError(t, err)
			require.NoError(t, g.Migrator().DropTable(&Record{}, &Payload{}, &Counter{}))
			t.Cleanup(func() { _ = g.Migrator().DropTable(&Record{}, &Payload{}, &Counter{}) })
			return g
		}})
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&Record{}, &Payload{}, &Counter{}))

			prev := qyDBHandleForCtxTest.Swap(gdb)
			t.Cleanup(func() { qyDBHandleForCtxTest.Store(prev) })

			now := common.GetTimestamp()
			old := now - 3*86400 // 保留期 1 天,这条必删
			fresh := now         // 这条必须留下

			for i, ts := range []int64{old, fresh} {
				rec := Record{
					RecNo: "vr-gc-" + string(rune('a'+i)), UserId: 700 + i, RuleId: 1,
					Phase: PhasePrompt, Action: ActionRecord, Status: RecordActive,
					FeeStatus: FeeStatusNone, HasPayload: true, CreatedAt: ts,
				}
				require.NoError(t, gdb.Create(&rec).Error)
				require.NoError(t, gdb.Create(&Payload{
					RecordId: rec.Id, Codec: "gzip", Body: []byte("x"), CreatedAt: ts,
				}).Error)
			}

			runRetentionGC(context.Background())

			var payloadCount int64
			require.NoError(t, gdb.Model(&Payload{}).Count(&payloadCount).Error)
			assert.EqualValues(t, 1, payloadCount, "%s:只有过期的那条证据该被删掉", fx.name)

			var kept []Record
			require.NoError(t, gdb.Order("created_at").Find(&kept).Error)
			require.Len(t, kept, 2, "记录行本身保留更久,一条都不该少")
			assert.False(t, kept[0].HasPayload,
				"%s:证据已删的旧记录必须被标回 has_payload=false,否则详情页永远打开一片空白", fx.name)
			assert.True(t, kept[1].HasPayload, "%s:证据还在的记录不得被改", fx.name)
		})
	}
}
