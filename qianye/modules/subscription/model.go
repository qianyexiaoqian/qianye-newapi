package subscription

// PlanSeat 是一个套餐的全站总名额配置。
//
// # 为什么单开一张表而不是塞进共享的 qy_settings
//
// qy_settings 是 (scope, k) → 字符串的 KV。名额是 per-plan 的整数,塞进去就得
// 把 plan_id 编进 k 里再反解,列表接口要么全表扫 scope 再逐条 parse,要么就得
// 接受"读不出所有套餐的名额"。而且 KV 的 V 是 text,一个手滑写进去的
// "100 " 或 "1e2" 在读取侧只会静默变成 0(= 不限),这正是名额这种闸门最不能
// 出的错。给它一张有类型的表,数据库本身就是第一道校验。
//
// # 为什么不塞进主库 subscription_plans 加一列
//
// 铁律:严禁给上游表加列。而且加列会让 AutoMigrate 去改上游表结构,
// 上游一次 struct 调整就可能把这一列冲掉。
type PlanSeat struct {
	// PlanId 直接做主键:一个套餐至多一份名额配置,天然唯一,
	// 也让"按套餐取"永远走聚簇索引。软引用主库 subscription_plans.id,无外键。
	PlanId int `json:"plan_id" gorm:"primaryKey"`

	// Capacity 是全站总名额:同一时刻最多允许多少个**不同的人**持有该套餐的
	// active 订阅。0 = 不限(与"没有这一行"等价)。
	//
	// 刻意允许 0 而不是删行:保留行才能留下 updated_by / updated_at,
	// 事后能看出"谁在什么时候把名额取消了",而删行只会留下一片空白。
	Capacity int `json:"capacity" gorm:"not null;default:0"`

	// UpdatedBy 是最后修改人的用户 id。名额直接决定还能不能卖,必须可追溯。
	UpdatedBy int `json:"updated_by" gorm:"not null;default:0"`

	// 时间戳一律 int64 unix 秒并手工赋值,不用 autoCreateTime/autoUpdateTime:
	// GORM 对 int64 时间字段的单位推断跨版本不稳定(见 qianye/model/tables.go)。
	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (PlanSeat) TableName() string { return "qy_subscription_plan_seats" }

// statusActive / statusCancelled 是主库 user_subscriptions.status 的字面量。
//
// 刻意写成本包的常量而不是去 model 包找:上游把这三个状态直接写成裸字符串
// ("active"/"expired"/"cancelled"),压根没有导出的常量可用。抄一份并在这里
// 说明来源,比在十几处散落裸字符串好。
const (
	statusActive    = "active"
	statusCancelled = "cancelled"
)
