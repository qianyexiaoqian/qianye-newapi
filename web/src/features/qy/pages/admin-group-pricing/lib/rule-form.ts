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
import { z } from 'zod'

import type { QyGpMode, QyGpRule, QyGpRuleInput } from '../types'
import { qyParseDecimal } from './pricing-math'

/**
 * 分组定价规则的表单契约。
 *
 * 与本项目其它规则表单同样的分工：**权威判定在后端**
 * （`grouppricing.ValidateValue` 里的各口径上界、小数位、tiered 不得为 0、
 * (分组,模型) 唯一性），前端只做能让人立刻改对的那一层，后端 400 的中文原文
 * 由 `qyOpsErrorMessage` 原样透出。两处各写一套判定必然漂移。
 *
 * 前端唯一坚持自己也拦一次的是**负数**：负价等于给用户充值，是 `AGENTS.md`
 * 写死的计费不变量。这种输入不该有机会被提交出去。
 */

/** 三种口径。顺序即下拉顺序：按次价最直观，阶梯乘数最少用。 */
export const QY_GP_MODES: QyGpMode[] = ['price', 'ratio', 'tiered']

/** 「该分组下全部模型」。与后端 `modelWildcard` 一致。 */
export const QY_GP_MODEL_WILDCARD = '*'

export const qyGpRuleSchema = z.object({
  group_name: z
    .string()
    .trim()
    .min(1, 'qy_gp_err_group_required')
    .max(64, 'qy_gp_err_group_too_long'),
  model_name: z
    .string()
    .trim()
    .min(1, 'qy_gp_err_model_required')
    .max(128, 'qy_gp_err_model_too_long'),
  mode: z.enum(['price', 'ratio', 'tiered']),
  /**
   * 覆盖值。含义随 `mode` 变化，因此只有一个输入框 —— 同时留着价格与倍率
   * 两个值，下一个人打开这条规则时无从判断哪个才生效。
   */
  value: z
    .string()
    .trim()
    .min(1, 'qy_gp_err_value_required')
    .refine((raw) => qyParseDecimal(raw) != null, 'qy_gp_err_value_invalid')
    .refine((raw) => {
      const parsed = qyParseDecimal(raw)
      return parsed == null || parsed.sign > 0 || parsed.digits === 0n
    }, 'qy_gp_err_value_negative'),
  enabled: z.boolean(),
  remark: z.string().max(255, 'qy_gp_err_remark_too_long'),
})

export type QyGpRuleFormValues = z.infer<typeof qyGpRuleSchema>

/**
 * 新建时的初值。
 *
 * `enabled: false` 是刻意的：一条建出来就生效的价格规则，会在管理员看清
 * 「× 分组倍率 = 实际扣费」之前就改变扣费金额。先存下来、在列表里核对折算
 * 结果，再回来打开它。
 */
export function qyEmptyGpRule(defaultGroup: string): QyGpRuleFormValues {
  return {
    group_name: defaultGroup,
    model_name: '',
    mode: 'price',
    value: '',
    enabled: false,
    remark: '',
  }
}

export function qyGpRuleToForm(rule: QyGpRule): QyGpRuleFormValues {
  return {
    group_name: rule.group_name,
    model_name: rule.model_name,
    mode: rule.mode,
    value: rule.value,
    enabled: rule.enabled,
    remark: rule.remark,
  }
}

/** 表单值 → 请求体。`value` 原样传字符串，不经过 `Number()`。 */
export function qyGpRuleToPayload(values: QyGpRuleFormValues): QyGpRuleInput {
  return {
    group_name: values.group_name.trim(),
    model_name: values.model_name.trim(),
    mode: values.mode,
    value: values.value.trim(),
    enabled: values.enabled,
    remark: values.remark.trim(),
  }
}
