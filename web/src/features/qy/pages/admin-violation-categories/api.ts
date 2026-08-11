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
  QyViolationCategoryArchived,
  QyViolationCategoryImpact,
  QyViolationCategoryInput,
  QyViolationCategoryList,
  QyViolationCategorySaved,
  QyViolationThresholdApplied,
  QyViolationThresholdSuggestions,
} from './types'

/**
 * 类型清单 + 每一类当前绑着多少条规则。
 *
 * 一个接口同时返回两者是刻意的：规则条数只在归档确认弹窗里用（"这 N 条规则会被
 * 改绑到 X"），分两次取的话两次请求之间有人建了规则，那个数就是错的 ——
 * 而它正是管理员按下"归档"之前唯一会读的数字。
 *
 * `staleTime: 0`：类型不分页、行数是个位数，而"刚保存完还显示旧阈值"在一个
 * 直接决定谁被封号的页面上是不可接受的。
 */
export function qyAdminViolationCategoriesQuery() {
  return queryOptions({
    queryKey: qyKeys.adminViolationCategories(),
    queryFn: () => qyGet<QyViolationCategoryList>('/violation/categories'),
    staleTime: 0,
  })
}

/**
 * 「把这一类调成这套阈值，现在有多少存量账号已经越线」的只读预览。
 *
 * 独立成 GET 而不是只靠保存时的 409：管理员需要**在按下保存之前**看到这个数。
 * 一个只在保存失败时才出现的数字，会让人先按一次保存去探路 —— 那正是二次确认
 * 要防的动作。语义是「已越线的存量账号数」，**不是**「这次保存会封掉几个人」：
 * 保存只写类型表，他们会在各自下一次违规命中时才被处置。
 */
export function qyPreviewViolationCategoryImpact(params: {
  id: number
  threshold: number
  window_hours: number
  enabled: boolean
}) {
  return qyGet<{ impact: QyViolationCategoryImpact }>(
    '/violation/categories/impact',
    params
  )
}

/**
 * 新建 / 编辑同一个入口：`id` 为 0 表示新建。
 *
 * 收紧阈值时后端会回 **409 + confirm_required**，`data.impact` 里带着已越线的
 * 存量账号数。前端据此弹二次确认，再带 `confirm: true` 重发 —— 服务端不信任
 * 前端弹没弹窗，它信任的是"这一位只可能由那次交互产生"。
 */
export function qySaveViolationCategory(body: QyViolationCategoryInput) {
  return qyPut<QyViolationCategorySaved>('/violation/categories', body)
}

/**
 * **归档**，不是删除。
 *
 * 历史违规记录一行不动（那是证据，而且每一行都冻结了类型名与公示文案），
 * 这一类下面的规则会被改绑到 `reassignTo`，缺省落到「未分类」兜底类型。
 */
export function qyArchiveViolationCategory(id: number, reassignTo?: number) {
  const suffix =
    reassignTo == null || reassignTo <= 0 ? '' : `?reassign_to=${reassignTo}`
  return qyDelete<QyViolationCategoryArchived>(
    `/violation/categories/${id}${suffix}`
  )
}

/**
 * 内置类型的建议阈值 + 逐类影响面。
 *
 * 建议值只活在后端代码里，**不在出厂种子里** —— 种子一旦带值，升级上来的站点
 * 会在部署完成的那一秒开始按一套没有人设定过的线封人。这个接口是那条纪律的
 * 出口：我们把想过的线摆出来，落库要有人看着影响面按下确认。
 *
 * `staleTime: 0`：这里面每一个数字都是「按下确认会发生什么」的依据。
 */
export function qyViolationThresholdSuggestionsQuery() {
  return queryOptions({
    queryKey: qyKeys.adminViolationCategorySuggestions(),
    queryFn: () =>
      qyGet<QyViolationThresholdSuggestions>('/violation/categories/suggestions'),
    staleTime: 0,
  })
}

/**
 * 把建议线落库。**这个动作会真的改变谁会被封号。**
 *
 * 两条纪律在后端强制，前端只是照做：
 *   - **只补空**：只有当前阈值为 0 的类型会被写，管理员手填过的线绝不被顶掉；
 *   - **先出数**：`confirm` 为 false 时后端回 409 并带上完整预览。
 *
 * 应用本身不处置任何人（回执里的 `acts_immediately` 恒为 false）：写的只是类型表，
 * 已越线的账号会在各自下一次违规命中时才被处置。
 */
export function qyApplyViolationThresholdSuggestions(confirm: boolean) {
  return qyPost<QyViolationThresholdApplied>(
    '/violation/categories/apply-suggested',
    { confirm }
  )
}
