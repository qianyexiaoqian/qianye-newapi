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
import type { QyTransferAdminConfig } from './types'

export function qyAdminTransferConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.adminTransferConfig(),
    queryFn: () => qyGet<QyTransferAdminConfig>('/admin/transfer/config'),
  })
}

/**
 * 修改划转门槛。
 *
 * 请求体是稀疏 map，**只传改动过的键** —— 后端逐键写 `qy_settings` 并写一条
 * 审计，把没改的键一起发过去会污染「谁在什么时候把日额度从 2 亿改成 20 亿」
 * 的追溯轨迹。
 *
 * 取值用字符串发送。划转的门槛都是整数、不存在浮点失真问题，但后端两种写法
 * 都收，而字符串是唯一不会在任何客户端上被改写的形状。
 *
 * 后端整批包在一个事务里：要么全生效要么全不生效。调用方成功后重新 GET 一次
 * 即可，别去适配响应体。
 */
export function qyUpdateTransferConfig(patch: Record<string, string>) {
  return qyPut<unknown>('/admin/transfer/config', patch)
}
