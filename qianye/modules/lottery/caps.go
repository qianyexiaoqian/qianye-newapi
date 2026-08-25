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

	"github.com/QuantumNous/new-api/common"
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

// quotaColumnCeilingText 是「填不了」那一档的**统一**说法。
//
// # 那个 ＄4294.967294 到底是什么
//
// 它是 common.MaxQuota(math.MaxInt32 = 2147483647)按 common.QuotaPerUnit
// (默认 500000)换算出来的刻度。MaxQuota 是**全站额度换算的整数上界**:
// common/quota_math.go 里每一处 quota 转换、饱和、四舍五入都以它为界,
// 所有计费与账本口径都按它立的。它写死在代码里,没有任何配置项能抬高它。
//
// 这里刻意**不**说它是"数据库那一列的物理宽度"。上一版是这么写的,而那句话
// 经不起查:model/user.go 上的 `gorm:"type:int"` 在 MySQL 与 PostgreSQL 上
// 落地成 bigint,SQLite 的 INTEGER 也是 8 字节 —— 运营一去查表就会发现每一列
// 都是 64 位的,然后连带不再相信整条解释。给一个经不起查的理由比不给理由更糟。
//
// 它与站点自己配的那两道硬顶(max_stake_quota / max_total_prize_quota)
// 在文案上必须分得开:
//
//   - 系统上界 = "填不了"。改任何配置、改任何开关都放不开它。
//   - 策略上限 = "本站不让"。去配置页改一个数,或者把活动的数字调小。
//
// 混在一起说的表现是运营拿着一句"不得超过系统上限"跑去配置页找一个根本不
// 存在的开关 —— 项目方那句「这是什么问题?」问的就是这件事。
//
// 句子的顺序也是刻意的:**先说填多少,再说为什么**。原先那几条报错整句都在
// 解释后果,唯独没说该填什么,于是每一次都要"填 → 被拒 → 读一段 → 再填"。
func quotaColumnCeilingText(field string) string {
	return fmt.Sprintf("%s请填 %s 以内。这一档是全站额度换算的整数上界"+
		"(common.MaxQuota,由代码写死),不是本站的策略上限 —— 改任何配置都放不开它",
		field, quotaText(int64(common.MaxQuota)))
}

// tierBudgetFloor 是「这一档的单份至少要填多少」,由**其它字段推出来**。
//
// 判据是 count × amount ≥ entriesCap(超募时该档预算由全部中签者均分,
// 人均不足 1 额度会有人分到 0,而 PlanPayouts 会跳过 amount<=0 的计划 ——
// 一个真中了奖的人被静默漏发)。这条不等式只有三个量,给定其中两个,第三个
// 就是**算出来的**,没有任何需要运营去猜的余地。
//
// 前端 lib/advice.ts 的 qyLotTierAmountFloor 是同一个式子的第二份实现,
// 两边都用向上取整 —— 向下取整会得到一个"界面说 OK、后端拒绝"的推荐值,
// 而那比不给推荐值更糟。
func tierBudgetFloor(count, entriesCap int) int64 {
	if count <= 0 || entriesCap <= 0 {
		return 0
	}
	return (int64(entriesCap) + int64(count) - 1) / int64(count)
}

// tierCountFloor 是同一条不等式的另一个解:单份已经定死时,份数至少要几份。
func tierCountFloor(amount int64, entriesCap int) int64 {
	if amount <= 0 || entriesCap <= 0 {
		return 0
	}
	return (int64(entriesCap) + amount - 1) / amount
}

// tierBudgetShort 是那条被项目方点名的报错(「一堆这种…很烦啊」)的新形态。
//
// 概率制与双色球共用它 —— 两处此前各写了一份几乎一样的格式串,而分叉的表现是
// 同一条规则在两种玩法上说两句不一样的话,运营会以为是两条不同的规则。
//
// 三个刻度在句子里必须分得清:单份是**钱**(quotaText 自带" 额度"后缀)、
// 份数是**份**、全场参与上限是**张票**。给票数缀一个"额度"会让人照着这句话
// 往错的方向调参。
func tierBudgetShort(tier, count int, amount int64, entriesCap int) *bizError {
	return errBadRequest(fmt.Sprintf(
		"奖级 %d 的单份请填 %s 以上,或者把数量改成 %d 份以上。"+
			"判据是「数量 × 单份 ≥ 全场参与上限 %d 张票」,当前是 数量 %d × 单份 %s —— "+
			"不满足时超募会有中奖者被摊薄到 0 而拿不到钱",
		tier, quotaText(tierBudgetFloor(count, entriesCap)),
		tierCountFloor(amount, entriesCap), entriesCap, count, quotaText(amount)))
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
