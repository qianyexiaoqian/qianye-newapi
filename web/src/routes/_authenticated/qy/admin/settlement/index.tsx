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
import { createFileRoute } from '@tanstack/react-router'
import z from 'zod'

import { QyAdminSettlementHub } from '@/features/qy/pages/admin-settlement/hub'

/**
 * `inviter_id` 是「从佣金余额下钻到这个人的逐笔计佣」那一跳的载体。
 *
 * 它此前挂在 `/qy/admin/commission-records` 上；那一页收进本页的选择夹之后，
 * 渲染它的路由变成了这一条，参数必须跟着搬 —— 留在旧路由上的话，重定向那一跳
 * 会把它送进一个**不认识这个参数**的路由，zod object 直接把它 strip 掉，
 * 下钻退化成"打开一张不带筛选的全量表"，而且没有任何报错。
 *
 * 走 URL 而不是路由 state：下钻之后运营会把这一屏发给别人（"你看 #412 这几笔"），
 * 而路由 state 复制出去就是一个不带筛选的空列表。`.catch('')` 让手改坏的地址
 * 退化成"不筛选"，而不是把整页打成错误边界。
 *
 * 标签本身不走 query 而走 hash（见 `features/qy/pages/lib/tabs.ts`）：选择夹
 * 只有一套机制，钱包那个宿主是上游路由、query 会被它的 `validateSearch` 抹掉。
 */
const settlementSearchSchema = z.object({
  inviter_id: z.string().optional().catch(''),
})

// 「结算台」选择夹的宿主：日消费明细 / 佣金审核 / 提现审核三张标签，
// 选中哪一张由 URL hash 决定（见 features/qy/pages/lib/tabs.ts）。
//
// ⚠️ 侧栏入口在 `lib/pages.ts`（「结算 → 结算台」）。本仓已经五次栽在
// 「页面写完了、路由建好了、页面表里那一行忘了加」——
// `features/qy/lib/__tests__/route-entry-guard.test.ts` 会因为缺那一行变红。
//
// 叶子路由不写守卫：登录由 `_authenticated/route.tsx` 保证，扩展启用由
// `qy/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证，各一处、零重复。
export const Route = createFileRoute('/_authenticated/qy/admin/settlement/')({
  validateSearch: settlementSearchSchema,
  component: QyAdminSettlementHub,
})
