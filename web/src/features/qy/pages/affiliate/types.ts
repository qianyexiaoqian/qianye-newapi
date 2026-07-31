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
 * 返佣看板 DTO。对应 `qianye/modules/commission/api_user.go`。
 *
 * **凡是 decimal 的字段后端一律以 string 下发**（`unsettled_amount`、
 * `pending_mature_quota`、`available_fiat`、`gross_amount`…）：`decimal(30,10)`
 * 超出 JS `number` 的精确表达范围，解析成数字再显示会丢位。前端只做展示，
 * 不参与任何运算。
 */
export type QyCommissionSummary = {
  invitee_count: number
  /** 已成熟、可直接提现的佣金额度。 */
  available_quota: number
  /** 已被提现单冻结、等待审核/打款的部分。 */
  frozen_quota: number
  withdrawn_quota: number
  total_earned_quota: number
  total_clawback_quota: number
  /** 不足 1 额度的精确余数，用来解释"我用了一天怎么没佣金"。 */
  unsettled_amount: string
  /** 已计佣但还没过成熟期的部分。 */
  pending_mature_quota: string
  /** 冲正欠账。为 true 时后端会拒绝一切提现。 */
  debt_blocked: boolean
  available_fiat: string
  fiat_currency: string
  last_settled_at: number
  rate: {
    topup_bps: number
    consume_bps: number
  }
  policy: {
    holding_days: number
    min_settle_quota: number
    settle_interval_seconds: number
    exclude_redemption: boolean
    exclude_subscription: boolean
  }
}

/**
 * 已邀请用户。
 *
 * `masked_name` **已由后端脱敏**（`commission/mask.go`），前端不得再处理。
 * 后端刻意不下发下线的 `user_id` / 邮箱 —— 邀请返佣不是获取他人隐私的授权，
 * 对外标识只有不可逆的 `ref`。
 */
export type QyInvitee = {
  ref: string
  masked_name: string
  bound_at: number
  total_base_quota: number
  /** decimal 字符串。 */
  total_commission: string
  blocked: boolean
}

/** 我的佣金流水。 */
export type QyCommissionRecord = {
  accrual_no: string
  /** `topup` | `redemption` | `consume` | `clawback`。 */
  source_type: string
  /** 已脱敏的来源单号（只留后 4 位）。 */
  source_ref: string
  invitee_ref: string
  invitee_masked_name: string
  base_quota: number
  rate_bps: number
  gross_amount: string
  settled_amount: string
  /** `accrued` | `settled` | `risk_hold` | `voided`。 */
  status: string
  mature_at: number
  bucket_date: string
  created_at: number
}
