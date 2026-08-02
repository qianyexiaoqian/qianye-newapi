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

import type {
  QyViolationAction,
  QyViolationFeeMode,
  QyViolationGroupScopeMode,
  QyViolationMatchType,
  QyViolationPhase,
  QyViolationRule,
  QyViolationRuleInput,
} from '../types'

/**
 * 规则表单的取值集合与校验。
 *
 * 校验规则逐条对应后端 `ValidateRule`。前端重写一遍不是为了替代后端
 * （后端仍然是唯一权威），而是因为这些约束里有三条会造成**静默失效**：
 * 保存成功、界面看着正常、但规则永远不会命中。管理员会以为规则太宽松而
 * 继续加码，直到某次配置变更把它们全部激活。
 */

export const QY_VIOLATION_PHASES: QyViolationPhase[] = [
  'prompt',
  'upstream_err',
  'reject_reason',
]

export const QY_VIOLATION_MATCH_TYPES: QyViolationMatchType[] = [
  'keyword',
  'regex',
  'request_rate',
  'error_code',
  'status_code',
  'upstream_text',
]

export const QY_VIOLATION_GROUP_SCOPE_MODES: QyViolationGroupScopeMode[] = [
  'include',
  'exclude',
]

export const QY_VIOLATION_ACTIONS: QyViolationAction[] = [
  'record',
  'charge',
  'block',
  'block_and_charge',
]

export const QY_VIOLATION_FEE_MODES: QyViolationFeeMode[] = [
  'none',
  'fixed',
  'model_price_multiple',
]

/** 只能用于上游阶段的匹配方式：prompt 阶段根本拿不到上游错误。 */
const UPSTREAM_ONLY_MATCH_TYPES = new Set<QyViolationMatchType>([
  'error_code',
  'status_code',
  'upstream_text',
])

/** `request_rate` 阈值的取值区间，与后端 `maxRequestRateThreshold` 同口径。 */
export const QY_VIOLATION_RATE_MIN = 1
export const QY_VIOLATION_RATE_MAX = 1_000_000

function isBlocking(action: QyViolationAction): boolean {
  return action === 'block' || action === 'block_and_charge'
}

function isCharging(action: QyViolationAction): boolean {
  return action === 'charge' || action === 'block_and_charge'
}

/** 非负十进制字符串。空串等价于 0（后端 `parseDecimal` 的约定）。 */
const DECIMAL_PATTERN = /^\d*(?:\.\d+)?$/

const decimalString = z
  .string()
  .refine((value) => DECIMAL_PATTERN.test(value.trim()), 'qy_vio_err_decimal')

export const qyViolationRuleSchema = z
  .object({
    name: z.string().trim().min(1, 'qy_vio_err_name_required').max(128),
    public_reason: z.string().max(128),
    remark: z.string().max(512),
    enabled: z.boolean(),
    dry_run: z.boolean(),
    priority: z.number().int().min(0).max(100000),
    phase: z.enum(['prompt', 'upstream_err', 'reject_reason']),
    match_type: z.enum([
      'keyword',
      'regex',
      'request_rate',
      'error_code',
      'status_code',
      'upstream_text',
    ]),
    pattern: z.string().max(8192, 'qy_vio_err_pattern_long'),
    case_sensitive: z.boolean(),
    model_scope: z.string().max(2048),
    group_scope: z.string().max(1024),
    group_scope_mode: z.enum(['include', 'exclude']),
    action: z.enum(['record', 'charge', 'block', 'block_and_charge']),
    fee_mode: z.enum(['none', 'fixed', 'model_price_multiple']),
    fee_fixed: decimalString,
    fee_multiple: decimalString,
    fee_max_quota: z.number().int().min(0),
    count_weight: z.number().int().min(0),
    severity: z.number().int().min(0).max(10),
    archive_context: z.boolean(),
    block_message: z.string().max(512),
  })
  .superRefine((data, ctx) => {
    // 阻断只在转发之前有意义：上游阶段字节已经发出去了。
    if (isBlocking(data.action) && data.phase !== 'prompt') {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['action'],
        message: 'qy_vio_err_block_phase',
      })
    }
    if (data.fee_mode !== 'none' && !isCharging(data.action)) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['fee_mode'],
        message: 'qy_vio_err_fee_action',
      })
    }
    if (
      data.phase === 'prompt' &&
      UPSTREAM_ONLY_MATCH_TYPES.has(data.match_type)
    ) {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['match_type'],
        message: 'qy_vio_err_match_phase',
      })
    }
    if (data.match_type !== 'request_rate') return
    // 频率判据数的是「即将发往上游的非流式请求」，只有转发前这一刻存在。
    // 挂在上游阶段的规则照样会执行，却只数得到失败的请求 —— 又一条
    // 保存成功、界面正常、线上永远不对的规则。
    if (data.phase !== 'prompt') {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['phase'],
        message: 'qy_vio_err_rate_phase',
      })
    }
    const threshold = Number(data.pattern.trim())
    if (
      !/^\d+$/.test(data.pattern.trim()) ||
      threshold < QY_VIOLATION_RATE_MIN ||
      threshold > QY_VIOLATION_RATE_MAX
    ) {
      // 阈值 0 会让每一个非流式请求都命中，包括计数失败时 fail-open 的那些。
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['pattern'],
        message: 'qy_vio_err_rate_pattern',
      })
    }
  })

export type QyViolationRuleFormValues = z.infer<typeof qyViolationRuleSchema>

/**
 * 新规则的默认值。
 *
 * `dry_run: true` 是刻意的：一条 `.*` 正则能在 30 秒内封掉全站用户。
 * 新规则默认只观察不执行，管理员确认命中分布之后再手动关掉。
 */
export function qyEmptyViolationRule(): QyViolationRuleFormValues {
  return {
    name: '',
    public_reason: '',
    remark: '',
    enabled: true,
    dry_run: true,
    priority: 100,
    phase: 'prompt',
    match_type: 'keyword',
    pattern: '',
    case_sensitive: false,
    model_scope: '',
    group_scope: '',
    group_scope_mode: 'include',
    action: 'record',
    fee_mode: 'none',
    fee_fixed: '0',
    fee_multiple: '0',
    fee_max_quota: 0,
    count_weight: 1,
    severity: 1,
    archive_context: false,
    block_message: '',
  }
}

export function qyViolationRuleToForm(
  rule: QyViolationRule
): QyViolationRuleFormValues {
  return {
    name: rule.name,
    public_reason: rule.public_reason,
    remark: rule.remark,
    enabled: rule.enabled,
    dry_run: rule.dry_run,
    priority: rule.priority,
    phase: rule.phase,
    match_type: rule.match_type,
    pattern: rule.pattern,
    case_sensitive: rule.case_sensitive,
    model_scope: rule.model_scope,
    group_scope: rule.group_scope,
    // 历史行（这一列出现之前写入的）可能是空串，按 include 读 ——
    // 那正是这一列出现之前的唯一语义。
    group_scope_mode:
      rule.group_scope_mode === 'exclude' ? 'exclude' : 'include',
    action: rule.action,
    fee_mode: rule.fee_mode,
    fee_fixed: rule.fee_fixed,
    fee_multiple: rule.fee_multiple,
    fee_max_quota: rule.fee_max_quota,
    count_weight: rule.count_weight,
    severity: rule.severity,
    archive_context: rule.archive_context,
    block_message: rule.block_message,
  }
}

/** 表单值 → 请求体。金额字段全程按字符串透传，不做任何数值运算。 */
export function qyViolationRuleToPayload(
  values: QyViolationRuleFormValues
): QyViolationRuleInput {
  return {
    ...values,
    name: values.name.trim(),
    fee_fixed: values.fee_fixed.trim() === '' ? '0' : values.fee_fixed.trim(),
    fee_multiple:
      values.fee_multiple.trim() === '' ? '0' : values.fee_multiple.trim(),
  }
}
