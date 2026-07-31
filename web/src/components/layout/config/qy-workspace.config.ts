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
import { getQyWorkspaceNavGroups } from '@/features/qy/nav'

import type { SidebarView } from '../types'

/**
 * `/qy/*` 的嵌套侧边栏视图。
 *
 * 进入推广与结算工作区后，根导航被这份视图替换，13 个子页面全部挂在这里，
 * 根侧边栏只保留一个入口。`parent.label` 传的是 i18n key，
 * `sidebar-view-header.tsx` 会对它调 `t()`。
 *
 * 分组内容与角色过滤全部由 `features/qy/nav.ts` 负责，本文件只做注册。
 */
export const QY_WORKSPACE_VIEW: SidebarView = {
  id: 'qy-workspace',
  pathPattern: /^\/qy(\/|$)/,
  parent: {
    to: '/dashboard/overview',
    label: 'qy_nav_back',
  },
  getNavGroups: getQyWorkspaceNavGroups,
}
