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
import { formatQyQuotaLedger } from '../../../lib/format'
import { qyKeys } from '../../../lib/query-keys'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { qyAdjustCommission } from '../api'

/**
 * 这个弹窗**真正需要**的字段，只有五个。
 *
 * 收窄成结构类型而不是继续要求一个完整的 `QyCommissionBalance`：「用户佣金」
 * 那张表的行来自另一个接口（`/admin/commission/users`，人从主库出），它有这
 * 五个字段但没有余额页那一整套。两种选择摆在面前时——在调用点写一个适配对象，
 * 还是把入参收窄到实际用到的那几个字段——后者更诚实：适配对象要凭空编出
 * `user_resolved` 这类本表根本没有的值，而编出来的值会被当成事实渲染。
 *
 * `user_resolved` 可选：来自主库分页的那张表里每一行都是查出来的真实账号，
 * 不存在"这个 id 查不到"的情形，缺省即视为已解析。
 */
export type QyAdjustSubject = {
  user_id: number
  username: string
  user_resolved?: boolean
  available_quota: number
  /** decimal(30,10) 字符串。只用于算扣减上限的**提示**，判据永远在后端。 */
  unsettled_amount: string
}

type AdjustCommissionDialogProps = {
  balance: QyAdjustSubject | null
  onClose: () => void
}

/**
 * 手工增减佣金。
 *
 * ── 它做的事和「登记已提现」完全不同 ──
 * 登记已提现只是**搬运**（可提现 → 已提现，恒等式两侧不变）；这里是真的
 * 凭空加钱 / 扣钱。所以它落成一条 `manual` 计佣行，由既有的结算流程吸收进余额，
 * 而不是把某个数字改掉 —— 直接改余额列会让 Σ计佣 与 Σ结算 当场对不上，
 * 而且没有任何一行流水能解释差额。
 *
 * ── 扣减的上限 ──
 * 「可提现 + 未结算余数 + 已成熟待结算佣金」。超过这个数不是"扣得更多"，
 * 而是给这个人记一笔**欠账**，欠账会冻结他的提现。所以这里在提交之前就把
 * 上限摆出来。上限由后端在持锁的事务里重算，前端这个数字只是**指引**：
 * 它来自列表页的快照，可能已经过时，真正的判据永远在后端。
 *
 * ── 为什么有幂等键 ──
 * 语义是"增量"而不是"设为绝对值"（"这个人历史上应该是 X"这种说法在佣金上
 * 不成立，佣金是挣出来的）。增量语义下一次网络重试就是第二笔，所以必须带
 * 幂等键；它在弹窗打开时生成一次，改金额再提交会撞 409 而不是发两笔。
 */
export function AdjustCommissionDialog(props: AdjustCommissionDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const amountId = useId()
  const reasonId = useId()
  const directionId = useId()

  const [direction, setDirection] = useState<'add' | 'sub'>('add')
  const [amount, setAmount] = useState('')
  const [reason, setReason] = useState('')
  const [requestId, setRequestId] = useState('')

  // 换一个人必须重置,并且重新生成幂等键 —— 沿用上一个人的键会被后端判成
  // 参数冲突(409),而运营完全看不出为什么。
  useEffect(() => {
    setDirection('add')
    setAmount('')
    setReason('')
    setRequestId(crypto.randomUUID())
  }, [props.balance])

  const mutation = useMutation({
    mutationFn: qyAdjustCommission,
    onSuccess: async (result) => {
      toast.success(
        result.created
          ? t('qy_adj_ok', { delta: formatQyQuotaLedger(result.delta_quota) })
          : t('qy_adj_replayed')
      )
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      props.onClose()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const balance = props.balance
  const amountValue = Number(amount)
  const amountValid =
    amount.trim() !== '' && Number.isInteger(amountValue) && amountValue > 0
  const delta = direction === 'add' ? amountValue : -amountValue

  // 与后端 reclaimableCeiling 逐项对应。unsettled_amount 是 decimal 字符串，
  // 这里只用来做**提示**，取整方向与后端一致（向下取整）。
  const carry = Math.floor(Number(balance?.unsettled_amount ?? '0'))
  const ceilingHint =
    balance == null ? 0 : Math.max(0, balance.available_quota + carry)
  const overCeiling =
    direction === 'sub' && amountValid && amountValue > ceilingHint
  // 上限是**输入框那一格**的上限，而那一格填的是额度整数，所以这里必须双写：
  // 只印 `$0.27` 的话，管理员没有任何办法知道该往框里敲哪个整数。
  const ceilingText = t('qy_common_quota_with_amount', {
    quota: ceilingHint,
    amount: formatQyQuotaLedger(ceilingHint),
  })
  const reasonValid = qyRuneLength(reason.trim()) >= QY_FUND_REASON_MIN_RUNES

  let subject: string | undefined
  if (balance != null) {
    subject =
      balance.user_resolved !== false
        ? `${balance.username} (#${balance.user_id})`
        : `#${balance.user_id}`
  }

  return (
    <QyResponsiveDialog
      open={balance != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_adj_title')}
      description={subject}
    >
      {balance != null && (
        <div className='space-y-4'>
          <Alert>
            <AlertDescription>{t('qy_adj_ledger_note')}</AlertDescription>
          </Alert>

          <dl className='divide-border divide-y text-sm'>
            <div className='flex justify-between gap-3 py-1.5 first:pt-0'>
              <dt className='text-muted-foreground'>{t('qy_cb_available')}</dt>
              <dd>
                <QyAmountText quota={balance.available_quota} />
              </dd>
            </div>
            <div className='flex justify-between gap-3 py-1.5 last:pb-0'>
              <dt className='text-muted-foreground'>{t('qy_adj_unsettled')}</dt>
              {/* 未结算余数与上面的可提现是**同一个单位**（都是站内额度，
                  只是这一个还没满 1 整数、由后端以 decimal 字符串下发）。
                  一个印 `$0.27`、一个印 `0.4700000000`，看的人会以为它们
                  是两种钱，而调整上限恰恰是这两个数加起来。 */}
              <dd>
                <QyAmountText quota={balance.unsettled_amount} />
              </dd>
            </div>
          </dl>

          <div className='space-y-1.5'>
            <Label htmlFor={directionId}>{t('qy_adj_direction')}</Label>
            <NativeSelect
              id={directionId}
              size='sm'
              value={direction}
              onChange={(event) =>
                setDirection(event.target.value === 'sub' ? 'sub' : 'add')
              }
            >
              <NativeSelectOption value='add'>
                {t('qy_adj_dir_add')}
              </NativeSelectOption>
              <NativeSelectOption value='sub'>
                {t('qy_adj_dir_sub')}
              </NativeSelectOption>
            </NativeSelect>
          </div>

          <div className='space-y-1.5'>
            <Label htmlFor={amountId}>{t('qy_adj_amount')}</Label>
            <Input
              id={amountId}
              inputMode='numeric'
              value={amount}
              aria-invalid={amount !== '' && (!amountValid || overCeiling)}
              onChange={(event) => setAmount(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {direction === 'sub'
                ? t('qy_adj_amount_hint_sub', { ceiling: ceilingText })
                : t('qy_adj_amount_hint_add')}
            </p>
          </div>

          {overCeiling && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t('qy_adj_over_ceiling', { ceiling: ceilingText })}
              </AlertDescription>
            </Alert>
          )}

          <div className='space-y-1.5'>
            <Label htmlFor={reasonId}>{t('qy_adj_reason')}</Label>
            <Textarea
              id={reasonId}
              rows={3}
              value={reason}
              aria-invalid={!reasonValid}
              placeholder={t('qy_adj_reason_ph')}
              onChange={(event) => setReason(event.target.value)}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_adj_reason_hint')}
            </p>
          </div>

          <Button
            variant={direction === 'sub' ? 'destructive' : 'default'}
            disabled={
              !amountValid || overCeiling || !reasonValid || mutation.isPending
            }
            onClick={() =>
              mutation.mutate({
                user_id: balance.user_id,
                delta_quota: delta,
                reason: reason.trim(),
                client_request_id: requestId,
              })
            }
          >
            {t('qy_adj_submit')}
          </Button>
        </div>
      )}
    </QyResponsiveDialog>
  )
}
