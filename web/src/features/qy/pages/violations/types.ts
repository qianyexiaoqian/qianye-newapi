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
 * 威慑价值大于泄露价值：知道「再违规 2 次就封号」的用户会主动收敛，
 * 不知道的只会在被封之后来发工单。
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
