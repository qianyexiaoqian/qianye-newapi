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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import { QyGmMatrixCell } from '../../admin-group-matrix/components/matrix-cell'
import { QyGmScopeStateBadges } from '../../admin-group-matrix/components/scope-state-badges'
import {
  qyGmCellKey,
  qyGmGrantedOf,
  qyGmNoteDraftOf,
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
   * 按格备注的改动。
   *
   * 缺省 = **不渲染备注输入框**。整页矩阵视图那个外壳一次要看多档，行里再塞
   * 一个自由文本框会把它压成一堵墙；配置弹窗一次只看一档，才画得下。
   */
  onNoteChange?: (modelGroup: string, note: string) => void
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

  /*
    每一行的勾选框都要有一个**可见**的标签，而 `<label htmlFor>` 需要一个 id。

    在这里取一次前缀、行内拼下标，而不是把整行拆成一个子组件只为了能调
    `useId()`：下标在同一次渲染的列表里唯一，而这个前缀在组件实例的整个生命周期
    里稳定。名字本身不能当 id —— 分组名是运营自由输入的字符串（站里已经有
    `浅夜の梦专属号池` 这类名字），空格与特殊字符会拼出无效的 id。
  */
  const toggleIdPrefix = useId()

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

      {/*
        没设范围时下面每一行的勾选框都是禁用的，而**禁用本身不解释自己**。

        上面的副标已经说了「未设定范围」意味着什么，但那是一句状态陈述；运营在
        这一屏真正会做的动作是「去点那些勾选框」，所以指路必须紧贴清单、并且
        指向同屏那个真的能解决问题的控件。缺这一句时，界面的表现是"一排点不动的
        灰勾选框，屏幕上没有一个字说为什么" —— 与项目方在删除按钮上投诉的是同一
        个形状。

        ── `where` 必须跟着外壳走 ──

        这个组件有两个外壳，而它们的范围入口**不是同一个东西**：编辑弹窗把范围
        表单内嵌在同屏上一节（标题就是 `qy_ug_scope_section`），独立页则在详情面
        板右上角放一枚按钮（文案 `qy_group_scope_edit`）。写死其中一个，另一个外
        壳上的运营就会照着指路去找一段根本不存在的东西 —— 那与"屏幕上没有一个
        字"是同一类失败，只是换成了指错路。判据用 `onEditScope`（第 152 行渲染那
        枚按钮的同一个判据），并且 `where` 直接取那个控件**自己的**文案键：控件
        改名时这句指路跟着改，不会留下一句谁也没在维护的旧称呼。
      */}
      {!scoped && (
        <p className='text-muted-foreground rounded-md border border-dashed p-2 text-xs leading-5'>
          {t('qy_ug_grant_needs_scope', {
            where:
              props.onEditScope != null
                ? t('qy_group_scope_edit')
                : t('qy_ug_scope_section'),
          })}
        </p>
      )}

      <ul
        className={cn(
          'min-h-0 space-y-1',
          (props.scrollList ?? true) && 'max-h-[52vh] overflow-y-auto'
        )}
      >
        {props.data.model_groups.map((column, index) => {
          const key = qyGmCellKey(props.userGroup.name, column.name)
          const cell = props.serverCells.get(key)
          const entry = props.draft.get(key)
          const granted = qyGmGrantedOf(cell, entry)
          const toggleId = `${toggleIdPrefix}-${index}`
          return (
            <li
              key={column.name}
              className='grid grid-cols-1 items-center gap-1 rounded-md p-1 sm:grid-cols-[minmax(0,1fr)_minmax(9rem,14rem)] sm:gap-2'
            >
              <div className='min-w-0'>
                <div className='flex flex-wrap items-center gap-1.5'>
                  {/*
                    模型分组名就是那一格勾选框的标签。绑上 `htmlFor` 之后点名字
                    即可切换（一行里最大的那块可点区域），读屏也念得出这个勾选框
                    管的是哪一个模型分组 —— 光靠格子里那个 `aria-label` 的话，
                    可见文字与控件之间在 DOM 上没有任何关联，而这一列恰好是运营
                    唯一用来定位"我在改哪一行"的东西。
                  */}
                  <label
                    htmlFor={toggleId}
                    className={cn(
                      'min-w-0 truncate text-xs font-medium',
                      scoped && 'cursor-pointer'
                    )}
                    title={t('qy_group_matrix_col_label', {
                      modelGroup: column.name,
                    })}
                  >
                    {column.name}
                  </label>
                  {/*
                    「已放开 / 未放开」写成字，不只靠勾选框的形状。

                    这一列一屏能排十几行，而勾选框的选中态是一个 16px 的色块；
                    运营在这一屏要回答的第一个问题是「这一档现在到底开了哪几个」，
                    那是一次**扫描**而不是一次逐格辨认。文字状态让扫描成立，
                    也让"我刚才那一下点中了没有"当场可见 —— 顶部计数条要滚到
                    上面才看得到。
                  */}
                  <Badge
                    variant='outline'
                    className={cn(
                      'px-1 py-0 text-[10px]',
                      granted
                        ? 'border-success/50 text-success'
                        : 'text-muted-foreground'
                    )}
                  >
                    {granted
                      ? t('qy_ug_grant_state_on')
                      : t('qy_ug_grant_state_off')}
                  </Badge>
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
                granted={granted}
                ratio={qyGmRatioDraftOf(cell, entry)}
                baseRatio={column.base_ratio}
                scoped={scoped}
                reachableVia={cell?.reachable_via}
                planTitles={cell?.plan_titles}
                selfEdge={column.name === props.userGroup.name}
                toggleId={toggleId}
                onToggleGranted={(next) =>
                  props.onToggleGranted(column.name, next)
                }
                onRatioChange={(ratio) =>
                  props.onRatioChange(column.name, ratio)
                }
              />

              {/*
                按格备注独占一行、横跨两列。

                ── 为什么它是 placeholder 而不是预填值 ──

                项目方原话：「用户分组分配模型分组的时候若不填写则显示分组备注
                （默认），若填写以用户分组的模型分组备注优先显示」。placeholder
                里放的就是「不填时用户会看到的那一句」—— 它同时是提示与预览。
                预填成真实值的话，运营随手保存一遍就把默认备注固化成一份复制品，
                此后运营在模型分组页改默认备注，这一格再也不跟着变，而没有任何
                人做过这个决定。倍率格上是同一条规则、同一个理由。
              */}
              {props.onNoteChange != null && (
                <QyUgCellNote
                  modelGroup={column.name}
                  cell={cell}
                  value={qyGmNoteDraftOf(cell, entry)}
                  scoped={scoped}
                  enforced={props.userGroup.scope_enforced}
                  granted={granted}
                  onChange={(note) => props.onNoteChange?.(column.name, note)}
                />
              )}
            </li>
          )
        })}
      </ul>
    </div>
  )
}

/**
 * 一格的「按格备注」输入框。
 *
 * ── 它为什么会被禁用，以及为什么必须**说出来** ──
 *
 * 后端对这条动作有两道硬闸门，两道都会返回 400：
 *
 *  1. 用户分组还没有自己的可用清单（`scope_state === 'unset'`）—— 按格备注只在
 *     权威清单生效的那一档被读到，给一个没设范围的分组写备注是一条死配置。
 *  2. 这一格没被勾中 —— 落库只 UPDATE 不 INSERT，没有 grant 行的格子写不进去。
 *
 * 早先这个输入框对两种情形都照常可编辑，运营敲完点保存才拿到一句让他去发 HTTP
 * 请求的报错（而站上绝大多数分组正处在第 1 种情形）。禁用 + `title` 把那句话
 * 提前到他动手之前，并且说的是界面上真实存在的控件。
 *
 * ── 「写进去了但用户看不到」这一档 ──
 *
 * `mode=shadow` 时清单一个字节都不生效，按格备注同样不生效（读侧逐位返回上游）。
 * 这时备注**写得进库**，所以不禁用 —— 运营正当地"先配好再切强制"。但后端回读的
 * `note_pending` 会为真，这里必须挂一句灰字说明用户此刻实际看到的是哪一段，
 * 否则界面上它与"已生效"长得一模一样。
 */
function QyUgCellNote(props: {
  modelGroup: string
  cell: QyGmCell | undefined
  value: string
  scoped: boolean
  enforced: boolean
  granted: boolean
  onChange: (note: string) => void
}) {
  const { t } = useTranslation()
  const { cell } = props

  let disabledReason: string | undefined
  if (!props.scoped) disabledReason = t('qy_ug_cell_note_needs_scope')
  else if (!props.granted) disabledReason = t('qy_ug_cell_note_needs_grant')

  return (
    <div className='space-y-1 sm:col-span-2'>
      <Input
        value={props.value}
        className='h-7 text-xs'
        disabled={disabledReason != null}
        title={disabledReason}
        aria-label={t('qy_ug_cell_note_label', {
          modelGroup: props.modelGroup,
        })}
        // placeholder 里放的是**用户此刻真的会看到的那一句**（后端解析完四级
        // 阶梯之后的结果），所以它同时是提示与预览。这一格自己写的备注真的生效
        // 时 `note_source === 'grant'`，输入框里已经是那一句，不再重复。
        placeholder={
          cell == null || cell.note_source === 'grant'
            ? t('qy_ug_cell_note_placeholder')
            : t('qy_ug_cell_note_inherits', { note: cell.effective_note })
        }
        onChange={(event) => props.onChange(event.target.value)}
      />
      {disabledReason != null && (
        <p className='text-muted-foreground text-[11px] leading-4'>
          {disabledReason}
        </p>
      )}
      {/*
        写进去了、但用户一个字都看不到。判据用后端下发的 `note_pending`，
        不在前端拿 `note` 与 `note_source` 自己比：那条比法是一条隐式规则，
        漏在任何一个外壳上，那个外壳就会把影子期的备注画成已生效。
      */}
      {disabledReason == null && cell?.note_pending === true && (
        <p className='text-warning text-[11px] leading-4'>
          {t('qy_ug_cell_note_pending', {
            note: cell.effective_note,
            state: props.enforced
              ? t('qy_ug_cell_note_pending_generic')
              : t('qy_ug_scope_shadow'),
          })}
        </p>
      )}
    </div>
  )
}
