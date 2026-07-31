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
 * 可编辑的运营参数元数据。
 *
 * key 必须与后端 `commission/settings.go` 的常量逐字一致：它同时是
 * `qy_settings.k` 与 PUT 请求体的字段名。后端还有一份 `editableKeys` 白名单，
 * 传了白名单外的键会整个请求 400，所以这里只登记元数据，**渲染哪些字段由
 * 接口返回的 `editable_keys` 决定** —— 后端收窄白名单时前端自动跟随。
 */
export type QyCommissionFieldMeta = {
  labelKey: string
  hintKey: string
  /** `bps` 万分比整数；`quota` 站内额度；`plain` 纯计数。 */
  unit: 'bps' | 'plain' | 'quota'
  min: number
  max: number
  /** 0 是否表示"不限"。是的话要在输入框旁提示，否则运营会以为填 0 等于关掉。 */
  zeroMeansUnlimited?: boolean
}

export const QY_COMMISSION_FIELDS: Record<string, QyCommissionFieldMeta> = {
  topup_rate_bps: {
    labelKey: 'qy_cm_f_topup_rate',
    hintKey: 'qy_cm_f_bps_hint',
    unit: 'bps',
    min: 0,
    max: 10000,
  },
  consume_rate_bps: {
    labelKey: 'qy_cm_f_consume_rate',
    hintKey: 'qy_cm_f_bps_hint',
    unit: 'bps',
    min: 0,
    max: 10000,
  },
  min_settle_quota: {
    labelKey: 'qy_cm_f_min_settle',
    hintKey: 'qy_cm_f_min_settle_hint',
    // 后端校验 `v <= 0` 直接 400，下限必须是 1。
    unit: 'quota',
    min: 1,
    max: Number.MAX_SAFE_INTEGER,
  },
  max_per_order_quota: {
    labelKey: 'qy_cm_f_max_per_order',
    hintKey: 'qy_cm_f_unlimited_hint',
    unit: 'quota',
    min: 0,
    max: Number.MAX_SAFE_INTEGER,
    zeroMeansUnlimited: true,
  },
  holding_days: {
    labelKey: 'qy_cm_f_holding_days',
    hintKey: 'qy_cm_f_holding_days_hint',
    unit: 'plain',
    min: 0,
    max: 365,
  },
  max_daily_quota_per_inviter: {
    labelKey: 'qy_cm_f_daily_cap',
    hintKey: 'qy_cm_f_unlimited_hint',
    unit: 'quota',
    min: 0,
    max: Number.MAX_SAFE_INTEGER,
    zeroMeansUnlimited: true,
  },
  large_accrual_alert_quota: {
    labelKey: 'qy_cm_f_large_alert',
    hintKey: 'qy_cm_f_large_alert_hint',
    unit: 'quota',
    min: 0,
    max: Number.MAX_SAFE_INTEGER,
    zeroMeansUnlimited: true,
  },
  min_invitee_age_hours: {
    labelKey: 'qy_cm_f_min_invitee_age',
    hintKey: 'qy_cm_f_min_invitee_age_hint',
    unit: 'plain',
    min: 0,
    max: 8760,
  },
}

export function qyCommissionFieldMeta(
  key: string
): QyCommissionFieldMeta | null {
  return QY_COMMISSION_FIELDS[key] ?? null
}
