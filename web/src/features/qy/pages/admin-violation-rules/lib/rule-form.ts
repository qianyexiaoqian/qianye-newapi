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

import {
  qyAppendGroupName,
  qySplitGroupNames,
} from '../../../lib/group-options'
import type {
  QyViolationAction,
  QyViolationFeeMode,
  QyViolationMode,
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
  // 转发后(异步)审核。它必须在这张清单里 —— 否则 AI 审核的第二个时机在
  // 界面上根本选不到,而后端支持它:本仓已经四次栽在"写了但找不到"上。
  'post_async',
]

export const QY_VIOLATION_MATCH_TYPES: QyViolationMatchType[] = [
  'keyword',
  'regex',
  'request_rate',
  'ai_review',
  'error_code',
  'status_code',
  'upstream_text',
]

export const QY_VIOLATION_GROUP_SCOPE_MODES: QyViolationGroupScopeMode[] = [
  'include',
  'exclude',
]

/**
 * `group_scope` 的分隔符，与后端 `qianye/modules/violation/rules.go` 的
 * `splitList` 逐字一致：只认逗号与换行。
 *
 * **刻意不认分号**，尽管划转分组规则那边认。两处后端的解析口径确实不同，
 * 前端跟着哪一边就必须跟到底：这里多认一个分号，一个名字里带分号的分组会在
 * 界面上被拆成两个（两个都标黄的假警报），而后端存的仍然是原来那一个。
 */
const QY_VIOLATION_GROUP_SEPARATOR = /[,\r\n]/

/** 把 `group_scope` 拆成分组名数组，供徽章展示与未定义分组的软告警计算。 */
export function qySplitViolationGroupScope(raw: string): string[] {
  return qySplitGroupNames(raw, QY_VIOLATION_GROUP_SEPARATOR)
}

/** 从下拉选中一项时把它追加进 `group_scope`，已经在里面就原样返回。 */
export function qyAppendViolationGroupScope(
  raw: string,
  entry: string
): string {
  return qyAppendGroupName(raw, entry, QY_VIOLATION_GROUP_SEPARATOR)
}

/**
 * 执行模式的取值。顺序即界面上的顺序 —— 影子在前，因为它是安全的那一侧，
 * 而这一页最重的一个动作就是把某条规则从影子切成真实。
 */
export const QY_VIOLATION_MODES: QyViolationMode[] = ['shadow', 'enforce']

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
    // 违规类型。0 是合法值（= 交给后端落「未分类」），所以这里只挡负数。
    category_id: z.number().int().min(0),
    remark: z.string().max(512),
    enabled: z.boolean(),
    mode: z.enum(['shadow', 'enforce']),
    priority: z.number().int().min(0).max(100000),
    phase: z.enum(['prompt', 'upstream_err', 'reject_reason', 'post_async']),
    match_type: z.enum([
      'keyword',
      'regex',
      'request_rate',
      'ai_review',
      'error_code',
      'status_code',
      'upstream_text',
    ]),
    pattern: z.string().max(8192, 'qy_vio_err_pattern_long'),
    case_sensitive: z.boolean(),
    status_scope: z.string().max(64),
    model_scope: z.string().max(2048),
    group_scope: z.string().max(1024),
    group_scope_mode: z.enum(['include', 'exclude']),
    action: z.enum(['record', 'charge', 'block', 'block_and_charge']),
    fee_mode: z.enum(['none', 'fixed', 'model_price_multiple']),
    fee_fixed: decimalString,
    fee_multiple: decimalString,
    fee_max_quota: z.number().int().min(0),
    ai_min_confidence: decimalString,
    // 命中一次给计数加几。0 合法（只按处置动作办、一条线都不推进），负数不合法
    // ——后端 ValidateRule 同判据（rules.go 的「count_weight 不得为负数」）。
    count_weight: z.number().int().min(0),
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
    // 与后端 ValidateRule 同一条判据：prompt 阶段状态码恒为 0，
    // 配了作用域就是一条永不命中的规则，而它保存成功、界面正常、零报错。
    if (data.status_scope.trim() !== '' && data.phase === 'prompt') {
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['status_scope'],
        message: 'qy_vio_err_status_scope_phase',
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
    // ── AI 审核:三条与后端 validateAIRule 同向的闸 ──
    if (data.phase === 'post_async' && data.match_type !== 'ai_review') {
      // 别的匹配方式在本地就能算出结果,推迟到异步只会失去拦截能力。
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['match_type'],
        message: 'qy_vio_err_async_match',
      })
    }
    if (data.match_type === 'ai_review') {
      if (data.phase !== 'prompt' && data.phase !== 'post_async') {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['phase'],
          message: 'qy_vio_err_ai_phase',
        })
      }
      // 异步时机拿不到本次请求的计费路由,扣费无法落到正确的额度池。
      if (data.phase === 'post_async' && data.action !== 'record') {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['action'],
          message: 'qy_vio_err_async_action',
        })
      }
      const conf = Number(data.ai_min_confidence.trim() || '0')
      if (!Number.isFinite(conf) || conf < 0 || conf > 1) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['ai_min_confidence'],
          message: 'qy_vio_err_ai_confidence',
        })
      }
    } else if (Number(data.ai_min_confidence.trim() || '0') > 0) {
      // 填在别的规则上一个字节都不生效,而界面上它看起来是配好的。
      ctx.addIssue({
        code: z.ZodIssueCode.custom,
        path: ['ai_min_confidence'],
        message: 'qy_vio_err_ai_confidence_type',
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
 * `mode: 'shadow'` 是刻意的：一条 `.*` 正则能在 30 秒内封掉全站用户。
 * 新规则默认只观察不执行，管理员确认命中分布之后再手动切成真实模式。
 * 后端 `ruleUpsertReq.apply` 也把空 mode 折回 shadow —— 两处同向，
 * 漏传字段的默认永远落在不扣钱的那一侧。
 */
export function qyEmptyViolationRule(): QyViolationRuleFormValues {
  return {
    name: '',
    public_reason: '',
    // 新建默认不指定类型：后端会落到「未分类」兜底类型。刻意不猜一个业务类型 ——
    // 猜错的后果是这条规则的命中记进了另一类的计数桶，而那一类的阈值判定从此全错。
    category_id: 0,
    remark: '',
    enabled: true,
    mode: 'shadow',
    priority: 100,
    phase: 'prompt',
    match_type: 'keyword',
    pattern: '',
    case_sensitive: false,
    status_scope: '',
    model_scope: '',
    group_scope: '',
    group_scope_mode: 'include',
    action: 'record',
    fee_mode: 'none',
    fee_fixed: '0',
    fee_multiple: '0',
    fee_max_quota: 0,
    ai_min_confidence: '0',
    count_weight: 1,
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
    // 历史行没有这一列，后端 AutoMigrate 回填 0，启动期迁移再把它补成兜底类型 id。
    // 读成 undefined 会让受控 Select 掉出受控态，所以这里显式折回 0。
    category_id: rule.category_id ?? 0,
    remark: rule.remark,
    enabled: rule.enabled,
    // 未知取值一律读成影子。后端的判据是 `mode === 'enforce'`，前端读成真实
    // 会让界面显示一个与线上行为相反的状态 —— 而那正是最贵的一种误判。
    mode: rule.mode === 'enforce' ? 'enforce' : 'shadow',
    priority: rule.priority,
    phase: rule.phase,
    match_type: rule.match_type,
    pattern: rule.pattern,
    case_sensitive: rule.case_sensitive,
    // 历史行没有这一列，后端 AutoMigrate 会回填空串（= 不限状态码），
    // 但一条从旧接口读来的规则可能整个字段都不存在 —— 读成 undefined 会让
    // 表单变成非受控组件，React 在下一次输入时把已填内容整段丢掉。
    status_scope: rule.status_scope ?? '',
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
    // 历史行没有这一列,后端 AutoMigrate 回填 0;读成 undefined 会让受控输入框
    // 掉出受控态,React 在下一次输入时把已填内容整段丢掉。
    ai_min_confidence: rule.ai_min_confidence ?? '0',
    fee_max_quota: rule.fee_max_quota,
    count_weight: rule.count_weight,
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
    ai_min_confidence:
      values.ai_min_confidence.trim() === ''
        ? '0'
        : values.ai_min_confidence.trim(),
  }
}
