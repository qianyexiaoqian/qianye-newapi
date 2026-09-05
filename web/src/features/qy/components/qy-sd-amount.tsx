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

import { useStardustName } from '../hooks/use-stardust-name'
import { formatSd, formatSdSigned } from '../lib/format-sd'

type QySdAmountProps = {
  /** 星屑整数。`null` / `undefined` / 非有限数显示 `-`。 */
  amount: number | null | undefined
  /** 正数是否显示 `+` 前缀。收支双向的流水列表需要打开。 */
  signed?: boolean
  /**
   * `ledger` 明细口径：表格与单据详情；
   * `hero` 概览口径：统计卡片与弹窗里最显眼的那个数，只是字号更大。
   * 两种口径**都不缩写**：星屑是整数积分，`1.2K` 会让用户对不上账。
   */
  variant?: 'hero' | 'ledger'
  className?: string
}

/**
 * 星屑金额展示：整数 + 千分位 + 单位名。
 *
 * **绝不**转调 `QyAmountText` / `formatQuotaWithCurrency`：那一套把整数按
 * quota→USD→展示币种换算，而星屑不是额度。抽奖 / 商城 / 星屑账本上凡是钱
 * 都走这里；仍然是额度的只剩参与条件里的三条门槛（余额 / 累计消费 / 近 N 日
 * 消费），它们继续走 `QyAmountText`。
 */
export function QySdAmount(props: QySdAmountProps) {
  const unit = useStardustName()
  if (props.amount == null || !Number.isFinite(props.amount)) {
    return (
      <span className={cn('text-muted-foreground', props.className)}>-</span>
    )
  }

  const text =
    props.signed === true
      ? formatSdSigned(props.amount)
      : formatSd(props.amount)
  const isNegative = Math.trunc(props.amount) < 0

  return (
    <span
      className={cn(
        'tabular-nums',
        props.variant === 'hero' && 'text-base font-semibold',
        isNegative && 'text-destructive',
        props.className
      )}
    >
      {/* 数字与单位之间留一个真实的空格：复制出去、读屏念出来都是
          「1,234 星屑」，与 formatSdWithUnit 拼进句子里的形状逐字相同。 */}
      {text}{' '}
      <span className='text-muted-foreground text-[0.8em] font-normal'>
        {unit}
      </span>
    </span>
  )
}
