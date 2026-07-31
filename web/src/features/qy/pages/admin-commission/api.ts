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

import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyAdminAccrual,
  QyCommissionAdminConfig,
  QyCommissionGroupRate,
} from './types'

export function qyAdminCommissionConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.adminCommissionConfig(),
    queryFn: () => qyGet<QyCommissionAdminConfig>('/admin/commission/config'),
  })
}

/**
 * 修改运营参数。
 *
 * 请求体是 `{key: string}` 的稀疏 map，只传改动过的键 —— 后端逐键写
 * `qy_settings` 并写一条审计，把没改的键一起发过去会污染"谁在什么时候
 * 把 3% 改成 8%"的追溯轨迹。
 *
 * 取值一律用**字符串**发送：返佣比例支持两位小数，而 JSON number 到了
 * JS 这一侧就是二进制浮点，10.25 有可能被序列化成 10.249999999999998。
 * 字符串把运营填的那个数字原样交给后端的 decimal 解析。
 *
 * 调用方成功后重新 GET 一次即可，别去适配响应体。
 */
export function qyUpdateCommissionConfig(patch: Record<string, string>) {
  return qyPut<unknown>('/admin/commission/config', patch)
}

/**
 * 新增或覆盖一条分组费率规则（按分组名 upsert）。
 *
 * 比例是百分比字符串，同上：不经过 JS 的 Number。
 * 后端每次都会写审计 —— 分组费率比全局费率更隐蔽，只影响一部分用户，
 * 不看审计根本查不出是谁改的。
 */
export function qyUpsertCommissionGroupRate(input: {
  group_name: string
  topup_rate_percent: string
  consume_rate_percent: string
  enabled: boolean
  remark: string
}) {
  return qyPut<QyCommissionGroupRate>('/admin/commission/group-rates', input)
}

/** 删除一条分组费率规则。该分组随即回落到全局默认费率，不是变成零费率。 */
export function qyDeleteCommissionGroupRate(groupName: string) {
  return qyDelete<{ group_name: string; deleted: boolean }>(
    `/admin/commission/group-rates?group_name=${encodeURIComponent(groupName)}`
  )
}

export type QyAdminAccrualFilters = {
  p: number
  page_size: number
  inviter_id?: string
  invitee_id?: string
  source_type?: string
  status?: string
  accrual_no?: string
}

export function qyAdminAccrualsQuery(filters: QyAdminAccrualFilters) {
  const query: Record<string, unknown> = {
    p: filters.p,
    page_size: filters.page_size,
  }
  for (const key of [
    'inviter_id',
    'invitee_id',
    'source_type',
    'status',
    'accrual_no',
  ] as const) {
    const value = filters[key]
    if (value != null && value !== '') query[key] = value
  }

  return queryOptions({
    queryKey: qyKeys.adminCommissionRecords(query),
    queryFn: () =>
      qyGet<QyPage<QyAdminAccrual>>('/admin/commission/records', query),
  })
}

/**
 * 人工冲正。
 *
 * `reason` 与 `client_request_id` 都是后端必填：前者是事后复盘的唯一依据，
 * 后者防止一次网络重试把佣金扣两遍。
 */
export function qyClawbackAccrual(input: {
  accrual_id: number
  quota: number
  reason: string
  client_request_id: string
}) {
  return qyPost<{ accrual_no: string; gross_amount: string }>(
    '/admin/commission/clawback',
    input
  )
}

/** 立即结算指定用户，不必等下一个周期。 */
export function qySettleCommission(userId: number) {
  return qyPost<{ settled: boolean; user_id: number }>(
    `/admin/commission/settle?user_id=${userId}`
  )
}

/** 拉黑/解封一条邀请关系。只停止未来计佣，已发放的佣金要另走冲正。 */
export function qyBlockInviteRelation(input: {
  invitee_id: number
  blocked: boolean
  reason: string
}) {
  return qyPost<{ invitee_id: number; blocked: boolean }>(
    '/admin/commission/relations/block',
    input
  )
}
