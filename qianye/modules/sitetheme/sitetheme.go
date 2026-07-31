// Package sitetheme 让超级管理员设置站点默认主题。
//
// 为什么需要它:上游的默认预设硬编码在前端(DEFAULT_THEME_CUSTOMIZATION.preset
// = 'default'),想换默认主题只能改代码重新构建。这个模块把"默认值"变成运营
// 可改的配置,而用户的个人偏好(cookie)仍然优先 —— 站点默认只在用户没表达
// 过偏好时生效。
//
// 存扩展库而不是上游 options 表:后者是主库,写进去就违背了"新功能数据进独立
// 数据库"的约束。代价是扩展禁用时站点默认失效、前端回落到上游硬编码的
// 'default' —— 那正是正确的降级行为。
package sitetheme

import (
	"github.com/QuantumNous/new-api/qianye/module"

	"github.com/gin-gonic/gin"
)

// Mod 是本模块的注册入口。
type Mod struct{ module.Base }

func (Mod) Name() string { return "sitetheme" }

// Tables 返回空:配置复用地基提供的 qy_settings 表,不另建表。
// 一个只有一行的表不值得单独建 —— 那正是 qy_settings 存在的理由。
func (Mod) Tables() []any { return nil }

func (Mod) RegisterAdminRoutes(g *gin.RouterGroup) {
	g.GET("/site-theme", handleGetSiteTheme)
	g.PUT("/site-theme", handlePutSiteTheme)
}

func init() { module.Register(Mod{}) }
