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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { qyTabHash } from '@/features/qy/lib/pages'

/**
 * 我的订单 —— `/qy/mall` 选择夹里的一张标签，没有独立入口。
 * 形状与 `stardust-ledger` 的重定向桩完全相同，理由见那里。
 */
export const Route = createFileRoute('/_authenticated/qy/mall-orders/')({
  beforeLoad: () => {
    throw redirect({
      to: '/qy/mall',
      hash: qyTabHash('/qy/mall-orders'),
      replace: true,
    })
  },
})
