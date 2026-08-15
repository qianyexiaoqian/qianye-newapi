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
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'

/**
 * 提交范围行的那一条通道 —— 两个外壳都从 `useQyGmEditor.submitScope` 走，
 * 所以「这次写入会清空草稿」那道闸门只有一份实现。
 *
 * `afterApply` 在服务端状态回读**之后**执行（回读会清空本地草稿），
 * 用于"先建清单、再把这一次的增删写进草稿"这条两步动作。
 */
export type QyUgScopeSubmit = {
  isSaving: boolean
  /**
   * 矩阵上还有没保存的格子。
   *
   * 范围写入是**另一个端点**，成功之后会强制回读服务端状态、清空本地草稿。
   * 所以每一个范围写入入口都要先看它，否则运营刚改的倍率静默消失，屏幕上
   * 只剩一句绿色的「范围已保存」。
   */
  hasUnsavedDraft: boolean
  onSubmit: (body: QyGmScopeRequest, afterApply?: () => void) => void
}

/**
 * 「这份清单现在算不算数」—— 状态条。
 *
 * ── 为什么它在列表**旁边**，而不是列表**前面** ──
 *
 * 上一版把"有没有 scope 行"和"以什么力度生效"合成一个前置表单：不先在那里
 * 把开关打开，下面整张清单的勾选框全是灰的。运营初见只会读成「什么都点不动」，
 * 而屏幕上唯一的解释是一段要求他先理解内部数据模型的话。现在"有没有清单"由
 * 列表内容隐式表达（第一次增删时建、清空时删）。
 *
 * ── 只剩一档 ──
 *
 * 曾经这里还有第二个运营决定：这份清单是先只观察（shadow），还是现在就拦人
 * （enforce）。影子档已整体下线 —— 有范围行 = 清单立即生效，没有第二档可切。
 *
 * 它仍然必须存在，因为「这一档现在会拦人」这件事必须一眼可见：运营配完一份
 * 会让一批令牌 403 的清单，屏幕上不能一个字都没提醒过他。
 */
export function QyUgScopeModeStrip(props: {
  userGroup: QyGmUserGroup
  scope: QyUgScopeSubmit
}) {
  const { t } = useTranslation()
  const row = props.userGroup
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
      </div>

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
            保存这两项也是一次范围写入 —— 它同样回读服务端状态、清空整张矩阵的
            未保存草稿。它不是止血动作（没有任何人因为它继续 403），所以这里与
            建清单、回落全局取同一个口径：先保存或撤销格子改动，再回来。
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
              disabled={props.scope.isSaving || props.scope.hasUnsavedDraft}
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
