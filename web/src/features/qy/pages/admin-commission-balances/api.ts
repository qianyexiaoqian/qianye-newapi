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
  QyAdjustCommissionResult,
  QyBalanceSort,
  QyCommissionBalancePage,
} from './types'

export type QyBalanceFilters = {
  p: number
  page_size: number
  sort: QyBalanceSort
  /** 精确匹配。后端不做模糊搜索，见 `adminListBalances` 的说明。 */
  user_id?: string
  username?: string
  debt_only?: boolean
}

export function qyAdminBalancesQuery(filters: QyBalanceFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
    sort: filters.sort,
  }
  if (filters.user_id != null && filters.user_id !== '') {
    query.user_id = filters.user_id
  }
  if (filters.username != null && filters.username !== '') {
    query.username = filters.username
  }
  if (filters.debt_only === true) query.debt_only = 'true'

  return queryOptions({
    queryKey: qyKeys.adminCommissionBalances(query),
    queryFn: () =>
      qyGet<QyCommissionBalancePage>('/admin/commission/balances', query),
  })
}

// 「登记已提现」（`POST /admin/commission/balances/withdrawn`）**不**回来：那是外挂
// 提现系统的数据迁移入口，D-15 之后「已入账」这一列只由自动入账任务写，没有任何
// 人工路径能改它。

/**
 * 手工增加 / 减少某个用户的佣金。`delta_quota` 带符号，负数是扣减。
 *
 * 后端把它落成一条 `source_type = manual` 的**计佣行**，再由既有的结算流程
 * 吸收进余额 —— 直接 UPDATE 余额列会让 Σ计佣 与 Σ结算 当场对不上，
 * 而且没有任何一行流水能解释差额。
 *
 * ── 为什么必须带 client_request_id ──
 * 语义是**增量**，而增量语义下一次网络重试就是第二笔。幂等键在弹窗打开时生成
 * 一次；改了金额再提交会撞 409（`qy_idem_key_conflict`）而不是发出两笔。
 *
 * 扣减上限由后端在持有余额行锁的事务里算：可用 + 未结算余数 + 已成熟待结算佣金。
 * 越界返回 400 `qy_adj_over_reclaimable`，而不是悄悄给这个人记一笔欠账。
 */
export function qyAdjustCommission(input: {
  user_id: number
  delta_quota: number
  reason: string
  client_request_id: string
}) {
  return qyPost<QyAdjustCommissionResult>(
    '/admin/commission/balances/adjust',
    input
  )
}
