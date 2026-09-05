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
import { Link } from '@tanstack/react-router'
import { TriangleAlert } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyStamp } from '../../../components/art/qy-stamp'
import { QyPayPasswordField } from '../../../components/qy-pay-password-field'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { useQyAfterMoneyChange } from '../../../hooks/use-qy-after-money-change'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { isQyError, qyErrorMessage } from '../../../lib/api'
import { formatSdWithUnit } from '../../../lib/format-sd'
import { qyTabTarget } from '../../../lib/pages'
import { qyKeys } from '../../../lib/query-keys'
import { qyRuneLength } from '../../lib/constants'
import { newQyRequestId } from '../../lib/request-id'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyStardustMeQuery } from '../../stardust/api'
import { createQyMallOrder } from '../api'
import { qyMallOrderStatusView, type QyMallOrderTarget } from '../lib/order'
import { qyMallKindKey } from '../lib/product'
import type { QyMallOrderReceipt } from '../types'

/**
 * 需要把支付密码那一格显示出来的四个 code。
 *
 * 比抽奖多两个（`not_set` / `locked`）：商城对兑换码与实物是**无条件**验密
 * （D-12），没设密码的用户第一次提交就会撞上 `qy_pay_pwd_not_set` —— 那一格
 * 自己会切换成「去设置」的引导，比一条 toast 有用得多。
 */
const PAY_PASSWORD_CODES = new Set([
  'qy_pay_pwd_required',
  'qy_pay_pwd_wrong',
  'qy_pay_pwd_not_set',
  'qy_pay_pwd_locked',
])

/** 与后端 `purchase.go` 的 maxAddressRunes / maxContactRunes 同值。 */
const MAX_ADDRESS_RUNES = 500
const MAX_CONTACT_RUNES = 128

/**
 * 下单弹窗。
 *
 * ## 幂等键在打开那一刻生成，且只生成一次
 *
 * `client_request_id` 是整条链路上唯一能把"同一次意图的两次请求"归并起来的
 * 东西：网络超时后的重试沿用同一个键，后端按 `<user_id>:<crid>` 唯一索引认出
 * 原单并重放（`replayed: true`），不会扣第二次。在 `mutationFn` 里生成会让每次
 * 重试都变成一次新的兑换。
 *
 * ## 先不带密码提交，后端说要才显示密码格
 *
 * 阈值与"要不要验密"只有后端知道（兑换码 / 实物无条件验、套餐可选，D-12）。
 * 所以不猜：第一次提交不带 `pay_password`，吃到 `qy_pay_pwd_*` 才把那一格
 * 显示出来 —— 那一次失败发生在验密阶段、事务之前，订单没有产生，同一个幂等键
 * 接着用。首次被要求输入时**不弹 toast**：那一格本身就是提示。
 *
 * ## 三种商品的「不可逆」要说成三句话
 *
 * 兑换码一经发放不退；实物发货前可取消全额退回；套餐由系统自动发放、失败
 * 自动退回。用同一句"确认后不可撤销"盖住三者，等于对实物用户撒谎。
 */
export function QyMallOrderDialog(props: {
  target: QyMallOrderTarget | null
  onClose: () => void
}) {
  const { target } = props
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()
  const afterMoneyChange = useQyAfterMoneyChange()
  const addressId = useId()
  const contactId = useId()

  const [requestId, setRequestId] = useState('')
  const [address, setAddress] = useState('')
  const [contact, setContact] = useState('')
  const [payPassword, setPayPassword] = useState('')
  const [needsPayPassword, setNeedsPayPassword] = useState(false)
  const [payPasswordBlocked, setPayPasswordBlocked] = useState(false)
  const [receipt, setReceipt] = useState<QyMallOrderReceipt | null>(null)

  // 每次**打开**重置一次：请求号在这一刻定死，后续重试沿用同一个。
  useEffect(() => {
    if (target == null) return
    setRequestId(newQyRequestId())
    setAddress('')
    setContact('')
    setPayPassword('')
    setNeedsPayPassword(false)
    setReceipt(null)
  }, [target])

  const product = target?.product ?? null
  const isPhysical = product?.kind === 'physical'
  const isPlan = product?.kind === 'plan'

  // 当前余额与售价并排：钱从这里扣，"够不够"是按下确认之前用户最想知道的事。
  // 不够时当场说，而不是让他提交后吃一个 qy_sd_insufficient —— 放行仍以后端为准。
  const meQuery = useQuery({
    ...qyStardustMeQuery(),
    enabled: target != null && receipt == null,
  })
  const balance = meQuery.data?.balance.available

  const mutation = useMutation({
    mutationFn: () => {
      if (product == null) throw new Error('no product')
      return createQyMallOrder({
        product_no: product.product_no,
        client_request_id: requestId,
        pay_password: needsPayPassword ? payPassword : undefined,
        address: isPhysical ? address.trim() : undefined,
        contact: isPhysical ? contact.trim() : undefined,
        expect_action: isPlan ? target?.expectAction : undefined,
        expect_superseded: isPlan ? target?.expectSuperseded : undefined,
      })
    },
    onSuccess: async (data) => {
      setReceipt(data)
      // 余额、商品的 my_count / sold、订单列表同时变了：全量失效。
      // 套餐商品还动了主库（用户分组 / 订阅），那一档要连顶栏余额一起刷。
      if (isPlan) {
        await afterMoneyChange()
        return
      }
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => {
      if (isQyError(error) && error.code != null) {
        if (PAY_PASSWORD_CODES.has(error.code)) {
          const firstAsk =
            error.code === 'qy_pay_pwd_required' && !needsPayPassword
          setNeedsPayPassword(true)
          if (firstAsk) return
        }
      }
      toast.error(qyErrorMessage(error, t))
    },
  })

  const addressLength = qyRuneLength(address.trim())
  const contactLength = qyRuneLength(contact.trim())
  const addressInvalid =
    isPhysical && (addressLength === 0 || addressLength > MAX_ADDRESS_RUNES)
  const contactInvalid =
    isPhysical && (contactLength === 0 || contactLength > MAX_CONTACT_RUNES)

  const canSubmit =
    product != null &&
    !mutation.isPending &&
    requestId !== '' &&
    !addressInvalid &&
    !contactInvalid &&
    (!needsPayPassword || (!payPasswordBlocked && payPassword.length > 0))

  let warnKey = 'qy_ml_order_warn_plan'
  if (product?.kind === 'code') warnKey = 'qy_ml_order_warn_code'
  if (product?.kind === 'physical') warnKey = 'qy_ml_order_warn_physical'

  return (
    <QyResponsiveDialog
      open={target != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={
        receipt == null ? t('qy_ml_order_title') : t('qy_ml_receipt_title')
      }
      description={product?.title}
      footer={
        receipt == null ? (
          <>
            <Button type='button' variant='outline' onClick={props.onClose}>
              {t('qy_common_cancel')}
            </Button>
            <Button
              type='button'
              disabled={!canSubmit}
              onClick={() => mutation.mutate()}
            >
              {t('qy_ml_order_submit')}
            </Button>
          </>
        ) : (
          <>
            {/* 走 qyTabTarget 而不是裸 `to='/qy/mall-orders'`：后者会先卸载整个
                宿主页去加载旧路由，再被 redirect 桩弹回来 —— 一次白闪加两张
                标签的查询全部重发，而目标只是切到隔壁那张标签。 */}
            <Button
              type='button'
              variant='outline'
              render={<Link {...qyTabTarget('/qy/mall-orders')} />}
            >
              {t('qy_nav_mall_orders')}
            </Button>
            <Button type='button' onClick={props.onClose}>
              {t('qy_common_close')}
            </Button>
          </>
        )
      }
    >
      {product != null && receipt == null && (
        <div className='space-y-4'>
          <div>
            <QyKeyValue label={t('qy_ml_kind')}>
              {t(qyMallKindKey(product.kind), { defaultValue: product.kind })}
            </QyKeyValue>
            <QyKeyValue label={t('qy_ml_price')}>
              <QySdAmount amount={product.price} variant='hero' />
            </QyKeyValue>
            {balance != null && (
              <QyKeyValue label={t('qy_sd_balance_current', { unit })}>
                <span className='inline-flex flex-wrap items-center gap-2'>
                  <QySdAmount amount={balance} />
                  {balance < product.price && (
                    <span className='text-destructive text-xs'>
                      {t('qy_sd_balance_short')}
                    </span>
                  )}
                </span>
              </QyKeyValue>
            )}
            {isPlan && target?.expectAction != null && (
              <QyKeyValue label={t('qy_ml_plan_expect')}>
                {t(`qy_ml_preview_${target.expectAction}`, {
                  defaultValue: target.expectAction,
                })}
              </QyKeyValue>
            )}
          </div>

          {isPhysical && (
            <div className='space-y-3'>
              <div className='space-y-1.5'>
                <Label htmlFor={addressId}>{t('qy_ml_address')}</Label>
                <Textarea
                  id={addressId}
                  rows={3}
                  value={address}
                  autoComplete='street-address'
                  placeholder={t('qy_ml_address_ph')}
                  aria-invalid={addressLength > MAX_ADDRESS_RUNES}
                  disabled={mutation.isPending}
                  onChange={(event) => setAddress(event.target.value)}
                />
                <p className='text-muted-foreground text-end text-xs tabular-nums'>
                  {t('qy_common_rune_counter', {
                    used: addressLength,
                    max: MAX_ADDRESS_RUNES,
                  })}
                </p>
              </div>
              <div className='space-y-1.5'>
                <Label htmlFor={contactId}>{t('qy_ml_contact')}</Label>
                <Input
                  id={contactId}
                  value={contact}
                  autoComplete='off'
                  placeholder={t('qy_ml_contact_ph')}
                  aria-invalid={contactLength > MAX_CONTACT_RUNES}
                  disabled={mutation.isPending}
                  onChange={(event) => setContact(event.target.value)}
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ml_address_hint')}
                </p>
              </div>
            </div>
          )}

          <Alert>
            <TriangleAlert />
            <AlertDescription>
              {t(warnKey, { amount: formatSdWithUnit(product.price, unit) })}
            </AlertDescription>
          </Alert>

          {needsPayPassword && (
            <QyPayPasswordField
              value={payPassword}
              onChange={setPayPassword}
              disabled={mutation.isPending}
              onBlockedChange={setPayPasswordBlocked}
            />
          )}
        </div>
      )}

      {product != null && receipt != null && (
        <QyMallReceipt receipt={receipt} />
      )}
    </QyResponsiveDialog>
  )
}

/**
 * 回执。
 *
 * `replayed` 必须说出来：它表示这一次是重放，钱只扣了一次 —— 用户重试之后
 * 看到两次"成功"，第一反应是"扣了两次"。
 */
function QyMallReceipt(props: { receipt: QyMallOrderReceipt }) {
  const { receipt } = props
  const { t } = useTranslation()
  const view = qyMallOrderStatusView(receipt.status, receipt.kind)

  let nextKey = 'qy_ml_receipt_plan_next'
  if (receipt.kind === 'code') nextKey = 'qy_ml_receipt_code_next'
  if (receipt.kind === 'physical') nextKey = 'qy_ml_receipt_physical_next'

  return (
    <div className='space-y-3'>
      {/* 图形回执：一枚戳记 + 数额，"成功"这件事先用图说，字只留单号与下一步。 */}
      <div className='qy-fx-rise flex items-center gap-4'>
        <QyStamp />
        <span className='flex min-w-0 flex-col leading-tight'>
          <span className='text-muted-foreground text-[11px]'>
            {t('qy_ml_price')}
          </span>
          <QySdAmount
            amount={receipt.price}
            variant='hero'
            className='text-2xl'
          />
        </span>
      </div>
      {receipt.replayed && (
        <Alert>
          <AlertDescription>{t('qy_ml_receipt_replayed')}</AlertDescription>
        </Alert>
      )}
      <div>
        <QyKeyValue label={t('qy_common_order_no')}>
          <span className='inline-flex items-center gap-1'>
            <span className='font-mono text-xs break-all'>
              {receipt.order_no}
            </span>
            <CopyButton
              value={receipt.order_no}
              className='size-6'
              iconClassName='size-3'
            />
          </span>
        </QyKeyValue>
        <QyKeyValue label={t('qy_common_status')}>
          <QyStatusBadge
            status={view.status}
            label={view.labelKey == null ? undefined : t(view.labelKey)}
          />
        </QyKeyValue>
      </div>
      <p className='text-muted-foreground text-sm'>{t(nextKey)}</p>
    </div>
  )
}
