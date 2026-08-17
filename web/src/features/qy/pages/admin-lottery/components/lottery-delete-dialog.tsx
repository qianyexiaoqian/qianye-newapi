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
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { deleteQyLotActivity } from '../api'
import type { QyLotAdminActivity } from '../types'

/**
 * 彻底删除一场活动。**不可逆。**
 *
 * ## 二次确认为什么不是「确定吗」
 *
 * 这个动作没有撤销、没有回收站、没有软删。它抹掉的不只是一行记录，而是
 * **这一场的公正性从此无法被任何人验证**：证据链端点会 404，仓库自带的
 * `lottery-verify.py` 再也拉不到数据，用户手里那份报名回执上的 `chain_hash`
 * 从此对不上任何东西。所以确认框里写的是这三件事各自的后果，并要求把活动编号
 * 原样敲一遍 —— 敲编号的那几秒，正是让人真的读完上面那段话的唯一办法。
 *
 * 服务端会再校验一次 `confirm_act_no`：只在前端拦，挡不住脚本化的误调用。
 *
 * ## 六道硬闸门在后端
 *
 * 未结束 / 出款未落定 / 文本奖未履行 / 参与未结算 / 对账异常未处理 /
 * 双色球系列结转未收口 —— 任何一条不满足都会回 409 并带一个精确的 `code`，
 * 前端原样展示。这里刻意**不**提前把按钮置灰：置灰要在前端复制一遍那六条判据，
 * 而复制出来的那一份必然与后端漂移，届时运营看到的是一个没有解释的灰按钮。
 */
export function QyLotDeleteDialog(props: {
  activity: QyLotAdminActivity
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const confirmId = useId()
  const reasonId = useId()
  const [confirmActNo, setConfirmActNo] = useState('')
  const [reason, setReason] = useState('')

  useEffect(() => {
    if (props.open) {
      setConfirmActNo('')
      setReason('')
    }
  }, [props.open])

  const mutation = useMutation({
    mutationFn: () =>
      deleteQyLotActivity(props.activity.act_no, {
        confirm_act_no: confirmActNo.trim(),
        reason: reason.trim(),
      }),
    onSuccess: async () => {
      toast.success(t('qy_lot_delete_done'))
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      // 详情页指向的那一行已经不存在了，留在原地只会得到一个 404。
      await navigate({ to: '/qy/admin/lottery' })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const matched = confirmActNo.trim() === props.activity.act_no

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_lot_delete_title')}
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
            disabled={!matched || reason.trim() === '' || mutation.isPending}
            onClick={() => mutation.mutate()}
          >
            {t('qy_lot_delete_confirm')}
          </Button>
        </>
      }
    >
      <div className='space-y-3'>
        <Alert variant='destructive'>
          <AlertTitle>{t('qy_lot_delete_warn_title')}</AlertTitle>
          <AlertDescription>
            <ul className='list-disc space-y-1 pl-4'>
              <li>{t('qy_lot_delete_warn_proof')}</li>
              <li>{t('qy_lot_delete_warn_money')}</li>
              <li>{t('qy_lot_delete_warn_rows')}</li>
              <li>{t('qy_lot_delete_warn_audit')}</li>
            </ul>
          </AlertDescription>
        </Alert>
        <div className='space-y-1'>
          <Label htmlFor={confirmId}>
            {t('qy_lot_delete_confirm_label', { actNo: props.activity.act_no })}
          </Label>
          <Input
            id={confirmId}
            autoComplete='off'
            spellCheck={false}
            className='font-mono'
            value={confirmActNo}
            onChange={(event) => setConfirmActNo(event.target.value)}
          />
        </div>
        <div className='space-y-1'>
          <Label htmlFor={reasonId}>{t('qy_lot_delete_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={3}
            maxLength={200}
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_delete_reason_hint')}
          </p>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
