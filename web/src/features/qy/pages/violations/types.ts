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
/**
 * 用户端违规视图。
 *
 * 字段集由后端**白名单**构造（`userRecordView`），刻意不含命中词、命中片段、
 * 内部规则名、rule_id、IP、渠道等信息 —— 那些等于把规则库送给刷子。
 * 前端不要试图从别的接口把这些补回来。
 */

export type QyMyViolationRecord = {
  id: number
  created_at: number
  model_name: string
  /** 规则的**对外文案**，不是内部规则名。 */
  reason: string
  /**
   * 违规类型的**公示标题**（命中当时冻结的那一份）。
   *
   * 不是类型的内部名，也不是现查类型表：类型被归档或改名之后，这一条记录仍要
   * 显示当时那个名字。该类型当时未公示时后端给空串。
   */
  category: string
  blocked: boolean
  fee_quota: number
  fee_status: string
  status: 'active' | 'appealed' | 'revoked' | (string & {})
  counter_after: number
}

/**
 * 「当前窗口违规几次、还差几次会被封号」。
 *
 * ── 整个类型现在都是**后端下发但用户端不渲染**的 ──
 *
 * 项目方原话：「我的违规记录，这里只显示违规类型就行。把窗口违规次数、距离封号
 * 还剩余、累计扣费这些移除掉吧。」`pages/violations/index.tsx` 因此不再调用
 * `getQyMyViolationSummary`，这一页上没有任何一个字段来自这里。
 *
 * **这不是漏渲染，别当成 bug 修回去。** 下一个人打开这一页会发现"后端明明给了
 * 这些数、界面上一个都没有"，最省事的直觉就是补一块统计回去 —— 那正是被要求
 * 拿掉的东西。要加回来先去问项目方。
 *
 * ── 为什么后端不一起删 ──
 *
 *  1. 这些字段是**管理端排障与将来恢复的依据**：`remaining_line` / `remaining_*`
 *     那一组编码的是"两条线 OR、取最先到达的那条"这个判定口径，它与封号判定
 *     （`nearestThresholdLine` / `anyReached`）同源。删接口字段会把改动带到那条
 *     链上，而那条链决定的是"这个账号会不会被封"。
 *  2. 风险不对等：留着的代价是几个没人读的 JSON 字段；删错的代价是动了封号判定。
 *
 * ── 用户还能从哪知道自己快被封了 ──
 *
 * 只剩公示卡片（`QyMyViolationCategoriesCard`）：它逐条给出账号总量线与每一个
 * **已公示**类型的「我几次 / 到几次 / 到了会怎样 / 还差几次」。站点一个类型都
 * 没勾公示时那张卡整块收起 —— 那种配置下用户不再有任何封号预警。这是移除三块
 * 统计的已知代价。
 *
 * 「因违规被扣了多少钱」没有丢：每次扣费都写一条主库计费日志
 *（`qianye/modules/violation/fee.go` 的 `writeConsumeLog`，内容为
 * 「违规扣费：<对外原因>」），用户在原生用量日志页逐笔可查；本页记录表也保留了
 * 单条的「扣费」列。丢掉的只是"累计"这一个合计数。
 */
export type QyMyViolationSummary = {
  hit_count: number
  window_hours: number
  /** **账号总量线**的阈值。它不是"离处置最近的那条线"，见 `remaining`。 */
  ban_threshold: number
  /**
   * 离处置最近的那条线还差几次 —— **跨两条线取最小值**，不是账号总量线的余量。
   *
   * 处置由 OR 判定触发（账号总量线与单类型线，任一越过即触发），所以用户真正
   * 会被处置的时点由**最先到达的那条线**决定。这个字段一度只算账号总量线，
   * 于是在"账号线 10、某一类 3"这种普通配置下，页面头条写着"还剩 8 次"，
   * 而用户下一次命中就被封了 —— 那不是少给信息，是给了一个反向的信息。
   *
   * 只在 `remaining_line !== 'none'` 时有意义。
   */
  remaining: number
  /**
   * `remaining` 落在哪条线上：`none` = 当前一条生效的线都没有。
   *
   * **不要再用 `ban_threshold > 0` 判断要不要显示倒计时**：账号线关着、
   * 某一类开着 3 次是完全合法的配置，那种站点上 `ban_threshold` 是 0，
   * 而用户离封号只有 3 次。
   */
  remaining_line?: 'none' | 'account' | 'category'
  /**
   * 最近那条线**自己的**三个数。
   *
   * `window_hours` / `ban_threshold` 描述的始终是账号总量线。只有那两个数时，
   * 一个被类型线封掉的人看到的是「触发线：类型」配上「阈值 0、窗口 24 小时」，
   * 而真正把他封掉的那条线是「阈值 2、不限期限」—— 一句话里混着两条线的数字，
   * 用户无从看出。这三个数存在就是为了消掉那种混淆。
   *
   * 谁要是把「还差几次」重新渲染出来，用的必须是这三个，不能回头拿
   * `window_hours` / `ban_threshold`。当前用户端一个都不渲染，见类型顶部注释。
   */
  remaining_threshold?: number
  remaining_window_hours?: number
  remaining_hit_count?: number
  /** 这个账号此刻是不是正被违规系统封着。已封的人不该再看到倒计时。 */
  banned?: boolean
  total_fee_quota: number
  /** 达到阈值之后会发生什么：record / restrict / ban。 */
  policy_action?: string
}

/**
 * 公示出来的一个违规类型。
 *
 * 字段集由后端**白名单**构造（`userCategoryView`），刻意不含类型的内部名、
 * 内部说明与类型标识 —— 内部说明写的就是匹配判据，公示它等于教人绕过。
 * 前端不要试图从别的接口把这些补回来。
 */
export type QyMyViolationCategory = {
  id: number
  /** 对外标题。后端保证：勾了公示就一定有值。 */
  title: string
  description: string
  /** 这一类累计多少次会触发处置。0 = 这一类不单独触发。 */
  threshold: number
  window_hours: number
  hit_count: number
  remaining: number
}

/**
 * 违规类型公示 + 自己在每一类上的计数。
 *
 * 两条线一起返回是刻意的：用户会撞的线有两条 —— 账号总量线（跨全部类型）与
 * 单类型线，**任一越过即触发**。只显示其中一条会让"到底几次"在另一条线上失真，
 * 而失真的方向是"我以为还剩 5 次，结果第 3 次就被限制了"。
 */
export type QyMyViolationCategories = {
  items: QyMyViolationCategory[]
  account_threshold: number
  account_hit_count: number
  account_window_hours: number
  policy_action: string
  /**
   * 这个账号此刻是不是正被违规系统封着。
   *
   * 达到门槛的**那一刻**账号就已经被处置了（判定用的是"已达"而不是
   * "恰好跨越"），所以 `remaining === 0` 不等于"下一次才会被封"。
   * 这两种状态要分开说：已经被封的人需要的是申诉入口，不是一句还没发生的预告。
   */
  banned?: boolean
  /** `any_line` = 两条线是 OR。口径由后端下发，前端不要写死在文案里。 */
  threshold_semantics: string
}
