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
 * 管理端配置页的「额度 ↔ USD」精确换算。
 *
 * # 为什么不复用 `lib/format.ts` 的 `parseQuotaFromDollars`
 *
 * 那一对函数是**展示**用的：它按站点当前展示币种折算，内部是
 * `Math.round(usd / exchangeRate * quotaPerUnit)` —— 一次浮点乘法加一次取整。
 * 用来渲染流水里的金额没问题（差一个 quota 没人看得见），用来编辑**门槛配置**
 * 就不行：
 *
 *  1. 门槛存的是整数额度，界面换算成 USD 再换回来必须**逐位相同**。差一个
 *     quota，运营什么都没改点一下保存，审计里就多出一条无中生有的门槛变更 ——
 *     而门槛变更正是「谁把日额度从 2 亿改成 20 亿」要追的那条线索。
 *  2. 浮点乘法真的会差。`0.29 * 500000` 在 IEEE754 下是 144999.99999999997，
 *     取整口径稍有不同就落到 144999。AGENTS.md 的计费不变量对后端禁掉了这种
 *     写法，前端这条录入通道是同一笔钱的另一端，没有理由用更松的口径。
 *  3. 展示币种可能是 CNY / 自定义币种，那条链路上还有一个**浮点汇率**
 *     （`usdExchangeRate`）。乘一个 7.2 再除回来，往返一致性根本无从谈起。
 *     所以本模块固定用 USD，不看 `quotaDisplayType` —— 项目方要的也正是 USD。
 *
 * # 口径
 *
 * 全程整数运算，一次浮点都不做:
 *
 *  - 内部单位是**微元**（micro-USD，1e-6 USD）。USD 文本按字符串拆成
 *    「整数部分 + 补齐到 6 位的小数部分」直接拼成微元整数，不经过
 *    `Number('0.29') * 1e6`。
 *  - `额度 = 微元 / microsPerQuota`，其中 `microsPerQuota = 1e6 / quotaPerUnit`。
 *  - 除不尽 → 这个 USD 金额在当前换算率下**表示不成整数额度**，直接判非法
 *    并由界面提示，**不四舍五入**。悄悄替运营把 0.000001 变成 0 或 1 个额度，
 *    与「悄悄替运营把 10.005 的费率变成 10.01」是同一类事故。
 *  - 小数位上限 6 位（{@link QY_USD_DECIMALS}），超出直接判非法而不是截断。
 *
 * # 换算率不整除时整页退回额度单位
 *
 * `quotaPerUnit` 是站点可配置的。只有 `1e6 % quotaPerUnit === 0` 时，任意额度
 * 整数才都能用 6 位小数的 USD **无损**表示（默认值 500000 满足：1 额度 =
 * 0.000002 USD）。不满足时 {@link qyUsdScale} 返回 `usable: false`，界面必须
 * 退回原来的额度整数录入 —— 显示一个四舍五入过的 USD，等于让运营在一个
 * 已经失真的数字上点保存。
 */

/** USD 输入允许的最大小数位。1e-6 USD 是本模块的最小刻度。 */
export const QY_USD_DECIMALS = 6

const MICROS_PER_USD = 1_000_000

/** 当前站点换算率下的换算参数。由 {@link qyUsdScale} 从 `quotaPerUnit` 推出。 */
export type QyUsdScale = {
  /**
   * 用于换算的 1 USD 折合额度数。**`usable` 为 false 时是 0**，
   * 因为那时它不是一个能拿来做算术的值。
   *
   * 要展示给人看的换算率一律用 {@link QyUsdScale.configuredRate} ——
   * 这两个字段刻意分开，理由见那一条。
   */
  quotaPerUnit: number
  /** 1 额度折合多少微元。`usable` 为 false 时是 0。 */
  microsPerQuota: number
  /** 当前换算率下 USD 与额度能否互相无损表示。false 时界面退回额度单位。 */
  usable: boolean
  /**
   * 站点**实际配置**的换算率原值，只用于展示；站点没有配出一个正数时为 `null`。
   *
   * # 为什么不能拿 quotaPerUnit 去显示
   *
   * 换算率不可用时 `quotaPerUnit` 被归零，而顶部那条红色告警的原话是
   * "站点配置的是 1 USD = {rate} 额度，这个换算率没法把每个额度值都无损写成
   * USD"。拿归零后的值去填，这句话就变成 "1 USD = 0 额度" —— 运营拿着这个 0
   * 去系统设置里找，那里显示的是 2.5，没有任何一处能解释这个 0 从哪来，
   * 而这条告警本来的作用恰恰是告诉他「去把换算率改成 1e6 的因数」。
   *
   * 整页唯一一处必须说真话的汇率数字，原来在这条分支上说的是假话。
   *
   * 触发它不需要什么异常操作：系统设置里 QuotaPerUnit 的 schema 是
   * `z.coerce.number().min(0)` + `step='0.01'`，没有 `.int()`，
   * 历史站点或直接写 option 都能得到 2.5 / 500000.5。
   */
  configuredRate: number | null
}

/** 从站点的 `quotaPerUnit` 推出换算参数。 */
export function qyUsdScale(
  quotaPerUnit: number | null | undefined
): QyUsdScale {
  // 展示口径：只要是个正的有限数就照实说，哪怕它不是整数、也不整除 1e6。
  const configuredRate =
    typeof quotaPerUnit === 'number' &&
    Number.isFinite(quotaPerUnit) &&
    quotaPerUnit > 0
      ? quotaPerUnit
      : null
  // 换算口径：必须是正的安全整数，且能整除 1e6，否则一律不可用。
  const per =
    typeof quotaPerUnit === 'number' &&
    Number.isSafeInteger(quotaPerUnit) &&
    quotaPerUnit > 0
      ? quotaPerUnit
      : 0
  if (per === 0 || MICROS_PER_USD % per !== 0) {
    return { quotaPerUnit: 0, microsPerQuota: 0, usable: false, configuredRate }
  }
  return {
    quotaPerUnit: per,
    microsPerQuota: MICROS_PER_USD / per,
    usable: true,
    configuredRate,
  }
}

/**
 * 额度整数 → USD 文本（不带符号，去掉尾随零：15000 → `"0.03"`）。
 *
 * 换算率不可用或额度不是合法的非负安全整数时返回 `null`。
 */
export function qyQuotaToUsdText(
  quota: number,
  scale: QyUsdScale
): string | null {
  if (!scale.usable) return null
  if (!Number.isSafeInteger(quota) || quota < 0) return null
  const micros = quota * scale.microsPerQuota
  if (!Number.isSafeInteger(micros)) return null

  // 纯字符串切分，不做 `micros / 1e6` —— 那是一次浮点除法。
  const digits = String(micros).padStart(QY_USD_DECIMALS + 1, '0')
  const whole = digits.slice(0, -QY_USD_DECIMALS).replace(/^0+(?=\d)/, '')
  const frac = digits.slice(-QY_USD_DECIMALS).replace(/0+$/, '')
  return frac === '' ? whole : `${whole}.${frac}`
}

/**
 * USD 文本 → 额度整数。非法输入返回 `null`，调用方负责报错。
 *
 * 判非法（而不是取整）的三种情况：写法不是十进制非负数、小数位超过
 * {@link QY_USD_DECIMALS}、以及在当前换算率下除不尽整数额度。
 */
export function qyUsdTextToQuota(
  raw: string,
  scale: QyUsdScale
): number | null {
  if (!scale.usable) return null
  const s = raw.trim()
  if (!/^\d+(?:\.\d{1,6})?$/.test(s)) return null

  const dot = s.indexOf('.')
  const whole = dot === -1 ? s : s.slice(0, dot)
  const frac = dot === -1 ? '' : s.slice(dot + 1)
  const micros = Number(whole + frac.padEnd(QY_USD_DECIMALS, '0'))
  if (!Number.isSafeInteger(micros)) return null
  if (micros % scale.microsPerQuota !== 0) return null
  return micros / scale.microsPerQuota
}

/**
 * 只读展示用：额度 → `"$0.03"`。换算不可用时原样回退成额度整数，
 * 让页面永远有东西可显示，而不是空一格。
 */
export function qyFormatQuotaAsUsd(quota: number, scale: QyUsdScale): string {
  const text = qyQuotaToUsdText(quota, scale)
  return text == null ? String(quota) : `$${text}`
}

/**
 * 存储额度 → 配置页输入框文本。
 *
 * `asUsd` 为 false 是纯整数通道（笔数、秒数、万分比这些本来就不是钱）。
 *
 * 与 {@link qyQuotaDraftValue} 严格互逆：这一对函数就是「界面显示 USD →
 * 运营不改直接保存 → 存回的额度逐位相同」这条不变量的落点，三个配置页
 * 共用它们，测试也钉在这里。
 *
 * `asUsd` 为 true 而额度换不出 USD 时回退成额度整数字面量。这一支在实际
 * 取值域里够不着（额度上界是主库的 int32，乘最大的 microsPerQuota 仍在安全
 * 整数内，见测试里的全域往返用例），留着只是不想让一个越界的脏值把整页
 * 卡成只读；它也不会静默存错 —— 那串数字随后会被当成 USD 解析，超出
 * 区间后界面直接标红。
 */
export function qyQuotaDraftText(
  quota: number,
  scale: QyUsdScale,
  asUsd: boolean
): string {
  if (!asUsd) return String(quota)
  return qyQuotaToUsdText(quota, scale) ?? String(quota)
}

/** 配置页输入框文本 → 存储额度。非法返回 `null`。 */
export function qyQuotaDraftValue(
  raw: string,
  scale: QyUsdScale,
  asUsd: boolean
): number | null {
  if (asUsd) return qyUsdTextToQuota(raw, scale)
  const s = raw.trim()
  if (!/^\d+$/.test(s)) return null
  const value = Number(s)
  return Number.isSafeInteger(value) ? value : null
}
