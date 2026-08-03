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
import { TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import type { QyFundOrder } from '../../../lib/types'
import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { resolveQyFundOrder } from '../api'

/**
 * uncertain 单的人工裁决。
 *
 * 这是整个资金系统唯一一处「由人决定钱算不算数」的地方，因此三条约束缺一不可：
 *   1. **理由必填**（后端也校验）—— 人工改动资金状态必须留下可复盘的依据；
 *   2. 决策只有 success / failed 两种，且必须复述金额与双方，避免点错行；
 *   3. 成功后无条件刷新，前端绝不本地推算余额。
 */
export function QyFundOrderResolveDialog(props: {
  order: QyFundOrder | null
  onClose: () => void
  onDone: () => void
}) {
  const { t } = useTranslation()
  const afterMoneyChange = useQyAfterMoneyChange()
  const fieldId = useId()
  const [decision, setDecision] = useState<'failed' | 'success'>('success')
  const [reason, setReason] = useState('')
  const order = props.order

  useEffect(() => {
    setDecision('success')
    setReason('')
  }, [order])

  const mutation = useMutation({
    mutationFn: () =>
      resolveQyFundOrder(order?.order_no ?? '', { decision, reason }),
    onSuccess: async () => {
      toast.success(t('qy_cfg_fund_resolve_done'))
      await afterMoneyChange()
      props.onDone()
      props.onClose()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={order != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_cfg_fund_resolve_title')}
      description={order?.order_no}
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
            {t('qy_cfg_fund_resolve_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('qy_cfg_fund_resolve_warn_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_cfg_fund_resolve_warn_desc')}
          </AlertDescription>
        </Alert>

        <div>
          <QyKeyValue label={t('qy_cfg_fund_kind')}>
            {order == null
              ? ''
              : t(`qy_cfg_fund_kind_${order.kind}`, {
                  defaultValue: order.kind,
                })}
          </QyKeyValue>
          <QyKeyValue label={t('qy_common_amount')}>
            <QyAmountText quota={order?.amount_quota} />
          </QyKeyValue>
          <QyKeyValue label={t('qy_common_user')}>
            {order == null
              ? ''
              : `#${order.user_id}${order.peer_user_id > 0 ? ` → #${order.peer_user_id}` : ''}`}
          </QyKeyValue>
          <QyKeyValue label={t('qy_common_created_at')}>
            {formatQyTs(order?.created_at)}
          </QyKeyValue>
          <QyKeyValue label={t('qy_cfg_fund_last_error')}>
            {order?.last_error === '' ? '-' : order?.last_error}
          </QyKeyValue>
        </div>

        <RadioGroup
          value={decision}
          onValueChange={(value) =>
            setDecision(value === 'failed' ? 'failed' : 'success')
          }
          className='flex gap-4'
        >
          <div className='flex items-center gap-2'>
            <RadioGroupItem value='success' id={`${fieldId}-success`} />
            <Label htmlFor={`${fieldId}-success`} className='cursor-pointer'>
              {t('qy_cfg_fund_decision_success')}
            </Label>
          </div>
          <div className='flex items-center gap-2'>
            <RadioGroupItem value='failed' id={`${fieldId}-failed`} />
            <Label htmlFor={`${fieldId}-failed`} className='cursor-pointer'>
              {t('qy_cfg_fund_decision_failed')}
            </Label>
          </div>
        </RadioGroup>

        <div className='space-y-1'>
          <Label htmlFor={`${fieldId}-reason`}>{t('qy_common_reason')}</Label>
          <Textarea
            id={`${fieldId}-reason`}
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t('qy_cfg_fund_reason_placeholder')}
          />
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
