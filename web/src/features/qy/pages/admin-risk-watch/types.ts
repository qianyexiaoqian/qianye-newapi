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

/** 监听任务状态,与后端 `qianye/modules/riskwatch/model.go` 的常量逐字对应。 */
export type QyRwStatus = 'running' | 'stopped' | 'finished' | 'expired'

/** 时间窗形态。三种在库里都归一成 starts_at / ends_at,这一格只决定怎么回显。 */
export type QyRwWindowMode = 'forever' | 'range' | 'countdown'

/**
 * 一个监听任务。
 *
 * 作用域三格(`target_user_id` / `target_group` / `target_model`)零值 = 不限,
 * 但**至少要填一格** —— 三格全空建出来的是一个全站监听任务,后端会用
 * `qy_rw_scope_empty` 拒绝。
 */
export type QyRwTask = {
  id: number
  name: string
  note: string
  target_user_id: number
  /** 建任务那一刻的用户名快照,只用于展示;用户改名后会过期。 */
  target_username: string
  target_group: string
  target_model: string
  /** 记录概率,万分比:10000 = 100%。 */
  sample_bps: number
  /** 抽满多少条自动停止,0 = 不限。 */
  max_records: number
  captured: number
  window_mode: QyRwWindowMode
  starts_at: number
  ends_at: number
  countdown_seconds: number
  /**
   * 保留天数。`null` = 跟随全局默认,`0` = 永久保留。
   *
   * 三个语义值,所以它是可空的:把"没填"和"填了 0"折成同一个数会让
   * 一份要跟着仲裁走完的取证材料在第 31 天被清掉。
   */
  retention_days: number | null
  status: QyRwStatus
  stopped_at: number
  stopped_reason: string
  created_by: number
  /** 乐观锁。编辑时必须原样带回,否则后端返回 409。 */
  version: number
  created_at: number
  updated_at: number
}

/** 列表页的一行监听记录,**不含正文**。正文点开详情才取。 */
export type QyRwCaptureBrief = {
  id: number
  task_id: number
  user_id: number
  username: string
  token_id: number
  token_name: string
  user_group: string
  model_name: string
  request_id: string
  client_ip: string
  is_stream: boolean
  /** 转发前的**预估**输入 token 数,不是结算值。 */
  prompt_tokens: number
  /** 截断**之前**的字符数。 */
  content_chars: number
  truncated: boolean
  has_files: boolean
  created_at: number
  /** 这一行的清理时刻,0 = 永久保留。 */
  expires_at: number
}

/** 详情接口返回的完整记录,比列表多 `content` 与 `files` 两格。 */
export type QyRwCapture = QyRwCaptureBrief & {
  content: string
  /** 多模态输入的描述符 JSON 数组(MIME / 字节数 / SHA256),**不含二进制本体**。 */
  files: string
}

/** 存储节点健康读数。没配时只有 `configured: false` 一个键。 */
export type QyRwStoreStats = {
  configured: boolean
  connected?: boolean
  available?: boolean
  open_conns?: number
  in_use?: number
  max_open?: number
  last_ping_ms?: number
}

export type QyRwStats = {
  tasks: number
  running: number
  captures: number
  max_active_tasks: number
  max_retention_days: number
  default_retention: number
  capture_max_chars: number
  store: QyRwStoreStats
}

/** 新建 / 编辑任务的请求体。 */
export type QyRwTaskPayload = {
  name: string
  note: string
  target_user_id: number
  target_group: string
  target_model: string
  sample_bps: number
  max_records: number
  window_mode: QyRwWindowMode
  starts_at: number
  ends_at: number
  countdown_seconds: number
  retention_days: number | null
  version: number
}
