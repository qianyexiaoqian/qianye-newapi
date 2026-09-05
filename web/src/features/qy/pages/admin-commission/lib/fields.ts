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
 * 可编辑的运营参数元数据（D-15 从 git HEAD 恢复，删掉法币折算那一档）。
 *
 * key 必须与后端 `commission/settings.go` 的常量逐字一致：它同时是
 * `qy_settings.k` 与 PUT 请求体的字段名。后端还有一份 `editableKeys` 白名单，
 * 传了白名单外的键会整个请求 400，所以这里只登记元数据，**渲染哪些字段由
 * 接口返回的 `editable_keys` 决定** —— 后端收窄白名单时前端自动跟随。
 */
/**
 * 星屑门槛的上界 = 后端 `common.MaxQuota`（`common/quota_math.go`）。
 *
 * 佣金 D-16 之后记星屑，但上界仍是这一个：`stardust.Credit` 用的也是
 * `common.MaxQuota`，两套账本共用同一条算术边界。
 *
 * # 为什么不能是 Number.MAX_SAFE_INTEGER
 *
 * 结算金额本身在后端已经被 `common.QuotaFromDecimalChecked` 夹在 `common.MaxQuota`
 * 内，所以一个超过它的门槛不是"更宽松"，是**永远无法被满足**。最坏的一个具体
 * 形状：把「最小结算额度」填成一个越界的数（多敲几个零就够），`net < minSettle`
 * 从此恒成立，net 恒为 0 —— 全站所有邀请人的佣金永远不再落账，不报错、不告警、
 * 没有日志，未结算额一路累加。自动入账的门槛同理。
 */
export const QY_MAX_QUOTA = 8_796_093_022_208

export type QyCommissionFieldMeta = {
  labelKey: string
  hintKey: string
  /**
   * `percent` 百分比（最多两位小数）；`stardust` 星屑整数；`plain` 纯计数。
   *
   * D-16 之前这里是 `quota`（站内额度），而额度对运营来说不是一个能直接填的数
   * （500000 = $1），所以那时金额字段按 USD 录入、再换算回额度。星屑本身就是
   * 运营和用户都直接读的整数，那一整层换算连同它的换算率可用性判定一起去掉了。
   */
  unit: 'percent' | 'plain' | 'stardust'
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
  min_settle_stardust: {
    labelKey: 'qy_cm_f_min_settle',
    hintKey: 'qy_cm_f_min_settle_hint',
    // 后端校验 `v <= 0` 直接 400，下限必须是 1。
    unit: 'stardust',
    min: 1,
    max: QY_MAX_QUOTA,
  },
  max_per_order_stardust: {
    labelKey: 'qy_cm_f_max_per_order',
    hintKey: 'qy_cm_f_max_per_order_hint_xh',
    unit: 'stardust',
    min: 0,
    max: QY_MAX_QUOTA,
    zeroMeansUnlimited: true,
  },
  holding_days: {
    labelKey: 'qy_cm_f_holding_days',
    hintKey: 'qy_cm_f_holding_days_hint_xh',
    unit: 'plain',
    min: 0,
    max: 365,
  },
  // ── 自动入账 ──
  // 门槛是星屑：可用余额攒到它才会被下一轮记入星屑余额；与 min_settle_stardust
  // 是两道门（前者管"攒到多少才发出去"，后者管"计佣攒到多少才落成余额"）。
  min_credit_stardust: {
    labelKey: 'qy_cm_f_min_credit',
    hintKey: 'qy_cm_f_min_credit_hint',
    unit: 'stardust',
    min: 1,
    max: QY_MAX_QUOTA,
  },
  // 入账任务的心跳周期（秒）。它不是"多久到账一次"的承诺：每一轮只处理够门槛的
  // 余额行，调小它只是让够门槛的人早几分钟看到星屑到账。
  credit_interval_seconds: {
    labelKey: 'qy_cm_f_credit_interval',
    hintKey: 'qy_cm_f_credit_interval_hint',
    unit: 'plain',
    min: 30,
    max: 86400,
  },
  max_daily_stardust_per_inviter: {
    labelKey: 'qy_cm_f_daily_cap',
    hintKey: 'qy_cm_f_unlimited_hint',
    unit: 'stardust',
    min: 0,
    max: QY_MAX_QUOTA,
    zeroMeansUnlimited: true,
  },
  large_accrual_alert_stardust: {
    labelKey: 'qy_cm_f_large_alert',
    hintKey: 'qy_cm_f_large_alert_hint',
    unit: 'stardust',
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

/** 去掉尾随零，与后端 `FormatRatePercent` 的输出形状对齐（"10.250" → "10.25"）。 */
export function qyNormalizePercent(raw: string): string {
  const s = raw.trim()
  if (!qyIsValidPercent(s)) return s
  if (!s.includes('.')) return String(Number(s))
  const trimmed = s.replace(/0+$/, '').replace(/\.$/, '')
  return String(Number(trimmed))
}
