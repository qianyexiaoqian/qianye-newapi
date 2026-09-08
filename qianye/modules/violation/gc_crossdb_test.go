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

// TestAIReviewRetentionGCIsIdenticalAcrossDialects 是上一条测试的同形防线,
// 换成了审核明细那张表。
//
// 为什么值得单独跑一遍而不是相信 sqlite 上的那条:上一条测试顶上列的两处
// MySQL 专有写法,是**先在 sqlite 上全绿、再在 PostgreSQL 上静默失效**的。
// 审核明细的清理是新写的同一类代码(先取主键、再按主键批量删),而它的失败
// 方向一模一样 —— 错误只进日志,表现是"清理一直在跑、表一直在涨"。
// 这张表还是全模块最大的一张,静默不清理的代价也最大。
func TestAIReviewRetentionGCIsIdenticalAcrossDialects(t *testing.T) {
	useTestConfig(t, "  enabled: true\n")

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
			require.NoError(t, g.Migrator().DropTable(&AIReview{}, &AISetting{}))
			t.Cleanup(func() { _ = g.Migrator().DropTable(&AIReview{}, &AISetting{}) })
			return g
		}})
	}

	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&AIReview{}, &AISetting{}))

			// 台账库没分家时 db.Log() 回落到主句柄,所以接一个就够 ——
			// 这也正是绝大多数部署的形态。
			prev := qyDBHandleForCtxTest.Swap(gdb)
			t.Cleanup(func() { qyDBHandleForCtxTest.Store(prev) })

			now := common.GetTimestamp()
			require.NoError(t, gdb.Create(&AISetting{
				Id: 1, LogRetentionDays: 1,
				PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
				MaxInputChars: defaultAIMaxInputChars,
				CreatedAt:     now, UpdatedAt: now,
			}).Error)

			// 内容列一起塞进去:它是 text,而"批量删一张带 text 列的表"正是
			// 这条清理在生产里要做的事。空内容的行删起来与真实情形不同。
			for i, ts := range []int64{now - 3*86400, now} {
				require.NoError(t, gdb.Create(&AIReview{
					ReviewNo:     "ai-gc-" + string(rune('a'+i)),
					UserId:       800 + i,
					Phase:        PhasePrompt,
					Outcome:      OutcomeClean,
					ModelName:    "gpt-5",
					UsingGroup:   "default",
					Content:      "一段送审内容,足够长到落在 text 列上",
					ContentChars: 20,
					CreatedAt:    ts,
				}).Error)
			}

			runAIReviewRetentionGC(context.Background())

			var left []string
			require.NoError(t, gdb.Model(&AIReview{}).Pluck("review_no", &left).Error)
			assert.Equal(t, []string{"ai-gc-b"}, left,
				"%s:保留期 1 天,3 天前那条必须删掉、今天那条必须留下", fx.name)
		})
	}
}
