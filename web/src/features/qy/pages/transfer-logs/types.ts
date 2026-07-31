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
import type { QyStatus } from '../../lib/types'

/**
 * 用户可见的划转流水。对应 `qianye/modules/transfer/handler.go` 的 `recordItem`。
 *
 * 注意后端**只下发用户该看的字段**：`client_ip` / `user_agent` / `fail_reason`
 * 是管理端排障用的，不在这个视图里。
 */
export type QyTransferRecord = {
  order_no: string
  /** `out` 转出（含手续费）/ `in` 转入。由后端按当前登录用户判定。 */
  direction: 'in' | 'out' | (string & {})
  /** 对手方脱敏用户名，**已由后端处理**。 */
  counterparty: string
  counterparty_id: number
  amount: number
  /** 手续费只在转出方向有值：它是从发起方额外扣的，不进收款方。 */
  fee_quota: number
  status: QyStatus
  fail_code: string
  remark: string
  created_at: number
  settled_at: number
}

export type QyTransferRecordsParams = {
  p: number
  page_size: number
  /** 空串表示不筛选（`in` / `out`）。 */
  direction?: string
  status?: string
}

/**
 * 管理端流水视图（`GET /api/qy/admin/transfer/records`）。
 *
 * 后端直接回 `qy_transfer_orders` 原始行，因此字段名与 GORM model 的 json tag
 * 一致，且比用户视图多出真实用户名、余额快照与来源 IP。
 */
export type QyAdminTransferRecord = {
  id: number
  order_no: string
  from_user_id: number
  to_user_id: number
  from_username: string
  to_username: string
  amount: number
  fee_quota: number
  status: QyStatus
  fail_code: string
  remark: string
  from_quota_before: number
  from_quota_after: number
  to_quota_before: number
  to_quota_after: number
  client_ip: string
  created_at: number
  settled_at: number
}

export type QyAdminTransferRecordsParams = {
  p: number
  page_size: number
  user_id?: number
  order_no?: string
  status?: string
}
