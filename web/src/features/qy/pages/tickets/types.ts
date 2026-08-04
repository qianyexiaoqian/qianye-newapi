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

/**
 * 工单的前后端契约，与 `qianye/modules/ticket/view.go` 一一对应。
 *
 * 用户端与管理端共用同一组类型：后端也是同一个 `ticketView`，只是按端裁剪字段
 * （用户端不下发管理员姓名、指派信息与内部备注）。分成两套类型会逼两个页面
 * 各写一份消息列表渲染，而它们要展示的东西是同一件事。
 */

/** 工单等级。顺序即前端选择器的顺序，与后端 `Priorities()` 一致。 */
export type QyTicketPriority = 'low' | 'normal' | 'high' | 'urgent'

/**
 * 工单状态。
 *
 * `open` 与 `user_replied` 都是"在等客服"，但后端刻意分开：前者是还没有人碰过
 * 的新单，后者是已经在对话中、对方在等回音。前端也必须分开显示，否则客服看不出
 * "今天有多少新问题进来"。
 *
 * 保留 `(string & {})` 是刻意的：后端日后新增状态时前端只应降级为中性徽章，
 * 绝不能因为拿到未知字符串而崩溃。
 */
export type QyTicketStatus =
  | 'open'
  | 'replied'
  | 'user_replied'
  | 'closed'
  | (string & {})

/** 一张图片附件。本体走鉴权接口取，`ref` 是唯一对外标识。 */
export type QyTicketAttachment = {
  ref: string
  mime_type: string
  size: number
}

/**
 * 一条消息。
 *
 * `body` 是 **Markdown 源码**，后端原样存原样回。渲染与转义全部发生在这里
 * （`components/ui/markdown` 的 marked + DOMPurify），后端不做任何 HTML 转换 ——
 * 两处各净化一次的话，两份白名单会各自漂移，而"两边都以为对方兜底"是 XSS
 * 最常见的来源。**不要**把 body 塞进 `dangerouslySetInnerHTML`。
 */
export type QyTicketMessage = {
  id: number
  author_type: 'user' | 'admin' | 'system' | (string & {})
  /** 用户端恒为空串：下发管理员真名等于把管理员账号名送给任何提单的人。 */
  author_name: string
  body: string
  /** 内部备注，只在管理端出现（用户端接口在 SQL 层就滤掉了）。 */
  internal: boolean
  attachments: QyTicketAttachment[]
  created_at: number
}

export type QyTicket = {
  ticket_no: string
  /** 管理端寻址用；用户端恒为 0，那边一律用 ticket_no 展示、用列表里的 id 取详情。 */
  id: number
  user_id: number
  username: string

  title: string
  priority: QyTicketPriority
  status: QyTicketStatus
  message_count: number

  assignee_id: number
  assignee_name: string

  last_reply_at: number
  first_replied_at: number
  closed_at: number
  closed_by: string
  created_at: number
  updated_at: number

  /** 列表接口恒为空数组（不是 null，后端已保证）；详情接口才有内容。 */
  messages: QyTicketMessage[]
}

/**
 * `/ticket/config` 的响应。
 *
 * 五项上限都要下发并显示：后端拦得住不代表用户知道为什么被拦。只在提交失败时
 * 才告诉他"已达上限"，等于让他把整篇正文白写一遍。
 */
export type QyTicketConfig = {
  priorities: QyTicketPriority[]
  title_max_runes: number
  body_max_runes: number
  max_open_per_user: number
  daily_max_count: number
  cooldown_seconds: number
  reply_cooldown_seconds: number
  max_messages_per_ticket: number
  auto_close_days: number
  /** 当前未关闭工单数，用于在按钮旁提示"还能开几张"。 */
  open_count: number

  image_enabled: boolean
  image_max_bytes: number
  image_max_per_message: number
  image_accept: string[]
}

/** 上传接口的响应。`ref` 拿去填进建单/回复请求的 `attachment_refs`。 */
export type QyTicketUpload = {
  ref: string
  mime_type: string
  size: number
  created_at: number
}

/** 管理端角标。空库时后端返回 `[]` 而不是 null。 */
export type QyTicketStatusBucket = {
  status: QyTicketStatus
  count: number
}
