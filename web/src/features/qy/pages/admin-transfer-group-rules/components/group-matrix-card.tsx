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
import { ArrowRight, Check, Minus } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from '@/lib/utils'

import type { QyTransferGroupMatrixRow } from '../types'

type QyGroupMatrixCardProps = {
  matrix: QyTransferGroupMatrixRow[]
  /**
   * 行与列的取值域，由后端下发。
   *
   * **包含规则表自己引用到的分组**：只取「站点定义过的分组」时，刚给一个未定义
   * 分组配好的规则在这张表里既不成行也不成列 —— 那一行看起来就是「谁都转不了」，
   * 而实际判定是放行的。矩阵与判定说的话必须是同一句。
   */
  knownGroups: string[]
  /** 站点没定义过的分组名（已归一）。只打黄标，不影响任何格子的取值。 */
  unknownGroups: Set<string>
}

/**
 * 「当前谁能转给谁」。
 *
 * 这一块是本页存在的理由。规则的形式是「策略 + 名单」，而运营真正要回答的问题
 * 是「A 组现在到底能转给谁」—— 兜底规则、`@self`、黑名单三者叠加之后，靠肉眼
 * 从规则列表推这个结论极易出错，而配错的直接后果是钱转到了不该去的地方。
 *
 * 每一格都是**后端用真正的判定函数**算出来的（`buildGroupMatrix` 逐格调
 * `allowsGroup`），前端只负责画。前端自己推会与后端分家，而那比没有矩阵更
 * 危险：它会让人放心地配错。
 */
export function QyGroupMatrixCard(props: QyGroupMatrixCardProps) {
  const { t } = useTranslation()
  const columns = props.knownGroups

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_trg_matrix_title')}</CardTitle>
        <CardDescription>{t('qy_trg_matrix_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <div className='w-full overflow-x-auto'>
          <table className='w-full min-w-[560px] border-collapse text-sm'>
            <caption className='sr-only'>{t('qy_trg_matrix_title')}</caption>
            <thead>
              <tr>
                <th
                  scope='col'
                  className='text-muted-foreground border-b p-2 text-start text-xs font-medium whitespace-nowrap'
                >
                  <span className='inline-flex items-center gap-1'>
                    {t('qy_trg_matrix_from')}
                    <ArrowRight className='size-3' aria-hidden='true' />
                    {t('qy_trg_matrix_to')}
                  </span>
                </th>
                {columns.map((group) => (
                  <th
                    key={group}
                    scope='col'
                    className={cn(
                      'border-b p-2 text-center text-xs font-medium',
                      props.unknownGroups.has(group)
                        ? 'text-warning'
                        : 'text-muted-foreground'
                    )}
                    title={
                      props.unknownGroups.has(group)
                        ? t('qy_trg_unknown_group_hint')
                        : undefined
                    }
                  >
                    {group}
                    {props.unknownGroups.has(group) && (
                      <span className='sr-only'>
                        {' '}
                        {t('qy_trg_unknown_group_hint')}
                      </span>
                    )}
                  </th>
                ))}
                <th
                  scope='col'
                  className='text-muted-foreground border-b p-2 text-start text-xs font-medium whitespace-nowrap'
                >
                  {t('qy_trg_field_policy')}
                </th>
              </tr>
            </thead>
            <tbody>
              {props.matrix.map((row) => {
                const allowed = new Set(row.to_groups)
                return (
                  <tr key={row.from_group}>
                    <th
                      scope='row'
                      className={cn(
                        'border-b p-2 text-start font-medium whitespace-nowrap',
                        props.unknownGroups.has(row.from_group) &&
                          'text-warning'
                      )}
                      title={
                        props.unknownGroups.has(row.from_group)
                          ? t('qy_trg_unknown_group_hint')
                          : undefined
                      }
                    >
                      {row.from_group}
                      {props.unknownGroups.has(row.from_group) && (
                        <span className='sr-only'>
                          {' '}
                          {t('qy_trg_unknown_group_hint')}
                        </span>
                      )}
                    </th>
                    {columns.map((group) => (
                      <MatrixCell
                        key={group}
                        allowed={allowed.has(group)}
                        from={row.from_group}
                        to={group}
                      />
                    ))}
                    <td className='border-b p-2'>
                      <Badge
                        variant={
                          row.policy === 'deny_all' ? 'destructive' : 'outline'
                        }
                      >
                        {t(`qy_trg_policy_${row.policy}`)}
                      </Badge>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('qy_trg_matrix_note')}
        </p>
      </CardContent>
    </Card>
  )
}

/**
 * 单格。
 *
 * 图标一律配 `sr-only` 文字：只靠 ✓/— 的形状与颜色区分「能转/不能转」，
 * 读屏用户与色觉障碍用户拿到的是一张空表。
 */
function MatrixCell(props: { allowed: boolean; from: string; to: string }) {
  const { t } = useTranslation()
  const label = t(
    props.allowed ? 'qy_trg_matrix_allowed' : 'qy_trg_matrix_denied',
    { from: props.from, to: props.to }
  )
  return (
    <td
      className={cn(
        'border-b p-2 text-center',
        props.allowed ? 'text-success' : 'text-muted-foreground/50'
      )}
    >
      {props.allowed ? (
        <Check className='inline size-4' aria-hidden='true' />
      ) : (
        <Minus className='inline size-4' aria-hidden='true' />
      )}
      <span className='sr-only'>{label}</span>
    </td>
  )
}
