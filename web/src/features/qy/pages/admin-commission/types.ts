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
 * 佣金管理端 DTO。对应 `qianye/modules/commission/api_admin.go`。
 *
 * 生效配置的 key 与后端 `settings.go` 的常量逐字一致 —— 它们同时是
 * `qy_settings` 表里的行键与 PUT 请求体的字段名，改一个字就写不进去。
 */
export type QyCommissionEffective = {
  topup_rate_bps: number
  consume_rate_bps: number
  min_settle_quota: number
  max_per_order_quota: number
  holding_days: number
  max_daily_quota_per_inviter: number
  large_accrual_alert_quota: number
  min_invitee_age_hours: number
}

/**
 * YAML 只读段。
 *
 * 这些开关涉及安全与启动行为（是否计佣、排除哪些口径、扫描周期），
 * 只能改文件后重载。管理端展示它们是为了让人看到"当前到底跑在什么口径下"，
 * 而不是让人以为可以在这里改。
 */
export type QyCommissionYamlReadonly = {
  enabled: boolean
  topup_rate_bps: number
  consume_rate_bps: number
  exclude_redemption_and_manual: boolean
  exclude_subscription_consume: boolean
  refund_clawback: boolean
  settle_interval_seconds: number
  topup_scan_interval_seconds: number
  topup_scan_lookback_hours: number
  inviter_cache_seconds: number
}

export type QyCommissionAdminConfig = {
  effective: QyCommissionEffective
  /** `qy_settings` 里的运营覆盖，值一律是字符串。 */
  overrides: Record<string, string>
  editable_keys: string[]
  yaml_readonly: QyCommissionYamlReadonly
}

/** 管理端计佣流水。后端直接回 `qy_commission_accrual` 原始行。 */
export type QyAdminAccrual = {
  id: number
  accrual_no: string
  idem_scope: string
  idem_key: string
  inviter_id: number
  invitee_id: number
  source_type: string
  source_ref: string
  base_quota: number
  base_money: string
  rate_bps: number
  gross_amount: string
  settled_amount: string
  usd_rate: string
  status: string
  risk_flags: string
  mature_at: number
  bucket_date: string
  remark: string
  created_at: number
}
