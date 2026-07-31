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

import { QyAdminTransferGroupRules } from '@/features/qy/pages/admin-transfer-group-rules'

// 叶子路由不写守卫：登录由 `_authenticated/route.tsx` 保证，扩展启用由
// `qy/route.tsx` 保证，管理员由 `qy/admin/route.tsx` 保证，各一处、零重复。
export const Route = createFileRoute(
  '/_authenticated/qy/admin/transfer-group-rules/'
)({
  component: QyAdminTransferGroupRules,
})
