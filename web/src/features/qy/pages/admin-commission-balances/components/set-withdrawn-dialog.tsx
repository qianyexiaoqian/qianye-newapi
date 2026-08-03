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

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qySetCommissionWithdrawn } from '../api'
import type { QyCommissionBalance } from '../types'

/** 后端 `adminSetWithdrawn` 的事由下限，两侧必须一致。 */
const REASON_MIN_RUNES = 4

type SetWithdrawnDialogProps = {
  balance: QyCommissionBalance | null
  onClose: () => void
}

/**
 * 登记「已提现」额度（外挂佣金系统的数据迁移）。
 *
 * ── 这个弹窗必须让运营看懂的一件事 ──
 * 它改的不只是一个显示数字：差额会**从可提现里划走**。所以这里实时算出
 * "改完之后可提现会变成多少"并直接摆在按钮上方 —— 只显示一个输入框的话，
 * 运营会以为自己在填一个统计值，直到用户来投诉佣金少了才发现。
 *
 * ── 为什么没有幂等键 ──
 * 语义是"设为绝对值"，重复提交天然是空操作。见 `api.ts` 的说明。
 */
export function SetWithdrawnDialog(props: SetWithdrawnDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const quotaId = useId()
  const reasonId = useId()

  const [target, setTarget] = useState('')
  const [reason, setReason] = useState('')

  // 换一个人必须重置，否则会把上一个人的目标值与事由带过来 ——
  // 这里带过来的是钱。
  useEffect(() => {
    setTarget(
      props.balance == null ? '' : String(props.balance.withdrawn_quota)
    )
    setReason('')
  }, [props.balance])

  const mutation = useMutation({
    mutationFn: qySetCommissionWithdrawn,
    onSuccess: async (result) => {
      toast.success(
        result.delta_quota === 0
          ? t('qy_cb_set_noop')
          : t('qy_cb_set_ok', { delta: result.delta_quota })
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const balance = props.balance
  const targetValue = Number(target)
  const targetValid =
    target.trim() !== '' && Number.isInteger(targetValue) && targetValue >= 0

  // 上界与后端 `delta > bal.AvailableQuota` 的判据逐字对应：
  // 已提现最多只能设到「当前已提现 + 当前可提现」，再多可提现就成负数了。
  const ceiling =
    balance == null ? 0 : balance.withdrawn_quota + balance.available_quota
  const delta = balance == null ? 0 : targetValue - balance.withdrawn_quota
  const nextAvailable = balance == null ? 0 : balance.available_quota - delta
  const overCeiling = targetValid && targetValue > ceiling
  const reasonValid = reason.trim().length >= REASON_MIN_RUNES

  let subject: string | undefined
  if (balance != null) {
    subject = balance.user_resolved
      ? `${balance.username} (#${balance.user_id})`
      : `#${balance.user_id}`
  }

  return (
    <QyResponsiveDialog
      open={balance != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_cb_set_title')}
      description={subject}
    >
      {balance != null && (
        <div className='space-y-4'>
          <dl className='divide-border divide-y text-sm'>
            <div className='flex justify-between gap-3 py-1.5 first:pt-0'>
              <dt className='text-muted-foreground'>{t('qy_cb_available')}</dt>
              <dd>
                <QyAmountText quota={balance.available_quota} />
              </dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5'>
              <dt className='text-muted-foreground'>{t('qy_cb_frozen')}</dt>
              <dd>
                <QyAmountText quota={balance.frozen_quota} />
              </dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5 last:pb-0'>
              <dt className='text-muted-foreground'>{t('qy_cb_withdrawn')}</dt>
              <dd>
                <QyAmountText quota={balance.withdrawn_quota} />
              </dd>
            </div>
          </dl>

          <div className='space-y-1.5'>
            <Label htmlFor={quotaId}>{t('qy_cb_set_target')}</Label>
            <Input
              id={quotaId}
              inputMode='numeric'
              value={target}
              aria-invalid={target !== '' && (!targetValid || overCeiling)}
              onChange={(event) => setTarget(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_cb_set_target_hint', { ceiling })}
            </p>
          </div>

          {targetValid &&
            !overCeiling && (
              // 这一句是本弹窗存在的理由：把"填这个数会发生什么"直接说出来。
              <Alert>
                <AlertDescription>
                  {delta === 0
                    ? t('qy_cb_set_preview_noop')
                    : t('qy_cb_set_preview', {
                        delta: Math.abs(delta),
                        direction: t(
                          delta > 0 ? 'qy_cb_set_dir_out' : 'qy_cb_set_dir_back'
                        ),
                        next: nextAvailable,
                      })}
                </AlertDescription>
              </Alert>
            )}
          {overCeiling && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('qy_cb_set_over_ceiling', { ceiling })}
              </AlertDescription>
            </Alert>
          )}

          <div className='space-y-1.5'>
            <Label htmlFor={reasonId}>{t('qy_cb_set_reason')}</Label>
            <Textarea
              id={reasonId}
              rows={3}
              value={reason}
              aria-invalid={!reasonValid}
              placeholder={t('qy_cb_set_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_cb_set_reason_hint')}
            </p>
          </div>

          <Button
            variant='destructive'
            disabled={
              !targetValid || overCeiling || !reasonValid || mutation.isPending
            }
            onClick={() =>
              mutation.mutate({
                user_id: balance.user_id,
                withdrawn_quota: targetValue,
                reason: reason.trim(),
              })
            }
          >
            {t('qy_cb_set_submit')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
