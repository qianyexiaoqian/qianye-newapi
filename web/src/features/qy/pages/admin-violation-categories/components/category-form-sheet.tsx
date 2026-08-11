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
import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  qyPreviewViolationCategoryImpact,
  qySaveViolationCategory,
} from '../api'
import {
  qyCategoryFormToPayload,
  qyCategoryTightens,
  qyCategoryToForm,
  qyEmptyCategoryForm,
  qyValidateCategoryForm,
  type QyCategoryFormValues,
} from '../lib/category-form'
import type { QyViolationCategory } from '../types'

const FORM_ID = 'qy-violation-category-form'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示新建。 */
  category: QyViolationCategory | null
  onSaved: () => void
}

/**
 * 违规类型新建 / 编辑抽屉。
 *
 * 这个表单的难点不是字段多，而是让运营看懂两条界线：
 *
 *  1. **内部说明与公示文案是两栏，不能互抄。** 内部说明写的是判据
 *     （"命中 DAN / 开发者模式 三组词表"），公示出去等于把绕过清单印给用户。
 *     所以两组之间有一条实体分隔与一句警告，而不是把五个输入框平铺下来。
 *  2. **阈值只出"线"，不出"处置动作"。** 越线之后是限制还是封号，由用户所在
 *     分组的处置策略档决定。表单上不给动作下拉，并用一句说明指向那一页 ——
 *     否则运营会在这里找"封号"选项，找不到就以为这个阈值没用。
 *
 * 保存前的二次确认走**预览优先**（与分组策略档那一页同口径）：会扩大处置面时先
 * 拉一次影响面，把"已经有多少存量账号越过这条线"摆给管理员看，再带 `confirm`
 * 提交。不这么做的话，那个数字只会在保存失败的 409 里出现 —— 于是管理员会先按
 * 一次保存去探路，而那正是二次确认要防的动作。
 */
export function QyViolationCategoryFormSheet(props: Props) {
  const { t } = useTranslation()
  const [values, setValues] = useState<QyCategoryFormValues>(
    qyEmptyCategoryForm()
  )
  const [error, setError] = useState<string | null>(null)
  const [pendingCount, setPendingCount] = useState<number | null>(null)

  useEffect(() => {
    if (!props.open) return
    setError(null)
    setPendingCount(null)
    setValues(
      props.category == null
        ? qyEmptyCategoryForm()
        : qyCategoryToForm(props.category)
    )
  }, [props.open, props.category])

  const saveMutation = useMutation({
    mutationFn: (confirm: boolean) =>
      qySaveViolationCategory(qyCategoryFormToPayload(values, confirm)),
    onSuccess: () => {
      toast.success(t('qy_vcat_saved'))
      setPendingCount(null)
      props.onSaved()
      props.onOpenChange(false)
    },
    onError: (err: unknown) => toast.error(qyOpsErrorMessage(err, t)),
  })

  // 预览失败**不能**挡住保存：事故当天最需要能执行的动作恰恰是放宽阈值，
  // 而"算不出影响面"的原因往往就是当天的慢查询。算不出时按 0 走确认弹窗，
  // 文案里说清楚这次没能评估 —— 后端对同一种情况也是这个口径。
  const previewMutation = useMutation({
    mutationFn: () =>
      qyPreviewViolationCategoryImpact({
        id: values.id,
        threshold: Number(values.threshold) || 0,
        window_hours: Number(values.window_hours) || 0,
        enabled: values.enabled,
      }),
    onSuccess: (data) => setPendingCount(data.impact.matched),
    onError: () => setPendingCount(0),
  })

  const isFallback = props.category?.is_fallback === true

  return (
    <>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={props.category == null ? t('qy_vcat_create') : t('qy_vcat_edit')}
        description={t('qy_vcat_form_desc')}
        contentClassName='sm:max-w-2xl'
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
              type='submit'
              form={FORM_ID}
              disabled={saveMutation.isPending || previewMutation.isPending}
            >
              {t('qy_common_submit')}
            </Button>
          </>
        }
      >
        <form
          id={FORM_ID}
          className='space-y-5'
          onSubmit={(e) => {
            e.preventDefault()
            const msg = qyValidateCategoryForm(values, t)
            setError(msg)
            if (msg != null) return
            if (qyCategoryTightens(props.category, values)) {
              previewMutation.mutate()
              return
            }
            saveMutation.mutate(false)
          }}
        >
          {error != null && (
            <p className='text-destructive text-sm' role='alert'>
              {error}
            </p>
          )}

          {/* ── 内部：只有管理端看得到 ── */}
          <section className='space-y-3'>
            <h3 className='text-sm font-medium'>{t('qy_vcat_sec_internal')}</h3>
            <div className='grid gap-3 sm:grid-cols-2'>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-vcat-key'>{t('qy_vcat_field_key')}</Label>
                <Input
                  id='qy-vcat-key'
                  value={values.key}
                  disabled={isFallback}
                  onChange={(e) =>
                    setValues((v) => ({ ...v, key: e.target.value }))
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {isFallback
                    ? t('qy_vcat_field_key_fallback')
                    : t('qy_vcat_field_key_desc')}
                </p>
              </div>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-vcat-name'>{t('qy_vcat_field_name')}</Label>
                <Input
                  id='qy-vcat-name'
                  value={values.name}
                  onChange={(e) =>
                    setValues((v) => ({ ...v, name: e.target.value }))
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_vcat_field_name_desc')}
                </p>
              </div>
            </div>
            <div className='space-y-1.5'>
              <Label htmlFor='qy-vcat-remark'>
                {t('qy_vcat_field_remark')}
              </Label>
              <Textarea
                id='qy-vcat-remark'
                rows={3}
                value={values.remark}
                onChange={(e) =>
                  setValues((v) => ({ ...v, remark: e.target.value }))
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t('qy_vcat_field_remark_desc')}
              </p>
            </div>
          </section>

          {/* ── 给 AI 的判定说明：随提示词发往第三方审核服务 ──

          这是**第三份文本**，它既不是内部备注也不是公示文案：

            内部备注   → 只有管理端。写运营口径（谁负责复核、误杀观察）。
            公示文案   → 用户端。绝不写判据。
            判定说明   → 发给审核模型。**写的就是判据**。

          三者共用一列的后果不是"少一列"，是永远只能二选一：拿公示文案当
          判定说明会让模型判得更差（它刻意不含判据）；把内部备注塞进提示词
          等于把人名与内部流程发到站外；而把判定说明公示出去就是把绕过方法
          印给用户。所以它自成一段，并且明说这段文字会离开本站。 */}
          <section className='space-y-3 rounded-md border p-3'>
            <h3 className='text-sm font-medium'>{t('qy_vcat_sec_ai')}</h3>
            <p className='text-muted-foreground text-xs'>
              {t('qy_vcat_sec_ai_desc')}
            </p>
            <div className='flex items-center gap-2'>
              <Switch
                id='qy-vcat-ai-participates'
                checked={!values.ai_excluded}
                onCheckedChange={(checked) =>
                  setValues((v) => ({ ...v, ai_excluded: checked !== true }))
                }
              />
              <Label htmlFor='qy-vcat-ai-participates'>
                {t('qy_vcat_field_ai_participates')}
              </Label>
            </div>
            <p className='text-muted-foreground text-xs'>
              {t('qy_vcat_field_ai_participates_desc')}
            </p>
            <div className='space-y-1.5'>
              <Label htmlFor='qy-vcat-ai-guidance'>
                {t('qy_vcat_field_ai_guidance')}
              </Label>
              <Textarea
                id='qy-vcat-ai-guidance'
                rows={3}
                disabled={values.ai_excluded}
                value={values.ai_guidance}
                onChange={(e) =>
                  setValues((v) => ({ ...v, ai_guidance: e.target.value }))
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t('qy_vcat_field_ai_guidance_desc')}
              </p>
            </div>
          </section>

          {/* ── 对外公示：用户端看得到 ── */}
          <section className='space-y-3 rounded-md border border-dashed p-3'>
            <h3 className='text-sm font-medium'>{t('qy_vcat_sec_public')}</h3>
            <p className='text-muted-foreground text-xs'>
              {t('qy_vcat_sec_public_warning')}
            </p>
            <div className='flex items-center gap-2'>
              <Switch
                id='qy-vcat-published'
                checked={values.published}
                onCheckedChange={(checked) =>
                  setValues((v) => ({ ...v, published: checked === true }))
                }
              />
              <Label htmlFor='qy-vcat-published'>
                {t('qy_vcat_field_published')}
              </Label>
            </div>
            <div className='space-y-1.5'>
              <Label htmlFor='qy-vcat-public-title'>
                {t('qy_vcat_field_public_title')}
              </Label>
              <Input
                id='qy-vcat-public-title'
                value={values.public_title}
                onChange={(e) =>
                  setValues((v) => ({ ...v, public_title: e.target.value }))
                }
              />
            </div>
            <div className='space-y-1.5'>
              <Label htmlFor='qy-vcat-public-desc'>
                {t('qy_vcat_field_public_desc')}
              </Label>
              <Textarea
                id='qy-vcat-public-desc'
                rows={2}
                value={values.public_desc}
                onChange={(e) =>
                  setValues((v) => ({ ...v, public_desc: e.target.value }))
                }
              />
            </div>
          </section>

          {/* ── 阈值：只出线，不出动作 ── */}
          <section className='space-y-3'>
            <h3 className='text-sm font-medium'>
              {t('qy_vcat_sec_threshold')}
            </h3>
            <p className='text-muted-foreground text-xs'>
              {t('qy_vcat_sec_threshold_desc')}
            </p>
            <div className='flex items-center gap-2'>
              <Switch
                id='qy-vcat-enabled'
                checked={values.enabled}
                onCheckedChange={(checked) =>
                  setValues((v) => ({ ...v, enabled: checked === true }))
                }
              />
              <Label htmlFor='qy-vcat-enabled'>
                {t('qy_vcat_field_enabled')}
              </Label>
            </div>
            <div className='grid gap-3 sm:grid-cols-3'>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-vcat-threshold'>
                  {t('qy_vcat_field_threshold')}
                </Label>
                <Input
                  id='qy-vcat-threshold'
                  type='number'
                  min={0}
                  value={values.threshold}
                  onChange={(e) =>
                    setValues((v) => ({ ...v, threshold: e.target.value }))
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_vcat_field_threshold_desc')}
                </p>
              </div>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-vcat-window'>
                  {t('qy_vcat_field_window')}
                </Label>
                <Input
                  id='qy-vcat-window'
                  type='number'
                  min={1}
                  value={values.window_hours}
                  onChange={(e) =>
                    setValues((v) => ({ ...v, window_hours: e.target.value }))
                  }
                />
              </div>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-vcat-sort'>{t('qy_vcat_field_sort')}</Label>
                <Input
                  id='qy-vcat-sort'
                  type='number'
                  min={0}
                  value={values.sort_order}
                  onChange={(e) =>
                    setValues((v) => ({ ...v, sort_order: e.target.value }))
                  }
                />
              </div>
            </div>
          </section>
        </form>
      </QyResponsiveDialog>

      {/* 收紧阈值的二次确认。文案口径必须与后端一致：**保存本身不处置任何人**，
          已越线的账号会在各自下一次违规命中时才被处置。说成"保存后立刻封号"
          会让管理员去封禁列表里找一批根本不会出现的行。 */}
      <QyConfirmDialog
        open={pendingCount != null}
        onOpenChange={(open) => {
          if (!open) setPendingCount(null)
        }}
        title={t('qy_vcat_confirm_title')}
        description={t('qy_vcat_confirm_desc', { count: pendingCount ?? 0 })}
        isLoading={saveMutation.isPending}
        onConfirm={() => saveMutation.mutate(true)}
      />
    </>
  )
}
