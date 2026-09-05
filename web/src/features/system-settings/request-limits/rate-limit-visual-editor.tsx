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
import { Plus, Search } from 'lucide-react'
import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isObjectRecord } from '../utils/json-validators'
import { RateLimitDialog, type RateLimitEntryData } from './rate-limit-dialog'

type RateLimitVisualEditorProps = {
  value: string
  onChange: (value: string) => void
  /**
   * 分组并发上限那一张表(ModelRequestConcurrencyGroup)。
   *
   * 与 RPM 表分开存、合起来编辑:后端是两个独立的配置项(一个是固定窗口计数,
   * 一个是在途请求数),但对运营来说它们是同一行「这一档用户能用多少」,
   * 拆成两张表去配等于让人在两个地方记同一批分组名。
   */
  concurrencyValue?: string
  onConcurrencyChange?: (value: string) => void
}

type RateLimitEntry = RateLimitEntryData

export function RateLimitVisualEditor({
  value,
  onChange,
  concurrencyValue,
  onConcurrencyChange,
}: RateLimitVisualEditorProps) {
  const { t } = useTranslation()
  const [searchText, setSearchText] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editData, setEditData] = useState<RateLimitEntry | null>(null)

  const concurrencyMap = useMemo(() => {
    if (!concurrencyValue || concurrencyValue.trim() === '') return {}
    const parsed = safeJsonParseWithValidation<Record<string, unknown>>(
      concurrencyValue,
      {
        fallback: {},
        validator: isObjectRecord,
        validatorMessage: 'Concurrency limits must be a JSON object',
        context: 'concurrency limits',
      }
    )
    const out: Record<string, number> = {}
    for (const [group, limit] of Object.entries(parsed)) {
      if (typeof limit === 'number') out[group] = limit
    }
    return out
  }, [concurrencyValue])

  const rateLimits = useMemo(() => {
    const parsed =
      !value || value.trim() === ''
        ? {}
        : safeJsonParseWithValidation<Record<string, unknown>>(value, {
            fallback: {},
            validator: isObjectRecord,
            validatorMessage: 'Rate limits must be a JSON object',
            context: 'rate limits',
          })

    const rows = new Map<string, RateLimitEntry>()
    for (const [groupName, limits] of Object.entries(parsed)) {
      if (
        Array.isArray(limits) &&
        limits.length === 2 &&
        typeof limits[0] === 'number' &&
        typeof limits[1] === 'number'
      ) {
        rows.set(groupName, {
          groupName,
          maxRequests: limits[0],
          maxSuccess: limits[1],
          maxConcurrency: concurrencyMap[groupName] ?? 0,
        })
      }
    }
    // 只配了并发、没配 RPM 的分组同样要出现在列表里 —— 否则运营配完并发之后
    // 这一行就从界面上消失了,再也点不开。
    for (const [groupName, limit] of Object.entries(concurrencyMap)) {
      if (!rows.has(groupName)) {
        rows.set(groupName, {
          groupName,
          maxRequests: 0,
          maxSuccess: 1,
          maxConcurrency: limit,
        })
      }
    }
    return [...rows.values()]
  }, [value, concurrencyMap])

  const filteredRateLimits = useMemo(() => {
    if (!searchText) return rateLimits
    const lowerSearch = searchText.toLowerCase()
    return rateLimits.filter((limit) =>
      limit.groupName.toLowerCase().includes(lowerSearch)
    )
  }, [rateLimits, searchText])

  const handleSave = (data: RateLimitEntryData) => {
    const parsed = safeJsonParseWithValidation<Record<string, unknown>>(value, {
      fallback: {},
      validator: isObjectRecord,
      silent: true,
    })

    if (editData && editData.groupName !== data.groupName) {
      delete parsed[editData.groupName]
    }

    parsed[data.groupName] = [data.maxRequests, data.maxSuccess]

    onChange(JSON.stringify(parsed, null, 2))
    writeConcurrency((next) => {
      if (editData && editData.groupName !== data.groupName) {
        delete next[editData.groupName]
      }
      if (data.maxConcurrency > 0) {
        next[data.groupName] = data.maxConcurrency
      } else {
        // 0 = 不限,与"这张表里没有这个键"等价。不留零值,免得表随时间只增不减。
        delete next[data.groupName]
      }
    })
  }

  /** 就地改写并发表。父层没接这两个 prop 时静默跳过(旧调用点仍可用)。 */
  const writeConcurrency = (
    mutate: (draft: Record<string, number>) => void
  ) => {
    if (!onConcurrencyChange) return
    const next = { ...concurrencyMap }
    mutate(next)
    onConcurrencyChange(JSON.stringify(next, null, 2))
  }

  const handleDelete = (groupName: string) => {
    const parsed = safeJsonParseWithValidation<Record<string, unknown>>(value, {
      fallback: {},
      validator: isObjectRecord,
      silent: true,
    })

    delete parsed[groupName]

    onChange(JSON.stringify(parsed, null, 2))
    writeConcurrency((next) => {
      delete next[groupName]
    })
  }

  const handleEdit = (limit: RateLimitEntry) => {
    setEditData(limit)
    setDialogOpen(true)
  }

  const handleAdd = () => {
    setEditData(null)
    setDialogOpen(true)
  }

  return (
    <div className='space-y-4'>
      <div className='flex items-center gap-4'>
        <div className='relative flex-1'>
          <Search className='text-muted-foreground absolute top-2.5 left-2.5 h-4 w-4' />
          <Input
            placeholder={t('Search group names...')}
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            className='pl-9'
          />
        </div>
        <Button onClick={handleAdd}>
          <Plus className='mr-2 h-4 w-4' />
          {/* 不用泛键 'Add group':那个键同时被模型分组页用,而这里加的是
           **用户分组**的限流条目 —— 两处共用一个词正是被点名的歧义来源。 */}
          {t('Add group rate limit')}
        </Button>
      </div>

      <StaticDataTable
        data={filteredRateLimits}
        getRowKey={(limit) => limit.groupName}
        emptyContent={
          searchText
            ? t('No groups match your search')
            : t(
                'No group-based rate limits configured. Click "Add group" to get started.'
              )
        }
        columns={[
          {
            id: 'group',
            // 表头写明「用户分组」:本分支把用户分组与模型分组拆开了,
            // 泛写「分组名称」时没人知道这一列按哪边匹配。
            header: t('User group'),
            cellClassName: 'font-medium',
            cell: (limit) => limit.groupName,
          },
          {
            id: 'max-requests',
            header: t('Max Requests (incl. failures)'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (limit) => (
              <span className='font-mono'>
                {limit.maxRequests === 0
                  ? t('Unlimited')
                  : limit.maxRequests.toLocaleString()}
              </span>
            ),
          },
          {
            id: 'max-success',
            header: t('Max Success'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (limit) => (
              <span className='font-mono'>
                {limit.maxSuccess.toLocaleString()}
              </span>
            ),
          },
          {
            id: 'max-concurrency',
            header: t('Concurrent'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (limit) => (
              <span className='font-mono'>
                {limit.maxConcurrency === 0
                  ? t('Unlimited')
                  : limit.maxConcurrency.toLocaleString()}
              </span>
            ),
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (limit) => (
              <StaticRowActions
                editLabel={t('Edit')}
                deleteLabel={t('Delete')}
                menuLabel={t('Open menu')}
                onEdit={() => handleEdit(limit)}
                onDelete={() => handleDelete(limit.groupName)}
              />
            ),
          },
        ]}
      />

      <RateLimitDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSave={handleSave}
        editData={editData}
      />
    </div>
  )
}
