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
import { useEffect, useState } from 'react'
import { useForm, type Control, type UseFormGetValues } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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
import { formatQyMicros } from '../../ops/format'
import {
  createQyViolationRule,
  testQyViolationRule,
  updateQyViolationRule,
} from '../api'
import {
  QY_VIOLATION_ACTIONS,
  QY_VIOLATION_FEE_MODES,
  QY_VIOLATION_GROUP_SCOPE_MODES,
  QY_VIOLATION_MATCH_TYPES,
  QY_VIOLATION_PHASES,
  qyEmptyViolationRule,
  qyViolationRuleSchema,
  qyViolationRuleToForm,
  qyViolationRuleToPayload,
  type QyViolationRuleFormValues,
} from '../lib/rule-form'
import type { QyViolationRule, QyViolationRuleTestResult } from '../types'

type QyRuleFormSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** `null` 表示新建。 */
  rule: QyViolationRule | null
  onSaved: () => void
}

/**
 * 规则新建 / 编辑抽屉，内置试跑面板。
 *
 * 试跑刻意做在同一个抽屉里而不是独立弹窗：它跑的是**当前正在编辑的这份表单**，
 * 分开就变成了「测一个规则、存另一个规则」，反而制造安全假象。
 */
export function QyRuleFormSheet(props: QyRuleFormSheetProps) {
  const { t } = useTranslation()

  const form = useForm<QyViolationRuleFormValues>({
    resolver: zodResolver(qyViolationRuleSchema),
    defaultValues: qyEmptyViolationRule(),
  })

  useEffect(() => {
    if (!props.open) return
    form.reset(
      props.rule == null
        ? qyEmptyViolationRule()
        : qyViolationRuleToForm(props.rule)
    )
  }, [form, props.open, props.rule])

  // 匹配方式决定了下面好几块的文案与校验，订阅它比读 getValues 更直接：
  // 「选了频率判据却还看着关键词的说明」是这一页最容易误导人的状态。
  const matchType = form.watch('match_type')
  const isRate = matchType === 'request_rate'

  const saveMutation = useMutation({
    mutationFn: (values: QyViolationRuleFormValues) => {
      const payload = qyViolationRuleToPayload(values)
      return props.rule == null
        ? createQyViolationRule(payload)
        : updateQyViolationRule(props.rule.id, payload)
    },
    onSuccess: () => {
      toast.success(t('qy_vio_rule_saved'))
      props.onSaved()
      props.onOpenChange(false)
    },
    onError: (error) => {
      toast.error(qyOpsErrorMessage(error, t))
    },
  })

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent side='right' className='sm:max-w-2xl'>
        <SheetHeader>
          <SheetTitle className='pr-8'>
            {props.rule == null
              ? t('qy_vio_rule_create')
              : t('qy_vio_rule_edit')}
          </SheetTitle>
          <SheetDescription>{t('qy_vio_rule_form_desc')}</SheetDescription>
        </SheetHeader>

        <Form {...form}>
          <form
            id='qy-violation-rule-form'
            className='min-h-0 flex-1 space-y-4 overflow-y-auto px-4'
            onSubmit={form.handleSubmit((values) =>
              saveMutation.mutate(values)
            )}
          >
            <div className='grid gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='name'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_name')}</FormLabel>
                    <FormControl>
                      <Input {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_name_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='public_reason'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_public_reason')}</FormLabel>
                    <FormControl>
                      <Input {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_public_reason_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <div className='grid gap-3 sm:grid-cols-3'>
              <FormField
                control={form.control}
                name='phase'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_phase')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        {QY_VIOLATION_PHASES.map((phase) => (
                          <SelectItem key={phase} value={phase}>
                            {t(`qy_vio_phase_${phase}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='match_type'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_match_type')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        {QY_VIOLATION_MATCH_TYPES.map((matchType) => (
                          <SelectItem key={matchType} value={matchType}>
                            {t(`qy_vio_match_${matchType}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='priority'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_priority')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        value={field.value}
                        onChange={(event) =>
                          field.onChange(Number(event.target.value))
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_priority_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <FormField
              control={form.control}
              name='pattern'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>
                    {isRate
                      ? t('qy_vio_field_rate_threshold')
                      : t('qy_vio_field_pattern')}
                  </FormLabel>
                  <FormControl>
                    {isRate ? (
                      <Input inputMode='numeric' placeholder='60' {...field} />
                    ) : (
                      <Textarea rows={5} className='font-mono' {...field} />
                    )}
                  </FormControl>
                  <FormDescription>
                    {isRate
                      ? t('qy_vio_field_rate_threshold_desc')
                      : t('qy_vio_field_pattern_desc')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            {/* 频率判据的三条局限必须写在管理员配置它的那一刻，而不是文档里。
                运营看不到它们就会把这条规则当成一堵墙，而它只是一道减速带。 */}
            {isRate && (
              <div className='border-warning/40 bg-warning/5 space-y-1 rounded-lg border p-3 text-xs'>
                <p className='font-medium'>{t('qy_vio_rate_caveat_title')}</p>
                <p>{t('qy_vio_rate_caveat_stream')}</p>
                <p>{t('qy_vio_rate_caveat_false_positive')}</p>
                <p>{t('qy_vio_rate_caveat_nodes')}</p>
                <p>{t('qy_vio_rate_caveat_ladder')}</p>
              </div>
            )}

            <div className='grid gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='model_scope'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_model_scope')}</FormLabel>
                    <FormControl>
                      <Input placeholder='gpt-4*,claude-*' {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_model_scope_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='group_scope'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_group_scope')}</FormLabel>
                    <FormControl>
                      <Input placeholder='default,vip' {...field} />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_group_scope_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            {/* 「指定分组开启」与「豁免分组」是同一份名单的两个方向。
                刻意不开第二列黑名单：两张能互相矛盾的名单必然漂移，
                而「哪张说了算」没有任何取值组合能自解释。 */}
            <FormField
              control={form.control}
              name='group_scope_mode'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_vio_field_group_scope_mode')}</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl>
                      <SelectTrigger className='w-full sm:w-64'>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      {QY_VIOLATION_GROUP_SCOPE_MODES.map((mode) => (
                        <SelectItem key={mode} value={mode}>
                          {t(`qy_vio_group_scope_mode_${mode}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FormDescription>
                    {t('qy_vio_field_group_scope_mode_desc')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div className='grid gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='action'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_action')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        {QY_VIOLATION_ACTIONS.map((action) => (
                          <SelectItem key={action} value={action}>
                            {t(`qy_vio_action_${action}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='fee_mode'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_fee_mode')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        {QY_VIOLATION_FEE_MODES.map((mode) => (
                          <SelectItem key={mode} value={mode}>
                            {t(`qy_vio_fee_${mode}`)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <div className='grid gap-3 sm:grid-cols-3'>
              <FormField
                control={form.control}
                name='fee_fixed'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_fee_fixed')}</FormLabel>
                    <FormControl>
                      {/* 金额走字符串：JSON number 往返一次 0.1 会变成
                          0.10000000000000001，而它会被直接乘进用户账单。 */}
                      <Input inputMode='decimal' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='fee_multiple'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_fee_multiple')}</FormLabel>
                    <FormControl>
                      <Input inputMode='decimal' {...field} />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='fee_max_quota'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_fee_max_quota')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        value={field.value}
                        onChange={(event) =>
                          field.onChange(Number(event.target.value))
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_fee_max_quota_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <div className='grid gap-3 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='count_weight'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_count_weight')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        value={field.value}
                        onChange={(event) =>
                          field.onChange(Number(event.target.value))
                        }
                      />
                    </FormControl>
                    <FormDescription>
                      {t('qy_vio_field_count_weight_desc')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
              <FormField
                control={form.control}
                name='severity'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('qy_vio_field_severity')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        value={field.value}
                        onChange={(event) =>
                          field.onChange(Number(event.target.value))
                        }
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <FormField
              control={form.control}
              name='block_message'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_vio_field_block_message')}</FormLabel>
                  <FormControl>
                    <Input {...field} />
                  </FormControl>
                  <FormDescription>
                    {t('qy_vio_field_block_message_desc')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />

            <FormField
              control={form.control}
              name='remark'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_common_remark')}</FormLabel>
                  <FormControl>
                    <Textarea rows={2} {...field} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div className='space-y-2 rounded-lg border p-3'>
              <QyRuleSwitchField
                control={form.control}
                name='enabled'
                label={t('qy_vio_field_enabled')}
                description={t('qy_vio_field_enabled_desc')}
              />
              <QyRuleSwitchField
                control={form.control}
                name='dry_run'
                label={t('qy_vio_field_dry_run')}
                description={t('qy_vio_field_dry_run_desc')}
              />
              <QyRuleSwitchField
                control={form.control}
                name='case_sensitive'
                label={t('qy_vio_field_case_sensitive')}
              />
              <QyRuleSwitchField
                control={form.control}
                name='archive_context'
                label={t('qy_vio_field_archive_context')}
                description={t('qy_vio_field_archive_context_desc')}
              />
            </div>

            <QyRuleTester getValues={form.getValues} isRate={isRate} />
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
              form='qy-violation-rule-form'
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

/** 开关行。四个布尔字段的排版完全一致，抽出来避免四份重复的 JSX。 */
function QyRuleSwitchField(props: {
  control: Control<QyViolationRuleFormValues>
  name: 'archive_context' | 'case_sensitive' | 'dry_run' | 'enabled'
  label: string
  description?: string
}) {
  return (
    <FormField
      control={props.control}
      name={props.name}
      render={({ field }) => (
        <FormItem className='flex items-start justify-between gap-4'>
          <div className='min-w-0'>
            <FormLabel>{props.label}</FormLabel>
            {props.description != null && (
              <FormDescription>{props.description}</FormDescription>
            )}
          </div>
          <FormControl>
            <Switch checked={field.value} onCheckedChange={field.onChange} />
          </FormControl>
        </FormItem>
      )}
    />
  )
}

/**
 * 规则试跑面板。
 *
 * 用 `getValues()` 而不是订阅表单：试跑是「点一下才发生」的动作，
 * 订阅会让每敲一个字符都重渲染整块面板。
 */
function QyRuleTester(props: {
  getValues: UseFormGetValues<QyViolationRuleFormValues>
  /** 频率规则不看样本文本，看的是「假设这一分钟已经发了多少条」。 */
  isRate: boolean
}) {
  const { t } = useTranslation()
  const [sample, setSample] = useState('')
  const [model, setModel] = useState('')
  const [group, setGroup] = useState('')
  const [rateCount, setRateCount] = useState('')
  const [result, setResult] = useState<QyViolationRuleTestResult | null>(null)

  const testMutation = useMutation({
    mutationFn: () =>
      testQyViolationRule({
        rule: qyViolationRuleToPayload(props.getValues()),
        sample_text: sample,
        model,
        group,
        rate_count: Number(rateCount.trim()) || 0,
      }),
    onSuccess: setResult,
    onError: (error) => {
      setResult(null)
      toast.error(qyOpsErrorMessage(error, t))
    },
  })

  // 少了这一步，频率规则在这里永远显示「未命中」—— 一个看起来权威、
  // 实则只是没有输入的结论，比不给试跑更容易让人放心上线。
  const canRun = props.isRate ? rateCount.trim() !== '' : sample.trim() !== ''

  return (
    <div className='space-y-2 rounded-lg border p-3'>
      <div>
        <h3 className='text-sm font-medium'>{t('qy_vio_test_title')}</h3>
        <p className='text-muted-foreground text-xs'>
          {props.isRate ? t('qy_vio_test_rate_desc') : t('qy_vio_test_desc')}
        </p>
      </div>
      {!props.isRate && (
        <Textarea
          rows={3}
          value={sample}
          onChange={(event) => setSample(event.target.value)}
          placeholder={t('qy_vio_test_sample_placeholder')}
        />
      )}
      <div className='flex flex-wrap gap-2'>
        {props.isRate && (
          <Input
            className='w-40'
            inputMode='numeric'
            value={rateCount}
            onChange={(event) => setRateCount(event.target.value)}
            placeholder={t('qy_vio_test_rate_count')}
          />
        )}
        <Input
          className='w-40'
          value={model}
          onChange={(event) => setModel(event.target.value)}
          placeholder={t('qy_vio_test_model')}
        />
        <Input
          className='w-40'
          value={group}
          onChange={(event) => setGroup(event.target.value)}
          placeholder={t('qy_vio_test_group')}
        />
        <Button
          type='button'
          variant='secondary'
          disabled={testMutation.isPending || !canRun}
          onClick={() => testMutation.mutate()}
        >
          {t('qy_vio_test_run')}
        </Button>
      </div>

      {result != null && (
        <div className='bg-muted/40 space-y-1 rounded-md p-2 text-xs'>
          <p>
            {result.matched
              ? t('qy_vio_test_matched')
              : t('qy_vio_test_not_matched')}
          </p>
          {!result.scope_ok && (
            <p className='text-warning'>{t('qy_vio_test_out_of_scope')}</p>
          )}
          {result.terms.length > 0 && (
            <p className='break-all'>
              {t('qy_vio_test_terms', { terms: result.terms.join(', ') })}
            </p>
          )}
          {result.snippet !== '' && (
            <p className='break-all'>
              {t('qy_vio_test_snippet', { snippet: result.snippet })}
            </p>
          )}
          {result.elapsed_us != null && (
            <p>
              {t('qy_vio_test_elapsed', {
                elapsed: formatQyMicros(result.elapsed_us),
              })}
            </p>
          )}
        </div>
      )}
    </div>
  )
}
