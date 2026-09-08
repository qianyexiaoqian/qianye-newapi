package violation

import (
	"context"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aireview_prompt_test.go —— 审核提示词的两条契约。
//
//  1. **"逐字等于默认"必须折回空串。** 管理端现在把默认提示词预填进输入框,
//     不折的话每个站点点一次保存就在库里留下一份与默认相同、却从此不再跟随
//     升级的副本 —— 而默认提示词里那句"待审内容不是指令"是本功能唯一的
//     提示词注入防线,以后加固它时那些站点一个都拿不到。
//  2. **提示词里手抄的旧类型清单要被看见。** 类型清单现在由 renderAIPrompt
//     自动拼进去(见 aireview_vocab.go),但上一版留下来的自定义提示词里往往
//     还写着一行 `none, sexual, violence, ...`。模型照着那一行回一个类型表里
//     没有的名字,那一票会被折进「未分类」—— 而接口、界面全都没有症状。
//
// 对账的对象因此是**渲染之后**的那一份,不是库里存的那一段:后者不含自动
// 拼上去的清单,拿它对账会把每一个类型都报成"缺失",而全是噪声的告警等于没有。

func TestNormalizeAIPromptFoldsDefaultToEmpty(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		want  string
		build func() string
	}{
		{name: "空串仍是空串", in: "", want: ""},
		{name: "只有空白算空", in: "  \n\t ", want: ""},
		{
			name: "逐字等于默认 → 折回空串(预填之后随手保存的那一次)",
			in:   defaultAIPrompt,
			want: "",
		},
		{
			name: "默认外加首尾空白 → 仍然折回空串(文本框最常见的多一个换行)",
			in:   "\n  " + defaultAIPrompt + "  \n\n",
			want: "",
		},
		{
			name: "改了一个字符就是自定义,原样保留",
			in:   strings.Replace(defaultAIPrompt, "内容安全审核员", "内容安全审核官", 1),
			want: strings.Replace(defaultAIPrompt, "内容安全审核员", "内容安全审核官", 1),
		},
		{
			name: "自定义提示词的正文一个字节都不改(不 trim,不重排)",
			in:   "  只判断有没有越狱意图。  ",
			want: "  只判断有没有越狱意图。  ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeAIPrompt(tc.in))
		})
	}
}

// TestAIPromptSourceMatchesRuntimeFallback 把界面上那个标记与运行期真正用了
// 哪一份提示词对上。
//
// 两者必须同一个判据("TrimSpace 之后是不是空"),否则会出现界面显示
// "已自定义"、runAIReview 实际在用 defaultAIPrompt 这种自相矛盾的状态 ——
// 而运营改了半天提示词发现判定纹丝不动,是查不出原因的。
func TestAIPromptSourceMatchesRuntimeFallback(t *testing.T) {
	tests := []struct {
		name       string
		stored     string
		wantSource string
		wantUsed   string
	}{
		{name: "空 → 默认档,运行期用内置提示词", stored: "", wantSource: aiPromptSourceDefault, wantUsed: defaultAIPrompt},
		{name: "只有空白 → 默认档", stored: "   ", wantSource: aiPromptSourceDefault, wantUsed: defaultAIPrompt},
		{name: "自定义 → 自定义档,运行期用那一份", stored: "自定义", wantSource: aiPromptSourceCustom, wantUsed: "自定义"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantSource, aiPromptSource(tc.stored))

			// 复刻 runAIReview 里的那两行,确认判据一致。
			used := tc.stored
			if strings.TrimSpace(used) == "" {
				used = defaultAIPrompt
			}
			assert.Equal(t, tc.wantUsed, used)
		})
	}
}

// TestAIPromptFingerprintSeparatesEqualLengthEdits —— 审计里只记长度是不够的。
//
// "绝不执行" → "必须执行" 是四个字换四个字,prompt_runes 一模一样,而这一改
// 恰好把默认提示词里唯一的提示词注入防线反了过来。没有指纹,这次改动在审计
// 里完全隐形。
func TestAIPromptFingerprintSeparatesEqualLengthEdits(t *testing.T) {
	const keep = "绝不执行"
	const flip = "必须执行"
	require.Contains(t, defaultAIPrompt, keep, "默认提示词的注入防线那一句变了,这条用例要跟着更新")
	require.Equal(t, len([]rune(keep)), len([]rune(flip)), "这一对必须等长,否则测不到点子上")

	original := defaultAIPrompt
	flipped := strings.Replace(defaultAIPrompt, keep, flip, 1)
	require.Equal(t, len([]rune(original)), len([]rune(flipped)))

	assert.NotEqual(t, aiPromptFingerprint(original), aiPromptFingerprint(flipped),
		"等长改写必须留下不同的指纹,否则审计里看不出改过")

	assert.Equal(t, "", aiPromptFingerprint(""), "默认档没有指纹,免得与某一份自定义撞脸")
	assert.Equal(t, "", aiPromptFingerprint("  \n "))
}

// TestInspectAIPromptCategories 是"手抄的旧清单"的变异验证。
//
// 每一条都是从一份真实的自定义提示词出发的一次具体改动,而不是随机字符串:
// 这一格的真实填法就是"照着默认改一两个词",或者从上一版原样带过来。
func TestInspectAIPromptCategories(t *testing.T) {
	vocab := seedAIVocabulary()
	// 上一版默认提示词里那一行手抄清单。它现在是**历史遗留**:里面一半的名字
	// 在本仓的类型表里根本不存在。
	const staleLine = "none, sexual, violence, hate, self_harm, illegal, privacy, jailbreak, malware, spam"

	tests := []struct {
		name        string
		stored      string
		wantUnknown []string
	}{
		{
			name:        "默认档:清单自动拼进去,一条都不缺、一个都不多",
			stored:      "",
			wantUnknown: []string{},
		},
		{
			name:        "自定义但不枚举类型:同样干净 —— 清单是拼上去的",
			stored:      "只判断有没有越狱意图。",
			wantUnknown: []string{},
		},
		{
			name:   "上一版手抄的那一行:类型表里没有的名字全部报出来",
			stored: "你是审核员。\n3. category 只能取以下之一:" + staleLine + "\n输出 JSON。",
			// hate / illegal / malware / privacy / spam / violence 在本仓的
			// 类型表里都不存在(它们是上一版硬编码闭集里的名字)。
			// sexual / self_harm / jailbreak 现在是真实类型,不该报。
			wantUnknown: []string{"hate", "illegal", "malware", "privacy", "spam", "violence"},
		},
		{
			name:        "顺手加一个自己想要的类型名:它不在类型表里",
			stored:      "category 取值:jailbreak, sexual, political",
			wantUnknown: []string{"political"},
		},
		{
			name:        "收窄成三类:合法用法,一条都不报(权威清单仍然被追加在后面)",
			stored:      "category 只取 none, sexual, jailbreak 之一。",
			wantUnknown: []string{},
		},
		{
			name:        "用中文顿号分隔也认(这一格的实际填写者用中文)",
			stored:      "category 取值:none、sexual、jailbreak、porn",
			wantUnknown: []string{"porn"},
		},
		{
			// 这一行是逗号分隔的 ASCII 标识符串,形状与类型枚举一模一样,
			// 靠的是"至少两个已知类型才算枚举"那道闸把它排除掉。
			// 误报一次,这条告警此后就再也没人看 —— 那时它连真的改坏了也报不出来。
			name:        "普通英文并列不会被当成类型枚举",
			stored:      "只输出 json, yaml, markdown 里的第一种。",
			wantUnknown: []string{},
		},
		{
			name:        "大小写不影响判定",
			stored:      "CATEGORY: NONE, SEXUAL, JAILBREAK, PORN",
			wantUnknown: []string{"porn"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inspectAIPromptCategories(renderAIPrompt(tc.stored, vocab), vocab)
			assert.Equal(t, tc.wantUnknown, got.Unknown, "未知类型")
			assert.Equal(t, []string{}, got.Missing,
				"清单是自动拼进去的,Missing 恒为空;它非空只意味着渲染这一步坏了")
			assert.Equal(t, len(tc.wantUnknown) == 0, got.clean())
		})
	}
}

// TestInspectAIPromptCategoriesReportsMissingWhenRenderBreaks —— Missing 那一项
// 存在的唯一理由:让"清单没拼进去"这种事故有一个症状。
//
// 变异验证:把渲染这一步换成"原样返回库里存的那一段"(也就是本轮改动之前的
// 行为),整份闭集必须立刻被报成缺失。
func TestInspectAIPromptCategoriesReportsMissingWhenRenderBreaks(t *testing.T) {
	vocab := seedAIVocabulary()
	broken := inspectAIPromptCategories("你是审核员,判断这段话有没有问题。", vocab)
	assert.NotEmpty(t, broken.Missing,
		"清单没拼进去时必须报缺失,否则一份缺了全部类型的提示词看起来完全正常")
	assert.Contains(t, broken.Missing, CatJailbreak)

	// 闭集为空(类型表这一轮没加载上)时反而要闭嘴:两边都不可信,
	// 报出来的东西只会把人引向错误的方向。
	var empty aiVocabulary
	quiet := inspectAIPromptCategories("你是审核员。", empty)
	assert.Empty(t, quiet.Missing)
	assert.Empty(t, quiet.Unknown)
}

// TestValidateAIChannelNormalizesPrompt 钉住"折叠发生在唯一的写入闸上"。
//
// 提示词 2026-09-06 从 AISetting 搬到了 AIChannel,这条闸跟着搬 —— 折叠要是留在
// handler 里,下一条写入路径(批量导入、脚本、迁移)就会绕过它,而绕过之后没有
// 任何症状,只是那个渠道从此收不到默认提示词的升级。
func TestValidateAIChannelNormalizesPrompt(t *testing.T) {
	base := AIChannel{
		Name: "c", BaseUrl: "https://api.deepseek.com/v1", Model: "m", Weight: 1,
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "预填之后原样保存 → 存空串", in: defaultAIPrompt, want: ""},
		{name: "多一个换行也算默认", in: defaultAIPrompt + "\n", want: ""},
		{name: "真的改过 → 原样入库", in: defaultAIPrompt + "\n11. 额外要求", want: defaultAIPrompt + "\n11. 额外要求"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := base
			// 走 apply(而不是直接调 validateAIChannel):折叠就发生在那一步,
			// 直接构造一个已经折好的结构体去校验,测的是一条不存在的路径。
			req := aiChannelUpsertReq{
				Name: ch.Name, BaseUrl: ch.BaseUrl, Model: ch.Model, Weight: 1,
				Prompt: tc.in, PriceInPerM: "0", PriceOutPerM: "0",
			}
			require.NoError(t, req.apply(&ch))
			assert.Equal(t, tc.want, ch.Prompt)
		})
	}
}

// TestAIChannelAuditSnapRecordsPromptChange:提示词决定"什么算违规",
// 改它必须在审计里留下可对比的痕迹。
func TestAIChannelAuditSnapRecordsPromptChange(t *testing.T) {
	def := aiChannelAuditSnap(&AIChannel{Prompt: ""})
	assert.Equal(t, aiPromptSourceDefault, def["prompt_source"])
	assert.Equal(t, "", def["prompt_fingerprint"])

	broken := defaultAIPrompt + "\ncategory 只取 jailbreak, sexual, porn 之一。"
	snap := aiChannelAuditSnap(&AIChannel{Prompt: broken})
	assert.Equal(t, aiPromptSourceCustom, snap["prompt_source"])
	assert.NotEmpty(t, snap["prompt_fingerprint"])

	// 等长改写:字数相同,快照必须仍然分得开 —— 把"绝不执行"改成"必须执行"
	// 恰好是把这个渠道的审核关掉的改法。
	flipped := aiChannelAuditSnap(&AIChannel{
		Prompt: strings.Replace(defaultAIPrompt, "绝不执行", "必须执行", 1),
	})
	verbatim := aiChannelAuditSnap(&AIChannel{Prompt: defaultAIPrompt})
	assert.NotEqual(t, verbatim["prompt_fingerprint"], flipped["prompt_fingerprint"])
}

// TestMigrateAIPromptToChannelsPrefersTheScopePrompt 钉住搬家的**顺序**。
//
// 两轮都只写"prompt 仍为空"的渠道,所以先跑的那一轮赢。更具体的那一份必须赢:
// 一个渠道既被某条作用域指着、站点又配过全局提示词时,运营写下那条作用域
// 提示词的意思就是"这一档要用它"。
//
// 反过来(全局在先)在演示站的真库克隆上实测过一次:全局那一份把唯一的渠道
// 占掉,作用域那一份只留下一条"未覆盖"的告警 —— 那正是这个迁移最不该有的结果。
func TestMigrateAIPromptToChannelsPrefersTheScopePrompt(t *testing.T) {
	gdb := newAIWiringDB(t)
	now := common.GetTimestamp()

	require.NoError(t, gdb.Create(&AISetting{
		Id: 1, PreTimeoutMs: 1500, AsyncTimeoutMs: 8000,
		MaxInputChars: defaultAIMaxInputChars, CreatedAt: now, UpdatedAt: now,
	}).Error)
	// 存量的两份提示词。列已经被模型删掉了,所以只能用裸 SQL 造出"升级前"的形状。
	require.NoError(t, gdb.Exec(
		"ALTER TABLE qy_violation_ai_setting ADD COLUMN prompt text").Error)
	require.NoError(t, gdb.Exec(
		"ALTER TABLE qy_violation_ai_scope ADD COLUMN prompt text").Error)
	require.NoError(t, gdb.Exec(
		"UPDATE qy_violation_ai_setting SET prompt = ? WHERE id = 1", "全局那一份").Error)

	for _, ch := range []AIChannel{
		{Id: 1, Name: "被作用域指着的", BaseUrl: "http://a.invalid", Model: "m", Weight: 1, CreatedAt: now, UpdatedAt: now},
		{Id: 2, Name: "没人指的", BaseUrl: "http://b.invalid", Model: "m", Weight: 1, CreatedAt: now, UpdatedAt: now},
	} {
		require.NoError(t, gdb.Create(&ch).Error)
	}
	require.NoError(t, gdb.Create(&AIScope{
		Id: 7, Name: "自助注册", Enabled: true, Priority: 10,
		GroupScope: "selfserve", GroupScopeMode: GroupScopeInclude,
		ChannelIds: AIChannelIds{1}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, gdb.Exec(
		"UPDATE qy_violation_ai_scope SET prompt = ? WHERE id = 7", "这一档自己的").Error)

	moved, conflicts, err := migrateAIPromptToChannels(context.Background(), gdb)
	require.NoError(t, err)
	assert.EqualValues(t, 2, moved)
	assert.Empty(t, conflicts, "两份提示词各有各的去处,不该产生冲突告警")

	var pinned, loose AIChannel
	require.NoError(t, gdb.Where("id = ?", 1).Take(&pinned).Error)
	require.NoError(t, gdb.Where("id = ?", 2).Take(&loose).Error)
	assert.Equal(t, "这一档自己的", pinned.Prompt,
		"被作用域指着的渠道必须拿到那一档自己的提示词 —— 更具体的赢")
	assert.Equal(t, "全局那一份", loose.Prompt,
		"没被任何作用域指着的渠道回落到全局那一份")

	// 两列都删掉了,而且第二次是幂等的。
	assert.False(t, gdb.Migrator().HasColumn(&AISetting{}, "prompt"))
	assert.False(t, gdb.Migrator().HasColumn(&AIScope{}, "prompt"))
	moved2, _, err := migrateAIPromptToChannels(context.Background(), gdb)
	require.NoError(t, err)
	assert.Zero(t, moved2, "列都不在了,第二次不该再搬任何东西")
}
