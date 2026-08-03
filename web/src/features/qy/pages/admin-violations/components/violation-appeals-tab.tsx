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
import { Gavel, MessagesSquare } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { qyKeys } from '../../../lib/query-keys'
import { QyPager } from '../../components/qy-pager'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs, QY_EMPTY_TEXT } from '../../ops/format'
import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { listQyViolationAppeals, reviewQyViolationAppeal } from '../api'
import type { QyViolationAppeal } from '../types'

const PAGE_SIZE = 20
const ALL = 'all'

const APPEAL_STATUS_VARIANT: Record<
  string,
  'danger' | 'neutral' | 'success' | 'warning'
> = {
  pending: 'warning',
  approved: 'success',
  rejected: 'danger',
  withdrawn: 'neutral',
}

/**
 * 申诉队列。
 *
 * 自动扣费 + 自动封号必然产生误判，申诉是唯一的纠错通道；申诉通过率本身
 * 也是「这条规则该不该改」的反馈回路。
 */
export function QyViolationAppealsTab() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('pending')
  const [userId, setUserId] = useState('')
  const [target, setTarget] = useState<QyViolationAppeal | null>(null)

  const params = useMemo(
    () => ({
      p: page,
      page_size: PAGE_SIZE,
      status: status === ALL ? undefined : status,
      user_id: userId.trim() === '' ? undefined : Number(userId),
    }),
    [page, status, userId]
  )

  const query = useQuery({
    queryKey: qyKeys.adminViolationAppeals(params),
    queryFn: () => listQyViolationAppeals(params),
    staleTime: 15_000,
  })

  const appeals = query.data?.items ?? []

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
              <SelectItem value='pending'>
                {t('qy_vio_appeal_pending')}
              </SelectItem>
              <SelectItem value='approved'>
                {t('qy_vio_appeal_approved')}
              </SelectItem>
              <SelectItem value='rejected'>
                {t('qy_vio_appeal_rejected')}
              </SelectItem>
              <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
            </SelectContent>
          </Select>
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
        isEmpty={query.data != null && appeals.length === 0}
        emptyIcon={MessagesSquare}
        emptyTitle={t('qy_vio_appeals_empty')}
      >
        <div className='space-y-3'>
          <StaticDataTable
            data={appeals}
            getRowKey={(row) => row.id}
            columns={[
              {
                id: 'created_at',
                header: t('qy_common_time'),
                cell: (row: QyViolationAppeal) => formatQyTs(row.created_at),
              },
              {
                id: 'user',
                header: t('qy_common_user'),
                cell: (row: QyViolationAppeal) => `#${row.user_id}`,
              },
              {
                id: 'record',
                header: t('qy_vio_col_record_id'),
                cell: (row: QyViolationAppeal) => `#${row.record_id}`,
              },
              {
                id: 'reason',
                header: t('qy_common_reason'),
                cell: (row: QyViolationAppeal) => row.reason,
              },
              {
                id: 'status',
                header: t('qy_common_status'),
                cell: (row: QyViolationAppeal) => (
                  <StatusBadge
                    label={t(`qy_vio_appeal_${row.status}`, {
                      defaultValue: row.status,
                    })}
                    variant={APPEAL_STATUS_VARIANT[row.status] ?? 'neutral'}
                    copyable={false}
                  />
                ),
              },
              {
                id: 'reviewed_at',
                header: t('qy_vio_appeal_reviewed_at'),
                cell: (row: QyViolationAppeal) =>
                  row.reviewed_at === 0
                    ? QY_EMPTY_TEXT
                    : formatQyTs(row.reviewed_at),
              },
              {
                id: 'actions',
                header: t('qy_common_actions'),
                cell: (row: QyViolationAppeal) => (
                  <Button
                    type='button'
                    variant='ghost'
                    size='icon-sm'
                    aria-label={t('qy_vio_appeal_review')}
                    disabled={row.status !== 'pending'}
                    onClick={() => setTarget(row)}
                  >
                    <Gavel aria-hidden='true' />
                  </Button>
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

      <QyAppealReviewDialog
        appeal={target}
        onClose={() => setTarget(null)}
        onDone={() => {
          void queryClient.invalidateQueries({ queryKey: qyKeys.all })
        }}
      />
    </div>
  )
}

/**
 * 申诉复核。
 *
 * 通过时可以一次性完成三件事：撤销记录并退款、解除封禁、清零计数。三者分开
 * 勾选而不是绑定在一起 —— 「记录确实误判但封号是别的规则触发的」是真实场景。
 */
function QyAppealReviewDialog(props: {
  appeal: QyViolationAppeal | null
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useTranslation()
  const afterMoneyChange = useQyAfterMoneyChange()
  const fieldId = useId()
  const [decision, setDecision] = useState<'approved' | 'rejected'>('approved')
  const [note, setNote] = useState('')
  const [refund, setRefund] = useState(true)
  const [unban, setUnban] = useState(false)
  const [resetCounter, setResetCounter] = useState(false)

  useEffect(() => {
    setDecision('approved')
    setNote('')
    setRefund(true)
    setUnban(false)
    setResetCounter(false)
  }, [props.appeal])

  const mutation = useMutation({
    mutationFn: () =>
      reviewQyViolationAppeal(props.appeal?.id ?? 0, {
        decision,
        note,
        refund,
        unban,
        reset_counter: resetCounter,
      }),
    onSuccess: async (data) => {
      toast.success(
        data.refunded_quota > 0
          ? t('qy_vio_appeal_done_refund', { quota: data.refunded_quota })
          : t('qy_vio_appeal_done')
      )
      await afterMoneyChange()
      props.onDone()
      props.onClose()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const approved = decision === 'approved'

  return (
    <QyResponsiveDialog
      open={props.appeal != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_vio_appeal_review')}
      description={props.appeal?.reason}
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <RadioGroup
          value={decision}
          onValueChange={(value) =>
            setDecision(value === 'rejected' ? 'rejected' : 'approved')
          }
          className='flex gap-4'
        >
          <div className='flex items-center gap-2'>
            <RadioGroupItem value='approved' id={`${fieldId}-approve`} />
            <Label htmlFor={`${fieldId}-approve`} className='cursor-pointer'>
              {t('qy_common_approve')}
            </Label>
          </div>
          <div className='flex items-center gap-2'>
            <RadioGroupItem value='rejected' id={`${fieldId}-reject`} />
            <Label htmlFor={`${fieldId}-reject`} className='cursor-pointer'>
              {t('qy_common_reject')}
            </Label>
          </div>
        </RadioGroup>

        <div className='space-y-1'>
          <Label htmlFor={`${fieldId}-note`}>{t('qy_common_remark')}</Label>
          <Textarea
            id={`${fieldId}-note`}
            rows={3}
            value={note}
            onChange={(event) => setNote(event.target.value)}
          />
        </div>

        {approved && (
          <div className='space-y-2 rounded-lg border p-3'>
            <div className='flex items-center justify-between gap-4'>
              <Label htmlFor={`${fieldId}-refund`}>
                {t('qy_vio_appeal_refund')}
              </Label>
              <Switch
                id={`${fieldId}-refund`}
                checked={refund}
                onCheckedChange={setRefund}
              />
            </div>
            <div className='flex items-center justify-between gap-4'>
              <Label htmlFor={`${fieldId}-unban`}>
                {t('qy_vio_appeal_unban')}
              </Label>
              <Switch
                id={`${fieldId}-unban`}
                checked={unban}
                onCheckedChange={setUnban}
              />
            </div>
            <div className='flex items-center justify-between gap-4'>
              <Label htmlFor={`${fieldId}-reset`}>
                {t('qy_vio_unban_reset')}
              </Label>
              <Switch
                id={`${fieldId}-reset`}
                checked={resetCounter}
                onCheckedChange={setResetCounter}
              />
            </div>
          </div>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
