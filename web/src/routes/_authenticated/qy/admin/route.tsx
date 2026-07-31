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

import { requireQyAdmin } from '@/features/qy/lib/guards'

/**
 * qy 管理区的布局路由。
 *
 * 全站唯一的管理员守卫落点 —— 6 个管理页零重复。前端守卫只是 UX，
 * 真正的鉴权在后端 `/api/qy/admin/*` 的 AdminAuth 中间件。
 */
export const Route = createFileRoute('/_authenticated/qy/admin')({
  beforeLoad: requireQyAdmin,
  component: Outlet,
})
