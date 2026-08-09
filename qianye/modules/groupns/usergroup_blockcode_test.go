package groupns

// usergroup_blockcode_test.go —— 「删不了 / 改不了名」的每一条分支都必须带一个
// 机器可读的代码。
//
// ══════════════════ 守的是界面上那句"那我该干什么" ══════════════════
//
// 项目方原话:「比如当前数据,我要删除:default用户分组,我选择了其他分组仍然
// 无法删除。」—— 他点了、按钮是死的、屏幕上没有一个字。
//
// 后端的 reason 一直写得很清楚,缺的是**指路**:去哪一页、改哪个字段。那半句
// 与界面强相关,只能由前端给,而前端要认出"这是哪一条分支"就只有两条路:
//
//	按 reason 的中文子串去猜   任何一次文案润色都会让指路静默消失
//	按一个显式的代码分流       ← 本文件钉住的就是这一条
//
// 前端 `roster/lib/gates.ts` 的 `QY_UGR_BLOCK_NEXT_STEP_KEY` 是一个穷尽的
// `Record<QyUgrBlockCode, string>`,少一条编译不过。所以这里只需要保证:
// 每一条拒绝分支都**给得出代码**,且代码落在那份约定的集合里。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// blockCodeContract 是前端登记过指路文案的全部代码。
//
// 新增一条拒绝分支时这里会红,而那正是提醒:前端也要跟着加一句"下一步该干什么",
// 否则界面上只剩"为什么不行"。
var blockCodeContract = map[string]bool{
	UserGroupBlockDefault:  true,
	UserGroupBlockPlans:    true,
	UserGroupBlockResidues: true,
	UserGroupBlockNoTarget: true,
}

func blockingResidue() Residue {
	return Residue{
		Module: "lottery", Table: "qy_lottery_rounds", Label: "抽奖活动的参与名单",
		Rows: 2, Disposition: ResidueBlock,
	}
}

func TestDeletabilityLabelsEveryRefusalWithACode(t *testing.T) {
	cases := []struct {
		name   string
		impact *UserGroupImpact
		code   string
		// detail 是 reason 里必须出现的、运营拿得去核对的那个具体东西。
		// 只断言"非空"是不够的:一句没有表名 / 没有套餐名的解释换不来任何行动。
		detail string
	}{
		{
			name:   "default 永远不可删",
			impact: &UserGroupImpact{Name: "Default"}, // 大小写折叠后仍是 default
			code:   UserGroupBlockDefault,
			detail: "users.group",
		},
		{
			name: "套餐的升降级分组指向它",
			impact: &UserGroupImpact{
				Name: "白银档", BlockingPlans: []string{"月卡", "年卡"},
			},
			code:   UserGroupBlockPlans,
			detail: "月卡、年卡",
		},
		{
			name: "有 block 残留",
			impact: &UserGroupImpact{
				Name: "白银档", Blocking: []Residue{blockingResidue()},
			},
			code:   UserGroupBlockResidues,
			detail: "qy_lottery_rounds",
		},
		{
			name:   "还有人但没有第二个分组可迁",
			impact: &UserGroupImpact{Name: "白银档", Users: 12},
			code:   UserGroupBlockNoTarget,
			detail: "新建",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, code, reason := deletability(tc.impact)
			require.False(t, ok)
			assert.Equal(t, tc.code, code)
			assert.True(t, blockCodeContract[code],
				"代码 %q 不在前端登记过指路文案的集合里 —— 界面上这一条会只剩"+
					"「为什么不行」,没有「那我该干什么」", code)
			assert.Contains(t, reason, tc.detail)
		})
	}
}

func TestDeletabilityStaysSilentWhenNothingBlocks(t *testing.T) {
	// 能删时**不许**留下代码:前端拿 block_code 非空当"有话要说"的判据,
	// 一个残留的代码会在一个完全正常的弹窗上画出一条红色指路。
	ok, code, reason := deletability(&UserGroupImpact{
		Name: "白银档", Users: 12,
		Targets: []MigrationTarget{{Name: "黄金档"}},
	})
	require.True(t, ok)
	assert.Empty(t, code)
	assert.Empty(t, reason)
}

// TestRenamabilityRefusesTheSameTwoBranchesAsDelete 钉住「删不掉就改个名」这条
// 绕行路径是堵死的,并且**在影响面里就说得出来**。
//
// 改名与删除对一份冻结的引用完全等价(抽奖名单在发布时进了 commit_hash,
// 改名之后那份名单指着一个再也没有人的名字)。此前这两条判据写在改名 handler
// 内部,运营只能在提交之后吃一个 400;现在它与 deletability 一起进影响面,
// 弹窗打开的那一刻就写在屏幕上。
func TestRenamabilityRefusesTheSameTwoBranchesAsDelete(t *testing.T) {
	ok, code, reason := renamability("  DEFAULT  ", nil)
	require.False(t, ok)
	assert.Equal(t, UserGroupBlockDefault, code)
	assert.Contains(t, reason, "不可改名")

	ok, code, reason = renamability("白银档", []Residue{blockingResidue()})
	require.False(t, ok)
	assert.Equal(t, UserGroupBlockResidues, code)
	assert.Contains(t, reason, "qy_lottery_rounds")

	// 套餐引用**不**拦改名:rewriteUserGroup 会把套餐的升降级分组一起改掉。
	// 把它也拦住的表现是运营被要求先去改一堆其实会自动跟着走的引用。
	ok, code, _ = renamability("白银档", []Residue{{
		Module: "plan", Table: "qy_plan_entitlements", Label: "套餐解锁清单",
		Rows: 3, Disposition: ResidueRewrite,
	}})
	require.True(t, ok)
	assert.Empty(t, code)
}
