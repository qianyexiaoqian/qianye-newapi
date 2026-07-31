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
import { RefreshCw, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { GroupBadge } from '@/components/group-badge'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { TitledCard } from '@/components/ui/titled-card'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyAvailabilityDefinition } from '../availability/components/availability-definition'
import {
  formatQyTps,
  getQyAvailStateStyle,
  QY_AVAIL_RANGES,
  qyAvailOutcomeKey,
} from '../availability/constants'
import type { QyAvailCell } from '../availability/types'
import { QyStatGrid } from '../components/qy-stat-grid'
import {
  formatQyAvailability,
  formatQyCount,
  formatQyMs,
  formatQyTs,
  QY_EMPTY_TEXT,
} from '../ops/format'
import { QyFilterBar, QyFilterField, QyKeyValue } from '../ops/qy-ops-ui'
import { getQyAdminAvailabilityStats } from './api'

/**
 * 管理端可用率总览。
 *
 * 这一页回答的是运维问题而不是业务问题：口径是什么、采样有没有丢、
 * 落库有没有失败、存储涨到哪了、哪些（分组, 模型）最差。业务视角的
 * 分组×模型矩阵在 `/qy/availability`。
 */
export function QyAdminAvailability() {
  const { t } = useTranslation()
  const [hours, setHours] = useState(24)

  const query = useQuery({
    queryKey: qyKeys.adminAvailabilityStats({ hours }),
    queryFn: () => getQyAdminAvailabilityStats({ hours }),
    staleTime: 30_000,
  })

  const stats = query.data
  const dropped =
    (stats?.hot_queue.dropped ?? 0) + (stats?.sampler.dropped_series_limit ?? 0)
  const flushFailures = stats?.flush.failures ?? 0
  const attemptMismatch =
    stats?.config.sample_attempt_level_requested === true &&
    stats.config.sample_attempt_level_supported === false

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('qy_avl_admin_title')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='space-y-3'>
          <QyFilterBar>
            <QyFilterField label={t('qy_avl_range')}>
              <Select
                value={String(hours)}
                onValueChange={(value) => setHours(Number(value ?? hours))}
              >
                <SelectTrigger className='w-28'>
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
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => {
                void query.refetch()
              }}
              disabled={query.isFetching}
            >
              <RefreshCw
                aria-hidden='true'
                className={query.isFetching ? 'animate-spin' : undefined}
              />
              {t('qy_common_refresh')}
            </Button>
            {stats != null && (
              <QyAvailabilityDefinition definition={stats.definition} />
            )}
          </QyFilterBar>

          <QyPageBoundary query={query}>
            {stats != null && (
              <div className='space-y-3'>
                {/* 采样丢弃意味着统计口径已经失真：显示出来的可用率比真实值好看。
                    这必须是红色告警而不是一个安静的数字。 */}
                {dropped > 0 && (
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertTitle>{t('qy_avl_alert_dropped_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_avl_alert_dropped_desc', { n: dropped })}
                    </AlertDescription>
                  </Alert>
                )}
                {flushFailures > 0 && (
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertTitle>{t('qy_avl_alert_flush_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_avl_alert_flush_desc', { n: flushFailures })}
                    </AlertDescription>
                  </Alert>
                )}
                {attemptMismatch && (
                  <Alert>
                    <TriangleAlert />
                    <AlertTitle>{t('qy_avl_alert_attempt_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_avl_alert_attempt_desc')}
                    </AlertDescription>
                  </Alert>
                )}

                <QyStatGrid
                  items={[
                    {
                      key: 'observed',
                      label: t('qy_avl_stat_observed'),
                      value: formatQyCount(stats.sampler.observed),
                    },
                    {
                      key: 'hot_series',
                      label: t('qy_avl_stat_hot_series'),
                      value: formatQyCount(stats.sampler.hot_series),
                      hint: t('qy_avl_stat_hot_series_hint', {
                        limit: stats.config.hot_series_limit,
                      }),
                    },
                    {
                      key: 'queue',
                      label: t('qy_avl_stat_queue_pending'),
                      value: `${stats.hot_queue.pending} / ${stats.hot_queue.capacity}`,
                    },
                    {
                      key: 'dropped',
                      label: t('qy_avl_stat_dropped'),
                      value: (
                        <span
                          className={
                            dropped > 0 ? 'text-destructive' : 'text-success'
                          }
                        >
                          {formatQyCount(dropped)}
                        </span>
                      ),
                      emphasis: true,
                    },
                  ]}
                />

                <TitledCard
                  title={t('qy_avl_admin_groups')}
                  description={t('qy_avl_admin_groups_desc')}
                >
                  <QyAvailabilityCellTable cells={stats.groups} showModel />
                </TitledCard>

                <TitledCard
                  title={t('qy_avl_admin_worst')}
                  description={t('qy_avl_admin_worst_desc')}
                >
                  <QyAvailabilityCellTable
                    cells={stats.worst_cells}
                    showModel
                  />
                </TitledCard>

                <div className='grid gap-3 lg:grid-cols-2'>
                  <TitledCard title={t('qy_avl_admin_pipeline')}>
                    <QyKeyValue label={t('qy_avl_pipeline_flush_runs')}>
                      {formatQyCount(stats.flush.runs)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_flush_rows')}>
                      {formatQyCount(stats.flush.rows)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_flush_failures')}>
                      <span
                        className={
                          flushFailures > 0 ? 'text-destructive' : undefined
                        }
                      >
                        {formatQyCount(stats.flush.failures)}
                      </span>
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_flush_last')}>
                      {formatQyTs(stats.flush.last_at)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_rollup')}>
                      {`${formatQyCount(stats.rollup.rows)} · ${formatQyTs(stats.rollup.last_at)}`}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_cleanup')}>
                      {`${formatQyCount(stats.cleanup.rows)} · ${formatQyTs(stats.cleanup.last_at)}`}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_no_model')}>
                      {formatQyCount(stats.sampler.dropped_no_model)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_pipeline_truncated')}>
                      {formatQyCount(stats.sampler.truncated_names)}
                    </QyKeyValue>
                  </TitledCard>

                  <TitledCard title={t('qy_avl_admin_storage')}>
                    <QyKeyValue label={t('qy_avl_storage_bucket_rows')}>
                      {stats.storage == null
                        ? QY_EMPTY_TEXT
                        : formatQyCount(stats.storage.bucket_rows)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_storage_hour_rows')}>
                      {stats.storage == null
                        ? QY_EMPTY_TEXT
                        : formatQyCount(stats.storage.hour_rows)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_storage_oldest')}>
                      {stats.storage == null
                        ? QY_EMPTY_TEXT
                        : formatQyTs(stats.storage.oldest_bucket_ts)}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_storage_retention')}>
                      {t('qy_avl_storage_retention_value', {
                        days: stats.config.retention_days,
                      })}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_storage_bucket_seconds')}>
                      {`${stats.config.bucket_seconds}s`}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_storage_flush_interval')}>
                      {`${stats.config.flush_interval_seconds}s`}
                    </QyKeyValue>
                    <QyKeyValue label={t('qy_avl_storage_max_series')}>
                      {formatQyCount(stats.config.max_series_per_query)}
                    </QyKeyValue>
                  </TitledCard>
                </div>
              </div>
            )}
          </QyPageBoundary>
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

/** 管理端两张表结构一致（分组汇总 / 最差格子），共用一个渲染。 */
function QyAvailabilityCellTable(props: {
  cells: QyAvailCell[]
  showModel: boolean
}) {
  const { t } = useTranslation()

  return (
    <StaticDataTable
      data={props.cells}
      getRowKey={(row) => `${row.group} ${row.model}`}
      emptyContent={t('qy_avl_empty_no_data')}
      columns={[
        {
          id: 'group',
          header: t('qy_avl_group'),
          cell: (row: QyAvailCell) => (
            <GroupBadge group={row.group} size='sm' />
          ),
        },
        ...(props.showModel
          ? [
              {
                id: 'model',
                header: t('qy_avl_model'),
                cell: (row: QyAvailCell) => row.model,
              },
            ]
          : []),
        {
          id: 'availability',
          header: t('qy_avl_availability'),
          cellClassName: 'tabular-nums',
          cell: (row: QyAvailCell) => formatQyAvailability(row.availability),
        },
        {
          id: 'state',
          header: t('qy_common_status'),
          cell: (row: QyAvailCell) => {
            const style = getQyAvailStateStyle(row.state)
            return (
              <StatusBadge
                label={t(style.labelKey)}
                variant={style.badge}
                copyable={false}
              />
            )
          },
        },
        {
          id: 'req_total',
          header: t('qy_avl_req_total'),
          cellClassName: 'tabular-nums',
          cell: (row: QyAvailCell) => formatQyCount(row.req_total),
        },
        {
          id: 'top_reason',
          header: t('qy_avl_top_reason'),
          cell: (row: QyAvailCell) =>
            row.top_reason == null || row.top_reason === ''
              ? QY_EMPTY_TEXT
              : t(qyAvailOutcomeKey(row.top_reason)),
        },
        // 运维视角同样要看延迟与速度：可用率 100% 但首字 20 秒，
        // 用户的体感就是「挂了」，只看可用率会漏掉整整一类故障。
        {
          id: 'latency',
          header: t('qy_avl_latency'),
          cellClassName: 'tabular-nums',
          cell: (row: QyAvailCell) => formatQyMs(row.avg_latency_ms),
        },
        {
          id: 'ttft',
          header: t('qy_avl_ttft'),
          cellClassName: 'tabular-nums',
          cell: (row: QyAvailCell) => formatQyMs(row.avg_ttft_ms),
        },
        {
          id: 'tps',
          header: t('qy_avl_tps'),
          cellClassName: 'tabular-nums',
          cell: (row: QyAvailCell) => formatQyTps(row.avg_tps),
        },
      ]}
    />
  )
}
