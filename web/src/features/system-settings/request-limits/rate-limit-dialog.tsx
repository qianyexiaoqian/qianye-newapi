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
import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
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
import { getUserGroupOptions } from '@/features/users/api'

const rateLimitDialogSchema = z.object({
  groupName: z.string().min(1, 'Group name is required'),
  maxRequests: z
    .number()
    .min(0, 'Must be ≥ 0')
    .max(2147483647, 'Must be ≤ 2,147,483,647'),
  maxSuccess: z
    .number()
    .min(1, 'Must be ≥ 1')
    .max(2147483647, 'Must be ≤ 2,147,483,647'),
  maxConcurrency: z
    .number()
    .min(0, 'Must be ≥ 0')
    .max(2147483647, 'Must be ≤ 2,147,483,647'),
})

type RateLimitDialogFormValues = z.infer<typeof rateLimitDialogSchema>

const RATE_LIMIT_FORM_ID = 'rate-limit-form'

export type RateLimitEntryData = {
  groupName: string
  maxRequests: number
  maxSuccess: number
  /** 同一时刻最多几个请求在飞。0 = 不限。 */
  maxConcurrency: number
}

type RateLimitDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (data: RateLimitEntryData) => void
  editData?: RateLimitEntryData | null
}

export function RateLimitDialog({
  open,
  onOpenChange,
  onSave,
  editData,
}: RateLimitDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!editData

  // 候选清单取**用户分组**,不是模型分组。
  //
  // 这一页配的是「哪一档用户每分钟能打几次」,而本 fork 把用户分组与模型分组拆成了
  // 两个命名空间(/api/user-group/options 与 /api/model-group/options)。之前这里
  // 是一个自由文本框,运营只能凭记忆敲名字,敲错了也没有任何提示 —— 那一行会安静地
  // 永不命中。allowCustomValue 保留:升级前配下的键可能是模型分组名或 auto,
  // 它们仍然有效(见 middleware/qy_rate_limit_export.go 的回落),不能让人改不了。
  const userGroupsQuery = useQuery({
    queryKey: ['user-group-options'],
    queryFn: getUserGroupOptions,
  })
  const userGroupOptions = useMemo(
    () =>
      (userGroupsQuery.data?.data ?? []).map((name) => ({
        value: name,
        label: name,
      })),
    [userGroupsQuery.data]
  )

  const form = useForm<RateLimitDialogFormValues>({
    resolver: zodResolver(rateLimitDialogSchema),
    defaultValues: {
      groupName: '',
      maxRequests: 0,
      maxSuccess: 1,
      maxConcurrency: 0,
    },
  })

  useEffect(() => {
    if (editData) {
      form.reset(editData)
    } else {
      form.reset({
        groupName: '',
        maxRequests: 0,
        maxSuccess: 1,
        maxConcurrency: 0,
      })
    }
  }, [editData, form, open])

  const handleSubmit = (values: RateLimitDialogFormValues) => {
    onSave(values)
    form.reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        isEditMode ? t('Edit group rate limit') : t('Add group rate limit')
      }
      // 口径句放在弹窗描述里,新增与编辑都看得到:本分支把用户分组与模型分组
      // 拆开了,「按哪个分组限流」正是回来编辑时最需要确认的一件事。
      description={t(
        'Limits apply to the user group (users.group), not the model group on the key.'
      )}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={RATE_LIMIT_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={RATE_LIMIT_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='groupName'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('User group')}</FormLabel>
                <FormControl>
                  {isEditMode ? (
                    <Input {...field} readOnly />
                  ) : (
                    <ComboboxInput
                      options={userGroupOptions}
                      value={field.value}
                      onValueChange={field.onChange}
                      allowCustomValue
                      emptyText={t('No user group found')}
                      placeholder={t('Pick a user group')}
                    />
                  )}
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='maxRequests'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Max Requests (including failures)')}</FormLabel>
                <FormControl>
                  <div className='flex items-center gap-2'>
                    <Input
                      type='number'
                      min={0}
                      max={2147483647}
                      step={1}
                      {...field}
                      onChange={(e) =>
                        field.onChange(parseInt(e.target.value) || 0)
                      }
                    />
                    <span className='text-muted-foreground text-sm'>
                      {t('times')}
                    </span>
                  </div>
                </FormControl>
                <FormDescription>
                  {t('Total requests allowed per period. 0 = unlimited.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='maxSuccess'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Max Successful Requests')}</FormLabel>
                <FormControl>
                  <div className='flex items-center gap-2'>
                    <Input
                      type='number'
                      min={1}
                      max={2147483647}
                      step={1}
                      {...field}
                      onChange={(e) =>
                        field.onChange(parseInt(e.target.value) || 1)
                      }
                    />
                    <span className='text-muted-foreground text-sm'>
                      {t('times')}
                    </span>
                  </div>
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='maxConcurrency'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Max concurrent requests')}</FormLabel>
                <FormControl>
                  <div className='flex items-center gap-2'>
                    <Input
                      type='number'
                      min={0}
                      max={2147483647}
                      step={1}
                      {...field}
                      onChange={(e) =>
                        field.onChange(parseInt(e.target.value) || 0)
                      }
                    />
                    <span className='text-muted-foreground text-sm'>
                      {t('in flight')}
                    </span>
                  </div>
                </FormControl>
                <FormDescription>
                  {t(
                    'How many requests from this group may run at the same time. 0 = unlimited. Counted per node.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
