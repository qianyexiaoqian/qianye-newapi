/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package lottery

import (
	"fmt"
	"math"
	"net/http"

	"github.com/QuantumNous/new-api/logger"
)

// caps.go —— 额度闸门的零值语义、刻度换算,以及"净增发"的二次确认。
//
// # 这一段替换掉了什么
//
// 抽奖派奖对 users.quota 是**净增发**:平台凭空造钱给中奖者,没有回收路径。
// 原先的防线是一串硬拒绝(参与费 ≤ max_stake_quota、Σ(count×amount) ≤
// max_total_prize_quota、单注上限 ≤ max_stake_quota、告警阈值 ≤ 奖品上限)。
// 它们的毛病不是"拦得太狠",而是拦的方式没有信息量:
//
//   - 拒绝文案里的数字是**裸额度**(`不得超过 5000000`),而运营在界面上看到的
//     刻度是站内余额($10)。同一件事的两个数对不上号,人只会以为自己看错了 ——
//     "怎么在抽奖设置这里不能超过 100 站点余额"这句困惑就是从这里来的。
//   - 一次拒绝不说明"这笔配置会让平台发出去多少钱",只说"你超了"。真正会造成
//     资损的那次手滑(多写一个零)与一次正当的大活动,在这道闸门面前一模一样。
//   - 而且它拦不住手滑本身:把上限调大一格,同一个零照样发得出去。
//     调大只是把"卡半天"推迟到更大的数字上。
//
// 所以硬拒绝换成「不拦,但把金额念出来再确认一次」:
//
//   - 三个额度上限的 **0 一律是"不限制"**(与 checkActiveCap 早已成立的口径
//     一致),而且是默认值。配成正数才是站点自己立的硬顶,此后在线只能调低。
//   - 奖品总额达到 large_prize_alert_quota 时,创建/改草稿必须回显那个**精确
//     金额**(confirm_net_issue_quota),否则 400。阈值配成 0 = 连确认都不要。
//
// # 为什么是"回显金额"而不是一个 confirmed=true
//
// 布尔会被一个默认 true 的表单、一段抄来的 curl、或者一次 JSON 模板复制**永久
// 按住** —— 那样它第一次就退化成一个恒真的字段,而恒真的确认等于没有确认。
// 回显要求调用方手里已经有那个数,而那个数只有看过界面(或自己把
// Σ(count×amount) 算过一遍)才有。它拦不住一个铁了心的脚本,但它拦的从来不是
// 脚本,是手滑。

// quotaText 把一个额度渲染成运营在界面上看到的那个刻度。
//
// 拒绝文案里的裸额度是本次改造要消灭的东西:配置里的 5000000 与界面上的 $10
// 是同一件事的两个写法,而运营手里只有后者。换算直接借 logger.LogQuota
// (签到、余额划转、本模块的派奖账本行都走它),不另写一份 —— 两份换算迟早
// 会在"站点改了展示币种"那天分叉,而分叉的表现是一句自相矛盾的报错。
func quotaText(q int64) string {
	// int 在本项目支持的全部目标平台上都是 64 位;payout.go 的账本行同样这么转。
	return logger.LogQuota(int(q))
}

// netIssueOverflowGuard 是 Σ(count × amount) 的**算术**护栏,不是业务上限。
//
// max_total_prize_quota 变成"默认不限"之后,这条累加就没有任何配置项夹着了:
// count 的上界是 max_total_entries_hard、amount 的上界是 int32,两者都能被
// YAML 配得离谱。int64 一旦绕回负数,后面每一道 `total > x` 的判定会**全部
// 通过**,而一个负的总额还会让二次确认的回显判定变成"回显 0 即可" ——
// 溢出在这里不是崩溃,是静默放行。
//
// 取 int64 上界的 1/1024:约 9.0e15 额度,按默认刻度是一百八十亿美元,
// 任何真实活动都够不着,而它离溢出还留着三个数量级的余量。
const netIssueOverflowGuard = int64(math.MaxInt64) / 1024

// requireNetIssueConfirm 是"这场活动最坏会发出多少站内余额"的二次确认。
//
// # 两个 0 的语义不同,必须一起读
//
//   - threshold == 0 —— 完全不打扰。任何金额都直接放行,连日志都不多写一行。
//     这是给"我知道我在干什么,别再拦我"的站点准备的那一档。
//   - echoed == 0 —— 请求里没带确认字段。它**永远不等于**一个已经越过阈值的
//     正数总额,所以"没填"的结果是被拒绝而不是被放行。
//     零值在这里必须落在安全的一侧,否则一个漏传字段的旧客户端会安静地绕过
//     整道闸门。
//
// total < threshold 的活动一个字都不问 —— 这是"不卡人"的全部实现:
// 默认阈值 500 万额度($10),一场手动测试的小活动碰不到它。
func requireNetIssueConfirm(threshold, total, echoed int64) error {
	if threshold <= 0 || total < threshold {
		return nil
	}
	if echoed == total {
		return nil
	}
	return netIssueConfirmRequired(total, threshold)
}

// netIssueConfirmRequired 造一条把**金额写出来**的拒绝。
//
// 文案里必须同时出现三件事:会发出去多少、发出去还能不能收回、以及"回填这个数
// 就能继续" —— 少了第三件,这条 400 读起来仍然是一道拦路的硬上限,运营的下一个
// 动作就又变成"去哪儿把这个限制调大"。
func netIssueConfirmRequired(total, threshold int64) *bizError {
	return &bizError{
		Status: http.StatusBadRequest,
		Code:   codeNetIssueConfirm,
		Msg: fmt.Sprintf(
			"这场活动最多可能发出 %s(二次确认阈值 %s)。抽奖派奖是对用户余额的净增发,"+
				"发出后没有任何回收路径 —— 请确认没有多写一个零,"+
				"并把 confirm_net_issue_quota 回填成 %d 后重新提交",
			quotaText(total), quotaText(threshold), total),
	}
}
