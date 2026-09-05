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

import { QyStardustHub } from '@/features/qy/pages/stardust/hub'

/**
 * 星屑：余额 / 流水 / 待结算三张标签的宿主（`QY_TAB_GROUPS`）。
 *
 * 侧栏入口在 `lib/pages.ts`（「星屑」组）。叶子路由不写守卫：登录由
 * `_authenticated/route.tsx` 保证，扩展启用由 `qy/route.tsx` 保证。
 */
export const Route = createFileRoute('/_authenticated/qy/stardust/')({
  component: QyStardustHub,
})
