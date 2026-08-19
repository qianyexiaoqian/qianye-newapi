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

import { qyGet } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'

/**
 * 受限账号仍能到达的一档接口。
 *
 * `key` 是 i18n 键的后缀（`qy_ra_cap_<key>`），不是文案：同一句话要出 7 个语种，
 * 后端下发中文只会把它钉死成一种。分档表本身住在后端
 * （`qianye/controller/restricted_accounts.go`），与会话白名单同包，
 * 并由 Go 测试双向核对 —— 前端再抄一份就是"同一概念的第二份拷贝"。
 */
export type QyRestrictedCapability = {
  key: string
  /**
   * 这一档现在**真的**可达（承载它的模块开着）。
   *
   * 白名单是静态的，模块是可以关掉的：`ticket.enabled=false` 时那 9 条工单路由
   * 压根没注册，受限账号手里一条申诉通道都没有。只照白名单渲染的页面会告诉
   * 管理员"他能提工单"，然后用户点进去 404。
   */
  available: boolean
  /** 本档认领的白名单条目原文（`METHOD /path`），已排序。 */
  routes: string[]
}

export type QyRestrictedAccountsOverview = {
  /** 当前受限账号数（status=disabled 且未软删）。 */
  count: number
  capabilities: QyRestrictedCapability[]
}

/**
 * 把后端下发的形状收敛成组件能直接用的形状。
 *
 * 缺字段一律按"零/空"处理而不是抛错：这一页上真正不能缺的是公告表单（它自带
 * 错误态），总览只是一块信息卡 —— 让整页因为一个计数取不到而变成错误页，
 * 会把唯一的公告编辑入口一起挡掉。
 */
function normalize(raw: unknown): QyRestrictedAccountsOverview {
  const data = (raw ?? {}) as Partial<QyRestrictedAccountsOverview>
  const count = typeof data.count === 'number' ? data.count : 0
  const capabilities = Array.isArray(data.capabilities)
    ? data.capabilities.flatMap((item): QyRestrictedCapability[] => {
        if (item == null || typeof item.key !== 'string' || item.key === '') {
          return []
        }
        return [
          {
            key: item.key,
            available: item.available === true,
            routes: Array.isArray(item.routes)
              ? item.routes.filter(
                  (route): route is string => typeof route === 'string'
                )
              : [],
          },
        ]
      })
    : []
  return { count, capabilities }
}

export function qyAdminRestrictedAccountsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminRestrictedAccounts(),
    queryFn: async () => normalize(await qyGet('/admin/restricted-accounts')),
  })
}
