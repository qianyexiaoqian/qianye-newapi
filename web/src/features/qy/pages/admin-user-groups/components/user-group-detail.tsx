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
import { Boxes, Settings2, TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

import { QyGmMatrixCell } from '../../admin-group-matrix/components/matrix-cell'
import { QyGmScopeStateBadges } from '../../admin-group-matrix/components/scope-state-badges'
import {
  qyGmCellKey,
  qyGmGrantedOf,
  qyGmRatioDraftOf,
  type QyGmDraft,
  type QyGmRatioDraft,
} from '../../admin-group-matrix/lib/draft'
import type {
  QyGmCell,
  QyGmMatrixResponse,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

type QyUgGroupDetailProps = {
  data: QyGmMatrixResponse
  userGroup: QyGmUserGroup
  serverCells: ReadonlyMap<string, QyGmCell>
  draft: QyGmDraft
  /** 草稿合并后在范围内的模型分组数。与左列表徽章共用同一个数。 */
  grantedCount: number
  onToggleGranted: (modelGroup: string, granted: boolean) => void
  onRatioChange: (modelGroup: string, ratio: QyGmRatioDraft) => void
  /**
   * 打开「范围设置」抽屉。
   *
   * 缺省 = **不渲染那个按钮**。配置弹窗（需求 4）把范围表单直接内嵌在同一屏里，
   * 那里再放一个"编辑范围"就会开出第二层浮层，而下面那一层正握着未保存的草稿。
   */
  onEditScope?: () => void
  onCopyFrom: (fromUserGroup: string) => void
  /**
   * 模型分组清单自己滚（缺省 true）。
   *
   * 主从式那一页里它与左列表并排，无限长的清单会把详情面板顶到屏幕外，所以要
   * 内滚。内嵌进弹窗时相反：弹窗正文本身就是滚动区，再套一层内滚动会出现两个
   * 滚动条，鼠标停在清单上时外层完全不动 —— 用户滚不到下面的东西。
   */
  scrollList?: boolean
}

/**
 * 右侧详情：**这一个用户分组**的模型分组分配 + 每个模型分组的倍率。
 *
 * ── 为什么可选性与倍率并排，而不是拆成两个区块 ──
 *
 * 它们是两份独立的数据（可选性落扩展库 `qy_group_grants`，倍率落上游
 * `options.GroupGroupRatio`），但运营做的是**一个**决定：「这一档的人能不能用
 * 这批渠道、用的话按什么价」。拆开之后，「放开了但忘了配价」这种组合会分散在
 * 两个区块里，而它的后果是一批用户立刻按兜底倍率扣钱 —— 恰好是本页最要防的
 * 那件事。所以一行 = 一个模型分组，可选性与倍率在同一行里。
 *
 * ── 倍率框在「不可选」的行上同样可编辑 ──
 *
 * 这不是疏忽。倍率在下面几种情形里 100% 在扣钱：该用户分组尚未设定范围（此时
 * 它能用全部模型分组）、设了范围但处于影子模式、以及同名的那一行（分组为空的
 * 令牌恒等于属主的用户分组，整段跳过可选性检查）。再加上全新安装时一条范围都
 * 没有 —— 而那是本轮的默认状态。倍率框随可选性一起消失，这一页会变成「一个
 * 倍率都改不了」，运营唯一的出路是先去给某一档设范围，而那是访问控制动作，
 * 不是改价动作。判定与渲染都在 {@link QyGmMatrixCell} 里，两个视图共用一份。
 */
export function QyUgGroupDetail(props: QyUgGroupDetailProps) {
  const { t } = useTranslation()

  const scoped = props.userGroup.scope_state !== 'unset'
  const emptyScope = props.userGroup.scope_state === 'empty'
  const otherUserGroups = props.data.user_groups
    .map((row) => row.name)
    .filter((name) => name !== props.userGroup.name)

  return (
    <div className='flex min-w-0 flex-col gap-3 rounded-lg border p-3'>
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='min-w-0 space-y-1'>
          <div className='flex flex-wrap items-center gap-1.5'>
            <span className='text-sm font-semibold'>
              {t('qy_group_matrix_row_label', {
                userGroup: props.userGroup.name,
              })}
            </span>
            <QyGmScopeStateBadges
              userGroup={props.userGroup}
              grantedCount={props.grantedCount}
            />
          </div>
          {/*
            副标必须跟着**这一档此刻的状态**走。写死成任意一句，对另一半的分组
            就是一句假陈述 —— 而这一句正是运营判断「我在这里勾掉一格到底有没有
            用」的唯一依据。未设定范围时勾选是禁用的（见格子组件），如果副标还在
            说「只有勾选的可用」，运营会以为界面坏了。
          */}
          <p className='text-muted-foreground text-xs'>
            {scoped
              ? t('qy_group_scope_switch_on_desc')
              : t('qy_group_scope_unset_hint')}
          </p>
        </div>

        <div className='flex shrink-0 items-center gap-2'>
          {props.onEditScope != null && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={props.onEditScope}
            >
              {t('qy_group_scope_edit')}
            </Button>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  type='button'
                  variant='ghost'
                  size='icon'
                  className='size-8'
                  aria-label={t('qy_group_matrix_row_menu', {
                    userGroup: props.userGroup.name,
                  })}
                />
              }
            >
              <Settings2 aria-hidden='true' className='size-4' />
            </DropdownMenuTrigger>
            <DropdownMenuContent align='end'>
              {/*
                整档复制只对**已设定范围**的分组开放：未设定范围的分组没有权威
                清单，往它头上写 grant 只会生成一批后端必然拒绝的动作，而运营
                从界面上看不出保存为什么失败。
              */}
              {otherUserGroups.map((name) => (
                <DropdownMenuItem
                  key={name}
                  disabled={!scoped}
                  onClick={() => props.onCopyFrom(name)}
                >
                  {t('qy_group_matrix_copy_row', { userGroup: name })}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      {/* 设了范围却一格都没勾：合法（隔离组），但强制模式下这一档什么都选不了。
          告警而不是拦截 —— 项目方要它能被推翻。 */}
      {emptyScope && (
        <p className='text-warning flex items-start gap-1.5 rounded-md border border-dashed p-2 text-xs'>
          <TriangleAlert
            aria-hidden='true'
            className='mt-0.5 size-3 shrink-0'
          />
          <span>
            {t('qy_group_scope_state_empty_hint', {
              userGroup: props.userGroup.name,
            })}
          </span>
        </p>
      )}

      <p className='text-muted-foreground text-xs'>
        {t('qy_group_ratio_inherit_vs_zero_hint')}
      </p>

      <div className='flex items-center gap-1.5 text-xs font-medium'>
        <Boxes aria-hidden='true' className='size-3.5 shrink-0' />
        {t('qy_group_matrix_axis_col_title')}
      </div>

      <ul
        className={cn(
          'min-h-0 space-y-1',
          (props.scrollList ?? true) && 'max-h-[52vh] overflow-y-auto'
        )}
      >
        {props.data.model_groups.map((column) => {
          const key = qyGmCellKey(props.userGroup.name, column.name)
          const cell = props.serverCells.get(key)
          const entry = props.draft.get(key)
          return (
            <li
              key={column.name}
              className='grid grid-cols-1 items-center gap-1 rounded-md p-1 sm:grid-cols-[minmax(0,1fr)_minmax(9rem,14rem)] sm:gap-2'
            >
              <div className='min-w-0'>
                <div className='flex flex-wrap items-center gap-1.5'>
                  <span
                    className='min-w-0 truncate text-xs font-medium'
                    title={t('qy_group_matrix_col_label', {
                      modelGroup: column.name,
                    })}
                  >
                    {column.name}
                  </span>
                  <Badge
                    variant='outline'
                    className='text-muted-foreground px-1 py-0 text-[10px] tabular-nums'
                  >
                    {t('qy_group_matrix_base_ratio', {
                      ratio: column.base_ratio,
                    })}
                  </Badge>
                  {/* 放开一个没有渠道的模型分组 = 放开一个空池子。它在挑的那
                      一刻就必须看得见，选完再说已经晚了。 */}
                  {!column.has_channels && (
                    <Badge
                      variant='outline'
                      className='border-warning/50 text-warning px-1 py-0 text-[10px]'
                      title={t('qy_group_matrix_col_no_channels')}
                    >
                      {t('qy_group_matrix_col_no_channels_short')}
                    </Badge>
                  )}
                </div>
              </div>

              <QyGmMatrixCell
                userGroup={props.userGroup.name}
                modelGroup={column.name}
                cell={cell}
                entry={entry}
                granted={qyGmGrantedOf(cell, entry)}
                ratio={qyGmRatioDraftOf(cell, entry)}
                baseRatio={column.base_ratio}
                scoped={scoped}
                reachableVia={cell?.reachable_via}
                planTitles={cell?.plan_titles}
                selfEdge={column.name === props.userGroup.name}
                onToggleGranted={(granted) =>
                  props.onToggleGranted(column.name, granted)
                }
                onRatioChange={(ratio) =>
                  props.onRatioChange(column.name, ratio)
                }
              />
            </li>
          )
        })}
      </ul>
    </div>
  )
}
