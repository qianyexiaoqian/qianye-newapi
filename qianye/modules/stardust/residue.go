package stardust

// residue.go —— 本模块对「用户分组名」这个键的处置声明,以及分组比例档的读取缓存。
//
// 三处以分组名为键,处置**不同**,这正是本文件存在的理由:
//
//	qy_sd_group_rate.user_group   分组比例**配置**            → clean(改名跟着改,删除清掉)
//	qy_sd_accrual.user_group      结算时冻结的分组**事实**    → keep
//	qy_sd_ledger.rate_group       计账时冻结的分组**事实**    → keep(列名不在守卫扫描集合内,
//	                                                          登记只为文档;它**不进**探测清单,见下)
//
// 后两处一个字节都不能动。它们是"这一笔按哪个分组的比例算出来的"这个事实,
// 事后拿着流水行复算全靠它;改掉等于篡改账目。分组被删掉之后那个名字变成一个
// 死字符串 —— 这正是它应有的样子,历史就是发生在一个已经不存在的分组上。
//
// 改名时比例档**绝不并入迁移目标**:目标分组可能已经有自己的一档,任何一种合并都等于
// 在没人批准的情况下改掉另一组人的比例。改名撞键(目标名已有一行)由 Update 撞 PK
// 报出带表名的错误,groupns 据此报 StageCleanup 的半成状态。
//
// qy_sd_ledger.rate_group 不列进探测结果:它的处置恒为 keep、行数为几都不改变运营
// 该按哪个键,而准确的行数要在每次打开删除弹窗时全表扫本模块最大的一张表
// (每一次余额变动一行,永不清理)。那是拿一次实打实的库压力换一个装饰性的数字。

import (
	"context"
	"fmt"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/groupname"
	"github.com/QuantumNous/new-api/qianye/modules/groupns"

	"gorm.io/gorm"
)

func init() {
	groupns.RegisterResidue(groupns.ResidueHandler{
		Module:      "stardust",
		Probe:       probeResidue,
		Sweep:       sweepResidue,
		AfterCommit: afterResidueCommit,
	})
}

// probeResidue 用 groupname.Normalize 查比例档:user_group 这一列**一律以归一化后的
// 形式存储**,拿原始大小写去查会漏掉整条规则,而漏掉的表现是"删完之后比例表里
// 还挂着一条永远不会命中的规则"。日桶列以 groupname.Effective 存(见 Accrual.UserGroup)。
func probeResidue(gdb *gorm.DB, userGroup string) ([]groupns.Residue, error) {
	if gdb == nil {
		return nil, nil
	}
	var rates int64
	if err := gdb.Model(&GroupRate{}).
		Where("user_group = ?", groupname.Normalize(userGroup)).
		Count(&rates).Error; err != nil {
		return nil, err
	}
	var frozen int64
	if err := gdb.Model(&Accrual{}).
		Where("user_group = ?", groupname.Effective(userGroup)).
		Count(&frozen).Error; err != nil {
		return nil, err
	}
	return []groupns.Residue{
		{
			Module: "stardust", Table: GroupRate{}.TableName(),
			Label: "该用户分组的星屑比例档(消费返 / 邀请返)", Rows: rates,
			Disposition: groupns.ResidueClean,
			Detail: "删掉之后这一档人回落全站默认比例。" +
				"迁移不会把源分组的比例带到目标分组上 —— 那会静默改掉另一档人的返还比例",
		},
		{
			Module: "stardust", Table: Accrual{}.TableName(),
			Label: "消费返日桶里冻结的分组名(user_group)", Rows: frozen,
			Disposition: groupns.ResidueKeep,
			Detail: "**保留**。它是「这一天的消费返当时按哪个分组算的」这个事实," +
				"改掉它等于篡改账目 —— 事后复算会得出另一个数字。流水表的 rate_group 同理保留",
		},
	}, nil
}

// sweepResidue 在扩展库事务里执行处置:改名 Update 键、删除 Delete 行,绝不并入目标。
func sweepResidue(tx *gorm.DB, from, to string, rename bool) error {
	key := groupname.Normalize(from)
	if rename {
		// 改名走 Update 而不是"删掉再建":后者会丢掉 operator_id,
		// 而"这条比例是谁配的"是审计之外唯一的线索。
		target := groupname.Normalize(to)
		if err := tx.Model(&GroupRate{}).Where("user_group = ?", key).
			Update("user_group", target).Error; err != nil {
			return fmt.Errorf("stardust: %s 的分组键 %q → %q 改写失败(目标名可能已有一档): %w",
				GroupRate{}.TableName(), key, target, err)
		}
		return nil
	}
	if err := tx.Where("user_group = ?", key).Delete(&GroupRate{}).Error; err != nil {
		return fmt.Errorf("stardust: 清理 %s 的分组键 %q: %w", GroupRate{}.TableName(), key, err)
	}
	// 缓存失效**不在这里**做,见 afterResidueCommit:事务内失效会让一条并发读用
	// 事务外的连接把未提交的旧行重新填回缓存,并按 60 秒 TTL 钉住。
	return nil
}

// afterResidueCommit 在扩展库事务**提交之后**刷进程内的比例档缓存。
//
// 比例是逐笔冻结进流水的:改名之后窗口内按旧行重填一次缓存,那 60 秒里每一笔
// 返还都按错档冻结,事后不追溯。
func afterResidueCommit(from, to string, rename bool) {
	_, _, _ = from, to, rename
	invalidateGroupRates()
}

// ───────────────────────── 分组比例档的读取缓存 ─────────────────────────

var (
	groupRateMu     sync.Mutex
	groupRateCache  map[string]GroupRate
	groupRateLoaded int64
	// groupRateEpoch 与 settingsEpoch 同一语义:见 settings.go。
	groupRateEpoch uint64
)

// groupRates 返回全部**启用**的分组比例档,按 groupname.Effective 归一化后的分组名索引。
//
// 规则条数等于分组数(几条到几十条),整表拉进内存每 settingsCacheSeconds 刷一次,
// 远比每次结算 / 每条 hook 回一次库便宜。读不到时沿用上一份快照,再不济返回空表
// 回落全站默认 —— 绝不因为读不到分组档就停止发放。
//
// 结构与 effectiveCtx 一致:持锁只做读 / 写快照,SELECT 在临界区之外发出,
// 写回时用代次判断这份快照是否已被 invalidateGroupRates 作废。
func groupRates(ctx context.Context) map[string]GroupRate {
	groupRateMu.Lock()
	if groupRateCache != nil && common.GetTimestamp()-groupRateLoaded < settingsCacheSeconds {
		cached := groupRateCache
		groupRateMu.Unlock()
		return cached
	}
	epoch := groupRateEpoch
	prior := groupRateCache
	groupRateMu.Unlock()

	// 沿用旧快照不算降级 —— 那是缓存本来的语义,比例仍是运营配的那份。
	// 只有**返回空表**才算:那一刻起所有分组一律按全站默认发放,而结果会冻结进
	// 流水行,事后分不出"这行是降级"还是"当时配的就是这个",所以必须留一条日志。
	gdb := db.Get()
	if gdb == nil {
		if prior != nil {
			return prior
		}
		common.SysError("qianye/stardust: 读取分组比例档失败,本轮按全站默认比例发放: " + db.ErrNotReady.Error())
		return map[string]GroupRate{}
	}
	var rows []GroupRate
	if err := gdb.WithContext(ctx).Where("enabled = ?", true).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		if prior != nil {
			return prior
		}
		common.SysError("qianye/stardust: 读取分组比例档失败,本轮按全站默认比例发放: " + err.Error())
		return map[string]GroupRate{}
	}
	m := make(map[string]GroupRate, len(rows))
	for _, r := range rows {
		// 建键与查表(groupRateFor)必须是同一个函数,否则历史行会变成永远查不到的规则。
		m[groupname.Effective(r.UserGroup)] = r
	}
	groupRateMu.Lock()
	if groupRateEpoch == epoch {
		groupRateCache = m
		groupRateLoaded = common.GetTimestamp()
	}
	groupRateMu.Unlock()
	return m
}

// groupRateFor 回答"这个分组有没有启用的比例档"。group 是主库 users.group 的原文,
// 空串按 default 分组判定(groupname.Effective)。第二个返回值为 false 时回落全站默认。
func groupRateFor(ctx context.Context, group string) (GroupRate, bool) {
	r, ok := groupRates(ctx)[groupname.Effective(group)]
	return r, ok
}

// invalidateGroupRates 失效本进程的分组比例档快照。管理端写 qy_sd_group_rate 之后必须调它。
func invalidateGroupRates() {
	groupRateMu.Lock()
	groupRateCache = nil
	groupRateLoaded = 0
	groupRateEpoch++
	groupRateMu.Unlock()
}
