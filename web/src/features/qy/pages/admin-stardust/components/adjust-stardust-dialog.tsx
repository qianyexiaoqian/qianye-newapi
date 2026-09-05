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

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QySdInput } from '../../../components/qy-sd-input'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { isQyError } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { qySdAdminErrorMessage } from '../../admin-stardust-config/lib/errors'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { newQyRequestId } from '../../lib/request-id'
import { adjustQyStardust } from '../api'

/**
 * 手调的对象。从余额表的一行点进来时三个字段都有；从页面右上角点进来时只有
 * `user_id = 0`，让管理员自己填。`available` 为 `null` 表示不知道（不做扣减上限提示）。
 */
export type QySdAdjustTarget = {
  user_id: number
  username: string
  available: number | null
}

type AdjustStardustDialogProps = {
  target: QySdAdjustTarget | null
  onClose: () => void
}

/**
 * 手工增减某个人的星屑（超级管理员专属，后端 `RootActionStardustAdjust`）。
 *
 * ── 它落成一条 `manual` 流水，不是改余额列 ──
 * 余额上的每个数字都是派生量、由流水解释。直接改列会当场打破账本恒等式，
 * 而且没有任何一行流水能解释差额。所以这里的每一次提交都是账本上的一条记账，
 * 带单号、事由与操作人。
 *
 * ── 幂等键 ──
 * 语义是**增量**，一次网络重试就是第二笔。所以键在弹窗打开时生成一次、重试沿用；
 * 改了金额再提交会撞 409（`qy_idem_key_conflict`）而不是发两笔 —— 那时账本上
 * 执行的是上一次那份，弹窗直接关掉让人重新打开。
 *
 * ── 扣减 ──
 * 后端不允许扣到负（`qy_sd_insufficient`）。列表带过来的余额只是**提示**，
 * 判据永远在后端持锁的事务里。
 */
export function AdjustStardustDialog(props: AdjustStardustDialogProps) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const userIdId = useId()
  const directionId = useId()
  const amountId = useId()
  const reasonId = useId()

  const [userIdText, setUserIdText] = useState('')
  const [direction, setDirection] = useState<'add' | 'sub'>('add')
  const [amount, setAmount] = useState(0)
  const [reason, setReason] = useState('')
  const [requestId, setRequestId] = useState('')

  // 换一个人必须重置，并且重新生成幂等键 —— 沿用上一个人的键会被后端判成
  // 参数冲突（409），而运营完全看不出为什么。
  useEffect(() => {
    if (props.target == null) return
    setUserIdText(props.target.user_id > 0 ? String(props.target.user_id) : '')
    setDirection('add')
    setAmount(0)
    setReason('')
    setRequestId(newQyRequestId())
  }, [props.target])

  const mutation = useMutation({
    mutationFn: adjustQyStardust,
    onSuccess: async (result) => {
      toast.success(
        result.replayed
          ? t('qy_sdadm_adjust_replayed')
          : t('qy_sdadm_adjust_ok', {
              delta: formatSdWithUnit(result.delta, unit),
              balance: formatSdWithUnit(result.balance_after, unit),
            })
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: async (error) => {
      toast.error(qySdAdminErrorMessage(error, t))
      // 409 = 这个幂等键已经带着另一组参数执行过了。账本可能真的动了，
      // 所以刷新所有视图并关掉弹窗 —— 留着它只会诱人再按一次。
      if (isQyError(error) && error.kind === 'conflict') {
        await queryClient.invalidateQueries({ queryKey: qyKeys.all })
        props.onClose()
      }
    },
  })

  const target = props.target
  const userId = /^\d+$/.test(userIdText) ? Number(userIdText) : 0
  const userIdValid = Number.isSafeInteger(userId) && userId > 0
  const amountValid = Number.isSafeInteger(amount) && amount > 0
  const known = target?.available ?? null
  const overAvailable =
    direction === 'sub' && amountValid && known != null && amount > known
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES
  const delta = direction === 'add' ? amount : -amount

  let subject: string | undefined
  if (target != null && target.user_id > 0) {
    subject =
      target.username === ''
        ? `#${target.user_id}`
        : `${target.username} (#${target.user_id})`
  }

  return (
    <QyResponsiveDialog
      open={target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_sdadm_adjust_title')}
      description={subject ?? t('qy_sdadm_adjust_desc_free')}
      contentClassName='sm:max-w-lg'
      footer={
        <Button
          variant={direction === 'sub' ? 'destructive' : 'default'}
          disabled={
            !userIdValid ||
            !amountValid ||
            overAvailable ||
            !reasonValid ||
            mutation.isPending
          }
          onClick={() =>
            mutation.mutate({
              user_id: userId,
              delta,
              reason: reason.trim(),
              client_request_id: requestId,
            })
          }
        >
          {t('qy_sdadm_adjust_submit')}
        </Button>
      }
    >
      {target != null && (
        <div className='space-y-4'>
          <Alert>
            <AlertDescription>
              {t('qy_sdadm_adjust_ledger_note')}
            </AlertDescription>
          </Alert>

          {target.user_id <= 0 && (
            <div className='space-y-1.5'>
              <Label htmlFor={userIdId}>{t('qy_sdadm_adjust_user_id')}</Label>
              <Input
                id={userIdId}
                inputMode='numeric'
                value={userIdText}
                aria-invalid={userIdText !== '' && !userIdValid}
                placeholder={t('qy_sdadm_adjust_user_id_ph')}
                onChange={(event) =>
                  setUserIdText(event.target.value.replaceAll(/\D/g, ''))
                }
              />
            </div>
          )}

          {known != null && (
            <dl className='divide-border divide-y text-sm'>
              <div className='flex justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
                <dt className='text-muted-foreground'>
                  {t('qy_sdadm_col_available')}
                </dt>
                <dd>
                  <QySdAmount amount={known} />
                </dd>
              </div>
            </dl>
          )}

          <div className='space-y-1.5'>
            <Label htmlFor={directionId}>
              {t('qy_sdadm_adjust_direction')}
            </Label>
            <NativeSelect
              id={directionId}
              className='w-full'
              value={direction}
              onChange={(event) =>
                setDirection(event.target.value === 'sub' ? 'sub' : 'add')
              }
            >
              <NativeSelectOption value='add'>
                {t('qy_sdadm_adjust_dir_add', { unit })}
              </NativeSelectOption>
              <NativeSelectOption value='sub'>
                {t('qy_sdadm_adjust_dir_sub', { unit })}
              </NativeSelectOption>
            </NativeSelect>
          </div>

          <div className='space-y-1.5'>
            <Label htmlFor={amountId}>{t('qy_common_amount')}</Label>
            <QySdInput
              id={amountId}
              value={amount}
              onChange={setAmount}
              min={1}
            />
            <p className='text-muted-foreground text-xs'>
              {direction === 'sub'
                ? t('qy_sdadm_adjust_amount_hint_sub')
                : t('qy_sdadm_adjust_amount_hint_add')}
            </p>
          </div>

          {overAvailable && known != null && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('qy_sdadm_adjust_over_available', {
                  available: formatSdWithUnit(known, unit),
                })}
              </AlertDescription>
            </Alert>
          )}

          <div className='space-y-1.5'>
            <Label htmlFor={reasonId}>{t('qy_common_reason')}</Label>
            <Textarea
              id={reasonId}
              rows={3}
              value={reason}
              aria-invalid={reason !== '' && !reasonValid}
              placeholder={t('qy_sdadm_adjust_reason_ph', {
                min: QY_FUND_REASON_MIN_RUNES,
              })}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_sdadm_adjust_reason_hint')}
            </p>
          </div>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
