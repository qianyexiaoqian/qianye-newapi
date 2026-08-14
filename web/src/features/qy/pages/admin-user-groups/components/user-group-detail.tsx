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
import { Boxes, Package, Plus, Settings2, TriangleAlert, X } from 'lucide-react'
import { useMemo, useState } from 'react'
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

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyGmMatrixCell } from '../../admin-group-matrix/components/matrix-cell'
import { QyGmScopeStateBadges } from '../../admin-group-matrix/components/scope-state-badges'
import {
  qyGmCellKey,
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
import { qyUgListView, type QyUgListItem } from '../lib/assigned-list'
import { QyUgScopeModeStrip, type QyUgScopeSubmit } from './scope-mode-strip'

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
  /** 范围行的提交通道（建清单 / 清空 / shadow ↔ enforce）。 */
  scope: QyUgScopeSubmit
  /**
   * 打开独立的「范围设置」抽屉。
   *
   * 缺省 = **不渲染那个入口**。它现在只剩一个用途：`/qy` 那个高级页上，
   * 直接改 `managed` / `allow_auto` 的逃生口。日常路径（建清单、清空、切
   * 生效力度）全部由这一屏的列表与状态条表达，不需要它。
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
 * 右侧详情：**这一个用户分组**能用哪些模型分组，以及每一项的倍率与备注。
 *
 * ── 它取代了什么 ──
 *
 * 上一版把站上**全部**模型分组平铺成行，每行一个勾选框；而勾选框在"这一档
 * 还没有自己的清单"时全部禁用，于是每一行下面都跟着同一段长文，叫运营先去
 * 上面那段「可用范围的力度」里把开关打开。项目方带截图报的原话是"为何不可以
 * 删除/添加模型分组"—— 初见的读法只能是「什么都点不动」。
 *
 * 病根不在那个开关的位置，在于它**是一个内部状态的直接投影**：`qy_group_scopes`
 * 有没有这一行。运营要做的决定从来只有一个（这一档能用哪些），而界面要求他先
 * 理解一个数据模型、再做一次与业务无关的操作。
 *
 * 现在：
 *
 *   · **已分配的一个列表** —— 名称 / 倍率(可改) / 备注(可改) / 移除；
 *   · **一个添加入口** —— 从尚未分配的里面挑，带兜底倍率与「无渠道」标记；
 *   · 「有没有自己的清单」由**列表内容**隐式表达：第一次增删时后端建 scope 行，
 *     清空时（确认之后）删掉它；
 *   · 「先观察 / 现在就生效」留在列表上方的状态条里 —— 它是真正的运营决定
 *     （灰度），不是前置开关，见 {@link QyUgScopeModeStrip}。
 *
 * ── 倍率框在**每一行**上都可编辑，包括"仅经套餐可达"的那些 ──
 *
 * 倍率与可选清单是两份独立的数据（前者落上游 `options.GroupGroupRatio`，
 * 后者落扩展库）。没有自己清单的那一档、影子模式下的那一档、以及经套餐可达
 * 的那些格子，倍率都 100% 在扣钱。倍率随可选性一起消失，这一屏就会变成
 * 「真的在扣钱的那些价一个都改不了」。判定与渲染共用 {@link QyGmMatrixCell}。
 */
export function QyUgGroupDetail(props: QyUgGroupDetailProps) {
  const { t } = useTranslation()

  const view = useMemo(
    () =>
      qyUgListView(props.data, props.serverCells, props.draft, props.userGroup),
    [props.data, props.serverCells, props.draft, props.userGroup]
  )

  const [addOpen, setAddOpen] = useState(false)
  /** 需要先为这一档建立独立清单，才能落地的那一次增删。 */
  const [pendingCreate, setPendingCreate] = useState<{
    modelGroup: string
    granted: boolean
  } | null>(null)
  /** 移除它就一个都不剩了 —— 二选一还没做完的那一项。 */
  const [pendingClear, setPendingClear] = useState<string | null>(null)

  const scoped = view.scoped
  /*
    「这份清单现在算不算数」。**总说明必须按它分叉**：同一份清单在 shadow 下
    一个字节都不生效（清单外的请求照旧放行、照旧按全局清单计价），在 enforce 下
    才真的替换全局清单。而这一屏建出来的清单一律是 shadow —— 无条件按 enforce
    的口径写那一句，运营读完会认为收紧已经落地然后走人，实际上什么都没拦住。
  */
  const enforced = props.userGroup.mode === 'enforce'
  /** 这一档已经有清单、但清单里一项都不剩（含未保存的草稿）。 */
  const emptyList = scoped && view.memberCount === 0
  const otherUserGroups = props.data.user_groups
    .map((row) => row.name)
    .filter((name) => name !== props.userGroup.name)

  /**
   * 列表的增删入口 —— **三条支线，全部收在这里**。
   *
   * 直接调 `onToggleGranted` 的那条是常态；另外两条各自对应一次不可能靠草稿
   * 表达的状态迁移（建 scope 行 / 删 scope 行），它们要先问一句再走。散在
   * 各个按钮上的话，日后加一个入口就会漏掉其中一条，而漏掉的表现分别是
   * 「后端 400、界面看不出为什么」与「整档人静默从'受限'变成'全放行'」。
   */
  function requestMembership(modelGroup: string, granted: boolean) {
    if (!scoped) {
      setPendingCreate({ modelGroup, granted })
      return
    }
    if (!granted && view.memberCount <= 1) {
      setPendingClear(modelGroup)
      return
    }
    props.onToggleGranted(modelGroup, granted)
  }

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
            {/*
              徽章的 title 用**这一屏正文里已经写着的那一句**覆盖掉组件自带的
              默认提示。默认那两句是给矩阵页写的，其中「未设定范围」那一句以
              「先给这个用户分组设定范围」收尾 —— 而这一屏里那个控件已经不
              存在（范围行由列表内容隐式表达）。指向一个不存在的控件比不给
              指路更糟：运营会去找，找不到，然后认为页面坏了。
            */}
            <QyGmScopeStateBadges
              userGroup={props.userGroup}
              grantedCount={props.grantedCount}
              unsetHint={t('qy_ugl_fallback_note')}
              emptyHint={t('qy_ugl_empty_scope')}
            />
          </div>
          {/*
            **这一屏关于"清单从哪来"的说明，全站只有这一句。**

            上一版把同一件事说了三遍（顶部两段 + 每一行下面重复一段），而三段
            里有两段说的是同一个内部状态。这里按当前状态三选一（没有清单 /
            有清单但只观察 / 有清单且真的在拦人），其余位置一个字都不再重复。
          */}
          <p className='text-muted-foreground text-xs leading-5'>
            {!scoped && t('qy_ugl_fallback_note')}
            {scoped &&
              (enforced ? t('qy_ugl_own_note') : t('qy_ugl_own_note_shadow'))}
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
                整档复制只对**已有自己清单**的分组开放：没有清单的那一档没有
                权威名单，往它头上写 grant 只会生成一批后端必然拒绝的动作，
                而运营从界面上看不出保存为什么失败。
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

      {/* 生效力度：有自己的清单时才有意义 —— 给一个不存在的清单标
          shadow/enforce 是纯粹的噪声。 */}
      {scoped && (
        <QyUgScopeModeStrip userGroup={props.userGroup} scope={props.scope} />
      )}

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='flex items-center gap-1.5 text-xs font-medium'>
          <Boxes aria-hidden='true' className='size-3.5 shrink-0' />
          {t('qy_ugl_section_title', { count: view.memberCount })}
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          aria-expanded={addOpen}
          onClick={() => setAddOpen((open) => !open)}
        >
          <Plus aria-hidden='true' />
          {t('qy_ugl_add')}
        </Button>
      </div>

      <p className='text-muted-foreground text-xs'>
        {t('qy_group_ratio_inherit_vs_zero_hint')}
      </p>

      {/*
        添加入口。就地展开而不是再开一层浮层：这一屏（弹窗形态）本身就握着
        一份未保存的草稿，叠一层之后 Esc 关掉的是哪一层不确定，按一次可能连
        草稿一起丢。
      */}
      {addOpen && (
        <div className='space-y-1 rounded-md border border-dashed p-2'>
          <p className='text-muted-foreground text-xs'>
            {view.addable.length === 0
              ? t('qy_ugl_add_none')
              : t('qy_ugl_add_title', { count: view.addable.length })}
          </p>
          <ul className='max-h-56 space-y-1 overflow-y-auto'>
            {view.addable.map((column) => (
              <li
                key={column.name}
                className='flex flex-wrap items-center justify-between gap-2 rounded-md p-1'
              >
                <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
                  <span className='min-w-0 truncate text-xs font-medium'>
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
                  {/* 放开一个没有渠道的模型分组 = 放开一个空池子。它在挑的
                      那一刻就必须看得见，选完再说已经晚了。 */}
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
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='h-7'
                  aria-label={t('qy_ugl_add_one_label', {
                    modelGroup: column.name,
                  })}
                  onClick={() => requestMembership(column.name, true)}
                >
                  <Plus aria-hidden='true' />
                  {t('qy_ugl_add_one')}
                </Button>
              </li>
            ))}
          </ul>
        </div>
      )}

      {/*
        「清单空着」这一件事**整屏只说一次**。

        上一版在同一屏上同时出现：页眉徽章的 title、正文里的一整段告警、以及
        这里的空态提示 —— 前两处逐字相同。读的人要先花时间确认这几段是不是在
        说不同的事，而这正是这一轮要消掉的噪声（只是从 13 行缩到了 1 屏）。
        现在只剩这一处，它同时回答「会发生什么」与「怎么回到回落态」；页眉
        徽章的 title 由调用处覆盖成同一句。
      */}
      {emptyList && (
        <p className='text-warning flex items-start gap-1.5 rounded-md border border-dashed p-2 text-xs leading-5'>
          <TriangleAlert
            aria-hidden='true'
            className='mt-0.5 size-3 shrink-0'
          />
          <span>{t('qy_ugl_empty_scope')}</span>
        </p>
      )}
      {!scoped && view.items.length === 0 && (
        <p className='text-muted-foreground rounded-md border border-dashed p-2 text-xs leading-5'>
          {t('qy_ugl_empty_fallback')}
        </p>
      )}

      <ul
        className={cn(
          'min-h-0 space-y-1',
          (props.scrollList ?? true) && 'max-h-[52vh] overflow-y-auto'
        )}
      >
        {view.items.map((item) => {
          const key = qyGmCellKey(props.userGroup.name, item.name)
          const cell = props.serverCells.get(key)
          const entry = props.draft.get(key)
          return (
            <li
              key={item.name}
              className='grid grid-cols-1 items-center gap-1 rounded-md border p-1.5 sm:grid-cols-[minmax(0,1fr)_minmax(9rem,14rem)_auto] sm:gap-2'
            >
              <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
                <span
                  className='min-w-0 truncate text-xs font-medium'
                  title={t('qy_group_matrix_col_label', {
                    modelGroup: item.name,
                  })}
                >
                  {item.name}
                </span>
                <Badge
                  variant='outline'
                  className='text-muted-foreground px-1 py-0 text-[10px] tabular-nums'
                >
                  {t('qy_group_matrix_base_ratio', { ratio: item.baseRatio })}
                </Badge>
                {!item.hasChannels && (
                  <Badge
                    variant='outline'
                    className='border-warning/50 text-warning px-1 py-0 text-[10px]'
                    title={t('qy_group_matrix_col_no_channels')}
                  >
                    {t('qy_group_matrix_col_no_channels_short')}
                  </Badge>
                )}
                {/*
                  「仅经套餐可达」是第三种状态，既不是"在清单里"也不是"用不了"。
                  画成任何一个都是假陈述，而它那一格的倍率此刻正在扣钱。
                */}
                {item.origin === 'plan' && (
                  <Badge
                    variant='outline'
                    className='border-info/50 text-info px-1 py-0 text-[10px]'
                    title={t('qy_ugl_origin_plan_hint', {
                      plans: item.planTitles.join('、'),
                    })}
                  >
                    <Package aria-hidden='true' className='size-2.5' />
                    {t('qy_ugl_origin_plan')}
                  </Badge>
                )}
              </div>

              <QyGmMatrixCell
                userGroup={props.userGroup.name}
                modelGroup={item.name}
                cell={cell}
                entry={entry}
                granted={item.origin !== 'plan'}
                ratio={qyGmRatioDraftOf(cell, entry)}
                baseRatio={item.baseRatio}
                scoped={scoped}
                reachableVia={cell?.reachable_via}
                planTitles={cell?.plan_titles}
                selfEdge={item.selfEdge}
                onRatioChange={(ratio) => props.onRatioChange(item.name, ratio)}
              />

              <QyUgRowAction item={item} onRequest={requestMembership} />

              {/*
                按格备注独占一行、横跨整行。

                **只画在真的写得进去的行上**：后端对按格备注有两道硬闸门（这一档
                得有自己的清单、这一格得在清单里），而列表里只有 `scope` 那一种
                来源同时满足两者。上一版对不满足的行照样画一个禁用的输入框，
                并在每一行下面重复同一段解释 —— 站上绝大多数分组都没有自己的
                清单，于是那段话在一屏里出现十几遍。现在它一次都不出现：写不进
                去的行根本没有这个框，为什么写不进去由上面那一句总说明回答。
              */}
              {props.onNoteChange != null && item.origin === 'scope' && (
                <QyUgCellNote
                  modelGroup={item.name}
                  cell={cell}
                  value={qyGmNoteDraftOf(cell, entry)}
                  enforced={props.userGroup.scope_enforced}
                  onChange={(note) => props.onNoteChange?.(item.name, note)}
                />
              )}
            </li>
          )
        })}
      </ul>

      {/*
        第一次增删 = 为这一档建立独立清单。

        它立刻写一次服务端（另一个端点、另一份数据），所以要问一句 —— 但问的
        是**这次动作的后果**，不是"请先打开一个开关"。
      */}
      <QyConfirmDialog
        open={pendingCreate != null}
        onOpenChange={(open) => {
          if (!open) setPendingCreate(null)
        }}
        title={t('qy_ugl_create_title', { userGroup: props.userGroup.name })}
        description={t('qy_ugl_create_desc')}
        details={
          <div className='space-y-2 text-xs leading-5'>
            <p>
              {t('qy_ugl_create_carries', {
                count: props.userGroup.model_groups.length,
              })}
            </p>
            <p>{t('qy_ugl_create_shadow')}</p>
            {props.scope.hasUnsavedDraft && (
              <p className='text-destructive'>
                {t('qy_ugl_create_discards_draft')}
              </p>
            )}
          </div>
        }
        confirmText={t('qy_ugl_create_confirm')}
        isLoading={props.scope.isSaving}
        confirmDisabled={props.scope.hasUnsavedDraft}
        onConfirm={() => {
          const target = pendingCreate
          if (target == null) return
          props.scope.onSubmit(
            {
              managed: true,
              // 新清单一律先建成影子：这一次点击本身不该让任何人当场 403。
              allow_auto: props.userGroup.allow_auto,
              note: props.userGroup.scope_note,
            },
            // 服务端状态回读之后才落草稿 —— 回读会清空本地草稿，反过来做
            // 等于这次增删被静默丢掉，而屏幕上只有一句绿色的「已保存」。
            () => props.onToggleGranted(target.modelGroup, target.granted)
          )
          setPendingCreate(null)
        }}
      />

      <QyUgClearListDialog
        open={pendingClear != null}
        onOpenChange={(open) => {
          if (!open) setPendingClear(null)
        }}
        userGroup={props.userGroup}
        modelGroup={pendingClear ?? ''}
        isSaving={props.scope.isSaving}
        /*
          「回落全局清单」那一支与「建立独立清单」走的是同一次范围写入，因此
          也会触发同一次服务端状态回读 —— 手上没保存的格子改动全部丢掉。而
          到达这个弹窗时它几乎必然为真：把清单删到只剩一项这个动作本身就产生
          了若干条未保存的撤销草稿。不设闸门的话，屏幕上只会出现一句绿色的
          「范围已保存」，而刚改的那个倍率静默没了。
        */
        hasUnsavedDraft={props.scope.hasUnsavedDraft}
        onFallback={() => {
          props.scope.onSubmit({
            managed: false,
            allow_auto: props.userGroup.allow_auto,
            note: props.userGroup.scope_note,
          })
          setPendingClear(null)
        }}
        onIsolate={() => {
          if (pendingClear != null) props.onToggleGranted(pendingClear, false)
          setPendingClear(null)
        }}
      />
    </div>
  )
}

/**
 * 行尾的那一个动作键。
 *
 * 三种来源对应两种动作，而**它们的措辞不能共用**：
 *
 *   · `scope`    这一档自己的清单里有它 → 「移除」
 *   · `fallback` 全局清单给的 → 同样是「移除」，但那一次点击会先为这一档
 *                建立独立清单（由调用方的确认弹窗说明）
 *   · `plan`     清单里没有它、套餐让它可达 → **不能移除**（移无可移），
 *                能做的是把它正式加进清单
 */
function QyUgRowAction(props: {
  item: QyUgListItem
  onRequest: (modelGroup: string, granted: boolean) => void
}) {
  const { t } = useTranslation()
  const { item } = props

  if (item.origin === 'plan') {
    return (
      <Button
        type='button'
        variant='ghost'
        size='sm'
        className='h-7 justify-self-end'
        aria-label={t('qy_ugl_add_one_label', { modelGroup: item.name })}
        onClick={() => props.onRequest(item.name, true)}
      >
        <Plus aria-hidden='true' />
        {t('qy_ugl_add_one')}
      </Button>
    )
  }

  return (
    <Button
      type='button'
      variant='ghost'
      size='sm'
      className='text-muted-foreground hover:text-destructive h-7 justify-self-end'
      aria-label={t('qy_ugl_remove_label', { modelGroup: item.name })}
      // 同名的那一行有一条反直觉的事实：删掉它并不会让分组为空的令牌发不出
      // 请求（那类令牌恒等于属主的用户分组，整段跳过可选性检查）。
      title={item.selfEdge ? t('qy_group_matrix_self_edge_hint') : undefined}
      onClick={() => props.onRequest(item.name, false)}
    >
      <X aria-hidden='true' />
      {t('qy_ugl_remove')}
    </Button>
  )
}

/**
 * 「移除最后一项」的二选一。
 *
 * ── 为什么不能替运营选 ──
 *
 * 一份空清单与"没有清单"在数据上差一行，在行为上是**相反**的两件事：前者
 * （强制档）让这一档人显式指定分组的令牌全部 403，后者让这一档回到全局
 * 「用户可选分组」—— 通常意味着能用的**变多**。两个错误方向分别是整档停摆
 * 和整档全放行，而且都静默。所以这里不设默认动作，两个出口各自说清后果。
 */
function QyUgClearListDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  userGroup: QyGmUserGroup
  modelGroup: string
  isSaving: boolean
  /**
   * 矩阵上还有没保存的格子。
   *
   * **只闸住「回落全局清单」那一支**：它是一次范围写入（`managed:false`），
   * 成功之后强制回读服务端状态、清空本地草稿。另一支「留一份空清单」只落草稿，
   * 一个字节都不写服务端，因此不受影响 —— 两支一起闸掉会让运营在有草稿时
   * 连隔离都做不了，而那一支本来是安全的。
   */
  hasUnsavedDraft: boolean
  onFallback: () => void
  onIsolate: () => void
}) {
  const { t } = useTranslation()

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_ugl_clear_title', { modelGroup: props.modelGroup })}
      description={t('qy_ugl_clear_desc', { userGroup: props.userGroup.name })}
    >
      <div className='space-y-3'>
        <div className='space-y-2 rounded-md border p-3'>
          <p className='text-sm font-medium'>{t('qy_ugl_clear_fallback')}</p>
          <p className='text-muted-foreground text-xs leading-5'>
            {t('qy_ugl_clear_fallback_desc')}
          </p>
          {props.hasUnsavedDraft && (
            <p className='text-destructive text-xs leading-5'>
              {t('qy_ugl_scope_write_discards_draft')}
            </p>
          )}
          <Button
            type='button'
            size='sm'
            variant='outline'
            disabled={props.isSaving || props.hasUnsavedDraft}
            onClick={props.onFallback}
          >
            {t('qy_ugl_clear_fallback_action')}
          </Button>
        </div>

        <div className='border-destructive/40 space-y-2 rounded-md border p-3'>
          <p className='text-destructive text-sm font-medium'>
            {t('qy_ugl_clear_isolate')}
          </p>
          <p className='text-muted-foreground text-xs leading-5'>
            {t('qy_ugl_clear_isolate_desc')}
          </p>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={props.onIsolate}
          >
            {t('qy_ugl_clear_isolate_action')}
          </Button>
        </div>

        <div className='flex justify-end'>
          <Button
            type='button'
            variant='ghost'
            size='sm'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
        </div>
      </div>
    </QyResponsiveDialog>
  )
}

/**
 * 一项的「按格备注」输入框。
 *
 * 只挂在真的写得进去的行上（见调用处），所以这里**没有禁用分支** ——
 * 上一版那两段"为什么不能写"的解释连同禁用态一起删掉了。
 *
 * ── 「写进去了但用户看不到」这一档 ──
 *
 * `mode=shadow` 时清单一个字节都不生效，按格备注同样不生效（读侧逐位返回
 * 上游）。这时备注**写得进库**，所以不拦；但后端回读的 `note_pending` 会为真，
 * 这里必须挂一句说明用户此刻实际看到的是哪一段，否则它与"已生效"长得一模一样。
 * 判据用后端下发的字段，不在前端拿 `note` 与 `note_source` 自己比：那条比法
 * 是一条隐式规则，漏在任何一个外壳上，那个外壳就会把影子期的备注画成已生效。
 */
function QyUgCellNote(props: {
  modelGroup: string
  cell: QyGmCell | undefined
  value: string
  enforced: boolean
  onChange: (note: string) => void
}) {
  const { t } = useTranslation()
  const { cell } = props

  return (
    <div className='space-y-1 sm:col-span-3'>
      <Input
        value={props.value}
        className='h-7 text-xs'
        aria-label={t('qy_ug_cell_note_label', {
          modelGroup: props.modelGroup,
        })}
        // placeholder 里放的是**用户此刻真的会看到的那一句**（后端解析完四级
        // 阶梯之后的结果），所以它同时是提示与预览。预填成真实值的话，运营
        // 随手保存一遍就把默认备注固化成一份复制品，此后在模型分组页改默认
        // 备注，这一格再也不跟着变，而没有任何人做过这个决定。
        placeholder={
          cell == null || cell.note_source === 'grant'
            ? t('qy_ug_cell_note_placeholder')
            : t('qy_ug_cell_note_inherits', { note: cell.effective_note })
        }
        onChange={(event) => props.onChange(event.target.value)}
      />
      {cell?.note_pending === true && (
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
