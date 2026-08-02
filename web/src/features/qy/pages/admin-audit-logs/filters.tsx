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

import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyFilterField } from '../ops/qy-ops-ui'

/**
 * 时间范围下拉。
 *
 * 资金审计与请求台账各写一份的话，两边的档位迟早会漂成「一边有 30 天、
 * 一边没有」，而管理员会以为那是数据本身的差别。
 *
 * `0` 表示不加时间条件（全部）。
 */
export function QyAuditRangeFilter(props: {
  hours: number
  onChange: (hours: number) => void
}) {
  const { t } = useTranslation()
  return (
    <QyFilterField label={t('qy_avl_range')}>
      <Select
        value={String(props.hours)}
        onValueChange={(value) => props.onChange(Number(value ?? props.hours))}
      >
        <SelectTrigger className='w-28'>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value='24'>{t('qy_avl_range_24h')}</SelectItem>
          <SelectItem value='168'>{t('qy_avl_range_7d')}</SelectItem>
          <SelectItem value='720'>{t('qy_avl_range_30d')}</SelectItem>
          <SelectItem value='0'>{t('qy_common_all')}</SelectItem>
        </SelectContent>
      </Select>
    </QyFilterField>
  )
}

/** 只收数字的筛选框（用户 ID 一类）。非数字字符直接丢弃，不做校验提示。 */
export function QyAuditNumberFilter(props: {
  label: string
  value: string
  onChange: (value: string) => void
  className?: string
}) {
  return (
    <QyFilterField label={props.label}>
      <Input
        className={props.className ?? 'w-28'}
        inputMode='numeric'
        value={props.value}
        onChange={(event) =>
          props.onChange(event.target.value.replaceAll(/\D/g, ''))
        }
      />
    </QyFilterField>
  )
}

/** 明细区的一行「标签: 值」。三个 tab 的展开详情共用同一种排版。 */
export function QyAuditDetailLine(props: {
  label: string
  value: string
  mono?: boolean
}) {
  if (props.value === '') return null
  return (
    <p className='break-all'>
      <span className='text-muted-foreground'>{props.label}: </span>
      <span className={props.mono === true ? 'font-mono' : undefined}>
        {props.value}
      </span>
    </p>
  )
}
