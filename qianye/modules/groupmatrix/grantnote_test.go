package groupmatrix

// grantnote_test.go —— 说明文案阶梯的四级,以及它在**用户侧**真的生效。
//
// 这一组用例守的不是"某个字段存下来了",而是项目方那句话的落点:
// 「用户分组分配模型分组的时候若不填写则显示分组备注(默认),
//   若填写以用户分组的模型分组备注优先显示」——
// 而"显示"发生在用户建 key 选分组那一屏,它读的是
// service.GetUserUsableGroups(用户分组) 这张 map 的 **value**。
// 只断言 qy_group_grants.note 落了库,等于把整条链路里最容易断的那一段放过去。

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// useModelGroupNote 替换 setting.QyGroupNote(模型分组备注,阶梯第 2 级)。
//
// 直接替换这个 seam 而不是去写 qy_model_groups + 起 groupns 的快照:
// 阶梯的判据是"这一级有没有非空文案",快照怎么来的与它无关,
// 而多拖一个模块的生命周期进来只会让失败原因变得难以归因。
func useModelGroupNote(t *testing.T, notes map[string]string) {
	t.Helper()
	prev := setting.QyGroupNote
	setting.QyGroupNote = func(name string) string { return notes[name] }
	t.Cleanup(func() { setting.QyGroupNote = prev })
}

func seedGrantNote(t *testing.T, gdb *gorm.DB, userGroup, modelGroup, note string) {
	t.Helper()
	require.NoError(t, gdb.Model(&Grant{}).
		Where("user_group = ? AND model_group = ?", userGroup, modelGroup).
		Update("note", note).Error)
	require.NoError(t, reload())
}

// TestUsableGroupDescriptionLadder 是本轮最重要的一条:
// 三处说明文案来源收敛成一条阶梯,而且**在用户侧真的生效**。
func TestUsableGroupDescriptionLadder(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	// 白名单(阶梯第 3 级)只给 whitelisted 一段原文;另外三个键不在白名单里。
	useUpstreamGroups(t,
		map[string]string{"whitelisted": "白名单原文"},
		map[string]float64{"vip": 1, "whitelisted": 1, "hasmgnote": 1, "hasgrantnote": 1, "bare": 1})
	// 模型分组备注(阶梯第 2 级)。
	useModelGroupNote(t, map[string]string{"hasmgnote": "模型分组备注", "hasgrantnote": "模型分组备注"})

	seedScope(t, gdb, "vip", ModeEnforce, false,
		"whitelisted", "hasmgnote", "hasgrantnote", "bare")
	// 按格备注(阶梯第 1 级)只写在 hasgrantnote 这一格上。
	seedGrantNote(t, gdb, "vip", "hasgrantnote", "这一格专属备注")

	// 从 service.GetUserUsableGroups 进,而不是直接调 Resolve:阶梯的后三级住在
	// 上游 setting 包里,只有走完整条链路才能证明"用户建 key 时看到的是这一段"。
	got := withRealHook(t, "vip")

	assert.Equal(t, "这一格专属备注", got["hasgrantnote"],
		"按格备注非空时必须压过模型分组备注 —— 这是项目方点名的那条优先级")
	assert.Equal(t, "模型分组备注", got["hasmgnote"],
		"按格备注为空时回落到模型分组备注(阶梯第 2 级)")
	assert.Equal(t, "白名单原文", got["whitelisted"],
		"前两级都空时回落到 options.UserUsableGroups 的 value(阶梯第 3 级)")
	assert.Equal(t, "bare", got["bare"],
		"三级都空时回落到分组名本身,绝不能是空串 —— 下拉里那一行总要有字")
}

// TestGrantNoteDoesNotLeakIntoShadow 守 shadow 与未设范围两档的**指针恒等**契约。
//
// 按格备注是一段装饰文案,而"未生效 = 逐位等于上游"是这套设计唯一可证的回退依据。
// 为了把文案提前显示出来而在 shadow 里返回一张新 map,等于拿回退能力换装饰品,
// 并且会让 shadow(「配好了但一个字节都不生效」)变成"挡不住人却改了文案"的半生效态。
func TestGrantNoteDoesNotLeakIntoShadow(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t,
		map[string]string{"paid": "白名单原文"},
		map[string]float64{"vip": 1, "paid": 1})

	seedScope(t, gdb, "vip", ModeShadow, false, "paid")
	seedGrantNote(t, gdb, "vip", "paid", "影子期不该出现的备注")

	upstream := map[string]string{"paid": "白名单原文"}
	got := Resolve("vip", upstream)

	require.Equal(t, upstream, got)
	assert.Equal(t, "白名单原文", got["paid"],
		"shadow 下必须逐位返回上游 —— 一段备注不值得推翻「未生效 = 上游」这条回退依据")
}

// TestSetNoteNeverCreatesGrant 守 set_note 的边界:它只改文案,不改成员资格。
//
// 没有 grant 行的格子不可选,给它写备注会造出一条 Resolve 永远遍历不到的死配置,
// 而管理端回读时那一格会显示出备注 —— 运营会以为已经配好了。
func TestSetNoteNeverCreatesGrant(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"vip": 1, "paid": 1, "free": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false, "paid")

	note := "写给一个没有授权的格子"
	require.NoError(t, applyGrantCells(gdb, []Cell{
		{UserGroup: "vip", ModelGroup: "free", Action: ActionSetNote, Note: &note},
	}, 1))

	var rows []Grant
	require.NoError(t, gdb.Where("user_group = ?", "vip").Find(&rows).Error)
	require.Len(t, rows, 1, "set_note 不得凭空建出 grant 行 —— 那是一次静默的授权")
	assert.Equal(t, "paid", rows[0].ModelGroup)
	assert.Empty(t, rows[0].Note)
}

// TestGrantThenNoteInOneSave 守同一次保存里「勾上 + 写备注」的顺序。
//
// 备注若先于 grant 执行,会撞上"这一格还没有行"而被静默丢掉 ——
// 运营看到分组勾上了、备注没了,而接口返回 200。
func TestGrantThenNoteInOneSave(t *testing.T) {
	gdb := newTestDB(t)
	useConfig(t, true)
	syncHotAsync(t)
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"vip": 1, "paid": 1})
	seedScope(t, gdb, "vip", ModeEnforce, false)

	note := "一次点击的两个动作"
	cells, err := normalizeCells([]Cell{
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetNote, Note: &note},
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionGrant},
	})
	require.NoError(t, err)
	require.NoError(t, applyGrantCells(gdb, cells, 1))

	var row Grant
	require.NoError(t, gdb.Where("user_group = ? AND model_group = ?", "vip", "paid").
		Take(&row).Error)
	assert.Equal(t, note, row.Note,
		"同一次保存里备注必须落在 grant 之后,否则它会被静默丢掉")

	// clear_note 走同一条落库分支,把它清回空串。
	cells, err = normalizeCells([]Cell{
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionClearNote},
	})
	require.NoError(t, err)
	require.NoError(t, applyGrantCells(gdb, cells, 1))
	require.NoError(t, gdb.Where("user_group = ? AND model_group = ?", "vip", "paid").
		Take(&row).Error)
	assert.Empty(t, row.Note, "clear_note 必须真的把备注清掉,而不是留着旧文案")
}

// TestNormalizeCellsNoteContract 守 set_note 的入参契约。
func TestNormalizeCellsNoteContract(t *testing.T) {
	empty := ""
	long := strings.Repeat("千", maxNoteLen+1)
	ratio := 0.5

	t.Run("不带 note 必须报错", func(t *testing.T) {
		_, err := normalizeCells([]Cell{
			{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetNote},
		})
		require.Error(t, err)
	})

	t.Run("clear_note 不带 note 也成立", func(t *testing.T) {
		out, err := normalizeCells([]Cell{
			{UserGroup: "vip", ModelGroup: "paid", Action: ActionClearNote},
		})
		require.NoError(t, err)
		require.Len(t, out, 1)
		require.NotNil(t, out[0].Note, "清除动作必须归一成指向空串的指针 —— "+
			"落库那一步只认 Note != nil,归一在这里做,后面就不必再判一次动作名")
		assert.Equal(t, "", *out[0].Note)
	})

	t.Run("set_note 带空串同样是清除", func(t *testing.T) {
		out, err := normalizeCells([]Cell{
			{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetNote, Note: &empty},
		})
		require.NoError(t, err)
		require.NotNil(t, out[0].Note)
		assert.Equal(t, "", *out[0].Note)
	})

	t.Run("超长拒绝而不是截断", func(t *testing.T) {
		_, err := normalizeCells([]Cell{
			{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetNote, Note: &long},
		})
		require.Error(t, err)
	})

	t.Run("其它动作不得挟带 note", func(t *testing.T) {
		out, err := normalizeCells([]Cell{
			{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetRatio, Ratio: &ratio, Note: &long},
		})
		require.NoError(t, err)
		assert.Nil(t, out[0].Note,
			"set_ratio 挟带的 note 必须被剥掉:一个动作只做一件事,"+
				"否则超长文案会从一条没有长度校验的路径溜进 varchar(255)")
	})
}

// TestResolveCellNoteReportsSource 守管理端那一份解析与用户侧同结论,
// 并且能说出"这一段来自哪一级"——界面据此把继承值以灰字显示而不预填进输入框。
func TestResolveCellNoteReportsSource(t *testing.T) {
	whitelist := map[string]string{"paid": "白名单原文"}

	note, src := resolveCellNote("按格", "模型分组", whitelist, "paid", true)
	assert.Equal(t, "按格", note)
	assert.Equal(t, NoteSourceGrant, src)

	note, src = resolveCellNote("", "模型分组", whitelist, "paid", true)
	assert.Equal(t, "模型分组", note)
	assert.Equal(t, NoteSourceModelGroup, src)

	note, src = resolveCellNote("", "", whitelist, "paid", true)
	assert.Equal(t, "白名单原文", note)
	assert.Equal(t, NoteSourceWhitelist, src)

	note, src = resolveCellNote("", "", whitelist, "bare", true)
	assert.Equal(t, "bare", note)
	assert.Equal(t, NoteSourceGroupName, src)
}

// TestResolveCellNoteSkipsGrantNoteWhenNotEnforced 钉住管理端与 Resolve 的
// **生效条件**同源。
//
// Resolve 在 mode != enforce 时逐位返回上游,按格备注一个字节都不生效
// (hook.go 的 `scope.Mode != ModeEnforce` 分支)。管理端如果照旧把第 1 级算进去,
// 那一格的 effective_note / note_source 会显示成"已生效",而运营在影子期
// 「先配好再切 enforce」时核对文案用的正是这个字段 —— 他会验收通过一段
// 用户永远看不到的文字。
func TestResolveCellNoteSkipsGrantNoteWhenNotEnforced(t *testing.T) {
	whitelist := map[string]string{"paid": "白名单原文"}

	note, src := resolveCellNote("按格", "模型分组", whitelist, "paid", false)
	assert.Equal(t, "模型分组", note,
		"影子档必须回落到下一级:用户此刻看到的就是模型分组备注")
	assert.Equal(t, NoteSourceModelGroup, src)

	note, src = resolveCellNote("按格", "", whitelist, "paid", false)
	assert.Equal(t, "白名单原文", note)
	assert.Equal(t, NoteSourceWhitelist, src)
}

// TestValidateCellsRejectsNoteOnCellThatWontExist 守「静默丢弃」被换成一句话。
//
// applyGrantCells 对按格备注**只 UPDATE 不 INSERT**(理由见那里的注释),所以
// 给一个保存之后仍然没有 grant 行的格子写备注,会走完这一整条路:
// validateCells 放行 → UPDATE 命中 0 行 → 事务提交 → 接口 200 →
// 审计写「改备注 1 项」,而库里一个字节都没变。
//
// 这条路径用鼠标就能走到:运营在还没勾中的那一行备注框里先把文案敲好
// (合理动作:先准备文案、下一步再勾),点保存拿到一句绿色提示,回读后备注是空的。
// 他多半会认为是自己没点上,于是重复操作;若此后勾中了那一格并以为备注还在,
// 用户在下拉里看到的是模型分组默认备注而不是他写的那句。
func TestValidateCellsRejectsNoteOnCellThatWontExist(t *testing.T) {
	// grant 那一条另有一道「必须在分组倍率表里」的闸门,与本用例无关但会先命中。
	useUpstreamGroups(t, map[string]string{}, map[string]float64{"paid": 1, "free": 1})
	scopes := map[string]Scope{"vip": {UserGroup: "vip", Mode: ModeEnforce}}
	before := map[string]map[string]struct{}{"vip": {"paid": {}}}
	note := "内测专用,暂不开放"

	err := validateCells([]Cell{
		{UserGroup: "vip", ModelGroup: "free", Action: ActionSetNote, Note: &note},
	}, scopes, before)
	require.Error(t, err, "给一个不在清单里的格子写备注必须报错,而不是 200 + 静默丢弃")
	assert.Contains(t, err.Error(), "free")
	assert.NotContains(t, err.Error(), "PUT /",
		"报错正文是给运营看的,不能让他去发 HTTP 请求")

	// 同一次保存里「勾上 + 写备注」是一次点击的两个动作,必须放行 ——
	// 落库顺序已经保证备注跑在 grant 之后(见 applyGrantCells)。
	require.NoError(t, validateCells([]Cell{
		{UserGroup: "vip", ModelGroup: "free", Action: ActionGrant},
		{UserGroup: "vip", ModelGroup: "free", Action: ActionSetNote, Note: &note},
	}, scopes, before))

	// 反方向:同一次保存里撤销了这一格,备注就没有落点了。
	require.Error(t, validateCells([]Cell{
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionRevoke},
		{UserGroup: "vip", ModelGroup: "paid", Action: ActionSetNote, Note: &note},
	}, scopes, before))
}

// TestValidateCellsUnmanagedErrorPointsAtTheUI 守那句 400 的**正文**。
//
// 站上绝大多数用户分组没有 scope 行,所以这是运营最常撞到的一条拒绝。
// 早先的正文写的是「请先用 PUT /group-matrix/scope/:ug 建立 scope 行」——
// 收到它的人手上只有一个弹窗,而正文让他去发一个 HTTP 请求。
func TestValidateCellsUnmanagedErrorPointsAtTheUI(t *testing.T) {
	note := "随手写的一句"
	err := validateCells([]Cell{
		{UserGroup: "没设范围的档", ModelGroup: "paid", Action: ActionSetNote, Note: &note},
	}, map[string]Scope{}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "PUT /")
	assert.NotContains(t, err.Error(), "scope 行")
	assert.Contains(t, err.Error(), "可用范围的力度",
		"正文必须点名界面上真的存在的那个控件")
}
