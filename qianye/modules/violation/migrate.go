package violation

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// migrate.go —— 规则模式(dry_run → mode)的一次性数据迁移。
//
// # 为什么迁移不能按 dry_run 逐条翻译
//
// 现网的实际状态是:YAML 里 violation.shadow_mode = true(两份 data/*.yaml 都是),
// 而叠加语义取更保守者胜 —— 也就是说**线上每一条规则实际上都在影子跑**,
// 不管它自己的 dry_run 是 true 还是 false。规则级 dry_run 因此从来没有被真实
// 检验过:运营在写它的时候,那一位对线上行为没有任何影响。
//
// 删掉全局层之后如果按 dry_run 逐条翻译(dry_run=false → enforce),那批
// **从未真正执行过**的规则会在部署完成的那一秒同时开始扣费与封号。
// 这不是"恢复原状",这是一次没有人按下过的上线,而且后果不可逆:
// 钱扣了要走退款流程,人封了要走申诉流程,两条都是工单。
//
// 所以迁移策略是:**一律置为 shadow,由运营逐条确认后再转真实**。
// 代价写在这里,不糊过去:运营需要人工过一遍规则表。这是唯一一次性的成本,
// 换来的是"没有任何一条规则在没人看过的情况下开始扣钱"。
//
// # 幂等与多节点
//
// 判据是 `mode = ”`(或 NULL)。AutoMigrate 给已有行 ADD COLUMN 时会用
// gorm tag 上的 default 回填,所以正常路径下这条 UPDATE 命中 0 行;它兜的是
// 那些绕过 AutoMigrate 的部署(关掉 auto_migrate、DBA 手工建表、滚动升级期间
// 旧节点插入的行)。命中 0 行不算失败,重复执行也不会改变任何已有取值 ——
// 因此不需要 lease,每个节点启动时各跑一次即可。
//
// 兜底还有第二层:compile 判定 `Mode == ModeEnforce`,任何未被迁移到的行在
// 运行期照样按影子处理。两层都指向同一个方向,漏掉一层不会造成误扣费。
func migrateRuleMode(ctx context.Context, gdb *gorm.DB) (int64, error) {
	if gdb == nil {
		return 0, db.ErrNotReady
	}
	// Unscoped:软删的规则也要迁。它们不会被加载进快照,但管理端复核申诉时会
	// 读到,一个空 mode 在界面上是无法渲染的第三种状态。
	res := gdb.WithContext(ctx).Unscoped().Model(&Rule{}).
		Where("mode IS NULL OR mode = ?", "").
		Update("mode", ModeShadow)
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// runRuleModeMigration 是启动期的调用点,失败只告警不阻断。
//
// 阻断启动没有意义:未迁移的行在运行期本来就按影子处理(compile 的判据),
// 而让主程序起不来才是真的事故。
func runRuleModeMigration() {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	n, err := migrateRuleMode(context.Background(), gdb)
	if err != nil {
		common.SysError("qianye/violation: 规则模式迁移失败(未迁移的规则运行期一律按影子处理): " + err.Error())
		return
	}
	if n > 0 {
		common.SysError(common.MapToJsonStr(map[string]any{
			"msg":   "qianye/violation: 已把没有模式的规则迁移为影子模式,请在管理端逐条确认后再切真实执行",
			"rows":  n,
			"mode":  ModeShadow,
			"scope": "qy_violation_rule",
		}))
	}
}

// ─────────────────── severity 列的删除 ───────────────────

// legacySeverityColumn 是那一列的列名。写成常量是为了让删除语句与探针用的是
// 同一个字符串:两处各写一遍字面量时,改错一处的表现是"迁移每次启动都说自己
// 删了、而列一直在"。
const legacySeverityColumn = "severity"

// dropLegacySeverityColumn 删掉 qy_violation_rule.severity。
//
// # 为什么现在删得掉
//
// severity(1=低 2=中 3=高)从头到尾**只写不读**:不参与判定、不参与排序、
// 不进导出、不进告警,违规类型的阈值与窗口已经完整表达了"这一类有多严重"。
// 上一轮把结构体字段、表单与 API 面撤掉时保留了数据库列,理由是"删列是不可逆
// 迁移,而留一个没人读的列代价为零"。项目方明确本项目尚未上线生产,不可逆
// 这一条的代价因此归零,列也就没有再留的理由。
//
// **它不影响内置规则指纹。** 指纹是 patternFingerprint = sha256(pattern),
// 只覆盖 pattern 一列(见 builtin.go 与 upgradeState)。severity 从来不在里面,
// 所以删它不会让任何一条已导入的内置规则变成 "modified" —— 不需要给指纹算法
// 加版本号,也不会有升级后的一屏假告警。这一点由
// TestBuiltinFingerprintIsPatternOnly 钉住。
//
// # 三家数据库分别发什么语句
//
// 用的是同一条标准 DDL,由 GORM 按方言补引号(clause.Table / clause.Column):
//
//	MySQL       ALTER TABLE `qy_violation_rule` DROP COLUMN `severity`
//	PostgreSQL  ALTER TABLE "qy_violation_rule" DROP COLUMN "severity"
//	SQLite      ALTER TABLE `qy_violation_rule` DROP COLUMN `severity`
//
// MySQL 自 5.x 起、PostgreSQL 自 8.x 起就支持 DROP COLUMN,两家都远低于本仓
// 的下限(5.7.8 / 9.6)。SQLite 的 DROP COLUMN 需要 3.35+;本二进制里编进来的是
// modernc.org/sqlite 的 3.50.4,满足。低于 3.35 的库只可能出现在有人换驱动的
// 情况下,那时这条语句会报语法错 —— 兜底见下。
//
// # 为什么不用 Migrator().DropColumn
//
// 它在 glebarez/sqlite 上**会静默失败**:该驱动不发 DROP COLUMN,而是把建表
// DDL 用正则删掉一段再重建表,正则是 "(`|'|\"| |\\[)severity(`|'|\"| |\\]) .*?,"
// —— 列名与类型之间只有一个空格时(`, severity integer NOT NULL DEFAULT 1,`,
// 也就是 ADD COLUMN 追加出来的那种形状)这个正则匹配不上,替换成了空操作,
// 于是表按原样重建、列原封不动,而 DropColumn 返回 nil。
// 实测确认:HasColumn 在 DropColumn 返回 nil 之后仍然是 true。
// 一条"报告成功却什么都没做"的迁移比不写更糟,所以这里直接发标准 DDL。
//
// # 幂等
//
// 判据是 HasColumn:列已经不在就直接返回 false, nil,重启多少次都是 no-op,
// 对全新建的库(结构体上没有这一列,AutoMigrate 根本不会建它)同样是 no-op。
// 因此不需要 lease,每个节点启动时各跑一次即可。
//
// 先查后删之间有一个 TOCTOU 窗口:多个节点**同时**启动时可能都读到"列还在",
// 于是都发一条 DROP COLUMN,先到的成功、后到的报"列不存在"。这不是故障
// (结果与预期完全一致 —— 列没了),但如果照常走错误分支,滚动升级当天
// 每台后到的机器都会打一条"迁移失败"告警,而运维分不清它和真正的失败。
// 所以出错之后再查一次:列已经不在,就说明有人替我们删掉了,按 no-op 收敛。
// 用重查而不是解析错误串,是因为"列不存在"的措辞三家数据库各不相同,
// 而"列还在不在"这个事实三家都答得一样。
//
// # 尊重 auto_migrate=false
//
// 站点把 database.auto_migrate 关掉,意思是"表结构由 DBA 管,程序别碰"。
// 删列是纯清理、不修任何缺陷(列没人读,留着不会算错任何一笔钱),
// 没有理由为它破例发一条 DDL。这种站点由 DBA 自己执行上面那条语句。
func dropLegacySeverityColumn(ctx context.Context, gdb *gorm.DB) (bool, error) {
	if gdb == nil {
		return false, db.ErrNotReady
	}
	m := gdb.WithContext(ctx).Migrator()
	if !m.HasTable(&Rule{}) {
		return false, nil
	}
	if !m.HasColumn(&Rule{}, legacySeverityColumn) {
		return false, nil
	}
	err := gdb.WithContext(ctx).Exec("ALTER TABLE ? DROP COLUMN ?",
		clause.Table{Name: Rule{}.TableName()},
		clause.Column{Name: legacySeverityColumn}).Error
	if err != nil {
		// 另一个节点抢先删掉了?那就是成功,不是失败(见上面 TOCTOU 那一段)。
		if !gdb.WithContext(ctx).Migrator().HasColumn(&Rule{}, legacySeverityColumn) {
			return false, nil
		}
		db.MarkFailure(err)
		return false, err
	}
	return true, nil
}

// runLegacySeverityColumnDrop 是启动期调用点,失败只告警不阻断。
//
// 阻断启动没有意义:这一列没有任何读点,删不掉的唯一后果就是它继续占着
// 几个字节。让主程序起不来才是真的事故(与 runRuleModeMigration 同口径)。
func runLegacySeverityColumnDrop() {
	if !config.Get().Database.ShouldAutoMigrate() {
		return
	}
	gdb := db.Get()
	if gdb == nil {
		return
	}
	dropped, err := dropLegacySeverityColumn(context.Background(), gdb)
	if err != nil {
		common.SysError("qianye/violation: 删除历史列 qy_violation_rule.severity 失败" +
			"(该列没有任何读点,保留它不影响任何判定): " + err.Error())
		return
	}
	if dropped {
		common.SysError(common.MapToJsonStr(map[string]any{
			"msg":    "qianye/violation: 已删除历史列 qy_violation_rule.severity(只写不读,严重程度改由违规类型阈值表达)",
			"table":  Rule{}.TableName(),
			"column": legacySeverityColumn,
		}))
	}
}
