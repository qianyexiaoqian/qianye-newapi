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
import { ChevronRight } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

/**
 * 抽奖竞猜用户端的「细则」折叠位。
 *
 * ## 它解决的问题
 *
 * 这一整套页面的说明文字是历轮为了正确性一条条加进去的：公正性协议、退款口径、
 * 承诺哈希、截断偏差、摊薄规则……每一条当初都有理由，但它们**同时铺在一屏上**
 * 的后果是没人读 —— 项目方原话「你是用户你是否有耐心看完这一大堆字」。
 *
 * 所以口径改成：**决定"要不要花这笔钱"的话留在明面上，解释"它为什么可信"的话
 * 折起来。** 折叠不是隐藏：触发器自己带一句话说明里面是什么，展开一次即可读到
 * 与改造前逐字相同的内容。
 *
 * ## 为什么用 Collapsible 而不是 `hidden` + CSS
 *
 * Base UI 的 `Collapsible.Panel` 默认在收起时**不挂载**。这一条正是可测性的
 * 全部来源：一屏的字数可以直接从 `textContent` 上量，收起的段落不会偷偷计入，
 * 也不会被屏幕阅读器与页内搜索读到。用 CSS 隐藏则三者全都不成立。
 */
export function QyLotFinePrint(props: {
  /** 触发器上的那一句。缺省是通用的「说明」。 */
  label?: string
  children: ReactNode
  className?: string
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)

  return (
    <Collapsible open={open} onOpenChange={setOpen} className={props.className}>
      <CollapsibleTrigger className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs'>
        <ChevronRight
          aria-hidden='true'
          className={cn('size-3 transition-transform', open && 'rotate-90')}
        />
        {props.label ?? t('qy_lot_fine_print')}
      </CollapsibleTrigger>
      <CollapsibleContent className='text-muted-foreground mt-1.5 space-y-1.5 text-xs leading-5'>
        {props.children}
      </CollapsibleContent>
    </Collapsible>
  )
}
