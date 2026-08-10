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
 * 佣金余额总览 DTO。对应 `qianye/modules/commission/api_admin_balance.go`。
 *
 * ── 四个额度列的关系 ──
 * 它们不是四个独立的数字，受同一条恒等式约束：
 *
 *   可提现 + 冻结中 + 已提现 = 累计已结算 − 累计冲正
 *
 * `derived_available_quota` 与 `ledger_drift` 由**后端**算好下发，前端一个字都
 * 不重算：这条恒等式在后端已经被结算 / 冲正 / 提现三条路径各实现了一遍，
 * 让前端再实现第四遍就是在等它漂移。
 */
export type QyCommissionBalance = {
  user_id: number
  /** 主库里的用户名。读不到时为空串，同时 `user_resolved` 为 false。 */
  username: string
  /** 假值表示主库里查不到这个 id（账号已删，或这一次主库读失败）。 */
  user_resolved: boolean

  available_quota: number
  frozen_quota: number
  withdrawn_quota: number
  total_earned_quota: number
  total_clawback_quota: number

  /** 已结算 − 已冲正 − 冻结中 − 已提现，即恒等式给出的可提现。 */
  derived_available_quota: number
  /** 实际可提现 − 派生可提现。非 0 = 账本漂移，改钱之前必须先查清楚。 */
  ledger_drift: number

  /** decimal(30,10) 字符串，转 number 会丢位。 */
  unsettled_amount: string
  available_fiat: string

  debt_blocked: boolean
  invitee_count: number
  last_settled_at: number
  updated_at: number
}

/** 列表页的合计，跟着当前筛选条件走。 */
export type QyCommissionBalanceTotals = {
  available_quota: number
  withdrawn_quota: number
}

export type QyCommissionBalancePage = {
  items: QyCommissionBalance[]
  total: number
  p: number
  page_size: number
  totals: QyCommissionBalanceTotals
}

/** 迁移编辑的返回：`delta_quota` 是**实际**划转额，重复提交时是 0。 */
export type QySetWithdrawnResult = {
  user_id: number
  delta_quota: number
  before: QyCommissionBalance
  after: QyCommissionBalance
}

/**
 * 手工增减佣金的返回。
 *
 * `delta_quota` 是**实际落账**的那个数：幂等重放时是 0，`created` 同时为假。
 * `reclaimable_ceiling` 是后端在持锁事务里算出来的扣减上限，回显给前端用于
 * 校准提示——弹窗里那个上限来自列表快照，可能已经过时。
 */
export type QyAdjustCommissionResult = {
  user_id: number
  delta_quota: number
  created: boolean
  accrual_no: string
  reclaimable_ceiling: number
  before: QyCommissionBalance
  after: QyCommissionBalance
}

/** 列表排序口径，与后端 `balanceSortOrders` 的键逐字一致。 */
export const QY_BALANCE_SORTS = [
  'available',
  'withdrawn',
  'earned',
  'updated',
  'user',
] as const

export type QyBalanceSort = (typeof QY_BALANCE_SORTS)[number]
