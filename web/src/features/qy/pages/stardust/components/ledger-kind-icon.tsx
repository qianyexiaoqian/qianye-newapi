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
import {
  CircleHelp,
  Crown,
  Gift,
  Percent,
  Store,
  Ticket,
  Undo2,
  UserPlus,
  Users,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { qySdKindKey } from '../lib/display'

/**
 * 十三种 kind → 图标。同一族用同一个图形（三种一次性的邀请返都是「加人」，
 * 下线消费返是「一群人」—— 它是逐日持续的、来自整个下线群的那一份；抽奖的
 * 押注 / 派奖 / 退款分别是票 / 礼物 / 回退），列表扫一眼就能按形状归类。
 */
const KIND_ICON: Record<string, LucideIcon> = {
  consume_rebate: Percent,
  invite_consume: Users,
  invite_topup: UserPlus,
  invite_redeem: UserPlus,
  invite_register: UserPlus,
  plan_buyer: Crown,
  plan_inviter: Crown,
  lot_stake: Ticket,
  lot_prize: Gift,
  lot_refund: Undo2,
  mall_order: Store,
  mall_refund: Undo2,
  manual: Wrench,
}

/**
 * 流水「种类」那一格：一个图标，文字进 `aria-label`。
 *
 * 表格里每一行都写一遍「消费返 / 抽奖押注 / 商城兑换」，一页二十行就是二十个
 * 四字词 —— 而用户扫这一列要的只是"这一笔是哪一类"。图标按族归类，形状比字
 * 快；读屏与悬停提示拿到的仍是那句完整的人话（`title` + `aria-label`），
 * 未知 kind 退回原样字符串、问号图标，不白屏。
 */
export function QySdLedgerKindIcon(props: {
  kind: string
  className?: string
}) {
  const { t } = useTranslation()
  const Icon = KIND_ICON[props.kind] ?? CircleHelp
  const label = t(qySdKindKey(props.kind), { defaultValue: props.kind })
  return (
    <span
      role='img'
      aria-label={label}
      title={label}
      data-kind={props.kind}
      className={cn(
        'bg-muted text-foreground inline-flex size-7 items-center justify-center rounded-full',
        props.className
      )}
    >
      <Icon aria-hidden='true' className='size-3.5' />
    </span>
  )
}
