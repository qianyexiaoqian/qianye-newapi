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
import { qyGet, qyPost } from '../../lib/api'
import type { QyFundOrder, QyPage } from '../../lib/types'

export type QyFundOrderParams = {
  p: number
  page_size: number
  /** 后端按数字比较 `status`，因此这里传的是 int8 的字符串形式。 */
  status?: string
  kind?: string
  order_no?: string
  ref_id?: string
  user_id?: number
  start_ts?: number
}

export function listQyFundOrders(
  params: QyFundOrderParams
): Promise<QyPage<QyFundOrder>> {
  return qyGet<QyPage<QyFundOrder>>('/admin/fund-orders', params)
}

/**
 * 立即对某笔单重跑一次主库探针。
 *
 * `qy_fund_outbox` 是判定「主库到底动没动」的唯一精确手段。重跑不改状态机，
 * 只是把补偿任务下一轮才会做的事提前做掉，因此是安全的、可重复的。
 */
export function reprobeQyFundOrder(orderNo: string): Promise<{
  order_no: string
  status: string
  main_applied: boolean
}> {
  return qyPost(`/admin/fund-orders/${encodeURIComponent(orderNo)}/reprobe`)
}

/**
 * 人工裁决一笔无法自动判定的资金单。
 *
 * 后端只接受 `uncertain` 态：`pending` 应交给补偿任务自动收敛，已终态的单
 * 不允许改写（那会破坏账目的不可变性）。理由必填并写入审计。
 */
export function resolveQyFundOrder(
  orderNo: string,
  body: { decision: 'failed' | 'success'; reason: string }
): Promise<{ order_no: string; status: string }> {
  return qyPost(
    `/admin/fund-orders/${encodeURIComponent(orderNo)}/resolve`,
    body
  )
}
