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
import { hideQyLotActivity } from '../api'
import type { QyLotAdminActivity } from '../types'

/**
 * 下架（「关闭」）。
 *
 * ## 这个弹窗最重要的一句话是「这不是取消」
 *
 * 同一屏上还有一个红色的「取消活动」按钮，而取消会**把参与费全额退回去**。
 * 运营把两者搞混的代价是真的花钱：以为在藏一场已经结清的活动，结果退掉了
 * 一整场。所以说明的第一行写的不是"确定下架吗"，而是这两个动作各自会做什么。
 *
 * ## 下架不遮什么
 *
 * 活动详情、我的参与、匿名证据链一律照常可达。这一条同样必须写出来 ——
 * 运营会默认"下架 = 用户再也看不到"，然后据此向用户承诺一件系统做不到的事。
 */
export function QyLotHideDialog(props: {
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
      hideQyLotActivity(props.activity.act_no, { reason: reason.trim() }),
    onSuccess: async () => {
      toast.success(t('qy_lot_hide_done'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_hide_title')}
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
            disabled={reason.trim() === '' || mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_hide_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <Alert>
          <AlertTitle>{t('qy_lot_hide_warn_title')}</AlertTitle>
          <AlertDescription>{t('qy_lot_hide_warn_desc')}</AlertDescription>
        </Alert>
        <div className='space-y-1'>
          <Label htmlFor={reasonId}>{t('qy_lot_hide_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={3}
            maxLength={200}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_hide_reason_hint')}
          </p>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
