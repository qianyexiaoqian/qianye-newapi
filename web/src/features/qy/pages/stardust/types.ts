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
import type { QyPageParams } from '../../lib/types'

/**
 * 星屑（用户端）的 DTO。与契约 `stardust-api-contract.md` §2 逐字对应。
 *
 * 金额一律**整数星屑**；`carry` / `gross` 是 decimal，后端以**字符串**下发以免
 * JS 丢位，前端只展示、不运算。时间戳 unix 秒。
 */

/** 余额被冻结的原因。空串 = 正常。 */
export type QyStardustHoldReason =
  | ''
  | 'account_disabled'
  | 'account_removed'
  | 'overdraft'
  | (string & {})

export type QyStardustBalance = {
  available: number
  total_earned: number
  total_spent: number
  total_refunded: number
  total_adjusted: number
  /** 不足 1 星屑的结转余数（decimal 字符串）。 */
  carry: string
  hold_reason: QyStardustHoldReason
}

/** 日桶状态：已算出 / 已结算入账 / 被扣住（见 `hold_reason`）。 */
export type QyStardustAccrualStatus =
  | 'computed'
  | 'held'
  | 'settled'
  | (string & {})

/** 昨天那一桶（`GET /stardust/me` 的 `yesterday`；没有消费时为 `null`）。 */
export type QyStardustYesterday = {
  bucket_date: string
  /** 计提基数，单位是**额度**（不是星屑）：它是消费返的分母。 */
  base_quota: number
  gross: string
  status: QyStardustAccrualStatus
  settled_amount: number
  hold_reason: string
  rate_bps: number
  quota_per_unit: number
}

export type QyStardustMe = {
  /** 运营配的货币名。展示单位统一走 `useStardustName()`，不要直接读这里。 */
  name: string
  quota_per_unit: number
  balance: QyStardustBalance
  /** 下一次结算时刻（后端算好：nextDayStart(now) + settle_delay）。 */
  next_settle_at: number
  yesterday: QyStardustYesterday | null
  pending_held_count: number
}

/** 流水类型，对齐契约 §2 的 kind 枚举。 */
export type QyStardustLedgerKind =
  | 'commission_credit'
  | 'consume_rebate'
  | 'invite_consume'
  | 'invite_register'
  | 'invite_redeem'
  | 'invite_topup'
  | 'lot_prize'
  | 'lot_refund'
  | 'lot_stake'
  | 'mall_order'
  | 'mall_refund'
  | 'manual'
  | 'plan_buyer'
  | 'plan_inviter'
  | (string & {})

export type QyStardustLedgerRow = {
  ledger_no: string
  kind: QyStardustLedgerKind
  /** 带符号：入账为正、扣减为负。 */
  amount: number
  balance_after: number
  ref_type: string
  ref_no: string
  act_no: string
  peer_user_id: number
  remark: string
  created_at: number
}

export type QyStardustLedgerParams = QyPageParams & {
  kind?: QyStardustLedgerKind
}

export type QyStardustAccrualRow = {
  bucket_date: string
  user_group: string
  rate_bps: number
  quota_per_unit: number
  base_quota: number
  gross: string
  status: QyStardustAccrualStatus
  hold_reason: string
  ledger_id: number
  computed_at: number
  settled_at: number
}
