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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { formatTimestampToDate } from '@/lib/format'

import { qyErrorMessage } from '../lib/api'
import {
  qyAdminPayPasswordStatusQuery,
  qyAdminResetPayPassword,
  qyAdminUnlockPayPassword,
} from '../lib/pay-password'
import { qyKeys } from '../lib/query-keys'
import { QyResponsiveDialog } from './qy-responsive-dialog'

/**
 * 事由下限。后端只要求非空（`errReasonRequired`），这里再收紧一点是刻意的：
 * 一个字符的事由与没有事由在事后仲裁时等价，而这条审计正是"为什么重置"的
 * 唯一答案。上限 200 字与后端 `reasonMaxRunes` 一致。
 */
const REASON_MIN_RUNES = 4
const REASON_MAX_RUNES = 200

type QyResetPayPasswordDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
  username: string
}

/**
 * 管理员强制重置某个用户的支付密码。
 *
 * ── 它做什么、不做什么 ──
 *
 * 做的是**清空**：后端 `clearPasswordByAdmin` 把 hash 置空并保留行，同时清掉
 * 错误计数与锁定。用户下一次划转 / 提现 / 抽奖会被 `qy_pay_pwd_not_set` 拦下
 * 并引导去重新设置。
 *
 * **不做**"代设一个新密码"。那条路径压根不存在于后端，也不该被前端造出来：
 * 只要管理员在某个时刻知道过用户的支付密码，「支付密码只有本人知道」这个前提
 * 就不成立，事后也没有办法自证不是管理员动的钱。
 *
 * ── 为什么先查一次状态 ──
 *
 * "这个人根本没设过支付密码"与"设过、只是锁住了"要求管理员做完全不同的事
 * （前者该去查工单说的是不是同一件事，后者只要解锁）。不摆出来的话，管理员
 * 会对着一个空账号执行一次带审计的破坏性操作，然后来问为什么没有效果。
 */
export function QyResetPayPasswordDialog(props: QyResetPayPasswordDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const reasonId = useId()
  const [reason, setReason] = useState('')

  const statusQuery = useQuery({
    ...qyAdminPayPasswordStatusQuery(props.userId),
    enabled: props.open && props.userId > 0,
  })

  // 换一个人（或重新打开）必须清掉上一次的事由，否则会把上一个账号的理由
  // 原样写进这一个账号的审计里。
  useEffect(() => {
    setReason('')
  }, [props.userId, props.open])

  const mutation = useMutation({
    mutationFn: qyAdminResetPayPassword,
    onSuccess: async () => {
      toast.success(t('qy_pp_admin_reset_ok', { username: props.username }))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onOpenChange(false)
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  /*
   * 解锁与重置是两件事，界面上必须都点得到。
   *
   * 连错 5 次锁 30 分钟；重置顺带会解锁，但它同时把密码清掉 —— 对着一个
   * 「密码没问题、只是自己输错了几次」的用户按重置，是把一次小麻烦升级成
   * 「下一次动钱直接被拦」。这张对话框本来就把「距离解锁还有多久」摆出来了，
   * 却只给了破坏性的那一个按钮，而解锁接口一直都在、只是没人调。
   */
  const unlock = useMutation({
    mutationFn: qyAdminUnlockPayPassword,
    onSuccess: async () => {
      toast.success(t('qy_pp_admin_unlock_ok', { username: props.username }))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const trimmed = reason.trim()
  const reasonValid =
    trimmed.length >= REASON_MIN_RUNES &&
    [...trimmed].length <= REASON_MAX_RUNES
  const status = statusQuery.data
  let lockText = t('qy_pp_status_loading')
  if (status != null) {
    lockText = status.locked
      ? t('qy_pp_state_locked_until', {
          time: formatTimestampToDate(status.locked_until),
        })
      : t('qy_pp_state_unlocked')
  }

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_pp_admin_reset_title')}
      description={`${props.username} (#${props.userId})`}
      contentClassName='sm:max-w-lg'
    >
      <div className='space-y-4'>
        <dl className='divide-border divide-y text-sm'>
          <div className='flex justify-between gap-3 py-1.5 first:pt-0'>
            <dt className='text-muted-foreground'>{t('qy_pp_status_set')}</dt>
            <dd>
              {status == null
                ? t('qy_pp_status_loading')
                : t(status.is_set ? 'qy_pp_state_set' : 'qy_pp_state_unset')}
            </dd>
          </div>
          <div className='flex justify-between gap-3 py-1.5 last:pb-0'>
            <dt className='text-muted-foreground'>{t('qy_pp_status_lock')}</dt>
            <dd>{lockText}</dd>
          </div>
        </dl>

        {status != null &&
          !status.is_set && (
            // 对一个从没设过密码的账号执行重置是一次没有效果的破坏性操作。
            // 不拦（后端也允许，它要保住 changed_at 这段历史），但必须说清楚。
            <Alert>
              <AlertDescription>{t('qy_pp_admin_reset_noop')}</AlertDescription>
            </Alert>
          )}

        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertDescription>
            {t('qy_pp_admin_reset_warn', { username: props.username })}
          </AlertDescription>
        </Alert>

        <div className='space-y-1.5'>
          <Label htmlFor={reasonId}>{t('qy_pp_admin_reset_reason')}</Label>
          <Textarea
            id={reasonId}
            rows={3}
            value={reason}
            aria-invalid={reason !== '' && !reasonValid}
            placeholder={t('qy_pp_admin_reset_reason_ph')}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_pp_admin_reset_reason_hint', { min: REASON_MIN_RUNES })}
          </p>
        </div>

        <div className='flex flex-wrap items-center gap-2'>
          <Button
            variant='destructive'
            disabled={!reasonValid || mutation.isPending}
            onClick={() =>
              mutation.mutate({ userId: props.userId, reason: trimmed })
            }
          >
            {t('qy_pp_admin_reset_submit')}
          </Button>
          {/* 只在真的锁着的时候出现：没锁的时候摆一个解锁按钮，点下去什么都不
              会变，只会让人怀疑自己是不是点错了。事由可留空 —— 解锁不改凭据。 */}
          {status?.locked === true && (
            <Button
              variant='outline'
              disabled={unlock.isPending}
              onClick={() =>
                unlock.mutate({ userId: props.userId, reason: trimmed })
              }
            >
              {t('qy_pp_admin_unlock_submit')}
            </Button>
          )}
        </div>
      </div>
    </QyResponsiveDialog>
  )
}
