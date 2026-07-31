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
import { createFileRoute, Outlet } from '@tanstack/react-router'

import { QyRouteError } from '@/features/qy/components/qy-route-error'
import { requireQyEnabled } from '@/features/qy/lib/guards'

/**
 * qy 工作区的布局路由。
 *
 * 全站唯一的"扩展启用"守卫落点：叶子页面一律不写守卫，
 * 登录由 `_authenticated/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证。
 */
export const Route = createFileRoute('/_authenticated/qy')({
  beforeLoad: ({ context }) => requireQyEnabled(context),
  component: Outlet,
  errorComponent: QyRouteError,
})
