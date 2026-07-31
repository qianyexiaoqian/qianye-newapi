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
import { cn } from '@/lib/utils'

type QyFiatTextProps = {
  /** 后端下发的 decimal 字符串，例如 `"128.400000"`。 */
  amount: string | null | undefined
  /** 币种代码（`CNY` / `USD`），来自单据自身的 `currency` 字段。 */
  currency?: string | null
  className?: string
}

/**
 * 法币金额展示。
 *
 * **绝不能用 `formatQuotaWithCurrency` / `formatCurrencyFromUSD` 渲染法币。**
 * 提现单上的法币是按"申请那一刻冻结的汇率"算出的绝对值，再走一次
 * quota→USD→展示币种的换算链会造成双重换算，用户看到的数字与实际到账对不上。
 *
 * 同理，`amount` 必须原样保留后端的字符串：`decimal(18,6)` 超出 JS `number`
 * 的安全表达范围，`parseFloat` 之后再参与任何运算都会引入无法解释的尾差。
 * 这里的 `Number()` 只喂给 `Intl.NumberFormat` 做一次性排版，结果不回流。
 */
export function QyFiatText(props: QyFiatTextProps) {
  const raw = props.amount?.trim()
  if (raw == null || raw === '') {
    return (
      <span className={cn('text-muted-foreground', props.className)}>-</span>
    )
  }

  const currency = props.currency?.trim()
  const suffix = currency == null || currency === '' ? '' : ` ${currency}`
  const numeric = Number(raw)
  // 解析不出来就原样显示：宁可让用户看到一串未格式化的数字，也不能显示 NaN。
  const text = Number.isFinite(numeric)
    ? new Intl.NumberFormat(undefined, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
      }).format(numeric)
    : raw

  return (
    <span className={cn('tabular-nums', props.className)}>
      {text}
      {suffix}
    </span>
  )
}
