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
import z from 'zod'

import { qyTabHash } from '@/features/qy/lib/pages'

/**
 * 旧路由 —— 本页已被收进 `/qy/admin/settlement`（「结算台」）的选择夹
 * （`QY_TAB_GROUPS`）。
 *
 * 保留成重定向而不是删掉：这个地址此前是侧栏「结算」组上的一行，运营的书签、
 * 浏览器历史、内部文档与工单里贴出去的链接都还指着它。目标 hash 由
 * `qyTabHash` 现算，与宿主页认标签用的是同一个函数 —— 不可能出现"跳过去了
 * 但选中的是另一张"。
 *
 * ⚠️ 这一条与另外两条重定向不同：它必须**把 `?inviter_id=` 转发过去**。
 * 「从佣金余额下钻到这个人的逐笔计佣」那一跳走的就是这个地址，而重定向
 * 默认不带 search —— 不转发的话，从旧书签/旧链接进来的下钻会安静地退化成
 * 一张不带筛选的全量表，没有任何报错。所以这条 stub 保留 `validateSearch`：
 * 不校验的话 `beforeLoad` 拿到的 `search` 是未经解析的，参数一样丢。
 *
 * `replace`：旧地址不该留在历史栈里，否则用户按返回键会被立刻再弹回来。
 */
const legacySearchSchema = z.object({
  inviter_id: z.string().optional().catch(''),
})

export const Route = createFileRoute(
  '/_authenticated/qy/admin/commission-records/'
)({
  validateSearch: legacySearchSchema,
  beforeLoad: ({ search }) => {
    throw redirect({
      to: '/qy/admin/settlement',
      hash: qyTabHash('/qy/admin/commission-records'),
      search,
      replace: true,
    })
  },
})
