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

import { qyGet } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import { QY_COMMISSION_CREDIT_STATUSES } from '../affiliate/types'
import type {
  QyAdminCommissionCreditPage,
  QyCommissionUserFilter,
  QyCommissionUserPage,
  QyCommissionUserSort,
} from './types'

export type QyCommissionUserFilters = {
  p: number
  page_size: number
  sort: QyCommissionUserSort
  /**
   * 一个输入框同时搜用户名 / id / 邮箱。
   *
   * 刻意**不拆成三个框**：运营手上拿到的是"某个人"的某一个标识，他事先并不
   * 知道那串东西算用户名还是邮箱。归一由后端做（纯数字优先按 id 精确匹配，
   * 同时仍 OR 上用户名/邮箱的前缀匹配）。
   */
  keyword?: string
  /** 行内筛选开关。只把为真的那些拼进 query，省得 URL 里全是 `=false`。 */
  flags?: readonly QyCommissionUserFilter[]
}

/**
 * 「佣金用户」列表。**一行 = 一个用户**。
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

export type QyAdminCommissionCreditFilters = {
  p: number
  page_size: number
  user_id?: string
  /** 留空 = 全部状态。取值只认 {@link QY_COMMISSION_CREDIT_STATUSES}，别的当空处理。 */
  status?: string
}

/**
 * 全站自动入账记录（`GET /admin/commission/credits`）。
 *
 * 运营在这张表上要盯的只有 `held`：那是资金单结局不明、等人裁决的单子，裁决
 * 在「资金对账」页按 `fund_order_no` 做，这里只负责把它们捞出来。
 */
export function qyAdminCommissionCreditsQuery(
  filters: QyAdminCommissionCreditFilters
) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
  }
  if (filters.user_id != null && filters.user_id !== '') {
    query.user_id = filters.user_id
  }
  if (
    filters.status != null &&
    (QY_COMMISSION_CREDIT_STATUSES as readonly string[]).includes(
      filters.status
    )
  ) {
    query.status = filters.status
  }

  return queryOptions({
    queryKey: qyKeys.adminCommissionCredits(query),
    queryFn: () =>
      qyGet<QyAdminCommissionCreditPage>('/admin/commission/credits', query),
  })
}

// 换绑（`relations/rebind`）与本目录此前的 `ManageRelationDialog` 不回来：邀请关系
// 的绑定 / 换绑 / 解绑 / 停止计返在 D-14 起归 invite 模块，界面在「邀请管理」。
