package invite

import "github.com/QuantumNous/new-api/model"

// installHooks 把实现注入上游 model 包的 hook 变量。
// 调用时机在 qianye.Init(),早于任何 HTTP 请求与后台协程。
func installHooks() {
	// 兑换码事件的单槽变量由本包占着,再转发给星屑(见 AfterRedeemSuccess)。
	// 本包自己对这个事件已经没有任何动作 —— 保留这一层只是为了让占槽方与
	// 转发槽的所有权在一个地方说清楚。
	model.QyOnRedeemSuccess = onRedeemSuccess
	// 账号分组变了就失效那一条邀请缓存。分组决定这个人作为推广人时的星屑
	// 档位,而档位会被**冻结**进流水 —— 晚生效五分钟不是"晚五分钟看到",
	// 是那五分钟的星屑永久按旧档发出去了。
	model.QyOnUserGroupChanged = invalidateInviter
}

// onRedeemSuccess 是余额兑换码兑换成功的入口,只做转发。
//
// 不设任何早退:这里没有本包自己的开关可判(invite.enabled 由星屑侧的
// InviteeEligible 在真正发放前判),多一道闸只会让星屑的兑换码返多一个
// 无人知晓的关闭条件。位置由 qianye/stardust_hookpoint_guard_test.go 钉住。
func onRedeemSuccess(userId int, redemptionId int, quota int) {
	AfterRedeemSuccess(userId, redemptionId, quota)
	// 星辉佣金(D-15)是并行的第二条线,各自判各自的开关。
	AfterRedeemSuccessCommission(userId, redemptionId, quota)
}
