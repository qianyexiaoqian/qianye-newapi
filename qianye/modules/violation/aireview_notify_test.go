package violation

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// 违规通知邮件的四条契约:
//
//	① 模板原样输出、值一律转义 —— "支持 HTML" 与"用户名不能注入脚本"必须同时成立
//	② 两格留空回落内置默认      —— 空模板发出去的是一封空信,那是最糟的失败方式
//	③ 三道闸(没配 / 影子 / 无用户)—— 影子模式绝不能给用户发信
//	④ 计数取"离处置最近的那条线" —— 报账号线会在类型线更近时给出反向的信息

func TestViolationEmailRenderEscapesValuesButNotTemplate(t *testing.T) {
	vars := violationEmailVars{
		SiteName: "浅夜の梦",
		// 用户名是站外可控的:它必须被转义,否则模板里就多了一段别人写的 HTML。
		Username:  `<img src=x onerror="alert(1)">`,
		Model:     "c-gemini-3-flash",
		Category:  "违禁品与非法交易",
		Reason:    "内容触发 <安全策略>",
		HitCount:  "2",
		Threshold: "3",
		Remaining: "1",
		RequestId: "req-1",
	}

	subject, body := renderViolationEmail(
		"【{{site_name}}】{{username}} 被拦下",
		`<p><b>{{username}}</b> 用 {{model}}:{{reason}}({{hit_count}}/{{threshold}})</p>`,
		vars,
	)

	assert.Equal(t, "【浅夜の梦】<img src=x onerror=\"alert(1)\"> 被拦下", subject,
		"标题进的是信头,HTML 实体在那里会被原样显示,所以只去换行不转义")
	assert.Equal(t,
		`<p><b>&lt;img src=x onerror=&#34;alert(1)&#34;&gt;</b> 用 c-gemini-3-flash:内容触发 &lt;安全策略&gt;(2/3)</p>`,
		body)
	assert.Contains(t, body, "<p><b>", "模板自己的标签必须原样留着")
}

func TestViolationEmailSubjectDropsHeaderNewlines(t *testing.T) {
	subject, _ := renderViolationEmail("标题 {{username}} 尾", "", violationEmailVars{
		Username: "a\r\nBcc: victim@example.com",
	})

	assert.NotContains(t, subject, "\n")
	assert.NotContains(t, subject, "\r")
	assert.Equal(t, "标题 a  Bcc: victim@example.com 尾", subject)
}

func TestViolationEmailFallsBackToBuiltinTemplateWhenBlank(t *testing.T) {
	subject, body := renderViolationEmail("   ", "\n\t", violationEmailVars{
		SiteName: "站点", Username: "u1", Category: "破限", HitCount: "1",
		Threshold: "3", Remaining: "2",
	})

	assert.Equal(t, "【站点】你的一次请求被安全策略拦下", subject)
	assert.Contains(t, body, "u1", "内置正文必须把用户名填进去")
	assert.Contains(t, body, "破限")
	assert.NotContains(t, body, "{{", "内置模板里不能留下没被替换的占位符")
}

func TestViolationEmailKeepsUnknownPlaceholderVisible(t *testing.T) {
	// 认不出来的占位符原样留着:运营在预览里一眼就能看出自己写错了键名,
	// 而静默替换成空串会让它看起来"生效了"。
	_, body := renderViolationEmail("s", "{{username}} / {{no_such_key}}", violationEmailVars{Username: "u1"})

	assert.Equal(t, "u1 / {{no_such_key}}", body)
}


func TestViolationEmailBlankValueRendersDash(t *testing.T) {
	// 空值渲染成 "—" 而不是留空:一个空格子和「模板坏了」长得一模一样,而这封信的
	// 收件人正在判断它是不是钓鱼。四个计数占位符本来就是这么做的,这里把同一条
	// 口径铺到全部键上。
	//
	// 2026-09-07 端到端实跑时撞到:AI 规则没填「对用户公示的理由」,真发出去的那封
	// 信里 {{reason}} 那一行就是一个空格子。
	_, body := renderViolationEmail("s", "<td>{{reason}}</td><td>{{group}}</td>", violationEmailVars{
		Username: "u1",
	})

	assert.Equal(t, "<td>—</td><td>—</td>", body)
}

func TestViolationEmailBlankSubjectValueRendersDash(t *testing.T) {
	subject, _ := renderViolationEmail("【{{site_name}}】{{username}}", "b", violationEmailVars{
		Username: "u1",
	})

	assert.Equal(t, "【—】u1", subject, "标题里的空值同样不能直接消失")
}

func TestViolationEmailVarKeysAllResolve(t *testing.T) {
	// 界面上那张占位符说明表读的就是 violationEmailVarKeys。少一个键会让运营
	// 照着界面写出一个永远不会被替换的 {{xxx}},多一个则相反。
	pairs := violationEmailVars{}.pairs()
	require.Len(t, pairs, len(violationEmailVarKeys))
	for _, key := range violationEmailVarKeys {
		_, ok := pairs[key]
		assert.Truef(t, ok, "占位符 %q 在 pairs() 里没有对应取值", key)
	}
}

func TestViolationEmailTaskGates(t *testing.T) {
	notice := &emailNotice{Subject: "s", Body: "b"}
	cases := []struct {
		name string
		rec  *Record
		want bool
	}{
		{"渠道开了开关就发", &Record{UserId: 7, notice: notice}, true},
		{"渠道没开开关不发", &Record{UserId: 7}, false},
		{"影子命中不发", &Record{UserId: 7, Shadow: true, notice: notice}, false},
		{"取不到用户不发", &Record{UserId: 0, notice: notice}, false},
		{"没有记录不发", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task, ok := violationEmailTaskOf(tc.rec, nil)
			require.Equal(t, tc.want, ok)
			if tc.want {
				assert.Equal(t, 7, task.UserId)
				assert.Equal(t, *notice, task.Notice)
			}
		})
	}
}

func TestViolationEmailVarsWithoutCounterRenderUnknown(t *testing.T) {
	// "只拦不计数"的规则同样会拦下用户,他一样要收到通知 —— 只是那四个
	// 计数占位符没有真实答案,必须渲染成 "—" 而不是 0。
	vars := violationEmailVarsOf(&Record{
		UserId: 7, Username: "u1", ModelName: "m", CategoryName: "内部名",
		CategoryPublicTitle: "公示名", Blocked: true, CreatedAt: 1788774301,
	}, nil)

	assert.Equal(t, violationEmailUnknown, vars.HitCount)
	assert.Equal(t, violationEmailUnknown, vars.Threshold)
	assert.Equal(t, violationEmailUnknown, vars.Remaining)
	assert.Equal(t, "否", vars.Banned)
	assert.Equal(t, "是", vars.Blocked)
	assert.Equal(t, "公示名", vars.Category, "用户看到的应当是公示标题")
}

func TestViolationEmailNearestLineIsTheOneThatFiresFirst(t *testing.T) {
	cases := []struct {
		name          string
		st            counterState
		wantHit       int
		wantThreshold int
	}{
		{
			// 账号线 10 还差 8、类型线 3 还差 1:报账号线会告诉用户"还剩 8 次",
			// 而他下一次命中就被封了。
			name: "类型线更近时报类型线",
			st: counterState{
				HitCount: 2, Policy: BanPolicy{Threshold: 10},
				CatHitCount: 2, Category: Category{Enabled: true, Threshold: 3},
			},
			wantHit: 2, wantThreshold: 3,
		},
		{
			name: "账号线更近时报账号线",
			st: counterState{
				HitCount: 2, Policy: BanPolicy{Threshold: 3},
				CatHitCount: 1, Category: Category{Enabled: true, Threshold: 10},
			},
			wantHit: 2, wantThreshold: 3,
		},
		{
			name: "类型未启用时只看账号线",
			st: counterState{
				HitCount: 1, Policy: BanPolicy{Threshold: 5},
				CatHitCount: 9, Category: Category{Enabled: false, Threshold: 1},
			},
			wantHit: 1, wantThreshold: 5,
		},
		{
			name: "两条线都没阈值时阈值报 0",
			st: counterState{
				HitCount: 4, Policy: BanPolicy{Threshold: 0},
				CatHitCount: 4, Category: Category{Enabled: true, Threshold: 0},
			},
			wantHit: 4, wantThreshold: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, threshold := nearestViolationLine(tc.st)
			assert.Equal(t, tc.wantHit, hit)
			assert.Equal(t, tc.wantThreshold, threshold)
		})
	}
}

func TestViolationEmailVarsWithoutThresholdSayUnlimited(t *testing.T) {
	vars := violationEmailVarsOf(&Record{UserId: 7}, &counterState{
		HitCount: 4, Policy: BanPolicy{Threshold: 0},
	})

	assert.Equal(t, "4", vars.HitCount)
	assert.Equal(t, violationEmailUnlimited, vars.Threshold)
	assert.Equal(t, violationEmailUnlimited, vars.Remaining)
}

func TestViolationEmailTemplateLengthGate(t *testing.T) {
	require.NoError(t, validateViolationEmailTemplate("标题", "<p>正文</p>"))

	err := validateViolationEmailTemplate(strings.Repeat("标", maxViolationEmailSubjectRunes+1), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "邮件标题过长")

	err = validateViolationEmailTemplate("", strings.Repeat("正", maxViolationEmailBodyRunes+1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "邮件正文过长")
}

func TestAIChannelEmailNoticeOnlyWhenSwitchOn(t *testing.T) {
	rt := &aiRuntime{Channels: []*aiChannelRT{
		{Id: 1, NotifyEmail: true, EmailSubject: "s1", EmailBody: "b1"},
		{Id: 2, NotifyEmail: false, EmailSubject: "s2", EmailBody: "b2"},
	}}

	require.NotNil(t, aiChannelEmailNotice(rt, 1))
	assert.Equal(t, emailNotice{Subject: "s1", Body: "b1"}, *aiChannelEmailNotice(rt, 1))
	assert.Nil(t, aiChannelEmailNotice(rt, 2), "开关关着的渠道不发")
	assert.Nil(t, aiChannelEmailNotice(rt, 99), "渠道刚被删掉时安静地退化成不发")
	assert.Nil(t, aiChannelEmailNotice(nil, 1))
}

// TestAIChannelWritableColumnsCoverEveryEditableField 把「加了新列却忘了加进
// 渠道编辑的写回清单」钉死在测试期。
//
// 漏掉的表现没有任何报错:接口 200、响应体里是新值(来自内存里的 row),
// 刷新之后变回旧值。2026-09-07 加通知邮件那三列时真的漏了一次。
//
// 判据从模型反射列名,而不是抄一份字面量:抄本会跟着漏。不该写回的列逐个列出
// 并写明理由 —— 这份排除清单本身就是"哪些列不走这条路"的答案。
func TestAIChannelWritableColumnsCoverEveryEditableField(t *testing.T) {
	// 密钥那五列由 applyAIChannelKey 单独落库(要加密、算掩码、记绑定地址);
	// id 是主键;created_at 只在新建时写。
	excluded := map[string]string{
		"id":           "主键",
		"key_nonce":    "密钥由 applyAIChannelKey 单独落库",
		"key_cipher":   "密钥由 applyAIChannelKey 单独落库",
		"key_version":  "密钥由 applyAIChannelKey 单独落库",
		"key_hint":     "密钥由 applyAIChannelKey 单独落库",
		"key_endpoint": "密钥由 applyAIChannelKey 单独落库",
		"created_at":   "只在新建时写",
	}

	parsed, err := schema.Parse(&AIChannel{}, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)

	writable := make(map[string]bool, len(aiChannelWritableColumns))
	for _, col := range aiChannelWritableColumns {
		writable[col] = true
	}

	for _, field := range parsed.Fields {
		col := field.DBName
		if col == "" {
			continue // 未导出/被忽略的字段(如 notice)本来就不落库
		}
		if _, skip := excluded[col]; skip {
			assert.Falsef(t, writable[col], "列 %q 在排除清单里,不该出现在写回清单", col)
			continue
		}
		assert.Truef(t, writable[col],
			"列 %q 可以在渠道表单里编辑,却不在 aiChannelWritableColumns 里 —— "+
				"保存会静默丢掉它(接口 200、刷新后变回旧值)", col)
	}
}
