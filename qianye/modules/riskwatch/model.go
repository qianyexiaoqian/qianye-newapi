// Package riskwatch 实现「风控预警」—— 管理员对可疑账号的定向监听取证。
//
// # 它回答的问题
//
// 违规检测回答「这一次请求该不该被拦」:判据是规则,处置是拦截、扣费、封号。
// 风控预警回答的是另一个问题 ——「这个账号最近到底在做什么」。它**只观察不处置**:
// 管理员立一个监听任务(盯某个用户、某个分组、某个分组下的某个模型),设定
// 记录概率、抽多少条、抽多久,系统按这个口径把命中请求的上下文原样留档,
// 供人事后翻看。
//
// 处置仍然走既有的路:违规规则、封号策略、工单。这一页只负责让管理员**看得到**
// 该处置谁 —— 一个刚被举报的账号,规则库里没有任何一条能命中他,而"他到底在
// 问什么"在此之前没有任何地方查得到。
//
// # 三条硬性边界
//
//	绝不拦截    挂在 relayguard 的观察者一侧,没有返回值。被违规规则拦下来的
//	            那一次照抓 —— 连续撞违规词、每次都被拒,恰恰最该有记录。
//	绝不同步落库 热路径只做"抄一份内存快照"这一件事,写库全部走 guard.HotAsync。
//	绝不进主库   两张表都住在单独配置的存储节点上(见 qianye/db/watchdb.go)。
//	            没配存储节点时本模块整个不注册。
//
// # 抓的是转发**之前**那一刻
//
// 挂载点在 relay 的前置闸门上,因此记录的是用户发过去的完整上下文,不含模型的
// 回复。这条边界是刻意的:回复要在转发**之后**才存在,而流式响应是边转发边吐的,
// 要留档就得在热路径上边转发边聚合整段输出 —— 那是给主业务加内存与延迟,
// 而取证要看的九成是"他问了什么",不是"模型答了什么"。
package riskwatch

// Task 是一个监听任务。
//
// # 作用域:三格,空 = 不限,至少填一格
//
// 项目方点名的三种形态(某个用户 / 某个分组 / 某个分组的某个模型)在这里是
// 三格的组合,不是三种类型。做成组合而不是枚举,是因为"某个用户的某个模型"
// 这种一望即知的第四种形态,在枚举那种表达里要么写不出来、要么要再加一个类型。
//
// **三格全空会被接口拒绝**。它在这里不是"全站监听"而是一个陷阱:一个全站
// 100% 的任务在页面上与一个用户级任务长得一模一样,而它的代价是这个存储节点
// 在几小时内被写满。要全站抽样请显式填一个分组。
type Task struct {
	Id int64 `json:"id" gorm:"primaryKey"`

	// Name 是任务名,给人看的(「疑似批量刷 claude」)。必填。
	Name string `json:"name" gorm:"type:varchar(128);not null;default:''"`

	// Note 是立案理由。选填,只展示不参与任何判定。
	//
	// 它是这张表上最该被写满的一格:一个三个月前建的监听任务,若没有理由,
	// 后来的人既不敢停也不敢留。
	Note string `json:"note" gorm:"type:varchar(512);not null;default:''"`

	// TargetUserId 是被监听的用户 id,0 = 不限。
	TargetUserId int `json:"target_user_id" gorm:"not null;default:0;index:,composite:target"`

	// TargetUsername 是建任务那一刻的用户名快照,只用于列表展示。
	//
	// 冗余一份而不是查表:两张表在**不同的数据库**上,列表页 JOIN 不回去
	// (见 module.WatchTabler 的硬约束)。用户改名之后这一格会过期,那可以接受 ——
	// 判定用的始终是 TargetUserId。
	TargetUsername string `json:"target_username" gorm:"type:varchar(64);not null;default:''"`

	// TargetGroup 是被监听的**用户分组**,空 = 不限。
	//
	// 列名写死 user_group 而不是 group:GROUPS/GROUP 在 MySQL 8 是保留字,
	// 而且本仓把「用户分组 / 模型分组」拆成了两个命名空间(见 groupns),
	// 名字必须把限定的是哪一个说死。这里比的是 relayInfo.UsingGroup。
	TargetGroup string `json:"target_group" gorm:"column:user_group;type:varchar(64);not null;default:''"`

	// TargetModel 是被监听的模型名,空 = 不限。
	//
	// 比的是 relayInfo.OriginModelName —— 用户**请求的**那个名字,不是渠道映射
	// 之后的那个。理由:管理员在页面上看到的、在日志里搜的,都是前者。
	TargetModel string `json:"target_model" gorm:"column:model_name;type:varchar(128);not null;default:''"`

	// SampleBps 是记录概率,万分比(10000 = 100%)。
	//
	// 用 bps 整数而不是浮点百分比,与全仓一致:整数可复现、可入库比较、
	// 不受浮点误差影响。它是这个功能唯一的成本闸门,因此界面上那个百分比
	// 必须是字面意思 —— 作用域闸排在抽样**之前**,作用域外的请求一次骰子都不摇。
	SampleBps int `json:"sample_bps" gorm:"not null;default:0"`

	// MaxRecords 是抽满多少条自动停止,0 = 不限。
	MaxRecords int `json:"max_records" gorm:"not null;default:0"`

	// Captured 是已抓条数。
	//
	// 它不是统计值而是**闸门本身**:落库那一步用一条带条件的 UPDATE 原子地
	// 预留一个名额(见 reserveSlot),抢不到就说明抽满了。靠"先 COUNT 再插入"
	// 在多节点下必然超抓,而"抽满自动停止"是运营在页面上写下的一个承诺。
	Captured int `json:"captured" gorm:"not null;default:0"`

	// WindowMode 是时间窗的形态:forever / range / countdown。
	//
	// 三种形态在库里都归一成 StartsAt/EndsAt 两个时间戳 —— 判定只认时间戳,
	// 这一格只决定管理端怎么回显(倒计时要显示"还剩 2 小时",时间范围要显示
	// 两个日期)。判定与回显分开是刻意的:让判定去认 mode,就得在热路径上
	// 为每种形态各写一条分支,而它们本来就是同一件事。
	WindowMode string `json:"window_mode" gorm:"type:varchar(16);not null;default:''"`

	// StartsAt 是生效起点(unix 秒),0 = 立即生效。
	StartsAt int64 `json:"starts_at" gorm:"not null;default:0"`

	// EndsAt 是失效时刻(unix 秒),0 = 永久监听。
	EndsAt int64 `json:"ends_at" gorm:"not null;default:0"`

	// CountdownSeconds 是倒计时形态下管理员填的那个秒数,只为回显与"重新开始"。
	CountdownSeconds int `json:"countdown_seconds" gorm:"not null;default:0"`

	// RetentionDays 是本任务记录的保留天数。nil = 跟随全局默认,0 = 永久保留。
	//
	// 用指针是因为这一格有**三个**语义值,而 int 只能表达两个:
	// "没填"(跟着 risk_watch.retention_days 走)与"填了 0"(这份取证材料要
	// 一直留着,通常是要跟着申诉或仲裁走完)在业务上完全不同,而它们在 int 上
	// 是同一个字节。本仓在 commission.holding_days 上栽过一次同形状的跟头。
	RetentionDays *int `json:"retention_days"`

	// Status 是任务状态:running / stopped / finished / expired。
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'';index:,composite:status"`

	// StoppedAt / StoppedReason 记录它是怎么停的。
	//
	// 停止原因必须落库而不是让人从 captured 与 ends_at 反推:一个
	// "抽满 500 条"与一个"到点了才抓到 37 条"在事后是完全不同的结论,
	// 而前者往往意味着这个账号的行为比预期密集得多。
	StoppedAt     int64  `json:"stopped_at" gorm:"not null;default:0"`
	StoppedReason string `json:"stopped_reason" gorm:"type:varchar(32);not null;default:''"`

	// CreatedBy 是立案的管理员 id。审计表里也有一份,这里冗余是为了列表页能直接显示。
	CreatedBy int `json:"created_by" gorm:"not null;default:0"`

	// Version 是乐观锁。两个管理员同时编辑同一个任务时,后提交的那次会 409
	// 而不是静默覆盖 —— 被覆盖掉的可能正是"把概率从 100% 调回 1%"那一次。
	Version int `json:"version" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Task) TableName() string { return "qy_riskwatch_task" }

// Capture 是一条监听记录。
//
// 这张表是整个功能的产出,也是它必须住在单独存储节点上的全部理由:行数由
// 「概率 × 作用域内的请求量 × 时长」决定,而这三个数都是管理员在页面上现填的。
type Capture struct {
	Id     int64 `json:"id" gorm:"primaryKey"`
	TaskId int64 `json:"task_id" gorm:"not null;default:0;index:,composite:task,priority:1"`

	UserId   int    `json:"user_id" gorm:"not null;default:0;index:,composite:user,priority:1"`
	Username string `json:"username" gorm:"type:varchar(64);not null;default:''"`

	// TokenId / TokenName 指出是哪一把密钥发的。
	//
	// 它是取证里最有用的一格之一:同一个账号下的多把密钥常常对应不同的用途
	// (自己用的、卖出去的、挂在某个客户端里的),而"哪一把在刷"决定了处置面。
	TokenId   int    `json:"token_id" gorm:"not null;default:0"`
	TokenName string `json:"token_name" gorm:"type:varchar(64);not null;default:''"`

	UserGroup string `json:"user_group" gorm:"column:user_group;type:varchar(64);not null;default:''"`
	ModelName string `json:"model_name" gorm:"column:model_name;type:varchar(128);not null;default:''"`

	// RequestId 是上游那一次请求的 id,用来与使用日志、违规记录对上。
	RequestId string `json:"request_id" gorm:"type:varchar(64);not null;default:''"`

	ClientIP string `json:"client_ip" gorm:"type:varchar(64);not null;default:''"`

	IsStream bool `json:"is_stream" gorm:"not null"`

	// PromptTokens 是转发前的**预估**输入 token 数。
	//
	// 刻意不叫 tokens:这一刻真实用量还不存在(上游还没回),把预估值放在一个
	// 看起来像结算值的字段名下,迟早有人拿它去对账。
	PromptTokens int `json:"prompt_tokens" gorm:"not null;default:0"`

	// Content 是归一化后的上下文正文,已剥离内联二进制并按上限截断。
	//
	// 用 text 而不是 varchar:上限由 risk_watch.capture_max_chars 决定,运营
	// 可以把它调到几万字符,而 MySQL 的行长上限会让一个足够大的 varchar 在
	// 建表那一刻就失败。
	Content string `json:"content" gorm:"type:text"`

	// ContentChars 是截断**之前**的字符数,Truncated 表示这一条被截过。
	//
	// 两格都要:只看 Content 的长度分不出"这个人就问了三个字"与"上限设得太小"。
	ContentChars int  `json:"content_chars" gorm:"not null;default:0"`
	Truncated    bool `json:"truncated" gorm:"not null"`

	// Files 是多模态输入的描述符 JSON 数组(MIME / 字节数 / SHA256),**不含
	// 二进制本体**。
	//
	// 与违规证据同一条口径:base64 图片一律不入库(一条就能有 10 MB,而且
	// 某些类别的违规图片留存本身可能违法),但哈希留着 —— 同一张图被多个账号
	// 反复上传时,靠哈希就能识别,完全不必保存图片本体。
	Files string `json:"files" gorm:"type:text"`

	// CreatedAt 同时是两条复合索引的第二列。
	//
	// 列表的排序键是 (created_at desc, id desc) 而不是单纯的 id desc,正是为了
	// 让这两条索引直接服务于筛选 + 排序:PostgreSQL 的二级索引**不**隐含主键
	// (InnoDB 隐含),只按 task_id 建索引的话,一个抓了几十万条的任务翻第一页
	// 也要先把全部命中行取出来再排一次。两列都要,而 id 兜住同 created_at
	// 那一秒内的全序。
	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:,composite:task,priority:2;index:,composite:user,priority:2"`

	// ExpiresAt 是这一行的清理时刻(unix 秒),0 = 永久保留。
	//
	// 保留期在**落库那一刻**折算成绝对时间戳,而不是让清理任务每轮回去读任务的
	// retention_days:后者要么跨表 JOIN(在这个库里能做,但会让清理从一条带索引
	// 的范围删除变成全表扫描),要么把任务表整个读进内存。改任务保留期时由
	// 管理端接口一次性重写这一列(见 api_admin.go 的 rewriteExpiry),
	// 那是一次有边界的、管理员触发的操作。
	ExpiresAt int64 `json:"expires_at" gorm:"not null;default:0;index:,composite:expiry"`
}

func (Capture) TableName() string { return "qy_riskwatch_capture" }

// 任务状态。取值同时是接口下发给前端的字符串,改动即接口变更。
const (
	// StatusRunning 生效中。它是唯一会被热路径看见的状态。
	StatusRunning = "running"
	// StatusStopped 管理员手动停止。窗口还没过时可以再启动。
	StatusStopped = "stopped"
	// StatusFinished 抽满了 max_records 自动停止。
	StatusFinished = "finished"
	// StatusExpired 时间窗结束自动停止。
	StatusExpired = "expired"
)

// 停止原因。与状态分开是因为「谁停的」和「现在什么状态」不是一件事:
// 一个 stopped 的任务可能是管理员停的,也可能是它被删掉的目标用户带停的。
const (
	ReasonManual     = "manual"
	ReasonMaxRecords = "max_records"
	ReasonWindowEnd  = "window_end"
	// ReasonGroupGone 是「目标分组被删了」。它必须与 manual 分开:一个被
	// 分组删除带停的任务,管理员并没有决定停止它 —— 他要能一眼认出这一批,
	// 改绑分组后重新启动(见 residue.go)。
	ReasonGroupGone = "group_deleted"
)

// 时间窗形态。
const (
	WindowForever   = "forever"
	WindowRange     = "range"
	WindowCountdown = "countdown"
)
