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

/**
 * 服务端在签发会话之前要求补一次两步验证时的应答形状。
 *
 * 每一条主登录通道(密码、OAuth 回调、微信、Telegram)都可能收到它 ——
 * 闸门在后端的 `setupLogin` 上,不挂在某一个 handler 里。所以判定写在一处,
 * 由各个调用点共用,免得再出现"只有密码登录认这个字段"的情况。
 */
export type TwoFALoginChallenge = {
  require_2fa: true
  flow_token: string
  expires_at?: number
}

/**
 * 从登录应答的 data 里取出两步验证挑战;不是挑战就返回 null。
 *
 * `flow_token` 为空时同样返回 null:没有它就走不到第二步,当成挑战会把用户
 * 送进一个提交必然失败的页面,还不如让调用点按登录失败处理。
 */
export function pickTwoFALoginChallenge(
  data: unknown
): TwoFALoginChallenge | null {
  if (typeof data !== 'object' || data === null) return null
  const candidate = data as Record<string, unknown>
  if (candidate.require_2fa !== true) return null
  const flowToken = candidate.flow_token
  if (typeof flowToken !== 'string' || flowToken.trim() === '') return null
  const expiresAt = candidate.expires_at
  return {
    require_2fa: true,
    flow_token: flowToken,
    expires_at: typeof expiresAt === 'number' ? expiresAt : undefined,
  }
}
