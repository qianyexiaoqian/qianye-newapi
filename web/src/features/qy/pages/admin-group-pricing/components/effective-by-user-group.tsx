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
import { TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

import { QyAmountText } from '../../../components/qy-amount-text'
import type { QyGpUserGroupEffective } from '../types'

/**
 * 一条规则在**各用户分组**下的最终生效价。
 *
 * # 这块 UI 为什么必须存在
 *
 * 规则的键是 (模型分组, 模型)，而真实倍率的键是 (用户分组, 模型分组)。
 * 因此一条规则的「最终生效价」结构上就是**一组值**，不是一个数。
 * 页面上那个头条数字只对「没有配专属倍率的用户分组」成立 ——
 * 在配了 `GroupGroupRatio` 的站点上，它对一部分人是错的。
 *
 * 硬凑一个标量就是继续骗人，所以这里把那一组值原样摊开。
 *
 * # 只列真的配了专属倍率的用户分组
 *
 * 后端不给全站每个用户分组都铺一行：没配专属倍率的分组，数字与兜底行逐位相同，
 * 铺出来只有噪音。第一行恒为 `*`（兜底口径），它覆盖的人最多。
 */

/** 兜底口径那一行的哨兵值，与后端 `wildcardUserGroup` 逐字一致。 */
const WILDCARD_USER_GROUP = '*'

type QyGpEffectiveByUserGroupProps = {
  rows: QyGpUserGroupEffective[]
  /** 当前面板正站在哪个用户分组的角度。该行会被高亮。空串表示兜底口径。 */
  highlight?: string
  className?: string
}

export function QyGpEffectiveByUserGroup(props: QyGpEffectiveByUserGroupProps) {
  const { t } = useTranslation()

  // 只有兜底一行 = 这个模型分组根本没有任何专属倍率，头条数字对所有人成立，
  // 再摊一张表只是噪音。
  if (props.rows.length <= 1) return null

  return (
    <div className={cn('space-y-1.5', props.className)}>
      <p className='text-muted-foreground text-xs font-medium'>
        {t('qy_group_pricing_effective_by_user_group')}
      </p>
      <div className='overflow-x-auto'>
        <table className='w-full text-xs'>
          <tbody>
            {props.rows.map((row) => (
              <QyGpUserGroupRow
                key={row.user_group}
                row={row}
                active={isActive(row, props.highlight)}
              />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

/**
 * 当前面板的口径落在哪一行。
 *
 * 兜底行的匹配条件是「没选用户分组」或「选的那个分组没有专属倍率」——
 * 后者判断不了（前端手上没有那份 map），所以只在明确为空时高亮兜底行。
 * 宁可不高亮，也不能高亮错一行：那会让人以为自己看的是另一个分组的价。
 */
function isActive(
  row: QyGpUserGroupEffective,
  highlight: string | undefined
): boolean {
  if (highlight == null || highlight === '') {
    return row.user_group === WILDCARD_USER_GROUP
  }
  return row.user_group === highlight
}

function QyGpUserGroupRow(props: {
  row: QyGpUserGroupEffective
  active: boolean
}) {
  const { t } = useTranslation()
  const row = props.row
  const wildcard = row.user_group === WILDCARD_USER_GROUP

  return (
    <>
      <tr className={cn(props.active && 'bg-muted/60')}>
        <td className='py-1 pe-2 align-top'>
          <span className='inline-flex items-center gap-1.5'>
            <span
              className={cn('font-medium', wildcard && 'text-muted-foreground')}
            >
              {wildcard ? t('qy_gp_user_group_wildcard') : row.user_group}
            </span>
            <Badge
              variant='outline'
              className={cn(
                'px-1 py-0 text-[10px]',
                row.source === 'override' && 'border-primary/50 text-primary'
              )}
            >
              {t(`qy_gp_ratio_source_${row.source}`)}
            </Badge>
          </span>
        </td>
        <td className='text-muted-foreground py-1 pe-2 text-end align-top tabular-nums'>
          ×&nbsp;{row.group_ratio}
        </td>
        <td className='py-1 text-end align-top font-semibold tabular-nums'>
          {row.rule_effective}
        </td>
        <td className='py-1 ps-2 align-top'>
          {/* 按次口径额外给 quota：运营看的是余额数字，quota 才是账面上真正减少的量。 */}
          {row.quota_per_call != null && (
            <QyAmountText
              quota={row.quota_per_call}
              className='text-muted-foreground'
            />
          )}
        </td>
      </tr>
      {row.warning != null && row.warning !== '' && (
        <tr>
          {/* 告警必须紧贴它所属的那一格。放进一个统一的告警区会让人看不出
              「是哪个用户分组有问题」，而那正是这条告警唯一有用的信息。 */}
          <td colSpan={4} className='text-warning pb-1.5'>
            <span className='flex items-start gap-1'>
              <TriangleAlert
                className='mt-0.5 size-3 shrink-0'
                aria-hidden='true'
              />
              <span>{row.warning}</span>
            </span>
          </td>
        </tr>
      )}
    </>
  )
}
