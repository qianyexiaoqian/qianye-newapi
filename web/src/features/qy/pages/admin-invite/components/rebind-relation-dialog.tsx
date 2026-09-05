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
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyErrorMessage } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { qyRebindInviteRelation } from '../api'
import { qyRelationSubject } from '../lib/relation-subject'
import type { QyInviteRelation } from '../types'

type RebindRelationDialogProps = {
  relation: QyInviteRelation | null
  onClose: () => void
}

/**
 * 换绑：把这个下线的上线换成另一个人。
 *
 * 它此前住在已删除的「用户佣金」页里（`manage-relation-dialog` 的
 * `replace_inviter` 一档）。接口是后端的**一个端点**（`relations/rebind`），
 * 不是前端"先解绑再绑"两次请求 —— 后者第二次失败时这个人会停在没有上线的
 * 中间态，而运营看到的只是一句"操作失败"。
 *
 * 语义与解绑一致：已发放的星屑留在旧上线名下、一笔不收回，此后的返给新上线。
 * 前端只挡两种当场可判的情况（自邀请、换成他现在这个上线）；任意长度的邀请
 * 环路由后端在事务里判（`qy_rel_cycle`）—— 前端拿不到全图，也不该拿。
 */
export function RebindRelationDialog(props: RebindRelationDialogProps) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const targetId = useId()
  const reasonId = useId()

  const [target, setTarget] = useState('')
  const [reason, setReason] = useState('')

  // 换一条关系必须重置：带着上一条的目标 id 提交，改的是另一个人的上线。
  useEffect(() => {
    setTarget('')
    setReason('')
  }, [props.relation])

  const mutation = useMutation({
    mutationFn: qyRebindInviteRelation,
    onSuccess: async (result) => {
      toast.success(
        t('qy_inv_a_rebind_ok', {
          kept: formatSdWithUnit(result.kept_stardust, unit),
        })
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const relation = props.relation
  const targetValue = Number(target)
  const targetValid =
    target.trim() !== '' && Number.isInteger(targetValue) && targetValue > 0
  const selfInvite = targetValid && targetValue === (relation?.invitee_id ?? 0)
  // 换成他现在这个上线：后端回 400（`qy_rel_same_inviter`）而不是当空操作，
  // 但让运营点完提交再看到红字是白跑一趟。
  const sameInviter = targetValid && targetValue === (relation?.inviter_id ?? 0)
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES

  return (
    <QyResponsiveDialog
      open={relation != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_inv_a_rebind_title')}
      description={
        relation == null ? undefined : qyRelationSubject(relation, t)
      }
    >
      {relation != null && (
        <div className='space-y-4'>
          <Alert>
            <AlertDescription>
              {t('qy_inv_a_rebind_semantics')}
            </AlertDescription>
          </Alert>

          <div className='space-y-1.5'>
            <Label htmlFor={targetId}>{t('qy_inv_a_rebind_target')}</Label>
            <Input
              id={targetId}
              inputMode='numeric'
              value={target}
              aria-invalid={
                target !== '' && (!targetValid || selfInvite || sameInviter)
              }
              onChange={(event) => setTarget(event.target.value)}
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
          {sameInviter && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('qy_err_rel_same_inviter')}
              </AlertDescription>
            </Alert>
          )}

          <div className='space-y-1.5'>
            <Label htmlFor={reasonId}>{t('qy_rel_reason')}</Label>
            <Textarea
              id={reasonId}
              rows={3}
              value={reason}
              aria-invalid={!reasonValid}
              placeholder={t('qy_rel_unbind_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_rel_reason_hint')}
            </p>
          </div>

          <Button
            disabled={
              !targetValid ||
              selfInvite ||
              sameInviter ||
              !reasonValid ||
              mutation.isPending
            }
            onClick={() =>
              mutation.mutate({
                invitee_id: relation.invitee_id,
                inviter_id: targetValue,
                reason: reason.trim(),
              })
            }
          >
            {t('qy_inv_a_rebind_submit')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
