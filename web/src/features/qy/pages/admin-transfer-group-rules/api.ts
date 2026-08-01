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
import type {
  QyTransferGroupRuleInput,
  QyTransferGroupRuleSaved,
  QyTransferGroupRulesPage,
} from './types'

/**
 * 规则清单 + 已解析的「谁能转给谁」矩阵。
 *
 * 一个接口同时返回两者是刻意的：分两个接口取，两次请求之间只要有人改了规则，
 * 管理员看到的矩阵就与他正在编辑的规则对不上。
 *
 * `staleTime: 0`：规则不分页、行数是个位数，而「刚保存完还显示旧的」在一个
 * 直接决定资金流向的页面上是不可接受的。
 */
export function qyAdminTransferGroupRulesQuery() {
  return queryOptions({
    queryKey: qyKeys.adminTransferGroupRules(),
    queryFn: () =>
      qyGet<QyTransferGroupRulesPage>('/admin/transfer/group-rules'),
    staleTime: 0,
  })
}

/**
 * 保存成功的回执里带 `unknown_groups`。
 *
 * 它是**软告警**：分组名不在站点定义清单里不构成拒绝（历史分组必须仍然能配
 * 规则），因此它出现在 200 的响应体里而不是 400 的错误里。
 */
export function qyCreateTransferGroupRule(body: QyTransferGroupRuleInput) {
  return qyPost<QyTransferGroupRuleSaved>('/admin/transfer/group-rules', body)
}

export function qyUpdateTransferGroupRule(
  id: number,
  body: QyTransferGroupRuleInput
) {
  return qyPut<QyTransferGroupRuleSaved>(
    `/admin/transfer/group-rules/${id}`,
    body
  )
}

/** 硬删。后端在删除前把完整内容写进了审计的 before 快照。 */
export function qyDeleteTransferGroupRule(id: number) {
  return qyDelete<{ id: number }>(`/admin/transfer/group-rules/${id}`)
}
