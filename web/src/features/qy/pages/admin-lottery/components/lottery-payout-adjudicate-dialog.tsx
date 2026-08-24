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
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { QY_EMPTY_TEXT } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { adjudicateQyLotPayout } from '../api'
import type { QyLotAdminPayout } from '../types'

/** 后端 `maxAdjudicateReasonRunes`。多一个字会被 400，不该等提交才知道。 */
const MAX_REASON_RUNES = 200

type Verdict = 'credited' | 'not_credited'

/**
 * 出款的**人工核对落账**。超级管理员专属。
 *
 * ## 它出现在「重试」按不动的那一档
 *
 * 一笔出款同时满足三条时，此前没有任何人、任何后台任务能处置它：
 * 业务状态 `held`（出款 worker 只扫 planned/paying/failed）、本代次资金单
 * `failed`（补偿任务只扫 pending/in_doubt，复判只扫 uncertain）、而主库探针说
 * 钱**可能已经动过**（于是「重试」的换代次分支被挡死 —— 换代次就是再发一次钱）。
 * 结果不是"少了个按钮"：那笔钱永久挂在冻结中，那一场活动也因此永远删不掉。
 *
 * ## 两个结论的代价完全不同，所以文案不能对称
 *
 *   - **确实已发放**：只把账做平（资金单判成功、出款置已到账、补一行账本）。
 *     一分钱都不再动。
 *   - **确实没发放**：换代次重排，**主库会真的再加一次钱**。这一支的全部安全性
 *     只来自"人真的去主库核对过"这一个前提，所以它单独染成危险色，而不是
 *     和上面那个并排成两个长得一样的选项。
 *
 * ## 为什么要求填核对依据
 *
 * 后端强制（≤200 字）。这条记录是这笔钱事后唯一的解释：探针与资金单在这一笔上
 * 互相矛盾，账面上没有任何东西能说明"为什么最后按已发放算"。
 */
export function QyLotPayoutAdjudicateDialog(props: {
  actNo: string
  payout: QyLotAdminPayout | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const fieldId = useId()
  const [verdict, setVerdict] = useState<Verdict>('credited')
  const [reason, setReason] = useState('')
  const payout = props.payout

  // 每次换一笔单都重置：上一笔的结论与依据残留下来，会让下一次点击变成
  // "照着上一笔的理由给这一笔落账"。
  useEffect(() => {
    setVerdict('credited')
    setReason('')
  }, [payout])

  const trimmed = reason.trim()
  const tooLong = [...trimmed].length > MAX_REASON_RUNES

  const mutation = useMutation({
    mutationFn: () =>
      adjudicateQyLotPayout(props.actNo, payout?.payout_no ?? '', {
        verdict,
        reason: trimmed,
      }),
    onSuccess: async () => {
      toast.success(t('qy_lot_adjudicate_done'))
      props.onClose()
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={payout != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_adjudicate_title')}
      description={payout?.payout_no}
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            variant={verdict === 'not_credited' ? 'destructive' : 'default'}
            disabled={mutation.isPending || trimmed === '' || tooLong}
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_adjudicate_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('qy_lot_adjudicate_warn_title')}</AlertTitle>
          <AlertDescription>
            {t('qy_lot_adjudicate_warn_desc')}
          </AlertDescription>
        </Alert>

        <div>
          <QyKeyValue label={t('qy_common_user')}>
            {payout == null ? QY_EMPTY_TEXT : `#${payout.user_id}`}
          </QyKeyValue>
          <QyKeyValue label={t('qy_common_amount')}>
            <QyAmountText quota={payout?.amount_quota} />
          </QyKeyValue>
          <QyKeyValue label={t('qy_common_order_no')}>
            {payout == null || payout.order_no === '' ? (
              QY_EMPTY_TEXT
            ) : (
              <span className='font-mono text-xs'>{payout.order_no}</span>
            )}
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_last_error')}>
            {payout == null || payout.last_error === ''
              ? QY_EMPTY_TEXT
              : payout.last_error}
          </QyKeyValue>
        </div>

        {/* 两个结论竖排而不是并排：并排会让它们看起来是一对等价选项，
            而下面那个会让主库再加一次钱。每一条都自带一句后果说明。 */}
        <RadioGroup
          value={verdict}
          onValueChange={(value) =>
            setVerdict(value === 'not_credited' ? 'not_credited' : 'credited')
          }
          className='flex flex-col gap-3'
        >
          <div className='flex items-start gap-2'>
            <RadioGroupItem value='credited' id={`${fieldId}-credited`} />
            <div className='space-y-0.5'>
              <Label htmlFor={`${fieldId}-credited`} className='cursor-pointer'>
                {t('qy_lot_adjudicate_credited')}
              </Label>
              <p className='text-muted-foreground text-xs leading-5'>
                {t('qy_lot_adjudicate_credited_desc')}
              </p>
            </div>
          </div>
          <div className='flex items-start gap-2'>
            <RadioGroupItem
              value='not_credited'
              id={`${fieldId}-not-credited`}
            />
            <div className='space-y-0.5'>
              <Label
                htmlFor={`${fieldId}-not-credited`}
                className='cursor-pointer'
              >
                {t('qy_lot_adjudicate_not_credited')}
              </Label>
              <p className='text-destructive text-xs leading-5'>
                {t('qy_lot_adjudicate_not_credited_desc')}
              </p>
            </div>
          </div>
        </RadioGroup>

        <div className='space-y-1'>
          <Label htmlFor={`${fieldId}-reason`}>
            {t('qy_lot_adjudicate_reason')}
          </Label>
          <Textarea
            id={`${fieldId}-reason`}
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t('qy_lot_adjudicate_reason_placeholder')}
          />
          {tooLong && (
            <p className='text-destructive text-xs'>
              {t('qy_lot_adjudicate_reason_too_long', {
                max: MAX_REASON_RUNES,
              })}
            </p>
          )}
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
