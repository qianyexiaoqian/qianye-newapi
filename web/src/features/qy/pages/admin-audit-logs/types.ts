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
/** 与 `qianye/model/audit_log.go` 的 `AuditLog` 对齐。 */

export type QyAuditCategory =
  | 'admin'
  | 'commission'
  | 'config'
  | 'fund'
  | 'transfer'
  | 'violation'
  | 'withdraw'
  | (string & {})

/** `system` = 补偿任务 / 结算任务。事故复盘时必须能与人工操作区分。 */
export type QyAuditActorType = 'admin' | 'system' | 'user' | (string & {})

export type QyAuditLog = {
  id: number
  /** 串起一笔资金的全生命周期，通常等于 `FundOrder.OrderNo`。 */
  trace_no: string
  category: QyAuditCategory
  /** 稳定英文标识（如 `withdraw.approve`），不存自然语言。 */
  action: string
  actor_type: QyAuditActorType
  actor_user_id: number
  actor_name: string
  target_user_id: number
  amount_quota: number
  /** decimal，后端序列化为字符串。前端只展示、不参与运算。 */
  amount_fiat: string
  currency: string
  frozen_rate: string
  result: 'fail' | 'ok' | 'pending' | (string & {})
  reason: string
  /** JSON 快照，按 `audit.snapshot_max_bytes` 截断，可能不是合法 JSON。 */
  before_snap: string
  after_snap: string
  ip: string
  user_agent: string
  request_id: string
  node_name: string
  created_at: number
}
