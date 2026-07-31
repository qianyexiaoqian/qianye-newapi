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
import { qyGet } from '../../lib/api'
import type { QyAdminAvailabilityStats } from './types'

/**
 * 管理端可用率总览。
 *
 * 与用户端矩阵的根本差别是**不做任何分组裁剪** —— 管理员必须看得到隐藏分组，
 * 以及 UsingGroup 解析失败留下的 `unknown` 行（它是 hook 或渠道配置出问题的信号）。
 */
export function getQyAdminAvailabilityStats(params: {
  hours: number
}): Promise<QyAdminAvailabilityStats> {
  return qyGet<QyAdminAvailabilityStats>('/admin/availability/stats', params)
}
