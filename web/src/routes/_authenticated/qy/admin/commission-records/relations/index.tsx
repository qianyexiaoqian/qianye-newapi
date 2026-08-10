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

// 与佣金余额页同一个理由挂在 commission-records 之下:`lib/pages.ts` 的
// qy-settlement 分组已经占满 7 行,`__tests__/pages-table.test.ts` 的
// "分组规模不超过上游 admin 组"用例会因为第 8 行直接变红。作为子路径它同时
// 天然继承了 `page-meta.ts` 的最长前缀规则 —— Steins Gate 区段头会显示佣金审核的
// LAB MEMO 编号与日文副标,而不是掉进"未登记"分支显示 00。
//
// 叶子路由不写守卫:登录由 `_authenticated/route.tsx` 保证,扩展启用由
// `qy/route.tsx` 保证,管理员由 `qy/admin/route.tsx` 保证,各一处、零重复。
export const Route = createFileRoute(
  '/_authenticated/qy/admin/commission-records/relations/'
)({
  component: QyAdminCommissionRelations,
})
