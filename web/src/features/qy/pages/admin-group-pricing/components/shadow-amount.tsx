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
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { QyAmountText } from '../../../components/qy-amount-text'
import { qyQuotaDirection } from '../lib/pricing-math'

const TONE = {
  down: 'text-success',
  flat: 'text-muted-foreground',
  up: 'text-destructive',
} as const

type QyGpDeltaAmountProps = {
  quota: number
  /** 只显示金额，不加方向词与配色。用于「实际扣费」这类没有方向语义的数字。 */
  plain?: boolean
}

/**
 * 影子差额金额。
 *
 * 与 `QyAmountText` 的差别是它必须**同时说出方向的业务含义**：一个带负号的
 * 红色数字既可能被读成「少收」也可能被读成「亏了」，而这一栏要回答的是
 * 「切换后多收还是少收」。所以数字后面直接跟一个词。
 */
export function QyGpDeltaAmount(props: QyGpDeltaAmountProps) {
  const { t } = useTranslation()

  if (props.plain === true) {
    return <QyAmountText quota={props.quota} variant='hero' />
  }

  const direction = qyQuotaDirection(props.quota)
  const tone = TONE[direction]

  return (
    <span className={cn('inline-flex items-center gap-1.5', tone)}>
      <QyAmountText
        quota={props.quota}
        variant='hero'
        signed
        className={tone}
      />
      <span className='text-xs'>{t(`qy_gp_sh_delta_${direction}`)}</span>
    </span>
  )
}
