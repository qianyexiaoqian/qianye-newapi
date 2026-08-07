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

import { qyDelete, qyGet, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type {
  QyMgDeleteRequest,
  QyMgDeleteResult,
  QyMgImpact,
  QyMgListResponse,
  QyMgUpdateRequest,
} from './types'

const QY_MG_BASE = '/admin/group-namespace/model-groups'

/**
 * 分组名进 URL 段时**必须** `encodeURIComponent`。
 *
 * 站里有「浅夜の梦专属号池」这类名字，而分组名是运营自由输入的字符串：
 * 含 `/` 或 `#` 的名字不编码会把请求打到另一条路由上 —— 在删除这条路径上，
 * 那意味着删掉的是另一个分组，而返回值看起来一切正常。
 */
function path(name: string, suffix = '') {
  return `${QY_MG_BASE}/${encodeURIComponent(name)}${suffix}`
}

/**
 * 模型分组登记表。
 *
 * `staleTime: 0`：这一页每一行都带着「有没有渠道」「在不在全局可选清单里」
 * 这类实时事实，而它们正是删除闸门的判据。缓存一份旧清单的后果是运营对着
 * 一个已经没有渠道的绿色徽标做决定。
 */
export function qyMgListQuery() {
  return queryOptions({
    queryKey: qyKeys.adminModelGroups(),
    queryFn: () => qyGet<QyMgListResponse>(QY_MG_BASE),
    staleTime: 0,
  })
}

/**
 * 删除影响面。**与删除端点走同一段服务端探测**，不是前端拼出来的估算。
 *
 * 两者判据分家的表现是「预览说能删、点删除被拒」，或者更糟的
 * 「预览说没影响、删完一批人 403」。
 */
export function qyMgImpactQuery(name: string | null) {
  return queryOptions({
    queryKey: qyKeys.adminModelGroupImpact(name ?? ''),
    queryFn: () => qyGet<QyMgImpact>(path(name ?? '', '/impact')),
    enabled: name != null && name !== '',
    staleTime: 0,
  })
}

export function qyMgUpdate(name: string, body: QyMgUpdateRequest) {
  return qyPut<unknown>(path(name), body)
}

/**
 * 删除并清理全部引用。
 *
 * 返回值里的 `partial` 非空表示两库只写成了一半，调用方**必须**把它渲染出来 ——
 * 报成功然后走人是这套两库设计最坏的失败方式。
 */
export function qyMgDelete(name: string, body: QyMgDeleteRequest) {
  return qyDelete<QyMgDeleteResult>(path(name), body)
}
