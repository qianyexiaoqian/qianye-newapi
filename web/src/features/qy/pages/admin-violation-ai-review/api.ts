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
import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyAiChannelInput,
  QyAiChannelList,
  QyAiChannelTestResult,
  QyAiReviewLog,
  QyAiScopeInput,
  QyAiScopeList,
  QyAiSetting,
  QyAiSettingResponse,
  QyAiSettingSaveResult,
  QyAiStats,
} from './types'

export function qyAiChannelsQuery() {
  return {
    queryKey: qyKeys.adminViolationAiChannels(),
    queryFn: () =>
      qyGet<QyAiChannelList>('/admin/violation/ai-review/channels'),
  }
}

export function qyAiSettingsQuery() {
  return {
    queryKey: qyKeys.adminViolationAiSettings(),
    queryFn: () =>
      qyGet<QyAiSettingResponse>('/admin/violation/ai-review/settings'),
  }
}

export function qyAiStatsQuery(days: number) {
  return {
    queryKey: qyKeys.adminViolationAiStats(days),
    queryFn: () =>
      qyGet<QyAiStats>('/admin/violation/ai-review/stats', { days }),
  }
}

export function qyAiLogsQuery(params: { p: number; page_size: number }) {
  return {
    queryKey: qyKeys.adminViolationAiLogs(params),
    queryFn: () =>
      qyGet<QyPage<QyAiReviewLog>>('/admin/violation/ai-review/logs', params),
  }
}

export function createQyAiChannel(body: QyAiChannelInput) {
  return qyPost<unknown>('/admin/violation/ai-review/channels', body)
}

export function updateQyAiChannel(id: number, body: QyAiChannelInput) {
  return qyPut<unknown>(`/admin/violation/ai-review/channels/${id}`, body)
}

export function deleteQyAiChannel(id: number) {
  return qyDelete<unknown>(`/admin/violation/ai-review/channels/${id}`)
}

/**
 * 连通性试跑。它会发一次**真实**的出站调用(用一段固定的良性文本),
 * 所以后端给它挂了关键操作限流 —— 界面上不要做成自动触发。
 */
export function testQyAiChannel(id: number) {
  return qyPost<QyAiChannelTestResult>(
    `/admin/violation/ai-review/channels/${id}/test`,
    {}
  )
}

export function updateQyAiSetting(body: Omit<QyAiSetting, 'id'>) {
  return qyPut<QyAiSettingSaveResult>(
    '/admin/violation/ai-review/settings',
    body
  )
}

export function qyAiScopesQuery() {
  return {
    queryKey: qyKeys.adminViolationAiScopes(),
    queryFn: () => qyGet<QyAiScopeList>('/admin/violation/ai-review/scopes'),
  }
}

/** 新建与编辑同一个入口:请求体带 id 即为编辑(与违规类型同形)。 */
export function upsertQyAiScope(body: QyAiScopeInput) {
  return qyPut<unknown>('/admin/violation/ai-review/scopes', body)
}

export function deleteQyAiScope(id: number) {
  return qyDelete<unknown>(`/admin/violation/ai-review/scopes/${id}`)
}
