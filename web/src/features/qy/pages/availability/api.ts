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
import { qyGet } from '../../lib/api'
import type { QyAvailMatrix, QyAvailMatrixParams, QyAvailSeries } from './types'

/**
 * 分组 × 模型可用率矩阵。
 *
 * 分组白名单在服务端强制裁剪，`groups` 参数只用于「在可见集合内进一步收窄」，
 * 因此这里可以放心把用户勾选的分组原样传上去。
 */
export function getQyAvailabilityMatrix(
  params: QyAvailMatrixParams
): Promise<QyAvailMatrix> {
  return qyGet<QyAvailMatrix>('/availability/matrix', params)
}

/** 单个模型在各可见分组下的可用率趋势。`groups` 为逗号分隔 CSV。 */
export function getQyAvailabilitySeries(params: {
  model: string
  hours: number
  groups?: string
}): Promise<QyAvailSeries> {
  return qyGet<QyAvailSeries>('/availability/series', params)
}
