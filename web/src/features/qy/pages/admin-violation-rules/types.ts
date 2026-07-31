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
 * 与 `qianye/modules/violation/model.go` + `api_admin.go` 对齐。
 *
 * 金额与倍数一律是 **string**：后端用 `shopspring/decimal`，默认序列化成带引号的
 * 字符串；同时入参也刻意收 string —— JSON number 在前端是 float64，`0.1`
 * 往返一次会变成 `0.10000000000000001`，而这个值会被直接乘进用户的账单。
 * 前端全程按字符串透传，绝不 `parseFloat` 后再回写。
 */

/** 规则生效阶段。 */
export type QyViolationPhase = 'prompt' | 'reject_reason' | 'upstream_err'

/** 匹配方式。后三种只能用于上游阶段（prompt 阶段拿不到上游错误）。 */
export type QyViolationMatchType =
  | 'error_code'
  | 'keyword'
  | 'regex'
  | 'status_code'
  | 'upstream_text'

/** 处置动作。含 block 的动作只在 prompt 阶段有意义。 */
export type QyViolationAction =
  | 'block_and_charge'
  | 'block'
  | 'charge'
  | 'record'

export type QyViolationFeeMode = 'fixed' | 'model_price_multiple' | 'none'

export type QyViolationRule = {
  id: number
  name: string
  remark: string
  /** 写给用户看的对外文案。与内部 `name` 分开，后者常含规则代号。 */
  public_reason: string
  enabled: boolean
  /** 规则级影子：新规则可以先灰度观察，不必打开全局 shadow_mode。 */
  dry_run: boolean
  priority: number
  phase: QyViolationPhase
  match_type: QyViolationMatchType
  pattern: string
  case_sensitive: boolean
  model_scope: string
  group_scope: string
  action: QyViolationAction
  fee_mode: QyViolationFeeMode
  fee_fixed: string
  fee_multiple: string
  fee_max_quota: number
  count_weight: number
  severity: number
  archive_context: boolean
  block_message: string
  created_at: number
  updated_at: number
  created_by: number
  updated_by: number
}

/** 新建 / 编辑的请求体（`ruleUpsertReq`）。 */
export type QyViolationRuleInput = {
  name: string
  remark: string
  public_reason: string
  enabled: boolean
  dry_run: boolean
  priority: number
  phase: QyViolationPhase
  match_type: QyViolationMatchType
  pattern: string
  case_sensitive: boolean
  model_scope: string
  group_scope: string
  action: QyViolationAction
  fee_mode: QyViolationFeeMode
  fee_fixed: string
  fee_multiple: string
  fee_max_quota: number
  count_weight: number
  severity: number
  archive_context: boolean
  block_message: string
}

/** 规则试跑结果。`scope_ok=false` 表示作用域没覆盖到试跑用的模型/分组。 */
export type QyViolationRuleTestResult = {
  scope_ok: boolean
  matched: boolean
  terms: string[]
  snippet: string
  elapsed_us?: number
}

/**
 * 熔断与影子模式状态。
 *
 * `shadow=true` 表示当前**只记录、不扣费、不阻断、不封号**。这是规则编辑
 * 界面必须最醒目展示的一件事：不知道自己在影子模式下改规则，会以为规则没生效
 * 而不断加码，等到关掉影子模式就是一次全站事故。
 */
export type QyViolationBreaker = {
  shadow: boolean
  /** `config` = YAML 配置的影子；其余为熔断自动回落时的触发原因。 */
  shadow_reason: string
  config_shadow: boolean
  forced_shadow_until: number
  forced_shadow_count: number
  window_scanned: number
  window_blocked: number
  block_rate_limit_bps: number
  ban_window_count: number
  ban_rate_limit_hour: number
  scan_total: number
  block_total: number
  shadow_hits: number
  record_drops: number
  scan_timeouts: number
  rule_refresh_fails: number
}

export type QyViolationStatBucket = {
  key: string
  cnt: number
  fee_quota: number
}

export type QyViolationStats = {
  hours: number
  record_count: number
  blocked: number
  /** 影子模式下的命中量：切真实模式前唯一的决策依据。 */
  shadow_count: number
  fee_quota: number
  clamp_count: number
  ban_count: number
  by_rule: QyViolationStatBucket[]
  by_model: QyViolationStatBucket[]
  breaker: QyViolationBreaker
  rules: {
    version: number
    loaded_at: number
    prompt_rule: number
    post_rule: number
  }
  policy: {
    insufficient_balance: string
    auto_ban_threshold: number
    auto_ban_window_h: number
    max_fee_quota: number
  }
}
