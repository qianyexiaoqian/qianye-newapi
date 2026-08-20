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
import { Eye, TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { QyTimeline } from '../../../components/qy-timeline'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { QyFiatText } from '../../components/qy-fiat-text'
import { qyPayeeChannelKey } from '../../withdraw/lib/payee-spec'
import {
  buildQyWithdrawTimeline,
  qyWithdrawActionKey,
} from '../../withdrawals/lib/timeline'
import {
  qyAdminWithdrawalQuery,
  qyApproveWithdrawal,
  qyFailWithdrawal,
  qyMarkWithdrawalPaid,
  qyRejectWithdrawal,
} from '../api'

/** 后端 `maxReasonRunes`。理由列宽固定，超长会直接写库失败。 */
const MAX_REASON_RUNES = 200

type ReviewAction = 'fail' | 'mark-paid' | 'reject'

type ReviewDialogProps = {
  withdrawalId: number | null
  onClose: () => void
  onReveal: (id: number) => void
}

/**
 * 提现审核 / 发放弹窗。
 *
 * **系统不发钱。** 审核通过只是把单据放进「待发放」队列，钱由管理员自己去加
 * 站内额度或线下打款，回来点「标记已发放」登记。因此这个弹窗承担的是
 * "告诉管理员该发多少、发给谁"，以及"逼他把发出去的数复述一遍"。
 *
 * 四条硬性约束，都不是样式问题：
 *   1. **驳回 / 发放失败的理由必填**，且必须写进事件流水 ——
 *      用户端时间线会原样展示它，那是"为什么被拒"的唯一答案；
 *   2. **标记已发放必须填发放凭证**，后端缺它直接 400：没有凭证的发放记录
 *      在争议时等于没发；
 *   3. **标记已发放必须复述实发金额**，后端要求与单据金额逐值相等。输入框
 *      预填单据上的数、并在旁边把它显示出来 —— 让人看着数字点确认，而不是
 *      让他背下来；改动它就会被后端挡回（`qy_wd_payout_amount_mismatch`）；
 *   4. 409（`qy_wd_status_conflict`）意味着这单已被另一个管理员处理过，
 *      必须刷新列表并关闭弹窗，绝不能重试 —— 重试就是重复发钱。
 */
export function ReviewDialog(props: ReviewDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const reasonId = useId()
  const refId = useId()
  const noteId = useId()
  const amountId = useId()

  const [action, setAction] = useState<ReviewAction | null>(null)
  const [reason, setReason] = useState('')
  const [payoutRef, setPayoutRef] = useState('')
  const [payoutNote, setPayoutNote] = useState('')
  /** 实发金额的复述值。字符串而不是 number：法币是 decimal，parseFloat 会丢位。 */
  const [payoutAmount, setPayoutAmount] = useState('')

  const query = useQuery({
    ...qyAdminWithdrawalQuery(props.withdrawalId ?? 0),
    enabled: props.withdrawalId != null,
  })
  const withdrawal = query.data

  useEffect(() => {
    setAction(null)
    setReason('')
    setPayoutRef('')
    setPayoutNote('')
    setPayoutAmount('')
  }, [props.withdrawalId])

  /**
   * 四个决策接口共用的收尾。
   *
   * 无论成败都要全量失效 `['qy']`：一次审核会同时改动队列、角标、单据详情
   * 与用户的佣金余额，前端没有办法精确知道哪些视图受影响。
   *
   * 409 表示这单已被另一个管理员处理过 —— 必须关闭弹窗让人重新看最新状态，
   * **绝不重试**，重试就是重复打款。
   */
  const handlers = {
    onSuccess: async () => {
      toast.success(t('qy_wd_a_done'))
      setAction(null)
      setReason('')
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: async (error: unknown) => {
      toast.error(qyErrorMessage(error, t))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      if (isQyError(error) && error.kind === 'conflict') props.onClose()
    },
  }

  const approveMutation = useMutation({
    mutationFn: qyApproveWithdrawal,
    ...handlers,
  })
  const rejectMutation = useMutation({
    mutationFn: qyRejectWithdrawal,
    ...handlers,
  })
  const markPaidMutation = useMutation({
    mutationFn: qyMarkWithdrawalPaid,
    ...handlers,
  })
  const failMutation = useMutation({
    mutationFn: qyFailWithdrawal,
    ...handlers,
  })

  const busy =
    approveMutation.isPending ||
    rejectMutation.isPending ||
    markPaidMutation.isPending ||
    failMutation.isPending

  const reasonLength = [...reason.trim()].length
  const reasonInvalid = reasonLength === 0 || reasonLength > MAX_REASON_RUNES

  const isFiat = withdrawal?.method === 'fiat'
  /** 单据上"该发多少"的权威值。fiat 是实付法币，quota 是额度整数。 */
  const owedAmount = isFiat
    ? (withdrawal?.net_amount ?? '')
    : String(withdrawal?.quota ?? '')
  const payoutAmountInvalid = payoutAmount.trim() === ''

  return (
    <QyResponsiveDialog
      open={props.withdrawalId != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_wd_a_review_title')}
      description={withdrawal?.withdraw_no}
    >
      {query.isLoading && <LoadingState />}
      {withdrawal != null && (
        <div className='space-y-4'>
          {withdrawal.sla_breached && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>
                {withdrawal.sla_kind === 'payout'
                  ? t('qy_wd_a_payout_sla_title')
                  : t('qy_wd_a_sla_title')}
              </AlertTitle>
              <AlertDescription>
                {withdrawal.sla_kind === 'payout'
                  ? t('qy_wd_a_payout_sla_desc', {
                      deadline: formatTimestampToDate(withdrawal.sla_deadline),
                    })
                  : t('qy_wd_a_sla_desc', {
                      deadline: formatTimestampToDate(withdrawal.sla_deadline),
                    })}
              </AlertDescription>
            </Alert>
          )}
          {withdrawal.status === 'approved' && (
            <Alert>
              <TriangleAlert />
              <AlertTitle>{t('qy_wd_a_manual_payout_title')}</AlertTitle>
              <AlertDescription>
                {withdrawal.method === 'fiat'
                  ? t('qy_wd_a_manual_payout_fiat')
                  : t('qy_wd_a_manual_payout_quota')}
              </AlertDescription>
            </Alert>
          )}
          {withdrawal.risk_flags !== '' && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>{t('qy_wd_a_risk_title')}</AlertTitle>
              <AlertDescription>
                {t(
                  `qy_wd_risk_${withdrawal.risk_flags}`,
                  withdrawal.risk_flags
                )}
              </AlertDescription>
            </Alert>
          )}

          <dl className='divide-border divide-y text-sm'>
            <Row
              label={t('qy_common_status')}
              value={<QyStatusBadge status={withdrawal.status} />}
            />
            <Row
              label={t('qy_common_user')}
              value={`#${withdrawal.user_id} ${withdrawal.username}`}
            />
            <Row
              label={t('qy_wd_method')}
              value={t(`qy_wd_m_${withdrawal.method}`)}
            />
            <Row
              label={t('qy_wd_amount')}
              value={<QyAmountText quota={withdrawal.quota} />}
            />
            {withdrawal.method === 'fiat' && (
              <>
                <Row
                  label={t('qy_wd_net')}
                  value={
                    <QyFiatText
                      amount={withdrawal.net_amount}
                      currency={withdrawal.currency}
                      className='font-semibold'
                    />
                  }
                />
                <Row
                  label={t('qy_wd_payee')}
                  value={
                    <span className='inline-flex items-center gap-2'>
                      <span className='truncate'>
                        {t(
                          qyPayeeChannelKey(withdrawal.payee_channel),
                          withdrawal.payee_channel
                        )}{' '}
                        · {withdrawal.payee_masked || '-'}
                      </span>
                      <Button
                        variant='outline'
                        size='xs'
                        onClick={() => props.onReveal(withdrawal.id)}
                      >
                        <Eye aria-hidden='true' />
                        {t('qy_wd_a_reveal')}
                      </Button>
                    </span>
                  }
                />
              </>
            )}
            {withdrawal.remark !== '' && (
              <Row label={t('qy_wd_remark')} value={withdrawal.remark} />
            )}
            <Row
              label={t('qy_common_created_at')}
              value={formatTimestampToDate(withdrawal.created_at)}
            />
            <Row label={t('qy_wd_a_client_ip')} value={withdrawal.client_ip} />
          </dl>

          <Separator />

          <QyTimeline items={buildQyWithdrawTimeline(withdrawal, t)} />

          {withdrawal.events != null && withdrawal.events.length > 0 && (
            <ul className='text-muted-foreground space-y-1 text-xs'>
              {withdrawal.events.map((event) => (
                // 事件流水只增不改，(时间, 动作, 目标状态) 在一张单上唯一。
                <li
                  key={`${event.created_at}-${event.action}-${event.to_status}`}
                  className='flex flex-wrap items-baseline gap-x-2'
                >
                  <span className='font-mono tabular-nums'>
                    {formatTimestampToDate(event.created_at)}
                  </span>
                  <span className='text-foreground'>
                    {t(qyWithdrawActionKey(event.action), event.action)}
                  </span>
                  {event.actor_name !== '' && <span>{event.actor_name}</span>}
                  {event.reason !== '' && <span>{event.reason}</span>}
                </li>
              ))}
            </ul>
          )}

          <Separator />

          {/* ── 决策区 ── */}
          {action == null ? (
            <div className='flex flex-wrap gap-2'>
              {withdrawal.status === 'pending' && (
                <>
                  <Button
                    disabled={busy}
                    onClick={() => approveMutation.mutate(withdrawal.id)}
                  >
                    {t('qy_common_approve')}
                  </Button>
                  <Button
                    variant='destructive'
                    disabled={busy}
                    onClick={() => setAction('reject')}
                  >
                    {t('qy_common_reject')}
                  </Button>
                </>
              )}
              {withdrawal.status === 'approved' && (
                <>
                  {/* 两种方式共用这一个出口：钱都由管理员自己发，系统只登记。 */}
                  <Button
                    disabled={busy}
                    onClick={() => {
                      // 预填单据上的权威金额：让人看着数字点确认，而不是背下来。
                      // 后端仍然逐值校验，改动它会被挡回。
                      setPayoutAmount(owedAmount)
                      setAction('mark-paid')
                    }}
                  >
                    {t('qy_wd_a_mark_paid')}
                  </Button>
                  <Button
                    variant='destructive'
                    disabled={busy}
                    onClick={() => setAction('fail')}
                  >
                    {t('qy_wd_a_mark_failed')}
                  </Button>
                </>
              )}
            </div>
          ) : (
            <div className='space-y-3 rounded-md border p-3'>
              {action === 'mark-paid' ? (
                <>
                  <div className='space-y-1.5'>
                    <Label htmlFor={amountId}>
                      {t('qy_wd_a_payout_amount')}
                    </Label>
                    <Input
                      id={amountId}
                      value={payoutAmount}
                      autoComplete='off'
                      inputMode='decimal'
                      aria-invalid={payoutAmountInvalid}
                      onChange={(event) => setPayoutAmount(event.target.value)}
                    />
                    <p className='text-muted-foreground text-xs'>
                      {isFiat
                        ? t('qy_wd_a_payout_amount_fiat_hint', {
                            amount: owedAmount,
                            currency: withdrawal.currency,
                          })
                        : t('qy_wd_a_payout_amount_quota_hint', {
                            quota: owedAmount,
                          })}
                    </p>
                  </div>
                  <div className='space-y-1.5'>
                    <Label htmlFor={refId}>{t('qy_wd_a_payout_ref')}</Label>
                    <Input
                      id={refId}
                      value={payoutRef}
                      autoComplete='off'
                      placeholder={t('qy_wd_a_payout_ref_ph')}
                      onChange={(event) => setPayoutRef(event.target.value)}
                    />
                    <p className='text-muted-foreground text-xs'>
                      {isFiat
                        ? t('qy_wd_a_payout_ref_hint')
                        : t('qy_wd_a_payout_ref_quota_hint')}
                    </p>
                  </div>
                  <div className='space-y-1.5'>
                    <Label htmlFor={noteId}>{t('qy_wd_a_payout_note')}</Label>
                    <Textarea
                      id={noteId}
                      rows={2}
                      value={payoutNote}
                      onChange={(event) => setPayoutNote(event.target.value)}
                    />
                  </div>
                </>
              ) : (
                <div className='space-y-1.5'>
                  <Label htmlFor={reasonId}>{t('qy_wd_a_reason')}</Label>
                  <Textarea
                    id={reasonId}
                    rows={3}
                    value={reason}
                    aria-invalid={reasonInvalid}
                    placeholder={t('qy_wd_a_reason_ph')}
                    onChange={(event) => setReason(event.target.value)}
                  />
                  <p className='text-muted-foreground text-end text-xs tabular-nums'>
                    {t('qy_common_rune_counter', {
                      used: reasonLength,
                      max: MAX_REASON_RUNES,
                    })}
                  </p>
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_wd_a_reason_visible')}
                  </p>
                </div>
              )}

              <div className='flex flex-wrap gap-2'>
                {action === 'reject' && (
                  <Button
                    variant='destructive'
                    disabled={busy || reasonInvalid}
                    onClick={() =>
                      rejectMutation.mutate({
                        id: withdrawal.id,
                        reason: reason.trim(),
                      })
                    }
                  >
                    {t('qy_wd_a_confirm_reject')}
                  </Button>
                )}
                {action === 'fail' && (
                  <Button
                    variant='destructive'
                    disabled={busy || reasonInvalid}
                    onClick={() =>
                      failMutation.mutate({
                        id: withdrawal.id,
                        reason: reason.trim(),
                      })
                    }
                  >
                    {t('qy_wd_a_confirm_failed')}
                  </Button>
                )}
                {action === 'mark-paid' && (
                  <Button
                    disabled={
                      busy || payoutRef.trim() === '' || payoutAmountInvalid
                    }
                    onClick={() =>
                      markPaidMutation.mutate({
                        id: withdrawal.id,
                        payout_ref: payoutRef.trim(),
                        // 两个确认字段按方式二选一。填错那个等于没填 ——
                        // 后端按单据的 method 只看对应的那一个。
                        confirm_quota: isFiat
                          ? 0
                          : Number(payoutAmount.trim() || '0'),
                        confirm_amount: isFiat ? payoutAmount.trim() : '',
                        // 0 交给后端取当前时间：客户端时钟不可信，
                        // 后端还会拒绝未来时间。
                        paid_at: 0,
                        payout_note: payoutNote.trim(),
                      })
                    }
                  >
                    {t('qy_wd_a_confirm_paid')}
                  </Button>
                )}
                <Button
                  variant='ghost'
                  disabled={busy}
                  onClick={() => setAction(null)}
                >
                  {t('qy_common_cancel')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}

function Row(props: { label: string; value: ReactNode }) {
  return (
    <div className='flex items-start justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
      <dt className='text-muted-foreground shrink-0'>{props.label}</dt>
      <dd className='min-w-0 text-right break-all'>{props.value}</dd>
    </div>
  )
}
