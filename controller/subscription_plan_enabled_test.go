package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 新建套餐时的上架开关必须能分辨「没写」与「明确写了 false」。
//
// 此前这件事由列默认值 gorm:"default:true" 兼着做,而 GORM 对带默认值的字段
// 在 Create 时**跳过零值** —— 于是两者一起变成 true,管理端那个开关在新建时
// 是个摆设:实测 POST /api/subscription/admin/plans 带 enabled:false 返回 200、
// 库里 enabled=1,套餐落库即上架,而四个支付网关的 !plan.Enabled 闸门全部放行。
// AdminCreateSubscriptionPlan 是全站唯一一条裸 struct Create 的套餐写入口
// (改套餐走 updates map、上下架走 Update 单列,那两条都不受影响)。
func TestPlanEnabledFromBodyDistinguishesAbsentFromFalse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *bool
	}{
		{"没写 enabled", `{"plan":{"title":"a"}}`, nil},
		{"明确写了 false", `{"plan":{"title":"a","enabled":false}}`, boolPtr(false)},
		{"明确写了 true", `{"plan":{"title":"a","enabled":true}}`, boolPtr(true)},
		{"整个 plan 都没有", `{}`, nil},
		{"空请求体", ``, nil},
		{"不是合法 JSON", `not json`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planEnabledFromBody([]byte(tc.body))
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got, "显式写出来的值不许被读成「没写」")
			assert.Equal(t, *tc.want, *got)
		})
	}
}

// 列上不许再出现布尔默认值:它既让 AutoMigrate 在 MySQL/PostgreSQL 上反复
// 发 ALTER(AGENTS.md 明令),又是上面那条「零值被跳过」的成因。
func TestSubscriptionPlanEnabledHasNoColumnDefault(t *testing.T) {
	src := readSubscriptionModelSource(t)
	assert.NotContains(t, src, "`json:\"enabled\" gorm:\"default:true\"`",
		"Enabled 不许带列默认值:业务默认由 AdminCreateSubscriptionPlan 承担")
}

func boolPtr(b bool) *bool { return &b }

func readSubscriptionModelSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "model", "subscription.go"))
	require.NoError(t, err)
	return string(raw)
}
