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
import { Link } from '@tanstack/react-router'
import { Gavel, RotateCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyMaskedUser } from '../../../components/qy-masked-user'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { qyLotPayoutBadgeStatus } from '../../lottery/lib/display'
import { QY_EMPTY_TEXT, formatQyTs } from '../../ops/format'
import { QyFilterBar, QyFilterField, QyKeyValue } from '../../ops/qy-ops-ui'
import { qyAdminLotPayoutsQuery, retryQyLotPayout } from '../api'
import type { QyLotAdminPayout } from '../types'
import { QyLotPayoutAdjudicateDialog } from './lottery-payout-adjudicate-dialog'

const PAGE_SIZE = 20
const ALL = 'all'

/** 只有这两种状态还需要人来推一把。其余的要么在自动重试，要么已经终了。 */
const RETRYABLE = new Set(['held', 'failed'])

/**
 * 派奖 / 赔付 / 退款进度。
 *
 * ## `held` 是这一页真正的工作项
 *
 * 它表示自动重试次数已经耗尽 —— 通常是收款账号被封禁或注销（给这类账号加额度
 * 在后端是走不通的）。**系统不会自动放弃、也不会自动改判**：钱是用户赢的，
 * 不能因为账号状态就悄悄没收。所以这一行会一直挂在这里等人处理。
 *
 * ## 「重试」不是「重发」
 *
 * 它只把 `next_attempt_at` 置零并把 `held` 推回 `planned`，绝不新建 payout 行。
 * 幂等键就是 `payout_no`，重入必然命中原来那张资金单 —— 这也是为什么这个按钮
 * 可以放心地让人多点几次。
 *
 * ## 「重试」按不动的那一档走「人工落账」
 *
 * 重试只在主库探针明确说“钱没动”时才能换代次出手。探针说“可能已生效”而
 * 资金单又已判失败时，它会 409 —— 而那一笔同时也不在任何后台任务的扫描范围里。
 * 没有人工落账的话，那笔钱永久挂在冻结中，而这一场活动也永远过不了删除闸门。
 *
 * 它是超级管理员专属（后端 `middleware.RootActionLotteryPayoutAdjudicate`）。
 * role=10 看到的不是一个点了吃 403 的按钮，也不是一片空白，而是一句“该去找谁”
 * —— 与录入开奖结果那一处（detail.tsx）同一条口径。
 */
export function QyLotPayoutsTab(props: { actNo: string }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState(ALL)
  const [target, setTarget] = useState<QyLotAdminPayout | null>(null)
  const [adjudicating, setAdjudicating] = useState<QyLotAdminPayout | null>(
    null
  )
  const isRoot =
    useAuthStore((state) => state.auth.user?.role) === ROLE.SUPER_ADMIN

  const params = {
    p: page,
    page_size: PAGE_SIZE,
    status: status === ALL ? undefined : status,
  }
  const query = useQuery(qyAdminLotPayoutsQuery(props.actNo, params))
  const items = qyArray(query.data?.items)
  // 冻结中的那几笔才是这一页真正的工作项。role=10 看得见它们、但落不了账，
  // 所以必须有一句话告诉他该找谁 —— 只藏按钮不给出口，屏幕上与“没事可做”同形。
  const heldCount = items.filter((row) => row.status === 'held').length

  const retry = useMutation({
    mutationFn: (payoutNo: string) => retryQyLotPayout(props.actNo, payoutNo),
    onSuccess: async () => {
      toast.success(t('qy_lot_retry_done'))
      setTarget(null)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
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
              <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
              <SelectItem value='planned'>{t('qy_lot_ps_planned')}</SelectItem>
              <SelectItem value='paying'>{t('qy_lot_ps_paying')}</SelectItem>
              <SelectItem value='paid'>{t('qy_lot_ps_paid')}</SelectItem>
              <SelectItem value='failed'>{t('qy_lot_ps_failed')}</SelectItem>
              <SelectItem value='held'>{t('qy_lot_ps_held')}</SelectItem>
            </SelectContent>
          </Select>
        </QyFilterField>
      </QyFilterBar>

      {heldCount > 0 && !isRoot && (
        <p className='text-warning text-xs leading-5'>
          {t('qy_lot_adjudicate_root_only')}
        </p>
      )}

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyTitle={t('qy_lot_a_payouts_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={items}
            getRowKey={(row: QyLotAdminPayout) => row.payout_no}
            columns={[
              {
                id: 'kind',
                header: t('qy_lot_payout_kind'),
                cell: (row: QyLotAdminPayout) =>
                  t(`qy_lot_payout_kind_${row.kind}`, {
                    defaultValue: row.kind,
                  }),
              },
              {
                id: 'user',
                header: t('qy_common_user'),
                cell: (row: QyLotAdminPayout) => (
                  <QyMaskedUser
                    userId={row.user_id}
                    maskedName={row.username}
                    copyable
                  />
                ),
              },
              {
                id: 'tier',
                header: t('qy_lot_tier'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotAdminPayout) =>
                  row.tier === 0 ? QY_EMPTY_TEXT : row.tier,
              },
              {
                id: 'amount',
                header: t('qy_common_amount'),
                cell: (row: QyLotAdminPayout) => (
                  <QyAmountText quota={row.amount_quota} />
                ),
              },
              {
                id: 'status',
                header: t('qy_common_status'),
                cell: (row: QyLotAdminPayout) => (
                  <QyStatusBadge status={qyLotPayoutBadgeStatus(row.status)} />
                ),
              },
              {
                id: 'attempts',
                header: t('qy_lot_attempts'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotAdminPayout) => row.attempts,
              },
              {
                id: 'order_no',
                header: t('qy_common_order_no'),
                cell: (row: QyLotAdminPayout) =>
                  row.order_no === '' ? (
                    QY_EMPTY_TEXT
                  ) : (
                    <Link
                      to='/qy/admin/fund-orders'
                      className='font-mono text-xs hover:underline'
                    >
                      {row.order_no}
                    </Link>
                  ),
              },
              {
                id: 'last_error',
                header: t('qy_lot_last_error'),
                cell: (row: QyLotAdminPayout) =>
                  row.last_error === '' ? QY_EMPTY_TEXT : row.last_error,
              },
              {
                id: 'settled_at',
                header: t('qy_common_settled_at'),
                cell: (row: QyLotAdminPayout) =>
                  row.settled_at === 0
                    ? QY_EMPTY_TEXT
                    : formatQyTs(row.settled_at),
              },
              {
                id: 'actions',
                header: t('qy_common_actions'),
                cell: (row: QyLotAdminPayout) => (
                  <div className='flex items-center gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon-sm'
                      aria-label={t('qy_lot_retry_title')}
                      disabled={!RETRYABLE.has(row.status)}
                      onClick={() => setTarget(row)}
                    >
                      <RotateCw aria-hidden='true' />
                    </Button>
                    {/* 人工落账只对 held 开放（后端同样只收 held），并且只对超管
                        渲染。不用 disabled：一个灰掉的按钮说不出“为什么不行”，
                        而表头上方那句话才是给 role=10 的出口。 */}
                    {isRoot && row.status === 'held' && (
                      <Button
                        type='button'
                        variant='ghost'
                        size='icon-sm'
                        aria-label={t('qy_lot_adjudicate_title')}
                        onClick={() => setAdjudicating(row)}
                      >
                        <Gavel aria-hidden='true' />
                      </Button>
                    )}
                  </div>
                ),
              },
            ]}
          />
          <QyPager
            page={page}
            pageSize={PAGE_SIZE}
            total={query.data?.total ?? 0}
            onPageChange={setPage}
            disabled={query.isFetching}
          />
        </div>
      </QyPageBoundary>

      <QyConfirmDialog
        open={target != null}
        onOpenChange={(open) => {
          if (!open) setTarget(null)
        }}
        title={t('qy_lot_retry_title')}
        description={t('qy_lot_retry_desc')}
        isLoading={retry.isPending}
        details={
          target == null ? null : (
            <div>
              <QyKeyValue label={t('qy_lot_payout_no')}>
                <span className='font-mono text-xs'>{target.payout_no}</span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_common_amount')}>
                <QyAmountText quota={target.amount_quota} />
              </QyKeyValue>
              <QyKeyValue label={t('qy_lot_last_error')}>
                {target.last_error === '' ? QY_EMPTY_TEXT : target.last_error}
              </QyKeyValue>
            </div>
          )
        }
        onConfirm={() => {
          if (target != null) retry.mutate(target.payout_no)
        }}
      />

      <QyLotPayoutAdjudicateDialog
        actNo={props.actNo}
        payout={adjudicating}
        onClose={() => setAdjudicating(null)}
      />
    </div>
  )
}
