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
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import {
  formatPlanBalanceScope,
  formatPlanUnlockGroups,
} from '@/features/subscriptions/lib'

import type { QyEntitlementResult } from './use-my-entitlements'

/**
 * 已购视图里的权益披露。
 *
 * 与套餐卡上的两行事实共用同一批 i18n key：同一件事在"买之前"和"买之后"必须
 * 是同一句话，否则用户会以为自己买到的是另一样东西。
 */

/**
 * 「我的订阅」整块的汇总：现在一共解锁了哪些模型分组、有没有花不出去的余额。
 *
 * 三态在这里是**齐的**，因为这块区域的数据无条件适用于当前用户：
 * loading 静默（避免闪一下就消失）、error 明说读取失败、ready 才给结论。
 * 缺了 error 那一档，一次接口抖动就会把"你解锁了 3 个分组"变成"你什么都没
 * 解锁"，而两者长得一模一样。
 */
export function QyMyEntitlementSummary({
  result,
}: {
  result: QyEntitlementResult
}) {
  const { t } = useTranslation()

  if (result.state === 'hidden' || result.state === 'loading') return null
  if (result.state === 'error') {
    return (
      <p className='text-destructive mt-2 text-xs'>
        {t('qy_ent_summary_failed')}
      </p>
    )
  }
  if (result.subscriptions.length === 0) return null

  return (
    <div className='mt-2 space-y-1.5 text-xs'>
      <div className='flex flex-wrap items-center gap-1.5'>
        <span className='text-muted-foreground'>
          {t('qy_ent_summary_title')}
        </span>
        {result.unlockedGroups.length > 0 ? (
          result.unlockedGroups.map((group) => (
            <StatusBadge
              key={group}
              label={group}
              variant='info'
              copyable={false}
            />
          ))
        ) : (
          <span className='text-muted-foreground'>
            {t('qy_ent_summary_none')}
          </span>
        )}
      </div>
      {/* 两句话不能合并：一句是「这笔钱只能花在 X 上」，另一句是「标着仅限但
          其实还没生效」。合成一句必然有一半的人被告知了与事实相反的限制。 */}
      {result.anyRestricted && result.balanceScopeEnforced && (
        <p className='text-muted-foreground'>{t('qy_ent_restricted_notice')}</p>
      )}
      {result.anyRestricted && !result.balanceScopeEnforced && (
        <p className='text-muted-foreground'>{t('qy_ent_restricted_off')}</p>
      )}
      {result.walletFallbackBlocked && (
        <p className='text-muted-foreground'>{t('qy_ent_wallet_blocked')}</p>
      )}
    </div>
  )
}

/**
 * 已购列表里**某一条订阅**的解锁清单与余额使用范围。
 *
 * 只在 ready 且这条订阅在响应里时渲染：整块的 loading / error 已经由
 * {@link QyMyEntitlementSummary} 说过一次，每行再重复一遍只会把失败说成
 * N 遍，而用户能做的动作只有一个（刷新）。
 */
export function QySubscriptionEntitlement({
  result,
  subscriptionId,
}: {
  result: QyEntitlementResult
  subscriptionId: number | undefined
}) {
  const { t } = useTranslation()

  if (result.state !== 'ready' || subscriptionId == null) return null
  const view = result.bySubscription.get(subscriptionId)
  if (view == null) return null

  // 走套餐卡那两行**同一个**格式化函数：买之前与买之后必须是同一句话。
  // 在这里重写一遍等于给同一件事留两处实现，而两处迟早会分叉。
  // `?? []`：`qyGet` 是纯类型断言，后端哪天把 `bound_groups` 改成 omitempty
  // 或返回 null，这里拿到的就是 `undefined`，而 `undefined.length` 会在渲染期
  // 抛异常，把整张钱包页白屏掉 —— 一条纯展示的披露信息不该有能力做到这件事。
  // `buildQyEntitlementIndex` 走的是同一道防线，这条路径存的是原对象，得自己补。
  const grant = {
    state: 'ready',
    unlockGroups: view.bound_groups ?? [],
    balanceScope: view.balance_scope,
    scopeEnforced: result.balanceScopeEnforced,
  } as const

  return (
    <>
      <div className='text-muted-foreground mt-1 [overflow-wrap:anywhere]'>
        {t('qy_plan_unlock_groups_label')}: {formatPlanUnlockGroups(grant, t)}
      </div>
      <div className='text-muted-foreground mt-1 flex flex-wrap items-center gap-1.5'>
        <span>
          {t('qy_plan_balance_scope_label')}: {formatPlanBalanceScope(grant, t)}
        </span>
        {view.will_charge_first && (
          <StatusBadge
            label={t('qy_ent_charge_first')}
            variant='success'
            copyable={false}
          />
        )}
      </div>
    </>
  )
}
