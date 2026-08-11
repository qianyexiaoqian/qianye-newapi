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

import { QyAdminCommissionRelations } from '@/features/qy/pages/admin-commission-relations'

// 与佣金余额页同一个理由挂在 commission-records 之下:三张表（逐笔计佣 /
// 按人汇总 / 邀请关系）是同一件事的三个视角,URL 层级把这层从属关系说出来。
//
// ⚠️ 侧栏入口在 `lib/pages.ts` 里（「结算 → AFF 关系」）。它一度**不在**那张表里,
// 于是整页只能靠佣金审核页上的按钮或手敲 URL 到达。补上之后由
// `features/qy/lib/__tests__/route-entry-guard.test.ts` 盯着:有路由无入口就变红。
//
// 叶子路由不写守卫:登录由 `_authenticated/route.tsx` 保证,扩展启用由
// `qy/route.tsx` 保证,管理员由 `qy/admin/route.tsx` 保证,各一处、零重复。
export const Route = createFileRoute(
  '/_authenticated/qy/admin/commission-records/relations/'
)({
  component: QyAdminCommissionRelations,
})
