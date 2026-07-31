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

import { qyGet, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyUserGroupConfig } from './types'

export function qyUserGroupConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.adminUserGroupConfig(),
    queryFn: () => qyGet<QyUserGroupConfig>('/admin/user-group/config'),
  })
}

/**
 * 保存新用户默认分组。
 *
 * 空串是合法值，表示取消配置、回到上游默认行为。因此这里必须始终把字段发出去，
 * 不能用「空就不传」的稀疏写法 —— 后端把缺字段判成 400，正是为了区分
 * 「我要清空」和「我漏传了」。
 */
export function qySaveUserGroupConfig(defaultGroup: string) {
  return qyPut<{ default_group: string; effective_group: string }>(
    '/admin/user-group/config',
    { default_group: defaultGroup }
  )
}
