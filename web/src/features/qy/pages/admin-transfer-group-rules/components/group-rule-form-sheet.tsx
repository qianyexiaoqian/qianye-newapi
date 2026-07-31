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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { qyOpsErrorMessage } from '../../ops/errors'
import { qyCreateTransferGroupRule, qyUpdateTransferGroupRule } from '../api'
import {
  QY_GROUP_POLICIES,
  qyEmptyGroupRule,
  qyGroupPolicyNeedsList,
  qyGroupRuleSchema,
  qyGroupRuleToForm,
  qyGroupRuleToPayload,
  type QyGroupRuleFormValues,
} from '../lib/rule-form'
import {
  QY_GROUP_SELF_TOKEN,
  QY_GROUP_WILDCARD,
  type QyTransferGroupRule,
} from '../types'

type QyGroupRuleFormSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示新建。 */
  rule: QyTransferGroupRule | null
  /** 已知分组候选，只作为填写提示；后端允许任意分组名。 */
  knownGroups: string[]
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
    onSuccess: () => {
      toast.success(t('qy_trg_saved'))
      props.onSaved()
      props.onOpenChange(false)
    },
    // 后端的校验消息（「白名单为空等同于禁止发起划转，请直接选择 deny_all」）
    // 是管理员唯一的修正依据，qyOpsErrorMessage 会把 400 的原文透出来。
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const policy = form.watch('policy')
  const needsList = qyGroupPolicyNeedsList(policy)

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent side='right' className='sm:max-w-xl'>
        <SheetHeader>
          <SheetTitle className='pr-8'>
            {props.rule == null ? t('qy_trg_create') : t('qy_trg_edit')}
          </SheetTitle>
          <SheetDescription>{t('qy_trg_form_desc')}</SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='qy-transfer-group-rule-form'
            className='min-h-0 flex-1 space-y-4 overflow-y-auto px-4'
            onSubmit={form.handleSubmit((values) =>
              saveMutation.mutate(values)
            )}
          >
            <FormField
              control={form.control}
              name='from_group'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_trg_field_from')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      list='qy-trg-known-groups'
                      placeholder={t('qy_trg_field_from_ph', {
                        wildcard: QY_GROUP_WILDCARD,
                      })}
                    />
                  </FormControl>
                  <datalist id='qy-trg-known-groups'>
                    {props.knownGroups.map((group) => (
                      <option key={group} value={group} />
                    ))}
                  </datalist>
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
                    <FormControl>
                      <Textarea rows={3} className='font-mono' {...field} />
                    </FormControl>
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

          <SheetFooter className='flex-row justify-end gap-2 border-t'>
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
          </SheetFooter>
        </Form>
      </SheetContent>
    </Sheet>
  )
}
