package groupns

// modelgroup_residue.go —— 「模型分组名」这个键在**别的模块**里的残留,以及它们的处置。
//
// 它与 residue.go 是同一个手法的两个轴:那一份管 users.group(这个人是谁),
// 这一份管 channels.group / abilities.group(这次请求去哪个渠道池子)。
// 两张注册表刻意不合并成一张带 axis 字段的表 —— 处置在两个轴上不同源:
// 同一张 qy_group_grants,按用户分组删是"这一档人的全部授权",按模型分组删是
// "全站指向这个池子的授权",两者的 Probe/Sweep 是两条不同的 SQL,合并只会得到
// 一个每处都要 `if axis == …` 的函数。
//
// ══════════════════════ 为什么必须是注册表,而不是一串 import ══════════════════════
//
// 与 residue.go 逐字同源的两条理由:
//
//  1. **依赖方向会立刻打结。** planentitlement 已经 import groupns;
//     再让 groupns 反向 import 它就成环,而 Go 的环是编译期硬失败。
//
//  2. **漏一个模块不会有任何症状。** 删掉一个模型分组之后,某张表里还留着一条
//     挂在死名字上的授权 —— 那条授权永远不会命中,而"永远不会命中的授权"与
//     "配置正确"在界面上长得一模一样。
//
// ══════════════════ 注册在 init() 里发生,而不是 InstallHooks ══════════════════
//
// 残留**不随模块开关消失**:group_matrix.enabled 关掉之后 qy_group_grants 里的行
// 一条都不会少,而删除路径必须照样把它们算进影响面并清掉。注册本身只是一次 map
// 写入,不依赖配置、不依赖数据库,所以放在包 init 里 —— 这样"这个模块此刻开不开"
// 与"它的表里有没有残留"就不可能被写成同一个条件。

import (
	"sort"
	"sync"

	"gorm.io/gorm"
)

// ModelResidueHandler 是一个模块对「模型分组名」的处置声明。
//
// 处置取值与 residue.go 共用同一组常量(clean / rewrite / keep / block),
// 语义逐字相同。本轮的模型分组删除**不做改名**,所以 rewrite 暂时没有实现方 ——
// 常量仍然可用,将来加改名时不必再开一套。
type ModelResidueHandler struct {
	// Module 是模块名(与 module.Module.Name() 同值)。
	Module string
	// Probe 报告该模块此刻对这个模型分组的残留。
	//
	// 冷路径(管理端打开影响面弹窗时一次),允许查扩展库。tx 是扩展库句柄。
	// 返回错误时删除会被拒绝:**算不清影响面就不许删**,这是唯一安全的方向 ——
	// 一份缺了一块的影响面清单和一份完整的在界面上长得一模一样,
	// 而运营会照着它按下删除。
	Probe func(tx *gorm.DB, modelGroup string) ([]Residue, error)
	// Sweep 在扩展库事务里清掉该模型分组的残留。
	//
	// 实现方**不得**把这些行改写到别的模型分组上:删除时没有"迁移目标"这个概念
	// (模型分组背后是一批渠道,换一个池子等于换一批上游供应商与一套价格),
	// 替运营选一个目标就是替他做了一次改价决定。
	Sweep func(tx *gorm.DB, modelGroup string) error
}

var (
	modelResidueMu       sync.RWMutex
	modelResidueHandlers = map[string]ModelResidueHandler{}
)

// RegisterModelResidue 由各模块在包 init() 里调用一次。
//
// 按模块名去重:init 在进程生命周期里只跑一次,但测试会反复调用,
// 追加式注册会让同一个 Sweep 在一次删除里跑三遍。
func RegisterModelResidue(h ModelResidueHandler) {
	if h.Module == "" || h.Probe == nil || h.Sweep == nil {
		return
	}
	modelResidueMu.Lock()
	defer modelResidueMu.Unlock()
	modelResidueHandlers[h.Module] = h
}

// modelResidueHandlerList 返回已注册的处置,按模块名排序。
//
// 排序是为了让影响面弹窗与审计正文的顺序稳定:一份每次刷新都换顺序的清单,
// 运营没法拿它和上一次比对。
func modelResidueHandlerList() []ModelResidueHandler {
	modelResidueMu.RLock()
	defer modelResidueMu.RUnlock()
	out := make([]ModelResidueHandler, 0, len(modelResidueHandlers))
	for _, h := range modelResidueHandlers {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// ProbeModelResidues 汇总全部模块对该模型分组的残留。
//
// 任何一个模块探测失败都整体返回错误,理由见 ModelResidueHandler.Probe。
func ProbeModelResidues(gdb *gorm.DB, modelGroup string) ([]Residue, error) {
	out := make([]Residue, 0, 8)
	for _, h := range modelResidueHandlerList() {
		rows, err := h.Probe(gdb, modelGroup)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// SweepModelResidues 在**一个扩展库事务**里执行全部模块的处置。
//
// 扩展表全在同一个库,所以这一整块是原子的 —— 不会出现"授权清了、套餐解锁还在"。
// 与主库那一半(options)之间的原子性做不到(两个库),顺序上的补偿见
// modelgroup_store.go 的 DeleteModelGroup。
func SweepModelResidues(tx *gorm.DB, modelGroup string) error {
	for _, h := range modelResidueHandlerList() {
		if err := h.Sweep(tx, modelGroup); err != nil {
			return err
		}
	}
	return nil
}

// ResetModelResiduesForTest 清空注册表。仅测试使用。
func ResetModelResiduesForTest() {
	modelResidueMu.Lock()
	defer modelResidueMu.Unlock()
	modelResidueHandlers = map[string]ModelResidueHandler{}
}
