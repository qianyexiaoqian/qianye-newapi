// Package paypass 实现「余额划转的支付密码」。
//
// # 范围
//
// 裁决 1 把本模块的保护范围钉死在**余额划转**这一条路径上,验密点只有一处:
// 划转的执行入口(qianye/modules/transfer 的 handleCreate)。不是"进入划转页",
// 不是"选中联系人"。
//
// 联系人不构成任何豁免:选中联系人只做一件事 —— 把收款人字段填好。
// 本包不接受任何"这个收款人是联系人"之类的入参,Require 的签名里根本没有
// 表达豁免的位置;no_exemption_test.go 另有一条 AST 断言在全仓禁掉
// `if isContact { skipPayPassword }` 这个形状。
//
// # 已知缺口(不要用"已保护资金操作"糊过去)
//
// 提现(qianye/modules/withdraw)是另一条真实的出钱路径,本轮**没有**接入支付密码。
// 兑换码、充值同理。也就是说:一个拿到用户会话的攻击者仍然可以走提现把钱转出去,
// 支付密码只挡住了划转。接入提现需要在 withdraw 的创建入口加同一行 Require,
// 但那属于 withdraw 模块的改动预算,本轮不做。
package paypass

// PayPassword 是一个用户的支付密码状态。
//
// # 为什么不塞进主库 users 表
//
// 与扩展的全局约束一致:新功能数据一律进独立数据库。塞进 users 会让
// AutoMigrate 去改主库最热的一张表,而且支付密码的锁定状态是高频写字段
// (每次输错都要写),混进 users 行会和上游的余额扣减抢同一把行锁。
//
// # 为什么单独一张表而不是 qy_settings
//
// qy_settings 是 (scope, k) → 字符串的 KV,没有 fail_count 这种要被并发原子
// 自增的列,也没法给 user_id 建主键。
type PayPassword struct {
	// UserId 直接做主键:一个用户至多一份支付密码,天然唯一。
	// 用它当主键而不是自增 id + 唯一索引,是为了让"按用户取"永远走聚簇索引,
	// 也让并发路径上的原子 UPDATE 只可能命中一行。
	UserId int `json:"user_id" gorm:"primaryKey"`

	// Algo 记录 Hash 用的算法,当前恒为 "bcrypt"。
	//
	// 存下来而不是隐含约定:将来若要换成 argon2id,必须能分辨库里哪些行是旧算法
	// 才能做渐进式重哈希。没有这一列的话唯一的迁移办法是让全员重设密码。
	Algo string `json:"algo" gorm:"type:varchar(16);not null;default:''"`

	// Hash 是慢哈希后的摘要。`json:"-"` 是硬要求 —— 本结构体会被管理端接口
	// 间接触碰,一次疏忽的 c.JSON(row) 就是把全站支付密码摘要发出去。
	Hash string `json:"-" gorm:"type:varchar(128);not null;default:''"`

	// FailCount 是连续输错次数,由**单条原子 UPDATE** 自增(见 counter.go)。
	// 绝不能读-改-写:同一账号并发打 N 个请求时那样只会算 1 次,
	// 锁定阈值形同虚设。
	FailCount int `json:"fail_count" gorm:"not null;default:0"`

	// LockedUntil 是锁定截止的 unix 秒,0 表示未锁定。
	LockedUntil int64 `json:"locked_until" gorm:"not null;default:0"`

	// SetAt 是首次设置时间;ChangedAt 是最近一次修改(改密/找回/管理员重置)时间。
	// 两者分开是为了让审计能回答"这个密码是什么时候被换掉的"。
	SetAt     int64 `json:"set_at" gorm:"not null;default:0"`
	ChangedAt int64 `json:"changed_at" gorm:"not null;default:0"`

	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (PayPassword) TableName() string { return "qy_pay_passwords" }

// isSet 表示这一行确实承载了一个可用的密码。
//
// 判据是 Hash 非空而不是"行存在":管理员重置采用的是**清空 Hash 但保留行**
// (见 api_admin.go 的说明),行存在并不等于密码存在。
func (p PayPassword) isSet() bool { return p.Hash != "" }

// lockedAt 判断在 now 这一刻是否处于锁定期内。
func (p PayPassword) lockedAt(now int64) bool { return p.LockedUntil > now }
