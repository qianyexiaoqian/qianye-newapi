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
  QyTransferGroupLimitInput,
  QyTransferGroupLimitsPage,
} from './group-limits-types'

export function qyAdminTransferGroupLimitsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminTransferGroupLimits(),
    queryFn: () =>
      qyGet<QyTransferGroupLimitsPage>('/admin/transfer/group-limits'),
  })
}

/**
 * 新建或整行覆盖一档。
 *
 * `overrides` 必须把**全部七个可分档键**都带上，没有覆盖的那些显式写 `null` ——
 * 后端是整行替换,缺席与 `null` 同义。这一点由调用方在提交前展开,
 * 不在这里替它补:替它补就等于这一层也开始猜"运营到底想不想覆盖这一项"。
 */
export function qyPutTransferGroupLimit(input: QyTransferGroupLimitInput) {
  return qyPut<unknown>('/admin/transfer/group-limits', input)
}

/**
 * 删掉一档,该分组回落全站门槛。
 *
 * 分组名走 query 而不是路径段:分组名允许包含 `/`,放进路径会被路由器切开,
 * 表现是「删不掉、也没有报错」。
 */
export function qyDeleteTransferGroupLimit(userGroup: string) {
  return qyDelete<unknown>(
    `/admin/transfer/group-limits?user_group=${encodeURIComponent(userGroup)}`
  )
}
