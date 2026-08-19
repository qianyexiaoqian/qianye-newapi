package controller

import (
	"errors"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"

	"github.com/gin-gonic/gin"
)

// restricted_accounts.go —— 管理端「系统设置 → 受限账号」那一页的取数端点。
//
// # 这一页为什么存在
//
// 项目方原话:「受限制账号,在系统设置里面单独进行配置。」在此之前受限态的唯一
// 可配项(公告)寄住在「内容管理」里,紧挨站点公告 —— 那一页回答的是"给全站用户
// 看什么",而受限态回答的是"被限制的人看什么、还能做什么"。两者的读者不同,
// 混在一起之后管理员想弄清"受限到底限了什么"没有任何一处可看。
//
// # 这个端点下发什么、为什么是这两件
//
//	count         当前有多少个受限账号。**只有后端答得出** —— 前端手里只有
//	              当前登录者自己的 status。
//	capabilities  受限账号仍然能到达哪几档接口,**从会话白名单本身派生**
//	              (middleware.RestrictedUserAllowedRoutes)。
//
// 后者是这一页最容易被做错的地方,所以把判据写清楚:
//
//   - 白名单**不是配置**,它是 middleware/restricted_user.go 里的显式清单。
//     做成可配的那一刻,"今后新增的接口默认对受限账号开放"这个失败模式就回来了
//     (见那份清单顶部关于白名单 vs 黑名单的论证)。所以这里只**读**、只展示,
//     没有任何写入口。
//   - 但"只展示"不等于"抄一份给前端显示"。抄一份的失败方式是静默的:白名单加了
//     一条,页面上的说明还是旧的,而管理员正是拿这一页去回答用户"我为什么点不了"。
//     所以分档表(restrictedCapabilities)与白名单同源同包,并由
//     restricted_accounts_test.go 双向核对:白名单里的每一条都必须**恰好**属于
//     一档,每一档都必须至少认领一条。新增一条没归档的白名单路由 = 测试变红。
//
// # available 为什么必须逐档下发
//
// 白名单是静态的,而模块是可以关掉的:`ticket.enabled=false` 时那 9 条工单路由
// 压根没有注册,受限账号手里就一条申诉通道都没有 —— 而白名单照样写着"工单放行"。
// 只显示白名单等于告诉管理员"他能提工单",然后用户点进去 404。这一档的
// available 由与 /api/qy/config 同一个数据源(config.Get())回答,两处不可能漂移。
type restrictedCapability struct {
	// Key 是前端 i18n 键的后缀(qy_ra_cap_<key>),不是给人看的文案。
	// 文案留在前端:同一句话要出 7 个语种,后端下发中文只会把它钉死成一种。
	Key string
	// Prefixes 是本档认领的白名单路径前缀。匹配按**路径段**收尾
	// (path == prefix 或 path 以 prefix+"/" 开头),不是裸 strings.HasPrefix ——
	// 后者会让 /api/user/self-service 这种将来可能出现的路径被 /api/user/self
	// 悄悄认领,而它根本不在白名单里。
	Prefixes []string
	// Available 报告这一档现在**真的**可达(承载它的模块开着)。
	Available func() bool
}

func restrictedCapabilityAlwaysOn() bool { return true }

// restrictedCapabilities 是「受限账号还能做什么」的分档表。
//
// 顺序 = 页面上的显示顺序,按"离申诉有多近"排:先是他能看见自己出了什么事,
// 再是正式通道。
var restrictedCapabilities = []restrictedCapability{
	{
		// 渲染受限落地页所需的最小自身信息。它不依赖任何模块,
		// 关不掉也不该能关 —— 关掉之后前端连"我被限制了"都判定不出来。
		Key:       "self",
		Prefixes:  []string{"/api/user/self"},
		Available: restrictedCapabilityAlwaysOn,
	},
	{
		// 本页配的那段公告。同样不依赖模块(见 restricted_notice.go 的说明)。
		Key:       "notice",
		Prefixes:  []string{"/api/qy/restricted-notice"},
		Available: restrictedCapabilityAlwaysOn,
	},
	{
		Key:       "violation",
		Prefixes:  []string{"/api/qy/violation"},
		Available: func() bool { return config.Get().Violation.Enabled },
	},
	{
		Key:       "ticket",
		Prefixes:  []string{"/api/qy/ticket"},
		Available: func() bool { return config.Get().Ticket.Enabled },
	},
}

// errRestrictedAccountsNoDB 是主库句柄还没接上时的出口。生产里不会发生
// (RegisterRoutes 在 model.InitDB 之后),但测试与将来的启动顺序调整会。
var errRestrictedAccountsNoDB = errors.New("main database is not ready")

// restrictedRoutePath 从白名单条目("METHOD /path")里取出路径。
// 取不出(格式不对)时返回空串,由调用方判成"没有任何一档认领"。
func restrictedRoutePath(entry string) string {
	idx := strings.IndexByte(entry, ' ')
	if idx < 0 {
		return ""
	}
	return entry[idx+1:]
}

// restrictedCapabilityOwns 报告某条白名单路径是否属于这一档。
func (group restrictedCapability) owns(path string) bool {
	if path == "" {
		return false
	}
	for _, prefix := range group.Prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// restrictedCapabilityView 是下发给前端的一档。
type restrictedCapabilityView struct {
	Key       string `json:"key"`
	Available bool   `json:"available"`
	// Routes 是本档认领的白名单条目原文,已排序。
	//
	// 下发它而不是只给一句概括:管理员真正会问的是"受限账号到底还能调什么",
	// 而路由模式本身不含任何机密(它们就写在开源仓库里)。有了这份清单,
	// 这一页才是可核对的,而不是四句需要读 Go 才能验证的断言。
	Routes []string `json:"routes"`
}

// AdminRestrictedAccountsOverview 受限账号总览。
// GET /api/qy/admin/restricted-accounts
//
// # 为什么不走 requireCore
//
// 与 UserSessionStats 同一理由:本端点**一个字都不碰扩展库** —— 计数查的是主库
// users,分档表是进程内常量。让它在扩展库不可用时 503,只会让管理员在最需要
// 弄清"现在有多少人被限制"的时刻拿到一个数据库错误。
// 同一页上的公告表单走的是扩展库、自带 requireCore,两者互不影响:扩展库挂了
// 的时候这一页仍然告诉你有几个受限账号,只是公告那一块报错。
func AdminRestrictedAccountsOverview(c *gin.Context) {
	if model.DB == nil {
		serverError(c, errRestrictedAccountsNoDB)
		return
	}
	var count int64
	// GORM 的软删除会自动带上 deleted_at IS NULL —— 已删除的账号不该算进
	// "当前有多少人被限制":管理员点进用户列表也看不到他们,两个数字必须对得上。
	if err := model.DB.Model(&model.User{}).
		Where("status = ?", common.UserStatusDisabled).
		Count(&count).Error; err != nil {
		serverError(c, err)
		return
	}

	allowed := middleware.RestrictedUserAllowedRoutes()
	views := make([]restrictedCapabilityView, 0, len(restrictedCapabilities))
	for _, group := range restrictedCapabilities {
		routes := make([]string, 0, 4)
		for _, entry := range allowed {
			if group.owns(restrictedRoutePath(entry)) {
				routes = append(routes, entry)
			}
		}
		sort.Strings(routes)
		views = append(views, restrictedCapabilityView{
			Key:       group.Key,
			Available: group.Available(),
			Routes:    routes,
		})
	}

	ok(c, gin.H{
		"count":        count,
		"capabilities": views,
	})
}
