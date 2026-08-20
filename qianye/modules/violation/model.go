// Package violation 实现违规检测:转发前提示词拦截、事后上游错误检测扣费、
// 累计计数自动封号、证据留存与误判申诉。
//
// 三条铁律,违反任何一条都会造成全站事故或资损:
//
//  1. 模式绑定在规则上,而且默认影子。一条 `.*` 正则能在 30 秒内封掉全站用户,
//     所以每条规则自带 Mode(shadow / enforce),新建与内置导入一律落 shadow;
//     影子命中除了不扣费不阻断不封号,**也不推进违规计数**(裁决 2)。
//     此外还有一道熔断:拦截率或封号速率越界时,把**全部**规则临时按影子执行
//     (见 breaker.go)—— 那是运行期的自动刹车,不是第二个"模式"。
//  2. 热路径永不阻塞。规则只读进程内快照,所有落库走 guard.HotAsync;
//     扩展库故障一律 fail-open(放行),扩展绝不能成为 relay 的单点故障。
//  3. 扣费金额只走 common.Quota* 转换并强制上限。违规扣费是"惩罚"而不是"计费",
//     配置写错的后果是一次扣光用户余额,因此规则级与全局级两道上限都必须生效。
package violation

import (
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// 规则生效阶段。拆开 Phase 与 MatchType 的理由:同一份词表既可能拦 prompt
// 也可能匹配上游返回,两者执行时机与开销完全不同,合并会让热路径跑无用规则。
const (
	PhasePrompt       = "prompt"        // 转发上游之前(能力 B)
	PhaseUpstreamErr  = "upstream_err"  // 上游返回错误之后(能力 A)
	PhaseRejectReason = "reject_reason" // 上游软违规信号 ContextKeyAdminRejectReason
)

// 匹配方式。
const (
	MatchKeyword      = "keyword"       // 换行分隔词表,走 service.AcSearch(AC 自动机)
	MatchRegex        = "regex"         // Go RE2,线性时间,无回溯灾难
	MatchErrorCode    = "error_code"    // 逗号分隔的 types.ErrorCode 精确值
	MatchStatusCode   = "status_code"   // 逗号分隔 HTTP 状态码或区间 "400-499"
	MatchUpstreamText = "upstream_text" // 换行分隔子串,匹配上游错误文本
	// MatchRequestRate 是唯一一种不看文本的匹配方式:pattern 是一个整数阈值,
	// 命中条件为"该用户 rateWindowSeconds 内已通过校验、即将发往上游的**非流式**
	// 请求条数 >= 阈值"。这条判据服务于"防蒸馏":批量采集训练语料的一方要的是
	// 完整 JSON,不会开 stream;开 stream 的通常是真人在等字。
	//
	// **它的局限必须公开写在管理端表单里**:客户端加一行 "stream": true 就能
	// 完全绕过。它是一道减速带,不是一堵墙。判据本身无法根治这一点 ——
	// 逐字节比对流式与非流式的产出没有可行的在线实现。
	MatchRequestRate = "request_rate"
	// MatchAIReview(= "ai_review",定义在 aireview_model.go)是第七种匹配方式。
	// 它同样不看本地文本表:pattern 是违规类型的白名单,判据来自一次外部模型调用。
)

// 分组作用域的名单方向。
//
// 只加这一列、不加第二份名单:两张能互相矛盾的名单(白名单 + 黑名单)必然漂移,
// 而"到底哪张说了算"没有任何一个取值组合能自解释。空 GroupScope 时这一列恒为
// include(见 ruleUpsertReq.apply),因为"空黑名单"与"空白名单"语义完全相同,
// 留两个等价状态只会让人以为它们不同。
const (
	GroupScopeInclude = "include" // 名单非空 = 只对名单内的分组生效
	GroupScopeExclude = "exclude" // 名单非空 = 对名单内的分组豁免,其余全部生效
)

// 规则的执行模式。**这是"影子 / 真实"唯一的开关**。
//
// # 为什么只剩这一层
//
// 在此之前是两层:全局开关(YAML violation.shadow_mode + qy_settings 覆盖 +
// PUT /violation/mode)与规则级 DryRun,叠加时取更保守者胜。项目方的原话是
// 「影子模式、真实模式是绑定到规则上,当前你这个切来切去的本来简单的功能搞得
// 那么复杂」——他要的用例只有一个:**把某一条规则设成影子,只记录不处罚,
// 拿它抓到的日志和上下文做分析**。两层结构让这个用例需要先确认全局在哪一档,
// 因为全局为影子时规则级怎么调都不生效。所以全局层整体删除,Mode 是唯一权威。
//
// # 未知取值一律按影子处理
//
// 空串(滚动升级期间旧节点写下的行、DBA 手工插的行、迁移尚未跑到的行)与任何
// 无法识别的值都不是 enforce。判据一律写成 `Mode == ModeEnforce` 而不是
// `Mode != ModeShadow`:前者对一切未知取值给出"不扣费不封号",后者给出
// "真实扣费封号",而这两种写法在正常数据上完全等价 —— 差别只在事故那天出现。
const (
	ModeShadow  = "shadow"  // 只匹配、只记录:不扣费、不阻断、不封号、不计数
	ModeEnforce = "enforce" // 真实执行
)

// 规则来源。区分"内置规则包导入的"与"运营自己写的",两者的升级策略不同。
const (
	SourceManual  = "manual"  // 管理端手写(空串按 manual 读,那是这一列出现之前的唯一语义)
	SourceBuiltin = "builtin" // 由内置防护规则包导入,见 builtin.go
)

// 处置动作。
const (
	ActionRecord         = "record"           // 仅记录
	ActionCharge         = "charge"           // 扣费,不阻断
	ActionBlock          = "block"            // 阻断(仅 prompt 阶段有效)
	ActionBlockAndCharge = "block_and_charge" // 阻断 + 扣费
)

// 扣费方式。
const (
	FeeNone               = "none"
	FeeFixed              = "fixed"                // 固定美元金额
	FeeModelPriceMultiple = "model_price_multiple" // 模型价格 × 倍数
)

// 违规记录状态。
const (
	RecordActive   = "active"
	RecordRevoked  = "revoked"
	RecordAppealed = "appealed"
)

// CounterAfterShadow 是影子记录 counter_after 列的固定取值。
//
// 影子命中不推进违规计数(裁决 2:「不扣费,不封号,不记录违规次数」),
// 所以"命中之后计数是多少"这个问题对它根本没有答案。取 0 会与"计数确实是 0"
// 混淆;-1 是 bumpCounter 不可能产生的哨兵值,直接查库的人也能一眼看出这一行
// 从未参与计数。管理端列表按 counted=false 显示 "-",不读这一列。
//
// **已知代价(不要在文档里糊过去)**:管理员因此失去了 O(1) 的
// "这个用户在影子模式下已经攒了多少次"。要得到这个数只能去 qy_violation_record
// 按 (user_id, shadow=true, created_at >= 窗口起点) 做一次 COUNT ——
// 在这张增长最快的表上是一次范围扫描,比读计数器贵得多。
// 这是裁决 2 明确接受的代价:另加一列"影子计数"就是在记录违规次数,
// 而它一旦存在,下一次改动就会有人把它接回封号判据。
const CounterAfterShadow = -1

// 扣费结果。want 与实际两列并存,是"余额不足被截断"这类偏差唯一的可审计留痕。
const (
	FeeStatusNone         = "none"
	FeeStatusCharged      = "charged"
	FeeStatusTruncated    = "truncated"    // 余额不足,按 clamp 策略只扣了一部分
	FeeStatusInsufficient = "insufficient" // 余额为 0,一分未扣
	FeeStatusShadow       = "shadow"       // 影子模式:算出了金额但没扣
	FeeStatusFailed       = "failed"
	FeeStatusRefunded     = "refunded"
	FeeStatusSkippedDup   = "skipped_dup_builtin" // 上游内置 Grok 违规扣费已扣过
)

// 封禁状态。
const (
	BanPending = "pending" // 已认领,主库六步尚未完成 → 补偿任务扫描
	BanBanned  = "banned"
	BanSkipped = "skipped" // 目标是 root / 已禁用 / 已删除
	BanFailed  = "failed"
	// BanDeferred 表示"计数确实达到了阈值,但当时的策略不允许执行封号"
	// (目前只有小时封号速率闸会产生它)。
	//
	// 它存在的唯一理由是把这个事实持久化。没有它,"该封号"只活在一次函数调用里:
	// 速率闸一挡,信号就消失了,管理端也看不到任何痕迹。deferred 行不会被补偿任务
	// 自动执行 —— 速率闸的语义就是"先让人看一眼",自动补做等于把它彻底架空 ——
	// 而是由管理员在封禁列表里判定"不予封禁"(unbanUser 接受 deferred),
	// 或等该用户下一次违规时在条件允许的情况下被提升执行。
	BanDeferred = "deferred"
	BanUnbanned = "unbanned"
	// BanObserved 表示"计数达到了阈值,但生效的策略档只要求记录"
	// (BanPolicy.Action == PolicyActionRecord)。
	//
	// 它必须落一行,不能只打一条日志。「仅记录」这一档的**唯一价值**就是让管理员
	// 看见"如果我把这个分组切成封号,现在会封掉谁"——那份名单只能来自封禁列表。
	// 只写 SysLog 的话它散落在文本日志里,既不可筛选也不可分页,等于没有。
	//
	// 它是终态:补偿任务不执行它(策略明说了不处置),unbanUser 也不接受它
	// (从来没有人被这一行禁用过,"解封"无从谈起)。
	BanObserved = "observed"
)

// 触发封号的那条线。Ban.TriggerKind 的取值。
//
// 两条线是 OR:任一越过即要求处置。区分它们不是为了做统计,而是为了让
// "他到底为什么在第 3 次就被封了"有一个不需要读源码的答案 —— 管理端封禁列表
// 直接显示这一列,`category` 那几行会同时显示类型名与该类型的阈值。
const (
	BanTriggerGlobal   = "global"   // 账号总量线(BanPolicy.Threshold,跨全部类型)
	BanTriggerCategory = "category" // 单类型线(Category.Threshold)
	BanTriggerBoth     = "both"     // 同一次命中把两条线一起推过
)

// 达到阈值之后的处置动作。BanPolicy.Action 的取值。
//
// # 只有两个真实的账号状态,不要假装有三个
//
// 主库的非删除停用态只有一个:common.UserStatusDisabled。它的语义在
// 「被禁用改成受限账号」之后是**受限账号 —— 能登录、只能提工单,其余一律 403**
// (见 common.UserStatusAllowsSession 的说明)。所以 restrict 与 ban 落到
// users.status 上是同一个值,差别在会话处置:
//
//   - restrict 保留用户当前的控制台会话。用户仍然登录着,能立刻看到自己被限制、
//     能直接提工单申诉。这是"减速带"档:目的是止损,不是驱逐。
//   - ban 额外吊销全部会话(model.RevokeAllUserSessions),用户被强制登出。
//     这是"驱逐"档,对应确认无误的滥用。
//
// 两档在 auth_version 上是一样的(都递增,残留 JWT 一律失效),因此 restrict
// **不是**一个更弱的安全边界 —— 它只是不主动把人踢出控制台。把这一点写在这里,
// 是因为下一个人很容易以为 restrict 漏做了什么。
const (
	PolicyActionRecord   = "record"   // 仅记录:落一行 observed,不动账号
	PolicyActionRestrict = "restrict" // 置为受限账号,保留控制台会话
	PolicyActionBan      = "ban"      // 置为受限账号 + 吊销全部会话
)

// BanPolicy 是**按用户分组**的违规处置策略档。
//
// # 它取代了什么
//
// 在此之前阈值与窗口只有 YAML 里的 violation.auto_ban_threshold /
// auto_ban_window_hours 两个全局数字:改一次影响全站,而且改完要重启进程。
// 项目方原话:「当前违规配置,你没有设定封号阈值是多少,没有地方可以配置这些策略,
// 我需要可以根据用户组不同调整封号策略,默认兜底。」
//
// # 兜底档不可删,这是这张表唯一的硬约束
//
// IsDefault 为 true 的那一行(UserGroup 恒为空串)是没有专属档的分组的落点。
// 删掉它 = 让这些分组落进一个不存在的策略。本仓在 default 用户分组上踩过同一个坑,
// 所以这里有三道锁,缺一道都不够:
//
//  1. 删除接口拒绝 is_default 行(api_admin_banpolicy.go 的 adminDeleteBanPolicy);
//  2. 启动期 ensureDefaultBanPolicy 无条件补建,种子取自 YAML 的两个旧字段 ——
//     因此升级上来的站点行为完全不变;
//  3. 解析时 resolveBanPolicy 找不到任何行就回落到 YAML 值。第 3 道兜的是
//     "扩展库不可达"与"表刚建还没种子"这两种前两道都盖不住的时刻。
//
// # UserGroup 存的是归一化后的比较键
//
// 与规则的 GroupScope 同理:扩展库与主库的分组列都是大小写不敏感的排序规则,
// 而 Go 的 map 查表是精确匹配。写入时统一走 groupname.Effective,
// 否则管理端配 "VIP"、用户实际分组是 "vip" 时,这一档保存成功、界面正常、
// 线上永不生效 —— 而"永不生效"在这里的含义是"这个分组的人永远不会被封"。
type BanPolicy struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// UserGroup 是 groupname.Effective 归一后的用户分组名。兜底档恒为空串。
	// 唯一索引同时保证"一个分组只有一档"与"兜底档只有一行"。
	UserGroup string `json:"user_group" gorm:"type:varchar(64);not null;default:'';uniqueIndex:uk_qy_vbp_group"`
	IsDefault bool   `json:"is_default" gorm:"not null"`

	// Enabled=false 表示这一档暂时停用:该分组回落到兜底档,而不是"不处置"。
	// 兜底档的 Enabled 被忽略(它没有可回落的下一级),界面上因此不给开关。
	Enabled bool `json:"enabled" gorm:"not null"`

	// WindowHours 是滚动统计窗口;Threshold 是窗口内累计达到多少次触发 Action。
	// Threshold <= 0 表示这一档不做任何自动处置(与旧的 auto_ban_threshold=0 同义)。
	WindowHours int `json:"window_hours" gorm:"not null;default:24"`
	Threshold   int `json:"threshold" gorm:"not null;default:0"`

	Action string `json:"action" gorm:"type:varchar(16);not null;default:'ban'"`
	Remark string `json:"remark" gorm:"type:varchar(512);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (BanPolicy) TableName() string { return "qy_violation_ban_policy" }

// ───────────────────────────── 违规类型 ─────────────────────────────

// FallbackCategoryKey 是「未分类」兜底类型的业务键。
//
// 它与 BanPolicy 的兜底档同性质:没有显式类型的规则都落在这里,所以它不可删除。
// 名字刻意是一个不像业务概念的词 —— 运营看到「未分类」就知道这里的规则还没归过类,
// 而不会以为它是某种违规。
const FallbackCategoryKey = "uncategorized"

// Category 是一等公民的违规类型。
//
// # 它取代了什么
//
// 在此之前"类型"只存在于两个地方:内置规则目录里的 builtinCategory(**只是**
// 管理端目录的展示分组,不落库、不参与任何判定),以及每条规则各自的 Name 与
// PublicReason 字符串。也就是说"这个用户在破限这一类上已经犯了几次"根本无法回答 ——
// 计数只有用户维度一个总数,而规则维度的聚合要去 record 表做范围扫描。
// 项目方要的「违规规则计数绑定到违规类型,违规类型计次达到多少次封号」在旧结构里
// 没有任何落点。
//
// # 内部说明与公示文案必须是两列
//
// Remark 会写匹配细节("命中 DAN / 开发者模式 / 忽略上述指令 三组词表")——
// 那是给运营看的,直接公示等于把绕过方法印在用户手册上。PublicTitle / PublicDesc
// 是对外那一份,只说"这一类是什么、几次会怎样"。两者共用一列的后果不是"少一列",
// 是**永远只能二选一**:要么运营看不到细节,要么用户看得到细节。
//
// # 阈值只出"线",不出"处置动作"
//
// Threshold / WindowHours 回答"这一类累计几次算越线";越线之后**怎么处置**
// 一律由用户所在分组的 BanPolicy.Action 决定(见 counter.go 的 resolveBanClaim)。
// 刻意不在类型上再放一个 Action:那会造出"VIP 分组只记录、但破限类型说封号"
// 这种没有任何取值组合能自解释的矛盾,而"到底几次封号"正是本轮要让人答得上来的问题。
//
// Threshold <= 0 表示这一类**不单独触发**处置,它的命中仍然计入账号总量线。
// 存量规则迁移过来的「未分类」就是这一档 —— 因此迁移不改变任何现有站点的行为。
type Category struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// Key 是稳定业务键,也是模块外(AI 审核)绑定类型的唯一入口(见 CategoryByKey)。
	//
	// 唯一索引是 (key, archive_seq) —— **不是** (key, deleted_at)。理由见 ArchiveSeq。
	Key  string `json:"key" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_vcat_key,priority:1"`
	Name string `json:"name" gorm:"type:varchar(64);not null;default:''"`
	// Remark 是**内部**说明:匹配细节、误杀场景、运营口径。绝不下发用户端。
	Remark string `json:"remark" gorm:"type:varchar(512);not null;default:''"`

	// PublicTitle / PublicDesc 是**对外公示**文案,用户端只看得到这两列。
	PublicTitle string `json:"public_title" gorm:"type:varchar(64);not null;default:''"`
	PublicDesc  string `json:"public_desc" gorm:"type:varchar(512);not null;default:''"`
	// Published 决定这一类是否出现在用户端公示里。
	//
	// 未公示不等于不生效:阈值照样算、照样封号。这两件事分开是刻意的 ——
	// 有些类型(例如仍在观察期的新类型)不适合先告诉用户,但它的计数不该因此停摆。
	Published bool `json:"published" gorm:"not null"`

	// AIGuidance 是**第三份文本**:给审核模型看的判定说明。
	//
	// # 三份文本各去哪里,这一段就是它们的边界
	//
	//	Remark       内部备注   → 只有管理端。不进提示词,不进用户端。
	//	PublicTitle  公示文案   → 用户端。绝不写判据。
	//	PublicDesc
	//	AIGuidance   判定说明   → 随提示词发往第三方审核服务。不进用户端。
	//
	// 为什么不能拿 Remark 顶上:Remark 写的是运营口径("这一类误杀多,先观察"、
	// "李四负责复核"),把它塞进提示词是在给模型灌噪声,还会把内部人名、
	// 复核流程一起送到第三方。反过来 AIGuidance 写的是判据("请求可执行的漏洞
	// 利用代码、C2 框架、勒索软件"),它与 Remark 只是碰巧都"内部",用途不同。
	//
	// 为什么不能拿 PublicDesc 顶上:公示文案泄露判定细节等于把绕过方法印给用户,
	// 而判定说明**必须**写细节 —— 两者的要求是直接矛盾的,共用一列只能二选一。
	//
	// 空串表示这一类只把 Key 与 Name 告诉模型,不给额外判据。那是可用的
	// (类型名本身通常已经有信息量),只是准确率会低一档。
	// 列宽刻意比 Remark(512)窄一半:这一段是**每一次审核调用都要付一遍**的
	// token,而类型条数由运营决定、没有上界。512 × 20 个类型是一份没人会注意到
	// 的固定成本,而它每天要被付几十万遍。
	AIGuidance string `json:"ai_guidance" gorm:"type:varchar(256);not null;default:''"`
	// AIExcluded=true 表示这一类**不出现在发给审核模型的类型清单里**。
	//
	// # 为什么是"排除"而不是"启用"
	//
	// 两条理由,方向一致:
	//
	//  1. 升级安全。存量库里这一列会被 AutoMigrate 补成 false,于是升级上来的
	//     站点全部类型照常参与 —— 不需要一次"把老行刷成 true"的迁移,
	//     而那种迁移一旦每次启动都跑,管理员关掉的开关会在下次重启被打开。
	//  2. 出厂语义。新建一个类型时运营想的是"让 AI 也认这一类",默认参与才对得上。
	//
	// # 什么时候真的要排除
	//
	// 判据不是文本的类型。本仓出厂就有两个:distill(判据是请求频率,不看文本)
	// 与 upstream(判据是上游返回的策略性 4xx)。把它们写进类型清单,模型会
	// 对着一段普通对话猜"这像不像批量采集",而那一票命中会加到一个语义完全
	// 不同的计数上 —— 那正是"配置正确却算错账"。
	AIExcluded bool `json:"ai_excluded" gorm:"not null"`

	// Enabled=false 表示这一类的**阈值**暂时停用(等价于 Threshold=0),
	// 类型本身仍然存在、规则仍然绑着它、计数仍然累加。
	//
	// 它与 AIExcluded 是两件事,不要合并:关掉阈值是"这条线先不生效",
	// 排除 AI 是"这一类模型判不了"。合成一个开关的话,运营为了暂停一条线
	// 就会顺手把这一类从 AI 的类型清单里删掉,而清单变了模型的输出分布就变了。
	Enabled     bool `json:"enabled" gorm:"not null"`
	WindowHours int  `json:"window_hours" gorm:"not null;default:24"`
	Threshold   int  `json:"threshold" gorm:"not null;default:0"`

	// SortOrder 是类型在列表里的排序位。
	//
	// **不写 gorm `default:`**:GORM 在 INSERT 时会跳过带默认值标签的零值字段,
	// 让数据库把 0 填成 100 —— 管理员显式排到最前面的那一类会静默排到最后。
	// 出厂默认由建类型那一路的构造函数给(见 api_admin_category.go)。
	SortOrder int `json:"sort_order" gorm:"not null"`
	// IsFallback 标记「未分类」那一行。它是没有显式类型的规则的落点,
	// 删掉它 = 让这些规则变成无类型的孤儿,所以删除接口拒绝它。
	IsFallback bool `json:"is_fallback" gorm:"not null"`

	CreatedAt int64 `json:"created_at" gorm:"not null"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`

	// ArchiveSeq 是 key 唯一索引的第二列:活着的行恒为 0,归档时置为该行自己的 Id。
	//
	// # 为什么不能直接把 deleted_at 放进唯一索引
	//
	// 这是本轮被测试当场抓到的一个真实缺陷。唯一索引写成 (key, deleted_at) 看起来
	// 很自然 —— "活着的行 deleted_at 都是 NULL,所以它们之间仍然唯一"。**这是错的**:
	// MySQL、PostgreSQL、SQLite 三家的唯一索引都把 NULL 视为**互不相等**,
	// 于是 (spam, NULL) 与 (spam, NULL) 不冲突,同一个 key 可以有任意多个活着的行。
	// 表现是 ensureSeedCategories 每次重启都把整套种子再插一遍
	// (TestSeedAndMigrationBindsExistingRules 的幂等子用例 6 → 12 行就是它)。
	//
	// # 为什么取自己的 Id 而不是时间戳
	//
	// 时间戳在"同一秒内归档两次同 key"时会撞;Id 是主键,天然唯一,
	// 因此归档行之间**永远**不会冲突,同名 key 可以反复建了又归档。
	// 活着的行恒为 0,于是 key 在活行之间是严格唯一的 —— 这正是要的约束。
	ArchiveSeq int64 `json:"-" gorm:"not null;default:0;uniqueIndex:uk_qy_vcat_key,priority:2"`
	// DeletedAt 是归档语义,不是删除。历史违规记录是证据,绝不级联删除;
	// 类型行本身也留着,否则管理端点开一条历史记录会看到一个查不到的 category_id。
	// 它只负责"默认查询里看不见",唯一性由 ArchiveSeq 负责。
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Category) TableName() string { return "qy_violation_category" }

// CategoryCounter 是 (用户, 违规类型) 维度的滚动窗口计数。
//
// # 为什么不能从 qy_violation_record 现算
//
// 「这个用户在破限这一类上本窗口犯了几次」在记录表上是一次
// (user_id, category_id, created_at >= 窗口起点) 的范围扫描,而记录表是全部扩展表里
// 增长最快的一张,这个查询会出现在**每一条违规命中的处理路径上**。
// 用户维度的 Counter 当初就是为了避开同一个扫描才存在的,类型维度没有理由退回去。
//
// # 与 Counter 的关系:两条线,一个封禁周期
//
// Counter 是账号总量线(跨全部类型),这张表是单类型线。两者是 OR:任一越线都要求处置。
// 但**封禁认领的互斥键仍然只有 (user_id, ban_cycle)**,ban_cycle 只存在于 Counter 上 ——
// 因此两条线同时越过也只会产生一次封号,不会封两次。
//
// 与 Counter 同口径:**只有真实命中会写这张表**,影子命中一个字节都不碰。
type CategoryCounter struct {
	UserId     int   `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	CategoryId int64 `json:"category_id" gorm:"primaryKey;autoIncrement:false"`

	WindowStart int64 `json:"window_start" gorm:"not null;default:0"`
	HitCount    int   `json:"hit_count" gorm:"not null;default:0"`
	TotalCount  int64 `json:"total_count" gorm:"not null;default:0"`
	LastHitAt   int64 `json:"last_hit_at" gorm:"not null;default:0"`
	UpdatedAt   int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (CategoryCounter) TableName() string { return "qy_violation_cat_counter" }

// 申诉状态。
const (
	AppealPending   = "pending"
	AppealApproved  = "approved"
	AppealRejected  = "rejected"
	AppealWithdrawn = "withdrawn"
)

// Rule 是一条违规规则。
//
// 软删而非硬删:历史记录冗余了 rule_name,但管理端复核时仍需要回看规则原文,
// 硬删会让申诉流程失去判断依据。
type Rule struct {
	Id     int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	Name   string `json:"name" gorm:"type:varchar(128);not null"`
	Remark string `json:"remark" gorm:"type:varchar(512);not null;default:''"`
	// PublicReason 是写进用户计费日志与用户端列表的对外文案。
	// 与 Name 分开:Name 常含内部代号(如 "csam_v3_高危"),直接给用户看等于泄漏规则库。
	PublicReason string `json:"public_reason" gorm:"type:varchar(128);not null;default:''"`

	// CategoryId 是这条规则所属的违规类型(见 Category)。
	//
	// **同一类型下多条规则的命中累加到同一个类型计数**,这正是"规则绑到类型、
	// 类型计次封号"的落点:计数键是 category_id 而不是 rule_id。
	//
	// 0 表示这一行还没绑过类型。运行期不存在孤儿:categoryForRule 把 0(以及任何
	// 指向已归档类型的值)折进「未分类」兜底类型。落库侧另有两道:写入接口把 0 解析成
	// 兜底类型 id,启动期迁移把存量 0 一次性补齐(见 migrateRuleCategory)。
	// 三道都指向同一个方向,漏掉任何一道都不会产生"不计数的规则"。
	CategoryId int64 `json:"category_id" gorm:"not null;default:0;index:idx_qy_vr_category"`

	Enabled bool `json:"enabled" gorm:"not null;index:idx_qy_vr_enabled_phase,priority:1"`
	// Mode 是这条规则的执行模式:ModeShadow(影子)或 ModeEnforce(真实)。
	//
	// 取代了旧的 DryRun 布尔列。默认值刻意写在 gorm tag 上而不是只靠代码:
	// AutoMigrate 给已有行 ADD COLUMN 时会用它回填,于是**现网每一条历史规则
	// 都会落在 shadow**,这正是迁移策略要的结果(见 migrate.go)。
	Mode string `json:"mode" gorm:"type:varchar(16);not null;default:'shadow'"`
	// Priority 升序,小者先判。
	//
	// **不写 gorm `default:`**:0 是一档合法配置(最高优先级),而 GORM 会跳过
	// 带默认值标签的零值字段,让数据库把它填成 100 —— "我把它设成 0 让它最先判"
	// 于是静默失效,命中顺序改由 100 这一档的次级排序决定。
	// 出厂默认(漏传字段时的 100)由 adminCreateRule 的构造给。
	Priority int `json:"priority" gorm:"not null"`

	// Source / BuiltinKey / BuiltinVersion / BuiltinFingerprint 只对内置规则包
	// 导入出来的规则有意义,手写规则一律留空。四列合起来回答升级时的唯一问题:
	// 「这条规则运营改过没有」—— 改过就绝不覆盖。详见 builtin.go。
	Source     string `json:"source" gorm:"type:varchar(16);not null;default:''"`
	BuiltinKey string `json:"builtin_key" gorm:"type:varchar(64);not null;default:'';index:idx_qy_vr_builtin"`
	// BuiltinVersion 是导入(或上一次升级)时内置目录里那条规则的版本号。
	BuiltinVersion int `json:"builtin_version" gorm:"not null;default:0"`
	// BuiltinFingerprint 是导入时**我们发出去的** pattern 的指纹。升级时拿它跟
	// 规则当前 pattern 的指纹比:相等 = 运营没动过 = 可以安全替换;不等 = 运营
	// 改过 = 跳过并如实上报。存指纹而不是存原文,是因为原文最长 8KB,
	// 而每条内置规则的原文本来就在代码里,重复存一份只会漂移。
	BuiltinFingerprint string `json:"builtin_fingerprint" gorm:"type:varchar(64);not null;default:''"`

	Phase     string `json:"phase" gorm:"type:varchar(24);not null;index:idx_qy_vr_enabled_phase,priority:2"`
	MatchType string `json:"match_type" gorm:"type:varchar(24);not null"`
	Pattern   string `json:"pattern" gorm:"type:text;not null"`
	// CaseSensitive 只对 keyword / upstream_text / regex 有意义。
	CaseSensitive bool `json:"case_sensitive" gorm:"not null"`

	// StatusScope 是**上游 HTTP 状态码前置条件**,与 ModelScope / GroupScope 同性质:
	// 它不是一种匹配方式,而是一道作用域闸 —— 空 = 不限状态码,否则逗号分隔的
	// 状态码或区间("400" / "400,403" / "400-499"),语法与 MatchStatusCode 的
	// pattern 完全一致(共用 parseStatusRange)。
	//
	// # 它解决的问题:一条规则同时要求"状态码"与"正文"
	//
	// 项目方给的那条 Anthropic 拒绝是 `status_code=400` **且**正文含
	// "This content was flagged for possible cybersecurity risk"。在此之前
	// MatchType 是单选:status_code 规则看不到正文,upstream_text 规则看不到状态码,
	// 两者只能拆成两条规则,而两条规则各自命中、各自计数 —— 一次上游拒绝会被
	// 计成两次违规,还会各扣一次费。
	//
	// # 为什么做成作用域列而不是第七种 MatchType
	//
	// 新增一个 "status_and_text" 匹配方式只能覆盖这一种组合;下一次要
	// "状态码 + 正则"或"状态码 + 错误码"时还得再加两种。作用域列与**全部**
	// 匹配方式正交,一次解决所有组合,而且它复用已有的 inScope 语义 ——
	// 管理端表单上它和"模型作用域""分组作用域"排在一起,是同一个心智模型。
	//
	// # 只在上游阶段有意义
	//
	// prompt 阶段还没有上游响应,状态码恒为 0。ValidateRule 因此拒绝在
	// PhasePrompt 上配置它:允许保存等于埋一条永不命中的规则。
	StatusScope string `json:"status_scope" gorm:"type:varchar(64);not null;default:''"`

	// ModelScope / GroupScope 对应需求原文"某个模型(全部分组或特定分组下)"。
	// 空 = 全部;否则逗号分隔,模型支持 "gpt-4*" / "*-vision" 前后缀通配。
	ModelScope string `json:"model_scope" gorm:"type:varchar(2048);not null;default:''"`
	GroupScope string `json:"group_scope" gorm:"type:varchar(1024);not null;default:''"`
	// GroupScopeMode 决定 GroupScope 是白名单还是黑名单(include / exclude)。
	// 空串按 include 处理:滚动升级期间旧节点写下的行、以及 DBA 手工建的表都可能
	// 留空,而 include 正是这一列出现之前的唯一语义,回落到它不改变任何既有规则。
	GroupScopeMode string `json:"group_scope_mode" gorm:"type:varchar(8);not null;default:'include'"`

	Action  string `json:"action" gorm:"type:varchar(24);not null;default:'record'"`
	FeeMode string `json:"fee_mode" gorm:"type:varchar(24);not null;default:'none'"`
	// FeeFixed / FeeMultiple 为 0 时回落到 YAML 的 fixed_fee_amount / fee_multiplier,
	// 这样"改一次配置调整全部规则"与"单条规则特殊定价"两种用法都成立。
	FeeFixed    decimal.Decimal `json:"fee_fixed" gorm:"type:decimal(18,8);not null;default:0.00000000"`
	FeeMultiple decimal.Decimal `json:"fee_multiple" gorm:"type:decimal(18,6);not null;default:0.000000"`
	// FeeMaxQuota 是规则级单笔上限(quota,0 = 不限)。
	// model_price_multiple 遇到高价模型 + 大倍数会一次扣穿余额,必须有闸。
	FeeMaxQuota int64 `json:"fee_max_quota" gorm:"not null;default:0"`

	// AIMinConfidence 是 ai_review 规则的置信度下限,取值 0..1,0 = 不设下限。
	// 其它匹配方式忽略这一列。
	//
	// 它是 AI 审核唯一的误判闸:模型判"违规"但只有 0.3 的把握时,把它当成
	// 一次真实违规去扣费封号是不可接受的。没有这道闸,唯一的调节手段就是
	// 把整条规则切回影子 —— 那等于关掉它。
	AIMinConfidence decimal.Decimal `json:"ai_min_confidence" gorm:"type:decimal(5,4);not null;default:0.0000"`

	// CountWeight 是这条规则命中一次给计数**加几**。
	//
	// 它落在两条线上,同一个数:账号总量线(bumpCounter)与这条规则所绑违规类型的
	// 类型线(bumpCategoryCounter)。所以它与类型阈值是**乘数与阈值**的关系,
	// 不是同一件事的两种写法:N 次命中推进 N × CountWeight,达到该类型的
	// Threshold 才触发处置。权重 2 配阈值 10,五次命中就到线。
	//
	// CountWeight = 0 是一档合法配置:命中照样按 Action / FeeMode 处置
	// (记录、扣费、拦截都照做),但**一条线都不推进** —— 这条规则永远不会
	// 把任何人推向封号。用在"这一类只想收钱/只想留证据"的规则上。
	//
	// 影子命中不写计数器(persistRecord 直接跳过 bumpCounter),但记录里仍然
	// 留下这个权重,回答"若真实执行,这一次会给计数加几"。
	// **不写 gorm `default:1`**:GORM 在 INSERT 时会跳过带默认值标签的零值字段,
	// 由数据库把 0 填成 1。于是一条管理员显式配成「只拦截、不计数」的规则,
	// 建出来之后每次命中都在推进两条线,把用户一路推向自动封号 —— 而管理端
	// 表单回填的仍然是 0(POST 只回 {id},运营看不到自己的配置被改写过)。
	// 方向是**多封人**。出厂默认(漏传字段时的 1)由 adminCreateRule 的构造给。
	CountWeight int `json:"count_weight" gorm:"not null"`

	// 这里曾经还有一个 Severity(1=低 2=中 3=高)。它从头到尾**只写不读**:
	// 全仓除了 upsert 赋值与内置种子之外没有任何消费者,既不参与判定、
	// 不参与排序、也不进导出与告警。违规类型体系落地之后,"这一类有多严重"
	// 已经由类型自己的阈值与窗口表达,再留一个装饰性的 1..3 只会让管理员
	// 以为改它会改变什么。所以表单、API 字段与内置种子全部移除。
	//
	// **数据库列 `severity` 也已删除**(dropLegacySeverityColumn,启动期一次性)。
	// 上一轮保留它的理由是"删列不可逆",而项目方确认本项目尚未上线生产,
	// 那条理由不再成立 —— 留一个没人读的列只会让后来者反复重新调查它是干什么的。
	//
	// 它不影响内置规则指纹:指纹是 sha256(pattern),从来不含 severity,
	// 所以删列不会把任何已导入的内置规则误报成 "modified"(见 builtin.go)。
	//
	// 不要把这个字段加回来。真要给"严重程度"一个用途,那是一次新功能,
	// 得先有读点 —— 而现在的读点应该是违规类型的阈值。

	ArchiveContext bool `json:"archive_context" gorm:"not null"`
	// BlockMessage 是返回给客户端的文案。严禁把命中词写进来 —— 等于告诉刷子绕过方法。
	BlockMessage string `json:"block_message" gorm:"type:varchar(512);not null;default:''"`

	CreatedAt int64          `json:"created_at" gorm:"not null"`
	UpdatedAt int64          `json:"updated_at" gorm:"not null"`
	CreatedBy int            `json:"created_by" gorm:"not null;default:0"`
	UpdatedBy int            `json:"updated_by" gorm:"not null;default:0"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

func (Rule) TableName() string { return "qy_violation_rule" }

// RuleVersion 是单行表,每次规则写操作 +1。
//
// 多节点部署下节点 A 改规则、节点 B 必须感知。轮询这张单行表(一次主键查询)
// 比轮询全表便宜三个数量级,所以快照刷新永远先读它、版本没变就不拉规则。
type RuleVersion struct {
	// autoIncrement:false 是必须的:GORM 默认会把整型主键建成 AUTO_INCREMENT,
	// 而这三张表的主键都是外部指定的业务键(恒为 1 / user_id / record_id),
	// 让数据库替它们自增只会制造误导。
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement:false"` // 恒为 1
	Version   int64 `json:"version" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
}

func (RuleVersion) TableName() string { return "qy_violation_rule_version" }

// Record 是一条违规记录。
//
// RecNo 唯一索引是幂等的唯一保障:defer 在 panic-recover 与未来上游改动下可能重入,
// 重试循环也可能让同一 request_id 多次走到检测点。
type Record struct {
	Id    int64  `json:"id" gorm:"primaryKey;autoIncrement"`
	RecNo string `json:"rec_no" gorm:"type:varchar(64);not null;uniqueIndex:uk_qy_vrec_no"`

	UserId int `json:"user_id" gorm:"not null;index:idx_qy_vrec_user,priority:1"`
	// Username / TokenName 冗余存一份:管理端列表跨库 join 主库是不可行的。
	Username  string `json:"username" gorm:"type:varchar(64);not null;default:''"`
	TokenId   int    `json:"token_id" gorm:"not null;default:0"`
	TokenName string `json:"token_name" gorm:"type:varchar(64);not null;default:''"`

	RuleId   int64  `json:"rule_id" gorm:"not null;index:idx_qy_vrec_rule"`
	RuleName string `json:"rule_name" gorm:"type:varchar(128);not null;default:''"`
	// PublicReason 冻结命中当时的对外文案。规则改名后用户端列表仍应显示原文案,
	// 否则"我当时为什么被扣钱"永远对不上。
	PublicReason string `json:"public_reason" gorm:"type:varchar(128);not null;default:''"`

	// CategoryId / CategoryName / CategoryPublicTitle 冻结命中当时的违规类型。
	//
	// 三列都要冻结,理由与 PublicReason 完全一致,而且更硬:类型可以被**归档**,
	// 归档之后再去 join 类型表就只能得到一行软删的数据或什么都没有 ——
	// 而历史记录是证据,它必须能独立回答"这一条当时算哪一类"。
	// CategoryName 是内部名(管理端可见),CategoryPublicTitle 是对外文案(用户端可见),
	// 两者分开的理由见 Category 的说明。
	CategoryId          int64  `json:"category_id" gorm:"not null;default:0;index:idx_qy_vrec_cat"`
	CategoryName        string `json:"category_name" gorm:"type:varchar(64);not null;default:''"`
	CategoryPublicTitle string `json:"category_public_title" gorm:"type:varchar(64);not null;default:''"`

	Phase  string `json:"phase" gorm:"type:varchar(24);not null"`
	Action string `json:"action" gorm:"type:varchar(24);not null"`
	// Shadow = true 表示这次命中按影子处理(规则 Mode=shadow,或熔断期间全部规则
	// 被临时按影子执行):不扣费、不阻断、不封号、**不计违规次数**,只留这一行
	// + 证据供管理员核查。fee_quota_want 仍然是算准的("若真实执行会扣多少钱"),
	// 那是影子模式的全部价值。
	//
	// 索引是为了"按规则筛影子命中做误判分析"这个用例:那是项目方给影子模式定的
	// 唯一目的,而它落到 SQL 上就是 (rule_id, shadow, created_at) 三个条件。
	Shadow bool `json:"shadow" gorm:"not null;index:idx_qy_vrec_shadow"`
	// ShadowReason 回答"这一条为什么没有真实执行"。取值是 ShadowReason* 常量。
	//
	// 必须落库:规则今天是 shadow、明天被运营切成 enforce,事后光看记录无法区分
	// "当时规则本身就是影子"与"当时规则是真实的,只是撞上了熔断"—— 而这两种
	// 记录在做误判分析时的含义完全相反(前者是预期内的观察样本,后者是事故现场)。
	ShadowReason string `json:"shadow_reason" gorm:"type:varchar(64);not null;default:''"`
	Blocked      bool   `json:"blocked" gorm:"not null"`

	ModelName   string `json:"model_name" gorm:"type:varchar(128);not null;default:''"`
	UsingGroup  string `json:"using_group" gorm:"column:using_group;type:varchar(64);not null;default:''"`
	ChannelId   int    `json:"channel_id" gorm:"not null;default:0"`
	RelayFormat string `json:"relay_format" gorm:"type:varchar(32);not null;default:''"`

	// RequestId 与主库 logs.request_id 对齐,是"新库记录 ↔ 主库计费日志"唯一的对账钥匙。
	RequestId string `json:"request_id" gorm:"type:varchar(64);not null;default:'';index:idx_qy_vrec_reqid"`
	Ip        string `json:"ip" gorm:"type:varchar(64);not null;default:''"`

	// MatchedTerms / MatchSnippet 是仅管理员可见的命中证据,列表页直接看这两列,
	// 不必拉 payload。写入前必须截断:AcSearch 在大词表上可能返回上万个命中。
	MatchedTerms string `json:"matched_terms" gorm:"type:varchar(1024);not null;default:''"`
	MatchSnippet string `json:"match_snippet" gorm:"type:varchar(2048);not null;default:''"`

	FeeMode      string          `json:"fee_mode" gorm:"type:varchar(24);not null;default:'none'"`
	FeeBaseUsd   decimal.Decimal `json:"fee_base_usd" gorm:"type:decimal(18,8);not null;default:0.00000000"`
	FeeMultiple  decimal.Decimal `json:"fee_multiple" gorm:"type:decimal(18,6);not null;default:0.000000"`
	GroupRatio   decimal.Decimal `json:"group_ratio" gorm:"type:decimal(18,6);not null;default:0.000000"`
	FeeQuotaWant int64           `json:"fee_quota_want" gorm:"not null;default:0"`
	FeeQuota     int64           `json:"fee_quota" gorm:"not null;default:0"`
	FeeStatus    string          `json:"fee_status" gorm:"type:varchar(24);not null;default:'none'"`
	FeeError     string          `json:"fee_error" gorm:"type:varchar(512);not null;default:''"`

	// BillingSource / SubscriptionId 冻结"这笔罚款当时到底从哪个池扣走"。
	//
	// service.PostConsumeQuota 按 relayInfo.BillingSource 把扣款路由到钱包或订阅池,
	// 并且无条件同步扣减 tokens.remain_quota(TokenId 那一列)。退款如果一律加回钱包:
	// 订阅用户的订阅池消耗永远不会归还、钱包却凭空多出等额额度;令牌额度则永久少掉
	// 这一笔。这两件事都无法事后从主库反推(计费日志里没有路由信息),所以扣费当时
	// 必须把上下文冻结进记录 —— 退款只能退回"当初扣的那个池、那个令牌"。
	//
	// 刻意不冻结 token_key:那是明文 API 密钥,落进扩展库等于把凭证复制一份,
	// 而且 Record 会被管理端列表接口整行返回。退款时按 TokenId 直接改 tokens 行,
	// 缓存则用 InvalidateUserTokensCache 整体失效,不需要密钥原文。
	BillingSource  string `json:"billing_source" gorm:"type:varchar(16);not null;default:''"`
	SubscriptionId int    `json:"subscription_id" gorm:"not null;default:0"`
	// QuotaClamp 落 common.QuotaClamp.AuditMap() 的 JSON。额度饱和几乎必然意味着
	// 规则配错,必须能在管理端单独筛出来。
	QuotaClamp string `json:"quota_clamp" gorm:"type:varchar(512);not null;default:''"`

	// CountWeight 是这次命中**本该**给违规计数加的权重。影子记录也写它
	// (回答"若真实执行会加几"),但影子命中不会推进计数器,见 CounterAfterShadow。
	CountWeight int `json:"count_weight" gorm:"not null;default:0"`
	// Counted 表示这次命中是否真的推进了 qy_violation_counter。
	// 影子记录恒为 false —— 撤销记录时的计数回退因此不会对它做无中生有的减法。
	Counted bool `json:"counted" gorm:"not null"`
	// CounterAfter 是推进之后的窗口内计数。影子记录取 CounterAfterShadow。
	CounterAfter int `json:"counter_after" gorm:"not null;default:0"`
	// CategoryCounterAfter 是推进之后的**该类型**窗口内计数,与 CounterAfter 同口径
	// (影子记录取 CounterAfterShadow)。两个数必须都留:账号总量线与类型线是两条
	// 独立的封号判据,事后复盘"他到底是撞了哪条线"只能靠这两列。
	CategoryCounterAfter int `json:"category_counter_after" gorm:"not null;default:0"`

	Status       string `json:"status" gorm:"type:varchar(16);not null;default:'active';index:idx_qy_vrec_status"`
	RevokedBy    int    `json:"revoked_by" gorm:"not null;default:0"`
	RevokedAt    int64  `json:"revoked_at" gorm:"not null;default:0"`
	RevokeReason string `json:"revoke_reason" gorm:"type:varchar(512);not null;default:''"`
	RefundQuota  int64  `json:"refund_quota" gorm:"not null;default:0"`

	HasPayload bool  `json:"has_payload" gorm:"not null"`
	CreatedAt  int64 `json:"created_at" gorm:"not null;index:idx_qy_vrec_user,priority:2;index:idx_qy_vrec_created"`
}

func (Record) TableName() string { return "qy_violation_record" }

// Payload 是违规上下文归档,与 Record 1:0..1 共主键。
//
// 为什么必须拆表:主表行约 1KB,payload 行可达数百 KB。放一起会让"按用户查最近
// 20 条违规"这种列表查询被 InnoDB 溢出页 IO 拖垮,保留期清理也必须动主表。
type Payload struct {
	RecordId int64 `json:"record_id" gorm:"primaryKey;autoIncrement:false"`

	Codec string `json:"codec" gorm:"type:varchar(16);not null;default:'gzip'"`
	// OriginBytes 是剥离 base64 之前的原始体积,RawBytes 是入压缩前的体积。
	// 两者的差值就是"我们替用户省下的存储",也是管理员判断证据是否够用的依据。
	OriginBytes int64 `json:"origin_bytes" gorm:"not null;default:0"`
	RawBytes    int64 `json:"raw_bytes" gorm:"not null;default:0"`
	StoredBytes int64 `json:"stored_bytes" gorm:"not null;default:0"`
	Truncated   bool  `json:"truncated" gorm:"not null"`

	Body []byte `json:"-" gorm:"type:mediumblob"`

	Redacted    bool   `json:"redacted" gorm:"not null"`
	RedactStats string `json:"redact_stats" gorm:"type:varchar(512);not null;default:''"`
	// FilesSummary 是多模态描述符 JSON 数组。绝不含二进制:一条含 10 张 1MB 图片的
	// base64 请求归档下来就是 10MB/条,1000 条/天即 300GB/月,不可接受。
	FilesSummary string `json:"files_summary" gorm:"type:text"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_vpay_created"`
}

func (Payload) TableName() string { return "qy_violation_payload" }

// Counter 是用户维度的滚动窗口违规计数,也是自动封号判据的唯一数据源。
//
// 单行 upsert(窗口过期判断与重置都在同一条语句里)+ 同事务回读,是"多节点并发
// 把计数推过阈值"唯一的正确解法。刻意不用 LAST_INSERT_ID():它是会话级变量,
// GORM 连接池会把 Exec 与 Raw 发到不同连接上 —— 详见 bumpCounter 的说明。
//
// **只有真实命中会写这张表。** 影子命中一律不推进(裁决 2),否则影子模式会把
// 用户推过封号线,而这条线上的下一次真实命中就会立刻落封禁行。
type Counter struct {
	UserId      int   `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	WindowStart int64 `json:"window_start" gorm:"not null;default:0"`
	HitCount    int   `json:"hit_count" gorm:"not null;default:0"`
	TotalCount  int64 `json:"total_count" gorm:"not null;default:0"`
	// BanCycle 在每次解封时 +1。不 +1 则下次达阈值时封禁认领的唯一键会永远冲突,
	// 自动封号从此静默失效 —— 这是本模块最隐蔽的一个失效模式。
	BanCycle  int   `json:"ban_cycle" gorm:"not null;default:0"`
	LastHitAt int64 `json:"last_hit_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Counter) TableName() string { return "qy_violation_counter" }

// Ban 是封禁认领与执行记录。
//
// (user_id, ban_cycle) 唯一 = 分布式互斥的"封号认领锁",一个周期内
// 只可能有一个节点插入成功,从根本上杜绝重复封号。
type Ban struct {
	Id       int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId   int   `json:"user_id" gorm:"not null;uniqueIndex:uk_qy_vban_user_cycle,priority:1"`
	BanCycle int   `json:"ban_cycle" gorm:"not null;uniqueIndex:uk_qy_vban_user_cycle,priority:2"`

	TriggerRecordId int64 `json:"trigger_record_id" gorm:"not null;default:0"`
	HitCountAt      int   `json:"hit_count_at" gorm:"not null;default:0"`
	Threshold       int   `json:"threshold" gorm:"not null;default:0"`
	// PolicyGroup / PolicyAction 冻结"当时是哪一档策略、判了什么动作"。
	//
	// 必须冻结:阈值改成按分组可配之后,"这个人为什么在第 5 次就被封了"
	// 不再能从任何全局配置反推 —— 用户的分组会变,策略档会被编辑,
	// 兜底档也会被调。不存这两列的话,事后复盘只剩一个孤零零的 threshold 数字,
	// 答不出"它来自哪一档"。PolicyGroup 为空串表示当时落的是兜底档。
	PolicyGroup  string `json:"policy_group" gorm:"type:varchar(64);not null;default:''"`
	PolicyAction string `json:"policy_action" gorm:"type:varchar(16);not null;default:''"`

	// TriggerKind 冻结"这次是撞了哪条线":BanTrigger* 三个取值之一。
	//
	// 阈值从一条变成两条(账号总量线 + 单类型线)之后,只存 Threshold / HitCountAt
	// 已经答不出"为什么是现在"—— 一个 hit_count=3、threshold=10 的封禁行看起来
	// 完全说不通,除非同时看到它其实撞的是"破限类 3 次"那条线。
	// 空串是本列出现之前的历史行,一律按账号总量线读。
	TriggerKind string `json:"trigger_kind" gorm:"type:varchar(16);not null;default:''"`
	// TriggerCategoryId / CategoryHitCount / CategoryThreshold 只在类型线参与触发时有值。
	TriggerCategoryId int64 `json:"trigger_category_id" gorm:"not null;default:0"`
	CategoryHitCount  int   `json:"category_hit_count" gorm:"not null;default:0"`
	CategoryThreshold int   `json:"category_threshold" gorm:"not null;default:0"`

	Status    string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_qy_vban_status"`
	Attempts  int    `json:"attempts" gorm:"not null;default:0"`
	LastError string `json:"last_error" gorm:"type:varchar(512);not null;default:''"`

	BannedAt   int64  `json:"banned_at" gorm:"not null;default:0"`
	UnbannedAt int64  `json:"unbanned_at" gorm:"not null;default:0"`
	UnbannedBy int    `json:"unbanned_by" gorm:"not null;default:0"`
	UnbanNote  string `json:"unban_note" gorm:"type:varchar(512);not null;default:''"`
	CreatedAt  int64  `json:"created_at" gorm:"not null;index:idx_qy_vban_created"`
}

func (Ban) TableName() string { return "qy_violation_ban" }

// Appeal 是误判申诉。自动扣费 + 自动封号必然产生误判,没有申诉通道时
// 误判会全部变成工单;申诉通过率本身也是"这条规则该不该改"的反馈回路。
type Appeal struct {
	Id       int64 `json:"id" gorm:"primaryKey;autoIncrement"`
	UserId   int   `json:"user_id" gorm:"not null;index:idx_qy_vap_user"`
	RecordId int64 `json:"record_id" gorm:"not null;uniqueIndex:uk_qy_vap_record"`

	Reason string `json:"reason" gorm:"type:varchar(2000);not null;default:''"`

	Status     string `json:"status" gorm:"type:varchar(16);not null;default:'pending';index:idx_qy_vap_status"`
	ReviewerId int    `json:"reviewer_id" gorm:"not null;default:0"`
	ReviewNote string `json:"review_note" gorm:"type:varchar(1000);not null;default:''"`
	ReviewedAt int64  `json:"reviewed_at" gorm:"not null;default:0"`

	CreatedAt int64 `json:"created_at" gorm:"not null;index:idx_qy_vap_created"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (Appeal) TableName() string { return "qy_violation_appeal" }
