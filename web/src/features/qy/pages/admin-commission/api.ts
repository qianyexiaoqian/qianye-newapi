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
  QyCommissionCreditSnapshot,
  QyCommissionRateOverlap,
  QyCommissionGroupRate,
  QyDailySettleSnapshot,
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
 * 字符串把运营填的那个数字原样交给后端的 decimal 解析。可空的百分比键用
 * 空串表达"取消这一档"。
 *
 * 请求体里**没有任何法币键**：佣金只记星屑、自动入账，
 * `fiat_rate_default` 那一档连同它的清空动作一起删除。
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
  /**
   * 兑换码档。**必须显式传 `null` 才表示"本组不单独配"** —— 这个接口是**整行
   * upsert**，把它漏掉就等于每次保存都在悄悄取消这一档。传 `'0'` 是显式 0%。
   */
  redemption_rate_percent: string | null
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

/**
 * 结算 / 入账调度快照。
 *
 * `daily_settle`：一日一结算之后，「今天这一跑成了没有」是运营唯一需要盯的那个数。
 * `credit`：自动入账累计发了多少（D-16 起入账是本地事务，没有在途也没有挂起）。
 * `rate_overlap`：佣金三档与 stardust 三档 invite_* 是否在给同一笔基数各返一次。
 * 后端若暂时不下发某一段，界面按"取不到"处理，不编数。
 */
export function qyAdminCommissionHealthQuery() {
  return queryOptions({
    queryKey: qyKeys.adminCommissionHealth(),
    queryFn: () =>
      qyGet<{
        daily_settle: QyDailySettleSnapshot
        credit?: QyCommissionCreditSnapshot
        rate_overlap?: QyCommissionRateOverlap
      }>('/admin/commission/health'),
  })
}

/**
 * 重跑今天这一轮结算。
 *
 * 它不直接结算任何人，只把今天那一行运行记录改回「还要再跑」，真正的排空交给
 * 下一次心跳 —— 排空可能持续很久，放在 HTTP 请求线程里会让运营以为超时失败
 * 又点一次。
 */
export function qyRerunDailySettle() {
  return qyPost<{ run_date: string; rearmed: boolean }>(
    '/admin/commission/settle/rerun'
  )
}

// 「立即结算指定用户」的前端封装（`POST /admin/commission/settle`）不回来。
//
// 项目方原话：「佣金审核的这个：立即结算 移除吧，全部由系统到时间自动结算。」
// **后端接口原样保留** —— 它与「重跑今天这一轮」不是同一件事：前者按人补一笔，
// 后者把今天那一行运行记录改回"还要再跑"。这里删掉的只是没有调用方的前端封装。
//
// 停止 / 恢复计返（`relations/block`）归 invite 模块：封装在 `admin-invite/api.ts`。
