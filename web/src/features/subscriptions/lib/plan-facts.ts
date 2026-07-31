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
import type { TFunction } from 'i18next'

import dayjs from '@/lib/dayjs'
import { formatQuota } from '@/lib/format'

import type { SubscriptionPlan } from '../types'
import { formatDuration, formatResetPeriod } from './format'

/**
 * 套餐"事实项"。
 *
 * 卡片与订阅弹窗此前各自手搓一份权益清单，字段还不一致（卡片有购买上限、
 * 弹窗没有；弹窗有降级组、卡片没有），改一处必漏另一处。收敛到这里之后，
 * 两边只是渲染方式不同，内容口径永远一致。
 */
export interface PlanFact {
  /**
   * 稳定的 React key。
   *
   * 原先卡片用翻译后的文案当 key（`key={label}`），文案里含用户可配的分组名，
   * 换语言或改分组会让整段列表重挂载，两条事实文案偶然相同时还会直接告警。
   */
  id: string
  label: string
  value: string
}

/** 常见币种符号。查不到的币种退化成 `12.00 XYZ`，不猜符号。 */
const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  CNY: '¥',
  EUR: '€',
  GBP: '£',
  JPY: '¥',
  HKD: 'HK$',
}

/**
 * 金额展示。
 *
 * 后端 `price_amount` 是 decimal(10,6)，原来一律 `toFixed(2)` 会把 $0.005
 * 显示成 $0.01 —— 展示价与实际扣款对不上。这里的规则是"两位能精确表示就用
 * 两位，否则最多六位并去掉尾零"，与后端精度对齐。
 *
 * 本函数只做展示格式化，不参与任何金额计算。
 */
function formatPlanAmount(raw: number): string {
  if (!Number.isFinite(raw) || raw < 0) return '0.00'
  const fixed2 = raw.toFixed(2)
  if (Math.abs(Number(fixed2) - raw) < 1e-9) return fixed2
  return raw.toFixed(6).replace(/0+$/, '')
}

/** 带币种的价格文案。取代散落各处硬编码的 `$`。 */
export function formatPlanPrice(plan: Partial<SubscriptionPlan>): string {
  const code = (plan?.currency || 'USD').toUpperCase()
  const amount = formatPlanAmount(Number(plan?.price_amount || 0))
  const symbol = CURRENCY_SYMBOLS[code]
  return symbol ? `${symbol}${amount}` : `${amount} ${code}`
}

/**
 * 客户端预估到期时间。
 *
 * 仅供购买前参考：真实到期时间由后端按下单时刻计算，跨月钳位（1/31 + 1 月）
 * 等细节可能与这里有出入，所以文案上必须标注"预计"。
 */
export function formatPlanExpiryPreview(
  plan: Partial<SubscriptionPlan>
): string {
  const unit = plan?.duration_unit || 'month'
  const base = dayjs()
  let end = base.add(Number(plan?.custom_seconds || 0), 'second')
  if (unit !== 'custom') {
    end = base.add(Number(plan?.duration_value || 1), unit)
  }
  // custom_seconds 是 int64，超大值算出来是 Invalid Date，不能直接 format
  return end.isValid() ? end.format('YYYY-MM-DD HH:mm') : '-'
}

export interface BuildPlanFactsOptions {
  /** 是否把升/降级分组并入事实列表。弹窗用 GroupBadge 单独渲染，故传 false。 */
  includeGroups?: boolean
  /** 当前用户已购次数，用于渲染 `已购 1/3`。 */
  purchaseCount?: number
}

/**
 * 生成套餐事实清单。
 *
 * 语义约定沿用后端：`total_amount === 0` 表示不限量、`max_purchase_per_user === 0`
 * 表示不限购，这两种情况分别渲染成"无限"和整行不渲染。
 */
export function buildPlanFacts(
  plan: Partial<SubscriptionPlan>,
  t: TFunction,
  opts: BuildPlanFactsOptions = {}
): PlanFact[] {
  const total = Number(plan?.total_amount || 0)
  const limit = Number(plan?.max_purchase_per_user || 0)
  const reset = formatResetPeriod(plan, t)
  const count = Number(opts.purchaseCount || 0)

  const facts: (PlanFact | null)[] = [
    {
      id: 'duration',
      label: t('Validity Period'),
      value: formatDuration(plan, t),
    },
    reset === t('No Reset')
      ? null
      : { id: 'reset', label: t('Quota Reset'), value: reset },
    {
      id: 'quota',
      label: t('Total Quota'),
      value: total > 0 ? formatQuota(total) : t('Unlimited'),
    },
    {
      id: 'expiry',
      label: t('qy_plan_expiry_preview'),
      value: formatPlanExpiryPreview(plan),
    },
    {
      id: 'overflow',
      label: t('qy_plan_wallet_overflow'),
      value:
        plan?.allow_wallet_overflow === false
          ? t('qy_plan_wallet_overflow_blocked')
          : t('qy_plan_wallet_overflow_allowed'),
    },
    limit > 0
      ? {
          id: 'limit',
          label: t('qy_plan_purchased'),
          value: `${count} / ${limit}`,
        }
      : null,
    opts.includeGroups && plan?.upgrade_group
      ? { id: 'upgrade', label: t('Upgrade Group'), value: plan.upgrade_group }
      : null,
    opts.includeGroups && plan?.downgrade_group
      ? {
          id: 'downgrade',
          label: t('Downgrade Group'),
          value: plan.downgrade_group,
        }
      : null,
  ]

  return facts.filter((fact): fact is PlanFact => fact !== null)
}
