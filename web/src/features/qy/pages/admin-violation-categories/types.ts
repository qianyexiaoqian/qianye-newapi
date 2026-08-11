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
/**
 * 违规类型（管理端）。
 *
 * 两组文案泾渭分明，界面上也必须分开摆：
 *   - `name` / `remark` 是**内部**的，写匹配口径与误杀场景，只有管理端看得到；
 *   - `public_title` / `public_desc` 是**对外公示**的，用户端只拿得到这两列。
 *
 * 把内部说明抄进公示文案等于把绕过方法印给用户，所以表单上这两组之间有一条
 * 明确的分隔与提示（见 `category-form-sheet.tsx`）。
 */
export type QyViolationCategory = {
  id: number
  /** 稳定业务键。外部审核来源（AI 审核）按它绑定类型，改名不影响它。 */
  key: string
  name: string
  /** 内部说明。**绝不**渲染进任何面向用户的位置。 */
  remark: string
  public_title: string
  public_desc: string
  published: boolean
  /** 这一类的**阈值**是否生效。false 等价于 threshold=0，计数照常累加。 */
  enabled: boolean
  window_hours: number
  /** 该类型累计多少次触发处置。0 = 这一类不单独触发。 */
  threshold: number
  sort_order: number
  /** 「未分类」兜底类型：不可归档，标识不可改。 */
  is_fallback: boolean
  created_at: number
  updated_at: number
}

export type QyViolationCategoryRow = {
  category: QyViolationCategory
  /** 当前绑在这一类上的规则条数（兜底那一行含 category_id=0 的历史规则）。 */
  rule_count: number
}

export type QyViolationCategoryList = {
  items: QyViolationCategoryRow[]
  fallback_id: number
  /**
   * 阈值口径，由后端下发而不是前端写死。
   *
   * `any_line` = 账号总量线与单类型线是 OR，任一越过即触发处置。判定侧改了口径
   * 而文案没改，界面上就是一句谎话，所以这句话的源头只能有一个。
   */
  threshold_semantics: string
}

/** 影响面预览：**已越线的存量账号数**，不是「这次保存会处置几个人」。 */
export type QyViolationCategoryImpact = {
  matched: number
  capped: boolean
  threshold: number
  window_hours: number
  user_ids: number[]
}

export type QyViolationCategoryInput = {
  id: number
  key: string
  name: string
  remark: string
  public_title: string
  public_desc: string
  published: boolean
  enabled: boolean
  window_hours: number
  threshold: number
  sort_order: number
  /** 二次确认位。收紧阈值时后端会先回 409，带上影响面。 */
  confirm: boolean
}

export type QyViolationCategorySaved = {
  category: QyViolationCategory
  impact: QyViolationCategoryImpact
}

/** 归档回执。`records_intact` 恒为 true —— 历史违规记录是证据，绝不级联删除。 */
export type QyViolationCategoryArchived = {
  archived: boolean
  reassigned_to: number
  moved_rules: number
  records_intact: boolean
}
