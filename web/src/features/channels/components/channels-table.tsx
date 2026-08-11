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
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type {
  ColumnFiltersState,
  OnChangeFn,
  SortingState,
  Row,
} from '@tanstack/react-table'
import { Eye, EyeOff } from 'lucide-react'
import { useState, useMemo, useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DISABLED_ROW_DESKTOP,
  DISABLED_ROW_MOBILE,
  DataTablePage,
  useDebouncedColumnFilter,
  useDataTable,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
// qy 扩展：批次报告（逐条失败明细）的**唯一**挂载点。
// 它必须挂在页面级而不是批量工具条旁边：清零现在还有两个不经过「批量操作」
// 开关的直达入口，那时整个工具条根本没被渲染，报告就没有挂载的机会 ——
// 屏幕上只剩一句"N 个渠道未处理成功，请查看明细"，而明细没有任何入口。
import { QyChannelBulkResultOutlet } from '@/features/qy/channel-bulk'
import { QyChannelResetUsageToolbarAction } from '@/features/qy/channel-bulk/reset-usage'
import { useQyChannelBulkVisible } from '@/features/qy/channel-bulk/visible'
import { useMediaQuery } from '@/hooks'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { getLobeIcon } from '@/lib/lobe-icon'

import { getChannels, searchChannels, getGroups } from '../api'
import {
  DEFAULT_PAGE_SIZE,
  CHANNEL_STATUS,
  CHANNEL_STATUS_OPTIONS,
} from '../constants'
import {
  channelsQueryKeys,
  aggregateChannelsByTag,
  getChannelTableRowId,
  isTagAggregateRow,
  getChannelTypeIcon,
  getChannelTypeLabel,
} from '../lib'
import type { Channel, ChannelSortBy } from '../types'
import { ChannelCard } from './channel-card'
import { resettableChannelsOf, useChannelsColumns } from './channels-columns'
import { useChannels } from './channels-provider'
import { DataTableBulkActions } from './data-table-bulk-actions'

const route = getRouteApi('/_authenticated/channels/')
const CHANNELS_COLUMN_VISIBILITY_STORAGE_KEY = 'channels:column-visibility'
const CHANNELS_COLUMN_SIZING_STORAGE_KEY = 'channels:column-sizing'
const CHANNELS_VIEW_MODE_STORAGE_KEY = 'channels:view-mode'
const CHANNELS_STATUS_FILTER_STORAGE_KEY = 'channel-status-filter'

const CHANNEL_SORTABLE_COLUMNS = new Set<ChannelSortBy>([
  'id',
  'name',
  'priority',
  'balance',
  'response_time',
  'test_time',
])

function isDisabledChannelRow(channel: Channel) {
  return (
    !isTagAggregateRow(channel) && channel.status !== CHANNEL_STATUS.ENABLED
  )
}

export function ChannelsTable() {
  const { t } = useTranslation()
  const {
    enableTagMode,
    idSort,
    batchMode,
    sensitiveVisible,
    setSensitiveVisible,
  } = useChannels()
  const isMobile = useMediaQuery('(max-width: 640px)')

  // Table state
  const [sorting, setSorting] = useState<SortingState>([])

  // URL state management
  const {
    globalFilter,
    onGlobalFilterChange,
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: {
      defaultPage: 1,
      defaultPageSize: isMobile ? 10 : DEFAULT_PAGE_SIZE,
    },
    globalFilter: { enabled: true, key: 'filter' },
    columnFilters: [
      {
        columnId: 'status',
        searchKey: 'status',
        type: 'array',
        deserialize: (value) => {
          if (value !== undefined) return value
          const stored = localStorage.getItem(
            CHANNELS_STATUS_FILTER_STORAGE_KEY
          )
          return stored === 'enabled' || stored === 'disabled' ? [stored] : []
        },
      },
      { columnId: 'type', searchKey: 'type', type: 'array' },
      { columnId: 'group', searchKey: 'group', type: 'array' },
      { columnId: 'model', searchKey: 'model', type: 'string' },
    ],
  })

  const handleColumnFiltersChange: OnChangeFn<ColumnFiltersState> = (
    updater
  ) => {
    onColumnFiltersChange((previous) => {
      const next = typeof updater === 'function' ? updater(previous) : updater
      const status = next.find((f) => f.id === 'status')?.value as
        | string[]
        | undefined
      localStorage.setItem(
        CHANNELS_STATUS_FILTER_STORAGE_KEY,
        status?.[0] ?? 'all'
      )
      return next
    })
  }

  // Extract filters from column filters
  const statusFilter =
    (columnFilters.find((f) => f.id === 'status')?.value as string[]) || []
  const typeFilter = useMemo(
    () => (columnFilters.find((f) => f.id === 'type')?.value as string[]) || [],
    [columnFilters]
  )
  const groupFilter =
    (columnFilters.find((f) => f.id === 'group')?.value as string[]) || []
  const {
    value: modelFilter,
    inputValue: modelFilterInput,
    onChange: onModelFilterInputChange,
    onCompositionStart: onModelFilterCompositionStart,
    onCompositionEnd: onModelFilterCompositionEnd,
    resetInput: resetModelFilterInput,
  } = useDebouncedColumnFilter({
    columnFilters,
    columnId: 'model',
    onColumnFiltersChange,
  })

  // Determine whether to use search or regular list API
  const shouldSearch = Boolean(globalFilter?.trim() || modelFilter.trim())

  const sortParams = useMemo(() => {
    const activeSort = sorting[0]
    if (
      !activeSort ||
      !CHANNEL_SORTABLE_COLUMNS.has(activeSort.id as ChannelSortBy)
    ) {
      return {}
    }

    return {
      sort_by: activeSort.id as ChannelSortBy,
      sort_order: activeSort.desc ? 'desc' : 'asc',
    } as const
  }, [sorting])

  const handleSortingChange: OnChangeFn<SortingState> = (updater) => {
    setSorting((previous) => {
      const next = typeof updater === 'function' ? updater(previous) : updater
      if (pagination.pageIndex > 0) {
        onPaginationChange({ ...pagination, pageIndex: 0 })
      }
      return next
    })
  }

  // Fetch groups for filter
  const { data: groupsData } = useQuery({
    queryKey: ['groups'],
    queryFn: getGroups,
  })

  const groupOptions = useMemo(
    () =>
      (groupsData?.data || []).map((g) => ({
        label: g,
        value: g,
      })),
    [groupsData]
  )

  // Fetch channels data
  // eslint-disable-next-line @tanstack/query/exhaustive-deps
  const { data, isLoading, isFetching } = useQuery({
    queryKey: channelsQueryKeys.list({
      keyword: globalFilter,
      model: modelFilter,
      group:
        groupFilter.length > 0 && !groupFilter.includes('all')
          ? groupFilter[0]
          : undefined,
      status:
        statusFilter.length > 0 && !statusFilter.includes('all')
          ? statusFilter[0]
          : undefined,
      type:
        typeFilter.length > 0 && !typeFilter.includes('all')
          ? Number(typeFilter[0])
          : undefined,
      tag_mode: enableTagMode,
      id_sort: idSort,
      ...sortParams,
      p: pagination.pageIndex + 1,
      page_size: pagination.pageSize,
    }),
    queryFn: async () => {
      if (shouldSearch) {
        return searchChannels({
          keyword: globalFilter,
          model: modelFilter,
          group:
            groupFilter.length > 0 && !groupFilter.includes('all')
              ? groupFilter[0]
              : undefined,
          status:
            statusFilter.length > 0 && !statusFilter.includes('all')
              ? statusFilter[0]
              : undefined,
          type:
            typeFilter.length > 0 && !typeFilter.includes('all')
              ? Number(typeFilter[0])
              : undefined,
          tag_mode: enableTagMode,
          id_sort: idSort,
          ...sortParams,
          p: pagination.pageIndex + 1,
          page_size: pagination.pageSize,
        })
      } else {
        return getChannels({
          group:
            groupFilter.length > 0 && !groupFilter.includes('all')
              ? groupFilter[0]
              : undefined,
          status:
            statusFilter.length > 0 && !statusFilter.includes('all')
              ? statusFilter[0]
              : undefined,
          type:
            typeFilter.length > 0 && !typeFilter.includes('all')
              ? Number(typeFilter[0])
              : undefined,
          tag_mode: enableTagMode,
          id_sort: idSort,
          ...sortParams,
          p: pagination.pageIndex + 1,
          page_size: pagination.pageSize,
        })
      }
    },
    placeholderData: (previousData) => previousData,
  })

  // Apply tag aggregation if tag mode is enabled
  const channels = useMemo(() => {
    const rawChannels = data?.data?.items || []

    if (enableTagMode && rawChannels.length > 0) {
      return aggregateChannelsByTag(rawChannels)
    }

    return rawChannels
  }, [data, enableTagMode])

  const totalCount = data?.data?.total || 0
  const typeCounts = data?.data?.type_counts

  // Columns configuration
  const columns = useChannelsColumns({ enableSelection: batchMode })
  const qyBulkVisible = useQyChannelBulkVisible()

  // React Table instance
  const { table } = useDataTable({
    data: channels,
    columns,
    totalCount,
    sorting,
    initialColumnVisibility: {
      models: false,
      tag: false,
    },
    columnVisibilityStorageKey: CHANNELS_COLUMN_VISIBILITY_STORAGE_KEY,
    columnSizingStorageKey: isMobile
      ? false
      : CHANNELS_COLUMN_SIZING_STORAGE_KEY,
    columnFilters,
    pagination,
    globalFilter,
    enableRowSelection: batchMode
      ? (row: Row<Channel>) => !isTagAggregateRow(row.original)
      : false,
    onSortingChange: handleSortingChange,
    onColumnFiltersChange: handleColumnFiltersChange,
    onPaginationChange,
    onGlobalFilterChange,
    getRowId: getChannelTableRowId,
    getSubRows: (row: Channel & { children?: Channel[] }) => row.children,
    manualPagination: true,
    manualSorting: true,
    manualFiltering: true,
    withExpandedRowModel: true,
    enableColumnResizing: !isMobile,
    ensurePageInRange,
  })

  useEffect(() => {
    if (!batchMode) {
      table.resetRowSelection()
    }
  }, [batchMode, table])

  // 翻页 / 改筛选 / 改搜索之后清空选中态。
  //
  // 这张表是 manualPagination + manualFiltering：`data` 里只有当前这一页的行，
  // 而选中态是按 row id 存的、**不会**随翻页消失。两者相加的后果不是"支持跨页
  // 选择"，而是一种只在批量操作这一步才暴露的错位：
  //
  //   1. 工具条的计数与 `DataTableBulkActions` 拿到的 id 都来自
  //      `getFilteredSelectedRowModel()`，它只认当前页加载出来的行 ——
  //      在第 1 页勾了 3 个、翻到第 2 页，那 3 个既不显示也不会被操作；
  //   2. 但它们仍然在选中态里躺着。翻回第 1 页，勾选框又是勾上的，
  //      于是同一批"选中"在不同页会给出不同的条数与不同的执行范围。
  //
  // 对「批量清空已用额度」「批量删除」这种不可逆动作，这个错位的具体形状是：
  // 确认框复述的条数与金额算的是当前页那几条，而管理员以为自己勾的是全部。
  // 与其让选中态跨页残留却不可执行，不如让"屏幕上勾着的"恒等于"会被执行的"。
  useEffect(() => {
    table.resetRowSelection()
  }, [
    table,
    pagination.pageIndex,
    pagination.pageSize,
    globalFilter,
    columnFilters,
  ])

  // Prepare filter options from existing channel types only.
  const typeFilterOptions = useMemo(() => {
    const counts = typeCounts || {}
    const typeIds = Object.entries(counts)
      .map(([type, count]) => ({
        type: Number(type),
        count: Number(count) || 0,
      }))
      .filter((item) => item.type > 0 && item.count > 0)
      .sort((a, b) => {
        const labelA = t(getChannelTypeLabel(a.type))
        const labelB = t(getChannelTypeLabel(b.type))
        return labelA.localeCompare(labelB)
      })

    const selectedType = typeFilter.find((value) => value !== 'all')
    if (selectedType) {
      const selectedTypeId = Number(selectedType)
      const alreadyIncluded = typeIds.some(
        (item) => item.type === selectedTypeId
      )
      if (selectedTypeId > 0 && !alreadyIncluded) {
        typeIds.push({
          type: selectedTypeId,
          count: Number(counts[selectedType]) || 0,
        })
      }
    }

    const totalTypes = Object.values(counts).reduce(
      (sum, count) => sum + (Number(count) || 0),
      0
    )

    return [
      {
        label: 'All Types',
        value: 'all',
        count: totalTypes,
      },
      ...typeIds.map((item) => {
        const iconName = getChannelTypeIcon(item.type)
        return {
          label: getChannelTypeLabel(item.type),
          value: String(item.type),
          count: item.count,
          iconNode: getLobeIcon(`${iconName}.Color`, 16),
        }
      }),
    ]
  }, [t, typeCounts, typeFilter])

  const groupFilterOptions = [
    { label: t('All Groups'), value: 'all' },
    ...groupOptions.map((option) => ({
      ...option,
      label: sensitiveVisible ? option.label : '••••',
    })),
  ]

  return (
    <>
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={isLoading}
        isFetching={isFetching}
        emptyTitle={t('No Channels Found')}
        emptyDescription={t(
          'No channels available. Create your first channel to get started.'
        )}
        skeletonKeyPrefix='channel-skeleton'
        enableCardView
        viewModeStorageKey={CHANNELS_VIEW_MODE_STORAGE_KEY}
        renderCard={(row, { isSelected }) => (
          <ChannelCard row={row} isSelected={isSelected} />
        )}
        cardGridClassName='grid grid-cols-1 gap-3 sm:gap-4 lg:grid-cols-3'
        applyHeaderSize
        toolbarProps={{
          searchPlaceholder: t('Filter by name, ID, or key...'),
          searchDebounceMs: 500,
          onReset: () => {
            resetModelFilterInput()
          },
          additionalSearch: (
            <Input
              placeholder={t('Filter by model...')}
              value={modelFilterInput}
              onChange={onModelFilterInputChange}
              onCompositionStart={onModelFilterCompositionStart}
              onCompositionEnd={onModelFilterCompositionEnd}
              className='w-full sm:w-[150px] lg:w-[180px]'
            />
          ),
          filters: [
            {
              columnId: 'status',
              title: t('Status'),
              options: [...CHANNEL_STATUS_OPTIONS],
              singleSelect: true,
            },
            {
              columnId: 'type',
              title: t('Type'),
              options: typeFilterOptions,
              singleSelect: true,
            },
            {
              columnId: 'group',
              title: t('Group'),
              options: groupFilterOptions,
              singleSelect: true,
            },
          ],
          preActions: (
            <>
              {/* qy 扩展：清空已用额度的**多渠道**直达入口。
                  它在表头上已经有一个（「已使用 / 剩余」那一列），但表头只在
                  表格视图里存在，而这一页 `enableCardView` 且没有传
                  `defaultViewMode` —— DataTablePage 的默认是**卡片视图**，
                  首次进页面（localStorage 里还没有选择）根本没有表头。
                  工具条两种视图下都在，所以多渠道入口在这里再放一份。 */}
              <QyChannelResetUsageToolbarAction
                channels={resettableChannelsOf(
                  table.getCoreRowModel().flatRows
                )}
              />
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      variant='ghost'
                      size='icon'
                      onClick={() => setSensitiveVisible(!sensitiveVisible)}
                      aria-label={sensitiveVisible ? t('Hide') : t('Show')}
                      className='text-muted-foreground hover:text-foreground size-8'
                    />
                  }
                >
                  {sensitiveVisible ? <Eye /> : <EyeOff />}
                </TooltipTrigger>
                <TooltipContent>
                  {sensitiveVisible ? t('Hide') : t('Show')}
                </TooltipContent>
              </Tooltip>
            </>
          ),
        }}
        getRowClassName={(row, { isMobile }) => {
          if (!isDisabledChannelRow(row.original)) {
            return undefined
          }
          if (isMobile) {
            return DISABLED_ROW_MOBILE
          }
          return DISABLED_ROW_DESKTOP
        }}
        bulkActions={batchMode ? <DataTableBulkActions table={table} /> : null}
      />
      {qyBulkVisible && <QyChannelBulkResultOutlet />}
    </>
  )
}
