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
import type { TFunction } from 'i18next'

import { isQyError, qyErrorMessage, qyGet, qyPost } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyBindRelationResult,
  QyBlockRelationResult,
  QyInviteAccrualPage,
  QyInviteRelationPage,
  QyRebindRelationResult,
  QyRelationScope,
  QyRelationSort,
  QyUnbindRelationResult,
} from './types'

/**
 * 邀请管理（管理端）取数。路径与契约「管理端接口」逐字一致（D-14：从
 * `/admin/commission/*` 搬到 `/admin/invite/*`，形状照旧）；后端 Y1 落地之前它们在
 * `route-contract.test.ts` 的 `MISS_EXEMPT` 里，落地之后请把那一条豁免删掉。
 */

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
    queryKey: qyKeys.adminInviteRelations(query),
    queryFn: () =>
      qyGet<QyInviteRelationPage>('/admin/invite/relations', query),
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
export function qyBindInviteRelation(input: {
  invitee_id: number
  inviter_id: number
  reason: string
}) {
  return qyPost<QyBindRelationResult>('/admin/invite/relations/bind', input)
}

/**
 * 解除一条邀请关系。
 *
 * 语义是「**已经返出去的星屑不收回，只是从此不再产生新的**」：星屑账本是
 * 只增不改的，已发放的那几笔早就变成了下线上线名下的可用余额。
 */
export function qyUnbindInviteRelation(input: {
  invitee_id: number
  reason: string
}) {
  return qyPost<QyUnbindRelationResult>('/admin/invite/relations/unbind', input)
}

/**
 * 换绑：把这个下线的上线从旧的改成新的。
 *
 * ── 为什么必须是后端的一个端点，而不是前端"先解绑再绑" ──
 * 后者是两次请求：第二次失败时这个人会停在**没有上线**的中间态，而运营看到的
 * 只是一句"操作失败"，他会以为什么都没变。后端 `adminRebindRelation` 在一个
 * 事务里改权威字段 + 更新快照，要么全成要么全不成。
 *
 * 换成他现在这个上线会被后端 400（`qy_rel_same_inviter`）而不是当空操作放行：
 * 空操作会写一条"换绑成功"的审计，而实际上什么都没发生。
 */
export function qyRebindInviteRelation(input: {
  invitee_id: number
  inviter_id: number
  reason: string
}) {
  return qyPost<QyRebindRelationResult>('/admin/invite/relations/rebind', input)
}

/**
 * 停止 / 恢复一条邀请关系的计返。只影响未来，已发放的星屑不动。
 *
 * 响应里的 `inviter_id` 是后端回显的这条关系的邀请人 —— 停掉之后运营最需要
 * 知道的是"我刚刚断掉的是谁的进项"。
 */
export function qyBlockInviteRelation(input: {
  invitee_id: number
  blocked: boolean
  reason: string
}) {
  return qyPost<QyBlockRelationResult>('/admin/invite/relations/block', input)
}

/**
 * 停止计返失败时该显示的**唯一一句**话。
 *
 * 后端按情形给出独立的 code（`qy_rel_no_relation` / `qy_rel_user_not_found` /
 * `qy_rel_not_bound`），所以这里只剩一件事：把通用的 `qyErrorMessage` 用上。
 *
 * 保留这个函数而不是让调用点直接用 `qyErrorMessage`，是因为 `network` 这一档
 * 需要**显式**保留："请求可能已经生效" 在停止计返上是准确的（后端可能已经写完
 * 快照行才断的连），而它在参数错误那一档是有害的 —— 读起来像"我刚才那一下
 * 也许扣了这个人的钱"。把这条分档写在这里，等于把"哪一档才配说可能已经生效"
 * 钉死在一个地方。
 */
export function qyBlockRelationErrorMessage(
  error: unknown,
  t: TFunction
): string {
  if (isQyError(error) && error.kind === 'network') return t('qy_err_network')
  return qyErrorMessage(error, t)
}

export type QyInviteAccrualFilters = {
  p: number
  page_size: number
  /** `YYYYMMDD`。留空 = 后端缺省（昨天那一桶）。 */
  day?: string
  inviter_id?: string
}

/**
 * 日结明细。对应 `GET /admin/invite/invite-accruals?day=&inviter_id=`。
 *
 * 一行一个（邀请人 × 下线 × 日）的桶：运营对账时问的是"这个人昨天为什么
 * 返了 / 没返这么多"，答案全在这一行上（基数、分组档、比例、状态、暂缓原因）。
 */
export function qyAdminInviteAccrualsQuery(filters: QyInviteAccrualFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
  }
  if (filters.day != null && filters.day !== '') query.day = filters.day
  if (filters.inviter_id != null && filters.inviter_id !== '') {
    query.inviter_id = filters.inviter_id
  }
  return queryOptions({
    queryKey: qyKeys.adminInviteAccruals(query),
    queryFn: () =>
      qyGet<QyInviteAccrualPage>('/admin/invite/invite-accruals', query),
  })
}
