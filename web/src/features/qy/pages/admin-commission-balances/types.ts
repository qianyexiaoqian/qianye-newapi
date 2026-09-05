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
 * 佣金余额总览 DTO。对应 `qianye/modules/commission/api_admin_balance.go`
 * （D-16：账本改记**星屑**）。
 *
 * ── 三个星屑列的关系 ──
 * 它们不是三个独立的数字，受同一条恒等式约束：
 *
 *   可用 + 已入账 = 累计已结算 − 累计冲正
 *
 * `derived_available` 与 `ledger_drift` 由**后端**算好下发，前端一个字都
 * 不重算：这条恒等式在后端已经被结算 / 冲正 / 自动入账三条路径各实现了一遍，
 * 让前端再实现第四遍就是在等它漂移。
 *
 * D-15 时中间还有一列「入账中(frozen)」：佣金记星辉、入账跨库，存在"已开单、
 * 主库还没落定"的中间态。D-16 入账是扩展库里的本地事务，那一列不再存在。
 *
 * 所有金额都是**星屑整数**，走 `QySdAmount` —— 这张表上没有法币、也没有额度。
 */
export type QyCommissionBalance = {
  user_id: number
  /** 主库里的用户名。读不到时为空串，同时 `user_resolved` 为 false。 */
  username: string
  /** 假值表示主库里查不到这个 id（账号已删，或这一次主库读失败）。 */
  user_resolved: boolean

  /** 已成熟、等自动入账攒够门槛的星屑。 */
  available: number
  /** 累计已自动记入星屑余额的部分。 */
  credited: number
  total_earned: number
  total_clawback: number

  /** 已结算 − 已冲正 − 已入账，即恒等式给出的可用。 */
  derived_available: number
  /** 实际可用 − 派生可用。非 0 = 账本漂移，改钱之前必须先查清楚。 */
  ledger_drift: number

  /** decimal(30,10) 字符串，转 number 会丢位。 */
  unsettled_amount: string

  /** 冲正欠账。为真时自动入账跳过这个人，直到后续佣金把欠账抵完。 */
  debt_blocked: boolean
  invitee_count: number
  last_settled_at: number
  updated_at: number
}

/** 列表页的合计，跟着当前筛选条件走。 */
export type QyCommissionBalanceTotals = {
  available: number
  credited: number
}

export type QyCommissionBalancePage = {
  items: QyCommissionBalance[]
  total: number
  p: number
  page_size: number
  totals: QyCommissionBalanceTotals
}

/**
 * 手工增减佣金的返回。
 *
 * `delta` 是**实际落账**的那个数：幂等重放时是 0，`created` 同时为假。
 * `reclaimable_ceiling` 是后端在持锁事务里算出来的扣减上限，回显给前端用于
 * 校准提示——弹窗里那个上限来自列表快照，可能已经过时。
 */
export type QyAdjustCommissionResult = {
  user_id: number
  delta: number
  created: boolean
  accrual_no: string
  reclaimable_ceiling: number
  before: QyCommissionBalance
  after: QyCommissionBalance
}

/**
 * 列表排序口径，与后端 `balanceSortOrders` 的键逐字一致。
 *
 * `credited` 顶替了此前的 `withdrawn`（列改名，排序键跟着改）—— Z1 若沿用
 * 旧键名，这里要跟着改回去，否则"选了没反应"。
 */
export const QY_BALANCE_SORTS = [
  'available',
  'credited',
  'earned',
  'updated',
  'user',
] as const

export type QyBalanceSort = (typeof QY_BALANCE_SORTS)[number]

/**
 * 排序项 → 文案键。走查表而不是模板串：`available` 那一项的旧文案写着「可提现」，
 * 现在的口径是「可用（待入账）」，而旧键留在主包里不能改，只能换键。
 */
export const QY_BALANCE_SORT_LABEL_KEY: Readonly<
  Record<QyBalanceSort, string>
> = {
  available: 'qy_cb_sort_available_xh',
  credited: 'qy_cb_sort_credited',
  earned: 'qy_cb_sort_earned',
  updated: 'qy_cb_sort_updated',
  user: 'qy_cb_sort_user',
}
