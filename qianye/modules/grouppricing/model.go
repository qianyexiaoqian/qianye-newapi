package grouppricing

import (
	"github.com/shopspring/decimal"
)

// 覆盖口径。一条规则只能是其中之一 —— 它们互斥:同一个模型不可能既按次计价
// 又按 token 计价。允许一条规则同时带价格和倍率,等于把"这次到底按哪个算"
// 交给读取顺序去决定,而那正是计费系统最不能有的性质。
const (
	// ModePrice 覆盖按次固定价(对应 ratio_setting.GetModelPrice)。单位:美元/次。
	ModePrice = "price"
	// ModeRatio 覆盖按 token 的模型倍率(对应 ratio_setting.GetModelRatio)。
	ModeRatio = "ratio"
	// ModeTiered 覆盖阶梯表达式计价,语义是**乘数**:
	// 最终 quota = 表达式结果 × 乘数 × 分组倍率。
	// 阶梯计价的"价格"是一整条表达式,没法用一个标量替换,只能给乘数。
	ModeTiered = "tiered"
)

// modelWildcard 是"该分组下的全部模型"。它也是前缀匹配里最短的那个前缀,
// 因此任何更具体的规则都会赢过它,不存在顺序依赖。
const modelWildcard = "*"

// 写入侧与快照编译侧共用的取值上下界。
//
// 为什么必须有上界:规则值会被直接乘进每一笔账单。上游对 max_tokens、image n
// 这类用户可控乘数都设了硬上界(见 AGENTS.md 计费安全不变量),分组价是管理员
// 可控乘数,同样必须有界 —— 一次手滑把倍率填成 1e18,配合大 token 数就会把
// 中间结果推过 int32,而饱和之后的账单没有任何意义。
const (
	// maxPriceUSD 单次调用 10 万美元。真实按次价在 0.001~1 美元量级,
	// 这个上界宽到不会误伤,又窄到能挡住手滑多敲的零。
	maxPriceUSD = 100000
	// maxModelRatio 与上游 defaultModelRatio 的量级(0.5~500)对齐后放宽三个数量级。
	maxModelRatio = 1000000
	// maxTieredMultiplier 阶梯乘数上界。它是纯倍数,100 倍已经远超任何合理定价。
	maxTieredMultiplier = 100

	maxGroupNameLen = 64
	maxModelNameLen = 128
	maxRemarkLen    = 255
)

// Rule 是一条 (分组, 模型) → 价格覆盖 的规则。
//
// (group_name, model_name) 唯一索引:一个分组下一个模型至多一条规则。
// 因此不需要优先级、不需要"先匹配到哪条" —— 一笔请求按什么价扣钱必须是
// 看一眼就能确定的,不能取决于数据库返回行的次序。
//
// 模型名支持三种形态,匹配优先级 精确 > 最长前缀 > "*":
//
//	"gpt-4o"        精确匹配
//	"gpt-4*"        前缀匹配(去掉尾部 * 后按前缀比,长者优先)
//	"*"             该分组下全部模型
//
// 刻意不做正则:一条写错的正则能在下一个刷新周期内改掉全站的价格,
// 而前缀匹配的影响范围一眼可见。
type Rule struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// GroupName 存的是 relayInfo.UsingGroup 的口径(本次请求实际使用的分组),
	// 与分组倍率、消费日志的 group 列同源,三方数字可对账。
	//
	// 列名刻意不叫 group:那是 MySQL 保留字,虽然 GORM 会自动加反引号,
	// 但一旦将来有人写一句原生 SQL 就会当场炸,而炸点在计费链路上。
	GroupName string `json:"group_name" gorm:"column:group_name;type:varchar(64);not null;uniqueIndex:uk_qy_gpr_group_model,priority:1"`
	ModelName string `json:"model_name" gorm:"type:varchar(128);not null;uniqueIndex:uk_qy_gpr_group_model,priority:2"`

	Enabled bool   `json:"enabled" gorm:"not null"`
	Mode    string `json:"mode" gorm:"type:varchar(16);not null"`

	// Value 是覆盖值,按 Mode 解释:price=美元/次,ratio=模型倍率,tiered=乘数。
	//
	// 用 decimal 而不是 double:配置往返(前端 → JSON → DB → 快照)每经过一次
	// float64 都可能改变最后一位,而这个数字乘的是用户的账单。上游的价格本身
	// 是 float64,所以只在"喂给 hook"那一刻做一次 InexactFloat64,
	// 存储与传输全程是十进制字符串。
	Value decimal.Decimal `json:"value" gorm:"type:decimal(24,10);not null;default:0"`

	Remark string `json:"remark" gorm:"type:varchar(255);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null"`
	CreatedBy int   `json:"created_by" gorm:"not null;default:0"`
	UpdatedBy int   `json:"updated_by" gorm:"not null;default:0"`
}

func (Rule) TableName() string { return "qy_group_price_rule" }

// RuleVersion 是单行表,每次规则写操作 +1。
//
// 多节点部署下节点 A 改了价格,节点 B 必须感知。轮询这张单行表(一次主键查询)
// 比每个周期全表拉规则便宜三个数量级,所以快照刷新永远先读它、版本没变就不拉规则。
type RuleVersion struct {
	// autoIncrement:false:主键是外部指定的业务键(恒为 1),
	// 让数据库替它自增只会制造误导。
	Id        int   `json:"id" gorm:"primaryKey;autoIncrement:false"`
	Version   int64 `json:"version" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (RuleVersion) TableName() string { return "qy_group_price_rule_version" }

// ShadowBucket 是影子模式的差额记录,按小时桶 × 维度聚合。
//
// # 为什么是聚合行而不是一请求一行
//
// 这条记录挂在 relay 的每一次计价上。按请求落行意味着与 relay QPS 等量的写入,
// 而影子模式往往要跑上几周 —— 那张表会比消费日志还大,却只是为了回答一个
// "这个月会多收/少收多少"的汇总问题。聚合行的基数是 分组 × 模型 × 口径,
// 是个位数到百量级,写入被折叠成每分钟一次累加 upsert。
//
// 请求标识仍然保留(SampleRequestId):运营需要能抽一条具体请求去核对,
// 但不需要全部。它存最近一次,与主库 logs.request_id 同源。
//
// # 差额怎么算出来的
//
// 这里只存"旧值 → 新值"和请求数,不存金额。因为计价时还不知道这次请求最终
// 会消耗多少 token —— 预扣额度不是实际扣费。实际扣费在主库消费日志里。
// 由于扣费对被覆盖的那个值是**线性**的(见包注释),
//
//	差额 = 实际扣费 × (新值 / 旧值 - 1)
//
// 这个折算是精确的,不是估算。api_admin.go 的 shadow/summary 就是拿这张表的
// 维度去主库日志库取 SUM(quota) 再乘系数。旧值为 0 或口径发生切换
// (原本按 token 计费却配了按次价)时线性不成立,那种行会被标成 exact=false
// 并单独列出,绝不混进合计数字里假装精确。
type ShadowBucket struct {
	Id int64 `json:"id" gorm:"primaryKey;autoIncrement"`

	// BucketTs 是小时对齐的桶起点(unix 秒)。
	BucketTs  int64  `json:"bucket_ts" gorm:"not null;uniqueIndex:uk_qy_gps_dim,priority:1;index:idx_qy_gps_ts"`
	GroupName string `json:"group_name" gorm:"column:group_name;type:varchar(64);not null;uniqueIndex:uk_qy_gps_dim,priority:2"`
	ModelName string `json:"model_name" gorm:"type:varchar(128);not null;uniqueIndex:uk_qy_gps_dim,priority:3"`
	Mode      string `json:"mode" gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_gps_dim,priority:4"`

	// OldValue / NewValue 进唯一键:同一小时内规则被改过,两段区间必须分行,
	// 合并成一行就再也算不出差额了。存字符串而不是 decimal 是为了让唯一索引
	// 按字面量比较 —— decimal 列上 "1.0" 与 "1.00" 是同一个值但可能是两个字面量,
	// 由 normalizeDecimal 统一成规范形式后入库。
	OldValue string `json:"old_value" gorm:"type:varchar(48);not null;uniqueIndex:uk_qy_gps_dim,priority:5"`
	NewValue string `json:"new_value" gorm:"type:varchar(48);not null;uniqueIndex:uk_qy_gps_dim,priority:6"`

	// Exact 为 false 表示这一段的差额无法按比例精确折算(旧值为 0,
	// 或计费口径从按 token 切成了按次)。汇总时单独列出,不并入合计。
	Exact bool `json:"exact" gorm:"not null;uniqueIndex:uk_qy_gps_dim,priority:7"`

	Requests int64 `json:"requests" gorm:"not null;default:0"`

	SampleRequestId string `json:"sample_request_id" gorm:"type:varchar(64);not null;default:''"`
	UpdatedAt       int64  `json:"updated_at" gorm:"not null;default:0"`
}

func (ShadowBucket) TableName() string { return "qy_group_price_shadow" }
