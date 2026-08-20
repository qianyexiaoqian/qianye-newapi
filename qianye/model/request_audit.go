package model

// RequestAudit 是扩展 HTTP 写请求的台账,与 AuditLog 刻意分成两张表。
//
// # 为什么不合进 qy_audit_logs
//
// 两者回答的是完全不同的问题:
//
//   - AuditLog(qy_audit_logs)是**资金决策台账**:一行等于一次改变了钱的判定,
//     带 trace_no、金额三件套、冻结汇率、前后快照。它的读者是仲裁纠纷的人,
//     每一行都要能被逐字辩论,因此保留期以年计、只追加、不允许一键清空。
//   - RequestAudit(qy_request_audits)是**HTTP 请求台账**:一行等于"谁在什么
//     时候调了哪个写接口、成功没成功、请求体长什么样"。它的读者是排查越权、
//     暴力枚举、误操作的人,价值在**覆盖率**而不在单行深度。
//
// 合成一张表只会两败俱伤:要么每行资金审计挂着 8 个恒为空的 HTTP 列,
// 要么资金台账被几千行"某人翻了一页列表"稀释到无法扫读。
//
// # 它补的是什么洞
//
// 资金审计靠手写埋点,写一处漏一处就是永久盲区 —— 加一个新接口而忘了埋点,
// 事后没有任何地方能证明它被调用过。请求台账由中间件统一兜底:
// 只要挂在 /api/qy 下,新接口天然有痕。两条路径互补,不互相替代。
type RequestAudit struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// Action 由 method + 路由模板推导(如 withdraw.payees.create),
	// 与 AuditLog.Action 同为稳定英文标识,前端按前缀分组。
	Action string `json:"action" gorm:"type:varchar(128);not null;default:'';index:idx_qy_req_action,priority:1"`
	Method string `json:"method" gorm:"type:varchar(8);not null;default:''"`
	// Path 存**路由模板**(/api/qy/admin/withdraw/:id/approve)而不是实际 URL。
	// 实际 URL 会把资源 ID 编进路径,让"同一个接口"散成上万个不同的 path 值,
	// 既无法聚合也无法按接口筛选;ID 另行进 Params。
	Path string `json:"path" gorm:"type:varchar(256);not null;default:''"`

	StatusCode int `json:"status_code" gorm:"not null;default:0"`
	// Success 冗余自 StatusCode(<400)。单独一列是为了让"只看失败"能走索引 ——
	// 越权探测与暴力枚举全是失败请求,那正是这张表最重要的查询。
	Success   bool  `json:"success" gorm:"not null;index:idx_qy_req_success,priority:1"`
	LatencyMs int64 `json:"latency_ms" gorm:"not null;default:0"`

	// ActorType 复用 AuditLog 的 ActorUser/ActorAdmin/ActorSystem 三值,
	// 不另起一套 —— 两张表的"操作者"必须是同一个概念,否则跨表对照时
	// 要先在脑子里做一次翻译,而翻译表迟早会漂移。
	ActorType   string `json:"actor_type" gorm:"type:varchar(16);not null;default:''"`
	ActorUserId int    `json:"actor_user_id" gorm:"not null;default:0;index:idx_qy_req_actor,priority:1"`
	ActorName   string `json:"actor_name" gorm:"type:varchar(64);not null;default:''"`
	// ActorRole 取上游 c.GetInt("role")(1 普通 / 10 管理 / 100 root)。
	ActorRole int `json:"actor_role" gorm:"not null;default:0"`
	// AuthMethod 取上游 c.GetBool("use_access_token") 的两种取值,
	// 不自己定义 jwt/passkey 之类上游根本没有写进 context 的值 ——
	// 那种"看起来更细"的枚举只会永远是同一个常量。
	AuthMethod string `json:"auth_method" gorm:"type:varchar(24);not null;default:''"`

	// TargetUserId 由路径参数 :user_id / :uid 推导,没有则为 0。
	TargetUserId int `json:"target_user_id" gorm:"not null;default:0;index:idx_qy_req_target,priority:1"`

	// Params / Query / Body 均已脱敏。Body 只在 Content-Type 为 JSON 且解析成功时
	// 入库,非 JSON 一律只留占位说明(表单里的密码字段没有键级结构可依。)
	Params string `json:"params" gorm:"type:varchar(512);not null;default:''"`
	Query  string `json:"query" gorm:"type:varchar(1024);not null;default:''"`
	Body   string `json:"body" gorm:"type:text"`

	IP        string `json:"ip" gorm:"type:varchar(64);not null;default:''"`
	UserAgent string `json:"user_agent" gorm:"type:varchar(256);not null;default:''"`
	RequestId string `json:"request_id" gorm:"type:varchar(64);not null;default:'';index"`
	NodeName  string `json:"node_name" gorm:"type:varchar(160);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_req_created;index:idx_qy_req_action,priority:2;index:idx_qy_req_actor,priority:2;index:idx_qy_req_target,priority:2;index:idx_qy_req_success,priority:2"`
}

func (RequestAudit) TableName() string { return "qy_request_audits" }

// 认证方式。取值直接对应上游 middleware/auth.go 写进 gin context 的
// use_access_token,不引入上游不存在的第三种。
const (
	AuthMethodSession     = "session"
	AuthMethodAccessToken = "access_token"
)
