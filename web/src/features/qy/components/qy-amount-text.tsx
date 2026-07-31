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

import { formatQyQuotaHero, formatQyQuotaLedger } from '../lib/format'

type QyAmountTextProps = {
  /** 站内额度（quota 整数）。`null` / `undefined` 显示 `-`。 */
  quota: number | null | undefined
  /**
   * `ledger` 明细口径：不缩写，用于表格与单据详情；
   * `hero` 概览口径：允许缩写（1.2K），用于统计卡片。
   */
  variant?: 'hero' | 'ledger'
  /** 正数是否显示 `+` 前缀。收支双向的流水列表需要打开。 */
  signed?: boolean
  className?: string
}

/**
 * 站内额度展示。
 *
 * 格式与钱包页、日志页完全一致 —— 内部转调上游的 `formatQuotaWithCurrency`，
 * 不自建换算。**法币金额（提现）不能用它**：那是按冻结汇率产生的绝对值，
 * 再走一次 quota→USD→展示币种会造成双重换算。
 */
export function QyAmountText(props: QyAmountTextProps) {
  if (props.quota == null || !Number.isFinite(props.quota)) {
    return (
      <span className={cn('text-muted-foreground', props.className)}>-</span>
    )
  }

  const format =
    props.variant === 'hero' ? formatQyQuotaHero : formatQyQuotaLedger
  const isNegative = props.quota < 0
  const prefix = props.signed === true && props.quota > 0 ? '+' : ''

  return (
    <span
      className={cn(
        'tabular-nums',
        isNegative && 'text-destructive',
        props.className
      )}
    >
      {prefix}
      {format(props.quota)}
    </span>
  )
}
