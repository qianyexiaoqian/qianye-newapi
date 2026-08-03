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
import { useMutation } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { qyOpsErrorMessage } from '../../ops/errors'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { revokeQyViolationRecord } from '../api'
import type { QyViolationRecord } from '../types'

/** 只有真的扣到钱的记录才谈得上退还。 */
function isRefundable(record: QyViolationRecord | null): boolean {
  if (record == null || record.fee_quota <= 0) return false
  return record.fee_status === 'charged' || record.fee_status === 'truncated'
}

/**
 * 撤销违规记录（可选退款）。
 *
 * 撤销同时会回退计数：一条误判记录被撤销后，用户不该继续背着这次计数
 * 走向封号。退款走跨库两阶段，以 `rec_no` 为幂等键，连点两次只退一次。
 */
export function QyViolationRevokeDialog(props: {
  record: QyViolationRecord | null
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useTranslation()
  const afterMoneyChange = useQyAfterMoneyChange()
  const refundId = useId()
  const [reason, setReason] = useState('')
  const [refund, setRefund] = useState(false)
  const record = props.record

  useEffect(() => {
    setReason('')
    setRefund(isRefundable(record))
  }, [record])

  const mutation = useMutation({
    mutationFn: () =>
      revokeQyViolationRecord(record?.id ?? 0, { reason, refund }),
    onSuccess: async (data) => {
      toast.success(
        data.refunded_quota > 0
          ? t('qy_vio_revoke_done_refund', { quota: data.refunded_quota })
          : t('qy_vio_revoke_done')
      )
      // 退款动的是主库 users.quota，前端缓存与顶栏余额都必须重取。
      await afterMoneyChange()
      props.onDone()
      props.onClose()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={record != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_vio_revoke_title')}
      description={t('qy_vio_revoke_desc')}
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={mutation.isPending || reason.trim() === ''}
            onClick={() => mutation.mutate()}
          >
            {t('qy_vio_revoke_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <div>
          <QyKeyValue label={t('qy_vio_col_rec_no')}>
            {record?.rec_no}
          </QyKeyValue>
          <QyKeyValue label={t('qy_common_user')}>
            {`${record?.username ?? ''} (#${record?.user_id ?? 0})`}
          </QyKeyValue>
          <QyKeyValue label={t('qy_vio_col_rule')}>
            {record?.rule_name}
          </QyKeyValue>
          <QyKeyValue label={t('qy_vio_col_fee')}>
            <QyAmountText quota={record?.fee_quota} />
          </QyKeyValue>
        </div>

        <div className='space-y-1'>
          <Label htmlFor={`${refundId}-reason`}>{t('qy_common_reason')}</Label>
          <Textarea
            id={`${refundId}-reason`}
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t('qy_vio_revoke_reason_placeholder')}
          />
        </div>

        <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
          <div className='min-w-0'>
            <Label htmlFor={refundId}>{t('qy_vio_revoke_refund')}</Label>
            <p className='text-muted-foreground text-xs'>
              {isRefundable(record)
                ? t('qy_vio_revoke_refund_desc')
                : t('qy_vio_revoke_refund_na')}
            </p>
          </div>
          <Switch
            id={refundId}
            checked={refund}
            disabled={!isRefundable(record)}
            onCheckedChange={setRefund}
          />
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
