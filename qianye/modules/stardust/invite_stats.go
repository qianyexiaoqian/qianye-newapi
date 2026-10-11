package stardust

import (
	"context"

	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/modules/invite"
)

// invite_stats.go —— 回答 invite 模块留的 PairRewardTotals 槽(export.go 的 func 变量):
// 按 (邀请人, 下线) 汇总邀请人在**星屑账本**上因这个下线直接得到的星屑。
//
// 下线充值 / 兑换码 / 消费带来的返还只走推广佣金,那一半由 commission 注入的
// PairCommissionTotals 回答;这里只剩与佣金基数不重叠的注册奖 / 套餐返上线,以及已下线的
// 星屑侧下线返留下的历史流水(已发放的不回收,所以照常计入)。
//
// invite 不能 import 本包(qianye/module_import_guard_test.go),所以由本包在 InstallHooks 里赋值。

func init() {
	hookInstallers = append(hookInstallers, func() {
		invite.PairRewardTotals = pairRewardTotals
	})
}

// inviteKinds 是"邀请人因下线在星屑账本上得到"的全部流水种类:用户端「我的推广」的
// 星屑口径与上面那个槽都取它。前三种已下线、不再产生新行,留在这里是为了历史流水
// 照常计入、照常可查。
var inviteKinds = []string{
	string(KindInviteConsume), string(KindInviteTopup), string(KindInviteRedeem),
	string(KindInviteRegister), string(KindPlanInviter),
}

// retiredInviteKinds 是已下线的三种星屑侧下线返。界面上把它们合成一行「历史下线返」,
// 不再与佣金并排各报一个"下线消费返",那正是要消灭的冲突统计。
var retiredInviteKinds = []string{
	string(KindInviteConsume), string(KindInviteTopup), string(KindInviteRedeem),
}

// pairRewardTotals 按 (user_id=邀请人, peer_user_id=下线) 汇总邀请类流水。
//
// 历史上的 invite_consume 是按邀请人汇总发的,peer_user_id 恒 0,按下线拆不开 ——
// 它只出现在「我的推广」的合计里,不出现在这里。
// 读失败返回空表而不是错误:调用方要么已经改完了主库,要么只是在渲染一个列表,
// 都不该为一个回显数字失败。
func pairRewardTotals(ctx context.Context, pairs [][2]int) map[[2]int]int64 {
	out := make(map[[2]int]int64, len(pairs))
	if len(pairs) == 0 {
		return out
	}
	gdb := db.Get()
	if gdb == nil {
		return out
	}
	wanted := make(map[[2]int]bool, len(pairs))
	invitees := make([]int, 0, len(pairs))
	seen := make(map[int]bool, len(pairs))
	for _, p := range pairs {
		wanted[p] = true
		if !seen[p[1]] {
			seen[p[1]] = true
			invitees = append(invitees, p[1])
		}
	}
	var rows []struct {
		UserId     int
		PeerUserId int
		Total      int64
	}
	if err := gdb.WithContext(ctx).Model(&Ledger{}).
		Select("user_id, peer_user_id, COALESCE(SUM(amount), 0) AS total").
		Where("peer_user_id IN ? AND kind IN ?", invitees, inviteKinds).
		Group("user_id, peer_user_id").Scan(&rows).Error; err != nil {
		db.MarkFailure(err)
		return out
	}
	for _, r := range rows {
		key := [2]int{r.UserId, r.PeerUserId}
		if wanted[key] {
			out[key] += r.Total
		}
	}
	return out
}
