package violation

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// category_index_test.go —— 违规类型唯一索引对账的回归。
//
// 守的是一个**已经上线的**真实缺陷:Category.Key 的唯一索引一度写成
// (key, deleted_at)。三家数据库都把 NULL 视为互不相等,于是活行之间根本没有
// 唯一性,ensureSeedCategories 的 OnConflict DoNothing 完全落空,每次重启都把
// 整套出厂类型再插一遍(演示站实测 6 → 24 行)。
//
// # 为什么这条回归必须单独存在
//
// model.go 上的 tag 早就改成了 (key, archive_seq),而且 category_test.go 里
// 已经有一条 TestCategoryKeyIsUniqueAmongLiveRows —— 它一直是绿的。原因是
// 全部既有测试都从**新定义**建表,新建的表天然是对的;而 GORM 的 AutoMigrate
// 只按**索引名**判断存在与否,名字还在就什么都不做,所以任何在修复之前跑过一次
// 的库永远停在旧定义上。"改了 tag 就以为修好了"正是这个缺陷的形状,因此本文件
// 从 legacyCategory(旧定义)建表,并先证明缺陷真的会复现。

// legacyCategory 是修复之前的表定义:唯一索引的第二列是 deleted_at。
type legacyCategory struct {
	Id          int64  `gorm:"primaryKey;autoIncrement"`
	Key         string `gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_vcat_key,priority:1"`
	Name        string `gorm:"type:varchar(64);not null"`
	Remark      string `gorm:"type:varchar(512);not null"`
	PublicTitle string `gorm:"type:varchar(64);not null"`
	PublicDesc  string `gorm:"type:varchar(512);not null"`
	Published   bool   `gorm:"not null"`
	Enabled     bool   `gorm:"not null"`
	WindowHours int    `gorm:"not null"`
	Threshold   int    `gorm:"not null"`
	SortOrder   int    `gorm:"not null"`
	IsFallback  bool   `gorm:"not null"`
	CreatedAt   int64  `gorm:"not null"`
	UpdatedAt   int64  `gorm:"not null"`
	UpdatedBy   int    `gorm:"not null"`
	ArchiveSeq  int64  `gorm:"not null"`

	DeletedAt gorm.DeletedAt `gorm:"index;uniqueIndex:uk_qy_vcat_key,priority:2"`
}

func (legacyCategory) TableName() string { return "qy_violation_category" }

// newLegacyCategoryDB 建一个"修复之前就已经存在"的库。
func newLegacyCategoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, gdb.AutoMigrate(&legacyCategory{}, &CategoryCounter{}, &Rule{}, &Record{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

// TestLegacyKeyIndexLetsSeedsPileUp 先证明缺陷是真的:旧定义下补种不幂等。
//
// 没有这一条,下面那条修复测试无法排除"这个库本来就是好的"。
func TestLegacyKeyIndexLetsSeedsPileUp(t *testing.T) {
	gdb := newLegacyCategoryDB(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, ensureSeedCategories(ctx, gdb))
	}
	var n int64
	require.NoError(t, gdb.Model(&Category{}).Count(&n).Error)
	assert.EqualValues(t, len(seedCategories)*3, n,
		"旧索引下补种本来就该堆叠;这里只有一套说明 fixture 没有真的重建出旧定义,后面的修复测试也就没有判别力")
}

// TestReconcileCategoryKeyIndexRepairsLegacyDatabase 是本次修复的主回归。
func TestReconcileCategoryKeyIndexRepairsLegacyDatabase(t *testing.T) {
	gdb := newLegacyCategoryDB(t)
	ctx := context.Background()

	// 演示站的现状:重启 3 次,攒了 3 套出厂类型。
	for i := 0; i < 3; i++ {
		require.NoError(t, ensureSeedCategories(ctx, gdb))
	}
	var all []Category
	require.NoError(t, gdb.Order("id asc").Find(&all).Error)
	require.Len(t, all, len(seedCategories)*3)

	first := all[1]                       // 第一套里的第 2 个类型 = 权威行
	third := all[len(seedCategories)*2+1] // 第三套里的同 key 行 = 待归档
	require.Equal(t, first.Key, third.Key, "fixture 自证:两者确实同 key")

	require.NoError(t, gdb.Create(&Rule{Name: "指向权威行", CategoryId: first.Id}).Error)
	require.NoError(t, gdb.Create(&Rule{Name: "指向重复行", CategoryId: third.Id}).Error)
	// 用户 7 在两行上各攒了计数 —— 合并之后一次都不能少。
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 7, CategoryId: first.Id, HitCount: 2, TotalCount: 5, WindowStart: 100, LastHitAt: 110,
	}).Error)
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 7, CategoryId: third.Id, HitCount: 3, TotalCount: 4, WindowStart: 200, LastHitAt: 220,
	}).Error)
	// 用户 8 只在重复行上有计数 —— 必须整行搬过去。
	require.NoError(t, gdb.Create(&CategoryCounter{
		UserId: 8, CategoryId: third.Id, HitCount: 1, TotalCount: 1, WindowStart: 300, LastHitAt: 310,
	}).Error)

	archived, err := reconcileCategoryKeyIndex(ctx, gdb)
	require.NoError(t, err)
	assert.EqualValues(t, len(seedCategories)*2, archived, "两套重复应当全部归档")

	// 1) 活行恢复成一套,每个 key 恰好一行,兜底只剩一行。
	var live []Category
	require.NoError(t, gdb.Order("id asc").Find(&live).Error)
	require.Len(t, live, len(seedCategories))
	seen := map[string]int{}
	for _, c := range live {
		seen[c.Key]++
		assert.EqualValues(t, 0, c.ArchiveSeq, "活行的 archive_seq 必须是 0")
	}
	for key, n := range seen {
		assert.Equal(t, 1, n, "key %s 仍有重复活行", key)
	}
	var fallbacks int64
	require.NoError(t, gdb.Model(&Category{}).Where("is_fallback = ?", true).Count(&fallbacks).Error)
	assert.EqualValues(t, 1, fallbacks, "多行 is_fallback 时兜底解析取哪一行是不确定的")

	// 2) 归档行仍在,且 archive_seq 已置为自己的 id(否则同名 key 建不回来)。
	var archivedRows []Category
	require.NoError(t, gdb.Unscoped().Where("deleted_at IS NOT NULL").Find(&archivedRows).Error)
	require.Len(t, archivedRows, len(seedCategories)*2)
	for _, c := range archivedRows {
		assert.Equal(t, c.Id, c.ArchiveSeq, "归档行的 archive_seq 必须是自己的主键")
	}

	// 3) 指向重复行的规则已改绑;指向权威行的没被动过。
	var rules []Rule
	require.NoError(t, gdb.Order("id asc").Find(&rules).Error)
	require.Len(t, rules, 2)
	assert.Equal(t, first.Id, rules[0].CategoryId)
	assert.Equal(t, first.Id, rules[1].CategoryId,
		"指向重复行的规则没有改绑,它的命中会记进一个已归档的类型")

	// 4) 类型计数并进权威行,一次都没少。
	var merged CategoryCounter
	require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 7, first.Id).Take(&merged).Error)
	assert.Equal(t, 5, merged.HitCount, "窗口内次数必须相加(2+3)")
	assert.EqualValues(t, 9, merged.TotalCount, "累计次数必须相加(5+4)")
	assert.EqualValues(t, 200, merged.WindowStart, "窗口取更新的那一边")
	assert.EqualValues(t, 220, merged.LastHitAt)
	var moved CategoryCounter
	require.NoError(t, gdb.Where("user_id = ? AND category_id = ?", 8, first.Id).Take(&moved).Error)
	assert.Equal(t, 1, moved.HitCount, "权威行上没有该用户时应整行搬迁,不能丢")
	var leftover int64
	require.NoError(t, gdb.Model(&CategoryCounter{}).Where("category_id = ?", third.Id).Count(&leftover).Error)
	assert.EqualValues(t, 0, leftover, "重复行上不该再有计数残留")

	// 5) 这才是"重启不再堆叠":对账之后再补种,行数不变。
	require.NoError(t, ensureSeedCategories(ctx, gdb))
	require.NoError(t, ensureSeedCategories(ctx, gdb))
	var after int64
	require.NoError(t, gdb.Model(&Category{}).Count(&after).Error)
	assert.EqualValues(t, len(seedCategories), after,
		"对账之后补种仍然堆叠 —— 唯一索引没有真的被重建")
}

// TestReconcileCategoryKeyIndexIsNoopOnHealthyDatabase 钉住"健康库上什么都不做"。
//
// 对账每次启动都跑,正常库上它必须既不归档任何行、也不重建索引 ——
// 否则 MySQL 上就是每次重启一次 DDL。
func TestReconcileCategoryKeyIndexIsNoopOnHealthyDatabase(t *testing.T) {
	gdb := newCategoryDB(t)
	ctx := context.Background()
	require.NoError(t, ensureSeedCategories(ctx, gdb))

	stale, err := categoryKeyIndexIsStale(ctx, gdb)
	require.NoError(t, err)
	assert.False(t, stale, "按新定义建的表被判成了过时,于是每次重启都会重建一次唯一索引")

	archived, err := reconcileCategoryKeyIndex(ctx, gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 0, archived)

	var n int64
	require.NoError(t, gdb.Model(&Category{}).Count(&n).Error)
	assert.EqualValues(t, len(seedCategories), n)
}

// TestCategoryKeyIndexIsStaleDetectsBothShapes 直接钉住判据本身。
//
// 判据错向任一方向都有代价:漏判 → 缺陷原样留着;误判 → 每次重启一次 DDL。
func TestCategoryKeyIndexIsStaleDetectsBothShapes(t *testing.T) {
	ctx := context.Background()
	legacy := newLegacyCategoryDB(t)
	stale, err := categoryKeyIndexIsStale(ctx, legacy)
	require.NoError(t, err)
	assert.True(t, stale, "(key, deleted_at) 必须被判成过时")

	fresh := newCategoryDB(t)
	stale, err = categoryKeyIndexIsStale(ctx, fresh)
	require.NoError(t, err)
	assert.False(t, stale, "(key, archive_seq) 必须被判成已是新定义")
}

// TestReconcileCategoryKeyIndexKeepsEvidence 与 archiveCategory 同一条纪律:
// 合并重复类型也绝不碰历史违规记录 —— 记录冻结了类型三列,能独立解释自己。
func TestReconcileCategoryKeyIndexKeepsEvidence(t *testing.T) {
	gdb := newLegacyCategoryDB(t)
	ctx := context.Background()
	require.NoError(t, ensureSeedCategories(ctx, gdb))
	require.NoError(t, ensureSeedCategories(ctx, gdb))

	var all []Category
	require.NoError(t, gdb.Order("id asc").Find(&all).Error)
	dup := all[len(seedCategories)+1] // 第二套里的一行,即将被归档

	require.NoError(t, gdb.Create(&Record{
		UserId: 42, CategoryId: dup.Id, CategoryName: dup.Name,
		CategoryPublicTitle: dup.PublicTitle, RuleName: "证据",
	}).Error)

	_, err := reconcileCategoryKeyIndex(ctx, gdb)
	require.NoError(t, err)

	var rec Record
	require.NoError(t, gdb.Where("user_id = ?", 42).Take(&rec).Error)
	assert.Equal(t, dup.Id, rec.CategoryId, "历史记录的类型 id 被改写了 —— 证据必须原样保留")
	assert.Equal(t, dup.Name, rec.CategoryName)
	assert.Equal(t, dup.PublicTitle, rec.CategoryPublicTitle)
	// 被指向的那一行仍然查得到(归档不是删除),否则管理端点开这条记录是一片空白。
	var archivedCat Category
	require.NoError(t, gdb.Unscoped().Where("id = ?", dup.Id).Take(&archivedCat).Error)
	assert.Equal(t, dup.Key, archivedCat.Key)
}
