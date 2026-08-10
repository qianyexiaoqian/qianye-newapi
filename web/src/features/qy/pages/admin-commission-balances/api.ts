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
  QySetWithdrawnResult,
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

/**
 * 把某个用户的「已提现」额度设成给定的**绝对值**，差额从可提现里划走。
 *
 * ── 为什么是绝对值而不是增量 ──
 * 迁移场景天然是"这个人历史上已经领过 X"。增量语义下，一次网络重试或者运营
 * 手滑双击就会把可提现扣两遍，而扣掉的额度没有任何自动路径能还回来。
 * 绝对值语义下重复提交是空操作，所以这个接口**刻意不带 client_request_id** ——
 * 幂等性由语义本身保证，多一个幂等键只是多一个能传错的地方。
 *
 * `reason` 是后端必填且至少 4 个字符（与提现模块看 PII 明文同口径）：
 * 直接改账的动作没有事由，事后无法与误操作区分。
 */
export function qySetCommissionWithdrawn(input: {
  user_id: number
  withdrawn_quota: number
  reason: string
}) {
  return qyPost<QySetWithdrawnResult>(
    '/admin/commission/balances/withdrawn',
    input
  )
}

/**
 * 手工增加 / 减少某个用户的佣金。`delta_quota` 带符号，负数是扣减。
 *
 * ── 它与「登记已提现」的区别 ──
 * 后者只是把额度从可提现搬到已提现（恒等式两侧不变）；这里是真的凭空加钱/扣钱。
 * 所以后端把它落成一条 `source_type = manual` 的**计佣行**，再由既有的结算流程
 * 吸收进余额 —— 直接 UPDATE 余额列会让 Σ计佣 与 Σ结算 当场对不上，
 * 而且没有任何一行流水能解释差额。
 *
 * ── 为什么必须带 client_request_id ──
 * 语义是**增量**（"佣金是挣出来的"，不存在"这个人历史上应该正好是 X"这种说法），
 * 而增量语义下一次网络重试就是第二笔。幂等键在弹窗打开时生成一次；改了金额再
 * 提交会撞 409（`qy_idem_key_conflict`）而不是发出两笔。
 *
 * 扣减上限由后端在持有余额行锁的事务里算：可提现 + 未结算余数 + 已成熟待结算佣金。
 * 越界返回 400 `qy_adj_over_reclaimable`，而不是悄悄给这个人记一笔冻结提现的欠账。
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
