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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'

import { QyMeterBar } from '../../../components/art/qy-meter-bar'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { formatQyTs } from '../../ops/format'
import {
  qyMallBuyBlock,
  qyMallKindKey,
  qyMallRemaining,
  qyMallSaleState,
} from '../lib/product'
import type { QyMallProduct } from '../types'
import { QyMallCover } from './mall-cover'

/**
 * 商品列表里的一张卡：封面为主体，价格与「不能买的原因」压在封面上。
 *
 * 一屏之内必须回答四个问题：这是什么（套餐 / 兑换码 / 实物）、要多少星屑、
 * 还有没有货、我还能不能买。少任何一个，用户都得点进去才知道，而列表的意义
 * 就是不用点进去。
 *
 * 「不能买」的原因直接写在封面角标上（售罄 / 限购已满 / 未开售 / 已结束），
 * 而不是一颗灰按钮：灰按钮回答不了"那我什么时候能买"。判定在 `lib/product.ts`，
 * 与详情弹窗共用同一份。剩余画成条：`3 件` 与 `3 / 100` 是两回事，条子把
 * "快没了"画出来，数字留在旁边。
 */
export function QyMallProductCard(props: {
  product: QyMallProduct
  nowSeconds: number
  onOpen: (productNo: string) => void
}) {
  const { product } = props
  const { t } = useTranslation()

  const block = qyMallBuyBlock(product, props.nowSeconds)
  const remaining = qyMallRemaining(product)
  const sale = qyMallSaleState(product, props.nowSeconds)
  // 条子的分母：兑换码类的 stock 本身就是剩余枚数，没有"初始"可比，按剩余画满。
  const stockTotal =
    product.kind === 'code' ? Math.max(product.stock, 1) : product.stock
  const remainingText =
    remaining == null
      ? t('qy_common_unlimited')
      : t('qy_ml_remaining_n', { count: remaining })

  return (
    <Card className='flex h-full flex-col overflow-hidden pt-0'>
      <div className='relative'>
        <QyMallCover product={product} />
        <div className='absolute inset-x-2 top-2 flex flex-wrap items-center justify-between gap-1'>
          <Badge variant='outline' className='bg-background/85'>
            {t(qyMallKindKey(product.kind), { defaultValue: product.kind })}
          </Badge>
          {block != null && (
            <Badge
              variant={block === 'limit' ? 'outline' : 'destructive'}
              className={block === 'limit' ? 'bg-background/85' : undefined}
            >
              {t(`qy_ml_block_${block}`)}
            </Badge>
          )}
        </div>
        {/* 价格徽标压在封面右下角：它是这张卡上最重要的数，不该排在第三行。 */}
        <span className='bg-background/85 absolute right-2 bottom-2 rounded-md border px-2 py-0.5'>
          <QySdAmount amount={product.price} variant='hero' />
        </span>
      </div>
      <CardHeader>
        <h3 className='truncate text-base font-medium' title={product.title}>
          {product.title}
        </h3>
      </CardHeader>

      <CardContent className='flex-1 space-y-2 text-sm'>
        <div className='space-y-1'>
          <div className='flex items-center justify-between gap-2 text-xs'>
            <span className='text-muted-foreground'>
              {t('qy_ml_remaining')}
            </span>
            <span className='tabular-nums'>{remainingText}</span>
          </div>
          {remaining != null && (
            <QyMeterBar
              value={remaining}
              max={stockTotal}
              label={`${t('qy_ml_remaining')} ${remainingText}`}
            />
          )}
        </div>
        {/* 限购与"我已经兑了几件"并排：允许多次兑换的商品里，用户最容易犯的错
            就是不记得自己已经买过几件而重复下单。不限购且没买过时什么都不写 ——
            列表里绝大多数卡都是这个状态，每张都挂同一句只会淹没真正有内容的。 */}
        {(product.per_user_limit > 0 || product.my_count > 0) && (
          <div className='flex items-center justify-between gap-2 text-xs'>
            <span className='text-muted-foreground'>{t('qy_ml_limit')}</span>
            <span className='tabular-nums'>
              {product.per_user_limit > 0
                ? t('qy_ml_limit_line', {
                    limit: product.per_user_limit,
                    mine: product.my_count,
                  })
                : t('qy_ml_my_count', { count: product.my_count })}
            </span>
          </div>
        )}
        {/* 售期只在它真的约束购买时出现：还没开售写开售时间，在售且有截止写
            截止时间，已结束由封面上的角标说。 */}
        {sale === 'upcoming' && (
          <div className='flex items-center justify-between gap-2 text-xs'>
            <span className='text-muted-foreground'>
              {t('qy_ml_sale_start_at')}
            </span>
            <span className='tabular-nums'>
              {formatQyTs(product.sale_start_at)}
            </span>
          </div>
        )}
        {sale === 'on_sale' && product.sale_end_at > 0 && (
          <div className='flex items-center justify-between gap-2 text-xs'>
            <span className='text-muted-foreground'>
              {t('qy_ml_sale_end_at')}
            </span>
            <span className='tabular-nums'>
              {formatQyTs(product.sale_end_at)}
            </span>
          </div>
        )}
        {product.description !== '' && (
          <p className='text-muted-foreground line-clamp-2 text-xs break-words'>
            {product.description}
          </p>
        )}
      </CardContent>

      <CardFooter className='flex items-center justify-end gap-2'>
        <Button
          type='button'
          size='sm'
          variant={block == null ? 'default' : 'outline'}
          onClick={() => props.onOpen(product.product_no)}
        >
          {t('qy_common_detail')}
        </Button>
      </CardFooter>
    </Card>
  )
}
