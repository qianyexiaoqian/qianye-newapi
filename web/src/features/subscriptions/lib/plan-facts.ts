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

/**
 * 「这个套餐解锁哪些模型分组、它那笔余额能花在什么上」的披露数据。
 *
 * 由 `features/qy/plan-entitlement` 从 `GET /api/qy/subscription/entitlements`
 * 取得。这里刻意是一个**三态**联合而不是一对可选数组：
 *
 * - `undefined`（整个字段不传）= 不知道。可能是扩展没装、也可能是这个套餐用户
 *   还没买（那个接口只覆盖已持有的订阅）。不知道就一行都不渲染。
 * - `loading` / `error` = 知道有这回事，但这次没读到。**必须显示出来** ——
 *   把读取失败渲染成"不解锁任何分组"是在替后端编造事实，而这条事实正是买家
 *   掏钱的理由。
 * - `ready` = 真值。空数组在这里才真的表示"不解锁额外分组"。
 */
export type PlanEntitlementDisclosure =
  | { state: 'error' }
  | { state: 'loading' }
  | {
      state: 'ready'
      /** 买了之后多拿到的模型分组。空数组 = 确实不解锁额外分组。 */
      unlockGroups: string[]
      balanceScope: 'restricted' | 'universal'
      /**
       * 「仅限」此刻是否真的在扣费路径上生效。为 false 时余额照样能被任意分组
       * 花掉 —— 说成"仅限"会让买家按一个不存在的限制去规划用量。
       */
      scopeEnforced: boolean
    }

/**
 * 「解锁哪些模型分组」这一行的值。
 *
 * 独立成函数不是为了少写几个字：**买之前（套餐卡 / 购买弹窗）与买之后
 * （我的订阅）必须是同一句话**，否则用户会以为自己买到的是另一样东西。
 * 两处各写一遍时，改一处必漏另一处 —— 这个文件顶上的注释记的就是那次教训。
 *
 * 每个分支都写成字面量 `t('qy_…')`，不走 `t(变量)`：
 * `features/qy/lib/__tests__/i18n-key-coverage.test.ts` 按字面量扫描键，
 * 写成变量的会绕过守卫，缺键时界面上直接渲染裸键而全仓没有任何信号。
 */
export function formatPlanUnlockGroups(
  grant: PlanEntitlementDisclosure,
  t: TFunction
): string {
  if (grant.state === 'loading') return t('qy_ent_loading')
  if (grant.state === 'error') return t('qy_ent_failed')
  if (grant.unlockGroups.length === 0) return t('qy_ent_unlock_none')
  // 原样列出而不折成计数：买家要判断的是"我想用的那个分组在不在里面"。
  return grant.unlockGroups.join(', ')
}

/** 「这笔余额能花在什么上」这一行的值。与上面同源同理。 */
export function formatPlanBalanceScope(
  grant: PlanEntitlementDisclosure,
  t: TFunction
): string {
  if (grant.state === 'loading') return t('qy_ent_loading')
  if (grant.state === 'error') return t('qy_ent_failed')
  if (grant.balanceScope !== 'restricted') return t('qy_ent_scope_universal')
  // 「仅限」还没接进扣费路径时说成"仅限"，等于让买家按一个不存在的限制去
  // 规划用量：他以为这笔钱花不到别处，实际上会被任意分组花光。
  if (!grant.scopeEnforced) return t('qy_ent_scope_restricted_off')
  return t('qy_ent_scope_restricted')
}

export interface BuildPlanFactsOptions {
  /**
   * 是否把「购买后用户分组会被改写成什么」并入事实列表。
   *
   * ── 这一项为什么还在，而且默认关 ──
   *
   * 管理端已经不再提供这两个字段的编辑入口：用户分组与模型分组分离之后，买套餐
   * 只该多解锁几个**模型分组**，不该把人从一个用户分组搬到另一个。但上游
   * `CreateUserSubscriptionFromPlanTx` 那段改写 `users.group` 的逻辑一行没动，
   * 于是**存量套餐仍然会改写**（直到管理员在新表单里把它保存一次，见
   * lib/plan-form.ts）。
   *
   * 只要它还会发生，就必须在**掏钱之前**告诉买家：换一个用户分组会连带换掉他的
   * 可用范围、倍率与自动分组。所以这两行不是历史包袱，是仍然有效的风险披露 ——
   * 而且它们本来就只在字段非空时渲染，那批套餐清干净之后会自动消失。
   */
  includeLegacyGroupRewrite?: boolean
  /** 当前用户已购次数，用于渲染 `已购 1/3`。 */
  purchaseCount?: number
  /**
   * 套餐解锁披露。省略 = 不知道，整两行都不渲染（见
   * {@link PlanEntitlementDisclosure}）。
   *
   * 这两行是本轮唯一新增的事实：管理端撤掉「升级分组 / 降级分组」之后，
   * 「买了能多用哪个模型分组」成了某些套餐**唯一的卖点**，而买家侧一个字
   * 都看不到；「余额只能花在 X 分组」更是付款前必须知道、付款后才发现等于
   * 误导的一条。
   */
  entitlement?: PlanEntitlementDisclosure
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
  const grant = opts.entitlement

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
    // 解锁与余额范围排在到期时间之后、限购之前：它们是"买了能得到什么"，
    // 比"买了之后还能不能再买"更靠近掏钱这个决定。
    grant == null
      ? null
      : {
          id: 'unlock_groups',
          label: t('qy_plan_unlock_groups_label'),
          value: formatPlanUnlockGroups(grant, t),
        },
    grant == null
      ? null
      : {
          id: 'balance_scope',
          label: t('qy_plan_balance_scope_label'),
          value: formatPlanBalanceScope(grant, t),
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
    opts.includeLegacyGroupRewrite && plan?.upgrade_group
      ? { id: 'upgrade', label: t('Upgrade Group'), value: plan.upgrade_group }
      : null,
    opts.includeLegacyGroupRewrite && plan?.downgrade_group
      ? {
          id: 'downgrade',
          label: t('Downgrade Group'),
          value: plan.downgrade_group,
        }
      : null,
  ]

  return facts.filter((fact): fact is PlanFact => fact !== null)
}
