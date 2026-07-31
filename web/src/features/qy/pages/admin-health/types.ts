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
import type { QyHotQueueStats } from '../ops/types'

/** 与 `qianye/controller/admin.go` 的 `AdminHealth` 响应对齐。 */
export type QyAdminHealth = {
  /** `db.Stats()`。连接池字段在句柄未建立时整组缺失。 */
  db: {
    available: boolean
    connected: boolean
    /** 熔断打开到该时刻为止（unix 秒），0 表示未熔断。 */
    breaker_open_until: number
    fail_streak: number
    last_ping_ms: number
    last_ping_at: number
    open_conns?: number
    in_use?: number
    idle?: number
    wait_count?: number
    max_open?: number
  }
  hot_queue: QyHotQueueStats
  /** `twophase.Stats()`。扩展库不可用时为空对象。 */
  two_phase: {
    pending?: number
    uncertain?: number
    oldest_pending_age_sec?: number
    oldest_pending_order_no?: string
  }
  leases: QyTaskLease[]
  migrate: { table_count: number }
  config: { path: string; loaded_at: number; mtime: number }
  node: { name: string; is_master: boolean; holder: string }
}

export type QyTaskLease = {
  name: string
  /** `NodeName:PID`。同机多实例会重名，所以不能只看 NodeName。 */
  holder: string
  /** 每次易主递增；老持有者恢复后 fence 已过期，写入会失败，不会双跑。 */
  fence: number
  lease_until: number
  acquired_at: number
  updated_at?: number
}

/** `GET /admin/leases` 比 `/admin/health` 多一个已算好的 `expired`。 */
export type QyLeaseListItem = QyTaskLease & { expired: boolean }

export type QyLeaseList = {
  items: QyLeaseListItem[]
  /** 当前节点的 holder 标识，用于在表里高亮「就是我」。 */
  self: string
}
