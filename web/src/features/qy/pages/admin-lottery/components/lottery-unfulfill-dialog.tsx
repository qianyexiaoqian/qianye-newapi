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
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { unfulfillQyLotPrize } from '../api'
import type { QyLotAdminTextPrize } from '../types'

/** 后端要求事由至少 4 个字符（与提现的揭示事由同一条口径）。 */
const MIN_REASON_RUNES = 4

/**
 * 撤销「已履行」标记。
 *
 * ## 它撤不回什么
 *
 * **不清空已存的兑换码**。用户可能已经看到并用掉了那个码，抹掉记录等于抹掉
 * 争议时唯一的证据。所以这个动作纠正的只是账面结论，不是既成事实 ——
 * 这一句原样写在弹窗上，而不是藏在文档里。
 *
 * ## 事件流是 append-only
 *
 * 履行与撤销各写一条活动事件 + 一条全局审计。误标的痕迹永远在，消失的只是
 * 那个错误的**当前态**。
 */
export function QyLotUnfulfillDialog(props: {
  onClose: () => void
  onDone: () => void
  target: QyLotAdminTextPrize | null
}) {
  const { t } = useTranslation()
  const reasonId = useId()
  const [reason, setReason] = useState('')

  useEffect(() => {
    setReason('')
  }, [props.target?.payout_no])

  const mutation = useMutation({
    mutationFn: unfulfillQyLotPrize,
    onSuccess: () => props.onDone(),
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const tooShort = [...reason.trim()].length < MIN_REASON_RUNES

  return (
    <QyResponsiveDialog
      open={props.target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_unfulfill')}
      footer={
        <Button
          variant='destructive'
          disabled={tooShort || mutation.isPending}
          onClick={() => {
            if (props.target == null) return
            mutation.mutate({
              payout_no: props.target.payout_no,
              reason: reason.trim(),
            })
          }}
        >
          {t('qy_lot_unfulfill')}
        </Button>
      }
    >
      <div className='space-y-4'>
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('qy_lot_unfulfill')}</AlertTitle>
          <AlertDescription>{t('qy_lot_unfulfill_warn')}</AlertDescription>
        </Alert>

        {props.target != null && (
          <QyKeyValue label={t('qy_lot_payout_no')}>
            <span className='font-mono text-xs'>{props.target.payout_no}</span>
          </QyKeyValue>
        )}

        <div className='space-y-2'>
          <Label htmlFor={reasonId}>{t('qy_lot_unfulfill_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={3}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
