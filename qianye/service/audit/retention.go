package audit

// retention.go —— audit.retention_days 的消费方:qy_audit_logs 的过期行清理。
//
// # 为什么这个文件必须存在
//
// 在它之前,audit.retention_days 定义了、有默认值、写进了示例 YAML、
// qianye/model/audit_log.go 的注释还写着"除按 retention_days 归档",
// 但全仓没有任何代码读它 —— 启动自检每次都把它打成"无消费方"。
// 一条每次启动都出现、却没人能修的告警,会训练运维忽略整套自检输出,
// 而自检正是本扩展为"纯函数写对了、调度层没接上"这个形状建立的主要防线。
//
// # 这里刻意保守
//
// qy_audit_logs 是这套资金系统事后仲裁的唯一凭据。允许按天删它,就等于允许
// 一次 `retention_days: 7` 的手滑抹掉纠纷证据。因此:
//
//   - 0(默认)= 永久保留,与本扩展上线以来的实际行为逐位一致,升级不改变
//     任何现存部署的行为;
//   - > 0 时必须不小于 config.MinAuditRetentionDays,该下限在配置加载时校验、
//     低于它直接拒绝启动(见 qianye/config/validate.go 的 validateAudit)。
//     pruneExpired 里再判一次是防御纵深:手改数据库/绕过 Load 直接塞 current
//     快照的路径不该能让删除生效。

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

// PruneInterval 是清理任务的运行周期。
//
// 与 twophase.prune_outbox 取同一量级:保留期以**年**计,清理的紧迫性极低,
// 跑得频繁只会白白抢扩展库的 IO。
const PruneInterval = 6 * time.Hour

const (
	// pruneScanSize 是一次主键窗口扫描的行数,也是单条 DELETE 的行数上限。
	pruneScanSize = 500
	// pruneMaxWindows 给单轮工作量封顶(500 × 200 = 10 万行/轮)。
	// 封顶不是为了"删得慢",是为了让每一轮都能在租约续期内结束:
	// 失去租约后还在删,就是与接管节点双跑。
	pruneMaxWindows = 200
	// prunePause 是窗口之间的让路间隔,避免清理任务独占扩展库的写入能力。
	prunePause = 50 * time.Millisecond
)

// pruneRow 只取判定所需的两列。
// 不 SELECT 整行:审计行带两段 text 快照,拉回来只为了看一眼时间戳是纯浪费。
type pruneRow struct {
	Id        int64
	CreatedAt int64
}

// Prune 删除超过 audit.retention_days 的审计行。
//
// 由 lease.Run("audit.prune", ...) 调度(见 qianye/bootstrap.go)。走租约而不是
// common.IsMasterNode:后者只是 NODE_TYPE 这个环境变量,多节点可能都被配成 master,
// 那会让几个节点同时删同一批行。
//
// 本函数只负责"取配置、取句柄、记账",判定与删除全在 pruneExpired ——
// 拆开的理由与 availability/flush.go 的 runCleanup/deleteBefore 相同:
// 真正要守的那几条(0 不是"删光"、下限不可绕过、必须分批)只有让数据库
// 真的执行一遍才验得了,而 db.Get() 的句柄在包外无法注入。
func Prune(ctx context.Context) {
	gdb := db.Get()
	if gdb == nil {
		return
	}
	days := config.Get().Audit.RetentionDays
	now := common.GetTimestamp()
	for _, target := range pruneTargets {
		// 句柄在进入 pruneExpired 之前就绑上 ctx:租约一丢 ctx 即 cancel,
		// 正在等连接池/等锁的那条语句也要跟着断,而不是只在下一个窗口开头才发现。
		deleted, err := pruneExpired(ctx, gdb.WithContext(ctx), target.model, days, now)
		if err != nil {
			db.MarkFailure(err)
			common.SysError("qianye: 审计清理失败(" + target.label + "): " + err.Error())
			// 不 return:两张表互不依赖,一张扫不动不该连累另一张。
			continue
		}
		if deleted > 0 {
			// 删除审计凭据本身是需要留痕的动作,而它显然不能记进正在被删的那张表。
			common.SysLog(fmt.Sprintf(
				"qianye: 已清理 %s 的 %d 行过期记录(保留期 %d 天)", target.label, deleted, days))
		}
	}
}

// pruneTarget 是一张受同一个保留期管辖的审计表。
type pruneTarget struct {
	label string
	// model 只用于让 GORM 解析表名与主键。这里存零值实例而不是构造函数:
	// Find 写的是另一个目标切片,Delete 读到的主键是零值(因此不会额外拼条件),
	// 两者都不会写回这个实例,复用是安全的。
	model any
}

// pruneTargets 列出全部按 audit.retention_days 清理的表。
//
// qy_request_audits 与 qy_audit_logs 共用同一个保留期,不另立配置项:
// 再开一个 request_retention_days 就是把"审计留多久"这件事拆成两个可以互相
// 矛盾的答案,而运维只会记住其中一个。共用的代价是请求台账也享受 365 天硬下限
// (它的体积是资金审计的几十倍),这是刻意选的方向 —— 台账留得太久只是磁盘问题,
// 留得太短是"当时到底谁调了这个接口"永远查不到。
var pruneTargets = []pruneTarget{
	{label: "qy_audit_logs", model: &qymodel.AuditLog{}},
	{label: "qy_request_audits", model: &qymodel.RequestAudit{}},
}

// pruneExpired 删除 created_at 早于 now-days 的审计行,返回实际删除的行数。
//
// # 为什么按主键开窗而不是一条 DELETE WHERE created_at < ?
//
// 两个理由,都不是理论风险:
//
//  1. 一条 DELETE 干掉上百万行会长时间持有行锁、撑爆 binlog,把同一个库上的
//     其它扩展功能(资金补偿、提现对账)一起拖死;
//  2. qy_audit_logs 上**没有** created_at 的单列索引 —— 现有三个组合索引都以
//     category / actor_user_id / target_user_id 打头。裸 `WHERE created_at < ?`
//     是一次全表扫描,而按 `id > cursor ORDER BY id LIMIT n` 开窗走的是主键
//     range scan,每一窗的代价都是有界且可预测的。
//
// 窗口内逐行比对 created_at 而不是整窗一起删:id 与 created_at 的单调关系来自
// "自增主键 + 写入时取当前时间",多节点时钟漂移会让这个关系在局部反转。
// 逐行判定意味着漂移最多让某几行**多留一轮**,永远不会让它们早删 —— 对仲裁
// 凭据来说,这是唯一可接受的失败方向。
func pruneExpired(ctx context.Context, gdb *gorm.DB, model any, days int, now int64) (int64, error) {
	if days <= 0 {
		// 0 = 永久保留(默认);负数已被 validateAudit 拒绝,这里一并兜住。
		//
		// 这一条不能写成 `cutoff := now - days*86400` 然后照常往下走:
		// days=0 时那个式子等于 cutoff=now,含义从"一行都不删"翻转成"删光"。
		return 0, nil
	}
	if days < config.MinAuditRetentionDays {
		// 正常路径下 validateAudit 已经让进程起不来,能走到这里说明配置快照
		// 是被绕过 Load 塞进来的。宁可不删,也不能按一个没经过校验的保留期动手。
		common.SysError(fmt.Sprintf(
			"qianye: audit.retention_days(%d)低于硬下限 %d,已跳过审计清理",
			days, config.MinAuditRetentionDays))
		return 0, nil
	}

	cutoff := now - int64(days)*86400
	var cursor, deleted int64
	for i := 0; i < pruneMaxWindows; i++ {
		if ctx.Err() != nil {
			return deleted, nil // 租约已丢失,立刻停手,否则就是双跑
		}
		var window []pruneRow
		if err := gdb.WithContext(ctx).Model(model).
			Select("id", "created_at").
			Where("id > ?", cursor).
			Order("id asc").Limit(pruneScanSize).
			Find(&window).Error; err != nil {
			return deleted, fmt.Errorf("扫描过期审计行失败: %w", err)
		}
		if len(window) == 0 {
			break // 扫到表尾
		}
		last := window[len(window)-1]
		cursor = last.Id

		expired := make([]int64, 0, len(window))
		for _, row := range window {
			if row.CreatedAt < cutoff {
				expired = append(expired, row.Id)
			}
		}
		if len(expired) > 0 {
			res := gdb.WithContext(ctx).Where("id IN ?", expired).Delete(model)
			if res.Error != nil {
				return deleted, fmt.Errorf("删除过期审计行失败: %w", res.Error)
			}
			deleted += res.RowsAffected
		}
		// 窗口末行已进入保留期 ⇒ 它之后的行只会更新,再往下扫全是要留的行。
		if last.CreatedAt >= cutoff {
			break
		}
		if len(window) < pruneScanSize {
			break // 窗口没填满,已到表尾
		}
		time.Sleep(prunePause)
	}
	return deleted, nil
}
