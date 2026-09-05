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
import { Gift, KeyRound, TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { QyTimeline } from '../../../components/qy-timeline'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyErrorMessage } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyKeys } from '../../../lib/query-keys'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { cancelQyMallOrder, qyMallOrderQuery } from '../api'
import {
  buildQyMallTimeline,
  isQyMallPrizeOrder,
  qyMallOrderStatusView,
} from '../lib/order'
import { qyMallKindKey } from '../lib/product'
import { QyMallPrizeAddressForm } from './mall-prize-address-form'

/**
 * 我的订单详情。
 *
 * 三种商品在这一屏上各有一件"接下来能做的事"：
 *   · 兑换码 `done` → 揭示码（验密，出口在列表页那一层，见 `onRevealCode`）；
 *   · 实物 `paid`   → 取消（全额退回星屑）；发货后只能等；
 *   · 套餐         → 没有动作，只看结果：`done` 显示订阅 id 与是否续期，
 *                    `held` 显示「待核对」—— 钱扣了、主库动没动不知道，交给人。
 *
 * 揭示码的弹窗不叠在这个弹窗上面，而是由列表页并列渲染：它自己是一层
 * `QyResponsiveDialog`，两层模态叠开在移动端抽屉形态下并不可靠。
 */
export function QyMallOrderDetailDialog(props: {
  orderNo: string | null
  onClose: () => void
  onRevealCode: (orderNo: string) => void
}) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const orderNo = props.orderNo ?? ''
  const query = useQuery(qyMallOrderQuery(orderNo))
  const order = query.data
  const [cancelOpen, setCancelOpen] = useState(false)

  const cancel = useMutation({
    mutationFn: () => cancelQyMallOrder(orderNo),
    onSuccess: async () => {
      toast.success(t('qy_ml_cancel_done'))
      setCancelOpen(false)
      // 退款改了余额、商品的 sold / my_count 与这张单本身：全量失效。
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: async (error) => {
      toast.error(qyErrorMessage(error, t))
      setCancelOpen(false)
      // 409 = 状态已被管理员改过（比如刚发货）：刷新后让人看最新状态。
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
  })

  const view =
    order == null
      ? null
      : qyMallOrderStatusView(order.status, order.kind, order)
  const isPrize = order != null && isQyMallPrizeOrder(order)
  // 奖品单没有"取消并退回星屑"这回事：它一分钱都没扣，后端对它也直接拒
  // （`order.go` 只给 source=mall 退款）。
  const canCancel =
    order?.kind === 'physical' && order.status === 'paid' && !isPrize
  const canReveal = order?.kind === 'code' && order.status === 'done'
  const needsAddress = order?.address_missing === true

  return (
    <QyResponsiveDialog
      open={props.orderNo != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_ml_order_detail')}
      description={props.orderNo ?? undefined}
      footer={
        <>
          {canCancel && (
            <Button
              type='button'
              variant='destructive'
              disabled={cancel.isPending}
              onClick={() => setCancelOpen(true)}
            >
              {t('qy_ml_cancel_btn')}
            </Button>
          )}
          {canReveal && (
            <Button
              type='button'
              onClick={() => props.onRevealCode(order.order_no)}
            >
              <KeyRound aria-hidden='true' />
              {t('qy_ml_code_reveal_btn')}
            </Button>
          )}
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
        </>
      }
    >
      <QyPageBoundary query={query}>
        {order != null && view != null && (
          <div className='space-y-4'>
            {order.status === 'held' && (
              // 「待核对」必须解释清楚：扣了钱、结果不明、有人在处理、不要重买。
              // 说成"处理中"用户会等，说成"失败"用户会再买一次。
              <Alert variant='destructive'>
                <TriangleAlert />
                <AlertTitle>{t('qy_ml_held_title')}</AlertTitle>
                <AlertDescription>{t('qy_ml_held_desc')}</AlertDescription>
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
              <QyKeyValue label={t('qy_ml_product')}>
                <span className='inline-flex flex-wrap items-center justify-end gap-1.5'>
                  <span>{order.title}</span>
                  {isPrize && (
                    <Badge variant='secondary' className='gap-1'>
                      <Gift aria-hidden='true' className='size-3' />
                      {t('qy_ml_source_lottery')}
                    </Badge>
                  )}
                </span>
              </QyKeyValue>
              <QyKeyValue label={t('qy_ml_kind')}>
                {t(qyMallKindKey(order.kind), { defaultValue: order.kind })}
              </QyKeyValue>
              {/* 奖品单：价格那一行写「奖品」而不是 0 星屑；出款号留着，能对回
                  「我的参与」里那一次中奖。 */}
              <QyKeyValue label={t('qy_ml_price')}>
                {isPrize ? (
                  t('qy_ml_price_prize')
                ) : (
                  <QySdAmount amount={order.price} variant='hero' />
                )}
              </QyKeyValue>
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
              {order.kind === 'plan' && order.user_subscription_id > 0 && (
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
            </div>

            {/* 实物奖品单的地址由中奖者事后补填一次：不填，这张单永远发不出去。
                表单与转盘结果屏上那份是同一个组件；填过之后后端
                `address_missing` 变 false，这一块自然消失。 */}
            {needsAddress && (
              <QyMallPrizeAddressForm orderNo={order.order_no} />
            )}

            <Separator />

            <QyTimeline items={buildQyMallTimeline(order, order.events, t)} />
          </div>
        )}
      </QyPageBoundary>

      {order != null && (
        <QyConfirmDialog
          open={cancelOpen}
          onOpenChange={setCancelOpen}
          title={t('qy_ml_cancel_title')}
          description={t('qy_ml_cancel_desc', {
            amount: formatSdWithUnit(order.price, unit),
          })}
          confirmText={t('qy_ml_cancel_confirm')}
          isLoading={cancel.isPending}
          onConfirm={() => cancel.mutate()}
        />
      )}
    </QyResponsiveDialog>
  )
}
