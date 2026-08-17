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
import { qyKeys } from '../../../lib/query-keys'
import { qyBlockInviteRelation, qyBlockRelationErrorMessage } from '../api'

/**
 * 一次「停止计佣 / 恢复计佣」的目标。
 *
 * `blocked` 是**当前**状态，不是要切成的状态 —— 弹窗据此决定自己是哪一面。
 * 调用方从列表行上原样取值即可，不必自己算方向。
 */
export type QyBlockRelationTarget = {
  inviteeId: number
  blocked: boolean
}

type BlockRelationDialogProps = {
  target: QyBlockRelationTarget | null
  onClose: () => void
}

/**
 * 停止 / 恢复一条邀请关系的计佣。
 *
 * ── 这个弹窗存在的全部理由 ──
 * 项目方看着这个按钮问过一句话：「佣金审核这里是不是有点多余？停止计佣去把这个人
 * 的 aff 关系解绑不就好了？而且停止计佣就没有办法恢复计算了。」
 *
 * 后半句在代码上从来不成立（后端 `adminBlockRelation` 收 `blocked bool`，同一个
 * 接口既停又恢复），前半句则是界面欠的账：**这两个动作的区别从没有被写在任何
 * 地方**，运营只能从按钮名字上猜，猜出来的结论就是"停了就废了，不如直接解绑"。
 *
 * 所以这里把两件事同时补上：
 *   1. 方向由 `target.blocked` 决定，停与恢复共用同一个入口，恢复不再需要去
 *      另一个页面找；
 *   2. 「停止计佣」与「解绑」的区别直接印在确认框里 ——
 *      · 停止计佣：关系还在（可追溯、可复盘），只是不再产生新佣金，随时能恢复；
 *      · 解绑：关系没了，历史佣金仍然保留，但从此查不到谁邀请了谁，再绑是一条
 *        新关系，统计口径断在那里。
 *
 * 「停止期间的消费恢复后不补算」同样写进文案：那是后端实际的行为
 * （`accrueConsume` 命中 blocked 直接 return，不留任何行，恢复只对之后的消费
 * 生效），也是这个开关唯一说得通的风控语义 —— 补算等于解封那一刻把挡掉的钱
 * 原样发出去。
 *
 * 事由可以留空：恢复计佣常常就是"复核完了，没问题"，强制填字只会逼出一串
 * 无意义的占位符。留空时落默认事由，审计里仍然有一条能读的话。
 */
export function BlockRelationDialog(props: BlockRelationDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const reasonId = useId()

  const [reason, setReason] = useState('')

  // 换一条关系必须重置：带着上一条的事由提交，审计上写的就是另一件事。
  useEffect(() => {
    setReason('')
  }, [props.target])

  const mutation = useMutation({
    mutationFn: qyBlockInviteRelation,
    onSuccess: async (result) => {
      toast.success(
        result.blocked ? t('qy_cm_block_ok') : t('qy_cm_unblock_ok')
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    // 后端给的是逐条独立的 code（`qy_rel_no_relation` / `qy_rel_user_not_found`
    // / `qy_rel_not_bound`），各出**一句**准确的话。
    onError: (error) => toast.error(qyBlockRelationErrorMessage(error, t)),
  })

  const target = props.target
  // 当前已停 → 这一次是恢复；当前正常 → 这一次是停止。
  const willBlock = target != null && !target.blocked

  return (
    <QyResponsiveDialog
      open={target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={willBlock ? t('qy_cm_block_title') : t('qy_cm_unblock_title')}
      description={
        target == null
          ? undefined
          : t('qy_cm_block_target', { invitee: target.inviteeId })
      }
    >
      {target != null && (
        <div className='space-y-4'>
          {/* 这一段是本弹窗存在的理由：这个动作到底会对已有的钱和关系做什么。 */}
          <Alert>
            <AlertDescription>
              {willBlock
                ? t('qy_cm_block_semantics')
                : t('qy_cm_unblock_semantics')}
            </AlertDescription>
          </Alert>

          {/* 与「解绑」的区别。运营认为本功能多余，正是因为从没有人告诉他这个。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_cm_block_vs_unbind')}
          </p>

          <div className='space-y-1.5'>
            <Label htmlFor={reasonId}>{t('qy_cm_block_reason')}</Label>
            <Textarea
              id={reasonId}
              rows={3}
              value={reason}
              placeholder={t('qy_cm_block_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_cm_block_reason_hint')}
            </p>
          </div>

          <Button
            variant={willBlock ? 'destructive' : 'default'}
            disabled={mutation.isPending}
            onClick={() =>
              mutation.mutate({
                invitee_id: target.inviteeId,
                blocked: willBlock,
                // 留空时落一条方向正确的默认事由。共用同一句的话，
                // 恢复计佣的审计正文会写着"手动停止计佣"。
                reason:
                  reason.trim() === ''
                    ? t(
                        willBlock
                          ? 'qy_cm_block_default_reason'
                          : 'qy_cm_unblock_default_reason'
                      )
                    : reason.trim(),
              })
            }
          >
            {willBlock ? t('qy_cm_block_submit') : t('qy_cm_unblock_submit')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
