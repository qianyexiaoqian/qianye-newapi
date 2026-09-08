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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import { QyTierMedal } from '../../../components/art/qy-tier-medal'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyMallKindBadge } from '../../mall/components/mall-kind-badge'
import { isQyLotProductPrize, isQyLotTextPrize, type QyLotTier } from '../types'

/**
 * 奖档清单的一行 —— 抽奖 / 双色球 / 转盘三张清单共用的形状。
 *
 * ## 为什么不再是一张表
 *
 * 三张清单此前都是 `StaticDataTable`：抽奖 4 列、双色球 5 列、转盘 4 列。
 * 表格把每一格摆成同等重量的一个单元，而这份清单里各格的权重差得很远 ——
 * 用户扫的是「第几档 → 能拿多少」，命中门槛与份数是他决定之后才会去核的。
 * 五列一字排开的结果是：真正要看的那个数与要核的那些数长得一模一样，
 * 而窄屏上五列会把整块内容区顶到横向滚动（详情页为此在两侧写了 `min-w-0`
 * 才勉强把滚动关回表格自己那一层）。
 *
 * 换成"一档一行"之后：档位是一枚圆牌（有序、一个字符宽）、奖品名与副行左对齐、
 * 能拿多少右对齐加重、要核的量降到脚注一行。窄屏下它自然折行，不再有横向滚动。
 *
 * ## 这不是"删字"
 *
 * 每一格的原文都还在，只是换了位置与字号 —— 命中门槛、预算份数、中奖概率
 * 都进了 `meta` 那一行。`lottery/__tests__/text-budget.test.tsx` 按原文逐条
 * 盯着它们还在不在。
 */
export function QyLotTierRow(props: {
  /** 档位号。`0` 画空心牌 —— 转盘的「谢谢参与」没有档位。 */
  tier: number
  /** 与盘面扇区对齐的颜色。只有转盘给。 */
  medalColor?: string
  name: ReactNode
  /** 奖档名后面的徽章（奖品形态 / 兜底选项 / 获胜）。 */
  badge?: ReactNode
  /** 名字下面那一行（命中门槛、商品摘要、文本奖说明）。 */
  sub?: ReactNode
  /** 右侧那一格：能拿多少。这一行里唯一加重的量。 */
  value: ReactNode
  /**
   * 右下角的脚注。空串与 `null` 会被丢掉 —— 调用方常常有一两项是条件性的，
   * 让它传空比让每个调用方各自拼一次数组干净。
   *
   * 收 `ReactNode` 而不是 `string`：转盘那份清单里，复算库存与公布库存对不上
   * 的那一项要自己变成警示色。收窄成字符串会逼着调用方把这个信号丢掉，或者
   * 在别处再开一个格子放它。
   */
  meta?: ReactNode[]
  className?: string
}) {
  const shown = (props.meta ?? []).filter(
    (item) => item !== '' && item != null && item !== false
  )

  return (
    <li className={cn('flex items-start gap-2.5 py-2.5', props.className)}>
      <QyTierMedal
        tier={props.tier}
        color={props.medalColor}
        className='mt-0.5'
      />
      <span className='min-w-0 flex-1'>
        <span className='flex flex-wrap items-center gap-1.5 text-sm'>
          <span className='break-words'>{props.name}</span>
          {props.badge}
        </span>
        {props.sub != null && (
          <span className='text-muted-foreground mt-0.5 block text-xs'>
            {props.sub}
          </span>
        )}
      </span>
      <span className='flex shrink-0 flex-col items-end gap-0.5 text-right'>
        {props.value}
        {shown.length > 0 && (
          <span className='text-muted-foreground flex flex-wrap justify-end gap-x-1.5 text-[11px] tabular-nums'>
            {shown.map((item, index) => (
              // 脚注项之间的分隔点。索引作 key 是安全的：这一串由调用方在同一次
              // 渲染里按固定顺序拼出来，不会重排，也没有任何一项持有状态。
              // eslint-disable-next-line react/no-array-index-key
              <span key={index} className='inline-flex items-center gap-1.5'>
                {index > 0 && <span aria-hidden='true'>·</span>}
                {item}
              </span>
            ))}
          </span>
        )}
      </span>
    </li>
  )
}

/**
 * 一档奖的「奖品」那一格：星屑金额 / 文本奖说明 / 商品摘要。
 *
 * 抽奖与转盘两张清单逐字共用这三种形态 —— 它们读的是同一个 `prize_type`，
 * 由同一段后端逻辑写入。分别实现一次的代价是：某一天给商品奖加一枚形态徽章，
 * 只会加在两处中的一处，而两张清单上的同一件商品从此长得不一样。
 */
export function QyLotPrizeValue(props: { tier: QyLotTier }) {
  const { tier } = props

  // 文本奖的 `amount_quota` 恒为 0。摆一个 0 出来会让人以为这一档是空的 ——
  // 它的价值全在那段公开说明里，所以这一格直接印说明。
  if (isQyLotTextPrize(tier)) {
    return (
      <span className='text-sm break-words whitespace-pre-wrap'>
        {tier.text_desc}
      </span>
    )
  }
  // 商品奖同样 0 星屑：这一格说的是"哪件商品、什么形态"。商品名与形态是按
  // 商品号从商城现读的摘要，不进承诺；进承诺的是 product_no，验证脚本比对
  // 的也是它。
  if (isQyLotProductPrize(tier)) {
    return (
      <span className='inline-flex flex-wrap items-center justify-end gap-1.5 text-sm'>
        <QyMallKindBadge kind={tier.product_kind ?? ''} />
        <span className='break-words'>
          {tier.product_title || tier.product_no}
        </span>
      </span>
    )
  }
  return <QySdAmount amount={tier.amount_quota} />
}

/** 奖品形态徽章（文本奖 / 商品奖）。星屑奖不挂 —— 它是默认形态。 */
export function QyLotPrizeTypeBadge(props: { tier: QyLotTier }) {
  const { t } = useTranslation()

  if (isQyLotTextPrize(props.tier)) {
    return <Badge variant='outline'>{t('qy_lot_prize_type_text')}</Badge>
  }
  if (isQyLotProductPrize(props.tier)) {
    return <Badge variant='outline'>{t('qy_lot_prize_type_product')}</Badge>
  }
  return null
}
