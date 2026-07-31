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
import type { ColumnDef } from '@tanstack/react-table'
import { AlertTriangle } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useQyConfig } from '@/features/qy/hooks/use-qy-config'
import { cn } from '@/lib/utils'

import type { UsageLog } from '../../data/schema'
import {
  cacheRateTextClass,
  formatCacheRate,
  getCacheRate,
  getLogOtherCached,
  getReasoning,
  reasoningLabelKey,
  reasoningVariant,
} from '../../lib/qy-log-metrics'
import { isDisplayableLogType } from '../../lib/utils'

type Translate = (key: string, opts?: Record<string, unknown>) => string

/**
 * 统一空态。与相邻列（details 列的 `—`）保持同一种"没有数据"的视觉语言。
 *
 * 用元素常量而不是组件：本文件只导出一个 hook，多一个顶层组件会触发
 * react/only-export-components，而这个 `—` 没有 props、无状态，元素常量足够。
 */
const metricEmptyDash = <span className='text-muted-foreground/50'>—</span>

function buildReasoningColumn(t: Translate): ColumnDef<UsageLog> {
  const label = t('Reasoning Effort')

  return {
    id: 'qy_reasoning',
    header: label,
    // accessorFn 不是可有可无的：View 菜单（data-table/toolbar/view-options）
    // 只列出 `typeof column.accessorFn !== 'undefined'` 的列，缺了它用户
    // 永远无法隐藏这一列。
    accessorFn: (row) => getReasoning(getLogOtherCached(row))?.level ?? '',
    cell: ({ row }) => {
      const log = row.original
      if (!isDisplayableLogType(log.type)) return null

      const reasoning = getReasoning(getLogOtherCached(log))
      if (!reasoning) return metricEmptyDash

      const levelText = t(reasoningLabelKey(reasoning.level))
      const budgetText =
        reasoning.budget > 0
          ? `${t('qy_log_thinking_budget')}: ${reasoning.budget.toLocaleString()}`
          : ''

      return (
        <TooltipProvider delay={300}>
          <Tooltip>
            <TooltipTrigger render={<span className='inline-flex' />}>
              <StatusBadge
                label={levelText}
                variant={reasoningVariant(reasoning.level)}
                size='sm'
                copyable={false}
              />
            </TooltipTrigger>
            <TooltipContent side='top' className='max-w-xs'>
              <div className='space-y-0.5'>
                <p>{levelText}</p>
                {reasoning.raw !== '' && (
                  <p className='text-muted-foreground font-mono text-xs break-all'>
                    {reasoning.raw}
                  </p>
                )}
                {budgetText !== '' && (
                  <p className='text-muted-foreground text-xs'>{budgetText}</p>
                )}
              </div>
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      )
    },
    // mobileHidden：移动端由 MobileTokensField 手工挂载这两个指标，
    // 不走通用卡片渲染，标记出来免得日后有人以为漏了。
    meta: {
      label,
      description: t('qy_log_reasoning_col_tip'),
      mobileHidden: true,
    },
    size: 110,
  }
}

function buildCacheRateColumn(t: Translate): ColumnDef<UsageLog> {
  const label = t('qy_log_cache_hit_rate')

  return {
    id: 'qy_cache_rate',
    header: label,
    accessorFn: (row) => getCacheRate(row, getLogOtherCached(row))?.pct ?? null,
    cell: ({ row }) => {
      const log = row.original
      if (!isDisplayableLogType(log.type)) return null

      const rate = getCacheRate(log, getLogOtherCached(log))
      // 语义不明的老日志一律 `—`：宁可不显示，也绝不给出一个可能错误的百分比。
      // 附 tooltip 解释原因，否则运营会被反复追问"为什么有些行是横杠"。
      if (!rate) {
        return (
          <TooltipProvider delay={300}>
            <Tooltip>
              <TooltipTrigger render={<span className='cursor-help' />}>
                {metricEmptyDash}
              </TooltipTrigger>
              <TooltipContent side='top' className='max-w-xs'>
                {t('qy_log_metric_unknown_tip')}
              </TooltipContent>
            </Tooltip>
          </TooltipProvider>
        )
      }

      const detail = t('qy_log_cache_detail_tip', {
        read: rate.cacheRead.toLocaleString(),
        total: rate.inputTotal.toLocaleString(),
      })

      return (
        <TooltipProvider delay={300}>
          <Tooltip>
            <TooltipTrigger render={<span className='inline-flex' />}>
              <span
                className={cn(
                  'inline-flex items-center gap-0.5 font-mono text-xs tabular-nums',
                  cacheRateTextClass(rate.pct)
                )}
              >
                {formatCacheRate(rate.pct)}
                {rate.anomaly && (
                  <AlertTriangle
                    className='text-warning size-3'
                    aria-hidden='true'
                  />
                )}
              </span>
            </TooltipTrigger>
            <TooltipContent side='top' className='max-w-xs'>
              <div className='space-y-0.5'>
                <p>{detail}</p>
                {rate.anomaly && (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_log_cache_over_tip')}
                  </p>
                )}
              </div>
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      )
    },
    meta: {
      label,
      description: t('qy_log_cache_col_tip'),
      mobileHidden: true,
    },
    size: 110,
  }
}

/**
 * 「推理强度」「缓存命中率」两列。
 *
 * 显隐完全由后端引导端点 `/api/qy/config` 的 `log_metrics` 决定：关掉时**根本
 * 不构造列**，而不是构造后隐藏——后者会在 View 菜单里留下一个打不开的开关，
 * 也会让 localStorage 里存下一个再也不该出现的列 id。扩展整体未启用时
 * `useQyConfig()` 返回全关配置，因此这里天然零痕迹。
 *
 * 插在 Tokens 与 Cost 之间：两列都是"输入侧特征"，与 Tokens 语义邻近，
 * 形成「用了多少 token → 缓存省了多少 → 推理花了多少 → 最终成本」的动线。
 */
export function useQyLogMetricColumns(): ColumnDef<UsageLog>[] {
  const { t } = useTranslation()
  const config = useQyConfig()

  const columns: ColumnDef<UsageLog>[] = []
  if (!config.enabled) return columns

  if (config.log_metrics.show_reasoning_effort) {
    columns.push(buildReasoningColumn(t))
  }
  if (config.log_metrics.show_cache_ratio) {
    columns.push(buildCacheRateColumn(t))
  }
  return columns
}
