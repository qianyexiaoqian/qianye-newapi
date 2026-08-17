package lottery

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/service/imagestore"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// cover_db_test.go —— 活动卡片背景图的四条不变量。
//
// 这一组用例守的全是"看不见的失效":封面配错了会当场被看到,而下面这四种
// 一个都不会在界面上留下痕迹 ——
//
//  1. **外链的形状校验**。javascript:/带账号密码/超长地址进库之后,它会被原样
//     写进每一张卡片的 src。校验是唯一的执行点,没有任何下游会再看一眼。
//  2. **认领的四个 WHERE 条件**。它们是"同一张图不会被两场活动同时占用"与
//     "管理员之间不共享待用上传"的唯一防线,而先读后写在并发下必然漏。
//  3. **换绑必须给旧图打 detached_at**。漏掉这一步的表现是磁盘上多一个
//     永远没人回收的文件 —— 没有任何一处会报错,直到磁盘满。
//  4. **三条回收口径各自扫到、且不扫到别的**。尤其"活动已被删除"那条:
//     删除活动的代码里没有任何一句提到封面,全靠这条 NOT IN 兜住。
//
// 变异验证见文件末尾。

// newCoverEnv 建一个装好扩展库句柄与配置的测试环境。
//
// 走真实的 config.Load(),于是 config.Path() 指向 t.TempDir() 下的临时文件,
// coverStore.Dir() 也就自然落在临时目录里 —— 测试之间天然隔离,不需要任何
// 全局替身,而且落盘/删盘是真的在动文件(回收路径的正确性有一半在
// "文件到底删没删",只写库行的话 Remove 的返回值永远是"文件本就不存在")。
func newCoverEnv(t *testing.T, extraLotteryYAML string) *gorm.DB {
	t.Helper()
	yaml := `
enabled: true
database:
  dsn: "u:p@tcp(127.0.0.1:3306)/qy"
lottery:
  enabled: true
` + extraLotteryYAML
	p := filepath.Join(t.TempDir(), "qianye.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0o600))
	t.Setenv(config.EnvConfigPath, p)
	require.NoError(t, config.Load())

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	// 内存库按连接隔离,多连接会各看到一个空库。
	sqlDB.SetMaxOpenConns(1)
	// 扩展库固定是 MySQL,db.LockForUpdate 无条件挂 FOR UPDATE,而 sqlite 不认。
	// 这是本仓既有做法(见 modules/ticket/testdb_test.go)。被吞掉的只是这条
	// SQL 子句,不是加锁语义本身 —— 单连接的内存库本来就是串行的。
	gdb.ClauseBuilders["FOR"] = func(clause.Clause, clause.Builder) {}
	require.NoError(t, gdb.AutoMigrate(tables()...))

	prevHandle := qyDBHandle.Swap(gdb)
	prevHealthy := qyDBHealthy.Swap(true)
	t.Cleanup(func() {
		qyDBHandle.Store(prevHandle)
		qyDBHealthy.Store(prevHealthy)
		_ = sqlDB.Close()
	})
	return gdb
}

// coverPNG 只造出足以通过魔数判定的最小前缀:被测代码刻意不解码图片
// (解码才是解压炸弹的风险面),后面是什么内容无关紧要。
var coverPNG = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("payload")...)

// seedCover 插入一行封面元数据并把文件真的写到磁盘上。
func seedCover(t *testing.T, gdb *gorm.DB, ref string, mutate func(*Cover)) *Cover {
	t.Helper()
	name, err := imagestore.NewStoredName("png")
	require.NoError(t, err)
	row := &Cover{
		Ref:        ref,
		UserId:     1,
		StoredName: name,
		MimeType:   "image/png",
		Size:       int64(len(coverPNG)),
		CreatedAt:  common.GetTimestamp(),
	}
	if mutate != nil {
		mutate(row)
	}
	require.NoError(t, coverStore.Write(row.StoredName, coverPNG))
	require.NoError(t, gdb.Create(row).Error)
	return row
}

func coverOnDisk(t *testing.T, storedName string) bool {
	t.Helper()
	_, err := coverStore.Locate(storedName)
	return err == nil
}

// TestNormalizeCoverURL 是外链那一路**唯一**的执行点。
//
// 服务端从不去拉取这个地址(浏览器直接加载),所以这里没有 SSRF 面 ——
// 于是也就没有任何"取一次看看能不能通"的兜底,形状校验是全部的把关。
func TestNormalizeCoverURL(t *testing.T) {
	long := "https://cdn.example.com/" + string(make([]byte, coverURLMaxRunes))

	cases := []struct {
		name string
		in   string
		want string
		bad  bool
		why  string
	}{
		{name: "空串表示不设外链", in: "   ", want: ""},
		{name: "https 放行", in: "https://cdn.example.com/a.png", want: "https://cdn.example.com/a.png"},
		{name: "http 放行", in: " http://cdn.example.com/a.png ", want: "http://cdn.example.com/a.png",
			why: "本站可能跑在 http 上,拒掉 http 等于让演示站一张图都配不了"},
		{name: "带查询串与端口照常放行", in: "https://cdn.example.com:8443/a.png?v=2",
			want: "https://cdn.example.com:8443/a.png?v=2"},
		{name: "javascript: 拒", in: "javascript:alert(1)", bad: true,
			why: "现代浏览器不会执行 <img src=javascript:>,但这个字段将来可能进 <a href> 或 CSS url()"},
		{name: "data: 拒", in: "data:image/png;base64,iVBORw0KGgo=", bad: true,
			why: "把 base64 从另一个字段绕进数据库正是本设计要避免的那件事"},
		{name: "file: 拒", in: "file:///etc/passwd", bad: true},
		{name: "相对地址拒", in: "//cdn.example.com/a.png", bad: true,
			why: "没有 Host 的地址在不同页面下解析成不同的东西"},
		{name: "带账号密码拒", in: "https://u:p@cdn.example.com/a.png", bad: true,
			why: "凭据会被画进每一张卡片,也是钓鱼链接最常见的伪装"},
		{name: "含控制字符拒", in: "https://cdn.example.com/a\nb.png", bad: true,
			why: "换行会在日志、CSV 导出与 HTTP 头里造成注入形状;首尾的空白由 TrimSpace 吃掉"},
		{name: "超长拒", in: long, bad: true,
			why: "超出列宽会在 MySQL 非严格模式下被静默截断,截断后的地址一定是坏的"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCoverURL(tc.in)
			if tc.bad {
				require.Error(t, err, tc.why)
				be, ok := AsBizError(err)
				require.True(t, ok, "必须是可以安全回给运营的业务错误")
				assert.Equal(t, "qy_lot_cover_bad_url", be.ErrCode())
				return
			}
			require.NoError(t, err, tc.why)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestResolveCoverInput 锁住"两种来源互斥"。
//
// 两者同时给值时到底显示哪一张,只能由前端的判断顺序回答 ——
// 而那是最坏的一种规则来源。
func TestResolveCoverInput(t *testing.T) {
	t.Run("只给外链", func(t *testing.T) {
		u, ref, err := resolveCoverInput(coverInput{CoverUrl: "https://a.example/x.png"})
		require.NoError(t, err)
		assert.Equal(t, "https://a.example/x.png", u)
		assert.Equal(t, "", ref)
	})
	t.Run("只给上传引用", func(t *testing.T) {
		u, ref, err := resolveCoverInput(coverInput{CoverRef: "R1"})
		require.NoError(t, err)
		assert.Equal(t, "", u)
		assert.Equal(t, "R1", ref)
	})
	t.Run("两个都给即拒", func(t *testing.T) {
		_, _, err := resolveCoverInput(coverInput{CoverUrl: "https://a.example/x.png", CoverRef: "R1"})
		assert.ErrorIs(t, err, errCoverBothSources)
	})
	t.Run("外链是空白时不算冲突", func(t *testing.T) {
		// 前端把两个字段都发上来是常态,空的那个必须被当成"没填"而不是"填了空串"。
		u, ref, err := resolveCoverInput(coverInput{CoverUrl: "  ", CoverRef: "R1"})
		require.NoError(t, err)
		assert.Equal(t, "", u)
		assert.Equal(t, "R1", ref)
	})
}

// TestBindCover 逐条验证认领的四个 WHERE 条件,以及换绑必须给旧图打 detached_at。
func TestBindCover(t *testing.T) {
	t.Run("本人的待用封面可以认领", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) { x.UserId = 7 })

		require.NoError(t, bindCover(gdb, 100, "ACT1", "", "R1", 7))

		var row Cover
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&row).Error)
		assert.Equal(t, int64(100), row.ActId)
		assert.Equal(t, "ACT1", row.ActNo)
		assert.NotZero(t, row.BoundAt)
	})

	t.Run("别人上传的封面不能被我认领", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) { x.UserId = 8 })

		assert.ErrorIs(t, bindCover(gdb, 100, "ACT1", "", "R1", 7), errCoverNotFound)

		var row Cover
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&row).Error)
		assert.Zero(t, row.ActId, "被拒绝的认领不该改动那一行")
	})

	t.Run("同一张图不能被两场活动认领", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) { x.UserId = 7 })

		require.NoError(t, bindCover(gdb, 100, "ACT1", "", "R1", 7))
		assert.ErrorIs(t, bindCover(gdb, 200, "ACT2", "", "R1", 7), errCoverNotFound,
			"两场活动共用一行的话,其中一场换图会把另一场的封面一起弄没")
	})

	t.Run("已回收的封面不能再被引用", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) {
			x.UserId = 7
			x.PurgedAt = common.GetTimestamp()
		})
		assert.ErrorIs(t, bindCover(gdb, 100, "ACT1", "", "R1", 7), errCoverNotFound,
			"文件已经从磁盘上消失了,引用它只会得到一张破图")
	})

	t.Run("换成另一张时旧的那张必须被标记待回收", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "OLD", func(x *Cover) {
			x.UserId = 7
			x.ActId = 100
			x.ActNo = "ACT1"
			x.BoundAt = common.GetTimestamp()
		})
		seedCover(t, gdb, "NEW", func(x *Cover) { x.UserId = 7 })

		require.NoError(t, bindCover(gdb, 100, "ACT1", "OLD", "NEW", 7))

		var old, fresh Cover
		require.NoError(t, gdb.Where("ref = ?", "OLD").Take(&old).Error)
		require.NoError(t, gdb.Where("ref = ?", "NEW").Take(&fresh).Error)
		assert.NotZero(t, old.DetachedAt,
			"不打这个标记,旧图就是一个永远没人回收的磁盘文件 —— 而且没有任何一处会报错")
		assert.Equal(t, int64(100), fresh.ActId)
		assert.True(t, coverOnDisk(t, old.StoredName),
			"标记的这一刻不能删文件:磁盘操作不参与事务回滚")
	})

	t.Run("改成外链时旧图同样要被标记", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "OLD", func(x *Cover) {
			x.UserId = 7
			x.ActId = 100
			x.BoundAt = common.GetTimestamp()
		})

		require.NoError(t, bindCover(gdb, 100, "ACT1", "OLD", "", 7))

		var old Cover
		require.NoError(t, gdb.Where("ref = ?", "OLD").Take(&old).Error)
		assert.NotZero(t, old.DetachedAt, "从上传图切回外链也是一次换绑")
	})

	t.Run("没换就什么都不动", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) {
			x.UserId = 7
			x.ActId = 100
			x.BoundAt = common.GetTimestamp()
		})

		require.NoError(t, bindCover(gdb, 100, "ACT1", "R1", "R1", 7))

		var row Cover
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&row).Error)
		assert.Zero(t, row.DetachedAt,
			"改标题这类不碰封面的保存会带着同一个 ref 再来一次,那不该把正在用的图判死")
	})

	t.Run("换下来的封面在宽限期内可以再绑回去", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "OLD", func(x *Cover) {
			x.UserId = 7
			x.ActId = 100
			x.ActNo = "ACT1"
			x.BoundAt = common.GetTimestamp()
		})
		// 换成外链 —— OLD 被打上 detached_at,但 act_id 仍留着 100
		//(那是回收任务"活动已被删除"那条口径要用的)。
		require.NoError(t, bindCover(gdb, 100, "ACT1", "OLD", "", 7))

		// 换错了想换回去。此刻 qy_lot_activity 里没有任何一行指着 OLD,
		// 它谁都没在用,认领条件只判 act_id = 0 的话这条路是堵死的,
		// 而运营看到的是"封面图片不存在或已被使用"。
		require.NoError(t, bindCover(gdb, 100, "ACT1", "", "OLD", 7))

		var row Cover
		require.NoError(t, gdb.Where("ref = ?", "OLD").Take(&row).Error)
		assert.Equal(t, int64(100), row.ActId)
		assert.NotZero(t, row.BoundAt)
		assert.Zero(t, row.DetachedAt,
			"重新用上之后必须清掉待回收标记,否则宽限期一到它会被连文件一起收走")
	})

	t.Run("正在被别的活动使用的封面仍然抢不走", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) {
			x.UserId = 7
			x.ActId = 100
			x.ActNo = "ACT1"
			x.BoundAt = common.GetTimestamp()
		})
		// 放宽认领条件时最容易顺手放开的就是这一格:detached_at = 0 表示
		// 它此刻正挂在 ACT1 上,抢走会让 ACT1 的封面凭空消失。
		assert.ErrorIs(t, bindCover(gdb, 200, "ACT2", "", "R1", 7), errCoverNotFound)

		var row Cover
		require.NoError(t, gdb.Where("ref = ?", "R1").Take(&row).Error)
		assert.Equal(t, int64(100), row.ActId)
	})
}

// TestDiscardPendingCover 锁住"只能丢自己的、且只能丢还没用上的"。
func TestDiscardPendingCover(t *testing.T) {
	t.Run("自己的待用封面连库行带文件一起消失", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		row := seedCover(t, gdb, "R1", func(x *Cover) { x.UserId = 7 })

		require.NoError(t, discardPendingCover(7, "R1"))

		var cnt int64
		require.NoError(t, gdb.Model(&Cover{}).Where("ref = ?", "R1").Count(&cnt).Error)
		assert.Zero(t, cnt)
		assert.False(t, coverOnDisk(t, row.StoredName))
	})

	t.Run("已经用在活动上的封面不能丢", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		row := seedCover(t, gdb, "R1", func(x *Cover) {
			x.UserId = 7
			x.ActId = 100
			x.BoundAt = common.GetTimestamp()
		})

		assert.ErrorIs(t, discardPendingCover(7, "R1"), errCoverNotFound,
			"丢得掉的话,一场活动的封面会凭空消失,而活动行上仍然指着它")
		assert.True(t, coverOnDisk(t, row.StoredName))
	})

	t.Run("别人的封面不能丢", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		seedCover(t, gdb, "R1", func(x *Cover) { x.UserId = 8 })
		assert.ErrorIs(t, discardPendingCover(7, "R1"), errCoverNotFound)
	})
}

// TestPruneCovers 三条回收口径各自扫到什么、以及**不**扫到什么。
//
// 第三条("活动已被删除")是唯一一条不依赖任何人记得写代码的口径:删除活动的
// 那段代码里一个字都没提封面,全靠这条 NOT IN 兜住 —— 漏掉它就是一个永久的
// 磁盘泄漏,因为回收任务按库行扫,一个没有行指向的文件谁也找不到。
func TestPruneCovers(t *testing.T) {
	ctx := context.Background()
	now := common.GetTimestamp()
	stale := now - coverOrphanSeconds - 10

	t.Run("从未使用且过了宽限期的被回收", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		old := seedCover(t, gdb, "OLD", func(x *Cover) { x.CreatedAt = stale })
		fresh := seedCover(t, gdb, "FRESH", func(x *Cover) { x.CreatedAt = now })

		pruneCovers(ctx)

		assert.False(t, coverOnDisk(t, old.StoredName))
		assert.True(t, coverOnDisk(t, fresh.StoredName),
			"宽限期是给「管理员正在向导里填别的字段」留的,窗口内不能动")

		var reloaded Cover
		require.NoError(t, gdb.Where("ref = ?", "OLD").Take(&reloaded).Error)
		assert.NotZero(t, reloaded.PurgedAt,
			"元数据行要留着:「确实传过、已按规则回收」与「没有这个 ref」是两个回答")
	})

	t.Run("被换下且过了宽限期的被回收", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		// 活动必须真的存在:第三条口径("活动已被删除")会把任何指向不存在活动的
		// 封面一起收走,用一个凭空的 act_id 会让这个用例测到的是那一条。
		act := seedActivity(t, gdb, nil)
		gone := seedCover(t, gdb, "GONE", func(x *Cover) {
			x.ActId = act.Id
			x.BoundAt = stale
			x.DetachedAt = stale
		})
		justSwapped := seedCover(t, gdb, "JUST", func(x *Cover) {
			x.ActId = act.Id
			x.BoundAt = stale
			x.DetachedAt = now
		})

		pruneCovers(ctx)

		assert.False(t, coverOnDisk(t, gone.StoredName))
		assert.True(t, coverOnDisk(t, justSwapped.StoredName),
			"刚换下来的那一刻,访客浏览器里还挂着上一份 HTML,立刻删只会制造破图")
	})

	t.Run("活动还在的封面永不回收", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		act := seedActivity(t, gdb, func(a *Activity) { a.CoverRef = "LIVE" })
		live := seedCover(t, gdb, "LIVE", func(x *Cover) {
			x.ActId = act.Id
			x.ActNo = act.ActNo
			// 时间戳刻意造得很旧:活动表是永不清理的证据表,一场三年前的抽奖
			// 今天仍然要能被翻出来看,按上传时间清掉只会让历史页多一个破图。
			x.CreatedAt = now - 3*365*86400
			x.BoundAt = now - 3*365*86400
		})

		pruneCovers(ctx)

		assert.True(t, coverOnDisk(t, live.StoredName), "正在用的封面没有保留期")
		var reloaded Cover
		require.NoError(t, gdb.Where("ref = ?", "LIVE").Take(&reloaded).Error)
		assert.Zero(t, reloaded.PurgedAt)
	})

	t.Run("活动被删掉之后封面跟着回收", func(t *testing.T) {
		gdb := newCoverEnv(t, "")
		act := seedActivity(t, gdb, func(a *Activity) { a.CoverRef = "ORPHAN" })
		orphan := seedCover(t, gdb, "ORPHAN", func(x *Cover) {
			x.ActId = act.Id
			x.ActNo = act.ActNo
			x.BoundAt = now
		})
		// 删除路径按 act_id 抹掉一场活动的全部行,封面表不在它的清单里 ——
		// 这里模拟的正是那之后的状态。
		require.NoError(t, gdb.Where("id = ?", act.Id).Delete(&Activity{}).Error)

		pruneCovers(ctx)

		assert.False(t, coverOnDisk(t, orphan.StoredName),
			"活动没了而封面还在,就是一个谁也找不到的磁盘泄漏")
	})
}

// ── 变异验证(手工执行并已回滚)────────────────────────────────────────
//
//	normalizeCoverURL 去掉 u.User != nil 那一条
//	    → TestNormalizeCoverURL/带账号密码拒 红
//	normalizeCoverURL 把 scheme 判定放宽成"非空即可"
//	    → javascript:/data:/file: 三个子用例同时红
//	bindCover 的认领条件去掉 user_id
//	    → TestBindCover/别人上传的封面不能被我认领 红
//	bindCover 的认领条件去掉 act_id = 0
//	    → 同一张图不能被两场活动认领 红
//	bindCover 删掉给 oldRef 打 detached_at 的那一段
//	    → 换成另一张 / 改成外链 两个子用例同时红
//	bindCover 去掉 newRef == oldRef 的短路
//	    → 没换就什么都不动 红(正在用的封面被判死)
//	discardPendingCover 的条件去掉 act_id = 0
//	    → 已经用在活动上的封面不能丢 红
//	pruneCovers 删掉第三条(活动已删除)口径
//	    → 活动被删掉之后封面跟着回收 红
//	pruneCovers 把第二条的 detached_at < cutoff 写成 detached_at > 0
//	    → 被换下且过了宽限期 里的 JUST 子断言 红
//	pruneCovers 给"活动还在"的封面加一条按 created_at 的保留期
//	    → 活动还在的封面永不回收 红
