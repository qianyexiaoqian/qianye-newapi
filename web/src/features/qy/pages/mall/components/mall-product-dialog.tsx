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
import { useQuery } from '@tanstack/react-query'
import { TriangleAlert } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyMallProductQuery } from '../api'
import type { QyMallOrderTarget } from '../lib/order'
import { qyMallBuyBlock, qyMallKindKey, qyMallRemaining } from '../lib/product'
import type { QyMallPlanBrief, QyMallProductDetail } from '../types'
import { QyMallCover } from './mall-cover'

/**
 * 商品详情。
 *
 * ## 套餐商品的「预览」为什么必须在这里、而且必须被确认
 *
 * 买套餐会动主库（发订阅、可能改分组）。后端在详情里下发 `preview`：这一单会
 * 新开 / 续期 / **顶替**现有分组 / 被拒绝。顶替意味着用户手里某个分组的剩余
 * 时间作废且不退 —— 2026-08-14 拍板「跨组顶替要用户确认」。所以 `supersede`
 * 那一档在按下购买之后先弹强制勾选的二次确认，确认过的动作与分组集合随下单
 * 请求原样回传（`expect_*`），主库事务里复核：不一致 → `qy_ml_plan_state_changed`。
 *
 * `reject` 直接禁用购买并把 `reason` 摆出来：用户看到的是"为什么不能买"，
 * 而不是一颗灰按钮。
 *
 * ## 为什么点购买会关掉这个弹窗
 *
 * 下单弹窗由列表页持有、与本弹窗并列渲染（见 `index.tsx`），而不是叠在这个
 * 弹窗上面：两层模态叠着开，焦点与滚动锁定在移动端抽屉形态下并不可靠，
 * 而下单弹窗本来就会把商品名与价格再复述一遍。
 */
export function QyMallProductDialog(props: {
  productNo: string | null
  onClose: () => void
  onBuy: (target: QyMallOrderTarget) => void
}) {
  const { t } = useTranslation()
  const productNo = props.productNo ?? ''
  const query = useQuery(qyMallProductQuery(productNo))
  const product = query.data
  const [supersedeOpen, setSupersedeOpen] = useState(false)

  // 详情弹窗打开的那一刻的时钟就够用：售期以秒计，弹窗里不需要倒计时。
  const nowSeconds = Math.floor(Date.now() / 1000)
  const block = product == null ? null : qyMallBuyBlock(product, nowSeconds)
  const preview = product?.preview ?? null
  const rejected = preview?.action === 'reject'
  const canBuy = product != null && block == null && !rejected

  const startBuy = () => {
    if (product == null) return
    if (preview?.action === 'supersede') {
      setSupersedeOpen(true)
      return
    }
    props.onBuy({
      product,
      expectAction: preview?.action,
      expectSuperseded: preview?.superseded_groups,
    })
  }

  return (
    <QyResponsiveDialog
      open={props.productNo != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={product?.title ?? t('qy_ml_product_detail')}
      description={
        product == null
          ? undefined
          : t(qyMallKindKey(product.kind), { defaultValue: product.kind })
      }
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_close')}
          </Button>
          <Button type='button' disabled={!canBuy} onClick={startBuy}>
            {/* 按钮上直接写原因：灰掉的「立即兑换」回答不了"那我什么时候能买"。 */}
            {block == null ? t('qy_ml_buy') : t(`qy_ml_block_${block}`)}
          </Button>
        </>
      }
    >
      <QyPageBoundary query={query}>
        {product != null && (
          <div className='space-y-4'>
            <QyMallCover product={product} variant='hero' />

            {block != null && (
              <Badge variant={block === 'limit' ? 'outline' : 'destructive'}>
                {t(`qy_ml_block_${block}`)}
              </Badge>
            )}

            <div>
              <QyKeyValue label={t('qy_ml_price')}>
                <QySdAmount amount={product.price} variant='hero' />
              </QyKeyValue>
              <QyKeyValue label={t('qy_ml_remaining')}>
                {qyMallRemaining(product) == null
                  ? t('qy_common_unlimited')
                  : t('qy_ml_remaining_n', {
                      count: qyMallRemaining(product) ?? 0,
                    })}
              </QyKeyValue>
              <QyKeyValue label={t('qy_ml_limit')}>
                {product.per_user_limit > 0
                  ? t('qy_ml_limit_line', {
                      limit: product.per_user_limit,
                      mine: product.my_count,
                    })
                  : t('qy_common_unlimited')}
              </QyKeyValue>
              {product.sale_start_at > 0 && (
                <QyKeyValue label={t('qy_ml_sale_start_at')}>
                  {formatQyTs(product.sale_start_at)}
                </QyKeyValue>
              )}
              {product.sale_end_at > 0 && (
                <QyKeyValue label={t('qy_ml_sale_end_at')}>
                  {formatQyTs(product.sale_end_at)}
                </QyKeyValue>
              )}
            </div>

            {product.description !== '' && (
              <p className='text-sm break-words whitespace-pre-wrap'>
                {product.description}
              </p>
            )}

            {product.kind === 'plan' && (
              <QyMallPlanSection product={product} plan={product.plan} />
            )}
          </div>
        )}
      </QyPageBoundary>

      {product != null && preview != null && (
        <QyConfirmDialog
          open={supersedeOpen}
          onOpenChange={setSupersedeOpen}
          title={t('qy_ml_supersede_title')}
          description={t('qy_ml_supersede_desc', {
            groups: preview.superseded_groups.join(' / '),
          })}
          details={
            <ul className='list-disc space-y-1 ps-5 text-sm'>
              {preview.superseded_groups.map((group) => (
                <li key={group} className='font-mono'>
                  {group}
                </li>
              ))}
            </ul>
          }
          irreversible
          irreversibleDesc={t('qy_ml_supersede_ack', {
            groups: preview.superseded_groups.join(' / '),
          })}
          confirmText={t('qy_ml_supersede_confirm')}
          onConfirm={() => {
            setSupersedeOpen(false)
            props.onBuy({
              product,
              expectAction: preview.action,
              expectSuperseded: preview.superseded_groups,
            })
          }}
        />
      )}
    </QyResponsiveDialog>
  )
}

/**
 * 套餐商品专属的那一段：套餐本身是什么，以及这一单会对我的账号做什么。
 *
 * `preview` 四种结论各说各的，而且 `reason` 原样显示（它是后端按当前账号
 * 算出来的具体理由：名额满了 / 已达购买上限 / 套餐停售）。
 */
function QyMallPlanSection(props: {
  product: QyMallProductDetail
  plan: QyMallPlanBrief | null
}) {
  const { t } = useTranslation()
  const { plan } = props
  const preview = props.product.preview

  return (
    <div className='space-y-3'>
      {plan != null && (
        <div className='rounded-lg border p-3'>
          <QyKeyValue label={t('qy_ml_plan_title')}>{plan.title}</QyKeyValue>
          <QyKeyValue label={t('qy_ml_plan_duration_label')}>
            {plan.duration_unit === 'permanent'
              ? t('qy_ml_plan_permanent')
              : t('qy_ml_plan_duration', {
                  value: plan.duration_value,
                  unit: t(`qy_ml_duration_${plan.duration_unit}`, {
                    defaultValue: plan.duration_unit,
                  }),
                })}
          </QyKeyValue>
          {plan.upgrade_group !== '' && (
            <QyKeyValue label={t('qy_ml_plan_group')}>
              <span className='font-mono'>{plan.upgrade_group}</span>
            </QyKeyValue>
          )}
          {plan.no_quota && (
            <QyKeyValue label={t('qy_ml_plan_no_quota')}>
              {t('qy_ml_plan_no_quota_desc')}
            </QyKeyValue>
          )}
        </div>
      )}

      {preview != null && (
        <Alert
          variant={preview.action === 'reject' ? 'destructive' : 'default'}
        >
          <TriangleAlert />
          <AlertTitle>
            {t(`qy_ml_preview_${preview.action}`, {
              defaultValue: preview.action,
            })}
          </AlertTitle>
          <AlertDescription className='space-y-1'>
            {preview.reason !== '' && <span>{preview.reason}</span>}
            {preview.action === 'supersede' &&
              preview.superseded_groups.length > 0 && (
                <span>
                  {t('qy_ml_preview_supersede_groups', {
                    groups: preview.superseded_groups.join(' / '),
                  })}
                </span>
              )}
            {!preview.seat_available && (
              <span>{t('qy_ml_preview_seat_full')}</span>
            )}
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
