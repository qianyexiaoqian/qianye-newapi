package groupns

// residue.go —— 「用户分组名」这个键在**别的模块**里的残留,以及它们的处置。
//
// ══════════════════ 为什么必须是一张注册表,而不是一串 import ══════════════════
//
// 删掉一个用户分组时,它的名字散落在至少六张扩展表和三处 options 里。最省事的
// 写法是让 groupns 直接 import groupmatrix / commission / transfer / usergroup,
// 一处一处删过去。那样做有两个具体的坏处:
//
//  1. **依赖方向会立刻打结。** planentitlement 已经 import groupns;再让 groupns
//     反向 import 三个兄弟模块,任何一个模块将来想读一次登记表就成环,
//     而 Go 的环是编译期硬失败 —— 修法只能是把代码搬来搬去。
//
//  2. **漏一个模块不会有任何症状。** 新模块加一张以分组名为键的表时,没有任何
//     机制提醒作者"删除路径要跟着改一处"。而漏掉的表现是:分组删掉了,
//     那张表里还留着一条挂在死名字上的规则 —— 一条永远不会命中的费率、
//     或者一条永远不会命中的划转限制(后者是资金闸门,不命中就是完全不设防)。
//
// 注册表让"我这张表以用户分组为键"变成一次**显式声明**,并且由
// TestUserGroupResidueCoverage 钉住"声明过的模块必须都在"。
//
// ══════════════════════════ 处置只有四种,必须显式给出 ══════════════════════════
//
//	clean    删除时清掉这一行。适用于"配置":范围、授权、费率、划转规则。
//	rewrite  改名时跟着改;删除时也跟着改(值本身仍然有意义,只是换了个名字)。
//	keep     一个字节都不动。适用于**历史冻结值**:佣金流水里的 rate_group、
//	         违规记录里的 using_group —— 它们是"当时按哪个分组算的"这个事实,
//	         改掉它等于篡改账目,事后拿着流水复算会得出另一个结论。
//	block    存在即拒绝删除。适用于"删掉之后会让别的功能指向一个死名字,
//	         而正确的新值只有运营知道"的那一类。
//
// 没有默认值:一个新模块必须为它的每一张表回答这个问题。

import (
	"sort"
	"sync"

	"gorm.io/gorm"
)

// 残留的四种处置。
const (
	ResidueClean   = "clean"
	ResidueRewrite = "rewrite"
	ResidueKeep    = "keep"
	ResidueBlock   = "block"
)

// Residue 是一处以用户分组名为键的残留,以及打算拿它怎么办。
type Residue struct {
	// Module 是声明它的模块名(与 module.Module.Name() 同值)。
	Module string `json:"module"`
	// Table 是库表名。它进确认弹窗:运营要能拿着表名去核对。
	Table string `json:"table"`
	// Label 是中文说明,回答"这一行是干什么的"。
	Label string `json:"label"`
	// Rows 是命中行数。0 行的残留仍然会被列出来 —— 「这里本来就没有」与
	// 「这里没查」在界面上必须是两件事。
	Rows int64 `json:"rows"`
	// Disposition ∈ clean / rewrite / keep / block。
	Disposition string `json:"disposition"`
	// Detail 是可选的补充说明(例如具体命中了哪几条规则)。
	Detail string `json:"detail,omitempty"`
}

// ResidueHandler 是一个模块对「用户分组名」的处置声明。
type ResidueHandler struct {
	// Module 是模块名。
	Module string
	// Probe 报告该模块此刻的残留。
	//
	// 冷路径(管理端打开确认弹窗时一次),允许查扩展库。tx 是扩展库句柄。
	// 返回错误时删除会被拒绝:算不清影响面就不许删,这是唯一安全的方向。
	Probe func(tx *gorm.DB, userGroup string) ([]Residue, error)
	// Sweep 在扩展库事务里执行处置。
	//
	//	rename=true   把 from 改写成 to(to 是一个**新**名字)
	//	rename=false  清掉 from 的残留(to 是用户的迁移目标,用于需要改写的那几处)
	//
	// 实现方**不得**在这里把 from 的配置合并进 to:删除时目标分组已经有自己的
	// 一套配置,合并等于在运营不知情的情况下改掉另一档人的价格与可用范围。
	//
	// **必须使用传进来的 tx。** 在 Sweep 里另开一条连接(db.Get())写的那一行
	// 不在这个事务里:后续任何一步失败都会回滚事务里的全部改动,唯独那一行
	// 留在库里,而接口返回的是"什么都没改成"。这是这套设计最难排查的失败形状。
	Sweep func(tx *gorm.DB, from, to string, rename bool) error
	// AfterCommit 可选,在扩展库事务**提交之后**执行,专门用来刷进程内缓存。
	//
	// 分成两步而不是在 Sweep 里顺手刷:事务回滚时 Sweep 写的库行会被撤销,
	// 而缓存不会 —— 那会留下一个"库里是 A、进程里是 B"的分叉,并且只有重启
	// 才会自愈。失败不影响已经提交的改动,因此它不返回错误,只允许写日志。
	AfterCommit func(from, to string, rename bool)
}

var (
	residueMu       sync.RWMutex
	residueHandlers = map[string]ResidueHandler{}
)

// RegisterResidue 由各模块在 InstallHooks() 里调用一次。
//
// 按模块名去重:InstallHooks 在进程生命周期里只跑一次,但测试会反复调用,
// 追加式注册会让同一个 Sweep 在一次删除里跑三遍。
func RegisterResidue(h ResidueHandler) {
	if h.Module == "" || h.Probe == nil || h.Sweep == nil {
		return
	}
	residueMu.Lock()
	defer residueMu.Unlock()
	residueHandlers[h.Module] = h
}

// residueHandlerList 返回已注册的处置,按模块名排序。
//
// 排序是为了让确认弹窗与审计正文的顺序稳定:一份每次刷新都换顺序的影响面清单,
// 运营没法拿它和上一次比对。
func residueHandlerList() []ResidueHandler {
	residueMu.RLock()
	defer residueMu.RUnlock()
	out := make([]ResidueHandler, 0, len(residueHandlers))
	for _, h := range residueHandlers {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Module < out[j].Module })
	return out
}

// ProbeResidues 汇总全部模块对该用户分组的残留。
//
// 任何一个模块探测失败都整体返回错误:影响面清单缺一块与完整的清单在界面上
// 长得一模一样,而运营会照着它按下删除。
func ProbeResidues(gdb *gorm.DB, userGroup string) ([]Residue, error) {
	out := make([]Residue, 0, 8)
	for _, h := range residueHandlerList() {
		rows, err := h.Probe(gdb, userGroup)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// SweepResidues 在**一个扩展库事务**里执行全部模块的处置。
//
// 扩展表全在同一个库,所以这一整块是原子的 —— 不会出现"范围清了、费率还在"。
// 与主库那一半之间的原子性做不到(两个库),顺序上的补偿见 usergroup_admin.go
// 的 rewriteUserGroup。
func SweepResidues(tx *gorm.DB, from, to string, rename bool) error {
	for _, h := range residueHandlerList() {
		if err := h.Sweep(tx, from, to, rename); err != nil {
			return err
		}
	}
	return nil
}

// CommitResidues 在扩展库事务提交之后逐个通知各模块刷新进程内缓存。
//
// 调用方必须在 SweepResidues 所在事务**成功提交之后**调一次;事务失败时
// 一次都不许调 —— 那会让进程里留着一份库里并不存在的配置。
func CommitResidues(from, to string, rename bool) {
	for _, h := range residueHandlerList() {
		if h.AfterCommit != nil {
			h.AfterCommit(from, to, rename)
		}
	}
}

// RewriteResidueRows 统计处置为 rewrite 且真的有行的残留总行数。
//
// 它是"删除时必须选一个迁移目标"的第二个来源:在册用户数为 0 并不等于
// 这个名字没有人再引用它。最典型的是「新注册用户的默认分组」—— 那一档人
// **还不存在**,不进任何影响面统计,而目标为空时它只能被清掉,
// 于是此后每一个新用户静默落进上游 default 档。
func RewriteResidueRows(rows []Residue) int64 {
	total := int64(0)
	for _, row := range rows {
		if row.Disposition == ResidueRewrite && row.Rows > 0 {
			total += row.Rows
		}
	}
	return total
}

// BlockingResidues 过滤出处置为 block 且真的有行的残留。
func BlockingResidues(rows []Residue) []Residue {
	out := make([]Residue, 0, len(rows))
	for _, row := range rows {
		if row.Disposition == ResidueBlock && row.Rows > 0 {
			out = append(out, row)
		}
	}
	return out
}

// ResetResiduesForTest 清空注册表。仅测试使用。
func ResetResiduesForTest() {
	residueMu.Lock()
	defer residueMu.Unlock()
	residueHandlers = map[string]ResidueHandler{}
}
