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
import { queryOptions } from '@tanstack/react-query'

import { qyGet, qyPost } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyAffRelationPage,
  QyBindRelationResult,
  QyRelationScope,
  QyRelationSort,
  QyUnbindRelationResult,
} from './types'

export type QyRelationFilters = {
  p: number
  page_size: number
  scope: QyRelationScope
  sort: QyRelationSort
  /** 精确匹配，两侧都比。后端不做模糊搜索，见 `adminListRelations` 的说明。 */
  username?: string
  inviter_id?: string
  invitee_id?: string
}

export function qyAdminRelationsQuery(filters: QyRelationFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
    scope: filters.scope,
    sort: filters.sort,
  }
  if (filters.username != null && filters.username !== '') {
    query.username = filters.username
  }
  if (filters.inviter_id != null && filters.inviter_id !== '') {
    query.inviter_id = filters.inviter_id
  }
  if (filters.invitee_id != null && filters.invitee_id !== '') {
    query.invitee_id = filters.invitee_id
  }

  return queryOptions({
    queryKey: qyKeys.adminCommissionRelations(query),
    queryFn: () =>
      qyGet<QyAffRelationPage>('/admin/commission/relations', query),
  })
}

/**
 * 手工建立一条邀请关系。
 *
 * 写的是**主库的 `users.inviter_id`**（权威字段），扩展库快照随后补上。
 * 后端三道闸门：自邀请、已经绑过上线、任意长度的邀请环路。
 *
 * 刻意不带 `client_request_id`：这个动作天然幂等——后端用
 * `WHERE inviter_id = 0` 的 CAS 写入，重复提交第二次会直接撞
 * `qy_rel_already_bound`，不会把关系改指向别处。
 */
export function qyBindAffRelation(input: {
  invitee_id: number
  inviter_id: number
  reason: string
}) {
  return qyPost<QyBindRelationResult>('/admin/commission/relations/bind', input)
}

/**
 * 解除一条邀请关系。
 *
 * 语义是「**已产生的佣金全部保留，只是从此不再产生新的**」：计佣流水是只增不改的
 * 账本，删掉会让 Σ计佣 与 Σ结算 对不上，而已结算的部分早就变成了可提现余额、
 * 甚至可能已经提现走了。要收回已发放的佣金必须单独走「冲正」。
 */
export function qyUnbindAffRelation(input: {
  invitee_id: number
  reason: string
}) {
  return qyPost<QyUnbindRelationResult>(
    '/admin/commission/relations/unbind',
    input
  )
}
