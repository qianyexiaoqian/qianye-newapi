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
import { formatSdDecimal } from '../lib/format-sd'

/**
 * 全精度星屑金额（decimal 字符串）+ 单位名。
 *
 * 与 {@link QySdAmount} 的分工只有一条：那个收**整数**（余额、流水、已发放），
 * 这个收后端以字符串下发的 `decimal(30,10)`（计佣 gross、未结算余数、待结算）。
 * 两者不能互相顶替 —— 把 decimal 字符串喂给收 number 的那个，会在 JS 的精确表达
 * 范围之外丢位，而丢的正好是佣金那几位小数。
 *
 * 字符串**不解析成 number**：只做字符层面的去零与分组（`formatSdDecimal`）。
 */
export function QySdDecimal(props: {
  /** 后端下发的 decimal 字符串。`null` / 空串显示 `-`。 */
  value: string | null | undefined
  className?: string
}) {
  const unit = useStardustName()
  const text = formatSdDecimal(props.value)
  if (text === '-') {
    return (
      <span className={cn('text-muted-foreground', props.className)}>-</span>
    )
  }
  return (
    <span className={cn('tabular-nums', props.className)}>
      {/* 数字与单位之间留一个真实的空格，与 QySdAmount / formatSdWithUnit 同形。 */}
      {text}{' '}
      <span className='text-muted-foreground text-[0.8em] font-normal'>
        {unit}
      </span>
    </span>
  )
}
