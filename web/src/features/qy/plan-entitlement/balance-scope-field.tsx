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

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'

import type { QyPlanBalanceScope } from './types'

/**
 * 「套餐余额的使用范围」选择器。
 *
 * ── 为什么是共用组件而不是各写一份 ──
 *
 * 这一项现在有两个入口：套餐编辑抽屉（与解锁清单在同一次编辑里，那是主路径）
 * 与行操作里的完整弹窗（多出影响面人数与现算倍率表）。两处渲染的是**同一个
 * 二选一**，而这个二选一决定的是"一笔钱能不能从这个池子里扣"。各写一份之后，
 * 改一处必漏另一处，而漏掉的那一处会让运营在两个页面上读到两句不同的解释、
 * 得出两个不同的结论。
 */
export function QyPlanBalanceScopeField(props: {
  value: QyPlanBalanceScope
  onChange: (scope: QyPlanBalanceScope) => void
  disabled?: boolean
  /**
   * 「仅限」+ 零**有效**绑定 = 一份任何请求都用不上的死钱。为 true 时出红字。
   * 判定由调用方做：抽屉与弹窗拿到候选清单的路径不同，但判据逐字相同。
   */
  needsBinding?: boolean
  /** 给出红字的**一键出口**。省略时只出红字（弹窗把这个按钮放在别的横幅里）。 */
  onResetToUniversal?: () => void
}) {
  const { t } = useTranslation()

  return (
    <div className='space-y-2'>
      <Label>{t('qy_plan_balance_scope_label')}</Label>
      <div className='grid gap-2'>
        <QyPlanScopeOption
          active={props.value === 'universal'}
          disabled={props.disabled}
          title={t('qy_plan_balance_scope_universal')}
          desc={t('qy_plan_balance_scope_universal_desc')}
          onSelect={() => props.onChange('universal')}
        />
        <QyPlanScopeOption
          active={props.value === 'restricted'}
          disabled={props.disabled}
          title={t('qy_plan_balance_scope_restricted')}
          desc={t('qy_plan_balance_scope_restricted_hint')}
          onSelect={() => props.onChange('restricted')}
        />
      </div>
      {props.needsBinding === true && (
        <div className='flex flex-col items-start gap-1.5'>
          <p className='text-destructive text-xs'>
            {t('qy_plan_balance_scope_need_binding')}
          </p>
          {props.onResetToUniversal != null && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={props.onResetToUniversal}
            >
              {t('qy_plan_bound_group_reset_to_universal')}
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

function QyPlanScopeOption(props: {
  active: boolean
  disabled?: boolean
  title: string
  desc: string
  onSelect: () => void
}) {
  return (
    <button
      type='button'
      onClick={props.onSelect}
      disabled={props.disabled}
      aria-pressed={props.active}
      className={
        props.active
          ? 'border-primary bg-primary/5 rounded-lg border p-3 text-start disabled:opacity-60'
          : 'hover:bg-muted/50 rounded-lg border p-3 text-start disabled:opacity-60'
      }
    >
      <span className='block text-sm font-medium'>{props.title}</span>
      <span className='text-muted-foreground block text-xs'>{props.desc}</span>
    </button>
  )
}
