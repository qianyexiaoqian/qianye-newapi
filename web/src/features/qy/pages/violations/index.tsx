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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CloudOff, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { SectionPageLayout } from '@/components/layout'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid } from '../components/qy-stat-grid'
import { formatQyTs } from '../ops/format'
import { getQyMyViolationSummary, listQyMyViolations } from './api'
import { QyViolationAppealDialog } from './components/appeal-dialog'
import type { QyMyViolationRecord } from './types'

const PAGE_SIZE = 20

const MY_STATUS_VARIANT: Record<string, 'danger' | 'success' | 'warning'> = {
  active: 'danger',
  appealed: 'warning',
  revoked: 'success',
}

/**
 * 我的违规记录。
 *
 * 存在的理由很简单：**钱被扣了必须给理由**。没有这一页，扣费对用户就是黑箱，
 * 只会换来工单与差评。展示内容严格分层 —— 时间 / 模型 / 对外原因 / 金额 /
 * 剩余次数给看，命中词与上下文不给看（那等于把规则库送出去）。
 */
export function QyMyViolations() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const config = useQyConfig()

  const [page, setPage] = useState(1)
  const [appealRecord, setAppealRecord] = useState<QyMyViolationRecord | null>(
    null
  )

  const featureOff = config.status === 'enabled' && !config.features.violation

  const summaryQuery = useQuery({
    queryKey: qyKeys.violationMySummary(),
    queryFn: getQyMyViolationSummary,
    enabled: !featureOff,
    staleTime: 60_000,
  })

  const listParams = { p: page, page_size: PAGE_SIZE }
  const listQuery = useQuery({
    queryKey: qyKeys.violationMyRecords(listParams),
    queryFn: () => listQyMyViolations(listParams),
    enabled: !featureOff,
    staleTime: 30_000,
  })

  if (featureOff) {
    return (
      <SectionPageLayout>
        <SectionPageLayout.Title>
          {t('qy_vio_my_title')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <EmptyState
            icon={CloudOff}
            title={t('qy_err_feature_off')}
            description={t('qy_cfg_disabled_desc')}
          />
        </SectionPageLayout.Content>
      </SectionPageLayout>
    )
  }

  const summary = summaryQuery.data
  const records = listQuery.data?.items ?? []
  const progress =
    summary == null || summary.ban_threshold <= 0
      ? 0
      : Math.min(100, (summary.hit_count / summary.ban_threshold) * 100)

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>{t('qy_vio_my_title')}</SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <div className='space-y-3'>
          {summary != null && (
            <div className='space-y-2'>
              <QyStatGrid
                items={[
                  {
                    key: 'hits',
                    label: t('qy_vio_my_window_hits'),
                    value:
                      summary.ban_threshold > 0
                        ? `${summary.hit_count} / ${summary.ban_threshold}`
                        : String(summary.hit_count),
                    hint: t('qy_vio_my_window_hint', {
                      hours: summary.window_hours,
                    }),
                    emphasis: true,
                  },
                  {
                    key: 'remaining',
                    label: t('qy_vio_my_remaining'),
                    // 剩余 1 次时标红：这是用户主动收敛行为的最后提醒。
                    value: (
                      <span
                        className={
                          summary.ban_threshold > 0 && summary.remaining <= 1
                            ? 'text-destructive'
                            : undefined
                        }
                      >
                        {summary.ban_threshold > 0
                          ? summary.remaining
                          : t('qy_common_unlimited')}
                      </span>
                    ),
                  },
                  {
                    key: 'total_fee',
                    label: t('qy_vio_my_total_fee'),
                    value: <QyAmountText quota={summary.total_fee_quota} />,
                  },
                ]}
              />
              {summary.ban_threshold > 0 && (
                <Progress
                  value={progress}
                  aria-label={t('qy_vio_my_progress')}
                />
              )}
            </div>
          )}

          <QyPageBoundary
            query={listQuery}
            isEmpty={listQuery.data != null && records.length === 0}
            emptyIcon={ShieldCheck}
            emptyTitle={t('qy_vio_my_empty')}
            emptyDescription={t('qy_vio_my_empty_desc')}
          >
            <div className='space-y-3'>
              <StaticDataTable
                data={records}
                getRowKey={(row) => row.id}
                columns={[
                  {
                    id: 'created_at',
                    header: t('qy_common_time'),
                    cell: (row: QyMyViolationRecord) =>
                      formatQyTs(row.created_at),
                  },
                  {
                    id: 'model',
                    header: t('qy_avl_model'),
                    cell: (row: QyMyViolationRecord) => row.model_name,
                  },
                  {
                    id: 'reason',
                    header: t('qy_common_reason'),
                    cell: (row: QyMyViolationRecord) => (
                      <span className='flex flex-wrap items-center gap-1'>
                        {row.reason}
                        {row.blocked && (
                          <Badge variant='destructive'>
                            {t('qy_vio_flag_blocked')}
                          </Badge>
                        )}
                      </span>
                    ),
                  },
                  {
                    id: 'fee',
                    header: t('qy_vio_col_fee'),
                    cell: (row: QyMyViolationRecord) => (
                      <QyAmountText quota={row.fee_quota} />
                    ),
                  },
                  {
                    id: 'status',
                    header: t('qy_common_status'),
                    cell: (row: QyMyViolationRecord) => (
                      <StatusBadge
                        label={t(`qy_vio_record_${row.status}`, {
                          defaultValue: row.status,
                        })}
                        variant={MY_STATUS_VARIANT[row.status] ?? 'neutral'}
                        copyable={false}
                      />
                    ),
                  },
                  {
                    id: 'actions',
                    header: t('qy_common_actions'),
                    cell: (row: QyMyViolationRecord) => (
                      <Button
                        type='button'
                        variant='outline'
                        size='sm'
                        disabled={row.status !== 'active'}
                        onClick={() => setAppealRecord(row)}
                      >
                        {t('qy_vio_my_appeal_title')}
                      </Button>
                    ),
                  },
                ]}
              />

              <QyPager
                page={page}
                pageSize={PAGE_SIZE}
                total={listQuery.data?.total ?? 0}
                onPageChange={setPage}
              />
            </div>
          </QyPageBoundary>

          <QyViolationAppealDialog
            record={appealRecord}
            onClose={() => setAppealRecord(null)}
            onDone={() => {
              void queryClient.invalidateQueries({ queryKey: qyKeys.all })
            }}
          />
        </div>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
