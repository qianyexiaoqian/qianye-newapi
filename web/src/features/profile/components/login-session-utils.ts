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
import type { TFunction } from 'i18next'

import type { LoginSession } from '@/stores/auth-store'

/** 一页 6 条（项目方指定）。 */
export const LOGIN_SESSIONS_PAGE_SIZE = 6

export interface LoginSessionSummary {
  /** 恒等于 active + expired，也恒等于所有分页加起来实际渲染的条数。 */
  total: number
  active: number
  expired: number
}

export interface LoginSessionEntry {
  session: LoginSession
  expired: boolean
}

export interface LoginSessionsView {
  summary: LoginSessionSummary
  /** 夹紧后的当前页，1 起。 */
  page: number
  pageCount: number
  /** 当前页要渲染的会话。 */
  visible: LoginSessionEntry[]
}

/**
 * 把一整份会话列表折算成"这一页渲染什么 + 头部统计显示什么"。
 *
 * 做成一个函数而不是几个散装 helper，是为了让"统计与列表同源"成为结构性保证：
 * 逐条的 `expired` 标记和 `summary` 的三个数字出自同一趟遍历、同一个
 * `nowSeconds`、同一份数组，不存在两处各自取一次时钟然后在边界上打架的可能。
 * 同理，这里**不按到期与否过滤列表** —— 一旦过滤，头部数字就会和实际能翻到的
 * 条数对不上，那是最显眼的一种缺陷。
 */
export function buildLoginSessionsView(
  sessions: readonly LoginSession[],
  requestedPage: number,
  nowSeconds: number
): LoginSessionsView {
  const entries = sessions.map((session) => ({
    session,
    // 判据只认后端真实下发的 expires_at（Unix 秒），不拿 last_active_at 推算。
    expired: session.expires_at <= nowSeconds,
  }))
  const expired = entries.filter((entry) => entry.expired).length

  const total = entries.length
  const pageCount = total <= 0 ? 1 : Math.ceil(total / LOGIN_SESSIONS_PAGE_SIZE)
  // 撤销掉当前页最后一条后总数会缩水，旧页码就指向一页空白。渲染时夹一次即等于
  // 自动回退，不必用 useEffect 追着 setState 补救（那会先渲染出一帧空列表）。
  const page = Number.isFinite(requestedPage)
    ? Math.min(Math.max(1, Math.trunc(requestedPage)), pageCount)
    : 1
  const start = (page - 1) * LOGIN_SESSIONS_PAGE_SIZE

  return {
    summary: { total, active: total - expired, expired },
    page,
    pageCount,
    visible: entries.slice(start, start + LOGIN_SESSIONS_PAGE_SIZE),
  }
}

export function sessionDevice(
  userAgent: string,
  unknownDevice: string,
  browserLabel: string,
  maxTouchPoints = 0
): string {
  if (!userAgent) return unknownDevice
  let browser = browserLabel
  if (userAgent.includes('Edg/')) browser = 'Edge'
  else if (userAgent.includes('Chrome/')) browser = 'Chrome'
  else if (userAgent.includes('Firefox/')) browser = 'Firefox'
  else if (userAgent.includes('Safari/')) browser = 'Safari'

  let system = ''
  const isIPad =
    userAgent.includes('iPad') ||
    (userAgent.includes('Macintosh') && maxTouchPoints > 1)
  if (userAgent.includes('iPhone') || isIPad) {
    system = 'iOS'
  } else if (userAgent.includes('Android')) system = 'Android'
  else if (userAgent.includes('Windows')) system = 'Windows'
  else if (userAgent.includes('Mac OS')) system = 'macOS'
  else if (userAgent.includes('Linux')) system = 'Linux'
  return system ? `${browser} · ${system}` : browser
}

export function loginMethodLabel(method: string, t: TFunction): string {
  const normalized = method.trim().toLowerCase()
  switch (normalized) {
    case 'password':
      return t('Password')
    case '2fa':
      return t('Two-factor Authentication')
    case 'passkey':
      return t('Passkey')
    case 'wechat':
      return t('WeChat')
    case 'telegram':
      return t('Telegram')
    case 'oauth':
      return t('OAuth')
    case 'unknown':
    case '':
      return t('Unknown')
    default:
      break
  }

  if (!normalized.startsWith('oauth:')) return method
  const provider = normalized.slice('oauth:'.length)
  const providerNames: Record<string, string> = {
    discord: 'Discord',
    github: 'GitHub',
    linuxdo: 'LinuxDO',
    oidc: 'OIDC',
  }
  return `${t('OAuth')} · ${providerNames[provider] || provider}`
}
