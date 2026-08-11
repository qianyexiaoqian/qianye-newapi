package violation

import (
	"fmt"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"gorm.io/gorm"
)

// AI 审核的作用域与分档抽样。
//
// ═════════════════════ 项目方要的是什么 ═════════════════════
//
// 原话:「AI内容审核要可以监控分组,因为AI审核基本都是后审核,秋后算账的」。
// 拆开是两件事:
//
//  1. **只盯某几个分组**。全站一个抽样率表达不了"自助注册的分组重点看,
//     内部对接的分组根本不用看"—— 而后者往往占了全站绝大多数流量,
//     等于把审核预算全花在最不需要审的请求上。
//  2. **不同分组不同力度**。高风险分组 50%、其余 1%,比全站一个数有用得多。
//     一个数字只能在"贵得离谱"与"抽不到任何东西"之间二选一。
//
// ═════════════════════ 为什么是多条策略而不是配置上的几个字段 ═════════════════════
//
// 把 group_scope / model_scope 直接加在 AISetting(单例配置)上是更小的改动,
// 但它**表达不了分档**:一份作用域只能配一个抽样率,于是"高风险 50%、其余 1%"
// 仍然无解。而分档正是项目方那句话里最实的那一半。
//
// 所以是一张策略表:每条策略 = 一个作用域 + 两个时机各自的抽样率。
// 热路径按 priority 升序取**第一条**匹配的策略(与规则表同一套心智:
// 优先级最高的一条说了算,不叠加),都不匹配时落到 AISetting.SampleRateBps。
//
// ═════════════════════ 迁移:零动作、零行为变化 ═════════════════════
//
// 一条策略都没有时,sampleRatesFor 返回的就是 AISetting.SampleRateBps ——
// 与这张表存在之前逐字节相同。已有站点升级上来什么都不用做,行为不变。
// 也因此**不需要**在 AISetting 上加"转发前兜底/转发后兜底"两列:要给兜底
// 分时机的站点建一条作用域全空的策略即可(它匹配一切请求,天然是兜底)。
// 加两列的代价是一次带哨兵值的数据迁移,而那正是本模块最不该有的东西。

// AIScope 是一条 AI 审核作用域策略。
//
// # 与违规规则的作用域列逐项同名同义
//
// ModelScope / GroupScope / GroupScopeMode 三列的语法、大小写折叠、通配语义
// 全部来自 compileScope —— 与 qy_violation_rule 上那三列**共用同一段代码**。
// 运营在两个页面上看到的是同一个输入框,得到的必须是同一个结果。
//
// # 为什么抽样率是两个而不是一个
//
// 两个时机的代价完全不同:
//
//	转发前(pre)   同步,结论回来才继续。它直接加在被抽中请求的首字节延迟上。
//	转发后(async) 异步,本次请求一秒不等。它只花钱,不花用户的时间。
//
// 运营对这两件事的容忍度差一个数量级:后审核("秋后算账")可以对全站开到
// 10%,而转发前审核通常只敢对最可疑的那一两个分组开。共用一个数字等于逼人
// 按较严的那一侧配,于是后审核的覆盖率被前审核的延迟顾虑白白拖低。
type AIScope struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// Name 是给人看的档名(「自助注册分组」「内部对接·免审」)。
	// 必填:一张只有作用域表达式的策略表,三个月后没人说得出每条是干什么的。
	Name    string `json:"name" gorm:"type:varchar(64);not null;default:''"`
	Enabled bool   `json:"enabled" gorm:"not null;default:false"`

	// Priority 升序,第一条匹配的策略说了算(与规则表同一套心智)。
	// 不叠加:两条策略同时命中却各给一个抽样率时,"叠加"没有任何一种
	// 定义是运营预期的(取大?取小?相乘?),而取第一条是可解释的。
	Priority int `json:"priority" gorm:"not null;default:100;index:idx_qy_vais_pri,priority:1"`

	ModelScope     string `json:"model_scope" gorm:"type:varchar(2048);not null;default:''"`
	GroupScope     string `json:"group_scope" gorm:"type:varchar(1024);not null;default:''"`
	GroupScopeMode string `json:"group_scope_mode" gorm:"type:varchar(8);not null;default:'include'"`

	// PreSampleRateBps / AsyncSampleRateBps 是万分比(30% = 3000),0 = 该时机不审。
	//
	// 两个都填 0 是**有意义的配置**,不是"没配":它表达「这些分组一律不送审」,
	// 也就是免审名单。放在高优先级上,它比 exclude 名单更直白 ——
	// exclude 要反过来想一遍,而"这一档抽 0%"是字面意思。
	PreSampleRateBps   int `json:"pre_sample_rate_bps" gorm:"not null;default:0"`
	AsyncSampleRateBps int `json:"async_sample_rate_bps" gorm:"not null;default:0"`

	Remark    string `json:"remark" gorm:"type:varchar(512);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null"`
	UpdatedBy int    `json:"updated_by" gorm:"not null;default:0"`
}

func (AIScope) TableName() string { return "qy_violation_ai_scope" }

// maxAIScopes 是启用中的策略条数上限。
//
// 上限的作用不是"防止配太多",是把热路径最坏耗时钉死:sampleRatesFor 是
// 每个请求都要跑的线性扫描,而它跑在 relay 线程上。64 条足够表达任何真实的
// 分组分档(现网用户分组数是个位数),而 64 次字符串比较在这条路径上不可测量。
const maxAIScopes = 64

// aiScopeRT 是策略的运行期形态:作用域已编译,只活在快照里。
type aiScopeRT struct {
	Id   int64
	Name string
	// scopeMatcher 与违规规则内嵌的是同一个类型、同一段判定代码。
	scopeMatcher
	PreBps   int
	AsyncBps int
}

// aiSampleRolls 是**抽样函数被调用**的累计次数。
//
// 它不是"送审次数"(那是 qy_violation_ai_review 的行数),而是摇骰子的次数,
// 也就是通过了作用域闸的请求数。摆出来有两个用处:
//
//   - 运营侧:它是抽样率的分母。审核明细只有 300 行、抽样率 10% 时,
//     这个数应该在 3000 上下;差一个数量级说明作用域闸把流量挡在了外面
//     (或者相反,某条策略比想象中覆盖得广)。
//   - 契约侧:「不在作用域内的请求连抽样都不算」这条不变量只能靠它来断言 ——
//     没有计数器时,"先抽中再丢弃"与"先判作用域再抽样"在外部完全同形,
//     而两者对抽样率含义的破坏是彻底的(10% 会变成"作用域内的某个未知比例")。
var aiSampleRolls atomic.Int64

// sampleRatesFor 返回本次请求两个时机各自的抽样率(万分比)。
//
// 纯内存比较:没有分配、没有加锁、没有随机数、没有数据库。它排在抽样之前,
// 这个顺序是**契约**而不是优化 —— 反过来(先抽样再判作用域)会让界面上那个
// "10%" 变成"作用域内的 10% 乘以一个谁也说不出来的数",而抽样率是本功能
// 唯一的成本闸门,它必须是字面意思。
//
// 都不匹配时落到 fallback(AISetting.SampleRateBps),这也是一条策略都没有
// 时的行为 —— 与这张表存在之前完全一致。
func (rt *aiRuntime) sampleRatesFor(model, group string) (pre, async int) {
	if rt == nil {
		return 0, 0
	}
	for _, s := range rt.Scopes {
		if s.inScope(model, group) {
			return s.PreBps, s.AsyncBps
		}
	}
	return rt.SampleRateBps, rt.SampleRateBps
}

// buildAIScopes 把启用中的策略行编译进快照。
//
// 顺序是 priority 升序、id 升序 —— 与规则表同一个 ORDER BY,因为"第一条匹配的
// 说了算"这句话只有在顺序确定时才有意义。同优先级不给保证的话,同一份配置在
// 两个节点上会给出不同的抽样率,而那种差异查不出来。
func buildAIScopes(gdb *gorm.DB) ([]*aiScopeRT, error) {
	if gdb == nil {
		return nil, nil
	}
	var rows []AIScope
	if err := gdb.Where("enabled = ?", true).
		Order("priority asc, id asc").Limit(maxAIScopes).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*aiScopeRT, 0, len(rows))
	for _, row := range rows {
		out = append(out, &aiScopeRT{
			Id: row.Id, Name: row.Name,
			scopeMatcher: compileScope(row.ModelScope, row.GroupScope, row.GroupScopeMode),
			PreBps:       clampInt(row.PreSampleRateBps, 0, 10000),
			AsyncBps:     clampInt(row.AsyncSampleRateBps, 0, 10000),
		})
	}
	return out, nil
}

// aiScopesReachAnySampling 回答"这份配置有没有任何一条路径会真的送审"。
//
// buildAIRuntime 用它替换掉原来的 `SampleRateBps <= 0 → 整体不生效`:兜底率
// 为 0 但某条策略配了 50% 是**最典型**的用法(只盯高风险分组),而旧判据会把
// 它整个关掉 —— 界面上策略配得好好的,线上一次都不跑,零报错。
func aiScopesReachAnySampling(fallbackBps int, scopes []*aiScopeRT) bool {
	if fallbackBps > 0 {
		return true
	}
	for _, s := range scopes {
		if s.PreBps > 0 || s.AsyncBps > 0 {
			return true
		}
	}
	return false
}

// validateAIScope 是策略的写入校验。归一(去空白)也在这里,与 validateAISetting 同规格。
func validateAIScope(s *AIScope) error {
	s.Name = strings.TrimSpace(s.Name)
	s.ModelScope = strings.TrimSpace(s.ModelScope)
	s.GroupScope = strings.TrimSpace(s.GroupScope)
	s.Remark = strings.TrimSpace(s.Remark)
	if s.GroupScopeMode == "" {
		s.GroupScopeMode = GroupScopeInclude
	}

	if s.Name == "" {
		return fmt.Errorf("策略名称不能为空 —— 一张只有作用域表达式的列表,三个月后没人说得出每条是干什么的")
	}
	if n := utf8.RuneCountInString(s.Name); n > 64 {
		return fmt.Errorf("策略名称过长(%d 字,上限 64 字)", n)
	}
	switch s.GroupScopeMode {
	case GroupScopeInclude, GroupScopeExclude:
	default:
		return fmt.Errorf("group_scope_mode 取值非法: %q(只能是 %q 或 %q)",
			s.GroupScopeMode, GroupScopeInclude, GroupScopeExclude)
	}
	if n := utf8.RuneCountInString(s.ModelScope); n > 2048 {
		return fmt.Errorf("模型作用域过长(%d 字,上限 2048 字)", n)
	}
	if n := utf8.RuneCountInString(s.GroupScope); n > 1024 {
		return fmt.Errorf("分组作用域过长(%d 字,上限 1024 字)", n)
	}
	if n := utf8.RuneCountInString(s.Remark); n > 512 {
		return fmt.Errorf("备注过长(%d 字,上限 512 字)", n)
	}
	if s.Priority < 0 || s.Priority > 10000 {
		return fmt.Errorf("优先级必须在 0..10000 之间(升序,越小越先匹配),当前为 %d", s.Priority)
	}
	if s.PreSampleRateBps < 0 || s.PreSampleRateBps > 10000 {
		return fmt.Errorf("转发前抽样率必须在 0..10000 之间(万分比,30%% = 3000),当前为 %d", s.PreSampleRateBps)
	}
	if s.AsyncSampleRateBps < 0 || s.AsyncSampleRateBps > 10000 {
		return fmt.Errorf("转发后抽样率必须在 0..10000 之间(万分比,30%% = 3000),当前为 %d", s.AsyncSampleRateBps)
	}
	return nil
}

// aiScopeSummaryRow 是「现在到底哪些分组在被监控、各自多少」这个问题的一行答案。
//
// 它不是策略行的回显:Shadowed 与 Fallback 两列在库里都不存在,它们是
// **这一份配置作为整体**的性质,而运营真正要看的正是这个整体。
type aiScopeSummaryRow struct {
	Id   int64  `json:"id"` // 0 = 兜底档(它没有对应的策略行)
	Name string `json:"name"`
	// Fallback 为真时这一行是「未匹配任何策略」那一档,取值来自 AISetting.SampleRateBps。
	Fallback       bool   `json:"fallback"`
	Enabled        bool   `json:"enabled"`
	Priority       int    `json:"priority"`
	ModelScope     string `json:"model_scope"`
	GroupScope     string `json:"group_scope"`
	GroupScopeMode string `json:"group_scope_mode"`
	PreBps         int    `json:"pre_sample_rate_bps"`
	AsyncBps       int    `json:"async_sample_rate_bps"`
	// Shadowed 为真表示这一行**永远不会被匹配到**:它前面有一条作用域为空
	// (= 匹配一切)的启用策略把所有请求都收走了。
	//
	// 这一列存在的唯一理由是本模块反复出现的失效形状:配置保存成功、界面
	// 显示正常、线上一次都不生效,而且没有任何报错。一条被遮住的策略与一条
	// 配错了作用域的策略在列表上长得一模一样。
	Shadowed bool `json:"shadowed"`
}

// summarizeAIScopes 把策略表 + 兜底率折成一张"谁在被监控"的表。
//
// 排序即匹配顺序,最后一行恒为兜底档 —— 因为热路径就是这样跑的,
// 界面上的顺序与判定顺序不一致时,运营会照着一个错误的心智模型去调优先级。
//
// 注意入参是**库里的全部策略行**(含未启用的),不是快照里那份:管理列表要
// 显示停用的档,而停用的档不参与遮蔽计算(它一个请求都收不到)。
func summarizeAIScopes(rows []AIScope, fallbackBps int) []aiScopeSummaryRow {
	out := make([]aiScopeSummaryRow, 0, len(rows)+1)
	covered := false // 前面是否已经有一条启用的、作用域为空的策略
	for _, r := range rows {
		row := aiScopeSummaryRow{
			Id: r.Id, Name: r.Name, Enabled: r.Enabled, Priority: r.Priority,
			ModelScope: r.ModelScope, GroupScope: r.GroupScope,
			GroupScopeMode: r.GroupScopeMode,
			PreBps:         r.PreSampleRateBps, AsyncBps: r.AsyncSampleRateBps,
			Shadowed: r.Enabled && covered,
		}
		out = append(out, row)
		if r.Enabled && strings.TrimSpace(r.ModelScope) == "" && strings.TrimSpace(r.GroupScope) == "" {
			covered = true
		}
	}
	out = append(out, aiScopeSummaryRow{
		Name: "未匹配任何策略", Fallback: true, Enabled: true,
		Priority: 1 << 30, GroupScopeMode: GroupScopeInclude,
		PreBps: fallbackBps, AsyncBps: fallbackBps,
		// 兜底档被遮住是**正常且常见**的:配了一条"全站 1%"的策略之后,
		// 兜底那一格就再也用不到了。标出来是为了让人知道改它没有用。
		Shadowed: covered,
	})
	return out
}
