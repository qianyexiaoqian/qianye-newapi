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

import { QyAdminCommissionUsersHub } from '@/features/qy/pages/admin-commission-users/hub'

// 「用户佣金」选择夹的宿主：用户总览 / AFF 关系 / 佣金余额三张标签，
// 选中哪一张由 URL hash 决定（见 features/qy/pages/lib/tabs.ts）。
//
// 挂在 commission-records 之下而不是平级新开一个 /qy/admin/commission-users：
// 佣金管理的几张表是同一件事的几个视角，URL 层级把这层从属关系说出来。
//
// ⚠️ 侧栏入口在 `lib/pages.ts`（「结算 → 用户佣金」）。本仓已经五次栽在
// 「页面写完了、路由建好了、页面表里那一行忘了加」——
// `features/qy/lib/__tests__/route-entry-guard.test.ts` 会因为缺那一行变红。
//
// 叶子路由不写守卫：登录由 `_authenticated/route.tsx` 保证，扩展启用由
// `qy/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证，各一处、零重复。
export const Route = createFileRoute(
  '/_authenticated/qy/admin/commission-records/users/'
)({
  component: QyAdminCommissionUsersHub,
})
