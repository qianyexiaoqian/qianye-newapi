package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGroupRatioSaveRefusesToOrphanAPinnedModelGroup 锁住「删掉一个仍被 pin 的模型分组
// 的兜底倍率」必须被拒。
//
// ── 它挡住的是一次看起来完全无害的删除 ──
//
// 保存默认模型分组时有三道闸门,但它们只在保存那一刻执行一次。之后运营在「模型分组」
// 页删掉那一行 —— 页面文案明写「删除一行只会删掉它的兜底倍率,不会动渠道、令牌或
// 任何一档人的可用范围」,一个字都没提默认模型分组 —— 保存 options 成功,而被 pin 的
// 那个用户分组下的**全部空分组令牌**从下一次请求起变成 HTTP 500
// (middleware/auth.go 的 pin 运行时校验),比它们原来的 503 更难懂,而且绝大多数
// 客户端 SDK 会把 500 当成可重试的网关故障从而放大流量。
func TestGroupRatioSaveRefusesToOrphanAPinnedModelGroup(t *testing.T) {
	original := service.QyPinnedModelGroups
	t.Cleanup(func() { service.QyPinnedModelGroups = original })

	t.Run("hook 未接线时整段跳过 —— 逐位等于上游", func(t *testing.T) {
		service.QyPinnedModelGroups = func() map[string][]string { return nil }
		assert.NoError(t, checkGroupRatioKeepsPinnedGroups(`{"default":1}`))
	})

	service.QyPinnedModelGroups = func() map[string][]string {
		return map[string][]string{
			"浅梦号池促销": {"default", "浅夜の自己人"},
			"免费の渠道":  {"清芯"},
		}
	}

	t.Run("被 pin 的分组还在:放行", func(t *testing.T) {
		assert.NoError(t, checkGroupRatioKeepsPinnedGroups(
			`{"default":1,"浅梦号池促销":0.25,"免费の渠道":0}`))
	})

	t.Run("删掉一个被 pin 的分组:拒绝,并点名是谁在 pin 它", func(t *testing.T) {
		err := checkGroupRatioKeepsPinnedGroups(`{"default":1,"免费の渠道":0}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "浅梦号池促销")
		assert.Contains(t, err.Error(), "default")
		assert.Contains(t, err.Error(), "浅夜の自己人",
			"错误里必须列出**全部**受影响的用户分组,只报一个会让运营改完一个又撞第二个")
	})

	t.Run("同时删掉两个:两个都要出现在同一条错误里", func(t *testing.T) {
		err := checkGroupRatioKeepsPinnedGroups(`{"default":1}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "浅梦号池促销")
		assert.Contains(t, err.Error(), "免费の渠道")
	})

	t.Run("倍率显式为 0 也算「还在」—— 判据是键存在,不是值", func(t *testing.T) {
		// 0 是一个合法的兜底倍率(白送)。按值判断会把「免费」误判成「已删除」,
		// 于是一次正常的保存被拒,而运营完全看不出原因。
		assert.NoError(t, checkGroupRatioKeepsPinnedGroups(
			`{"浅梦号池促销":0,"免费の渠道":0}`))
	})

	t.Run("坏 JSON 放行 —— 语法由 CheckGroupRatio 判,这里不重复报错", func(t *testing.T) {
		assert.NoError(t, checkGroupRatioKeepsPinnedGroups(`{ 坏的`))
	})
}
