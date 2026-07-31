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
import type { QyPage } from '../../lib/types'
import type { QyAuditLog } from './types'

export type QyAuditLogParams = {
  p: number
  page_size: number
  category?: string
  action?: string
  trace_no?: string
  actor_user_id?: number
  target_user_id?: number
  start_ts?: number
}

/** 审计流水只追加不修改，因此这里只有读接口。 */
export function listQyAuditLogs(
  params: QyAuditLogParams
): Promise<QyPage<QyAuditLog>> {
  return qyGet<QyPage<QyAuditLog>>('/admin/audit-logs', params)
}
