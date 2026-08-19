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
/**
 * 额度门槛的上界 = 主库额度列的上界（`users.quota` 是 int32，
 * 后端 `common.MaxQuota` 就是这个数）。
 *
 * # 为什么不能是 Number.MAX_SAFE_INTEGER
 *
 * 结算金额本身在后端已经被 `common.QuotaFromDecimalChecked` 夹在 int32 内，
 * 所以一个超过它的门槛不是"更宽松"，是**永远无法被满足**。最坏的一个具体形状：
 * 把「最小结算额度」填成 5000000000（按 USD 录入之后只要敲 5 位数），
 * `net < minSettle` 从此恒成立，net 恒为 0 —— 全站所有邀请人的佣金永远不再落账，
 * 不报错、不告警、没有日志，未结算额一路累加。
 *
 * 划转与抽奖两页的这条路早就被后端下发的 bounds 堵住了，只有佣金页没有；
 * 本轮把上界补在两边：这里（界面立刻标红）与后端校验/读取回落（真正说了算的）。
 */
export const QY_MAX_QUOTA = 2147483647

/**
 * 法币折算比例的上界，与后端 `maxFiatRateValue` 逐字一致。
 *
 * 一百万足以覆盖任何真实币种（越南盾、印尼盾在两万五量级），同时把手滑
 * 多敲几个零的后果限制成"一个一眼能看出来的离谱数字"。
 */
export const QY_MAX_FIAT_RATE = 1000000

/** 法币折算比例的最大小数位，与后端存储列 `decimal(18,8)` 一致。 */
export const QY_FIAT_RATE_DECIMALS = 8

export type QyCommissionFieldMeta = {
  labelKey: string
  hintKey: string
  /**
   * `percent` 百分比（最多两位小数）；`quota` 站内额度；`plain` 纯计数；
   * `fiat_rate` 法币折算比例。
   *
   * `fiat_rate` 必须与 `percent` 分开：它是一个**乘数**（`7.3` = 一美元折
   * 7.3 元），区间是 `(0, 1000000]`、最多 8 位小数，而且 0 是非法值不是
   * "免费"。当成百分比渲染的话，输入框旁边会多一个 `%`，运营填 7.3
   * 就以为自己配的是 7.3% —— 差两个数量级的资金参数。
   */
  unit: 'percent' | 'plain' | 'quota' | 'fiat_rate'
  min: number
  max: number
  /** 0 是否表示"不限"。是的话要在输入框旁提示，否则运营会以为填 0 等于关掉。 */
  zeroMeansUnlimited?: boolean
}

export const QY_COMMISSION_FIELDS: Record<string, QyCommissionFieldMeta> = {
  topup_rate_percent: {
    labelKey: 'qy_cm_f_topup_rate',
    hintKey: 'qy_cm_f_percent_hint',
    unit: 'percent',
    min: 0,
    max: 100,
  },
  consume_rate_percent: {
    labelKey: 'qy_cm_f_consume_rate',
    hintKey: 'qy_cm_f_percent_hint',
    unit: 'percent',
    min: 0,
    max: 100,
  },
  redemption_rate_percent: {
    labelKey: 'qy_cm_f_redemption_rate',
    hintKey: 'qy_cm_f_redemption_rate_hint',
    unit: 'percent',
    min: 0,
    max: 100,
  },
  fiat_rate_default: {
    labelKey: 'qy_cm_f_fiat_rate_default',
    hintKey: 'qy_cm_f_fiat_rate_default_hint',
    unit: 'fiat_rate',
    // 下界写 0 只是元数据上的形式：真正的判定在 qyIsValidFiatRate 里，
    // 那里 0 是**非法**的（0 不是"免费"，见 fields 顶部的说明）。
    min: 0,
    max: QY_MAX_FIAT_RATE,
  },
  min_settle_quota: {
    labelKey: 'qy_cm_f_min_settle',
    hintKey: 'qy_cm_f_min_settle_hint',
    // 后端校验 `v <= 0` 直接 400，下限必须是 1。
    unit: 'quota',
    min: 1,
    max: QY_MAX_QUOTA,
  },
  max_per_order_quota: {
    labelKey: 'qy_cm_f_max_per_order',
    hintKey: 'qy_cm_f_unlimited_hint',
    unit: 'quota',
    min: 0,
    max: QY_MAX_QUOTA,
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
    max: QY_MAX_QUOTA,
    zeroMeansUnlimited: true,
  },
  large_accrual_alert_quota: {
    labelKey: 'qy_cm_f_large_alert',
    hintKey: 'qy_cm_f_large_alert_hint',
    unit: 'quota',
    min: 0,
    max: QY_MAX_QUOTA,
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

/** 百分比的最大小数位。与后端 `config.RatePercentUnits` 的判定一致。 */
export const QY_PERCENT_DECIMALS = 2

/**
 * 校验一个百分比输入。
 *
 * 刻意不用 `Number()` 判小数位：`Number('10.005')` 之后就再也看不出原始
 * 写了几位小数了。这里对字面量做正则，与后端"超过两位小数直接拒绝、
 * 不四舍五入"的口径逐字对齐 —— 前端悄悄替运营把 10.005 变成 10.01，
 * 就是一次没有人签字的加薪。
 */
export function qyIsValidPercent(raw: string): boolean {
  const s = raw.trim()
  if (!/^\d+(\.\d{1,2})?$/.test(s)) return false
  const value = Number(s)
  return Number.isFinite(value) && value >= 0 && value <= 100
}

/**
 * 校验一个**可空**百分比输入：空是合法的，含义是"没单独配，跟随充值档"。
 *
 * 单独一个函数而不是给 `qyIsValidPercent` 加参数：调用点必须一眼看出
 * 这里的空到底算不算数。兑换码档是全仓唯一一个"空有含义"的费率输入框，
 * 而 0% 又恰好是一个合法配置 —— 两者混起来的代价是一整档费率。
 */
export function qyIsValidNullablePercent(raw: string): boolean {
  return raw.trim() === '' || qyIsValidPercent(raw)
}

/**
 * 校验一个**法币折算比例**输入。区间 `(0, 1000000]`，最多 8 位小数。
 *
 * 与 `qyIsValidPercent` 分成两个函数而不是加参数，理由与可空百分比那一对
 * 完全相同：调用点必须一眼看出这里的 0 和空到底算不算数。
 *
 * # 0 与空都是非法
 *
 * `0` 不是"免费/不折算"：它会让后端 `applyFiat` 一分法币都不加而额度照加，
 * `available_fiat` 与 `available_quota` 从此永久漂移，提现按前者折算会给
 * 用户 0 元，而他的站内佣金余额明明是正的。想停掉某个分组的佣金，
 * 既有的杠杆是把返佣费率设成 0%（两侧都停在 0，账是平的）。
 *
 * 空串同理不是"清掉这一档"：兜底档不可清空（清空之后没配分组档的用户会
 * 悄悄退回充值页汇率，而界面上还写着兜底档）。后端两处都直接 400，
 * 前端这里先标红，免得运营点了保存才知道。
 */
export function qyIsValidFiatRate(raw: string): boolean {
  const s = raw.trim()
  // 与后端 parseFiatRate 逐条对齐：正则先卡形状与小数位（不用 Number()
  // 判小数位，`Number('0.000000001')` 之后就再也看不出原始写了几位了）。
  if (!new RegExp(`^\\d+(\\.\\d{1,${QY_FIAT_RATE_DECIMALS}})?$`).test(s)) {
    return false
  }
  const value = Number(s)
  return Number.isFinite(value) && value > 0 && value <= QY_MAX_FIAT_RATE
}

/**
 * 规范化一个法币折算比例，与后端 `decimal.Decimal.String()` 的输出形状对齐
 * （`"7.30"` → `"7.3"`、`"007"` → `"7"`）。非法输入原样返回。
 *
 * 全程字符串运算，**绝不经过 `Number()`**：`String(Number('0.00000001'))`
 * 会得到 `'1e-8'`，那既提交不上去，也会让"改了没改"的比较永远判成改了。
 */
export function qyNormalizeFiatRate(raw: string): string {
  const s = raw.trim()
  if (!qyIsValidFiatRate(s)) return s
  const [rawInt, rawFrac = ''] = s.split('.')
  const int = rawInt.replace(/^0+(?=\d)/, '')
  const frac = rawFrac.replace(/0+$/, '')
  return frac === '' ? int : `${int}.${frac}`
}

/** 去掉尾随零，与后端 `FormatRatePercent` 的输出形状对齐（"10.250" → "10.25"）。 */
export function qyNormalizePercent(raw: string): string {
  const s = raw.trim()
  if (!qyIsValidPercent(s)) return s
  if (!s.includes('.')) return String(Number(s))
  const trimmed = s.replace(/0+$/, '').replace(/\.$/, '')
  return String(Number(trimmed))
}
