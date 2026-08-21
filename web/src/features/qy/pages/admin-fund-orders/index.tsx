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
import { Gavel, Radar, Scale } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { qyKeys } from '../../lib/query-keys'
import { qyFundOrderStatusName } from '../../lib/status'
import type { QyFundOrder } from '../../lib/types'
import { QyPager } from '../components/qy-pager'
import { qyOpsErrorMessage } from '../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../ops/format'
import { QyFilterBar, QyFilterField } from '../ops/qy-ops-ui'
import { listQyFundOrders, reprobeQyFundOrder } from './api'
import { QyFundOrderResolveDialog } from './components/fund-order-resolve-dialog'

const PAGE_SIZE = 20
const ALL = 'all'

/** `qy_fund_orders.status` 的 int8 取值，与后端常量一一对应。 */
const STATUS_OPTIONS = [
  { value: '0', labelKey: 'qy_common_st_pending' },
  { value: '2', labelKey: 'qy_common_st_success' },
  { value: '3', labelKey: 'qy_common_st_failed' },
  { value: '4', labelKey: 'qy_common_st_uncertain' },
  { value: '5', labelKey: 'qy_common_st_reversed' },
  { value: '6', labelKey: 'qy_common_st_in_doubt' },
]

/** `qymodel.StatusUncertain`：唯一可以被人工裁决的状态。 */
const STATUS_UNCERTAIN = 4
/** `qymodel.StatusInDoubt`：主库 COMMIT 已发出、结局不明，系统正在自动复判。 */
const STATUS_IN_DOUBT = 6

const KIND_OPTIONS = [
  'transfer',
  'commission_settle',
  'commission_reverse',
  'withdraw_quota',
  'withdraw_fiat',
  'violation_fee',
]

/**
 * 对账台。
 *
 * 主视角是 **uncertain 态**：跨库两阶段下「主库到底动没动」偶尔会判定不了，
 * 那些单会停在这个状态等人裁决。默认筛选就落在 uncertain，因为其余状态的单
 * 都会由补偿任务自己收敛，不需要人看。
 *
 * in_doubt 是它前面的一档（主库 COMMIT 已发出、结局不明）。它**不**进默认视图：
 * 那些单会在一个宽限期内由探针自己收敛，把它们混进人工队列只会让真正需要人的
 * 那几笔被淹掉。它有自己的筛选项，并且在健康页上单列一格。
 */
export function QyAdminFundOrders() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('4')
  const [kind, setKind] = useState(ALL)
  const [orderNo, setOrderNo] = useState('')
  const [userId, setUserId] = useState('')
  const [resolveTarget, setResolveTarget] = useState<QyFundOrder | null>(null)

  const params = useMemo(
    () => ({
      p: page,
      page_size: PAGE_SIZE,
      status: status === ALL ? undefined : status,
      kind: kind === ALL ? undefined : kind,
      order_no: orderNo.trim() === '' ? undefined : orderNo.trim(),
      user_id: userId.trim() === '' ? undefined : Number(userId),
    }),
    [kind, orderNo, page, status, userId]
  )

  const query = useQuery({
    queryKey: qyKeys.adminFundOrders(params),
    queryFn: () => listQyFundOrders(params),
    staleTime: 15_000,
  })

  const reprobeMutation = useMutation({
    mutationFn: (order: QyFundOrder) => reprobeQyFundOrder(order.order_no),
    onSuccess: (data) => {
      toast.success(
        t('qy_cfg_fund_reprobe_done', {
          applied: data.main_applied
            ? t('qy_cfg_fund_applied_yes')
            : t('qy_cfg_fund_applied_no'),
        })
      )
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const orders = query.data?.items ?? []

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_cfg_fund_title')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <div className='space-y-3'>
          <QyFilterBar>
            <QyFilterField label={t('qy_common_status')}>
              <Select
                value={status}
                onValueChange={(value) => {
                  setStatus(value ?? ALL)
                  setPage(1)
                }}
              >
                <SelectTrigger className='w-36'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {STATUS_OPTIONS.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {t(option.labelKey)}
                    </SelectItem>
                  ))}
                  <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                </SelectContent>
              </Select>
            </QyFilterField>

            <QyFilterField label={t('qy_cfg_fund_kind')}>
              <Select
                value={kind}
                onValueChange={(value) => {
                  setKind(value ?? ALL)
                  setPage(1)
                }}
              >
                <SelectTrigger className='w-44'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
                  {KIND_OPTIONS.map((item) => (
                    <SelectItem key={item} value={item}>
                      {t(`qy_cfg_fund_kind_${item}`, { defaultValue: item })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </QyFilterField>

            <QyFilterField label={t('qy_common_order_no')}>
              <Input
                className='w-56'
                value={orderNo}
                onChange={(event) => {
                  setOrderNo(event.target.value)
                  setPage(1)
                }}
              />
            </QyFilterField>

            <QyFilterField label={t('qy_vio_filter_user_id')}>
              <Input
                className='w-28'
                inputMode='numeric'
                value={userId}
                onChange={(event) => {
                  setUserId(event.target.value.replaceAll(/\D/g, ''))
                  setPage(1)
                }}
              />
            </QyFilterField>
          </QyFilterBar>

          <QyPageBoundary
            query={query}
            isEmpty={query.data != null && orders.length === 0}
            emptyIcon={Scale}
            emptyTitle={t('qy_cfg_fund_empty')}
            emptyDescription={t('qy_cfg_fund_empty_desc')}
          >
            <div className='space-y-3'>
              <StaticDataTable
                data={orders}
                getRowKey={(row) => row.id}
                // uncertain 行整行加底色：这一页存在的全部意义就是把它们挑出来。
                getRowClassName={(row) =>
                  row.status === STATUS_UNCERTAIN ? 'bg-warning/5' : undefined
                }
                columns={[
                  {
                    id: 'created_at',
                    header: t('qy_common_created_at'),
                    cell: (row: QyFundOrder) => formatQyTs(row.created_at),
                  },
                  {
                    id: 'order_no',
                    header: t('qy_common_order_no'),
                    cell: (row: QyFundOrder) => row.order_no,
                  },
                  {
                    id: 'kind',
                    header: t('qy_cfg_fund_kind'),
                    cell: (row: QyFundOrder) =>
                      t(`qy_cfg_fund_kind_${row.kind}`, {
                        defaultValue: row.kind,
                      }),
                  },
                  {
                    id: 'amount',
                    header: t('qy_common_amount'),
                    cell: (row: QyFundOrder) => (
                      <QyAmountText quota={row.amount_quota} />
                    ),
                  },
                  {
                    id: 'user',
                    header: t('qy_common_user'),
                    cell: (row: QyFundOrder) =>
                      row.peer_user_id > 0
                        ? `#${row.user_id} → #${row.peer_user_id}`
                        : `#${row.user_id}`,
                  },
                  {
                    id: 'status',
                    header: t('qy_common_status'),
                    cell: (row: QyFundOrder) => (
                      <QyStatusBadge
                        status={qyFundOrderStatusName(row.status)}
                      />
                    ),
                  },
                  {
                    id: 'attempts',
                    header: t('qy_cfg_fund_attempts'),
                    cellClassName: 'tabular-nums',
                    cell: (row: QyFundOrder) => row.attempts,
                  },
                  {
                    id: 'last_error',
                    header: t('qy_cfg_fund_last_error'),
                    cell: (row: QyFundOrder) => (
                      <span className='space-y-0.5'>
                        <span className='block'>
                          {row.last_error === ''
                            ? QY_EMPTY_TEXT
                            : row.last_error}
                        </span>
                        {/* in_doubt 的行必须当场说清"不用你动手"，否则运营看到
                            一个不认识的状态 + 一条 COMMIT 报错，第一反应是手工重发。 */}
                        {row.status === STATUS_IN_DOUBT && (
                          <span className='text-muted-foreground block text-xs'>
                            {t('qy_cfg_fund_in_doubt_hint')}
                          </span>
                        )}
                      </span>
                    ),
                  },
                  {
                    id: 'settled_at',
                    header: t('qy_common_settled_at'),
                    cell: (row: QyFundOrder) =>
                      row.settled_at === 0
                        ? QY_EMPTY_TEXT
                        : formatQyTs(row.settled_at),
                  },
                  {
                    id: 'actions',
                    header: t('qy_common_actions'),
                    cell: (row: QyFundOrder) => (
                      <span className='flex items-center gap-1'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-sm'
                          aria-label={t('qy_cfg_fund_reprobe')}
                          disabled={reprobeMutation.isPending}
                          onClick={() => reprobeMutation.mutate(row)}
                        >
                          <Radar aria-hidden='true' />
                        </Button>
                        <Button
                          type='button'
                          variant='ghost'
                          size='icon-sm'
                          aria-label={t('qy_cfg_fund_resolve_title')}
                          // 只有 uncertain 能裁决：pending / in_doubt 交给补偿
                          // 任务自己收敛，终态改写会破坏账目的不可变性（后端也会拒）。
                          disabled={row.status !== STATUS_UNCERTAIN}
                          onClick={() => setResolveTarget(row)}
                        >
                          <Gavel aria-hidden='true' />
                        </Button>
                      </span>
                    ),
                  },
                ]}
              />

              <QyPager
                page={page}
                pageSize={PAGE_SIZE}
                total={query.data?.total ?? 0}
                onPageChange={setPage}
              />
            </div>
          </QyPageBoundary>

          <QyFundOrderResolveDialog
            order={resolveTarget}
            onClose={() => setResolveTarget(null)}
            onDone={() => {
              void queryClient.invalidateQueries({ queryKey: qyKeys.all })
            }}
          />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}
