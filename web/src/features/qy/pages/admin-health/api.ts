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
import type {
  QyAdminHealth,
  QyLeaseList,
  QyOverdraftReport,
  QyVersionInfo,
} from './types'

export function getQyAdminHealth(): Promise<QyAdminHealth> {
  return qyGet<QyAdminHealth>('/admin/health')
}

/**
 * 版本三元组。
 *
 * 刻意与 `/admin/health` 分成两个请求：后端那一侧 `/admin/version` 不走
 * `requireCore`，扩展库不可用时仍然是 200，而 `/admin/health` 会 503。
 * 合并成一个请求就把这个降级能力白白丢掉了 —— 排障的第一个问题正是
 * 「现在跑的到底是哪个版本」。
 */
export function getQyVersion(): Promise<QyVersionInfo> {
  return qyGet<QyVersionInfo>('/admin/version')
}

/** 租约持有情况。用于确认多节点没有双跑同一个后台任务。 */
export function listQyLeases(): Promise<QyLeaseList> {
  return qyGet<QyLeaseList>('/admin/leases')
}

/**
 * 重新加载 YAML 配置。
 *
 * `database` 段永不重载 —— 连接池与 DSN 不能热切，那会让正在进行的事务
 * 落到旧连接上。其余段（费率、开关、影子模式）立即生效并写审计。
 */
export function reloadQyConfig(): Promise<{
  reloaded: boolean
  path: string
  loaded_at: number
}> {
  return qyPost('/admin/config/reload')
}

/**
 * 负余额（透支）总览。
 *
 * 刻意与 `/admin/health` 分成两个请求，理由与 `/admin/version` 那条相同：
 * 后端那一侧 `/admin/overdraft` **不走 `requireCore`**（它只查主库 users），
 * 扩展库不可用时仍然是 200，而 `/admin/health` 会 503。合并成一个请求
 * 就把这个降级能力白白丢掉了 —— 而"站上现在欠了多少钱"恰恰是排障时
 * 最不该跟着扩展库一起消失的数字。
 */
export function getQyOverdraft(): Promise<QyOverdraftReport> {
  return qyGet<QyOverdraftReport>('/admin/overdraft')
}
