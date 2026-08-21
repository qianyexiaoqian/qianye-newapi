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
 * `replace`：旧地址不该留在历史栈里，否则用户按返回键会被立刻再弹回来。
 */
export const Route = createFileRoute('/_authenticated/qy/admin/withdrawals/')({
  beforeLoad: () => {
    throw redirect({
      to: '/qy/admin/settlement',
      hash: qyTabHash('/qy/admin/withdrawals'),
      replace: true,
    })
  },
})
