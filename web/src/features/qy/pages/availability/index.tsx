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
import { CloudOff, HeartPulse } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { EmptyState } from '@/components/empty-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useDebounce } from '@/hooks'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid } from '../components/qy-stat-grid'
import { formatQyAvailability, formatQyCount, formatQyMs } from '../ops/format'
import { getQyAvailabilityMatrix } from './api'
import { QyAvailabilityCellSheet } from './components/availability-cell-sheet'
import { QyAvailabilityMatrix } from './components/availability-matrix'
import { QyAvailabilityTable } from './components/availability-table'
import { QyAvailabilityToolbar } from './components/availability-toolbar'
import { formatQyTps, getQyAvailMetric } from './constants'
import type { QyAvailCell, QyAvailMetricKey, QyAvailSortMode } from './types'

const PAGE_SIZE = 30

/**
 * 模型可用率监控（按分组）。
 *
 * 上游模型广场展示的是**全站汇总**可用率：一个高流量的 default 分组会把
 * svip 的真实状况完全淹没，而这两个分组背后往往是不同的渠道。本页把分组
 * 维度还原出来，是这个功能存在的全部理由。
 *
 * 分组白名单由服务端强制裁剪，普通用户只能看到自己有权的分组；管理员的
 * 全分组视图在 `/qy/admin/availability`（那里读的是不做裁剪的管理端端点）。
 */
export function QyAvailability() {
  const { t } = useTranslation()
  const config = useQyConfig()

  const [hours, setHours] = useState(24)
  const [selectedGroups, setSelectedGroups] = useState<string[]>([])
  const [search, setSearch] = useState('')
  const [sort, setSort] = useState<QyAvailSortMode>('availability_asc')
  const [metricKey, setMetricKey] = useState<QyAvailMetricKey>('availability')
  const [page, setPage] = useState(1)
  const [selectedCell, setSelectedCell] = useState<QyAvailCell | null>(null)

  const metric = getQyAvailMetric(metricKey)

  const debouncedSearch = useDebounce(search, 300)
  const featureOff =
    config.status === 'enabled' && !config.features.availability

  const params = useMemo(
    () => ({
      hours,
      groups: selectedGroups.length > 0 ? selectedGroups.join(',') : undefined,
      q: debouncedSearch.trim() === '' ? undefined : debouncedSearch.trim(),
      sort,
      page,
      page_size: PAGE_SIZE,
    }),
    [debouncedSearch, hours, page, selectedGroups, sort]
  )

  const query = useQuery({
    queryKey: qyKeys.availabilityMatrix(params),
    queryFn: () => getQyAvailabilityMatrix(params),
    enabled: !featureOff,
    staleTime: 30_000,
    refetchInterval: 60_000,
  })

  // 筛选条件一变就回到第一页：停在第 5 页却只剩 2 页数据时，用户看到的是空表。
  useEffect(() => {
    setPage(1)
  }, [debouncedSearch, hours, selectedGroups, sort])

  // 分组下拉的选项必须来自「未收窄」的那一次响应：响应里的 `groups` 会跟着
  // 筛选一起变小，直接拿它当选项集会让用户再也选不回其他分组。
  const [groupOptions, setGroupOptions] = useState<string[]>([])
  useEffect(() => {
    if (selectedGroups.length > 0) return
    const groups = query.data?.groups
    if (groups == null) return
    setGroupOptions(groups.map((group) => group.group))
  }, [query.data?.groups, selectedGroups.length])

  const matrix = query.data
  const overall = matrix?.overall

  const downModels = useMemo(() => {
    if (matrix == null) return 0
    const models = new Set<string>()
    for (const cell of matrix.cells) {
      if (cell.state === 'down') models.add(cell.model)
    }
    return models.size
  }, [matrix])

  const excludedRatio =
    overall == null || overall.req_total <= 0
      ? null
      : (overall.excluded_total / overall.req_total) * 100

  if (featureOff) {
    return (
      <QySectionPageLayout>
        <QySectionPageLayout.Title>
          {t('qy_avl_title')}
        </QySectionPageLayout.Title>
        <QySectionPageLayout.Content>
          <EmptyState
            icon={CloudOff}
            title={t('qy_err_feature_off')}
            description={t('qy_cfg_disabled_desc')}
          />
        </QySectionPageLayout.Content>
      </QySectionPageLayout>
    )
  }

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>{t('qy_avl_title')}</QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyAvailabilityToolbar
            hours={hours}
            onHoursChange={setHours}
            metric={metricKey}
            onMetricChange={(next) => {
              setMetricKey(next)
              // 排序跟着主指标走。分页在服务端按模型切，排序不跟着切的话，
              // 切到「延迟」后第一页仍是可用率最差的模型，最慢的那个可能在第 7 页。
              setSort(getQyAvailMetric(next).sort)
            }}
            groupOptions={groupOptions}
            selectedGroups={selectedGroups}
            onGroupsChange={setSelectedGroups}
            search={search}
            onSearchChange={setSearch}
            sort={sort}
            onSortChange={setSort}
            definition={matrix?.definition ?? null}
            onRefresh={() => {
              void query.refetch()
            }}
            isFetching={query.isFetching}
          />

          <QyPageBoundary
            query={query}
            isEmpty={matrix != null && matrix.models.length === 0}
            emptyIcon={HeartPulse}
            emptyTitle={t('qy_avl_empty_title')}
            emptyDescription={t('qy_avl_empty_no_data')}
          >
            {matrix != null && (
              <div className='space-y-3'>
                {/* KPI 行固定展示四个维度，**不跟随主指标切换**：卡片标题写死
                    「整体可用率」却填一个延迟数字，是最容易被误读的一种错。
                    主指标只影响矩阵、趋势图与排序。 */}
                <QyStatGrid
                  items={[
                    {
                      key: 'overall',
                      label: t('qy_avl_kpi_overall'),
                      value: formatQyAvailability(overall?.availability),
                      hint: t('qy_avl_kpi_overall_hint'),
                      emphasis: true,
                    },
                    {
                      key: 'latency',
                      label: t('qy_avl_latency'),
                      value: formatQyMs(overall?.avg_latency_ms),
                      hint: t('qy_avl_samples_n', {
                        n: formatQyCount(overall?.latency_samples),
                      }),
                    },
                    {
                      key: 'ttft',
                      label: t('qy_avl_ttft'),
                      value: formatQyMs(overall?.avg_ttft_ms),
                      hint: t('qy_avl_samples_n', {
                        n: formatQyCount(overall?.ttft_samples),
                      }),
                    },
                    {
                      key: 'tps',
                      label: t('qy_avl_tps'),
                      value: formatQyTps(overall?.avg_tps),
                      hint: t('qy_avl_samples_n', {
                        n: formatQyCount(overall?.speed_samples),
                      }),
                    },
                    {
                      key: 'worst_group',
                      label: t('qy_avl_kpi_worst_group'),
                      value:
                        overall?.group == null || overall.group === ''
                          ? t('qy_avl_kpi_none')
                          : overall.group,
                    },
                    {
                      key: 'down_models',
                      label: t('qy_avl_kpi_down_models'),
                      value: (
                        <span
                          className={
                            downModels > 0 ? 'text-destructive' : undefined
                          }
                        >
                          {downModels}
                        </span>
                      ),
                    },
                    {
                      key: 'excluded',
                      label: t('qy_avl_kpi_excluded'),
                      value:
                        excludedRatio == null
                          ? t('qy_avl_kpi_none')
                          : `${excludedRatio.toFixed(1)}%`,
                      hint: t('qy_avl_kpi_excluded_hint', {
                        n: formatQyCount(overall?.excluded_total),
                      }),
                    },
                  ]}
                />

                <Tabs defaultValue='heatmap'>
                  <TabsList>
                    <TabsTrigger value='heatmap'>
                      {t('qy_avl_view_heatmap')}
                    </TabsTrigger>
                    <TabsTrigger value='table'>
                      {t('qy_avl_view_table')}
                    </TabsTrigger>
                  </TabsList>
                  <TabsContent value='heatmap'>
                    <QyAvailabilityMatrix
                      matrix={matrix}
                      metric={metric}
                      onSelectCell={setSelectedCell}
                    />
                  </TabsContent>
                  <TabsContent value='table'>
                    <QyAvailabilityTable
                      cells={matrix.cells}
                      onSelectCell={setSelectedCell}
                    />
                  </TabsContent>
                </Tabs>

                <QyPager
                  page={matrix.page}
                  pageSize={matrix.page_size}
                  total={matrix.total_models}
                  onPageChange={setPage}
                />
              </div>
            )}
          </QyPageBoundary>

          {/* 抽屉必须放在 Content 插槽内：QySectionPageLayout 只渲染
              Title / Actions / Content 三个具名插槽，其余子节点会被直接丢弃。 */}
          <QyAvailabilityCellSheet
            cell={selectedCell}
            hours={hours}
            metric={metric}
            definition={matrix?.definition ?? null}
            onClose={() => setSelectedCell(null)}
          />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
