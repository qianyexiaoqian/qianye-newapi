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
import { api } from '@/lib/api'

/** 发件模式。后端 `common.SMTPSendMode*` 三个常量的镜像。 */
export type SmtpSendMode = 'fixed' | 'random' | 'sequential'

/** 一个 SMTP 发件账号。字段与后端 `common.SMTPAccountConfig` 一一对应。 */
export type SmtpAccount = {
  id: string
  name: string
  enabled: boolean
  server: string
  port: number
  account: string
  token: string
  from: string
  ssl_enabled: boolean
  start_tls_enabled: boolean
  insecure_skip_verify: boolean
  force_auth_login: boolean
  /** 一小时内允许发出的封数，0 表示不限。 */
  hourly_limit: number
}

export function emptySmtpAccount(id: string): SmtpAccount {
  return {
    id,
    name: '',
    enabled: true,
    server: '',
    port: 587,
    account: '',
    token: '',
    from: '',
    ssl_enabled: false,
    start_tls_enabled: false,
    insecure_skip_verify: false,
    force_auth_login: false,
    hourly_limit: 0,
  }
}

/**
 * 解析存在 option 里的账号表。
 *
 * 任何解析失败都回落成空数组而不是抛错：这份 JSON 由管理端自己写回，理论上
 * 永远合法；但真出现坏值时，整页白屏比"账号表看起来是空的"糟得多 ——
 * 后者至少还能重新配一遍，前者连设置页都进不去。
 */
export function parseSmtpAccounts(raw: string | undefined): SmtpAccount[] {
  if (!raw) return []
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.map((item) => ({
      ...emptySmtpAccount(''),
      ...(item as Partial<SmtpAccount>),
    }))
  } catch {
    return []
  }
}

/** 单个账号的发送量统计。 */
export type SmtpAccountStat = {
  account_id: string
  account_name: string
  total: number
  success: number
  failed: number
  last_hour: number
  last_sent_at: number
  hourly_limit: number
  enabled: boolean
  /** false = 台账里有、但账号表里已经删掉了。 */
  configured: boolean
}

export async function getSmtpAccountStats(
  since?: number
): Promise<{ success: boolean; message?: string; data?: SmtpAccountStat[] }> {
  const res = await api.get('/api/email-log/stats', {
    params: since ? { since } : undefined,
  })
  return res.data
}

/** 一条发件台账。 */
export type EmailSendLogItem = {
  id: number
  account_id: string
  account_name: string
  from_addr: string
  receiver: string
  subject: string
  success: boolean
  error_msg: string
  duration_ms: number
  created_at: number
}

export type EmailSendLogQuery = {
  p: number
  page_size: number
  account_id?: string
  receiver?: string
  status?: '' | 'success' | 'failed'
  start_timestamp?: number
  end_timestamp?: number
}

export async function getEmailSendLogs(params: EmailSendLogQuery): Promise<{
  success: boolean
  message?: string
  data?: { items: EmailSendLogItem[]; total: number }
}> {
  const res = await api.get('/api/email-log/', { params })
  return res.data
}
