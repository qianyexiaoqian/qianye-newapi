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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { BillingSettings } from '@/features/system-settings/billing'
import {
  BILLING_DEFAULT_SECTION,
  BILLING_SECTION_IDS,
  type BillingSectionId,
} from '@/features/system-settings/billing/section-registry.tsx'

/**
 * 已下线的 section id → 它的去处。
 *
 * `group-pricing` 是「分组定价」拆成三页之前那一整页的 id。管理员的书签、
 * 浏览器历史、工单与审计记录里都还留着这个地址，而下面的白名单对认不出来的
 * id 一律弹回默认页（「额度设置」）—— 那看起来像是「我记错地址了」，
 * 而不是「这一页搬了」。映射到 `model-groups`：原页面的主体（分组倍率表、
 * auto 顺序）在那里。
 *
 * 写在路由文件而不是 section 表里：那张表在 Fast Refresh 的口径下只该导出
 * 组件，而这条映射的唯一消费者就是下面这个 `beforeLoad`。
 */
const BILLING_SECTION_ALIASES: Readonly<Record<string, BillingSectionId>> = {
  'group-pricing': 'model-groups',
}

export const Route = createFileRoute(
  '/_authenticated/system-settings/billing/$section'
)({
  beforeLoad: ({ params }) => {
    const validSections = BILLING_SECTION_IDS as unknown as string[]
    if (validSections.includes(params.section)) return

    // 已下线的 id 先查一次映射表再谈默认页：直接弹回「额度设置」的话，一条
    // 存在书签里的旧地址看起来像是自己记错了，而不是「那一页搬了」。
    // `replace`：旧地址不该留在历史栈里，否则按返回键会被立刻再弹回来。
    const alias = BILLING_SECTION_ALIASES[params.section]
    throw redirect({
      to: '/system-settings/billing/$section',
      params: { section: alias ?? BILLING_DEFAULT_SECTION },
      replace: alias != null,
    })
  },
  component: BillingSettings,
})
