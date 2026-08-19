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

/** 一行 = 一个用户在所选区间内的消费。与后端 `dailyConsumeRow` 逐字对应。 */
export type QyDailyConsumeRow = {
  user_id: number
  username: string
  display_name: string
  email: string
  user_group: string
  request_count: number
  /**
   * 区间内**真实扣掉**的额度，口径是主库 `logs` 的 `type=2`。
   *
   * 它与下面的 `commission_base_quota` 不是同一个数，而且**天然更大** ——
   * 差在哪一列由 `uncounted_quota` 回答，原因由页面上那段说明回答。
   */
  consume_quota: number
  /** 同一区间进了计佣表的基数。0 且 `has_commission` 为 false = 一行计佣都没有。 */
  commission_base_quota: number
  /** `consume_quota − commission_base_quota`，即“消费了但没计佣”的那部分。 */
  uncounted_quota: number
  /** 计佣金额，`decimal(30,10)` 字符串。 */
  commission_gross: string
  has_commission: boolean
  inviter_id: number
  inviter_username: string
  /**
   * 这一行的账号在 users 里已经不在了（软删或被硬删）。
   *
   * logs 是永久的、users 不是：被删掉的账号的消费仍然在这张表里，而它的
   * `uncounted_quota` 恒等于全额消费额。页面上那段说明列了七条“为什么两个数
   * 不一样”，这是第八条，必须由行上的徽标直接说出来，否则运营按那七条去查
   * 一条都对不上。
   */
  account_removed: boolean
}

export type QyDailyConsumeRange = {
  start_date: string
  end_date: string
  days: number
  max_days: number
}

export type QyDailyConsumeSummary = {
  user_count: number
  request_count: number
  consume_quota: number
  commission_base_quota: number
  uncounted_quota: number
}

export type QyDailyConsumePage = {
  items: QyDailyConsumeRow[]
  total: number
  p: number
  page_size: number
  range: QyDailyConsumeRange
  summary: QyDailyConsumeSummary
  /**
   * 报表依赖的那条覆盖索引在不在。
   *
   * 为假时这张表会从秒级掉到分钟级（备份库实测 7 天区间从 1.5 秒退化到
   * 9 分钟以上），所以它必须显式出现在界面上 —— 否则运营只会觉得“今天有点卡”，
   * 而真正的原因是索引被删了。
   */
  index_ready: boolean
  /**
   * “计佣表里有、logs 里没有”的下线数。
   *
   * 正常恒为 0。不为 0 的唯一合理解释是日志保留期把那段消费清掉了，而计佣行
   * 是永久账本 —— 那时消费额一侧天然缺一块，必须让运营看见这个数字。
   */
  accrual_users_without_logs: number
}

/** 排序键。与后端 `dailyConsumeSorts` 的键集合一致。 */
export type QyDailyConsumeSort =
  | 'commission_base_quota'
  | 'consume_quota'
  | 'request_count'
  | 'uncounted_quota'
  | 'user_id'

/**
 * 按天下钻的一行 = 这个用户的某一天。与后端 `adminUserDailyConsume` 的
 * `items` 逐字对应。
 *
 * 区间内**每一天都有一行**，没消费的那天全是 0：缺行的表会让运营把
 * “这天没花钱”与“这天没查出来”看成同一件事，而这恰恰是他点开下钻要区分的。
 */
export type QyDailyConsumeByDayRow = {
  /** yyyymmdd。日界由后端的 `commission.day_offset_minutes` 决定，前端不自己算。 */
  date: string
  /** 该天日界的 unix 秒。排序与画图用它，不要拿 `date` 去 parse。 */
  day_start: number
  request_count: number
  consume_quota: number
  commission_base_quota: number
  uncounted_quota: number
  /** decimal 字符串。 */
  commission_gross: string
}

export type QyDailyConsumeByDayPage = {
  user_id: number
  items: QyDailyConsumeByDayRow[]
  range: QyDailyConsumeRange
  summary: {
    request_count: number
    consume_quota: number
    commission_base_quota: number
    uncounted_quota: number
    commission_gross: string
  }
  /**
   * 下钻**自己那条**覆盖索引在不在（`idx_qy_logs_user_daily`），
   * 与主表那条各建各的：主表快不代表下钻快。
   *
   * 为假时这条查询会从百毫秒掉到数秒（备份库实测 31 天区间 163ms → 6523ms），
   * 所以必须显式出现在界面上。
   */
  index_ready: boolean
}
