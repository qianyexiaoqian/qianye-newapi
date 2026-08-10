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

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { qyBindAffRelation } from '../api'

type BindRelationDialogProps = {
  open: boolean
  onClose: () => void
}

/**
 * 手工绑定 AFF 关系。
 *
 * ── 这个弹窗必须让运营看懂的两件事 ──
 *  1. 它改的是**权威字段**：从提交那一刻起，被邀请人此后所有的消费与充值都会给
 *     新的邀请人分成。这不是一个显示设置。
 *  2. 它**不补发**上游的邀请奖励（`aff_quota`）：那笔额度是注册时发的，
 *     事后补等于凭空造钱。不写出来的话运营会以为"绑了就等于重新走了一遍注册"。
 *
 * 刻意不带幂等键：后端用 `WHERE inviter_id = 0` 的 CAS 写入，重复提交第二次会
 * 直接被 `qy_rel_already_bound` 挡回来，不会把关系悄悄改指向别处。
 */
export function BindRelationDialog(props: BindRelationDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const inviteeInputId = useId()
  const inviterInputId = useId()
  const reasonId = useId()

  const [inviteeId, setInviteeId] = useState('')
  const [inviterId, setInviterId] = useState('')
  const [reason, setReason] = useState('')

  // 关掉再打开必须是干净的一张表单：带着上一次的 id 提交，改的是另一个人的钱。
  useEffect(() => {
    if (!props.open) return
    setInviteeId('')
    setInviterId('')
    setReason('')
  }, [props.open])

  const mutation = useMutation({
    mutationFn: qyBindAffRelation,
    onSuccess: async () => {
      toast.success(t('qy_rel_bind_ok'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const inviteeValue = Number(inviteeId)
  const inviterValue = Number(inviterId)
  const inviteeValid =
    inviteeId.trim() !== '' &&
    Number.isInteger(inviteeValue) &&
    inviteeValue > 0
  const inviterValid =
    inviterId.trim() !== '' &&
    Number.isInteger(inviterValue) &&
    inviterValue > 0
  // 自邀请在前端就挡掉：后端同样会拒，但让运营点完提交再看到红字是白跑一趟。
  const selfInvite =
    inviteeValid && inviterValid && inviteeValue === inviterValue
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_rel_bind_title')}
      description={t('qy_rel_bind_desc')}
    >
      <div className='space-y-4'>
        <Alert>
          <AlertDescription>{t('qy_rel_bind_warning')}</AlertDescription>
        </Alert>

        <div className='space-y-1.5'>
          <Label htmlFor={inviteeInputId}>{t('qy_rel_invitee_id')}</Label>
          <Input
            id={inviteeInputId}
            inputMode='numeric'
            value={inviteeId}
            aria-invalid={inviteeId !== '' && (!inviteeValid || selfInvite)}
            onChange={(event) => setInviteeId(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_rel_invitee_id_hint')}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={inviterInputId}>{t('qy_rel_inviter_id')}</Label>
          <Input
            id={inviterInputId}
            inputMode='numeric'
            value={inviterId}
            aria-invalid={inviterId !== '' && (!inviterValid || selfInvite)}
            onChange={(event) => setInviterId(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_rel_inviter_id_hint')}
          </p>
        </div>

        {selfInvite && (
          <Alert variant='destructive'>
            <AlertDescription>{t('qy_err_rel_self_invite')}</AlertDescription>
          </Alert>
        )}

        <div className='space-y-1.5'>
          <Label htmlFor={reasonId}>{t('qy_rel_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={3}
            value={reason}
            aria-invalid={!reasonValid}
            placeholder={t('qy_rel_bind_reason_ph')}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_rel_reason_hint')}
          </p>
        </div>

        <Button
          disabled={
            !inviteeValid ||
            !inviterValid ||
            selfInvite ||
            !reasonValid ||
            mutation.isPending
          }
          onClick={() =>
            mutation.mutate({
              invitee_id: inviteeValue,
              inviter_id: inviterValue,
              reason: reason.trim(),
            })
          }
        >
          {t('qy_rel_bind_submit')}
        </Button>
      </div>
    </QyResponsiveDialog>
  )
}
