package violation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// api_admin_batch_test.go —— 规则列表多选批量操作的回归。
//
// 这一层守的是四件事,每一件都对应一种"接口 200、界面正常、防护其实变了"的形态:
//
//  1. **覆盖与追加是两件事,而且不许互相冒充。** 一次以为在追加、实际在覆盖的批量,
//     会把一批规则原有的作用分组整串抹掉,而列表上那几条规则看起来一个字都没改。
//  2. **方向(include / exclude)必须跟着名单一起说清楚。** 同一串分组名在两个方向下
//     含义完全相反:追加到 exclude 名单上是**多豁免一个分组**,而操作者以为自己
//     多防了一个。所以 append / remove 遇到方向不一致一律拒做,绝不替他翻向。
//  3. **三档计数必须互斥且合起来等于全集。** "选 20 条、成功 18 条"如果分不清剩下
//     2 条是失败了还是本来就不用动,管理员会去排查一个不存在的故障。
//  4. **批量不能成为 mode 的第二个入口。** 批量只写 enabled / group_scope 那几列,
//     一个字节都不许碰 mode、pattern、action、fee_*。

// scopedSeedRule 造一条带作用分组的规则。绕过 ValidateRule 是刻意的:
// 测试里要造出"库里存着 mode 为空串的历史行"这类状态,而写接口本来就拦得住它。
func scopedSeedRule(t *testing.T, gdb *gorm.DB, scope, mode string) *Rule {
	t.Helper()
	row := goodRule(true)
	row.GroupScope = scope
	row.GroupScopeMode = mode
	return seedRule(t, gdb, row)
}

// ───────────────────────── 入参归一 ─────────────────────────

func TestNormalizeRuleIds(t *testing.T) {
	tests := []struct {
		name    string
		in      []int64
		want    []int64
		wantErr string
	}{
		{"空选中整批拒绝", nil, nil, "请先勾选要操作的规则"},
		{"空数组同理", []int64{}, nil, "请先勾选要操作的规则"},
		// 去重静默合并:同一个 id 出现两次是前端的事,对管理员来说"这条规则被启用了"
		// 就是一件事,报两行只会让计数看起来对不上。
		{"重复 id 静默合并", []int64{3, 1, 3, 1, 2}, []int64{3, 1, 2}, ""},
		// 非法 id 整批拒绝而不是逐条 failed:它不可能来自列表页的勾选框,
		// 计成"这一条失败了"会让报告里混进一条管理员看不懂也无从处理的行。
		{"0 号 id 整批拒绝", []int64{1, 0}, nil, "非法的规则 id: 0"},
		{"负数 id 整批拒绝", []int64{-5}, nil, "非法的规则 id: -5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeRuleIds(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("超过上限整批拒绝", func(t *testing.T) {
		ids := make([]int64, maxRuleBatchIds+1)
		for i := range ids {
			ids[i] = int64(i + 1)
		}
		_, err := normalizeRuleIds(ids)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "分批")

		ids = ids[:maxRuleBatchIds]
		got, err := normalizeRuleIds(ids)
		require.NoError(t, err, "刚好等于上限必须放行,否则上限实际是 %d-1", maxRuleBatchIds)
		assert.Len(t, got, maxRuleBatchIds)
	})
}

func TestNormalizeBatchGroups(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{"去空白与空项", []string{" vip ", "", "  ", "svip"}, []string{"vip", "svip"}, ""},
		// 按判定口径(groupname.Effective)折叠去重:compiledRule 就是这样建索引的,
		// 不折叠的话名单里会留下两份等价项白占长度上限。
		{"大小写等价项只留一份", []string{"VIP", "vip", "Vip"}, []string{"VIP"}, ""},
		// 判定不看大小写,但界面看:把操作者填的 "VIP" 改写成 "vip"
		// 是一次没有人要求过的改写。
		{"保留操作者写下的原始大小写", []string{"VIP"}, []string{"VIP"}, ""},
		// group_scope 是逗号/换行分隔的一串名字。放一个带逗号的"名字"进去,
		// 存下来就是两个分组,而界面上还显示成一个。
		{"名字里带逗号整批拒绝", []string{"vip,svip"}, nil, "不能包含逗号或换行"},
		{"名字里带换行整批拒绝", []string{"vip\nsvip"}, nil, "不能包含逗号或换行"},
		{"名字过长整批拒绝", []string{strings.Repeat("g", maxGroupNameRunes+1)}, nil, "分组名过长"},
		{"刚好等于上限放行", []string{strings.Repeat("g", maxGroupNameRunes)},
			[]string{strings.Repeat("g", maxGroupNameRunes)}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeBatchGroups(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestGroupScopeMaxRunesTracksColumnLimit 钉住"长度上限只有一份事实"。
//
// 批量层不另抄一个 1024:两份上限一旦漂移,批量就会写进一条数据库拒绝的行
// (列被改窄)或拦下一条数据库接受的行(列被改宽)—— 那正是 ruleVarcharLimits
// 这张表存在的理由。
func TestGroupScopeMaxRunesTracksColumnLimit(t *testing.T) {
	for _, lim := range ruleVarcharLimits {
		if lim.Field == "GroupScope" {
			assert.Equal(t, lim.Max, groupScopeMaxRunes)
			return
		}
	}
	t.Fatal("ruleVarcharLimits 里没有 GroupScope")
}

// ───────────────────────── 覆盖 vs 追加 vs 移除 ─────────────────────────

// TestPlanGroupScope 是本文件的核心:三种写法的语义各自是什么,以及它们在
// 方向不一致时的行为。
func TestPlanGroupScope(t *testing.T) {
	tests := []struct {
		name        string
		curScope    string
		curMode     string
		op          string
		groups      []string
		wantMode    string
		wantScope   string
		wantNewMode string
		wantOutcome string
		wantCode    string
	}{
		// ── 覆盖 ──
		{
			name: "覆盖:整串换掉,方向一起换", curScope: "vip,svip", curMode: GroupScopeInclude,
			op: batchScopeReplace, groups: []string{"batch"}, wantMode: GroupScopeInclude,
			wantScope: "batch", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},
		{
			// 覆盖是唯一允许翻转方向的写法 —— "覆盖"这个词本身就包含了方向。
			name: "覆盖:允许把 include 规则翻成 exclude", curScope: "vip", curMode: GroupScopeInclude,
			op: batchScopeReplace, groups: []string{"batch"}, wantMode: GroupScopeExclude,
			wantScope: "batch", wantNewMode: GroupScopeExclude, wantOutcome: batchItemOK,
		},
		{
			// 清空 = 对全部分组生效,是一次**放宽**。方向随之折回 include:
			// "空黑名单"与"空白名单"语义完全相同,留两个等价状态只会让界面上
			// 出现一个看得见、却什么都不改变的开关(与 ruleUpsertReq.apply 同口径)。
			name:     "覆盖:空名单 = 对全部分组生效,方向折回 include",
			curScope: "vip", curMode: GroupScopeExclude,
			op: batchScopeReplace, groups: nil, wantMode: GroupScopeExclude,
			wantScope: "", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},

		// ── 追加 ──
		{
			name: "追加:并进末尾,不动已有项", curScope: "vip", curMode: GroupScopeInclude,
			op: batchScopeAppend, groups: []string{"svip"}, wantMode: GroupScopeInclude,
			wantScope: "vip,svip", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},
		{
			// 已经在名单上的按判定口径识别:compiledRule 用 groupname.Effective 建索引,
			// "VIP" 与 "vip" 是同一个分组,再追加一份纯属白占长度上限。
			name: "追加:已在名单上(大小写不同)= 无变化", curScope: "vip", curMode: GroupScopeInclude,
			op: batchScopeAppend, groups: []string{"VIP"}, wantMode: GroupScopeInclude,
			wantOutcome: batchItemSkipped, wantCode: batchCodeNoChange,
		},
		{
			// 空名单没有方向可言(空名单时 mode 恒为 include),所以往它上面追加
			// 永远合法,方向取请求值。这是一次**收窄**:从"全部分组"变成"只对 vip"。
			name: "追加:空作用域的规则,方向取请求值", curScope: "", curMode: GroupScopeInclude,
			op: batchScopeAppend, groups: []string{"vip"}, wantMode: GroupScopeExclude,
			wantScope: "vip", wantNewMode: GroupScopeExclude, wantOutcome: batchItemOK,
		},
		{
			// 本文件最重要的一条。给一条 exclude 规则追加 "vip",在 include 语义下是
			// "多防一个分组",在它自己的 exclude 语义下却是"多豁免一个分组"——
			// 结果相反。任何自动处理都是替操作者做了一个他没做过的决定。
			name: "追加:方向不一致 —— 拒做,不翻向", curScope: "batch", curMode: GroupScopeExclude,
			op: batchScopeAppend, groups: []string{"vip"}, wantMode: GroupScopeInclude,
			wantOutcome: batchItemFailed, wantCode: batchCodeDirectionMismatch,
		},
		{
			// 历史行(这一列出现之前写入的)方向是空串,按 include 读 ——
			// 那正是这一列出现之前的唯一语义。
			name: "追加:历史行的空方向按 include 读", curScope: "vip", curMode: "",
			op: batchScopeAppend, groups: []string{"svip"}, wantMode: GroupScopeInclude,
			wantScope: "vip,svip", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},
		{
			name: "追加:历史行的空方向与 exclude 请求不一致", curScope: "vip", curMode: "",
			op: batchScopeAppend, groups: []string{"svip"}, wantMode: GroupScopeExclude,
			wantOutcome: batchItemFailed, wantCode: batchCodeDirectionMismatch,
		},

		// ── 移除 ──
		{
			name: "移除:按判定口径摘掉", curScope: "vip,svip,batch", curMode: GroupScopeInclude,
			op: batchScopeRemove, groups: []string{"SVIP"}, wantMode: GroupScopeInclude,
			wantScope: "vip,batch", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},
		{
			// 摘空了 = 对全部分组生效,方向折回 include。这是一次**大幅放宽**,
			// 对 exclude 规则尤其如此:原本只豁免 batch,现在对所有分组都生效。
			name: "移除:摘空之后方向折回 include", curScope: "batch", curMode: GroupScopeExclude,
			op: batchScopeRemove, groups: []string{"batch"}, wantMode: GroupScopeExclude,
			wantScope: "", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},
		{
			name: "移除:名字不在名单上 = 无变化", curScope: "vip", curMode: GroupScopeInclude,
			op: batchScopeRemove, groups: []string{"batch"}, wantMode: GroupScopeInclude,
			wantOutcome: batchItemSkipped, wantCode: batchCodeNoChange,
		},
		{
			name: "移除:方向不一致同样拒做", curScope: "batch", curMode: GroupScopeExclude,
			op: batchScopeRemove, groups: []string{"batch"}, wantMode: GroupScopeInclude,
			wantOutcome: batchItemFailed, wantCode: batchCodeDirectionMismatch,
		},

		// ── 无变化的识别口径 ──
		{
			// 库里存的可能带空格。照原始字符串比会把一次纯粹的空白重排报成"改动过",
			// 于是一批什么都没变的规则会刷新 updated_at、bump 规则版本、
			// 让所有节点白拉一遍全表。
			name: "覆盖成等价名单(仅空白差异)= 无变化", curScope: "vip, svip", curMode: GroupScopeInclude,
			op: batchScopeReplace, groups: []string{"vip", "svip"}, wantMode: GroupScopeInclude,
			wantOutcome: batchItemSkipped, wantCode: batchCodeNoChange,
		},
		{
			// 顺序敏感:名单顺序不影响判定,但它是管理员在界面上读到的东西,
			// 一次重排对他来说就是一次改动。
			name: "覆盖成同一批名字但顺序不同 = 有变化", curScope: "vip,svip", curMode: GroupScopeInclude,
			op: batchScopeReplace, groups: []string{"svip", "vip"}, wantMode: GroupScopeInclude,
			wantScope: "svip,vip", wantNewMode: GroupScopeInclude, wantOutcome: batchItemOK,
		},
		{
			// 名单一样但方向翻了 —— 这是含义完全相反的一次改动,绝不能报"无变化"。
			name: "名单相同但方向翻转 = 有变化", curScope: "vip", curMode: GroupScopeInclude,
			op: batchScopeReplace, groups: []string{"vip"}, wantMode: GroupScopeExclude,
			wantScope: "vip", wantNewMode: GroupScopeExclude, wantOutcome: batchItemOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := &Rule{Id: 1, GroupScope: tc.curScope, GroupScopeMode: tc.curMode}
			scope, mode, outcome, code, _ := planGroupScope(row, tc.op, tc.groups, tc.wantMode)
			assert.Equal(t, tc.wantOutcome, outcome)
			if tc.wantOutcome != batchItemOK {
				assert.Equal(t, tc.wantCode, code)
				return
			}
			assert.Equal(t, tc.wantScope, scope)
			assert.Equal(t, tc.wantNewMode, mode)
		})
	}
}

// ───────────────────────── 落库:只动该动的列 ─────────────────────────

// TestApplyBatchGroupScopeOnlyTouchesScopeColumns 是"静默回滚"这一形状的回归。
//
// 场景:管理员 A 勾了 20 条规则准备批量追加一个模型分组,B 在这期间把其中一条的
// pattern 改窄、把 mode 从真实调回影子。批量若用 Save(row) 写回整行,B 的改动会被
// 无声抹掉 —— 而抹掉的正是决定谁被扣钱、谁被封号的那几列。
func TestApplyBatchGroupScopeOnlyTouchesScopeColumns(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	row := scopedSeedRule(t, gdb, "vip", GroupScopeInclude)
	require.NoError(t, gdb.Model(&Rule{}).Where("id = ?", row.Id).
		Updates(map[string]any{"mode": ModeEnforce, "pattern": "越狱\n破限"}).Error)

	// A 手上那份快照还是旧的(mode=shadow、pattern 是别的),而 B 已经改过库。
	stale := loadRule(t, gdb, row.Id)
	stale.Mode = ModeShadow
	stale.Pattern = "旧词表"
	stale.Action = ActionBlockAndCharge

	outcome, code, detail := applyBatchGroupScope(gdb, &stale, batchScopeAppend,
		[]string{"svip"}, GroupScopeInclude, 42, 9000)
	require.Equal(t, batchItemOK, outcome, "code=%s detail=%s", code, detail)

	after := loadRule(t, gdb, row.Id)
	assert.Equal(t, "vip,svip", after.GroupScope)
	assert.Equal(t, GroupScopeInclude, after.GroupScopeMode)
	assert.Equal(t, ModeEnforce, after.Mode, "批量作用分组把 mode 写回去了 —— 那是「真拦人」的开关")
	assert.Equal(t, "越狱\n破限", after.Pattern, "批量作用分组把别人改窄的词表回滚了")
	assert.Equal(t, ActionRecord, after.Action, "批量作用分组改掉了处置动作")
	// updated_at / updated_by 必须跟着走:它们是"谁在什么时候改的"在列表上的唯一
	// 可见投影,不写的话审计日志与界面就此对不上。
	assert.Equal(t, int64(9000), after.UpdatedAt)
	assert.Equal(t, 42, after.UpdatedBy)
}

// TestApplyBatchGroupScopeDetectsConcurrentEdit 固化 CAS。
//
// 两个管理员同时批量改同一批规则时,后到的那次必须如实报"已被改过",
// 而不是把别人刚写的东西盖掉。
func TestApplyBatchGroupScopeDetectsConcurrentEdit(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	row := scopedSeedRule(t, gdb, "vip", GroupScopeInclude)
	stale := loadRule(t, gdb, row.Id)

	// B 抢先把作用域改掉。
	require.NoError(t, gdb.Model(&Rule{}).Where("id = ?", row.Id).
		Update("group_scope", "batch").Error)

	outcome, code, _ := applyBatchGroupScope(gdb, &stale, batchScopeReplace,
		[]string{"svip"}, GroupScopeInclude, 42, 9000)
	assert.Equal(t, batchItemFailed, outcome)
	assert.Equal(t, batchCodeStale, code)
	assert.Equal(t, "batch", loadRule(t, gdb, row.Id).GroupScope,
		"后到的批量把另一个管理员刚写的作用域盖掉了")
}

// TestApplyBatchGroupScopeRejectsOverlongResult 固化长度闸。
//
// 没有它的时候超长是靠 MySQL 的 Error 1406 挡下来的,那条错误会被折成一句
// "处理失败,请稍后重试";SQLite 更糟 —— 它根本不校验 varchar 长度,同一份数据
// 在 SQLite 上存得进去、迁到 MySQL 就整条 UPDATE 失败。
func TestApplyBatchGroupScopeRejectsOverlongResult(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	long := strings.Repeat("g", maxGroupNameRunes)
	current := make([]string, 0, 16)
	for len(strings.Join(current, ","))+len(long) < groupScopeMaxRunes {
		current = append(current, long)
	}
	row := scopedSeedRule(t, gdb, strings.Join(current, ","), GroupScopeInclude)
	before := loadRule(t, gdb, row.Id)

	outcome, code, detail := applyBatchGroupScope(gdb, &before, batchScopeAppend,
		[]string{strings.Repeat("h", maxGroupNameRunes)}, GroupScopeInclude, 42, 9000)
	require.Equal(t, batchItemFailed, outcome, "detail=%s", detail)
	assert.Equal(t, batchCodeTooLong, code)
	assert.Equal(t, before.GroupScope, loadRule(t, gdb, row.Id).GroupScope,
		"超长被拒之后不该留下半截写入")
}

// ───────────────────────── 批量启停 ─────────────────────────

// TestApplyBatchEnabledThreeWaySplit 固化三档计数的互斥。
//
// 把"本来就是启用的"算进失败里,一次"全选 → 批量启用"会报「18 条启用失败」,
// 管理员会去排查一个根本不存在的故障。
func TestApplyBatchEnabledThreeWaySplit(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	already := seedRule(t, gdb, goodRule(true))
	off := seedRule(t, gdb, goodRule(false))
	broken := goodRule(false)
	broken.MatchType = MatchRegex
	broken.Pattern = "(未闭合"
	bad := seedRule(t, gdb, broken)

	res := runRuleBatch(gdb, []int64{already.Id, off.Id, bad.Id, 4040},
		func(row *Rule) (string, string, string) {
			return applyBatchEnabled(gdb, row, true, false, 7, 2000)
		})

	require.Equal(t, 4, res.Total)
	assert.Equal(t, 1, res.Succeeded)
	assert.Equal(t, 1, res.Skipped)
	assert.Equal(t, 2, res.Failed)
	// 恒等式是前端唯一能信的东西:少了它,"选 4 条、成功 1 条"就无法回答
	// 剩下 3 条是失败了还是本来就不用动。
	assert.Equal(t, res.Total, res.Succeeded+res.Skipped+res.Failed)

	byId := map[int64]batchRuleItem{}
	for _, item := range res.Items {
		byId[item.Id] = item
	}
	assert.Equal(t, batchItemSkipped, byId[already.Id].Outcome)
	assert.Equal(t, batchCodeNoChange, byId[already.Id].Code)
	assert.Equal(t, batchItemOK, byId[off.Id].Outcome)
	// 启用一条编译不过的规则必须被拒:reloadCtx 对编译失败的规则是静默跳过的,
	// 放行的话就是"批量启用报成功、界面显示已启用、线上永不命中"。
	assert.Equal(t, batchCodeWontCompile, byId[bad.Id].Code)
	assert.False(t, loadRule(t, gdb, bad.Id).Enabled)
	// 不存在的 id 逐条报,不整批炸:批次跑到一半发现有人删了一条规则,
	// 剩下 19 条不该跟着一起失败。
	assert.Equal(t, batchCodeNotFound, byId[4040].Code)
	assert.Equal(t, "", byId[4040].Name)
	assert.Equal(t, already.Name, byId[already.Id].Name,
		"失败/跳过列表里只有 id 的话,管理员得回列表页一个个对照才知道是哪条")
}

// TestApplyBatchEnabledNeedsEnforceAck 是"批量把一批规则送进真实执行"这条路的闸。
//
// 启用一条已经是 enforce 的规则,效果与把它从影子切成真实一模一样:下一秒开始
// 真的扣钱、阻断、累计封号。批量入口看不到 pattern 与作用域,所以这一档必须被
// 单独确认过才放行。
func TestApplyBatchEnabledNeedsEnforceAck(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	enforceOff := goodRule(false)
	enforceOff.Mode = ModeEnforce
	pending := seedRule(t, gdb, enforceOff)

	row := loadRule(t, gdb, pending.Id)
	outcome, code, _ := applyBatchEnabled(gdb, &row, true, false, 7, 2000)
	assert.Equal(t, batchItemFailed, outcome)
	assert.Equal(t, batchCodeEnforceAck, code)
	assert.False(t, loadRule(t, gdb, pending.Id).Enabled,
		"没有确认过就把一条真实模式规则打开了 —— 那是真的开始扣钱")

	row = loadRule(t, gdb, pending.Id)
	outcome, _, _ = applyBatchEnabled(gdb, &row, true, true, 7, 2000)
	assert.Equal(t, batchItemOK, outcome, "确认之后必须能真的启用,否则这个闸就是死路")
	assert.True(t, loadRule(t, gdb, pending.Id).Enabled)
}

// TestApplyBatchEnabledNeverNeedsAckToDisable 固化"停用永远畅通"。
//
// 关掉一条正在误伤的 enforce 规则是紧急出口。给它加一道确认闸,等于让人在
// 线上正在误封用户的时候先去回答一个问句。
func TestApplyBatchEnabledNeverNeedsAckToDisable(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	enforceOn := goodRule(true)
	enforceOn.Mode = ModeEnforce
	row := seedRule(t, gdb, enforceOn)

	cur := loadRule(t, gdb, row.Id)
	outcome, code, _ := applyBatchEnabled(gdb, &cur, false, false, 7, 2000)
	assert.Equal(t, batchItemOK, outcome, "code=%s", code)
	assert.False(t, loadRule(t, gdb, row.Id).Enabled)
}

// TestBatchEnabledNeverTouchesMode 是"批量不能成为 mode 的第二个入口"这条边。
//
// 项目方对影子/真实的原话是「绑定到规则上」,而 mode 是本模块唯一决定"要不要真的
// 扣钱封号"的开关。批量启停只写 enabled / updated_at / updated_by 三列,
// 批量作用分组只写 group_scope / group_scope_mode / updated_at / updated_by 四列。
func TestBatchEnabledNeverTouchesMode(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	shadow := seedRule(t, gdb, goodRule(false))
	enforceRow := goodRule(false)
	enforceRow.Mode = ModeEnforce
	enforce := seedRule(t, gdb, enforceRow)

	res := runRuleBatch(gdb, []int64{shadow.Id, enforce.Id},
		func(row *Rule) (string, string, string) {
			return applyBatchEnabled(gdb, row, true, true, 7, 2000)
		})
	require.Equal(t, 2, res.Succeeded)

	assert.Equal(t, ModeShadow, loadRule(t, gdb, shadow.Id).Mode,
		"批量启用把影子规则转正了 —— 那一批规则下一秒就开始真的扣钱")
	assert.Equal(t, ModeEnforce, loadRule(t, gdb, enforce.Id).Mode,
		"批量启用把一条真实规则降成了影子 —— 一道正在生效的防护被静默关掉")
}

// TestBatchGroupScopeMixedDirections 是"部分失败"的完整形态。
//
// 一批规则里方向本来就是混的(现网必然如此),一次 append 只应改动方向一致的那些,
// 其余如实上报并原样保留 —— 而不是整批回滚,也不是替它们翻向。
func TestBatchGroupScopeMixedDirections(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	inc := scopedSeedRule(t, gdb, "vip", GroupScopeInclude)
	exc := scopedSeedRule(t, gdb, "batch", GroupScopeExclude)
	empty := scopedSeedRule(t, gdb, "", GroupScopeInclude)
	dup := scopedSeedRule(t, gdb, "svip", GroupScopeInclude)

	res := runRuleBatch(gdb, []int64{inc.Id, exc.Id, empty.Id, dup.Id},
		func(row *Rule) (string, string, string) {
			return applyBatchGroupScope(gdb, row, batchScopeAppend,
				[]string{"svip"}, GroupScopeInclude, 7, 3000)
		})

	assert.Equal(t, 2, res.Succeeded)
	assert.Equal(t, 1, res.Skipped)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, res.Total, res.Succeeded+res.Skipped+res.Failed)

	assert.Equal(t, "vip,svip", loadRule(t, gdb, inc.Id).GroupScope)
	assert.Equal(t, "svip", loadRule(t, gdb, empty.Id).GroupScope)
	// 方向不一致的那条一个字节都没动 —— 追加到 exclude 名单上是"多豁免一个分组",
	// 与操作者以为的"多防一个"结果相反。
	after := loadRule(t, gdb, exc.Id)
	assert.Equal(t, "batch", after.GroupScope)
	assert.Equal(t, GroupScopeExclude, after.GroupScopeMode)
	// 已经在名单上的那条不该刷新 updated_at:一次什么都没变的写入会让
	// 列表上的更新时间集体跳到今天,而它们其实什么都没改。
	assert.Equal(t, int64(1000), loadRule(t, gdb, dup.Id).UpdatedAt)
}

// TestBatchGroupScopeReplaceOverwritesEverySelectedRule 固化"覆盖"确实是覆盖。
//
// 这是与"追加"最容易被混淆的一条:如果 replace 被实现成了 append,
// 一批本来只对 vip 生效的规则会突然对 vip 与 batch 都生效,而管理员以为自己
// 把作用域收窄到了 batch。
func TestBatchGroupScopeReplaceOverwritesEverySelectedRule(t *testing.T) {
	gdb := newBuiltinRuleDB(t)
	inc := scopedSeedRule(t, gdb, "vip,svip", GroupScopeInclude)
	exc := scopedSeedRule(t, gdb, "batch", GroupScopeExclude)

	res := runRuleBatch(gdb, []int64{inc.Id, exc.Id},
		func(row *Rule) (string, string, string) {
			return applyBatchGroupScope(gdb, row, batchScopeReplace,
				[]string{"batch"}, GroupScopeExclude, 7, 3000)
		})
	require.Equal(t, 1, res.Succeeded, "覆盖必须改掉 include 那条")
	require.Equal(t, 1, res.Skipped, "覆盖成与现状等价的值不该产生一次写入")

	after := loadRule(t, gdb, inc.Id)
	assert.Equal(t, "batch", after.GroupScope, "覆盖被实现成了追加")
	assert.Equal(t, GroupScopeExclude, after.GroupScopeMode,
		"覆盖没有把方向一起换 —— 名单换了方向没换,这条规则的含义与界面显示相反")
}
