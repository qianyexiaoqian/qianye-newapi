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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { qyKeys } from '../../../lib/query-keys'
import { qyWindowIsUnlimited } from '../../../lib/violation-thresholds'
import { QyPager } from '../../components/qy-pager'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { listQyViolationCounters, resetQyViolationCounter } from '../api'
import type { QyViolationCounter } from '../types'

const PAGE_SIZE = 10

/**
 * 违规计数器维护。
 *
 * **它为什么存在**：本轮之前，影子模式下的命中也会推进 `hit_count`，而
 * `hit_count` 是自动封号判据的唯一输入。修复只能保证从此不再混入，
 * 历史行里哪几次来自影子已经无从分辨 —— 现网的计数器就是脏的。
 *
 * 静默清库是不可接受的：那会连真实违规的累计一起抹掉，等于给所有正在攒次数的
 * 用户一次赦免，而且没有任何记录说明这件事发生过。所以这里给的是一个显式、
 * 逐个、写审计的动作，并且后端只清 `hit_count` 与窗口起点 ——
 * `total_count`（终身累计）与 `ban_cycle`（封禁认领互斥键）都不动。
 */
export function QyViolationCounterCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [page, setPage] = useState(1)
  const [userId, setUserId] = useState('')
  const [pending, setPending] = useState<QyViolationCounter | null>(null)

  const parsedUserId = Number.parseInt(userId.trim(), 10)
  const params = {
    p: page,
    page_size: PAGE_SIZE,
    user_id:
      Number.isFinite(parsedUserId) && parsedUserId > 0
        ? parsedUserId
        : undefined,
  }

  const countersQuery = useQuery({
    queryKey: qyKeys.adminViolationCounters(params),
    queryFn: () => listQyViolationCounters(params),
    staleTime: 15_000,
  })

  const resetMutation = useMutation({
    mutationFn: (row: QyViolationCounter) =>
      resetQyViolationCounter(row.user_id, t('qy_vio_counter_reset_reason')),
    onSuccess: () => {
      toast.success(t('qy_vio_counter_reset_done'))
      setPending(null)
      void queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const rows = countersQuery.data?.items ?? []
  const threshold = countersQuery.data?.threshold ?? 0

  return (
    <section className='space-y-3'>
      <div>
        <h2 className='text-sm font-medium'>{t('qy_vio_counter_title')}</h2>
        <p className='text-muted-foreground text-xs'>
          {/* 窗口来自兜底策略档,可能是「不限期限」哨兵。整句换一句说,而不是
              把 -1 塞进 {{hours}} —— 这句话的全部作用就是说清"这些次数按什么
              时间口径算",印成「近 -1 小时窗口」等于把它作废。 */}
          {qyWindowIsUnlimited(countersQuery.data?.window_hours)
            ? t('qy_vio_counter_desc_unlimited', { threshold })
            : t('qy_vio_counter_desc', {
                hours: countersQuery.data?.window_hours ?? 0,
                threshold,
              })}
        </p>
      </div>

      <QyFilterBar>
        <QyFilterField label={t('qy_vio_filter_user_id')}>
          <Input
            className='w-32'
            inputMode='numeric'
            value={userId}
            onChange={(event) => {
              setUserId(event.target.value)
              setPage(1)
            }}
          />
        </QyFilterField>
      </QyFilterBar>

      <StaticDataTable
        data={rows}
        getRowKey={(row) => row.user_id}
        columns={[
          {
            id: 'user_id',
            header: t('qy_common_user'),
            cellClassName: 'tabular-nums',
            cell: (row: QyViolationCounter) => row.user_id,
          },
          {
            id: 'hit_count',
            header: t('qy_vio_counter_hit_count'),
            cellClassName: 'tabular-nums',
            cell: (row: QyViolationCounter) =>
              threshold > 0
                ? `${row.hit_count} / ${threshold}`
                : String(row.hit_count),
          },
          {
            id: 'total_count',
            header: t('qy_vio_counter_total_count'),
            cellClassName: 'tabular-nums',
            cell: (row: QyViolationCounter) => row.total_count,
          },
          {
            id: 'ban_cycle',
            header: t('qy_vio_counter_ban_cycle'),
            cellClassName: 'tabular-nums',
            cell: (row: QyViolationCounter) => row.ban_cycle,
          },
          {
            id: 'last_hit_at',
            header: t('qy_vio_counter_last_hit'),
            cell: (row: QyViolationCounter) =>
              row.last_hit_at === 0
                ? QY_EMPTY_TEXT
                : formatQyTs(row.last_hit_at),
          },
          {
            id: 'actions',
            header: t('qy_common_actions'),
            cell: (row: QyViolationCounter) => (
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() => setPending(row)}
              >
                {t('qy_vio_counter_reset')}
              </Button>
            ),
          },
        ]}
      />

      <QyPager
        page={page}
        pageSize={PAGE_SIZE}
        total={countersQuery.data?.total ?? 0}
        onPageChange={setPage}
      />

      <QyConfirmDialog
        open={pending != null}
        onOpenChange={(open) => {
          if (!open) setPending(null)
        }}
        title={t('qy_vio_counter_reset')}
        description={t('qy_vio_counter_reset_desc', {
          user: pending?.user_id ?? 0,
          count: pending?.hit_count ?? 0,
        })}
        confirmText={t('qy_vio_counter_reset')}
        isLoading={resetMutation.isPending}
        onConfirm={() => {
          if (pending != null) resetMutation.mutate(pending)
        }}
      />
    </section>
  )
}
