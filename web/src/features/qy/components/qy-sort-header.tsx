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
import { ArrowDown, ArrowUp, ArrowUpDown } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { qyNextSort, type QySortState } from '../lib/sort'

/**
 * 可点击排序的表头：点一次按这一列从多到少，再点一次从少到多。
 *
 * 排序在**后端**做（白名单列 + asc / desc），这里只切换状态 —— 分页列表按当前页
 * 在前端重排是错的：第 2 页里可能有比第 1 页还大的数。
 */
export function QySortHeader<K extends string>(props: {
  label: string
  sortKey: K
  state: QySortState<K>
  onChange: (next: QySortState<K>) => void
  /** 数字列右对齐，与单元格同侧。 */
  align?: 'left' | 'right'
}) {
  const { t } = useTranslation()
  const active = props.state.key === props.sortKey
  let Icon = ArrowUpDown
  let stateLabel = t('qy_sort_none')
  if (active) {
    Icon = props.state.order === 'desc' ? ArrowDown : ArrowUp
    stateLabel =
      props.state.order === 'desc' ? t('qy_sort_desc') : t('qy_sort_asc')
  }

  return (
    <button
      type='button'
      className={cn(
        'hover:text-foreground inline-flex items-center gap-1 select-none',
        props.align === 'right' && 'ml-auto flex-row-reverse',
        active && 'text-foreground'
      )}
      aria-label={`${props.label}，${stateLabel}`}
      title={stateLabel}
      data-sort-key={props.sortKey}
      data-sort-order={active ? props.state.order : undefined}
      onClick={() => props.onChange(qyNextSort(props.state, props.sortKey))}
    >
      <span>{props.label}</span>
      <Icon
        aria-hidden='true'
        className={cn('size-3.5', !active && 'opacity-40')}
      />
    </button>
  )
}
