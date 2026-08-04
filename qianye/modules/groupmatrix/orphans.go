package groupmatrix

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/gin-gonic/gin"
)

// orphans.go —— L0 基线:**与本次改动无关**的存量问题。
//
// ══════════════════════════ 为什么它必须是常驻端点 ══════════════════════════
//
// 「有 545 条令牌今天就已经是保存得下、一发请求就 403」这个数字,必须在运营按
// 保存**之前**就摆在那里,并且明确标注它与本次改动无关。没有基线的影响面报告
// 不可读 —— 第一次预览里那个巨大的数字会让人直接放弃这个功能,
// 或者更糟:以为是自己刚才那次改动造成的。
//
// 根因是上游的一处不对称:controller/token.go 的 AddToken / UpdateToken
// **完全不校验** token.Group,校验只存在于请求时(middleware/auth.go)。
// 本轮在写入侧补上了闸门,所以孤儿从"存量问题"变成"不再新增" ——
// 但存量的那些一条都不会自愈。
//
// ══════════════════════════ 一个诚实的已知偏差 ══════════════════════════
//
// 「令牌分组不在属主可选清单里」这条判据走 service.GetUserUsableGroups,
// 而那个函数**已经被本模块接管**。因此某个用户分组切到 enforce 之后,
// 被那次改动打断的令牌会自动流进这份报告。响应里的 measured_at 与
// enforced_groups 就是说这件事:基线是"此刻的口径",不是"切换前冻结的口径"。
// 事故复盘时必须靠这两个字段判断,而不是靠"报告说它与改动无关"。

// orphanRow 是按分组聚合的一行。
//
// 字段名与 web/src/features/qy/pages/admin-group-matrix/types.ts 的
// QyGmOrphanRow 逐字对齐。Group 是**展示用的复合名**(用户分组 → 模型分组),
// 因为一个模型分组在不同用户分组下的处境完全不同:同一个 vip 对 default 是孤儿、
// 对 paid 可能完全正常,合并成一行会让运营找不到该通知谁。
type orphanRow struct {
	Group string `json:"group"`
	// UserGroup / ModelGroup 是可机读的原始字段,CSV 与后续排查用。
	UserGroup  string `json:"user_group"`
	ModelGroup string `json:"model_group"`

	Count        int64 `json:"count"`
	EnabledCount int64 `json:"enabled_count"`
	Active30d    int64 `json:"active_30d"`

	// Reason 直接给出上游会抛的**原话**,让运营能照着通知用户,
	// 而不是自己去猜"无权访问"和"已被弃用"有什么区别。
	Reason string `json:"reason"`

	// Samples 带 token_id —— 单条「清空分组以修复」的入参就在这里。
	// 没有它,报告页上那个按钮无从下手。
	Samples []tokenSample `json:"samples"`
}

type orphanReport struct {
	// TokenTotal / UserTotal 是**全部失效令牌**的合计(无权访问 + 分组已弃用)。
	//
	// 刻意合并这两类:它们在 preview 的块 B 里也是合并统计的,
	// 两个面板对"存量欠账"给出相差 5 倍的两个数字,只会让运营两个都不信,
	// 进而不再信任块 A —— 而块 A 才是本次改动的责任。
	TokenTotal int64 `json:"token_total"`
	UserTotal  int64 `json:"user_total"`

	// OrphanTokens:令牌分组非空、且不在其属主用户分组的可选清单里。
	OrphanTokens []orphanRow `json:"orphan_tokens"`
	// DeprecatedTokens:令牌分组已不在分组倍率表里(上游的「分组已被弃用」)。
	DeprecatedTokens []orphanRow `json:"deprecated_tokens"`
	// AutoGroupTokens:auto 候选里含已失效分组。静默缩短候选列表没有任何
	// 其它信号,必须单列。
	AutoGroupTokens []orphanRow `json:"auto_group_tokens"`
	// OrphanUsers:users.group 不在分组倍率表里的用户。
	//
	// ⚠ 这一栏是**看门狗不是救火**:上游 GetGroupRatio 找不到分组时静默返回 1
	// 并只写一条 SysLog。这些用户的每一笔请求都在按 1.0 倍扣费,零告警。
	// 本模块的 hook 射程之外(空分组令牌绕过全部检查),只能探测,不能拦截。
	OrphanUsers []orphanRow `json:"orphan_users"`

	// EmptyGroupTokens 单独一栏并显式标注结构性免疫。
	EmptyGroupTokens int64  `json:"empty_group_tokens"`
	EmptyGroupNote   string `json:"empty_group_note"`

	// Incomplete 为 true 时某一段统计没跑成功,数字偏小。
	Incomplete bool `json:"incomplete"`

	// MeasuredAt / EnforcedGroups 是这份基线的**时点与口径**。
	// 「与改动无关」这句话只在 enforced_groups 为空时严格成立(见文件头)。
	MeasuredAt     int64    `json:"measured_at"`
	EnforcedGroups []string `json:"enforced_groups"`

	Note string `json:"note"`
}

func adminOrphans(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagGroupMatrix) {
		return
	}
	rep, err := buildOrphanReport()
	if err != nil {
		internalError(c, err)
		return
	}
	if c.Query("format") == "csv" {
		writeOrphanCSV(c, rep)
		return
	}
	respond(c, rep)
}

func buildOrphanReport() (*orphanReport, error) {
	rep := &orphanReport{
		OrphanTokens: make([]orphanRow, 0), DeprecatedTokens: make([]orphanRow, 0),
		AutoGroupTokens: make([]orphanRow, 0), OrphanUsers: make([]orphanRow, 0),
		EnforcedGroups: enforcedGroupNames(),
		MeasuredAt:     common.GetTimestamp(),
		EmptyGroupNote: "分组为空的令牌**结构性免疫**可选性检查:上游 middleware/auth.go 的 " +
			"`if tokenGroup != \"\"` 让它们整段绕过,使用分组恒等于 users.group。" +
			"这个数字大不代表有风险,它只是说明大多数用户没有显式指定分组。",
		Note: "本报告是**当前口径**下的存量盘点。根因是上游的写入侧不校验令牌分组," +
			"而校验只发生在请求时 —— 本轮已在写入侧补上闸门,所以这些数字不会再因为新建令牌而增长。" +
			"⚠ 但判据走的是 service.GetUserUsableGroups,而它已被本模块接管:" +
			"enforced_groups 非空时,那些分组被本功能打断的令牌**也会出现在这里**。" +
			"复盘时请以 measured_at 与 enforced_groups 为准,不要把这份报告当成切换前的冻结基线。",
	}
	stats, err := tokenPairStats()
	if err != nil {
		return nil, err
	}
	rep.EmptyGroupTokens = stats.emptyGroup

	// 每个用户分组的可选清单只算一次:pairs 里同一个用户分组会出现多行。
	allowed := map[string]map[string]struct{}{}
	usable := func(ug string) map[string]struct{} {
		if set, ok := allowed[ug]; ok {
			return set
		}
		set := map[string]struct{}{}
		for name := range service.GetUserUsableGroups(ug) {
			set[name] = struct{}{}
		}
		allowed[ug] = set
		return set
	}

	affectedUsers := int64(0)
	for _, p := range stats.pairs {
		if p.ModelGroup == "" {
			continue
		}
		row := orphanRow{
			Group:     p.UserGroup + " → " + p.ModelGroup,
			UserGroup: p.UserGroup, ModelGroup: p.ModelGroup,
			Count: p.Tokens, EnabledCount: p.TokensActive, Active30d: p.TokensRecent,
			Samples: make([]tokenSample, 0),
		}
		switch {
		case !ratio_setting.ContainsGroupRatio(p.ModelGroup):
			row.Reason = fmt.Sprintf("请求时会被上游拒绝:「分组 %s 已被弃用」(该分组已不在分组倍率表里)", p.ModelGroup)
			rep.DeprecatedTokens = append(rep.DeprecatedTokens, row)
		default:
			if _, ok := usable(p.UserGroup)[p.ModelGroup]; ok {
				continue
			}
			row.Reason = fmt.Sprintf("请求时会被上游拒绝:「无权访问 %s 分组」", p.ModelGroup)
			rep.OrphanTokens = append(rep.OrphanTokens, row)
		}
		rep.TokenTotal += p.Tokens
		affectedUsers += p.Users
	}
	rep.UserTotal = affectedUsers

	byTokens := func(rows []orphanRow) {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Count != rows[j].Count {
				return rows[i].Count > rows[j].Count
			}
			return rows[i].Group < rows[j].Group
		})
	}
	byTokens(rep.OrphanTokens)
	byTokens(rep.DeprecatedTokens)

	// 样本必须带 token_id:界面上「清空分组以修复」的入参就是它。
	attachOrphanSamples(rep.OrphanTokens)
	attachOrphanSamples(rep.DeprecatedTokens)

	if n := countAutoGroupsShrink(); n > 0 {
		rep.AutoGroupTokens = append(rep.AutoGroupTokens, orphanRow{
			Group: "auto_groups", Count: n, Samples: make([]tokenSample, 0),
			Reason: "这些令牌的 auto 候选列表里含已失效分组,候选会被静默缩短 —— " +
				"请求照常成功,只是永远轮不到那个分组,没有任何其它信号。" +
				"修复方式是让用户在令牌编辑页重选 auto 候选,不能由后台代改。",
		})
	}

	users, incomplete := orphanUserGroups()
	rep.OrphanUsers = users
	rep.Incomplete = incomplete
	return rep, nil
}

// enforcedGroupNames 列出当前真的在 enforce 的用户分组。它决定了上面那份
// 基线还算不算"与本功能无关"。
func enforcedGroupNames() []string {
	out := make([]string, 0)
	s, ok := SnapshotView()
	if !ok {
		return out
	}
	for name, sc := range s.Scopes {
		if sc.Mode == ModeEnforce {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// attachOrphanSamples 给每一行补令牌样本(带 token_id 与掩码 key)。
func attachOrphanSamples(rows []orphanRow) {
	pairs := make([]pairImpact, 0, len(rows))
	for _, r := range rows {
		pairs = append(pairs, pairImpact{
			UserGroup: r.UserGroup, ModelGroup: r.ModelGroup,
			Samples: make([]tokenSample, 0),
		})
	}
	attachSamples(pairs, orphanSampleLimit)
	for i := range rows {
		rows[i].Samples = pairs[i].Samples
	}
}

// orphanSampleLimit 是基线里每一行的样本上限。
//
// 比预览那一侧更小:这份报告一次列出全部历史欠账(本站 34 个组合),
// 每行都拉 20 条带掩码 key 的记录既慢又没必要 —— 运营需要的是"能点开修一条",
// 完整名单走 CSV 导出。
const orphanSampleLimit = 5

// orphanUserGroups 找出 users.group 不在分组倍率表里的用户。
//
// 这些用户的**每一笔请求**都在按 1.0 倍扣费而没有任何告警(上游 GetGroupRatio
// 未命中时返回 1 并只写一条 SysLog)。本站配了几个 ratio=0 / 0.125 的分组,
// 一旦名字对不上就从免费/极低价变成原价。
//
// 第二个返回值是"统计没跑成功"。这一条不能吞:它是那条 fail-open 的唯一探测器,
// 失败时静默返回空切片,界面会显示"干净"而钱照漏。
func orphanUserGroups() ([]orphanRow, bool) {
	out := make([]orphanRow, 0)
	if model.DB == nil {
		return out, true
	}
	type row struct {
		Grp string `gorm:"column:grp"`
		N   int64  `gorm:"column:n"`
	}
	col := model.QyCommonGroupCol()
	var rows []row
	err := groupByRaw(model.DB.Model(&model.User{}).
		Select(col+" as grp, count(*) as n"), col).Scan(&rows).Error
	if err != nil {
		common.SysError("qianye/groupmatrix: 统计用户分组失败(1.0 倍静默扣费的探测器本次没跑成): " + err.Error())
		return out, true
	}
	for _, r := range rows {
		if r.Grp == "" || ratio_setting.ContainsGroupRatio(r.Grp) {
			continue
		}
		out = append(out, orphanRow{
			Group: r.Grp, UserGroup: r.Grp, Count: r.N, Samples: make([]tokenSample, 0),
			Reason: fmt.Sprintf("%d 个用户的用户分组 %q 不在分组倍率表里 —— "+
				"他们的请求正在按 1.0 倍静默扣费(上游未命中时返回 1 且只写一条系统日志)", r.N, r.Grp),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, false
}

// writeOrphanCSV 导出基线。运营拿它去**主动通知用户自己改**,
// 而不是替用户改 —— 批量改写令牌分组不可逆,而"用户当初选这个分组是有原因的"
// 这件事系统不知道。
func writeOrphanCSV(c *gin.Context, rep *orphanReport) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="qy_group_matrix_orphans.csv"`)
	// UTF-8 BOM:不带的话 Excel 打开会把中文列名显示成乱码,
	// 而这份 CSV 的唯一用途就是运营用 Excel 打开它去联系人。
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	w := csv.NewWriter(c.Writer)
	defer w.Flush()
	_ = w.Write([]string{"kind", "user_group", "model_group", "count", "enabled_count",
		"active_30d", "reason", "token_id", "token_name", "key_masked", "user_id", "username"})
	emit := func(kind string, rows []orphanRow) {
		for _, r := range rows {
			_ = w.Write([]string{kind, r.UserGroup, r.ModelGroup,
				strconv.FormatInt(r.Count, 10),
				strconv.FormatInt(r.EnabledCount, 10),
				strconv.FormatInt(r.Active30d, 10), r.Reason, "", "", "", "", ""})
			for _, s := range r.Samples {
				// 样本行的汇总列留空:同一个计数出现两次会被 Excel 的求和直接算错。
				_ = w.Write([]string{kind, r.UserGroup, r.ModelGroup, "", "", "", "",
					strconv.Itoa(s.TokenId), s.TokenName, s.MaskedKey,
					strconv.Itoa(s.UserId), s.Username})
			}
		}
	}
	emit("no_permission", rep.OrphanTokens)
	emit("deprecated_group", rep.DeprecatedTokens)
	emit("auto_groups", rep.AutoGroupTokens)
	emit("orphan_user_group", rep.OrphanUsers)
	c.Status(http.StatusOK)
}
