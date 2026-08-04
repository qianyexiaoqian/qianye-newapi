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

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { cancelQyLotActivity } from '../api'
import type { QyLotAdminActivity } from '../types'

/**
 * 整场取消。
 *
 * 这是管理员在开奖这件事上**唯一**能做的动作，而且它的代价是确定的：全额退款、
 * 平台零收益、理由对用户公开。之所以做成"只能不开、不能挑一个开"，是因为一旦
 * 允许有选择地开奖，「选时攻击」就回来了 —— 管理员可以只在结果对自己有利时
 * 才让活动继续。
 *
 * 理由是必填的，且会写进事件流与审计：一次没有理由的取消，在事后无法与
 * "结果不合心意所以不开了"区分开。
 */
export function QyLotCancelDialog(props: {
  activity: QyLotAdminActivity
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const reasonId = useId()
  const [reason, setReason] = useState('')

  useEffect(() => {
    if (props.open) setReason('')
  }, [props.open])

  const mutation = useMutation({
    mutationFn: () =>
      cancelQyLotActivity(props.activity.act_no, { reason: reason.trim() }),
    onSuccess: async () => {
      toast.success(t('qy_lot_cancelled'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_cancel_title')}
      description={props.activity.title}
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            variant='destructive'
            disabled={reason.trim() === '' || mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_cancel_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <Alert variant='destructive'>
          <AlertTitle>{t('qy_lot_cancel_warn_title')}</AlertTitle>
          <AlertDescription>{t('qy_lot_cancel_warn_desc')}</AlertDescription>
        </Alert>
        <div className='space-y-1'>
          <Label htmlFor={reasonId}>{t('qy_common_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={3}
            maxLength={255}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_cancel_reason_hint')}
          </p>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
