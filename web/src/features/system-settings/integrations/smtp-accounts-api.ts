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

/**
 * 一个 SMTP 发件账号（管理端视角）。
 *
 * **没有 token 字段** —— 后端从不回显密码（回显等于把全部发件凭据发给每一个
 * 打开设置页的管理员）。提交时留空表示「不改密码」，`has_token` 用来区分
 * 「这个号还没配密码」与「配了但不给你看」：少了它两种状态在页面上一模一样，
 * 而前者发一封信就会认证失败。
 */
export type SmtpAccount = {
  id: number
  account_id: string
  name: string
  enabled: boolean
  server: string
  port: number
  account: string
  from_addr: string
  ssl_enabled: boolean
  start_tls_enabled: boolean
  insecure_skip_verify: boolean
  force_auth_login: boolean
  /** 一小时内允许发出的封数，0 表示不限。 */
  hourly_limit: number
  sort_order: number
  has_token: boolean
}

/** 新建/编辑时提交的载荷。`token` 留空即不改密码。 */
export type SmtpAccountPayload = Omit<SmtpAccount, 'has_token'> & {
  token: string
}

export function emptySmtpAccountPayload(): SmtpAccountPayload {
  return {
    id: 0,
    // 与账号名无关的随机串：account_id 是发件台账与用量统计的归集键，
    // 拿邮箱或名字当它的话，运营改个备注名就会让历史统计断成两截。
    account_id: `smtp_${Math.random().toString(36).slice(2, 10)}`,
    name: '',
    enabled: true,
    server: '',
    port: 587,
    account: '',
    token: '',
    from_addr: '',
    ssl_enabled: false,
    start_tls_enabled: false,
    insecure_skip_verify: false,
    force_auth_login: false,
    hourly_limit: 0,
    sort_order: 0,
  }
}

export function toPayload(a: SmtpAccount): SmtpAccountPayload {
  const { has_token: _hasToken, ...rest } = a
  return { ...rest, token: '' }
}

export async function listSmtpAccounts(): Promise<{
  success: boolean
  message?: string
  data?: SmtpAccount[]
}> {
  const res = await api.get('/api/smtp-account/')
  return res.data
}

export async function createSmtpAccount(payload: SmtpAccountPayload) {
  const res = await api.post('/api/smtp-account/', payload)
  return res.data as { success: boolean; message?: string }
}

export async function updateSmtpAccount(payload: SmtpAccountPayload) {
  const res = await api.put('/api/smtp-account/', payload)
  return res.data as { success: boolean; message?: string }
}

export async function deleteSmtpAccount(id: number) {
  const res = await api.delete(`/api/smtp-account/${id}`)
  return res.data as { success: boolean; message?: string }
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
