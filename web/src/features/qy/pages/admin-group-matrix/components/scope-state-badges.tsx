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

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import type { QyGmUserGroup } from '../types'

type QyGmScopeStateBadgesProps = {
  userGroup: QyGmUserGroup
  /** 草稿合并后这一档当前有几个模型分组在范围里。 */
  grantedCount: number
  /**
   * 覆盖「未设定范围」徽章的 title。
   *
   * 默认那一句以「先给这个用户分组设定范围」收尾 —— 它指的是矩阵页上那个
   * 「设定模型分组范围」按钮，而**那个控件不是每个外壳上都有**：用户分组的
   * 编辑弹窗里，范围行由列表内容隐式表达，那个按钮根本不渲染。指路指向一个
   * 不存在的控件比不给指路更糟，运营会去找、找不到，然后认为页面坏了。
   */
  unsetHint?: string
  /** 覆盖「空范围」徽章的 title。理由同上：外壳不同，下一步动作不同。 */
  emptyHint?: string
}

/**
 * 一个用户分组的**范围三态 + 生效力度**徽章。
 *
 * ── 三态，而不是「已接管 / 未接管」两态 ──
 *
 *   · 未设定范围        不受本页限制，逐位沿用上游原行为
 *   · 已设定范围 · N 个  只有这 N 个模型分组可用
 *   · 已设定范围 · 空    一个模型分组都不可用（隔离组）—— **红色**
 *
 * 「已接管 / 未接管」这套词在新口径下会直接把人带偏：运营会把「未接管」读成
 * 「不能用」，而它的真实含义恰好是「全都能用」。所以这里**绝不出现那两个词**。
 *
 * 第三态必须与第二态分开，理由与后端不肯用 grants 行数推断三态一样：
 * 「一条都不许用」与「没配过」的两个错误方向分别是整组用户 403 和整组用户全放行，
 * 而且都静默。
 *
 * ── 为什么抽成组件 ──
 * 同一档分组的状态现在出现在两处：主从式左列表的每一行，与全站矩阵的行头。
 * 两处各写一遍必然漂移成「列表说已设定范围 3 个、矩阵说空」，而运营会据此
 * 认为页面坏了并去重配一遍 —— 重配的动作恰好是撤销与改价。
 */
export function QyGmScopeStateBadges(props: QyGmScopeStateBadgesProps) {
  const { t } = useTranslation()

  // 三态一律读后端下发的 scope_state，**不自己用 managed + 可见列里勾了几个推**。
  // 两套判据在「范围里那个模型分组已从分组倍率表消失」这一种输入上会给出相反
  // 答案：后端是 set（清单里确实有一项，并另出一条告警），前端因为列轴上没有
  // 那一列而算出 0、画成红色的「空」。运营照红色去随便勾几个把它消掉，真正的
  // 悬空授权既没被修，也不再有提示。
  const scoped = props.userGroup.scope_state !== 'unset'
  const emptyScope = props.userGroup.scope_state === 'empty'

  return (
    <>
      {!scoped && (
        <Badge
          variant='outline'
          className='text-muted-foreground px-1 py-0 text-[10px]'
          title={props.unsetHint ?? t('qy_group_scope_unset_hint')}
        >
          {t('qy_group_scope_state_unset')}
        </Badge>
      )}
      {scoped && !emptyScope && (
        <Badge
          variant='outline'
          className='border-info/50 text-info px-1 py-0 text-[10px]'
        >
          {t('qy_group_scope_state_set', { count: props.grantedCount })}
        </Badge>
      )}
      {emptyScope && (
        <Badge
          variant='outline'
          className='border-destructive/60 text-destructive px-1 py-0 text-[10px]'
          title={
            props.emptyHint ??
            t('qy_group_scope_state_empty_hint', {
              userGroup: props.userGroup.name,
            })
          }
        >
          {t('qy_group_scope_state_empty')}
        </Badge>
      )}
      {/* 第二段：范围以什么力度生效。未设定范围时没有这一段 ——
          给一个不存在的范围标 shadow/enforce 是纯粹的噪声。 */}
      {scoped && (
        <Badge
          variant='outline'
          className={cn(
            'px-1 py-0 text-[10px]',
            props.userGroup.mode === 'enforce'
              ? 'border-destructive/50 text-destructive'
              : 'border-warning/50 text-warning'
          )}
        >
          {props.userGroup.mode === 'enforce'
            ? t('qy_group_matrix_mode_enforce')
            : t('qy_group_matrix_mode_shadow')}
        </Badge>
      )}
    </>
  )
}
