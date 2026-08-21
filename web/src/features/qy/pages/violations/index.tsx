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

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { qyKeys } from '../../lib/query-keys'
import { QyPager } from '../components/qy-pager'
import { formatQyTs } from '../ops/format'
import { listQyMyViolationCategories, listQyMyViolations } from './api'
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
 * 只会换来工单与差评。展示内容严格分层 —— 时间 / 模型 / 对外原因 / 金额给看，
 * 命中词与上下文不给看（那等于把规则库送出去）。
 *
 * ── 这一页刻意**不**显示的三块 ──
 * 「当前窗口违规次数」「距离封号还剩余」「累计扣费」三个统计块已按项目方要求
 * 移除（原话：「我的违规记录，这里只显示违规类型就行」）。后端 `my-summary`
 * 仍然如实下发那些字段，只是用户端不渲染 —— 理由与代价写在 `types.ts` 的
 * `QyMyViolationSummary` 上，**先读那段注释再动手加回来**。
 *
 * 「我离处置还有多远」这件事没有丢：下面的公示卡片
 * （`QyMyViolationCategoriesCard`）逐条给出账号总量线与每一个违规类型自己的
 * 「我几次 / 到几次 / 到了会怎样 / 还差几次」。它是现在**唯一**的预警渠道，
 * 站点一个类型都没公示时整块会收起，那种配置下用户不再有任何倒计时。
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

  // 违规类型公示。项目方原话：「这些在用户前端要公示出来」。
  // 三个统计块移除之后这是这一页唯一的聚合视图，`my-summary` 不再被拉取
  // （那一个接口只喂过那三块）。
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

  const records = listQuery.data?.items ?? []

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_vio_my_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
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
