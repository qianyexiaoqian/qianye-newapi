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
import { useMutation, useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { QyTimeline } from '../../../components/qy-timeline'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { qyErrorMessage } from '../../../lib/api'
import { QyFiatText } from '../../components/qy-fiat-text'
import { qyCancelWithdrawal, qyWithdrawRecordQuery } from '../../withdraw/api'
import { QyWithdrawProofImage } from '../../withdraw/components/withdraw-proof-image'
import { qyPayeeChannelKey } from '../../withdraw/lib/payee-spec'
import { buildQyWithdrawTimeline, qyWithdrawActionKey } from '../lib/timeline'

type WithdrawalDetailDialogProps = {
  /** `null` 表示关闭。用 id 而不是整行数据：详情接口才带 `events`。 */
  withdrawalId: number | null
  onClose: () => void
}

/**
 * 提现单详情。
 *
 * 必须重新按 id 拉一次而不是复用列表行：列表接口不下发 `events`，
 * 而时间线正是这个弹窗存在的理由。
 */
export function WithdrawalDetailDialog(props: WithdrawalDetailDialogProps) {
  const { t } = useTranslation()
  const afterMoneyChange = useQyAfterMoneyChange()

  const query = useQuery({
    ...qyWithdrawRecordQuery(props.withdrawalId ?? 0),
    enabled: props.withdrawalId != null,
  })
  const withdrawal = query.data

  const cancelMutation = useMutation({
    mutationFn: qyCancelWithdrawal,
    onSuccess: async () => {
      toast.success(t('qy_wd_cancel_ok'))
      // 撤销会把佣金退回 available，余额与列表都要回服务端重取。
      await afterMoneyChange()
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <Dialog
      open={props.withdrawalId != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_wd_detail_title')}
      description={withdrawal?.withdraw_no}
      footer={
        withdrawal != null &&
        withdrawal.status === 'pending' && (
          <Button
            variant='destructive'
            disabled={cancelMutation.isPending}
            onClick={() => cancelMutation.mutate(withdrawal.id)}
          >
            {t('qy_wd_cancel')}
          </Button>
        )
      }
    >
      {query.isLoading && <LoadingState />}
      {withdrawal != null && (
        <div className='space-y-4'>
          <dl className='divide-border divide-y text-sm'>
            <DetailRow
              label={t('qy_common_status')}
              value={<QyStatusBadge status={withdrawal.status} />}
            />
            <DetailRow
              label={t('qy_wd_method')}
              value={t(`qy_wd_m_${withdrawal.method}`)}
            />
            <DetailRow
              label={t('qy_wd_amount')}
              value={<QyAmountText quota={withdrawal.quota} />}
            />
            {withdrawal.method === 'fiat' && (
              <>
                <DetailRow
                  label={t('qy_wd_gross')}
                  value={
                    <QyFiatText
                      amount={withdrawal.gross_amount}
                      currency={withdrawal.currency}
                    />
                  }
                />
                <DetailRow
                  label={t('qy_common_fee')}
                  value={
                    <QyFiatText
                      amount={withdrawal.fee_amount}
                      currency={withdrawal.currency}
                    />
                  }
                />
                <DetailRow
                  label={t('qy_wd_net')}
                  value={
                    <QyFiatText
                      amount={withdrawal.net_amount}
                      currency={withdrawal.currency}
                      className='font-semibold'
                    />
                  }
                />
                <DetailRow
                  label={t('qy_wd_payee')}
                  value={
                    withdrawal.payee_masked === ''
                      ? '-'
                      : `${t(qyPayeeChannelKey(withdrawal.payee_channel), withdrawal.payee_channel)} · ${withdrawal.payee_masked}`
                  }
                />
                {/* 汇率必须展示：它是"为什么这笔和上一笔金额不同"的唯一解释。 */}
                <DetailRow
                  label={t('qy_wd_frozen_rate')}
                  value={withdrawal.frozen_fx_rate}
                />
              </>
            )}
            {withdrawal.remark !== '' && (
              <DetailRow label={t('qy_wd_remark')} value={withdrawal.remark} />
            )}
            <DetailRow
              label={t('qy_common_created_at')}
              value={formatTimestampToDate(withdrawal.created_at)}
            />
          </dl>

          {/* has_proof 只说明"附过"，图片可能已随单据终结被清理 ——
              那种情况由 QyWithdrawProofImage 转述后端的 qy_wd_proof_purged。 */}
          {withdrawal.has_proof && (
            <div className='space-y-2'>
              <h4 className='text-sm font-medium'>{t('qy_wd_proof_title')}</h4>
              <QyWithdrawProofImage withdrawalId={withdrawal.id} />
            </div>
          )}

          <Separator />

          <div className='space-y-2'>
            <h4 className='text-sm font-medium'>{t('qy_wd_timeline_title')}</h4>
            <QyTimeline items={buildQyWithdrawTimeline(withdrawal, t)} />
          </div>

          {withdrawal.events != null && withdrawal.events.length > 0 && (
            <div className='space-y-2'>
              <h4 className='text-sm font-medium'>{t('qy_wd_events_title')}</h4>
              <ul className='text-muted-foreground space-y-1 text-xs'>
                {withdrawal.events.map((event) => (
                  // 事件流水只增不改，(时间, 动作, 目标状态) 在一张单上唯一：
                  // 同一秒内不可能对同一张单执行两次同样的跃迁。
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
                    {event.reason !== '' && <span>{event.reason}</span>}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </Dialog>
  )
}

function DetailRow(props: { label: string; value: ReactNode }) {
  return (
    <div className='flex items-start justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
      <dt className='text-muted-foreground shrink-0'>{props.label}</dt>
      <dd className='min-w-0 text-right break-all'>{props.value}</dd>
    </div>
  )
}
