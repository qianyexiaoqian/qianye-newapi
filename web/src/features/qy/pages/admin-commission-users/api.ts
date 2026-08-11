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
  QyCommissionUserFilter,
  QyCommissionUserPage,
  QyCommissionUserSort,
  QyRebindRelationResult,
} from './types'

export type QyCommissionUserFilters = {
  p: number
  page_size: number
  sort: QyCommissionUserSort
  /**
   * 一个输入框同时搜用户名 / id / 邮箱。
   *
   * 刻意**不拆成三个框**：运营手上拿到的是"某个人"的某一个标识，他事先并不
   * 知道那串东西算用户名还是邮箱。三个框会强迫他先给自己手上的字符串分类，
   * 分错了就得到一个空列表 —— 而空列表在这张表上与"这个人真的没有佣金"
   * 长得一模一样。归一由后端做（纯数字优先按 id 精确匹配，同时仍 OR 上
   * 用户名/邮箱的前缀匹配）。
   */
  keyword?: string
  /** 行内筛选开关。只把为真的那些拼进 query，省得 URL 里全是 `=false`。 */
  flags?: readonly QyCommissionUserFilter[]
}

/**
 * 「用户佣金」列表。**一行 = 一个用户**。
 *
 * 对应 `GET /api/qy/admin/commission/users`。后端跨主库（人与邀请关系）与扩展库
 * （钱）聚合，查询次数与页长无关；本页一个字都不重算。
 */
export function qyAdminCommissionUsersQuery(filters: QyCommissionUserFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
    sort: filters.sort,
  }
  if (filters.keyword != null && filters.keyword !== '') {
    query.keyword = filters.keyword
  }
  for (const flag of filters.flags ?? []) query[flag] = 'true'

  return queryOptions({
    queryKey: qyKeys.adminCommissionUsers(query),
    queryFn: () =>
      qyGet<QyCommissionUserPage>('/admin/commission/users', query),
  })
}

/**
 * 换绑：把这个用户的上线从旧的改成新的。
 *
 * ── 为什么必须是后端的一个端点，而不是前端"先解绑再绑" ──
 * 后者是两次请求：第二次失败时这个人会停在**没有上线**的中间态，而运营看到的
 * 只是一句"操作失败"，他会以为什么都没变。后端 `adminRebindRelation` 在一个
 * 事务里改权威字段 + 更新快照，要么全成要么全不成。
 *
 * 语义与解绑一致：**已产生的佣金全部留在旧上线名下**（返回的
 * `kept_commission_quota` 就是那个数），只是从此不再产生新的。
 * 也**不补发**上游的邀请奖励（`aff_quota`）—— 那笔额度是注册时发的。
 *
 * 换成他现在这个上线会被后端 400（`qy_rel_same_inviter`）而不是当空操作放行：
 * 空操作会写一条"换绑成功"的审计，而实际上什么都没发生。
 */
export function qyRebindAffRelation(input: {
  invitee_id: number
  inviter_id: number
  reason: string
}) {
  return qyPost<QyRebindRelationResult>(
    '/admin/commission/relations/rebind',
    input
  )
}
