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
import { ChevronDown, ShieldCheck } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'

import type {
  QyGmPreviewResponse,
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

/**
 * 提交范围行的那一条通道 —— 两个外壳都从 `useQyGmEditor.submitScope` 走，
 * 所以切 enforce 的双哈希闸门只有一份实现。
 *
 * `afterApply` 在服务端状态回读**之后**执行（回读会清空本地草稿），
 * 用于"先建清单、再把这一次的增删写进草稿"这条两步动作。
 */
export type QyUgScopeSubmit = {
  isSaving: boolean
  /** 矩阵上还有没保存的格子。切 enforce 之前必须先保存（后端按哈希比对）。 */
  hasUnsavedDraft: boolean
  /**
   * 针对**这一个**用户分组的影响面预览结果；没预览过时为 `null`。
   *
   * 必须是单分组预览：服务端切换时用 `previewDigest(userGroup)` 只重算这一个
   * 分组，复用整页那份通用预览会让两侧的 `impact_hash` 永不相等，enforce 被
   * 409 永久锁死。
   */
  enforcePreview: QyGmPreviewResponse | null
  isPreviewing: boolean
  onPreviewForEnforce: () => void
  onSubmit: (body: QyGmScopeRequest, afterApply?: () => void) => void
}

/**
 * 「这份清单现在算不算数」—— shadow / enforce 的状态与切换。
 *
 * ── 为什么它在列表**旁边**，而不是列表**前面** ──
 *
 * 上一版把"有没有 scope 行"和"以什么力度生效"合成一个前置表单：不先在那里
 * 把开关打开，下面整张清单的勾选框全是灰的。运营初见只会读成「什么都点不动」，
 * 而屏幕上唯一的解释是一段要求他先理解内部数据模型的话。现在"有没有清单"由
 * 列表内容隐式表达（第一次增删时建、清空时删），这里只剩下一个**真正的运营
 * 决定**：配好的这份清单，是先只观察，还是现在就拦人。
 *
 * 它仍然必须存在。灰度能力（先配好、观察一段、确认无误再真拦人）是资金与权限
 * 变更唯一的安全网，而"这一档现在到底拦不拦人"必须一眼可见 —— 那两种状态下
 * 同一份清单的行为完全相反。
 *
 * ── 切 enforce 保留 409 闸门 ──
 *
 * 切过去是这一页唯一会让一批用户当场 403 的动作，所以它是这里唯一需要展开、
 * 需要先看影响面的分支。切回 shadow 不打断任何流量，一次点击即可。
 */
export function QyUgScopeModeStrip(props: {
  userGroup: QyGmUserGroup
  scope: QyUgScopeSubmit
}) {
  const { t } = useTranslation()
  const row = props.userGroup
  // enforced / switching / enforceUnlocked 三个状态随 shadow 一并删除:
  // 有范围行就一定生效,没有第二档可切,也就没有需要解锁的确认动作。
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [allowAuto, setAllowAuto] = useState(row.allow_auto)
  const [note, setNote] = useState(row.scope_note)

  /*
    换了一档人才重置这两个字段。

    触发条件是**分组名**，读的值走 ref。两者要的东西不同：重置要"最新的值"，
    触发要"换了一个人"。依赖里放行对象的话，外壳每次矩阵保存成功都会
    `setQueryData` 写入一个全新的响应对象 —— 按一次保存就把运营正在填的范围
    备注静默清回旧值，而屏幕上只有一句绿色的「已保存」。这条坑在范围抽屉上
    已经踩过一次（见 `scope-sheet.tsx` 的同名注释）。
  */
  const latestRow = useRef(row)
  latestRow.current = row
  const rowName = row.name
  useEffect(() => {
    setAllowAuto(latestRow.current.allow_auto)
    setNote(latestRow.current.scope_note)
  }, [rowName])

  return (
    <div className='space-y-2 rounded-md border p-2'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='flex min-w-0 flex-wrap items-center gap-1.5'>
          <Badge
            variant='outline'
            className='border-destructive/50 text-destructive px-1.5 py-0 text-[11px]'
          >
            <ShieldCheck aria-hidden='true' className='size-3' />
            {t('qy_ugl_mode_enforce_chip')}
          </Badge>
          <span className='text-muted-foreground text-xs'>
            {t('qy_ugl_mode_enforce_desc')}
          </span>
        </div>

        {/*
          ── 这里曾经是「切回影子 / 切到强制」两个按钮 ──

          shadow 模式已整体下线:有 scope 行 = 清单立即生效,没有第二档,
          因此也就没有可切的东西。留着一个点了不会有任何变化的按钮,
          正是这一轮在消灭的形状(后端已经不再读请求体里的 mode 字段)。

          清单本身的增删仍然由下面那张表直接完成,保存即生效。
        */}
      </div>

      {/*
        这里曾经是两块随 shadow 一并作废的 UI:

          1. 「切回『先观察』会把未保存的格子改动丢掉」那条红字警告 ——
             已经没有可切回去的东西了;而且它把 `**不设闸门**` 这种 markdown
             原样印在屏幕上(这段文案从来没有经过 markdown 渲染)。
          2. 「切到强制」的确认面板(说明 → 跑影响面 → 解锁确认)——
             `enforced` 现在恒为真,它永远不会渲染,留着只会让人以为闸门还在。

        影响面预览本身没有删:它仍然是一个可以主动调的只读端点,
        只是不再是保存的前置条件。
      */}

      {/*
        `auto` 伪分组与范围备注收在一个折叠区里。

        它们是范围行上的两个字段，但都不是运营配这一档时的主线动作（主线是
        "这一档能用哪些"）。摊在列表前面就又变成一段必须先读完才敢往下走的
        前置表单 —— 那正是这一轮要消掉的形状。折叠而不是删掉：`allow_auto`
        决定「自动选池」还不还给用户，删掉它这一页就再也表达不了。
      */}
      <Button
        type='button'
        variant='ghost'
        size='sm'
        className='h-7 px-1 text-xs'
        aria-expanded={advancedOpen}
        onClick={() => setAdvancedOpen((open) => !open)}
      >
        <ChevronDown
          aria-hidden='true'
          className={cn(
            'size-3.5 transition-transform',
            advancedOpen && 'rotate-180'
          )}
        />
        {t('qy_ugl_advanced')}
      </Button>

      {advancedOpen && (
        <div className='space-y-3 rounded-md border border-dashed p-2'>
          <div className='flex items-start justify-between gap-4'>
            <div className='space-y-0.5'>
              <Label>{t('qy_group_matrix_allow_auto')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t('qy_group_matrix_allow_auto_desc')}
              </p>
            </div>
            <Switch checked={allowAuto} onCheckedChange={setAllowAuto} />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='qy-ugl-scope-note'>{t('qy_common_remark')}</Label>
            <Input
              id='qy-ugl-scope-note'
              value={note}
              maxLength={255}
              placeholder={t('qy_group_matrix_note_placeholder')}
              onChange={(event) => setNote(event.target.value)}
            />
          </div>
          {/*
            这里曾经有一句「保存这两项也要先跑预览」的提示与对应的 disabled。

            后端那道「切 enforce 前必须回传双哈希」的闸门已随 shadow 一并拆除,
            于是它变成了一道只存在于前端的锁:`enforced` 现在恒为真,
            运营改个备注或开关都会发现保存按钮是灰的,而屏幕上要求他先去跑一次
            与这次改动毫无关系的影响面预览。
          */}
          {/*
            影子档保存这两项此前一道闸门都没有（那一整条与号在 `enforced`
            为假时短路），而它同样会回读并清空草稿。这两项都不是止血动作，
            所以这里与建清单、回落全局取同一个口径：先保存或撤销格子改动。
          */}
          {props.scope.hasUnsavedDraft && (
            <p className='text-destructive text-xs leading-5'>
              {t('qy_ugl_scope_write_discards_draft')}
            </p>
          )}
          <div className='flex justify-end'>
            <Button
              type='button'
              size='sm'
              disabled={
                props.scope.isSaving || props.scope.hasUnsavedDraft
              }
              onClick={() =>
                props.scope.onSubmit({
                  managed: true,
                  allow_auto: allowAuto,
                  note,
                })
              }
            >
              {t('qy_group_scope_apply')}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
