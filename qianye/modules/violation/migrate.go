package violation

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"

	"gorm.io/gorm"
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
