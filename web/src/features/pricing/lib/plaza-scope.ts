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
// ----------------------------------------------------------------------------
// Model Square scope helpers (qy)
// ----------------------------------------------------------------------------

/**
 * 模型广场"一个模型都没有"的三种成因。
 *
 * 分开是因为给用户的那句话完全不同：
 *
 * - `filters`         用户自己筛没了 —— 该提示他清空筛选，页面上有按钮可点。
 * - `anonymous-scope` 后端按"新用户注册后的默认分组"渲染，而那一档确实没有任何
 *                     可用模型分组。清空筛选不会有任何变化，让他去点"清空筛选"
 *                     是把人往死路上引；正确的出口是登录后看自己那一档。
 * - `none`            有内容，不需要空态。
 *
 * 后端在这种情况下**刻意**返回空列表而不是回落全量（见
 * controller/qy_plaza_viewer.go 的边界 2），前端必须把这个"空"解释清楚，
 * 否则那条选择就变成了"站点看起来坏了"。
 */
export type PlazaEmptyReason = 'none' | 'filters' | 'anonymous-scope'

export function plazaEmptyReason(input: {
  /** 后端 /api/pricing 的 anonymous_preview：本次响应按注册默认分组渲染。 */
  anonymousPreview: boolean
  /** 后端下发的模型总数（未经前端筛选）。 */
  totalModels: number
  /** 当前筛选之后剩下的模型数。 */
  filteredModels: number
}): PlazaEmptyReason {
  if (input.filteredModels > 0) return 'none'
  // 总数为 0 时筛选条件与结果无关：不管清不清空都还是空的。
  if (input.totalModels === 0 && input.anonymousPreview) {
    return 'anonymous-scope'
  }
  return 'filters'
}
