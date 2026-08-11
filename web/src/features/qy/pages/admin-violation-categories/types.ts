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
  /** 内部说明。**绝不**渲染进任何面向用户的位置,也**不**进 AI 提示词。 */
  remark: string
  public_title: string
  public_desc: string
  /**
   * 给审核模型看的判定说明。**第三份文本**,与 `remark`、`public_*` 各去各的:
   *
   *   remark        → 只有管理端。写运营口径。
   *   public_title  → 用户端。绝不写判据。
   *   public_desc
   *   ai_guidance   → 随提示词发往第三方审核服务。写判据。不进用户端。
   *
   * 拿公示文案当判定说明会让模型判得更差(它刻意不含判据);反过来把判定说明
   * 公示出去等于把绕过方法印给用户。
   */
  ai_guidance: string
  /**
   * 这一类**不**出现在发给审核模型的类型清单里。
   *
   * 只给判据不是文本的类型用:蒸馏看的是请求频率、上游拒绝看的是上游 4xx。
   * 把它们放进清单,模型会对着单条请求猜,而猜出来的那一票会加到一个语义
   * 完全不同的计数上。
   */
  ai_excluded: boolean
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

/**
 * 阈值三态。**由后端下发，前端不要自己从 (enabled, threshold) 推。**
 *
 * 判定侧 `unset` 与 `disabled` 完全等价（两者都不出线），正因为等价才容易被
 * 写成同一个分支 —— 而那一塌就是项目方看到的现象：六个类型全显示一个 0，
 * 分不出「还没配」与「配了但关着」，于是「到多少次封号」在界面上等于不存在。
 *
 *   - `unset`    从来没配过线。命中照常计数、照常计入账号总量线；
 *   - `disabled` 配过线但阈值开关关着。数字还在，当下不生效；
 *   - `active`   线正在生效。
 */
export type QyViolationThresholdState = 'unset' | 'disabled' | 'active'

export type QyViolationCategoryRow = {
  category: QyViolationCategory
  /** 当前绑在这一类上的规则条数（兜底那一行含 category_id=0 的历史规则）。 */
  rule_count: number
  threshold_state: QyViolationThresholdState
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
  ai_guidance: string
  ai_excluded: boolean
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

/**
 * 一条建议阈值。
 *
 * `why` 会原样显示在弹窗里：一个没有理由的数字，管理员只能全盘照抄或全盘不用，
 * 而这两种都不是「按自己站点的情况拍板」。
 *
 * `applicable === false` 时 `skip_reason` 必有值 —— 一个只灰不给理由的行会被
 * 当成 bug，而管理员的下一步就是绕过界面直接改库。
 */
export type QyViolationThresholdSuggestion = {
  id: number
  key: string
  name: string
  current_threshold: number
  current_window_hours: number
  current_enabled: boolean
  state: QyViolationThresholdState
  suggested_threshold: number
  suggested_window_hours: number
  why: string
  applicable: boolean
  skip_reason: string
  /** 按建议线，这一类现在有多少存量账号**已经处在越线状态**。 */
  impact: QyViolationCategoryImpact
}

/**
 * 建议阈值预览。
 *
 * `affected_users` 是**去重后**的账号数：逐类相加会把同时越两类线的人算两次，
 * 而这个数正是管理员按下确认之前唯一会读的东西。
 *
 * `account_action` 是这些人越线之后会被**怎么**处置（来自兜底策略档）。类型线
 * 只决定「几次」，动作一律由用户所在分组的策略档决定 —— 不显示它，确认弹窗
 * 就只能说「会触发处置」，而站点当前的兜底动作可能正是封号。
 */
export type QyViolationThresholdSuggestions = {
  items: QyViolationThresholdSuggestion[]
  applicable_count: number
  affected_users: number
  capped: boolean
  account_action: string
  account_threshold: number
  threshold_semantics: string
}

/** 应用回执。`acts_immediately` 恒为 false —— 应用只写类型表，不处置任何人。 */
export type QyViolationThresholdApplied = {
  applied: {
    id: number
    key: string
    name: string
    threshold: number
    window_hours: number
  }[]
  applied_count: number
  affected_users: number
  acts_immediately: boolean
}

/** 归档回执。`records_intact` 恒为 true —— 历史违规记录是证据，绝不级联删除。 */
export type QyViolationCategoryArchived = {
  archived: boolean
  reassigned_to: number
  moved_rules: number
  records_intact: boolean
}
