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
 * 使用日志「推理强度」「缓存命中率」两列的全部计算逻辑。
 *
 * 集中在纯函数模块里，而不是散在 cell 渲染函数中，是因为同一套公式要被
 * 表格列、移动端卡片、详情弹窗三处复用。三处各写一遍必然漂移，而缓存率
 * 一旦算错是"看起来完全合理"的静默错误——用户会当真。
 *
 * 数据来源是后端 `qianye/modules/logmetrics` 写进 `logs.other` 的 `qy_*` 键。
 * 本文件的归一化映射表必须与后端 `reasoning.go` 保持逐条一致。
 */
import type { StatusBadgeProps } from '@/components/status-badge'

import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'
import { parseLogOther } from './format'

// ============================================================================
// other 解析缓存
// ============================================================================

/**
 * `parseLogOther` 的 WeakMap 记忆化版本。
 *
 * 上游的 `parseLogOther` 每次调用都会跑一次 `JSON.parse`，而单行日志的 `other`
 * 已经被 4 处 cell 各自解析一遍。新增两列会让每行变成 6 次解析，100 行/页时
 * 就是 600 次。用 WeakMap 按行对象缓存：react-query 每次取数返回全新对象，
 * 因此缓存天然随数据刷新失效，也不会阻止 GC。
 *
 * 刻意不去改 `format.ts` 的 `parseLogOther` —— 那是上游文件，改它等于给每次
 * 合并上游制造一处冲突，收益却只是少几百次微秒级解析。
 */
const otherCache = new WeakMap<UsageLog, LogOtherData | null>()

export function getLogOtherCached(log: UsageLog): LogOtherData | null {
  const cached = otherCache.get(log)
  if (cached !== undefined) return cached
  const parsed = parseLogOther(log.other)
  otherCache.set(log, parsed)
  return parsed
}

// ============================================================================
// 推理强度
// ============================================================================

export type QyReasoningLevel =
  | 'none'
  | 'minimal'
  | 'low'
  | 'medium'
  | 'high'
  | 'max'
  | 'auto'

export interface QyReasoning {
  /** 归一化档位，只用于快速扫视与配色。 */
  level: QyReasoningLevel
  /** 厂商原值。归一化必然丢信息，tooltip 要能还原真相。 */
  raw: string
  /** 数值型思考预算（tokens）。无数值口径时为 0；Gemini 动态预算保留 -1。 */
  budget: number
  /** 后端探测分支（relay_info / claude_thinking / …），排障用。 */
  src: string
}

const REASONING_LEVELS: readonly QyReasoningLevel[] = [
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'max',
  'auto',
]

/**
 * 枚举口径 → 档位。与后端 `logmetrics.NormalizeEffort` 逐条对应。
 *
 * 前端仍然需要这张表：`qy_reasoning` 上线前的历史日志只有上游写的
 * `other.reasoning_effort` 字符串，没有归一化结果。
 */
export function normalizeEffortLevel(raw: string): QyReasoningLevel {
  switch (raw.trim().toLowerCase()) {
    case 'none':
    case 'off':
    case 'no':
    case 'false':
    case 'disable':
    case 'disabled':
    case 'nothinking':
      return 'none'
    case 'minimal':
    case 'min':
    case 'very_low':
    case 'very-low':
      return 'minimal'
    case 'low':
      return 'low'
    case 'medium':
    case 'mid':
    case 'moderate':
    case 'default':
    case 'standard':
    case 'enabled':
    case 'enable':
    case 'true':
      return 'medium'
    case 'high':
      return 'high'
    case 'xhigh':
    case 'x-high':
    case 'extra_high':
    case 'max':
    case 'maximum':
    case 'highest':
    case 'ultra':
      return 'max'
    case 'auto':
    case 'dynamic':
    case 'adaptive':
      return 'auto'
    default:
      // 未知口径保守居中：归成 none 会让用户以为没花思考的钱，
      // 归成 max 又制造无谓的成本恐慌。raw 一并保留，tooltip 里还原。
      return 'medium'
  }
}

function toReasoningLevel(value: unknown): QyReasoningLevel | null {
  if (typeof value !== 'string') return null
  return (REASONING_LEVELS as readonly string[]).includes(value)
    ? (value as QyReasoningLevel)
    : null
}

function toBudget(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 0
  return Math.trunc(value)
}

/**
 * 取这条日志的推理强度。返回 `null` 表示无数据，调用方应渲染 `—`。
 *
 * 优先读后端归一化好的 `qy_reasoning`；它覆盖 Claude thinking、Gemini 数值
 * 预算、Qwen、通用 OpenAI 兼容渠道等上游 `reasoning_effort` 拿不到的场景。
 * 历史日志回退到 `reasoning_effort` 字符串，本地归一化后展示。
 */
export function getReasoning(other: LogOtherData | null): QyReasoning | null {
  if (!other) return null

  const payload = other.qy_reasoning
  if (payload != null && typeof payload === 'object') {
    const raw = typeof payload.raw === 'string' ? payload.raw : ''
    return {
      level: toReasoningLevel(payload.level) ?? normalizeEffortLevel(raw),
      raw,
      budget: toBudget(payload.budget),
      src: typeof payload.src === 'string' ? payload.src : '',
    }
  }

  const legacy = other.reasoning_effort
  if (typeof legacy === 'string' && legacy.trim() !== '') {
    return {
      level: normalizeEffortLevel(legacy),
      raw: legacy.trim(),
      budget: 0,
      src: 'legacy',
    }
  }

  return null
}

const REASONING_VARIANTS: Record<
  QyReasoningLevel,
  NonNullable<StatusBadgeProps['variant']>
> = {
  none: 'grey',
  minimal: 'light-blue',
  low: 'green',
  medium: 'yellow',
  high: 'orange',
  max: 'red',
  auto: 'violet',
}

/** 档位 → 徽章配色。表格、移动端、详情弹窗共用，避免三处漂移。 */
export function reasoningVariant(
  level: QyReasoningLevel
): NonNullable<StatusBadgeProps['variant']> {
  return REASONING_VARIANTS[level]
}

const REASONING_LABEL_KEYS: Record<QyReasoningLevel, string> = {
  none: 'qy_log_reasoning_none',
  minimal: 'qy_log_reasoning_minimal',
  low: 'qy_log_reasoning_low',
  medium: 'qy_log_reasoning_medium',
  high: 'qy_log_reasoning_high',
  max: 'qy_log_reasoning_max',
  auto: 'qy_log_reasoning_auto',
}

/** 档位 → i18n key。键是常量表而非模板拼接，漏翻时能被静态扫描发现。 */
export function reasoningLabelKey(level: QyReasoningLevel): string {
  return REASONING_LABEL_KEYS[level]
}

// ============================================================================
// 缓存命中率
// ============================================================================

export interface QyCacheRate {
  /** 0–100，已钳制。 */
  pct: number
  /** 分子：缓存读取 token。 */
  cacheRead: number
  /** 分母：输入 token 总量（已按计费语义修正）。 */
  inputTotal: number
  /** 上游 usage 自相矛盾（分子 > 分母 / 负数 / 溢出）。 */
  anomaly: boolean
}

function safeCount(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) {
    return 0
  }
  return Math.trunc(value)
}

/**
 * 缓存写入合计。
 *
 * 必须先看 5m/1h 之和、两者都为 0 才回退 `cache_creation_tokens`：Claude 会同时
 * 写入总量和 5m/1h 分档，直接相加会重复计数。这与 Tokens 列的 `hasSplitCache`
 * 判断是同一套口径。
 */
export function getCacheWriteTokens(other: LogOtherData | null): number {
  if (!other) return 0
  if (other.qy_cache_write != null) return safeCount(other.qy_cache_write)

  const split =
    safeCount(other.cache_creation_tokens_5m) +
    safeCount(other.cache_creation_tokens_1h)
  if (split > 0) return split
  return safeCount(other.cache_creation_tokens)
}

function isAnthropicSemantic(other: LogOtherData): boolean {
  // 三个都是"正向"标记：出现即代表确实是 Anthropic 计费语义，
  // 缺失才是二义状态。绝不用"缺失 ⇒ OpenAI 语义"这种反向推断。
  return (
    other.qy_semantic === 'anthropic' ||
    other.usage_semantic === 'anthropic' ||
    other.claude === true
  )
}

/**
 * 计算缓存命中率。返回 `null` 表示**不可判定**，调用方必须渲染 `—`。
 *
 * 核心陷阱：`prompt_tokens` 是否已经包含 `cached_tokens` 取决于计费语义。
 * OpenAI / Gemini 语义包含，Anthropic 语义不包含。判别键缺失是个二义状态
 * （「新的 OpenAI 语义日志」∪「标记上线前的全部日志，含 Claude」），
 * 无法用现有数据区分。对一条历史 Claude 日志误用 OpenAI 公式，结果会落在
 * 0–100% 区间里静默出错，比不显示更糟。
 *
 * 因此按可信度严格降级，命中即停：
 *  1. `qy_input_total` —— 后端在持有 `IsClaudeUsageSemantic` 时固化的权威分母
 *  2. 分子为 0 —— 任何分母都得 0，是历史日志里唯一无歧义的情形
 *  3. 正向的 Anthropic 语义标记 —— 分母 = prompt + read + write
 *  4. 上游 `input_tokens_total` —— 只在可信时写入
 *  5. 其余 —— 不可判定
 */
export function getCacheRate(
  log: UsageLog,
  other: LogOtherData | null
): QyCacheRate | null {
  if (!other) return null

  const promptTokens = safeCount(log.prompt_tokens)
  const completionTokens = safeCount(log.completion_tokens)
  if (promptTokens === 0 && completionTokens === 0) return null

  const cacheRead =
    other.qy_cache_read != null
      ? safeCount(other.qy_cache_read)
      : safeCount(other.cache_tokens)
  const cacheWrite = getCacheWriteTokens(other)

  let inputTotal = safeCount(other.qy_input_total)
  if (inputTotal === 0) {
    if (cacheRead === 0 && cacheWrite === 0 && promptTokens > 0) {
      inputTotal = promptTokens
    } else if (isAnthropicSemantic(other)) {
      inputTotal = promptTokens + cacheRead + cacheWrite
    } else {
      inputTotal = safeCount(other.input_tokens_total)
    }
  }
  if (inputTotal <= 0) return null

  let anomaly = other.qy_cache_anomaly === true
  let pct = (cacheRead / inputTotal) * 100
  if (!Number.isFinite(pct)) return null
  if (pct > 100) {
    // 分子大于分母只可能是上游数据错误。钳到 100% 并留痕，
    // 静默钳制会把上游 bug 藏起来。
    pct = 100
    anomaly = true
  }
  if (pct < 0) {
    pct = 0
    anomaly = true
  }

  return { pct, cacheRead, inputTotal, anomaly }
}

/**
 * 百分比文本。
 *
 * `> 0` 但 `< 0.1%` 显示 `<0.1%` 而不是 `0.0%`：后者会被读成"完全没命中缓存"，
 * 而实际上是命中了、只是占比极小。
 */
export function formatCacheRate(pct: number): string {
  if (pct <= 0) return '0%'
  if (pct < 0.1) return '<0.1%'
  // 先四舍五入再判满格：99.96% 应该显示成 100%，而不是别扭的 "100.0%"。
  const rounded = Math.round(pct * 10) / 10
  if (rounded >= 100) return '100%'
  return `${rounded.toFixed(1)}%`
}

/**
 * 缓存率热力配色。用纯文本着色而非 pill：表格里已有多个徽章，再加会视觉过载。
 */
export function cacheRateTextClass(pct: number): string {
  if (pct >= 70) return 'text-success'
  if (pct >= 30) return 'text-warning'
  if (pct > 0) return 'text-muted-foreground'
  return 'text-muted-foreground/60'
}
