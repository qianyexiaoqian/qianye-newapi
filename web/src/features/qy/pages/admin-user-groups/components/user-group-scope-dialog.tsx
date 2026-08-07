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
import { Eye, EyeOff } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { QyGmDiffBar } from '../../admin-group-matrix/components/diff-bar'
import { QyGmPreviewBody } from '../../admin-group-matrix/components/preview-dialog'
import { QyGmScopeForm } from '../../admin-group-matrix/components/scope-sheet'
import { QyGmStatusBanners } from '../../admin-group-matrix/components/status-banners'
import { useQyGmEditor } from '../../admin-group-matrix/lib/use-editor'
import { QyUgGroupDetail } from './user-group-detail'

/**
 * 「用户分组」表行内的**配置可用模型分组**弹窗（需求 4）。
 *
 * 项目方原话：「用户分组页面，配置可用模型分组，这里直接弹窗选择配置，不要再
 * 跳转到用户分组可用的模型分组配置。」
 *
 * ── 弹窗里必须装下整条链路，不能只装勾选框 ──
 *
 * 勾掉一个模型分组会让一批正在跑的令牌当场 403，改一格倍率会在保存成功那一秒
 * 起按新价扣钱。所以这个弹窗里装的是与整页视图**逐字相同**的那条链路：
 * 状态横幅 → 差异条（含"没预览过不许保存"的闸门）→ 范围设置 → 逐个模型分组的
 * 可选性与倍率 → 影响面报告。少任何一段，弹窗就成了一条绕开闸门的近路，
 * 而绕开闸门的那次点击恰好是最危险的一次。
 *
 * 实现上共用 {@link useQyGmEditor} 这一份状态机 —— 两个外壳的闸门不可能松紧不一。
 *
 * ── 关掉即卸载，草稿不跨次留存 ──
 * 正文只在打开时挂载（Base UI 的 Portal 默认不 keepMounted），于是关掉弹窗
 * 就是丢弃草稿。这是刻意的：残留一份看不见的草稿，下一次打开另一个分组时
 * 会把上一次的改动混进影响面与保存动作里，而运营完全不知道自己在提交什么。
 */
export function QyUgScopeDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 要配置的用户分组名。`null` = 没有选中任何一档（弹窗此时也不该是开的）。 */
  userGroup: string | null
}) {
  const { t } = useTranslation()

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_group_scope_dialog_title', {
        userGroup: props.userGroup ?? '',
      })}
      description={t('qy_group_scope_dialog_desc')}
      contentClassName='sm:max-w-4xl'
    >
      {props.userGroup == null ? null : (
        <QyUgScopeDialogBody userGroup={props.userGroup} />
      )}
    </QyResponsiveDialog>
  )
}

function QyUgScopeDialogBody(props: { userGroup: string }) {
  const { t } = useTranslation()
  const editor = useQyGmEditor()
  const data = editor.data
  const row = editor.userGroups.find((item) => item.name === props.userGroup)

  /**
   * 影响面报告**内联展开**，不叠第二层浮层。
   *
   * 报告是保存前的必读材料，而握着草稿的是这个弹窗本身：再叠一层浮层之后，
   * Esc 键关掉的是哪一层不确定，运营按一次可能连草稿一起丢。
   */
  const [reportOpen, setReportOpen] = useState(false)
  const showReport = reportOpen || editor.isPreviewing

  return (
    <QyPageBoundary
      query={editor.query}
      isEmpty={data != null && row == null}
      emptyTitle={t('qy_group_scope_dialog_gone')}
      emptyDescription={t('qy_group_scope_dialog_gone_desc')}
    >
      {data != null && row != null && (
        <div className='space-y-3'>
          <QyGmStatusBanners
            snapshot={data.snapshot}
            partial={editor.partial}
            ratioDrift={editor.ratioDrift}
            selfExcluded={editor.selfExcluded}
            caseNearMiss={editor.caseNearMiss}
            warnings={data.warnings}
            shadowWriteDenies={data.shadow_write_denies}
            emptyScopeGroups={editor.emptyScopeGroups}
            onReload={editor.reload}
          />

          {/* 差异条与整页视图上是同一个组件、同一道闸门。它 `sticky top-0`，
              在弹窗的滚动正文里同样贴顶 —— 保存键不会随着模型分组清单滚走。 */}
          <QyGmDiffBar
            counts={editor.counts}
            invalidCount={editor.invalidCells.length}
            hasDraft={editor.draft.size > 0}
            needsPreview={editor.needsPreview}
            isPreviewing={editor.isPreviewing}
            isSaving={editor.isSaving}
            onPreview={() => {
              setReportOpen(true)
              editor.runPreview()
            }}
            onSave={editor.runSave}
            onReset={editor.resetDraft}
          />

          {showReport && (
            <section className='space-y-2 rounded-lg border p-3'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <h3 className='text-sm font-medium'>
                  {t('qy_group_matrix_preview_title')}
                </h3>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  onClick={() => setReportOpen(false)}
                >
                  <EyeOff aria-hidden='true' />
                  {t('qy_group_scope_dialog_hide_report')}
                </Button>
              </div>
              <QyGmPreviewBody
                preview={editor.preview}
                isLoading={editor.isPreviewing}
              />
            </section>
          )}
          {!showReport && editor.preview != null && (
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => setReportOpen(true)}
            >
              <Eye aria-hidden='true' />
              {t('qy_group_scope_dialog_show_report')}
            </Button>
          )}

          {/*
            范围设置自成一段，且有**自己的**提交键。

            它写的是另一个端点、另一份数据（`qy_group_scopes`），与上面差异条
            保存的可选性 / 倍率不是一回事。合成一个按钮的话，一次点击会同时改掉
            访问控制的力度与一批价格，而两者的失败方式完全不同 ——
            这套两库设计最坏的失败就是"以为一起成功了"。
          */}
          <section className='space-y-3 rounded-lg border p-3'>
            <h3 className='text-sm font-medium'>{t('qy_group_scope_edit')}</h3>
            <QyGmScopeForm
              open
              userGroup={row}
              grantedCount={editor.grantedCounts.get(row.name) ?? 0}
              hasUnsavedDraft={editor.counts.total > 0}
              enforcePreview={
                editor.enforcePreview?.userGroup === row.name
                  ? editor.enforcePreview.result
                  : null
              }
              isPreviewing={editor.isEnforcePreviewing}
              onPreviewForEnforce={() => editor.runEnforcePreview(row.name)}
              isSaving={editor.isScopeSaving}
              onSubmit={(body) => editor.submitScope(row.name, body)}
            />
          </section>

          <QyUgGroupDetail
            data={data}
            userGroup={row}
            serverCells={editor.serverCells}
            draft={editor.draft}
            grantedCount={editor.grantedCounts.get(row.name) ?? 0}
            onToggleGranted={(modelGroup, granted) =>
              editor.toggleGranted(row.name, modelGroup, granted)
            }
            onRatioChange={(modelGroup, ratio) =>
              editor.changeRatio(row.name, modelGroup, ratio)
            }
            onCopyFrom={(fromUserGroup) =>
              editor.copyRow(fromUserGroup, row.name)
            }
            scrollList={false}
          />
        </div>
      )}
    </QyPageBoundary>
  )
}
