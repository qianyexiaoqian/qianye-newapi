// Package invite 维护邀请关系(谁是谁的上线)与它的共享设施。
//
// 它曾经是返佣模块(commission):账本、结算、佣金余额、法币折算、冲正、提现全部
// 挂在这里。D-14 之后邀请人的**全部**收益只有星屑(qianye/modules/stardust),
// 本包退回到只回答三个问题:
//
//  1. 这个下线现在归谁、能不能给他上线返东西(InviteeEligible / InviteMatch);
//  2. 「一天」从哪一秒开始(dayline.go,星屑日结与下线日消费都用它);
//  3. 管理端怎么看、怎么改一条邀请关系(绑定 / 换绑 / 解绑 / 拉黑)。
//
// 依赖方向只有一个:stardust → invite。本包不 import stardust;stardust 需要反向
// 通知的地方(兑换码事件、按下线汇总的星屑)一律走本包导出的 func 变量(export.go)。
package invite

// InviteRelation 是邀请关系快照。
//
// # 它不是权威,users.inviter_id 才是
//
// 「谁是这个人的邀请人」永远只问主库的 users.inviter_id(resolveInviter → peekInviter),
// 本表只是**懒建**的展示快照:ensureRelation 在某个下线第一次被解析到上线时才写这一行。
// 因此本表的行数天然少于真实的绑定数,拿它当"关系列表"的数据源会漏掉绝大多数关系
// (管理端列表一律从主库出,见 api_admin_relation.go)。
//
// 存在的意义是"已邀请用户列表"这个页面:脱敏名在服务端算好并缓存在这里,
// 列表页零主库访问,也就不存在把真实用户名/邮箱漏给邀请人的可能。
//
// 项目没有独立的邀请绑定时间列(users.inviter_id 是注册时一次性写入),
// 所以自动建出来的行 BoundAt 只能取 users.created_at;管理员手工绑定的行
// 取绑定发生的那一刻 —— 两者都是"这条关系是什么时候成立的"这个事实。
type InviteRelation struct {
	InviteeId int `json:"invitee_id" gorm:"primaryKey"`
	InviterId int `json:"inviter_id" gorm:"not null;index:idx_qy_ir_inviter"`

	MaskedName string `json:"masked_name" gorm:"type:varchar(64);not null;default:''"`
	// InviteeRef 是对外唯一标识,绝不下发 user_id —— 那等于把用户 id 空间
	// 暴露给任何一个邀请人。
	InviteeRef string `json:"invitee_ref" gorm:"type:varchar(16);not null;uniqueIndex:uk_qy_ir_ref"`

	BoundAt int64 `json:"bound_at" gorm:"not null;default:0"`

	// UnboundAt 是管理员解绑这条关系的时刻,0 表示仍然绑定中。
	//
	// 解绑**不删除本行**:星屑流水里 peer_user_id 指着这个 invitee_id,删掉快照会让
	// 那些流水在用户端失去脱敏名 —— 钱还在账上,却说不清是谁挣的。
	// 真正让这条关系停止返星屑的是主库 users.inviter_id 被清零;本列只是把
	// "曾经绑过、什么时候解的"这个事实留在扩展库里,供管理端反查。
	UnboundAt int64 `json:"unbound_at" gorm:"not null;default:0;index:idx_qy_ir_unbound"`

	// RiskFlags 是**自动风控**写的标记(目前只有 reciprocal_invite:互邀环路)。
	// 它由 ensureRelation 在建快照时算出,此后不该被任何人工动作覆盖 ——
	// 覆盖掉的话关系页上那个徽标显示的就是人写的话,而"这条关系是系统
	// 自动判定为互刷的"这个事实再也看不到了。人工停止/恢复的事由写 BlockReason。
	RiskFlags string `json:"risk_flags" gorm:"type:varchar(255);not null;default:''"`
	// Blocked 由管理员设置,置位后该关系不再产生任何邀请返。已发放的星屑不回收。
	Blocked bool `json:"blocked" gorm:"not null"`
	// BlockReason 是管理员最近一次停止/恢复填的事由。
	//
	// 零值语义:空串 = 没有填过事由(或这条关系从未被人工停过),**不是**
	// "事由是空的"。它与 Blocked 是两件事 —— 恢复时事由照样留下,
	// 因为"为什么恢复"与"为什么停"同样是事后要查的。
	BlockReason string `json:"block_reason" gorm:"type:varchar(255);not null;default:''"`

	CreatedAt int64 `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt int64 `json:"updated_at" gorm:"not null;default:0"`
}

func (InviteRelation) TableName() string { return "qy_invite_relation" }
