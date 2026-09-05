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
import type {
  QyStardustAccrualRow,
  QyStardustAccrualStatus,
  QyStardustHoldReason,
  QyStardustLedgerKind,
  QyStardustLedgerRow,
} from '../stardust/types'

/**
 * 星屑账本（管理端）的 DTO。与契约 §3 逐字对应，实施期的三处偏差也已经收进来
 * （`ledger-check` 多 `ok` / `error`、`settle/status` 独立成一条、重跑多两个计数）。
 * 流水与日桶的行形状与用户端相同、只多一个 `user_id`，所以直接从用户端类型派生。
 */

/** 手调请求体（RootActionGate：超级管理员专属）。 */
export type QyStardustAdjustInput = {
  user_id: number
  /** 带符号、≠ 0。扣到负会被 400 `qy_sd_insufficient`。 */
  delta: number
  /** ≥ 4 个字符。 */
  reason: string
  /** 每次打开弹窗生成一次、重试沿用（幂等键）。 */
  client_request_id: string
}

export type QyStardustAdjustResult = {
  ledger_no: string
  user_id: number
  delta: number
  balance_after: number
  replayed: boolean
}

export type QyStardustBalanceRow = {
  user_id: number
  username: string
  available: number
  total_earned: number
  total_spent: number
  total_refunded: number
  total_adjusted: number
  carry: string
  hold_reason: QyStardustHoldReason
  updated_at: number
}

export type QyStardustBalancesParams = QyPageParams & {
  user_id?: number
  keyword?: string
}

export type QyStardustAdminLedgerRow = QyStardustLedgerRow & {
  user_id: number
}

export type QyStardustAdminLedgerParams = QyPageParams & {
  user_id?: number
  kind?: QyStardustLedgerKind
  act_no?: string
}

export type QyStardustAdminAccrualRow = QyStardustAccrualRow & {
  user_id: number
}

export type QyStardustAdminAccrualsParams = QyPageParams & {
  user_id?: number
  bucket_date?: string
  status?: QyStardustAccrualStatus
}

/** 最近一次结算运行（`qy_sd_settle_run` 的一行）。字段与后端 `settleStatus` 逐字一致。 */
export type QyStardustSettleRun = {
  run_date: string
  target_date: string
  /** `running` / `done` / `partial` / `failed`… 以后端为准，界面只原样显示并按 done 上色。 */
  status: string
  attempts: number
  started_at: number
  finished_at: number
  processed: number
  failed: number
  held: number
  granted: number
  remark: string
}

/**
 * `GET /stardust/settle/status`。
 *
 * `next_settle_at` 只由后端下发：今天这一跑已 done 就指向明天，否则指向今天的
 * 门槛（已过门槛而未 done 时它在过去，界面据此显示"进行中 / 待重试"）。
 * `error` 只在扩展库读不到时出现，其余字段仍然有效。
 */
export type QyStardustSettleStatus = {
  last_run: QyStardustSettleRun | null
  next_settle_at: number
  target_day: string
  run_date: string
  ready: boolean
  max_attempts: number
  error?: string
}

/**
 * `GET /stardust/ledger-check`。
 *
 * 契约字段之外多 `ok` / `error`：读失败时后端明说"这次没查成"而不是 500，
 * 界面上要展示 `error` 而不是把零值当成"全部正常"。`worst_drift` 是 decimal
 * 字符串（I2 那条恒等式比的是零头），只展示不运算。
 */
export type QyStardustLedgerCheck = {
  ok: boolean
  error: string
  checked_users: number
  drifted_users: number
  worst_user_id: number
  worst_drift: string
  held_rows: number
  oldest_held_day: string
  held_alert: boolean
}

export type QyStardustSettleRerunResult = {
  day: string
  recomputed: number
  settled: number
  held: number
  skipped: number
  failed: number
  /** 这一跑真正发出去的星屑总数。 */
  granted: number
}
