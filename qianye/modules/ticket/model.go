// Package ticket 实现工单系统:用户提交问题(标题 / 正文 / 等级),管理员答复。
//
// 正文是 Markdown 源码,**后端原样存、原样回**,不做任何 HTML 转换 ——
// 渲染与转义全部发生在浏览器里(marked 解析 + DOMPurify 净化,见前端
// components/ui/markdown.tsx)。后端如果自作主张生成一段 HTML 存进库,
// 那段 HTML 从此绕过前端的净化器,而它同时也是唯一的净化器。
//
// 两条与本仓其他模块不同的地方,先说清楚:
//
//  1. **不动钱**。工单不碰额度、不碰佣金、不进两阶段。因此没有 fund_order、
//     没有补偿任务、没有对账。管理端的写接口仍然全部写审计 —— 改等级/关单/
//     指派都是"事后要能解释为什么"的动作。
//
//  2. **图片不入库**。附件本体落本地磁盘,元数据留在 qy_ticket_attachments。
//     落盘/取回/清理的机制在 qianye/service/imagestore,与提现凭证共用同一份
//     (理由见那个包的包注释)。
package ticket

// Ticket 是一张工单的头。
//
// 消息正文全部在 qy_ticket_messages 里,本表一个字都不存 —— 列表页要能按
// 状态/等级/用户聚合与排序,而正文是每行几 KB 的冷数据,放进主表意味着
// 每一次列表查询都拖着它走。
type Ticket struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// TicketNo 是面向用户与客服的业务单号。对外一律用它,绝不下发自增 id ——
	// 那等于把主键空间暴露给客户端,也让越权猜测变得廉价。
	TicketNo string `json:"ticket_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_tk_no"`

	UserId int `json:"user_id" gorm:"not null;index:idx_qy_tk_user,priority:1"`
	// Username 是快照。用户可改名可注销,管理端队列不能靠实时 join 主库。
	Username string `json:"username" gorm:"type:varchar(64);not null;default:''"`

	Title string `json:"title" gorm:"type:varchar(500);not null;default:''"`
	// Priority ∈ low|normal|high|urgent。用户在建单时自选,之后只有管理员能改 ——
	// 用户自己能反复调高等级的话,这一列在队列排序里就没有任何信息量了。
	Priority string `json:"priority" gorm:"type:varchar(16);not null;default:'normal';index:idx_qy_tk_status,priority:2"`
	// Status ∈ open|replied|user_replied|closed,状态机见 status.go。
	Status string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_tk_status,priority:1;index:idx_qy_tk_updated,priority:1"`

	// MessageCount 是消息条数的冗余投影,唯一写入点是 appendMessage 所在的事务
	// (与消息插入同生共死)。不冗余的话,列表页每渲染一行就要多一次 count(*),
	// 而 max_messages_per_ticket 那道闸也得在每次追加前多查一次。
	MessageCount int `json:"message_count" gorm:"not null;default:0"`

	// LastReplyAt / LastReplyBy 回答"这张单最后一次动是谁在什么时候" ——
	// 客服队列按它排序(等得最久的排前面),自动关闭也按它计时。
	LastReplyAt int64  `json:"last_reply_at" gorm:"not null;default:0;index:idx_qy_tk_updated,priority:2"`
	LastReplyBy string `json:"last_reply_by" gorm:"type:varchar(16);not null;default:''"`
	// FirstRepliedAt 是首次人工响应时刻,0 表示还没有人回过。
	// 它是"首响时长"这个唯一有意义的客服指标的原始数据,被后续回复覆盖就没了。
	FirstRepliedAt int64 `json:"first_replied_at" gorm:"not null;default:0"`

	// AssigneeId / AssigneeName 是指派给哪个管理员,0 表示未指派。
	// 快照姓名的理由与 Username 相同。
	AssigneeId   int    `json:"assignee_id" gorm:"not null;default:0;index:idx_qy_tk_assignee"`
	AssigneeName string `json:"assignee_name" gorm:"type:varchar(64);not null;default:''"`

	// ClosedAt / ClosedBy 回答"什么时候关的、谁关的"。
	// ClosedBy ∈ user|admin|system(system = auto_close_days 到期自动关闭)。
	//
	// ClosedAt 同时是图片保留期的计时起点(见 tasks.go):从关闭时刻起算而不是
	// 上传时刻 —— 一张挂了半年还在争议中的工单,它的截图正是争议的物证。
	ClosedAt int64  `json:"closed_at" gorm:"not null;default:0;index:idx_qy_tk_closed"`
	ClosedBy string `json:"closed_by" gorm:"type:varchar(16);not null;default:''"`

	ClientIp  string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;index:idx_qy_tk_user,priority:2"`
	UpdatedAt int64  `json:"updated_at" gorm:"not null"`
}

func (Ticket) TableName() string { return "qy_tickets" }

// Message 是工单里的一条消息,append-only。
//
// 永不 UPDATE、永不删除:工单的全部价值就是"当时到底说了什么"。允许编辑等于
// 允许一方在争议之后改口,而另一方手里没有任何副本。要更正就再发一条。
type Message struct {
	Id       int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	TicketId int64  `json:"-" gorm:"not null;index:idx_qy_tkm_tid,priority:1"`
	TicketNo string `json:"ticket_no" gorm:"type:varchar(64);not null;default:''"`

	// AuthorType ∈ user|admin|system。
	AuthorType string `json:"author_type" gorm:"type:varchar(16);not null"`
	AuthorId   int    `json:"-" gorm:"not null;default:0"`
	// AuthorName 在下发给普通用户时会被降级成"管理员"(见 view.go),
	// 否则用户可以把全部管理员账号名枚举出来。
	AuthorName string `json:"author_name" gorm:"type:varchar(64);not null;default:''"`

	// Body 是 **Markdown 源码**。后端不解析、不转 HTML、不做标签白名单 ——
	// 净化只发生在渲染那一侧,而且只有那一处。存 HTML 会让这段内容永久绕过它。
	Body string `json:"body" gorm:"type:text"`

	// Internal 标记管理员内部备注:只在管理端可见,用户端接口按 SQL 条件过滤掉。
	//
	// 过滤必须写进 **WHERE**(见 loadUserMessages),不能取回来再过滤:
	// 少写一个 if 的后果是把客服的内部判断原样发给被投诉的那个用户。
	Internal bool `json:"internal" gorm:"not null;default:false"`

	ClientIp  string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	CreatedAt int64  `json:"created_at" gorm:"not null;index:idx_qy_tkm_tid,priority:2"`
}

func (Message) TableName() string { return "qy_ticket_messages" }

// Attachment 是一张工单图片的元数据。图片本体在【本地磁盘】上,本表只存指针。
//
// 为什么不入库:一张 2 MiB 的手机截图进 MySQL,等于让每一次 SELECT *、每一次
// mysqldump、每一次主从复制都拖着它走。图片是只在看这张工单时才被读一次的
// 冷数据,放数据库里付的是全链路的热成本(withdraw/model.go 的 Proof 已经
// 算过同一笔账)。
//
// 已知限制:多节点部署时本地磁盘各存各的,A 节点收到的上传 B 节点下载不到。
// 单节点部署无碍;多节点需要共享存储或后续接对象存储。这条写在配置注释里。
type Attachment struct {
	Id int64 `json:"-" gorm:"primaryKey;autoIncrement"`
	// Ref 是对外唯一标识,同样绝不下发自增 id。
	Ref string `json:"ref" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_tka_ref"`
	// UserId 是**上传者**(可能是提单用户,也可能是回复的管理员)。
	UserId int `json:"-" gorm:"not null;index:idx_qy_tka_user,priority:1"`

	// TicketId = 0 表示"上传了但还没随任何一条消息提交"。孤儿清理正是扫这个条件 ——
	// 用户传完图关掉页面是常态,不清理就是一个只增不减的磁盘泄漏。
	TicketId  int64  `json:"-" gorm:"not null;default:0;index:idx_qy_tka_tid"`
	MessageId int64  `json:"-" gorm:"not null;default:0"`
	TicketNo  string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	// StoredName 是【服务端生成】的落盘文件名(32 位十六进制 + 白名单扩展名)。
	// 用户提供的文件名从不落盘、也从不拼进路径:那是路径穿越最短的一条路。
	// 用户原始文件名连存都不存 —— 它本身就可能是 PII(比如"张三身份证.jpg")。
	StoredName string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// MimeType 由【魔数】判定,不信任扩展名,也不信任 Content-Type 请求头。
	MimeType string `json:"mime_type" gorm:"type:varchar(32);not null;default:''"`
	Size     int64  `json:"size" gorm:"not null;default:0"`
	// Sha256 用于事后自证"下载到的与当初上传的是同一张图",也顺带能发现重复上传。
	Sha256 string `json:"-" gorm:"type:char(64);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_tka_user,priority:2"`
	BoundAt   int64 `json:"-" gorm:"not null;default:0"`
	// PurgedAt 是文件已从磁盘删除的时间戳(0 = 文件还在)。元数据行留着 ——
	// 它是"这条消息当时附过一张图"的证据,可读取的内容销毁,证据不销毁。
	PurgedAt int64 `json:"-" gorm:"not null;default:0;index:idx_qy_tka_purge"`
}

func (Attachment) TableName() string { return "qy_ticket_attachments" }
