// Package lottery 实现娱乐功能:抽奖(kind=draw)与竞猜(kind=guess)。
//
// # 为什么是一个模块而不是两个
//
// 两者的资格引擎、扣费链路、名单冻结、逐笔派奖状态机、证据链、退款路径完全同构,
// 唯一差别是"谁是赢家"这个纯函数。拆两个模块等于把最危险的资金代码抄两遍,
// 还要改两次 qianye/model/fund_order.go。
//
// # 开奖机制:承诺-揭示(commit-reveal)
//
// 创建活动时生成随机种子,发布时只公布种子的哈希;开奖后公开种子原文,
// 任何人都能自己算一遍验证中奖名单。它保证的是**不可抵赖地被检出**,
// 不是物理不可篡改 —— 这条边界在 api_proof.go 的对抗分析里写清楚了,
// 也必须原样出现在用户可见的活动规则页上。
//
// # 资金铁律(违反即资损,配 fund_guard_test.go 的 AST 断言)
//
//   - 禁止 model.DecreaseUserQuota:它直接 UPDATE quota = quota - ?,既无
//     WHERE quota >= ?,也不检查 RowsAffected,会把余额扣成负数;且在
//     BatchUpdateEnabled 下只进内存批量队列,钱什么时候落库不可确定。
//   - 禁止 model.IncreaseUserQuota:同上,且无溢出校验。加款一律带上限条件
//     `WHERE id=? AND quota <= (MaxQuota - amount)` —— users.quota 是 int32
//     且上游全无溢出校验。
//   - 禁止 tx.Save(&User{})(全字段覆盖冲掉并发变更)、禁止 user.Update() 与
//     IncrementUserAuthVersion(会吊销用户全部会话)。
//   - 扣款一律:model.QyLockForUpdate 取行锁 → 锁内复检 status/group →
//     `UPDATE users SET quota=quota-? WHERE id=? AND quota>=?` 并断言
//     RowsAffected==1。SQLite 下行锁是空操作,CAS 条件才是唯一正确性保障。
//   - 金额一律 int64 + shopspring/decimal;转换只用 common.QuotaFromDecimal /
//     QuotaFromFloat / QuotaRound,禁止裸 int() 与 float64。
//   - ctx 一律 guard.ColdContext(context.Background()),不用
//     c.Request.Context() —— 客户端断连不该中断已在动钱的操作。
//
// # 证据表永不清理
//
// activity / seed / prize / option / entry / payout / event 七张表显式排除在
// 所有 Prune 之外,StartTasks 里不注册任何针对它们的清理任务。
// qy_fund_orders 与全局审计的保留期不归本模块管,因此证据链绝不能依赖它们 ——
// 一切必须在本模块的表里自洽。
package lottery

// ─────────────────────────── 活动 ───────────────────────────

// Activity 是活动主表,抽奖与竞猜共用。永不清理。
//
// Status 是**单向线性**的:draft → published → locked → settling → finished,
// 没有回退边。取消/流局/全错三种结局都复用同一条 settling→finished 的退款路径,
// 由 Outcome 承载差异,而不是各建一个状态 —— 状态机每多一个分支,
// "这一步之后还能不能动钱"就多一次要人去推理的地方。
type Activity struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// ActNo 由 crypto/rand 生成。禁止时间戳自增:可预测等于可枚举,
	// 而活动号是证据链里所有哈希的域分隔量之一。
	ActNo string `json:"act_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_lot_act_no"`
	Kind  string `json:"kind" gorm:"type:varchar(8);not null;index:idx_qy_lot_act_pick,priority:1"`

	Status  string `json:"status" gorm:"type:varchar(16);not null;index:idx_qy_lot_act_tick,priority:1;index:idx_qy_lot_act_draw,priority:1;index:idx_qy_lot_act_pick,priority:2"`
	Outcome string `json:"outcome" gorm:"type:varchar(24);not null;default:''"`

	Title string `json:"title" gorm:"type:varchar(120);not null;default:''"`
	// Intro 是纯文本,前端不渲染 HTML。
	Intro string `json:"intro" gorm:"type:text"`

	// ── 卡片背景图(封面)──
	//
	// 两种来源,**互斥**,至多一个非空。两列而不是一列 varchar 加前缀:
	// 它们的校验与生命周期根本不同 —— CoverUrl 是一个只做形状校验的自由字符串,
	// CoverRef 是指向 qy_lot_covers 的引用,背后挂着磁盘上的一个文件和一整套
	// 孤儿回收。挤进一列意味着每一次读都要先解析一次前缀,而一个恰好长得像
	// 引用的外链就会被送进错误的那条分支。
	//
	// **封面不进任何哈希原像**(commit/rules/spec 全都不含它),因此它是活动
	// 发布之后仍然可改的极少数字段之一 —— 一个 404 的封面在已发布活动上必须
	// 能被修好,而奖档、时刻、条件永远不能。这条取舍写在管理端的换图弹窗上。
	//
	// CoverUrl 只接受 http/https,且**服务端从不去拉取它**(浏览器直接加载),
	// 所以这条路上没有 SSRF 面。理由与判定见 cover.go。
	CoverUrl string `json:"cover_url" gorm:"type:varchar(500);not null;default:''"`
	// CoverRef 指向 qy_lot_covers.ref。图片本体在本地磁盘上,本列只是指针 ——
	// 绝不在这里存 base64:大厅列表是抽奖的首屏,每一次分页查询都会把
	// 那几百 KB 的字符串一起读出来、序列化、压缩、发出去。
	CoverRef string `json:"cover_ref" gorm:"type:varchar(64);not null;default:''"`

	// StakeQuota 强制 > 0。免费场在 v1 明确不做:twophase 的入口校验强制
	// 0 < amount ≤ MaxQuota,0 元要另开一条不动钱的路径 = 第二套状态机 +
	// 第二套幂等 + 第二套补偿,而需求原文就是"用户花费多少余额参与抽奖"。
	StakeQuota int64 `json:"stake_quota" gorm:"not null;default:0"`
	// BetMinQuota / BetMaxQuota 是竞猜单注上下限,0 = 不限。
	// 不设上限时一个大户可以在封盘前几秒压满获胜选项吃掉奖池,散户期望收益归零;
	// 不设下限则会有 1 单位的骚扰投注刷名单。
	BetMinQuota int64 `json:"bet_min_quota" gorm:"not null;default:0"`
	BetMaxQuota int64 `json:"bet_max_quota" gorm:"not null;default:0"`

	// 四个时刻在 publish 之后不可改,全部进承诺哈希。
	OpenAt         int64 `json:"open_at" gorm:"not null;default:0"`
	CloseAt        int64 `json:"close_at" gorm:"not null;default:0;index:idx_qy_lot_act_tick,priority:2"`
	DrawAt         int64 `json:"draw_at" gorm:"not null;default:0;index:idx_qy_lot_act_draw,priority:2"`
	SettleDeadline int64 `json:"settle_deadline" gorm:"not null;default:0"`

	// RulesText / SpecText 是规范化后的参与条件与奖档/选项集合。
	// 哈希的就是落库的这份字节,绝不读出来重序列化 —— Go struct 的序列化
	// 顺序与第三方不一致,重序列化一次就会让所有外部验证者算出不同的哈希。
	RulesText string `json:"rules_text" gorm:"type:text"`
	RulesHash string `json:"rules_hash" gorm:"type:varchar(64);not null;default:''"`
	SpecText  string `json:"spec_text" gorm:"type:text"`
	SpecHash  string `json:"spec_hash" gorm:"type:varchar(64);not null;default:''"`

	// CommitHash 在 publish 时写入,此后只读。
	CommitHash string `json:"commit_hash" gorm:"type:varchar(64);not null;default:''"`
	Algo       string `json:"algo" gorm:"type:varchar(24);not null;default:''"`

	// DrawMode 是抽奖的定档方式:rank(按名次抽 N 个)/ prob(按公示概率摇号)
	// / ball(双色球)。竞猜恒为空串。
	//
	// **刻意不新增 kind**:runReveal 按 kind='draw' 扫、runVoidExpired 按
	// kind='guess' 扫、entry.go 在两个 kind 上分支、资格与扣费完全不看 kind ——
	// 新增一个 kind 要在这四处各补一个分支,每漏一处就是一条静默死路。
	// 加一列 draw_mode 则整条生命周期一行都不用改。
	DrawMode string `json:"draw_mode" gorm:"type:varchar(8);not null;default:''"`
	// TextGrantCount 是开奖那一刻算出的文本奖中奖位数,与 payout 计划在**同一个
	// 事务**里写入。对账用它做 O(1) 的"文本奖有没有被删掉一行"判定 ——
	// 没有它就只能在对账里重跑一遍抽签,而那是每分钟一次的任务。
	TextGrantCount int `json:"text_grant_count" gorm:"not null;default:0"`

	// ── 双色球期次(draw_mode=ball 专用,其余模式恒为零值)──
	//
	// 这一整段与 SeriesSnapshot 一一对应,**逐个进 commit 原像**:非 ball 活动
	// 也占着这些位,少一个占位就等于允许管理员把一场普通抽奖悄悄改成双色球。
	//
	// SeriesNo 在活动行上冗余一份而不是每次 JOIN 系列表:承诺原像必须能在
	// 只有活动行 + 种子的情况下算出来,而系列表是可以被改名的。
	SeriesId int64  `json:"series_id" gorm:"not null;default:0;index:idx_qy_lot_act_series,priority:1"`
	SeriesNo string `json:"series_no" gorm:"type:varchar(32);not null;default:''"`
	IssueNo  int    `json:"issue_no" gorm:"not null;default:0;index:idx_qy_lot_act_series,priority:2"`

	// PoolCarryQuota 是本期开局从系列池取走的**滚存**(上期未派出的部分),
	// PoolSeedQuota 是其中属于本期新注资的部分,两者之和 = PoolOpenQuota。
	// 三个数都在 publish 那一刻从系列行上原子取走并冻结进承诺。
	//
	// 注意 PoolOpenQuota 是**开局基数**,不含本期投注的入池部分:后者要到封盘
	// 才定得下来,而承诺必须在发布时就写死。开奖时真正可派发的池子是
	// PoolOpenQuota + floor(PoolQuota × PoolShareBps / 10000),验证脚本按同一个
	// 式子从公开数据复算。
	PoolSeedQuota  int64 `json:"pool_seed_quota" gorm:"not null;default:0"`
	PoolCarryQuota int64 `json:"pool_carry_quota" gorm:"not null;default:0"`
	PoolOpenQuota  int64 `json:"pool_open_quota" gorm:"not null;default:0"`
	// PoolShareBps 是本期投注额进入奖池的万分比,其余部分是平台手续费。
	// 滚存与注资**零费率** —— 否则平台会反复抽自己的种子钱,池子在一次都没派
	// 出去之前就被费率蚕食干净。
	PoolShareBps int `json:"pool_share_bps" gorm:"not null;default:0"`

	// 号池四元组在 series 上定死、期与期之间不可变(这样中奖率可比,运营也不能
	// 靠调号码空间悄悄改概率),同时进每期的 commit 原像。
	BallRedPool  int `json:"ball_red_pool" gorm:"not null;default:0"`
	BallRedPick  int `json:"ball_red_pick" gorm:"not null;default:0"`
	BallBluePool int `json:"ball_blue_pool" gorm:"not null;default:0"`
	BallBluePick int `json:"ball_blue_pick" gorm:"not null;default:0"`
	// BallResult 是开奖号的规范化串(与选号同格式),开奖那一刻写入,此后只读。
	// 它是**结果**而不是承诺的一部分:任何人都能用公开的种子自己摇一遍。
	BallResult string `json:"ball_result" gorm:"type:varchar(64);not null;default:''"`

	// AllowMultiWin 刻意不用 gorm default 标签:布尔默认值在 MySQL 与
	// PostgreSQL 上会被 AutoMigrate 反复 ALTER。业务默认值在请求归一化时给。
	AllowMultiWin bool `json:"allow_multi_win" gorm:"not null"`
	// FeeBps 是竞猜手续费万分比,publish 时冻结。
	FeeBps int `json:"fee_bps" gorm:"not null;default:0"`
	// MinEntriesToHold 是平台侧唯一的止损阀,且对用户完全公平:
	// 不足即流局全额退款。没有它,抽奖会出现"3 个人参加、平台净亏一个一等奖",
	// 竞猜会出现"2 个人对赌"的荒诞局面。
	MinEntriesToHold int `json:"min_entries_to_hold" gorm:"not null;default:0"`

	MaxEntriesPerUser  int `json:"max_entries_per_user" gorm:"not null;default:0"`
	MaxAttemptsPerUser int `json:"max_attempts_per_user" gorm:"not null;default:0"`
	MaxTotalEntries    int `json:"max_total_entries" gorm:"not null;default:0"`
	MaxTotalUsers      int `json:"max_total_users" gorm:"not null;default:0"`
	MaxPerInviter      int `json:"max_per_inviter" gorm:"not null;default:0"`
	CooldownSeconds    int `json:"cooldown_seconds" gorm:"not null;default:0"`
	// DedupIp 默认关闭。它会误伤家庭/公司/校园/运营商 NAT 共用出口的真实用户,
	// 而 c.ClientIP() 在反代未正确配置 trusted proxies 时可被 X-Forwarded-For
	// 伪造 —— 防御方向恰好反了。管理端表单必须写清这个代价。
	DedupIp bool `json:"dedup_ip" gorm:"not null"`

	// EntrySeq 是已分配的最大序号(含失败条目),哈希链顺序的唯一权威。
	// ChainHead 是最后一条 entry 的 chain_hash,即下一条的 prev。
	// 两者与全场计数同在这一行:一把活动行锁全解决,不需要额外的计数表。
	EntrySeq  int    `json:"entry_seq" gorm:"not null;default:0"`
	ChainHead string `json:"chain_head" gorm:"type:varchar(64);not null;default:''"`

	ActiveCount  int   `json:"active_count" gorm:"not null;default:0"`
	PendingCount int   `json:"pending_count" gorm:"not null;default:0"`
	PoolQuota    int64 `json:"pool_quota" gorm:"not null;default:0"`

	// RosterHash / RosterCount 在封盘时落库并**先于种子公开**。
	// 这个先后顺序是整个协议的关键。
	RosterHash  string `json:"roster_hash" gorm:"type:varchar(64);not null;default:''"`
	RosterCount int    `json:"roster_count" gorm:"not null;default:0"`

	// 结算三口径,管理端"本场收支"直读。
	PlatformFeeQuota int64 `json:"platform_fee_quota" gorm:"not null;default:0"`
	PayoutQuota      int64 `json:"payout_quota" gorm:"not null;default:0"`
	RefundQuota      int64 `json:"refund_quota" gorm:"not null;default:0"`

	// 竞猜结果一经写入不可改。录错只能整场作废 + 全额退款 + 公示。
	WinOptionId    int64  `json:"win_option_id" gorm:"not null;default:0"`
	ResultEvidence string `json:"result_evidence" gorm:"type:varchar(1000);not null;default:''"`
	ResultBy       int    `json:"result_by" gorm:"not null;default:0"`

	CancelReason string `json:"cancel_reason" gorm:"type:varchar(255);not null;default:''"`
	CreatedBy    int    `json:"created_by" gorm:"not null;default:0"`

	// ── 下架(「关闭」)──
	//
	// HiddenAt > 0 表示这一场已从用户端的活动大厅撤下。它与 Outcome/Status
	// **完全正交**:下架一个字节的资金都不动、一条记录都不删、随时可撤回,
	// 与 cancel(整场取消 → 必然全额退款 → 不可逆)是两件事。
	//
	// 刻意不做成 status 的一个新取值:Status 是单向线性的状态机,而下架可逆;
	// 塞进去就等于给状态机加了一条回退边,而"这一步之后还能不能动钱"
	// 从此要多推理一层。
	//
	// 下架只遮住**大厅列表**,活动详情、我的参与、匿名证据链一律照常可达 ——
	// 一场活动的公正性一旦公布过就不能被运营收回,而参与过的人必须还能查到
	// 自己那一票。这条取舍写在管理端的下架弹窗上。
	HiddenAt int64 `json:"hidden_at" gorm:"not null;default:0"`
	HiddenBy int   `json:"hidden_by" gorm:"not null;default:0"`
	// HiddenReason 只对管理员可见(它不进任何用户侧接口),但必填:
	// 一次没有理由的下架,事后无法与"结果不好看所以藏起来"区分开。
	HiddenReason string `json:"hidden_reason" gorm:"type:varchar(255);not null;default:''"`

	CreatedAt   int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_lot_act_pick,priority:3"`
	UpdatedAt   int64 `json:"updated_at" gorm:"not null;default:0"`
	PublishedAt int64 `json:"published_at" gorm:"not null;default:0"`
	LockedAt    int64 `json:"locked_at" gorm:"not null;default:0"`
	RevealedAt  int64 `json:"revealed_at" gorm:"not null;default:0"`
	SettledAt   int64 `json:"settled_at" gorm:"not null;default:0"`
}

func (Activity) TableName() string { return "qy_lot_activity" }

// 活动类型。
const (
	KindDraw  = "draw"
	KindGuess = "guess"
)

// 活动状态。单向线性,没有回退边。
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusLocked    = "locked"
	StatusSettling  = "settling"
	StatusFinished  = "finished"
)

// 活动结局。与状态分开:三种"没有正常开出结果"的收场共用同一条退款路径。
const (
	OutcomeNone           = ""
	OutcomeDrawn          = "drawn"
	OutcomeCancelled      = "cancelled"
	OutcomeVoidMinEntries = "void_min_entries"
	OutcomeVoidNoWinner   = "void_no_winner"
	OutcomeVoidAllCorrect = "void_all_correct"
	OutcomeVoidDeadline   = "void_deadline"
)

// AlgoV1 / AlgoV2 是公正性协议的版本号。原像改一个字节就是不兼容变更,必须升版本。
//
// 新活动一律写 lot-v2;存量活动的 algo 列保持 lot-v1 且**永不回填** ——
// 回填会让所有已开完活动的历史公正查询集体变成 FAIL,而那正是这套系统
// 唯一不能出的事。
const (
	AlgoV1 = "lot-v1"
	AlgoV2 = "lot-v2"
)

// 抽奖的定档方式。
//
// rank 与 prob 是两套语义:rank 是"从名单里抽前 N 个"(随机量的作用域是全场,
// 一张票的名次取决于其他所有票),prob 是"每张票各摇一次、按公示概率定档"
// (作用域是单张票,结果完全不依赖别人)。共用同一个随机源与同一份票面推导,
// 区别只在读票面的那把尺子。
const (
	DrawModeRank = "rank"
	DrawModeProb = "prob"
	DrawModeBall = "ball"
)

// 奖品类型。单选,不允许一档既给额度又给文本 —— 混合会让派奖在同一行里分叉,
// 要两者就配两档。
const (
	PrizeTypeQuota = "quota"
	PrizeTypeText  = "text"
)

// NoWinnerPolicy 是"全部猜错"的口径,写死为全额退回、手续费一分不收。
//
// 它进 commit_hash 原像、进 proof JSON、写在活动规则页 —— 否则事后无论怎么
// 处理都会被指控临时改规则。刻意不做成配置项:只要平台在"没人猜中"时有收益,
// 平台就有动机去设置不可能达成的选项,而这个漏洞任何审计都补不上。
// 把一个有害口径做成开关,等于把决策推给未来某个赶时间的运营。
const NoWinnerPolicy = "refund_all"

// ─────────────────────────── 种子保险柜 ───────────────────────────

// Seed 是种子保险柜。
//
// 单独成表是核心防御,而不是给 Activity 加一个 json:"-" 列:管理端整行返回
// model 的先例在本仓已经存在,而 json:"-" 只挡 JSON 这一条路。
// 没被 SELECT 出来的东西才泄不了。
//
// 硬规则:全字段 json:"-";包内只有 loadSeedForReveal 与 newSeed 两个函数
// 触碰 Seed 字段;审计快照一律显式挑字段构造,绝不整行序列化;禁止 %+v 打印。
// 由 seed_guard_test.go 的 AST 断言守住。永不清理。
type Seed struct {
	ActId int64 `json:"-" gorm:"primaryKey;autoIncrement:false"`
	// Seed 是 hex(32 字节 crypto/rand)。
	Seed string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// RefSalt 是 user_ref 的每活动盐,**永不公开、且刻意不进 commit 原像**。
	//
	// 若它进原像:不揭示就没人能复算 commit_hash,揭示了就等于公开盐,
	// 而 user_id 空间只有几万到几十万,盐一公开就能全量枚举反查全部参与者身份。
	// 代价是"同一个人的票确实标了同一个 user_ref"对外部验证者不可证 ——
	// 它只影响 allow_multi_win=false 这一条约束,不影响每张票中签概率严格相等。
	RefSalt string `json:"-" gorm:"type:varchar(64);not null;default:''"`
	// IpSalt 用于 IP 去重的 HMAC。IPv4 只有 2^32,无盐哈希可被完整彩虹表反查。
	IpSalt string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	CreatedAt  int64 `json:"-" gorm:"not null;default:0"`
	RevealedAt int64 `json:"-" gorm:"not null;default:0"`
}

func (Seed) TableName() string { return "qy_lot_seed" }

// ─────────────────────────── 奖档与选项 ───────────────────────────

// Prize 是抽奖的奖档。永不清理。
type Prize struct {
	Id    int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	ActId int64 `json:"act_id" gorm:"not null;uniqueIndex:uk_qy_lot_prize,priority:1"`
	// Tier 决定切片顺序,1 = 一等奖。
	Tier        int    `json:"tier" gorm:"not null;uniqueIndex:uk_qy_lot_prize,priority:2"`
	Name        string `json:"name" gorm:"type:varchar(80);not null;default:''"`
	AmountQuota int64  `json:"amount_quota" gorm:"not null;default:0"`
	// Count 在 rank 模式下是硬名额;在 prob 模式下是**本档预算的份数**
	// (预算 = Count × AmountQuota),超募时该预算由全部中签者均分。
	// 语义随模式变化是刻意的:唯有如此,公示的中奖概率才在任何人数下都为真。
	Count int `json:"count" gorm:"not null;default:0"`

	// PrizeType 是 quota 或 text。空串按 quota 处理(存量行)。
	PrizeType string `json:"prize_type" gorm:"type:varchar(8);not null;default:''"`
	// WinPpm 是本档的中奖概率(百万分比)。rank 模式下恒为 0。
	// 它进 spec 原像 → spec_hash → commit_hash,发布之后改一个数字就开不了奖。
	WinPpm int `json:"win_ppm" gorm:"not null;default:0"`
	// TextDesc 是文本奖的**公开**履行说明(如"请在 8 月 31 日前联系客服领取")。
	//
	// 它与 Name 同级,明文进 spec 原像 —— 它本来就要展示给所有人看,
	// 不需要哈希、也不需要盐。**绝不能把兑换码写在这里**:管理端表单上
	// 常驻一条警告,而真正的兑换码走 Payout 上那三列私密字段。
	TextDesc string `json:"text_desc" gorm:"type:varchar(500);not null;default:''"`

	// RedMatch / BlueMatch / PoolShareBps 是双色球奖级的定档条件与池子份额,
	// 由双色球那一路填。非 ball 模式恒为 0,但它们**必须出现在 spec 原像里**。
	RedMatch     int `json:"red_match" gorm:"not null;default:0"`
	BlueMatch    int `json:"blue_match" gorm:"not null;default:0"`
	PoolShareBps int `json:"pool_share_bps" gorm:"not null;default:0"`
}

// Type 返回归一化后的奖品类型:空串(存量行)按额度处理。
func (p Prize) Type() string {
	if p.PrizeType == PrizeTypeText {
		return PrizeTypeText
	}
	return PrizeTypeQuota
}

func (Prize) TableName() string { return "qy_lot_prize" }

// Option 是竞猜的选项。永不清理。
type Option struct {
	Id    int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	ActId int64 `json:"act_id" gorm:"not null;uniqueIndex:uk_qy_lot_opt,priority:1"`
	// OptNo 是对外稳定编号。进哈希的是它,不是自增 id —— 自增 id 跨环境不稳定,
	// 而证据链必须能在任何一份数据库副本上算出同一个结果。
	OptNo int    `json:"opt_no" gorm:"not null;uniqueIndex:uk_qy_lot_opt,priority:2"`
	Label string `json:"label" gorm:"type:varchar(80);not null;default:''"`
	// IsCatchAll 标记"以上都不是"兜底项。没有它,"全部猜错"会频繁发生,
	// 届时全场退款、平台零收益 —— 删除它要在管理端二次确认并明写这个代价。
	IsCatchAll bool `json:"is_catch_all" gorm:"not null"`

	BetQuota int64 `json:"bet_quota" gorm:"not null;default:0"`
	BetCount int   `json:"bet_count" gorm:"not null;default:0"`
	IsWinner bool  `json:"is_winner" gorm:"not null"`
}

func (Option) TableName() string { return "qy_lot_option" }

// ─────────────────────────── 参与明细 ───────────────────────────

// Entry 是参与/投注明细。**只进不出**:没有用户退出、没有管理员删单,
// 取消只能整场。这是 grinding 防御的基石 —— 每次试探都要真扣一笔费用
// 并在公开名单里永久留一条记录。永不清理。
type Entry struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// EntryNo 是用户手里的凭据、票号,也是公开名单的排序键。crypto/rand 生成,
	// 用户不能自选号码。
	EntryNo string `json:"entry_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_lot_entry_no"`

	ActId   int64  `json:"act_id" gorm:"not null;uniqueIndex:uk_qy_lot_entry_idem,priority:1;uniqueIndex:uk_qy_lot_entry_seq,priority:1;index:idx_qy_lot_entry_scan,priority:1;index:idx_qy_lot_entry_user,priority:1;index:idx_qy_lot_entry_inv,priority:1;index:idx_qy_lot_entry_ip,priority:1"`
	IdemKey string `json:"-" gorm:"type:varchar(96);not null;default:'';uniqueIndex:uk_qy_lot_entry_idem,priority:2"`
	// Seq 是活动内单调序号,**含失败条目**。uk(act_id, seq) 由数据库强制链的
	// 顺序完整 —— 失败条目永久留在链上占一个 seq,删除即破链。
	Seq int `json:"seq" gorm:"not null;default:0;uniqueIndex:uk_qy_lot_entry_seq,priority:2"`

	UserId int `json:"user_id" gorm:"not null;index:idx_qy_lot_entry_user,priority:2;index:idx_qy_lot_entry_my,priority:1"`
	// Username 是快照:主库可改名、可软删,跨库不能 JOIN。
	Username string `json:"username" gorm:"type:varchar(64);not null;default:''"`
	// InviterId 在报名瞬间从主库快照,供"同一邀请人名下最多 N 个下线"判定。
	InviterId int `json:"inviter_id" gorm:"not null;default:0;index:idx_qy_lot_entry_inv,priority:2"`
	// UserRef 是公开名单里的脱敏稳定标识(每活动加盐)。
	UserRef string `json:"user_ref" gorm:"type:varchar(32);not null;default:''"`

	OptionId int64 `json:"option_id" gorm:"not null;default:0"`
	OptNo    int   `json:"opt_no" gorm:"not null;default:0"`
	Amount   int64 `json:"amount" gorm:"not null;default:0"`

	// Pick 是双色球的选号(规范化格式,如 "03,05,12|08")。非 ball 活动恒为空串。
	//
	// 它进 lot-v2 的链原像与名单原像两处 —— 否则平台可以在开奖之后把某个人的号
	// 改成中奖号,而链尾、条目计数、名单重算三道校验会照常全部通过。
	Pick string `json:"pick" gorm:"type:varchar(64);not null;default:''"`

	Status string `json:"status" gorm:"type:varchar(12);not null;index:idx_qy_lot_entry_scan,priority:2;index:idx_qy_lot_entry_user,priority:3"`
	// OrderNo 是 twophase 资金单号,跨库对账锚点。
	OrderNo string `json:"order_no" gorm:"type:varchar(64);not null;default:''"`

	PrevHash  string `json:"prev_hash" gorm:"type:varchar(64);not null;default:''"`
	ChainHash string `json:"chain_hash" gorm:"type:varchar(64);not null;default:''"`

	// EligibilitySnapshot 是报名那一刻的判定输入(分组/余额/累计消费/近 N 日
	// 消费/注册秒数/违规计数)。主库 users 无历史版本,这是争议仲裁的唯一凭据。
	// 不下发给任何非管理员接口。
	EligibilitySnapshot string `json:"-" gorm:"type:text"`

	QuotaBefore int64 `json:"quota_before" gorm:"not null;default:0"`
	QuotaAfter  int64 `json:"quota_after" gorm:"not null;default:0"`

	// IpHash / UaHash 是 HMAC(每活动盐),不落明文。明文在 qy_request_audits
	// 里已有一份,这里再存一份只是多一个泄漏面。
	IpHash string `json:"-" gorm:"type:varchar(64);not null;default:'';index:idx_qy_lot_entry_ip,priority:2"`
	UaHash string `json:"-" gorm:"type:varchar(64);not null;default:''"`

	FailCode string `json:"fail_code" gorm:"type:varchar(48);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_lot_entry_my,priority:2"`
	SettledAt int64 `json:"settled_at" gorm:"not null;default:0"`
}

func (Entry) TableName() string { return "qy_lot_entry" }

// 参与状态。
//
// excluded 是"封盘时仍未决"的条目:它既不算进有效名单,也不能立刻登记退款 ——
// 那笔资金单可能最终判定为 Failed(主库根本没扣钱),退一笔从没收过的钱
// 会在资金表里留下一条假的成功记录。退款由资金单的终态驱动,见 lifecycle.go。
const (
	EntryPending  = "pending"
	EntrySuccess  = "success"
	EntryFailed   = "failed"
	EntryExcluded = "excluded"
	EntryRefunded = "refunded"
)

// ─────────────────────────── 出款 ───────────────────────────

// Payout 是派奖/赔付/退款的逐笔可重入状态机。
//
// 开奖只落计划(planned),一分钱不动。真正动钱由 worker 逐笔驱动,
// 每一笔都是独立的两阶段资金单 —— 绝不在开奖 handler 里循环 Execute。
// 永不清理。
type Payout struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// PayoutNo 由 crypto/rand 生成,同时是 twophase 的 IdemKey。
	// 出款由服务端发起,幂等键必须是服务端生成的 —— 与报名的取舍方向相反,
	// 因为"谁在重试"不同。
	PayoutNo string `json:"payout_no" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_lot_payout_no"`

	ActId int64 `json:"act_id" gorm:"not null;uniqueIndex:uk_qy_lot_payout_slot,priority:1"`
	// EntryId + Kind 构成"同一张票的同一类出款只可能有一行"的结构性保证。
	// 开奖被重复触发(网络重试 / 两节点同时到 draw_at)时第二遍整体撞唯一键,
	// 不会产生双份计划。
	EntryId int64  `json:"entry_id" gorm:"not null;uniqueIndex:uk_qy_lot_payout_slot,priority:2"`
	Kind    string `json:"kind" gorm:"type:varchar(12);not null;uniqueIndex:uk_qy_lot_payout_slot,priority:3"`

	UserId      int   `json:"user_id" gorm:"not null;index:idx_qy_lot_payout_user,priority:1"`
	Tier        int   `json:"tier" gorm:"not null;default:0"`
	DrawPos     int   `json:"draw_pos" gorm:"not null;default:0"`
	AmountQuota int64 `json:"amount_quota" gorm:"not null;default:0"`

	Status  string `json:"status" gorm:"type:varchar(12);not null;index:idx_qy_lot_payout_drive,priority:1"`
	OrderNo string `json:"order_no" gorm:"type:varchar(64);not null;default:''"`

	// Epoch 是这一笔的**资金单代次**,与 payout_no 一起组成 twophase 的幂等键。
	//
	// 没有它,一笔出款的资金单一旦被判 Failed 就永久失效:重入 Execute 必然幂等
	// 命中原单并返回 ErrOrderFailed,自动重试与管理端「重试」按钮都只是空转,
	// 用户赢来的钱再也发不出去。代次只在**主库探针确认没生效**时才 +1 ——
	// 那是唯一能保证"重开一张单不会重复发钱"的前提。
	Epoch int `json:"epoch" gorm:"not null;default:0"`

	Attempts      int    `json:"attempts" gorm:"not null;default:0"`
	NextAttemptAt int64  `json:"next_attempt_at" gorm:"not null;default:0;index:idx_qy_lot_payout_drive,priority:2"`
	LastError     string `json:"last_error" gorm:"type:varchar(512);not null;default:''"`

	// ── 文本奖的履行标记(kind='text' 专用)──
	//
	// **履行刻意不改 Status**:Status 的语义严格保持"资金终态",绝不让它兼职
	// 表达"人做完了没有"。把 paid 复用成"已履行"会让 finishIfDone 按
	// status=paid 分组求和的那段聚合把 0 元文本行也算进去,语义当场被污染。
	FulfilledAt int64  `json:"fulfilled_at" gorm:"not null;default:0"`
	FulfilledBy int    `json:"fulfilled_by" gorm:"not null;default:0"`
	FulfillNote string `json:"fulfill_note" gorm:"type:varchar(255);not null;default:''"`

	// SecretNonce / SecretCipher / SecretKeyVersion 存放管理员履行时填入的
	// **实际兑换码**。全部 json:"-" —— 与 Seed 同一档纪律。
	//
	// 硬规则(secret_guard_test.go 的 AST 断言守住):
	//   - 包内只有 sealPrizeSecret / openPrizeSecret 两个函数触碰这三列;
	//   - 不进任何列表接口、不进日志、不进 %+v、不进审计快照;
	//   - **不进证据链的任何一层,连哈希都不进**。
	//
	// 为什么连哈希都不进:proof 是匿名可访问的,act_no / tier / 域前缀全部公开,
	// 一个 8 位兑换码约 40 位熵 —— 公开一个可复现原像的哈希等于给了离线爆破面,
	// 与直接公开只差几分钟算力。
	//
	// SecretKeyVersion == 0 表示**明文直存**:启用加密需要一个新的 YAML 配置键,
	// 而配置那几个文件当前被并行工作流占用。列结构一次到位,加密是后续一次
	// backfill 而不是一次迁移。这是一处明确记账的欠债,不是遗漏。
	SecretNonce      []byte `json:"-" gorm:"type:blob"`
	SecretCipher     []byte `json:"-" gorm:"type:blob"`
	SecretKeyVersion int    `json:"-" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_lot_payout_user,priority:2"`
	SettledAt int64 `json:"settled_at" gorm:"not null;default:0"`
}

func (Payout) TableName() string { return "qy_lot_payout" }

// 出款类型。三者对主库做的是同一件事(加额度),因此共用一个资金 Kind。
const (
	PayoutPrize  = "prize"  // 抽奖派奖
	PayoutWin    = "win"    // 竞猜赔付
	PayoutRefund = "refund" // 取消 / 流局 / 全错 / 未决排除的退款
	// PayoutText 是文本奖的中奖位。它**不动钱、不进跨库资金链路**,
	// 一落库就是终态(granted),等管理员手工履行。
	PayoutText = "text"
)

// 出款状态。
//
// held 是"我不知道 / 我不能自动决定,交给人"的出口:重试耗尽、收款人已封禁、
// 资金单不可判定都落在这里。绝不自动放弃、绝不自动改判。
const (
	PayoutPlanned = "planned"
	PayoutPaying  = "paying"
	PayoutPaid    = "paid"
	PayoutFailed  = "failed"
	PayoutHeld    = "held"
	// PayoutGranted 是文本奖落库那一刻就到达的终态。
	//
	// 它**刻意不在** DrivePayouts 与 finishIfDone 的扫描集合里:文本奖不动钱、
	// 不跨库,没有任何东西需要驱动。履行与否由 FulfilledAt 三列单独表达 ——
	// Status 的语义严格保持"资金终态",绝不让它兼职表达"人做完了没有"。
	PayoutGranted = "granted"
)

// ─────────────────────────── 事件流与异常 ───────────────────────────

// Event 是活动状态机的事件流,**对用户可见、属于证据链**。
//
// 与 qianye/service/audit 是两回事:那是跨模块管理台账,可 Prune。
// 两边都要写。永不清理。
type Event struct {
	Id    int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	ActId int64 `json:"act_id" gorm:"not null;index:idx_qy_lot_event,priority:1"`

	FromStatus string `json:"from_status" gorm:"type:varchar(16);not null;default:''"`
	ToStatus   string `json:"to_status" gorm:"type:varchar(16);not null;default:''"`
	Action     string `json:"action" gorm:"type:varchar(40);not null;default:''"`

	ActorType   string `json:"actor_type" gorm:"type:varchar(16);not null;default:''"`
	ActorUserId int    `json:"actor_user_id" gorm:"not null;default:0"`

	// Detail 只放关键哈希与计数,不放种子、不放任何 PII。
	Detail string `json:"detail" gorm:"type:text"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_lot_event,priority:2"`
}

func (Event) TableName() string { return "qy_lot_event" }

// PrizeSecretHist 是文本奖明文的**只增不改**履历。
//
// 存在的唯一理由是一条 unfulfill → fulfill 的路径:撤销履行刻意不清密文
// (用户可能已经用掉了那串码),但再次履行的那条 CAS 只判 fulfilled_at = 0,
// 于是第二串码会把第一串**整列覆盖**。而 Event 与审计快照按设计都不含明文,
// 覆盖之后"用户当初拿到的是哪一串"在系统里彻底消失 —— 那恰恰是撤销这个
// 功能自己承诺要保住的东西。
//
// 纪律与 Payout.Secret* 完全一致:全部 json:"-",不进任何列表接口、不进日志、
// 不进审计快照、**不进证据链的任何一层**。唯一的读出口是写审计的 reveal 接口。
type PrizeSecretHist struct {
	Id int64 `json:"-" gorm:"primaryKey;autoIncrement"`
	// PayoutNo + Seq 唯一:同一笔出款的第 n 次履行只可能归档一行。
	PayoutNo string `json:"-" gorm:"type:varchar(32);not null;uniqueIndex:uk_qy_lot_secret_hist,priority:1"`
	Seq      int    `json:"-" gorm:"not null;uniqueIndex:uk_qy_lot_secret_hist,priority:2"`

	SecretNonce      []byte `json:"-" gorm:"type:blob"`
	SecretCipher     []byte `json:"-" gorm:"type:blob"`
	SecretKeyVersion int    `json:"-" gorm:"not null;default:0"`

	// FulfilledAt / FulfilledBy / FulfillNote 是被顶替掉的那一次履行的账面。
	FulfilledAt  int64  `json:"-" gorm:"not null;default:0"`
	FulfilledBy  int    `json:"-" gorm:"not null;default:0"`
	FulfillNote  string `json:"-" gorm:"type:varchar(255);not null;default:''"`
	SupersededAt int64  `json:"-" gorm:"not null;default:0"`
	SupersededBy int    `json:"-" gorm:"not null;default:0"`
}

func (PrizeSecretHist) TableName() string { return "qy_lot_prize_secret_hist" }

// 事件动作。
const (
	ActionCreate      = "create"
	ActionPublish     = "publish"
	ActionLock        = "lock"
	ActionReveal      = "reveal"
	ActionGuessResult = "guess_result"
	ActionSettle      = "settle"
	ActionCancel      = "cancel"
	ActionVoid        = "void"
	ActionFinish      = "finish"
	// 文本奖的履行履历。Event 是 append-only:误标的痕迹永远在,
	// 消失的只是那个错误的当前态。
	//
	// detail 里**只放 payout_no 与 tier,绝不放任何文本内容** ——
	// 事件流对用户可见,而兑换码只对中奖者本人可见。
	ActionPrizeFulfill   = "prize_fulfill"
	ActionPrizeUnfulfill = "prize_unfulfill"
	ActionPrizeReveal    = "prize_reveal"
)

// SpendDaily 是"近 N 日消费"的唯一数据源。
//
// **本模块唯一允许被清理的表**:它是可从 logs 重建的派生数据,不是证据。
//
// 为什么必须预聚合:logs 可能在独立日志库甚至 ClickHouse 上,无法与 users
// JOIN;它的索引是 (user_id, id) 与 (created_at, type),**没有
// (user_id, created_at) 复合索引** —— 几百万行上按"某用户近 30 天 type=2 的
// SUM(quota)"会退化成按 user_id 取一大段再过滤,而报名是用户点了按钮在等的路径。
type SpendDaily struct {
	UserId int `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	// Day 是 YYYYMMDD。"近 N 日"就是主键上的范围扫描,索引覆盖。
	Day   int32 `json:"day" gorm:"primaryKey;autoIncrement:false;index:idx_qy_lot_spend_day"`
	Quota int64 `json:"quota" gorm:"not null;default:0"`
	Cnt   int   `json:"cnt" gorm:"not null;default:0"`
	// Final 表示该日桶已由重算道确认。增量道只会**少算**消费(方向安全),
	// 重算道修掉它任何可能的跳行。
	Final     bool  `json:"final" gorm:"not null"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (SpendDaily) TableName() string { return "qy_lot_spend_daily" }

// Flag 是对账任务发现的账不平/卡单/名单漂移,管理端红点直读。
type Flag struct {
	Id    int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	ActId int64 `json:"act_id" gorm:"not null;index:idx_qy_lot_flag,priority:1"`

	Code   string `json:"code" gorm:"type:varchar(40);not null;default:''"`
	Detail string `json:"detail" gorm:"type:varchar(512);not null;default:''"`

	Resolved   bool  `json:"resolved" gorm:"not null;index:idx_qy_lot_flag,priority:2"`
	ResolvedBy int   `json:"resolved_by" gorm:"not null;default:0"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;default:0;index:idx_qy_lot_flag,priority:3"`
	ResolvedAt int64 `json:"resolved_at" gorm:"not null;default:0"`
}

func (Flag) TableName() string { return "qy_lot_flag" }

// 异常代码。
const (
	FlagPoolMismatch = "pool_mismatch"
	FlagCountDrift   = "count_drift"
	FlagRosterDrift  = "roster_drift"
	FlagChainDrift   = "chain_drift"
	FlagPayoutStuck  = "payout_stuck"
	FlagOrphanOrder  = "orphan_order"
	FlagEntryStuck   = "entry_stuck"
	FlagRevealRefuse = "reveal_refused"
	// FlagTextGrantMissing 是真事故:有人中了文本奖,但库里没有那一行。
	FlagTextGrantMissing = "text_grant_missing"
	// FlagTextPrizeStale 是运营欠账:文本奖挂了两周还没人履行。
	// 没有它,文本奖会静默烂掉,而用户会以为是抽奖作弊。
	FlagTextPrizeStale = "text_prize_stale"
	// FlagSpecDrift 是"公示的奖档/概率表被事后改过"。
	//
	// 承诺哈希覆盖的是 spec_hash 这个**字符串**,不是奖档表本身:只核
	// commit=H(...spec_hash...) 证明不了 qy_lot_prize 的行没被改。发布之后改一个
	// win_ppm 就等于点名挑中奖者,改一个 amount_quota 就绕过了净增发闸门,
	// 而这两件事原本只有用户自己下载证据链跑脚本才会发现。
	FlagSpecDrift = "spec_drift"
	// FlagRefundDrift 是"退款金额与那笔参与的资金单对不上"。
	//
	// 退款按 qy_lot_entry.amount 出款,而这笔钱**真实**收了多少写在
	// qy_fund_orders.amount_quota 上(entry.order_no 就是锚点)。开奖侧对同一份
	// 证据是核过的(roster_hash 对不上就停手挂起),取消/流局侧原先一次都不核 ——
	// 于是扩展库单表的一次 UPDATE 就能变成主库净增发,而取消恰恰是"出事之后的
	// 止损动作"。现在按资金单的金额出款,并在两者不等时留这条异常。
	FlagRefundDrift = "refund_drift"
)
