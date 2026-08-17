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
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { qyUnbindAffRelation } from '../api'
import type { QyAffRelation } from '../types'

type UnbindRelationDialogProps = {
  relation: QyAffRelation | null
  onClose: () => void
}

/**
 * 解除一条 AFF 关系。
 *
 * ── 这个弹窗存在的全部理由 ──
 * "解绑"这两个字不说明**已经产生的佣金**会怎样，而那正是运营点这个按钮时最想
 * 知道的事。所以语义必须直接摆在按钮上方，并且把金额算出来：
 *
 *   已产生的佣金全部保留（含已结算进余额、已提现的部分），从此不再产生新的。
 *
 * 这不是保守选择：计佣流水是只增不改的账本，删掉会让 Σ计佣 与 Σ结算 当场对不上；
 * 已结算的部分早就变成了可提现余额，甚至可能已经提现走了。要收回已发放的佣金
 * 必须单独走「冲正」——那是一个独立的决定，必须由人显式做出并填写理由。
 */
export function UnbindRelationDialog(props: UnbindRelationDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const reasonId = useId()

  const [reason, setReason] = useState('')

  // 换一条关系必须重置：带着上一条的事由提交，审计上写的就是另一件事。
  useEffect(() => {
    setReason('')
  }, [props.relation])

  const mutation = useMutation({
    mutationFn: qyUnbindAffRelation,
    onSuccess: async (result) => {
      toast.success(
        t('qy_rel_unbind_ok', { kept: result.kept_commission_quota })
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const relation = props.relation
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES

  let subject: string | undefined
  if (relation != null) {
    const invitee = relation.invitee_resolved
      ? `${relation.invitee_username} (#${relation.invitee_id})`
      : `#${relation.invitee_id}`
    const inviter = relation.inviter_resolved
      ? `${relation.inviter_username} (#${relation.inviter_id})`
      : `#${relation.inviter_id}`
    subject = t('qy_rel_pair', { invitee, inviter })
  }

  return (
    <QyResponsiveDialog
      open={relation != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_rel_unbind_title')}
      description={subject}
    >
      {relation != null && (
        <div className='space-y-4'>
          {/* 这一段是本弹窗存在的理由：把"解绑之后那笔钱怎么办"直接说出来。 */}
          <Alert>
            <AlertDescription>{t('qy_rel_unbind_semantics')}</AlertDescription>
          </Alert>

          {/* 「解绑」的对照面。运营多数时候真正想要的是"先停一段时间看看"，
              而那是「停止计佣」——关系还在，随时能恢复。不写在这里的话，
              他只会看到两个按钮和两个名字，然后按下不可逆的那一个。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_rel_unbind_vs_block')}
          </p>

          <dl className='divide-border divide-y text-sm'>
            <div className='flex justify-between gap-3 py-1.5 first:pt-0'>
              <dt className='text-muted-foreground'>
                {t('qy_rel_kept_commission')}
              </dt>
              <dd>
                <QyAmountText quota={relation.total_commission_quota} />
              </dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5 last:pb-0'>
              <dt className='text-muted-foreground'>
                {t('qy_rel_accrual_count')}
              </dt>
              <dd className='tabular-nums'>{relation.accrual_count}</dd>
            </div>
          </dl>

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
            variant='destructive'
            disabled={!reasonValid || mutation.isPending}
            onClick={() =>
              mutation.mutate({
                invitee_id: relation.invitee_id,
                reason: reason.trim(),
              })
            }
          >
            {t('qy_rel_unbind_submit')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
