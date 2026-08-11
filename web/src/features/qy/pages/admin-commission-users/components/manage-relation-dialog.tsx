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
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import {
  qyBindAffRelation,
  qyUnbindAffRelation,
} from '../../admin-commission-relations/api'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { qyRebindAffRelation } from '../api'
import {
  QY_RELATION_ACTIONS,
  type QyCommissionUser,
  type QyRelationAction,
} from '../types'

type ManageRelationDialogProps = {
  user: QyCommissionUser | null
  onClose: () => void
}

/**
 * 管理一个用户的佣金绑定关系：添加 / 更换 / 移除上线，以及给他添加下线。
 *
 * ── 这个弹窗必须说清的那一句 ──
 * 「已经产生的佣金全部保留（含已结算进余额、已提现的部分），从此不再产生
 * 新的。」四个动作**都**适用这一句，所以它固定挂在最上方，而不是只写在
 * 「移除」那一档上。运营点这些按钮时唯一真正想知道的就是"那笔钱怎么办"，
 * 而这个答案对四个动作是同一个 —— 分档写反而会让人以为它们不一样。
 *
 * 收回已发放的佣金必须单独走「冲正」：那是一个独立的决定，要由人显式做出
 * 并留下理由，不能作为改绑定的副作用发生。
 *
 * ── 三个端点，不是"前端拼出来的三个动作" ──
 * 添加走 `relations/bind`，移除走 `relations/unbind`，**换绑走
 * `relations/rebind`** —— 后者是后端在一个事务里完成的，不是前端"先解绑再绑"
 * 两次请求。两次请求的失败模式是：第二次挂掉时这个人停在"没有上线"的中间态，
 * 而运营看到的只是一句"操作失败"，他会以为什么都没变。
 *
 * ── 防环与自邀请 ──
 * 前端只挡两种当场可判的情况（自己邀请自己、目标 id 非正整数）与两种由本行
 * 数据就能判定的情况（没有上线却想换/移除、已有上线却想再添加一个）。任意
 * 长度的邀请环路由后端在事务里判（`qy_rel_cycle`）：前端拿不到全图，也不该
 * 拿 —— 拿到了就是把一份会漂移的图算法复制到浏览器里。
 */
export function ManageRelationDialog(props: ManageRelationDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const actionId = useId()
  const targetId = useId()
  const reasonId = useId()

  const user = props.user
  const hasInviter = (user?.inviter_id ?? 0) > 0

  const [action, setAction] = useState<QyRelationAction>('set_inviter')
  const [target, setTarget] = useState('')
  const [reason, setReason] = useState('')

  // 换一个人必须重置：带着上一个人的目标 id 与事由提交，改的是另一个人的
  // 邀请关系，而审计上写的是这一次的事由。
  useEffect(() => {
    setAction(hasInviter ? 'replace_inviter' : 'set_inviter')
    setTarget('')
    setReason('')
  }, [user, hasInviter])

  const mutation = useMutation({
    // 三个端点的返回体不同，只有「留在旧上线名下的钱」是共有的关心点，
    // 所以统一收敛成这一个可选字段 —— 调用点不需要知道自己刚才打的是哪条路由。
    mutationFn: async (input: {
      action: QyRelationAction
      user_id: number
      target_id: number
      reason: string
    }): Promise<{ kept_commission_quota?: number }> => {
      const { action: kind, user_id, target_id, reason: why } = input
      if (kind === 'remove_inviter') {
        const out = await qyUnbindAffRelation({
          invitee_id: user_id,
          reason: why,
        })
        return { kept_commission_quota: out.kept_commission_quota }
      }
      if (kind === 'replace_inviter') {
        const out = await qyRebindAffRelation({
          invitee_id: user_id,
          inviter_id: target_id,
          reason: why,
        })
        return { kept_commission_quota: out.kept_commission_quota }
      }
      // 剩下两档都是 bind，只是方向不同：「添加下线」时这个人是**邀请人**。
      // 绑定不涉及任何既有佣金，所以没有 kept 可回显。
      await qyBindAffRelation(
        kind === 'add_invitee'
          ? { invitee_id: target_id, inviter_id: user_id, reason: why }
          : { invitee_id: user_id, inviter_id: target_id, reason: why }
      )
      return {}
    },
    onSuccess: async (result) => {
      // 换绑与解绑都会回显"留在旧上线名下的钱"。把这个数字念出来，是让那句
      // 「历史佣金全部保留」在动作完成之后仍然是可核对的，而不是一句承诺。
      const kept = result.kept_commission_quota
      toast.success(
        kept == null
          ? t(`qy_cu_rel_ok_${action}`)
          : `${t(`qy_cu_rel_ok_${action}`)} · ${t('qy_rel_kept_commission')}: ${kept}`
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const targetValue = Number(target)
  const targetValid =
    target.trim() !== '' && Number.isInteger(targetValue) && targetValue > 0
  const needsTarget = action !== 'remove_inviter'
  const selfInvite = targetValid && targetValue === (user?.user_id ?? 0)
  // 换成他现在这个上线：后端回 400（`qy_rel_same_inviter`）而不是当空操作，
  // 但让运营点完提交再看到红字是白跑一趟。
  const sameInviter =
    action === 'replace_inviter' &&
    targetValid &&
    targetValue === (user?.inviter_id ?? 0)
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES

  // 可用动作完全由「这一行有没有上线」决定，不给一个点了必然报错的选项：
  // 没有上线时「更换/移除」在后端一定是 `qy_rel_not_bound`，
  // 已有上线时「添加上线」一定是 `qy_rel_already_bound`。
  const available = QY_RELATION_ACTIONS.filter((value) => {
    if (value === 'set_inviter') return !hasInviter
    if (value === 'replace_inviter' || value === 'remove_inviter') {
      return hasInviter
    }
    return true
  })

  let subject: string | undefined
  if (user != null) {
    subject = user.user_resolved
      ? `${user.username} (#${user.user_id})`
      : `#${user.user_id}`
  }

  return (
    <QyResponsiveDialog
      open={user != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_cu_manage_relation')}
      description={subject}
    >
      {user != null && (
        <div className='space-y-4'>
          {/* 四个动作共用的那一句：历史保留、不再产生新的。 */}
          <Alert>
            <AlertDescription>{t('qy_cu_rel_semantics')}</AlertDescription>
          </Alert>

          <dl className='divide-border divide-y text-sm'>
            <div className='flex justify-between gap-3 py-1.5 first:pt-0'>
              <dt className='text-muted-foreground'>
                {t('qy_cu_col_inviter')}
              </dt>
              <dd>
                {hasInviter
                  ? `${user.inviter_resolved ? user.inviter_username : t('qy_rel_user_gone')} (#${user.inviter_id})`
                  : t('qy_cu_no_inviter')}
              </dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5'>
              <dt className='text-muted-foreground'>
                {t('qy_cu_col_invitees')}
              </dt>
              <dd className='tabular-nums'>{user.invitee_count}</dd>
            </div>
            {/*
              「改了之后那笔钱怎么办」的量化形式。两处刻意的选择：

              一、念的是 `inviter_commission_quota`（当前上线**从这个人身上**
              挣到的），不是 `total_earned_quota`（这个人从**他自己下线**身上
              挣到的）。两者是反方向的数：397 号自己没有下线所以后者是 0，而
              他上线 391 从他身上已挣到 13517。渲染错那一个的后果是确认框写
              「保留 0」、点完的成功提示写「保留 13517」—— 在一个改钱的页面上
              当着运营的面自相矛盾。后端与 `kept_commission_quota` 同一份实现。

              二、只在**动到既有关系**的两档显示。「指定上线」与「添加下线」
              建立的是一条全新关系，没有任何既有佣金可言，此时挂一个金额出来
              只会让人以为这次操作会动到那笔钱。
            */}
            {(action === 'replace_inviter' || action === 'remove_inviter') && (
              <div className='flex justify-between gap-3 py-1.5 last:pb-0'>
                <dt className='text-muted-foreground'>
                  {t('qy_cu_rel_kept_from_inviter')}
                </dt>
                <dd>
                  <QyAmountText quota={user.inviter_commission_quota} />
                </dd>
              </div>
            )}
          </dl>

          <div className='space-y-1.5'>
            <Label htmlFor={actionId}>{t('qy_cu_rel_action')}</Label>
            <NativeSelect
              id={actionId}
              size='sm'
              value={action}
              onChange={(event) =>
                setAction(event.target.value as QyRelationAction)
              }
            >
              {available.map((value) => (
                <NativeSelectOption key={value} value={value}>
                  {t(`qy_cu_rel_act_${value}`)}
                </NativeSelectOption>
              ))}
            </NativeSelect>
            <p className='text-muted-foreground text-xs'>
              {t(`qy_cu_rel_desc_${action}`)}
            </p>
          </div>

          {needsTarget && (
            <div className='space-y-1.5'>
              <Label htmlFor={targetId}>
                {action === 'add_invitee'
                  ? t('qy_rel_invitee_id')
                  : t('qy_rel_inviter_id')}
              </Label>
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
                {t('qy_cu_rel_target_hint')}
              </p>
            </div>
          )}

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
              placeholder={t('qy_cu_rel_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_rel_reason_hint')}
            </p>
          </div>

          <Button
            variant={action === 'remove_inviter' ? 'destructive' : 'default'}
            disabled={
              (needsTarget && (!targetValid || selfInvite || sameInviter)) ||
              !reasonValid ||
              mutation.isPending
            }
            onClick={() =>
              mutation.mutate({
                action,
                user_id: user.user_id,
                target_id: targetValue,
                reason: reason.trim(),
              })
            }
          >
            {t(`qy_cu_rel_act_${action}`)}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
