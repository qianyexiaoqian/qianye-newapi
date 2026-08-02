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
import { qyGet } from '../../lib/api'
import type { QyPage } from '../../lib/types'
import type { QyAuditLog, QyPiiAudit, QyRequestAudit } from './types'

export type QyAuditLogParams = {
  p: number
  page_size: number
  category?: string
  /**
   * **前缀**匹配（后端 `LIKE 'x%'`）。
   *
   * 不是精确匹配：action 是分层命名的（`withdraw.approve` /
   * `withdraw.payee.create`），而排障时问的永远是「提现这一块都发生过什么」。
   */
  action?: string
  result?: string
  actor_type?: string
  ip?: string
  trace_no?: string
  actor_user_id?: number
  target_user_id?: number
  start_ts?: number
  end_ts?: number
}

/** 审计流水只追加不修改，因此这里只有读接口。 */
export function listQyAuditLogs(
  params: QyAuditLogParams
): Promise<QyPage<QyAuditLog>> {
  return qyGet<QyPage<QyAuditLog>>('/admin/audit-logs', params)
}

export type QyRequestAuditParams = {
  p: number
  page_size: number
  /** 同 {@link QyAuditLogParams.action}：前缀匹配。 */
  action?: string
  method?: string
  actor_type?: string
  ip?: string
  request_id?: string
  /**
   * 三态：不传 = 全部。
   *
   * 失败请求是这张表最有价值的切片 —— 越权探测与暴力枚举全是失败请求。
   */
  success?: 'false' | 'true'
  status_code?: number
  actor_user_id?: number
  target_user_id?: number
  start_ts?: number
  end_ts?: number
}

export function listQyRequestAudits(
  params: QyRequestAuditParams
): Promise<QyPage<QyRequestAudit>> {
  return qyGet<QyPage<QyRequestAudit>>('/admin/request-audits', params)
}

export type QyPiiAuditParams = {
  p: number
  page_size: number
  admin_id?: number
  target_user_id?: number
}

/**
 * 明文访问记录。
 *
 * 接口挂在 withdraw 模块下（`GET /admin/withdraw/pii-audits`），
 * 但它回答的是一个纯合规问题「谁看过谁的收款信息」，因此在界面上
 * 与另外两张审计表并列，而不是藏进提现管理页。
 */
export function listQyPiiAudits(
  params: QyPiiAuditParams
): Promise<QyPage<QyPiiAudit>> {
  return qyGet<QyPage<QyPiiAudit>>('/admin/withdraw/pii-audits', params)
}
