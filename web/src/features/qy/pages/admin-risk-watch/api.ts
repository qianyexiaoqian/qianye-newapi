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
import { queryOptions } from '@tanstack/react-query'

import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyRwCapture,
  QyRwCaptureBrief,
  QyRwStats,
  QyRwTask,
  QyRwTaskPayload,
} from './types'

/**
 * 任务列表。它比 {@link QyPage} 多带三个上限值。
 *
 * 上限跟着列表一起下发,前端不自己抄一份:抄一份的结果是后端把
 * `max_active_tasks` 从 50 调到 10 之后,页面还在让人建第 11 个,
 * 直到提交才被 `qy_rw_too_many_active` 拒掉。
 */
export type QyRwTaskPage = QyPage<QyRwTask> & {
  max_active_tasks: number
  max_retention_days: number
  default_retention: number
}

export function qyRiskWatchTasksQuery(params: {
  p: number
  page_size: number
  status?: string
  target_user_id?: number
}) {
  return queryOptions({
    queryKey: qyKeys.adminRiskWatchTasks(params),
    queryFn: () => qyGet<QyRwTaskPage>('/admin/risk-watch/tasks', params),
  })
}

export function qyRiskWatchCapturesQuery(params: {
  p: number
  page_size: number
  task_id?: number
  user_id?: number
  model_name?: string
}) {
  return queryOptions({
    queryKey: qyKeys.adminRiskWatchCaptures(params),
    queryFn: () =>
      qyGet<QyPage<QyRwCaptureBrief>>('/admin/risk-watch/captures', params),
  })
}

/**
 * 一条记录的完整正文。
 *
 * 调用方必须自己带 `enabled: id > 0`:正文是这一页唯一一段用户原文,没有理由
 * 在列表渲染时就把整页的内容全拉下来 —— 那既是流量,也是一次说不清楚的
 * "谁在批量读用户内容"。
 */
export function qyRiskWatchCaptureQuery(id: number) {
  return queryOptions({
    queryKey: qyKeys.adminRiskWatchCapture(id),
    queryFn: () => qyGet<QyRwCapture>(`/admin/risk-watch/captures/${id}`),
  })
}

export function qyRiskWatchStatsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminRiskWatchStats(),
    queryFn: () => qyGet<QyRwStats>('/admin/risk-watch/stats'),
  })
}

export function qyCreateRiskWatchTask(payload: QyRwTaskPayload) {
  return qyPost<QyRwTask>('/admin/risk-watch/tasks', payload)
}

export function qyUpdateRiskWatchTask(id: number, payload: QyRwTaskPayload) {
  return qyPut<QyRwTask>(`/admin/risk-watch/tasks/${id}`, payload)
}

/**
 * 停止 / 启动。
 *
 * `version` 走查询串而不是请求体:两个动作都没有请求体,而为了带一个版本号
 * 造一个只有一格的 JSON,会让"这两条是幂等的状态迁移"这件事在接口形状上失真。
 * 后端把它当**可选**校验 —— 传了就核对,核不上返回 409。
 */
export function qyStopRiskWatchTask(id: number, version: number) {
  return qyPost<QyRwTask>(
    `/admin/risk-watch/tasks/${id}/stop?version=${version}`,
    {}
  )
}

export function qyStartRiskWatchTask(id: number, version: number) {
  return qyPost<QyRwTask>(
    `/admin/risk-watch/tasks/${id}/start?version=${version}`,
    {}
  )
}

/** 删除任务,**连同它的全部记录**。不可逆。 */
export function qyDeleteRiskWatchTask(id: number) {
  return qyDelete<{ deleted: boolean }>(`/admin/risk-watch/tasks/${id}`)
}
