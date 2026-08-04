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
import { QY_LOT_EMPTY_RULES, type QyLotRules } from '../types'

/**
 * 解析并补齐参与条件。
 *
 * 缺字段一律补 `0` / `false`（= 不限），而不是抛错：后端加了新条件而前端还没
 * 认识它时，用户看到的应该是"少列了一条"，不是整块条件消失。
 */
export function parseQyLotRules(rulesText: string): QyLotRules | null {
  if (rulesText.trim() === '') return { ...QY_LOT_EMPTY_RULES }
  let raw: unknown
  try {
    raw = JSON.parse(rulesText)
  } catch {
    return null
  }
  if (typeof raw !== 'object' || raw === null) return null
  const source = raw as Record<string, unknown>

  const strings = (key: string): string[] => {
    const value = source[key]
    return Array.isArray(value)
      ? value.filter((item): item is string => typeof item === 'string')
      : []
  }
  const int = (key: string): number => {
    const value = source[key]
    return typeof value === 'number' && Number.isFinite(value)
      ? Math.trunc(value)
      : 0
  }
  const flag = (key: string): boolean => source[key] === true

  // 键名与后端 `Rules` 的 json tag 逐字一致。这份 JSON 就是进 `rules_hash`
  // 的那份字节，本函数只**读**它，绝不重新序列化 —— JS 与 Go 的对象序列化顺序
  // 不一致，重序列化必然算出另一个哈希。
  return {
    allow_groups: strings('allow_groups'),
    deny_groups: strings('deny_groups'),
    min_account_age_days: int('min_account_age_days'),
    min_quota: int('min_quota'),
    min_used_quota: int('min_used_quota'),
    recent_spend_days: int('recent_spend_days'),
    recent_spend_quota: int('recent_spend_quota'),
    exclude_violation: flag('exclude_violation'),
    max_violation_hits: int('max_violation_hits'),
    exclude_ever_auto_banned: flag('exclude_ever_auto_banned'),
    exclude_currently_disabled: flag('exclude_currently_disabled'),
    require_email: flag('require_email'),
    require_oauth: flag('require_oauth'),
    require_pay_password: flag('require_pay_password'),
    max_entries_per_user: int('max_entries_per_user'),
    max_attempts_per_user: int('max_attempts_per_user'),
    max_total_entries: int('max_total_entries'),
    max_total_users: int('max_total_users'),
    max_per_inviter: int('max_per_inviter'),
    cooldown_seconds: int('cooldown_seconds'),
    dedup_ip: flag('dedup_ip'),
  }
}
