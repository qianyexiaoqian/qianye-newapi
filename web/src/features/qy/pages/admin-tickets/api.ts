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
import { qyDelete, qyGet, qyPost } from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type {
  QyTicket,
  QyTicketPriority,
  QyTicketStatusBucket,
  QyTicketUpload,
} from '../tickets/types'

/** 管理端队列。空参数一律不传，让后端按默认口径（等得最久的在前）排。 */
export function listQyAdminTickets(params: {
  p: number
  page_size: number
  status?: string
  priority?: string
  user_id?: number
  keyword?: string
}): Promise<QyPage<QyTicket>> {
  return qyGet<QyPage<QyTicket>>('/admin/ticket', params)
}

export function getQyAdminTicketStats(): Promise<{
  buckets: QyTicketStatusBucket[]
}> {
  return qyGet<{ buckets: QyTicketStatusBucket[] }>('/admin/ticket/stats')
}

export function getQyAdminTicket(id: number): Promise<QyTicket> {
  return qyGet<QyTicket>(`/admin/ticket/${id}`)
}

/** `internal: true` 是内部备注：不下发给用户，也不改变工单状态。 */
export function replyQyAdminTicket(
  id: number,
  body: { body: string; attachment_refs: string[]; internal: boolean }
): Promise<QyTicket> {
  return qyPost<QyTicket>(`/admin/ticket/${id}/reply`, body)
}

/** 只接受 `closed` / `open`：中间状态由消息驱动，手工设置会与实际对话脱节。 */
export function setQyAdminTicketStatus(
  id: number,
  body: { status: 'closed' | 'open'; reason?: string }
): Promise<QyTicket> {
  return qyPost<QyTicket>(`/admin/ticket/${id}/status`, body)
}

export function setQyAdminTicketPriority(
  id: number,
  priority: QyTicketPriority
): Promise<QyTicket> {
  return qyPost<QyTicket>(`/admin/ticket/${id}/priority`, { priority })
}

/** `assignee_id: 0` 表示取消指派。 */
export function assignQyAdminTicket(
  id: number,
  assigneeId: number
): Promise<QyTicket> {
  return qyPost<QyTicket>(`/admin/ticket/${id}/assign`, {
    assignee_id: assigneeId,
  })
}

export function uploadQyAdminTicketImage(file: File): Promise<QyTicketUpload> {
  const form = new FormData()
  form.append('file', file)
  return qyPost<QyTicketUpload>('/admin/ticket/images', form)
}

/** 丢弃一张自己上传、尚未随回复提交的图片。理由见用户端同名函数。 */
export function discardQyAdminTicketImage(ref: string): Promise<void> {
  return qyDelete<void>(`/admin/ticket/images/${encodeURIComponent(ref)}`)
}
