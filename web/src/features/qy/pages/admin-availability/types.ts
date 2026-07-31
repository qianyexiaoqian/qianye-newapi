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
import type { QyAvailCell, QyAvailDefinition } from '../availability/types'
import type { QyHotQueueStats } from '../ops/types'

/** 与 `qianye/modules/availability/api.go` 的 `adminStats` 响应一一对应。 */
export type QyAdminAvailabilityStats = {
  definition: QyAvailDefinition
  config: {
    bucket_seconds: number
    flush_interval_seconds: number
    retention_days: number
    max_series_per_query: number
    hot_series_limit: number
    /** YAML 里请求了 attempt 级采样。 */
    sample_attempt_level_requested: boolean
    /** 后端本期并未实现 attempt 级采样，恒为 false。两者不一致要提示运维。 */
    sample_attempt_level_supported: boolean
  }
  sampler: {
    observed: number
    dropped_no_model: number
    dropped_series_limit: number
    truncated_names: number
    hot_series: number
  }
  flush: {
    runs: number
    rows: number
    failures: number
    last_at: number
  }
  rollup: { rows: number; last_at: number }
  cleanup: { rows: number; last_at: number }
  hot_queue: QyHotQueueStats
  /** 扩展库不可读时后端会整段省略。 */
  storage?: {
    bucket_rows: number
    hour_rows: number
    oldest_bucket_ts: number
  }
  start_ts: number
  end_ts: number
  /** 每个分组的汇总格子（`model` 恒为 `*`），不做白名单裁剪。 */
  groups: QyAvailCell[]
  /** 可用率最低的 20 个（分组, 模型）。 */
  worst_cells: QyAvailCell[]
}
