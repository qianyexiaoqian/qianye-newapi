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

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyErrorMessage } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { qyUnbindInviteRelation } from '../api'
import { qyRelationSubject } from '../lib/relation-subject'
import type { QyInviteRelation } from '../types'

type UnbindRelationDialogProps = {
  relation: QyInviteRelation | null
  onClose: () => void
}

/**
 * 解除一条邀请关系。
 *
 * ── 这个弹窗存在的全部理由 ──
 * "解绑"这两个字不说明**已经返出去的星屑**会怎样，而那正是运营点这个按钮时
 * 最想知道的事。所以语义必须直接摆在按钮上方：
 *
 *   已发放的星屑一笔都不收回，从此不再产生新的。
 *
 * 星屑账本是只增不改的，已发放的那几笔早就变成了上线名下的可用余额，甚至可能
 * 已经花掉了；要收回只能走星屑手调，那是一个独立的决定，必须由人显式做出。
 */
export function UnbindRelationDialog(props: UnbindRelationDialogProps) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const reasonId = useId()

  const [reason, setReason] = useState('')

  // 换一条关系必须重置：带着上一条的事由提交，审计上写的就是另一件事。
  useEffect(() => {
    setReason('')
  }, [props.relation])

  const mutation = useMutation({
    mutationFn: qyUnbindInviteRelation,
    // 把"留在上线名下的星屑"念出来，是让那句「已发放的一笔不收回」在动作完成
    // 之后仍然是可核对的，而不是一句承诺。
    onSuccess: async (result) => {
      toast.success(
        t('qy_inv_a_unbind_ok', {
          kept: formatSdWithUnit(result.kept_stardust, unit),
        })
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const relation = props.relation
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES

  return (
    <QyResponsiveDialog
      open={relation != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_inv_a_unbind_title')}
      description={
        relation == null ? undefined : qyRelationSubject(relation, t)
      }
    >
      {relation != null && (
        <div className='space-y-4'>
          {/* 这一段是本弹窗存在的理由：把"解绑之后那些星屑怎么办"直接说出来。 */}
          <Alert>
            <AlertDescription>
              {t('qy_inv_a_unbind_semantics')}
            </AlertDescription>
          </Alert>

          {/* 「解绑」的对照面。运营多数时候真正想要的是"先停一段时间看看"，
              而那是「停止计返」——关系还在，随时能恢复。不写在这里的话，
              他只会看到两个按钮和两个名字，然后按下不可逆的那一个。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_inv_a_unbind_vs_block')}
          </p>

          {/* 「改了之后那些星屑怎么办」的量化形式：这一对到目前为止返了多少。 */}
          <dl className='divide-border divide-y text-sm'>
            <div className='flex justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
              <dt className='text-muted-foreground'>
                {t('qy_inv_a_col_total_stardust')}
              </dt>
              <dd>
                <QySdAmount amount={relation.total_stardust} />
              </dd>
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
