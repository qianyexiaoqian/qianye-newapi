// Package groupratio 是扩展侧读取「分组倍率」的唯一口径,并补上上游
// ratio_setting.GetGroupRatio 那条 fail-open 缺口的可观测信号。
//
// ═══════════════════════════ 这个包解决什么 ═══════════════════════════
//
// 上游 setting/ratio_setting/group_ratio.go:79-86 的 GetGroupRatio 在分组名
// 查不到时**返回 1 并只写一条 SysLog**:
//
//	func GetGroupRatio(name string) float64 {
//		ratio, ok := groupRatioMap.Get(name)
//		if !ok {
//			common.SysLog("group ratio not found: " + name)
//			return 1
//		}
//		return ratio
//	}
//
// 任何名字对不上 —— 分组被改名、被从倍率表删掉、大小写写岔 —— 都不报错、
// 不拒绝、**静默按 1.0 倍扣费**。本站有若干个 ratio=0 / 0.125 的分组,
// 它们一旦对不上就从免费/极低价变成原价,而唯一的痕迹是一行会被滚走的 SysLog:
// 没有任何界面、没有任何计数器、没有任何告警。
//
// **本包刻意不改那条 fail-open。** 改它要动 3 条计费路径的调用方(relay/helper/
// price.go、service/quota.go、service/task_billing.go),超出上游改动预算,而且把
// "查不到"改成拒绝会让请求 500 —— 一个配置手误立刻变成全站故障。做法是在上游
// 之外补两层可观测性:
//
//  1. **Resolve** —— 扩展侧任何"要显示一个分组倍率"的地方都必须走它。金额本身
//     仍旧来自 service.GetUserGroupRatio(全仓唯一没有被复制的那一份解析),本包
//     只额外回答"这个数字从哪来、可不可信",并在查不到时把名字记进本进程的
//     失配登记簿 + 限频 SysError。
//  2. **Scan** —— 主动扫 users.group ∪ tokens.group,把"哪些分组名不在倍率表里、
//     影响多少用户与多少令牌"变成一份可以打开看的报表(/api/qy/admin/group-ratio/
//     orphans)与 /admin/health 的一段输出。
//
// ═══════════════════════════ 为什么不折叠大小写 ═══════════════════════════
//
// 倍率侧 ratio_setting.GetGroupGroupRatio / GetGroupRatio 都是**精确 map 查找**,
// 而它们在 3 条计费路径上,我们无权改。若本包在成员资格/展示侧折叠大小写而倍率
// 侧不折叠,就会造出"管理端显示 0.3、实际按兜底 1.0 扣费、零告警"的新骗人数字 ——
// 那正是本轮要消灭的形状。
//
// 所以:**精确匹配,与上游逐位一致**;仅大小写不同的近似项作为 warning 列出
// (Resolution.BaseNearMiss / Orphan.NearMiss),让人看见并自己决定,不替他折叠。
package groupratio

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// 倍率来源。与上游 relay/helper/price.go:58-68 的两个分支一一对应。
const (
	// SourceOverride:命中 GroupGroupRatio[用户分组][模型分组],整体替换兜底倍率。
	SourceOverride = "override"
	// SourceInherit:未命中,回落 GroupRatio[模型分组](模型分组的初始/兜底倍率)。
	SourceInherit = "inherit"
)

// Resolution 是一次 (用户分组, 模型分组) 倍率解析的完整结果。
type Resolution struct {
	// Ratio 是**真正会被乘进账单**的那个倍率,来自 service.GetUserGroupRatio。
	Ratio float64
	// Source 是 Ratio 的来历:SourceOverride / SourceInherit。
	Source string
	// Base 是模型分组的兜底倍率(GroupRatio)。Source==SourceInherit 时 Base==Ratio。
	Base float64
	// BaseMissing 为 true 表示模型分组**不在** GroupRatio 里 —— 此时上游
	// GetGroupRatio 已经 fail-open 返回了 1,Base 也是 1,但那个 1 不是任何人
	// 配出来的,它是一次静默的按原价计费。
	BaseMissing bool
	// BaseNearMiss 非空表示倍率表里存在一个**仅大小写不同**的名字。
	// 这是 BaseMissing 最常见的成因,也是最容易被误判成"配了没生效"的一种。
	BaseNearMiss string
}

// Resolve 解析 (用户分组, 模型分组) 的倍率,并顺带登记失配。
//
// userGroup 传空串表示"兜底口径"——即没有为该模型分组配置专属倍率的那些用户分组
// 看到的价。这与上游 HandleGroupRatio 在 GetGroupGroupRatio 未命中时的分支一致。
//
// 金额刻意走 service.GetUserGroupRatio 而不是在这里再写一遍 if:全仓已经有三份
// 复制的倍率解析分支(price.go:59 / quota.go:119 / task_billing.go:308),第四份
// 复制品迟早会自己漂移,而漂移的表现正是"管理端显示 A、热路径乘 B"。
func Resolve(userGroup, modelGroup string) Resolution {
	r := Resolution{
		Ratio:  service.GetUserGroupRatio(userGroup, modelGroup),
		Source: SourceInherit,
		Base:   1,
	}
	if _, ok := ratio_setting.GetGroupGroupRatio(userGroup, modelGroup); ok {
		r.Source = SourceOverride
	}
	if ratio_setting.ContainsGroupRatio(modelGroup) {
		// Contains 命中时 GetGroupRatio 一定走正常分支,不会再打一条 SysLog。
		r.Base = ratio_setting.GetGroupRatio(modelGroup)
		return r
	}
	r.BaseMissing = true
	r.BaseNearMiss = NearMiss(modelGroup)
	noteMissing(modelGroup, func() string { return r.BaseNearMiss })
	return r
}

// NoteMissingGroup 手动登记一次「这个分组名不在分组倍率表里」。
//
// Resolve 只覆盖"扩展自己去解析一个倍率"的那些组合。有两类真实失配它看不到:
//
//   - **users.group 本身是孤儿。** 上游 middleware/auth.go 的 `if tokenGroup != ""`
//     让空分组令牌整段绕过令牌分组校验,UsingGroup 直接等于 users.group;
//     那个名字若不在 GroupRatio 里,GetGroupRatio 静默返回 1,按原价扣费、零告警。
//     这条路径上没有任何一处会去解析交叉倍率,所以 Resolve 永远撞不到它。
//   - 扩展的清单/解锁快照编译期丢弃的条目。
//
// 调用方在**已经拿到那个名字**的地方顺手调一次即可,不必额外查任何东西。
// 首次必报 + 每 alarmEvery 次限频,与 Resolve 内部同一个登记簿、同一套节流。
//
// nearMiss 由本函数自己算,调用方不必传:算它只是一次 GetGroupRatioCopy 遍历,
// 而让调用方各算各的迟早会有一处算错,那一处的提示就会指向一个不存在的名字。
func NoteMissingGroup(group string) {
	if group == "" {
		return
	}
	noteMissing(group, func() string { return NearMiss(group) })
}

// NearMiss 返回倍率表里与 name 仅大小写不同的那个名字,没有则返回空串。
//
// 只做展示用途,**绝不**拿它去代替 name 查倍率 —— 那就是在成员资格侧折叠、
// 在倍率侧不折叠,会造出与实际扣费不符的界面数字。
func NearMiss(name string) string {
	if name == "" {
		return ""
	}
	folded := strings.ToLower(name)
	for defined := range ratio_setting.GetGroupRatioCopy() {
		if defined != name && strings.ToLower(defined) == folded {
			return defined
		}
	}
	return ""
}

// ─────────────────────────── 失配登记簿(被动信号)───────────────────────────

// maxTrackedGroups 是登记簿的容量上限。
//
// 上限必须存在:分组名来自 users.group / tokens.group / 规则表,理论上是无界的
// 用户可控字符串,无上限的 map 就是一条内存增长路径。本站分组数是个位数,
// 256 这个上限在正常运行中永远碰不到 —— 碰到了本身就是一个值得告警的异常。
const maxTrackedGroups = 256

// alarmEvery 控制同一个名字重复出现时的告警频率。
// 首次必报(那是真正有信息量的一次),之后按次数节流,避免刷屏。
const alarmEvery = 1000

// Miss 是一个失配分组名的累计观测。
type Miss struct {
	Group     string `json:"group"`
	NearMiss  string `json:"near_miss,omitempty"`
	Count     int64  `json:"count"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

var (
	missMu   sync.Mutex
	misses   = map[string]*Miss{}
	overflow int64
)

// noteMissing 登记一次失配。
//
// nearMiss 是**惰性**的:它内部要遍历一遍分组倍率表,而本函数可能挂在热路径上
// (一个孤儿 users.group 会让它的每一次请求都走到这里)。只在真正需要那个字符串的
// 两个时刻求值 —— 新建记录、以及决定要打日志的那一次。
func noteMissing(group string, nearMiss func() string) {
	now := common.GetTimestamp()

	missMu.Lock()
	rec, ok := misses[group]
	if !ok {
		if len(misses) >= maxTrackedGroups {
			overflow++
			missMu.Unlock()
			return
		}
		rec = &Miss{Group: group, NearMiss: nearMiss(), FirstSeen: now}
		misses[group] = rec
	}
	rec.Count++
	rec.LastSeen = now
	count, hintName := rec.Count, rec.NearMiss
	missMu.Unlock()

	if count != 1 && count%alarmEvery != 0 {
		return
	}
	hint := ""
	if nearMiss := hintName; nearMiss != "" {
		hint = fmt.Sprintf(",倍率表里有一个仅大小写不同的 %q(分组倍率按精确匹配,二者是两个分组)", nearMiss)
	}
	common.SysError(fmt.Sprintf(
		"qianye/groupratio: 分组 %q 不在分组倍率表(GroupRatio)里%s —— "+
			"上游 GetGroupRatio 会 fail-open 静默按 1.0 倍计费,累计已观测 %d 次。"+
			"请在「模型分组定价」的分组表里确认这个名字是被删掉了还是拼错了",
		group, hint, count))
}

// Observed 返回本进程启动以来观测到的全部失配分组名,按最近一次出现时间降序。
func Observed() []Miss {
	missMu.Lock()
	out := make([]Miss, 0, len(misses))
	for _, rec := range misses {
		out = append(out, *rec)
	}
	missMu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen != out[j].LastSeen {
			return out[i].LastSeen > out[j].LastSeen
		}
		return out[i].Group < out[j].Group
	})
	return out
}

// ResetObservedForTest 清空登记簿。仅供测试,避免用例之间互相污染。
func ResetObservedForTest() {
	missMu.Lock()
	misses = map[string]*Miss{}
	overflow = 0
	missMu.Unlock()
	lastScan.Store(nil)
}

// ─────────────────────────── 主动扫描(全站信号)───────────────────────────

// Orphan 是一个"不在分组倍率表里"的分组名及其影响面。
type Orphan struct {
	Group string `json:"group"`

	// Users 是 users.group 命中该名字的在册用户数。
	//
	// **这一栏才是资损。** 上游 middleware/auth.go 的 `if tokenGroup != ""` 让
	// 空分组令牌整段绕过分组校验,直接拿 users.group 当 UsingGroup 走进计价;
	// 倍率查不到就按 1.0 扣,没有任何拒绝、没有任何告警。
	Users int64 `json:"users"`

	// Tokens / TokensEnabled 是 tokens.group 命中该名字的令牌数。
	//
	// **这一栏不是资损。** 上游 middleware/auth.go 在令牌分组这条路径上已经用
	// ContainsGroupRatio 挡住了,表现是 403「分组 %s 已被弃用」,请求根本进不到
	// 计价。它列在这里是为了回答另一个问题:有多少人正在撞这个 403。
	Tokens        int64 `json:"tokens"`
	TokensEnabled int64 `json:"tokens_enabled"`

	// NearMiss 非空表示倍率表里存在一个仅大小写不同的名字。
	NearMiss string `json:"near_miss,omitempty"`
}

// ScanResult 是一次全站失配扫描的结果。
type ScanResult struct {
	// At 是本次扫描完成的时间戳。结果带缓存,页面必须显示它,
	// 否则运营会把一份几分钟前的快照当成实时结论。
	At int64 `json:"at"`

	// Orphans 按影响面(用户数 → 令牌数)降序。
	Orphans []Orphan `json:"orphans"`

	// OrphanUsers 是全部失配用户分组下的用户数合计 —— 这个数字大于 0 就意味着
	// 有人正在被按 1.0 倍静默计费。它必须单独给,不能让人自己去加。
	OrphanUsers int64 `json:"orphan_users"`

	// DefinedGroups 是当前分组倍率表里的分组数,用于回答"是不是整张表都空了"。
	DefinedGroups int `json:"defined_groups"`

	// Error 非空表示扫描没跑完,Orphans 不完整。宁可返回一半并说明,
	// 也不能让运营拿着一份看起来干净的报表以为站点没问题。
	Error string `json:"error,omitempty"`
}

// scanCacheSeconds 是扫描结果的缓存时长。
//
// 这两条查询是 users / tokens 上的全表 GROUP BY,不重也不轻;而调用方
// (/admin/health、排障页面)会被反复刷新。缓存把"随手刷新"的代价钳住,
// 同时 60 秒足够短到不会让人对着一份过期结论做决定。
const scanCacheSeconds = 60

var lastScan atomic.Pointer[ScanResult]

// Scan 扫描主库里所有正在被使用的分组名,挑出不在分组倍率表里的那些。
//
// force=false 时命中缓存直接返回上一次结果(带 At 时间戳,调用方自己判断新鲜度)。
//
// 扫描失败**不返回 error**:它是一个诊断接口,半份结果 + 一句说明比一个 500
// 有用得多 —— 而且失败最可能发生的时刻,正是运维最需要打开这一页的时刻。
func Scan(ctx context.Context, force bool) ScanResult {
	if !force {
		if cached := lastScan.Load(); cached != nil &&
			common.GetTimestamp()-cached.At < scanCacheSeconds {
			return *cached
		}
	}

	defined := ratio_setting.GetGroupRatioCopy()
	res := ScanResult{
		At:            common.GetTimestamp(),
		Orphans:       make([]Orphan, 0, 8),
		DefinedGroups: len(defined),
	}

	gdb := model.DB
	if gdb == nil {
		res.Error = "主库未初始化"
		lastScan.Store(&res)
		return res
	}
	gdb = gdb.WithContext(ctx)
	col := model.QyCommonGroupCol()

	// 一行一个分组名的聚合。`group` 是三种方言里的保留字,列名一律走
	// model.QyCommonGroupCol()(PostgreSQL 用 "group"、MySQL/SQLite 用 `group`);
	// 别名 grp/cnt/act 刻意选了非保留字,不需要再引一层。
	type groupCount struct {
		Grp string `gorm:"column:grp"`
		Cnt int64  `gorm:"column:cnt"`
		Act int64  `gorm:"column:act"`
	}

	byName := map[string]*Orphan{}
	orphanOf := func(name string) *Orphan {
		if o, ok := byName[name]; ok {
			return o
		}
		o := &Orphan{Group: name, NearMiss: NearMiss(name)}
		byName[name] = o
		return o
	}

	var userRows []groupCount
	// deleted_at IS NULL:users / tokens 都是软删除,漏掉这个条件会把早已注销的
	// 账号算进影响面,让一个 0 影响的历史分组看起来还有几百人在用。
	userSQL := "SELECT " + col + " AS grp, COUNT(*) AS cnt, 0 AS act " +
		"FROM users WHERE deleted_at IS NULL GROUP BY " + col
	if err := gdb.Raw(userSQL).Scan(&userRows).Error; err != nil {
		res.Error = "统计用户分组失败: " + err.Error()
	}
	for _, row := range userRows {
		if row.Grp == "" || ratio_setting.ContainsGroupRatio(row.Grp) {
			continue
		}
		orphanOf(row.Grp).Users += row.Cnt
	}

	var tokenRows []groupCount
	// SUM(CASE WHEN ...) 而不是 SUM(status = 1):后者在 PostgreSQL 上是布尔求和,
	// 直接语法错误。CASE 形式三种方言通用。
	tokenSQL := "SELECT " + col + " AS grp, COUNT(*) AS cnt, " +
		fmt.Sprintf("SUM(CASE WHEN status = %d THEN 1 ELSE 0 END) AS act ", common.TokenStatusEnabled) +
		"FROM tokens WHERE deleted_at IS NULL GROUP BY " + col
	if err := gdb.Raw(tokenSQL).Scan(&tokenRows).Error; err != nil {
		if res.Error != "" {
			res.Error += "; "
		}
		res.Error += "统计令牌分组失败: " + err.Error()
	}
	for _, row := range tokenRows {
		if row.Grp == "" || ratio_setting.ContainsGroupRatio(row.Grp) {
			continue
		}
		o := orphanOf(row.Grp)
		o.Tokens += row.Cnt
		o.TokensEnabled += row.Act
	}

	for _, o := range byName {
		res.Orphans = append(res.Orphans, *o)
		res.OrphanUsers += o.Users
	}
	sort.Slice(res.Orphans, func(i, j int) bool {
		a, b := res.Orphans[i], res.Orphans[j]
		if a.Users != b.Users {
			return a.Users > b.Users
		}
		if a.Tokens != b.Tokens {
			return a.Tokens > b.Tokens
		}
		return a.Group < b.Group
	})

	if res.OrphanUsers > 0 {
		common.SysError(fmt.Sprintf(
			"qianye/groupratio: 有 %d 个用户的用户分组不在分组倍率表里,"+
				"他们的请求正在按 1.0 倍静默计费(涉及 %d 个分组名)—— "+
				"详见 /api/qy/admin/group-ratio/orphans",
			res.OrphanUsers, len(res.Orphans)))
	}

	lastScan.Store(&res)
	return res
}

// Health 是 /admin/health 里的那一段。
//
// 它同时给出两类信号,缺一不可:
//   - observed  被动:扩展自己解析倍率时撞到的失配名(只覆盖扩展看过的组合)
//   - last_scan 主动:全站 users/tokens 的失配名(覆盖扩展从没看过的组合)
//
// last_scan 为 nil 表示本进程还没扫过一次 —— 那不是"没有问题",而是"还不知道",
// 两者必须能被分开,所以刻意不用一个空结果去冒充。
func Health() map[string]any {
	missMu.Lock()
	dropped := overflow
	missMu.Unlock()

	h := map[string]any{
		"observed":         Observed(),
		"observed_dropped": dropped,
		// orphan_users 单列一项而不是让人自己去 last_scan 里翻。
		//
		// 它是**唯一**能发现"有人正在被静默按 1.0 倍计费"的数字:那条路径
		// (空分组令牌 + 孤儿 users.group)上,上游 GetGroupRatio 的 fail-open
		// 不拒绝、不报错、只写一行会被滚走的 SysLog。埋在一个嵌套结构里的数字
		// 与没有这个数字是同一回事。
		//
		// -1 表示"本进程还没扫过一次"。刻意不用 0 冒充:0 是"扫过了、很干净",
		// 而"还不知道"必须能与它分开 —— 这正是本包在防的那种混淆。
		"orphan_users":    int64(-1),
		"needs_attention": false,
		"scan_never_ran":  true,
	}
	if scan := lastScan.Load(); scan != nil {
		h["last_scan"] = *scan
		h["orphan_users"] = scan.OrphanUsers
		h["scan_never_ran"] = false
		// 扫描本身失败(Error 非空)时 Orphans 不完整,一份看起来干净的报表比
		// 没有报表更危险 —— 所以它同样进 needs_attention。
		h["needs_attention"] = scan.OrphanUsers > 0 || scan.Error != ""
	}
	return h
}
