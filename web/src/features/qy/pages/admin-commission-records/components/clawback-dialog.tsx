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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qyClawbackAccrual } from '../../admin-commission/api'
import type { QyAdminAccrual } from '../../admin-commission/types'
import { useQyRequestId } from '../../lib/request-id'

type ClawbackDialogProps = {
  accrual: QyAdminAccrual | null
  onClose: () => void
}

/**
 * 人工冲正。
 *
 * 冲正会写一条负额计佣行并直接扣减邀请人的佣金余额，因此：
 *   - **理由必填**（后端 `qy_reason_required`）：没有理由的扣款事后无法复盘；
 *   - 带 `client_request_id`，重试不会扣两遍；
 *   - 扣减额度**不能超过原始计佣额**，超出部分会变成欠账并冻结对方提现，
 *     所以输入框上界直接钉在 `base_quota` 对应的佣金上。
 */
export function ClawbackDialog(props: ClawbackDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const requestId = useQyRequestId()
  const quotaId = useId()
  const reasonId = useId()

  const [quota, setQuota] = useState('')
  const [reason, setReason] = useState('')

  // 换一行必须重置，否则会把上一条的金额与理由带到这一条上。
  useEffect(() => {
    setQuota('')
    setReason('')
    requestId.renew()
  }, [props.accrual, requestId])

  const clawback = useMutation({
    mutationFn: qyClawbackAccrual,
    onSuccess: async () => {
      toast.success(t('qy_cm_clawback_ok'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const quotaValue = Number(quota)
  const invalid =
    !Number.isInteger(quotaValue) || quotaValue <= 0 || reason.trim() === ''

  return (
    <QyResponsiveDialog
      open={props.accrual != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_cm_clawback_title')}
      description={props.accrual?.accrual_no}
    >
      {props.accrual != null && (
        <div className='space-y-4'>
          <dl className='divide-border divide-y text-sm'>
            <div className='flex justify-between gap-3 py-1.5 first:pt-0'>
              <dt className='text-muted-foreground'>{t('qy_cm_inviter')}</dt>
              <dd>#{props.accrual.inviter_id}</dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5'>
              <dt className='text-muted-foreground'>
                {t('qy_aff_base_quota')}
              </dt>
              <dd>
                <QyAmountText quota={props.accrual.base_quota} />
              </dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5 last:pb-0'>
              <dt className='text-muted-foreground'>{t('qy_aff_gross')}</dt>
              <dd className='tabular-nums'>{props.accrual.gross_amount}</dd>
            </div>
          </dl>

          <div className='space-y-1.5'>
            <Label htmlFor={quotaId}>{t('qy_cm_clawback_quota')}</Label>
            <Input
              id={quotaId}
              inputMode='numeric'
              value={quota}
              aria-invalid={quota !== '' && !Number.isInteger(quotaValue)}
              onChange={(event) => setQuota(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_cm_clawback_quota_hint')}
            </p>
          </div>

          <div className='space-y-1.5'>
            <Label htmlFor={reasonId}>{t('qy_common_reason')}</Label>
            <Textarea
              id={reasonId}
              rows={3}
              value={reason}
              aria-invalid={reason.trim() === ''}
              placeholder={t('qy_cm_clawback_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
          </div>

          <Button
            variant='destructive'
            disabled={invalid || clawback.isPending}
            onClick={() =>
              clawback.mutate({
                accrual_id: props.accrual?.id ?? 0,
                quota: quotaValue,
                reason: reason.trim(),
                client_request_id: requestId.peek(),
              })
            }
          >
            {t('qy_cm_clawback_submit')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
