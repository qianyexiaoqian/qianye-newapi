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
import { api } from '@/lib/api'

import {
  QY_API_PREFIX,
  qyDelete,
  qyErrorFromBlobFailure,
  qyGet,
  qyPost,
} from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type {
  QyTicket,
  QyTicketConfig,
  QyTicketPriority,
  QyTicketUpload,
} from './types'

export function getQyTicketConfig(): Promise<QyTicketConfig> {
  return qyGet<QyTicketConfig>('/ticket/config')
}

export function listQyTickets(params: {
  p: number
  page_size: number
  status?: string
}): Promise<QyPage<QyTicket>> {
  return qyGet<QyPage<QyTicket>>('/ticket/list', params)
}

/**
 * 取一张工单的详情。
 *
 * 寻址用**业务单号**而不是自增 id：用户端视图刻意不下发 id（下发等于把主键
 * 空间交给客户端），列表里那一行的 `id` 恒为 0。曾经这里传的就是那个 0，
 * 结果是详情 / 追加回复 / 关单三条路一起 400。
 */
export function getQyTicket(ticketNo: string): Promise<QyTicket> {
  return qyGet<QyTicket>(`/ticket/${encodeURIComponent(ticketNo)}`)
}

/** 新建工单。`attachment_refs` 来自先行上传，见 {@link uploadQyTicketImage}。 */
export function createQyTicket(body: {
  title: string
  body: string
  priority: QyTicketPriority
  attachment_refs: string[]
}): Promise<QyTicket> {
  return qyPost<QyTicket>('/ticket', body)
}

export function replyQyTicket(
  ticketNo: string,
  body: { body: string; attachment_refs: string[] }
): Promise<QyTicket> {
  return qyPost<QyTicket>(`/ticket/${encodeURIComponent(ticketNo)}/reply`, body)
}

export function closeQyTicket(ticketNo: string): Promise<QyTicket> {
  return qyPost<QyTicket>(`/ticket/${encodeURIComponent(ticketNo)}/close`, {})
}

/**
 * 上传一张图片，拿到可以填进 `attachment_refs` 的标识。
 *
 * 两步式（先传图拿 ref、再提交正文）是后端要求的：提交那一步跑在一个要写三张表
 * 的事务里，把几 MiB 的上行塞进去等于让事务持有时间跟着用户的带宽走。
 */
export function uploadQyTicketImage(file: File): Promise<QyTicketUpload> {
  const form = new FormData()
  form.append('file', file)
  return qyPost<QyTicketUpload>('/ticket/images', form)
}

/**
 * 丢弃一张**已上传但还没随消息提交**的图片，把后端的未提交上传名额还回去。
 *
 * 没有它，用户在弹窗里移除一张图只会删掉本地那一项，服务端那条行要等 24 小时
 * 孤儿清理才消失 —— 两次"选了又不发"之后他就再也传不了图了。
 */
export function discardQyTicketImage(ref: string): Promise<void> {
  return qyDelete<void>(`/ticket/images/${encodeURIComponent(ref)}`)
}

/**
 * 取回一张工单图片的本体。
 *
 * **不能用 `<img src="/api/qy/ticket/images/xxx">`**：那条接口要 Bearer 头，
 * 浏览器给 `img` 发的请求不带它，结果是一个永远加载失败的破图。所以走 axios
 * 取 Blob，再由调用方 `createObjectURL`（并在卸载时 revoke）。
 *
 * 不能用 `qyGet`：那条路会把响应体当 `{success,data}` 信封解，而这里的成功响应
 * 就是二进制图片本身。失败时 axios 给回的 `response.data` 也是 Blob
 * （responseType 对错误响应一视同仁），所以错误还原走 `qyErrorFromBlobFailure`，
 * 否则 410 `qy_tk_image_purged`（"已按保留期清理"）会被糊成"请求参数不合法"。
 */
export async function fetchQyTicketImageBlob(
  ref: string,
  scope: 'user' | 'admin' = 'user'
): Promise<Blob> {
  const base = scope === 'admin' ? '/admin/ticket/images' : '/ticket/images'
  try {
    const res = await api.get(`${QY_API_PREFIX}${base}/${ref}`, {
      skipErrorHandler: true,
      skipBusinessError: true,
      responseType: 'blob',
      // 上游的在途 GET 去重只按 url + params 归并，认不出 responseType 的差异。
      disableDuplicate: true,
    })
    return res.data as Blob
  } catch (error) {
    throw await qyErrorFromBlobFailure(error)
  }
}
