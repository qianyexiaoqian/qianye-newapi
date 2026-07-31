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
import { useQuery } from '@tanstack/react-query'

import { getQyConfigSnapshot, qyConfigQueryOptions } from '../lib/config-query'
import type { QyConfig } from '../lib/types'

/**
 * 扩展探测结果的三态。
 *
 * - `enabled`  后端明确回了 `enabled:true`
 * - `disabled` 后端明确回了 404 / `enabled:false` —— 前端必须**零痕迹**隐藏
 * - `unknown`  还在取数，或 503/网络错误 —— 沿用本地快照，不做破坏性动作
 *
 * 区分 `disabled` 与 `unknown` 是必要的：把"后端抖了一下"当成"功能被删了"，
 * 会让用户在服务恢复前一直看不到入口，还找不到原因。
 */
export type QyConfigStatus = 'disabled' | 'enabled' | 'unknown'

export type UseQyConfigResult = QyConfig & {
  status: QyConfigStatus
  /** 首次取数中（有本地快照时为 false，因此不会引起菜单闪烁）。 */
  isLoading: boolean
  /** 503 / 网络错误。扩展未启用**不算**错误。 */
  isError: boolean
  refetch: () => void
}

/**
 * 读取扩展配置。**所有扩展入口的渲染都必须依赖它。**
 *
 * 返回的 `enabled` / `features` 取的是"当前最可信的值"：
 * 取数成功用服务端值，失败或未完成用本地快照。这样冷启动时菜单不会
 * 先出现再消失，也不会因为一次 503 就整体塌掉。
 */
export function useQyConfig(): UseQyConfigResult {
  const query = useQuery(qyConfigQueryOptions())
  const config = query.data ?? getQyConfigSnapshot()

  let status: QyConfigStatus = 'unknown'
  if (query.isError) {
    status = 'unknown'
  } else if (query.data != null && !query.isPlaceholderData) {
    status = query.data.enabled ? 'enabled' : 'disabled'
  }

  return {
    ...config,
    status,
    isLoading: query.isLoading,
    isError: query.isError,
    refetch: query.refetch,
  }
}
