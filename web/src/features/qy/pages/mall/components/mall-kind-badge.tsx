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
import { CreditCard, Package, Ticket } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import { qyMallKindKey } from '../lib/product'
import type { QyMallProductKind } from '../types'

/**
 * 商品形态徽章：图标 + 文字（套餐 / 兑换码 / 实物）。
 *
 * 抽奖的商品奖在转盘面、奖档表、结果屏、我的转动、我的参与五处都要说"中的是
 * 哪一种东西"——三种形态的兑现路径完全不同（套餐立即生效 / 码当场发放 / 实物
 * 要先填地址），而这五处的字都很小。图标让它不看字也分得开；文字仍在，那是
 * 读屏与复制用的那一份。未知形态没有图标、原样显示字符串，后端加新形态时前端
 * 不白屏。
 */
const KIND_ICON = {
  plan: CreditCard,
  code: Ticket,
  physical: Package,
} as const

export function QyMallKindBadge(props: {
  kind: QyMallProductKind
  className?: string
}) {
  const { t } = useTranslation()
  const Icon =
    props.kind in KIND_ICON
      ? KIND_ICON[props.kind as keyof typeof KIND_ICON]
      : null
  return (
    <Badge
      variant='outline'
      className={cn('gap-1', props.className)}
      data-qy-mall-kind={props.kind}
    >
      {Icon != null && <Icon aria-hidden='true' className='size-3' />}
      {t(qyMallKindKey(props.kind), { defaultValue: props.kind })}
    </Badge>
  )
}
