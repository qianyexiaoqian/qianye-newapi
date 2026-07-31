package groupvis

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	perfmetrics "github.com/QuantumNous/new-api/pkg/perf_metrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupSet 构造 service.GetUserUsableGroups 形态的白名单(值是分组描述,过滤只看键)。
func groupSet(names ...string) map[string]string {
	m := make(map[string]string, len(names))
	for _, n := range names {
		m[n] = n + " 分组"
	}
	return m
}

func TestFilterPricing(t *testing.T) {
	cases := []struct {
		name        string
		enableGroup []string
		usable      map[string]string
		keepAuto    bool
		wantDropped bool
		want        []string
	}{
		{
			name:        "只保留用户有权的分组子集",
			enableGroup: []string{"default", "internal", "vip"},
			usable:      groupSet("default", "vip"),
			keepAuto:    true,
			want:        []string{"default", "vip"},
		},
		{
			name:        "全部分组均无权时整条模型不下发",
			enableGroup: []string{"internal", "partner"},
			usable:      groupSet("default", "vip"),
			keepAuto:    true,
			wantDropped: true,
		},
		{
			name:        "匿名访客只看到公开白名单内的分组",
			enableGroup: []string{"default", "internal"},
			usable:      groupSet("default", "vip"), // GetUserUsableGroups("") 的返回形态
			keepAuto:    true,
			want:        []string{"default"},
		},
		{
			name:        "auto 伪分组在 keepAuto 时始终保留",
			enableGroup: []string{"auto", "internal"},
			usable:      groupSet("default"),
			keepAuto:    true,
			want:        []string{"auto"},
		},
		{
			name:        "auto 在 keepAuto 关闭时按普通分组处理",
			enableGroup: []string{"auto", "internal"},
			usable:      groupSet("default"),
			keepAuto:    false,
			wantDropped: true,
		},
		{
			name:        "all 通配展开为用户自己的可用分组",
			enableGroup: []string{"all"},
			usable:      groupSet("default", "vip"),
			keepAuto:    true,
			want:        []string{"default", "vip"},
		},
		{
			name:        "all 与真实分组并存时不产生重复项",
			enableGroup: []string{"all", "default"},
			usable:      groupSet("default", "vip"),
			keepAuto:    true,
			want:        []string{"default", "vip"},
		},
		{
			name:        "白名单为空时不下发任何模型",
			enableGroup: []string{"default", "vip"},
			usable:      nil,
			keepAuto:    true,
			wantDropped: true,
		},
		{
			// EnableGroup 源自 types.Set.Items() 的 map 随机序,而前端用 groups[0]
			// 当主分组展示;不排序会让同一模型每次缓存刷新换一个分组名。
			name:        "输出按字典序稳定",
			enableGroup: []string{"vip", "default", "beta"},
			usable:      groupSet("default", "vip", "beta"),
			keepAuto:    true,
			want:        []string{"beta", "default", "vip"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := []model.Pricing{{ModelName: "gpt-x", EnableGroup: tc.enableGroup}}
			got := filterPricing(in, tc.usable, tc.keepAuto)
			require.NotNil(t, got)
			if tc.wantDropped {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, "gpt-x", got[0].ModelName)
			assert.Equal(t, tc.want, got[0].EnableGroup)
		})
	}
}

// TestFilterPricingDoesNotTouchInputBackingArray 锁定本模块最关键的内存安全约束。
//
// model.GetPricing() 返回的是包级缓存 pricingMap 本身,且 modelEnableGroups
// (管理端模型元数据的数据源)与它共享同一批底层数组。任何原地覆写或对
// item.EnableGroup 的 append,都会把某一个用户的可见范围写进全局缓存。
func TestFilterPricingDoesNotTouchInputBackingArray(t *testing.T) {
	t.Run("普通分组不得被原地压缩", func(t *testing.T) {
		backing := []string{"default", "internal", "vip"}
		in := []model.Pricing{{ModelName: "gpt-x", EnableGroup: backing}}

		got := filterPricing(in, groupSet("default", "vip"), true)
		require.Len(t, got, 1)
		assert.Equal(t, []string{"default", "vip"}, got[0].EnableGroup)

		// 原地压缩的典型症状是 backing 变成 ["default","vip","vip"]。
		assert.Equal(t, []string{"default", "internal", "vip"}, backing,
			"入参底层数组被修改,会污染 pricingMap 与 modelEnableGroups 全局缓存")
		assert.Equal(t, []string{"default", "internal", "vip"}, in[0].EnableGroup)

		// 返回值与入参必须彻底解耦:改动返回值不得回写到缓存数组。
		got[0].EnableGroup[0] = "tampered"
		assert.Equal(t, "default", backing[0], "返回的切片仍与入参共享底层数组")
	})

	t.Run("all 通配展开不得 append 进共享数组的空闲容量", func(t *testing.T) {
		// len=1 / cap=3:实现若对 item.EnableGroup 直接 append,会覆写两个哨兵值。
		backing := []string{"all", "canary-1", "canary-2"}
		in := []model.Pricing{{ModelName: "gpt-x", EnableGroup: backing[:1:3]}}

		got := filterPricing(in, groupSet("default", "vip"), true)
		require.Len(t, got, 1)
		assert.Equal(t, []string{"default", "vip"}, got[0].EnableGroup)
		assert.Equal(t, []string{"all", "canary-1", "canary-2"}, backing,
			"append 写进了入参底层数组的空闲容量")
	})
}

// TestFilterPricingConcurrentUsersDoNotInterfere 在 -race 下验证:
// 多个用户并发命中同一份定价缓存时,彼此的裁剪结果互不影响,且缓存本身只被读取。
func TestFilterPricingConcurrentUsersDoNotInterfere(t *testing.T) {
	shared := []model.Pricing{
		{ModelName: "gpt-x", EnableGroup: []string{"default", "internal", "vip"}},
		{ModelName: "gpt-y", EnableGroup: []string{"all"}},
	}

	views := []struct {
		usable map[string]string
		want   []string
	}{
		{groupSet("default"), []string{"default"}},
		{groupSet("vip"), []string{"vip"}},
		{groupSet("default", "vip"), []string{"default", "vip"}},
		{groupSet("internal"), []string{"internal"}},
	}

	var wg sync.WaitGroup
	for round := 0; round < 32; round++ {
		for _, v := range views {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// 子协程里只能用 assert:require 会调用 t.FailNow(),
				// 而它必须在测试主协程执行,否则失败无法被正确记录。
				got := filterPricing(shared, v.usable, true)
				if !assert.Len(t, got, 2) {
					return
				}
				assert.Equal(t, v.want, got[0].EnableGroup)
				assert.Equal(t, v.want, got[1].EnableGroup) // "all" 展开成本用户的白名单
			}()
		}
	}
	wg.Wait()

	assert.Equal(t, []string{"default", "internal", "vip"}, shared[0].EnableGroup)
	assert.Equal(t, []string{"all"}, shared[1].EnableGroup)
}

func TestFilterGroupKeys(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		usable     map[string]string
		keepAuto   bool
		want       []string
	}{
		{
			name:       "剔除用户无权的分组",
			candidates: []string{"default", "internal", "vip", "auto"},
			usable:     groupSet("default", "vip"),
			keepAuto:   true,
			want:       []string{"default", "vip", "auto"},
		},
		{
			name:       "匿名访客只保留公开白名单与 auto",
			candidates: []string{"default", "internal", "partner", "auto"},
			usable:     groupSet("default", "vip"),
			keepAuto:   true,
			want:       []string{"default", "auto"},
		},
		{
			name:       "候选中重复的 auto 只出现一次",
			candidates: []string{"auto", "default", "auto"},
			usable:     groupSet("default"),
			keepAuto:   true,
			want:       []string{"auto", "default"},
		},
		{
			name:       "keepAuto 关闭时 auto 按普通分组处理",
			candidates: []string{"default", "auto"},
			usable:     groupSet("default"),
			keepAuto:   false,
			want:       []string{"default"},
		},
		{
			name:       "候选缺少 auto 时由本函数补齐,保证结果非空",
			candidates: []string{"internal"},
			usable:     nil,
			keepAuto:   true,
			want:       []string{"auto"},
		},
		{
			name:       "全部无权且不保留 auto 时返回空切片(而非 nil)",
			candidates: []string{"internal", "partner"},
			usable:     nil,
			keepAuto:   false,
			want:       []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterGroupKeys(tc.candidates, tc.usable, tc.keepAuto)
			// nil 会被 model.GetPerfMetricsSummaryBucketsAll 与 allowedGroupSet
			// 解读为「不过滤」,语义直接翻转成全站泄漏 —— 这是必须守住的不变量。
			require.NotNil(t, got, "返回 nil 会让下游退化为不过滤")
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFilterGroupKeysDoesNotMutateCandidates(t *testing.T) {
	candidates := []string{"default", "internal", "vip"}
	got := filterGroupKeys(candidates, groupSet("default"), true)

	assert.Equal(t, []string{"default", "auto"}, got)
	assert.Equal(t, []string{"default", "internal", "vip"}, candidates)
}

func TestFilterPerfGroups(t *testing.T) {
	rows := []perfmetrics.GroupResult{
		{Group: "default", SuccessRate: 0.99},
		{Group: "internal", SuccessRate: 0.42},
		{Group: "auto", SuccessRate: 0.97},
	}

	cases := []struct {
		name     string
		usable   map[string]string
		keepAuto bool
		want     []string
	}{
		{
			name:     "无权分组的运营数据被整条剔除",
			usable:   groupSet("default", "vip"),
			keepAuto: true,
			want:     []string{"default", "auto"},
		},
		{
			name:     "匿名探测无权分组时拿不到任何行",
			usable:   nil,
			keepAuto: false,
			want:     []string{},
		},
		{
			name:     "auto 在 keepAuto 时保留",
			usable:   nil,
			keepAuto: true,
			want:     []string{"auto"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterPerfGroups(rows, tc.usable, tc.keepAuto)
			require.NotNil(t, got)
			names := make([]string, 0, len(got))
			for _, g := range got {
				names = append(names, g.Group)
			}
			assert.Equal(t, tc.want, names)
			assert.Len(t, rows, 3, "入参不得被裁剪")
		})
	}
}
