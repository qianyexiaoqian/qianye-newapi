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
import { Eye, Gift, TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { QY_FUND_REASON_MIN_RUNES, qyRuneLength } from '../../lib/constants'
import { isQyMallPrizeOrder, qyMallOrderStatusView } from '../../mall/lib/order'
import { qyMallKindKey } from '../../mall/lib/product'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import {
  adjudicateQyMallOrder,
  failQyMallOrder,
  revokeQyMallOrderCode,
  shipQyMallOrder,
} from '../api'
import { qyMallAdminActions } from '../lib/order-actions'
import type { QyMallAdjudicateInput, QyMallAdminOrder } from '../types'

/** 后端事由列宽 255，与 ship_note / fail_reason 同一列宽。 */
const MAX_REASON_RUNES = 255

type PendingAction = 'adjudicate' | 'complete' | 'fail' | 'revoke_code' | 'ship'

/**
 * 订单处理弹窗（管理端）。
 *
 * 单据来自列表那一行（契约 §5 没有管理端的单张详情接口，也就拿不到事件时间线），
 * 所以这里的四个动作成功后一律**关掉弹窗并全量失效**：手里那一行已经过期，
 * 留着它只会让人对着旧状态再点一次。
 *
 * 五个动作各自的硬约束，都不是样式问题：
 *   1. 发货必填单号 —— 没有单号的"已发货"在争议时等于没发；
 *      「标记完成」走同一发货端点的 done:true(shipped → done),单号可不填 ——
 *      发货那次已经填过;发货表单里勾「发货即完结」则一步到 done;
 *   2. 标记失败 / 撤回码必填事由，且会**退星屑**：事由写进事件流水，用户端
 *      时间线原样显示，那是"为什么"的唯一答案；
 *   3. 揭示地址是被审计的高敏读取，走独立弹窗（`onRevealAddress`）；
 *   4. **裁决**只对 `kind=plan && status=held`，且是超级管理员专属
 *      （后端 `RootActionGate`）：role=10 看到的是一句"该找谁"，不是一颗
 *      点了吃 403 的按钮，形状照 `__tests__/root-action-gates.test.tsx`；
 *   5. 409 表示单据刚被别人改过，关掉弹窗让人看最新状态，绝不重试。
 */
export function QyMallAdminOrderDialog(props: {
  order: QyMallAdminOrder | null
  onClose: () => void
  onRevealAddress: (orderNo: string) => void
}) {
  const { order } = props
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const trackingId = useId()
  const noteId = useId()
  const doneId = useId()
  const reasonId = useId()
  const isRoot =
    useAuthStore((state) => state.auth.user?.role) === ROLE.SUPER_ADMIN

  const [action, setAction] = useState<PendingAction | null>(null)
  const [trackingNo, setTrackingNo] = useState('')
  const [shipNote, setShipNote] = useState('')
  const [shipDone, setShipDone] = useState(false)
  const [reason, setReason] = useState('')
  const [verdict, setVerdict] =
    useState<QyMallAdjudicateInput['verdict']>('not_applied')

  useEffect(() => {
    setAction(null)
    setTrackingNo('')
    setShipNote('')
    setShipDone(false)
    setReason('')
    setVerdict('not_applied')
  }, [order])

  const handlers = {
    onSuccess: async () => {
      toast.success(t('qy_mladm_action_done'))
      props.onClose()
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: async (error: unknown) => {
      toast.error(qyErrorMessage(error, t))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      if (isQyError(error) && error.kind === 'conflict') props.onClose()
    },
  }
  const orderNo = order?.order_no ?? ''
  const ship = useMutation({
    mutationFn: () => {
      const done = action === 'complete' || shipDone
      return shipQyMallOrder(orderNo, {
        tracking_no: trackingNo.trim(),
        ship_note: shipNote.trim() === '' ? undefined : shipNote.trim(),
        ...(done ? { done: true } : {}),
      })
    },
    ...handlers,
  })
  const fail = useMutation({
    mutationFn: () => failQyMallOrder(orderNo, reason.trim()),
    ...handlers,
  })
  const revoke = useMutation({
    mutationFn: () => revokeQyMallOrderCode(orderNo, reason.trim()),
    ...handlers,
  })
  const adjudicate = useMutation({
    mutationFn: () =>
      adjudicateQyMallOrder(orderNo, { verdict, reason: reason.trim() }),
    ...handlers,
  })
  const busy =
    ship.isPending || fail.isPending || revoke.isPending || adjudicate.isPending

  const actions = order == null ? [] : qyMallAdminActions(order)
  const reasonLength = qyRuneLength(reason.trim())
  const reasonInvalid =
    reasonLength < QY_FUND_REASON_MIN_RUNES || reasonLength > MAX_REASON_RUNES
  const view =
    order == null
      ? null
      : qyMallOrderStatusView(order.status, order.kind, order)
  const isPrize = order != null && isQyMallPrizeOrder(order)
  // 实物奖品单没填地址：发不了货、也没有地址可揭示。按钮留着但禁用，旁边说明
  // 为什么 —— 抹掉按钮会让运营以为这张单坏了。后端对它同样是 409
  // `qy_ml_address_missing`，这里只是别让人白点一次。
  const addressMissing = order?.address_missing === true

  return (
    <QyResponsiveDialog
      open={order != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_mladm_order_detail')}
      description={order?.order_no}
      footer={
        <Button type='button' variant='outline' onClick={props.onClose}>
          {t('qy_common_close')}
        </Button>
      }
    >
      {order != null && view != null && (
        <div className='space-y-4'>
          {order.status === 'held' && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>{t('qy_mladm_held_title')}</AlertTitle>
              <AlertDescription>{t('qy_mladm_held_desc')}</AlertDescription>
            </Alert>
          )}

          <div>
            <QyKeyValue label={t('qy_common_order_no')}>
              <span className='inline-flex items-center gap-1'>
                <span className='font-mono text-xs break-all'>
                  {order.order_no}
                </span>
                <CopyButton
                  value={order.order_no}
                  className='size-6'
                  iconClassName='size-3'
                />
              </span>
            </QyKeyValue>
            <QyKeyValue label={t('qy_common_user')}>
              #{order.user_id} {order.username}
            </QyKeyValue>
            <QyKeyValue label={t('qy_ml_product')}>
              <span className='inline-flex flex-wrap items-center justify-end gap-1.5'>
                <span>{order.title}</span>
                <Badge variant='outline'>
                  {t(qyMallKindKey(order.kind), { defaultValue: order.kind })}
                </Badge>
                {isPrize && (
                  <Badge variant='secondary' className='gap-1'>
                    <Gift aria-hidden='true' className='size-3' />
                    {t('qy_ml_source_lottery')}
                  </Badge>
                )}
              </span>
            </QyKeyValue>
            {(order.product_no ?? '') !== '' && (
              <QyKeyValue label={t('qy_mladm_product_no')}>
                <span className='font-mono text-xs'>{order.product_no}</span>
              </QyKeyValue>
            )}
            <QyKeyValue label={t('qy_ml_price')}>
              {isPrize ? (
                t('qy_ml_price_prize')
              ) : (
                <QySdAmount amount={order.price} variant='hero' />
              )}
            </QyKeyValue>
            {/* 出款号：对回抽奖后台「派奖」那一行，争议时两边要能互相找到。 */}
            {isPrize && order.ref_no !== '' && (
              <QyKeyValue label={t('qy_ml_prize_ref_no')}>
                <span className='font-mono text-xs break-all'>
                  {order.ref_no}
                </span>
              </QyKeyValue>
            )}
            <QyKeyValue label={t('qy_common_status')}>
              <QyStatusBadge
                status={view.status}
                label={view.labelKey == null ? undefined : t(view.labelKey)}
              />
            </QyKeyValue>
            <QyKeyValue label={t('qy_common_created_at')}>
              {formatQyTs(order.created_at)}
            </QyKeyValue>
            {order.fulfilled_at > 0 && (
              <QyKeyValue label={t('qy_ml_fulfilled_at')}>
                {formatQyTs(order.fulfilled_at)}
              </QyKeyValue>
            )}
            {order.tracking_no !== '' && (
              <QyKeyValue label={t('qy_ml_tracking_no')}>
                <span className='font-mono text-xs break-all'>
                  {order.tracking_no}
                </span>
              </QyKeyValue>
            )}
            {order.ship_note !== '' && (
              <QyKeyValue label={t('qy_ml_ship_note')}>
                {order.ship_note}
              </QyKeyValue>
            )}
            {order.fail_reason !== '' && (
              <QyKeyValue label={t('qy_ml_fail_reason')}>
                <span className='text-destructive'>{order.fail_reason}</span>
              </QyKeyValue>
            )}
            {order.kind === 'plan' && (
              <>
                {order.fund_order_no !== '' && (
                  <QyKeyValue label={t('qy_mladm_fund_order_no')}>
                    <span className='font-mono text-xs break-all'>
                      {order.fund_order_no}
                    </span>
                  </QyKeyValue>
                )}
                {order.user_subscription_id > 0 && (
                  <QyKeyValue label={t('qy_ml_plan_subscription')}>
                    <span className='inline-flex flex-wrap items-center justify-end gap-1.5'>
                      <span className='font-mono text-xs'>
                        #{order.user_subscription_id}
                      </span>
                      <span className='text-muted-foreground text-xs'>
                        {order.sub_renewed
                          ? t('qy_ml_plan_renewed')
                          : t('qy_ml_plan_new_sub')}
                      </span>
                    </span>
                  </QyKeyValue>
                )}
              </>
            )}
          </div>

          <Separator />

          {action == null ? (
            <div className='flex flex-wrap items-center gap-2'>
              {actions.includes('ship') && (
                <Button
                  type='button'
                  disabled={busy || addressMissing}
                  onClick={() => setAction('ship')}
                >
                  {t('qy_mladm_ship')}
                </Button>
              )}
              {addressMissing && (
                <span className='text-muted-foreground text-xs'>
                  {t('qy_mladm_ship_needs_address')}
                </span>
              )}
              {actions.includes('complete') && (
                <Button
                  type='button'
                  disabled={busy}
                  onClick={() => setAction('complete')}
                >
                  {t('qy_mladm_complete')}
                </Button>
              )}
              {actions.includes('fail') && (
                <Button
                  type='button'
                  variant='destructive'
                  disabled={busy}
                  onClick={() => setAction('fail')}
                >
                  {t('qy_mladm_fail')}
                </Button>
              )}
              {actions.includes('revoke_code') && (
                <Button
                  type='button'
                  variant='destructive'
                  disabled={busy}
                  onClick={() => setAction('revoke_code')}
                >
                  {t('qy_mladm_revoke_code')}
                </Button>
              )}
              {actions.includes('reveal_address') && (
                <Button
                  type='button'
                  variant='outline'
                  disabled={busy || addressMissing}
                  onClick={() => props.onRevealAddress(order.order_no)}
                >
                  <Eye aria-hidden='true' />
                  {t('qy_mladm_reveal_address')}
                </Button>
              )}
              {actions.includes('adjudicate') &&
                (isRoot ? (
                  <Button
                    type='button'
                    variant='destructive'
                    disabled={busy}
                    onClick={() => setAction('adjudicate')}
                  >
                    {t('qy_mladm_adjudicate')}
                  </Button>
                ) : (
                  // 按钮换成一句话而不是抹掉：role=10 看到的不能是一张没有任何
                  // 出口的单，而正确的下一步（"裁决这一步交给超管"）在界面上得有字。
                  <span className='text-muted-foreground text-xs'>
                    {t('qy_mladm_adjudicate_root_only')}
                  </span>
                ))}
            </div>
          ) : (
            <div className='space-y-3 rounded-md border p-3'>
              {action === 'ship' && (
                <>
                  <div className='space-y-1.5'>
                    <Label htmlFor={trackingId}>
                      {t('qy_mladm_tracking_no')}
                    </Label>
                    <Input
                      id={trackingId}
                      value={trackingNo}
                      autoComplete='off'
                      placeholder={t('qy_mladm_tracking_no_ph')}
                      aria-invalid={trackingNo.trim() === ''}
                      onChange={(event) => setTrackingNo(event.target.value)}
                    />
                  </div>
                  <div className='space-y-1.5'>
                    <Label htmlFor={noteId}>{t('qy_mladm_ship_note')}</Label>
                    <Textarea
                      id={noteId}
                      rows={2}
                      value={shipNote}
                      maxLength={MAX_REASON_RUNES}
                      onChange={(event) => setShipNote(event.target.value)}
                    />
                    <p className='text-muted-foreground text-xs'>
                      {t('qy_mladm_ship_note_hint')}
                    </p>
                  </div>
                  <Label htmlFor={doneId} className='text-sm font-normal'>
                    <Checkbox
                      id={doneId}
                      checked={shipDone}
                      onCheckedChange={(checked) =>
                        setShipDone(checked === true)
                      }
                    />
                    {t('qy_mladm_ship_done_now')}
                  </Label>
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_mladm_ship_done_now_hint')}
                  </p>
                </>
              )}

              {action === 'complete' && (
                <div className='space-y-1.5'>
                  <Label htmlFor={trackingId}>
                    {t('qy_mladm_tracking_no')}
                  </Label>
                  <Input
                    id={trackingId}
                    value={trackingNo}
                    autoComplete='off'
                    placeholder={
                      order.tracking_no === ''
                        ? t('qy_mladm_tracking_no_ph')
                        : order.tracking_no
                    }
                    onChange={(event) => setTrackingNo(event.target.value)}
                  />
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_mladm_complete_hint')}
                  </p>
                </div>
              )}

              {action === 'adjudicate' && (
                <div className='space-y-2'>
                  <Alert variant='destructive'>
                    <TriangleAlert />
                    <AlertDescription>
                      {t('qy_mladm_adjudicate_warn')}
                    </AlertDescription>
                  </Alert>
                  <Label>{t('qy_mladm_verdict')}</Label>
                  <RadioGroup
                    value={verdict}
                    onValueChange={(value) =>
                      setVerdict(
                        value === 'applied' ? 'applied' : 'not_applied'
                      )
                    }
                    className='gap-2'
                  >
                    {(['not_applied', 'applied'] as const).map((item) => (
                      <label
                        key={item}
                        className='hover:bg-muted/40 flex cursor-pointer items-start gap-3 rounded-lg border p-3'
                      >
                        <RadioGroupItem value={item} className='mt-0.5' />
                        <span className='min-w-0 flex-1'>
                          <span className='block text-sm font-medium'>
                            {t(`qy_mladm_verdict_${item}`)}
                          </span>
                          <span className='text-muted-foreground mt-0.5 block text-xs'>
                            {t(`qy_mladm_verdict_${item}_desc`, {
                              amount: formatSdWithUnit(order.price, unit),
                            })}
                          </span>
                        </span>
                      </label>
                    ))}
                  </RadioGroup>
                </div>
              )}

              {action !== 'ship' && action !== 'complete' && (
                <div className='space-y-1.5'>
                  <Label htmlFor={reasonId}>{t('qy_common_reason')}</Label>
                  <Textarea
                    id={reasonId}
                    rows={3}
                    value={reason}
                    aria-invalid={reasonInvalid}
                    placeholder={t('qy_mladm_reason_ph')}
                    onChange={(event) => setReason(event.target.value)}
                  />
                  <p className='text-muted-foreground text-end text-xs tabular-nums'>
                    {t('qy_common_rune_counter', {
                      used: reasonLength,
                      max: MAX_REASON_RUNES,
                    })}
                  </p>
                  <p className='text-muted-foreground text-xs'>
                    {action === 'adjudicate'
                      ? t('qy_mladm_reason_hint', {
                          min: QY_FUND_REASON_MIN_RUNES,
                        })
                      : t('qy_mladm_refund_hint', {
                          min: QY_FUND_REASON_MIN_RUNES,
                          amount: formatSdWithUnit(order.price, unit),
                        })}
                  </p>
                </div>
              )}

              <div className='flex flex-wrap gap-2'>
                {action === 'ship' && (
                  <Button
                    type='button'
                    disabled={busy || trackingNo.trim() === ''}
                    onClick={() => ship.mutate()}
                  >
                    {t('qy_mladm_confirm_ship')}
                  </Button>
                )}
                {action === 'complete' && (
                  <Button
                    type='button'
                    disabled={busy}
                    onClick={() => ship.mutate()}
                  >
                    {t('qy_mladm_confirm_complete')}
                  </Button>
                )}
                {action === 'fail' && (
                  <Button
                    type='button'
                    variant='destructive'
                    disabled={busy || reasonInvalid}
                    onClick={() => fail.mutate()}
                  >
                    {t('qy_mladm_confirm_fail')}
                  </Button>
                )}
                {action === 'revoke_code' && (
                  <Button
                    type='button'
                    variant='destructive'
                    disabled={busy || reasonInvalid}
                    onClick={() => revoke.mutate()}
                  >
                    {t('qy_mladm_confirm_revoke')}
                  </Button>
                )}
                {action === 'adjudicate' && (
                  <Button
                    type='button'
                    variant='destructive'
                    disabled={busy || reasonInvalid}
                    onClick={() => adjudicate.mutate()}
                  >
                    {t('qy_mladm_confirm_adjudicate')}
                  </Button>
                )}
                <Button
                  type='button'
                  variant='ghost'
                  disabled={busy}
                  onClick={() => setAction(null)}
                >
                  {t('qy_common_cancel')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </QyResponsiveDialog>
  )
}
