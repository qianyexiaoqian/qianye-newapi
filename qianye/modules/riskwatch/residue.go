package riskwatch

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

// residue.go —— 本模块对「用户分组名」这个键的处置声明。
//
// 两张表各有一列 user_group,而它们的性质**完全相反**:
//
//	qy_riskwatch_task.user_group     判据。它决定这个任务盯谁,分组改名/删除后
//	                                 必须跟着动,否则这条监听会静默地再也不命中。
//	qy_riskwatch_capture.user_group  证据。它记录那一次请求**当时**走的是哪个
//	                                 分组,是历史事实。改它就是篡改证据。
//
// 所以判据那一列跟着改、证据那一列一个字都不动。把两者按同一种处置对待
// (无论哪一种)都会坏掉其中一半。
//
// ══════════════ 删除时为什么是「停任务、留名字」而不是摘空 ══════════════
//
// 摘空在这里是**放宽**:三格作用域里空串 = 不限,把 user_group 摘掉之后,
// 一个原本只盯 vip 分组的任务会变成盯全站;要是它本来就只填了这一格,
// 摘完就是一个全站 100% 的监听任务 —— 而那正是这个存储节点最不能承受的东西
// (接口层为此专门拒绝三格全空的入参,见 errScopeEmpty)。
//
// 因此删除路径上把命中的**运行中**任务停掉,而 user_group 那一格原样留着:
// 停掉是 fail-closed,留名字让这份立案记录事后还读得懂(「当时盯的是 vip」)。
// 管理员改绑分组后可以重新启动。
//
// ══════════════ 为什么这里不用传进来的 tx ══════════════
//
// ResidueHandler 的契约要求 Sweep 使用传入的 tx,理由是"另开连接写的那一行
// 不在事务里,回滚之后它会独自留在库里"。那条契约在这里**做不到**,而且不是
// 疏忽:本模块的两张表住在单独配置的存储节点上(见 qianye/db/watchdb.go),
// 与主扩展库不是同一个连接,更不可能在同一个事务里。
//
// 于是这里显式接受那个代价,并把它压到最小:
//
//	主事务回滚 + 本模块已经扫过  →  几个监听任务被多停了一次 / 名字被改了
//	                               管理员在页面上点「启动」即可恢复,记录一条不少
//	不扫                        →  一条指向已删分组的监听任务静默存活,
//	                               将来同名分组被重新创建时它会突然对一批
//	                               毫不相干的新用户生效
//
// 后者才是不可接受的那一个:它没有任何症状,而且发作时间由别人决定。

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module:      "riskwatch",
		Probe:       probeResidue,
		Sweep:       sweepResidue,
		AfterCommit: afterResidueCommit,
	})
}

// probeResidue 报告有多少监听任务盯着这个分组。
//
// 入参 tx 是**主扩展库**的句柄,本函数刻意不用它(理由见文件头)。
// 存储节点没配(功能整个关着)时返回 nil:那不是"没查",是真的没有这张表。
//
// 存储节点配了却连不上时返回错误 —— 按 ResidueHandler 的契约,那会让这次
// 分组删除被拒绝。这一条是刻意保留的严格:算不清影响面就不许删,而这里
// 算不清的后果是一条监听任务带着一个即将被回收的分组名活下去。
func probeResidue(_ *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	gdb := db.Watch()
	if gdb == nil {
		return nil, nil
	}
	key := groupname.Effective(userGroup)

	var running, total int64
	if err := gdb.Model(&Task{}).Where("user_group = ?", key).Count(&total).Error; err != nil {
		db.MarkWatchFailure(err)
		return nil, err
	}
	if err := gdb.Model(&Task{}).Where("user_group = ? AND status = ?", key, StatusRunning).
		Count(&running).Error; err != nil {
		db.MarkWatchFailure(err)
		return nil, err
	}
	var captures int64
	if err := gdb.Model(&Capture{}).Where("user_group = ?", key).Count(&captures).Error; err != nil {
		db.MarkWatchFailure(err)
		return nil, err
	}

	return []groupns.Residue{
		{
			Module: "riskwatch", Table: Task{}.TableName(),
			Label: "把它设为**监听目标分组**的风控预警任务", Rows: total,
			Disposition: groupns.ResidueKeep,
			Detail: fmt.Sprintf("其中 %d 个正在运行。删除分组时这些任务会被**停止**,"+
				"而目标分组那一格原样保留 —— 摘空它等于把一个只盯某个分组的任务放宽成全站监听,"+
				"而留着名字让这份立案记录事后还读得懂。改名则跟着改名,任务继续运行。"+
				"改绑分组后可在风控预警页重新启动", running),
		},
		{
			Module: "riskwatch", Table: Capture{}.TableName(),
			Label: "分组字段记着它的**监听记录**(证据)", Rows: captures,
			Disposition: groupns.ResidueKeep,
			Detail: "一个字都不动。这一列记的是那一次请求**当时**走的哪个分组,是历史事实;" +
				"跟着改名或清空都是篡改证据,而这些记录的用途恰恰是事后复盘",
		},
	}, nil
}

// sweepResidue 执行处置:改名跟着改,删除则停任务。
//
// 两条都只碰任务表。记录表在这两种情况下都不动(见文件头)。
func sweepResidue(_ *gorm.DB, from, to string, rename bool) error {
	gdb := db.Watch()
	if gdb == nil {
		return nil
	}
	fromKey := groupname.Effective(from)
	now := common.GetTimestamp()

	if rename {
		err := gdb.Model(&Task{}).Where("user_group = ?", fromKey).
			Updates(map[string]any{
				"user_group": groupname.Normalize(to),
				"updated_at": now,
				"version":    gorm.Expr("version + 1"),
			}).Error
		if err != nil {
			db.MarkWatchFailure(err)
		}
		return err
	}

	// 删除:只停运行中的那些。已经停了的任务原样留着 —— 它的 stopped_reason
	// 记着当初是怎么停的,而那比"分组没了"更有信息量。
	err := gdb.Model(&Task{}).
		Where("user_group = ? AND status = ?", fromKey, StatusRunning).
		Updates(map[string]any{
			"status":         StatusStopped,
			"stopped_at":     now,
			"stopped_reason": ReasonGroupGone,
			"updated_at":     now,
			"version":        gorm.Expr("version + 1"),
		}).Error
	if err != nil {
		db.MarkWatchFailure(err)
	}
	return err
}

// afterResidueCommit 在主库事务提交之后刷一次快照。
//
// 分成两步而不是在 Sweep 里顺手刷:事务回滚时快照会保留一个已经不存在的口径,
// 而这里刷新的代价只是一次查询。失败只写日志 —— 下一个刷新周期会自己纠正。
func afterResidueCommit(_, _ string, _ bool) {
	if err := Reload(); err != nil {
		common.SysError("qianye/riskwatch: 分组变更后刷新监听任务快照失败(下个周期自愈): " + err.Error())
	}
}
