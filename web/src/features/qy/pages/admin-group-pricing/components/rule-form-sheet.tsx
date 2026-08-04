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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyOpsErrorMessage } from '../../ops/errors'
import { qyCreateGpRule, qyUpdateGpRule } from '../api'
import {
  QY_GP_MODEL_WILDCARD,
  QY_GP_MODES,
  qyEmptyGpRule,
  qyGpRuleSchema,
  qyGpRuleToForm,
  qyGpRuleToPayload,
  type QyGpRuleFormValues,
} from '../lib/rule-form'
import type { QyGpRule } from '../types'
import { QyGpEffectivePreview } from './effective-preview'

const FORM_ID = 'qy-group-pricing-rule-form'

type QyGpRuleFormSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示新建。 */
  rule: QyGpRule | null
  /** 模型分组候选。仅作为输入辅助，任何价格都不由它算出来。 */
  groups: string[]
  models: string[]
  /**
   * **用户分组**候选，后端下发。只用于试算，不写进规则。
   *
   * 两个轴始终分列显示，绝不合并成一个下拉：方案 3 的已知代价是
   * `users.group` 与 `channels.group` 共用同一个字符串命名空间，
   * 界面必须替运营把这两件事分开。
   */
  userGroups: string[]
  shadowMode: boolean
  onSaved: () => void
}

/**
 * 分组价格规则的新建 / 编辑抽屉。
 *
 * 表单只有五个字段，真正的内容是下方那块**随输入实时更新的折算面板**：
 * 相乘方案下「0.5」这个输入值本身没有意义，运营要决策的是「× 1.2 之后是 0.6，
 * 比现在的 0.48 涨了 25%」。面板不是补充说明，是这个表单的主体。
 *
 * 分组用 Select（分组是有限枚举，手输必然打错）；模型用**允许自定义值**的
 * Combobox —— 后端支持 `*` 与 `前缀*` 两种通配形态，锁死成纯下拉会让这两种
 * 形态无法录入。写错模型名的风险由后端的 `warning` 兜住（它读得到该模型当前的
 * 全局计费口径，能直接说「这条规则不会生效」）。
 */
export function QyGpRuleFormSheet(props: QyGpRuleFormSheetProps) {
  const { t } = useTranslation()
  const [confirmOpen, setConfirmOpen] = useState(false)

  /**
   * 试算口径:站在哪个用户分组的角度看这条规则。
   *
   * 刻意**不放进 react-hook-form**:它不是规则的字段，规则的键是
   * (模型分组, 模型)，里面没有用户分组这个维度。放进表单值会让人以为它会被存下来，
   * 而下一个人多半就真的把它存下来了。
   */
  const [trialUserGroup, setTrialUserGroup] = useState('')

  const form = useForm<QyGpRuleFormValues>({
    resolver: zodResolver(qyGpRuleSchema),
    defaultValues: qyEmptyGpRule(''),
  })

  useEffect(() => {
    if (!props.open) return
    setConfirmOpen(false)
    // 刻意不预填一个默认用户分组:随便挑一个得到的是一个「看起来精确」的错数字。
    // 沿用后端 adminPreview 的同一条原则 —— 缺省时不猜，直接说缺什么。
    setTrialUserGroup('')
    form.reset(
      props.rule == null
        ? qyEmptyGpRule(props.groups[0] ?? '')
        : qyGpRuleToForm(props.rule)
    )
  }, [form, props.groups, props.open, props.rule])

  const saveMutation = useMutation({
    mutationFn: (values: QyGpRuleFormValues) => {
      const payload = qyGpRuleToPayload(values)
      return props.rule == null
        ? qyCreateGpRule(payload)
        : qyUpdateGpRule(props.rule.id, payload)
    },
    onSuccess: () => {
      toast.success(t('qy_gp_saved'))
      setConfirmOpen(false)
      props.onSaved()
      props.onOpenChange(false)
    },
    // 400 的后端原文（覆盖值越界、口径不匹配、(分组,模型) 已存在）是管理员
    // 唯一的修正依据，qyOpsErrorMessage 会原样透出。
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const groupName = form.watch('group_name')
  const modelName = form.watch('model_name')
  const mode = form.watch('mode')
  const value = form.watch('value')
  // 用 watch 而不是 getValues：这个值决定二次确认里要不要强制勾选那道闸门，
  // getValues 不订阅变更，会在「刚打开启用开关就提交」时读到上一轮的旧值。
  const enabled = form.watch('enabled')

  // 编辑一条分组已经不在候选清单里的规则时，仍要能选中它自己 ——
  // 否则打开编辑抽屉会看到一个空的分组下拉，保存下去就把分组改没了。
  const groupOptions = useMemo(() => {
    if (groupName === '' || props.groups.includes(groupName)) {
      return props.groups
    }
    return [groupName, ...props.groups]
  }, [groupName, props.groups])

  const preview = (
    <div className='space-y-2'>
      {/* 试算口径的选择器紧贴折算面板，不混进上面那组规则字段:
          它不是规则的一部分，把它排进字段列表会让人以为它会被保存。 */}
      <div className='space-y-1'>
        <p className='text-sm font-medium'>{t('qy_gp_field_user_group')}</p>
        <Select
          value={trialUserGroup}
          // 清空(null)归一成空串:空串是「还没选」的唯一表示，
          // 多一个 null 只会让下游多一处 == null 判断，而两者含义完全相同。
          onValueChange={(next) => setTrialUserGroup(next ?? '')}
        >
          <SelectTrigger className='w-full'>
            <SelectValue placeholder={t('qy_gp_field_user_group_ph')} />
          </SelectTrigger>
          <SelectContent>
            {props.userGroups.map((group) => (
              <SelectItem key={group} value={group}>
                {group}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className='text-muted-foreground text-xs'>
          {t('qy_gp_field_user_group_desc')}
        </p>
      </div>
      <QyGpEffectivePreview
        groupName={groupName}
        modelName={modelName}
        mode={mode}
        value={value}
        userGroup={trialUserGroup}
        shadowMode={props.shadowMode}
      />
    </div>
  )

  return (
    <Form {...form}>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={props.rule == null ? t('qy_gp_create') : t('qy_gp_edit')}
        description={t('qy_gp_form_desc')}
        contentClassName='sm:max-w-xl'
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
              disabled={saveMutation.isPending}
            >
              {t('qy_common_submit')}
            </Button>
          </>
        }
      >
        <form
          id={FORM_ID}
          className='space-y-4'
          onSubmit={form.handleSubmit((values) => {
            // 影子模式下保存不动任何人的余额，直接存；真实模式下每一次保存都会
            // 立刻改变扣费金额，必须先把「改前 → 改后」复述一遍再让人点确认。
            if (props.shadowMode) {
              saveMutation.mutate(values)
              return
            }
            setConfirmOpen(true)
          })}
        >
          <FormField
            control={form.control}
            name='group_name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_gp_field_group')}</FormLabel>
                <Select value={field.value} onValueChange={field.onChange}>
                  <FormControl>
                    <SelectTrigger className='w-full'>
                      <SelectValue placeholder={t('qy_gp_field_group_ph')} />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent>
                    {groupOptions.map((group) => (
                      <SelectItem key={group} value={group}>
                        {group}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FormDescription>{t('qy_gp_field_group_desc')}</FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='model_name'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_gp_field_model')}</FormLabel>
                <FormControl>
                  <Combobox
                    options={props.models.map((model) => ({
                      value: model,
                      label: model,
                    }))}
                    value={field.value}
                    onValueChange={(next) => field.onChange(next ?? '')}
                    searchPlaceholder={t('qy_gp_field_model_ph')}
                    emptyText={t('qy_gp_field_model_empty')}
                    allowCustomValue
                  />
                </FormControl>
                <FormDescription>
                  {t('qy_gp_field_model_desc', {
                    wildcard: QY_GP_MODEL_WILDCARD,
                  })}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='mode'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_gp_field_mode')}</FormLabel>
                <Select value={field.value} onValueChange={field.onChange}>
                  <FormControl>
                    <SelectTrigger className='w-full'>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent>
                    {QY_GP_MODES.map((item) => (
                      <SelectItem key={item} value={item}>
                        {t(`qy_gp_mode_${item}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t(`qy_gp_mode_${mode}_desc`)}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='value'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t(`qy_gp_field_value_${mode}`)}</FormLabel>
                <FormControl>
                  <Input
                    {...field}
                    inputMode='decimal'
                    autoComplete='off'
                    className='font-mono'
                    placeholder={t('qy_gp_field_value_ph')}
                  />
                </FormControl>
                <FormDescription>{t('qy_gp_field_value_desc')}</FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {preview}

          <FormField
            control={form.control}
            name='remark'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_common_remark')}</FormLabel>
                <FormControl>
                  <Input {...field} placeholder={t('qy_gp_remark_ph')} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <FormItem className='flex items-start justify-between gap-4 rounded-lg border p-3'>
                <div className='min-w-0'>
                  <FormLabel>{t('qy_gp_field_enabled')}</FormLabel>
                  <FormDescription>
                    {t('qy_gp_field_enabled_desc')}
                  </FormDescription>
                </div>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </FormItem>
            )}
          />
        </form>
      </QyResponsiveDialog>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_gp_save_live_title')}
        description={t('qy_gp_save_live_desc', {
          group: groupName,
          model: modelName,
        })}
        details={preview}
        // 只有「启用」的规则才会立刻改变扣费。存一条未启用的草稿不需要
        // 强制勾选，否则这道闸门会因为太常出现而被人条件反射地划过去。
        irreversible={enabled}
        confirmText={t('qy_gp_save_live_confirm')}
        isLoading={saveMutation.isPending}
        onConfirm={() => saveMutation.mutate(form.getValues())}
      />
    </Form>
  )
}
