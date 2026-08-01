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
 * 可编辑的划转门槛元数据。
 *
 * key 必须与后端 `transfer/settings.go` 的常量逐字一致：它同时是
 * `qy_settings.k` 与 PUT 请求体的字段名。后端还有一份 `editableKeys` 白名单，
 * 传了白名单外的键会整个请求 400，所以这里只登记元数据，**渲染哪些字段由
 * 接口返回的 `editable_keys` 决定** —— 后端收窄白名单时前端自动跟随。
 *
 * # 这里刻意没有 min / max
 *
 * 取值区间由接口的 `bounds` 下发。后端 `settingBounds` 是唯一定义，读取路径
 * （防手工改库）、写入校验与这里的输入框校验共用它。前端再抄一份的话，
 * 后端收紧区间的那天界面还在放行，运营会得到一个没有任何字段级提示的 400。
 */
export type QyTransferFieldMeta = {
  labelKey: string
  hintKey: string
  /**
   * 单位，只影响输入框右侧的后缀与数值的呈现方式。
   * `quota` 站内额度；`bps` 万分比；其余是纯计数/时长。
   */
  unit: 'bps' | 'count' | 'hours' | 'minutes' | 'quota' | 'seconds'
  /**
   * 0 是否表示「不限」。是的话要在输入框旁提示，否则运营会以为填 0 等于关掉。
   *
   * 这一位还有第二个后果：日额度/日次数配成 0 时，**用户端的「今日剩余」
   * 整块不会显示**（展示「剩余 0」会被理解成已经用光了）。见 hint 文案。
   */
  zeroMeansUnlimited?: boolean
}

export const QY_TRANSFER_FIELDS: Record<string, QyTransferFieldMeta> = {
  min_quota: {
    labelKey: 'qy_tc_f_min_quota',
    hintKey: 'qy_tc_f_min_quota_hint',
    unit: 'quota',
    zeroMeansUnlimited: true,
  },
  max_per_tx_quota: {
    labelKey: 'qy_tc_f_max_per_tx',
    hintKey: 'qy_tc_f_max_per_tx_hint',
    unit: 'quota',
  },
  daily_max_quota: {
    labelKey: 'qy_tc_f_daily_quota',
    hintKey: 'qy_tc_f_daily_zero_hint',
    unit: 'quota',
    zeroMeansUnlimited: true,
  },
  daily_max_count: {
    labelKey: 'qy_tc_f_daily_count',
    hintKey: 'qy_tc_f_daily_zero_hint',
    unit: 'count',
    zeroMeansUnlimited: true,
  },
  cooldown_seconds: {
    labelKey: 'qy_tc_f_cooldown',
    hintKey: 'qy_tc_f_cooldown_hint',
    unit: 'seconds',
    zeroMeansUnlimited: true,
  },
  receiver_daily_max_in_count: {
    labelKey: 'qy_tc_f_receiver_in',
    hintKey: 'qy_tc_f_receiver_in_hint',
    unit: 'count',
    zeroMeansUnlimited: true,
  },
  new_account_freeze_hours: {
    labelKey: 'qy_tc_f_freeze_hours',
    hintKey: 'qy_tc_f_freeze_hours_hint',
    unit: 'hours',
    zeroMeansUnlimited: true,
  },
  fee_bps: {
    labelKey: 'qy_tc_f_fee_bps',
    hintKey: 'qy_tc_f_fee_bps_hint',
    unit: 'bps',
    zeroMeansUnlimited: true,
  },
  fee_min_quota: {
    labelKey: 'qy_tc_f_fee_min',
    hintKey: 'qy_tc_f_fee_min_hint',
    unit: 'quota',
  },
  pay_pwd_max_attempts: {
    labelKey: 'qy_tc_f_pwd_attempts',
    hintKey: 'qy_tc_f_pwd_attempts_hint',
    unit: 'count',
  },
  pay_pwd_lock_minutes: {
    labelKey: 'qy_tc_f_pwd_lock',
    hintKey: 'qy_tc_f_pwd_lock_hint',
    unit: 'minutes',
  },
}

export function qyTransferFieldMeta(key: string): QyTransferFieldMeta | null {
  return QY_TRANSFER_FIELDS[key] ?? null
}

/**
 * 校验一个整数输入是否落在给定闭区间内。
 *
 * `bound` 为 `null`（接口没下发这个键的区间）时只要求「是个非负整数」：
 * 编不出区间就放行到后端，让后端那份权威校验去拒绝，而不是在前端凭空
 * 造一个上界 —— 那正是「第 N 份拷贝各自漂移」的起点。
 */
export function qyIsValidSettingValue(
  raw: string,
  bound: { min: number; max: number } | null
): boolean {
  const s = raw.trim()
  if (!/^\d+$/.test(s)) return false
  const value = Number(s)
  if (!Number.isSafeInteger(value)) return false
  if (bound == null) return true
  return value >= bound.min && value <= bound.max
}

/** 万分比转百分比字符串，用于 `fee_bps` 的旁注（500 → "5"）。 */
export function qyBpsToPercentText(bps: number): string {
  if (!Number.isFinite(bps)) return '0'
  return String(bps / 100)
}
