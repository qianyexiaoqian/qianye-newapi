package service

// qy_group_usable_parity_test.go —— 「未设范围 = 上游原行为」的**逐位对比证明**。
//
// 本轮从 GetUserUsableGroups 里拆掉了两样东西:
//
//	1. GroupSpecialUsableGroup 的 +: / -: / 裸键差分(整套下线)
//	2. 「无条件把 userGroup 补回自己」里的「无条件」三个字
//
// 这两样都直接决定"谁能选到哪个模型分组",而写错**不报错**:表现是一批令牌
// 在下一次请求同时 403,或者反过来,一个私有号池对全站可见。所以这里不写
// 「我认为等价」,而是把**上游那一版原样抄进测试**,在一张覆盖两个判据全部
// 取值组合的表上逐位比对两份返回值。
//
// 前提:范围未设定(qy_group_scopes 里没有这一行)。此时 QyResolveUsableGroups
// 是恒等函数(hook 未安装 / 未接管),两份实现的差别就只剩上面那两处。

import (
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/stretchr/testify/assert"
)

// upstreamGetUserUsableGroups 是本轮改动**之前**的实现,逐字节照抄自
// `git show HEAD:service/group.go`,只把已删除的 GroupSpecialUsableGroup
// 换成一个显式传进来的等价 map(那张表的读取语义就是 map[用户分组]map[键]说明)。
//
// 照抄而不是"用当前实现加个开关":加开关的话两份代码共享同一批 bug,
// 而这条用例要证明的恰恰是"没有引入新的差异"。
func upstreamGetUserUsableGroups(userGroup string, special map[string]map[string]string) map[string]string {
	groupsCopy := setting.GetUserUsableGroupsCopy()
	if userGroup != "" {
		specialSettings, b := special[userGroup]
		if b {
			for specialGroup, desc := range specialSettings {
				if strings.HasPrefix(specialGroup, "-:") {
					groupToRemove := strings.TrimPrefix(specialGroup, "-:")
					delete(groupsCopy, groupToRemove)
				} else if strings.HasPrefix(specialGroup, "+:") {
					groupToAdd := strings.TrimPrefix(specialGroup, "+:")
					groupsCopy[groupToAdd] = desc
				} else {
					groupsCopy[specialGroup] = desc
				}
			}
		}
		if _, ok := groupsCopy[userGroup]; !ok {
			// 用常量而不是字面量:这份复刻要隔离的是**收窄**(哪些键在、哪些不在),
			// 不是那段说明文案。写死字面量的话,一次纯文案调整会让这组
			// 「差异恰好是一个键」的断言整片变红,而键集合一个都没变。
			groupsCopy[userGroup] = SelfUsableGroupDescription
		}
	}
	return QyResolveUsableGroups(userGroup, groupsCopy)
}

// TestUnsetScopeOnTheSiteConfigurationDiffersOnlyBySelfInsert 用**本站此刻的
// 真实配置形状**跑一遍全部 8 个用户分组,并把差异逐个钉死。
//
// ── 结论(实测,不是推断) ──
//
//	GroupSpecialUsableGroup 整套下线   逐位零差异。
//	                                  站上唯一一条规则是
//	                                  {"浅夜の自己人":{"-:浅夜の自己人":"remove"}},
//	                                  它想把「浅夜の自己人」从自己的清单里摘掉,
//	                                  而上游紧接着那句无条件自我补入会原样补回来 ——
//	                                  这条规则**从来没有生效过**。
//	自我补入收窄                       差异恰好是**一个键**:那些不在 GroupRatio
//	                                  里的用户分组,不再把自己的名字补进清单。
//	                                  本站 8 个用户分组里有 7 个落在这一格。
//
// 这条用例的价值不在"全绿",而在于它把这 7 个分组各自的**确切差异**写在断言里:
// 将来任何一次改动让差异变大(比如少了一个真正的模型分组),它会当场变红。
func TestUnsetScopeOnTheSiteConfigurationDiffersOnlyBySelfInsert(t *testing.T) {
	// 本站现状:GroupRatio 只有这两个键。其余用户分组都不在倍率表里 ——
	// 包括那三个有 enabled abilities 的私有号池(管理员分组 / 反重力的哈基米 /
	// 初衷专属分组),它们一直靠自我补入隐式获得可选性。
	useGroupState(t,
		`{"浅夜の梦专属号池":"高速通道","免费の渠道":"免费池"}`,
		`{"浅夜の梦专属号池":0.5,"免费の渠道":1}`)

	special := map[string]map[string]string{
		"浅夜の自己人": {"-:浅夜の自己人": "remove"},
	}
	cases := []struct {
		userGroup string
		// inGroupRatio 为 true 时两份实现逐位相同;为 false 时上游多一个
		// 「自己的名字 → 用户分组」。
		inGroupRatio bool
	}{
		{"default", false},
		{"qy-canary-ug", false},
		{"初衷专属分组", false},
		{"反重力的哈基米", false},
		{"浅夜の梦专属号池", true},
		{"浅夜の自己人", false},
		{"清芯", false},
		{"管理员分组", false},
		{"", true}, // 匿名口径:两份都原样返回全局白名单
	}

	for _, tc := range cases {
		name := tc.userGroup
		if name == "" {
			name = "<匿名>"
		}
		t.Run(name, func(t *testing.T) {
			before := upstreamGetUserUsableGroups(tc.userGroup, special)
			after := GetUserUsableGroups(tc.userGroup)

			if tc.inGroupRatio {
				assert.Equal(t, before, after, "零差异")
				return
			}
			// 差异必须**恰好**是自我补入那一个键,一个不多一个不少。
			want := map[string]string{}
			for k, v := range after {
				want[k] = v
			}
			want[tc.userGroup] = SelfUsableGroupDescription
			assert.Equal(t, want, before,
				"上游与本版的差异必须只有「自己的名字」这一个键 —— "+
					"少掉任何一个真正的模型分组都是一次静默的 403")
			assert.NotContains(t, after, tc.userGroup)
		})
	}

	// 差分下线本身零差异:把那条规则换成 nil,上游的返回值一字不变。
	withRule := upstreamGetUserUsableGroups("浅夜の自己人", special)
	withoutRule := upstreamGetUserUsableGroups("浅夜の自己人", nil)
	assert.Equal(t, withoutRule, withRule,
		"站上唯一那条 -: 规则被紧随其后的无条件自我补入抵消,从来没有生效过")
}

// TestUnsetScopeParityAcrossBothPredicates 把两个判据的全部取值组合摆开。
//
// 只用真实配置跑一遍是不够的:真实配置里一条 +: 规则都没有,而 +: 与裸键
// 恰恰是差分下线**唯一**可能造成真实损失的两种写法。
func TestUnsetScopeParityAcrossBothPredicates(t *testing.T) {
	useGroupState(t,
		`{"在白名单里":"白名单说明","公共池":"公共"}`,
		`{"公共池":1,"在白名单里":1,"又在白名单又在倍率表":1,"只在倍率表里":0.5}`)

	special := map[string]map[string]string{
		// 差分的三种写法各一条,全部挂在一个既不在白名单也不在倍率表的档次上。
		"带差分的档": {
			"+:公共池":   "加进来",
			"-:在白名单里": "摘出去",
			"裸键池":     "直接加(那个池子根本不存在)",
		},
	}

	t.Run("既在白名单又在倍率表", func(t *testing.T) {
		assert.Equal(t,
			upstreamGetUserUsableGroups("又在白名单又在倍率表", special),
			GetUserUsableGroups("又在白名单又在倍率表"))
	})
	t.Run("只在倍率表里(legacy_dual 的形状)", func(t *testing.T) {
		assert.Equal(t,
			upstreamGetUserUsableGroups("只在倍率表里", special),
			GetUserUsableGroups("只在倍率表里"),
			"这一格保住了那 5 个 legacy_dual 名字的可选性 —— 收窄写反的话整档 403")
	})
	t.Run("只在白名单里", func(t *testing.T) {
		assert.Equal(t,
			upstreamGetUserUsableGroups("在白名单里", special),
			GetUserUsableGroups("在白名单里"))
	})
	t.Run("匿名", func(t *testing.T) {
		assert.Equal(t,
			upstreamGetUserUsableGroups("", special),
			GetUserUsableGroups(""))
	})

	t.Run("两边都不在:只差自我补入那一个键", func(t *testing.T) {
		assert.Equal(t, map[string]string{
			"在白名单里":  "白名单说明",
			"公共池":    "公共",
			"纯粹的一档人": SelfUsableGroupDescription,
		}, upstreamGetUserUsableGroups("纯粹的一档人", special))
		assert.Equal(t, map[string]string{
			"在白名单里": "白名单说明",
			"公共池":   "公共",
		}, GetUserUsableGroups("纯粹的一档人"))
	})

	t.Run("有一条差分规则命中它:三种写法的确切损失", func(t *testing.T) {
		// 上游:+: 覆盖说明文案、-: 删掉一个白名单项、裸键凭空加一个不存在的池子。
		assert.Equal(t, map[string]string{
			"公共池":   "加进来",
			"裸键池":   "直接加(那个池子根本不存在)",
			"带差分的档": SelfUsableGroupDescription,
		}, upstreamGetUserUsableGroups("带差分的档", special))
		// 本版:全局白名单原样,不加不减。
		assert.Equal(t, map[string]string{
			"在白名单里": "白名单说明",
			"公共池":   "公共",
		}, GetUserUsableGroups("带差分的档"))

		// 差分下线在**可达行为**上的净效果:那三个键没有一个能被真正选中 ——
		// 「裸键池」不在倍率表里,「带差分的档」也不在。唯一真实的损失是
		// 「在白名单里」重新出现(即 -: 的收紧作用消失),而这一条正是
		// 下线时给出的取舍:能表达 per-(用户分组, 模型分组) 收紧的只有
		// groupmatrix 的权威清单,它显式、有预览、可审计。
		assert.False(t, IsUserSelectableGroup("带差分的档", "裸键池"))
		assert.False(t, IsUserSelectableGroup("带差分的档", "带差分的档"))
		assert.True(t, IsUserSelectableGroup("带差分的档", "在白名单里"),
			"这是差分下线唯一的真实行为变化:-: 的收紧作用消失")
	})
}

// TestSelfInsertNarrowingIsTheOnlyDifference 把唯一那一类差异摊开写清楚。
//
// ── 差成什么样 ──
// 一个「既不在全局白名单、也不在 options.GroupRatio」的用户分组,上游会把
// 自己的名字补进可选清单(说明文案固定是「用户分组」),收窄之后不补。
//
// ── 为什么可以接受 ──
// 鉴权与选择性三处本来就要求 ContainsGroupRatio,所以完全不受影响:
// middleware/auth.go、controller.GetUserGroups、IsUserSelectableGroup。
// 唯一的可观测差异是 403 的**文案**(「分组已被弃用」→「无权访问 X 分组」),
// 而这种令牌本来就是孤儿。
//
// 清单还有两条消费方不过 ContainsGroupRatio(controller/user.go 的
// GetUserModels、controller/pricing.go 的 GetPricing),那里差异是真实的:
// 这样一个名字不再自动出现在它自己那一档人的模型选择器与价格页上。
// 这个方向是可接受的 —— 一个没有倍率的模型分组出现在**价格页**上,印出来的
// 是 GetGroupRatio fail-open 的 1.0,那是一个假价格。
func TestSelfInsertNarrowingIsTheOnlyDifference(t *testing.T) {
	useGroupState(t, `{"公共池":"公共"}`, `{"公共池":1,"有倍率的私有池":0.3}`)

	// 有倍率 ⇒ 两份实现都补自己 ⇒ 逐位相同。这一格保住了那 5 个 legacy_dual
	// 名字(如「浅夜の梦专属号池」539 用户 + 76 行 abilities)的可选性。
	assert.Equal(t,
		upstreamGetUserUsableGroups("有倍率的私有池", nil),
		GetUserUsableGroups("有倍率的私有池"))

	// 没倍率 ⇒ 只有上游补自己。差异恰好是**一个键**,而且是那个够不到东西的键。
	before := upstreamGetUserUsableGroups("没倍率的一档人", nil)
	after := GetUserUsableGroups("没倍率的一档人")
	assert.Equal(t, map[string]string{
		"公共池":     "公共",
		"没倍率的一档人": SelfUsableGroupDescription,
	}, before, "上游:无条件补自己")
	assert.Equal(t, map[string]string{"公共池": "公共"}, after, "收窄后:不补")
	assert.Equal(t, len(before)-1, len(after), "差异必须恰好是一个键,不多不少")

	// 而那个键在选择性判据上本来就是 false —— 两份实现在这里给出同一个答案,
	// 这正是"收窄不改变任何可达行为"的可执行形式。
	assert.False(t, IsUserSelectableGroup("没倍率的一档人", "没倍率的一档人"))
	assert.False(t, ratio_setting.ContainsGroupRatio("没倍率的一档人"))
}
