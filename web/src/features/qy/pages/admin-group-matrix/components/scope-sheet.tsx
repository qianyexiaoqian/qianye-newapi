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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import type {
  QyGmMode,
  QyGmPreviewResponse,
  QyGmScopeRequest,
  QyGmUserGroup,
} from '../types'

type QyGmScopeSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  userGroup: QyGmUserGroup | null
  /** 该用户分组当前草稿里可选的模型分组数，用于把「空清单」说清楚。 */
  grantedCount: number
  /** 矩阵上还有没保存的格子。切 enforce 之前必须先保存（见下）。 */
  hasUnsavedDraft: boolean
  /**
   * 针对**这一个用户分组**的影响面预览结果，没预览过时为 `null`。
   *
   * 只有它非空且 `preview_incomplete` 为 false 时才允许切 `enforce` ——
   * 切 enforce 是这一页唯一会让一批用户当场 403 的动作，没看过影响面就不许按。
   * `shadow` 与取消接管不需要（两者都不打断任何流量）。
   *
   * **必须是单分组预览**，不能复用矩阵页那份通用预览：服务端在切换时用
   * `previewDigest(userGroup)` 只重算这一个分组，两侧取值范围不同则
   * `impact_hash` 永不相等，enforce 会被 409 永久锁死。
   */
  enforcePreview: QyGmPreviewResponse | null
  isPreviewing: boolean
  onPreviewForEnforce: () => void
  isSaving: boolean
  onSubmit: (body: QyGmScopeRequest) => void
}

/**
 * 单个用户分组的接管设置。
 *
 * ── 三个状态，不是两个 ──
 *   · 未接管（`managed:false`）  = 扩展库里没有这一行 = 完全走上游原行为。
 *     这是零行为变更的默认态，也是 L3 回退（删掉行即可）。
 *   · 接管 + shadow              = 清单已经是权威的，但只记录不阻断。
 *   · 接管 + enforce             = 不在清单里的模型分组，令牌选不了、请求 403。
 *
 * 「接管了但清单是空的」是**合法且危险**的配置（隔离组 / 封禁组），必须能表达，
 * 所以它不能靠「有没有 grant 行」来推断 —— 那会让「一条都不许用」与「没配过」
 * 再次不可区分，而这一次的后果是整组用户 403。
 */
export function QyGmScopeSheet(props: QyGmScopeSheetProps) {
  const { t } = useTranslation()
  const [managed, setManaged] = useState(false)
  const [mode, setMode] = useState<QyGmMode>('shadow')
  const [allowAuto, setAllowAuto] = useState(true)
  const [note, setNote] = useState('')

  const userGroup = props.userGroup

  // 每次打开都从服务端状态重置：残留上一次编辑的另一个分组的值，会让运营
  // 在完全不知情的情况下把 A 组的设置按到 B 组头上。
  useEffect(() => {
    if (!props.open || userGroup == null) return
    setManaged(userGroup.managed)
    setMode(userGroup.mode)
    setAllowAuto(userGroup.allow_auto)
    setNote(userGroup.note ?? '')
  }, [props.open, userGroup])

  if (userGroup == null) return null

  const enforceUnlocked =
    props.enforcePreview != null &&
    !props.enforcePreview.preview_incomplete &&
    !props.hasUnsavedDraft
  const enforceBlocked = managed && mode === 'enforce' && !enforceUnlocked
  const emptyList = managed && props.grantedCount === 0

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_group_matrix_managed_edit_title', {
        userGroup: userGroup.name,
      })}
      description={t('qy_group_matrix_managed_hint')}
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={props.isSaving || enforceBlocked}
            onClick={() =>
              props.onSubmit({
                managed,
                mode,
                allow_auto: allowAuto,
                note: note.trim(),
              })
            }
          >
            {t('qy_common_submit')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='flex items-start justify-between gap-4'>
          <div className='space-y-0.5'>
            <Label>{t('qy_group_matrix_managed_on')}</Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_group_matrix_managed_desc')}
            </p>
          </div>
          <Switch checked={managed} onCheckedChange={setManaged} />
        </div>

        {managed && (
          <>
            <div className='space-y-2'>
              <Label>{t('qy_group_matrix_mode_label')}</Label>
              <div className='grid gap-2'>
                <QyGmModeOption
                  active={mode === 'shadow'}
                  title={t('qy_group_matrix_mode_shadow')}
                  desc={t('qy_group_matrix_mode_shadow_desc')}
                  onSelect={() => setMode('shadow')}
                />
                <QyGmModeOption
                  active={mode === 'enforce'}
                  title={t('qy_group_matrix_mode_enforce')}
                  desc={t('qy_group_matrix_mode_enforce_desc')}
                  onSelect={() => setMode('enforce')}
                />
              </div>
              {mode === 'enforce' && (
                <div className='space-y-2'>
                  {enforceBlocked && (
                    <p className='text-destructive text-xs'>
                      {props.hasUnsavedDraft
                        ? t('qy_group_matrix_enforce_needs_saved_draft')
                        : t('qy_group_matrix_mode_switch_confirm')}
                    </p>
                  )}
                  {/* 这一份预览**只评估这个用户分组**，与矩阵页顶部那份通用预览
                      刻意分开：服务端切换时也只重算这一个分组，取值范围不同的
                      两个 impact_hash 永远不相等，enforce 会被 409 锁死。 */}
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    disabled={props.isPreviewing || props.hasUnsavedDraft}
                    onClick={props.onPreviewForEnforce}
                  >
                    {t('qy_group_matrix_enforce_preview_btn', {
                      userGroup: userGroup.name,
                    })}
                  </Button>
                  {props.enforcePreview != null && (
                    <p className='text-muted-foreground text-xs tabular-nums'>
                      {t('qy_group_matrix_enforce_preview_summary', {
                        broken: props.enforcePreview.newly_broken.length,
                        tokens:
                          props.enforcePreview.total_newly_broken_tokens ?? 0,
                      })}
                    </p>
                  )}
                </div>
              )}
            </div>

            <div className='flex items-start justify-between gap-4'>
              <div className='space-y-0.5'>
                <Label>{t('qy_group_matrix_allow_auto')}</Label>
                <p className='text-muted-foreground text-xs'>
                  {t('qy_group_matrix_allow_auto_desc')}
                </p>
              </div>
              <Switch checked={allowAuto} onCheckedChange={setAllowAuto} />
            </div>

            {/* 空清单是合法配置（隔离组），所以这里是警告不是拦截。
                但它必须被说出来：接管一个分组却一格都没勾，enforce 之后
                这一组人一个模型分组都选不了。 */}
            {emptyList && (
              <p className='text-warning rounded-md border border-dashed p-2 text-xs'>
                {t('qy_group_matrix_empty_list_warning', {
                  userGroup: userGroup.name,
                })}
              </p>
            )}
          </>
        )}

        <div className='space-y-2'>
          <Label htmlFor='qy-gm-scope-note'>{t('qy_common_remark')}</Label>
          <Input
            id='qy-gm-scope-note'
            value={note}
            maxLength={255}
            onChange={(event) => setNote(event.target.value)}
            placeholder={t('qy_group_matrix_note_placeholder')}
          />
        </div>
      </div>
    </QyResponsiveDialog>
  )
}

function QyGmModeOption(props: {
  active: boolean
  title: string
  desc: string
  onSelect: () => void
}) {
  return (
    <button
      type='button'
      onClick={props.onSelect}
      aria-pressed={props.active}
      className={
        props.active
          ? 'border-primary bg-primary/5 rounded-lg border p-3 text-start'
          : 'hover:bg-muted/50 rounded-lg border p-3 text-start'
      }
    >
      <span className='block text-sm font-medium'>{props.title}</span>
      <span className='text-muted-foreground block text-xs'>{props.desc}</span>
    </button>
  )
}
