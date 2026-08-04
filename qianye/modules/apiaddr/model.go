// Package apiaddr 实现「站点可选 API 地址簿」。
//
// # 它解决的问题
//
// 密钥列表上的「复制链接信息」把 `{"_type":"newapi_channel_conn","key":…,"url":…}`
// 塞进剪贴板给 CC Switch 用。改造前那个 url 是前端从 `localStorage.status
// .server_address` 单方面取的**唯一**值 —— 而实际部署里同一套站点常常有多个
// 可用入口(主域 / 备用域 / 直连 IP / 加速线路),用户拿到的永远是运营那一刻
// 配在系统设置里的那一个,想换只能自己手改 JSON。
//
// 本模块把「有哪些 API 地址可以给用户选」变成运营可维护的一张表:
// 名称 / 备注 / URL / 排序,增删改查 + 整表重排。用户侧只读已启用的那部分。
//
// # 边界
//
//   - 它**不**决定请求真正打到哪里:relay 的路由与本表无关,这里存的只是
//     "告诉用户该往客户端里填哪个地址"。填错了的后果是客户端连不上,
//     不会让任何一笔计费走偏。
//   - 因此它**没有**自己的 YAML 功能开关(guard.FlagCore):一张零热路径开销的
//     配置表,给它加开关只会多出一个"关掉之后前端回落站点地址、而管理员还在
//     那张页面上认真编辑"的状态。真要停用某一条,把它 disabled 即可。
package apiaddr

// Address 是一条可供用户选择的 API 地址。
//
// # 为什么不塞进上游的 options 表
//
// 上游 `options` 是 (key → 字符串) 的 KV,存一个有序列表只能塞 JSON 字符串,
// 于是"改一条"变成"读出整串、解析、改、写回",两个管理员同时编辑必然互相覆盖,
// 而且没有任何一行能被单独审计。这里要的恰恰是逐行的增删改与逐行的 before/after。
type Address struct {
	Id int `json:"id" gorm:"primaryKey"`

	// Name 是给用户看的名字(「主线路」「备用域名」)。必填。
	Name string `json:"name" gorm:"type:varchar(64);not null;default:''"`

	// Remark 是补充说明(「海外优先」「仅内网可达」)。选填,只展示不参与任何判定。
	Remark string `json:"remark" gorm:"type:varchar(256);not null;default:''"`

	// URL 是最终写进连接信息 JSON 的那个值,已归一化(见 normalizeURL):
	// 只允许 http/https、不带凭据、不带查询串与片段、去掉尾部斜杠。
	//
	// 归一化必须发生在**入库前**而不是下发时:前端拿它去拼字符串的地方不止一处,
	// 每处各归一化一遍就是同一概念的第 N 份拷贝,迟早出现"管理端看着是 a.com/,
	// 复制出来是 a.com"这种对不上。
	URL string `json:"url" gorm:"column:url;type:varchar(512);not null;default:''"`

	// SortOrder 是展示顺序,小的在前。相同则按 Id 升序 —— 排序键必须是全序,
	// 否则同 SortOrder 的两行在不同数据库/不同执行计划下顺序会变,
	// 而"第一条"正是前端在只有一条时直接采用的那一条。
	SortOrder int `json:"sort_order" gorm:"not null;default:0"`

	// Enabled 为 false 的地址只在管理端可见,用户侧列表不下发。
	//
	// 刻意不写 `gorm:"default:true"`:MySQL 与 PostgreSQL 对布尔默认值的归一化
	// 不同,AutoMigrate 会在每次重启时反复 ALTER TABLE(见 AGENTS.md)。
	// "新建的地址默认启用"是业务规则,由 createReq.apply 在代码里落。
	Enabled bool `json:"enabled" gorm:"not null;default:false"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
	CreatedBy int   `json:"created_by" gorm:"not null;default:0"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (Address) TableName() string { return "qy_api_addresses" }

// userView 是下发给普通用户的形态。
//
// 刻意不复用 Address:那个结构体带着 created_by / updated_by(管理员的用户 id)
// 与启用状态。今天它们无害,但"用户侧直接下发管理端行"这个形状本身就是
// 下一次泄漏的入口 —— 白名单让新增字段默认不外发,而不是默认外发。
type userView struct {
	Id     int    `json:"id"`
	Name   string `json:"name"`
	Remark string `json:"remark"`
	URL    string `json:"url"`
}
