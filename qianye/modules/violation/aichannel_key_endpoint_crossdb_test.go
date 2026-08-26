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

// qy_violation_ai_channel.key_endpoint 这一列与它的回填,此前**只在 SQLite 上
// 跑过**。而这条路径上有两处判定的正确性完全取决于方言,SQLite 一家都验证不了:
//
//	① AIChannel.HasKey() 读的是 key_nonce / key_cipher 两个二进制列。
//	   SQLite 上它们是 BLOB,MySQL 上是 VARBINARY,PostgreSQL 上是 bytea ——
//	   三家把「空」读回 Go 的方式并不一致(nil / 长度 0 的 []byte),而回填的
//	   全部判据就是「有密钥且绑定为空」。判反一次的后果是把没有密钥的渠道也
//	   写上绑定,或者把有密钥的历史行漏掉 —— 后者正是这条闸门要堵的那条越权路径。
//	② 回填的 UPDATE 带 `key_endpoint = ''` 这个条件。空串在三家里都是合法的
//	   varchar 值,但 PostgreSQL 的 `not null default ''::character varying`
//	   与 MySQL 的 `NOT NULL DEFAULT ''` 是两条不同的 DDL,AutoMigrate 生成
//	   哪一条要在真库上看。
//
// 判据与 SQLite 那条(TestMigrateAIChannelKeyEndpointBackfillsExistingRows)
// 逐条相同 —— 这里要证的不是"回填有效",而是"**在真库上**同样有效"。
//
// 不设 QY_TEST_MYSQL_DSN / QY_TEST_PG_DSN 就只跑 sqlite,与本模块其余
// crossdb 测试同口径。
func TestMigrateAIChannelKeyEndpointAcrossDialects(t *testing.T) {
	useAIReviewKey(t)

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
			require.NoError(t, g.Migrator().DropTable(&AIChannel{}))
			t.Cleanup(func() { _ = g.Migrator().DropTable(&AIChannel{}) })
			return g
		}})
	}

	const origin = "http://127.0.0.1:11434/v1"
	for _, fx := range fixtures {
		t.Run(fx.name, func(t *testing.T) {
			gdb := fx.open(t)
			require.NoError(t, gdb.AutoMigrate(&AIChannel{}))
			now := common.GetTimestamp()

			withKey := AIChannel{
				Id: 1, Name: "legacy", BaseUrl: origin, Model: "m",
				Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
			}
			require.NoError(t, gdb.Create(&withKey).Error)
			require.NoError(t, applyAIChannelKey(gdb, &withKey, "sk-LEGACY-1234"))
			// 抹回空串 = 造一行「这一列存在之前写入的」历史数据。
			require.NoError(t, gdb.Model(&AIChannel{}).Where("id = ?", 1).
				Update("key_endpoint", "").Error)

			keyless := AIChannel{
				Id: 2, Name: "no-key", BaseUrl: origin, Model: "m",
				Weight: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
			}
			require.NoError(t, gdb.Create(&keyless).Error)

			// ① 的判据先单独钉一次:二进制列读回来之后 HasKey 必须分得开这两行。
			// 合进下面的回填断言会让"两行都被判成有密钥"与"回填多写了一行"
			// 表现成同一条失败,而它们要改的地方完全不同。
			var before []AIChannel
			require.NoError(t, gdb.Order("id asc").Find(&before).Error)
			require.Len(t, before, 2)
			assert.True(t, before[0].HasKey(), "写过密钥的行必须被认出来")
			assert.False(t, before[1].HasKey(), "没写过密钥的行必须被认出来")
			assert.False(t, before[0].KeyBoundElsewhere(),
				"回填之前必须照常可用:空绑定 = 这一列存在之前的行为")

			filled, err := migrateAIChannelKeyEndpoint(context.Background(), gdb)
			require.NoError(t, err)
			assert.EqualValues(t, 1, filled, "没有密钥的渠道不需要绑定,不该被计进去")

			var after []AIChannel
			require.NoError(t, gdb.Order("id asc").Find(&after).Error)
			require.Len(t, after, 2)
			assert.Equal(t, origin, after[0].KeyEndpoint,
				"回填值只能是当前地址:回填这一刻,那把密钥正在被发往这里")
			assert.Empty(t, after[1].KeyEndpoint, "没有密钥就没有可绑的东西")

			moved := after[0]
			moved.BaseUrl = "http://127.0.0.1:18099"
			assert.True(t, moved.KeyBoundElsewhere(),
				"回填的全部意义就在这一条:存量行从此也堵得住")

			again, err := migrateAIChannelKeyEndpoint(context.Background(), gdb)
			require.NoError(t, err)
			assert.Zero(t, again, "迁移必须幂等 —— 每个节点启动时都会各跑一次")
		})
	}
}
