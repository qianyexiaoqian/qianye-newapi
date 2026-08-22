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

import { BadgeCell, TruncatedCell } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { StatusBadge } from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import {
  // AutoGroupBadge,
  GroupRatioBadge,
  type GroupRatio,
} from './auto-group-visuals'

type ApiKeyGroupCellProps = {
  crossGroupRetry: boolean
  group: string
  /**
   * 只用 phrasing content 渲染（全是 `<span>`，不带任何 Tooltip）。
   *
   * 分组行内快切把这一格塞进了 `<button>` 里（它同时是下拉的触发器）。
   * 默认那条路径外面包的是 `TruncatedCell` / `BadgeCell`，两者都渲染 `<div>` ——
   * `<div>` 是 flow content，放进 `<button>` 是不合法的嵌套；而里面那个
   * `TooltipTrigger` 又会在按钮内部再挂一层交互元素。浏览器都能画出来，
   * 但那是"能跑"，不是"对"。
   *
   * 所以这一档只去掉**包装**，徽标本身（StatusBadge / Badge 都是 `<span>`）
   * 与默认路径逐字相同 —— 两处显示不会分家。auto 那句解释在这一档没有落脚点，
   * 由调用方挂到按钮自己的 `title` 上（见 api-key-group-switch-cell.tsx）。
   */
  inline?: boolean
  ratio?: GroupRatio
  shouldReduceMotion: boolean
}

export function ApiKeyGroupCell(props: ApiKeyGroupCellProps) {
  const { t } = useTranslation()

  if (props.inline) {
    const ratio = typeof props.ratio === 'number' ? props.ratio : undefined
    if (props.group !== 'auto') {
      return (
        <span className='flex max-w-full min-w-0 overflow-hidden'>
          <GroupBadge group={props.group} ratio={ratio} />
        </span>
      )
    }
    return (
      <span
        data-api-key-group-cell='auto-inline'
        className='flex max-w-full min-w-0 items-center gap-1.5 overflow-hidden text-xs'
      >
        <StatusBadge label={t('Cross-group')} variant='info' copyable={false} />
        <GroupRatioBadge
          ratio={props.ratio}
          isAuto
          shouldReduceMotion={props.shouldReduceMotion}
        />
      </span>
    )
  }

  if (props.group !== 'auto') {
    const ratio = typeof props.ratio === 'number' ? props.ratio : undefined
    return (
      <TruncatedCell
        className='-ml-1.5'
        tooltipContent={props.group || '-'}
        tooltipClassName='break-all'
      >
        <GroupBadge group={props.group} ratio={ratio} />
      </TruncatedCell>
    )
  }

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <BadgeCell
            data-api-key-group-cell='auto'
            className='gap-1.5 overflow-visible text-xs'
          />
        }
      >
        <StatusBadge label={t('Cross-group')} variant='info' copyable={false} />
        {/*<AutoGroupBadge shouldReduceMotion={props.shouldReduceMotion} />*/}
        <GroupRatioBadge
          ratio={props.ratio}
          isAuto
          shouldReduceMotion={props.shouldReduceMotion}
        />
      </TooltipTrigger>
      <TooltipContent>
        <span className='text-xs'>
          {t(
            'Automatically selects the best available group with circuit breaker mechanism'
          )}
        </span>
      </TooltipContent>
    </Tooltip>
  )
}
