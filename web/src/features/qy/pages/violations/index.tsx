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
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyKeys } from '../../lib/query-keys'
import {
  qyRemainingDisplay,
  qyRemainingLineKey,
  qyWindowIsUnlimited,
} from '../../lib/violation-thresholds'
import { QyPager } from '../components/qy-pager'
import { QyStatGrid } from '../components/qy-stat-grid'
import { formatQyTs } from '../ops/format'
import {
  getQyMyViolationSummary,
  listQyMyViolationCategories,
  listQyMyViolations,
} from './api'
import { QyViolationAppealDialog } from './components/appeal-dialog'
import { QyMyViolationCategoriesCard } from './components/categories-card'
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

  // 违规类型公示。项目方原话：「这些在用户前端要公示出来」。
  // 单独一个查询而不是并进 summary：它要读一张类型表 + 一批计数行，
  // 而 summary 是每次打开页面都拉的那一个，不该被它拖慢。
  const categoriesQuery = useQuery({
    queryKey: qyKeys.violationMyCategories(),
    queryFn: listQyMyViolationCategories,
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
      <QySectionPageLayout>
        <QySectionPageLayout.Title>
          {t('qy_vio_my_title')}
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

  const summary = summaryQuery.data
  const records = listQuery.data?.items ?? []
  const progress =
    summary == null || summary.ban_threshold <= 0
      ? 0
      : Math.min(100, (summary.hit_count / summary.ban_threshold) * 100)
  const remainingDisplay = qyRemainingDisplay(
    summary ?? { ban_threshold: 0, remaining: 0 }
  )
  // 三态各自的字面。查表而不是嵌套三元：这一格的三种状态说的是三件完全不同的
  // 事（已被处置 / 没有门槛 / 还差几次），叠成一行三元之后新增一态必然写错。
  const remainingText = {
    banned: t('qy_vio_my_remaining_banned'),
    none: t('qy_common_unlimited'),
    countdown:
      remainingDisplay.kind === 'countdown' ? remainingDisplay.remaining : 0,
  }[remainingDisplay.kind]

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_vio_my_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
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
                    // 「滚动 N 小时窗口」在不限期限下是一句假话:那些次数
                    // 永远不会因为时间过去而清零。整句换掉,不是换个数字。
                    hint: qyWindowIsUnlimited(summary.window_hours)
                      ? t('qy_vio_my_window_hint_unlimited')
                      : t('qy_vio_my_window_hint', {
                          hours: summary.window_hours,
                        }),
                    emphasis: true,
                  },
                  {
                    key: 'remaining',
                    label: t('qy_vio_my_remaining'),
                    // 三态由 qyRemainingDisplay 定，**不要**在这里重新拿
                    // ban_threshold 推：那个字段只描述账号总量线，而处置由
                    // 两条线的 OR 触发。详见该函数的注释。
                    value: (
                      <span
                        className={
                          remainingDisplay.kind === 'banned' ||
                          (remainingDisplay.kind === 'countdown' &&
                            remainingDisplay.remaining <= 1)
                            ? 'text-destructive'
                            : undefined
                        }
                      >
                        {remainingText}
                      </span>
                    ),
                    // 撞的是哪条线必须说：同一个「还剩 1 次」，落在账号总量线上
                    // 和落在某一个违规类型上，用户该收敛的行为不是同一件事。
                    //
                    // 而且要连**那条线自己的窗口与阈值**一起说。上面那个
                    // 「N / M」块与它的窗口提示描述的始终是账号总量线；只报
                    // 「触发线：类型」而不给类型线的数，用户看到的就是
                    // 「触发线：类型」配「阈值 0、窗口 24 小时」，两条线的数字
                    // 混在一句话里而没有任何办法分辨。
                    hint:
                      remainingDisplay.kind === 'countdown' ? (
                        <>
                          {t(qyRemainingLineKey(remainingDisplay.line))}
                          {summary.remaining_threshold != null &&
                            summary.remaining_threshold > 0 && (
                              <>
                                {' · '}
                                {qyWindowIsUnlimited(
                                  summary.remaining_window_hours ??
                                    summary.window_hours,
                                )
                                  ? t('qy_vio_my_remaining_line_scale_unlimited', {
                                      hits: summary.remaining_hit_count ?? 0,
                                      threshold: summary.remaining_threshold,
                                    })
                                  : t('qy_vio_my_remaining_line_scale', {
                                      hits: summary.remaining_hit_count ?? 0,
                                      threshold: summary.remaining_threshold,
                                      hours:
                                        summary.remaining_window_hours ??
                                        summary.window_hours,
                                    })}
                              </>
                            )}
                        </>
                      ) : undefined,
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

          {/* 公示卡片排在记录列表**之前**：用户打开这一页时最该先看到的是
              「有哪些类型、各自几次会被处置、我现在各是几次」，而不是
              一条条已经发生的记录。规则本身永远不公示 —— 那等于教人绕过。 */}
          <QyMyViolationCategoriesCard data={categoriesQuery.data} />

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
                    id: 'category',
                    header: t('qy_vio_col_category'),
                    // 命中当时冻结的公示标题。类型被归档或改名之后，
                    // 这一行仍然显示当时那个名字。
                    cell: (row: QyMyViolationRecord) => row.category || '—',
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
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
