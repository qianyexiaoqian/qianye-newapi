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
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ComboboxInput } from '@/components/ui/combobox-input'
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
import { Textarea } from '@/components/ui/textarea'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyOpsErrorMessage } from '../../ops/errors'
import { qyCreateTransferGroupRule, qyUpdateTransferGroupRule } from '../api'
import {
  QY_GROUP_POLICIES,
  qyAppendGroup,
  qyEmptyGroupRule,
  qyGroupOptionLabel,
  qyGroupPolicyNeedsList,
  qyGroupRuleSchema,
  qyGroupRuleToForm,
  qyGroupRuleToPayload,
  qyIsSelfToken,
  qyNormalizeGroupName,
  qyRuleGroupNames,
  qySplitGroupList,
  qyUnknownGroupNames,
  type QyGroupRuleFormValues,
} from '../lib/rule-form'
import {
  QY_GROUP_SELF_TOKEN,
  QY_GROUP_WILDCARD,
  type QyTransferGroupOption,
  type QyTransferGroupRule,
} from '../types'

type QyGroupRuleFormSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示新建。 */
  rule: QyTransferGroupRule | null
  /**
   * 站点定义过的分组，带倍率 / 渠道 / 公开可选三项元数据。
   *
   * 它是下拉的取值域，也是「这个名字站点定义过没有」的判据 —— 但**不是闸门**：
   * 两个输入都允许自由填写，历史分组必须仍然能配规则。
   */
  groupOptions: QyTransferGroupOption[]
  /** abilities 探测是否成功。false 时不能拿 `has_channels` 说事。 */
  channelsProbeOk: boolean
  onSaved: () => void
}

/**
 * 分组规则新建 / 编辑抽屉。
 *
 * 这一页的难点不是表单本身，而是让运营看懂两件事：
 *   1. **规则只约束发起方**。「A 只能转给 B」不会顺带禁止 B 转给 A。
 *   2. **`@self` 是什么**。它是「只能转给同组」「禁止组内互转」这两种常见
 *      形态的唯一写法，不解释清楚就没人会用，运营只能退化成为每个分组各写
 *      一条只有自己名字的规则。
 *
 * 因此策略下拉与名单输入框各自带一段随策略变化的说明，而不是一段静态帮助文本。
 */
export function QyGroupRuleFormSheet(props: QyGroupRuleFormSheetProps) {
  const { t } = useTranslation()

  const form = useForm<QyGroupRuleFormValues>({
    resolver: zodResolver(qyGroupRuleSchema),
    defaultValues: qyEmptyGroupRule(),
  })

  useEffect(() => {
    if (!props.open) return
    form.reset(
      props.rule == null ? qyEmptyGroupRule() : qyGroupRuleToForm(props.rule)
    )
  }, [form, props.open, props.rule])

  const saveMutation = useMutation({
    mutationFn: (values: QyGroupRuleFormValues) => {
      const payload = qyGroupRuleToPayload(values)
      return props.rule == null
        ? qyCreateTransferGroupRule(payload)
        : qyUpdateTransferGroupRule(props.rule.id, payload)
    },
    onSuccess: (saved) => {
      toast.success(t('qy_trg_saved'))
      // 后端在 200 的回执里带软告警：保存成功 ≠ 这条规则会命中。
      // 抽屉这时已经关了，所以必须靠 toast 说出来。
      if (saved.unknown_groups.length > 0) {
        toast.warning(
          t('qy_trg_unknown_warning', {
            groups: saved.unknown_groups.join('、'),
          })
        )
      }
      props.onSaved()
      props.onOpenChange(false)
    },
    // 后端的校验消息（「白名单为空等同于禁止发起划转，请直接选择 deny_all」）
    // 是管理员唯一的修正依据，qyOpsErrorMessage 会把 400 的原文透出来。
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const policy = form.watch('policy')
  const fromGroup = form.watch('from_group')
  const toGroups = form.watch('to_groups')
  const needsList = qyGroupPolicyNeedsList(policy)
  // 未定义分组是**当场**算的：等保存回执才提示，运营已经点完确认了。
  const unknown = qyUnknownGroupNames(
    qyRuleGroupNames(fromGroup, toGroups, policy),
    props.groupOptions
  )

  return (
    <Form {...form}>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={props.rule == null ? t('qy_trg_create') : t('qy_trg_edit')}
        description={t('qy_trg_form_desc')}
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
              form='qy-transfer-group-rule-form'
              disabled={saveMutation.isPending}
            >
              {t('qy_common_submit')}
            </Button>
          </>
        }
      >
        <form
          id='qy-transfer-group-rule-form'
          className='space-y-4'
          onSubmit={form.handleSubmit((values) => saveMutation.mutate(values))}
        >
          {/* 分组的定义方不是这一页。说清楚它，否则运营会在这里找"新建分组"。 */}
          <p className='text-muted-foreground rounded-md border border-dashed p-3 text-xs'>
            {t('qy_trg_group_source_note')}
          </p>

          <FormField
            control={form.control}
            name='from_group'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_trg_field_from')}</FormLabel>
                <FormControl>
                  {/* 裸 datalist 只提示、不校验、不告警：名字打错一个字母不会
                        有任何信号，而那条规则会静默变成永不命中。换成带元数据的
                        下拉，同时保留自由输入（历史分组仍要能配）。 */}
                  <ComboboxInput
                    options={[
                      {
                        value: QY_GROUP_WILDCARD,
                        label: `${QY_GROUP_WILDCARD} · ${t('qy_trg_fallback_label')}`,
                      },
                      ...props.groupOptions.map((option) => ({
                        value: option.name,
                        label: qyGroupOptionLabel(
                          option,
                          props.channelsProbeOk,
                          t
                        ),
                      })),
                    ]}
                    value={field.value}
                    onValueChange={field.onChange}
                    allowCustomValue
                    emptyText='qy_trg_group_picker_empty'
                    placeholder={t('qy_trg_field_from_ph', {
                      wildcard: QY_GROUP_WILDCARD,
                    })}
                  />
                </FormControl>
                <FormDescription>
                  {t('qy_trg_field_from_desc', {
                    wildcard: QY_GROUP_WILDCARD,
                  })}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='policy'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_trg_field_policy')}</FormLabel>
                <Select value={field.value} onValueChange={field.onChange}>
                  <FormControl>
                    <SelectTrigger className='w-full'>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent>
                    {QY_GROUP_POLICIES.map((item) => (
                      <SelectItem key={item} value={item}>
                        {t(`qy_trg_policy_${item}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FormDescription>
                  {t(`qy_trg_policy_${policy}_desc`)}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {/* 非名单策略下把输入框整个撤掉而不是禁用：留一个灰着的框，
                下一个人打开这条规则时仍会以为里面的名单还算数。 */}
          {needsList && (
            <FormField
              control={form.control}
              name='to_groups'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_trg_field_to')}</FormLabel>
                  {/* 选一个就追加一项。名单本身仍然可以直接编辑文本框：
                        下拉解决"有哪些分组、它们什么样"，自由输入解决
                        "站点已经不认的历史分组仍要能配"。 */}
                  <ComboboxInput
                    options={[
                      {
                        value: QY_GROUP_SELF_TOKEN,
                        label: `${QY_GROUP_SELF_TOKEN} · ${t('qy_trg_self_label')}`,
                      },
                      ...props.groupOptions.map((option) => ({
                        value: option.name,
                        label: qyGroupOptionLabel(
                          option,
                          props.channelsProbeOk,
                          t
                        ),
                      })),
                    ]}
                    value=''
                    onValueChange={(picked) =>
                      field.onChange(qyAppendGroup(field.value, picked))
                    }
                    emptyText='qy_trg_group_picker_empty'
                    placeholder={t('qy_trg_to_picker_ph')}
                  />
                  <FormControl>
                    <Textarea rows={3} className='font-mono' {...field} />
                  </FormControl>
                  {field.value.trim() !== '' && (
                    <div className='flex flex-wrap gap-1'>
                      {/* 文本框是自由编辑的，名单里可能出现重复项（后端保存时
                            才会去重），因此 key 必须带序号。 */}
                      {qySplitGroupList(field.value).map((entry, index) => (
                        <Badge
                          key={`${entry}-${index}`}
                          variant={
                            unknown.includes(qyNormalizeGroupName(entry))
                              ? 'warning'
                              : 'secondary'
                          }
                          className='font-normal'
                        >
                          {qyIsSelfToken(entry)
                            ? t('qy_trg_self_label')
                            : entry}
                        </Badge>
                      ))}
                    </div>
                  )}
                  <FormDescription className='space-y-1'>
                    <span className='block'>{t('qy_trg_field_to_desc')}</span>
                    <span className='block'>
                      <Badge variant='outline' className='me-1 font-mono'>
                        {QY_GROUP_SELF_TOKEN}
                      </Badge>
                      {t('qy_trg_self_token_desc')}
                    </span>
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}

          {/* 软告警，不是错误：不禁用提交按钮，也不进 zod schema。
                历史分组恰恰是最需要限制转出的一批账号。 */}
          {unknown.length > 0 && (
            <p className='text-warning text-xs'>
              {t('qy_trg_unknown_warning', { groups: unknown.join('、') })}
            </p>
          )}

          <FormField
            control={form.control}
            name='remark'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_common_remark')}</FormLabel>
                <FormControl>
                  <Input {...field} placeholder={t('qy_trg_remark_ph')} />
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
                  <FormLabel>{t('qy_trg_field_enabled')}</FormLabel>
                  <FormDescription>
                    {t('qy_trg_field_enabled_desc')}
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
    </Form>
  )
}
