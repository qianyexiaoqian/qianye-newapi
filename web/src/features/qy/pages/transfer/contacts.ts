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
 * 划转联系人簿的 DTO 与请求封装。
 *
 * 字段与 `qianye/modules/transfer/contacts.go` 的 `contactView` 一一对应。
 *
 * ## 这个模块**不是**什么
 *
 * 联系人只做一件事：把收款人输入框填好。它不是信任凭据 —— 选中一个联系人
 * 之后，仍然要走完整的 `preview` → 二次确认 → 提交（支付密码、分组限制、
 * 单笔与日限额、冷却）这条链路，一步都不少。
 *
 * 所以这里**刻意没有**「用联系人直接发起划转」的接口，也没有任何会被
 * 提交请求体读到的字段。前端能从联系人拿到的只有一个 `user_id`，
 * 而那正是用户本来就要手动输入的那个值。后端侧的同一条约束由
 * `contacts_isolation_test.go` 的双向 AST 断言钉死。
 */
import { queryOptions } from '@tanstack/react-query'

import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'

/**
 * 联系人对应账号的当前状态，由后端回读主库得出。
 *
 * `gone` 与 `unknown` 是两件事，前端必须区别对待：前者是「对方账号确实没了」，
 * 后者是「这次没读到主库」。把 `unknown` 显示成「已注销」，用户看到的会是
 * 「我存的人全没了」。
 */
export type QyContactStatus =
  | 'active'
  | 'disabled'
  | 'gone'
  | 'unknown'
  | (string & {})

/** `GET /api/qy/transfer/contacts` 的单项。 */
export type QyContact = {
  id: number
  user_id: number
  /** 用户自己起的备注名，可能为空串。后端已过滤控制字符与双向覆盖。 */
  alias: string
  /** **已由后端脱敏**，如 `zh***ng`。前端只做展示。 */
  masked_username: string
  status: QyContactStatus
  created_at: number
}

export type QyContactList = {
  items: QyContact[]
  /** 可保存的条数上限，用于渲染「还能加几个」。真正的判定在后端。 */
  max: number
}

/** `POST /api/qy/transfer/contacts` 的请求体。 */
export type QyAddContactRequest = {
  /**
   * 与 `/transfer/preview` 完全相同的字段语义：用户 ID，或（当
   * `recipient_lookup === 'id_or_email'` 时）完整邮箱。
   *
   * 传 identifier 而不是 user_id 是后端的要求：解析那一步挂着反枚举日志、
   * 查找方式开关与按用户限流，绕过它等于把三道防线一起绕过。
   */
  identifier: string
  alias: string
}

/**
 * 联系人列表。
 *
 * `staleTime: 0`：对方可能刚被封禁或注销，缓存住会让用户对着一个
 * 「正常」的条目一路填到确认弹窗才被拒。
 */
export function qyTransferContactsQuery() {
  return queryOptions({
    queryKey: qyKeys.transferContacts(),
    queryFn: () => qyGet<QyContactList>('/transfer/contacts'),
    staleTime: 0,
  })
}

export function qyAddContact(body: QyAddContactRequest) {
  return qyPost<QyContact>('/transfer/contacts', body)
}

export function qyRenameContact(args: { id: number; alias: string }) {
  return qyPut<{ id: number }>(`/transfer/contacts/${args.id}`, {
    alias: args.alias,
  })
}

export function qyDeleteContact(id: number) {
  return qyDelete<{ id: number }>(`/transfer/contacts/${id}`)
}

/** 备注名长度上限，与后端 `maxAliasRunes` 对齐。 */
export const QY_CONTACT_ALIAS_MAX_RUNES = 32

/**
 * 在**自己的**联系人簿里按关键字筛选。
 *
 * ## 为什么这一步只能在前端做
 *
 * 项目方要「联系人的添加提供用户名搜索」。用户名模糊搜索**全站用户**是一个
 * 用户枚举面：`qianye/modules/transfer/lookup.go` 顶部刻意写死了只有
 * `id` / `email` 两档查找方式，理由是"模糊搜索等于把整个用户表开放给任何登录
 * 用户枚举"。所以本函数搜的是调用者自己已经存下的那几十条记录 ——
 * 那些数据本来就在他手上，翻多少遍都不产生新信息。
 *
 * 真正的"按用户名找到一个陌生人"需要一个新的后端接口，那是一次安全决策而不是
 * 一次 UI 调整；接口设计（限流、脱敏、最短前缀、审计）写在交付报告里，由集成者
 * 决定是否加。在它落地之前，**添加**联系人仍然只接受 ID / 邮箱，与
 * `/transfer/preview` 同一套口径、同一张反枚举日志、同一个限流器。
 *
 * 匹配 masked_username 是安全的：那是后端已经脱敏过的串（`zh***ng`），
 * 前端不持有、也不需要真实用户名。
 */
export function qyFilterContacts(
  items: readonly QyContact[],
  keyword: string
): QyContact[] {
  const needle = keyword.trim().toLowerCase()
  if (needle === '') return [...items]
  return items.filter((contact) => {
    if (contact.alias.toLowerCase().includes(needle)) return true
    if (contact.masked_username.toLowerCase().includes(needle)) return true
    return String(contact.user_id).includes(needle)
  })
}

/** 状态 → i18n key。未知取值回落到 `unknown`，绝不显示成「正常」。 */
export function qyContactStatusKey(status: QyContactStatus): string {
  switch (status) {
    case 'active':
      return 'qy_tr_ct_status_active'
    case 'disabled':
      return 'qy_tr_ct_status_disabled'
    case 'gone':
      return 'qy_tr_ct_status_gone'
    default:
      return 'qy_tr_ct_status_unknown'
  }
}
