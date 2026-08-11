package violation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// category.go —— 违规类型:种子、快照、计数、迁移。
//
// 表结构与"内部说明 / 公示文案必须两列""阈值只出线不出动作"两条设计约束写在
// model.go 的 Category 上。这里只做四件事:保证兜底与内置类型存在、把类型表
// 供给热路径、把一次命中累加到类型计数、把存量规则一次性绑到类型上。
//
// # 快照挂在规则快照上,不另建一份缓存
//
// 类型的读取点与规则完全重合(命中当时要冻结类型名、推进计数时要读阈值与窗口),
// 而规则快照已经解决了"热路径只读进程内、多节点靠 rule_version 收敛"这两件事。
// 再建一份独立 TTL 的类型缓存会让同一条命中读到"新规则 + 旧类型"这种组合 ——
// 规则刚被改绑到新类型、类型快照还没刷新时,计数会加到旧类型上,而这种错位
// 在任何日志里都看不出来。所以类型跟着规则版本一起走:写类型也 bump 规则版本。

// ───────────────────────────── 种子 ─────────────────────────────

// seedCategory 是一条内置类型的种子。
type seedCategory struct {
	Key   string
	Name  string
	Desc  string // 内部说明,写匹配口径
	Title string // 对外公示标题
	Pub   string // 对外公示说明:只说"这一类是什么",不说"怎么判的"
}

// seedCategories 是随内置规则包一起出厂的类型。
//
// # 为什么种子必须与 builtinCategories 同键
//
// 内置规则目录里每条规则都声明了自己属于哪一类(builtinRule.Category),那是本仓
// 已经存在的、唯一一份"规则 → 类型"的映射。种子用同一组 key,存量的内置规则
// 就能靠 builtin_key 精确落位(见 migrateRuleCategory),而不是全部堆进「未分类」。
//
// # 公示文案是重写的,不是把 Desc 抄过去
//
// builtinCategories[].Desc 写的是判据("DAN 人格、开发者模式、要求关闭安全过滤"),
// 那是给运营看的;原样公示等于把绕过清单印给用户。Pub 只保留"这一类是什么"。
//
// # 出厂阈值一律 0
//
// 0 = 这一类不单独触发处置。种子绝不能带一个我们替站点决定的次数:阈值一旦
// 出厂就带值,升级上来的站点会在部署完成的那一秒开始按一套没人设定过的线封人。
// 与规则包"导入一律影子"是同一条纪律。
var seedCategories = []seedCategory{
	{FallbackCategoryKey, "未分类", "没有显式归类的规则都落在这里。它是兜底类型,不可删除。", "", ""},
	{CatJailbreak, "破限(越狱)",
		"诱导模型跳出安全策略:DAN 人格、开发者模式、要求关闭安全过滤。",
		"绕过安全策略", "试图让模型跳出既定的安全与合规限制。"},
	{CatReverse, "逆向(套提示词)",
		"套取系统提示词与初始设定:要求复述上文、输出 system prompt。",
		"套取系统设定", "试图套取服务方的系统提示词或初始设定。"},
	{CatDistill, "蒸馏(批量采集)",
		"批量采集模型输出用作训练语料。判据是请求频率,不看文本。",
		"批量采集", "以明显超出正常使用的方式批量采集模型输出。"},
	{CatPressure, "高压(提示词注入)",
		"直接覆盖指令、伪造角色标签、注入控制 token。",
		"指令注入", "试图覆盖或伪造对话中的指令与角色。"},
	{CatUpstream, "上游安全拒绝",
		"请求已发往上游、上游以策略原因拒绝(4xx)。判据是上游的结论,不是我们的猜测。",
		"上游安全拒绝", "请求被模型服务方以内容策略为由拒绝。"},
}

// ensureSeedCategories 补建出厂类型。启动期调用,幂等。
//
// OnConflict DoNothing 而不是 Upsert:Upsert 会在**每次重启**时把管理员改过的
// 公示文案、阈值、是否公示全部覆盖回出厂值。这与 ensureDefaultBanPolicy 的取舍
// 完全一致,理由也一样 —— 种子只负责"从无到有",不负责"保持一致"。
//
// 只有兜底类型出厂即公示为 false 且 Enabled 为 false:「未分类」对用户没有任何
// 信息量,公示出来只会得到一行读不懂的"未分类 0/0"。
func ensureSeedCategories(ctx context.Context, gdb *gorm.DB) error {
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	rows := make([]Category, 0, len(seedCategories))
	for i, s := range seedCategories {
		fallback := s.Key == FallbackCategoryKey
		rows = append(rows, Category{
			Key: s.Key, Name: s.Name, Remark: s.Desc,
			PublicTitle: s.Title, PublicDesc: s.Pub,
			// 兜底类型不公示;其余出厂即公示 —— 项目方要的就是"这些在用户前端要公示出来",
			// 出厂默认不公示会让这个需求在没人去点开关之前一直是假的。
			Published: !fallback,
			// Enabled 是"类型阈值是否生效"。出厂 Threshold 恒为 0,所以这一位取 true
			// 也不会处置任何人;取 true 是为了让管理员填完阈值就生效,
			// 而不是填完发现还有第二个开关没开。
			Enabled:     !fallback,
			WindowHours: 24,
			Threshold:   0,
			SortOrder:   (i + 1) * 10,
			IsFallback:  fallback,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	res := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return res.Error
	}
	return nil
}

// ─────────────────── 唯一索引对账(修复存量库的重复种子) ───────────────────

// categoryKeyIndexName 是 Category.Key + ArchiveSeq 的唯一索引名,与 gorm tag 同值。
const categoryKeyIndexName = "uk_qy_vcat_key"

// reconcileCategoryKeyIndex 把**存量库**上的旧唯一索引换成正确的那一个,
// 并在换之前把已经攒下来的重复活行合并掉。返回合并的行数。
//
// # 为什么必须有这一步:AutoMigrate 不会重建一个已经存在的同名索引
//
// Category.Key 的唯一索引一度写成 (key, deleted_at)。三家数据库都把 NULL 视为
// 互不相等,于是 (spam, NULL) 与 (spam, NULL) 不冲突 —— ensureSeedCategories 的
// OnConflict DoNothing 完全落空,**每次重启都把整套种子再插一遍**。
// model.go 上的 tag 已经改成 (key, archive_seq),但 GORM 的 AutoMigrate 只按
// **索引名**判断存在与否:名字还在,它就什么都不做。因此在修复之前跑过一次的库,
// 索引永远停在旧定义上,而单元测试跑在全新建的 SQLite 表上,一路全绿 ——
// 这正是那个缺陷能活到线上的原因,所以本函数的回归测试必须从**旧定义**建表。
//
// 现网后果(演示站实测 6 → 24 行):用户端公示把同一个类型列 4 次;
// 类型计数以 category_id 为键,同一类型的命中被劈进 4 个桶,单类型阈值被稀释 4 倍;
// 出现 4 行 is_fallback,兜底解析取哪一行不确定。
//
// # 合并方向:留最小 id,其余归档
//
// 最小 id 是第一次种下的那一行,也是存量规则、历史记录实际指着的那一行
// (演示站上 26 条规则全部指向 1..6)。把它留下,改动面最小。
// 重复行走与 archiveCategory 完全相同的三步(改绑规则 → 写 archive_seq → 软删),
// 因此**一行历史记录、一行类型计数都不会被删** —— 归档不是删除这条纪律在这里同样成立。
// 类型计数额外做一次搬迁:(user_id, category_id) 是复合主键,重复行上攒下的计数
// 必须并回权威行,否则"合并完计数清零"会把用户已经攒下的次数悄悄抹掉。
func reconcileCategoryKeyIndex(ctx context.Context, gdb *gorm.DB) (int64, error) {
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	m := gdb.WithContext(ctx).Migrator()
	if !m.HasTable(&Category{}) {
		return 0, nil
	}
	// 只在**确实过时**时动索引:每次启动都 DROP + CREATE 一个唯一索引,
	// 在 MySQL 上就是每次重启一次 DDL。索引已经正确时活行之间不可能有重复,
	// 合并跑了也只会命中 0 行,直接返回。
	stale, err := categoryKeyIndexIsStale(ctx, gdb)
	if err != nil {
		return 0, err
	}
	if !stale {
		return 0, nil
	}

	// ① 先摘掉旧索引。顺序不能反,而且这一点是回归测试当场抓出来的:
	// 归档要写 deleted_at,而旧索引恰恰是 (key, deleted_at) —— 同一秒归档两行
	// 同 key 的重复行,两行的 deleted_at 一模一样,当场撞唯一键,合并整个失败。
	// 先摘索引 → 合并 → 再按新定义建,是唯一走得通的顺序。
	//
	// 中途失败会让表暂时没有这个唯一索引:下次启动重新探针(没有索引 ⇒ 判为过时)
	// → 再走一遍同样三步,自愈。反过来"先建后合并"根本建不出来。
	if m.HasIndex(&Category{}, categoryKeyIndexName) {
		if err := m.DropIndex(&Category{}, categoryKeyIndexName); err != nil {
			db.MarkFailure(err)
			return 0, err
		}
	}
	// ② 合并重复活行。
	merged, err := mergeDuplicateCategories(ctx, gdb)
	if err != nil {
		return 0, err
	}
	// ③ 按新定义建索引。此时活行的 (key, 0) 已经唯一,归档行的 (key, id) 天然唯一。
	if err := m.CreateIndex(&Category{}, categoryKeyIndexName); err != nil {
		db.MarkFailure(err)
		return merged, err
	}
	common.SysError("qianye/violation: 违规类型唯一索引已重建为 (key, archive_seq);" +
		"在此之前每次重启都会重复插入一整套出厂类型")
	return merged, nil
}

// errCategoryIndexProbe 是探针事务的回滚哨兵,永远不会逃出 categoryKeyIndexIsStale。
var errCategoryIndexProbe = errors.New("qy: category key index probe rollback")

// categoryKeyIndexIsStale 报告"这张表现在还允不允许两个同 key 的活行"。
//
// # 判据是那条性质本身,不是索引的列名
//
// 直觉写法是读出索引定义、比对列是不是 (key, archive_seq)。那是一个**代理判据**:
// 它假设"列对了 ⇒ 约束就成立",而这个缺陷的全部教训恰恰是代理判据会骗人 ——
// 当初 (key, deleted_at) 的列名看着也很合理。而且 GORM 的 GetIndexes 在
// glebarez/sqlite 上直接返回 "not support",于是代理判据在 SQLite 上只能回落到
// "看不清就重建",表现是每次启动都重建一次索引。
//
// 这里改成直接问那件事:在一个**永远回滚**的事务里插两行同 key、archive_seq 都为 0
// 的哨兵。第二行被拒 ⇒ 约束成立(索引已是新定义);两行都插得进 ⇒ 活行之间根本
// 没有唯一性,正是要修的那个状态。哨兵 key 带前后双下划线,不与任何业务键相撞;
// 事务必然回滚,一行都不会留在表里。
//
// PostgreSQL 上一条语句报错会让整个事务进入 aborted 状态 —— 因此探针在拿到
// 第二次 Create 的结果之后立刻返回哨兵错误回滚,不再发任何语句。
func categoryKeyIndexIsStale(ctx context.Context, gdb *gorm.DB) (bool, error) {
	const probeKey = "__qy_vcat_uk_probe__"
	now := common.GetTimestamp()
	row := func() *Category {
		return &Category{Key: probeKey, Name: probeKey, ArchiveSeq: 0, CreatedAt: now, UpdatedAt: now}
	}

	stale := false
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(row()).Error; err != nil {
			// 第一行都插不进 —— 与唯一索引无关(列宽、权限、表不存在),原样上报。
			return err
		}
		// 第二行:约束成立时这里必须失败。
		stale = tx.Create(row()).Error == nil
		return errCategoryIndexProbe
	})
	if errors.Is(err, errCategoryIndexProbe) {
		return stale, nil
	}
	if err != nil {
		db.MarkFailure(err)
		return false, err
	}
	// 事务没有按预期回滚:宁可什么都不做,也不能在状态不明时去重建唯一索引。
	return false, fmt.Errorf("违规类型唯一索引探针未按预期回滚")
}

// mergeDuplicateCategories 把 key 相同的多个活行合并成一个,返回被归档的行数。
//
// 健康库上这是一次 GROUP BY 扫描,命中 0 行直接返回 —— 类型表规模是几十行。
func mergeDuplicateCategories(ctx context.Context, gdb *gorm.DB) (int64, error) {
	var live []Category
	// 只按 id 升序取活行;不用 GROUP BY HAVING,因为 `key` 是三家方言的保留字,
	// 拿它写原生 GROUP BY 要各写一遍引号(见 AGENTS.md 的 commonKeyCol 一节)。
	// 类型表只有几十行,全量取回来在内存里分组更简单也更可移植。
	if err := gdb.WithContext(ctx).Order("id asc").Find(&live).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	canonical := make(map[string]int64, len(live))
	dups := make(map[int64]int64) // 重复行 id → 权威行 id
	for _, c := range live {
		keep, ok := canonical[c.Key]
		if !ok {
			canonical[c.Key] = c.Id
			continue
		}
		dups[c.Id] = keep
	}
	if len(dups) == 0 {
		return 0, nil
	}

	var archived int64
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for dupId, keepId := range dups {
			// 1) 规则改绑。Unscoped:软删的规则也要跟着走,理由同 archiveCategory。
			if err := tx.Unscoped().Model(&Rule{}).Where("category_id = ?", dupId).
				Update("category_id", keepId).Error; err != nil {
				return err
			}
			// 2) 类型计数搬迁。权威行上已有同一个用户的计数行时不能直接改主键
			//    (会撞复合主键),把两边的次数并进权威行再删掉重复行那一条。
			if err := mergeCategoryCounters(tx, dupId, keepId); err != nil {
				return err
			}
			// 3) 归档:先写 archive_seq 再软删,顺序理由见 archiveCategory。
			if err := tx.Model(&Category{}).Where("id = ?", dupId).
				Update("archive_seq", dupId).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", dupId).Delete(&Category{}).Error; err != nil {
				return err
			}
			archived++
		}
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	common.SysError(common.MapToJsonStr(map[string]any{
		"msg":      "qianye/violation: 已合并重复的违规类型活行(旧唯一索引导致每次重启重复插入出厂类型)",
		"archived": archived,
		"note":     "历史违规记录、证据、封禁行一行未动;规则与类型计数已并入保留下来的那一行",
	}))
	return archived, nil
}

// mergeCategoryCounters 把 dupId 上的类型计数并进 keepId。
//
// 两边都有同一个用户时取"窗口更新的那一边"的窗口状态,次数相加:
// 反方向(丢掉其中一边)会让用户已经攒下的次数凭空少掉,而少掉的方向是"更难被处置"。
func mergeCategoryCounters(tx *gorm.DB, dupId, keepId int64) error {
	var rows []CategoryCounter
	if err := tx.Where("category_id = ?", dupId).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		var keep CategoryCounter
		err := tx.Where("user_id = ? AND category_id = ?", row.UserId, keepId).Take(&keep).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			// 权威行上没有这个用户 → 直接改挂过去。
			if err := tx.Model(&CategoryCounter{}).
				Where("user_id = ? AND category_id = ?", row.UserId, dupId).
				Update("category_id", keepId).Error; err != nil {
				return err
			}
			continue
		}
		merged := CategoryCounter{
			WindowStart: keep.WindowStart,
			HitCount:    keep.HitCount + row.HitCount,
			TotalCount:  keep.TotalCount + row.TotalCount,
			LastHitAt:   keep.LastHitAt,
			UpdatedAt:   common.GetTimestamp(),
		}
		if row.WindowStart > merged.WindowStart {
			merged.WindowStart = row.WindowStart
		}
		if row.LastHitAt > merged.LastHitAt {
			merged.LastHitAt = row.LastHitAt
		}
		if err := tx.Model(&CategoryCounter{}).
			Where("user_id = ? AND category_id = ?", row.UserId, keepId).
			Updates(map[string]any{
				"window_start": merged.WindowStart,
				"hit_count":    merged.HitCount,
				"total_count":  merged.TotalCount,
				"last_hit_at":  merged.LastHitAt,
				"updated_at":   merged.UpdatedAt,
			}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? AND category_id = ?", row.UserId, dupId).
			Delete(&CategoryCounter{}).Error; err != nil {
			return err
		}
	}
	return nil
}

// ───────────────────────────── 快照读取 ─────────────────────────────

// categoryForRule 给出一条规则实际生效的违规类型,**永不返回孤儿**。
//
// 三种输入都折进兜底类型:id 为 0(从未绑过)、id 指向一个已归档的类型、
// 快照里根本没有这个 id(类型刚建、本节点还没刷新到)。折叠的方向只能是兜底,
// 不能是"不计数":后者会让一次归档静默关掉一整批规则的类型计数,
// 而这件事没有任何报错、没有任何日志,只有几天后"他怎么没被封"才会被发现。
//
// 兜底类型也不在快照里时返回零值 Category(Id=0):此时 bumpCategoryCounter 直接跳过,
// 账号总量线照常工作。这是扩展库刚起来、种子还没落地的那几毫秒,fail-open 与
// 本模块其余部分同口径。
func categoryForRule(s *snapshot, categoryId int64) Category {
	if s == nil {
		return Category{}
	}
	if categoryId > 0 {
		if c, ok := s.catById[categoryId]; ok {
			return c
		}
	}
	return s.catFallback
}

// CategoryByKey 是**模块外**(AI 内容审核等新的审核来源)绑定违规类型的唯一入口。
//
// 用 key 而不是 id:id 是自增主键,在不同站点上是不同的数字,而审核来源要写死的是
// "这次命中算破限"这个语义。key 在类型的整个生命周期里不变(改名改的是 Name)。
//
// 返回 false 表示这个 key 当前不存在或已归档 —— 调用方应当落到自己的兜底类型上
// (或直接传 0,由 categoryForRule 折进「未分类」),绝不要因此丢掉这条命中。
func CategoryByKey(key string) (Category, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return Category{}, false
	}
	s := Snapshot()
	if s == nil {
		return Category{}, false
	}
	c, ok := s.catByKey[key]
	return c, ok
}

// ApplyCategory 把一个违规类型冻结进一条待写入的记录。
//
// 导出是给模块外的审核来源用的:Record 上有三列类型信息(id + 内部名 + 公示文案),
// 只写 category_id 而漏掉后两列,历史记录在类型被归档之后就再也解释不了自己 ——
// 那正是这三列存在的理由。一个函数写全三列,漏写就不可能发生。
func ApplyCategory(rec *Record, categoryId int64) {
	if rec == nil {
		return
	}
	c := categoryForRule(Snapshot(), categoryId)
	rec.CategoryId = c.Id
	rec.CategoryName = truncate(c.Name, 64)
	rec.CategoryPublicTitle = truncate(c.PublicTitle, 64)
}

// categoryIdForBuiltin 把内置目录里的类别名解析成一个类型 id。
//
// 查不到同名类型(运营把它归档了、或者种子没落地)就回落兜底类型;兜底也查不到
// 就返回 0,由 categoryForRule 在运行期折叠。导入一条内置规则**绝不能**因为
// 类型解析失败而失败:那会让"一键导入防护规则包"这条路径依赖一张与它无关的表。
func categoryIdForBuiltin(gdb *gorm.DB, catKey string) int64 {
	if gdb == nil {
		return 0
	}
	var cat Category
	if catKey != "" {
		if err := gdb.Where(clause.Eq{Column: clause.Column{Name: "key"}, Value: catKey}).
			Take(&cat).Error; err == nil {
			return cat.Id
		}
	}
	var fallback Category
	if err := gdb.Where("is_fallback = ?", true).Take(&fallback).Error; err != nil {
		return 0
	}
	return fallback.Id
}

// ───────────────────────────── 计数 ─────────────────────────────

// bumpCategoryCounter 把一次真实命中累加到 (用户, 类型) 的滚动窗口计数,
// 并回答"推进之后是否已达该类型的阈值"。
//
// # 调用方只有一处,且必须先排除影子命中
//
// 与 bumpCounter 完全同口径(persistRecord)。往这张表里写一次影子命中,
// 就等于让"不会真实执行"变成"延迟几分钟之后真实执行"。
//
// # 为什么是两条语句而不是一条 upsert
//
// bumpCounter 用的是 MySQL 专有的 `INSERT ... ON DUPLICATE KEY UPDATE` +
// `IF(window_start < ?, ...)`,窗口过期判断塞在同一条语句里。那条语句在 SQLite 上
// 连语法都过不了,于是"计数到底变成几"这条最核心的判据在本仓从来没有被真正执行过 ——
// 只能靠断言纯函数间接覆盖。
//
// 这里改成两条可移植语句:
//   - 先做一次**条件 UPDATE** 把过期窗口清零(WHERE window_start < 起点,原子);
//   - 再做一次 GORM OnConflict 累加(MySQL 译成 ON DUPLICATE KEY UPDATE,
//     SQLite/PostgreSQL 译成 ON CONFLICT DO UPDATE,赋值表达式里的裸列名
//     在三种数据库上都指向"已存在的那一行")。
//
// 两条之间的竞态是良性的:另一个节点在中间插入或同样重置窗口,第二条语句照样
// 把两次权重都加上去,结果与串行执行一致。用两条语句换来的是这条路径能在测试里
// 真的跑一遍 —— 计数累加是本轮需求的核心,它必须被执行过,而不是被推理过。
//
// 全程在一个事务里:第二条语句对该行加了排他锁并持有到提交,紧随其后的读必然
// 读到本次推进的结果,而不是别人已经推进过的值(与 bumpCounter 的理由相同,
// 同样刻意不用 LAST_INSERT_ID())。
func bumpCategoryCounter(ctx context.Context, gdb *gorm.DB, userId int, cat Category, weight int) (int, bool, error) {
	if cat.Id <= 0 || weight <= 0 {
		return 0, false, nil
	}
	if gdb == nil {
		return 0, false, db.ErrNotReady
	}
	windowHours := cat.WindowHours
	if windowHours <= 0 {
		windowHours = 24
	}
	now := common.GetTimestamp()
	winFrom := now - int64(windowHours)*3600

	hit := 0
	err := gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&CategoryCounter{}).
			Where("user_id = ? AND category_id = ? AND window_start < ?", userId, cat.Id, winFrom).
			Updates(map[string]any{"hit_count": 0, "window_start": now, "updated_at": now}).Error; err != nil {
			return err
		}
		row := CategoryCounter{
			UserId: userId, CategoryId: cat.Id,
			WindowStart: now, HitCount: weight, TotalCount: int64(weight),
			LastHitAt: now, UpdatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}, {Name: "category_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"hit_count":   gorm.Expr("hit_count + ?", weight),
				"total_count": gorm.Expr("total_count + ?", weight),
				"last_hit_at": now,
				"updated_at":  now,
			}),
		}).Create(&row).Error; err != nil {
			return err
		}
		var back CategoryCounter
		if err := tx.Where("user_id = ? AND category_id = ?", userId, cat.Id).Take(&back).Error; err != nil {
			return err
		}
		hit = back.HitCount
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return 0, false, err
	}
	return hit, categoryReached(cat, hit), nil
}

// categoryReached 判断类型计数是否已经越过这一类自己的线。
//
// 判据与 reachedThreshold 同形("已达"而不是"恰好跨越",理由见 counterState.Reached),
// 外加一层:Enabled=false 的类型等价于 Threshold=0,即"这一类不单独触发处置"。
// **不是**"这一类不计数" —— 计数照常累加,停用只是把线撤掉。这个区分很实在:
// 管理员把一类的阈值临时撤掉去观察,重新打开时看到的是这段时间真实攒下来的数,
// 而不是一段空白。
func categoryReached(cat Category, after int) bool {
	if !cat.Enabled || cat.Threshold <= 0 {
		return false
	}
	return after >= cat.Threshold
}

// revertCategoryCounter 在管理员撤销违规记录时回退类型计数。
//
// 与 revertCounter 逐字同构,包括那条 window_start 相等条件:窗口已经滚过就不回退,
// 那个计数值已经失效,强行减会把当前窗口的合法计数扣掉。
// 两个计数器必须一起回退 —— 只退一个的话,撤销一条误判记录之后用户在另一条线上
// 仍然背着这一次,而管理端界面上没有任何地方显示这个差额。
func revertCategoryCounter(userId int, categoryId int64, weight int, windowStart int64) error {
	if weight <= 0 || categoryId <= 0 {
		return nil
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	return gdb.Exec(`UPDATE qy_violation_cat_counter
		SET hit_count = CASE WHEN hit_count - ? < 0 THEN 0 ELSE hit_count - ? END,
		    total_count = CASE WHEN total_count - ? < 0 THEN 0 ELSE total_count - ? END,
		    updated_at = ?
		WHERE user_id = ? AND category_id = ? AND window_start = ?`,
		weight, weight, weight, weight, common.GetTimestamp(), userId, categoryId, windowStart).Error
}

// revertHitCounters 把一条被撤销的违规记录**推进过的两个计数**一起退回去。
//
// # 为什么是一个函数而不是调用点上的两段
//
// 计数从一条(账号总量)变成两条(账号总量 + 单类型)之后,"撤销要退哪些计数"
// 成了一个有内容的业务问题,而它只有一个正确答案:**推进过几个就退几个**。
// 散在调用点上的两段代码里,漏掉后加的那一条不会有任何症状 —— 接口照常 200、
// 记录照常变成 revoked、账号总量线照常回退,只有"离该类型封号还差几次"这个
// 公示给用户看的数字悄悄少了一次,而它要等到有人被封的那一刻才会暴露。
// 收成一个函数之后,这条不变量有了唯一的落点,也才测得到
// (TestRevertHitCountersRevertsBothLines)。
//
// 两条都带各自的 window_start 相等条件:窗口已经滚过就不退 —— 那个计数值已经
// 失效,强行减会把当前窗口的合法计数扣掉,反而放过真正的违规用户。
//
// 影子记录 Counted 恒为 false、权重可能为 0,两者任一成立就一个字节都不动:
// 它们从来没有推进过任何计数,退一次就是凭空做减法。
func revertHitCounters(gdb *gorm.DB, rec *Record) {
	if gdb == nil || rec == nil || !rec.Counted || rec.CountWeight <= 0 {
		return
	}
	var counter Counter
	if err := gdb.Where("user_id = ?", rec.UserId).Take(&counter).Error; err == nil {
		if e := revertCounter(rec.UserId, rec.CountWeight, counter.WindowStart); e != nil {
			common.SysError("qianye/violation: 撤销时回退计数失败: " + e.Error())
		}
	}
	if rec.CategoryId <= 0 {
		return
	}
	var catCounter CategoryCounter
	if err := gdb.Where("user_id = ? AND category_id = ?", rec.UserId, rec.CategoryId).
		Take(&catCounter).Error; err == nil {
		if e := revertCategoryCounter(rec.UserId, rec.CategoryId, rec.CountWeight, catCounter.WindowStart); e != nil {
			common.SysError("qianye/violation: 撤销时回退违规类型计数失败: " + e.Error())
		}
	}
}

// ───────────────────────────── 归档 ─────────────────────────────

// archiveCategory 归档一个类型:先把它下面的规则改绑到 targetId,再软删这一行。
// 返回改绑的规则条数。
//
// # 顺序不能反,而且必须在同一个事务里
//
// 先软删再改绑的话,两步之间进程崩溃会留下"类型没了、规则还指着它"的状态。
// 那个状态不会报错(categoryForRule 会把它们折进兜底),但管理端列表上这批规则
// 显示的是一个查不到的 category_id,而运营唯一能做的就是逐条重新选一遍。
//
// # 这里绝不碰 qy_violation_record 与 qy_violation_cat_counter
//
// 历史记录是证据:申诉复核、退款争议、"这个账号为什么被封"全部依赖它,而且每一行
// 都冻结了类型 id / 内部名 / 公示文案,归档之后仍能独立解释自己。类型计数是历史事实,
// 而且类型随时可能被恢复。级联删除这两张表里的任何一行,都是把一次"归档"
// 变成一次不可逆的证据销毁 —— TestArchiveCategoryNeverTouchesEvidence 从源码层面
// 钉住这一点。
func archiveCategory(gdb *gorm.DB, id, targetId int64) (int64, error) {
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	var moved int64
	err := gdb.Transaction(func(tx *gorm.DB) error {
		// Unscoped:软删的规则也要改绑,理由与 migrateRuleCategory 一致 ——
		// 它们不进快照,但管理端复核申诉时会读到。
		res := tx.Unscoped().Model(&Rule{}).Where("category_id = ?", id).
			Update("category_id", targetId)
		if res.Error != nil {
			return res.Error
		}
		moved = res.RowsAffected
		// archive_seq 必须在软删**之前**写:它是 key 唯一索引的第二列,
		// 把这一行从"活着的 (key, 0)"挪到"归档的 (key, id)",于是同名 key
		// 立刻可以再建一个,而活行之间的唯一性不受影响(见 Category.ArchiveSeq)。
		// 顺序反过来的话第二条语句会被软删作用域挡住,archive_seq 永远留在 0,
		// 归档行与新建的同名行当场撞唯一键。
		if err := tx.Model(&Category{}).Where("id = ?", id).
			Update("archive_seq", id).Error; err != nil {
			return err
		}
		// 软删:gorm.DeletedAt 置位。历史记录、类型计数、审计一律不动。
		return tx.Where("id = ?", id).Delete(&Category{}).Error
	})
	if err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	return moved, nil
}

// ───────────────────────────── 校验 ─────────────────────────────

const (
	// categoryKeyMax 等必须与 Category 的 gorm tag 同值,
	// 由 TestCategoryVarcharLimitsMatchColumnTags 双向钉住(与 Rule 那次 Error 1406 同形)。
	categoryKeyMax         = 64
	categoryNameMax        = 64
	categoryRemarkMax      = 512
	categoryPublicTitleMax = 64
	categoryPublicDescMax  = 512
	// maxCategoryWindowHours / maxCategoryThreshold 与策略档同口径,理由见
	// maxPolicyWindowHours:上界挡的不是"太长",是 now - hours*3600 溢出成远古时间戳
	// 导致窗口判据恒为真、计数永不过期。
	maxCategoryWindowHours = maxPolicyWindowHours
	maxCategoryThreshold   = maxPolicyThreshold
)

// categoryVarcharLimits 是写入前的长度校验表,一行对应 model.go 里的一个 varchar 列。
// 形状与 ruleVarcharLimits 完全一致,理由见那里的长注释(SQLite 不校验长度,
// 同一份数据在 SQLite 上存得进、迁到 MySQL 整条 INSERT 失败)。
var categoryVarcharLimits = []struct {
	Field string
	Label string
	Max   int
	Get   func(*Category) string
}{
	{"Key", "类型标识", categoryKeyMax, func(c *Category) string { return c.Key }},
	{"Name", "类型名称", categoryNameMax, func(c *Category) string { return c.Name }},
	{"Remark", "内部说明", categoryRemarkMax, func(c *Category) string { return c.Remark }},
	{"PublicTitle", "公示标题", categoryPublicTitleMax, func(c *Category) string { return c.PublicTitle }},
	{"PublicDesc", "公示说明", categoryPublicDescMax, func(c *Category) string { return c.PublicDesc }},
}

// validateCategory 校验一个违规类型。管理端写入前调用。
func validateCategory(cat *Category) error {
	cat.Key = strings.ToLower(strings.TrimSpace(cat.Key))
	cat.Name = strings.TrimSpace(cat.Name)
	cat.PublicTitle = strings.TrimSpace(cat.PublicTitle)

	if cat.Key == "" {
		return fmt.Errorf("类型标识不能为空")
	}
	// 只放行 [a-z0-9_-]:key 会被外部审核来源写死在代码里(见 CategoryByKey),
	// 也会出现在导出的 CSV 与审计快照里。允许空格与中文会让"同一个类型"在不同
	// 调用方手上变成两个字符串,而这种不一致的表现是"AI 审核的命中一条都没落到类型上"。
	for _, r := range cat.Key {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("类型标识只能包含小写字母、数字、下划线与连字符,当前为 %q", cat.Key)
	}
	if cat.Name == "" {
		return fmt.Errorf("类型名称不能为空")
	}
	// 公示了却没有对外标题 = 用户端会看到一行空白。内部名不是它的替代品:
	// 内部名常含代号("破限 v3 高危"),那正是不该给用户看的东西。
	if cat.Published && cat.PublicTitle == "" {
		return fmt.Errorf("勾选了对用户公示时,必须填写公示标题(不能用内部名称代替)")
	}
	if cat.WindowHours < 1 || cat.WindowHours > maxCategoryWindowHours {
		return fmt.Errorf("统计窗口必须在 1..%d 小时之间,当前为 %d", maxCategoryWindowHours, cat.WindowHours)
	}
	if cat.Threshold < 0 || cat.Threshold > maxCategoryThreshold {
		return fmt.Errorf("次数阈值必须在 0..%d 之间(0 表示这一类不单独触发处置),当前为 %d",
			maxCategoryThreshold, cat.Threshold)
	}
	for _, lim := range categoryVarcharLimits {
		if n := utf8.RuneCountInString(lim.Get(cat)); n > lim.Max {
			return fmt.Errorf("%s过长(%d 字,上限 %d 字)", lim.Label, n, lim.Max)
		}
	}
	return nil
}

// ───────────────────────────── 存量迁移 ─────────────────────────────

// migrateRuleCategory 把存量规则一次性绑到违规类型上。
//
// # 迁移策略:内置规则精确落位,手写规则进「未分类」
//
// 内置规则包导入出来的行带着 builtin_key,而内置目录里每条规则都声明了自己属于
// 哪一类(builtinRule.Category)—— 这是本仓已经存在的、唯一一份可信的"规则 → 类型"
// 映射,不用它就是白白把几十条已经归好类的规则倒进「未分类」。
// 手写规则没有任何可推断的依据(名字是自由文本,拿它做正则匹配是在猜),
// 所以一律进「未分类」,由运营自己归类。
//
// # 为什么「未分类」的阈值必须是 0
//
// 迁移完成的那一秒,全站每一条规则都绑上了类型,类型线随即开始参与封号判定。
// 如果「未分类」带一个正数阈值,这就是一次没有人按下过的上线:一批用户会在
// 部署完成后的几分钟内因为一条**从来没有人配置过**的线被处置。
// 阈值 0 表示这一类不单独触发处置 —— 于是迁移之后的行为与迁移之前**逐字节相同**,
// 账号总量线(BanPolicy)仍然是唯一的封号判据。这与 migrateRuleMode 一律置 shadow
// 是同一条纪律:迁移只搬结构,绝不顺手改变谁会被处置。
//
// # 幂等与多节点
//
// 判据是 `category_id = 0`,命中 0 行不算失败,重复执行不改变任何已有取值,
// 因此不需要 lease。运行期还有第二层:categoryForRule 把 0 折进兜底类型,
// 所以哪怕这次迁移完全没跑到,也不会出现"不计数的规则"。
func migrateRuleCategory(ctx context.Context, gdb *gorm.DB) (int64, error) {
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	var cats []Category
	if err := gdb.WithContext(ctx).Find(&cats).Error; err != nil {
		db.MarkFailure(err)
		return 0, err
	}
	byKey := make(map[string]int64, len(cats))
	var fallbackId int64
	for _, c := range cats {
		byKey[c.Key] = c.Id
		if c.IsFallback {
			fallbackId = c.Id
		}
	}
	if fallbackId <= 0 {
		return 0, fmt.Errorf("「未分类」兜底类型不存在,规则绑定迁移已跳过")
	}

	// 内置规则按 builtin_key 分组落位。一次一类,IN 列表长度受内置目录规模限制
	// (当前 30 条上下),远低于 SQLite 999 个占位符的下限。
	keysByCat := map[string][]string{}
	for _, b := range builtinCatalog {
		keysByCat[b.Category] = append(keysByCat[b.Category], b.Key)
	}
	var moved int64
	for catKey, builtinKeys := range keysByCat {
		catId, ok := byKey[catKey]
		if !ok || catId <= 0 {
			continue
		}
		sort.Strings(builtinKeys) // 稳定的 SQL,便于比对慢日志
		// Unscoped:软删的规则也要绑。它们不进快照,但管理端复核申诉时会读到,
		// 一个 category_id=0 在界面上是渲染不出来的第三种状态(与 migrateRuleMode 同理)。
		res := gdb.WithContext(ctx).Unscoped().Model(&Rule{}).
			Where("category_id = ? AND source = ? AND builtin_key IN ?", 0, SourceBuiltin, builtinKeys).
			Update("category_id", catId)
		if res.Error != nil {
			db.MarkFailure(res.Error)
			return moved, res.Error
		}
		moved += res.RowsAffected
	}

	res := gdb.WithContext(ctx).Unscoped().Model(&Rule{}).
		Where("category_id = ? OR category_id IS NULL", 0).
		Update("category_id", fallbackId)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return moved, res.Error
	}
	return moved + res.RowsAffected, nil
}

// runCategoryMigration 是启动期的调用点,失败只告警不阻断。
//
// 阻断启动没有意义:未绑定的规则在运行期由 categoryForRule 折进兜底类型,
// 计数照常工作;而让主程序起不来才是真的事故(与 runRuleModeMigration 同口径)。
func runCategoryMigration() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	// 唯一索引对账必须排在补种之前:旧索引还在的库上,补种就是又插一整套重复类型。
	// 失败不阻断 —— 重复行的后果是公示重复与计数被稀释,而让主程序起不来更糟。
	if _, err := reconcileCategoryKeyIndex(context.Background(), gdb); err != nil {
		common.SysError("qianye/violation: 违规类型唯一索引对账失败(出厂类型可能重复,公示会重复展示): " + err.Error())
	}
	if err := ensureSeedCategories(context.Background(), gdb); err != nil {
		common.SysError("qianye/violation: 违规类型种子补建失败(规则将回落到「未分类」): " + err.Error())
		return
	}
	n, err := migrateRuleCategory(context.Background(), gdb)
	if err != nil {
		common.SysError("qianye/violation: 规则类型绑定迁移失败(未绑定的规则运行期一律按「未分类」计数): " + err.Error())
		return
	}
	if n > 0 {
		common.SysError(common.MapToJsonStr(map[string]any{
			"msg":   "qianye/violation: 已把没有类型的规则绑定到违规类型,内置规则按内置目录落位,其余进「未分类」",
			"rows":  n,
			"scope": "qy_violation_rule",
			"note":  "「未分类」阈值为 0,迁移不改变任何用户的封号判定",
		}))
	}
}
