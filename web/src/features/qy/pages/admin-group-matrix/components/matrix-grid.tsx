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
import {
  ArrowDown,
  ArrowRight,
  Boxes,
  MoreHorizontal,
  Settings2,
  TriangleAlert,
  Users,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

import { formatQyCount } from '../../ops/format'
import {
  qyGmCellKey,
  qyGmGrantedOf,
  qyGmRatioDraftOf,
  type QyGmDraft,
  type QyGmRatioDraft,
} from '../lib/draft'
import type { QyGmCell, QyGmMatrixResponse } from '../types'
import { QyGmMatrixCell } from './matrix-cell'
import { QyGmScopeStateBadges } from './scope-state-badges'

export type QyGmColumnBulk = 'clear' | 'inherit_all' | 'select_all'

type QyGmMatrixGridProps = {
  data: QyGmMatrixResponse
  serverCells: ReadonlyMap<string, QyGmCell>
  draft: QyGmDraft
  onToggleGranted: (
    userGroup: string,
    modelGroup: string,
    granted: boolean
  ) => void
  onRatioChange: (
    userGroup: string,
    modelGroup: string,
    ratio: QyGmRatioDraft
  ) => void
  onColumnBulk: (modelGroup: string, action: QyGmColumnBulk) => void
  onCopyRow: (fromUserGroup: string, toUserGroup: string) => void
  onEditScope: (userGroup: string) => void
}

/**
 * 用户分组 × 模型分组 矩阵。
 *
 * ── 两个轴始终分列显示，绝不合并成一个下拉 ──
 * 方案 3 的已知代价是 `users.group` 与 `channels.group` 共用同一个字符串
 * 命名空间，两个轴上可能出现同名项。界面必须替运营把它们分开：行永远是
 * 「谁」，列永远是「用哪批渠道」。合并成一个选择器，同名项会看起来像一件事。
 *
 * 用 CSS Grid 而不是 `<table>`：粘性行头 + 粘性列头 + 每格一个输入框这组合在
 * table 上要靠一堆 `position: sticky` 的补丁，而 grid 上是两行样式。
 * 与可用率热力图（`pages/availability/components/availability-matrix.tsx`）
 * 同一套骨架，两页的滚动与对齐行为因此一致。
 *
 * 本站规模 7 × 15，**本轮不做虚拟滚动**：虚拟化会让「整列全选」这类跨行操作
 * 需要额外处理未挂载的行，代价换不来收益。
 */
export function QyGmMatrixGrid(props: QyGmMatrixGridProps) {
  const { t } = useTranslation()

  const columns = `minmax(13rem, 1.2fr) repeat(${props.data.model_groups.length}, minmax(7rem, 1fr))`

  return (
    /*
      `max-h` + `overflow-auto` 是列头 `sticky top-0` 生效的前提。
      只写 `overflow-x-auto` 时容器高度随内容增长、scrollHeight == clientHeight，
      纵向根本不产生 scrollport，列头（连同它上面的初始倍率）会随页面一起滚出
      视野 —— 行数一多，运营就在只看得到数字格的情况下继续编辑，很容易把倍率
      填到错误的列上。本站 7 行看不出来，按等级细分之后就会暴露。
    */
    <div className='max-h-[70vh] overflow-auto rounded-lg border'>
      <div className='min-w-max'>
        {/* 列头：模型分组 + 它的初始倍率。倍率放在列头而不是每格重复一遍 ——
            继承格的占位符已经在说同一件事，列头是它的唯一定义处。 */}
        <div
          className='bg-muted/50 sticky top-0 z-10 grid border-b'
          style={{ gridTemplateColumns: columns }}
        >
          {/*
            左上角是两个轴的定义处，**不是一句 `行 × 列` 的标题**。

            底层两个轴共用同一个字符串命名空间，两边可能出现同名项；把定义压缩成
            「用户分组 × 模型分组」一行字，运营扫一眼只会记住一个"分组"。这里改成
            上下两行、各带自己的图标与方向箭头 —— 图标与箭头在滚动时始终贴在
            左上角，是这一页上唯一一处**永远可见**的图例。
          */}
          <div className='bg-muted/50 border-info/40 sticky left-0 z-20 flex flex-col gap-0.5 border-s-2 px-3 py-1.5 text-[11px]'>
            <span className='flex items-center gap-1 font-medium'>
              <Users aria-hidden='true' className='size-3 shrink-0' />
              {t('qy_group_matrix_axis_row_title')}
              <ArrowDown
                aria-hidden='true'
                className='text-muted-foreground size-2.5 shrink-0'
              />
            </span>
            <span className='text-muted-foreground flex items-center gap-1'>
              <Boxes aria-hidden='true' className='size-3 shrink-0' />
              {t('qy_group_matrix_axis_col_title')}
              <ArrowRight aria-hidden='true' className='size-2.5 shrink-0' />
            </span>
          </div>
          {props.data.model_groups.map((modelGroup) => (
            <div
              key={modelGroup.name}
              className='flex flex-col items-center gap-0.5 px-1 py-1.5'
            >
              <div className='flex w-full items-center justify-center gap-0.5'>
                {/*
                  每一列都带模型分组图标，而不是只在表头出现一次。
                  行头也有它自己的图标（Users）—— 两个轴上出现同名项时，
                  图标是唯一一个跟着名字一起走的区分信号。
                */}
                <Boxes
                  aria-hidden='true'
                  className='text-muted-foreground size-3 shrink-0'
                />
                <span
                  className='truncate text-xs font-medium'
                  title={t('qy_group_matrix_col_label', {
                    modelGroup: modelGroup.name,
                  })}
                >
                  {modelGroup.name}
                </span>
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button
                        type='button'
                        variant='ghost'
                        size='icon'
                        className='size-5 shrink-0'
                        aria-label={t('qy_group_matrix_col_menu', {
                          modelGroup: modelGroup.name,
                        })}
                      />
                    }
                  >
                    <MoreHorizontal aria-hidden='true' className='size-3' />
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align='end'>
                    <DropdownMenuItem
                      onClick={() =>
                        props.onColumnBulk(modelGroup.name, 'select_all')
                      }
                    >
                      {t('qy_group_matrix_col_select_all')}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onClick={() =>
                        props.onColumnBulk(modelGroup.name, 'clear')
                      }
                    >
                      {t('qy_group_matrix_col_clear')}
                    </DropdownMenuItem>
                    <DropdownMenuItem
                      onClick={() =>
                        props.onColumnBulk(modelGroup.name, 'inherit_all')
                      }
                    >
                      {t('qy_group_matrix_col_inherit_all')}
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              <span className='text-muted-foreground text-[10px] tabular-nums'>
                {t('qy_group_matrix_base_ratio', {
                  ratio: modelGroup.base_ratio,
                })}
              </span>
              {!modelGroup.has_channels && (
                <span
                  className='text-warning inline-flex items-center gap-0.5 text-[10px]'
                  title={t('qy_group_matrix_col_no_channels')}
                >
                  <TriangleAlert aria-hidden='true' className='size-2.5' />
                  {t('qy_group_matrix_col_no_channels_short')}
                </span>
              )}
            </div>
          ))}
        </div>

        {props.data.user_groups.map((userGroup) => (
          <div
            key={userGroup.name}
            className='hover:bg-muted/20 grid border-b last:border-b-0'
            style={{ gridTemplateColumns: columns }}
          >
            <QyGmRowHeader
              userGroup={userGroup}
              grantedCount={
                props.data.model_groups.filter((column) =>
                  qyGmGrantedOf(
                    props.serverCells.get(
                      qyGmCellKey(userGroup.name, column.name)
                    ),
                    props.draft.get(qyGmCellKey(userGroup.name, column.name))
                  )
                ).length
              }
              otherUserGroups={props.data.user_groups
                .map((row) => row.name)
                .filter((name) => name !== userGroup.name)}
              onCopyRow={(from) => props.onCopyRow(from, userGroup.name)}
              onEditScope={() => props.onEditScope(userGroup.name)}
            />
            {props.data.model_groups.map((modelGroup) => {
              const key = qyGmCellKey(userGroup.name, modelGroup.name)
              const cell = props.serverCells.get(key)
              const entry = props.draft.get(key)
              return (
                <QyGmMatrixCell
                  key={modelGroup.name}
                  userGroup={userGroup.name}
                  modelGroup={modelGroup.name}
                  cell={cell}
                  entry={entry}
                  granted={qyGmGrantedOf(cell, entry)}
                  ratio={qyGmRatioDraftOf(cell, entry)}
                  baseRatio={modelGroup.base_ratio}
                  scoped={userGroup.managed}
                  reachableVia={cell?.reachable_via}
                  planTitles={cell?.plan_titles}
                  selfEdge={userGroup.name === modelGroup.name}
                  onToggleGranted={(granted) =>
                    props.onToggleGranted(
                      userGroup.name,
                      modelGroup.name,
                      granted
                    )
                  }
                  onRatioChange={(ratio) =>
                    props.onRatioChange(userGroup.name, modelGroup.name, ratio)
                  }
                />
              )
            })}
          </div>
        ))}
      </div>

      <p className='text-muted-foreground border-t px-3 py-2 text-xs'>
        {t('qy_group_matrix_namespace_note')}
      </p>
    </div>
  )
}

type QyGmRowHeaderProps = {
  userGroup: QyGmMatrixResponse['user_groups'][number]
  /** 草稿合并后这一行当前有几个模型分组在范围里。三态行头的第三态靠它。 */
  grantedCount: number
  otherUserGroups: string[]
  onCopyRow: (fromUserGroup: string) => void
  onEditScope: () => void
}

/**
 * 行头：用户分组 + 在册人数 + 活跃令牌数 + **范围三态**。
 *
 * ── 三态，而不是「已接管 / 未接管」两态 ──
 *
 *   · 未设定范围         全部模型分组可用，各按兜底倍率（= 与上游逐位一致的默认）
 *   · 已设定范围 · N 个   只有这 N 个可用
 *   · 已设定范围 · 空     一个模型分组都不可用（隔离组）—— **红色**
 *
 * 「已接管 / 未接管」这套词在新口径下会直接把人带偏：运营会把「未接管」读成
 * 「不能用」，而它的真实含义恰好是「全都能用」。所以这一页**绝不出现那两个词**。
 *
 * 第三态必须与第二态分开，理由与后端不肯用 grants 行数推断三态一样：
 * 「一条都不许用」与「没配过」的两个错误方向分别是整组用户 403 和整组用户全放行，
 * 而且都静默。
 *
 * 人数与活跃令牌数必须长在行头上而不是藏进弹窗：它们是「收紧这一行会打断谁」
 * 的分母，运营在动格子的那一刻就需要看见，点开才看得到就已经晚了。
 */
function QyGmRowHeader(props: QyGmRowHeaderProps) {
  const { t } = useTranslation()
  // 三态一律读后端下发的 scope_state，**不自己用 managed + 可见列里勾了几个推**。
  // 判定与渲染都收在 {@link QyGmScopeStateBadges} 里，因为同一份状态也出现在
  // 主从式那一页的左列表上 —— 两处各写一遍必然漂移成两个互相矛盾的数字。
  const scoped = props.userGroup.scope_state !== 'unset'

  return (
    /*
      行头带一条 `border-info` 左边条 + Users 图标：这一整列都是「谁」，
      而右边所有列都是「用哪批渠道」。sticky 列必须是不透明背景（否则滚动内容
      会透过来），所以轴身份押在边条与图标上，不押在底色上。
    */
    <div className='bg-background border-info/40 sticky left-0 z-10 flex items-center gap-2 border-s-2 px-3 py-1.5'>
      <div className='min-w-0 flex-1'>
        <div className='flex items-center gap-1.5'>
          <Users
            aria-hidden='true'
            className='text-muted-foreground size-3 shrink-0'
          />
          <span
            className='truncate text-xs font-medium'
            title={t('qy_group_matrix_row_label', {
              userGroup: props.userGroup.name,
            })}
          >
            {props.userGroup.name}
          </span>
          <QyGmScopeStateBadges
            userGroup={props.userGroup}
            grantedCount={props.grantedCount}
          />
        </div>
        <div className='text-muted-foreground text-[10px] tabular-nums'>
          {t('qy_group_matrix_row_stats', {
            users: formatQyCount(props.userGroup.user_count),
            tokens: formatQyCount(props.userGroup.active_token_count),
          })}
        </div>
      </div>

      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              type='button'
              variant='ghost'
              size='icon'
              className='size-6 shrink-0'
              aria-label={t('qy_group_matrix_row_menu', {
                userGroup: props.userGroup.name,
              })}
            />
          }
        >
          <Settings2 aria-hidden='true' className='size-3.5' />
        </DropdownMenuTrigger>
        <DropdownMenuContent align='end'>
          <DropdownMenuItem onClick={props.onEditScope}>
            {t('qy_group_scope_edit')}
          </DropdownMenuItem>
          {props.otherUserGroups.map((name) => (
            <DropdownMenuItem
              key={name}
              disabled={!scoped}
              onClick={() => props.onCopyRow(name)}
            >
              {t('qy_group_matrix_copy_row', { userGroup: name })}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
