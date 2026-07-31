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
import { RefreshCw } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { QY_AVAIL_RANGES, QY_AVAIL_SORTS } from '../constants'
import type { QyAvailDefinition, QyAvailSortMode } from '../types'
import { QyAvailabilityDefinition } from './availability-definition'

type QyAvailabilityToolbarProps = {
  hours: number
  onHoursChange: (hours: number) => void
  /** 全部可见分组（服务端已按白名单裁剪，前端只做二次收窄）。 */
  groupOptions: string[]
  selectedGroups: string[]
  onGroupsChange: (groups: string[]) => void
  search: string
  onSearchChange: (value: string) => void
  sort: QyAvailSortMode
  onSortChange: (sort: QyAvailSortMode) => void
  definition: QyAvailDefinition | null
  onRefresh: () => void
  isFetching: boolean
}

export function QyAvailabilityToolbar(props: QyAvailabilityToolbarProps) {
  const { t } = useTranslation()
  const searchId = useId()
  const rangeId = useId()
  const sortId = useId()

  return (
    <QyFilterBar>
      <QyFilterField label={t('qy_avl_range')} htmlFor={rangeId}>
        <Select
          value={String(props.hours)}
          onValueChange={(value) =>
            props.onHoursChange(Number(value ?? props.hours))
          }
        >
          <SelectTrigger id={rangeId} className='w-28'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {QY_AVAIL_RANGES.map((range) => (
              <SelectItem key={range.hours} value={String(range.hours)}>
                {t(range.labelKey)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </QyFilterField>

      <QyFilterField label={t('qy_avl_group')} className='min-w-[16rem] flex-1'>
        <MultiSelect
          options={props.groupOptions.map((group) => ({
            label: group,
            value: group,
          }))}
          selected={props.selectedGroups}
          onChange={props.onGroupsChange}
          placeholder={t('qy_avl_group_all')}
          maxVisibleChips={4}
        />
      </QyFilterField>

      <QyFilterField label={t('qy_avl_model')} htmlFor={searchId}>
        <Input
          id={searchId}
          value={props.search}
          onChange={(event) => props.onSearchChange(event.target.value)}
          placeholder={t('qy_avl_model_search')}
          className='w-44'
        />
      </QyFilterField>

      <QyFilterField label={t('qy_avl_sort')} htmlFor={sortId}>
        <Select
          value={props.sort}
          onValueChange={(value) =>
            props.onSortChange((value ?? props.sort) as QyAvailSortMode)
          }
        >
          <SelectTrigger id={sortId} className='w-36'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {QY_AVAIL_SORTS.map((sort) => (
              <SelectItem key={sort.value} value={sort.value}>
                {t(sort.labelKey)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </QyFilterField>

      <div className='flex items-center gap-2'>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={props.onRefresh}
          disabled={props.isFetching}
        >
          <RefreshCw
            aria-hidden='true'
            className={props.isFetching ? 'animate-spin' : undefined}
          />
          {t('qy_common_refresh')}
        </Button>
        {props.definition != null && (
          <QyAvailabilityDefinition definition={props.definition} />
        )}
      </div>
    </QyFilterBar>
  )
}
