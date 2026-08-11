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

import { QyAdminCommissionRecords } from '@/features/qy/pages/admin-commission-records'

/**
 * `inviter_id` 是「从佣金余额下钻到这个人的逐笔计佣」那一跳的载体。
 *
 * 走 URL 而不是路由 state：下钻之后运营会把这一屏发给别人（"你看 #412 这几笔"），
 * 而路由 state 复制出去就是一个不带筛选的空列表。`.catch('')` 让手改坏的地址
 * 退化成"不筛选"，而不是把整页打成错误边界。
 */
const commissionRecordsSearchSchema = z.object({
  inviter_id: z.string().optional().catch(''),
})

// 叶子路由不写守卫：登录由 `_authenticated/route.tsx` 保证，扩展启用由
// `qy/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证，各一处、零重复。
export const Route = createFileRoute(
  '/_authenticated/qy/admin/commission-records/'
)({
  validateSearch: commissionRecordsSearchSchema,
  component: QyAdminCommissionRecords,
})
