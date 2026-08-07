package ratio_setting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadRatioTables 把两张倍率表设成用例需要的内容。
// 显式初始化而不是依赖包级默认值:默认值随时会被别的用例改掉,
// 而这批断言的全部意义就是"输入确定时输出逐位确定"。
func loadRatioTables(t *testing.T, groupRatio, groupGroupRatio string) {
	t.Helper()
	require.NoError(t, UpdateGroupRatioByJSONString(groupRatio))
	require.NoError(t, UpdateGroupGroupRatioByJSONString(groupGroupRatio))
}

// TestResolveGroupRatioMatchesUpstreamBranch 是「三份复制品合一」的行为等价证明。
//
// want 一列是**改动前**那三段 if 的输出:
//
//	命中 GroupGroupRatio[U][M] → 用它(哪怕是 0)
//	未命中                     → GroupRatio[M];查不到 fail-open 返回 1
//
// 任何一处把优先级写反、把"key 存在"误写成"值非零"、或把 fail-open 改成别的数,
// 这张表立刻红。这是本轮唯一一条能证明"金额没变"的测试。
func TestResolveGroupRatioMatchesUpstreamBranch(t *testing.T) {
	loadRatioTables(t,
		`{"default":1,"免费の渠道":0,"低价池":0.125}`,
		`{"vip":{"低价池":0.3,"免费の渠道":0.5},"零覆盖组":{"default":0}}`)

	cases := []struct {
		name       string
		userGroup  string
		modelGroup string
		wantRatio  float64
		wantSource string
		wantMiss   bool
		wantSilent bool
	}{
		{"交叉格命中", "vip", "低价池", 0.3, GroupRatioSourceOverride, false, false},
		{"交叉格显式配 0 必须胜过兜底", "零覆盖组", "default", 0, GroupRatioSourceOverride, false, false},
		{"未命中交叉格回落兜底", "default", "低价池", 0.125, GroupRatioSourceInherit, false, false},
		{"兜底本身就是 0", "default", "免费の渠道", 0, GroupRatioSourceInherit, false, false},
		{"空用户分组走兜底", "", "低价池", 0.125, GroupRatioSourceInherit, false, false},
		{"模型分组不在倍率表 → fail-open 1.0", "default", "已被删掉的池", 1, GroupRatioSourceInherit, true, true},
		{"交叉格命中但兜底缺失 → 不算静默兜底", "vip", "只有交叉格的池", 1, GroupRatioSourceInherit, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "交叉格命中但兜底缺失 → 不算静默兜底" {
				// 单独补一格,避免污染其它用例的输入。
				loadRatioTables(t,
					`{"default":1,"免费の渠道":0,"低价池":0.125}`,
					`{"vip":{"只有交叉格的池":0.2}}`)
				res := ResolveGroupRatio(tc.userGroup, tc.modelGroup)
				assert.Equal(t, 0.2, res.Ratio)
				assert.Equal(t, GroupRatioSourceOverride, res.Source)
				assert.True(t, res.BaseMissing, "兜底确实缺失,这是一处需要在管理端清理的登记不一致")
				assert.False(t, res.SilentFallback(),
					"这一笔的价是运营在交叉格里配出来的,记成「静默按 1.0 计费」是假阳性")
				return
			}
			res := ResolveGroupRatio(tc.userGroup, tc.modelGroup)
			assert.Equal(t, tc.wantRatio, res.Ratio, "乘进账单的倍率必须与改动前逐位相同")
			assert.Equal(t, tc.wantSource, res.Source)
			assert.Equal(t, tc.wantMiss, res.BaseMissing)
			assert.Equal(t, tc.wantSilent, res.SilentFallback())
		})
	}
}

// TestGroupRatioMissHookSeparatesBillingFromDisplay 钉死 billing 参数的语义。
//
// 缺了这条区分,一个列着 12 个模型分组的管理页每打开一次就会刷出若干条"告警",
// 登记簿在一周内变成没人看的噪声 —— 而背景噪声与告警缺席是同一种失败。
func TestGroupRatioMissHookSeparatesBillingFromDisplay(t *testing.T) {
	loadRatioTables(t, `{"default":1}`, `{}`)

	type call struct {
		group   string
		billing bool
	}
	var calls []call
	original := QyNoteGroupRatioMiss
	QyNoteGroupRatioMiss = func(modelGroup string, billing bool) {
		calls = append(calls, call{modelGroup, billing})
	}
	t.Cleanup(func() { QyNoteGroupRatioMiss = original })

	ResolveGroupRatio("default", "孤儿池")
	InspectGroupRatio("default", "孤儿池")
	GetGroupRatio("孤儿池")

	require.Len(t, calls, 3, "源(计费/展示)与汇(GetGroupRatio)三处都必须上报")
	assert.Equal(t, call{"孤儿池", true}, calls[0], "计费解析:这一笔钱已经按凭空的 1.0 扣掉了")
	assert.Equal(t, call{"孤儿池", false}, calls[1], "展示解析:没有任何一笔钱被算错")
	assert.Equal(t, call{"孤儿池", false}, calls[2], "汇处拿不到用户分组,判断不出是不是在扣钱")
}

// TestGetGroupRatioStillFailsOpen 锁住上游那条 fail-open 本身没有被顺手改成拒绝。
//
// 它必须留在 1.0:严格模式的正确落点在 middleware/auth.go 的鉴权处
// (那里拒绝时请求还没花钱),不在这里(这里报错时上游 token 已经烧掉了)。
func TestGetGroupRatioStillFailsOpen(t *testing.T) {
	loadRatioTables(t, `{"default":1}`, `{}`)
	assert.Equal(t, float64(1), GetGroupRatio("不存在的分组"))
	assert.Equal(t, float64(1), GetGroupRatio("default"))
}
