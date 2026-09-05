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
 * ⚠ **佣金账本不再走这里**。D-16 之后佣金以星屑记账（`gross = base_quota × 费率
 * / quota_per_unit`），整数走 `QySdAmount`、decimal 字符串走 `QySdDecimal`。
 * 佣金那一族里仍然是额度的只剩 `base_quota` —— 下线实际花掉的那个数，它是分母
 * 不是佣金本身，所以还在这里。
 *
 * 现在的消费方是真正的额度金额：渠道已用额度、划转金额、活动参与条件里的三条
 * 额度门槛、上游注册奖 `aff_quota`。
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

// `formatQyQuotaBound`（"照着它填必须能通过"的边界值展示）已随抽奖金额改走星屑
// 而删除：它唯一的消费方是建活动向导与期次系列面板上那几行上限文案，而星屑是
// 整数、没有"4 位小数把推荐值抹到下限之下"这回事。星屑的对应物见 `format-sd.ts`。

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
