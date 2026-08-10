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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import {
  qyGroupOptionLabel,
  qyGroupOptionsQuery,
} from '../../../lib/group-options'
import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  previewQyViolationBanPolicyImpact,
  upsertQyViolationBanPolicy,
  upsertQyViolationDefaultBanPolicy,
} from '../api'
import type { QyViolationBanPolicy, QyViolationBanPolicyAction } from '../types'

const ACTIONS: QyViolationBanPolicyAction[] = ['record', 'restrict', 'ban']

/** 与后端 `maxPolicyWindowHours` / `maxPolicyThreshold` 同口径。 */
const MAX_WINDOW_HOURS = 24 * 90
const MAX_THRESHOLD = 1_000_000

type FormState = {
  userGroup: string
  enabled: boolean
  windowHours: number
  threshold: number
  action: QyViolationBanPolicyAction
  remark: string
}

function toForm(policy: QyViolationBanPolicy | null): FormState {
  if (policy == null) {
    return {
      userGroup: '',
      enabled: true,
      windowHours: 24,
      threshold: 0,
      // 新建默认落「仅记录」:新增一档策略最不该产生的效果就是当场封掉一批人。
      // 与内置规则一律落影子是同一个取舍 —— 先观察，确认之后再收紧。
      action: 'record',
      remark: '',
    }
  }
  return {
    userGroup: policy.user_group,
    enabled: policy.enabled,
    windowHours: policy.window_hours,
    threshold: policy.threshold,
    action: policy.action,
    remark: policy.remark,
  }
}

/**
 * 新建 / 编辑一档处置策略。
 *
 * # 这个弹窗真正要解决的问题
 *
 * 改阈值不是改一个配置项：滚动窗口里的计数早就攒好了，阈值一降，一批账号
 * 当场就处在越线状态，他们各自的下一次违规命中会把他们收走。所以表单一边填、
 * 一边实时拉影响面预览，把「有几个人已经越线」摆在提交按钮正上方，
 * 而不是等按下之后再用一个 409 告诉他。
 *
 * # 这个数不是「按下保存会封掉几个人」
 *
 * 保存只写策略表，服务端全程不处置任何账号（见后端 adminUpsertBanPolicy 的说明）。
 * 文案必须照「已越线 / 下一次命中时处置」这个语义写：说成「保存后立刻处置」，
 * 管理员会去封禁列表里找一批根本不会出现的行，然后以为功能坏了。
 *
 * 提交分两步:先看数字、再点确认，`confirm` 那一位只在第二步带上。
 * 后端的 409 是给绕过界面直接调接口的那条路兜底的，不是这里的正常路径。
 */
export function QyViolationBanPolicyDialog(props: {
  open: boolean
  policy: QyViolationBanPolicy | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const fieldId = useId()
  const [form, setForm] = useState<FormState>(toForm(props.policy))
  const [confirming, setConfirming] = useState(false)

  const isDefault = props.policy?.is_default === true
  const isEditing = props.policy != null

  useEffect(() => {
    setForm(toForm(props.policy))
    setConfirming(false)
  }, [props.policy, props.open])

  const groupQuery = useQuery({ ...qyGroupOptionsQuery(), enabled: props.open })
  const groupOptions = groupQuery.data?.options ?? []

  const impactParams = {
    user_group: isDefault ? '' : form.userGroup.trim(),
    threshold: form.threshold,
    window_hours: form.windowHours,
    action: form.action,
  }
  const impactQuery = useQuery({
    queryKey: qyKeys.adminViolationBanPolicyImpact(impactParams),
    queryFn: () => previewQyViolationBanPolicyImpact(impactParams),
    // 阈值 0 = 不处置，影响面恒为 0，没有必要为它发请求。
    enabled: props.open && form.threshold > 0,
    staleTime: 10_000,
    retry: false,
  })

  const mutation = useMutation({
    mutationFn: () => {
      const body = {
        window_hours: form.windowHours,
        threshold: form.threshold,
        action: form.action,
        remark: form.remark,
        confirm: true,
      }
      if (isDefault) return upsertQyViolationDefaultBanPolicy(body)
      return upsertQyViolationBanPolicy({
        ...body,
        user_group: form.userGroup.trim(),
        enabled: form.enabled,
      })
    },
    onSuccess: async (data) => {
      toast.success(
        data.impact.matched > 0
          ? t('qy_vio_policy_saved_with_impact', { count: data.impact.matched })
          : t('qy_vio_policy_saved')
      )
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminViolationBanPolicies(),
      })
      props.onClose()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const groupMissing = !isDefault && form.userGroup.trim() === ''
  const windowInvalid =
    form.windowHours < 1 || form.windowHours > MAX_WINDOW_HOURS
  const thresholdInvalid = form.threshold < 0 || form.threshold > MAX_THRESHOLD
  const canSubmit = !groupMissing && !windowInvalid && !thresholdInvalid

  const impact = impactQuery.data?.impact
  const impactCount = form.threshold > 0 ? (impact?.matched ?? null) : 0

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={
        isDefault
          ? t('qy_vio_policy_edit_default_title')
          : isEditing
            ? t('qy_vio_policy_edit_title')
            : t('qy_vio_policy_create_title')
      }
      description={
        isDefault
          ? t('qy_vio_policy_edit_default_desc')
          : t('qy_vio_policy_edit_desc')
      }
      footer={
        <>
          <Button type='button' variant='outline' onClick={props.onClose}>
            {t('qy_common_cancel')}
          </Button>
          {confirming ? (
            <Button
              type='button'
              disabled={mutation.isPending}
              onClick={() => mutation.mutate()}
            >
              {t('qy_vio_policy_confirm_apply')}
            </Button>
          ) : (
            <Button
              type='button'
              disabled={!canSubmit}
              onClick={() => setConfirming(true)}
            >
              {t('qy_common_submit')}
            </Button>
          )}
        </>
      }
    >
      <div className='space-y-3'>
        {!isDefault && (
          <div className='space-y-1'>
            <Label htmlFor={`${fieldId}-group`}>
              {t('qy_vio_policy_field_group')}
            </Label>
            {/* 下拉解决「站点有哪些分组」，文本框保留自由输入 ——
                站点已经不认的历史分组仍然可能有活跃账号，而那批账号恰恰
                最需要被策略覆盖。打错字的后果是这一档永不生效(该分组
                静默回落兜底)，所以下面的未知分组提示不能省。 */}
            <ComboboxInput
              options={groupOptions.map((option) => ({
                value: option.name,
                label: qyGroupOptionLabel(
                  option,
                  groupQuery.data?.probe_ok === true,
                  t
                ),
              }))}
              value=''
              onValueChange={(picked) =>
                setForm((prev) => ({ ...prev, userGroup: picked }))
              }
              emptyText='qy_trg_group_picker_empty'
              placeholder={t('qy_vio_policy_field_group_pick')}
            />
            <Input
              id={`${fieldId}-group`}
              value={form.userGroup}
              placeholder='vip'
              onChange={(event) =>
                setForm((prev) => ({ ...prev, userGroup: event.target.value }))
              }
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_vio_policy_field_group_desc')}
            </p>
          </div>
        )}

        <div className='grid grid-cols-2 gap-3'>
          <div className='space-y-1'>
            <Label htmlFor={`${fieldId}-window`}>
              {t('qy_vio_policy_field_window')}
            </Label>
            <Input
              id={`${fieldId}-window`}
              type='number'
              min={1}
              max={MAX_WINDOW_HOURS}
              value={form.windowHours}
              onChange={(event) =>
                setForm((prev) => ({
                  ...prev,
                  windowHours: Number(event.target.value),
                }))
              }
            />
          </div>
          <div className='space-y-1'>
            <Label htmlFor={`${fieldId}-threshold`}>
              {t('qy_vio_policy_field_threshold')}
            </Label>
            <Input
              id={`${fieldId}-threshold`}
              type='number'
              min={0}
              max={MAX_THRESHOLD}
              value={form.threshold}
              onChange={(event) =>
                setForm((prev) => ({
                  ...prev,
                  threshold: Number(event.target.value),
                }))
              }
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_vio_policy_field_threshold_desc')}
            </p>
          </div>
        </div>

        <div className='space-y-1'>
          <Label>{t('qy_vio_policy_field_action')}</Label>
          <Select
            value={form.action}
            onValueChange={(value) =>
              setForm((prev) => ({
                ...prev,
                action: value as QyViolationBanPolicyAction,
              }))
            }
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ACTIONS.map((action) => (
                <SelectItem key={action} value={action}>
                  {t(`qy_vio_policy_action_${action}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {t(`qy_vio_policy_action_${form.action}_desc`)}
          </p>
        </div>

        {/* 兜底档没有停用开关:它没有可回落的下一级，停用等于让没配分组的
            用户落进一个不存在的策略。 */}
        {!isDefault && (
          <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
            <div className='min-w-0'>
              <Label htmlFor={`${fieldId}-enabled`}>
                {t('qy_vio_policy_field_enabled')}
              </Label>
              <p className='text-muted-foreground text-xs'>
                {t('qy_vio_policy_field_enabled_desc')}
              </p>
            </div>
            <Switch
              id={`${fieldId}-enabled`}
              checked={form.enabled}
              onCheckedChange={(checked) =>
                setForm((prev) => ({ ...prev, enabled: checked }))
              }
            />
          </div>
        )}

        <div className='space-y-1'>
          <Label htmlFor={`${fieldId}-remark`}>{t('qy_common_remark')}</Label>
          <Textarea
            id={`${fieldId}-remark`}
            rows={2}
            value={form.remark}
            onChange={(event) =>
              setForm((prev) => ({ ...prev, remark: event.target.value }))
            }
          />
        </div>

        {/* 影响面。它是这个弹窗存在的理由，所以永远渲染 —— 包括「0 个」
            和「算不出来」两种情况:一个时有时无的数字，会让人以为没显示
            就等于没有影响。 */}
        <div className='space-y-1 rounded-lg border p-3 text-xs'>
          <p className='text-foreground font-medium'>
            {t('qy_vio_policy_impact_title')}
          </p>
          {form.threshold <= 0 ? (
            <p>{t('qy_vio_policy_impact_threshold_off')}</p>
          ) : impactQuery.isPending ? (
            <p className='text-muted-foreground'>
              {t('qy_vio_policy_loading')}
            </p>
          ) : impactQuery.isError ? (
            <p className='text-amber-600 dark:text-amber-400'>
              {t('qy_vio_policy_impact_failed')}
            </p>
          ) : (
            <>
              <p
                className={
                  (impactCount ?? 0) > 0
                    ? 'text-amber-600 dark:text-amber-400'
                    : 'text-muted-foreground'
                }
              >
                {t('qy_vio_policy_impact_count', {
                  count: impactCount ?? 0,
                  action: t(`qy_vio_policy_action_${form.action}`),
                })}
              </p>
              {impact != null && impact.capped && (
                <p>{t('qy_vio_policy_impact_capped')}</p>
              )}
              {impact != null && impact.user_ids.length > 0 && (
                <p className='text-muted-foreground'>
                  {t('qy_vio_policy_impact_sample', {
                    ids: impact.user_ids.join(', '),
                  })}
                </p>
              )}
              {impactQuery.data?.scope_all_groups === true && (
                <p className='text-muted-foreground'>
                  {t('qy_vio_policy_impact_all_groups')}
                </p>
              )}
            </>
          )}
        </div>

        {confirming && (
          <div className='rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-xs'>
            {t('qy_vio_policy_confirm_warning', {
              count: impactCount ?? 0,
              action: t(`qy_vio_policy_action_${form.action}`),
            })}
          </div>
        )}
      </div>
    </QyResponsiveDialog>
  )
}
