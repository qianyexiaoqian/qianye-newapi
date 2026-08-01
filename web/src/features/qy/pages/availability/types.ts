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
 * 与 `qianye/modules/availability/api.go` + `query.go` + `outcome.go` 的
 * 响应体一一对应。管理端总览页已移除，本文件现在只服务用户端可用率页。
 */

/**
 * 六态。核心是把「没有数据」与「全挂了」彻底分开。
 *
 * - `ok` / `degraded` / `down`：有足够样本，`availability` 一定非空
 * - `low_sample`：有流量但样本不足以下结论，**不给百分比**
 * - `no_data`：该分组提供这个模型，但时段内没有请求
 * - `not_offered`：该分组根本不提供这个模型
 */
export type QyAvailState =
  | 'degraded'
  | 'down'
  | 'low_sample'
  | 'no_data'
  | 'not_offered'
  | 'ok'
  | (string & {})

/** 失败归因。取值同后端 `Outcome`，用于 top_reason 与口径清单。 */
export type QyAvailOutcome =
  | 'channel_test'
  | 'client_error'
  | 'client_gone'
  | 'internal_error'
  | 'no_channel'
  | 'quota_error'
  | 'rate_limit'
  | 'soft_fail'
  | 'success'
  | 'timeout'
  | 'upstream_error'
  | 'violation'
  | (string & {})

/**
 * 性能维度：端到端延迟、首字延迟、生成速度。
 *
 * 三个均值都可能是 `null`，含义与 `availability` 的 `null` 完全一致：
 * **样本不足以下结论**。渲染成 0 会被读成「延迟 0ms / 一个字都吐不出来」，
 * 两种误读都会让人对着一个根本不存在的数字做决策。
 *
 * 三个样本数一并下发，页面据此解释「这一格为什么是横杠」。
 */
export type QyAvailPerf = {
  avg_latency_ms: number | null
  avg_ttft_ms: number | null
  avg_tps: number | null
  latency_samples: number
  ttft_samples: number
  speed_samples: number
}

/**
 * 矩阵的一个格子（分组 × 模型）。
 *
 * `availability` 为 `null` 表示「无数据 / 样本不足」，前端**绝不能**渲染成 0%。
 */
export type QyAvailCell = QyAvailPerf & {
  group: string
  model: string
  availability: number | null
  state: QyAvailState
  req_total: number
  counted: number
  success: number
  excluded_total: number
  top_reason?: string
  has_channel: boolean
}

/**
 * 当前生效的可用率口径，随每次查询一并下发。
 *
 * 页面必须能在首屏回答「99.4% 是怎么算出来的」，否则上线后会持续收到
 * 「为什么我这边失败了这里显示 100%」的工单。
 */
export type QyAvailDefinition = {
  numerator: string
  denominator: QyAvailOutcome[]
  excluded: QyAvailOutcome[]
  count_client_errors: boolean
  count_rate_limited: boolean
  min_samples: number
  /** 延迟 / 首字 / 速度的样本下限。与 `min_samples` 不同，见后端 `perf.go`。 */
  perf_min_samples: number
  ok_threshold: number
  degraded_threshold: number
  note: string
}

export type QyAvailGroupInfo = {
  group: string
  desc: string
}

export type QyAvailMatrix = {
  start_ts: number
  end_ts: number
  bucket_seconds: number
  granularity: string
  definition: QyAvailDefinition
  groups: QyAvailGroupInfo[]
  models: string[]
  total_models: number
  page: number
  page_size: number
  /** 命中 `max_series_per_query` 上限，展示的格子不完整。 */
  truncated: boolean
  cells: QyAvailCell[]
  /** 当前页整体汇总，`group` 字段被复用为「最差分组」。 */
  overall: QyAvailCell
}

export type QyAvailSeriesPoint = QyAvailPerf & {
  ts: number
  availability: number | null
  state: QyAvailState
  req_total: number
  counted: number
  success: number
}

export type QyAvailSeriesLine = {
  group: string
  points: QyAvailSeriesPoint[]
}

export type QyAvailSeries = {
  model_name: string
  start_ts: number
  end_ts: number
  bucket_seconds: number
  granularity: string
  definition: QyAvailDefinition
  truncated: boolean
  series: QyAvailSeriesLine[]
}

/** 矩阵查询参数。`groups` / `models` 是逗号分隔的 CSV（后端 `splitCSV`）。 */
export type QyAvailMatrixParams = {
  hours: number
  groups?: string
  q?: string
  sort?: QyAvailSortMode
  page: number
  page_size: number
}

/**
 * 排序模式。默认按可用率升序 —— 打开页面先看最烂的那几个。
 *
 * 三个性能模式同样是「最差优先」：延迟 / 首字取最慢，速度取最慢。
 * **排序必须走服务端**：分页是按模型切的，前端只拿得到当前页，
 * 在本地重排永远排不出「全站最慢的那个模型」。
 */
export type QyAvailSortMode =
  | 'availability_asc'
  | 'latency_desc'
  | 'model_asc'
  | 'requests_desc'
  | 'tps_asc'
  | 'ttft_desc'

/** 矩阵的主指标。切换它只换展示与排序，不改变已经拿到的数据。 */
export type QyAvailMetricKey = 'availability' | 'latency' | 'tps' | 'ttft'
