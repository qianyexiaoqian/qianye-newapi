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
 * 划转分组限制的 DTO。字段与 `qianye/modules/transfer/grouprule.go`、
 * `api_group_rules.go` 一一对应。
 */

/**
 * 规则策略。
 *
 * 这三样（策略枚举、通配符、`@self`）刻意在前端硬编码，而不是由后端下发：
 * 每个策略都要有一条对应的 i18n 文案（`qy_trg_policy_*`），前端拿到一个它不
 * 认识的策略也只能渲染出裸 key。后端因此也不下发它们 —— 那会变成一份没有
 * 消费方的数据。
 *
 * **代价是必须与 `qianye/modules/transfer/grouprule.go` 手工保持同步**：
 * 后端新增一种策略时，这里加取值、`lib/rule-form.ts` 加进下拉、
 * `i18n/qy/*.json` 加两条文案（`qy_trg_policy_x` 与 `qy_trg_policy_x_desc`）。
 */
export type QyGroupPolicy =
  | 'allow_all'
  | 'allow_list'
  | 'deny_all'
  | 'deny_list'

/** 兜底规则的 `from_group`：匹配所有没有专属规则的分组。 */
export const QY_GROUP_WILDCARD = '*'

/**
 * `to_groups` 里的「与发起方同组」令牌。
 *
 * 有了它，「只能转给同组」= allow_list + [@self]，「禁止组内互转」=
 * deny_list + [@self] —— 这两种最常见的形态不需要为每个分组各写一条规则。
 */
export const QY_GROUP_SELF_TOKEN = '@self'

export type QyTransferGroupRule = {
  id: number
  /** 发起方分组；`*` 表示兜底规则。 */
  from_group: string
  policy: QyGroupPolicy
  /** 逗号分隔的目标分组名单，可含 `@self`。allow_all / deny_all 时后端会清空它。 */
  to_groups: string
  /** false 时视同该规则不存在，因此会落到兜底规则（若有）上。 */
  enabled: boolean
  remark: string
  created_at: number
  updated_at: number
  updated_by: number
}

/**
 * 「谁能转给谁」矩阵的一行。
 *
 * `to_groups` 是**后端逐格用真正的判定函数算出来的**结果，不是前端从规则推的：
 * 兜底规则、`@self`、黑名单三者叠加之后，前端自己推极易与后端分家，
 * 而那会让管理员放心地配错。
 */
export type QyTransferGroupMatrixRow = {
  from_group: string
  /** 0 表示这个分组没有被任何规则覆盖。 */
  rule_id: number
  /** 规则原样的策略，外加 `unrestricted` 表示「没有规则覆盖」。 */
  policy: QyGroupPolicy | 'unrestricted'
  /** 在已知分组范围内实际可以转入的分组。 */
  to_groups: string[]
}

export type QyTransferGroupRulesPage = {
  items: QyTransferGroupRule[]
  /** 运营配置过的分组候选清单。表单允许自由填写，这里只是下拉建议。 */
  known_groups: string[]
  matrix: QyTransferGroupMatrixRow[]
  /** 规则条数上限。到顶时禁用「新建」并说明原因，而不是让人保存后才吃 400。 */
  max_rule_count: number
}

/** 新建 / 编辑入参。 */
export type QyTransferGroupRuleInput = {
  from_group: string
  policy: QyGroupPolicy
  to_groups: string
  enabled: boolean
  remark: string
}
