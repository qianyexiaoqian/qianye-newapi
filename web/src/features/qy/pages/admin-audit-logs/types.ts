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

/**
 * 与 `qianye/model/request_audit.go` 的 `RequestAudit` 对齐。
 *
 * 与 {@link QyAuditLog} 是两张不同的表，刻意不合并：前者记「资金判定」
 * （带金额三件套、冻结汇率、前后快照），后者记「HTTP 调用」
 * （谁、何时、成没成功）。合成一个类型只会得到一堆恒为空的可选字段。
 */
export type QyRequestAudit = {
  id: number
  /** 由 method + 路由模板推导，如 `admin.withdraw.approve.create`。 */
  action: string
  method: string
  /** **路由模板**（`/api/qy/admin/withdraw/:id/approve`），不是实际 URL。 */
  path: string
  status_code: number
  success: boolean
  latency_ms: number
  actor_type: QyAuditActorType | ''
  actor_user_id: number
  actor_name: string
  /** 上游角色码：1 普通 / 10 管理 / 100 root。 */
  actor_role: number
  auth_method: string
  target_user_id: number
  /** 路径参数的 JSON，已按键名脱敏。 */
  params: string
  /** 已脱敏的 query 串。 */
  query: string
  /** 已脱敏的请求体；非 JSON 与凭证型路由只留占位说明。 */
  body: string
  ip: string
  user_agent: string
  request_id: string
  node_name: string
  created_at: number
}

/**
 * 与 `qianye/modules/withdraw/model.go` 的 `PiiAudit` 对齐。
 *
 * 每一行 = 一次收款信息明文/打款凭证的访问。这是合规检查要单独导出的那张表，
 * 保留期比资金审计更长，因此后端也把它单独放在 `qy_pii_audits`。
 * 后端刻意不下发 `user_agent`（模型上是 `json:"-"`）。
 */
export type QyPiiAudit = {
  id: number
  /** 被访问的资源类型：`payee` 收款信息 / `proof` 打款凭证。 */
  resource: string
  resource_id: number
  target_user_id: number
  admin_id: number
  admin_name: string
  action: string
  /** 本次真正解密出来的字段名，逗号分隔。 */
  fields: string
  /** 强制填写的访问事由 —— 没有事由就无法区分「正常核对」与「顺手看看」。 */
  reason: string
  ip: string
  created_at: number
}
