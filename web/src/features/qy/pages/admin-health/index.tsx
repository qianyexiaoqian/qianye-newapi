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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { RefreshCw, RotateCw, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { TitledCard } from '@/components/ui/titled-card'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { qyKeys } from '../../lib/query-keys'
import { QyStatGrid } from '../components/qy-stat-grid'
import { qyOpsErrorMessage } from '../ops/errors'
import {
  formatQyCount,
  formatQyDuration,
  formatQyMs,
  formatQyTs,
  QY_EMPTY_TEXT,
} from '../ops/format'
import { QyKeyValue } from '../ops/qy-ops-ui'
import { getQyAdminHealth, listQyLeases, reloadQyConfig } from './api'
import type { QyLeaseListItem } from './types'

/**
 * 扩展健康面板。
 *
 * 排障的第一入口：数据库通不通、熔断开没开、连接池是不是打满、后台任务的
 * 租约在谁手里、热路径队列有没有丢事件、配置是从哪个文件加载的。
 *
 * **队列丢弃数是这一页最重要的一个数字**：它是全扩展唯一会造成
 * 「用户该拿的钱没拿到」的路径，非零必须红字告警。
 */
export function QyAdminHealth() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [reloadOpen, setReloadOpen] = useState(false)

  const healthQuery = useQuery({
    queryKey: qyKeys.adminHealth(),
    queryFn: getQyAdminHealth,
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  const leasesQuery = useQuery({
    queryKey: qyKeys.adminLeases(),
    queryFn: listQyLeases,
    staleTime: 10_000,
    refetchInterval: 30_000,
  })

  const reloadMutation = useMutation({
    mutationFn: reloadQyConfig,
    onSuccess: () => {
      toast.success(t('qy_cfg_health_reload_done'))
      setReloadOpen(false)
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const health = healthQuery.data
  const now = Math.floor(Date.now() / 1000)
  const dropped = health?.hot_queue.dropped ?? 0
  const breakerOpen = (health?.db.breaker_open_until ?? 0) > now
  const uncertain = health?.two_phase.uncertain ?? 0

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('qy_cfg_health_title')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={healthQuery.isFetching}
          onClick={() => {
            void healthQuery.refetch()
            void leasesQuery.refetch()
          }}
        >
          <RefreshCw
            aria-hidden='true'
            className={healthQuery.isFetching ? 'animate-spin' : undefined}
          />
          {t('qy_common_refresh')}
        </Button>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() => setReloadOpen(true)}
        >
          <RotateCw aria-hidden='true' />
          {t('qy_cfg_health_reload')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <QyPageBoundary query={healthQuery}>
          {health != null && (
            <div className='space-y-3'>
              {/* 丢弃 = 佣金 / 违规记录 / 采样事件被永久丢掉，无法补回。 */}
              {dropped > 0 && (
                <Alert variant='destructive'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_cfg_health_drop_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_cfg_health_drop_desc', { n: dropped })}
                  </AlertDescription>
                </Alert>
              )}
              {breakerOpen && (
                <Alert variant='destructive'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_cfg_health_breaker_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_cfg_health_breaker_desc', {
                      time: formatQyTs(health.db.breaker_open_until),
                    })}
                  </AlertDescription>
                </Alert>
              )}

              <QyStatGrid
                items={[
                  {
                    key: 'db',
                    label: t('qy_cfg_health_db'),
                    value: (
                      <StatusBadge
                        label={
                          health.db.available
                            ? t('qy_cfg_health_db_up')
                            : t('qy_cfg_health_db_down')
                        }
                        variant={health.db.available ? 'success' : 'danger'}
                        copyable={false}
                        size='md'
                      />
                    ),
                    hint: t('qy_cfg_health_ping', {
                      ms: formatQyMs(health.db.last_ping_ms),
                    }),
                  },
                  {
                    key: 'queue',
                    label: t('qy_cfg_health_queue'),
                    value: `${health.hot_queue.pending} / ${health.hot_queue.capacity}`,
                    hint: t('qy_cfg_health_submitted', {
                      n: formatQyCount(health.hot_queue.submitted),
                    }),
                  },
                  {
                    key: 'dropped',
                    label: t('qy_cfg_health_dropped'),
                    // 丢弃是本页最重要的一个数字：非零意味着有影响资金的
                    // 事件被永久丢掉，必须一眼看到。
                    value: (
                      <span
                        className={
                          dropped > 0 ? 'text-destructive' : 'text-success'
                        }
                      >
                        {formatQyCount(dropped)}
                      </span>
                    ),
                    hint: t('qy_cfg_health_dropped_hint'),
                    emphasis: true,
                  },
                  {
                    key: 'uncertain',
                    label: t('qy_cfg_health_uncertain'),
                    value: (
                      <span
                        className={uncertain > 0 ? 'text-warning' : undefined}
                      >
                        {formatQyCount(uncertain)}
                      </span>
                    ),
                    hint: t('qy_cfg_health_uncertain_hint'),
                  },
                ]}
              />

              <div className='grid gap-3 lg:grid-cols-2'>
                <TitledCard title={t('qy_cfg_health_db_pool')}>
                  <QyKeyValue label={t('qy_cfg_health_connected')}>
                    {health.db.connected
                      ? t('qy_cfg_health_yes')
                      : t('qy_cfg_health_no')}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_fail_streak')}>
                    {health.db.fail_streak}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_last_ping_at')}>
                    {formatQyTs(health.db.last_ping_at)}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_open_conns')}>
                    {health.db.open_conns == null
                      ? QY_EMPTY_TEXT
                      : `${health.db.open_conns} / ${health.db.max_open ?? 0}`}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_in_use')}>
                    {health.db.in_use ?? QY_EMPTY_TEXT}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_idle')}>
                    {health.db.idle ?? QY_EMPTY_TEXT}
                  </QyKeyValue>
                  {/* wait_count 持续增长 = 连接池太小，请求在排队等连接。 */}
                  <QyKeyValue label={t('qy_cfg_health_wait_count')}>
                    {health.db.wait_count ?? QY_EMPTY_TEXT}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_tables')}>
                    {health.migrate.table_count}
                  </QyKeyValue>
                </TitledCard>

                <TitledCard title={t('qy_cfg_health_two_phase')}>
                  <QyKeyValue label={t('qy_common_st_pending')}>
                    {health.two_phase.pending ?? QY_EMPTY_TEXT}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_common_st_uncertain')}>
                    {health.two_phase.uncertain ?? QY_EMPTY_TEXT}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_oldest_pending')}>
                    {formatQyDuration(health.two_phase.oldest_pending_age_sec)}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_common_order_no')}>
                    {health.two_phase.oldest_pending_order_no ?? QY_EMPTY_TEXT}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_node')}>
                    {health.node.name}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_is_master')}>
                    {health.node.is_master
                      ? t('qy_cfg_health_yes')
                      : t('qy_cfg_health_no')}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_config_path')}>
                    {health.config.path}
                  </QyKeyValue>
                  <QyKeyValue label={t('qy_cfg_health_config_loaded')}>
                    {formatQyTs(health.config.loaded_at)}
                  </QyKeyValue>
                </TitledCard>
              </div>

              <TitledCard
                title={t('qy_cfg_health_leases')}
                description={t('qy_cfg_health_leases_desc')}
              >
                <StaticDataTable
                  data={leasesQuery.data?.items ?? []}
                  getRowKey={(row) => row.name}
                  emptyContent={t('qy_cfg_health_leases_empty')}
                  columns={[
                    {
                      id: 'name',
                      header: t('qy_cfg_health_lease_name'),
                      cell: (row: QyLeaseListItem) => row.name,
                    },
                    {
                      id: 'holder',
                      header: t('qy_cfg_health_lease_holder'),
                      cell: (row: QyLeaseListItem) =>
                        row.holder === leasesQuery.data?.self
                          ? `${row.holder} (${t('qy_cfg_health_lease_self')})`
                          : row.holder,
                    },
                    {
                      id: 'fence',
                      header: t('qy_cfg_health_lease_fence'),
                      cellClassName: 'tabular-nums',
                      cell: (row: QyLeaseListItem) => row.fence,
                    },
                    {
                      id: 'lease_until',
                      header: t('qy_cfg_health_lease_until'),
                      cell: (row: QyLeaseListItem) =>
                        formatQyTs(row.lease_until),
                    },
                    {
                      id: 'expired',
                      header: t('qy_common_status'),
                      cell: (row: QyLeaseListItem) => (
                        <StatusBadge
                          label={
                            row.expired
                              ? t('qy_cfg_health_lease_expired')
                              : t('qy_cfg_health_lease_held')
                          }
                          variant={row.expired ? 'warning' : 'success'}
                          copyable={false}
                        />
                      ),
                    },
                  ]}
                />
              </TitledCard>

              <QyConfirmDialog
                open={reloadOpen}
                onOpenChange={setReloadOpen}
                title={t('qy_cfg_health_reload')}
                description={t('qy_cfg_health_reload_desc')}
                confirmText={t('qy_cfg_health_reload')}
                isLoading={reloadMutation.isPending}
                onConfirm={() => reloadMutation.mutate()}
              />
            </div>
          )}
        </QyPageBoundary>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
