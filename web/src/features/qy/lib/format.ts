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
import { formatQuotaWithCurrency } from '@/lib/currency'
import { parseQuotaFromDollars, quotaUnitsToDollars } from '@/lib/format'

/**
 * qy 的额度格式化。
 *
 * 全部转调 `@/lib/currency`、`@/lib/format` 的既有函数，**不自建换算**：
 * 站内额度的 quota→USD→展示币种链路已经由上游实现，重写一遍必然与钱包页、
 * 日志页的数字对不上。
 *
 * 唯一的红线：法币提现金额（后端返回 string、按冻结汇率）**不能**走这里，
 * 那会被再乘一次当前汇率造成双重换算。法币走各自模块的展示组件。
 */

/**
 * 账本上的额度金额。
 *
 * 佣金账本里的 `gross_amount` / `settled_amount` / `unsettled_amount` 与余额行
 * 上的 `available_quota` / `frozen_quota` / `withdrawn_quota` **是同一个单位**
 * （都是站内额度）：`gross = base_quota × 费率`（`accrual.go` 的 `calcGross`），
 * 结算时 `floor(carry + Δgross)` 直接加进 `available_quota`
 * （`settle.go` 的 `computeSettlement`）。差别只有一处 —— 前者是
 * `decimal(30,10)` 的精确值、后端以**字符串**下发以免 JS 丢位，后者是整数。
 *
 * 所以展示层必须把这两类渲染成同一个东西。一列印 `$0.27`、隔壁一列印
 * `1370.0000000000`，看的人无从判断这两个数能不能相加 —— 而它们恰恰能。
 *
 * 这里的解析只服务于排版：结果不回流参与任何运算，长尾小数在展示精度
 * （最多 4 位）下本来就看不见。**法币金额不能走这里**，理由见文件头。
 */
export type QyQuotaAmount = number | string | null | undefined

/** 额度金额 → 可格式化的数值。解析不出来（含空串、NaN、±Inf）返回 `null`。 */
export function qyQuotaValue(amount: QyQuotaAmount): number | null {
  if (amount == null) return null
  if (typeof amount === 'number') return Number.isFinite(amount) ? amount : null
  const raw = amount.trim()
  if (raw === '') return null
  const parsed = Number(raw)
  return Number.isFinite(parsed) ? parsed : null
}

/** 明细展示：不缩写，保留 4 位小数，用于表格与单据详情。 */
export function formatQyQuotaLedger(amount: QyQuotaAmount): string {
  return formatQuotaWithCurrency(qyQuotaValue(amount), {
    digitsLarge: 2,
    digitsSmall: 4,
    abbreviate: false,
  })
}

/**
 * 边界值展示：**照着它填必须能通过**。
 *
 * `formatQyQuotaLedger` 最多印 4 位小数（≥1 时只有 2 位），那对账本上的读数
 * 是对的，对一个"上限/下限"却是错的：
 *
 * - 上限 2147483647 quota 印成 `$4,294.97`，而照着填回去是 2147485000 quota
 *   —— 比真上限**多 1353**，界面自己念出来的那个数提交就被后端 400。
 * - 下限 16667 quota 印成 `$0.0333`，照着填回去是 16650 quota —— 比真下限
 *   **少 17**，于是界面自己给的推荐值当场撞上界面自己的红字。
 *
 * 两者是同一个根因：判据按 quota 整数走（1 quota = $0.000002），而展示按 4 位
 * 小数走。所以这里按**能不能原样填回去**决定小数位数：取最少的位数 d，使得
 * `parseQyQuota(round(amount, d)) === quota`。
 *
 * 只给"运营会照着填的那个数"用。账本金额仍旧走 {@link formatQyQuotaLedger} ——
 * 那里多印几位小数只是噪声。
 */
export function formatQyQuotaBound(quota: number): string {
  const value = qyQuotaValue(quota)
  if (value == null) return formatQyQuotaLedger(quota)
  const amount = qyQuotaToDisplayAmount(value)
  // 8 位足够覆盖任何 quotaPerUnit：默认刻度下 1 quota = $0.000002（6 位）。
  // 找不到（自定义汇率把它推到 8 位之外）就退回最宽的那一档，宁可多印几位。
  let digits = 8
  for (let d = 0; d <= 8; d++) {
    if (parseQyQuota(Number(amount.toFixed(d))) === value) {
      digits = d
      break
    }
  }
  return formatQuotaWithCurrency(value, {
    digitsLarge: digits,
    digitsSmall: digits,
    abbreviate: false,
  })
}

/** 概览展示：允许缩写（1.2K），用于统计卡片与图表轴。 */
export function formatQyQuotaHero(amount: QyQuotaAmount): string {
  return formatQuotaWithCurrency(qyQuotaValue(amount), {
    digitsLarge: 2,
    digitsSmall: 4,
    abbreviate: true,
  })
}

/**
 * 展示金额 → quota 整数。
 *
 * 直接复用 `parseQuotaFromDollars`（内部按当前展示币种与汇率折算并四舍五入）。
 * 非有限数与负数一律归零，交由表单校验去报错，而不是把 NaN 提交给后端。
 */
export function parseQyQuota(amount: number): number {
  if (!Number.isFinite(amount) || amount <= 0) return 0
  return parseQuotaFromDollars(amount)
}

/** quota 整数 → 展示金额（`parseQyQuota` 的逆运算，用于输入框回显）。 */
export function qyQuotaToDisplayAmount(quota: number): number {
  if (!Number.isFinite(quota)) return 0
  return quotaUnitsToDollars(quota)
}
