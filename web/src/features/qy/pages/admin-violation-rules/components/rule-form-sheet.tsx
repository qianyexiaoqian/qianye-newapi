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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useForm, type Control, type UseFormGetValues } from 'react-hook-form'
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
import {
  qyGroupOptionLabel,
  qyGroupOptionsQuery,
  qyNormalizeGroupName,
  qyUnknownGroupNames,
} from '../../../lib/group-options'
import { qyAdminViolationCategoriesQuery } from '../../admin-violation-categories/api'
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
  QY_VIOLATION_MODES,
  QY_VIOLATION_PHASES,
  qyAppendViolationGroupScope,
  qyEmptyViolationRule,
  qySplitViolationGroupScope,
  qyViolationRuleSchema,
  qyViolationRuleToForm,
  qyViolationRuleToPayload,
  type QyViolationRuleFormValues,
} from '../lib/rule-form'
import { qyRuleTestContentInputs, qyRuleTestInputs } from '../lib/rule-test'
import type {
  QyViolationMatchType,
  QyViolationPhase,
  QyViolationRule,
  QyViolationRuleTestResult,
  QyViolationTestInput,
} from '../types'

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
  // 生效阶段决定状态码作用域这一格显不显示：prompt 阶段没有上游响应，
  // 摆一个填了也永不生效的格子只会让人以为自己配对了。
  const phase = form.watch('phase')

  /**
   * 分组作用域的候选清单。
   *
   * 只在抽屉打开时拉：这份数据只有表单用得上，跟着列表页一起拉会让每次翻页都
   * 多一个请求。它**永远只是输入辅助** —— 拉不到照样能手输、照样能保存，
   * 三种非正常状态（加载中 / 拉取失败 / 站点没有分组）各自有独立的提示。
   */
  const groupQuery = useQuery({
    ...qyGroupOptionsQuery(),
    enabled: props.open,
  })
  // 违规类型清单。只在抽屉打开时拉：它是一张行数个位数的表，但没必要在
  // 规则列表页每次渲染都取一次。
  const categoryQuery = useQuery({
    ...qyAdminViolationCategoriesQuery(),
    enabled: props.open,
  })
  const groupOptions = groupQuery.data?.options ?? []
  const groupScopeEntries = qySplitViolationGroupScope(
    form.watch('group_scope')
  )
  // 清单为空（拉取失败，或者站点真的一个分组都没定义）时一律不算未定义分组：
  // 那会把运营填的每一个名字都标成黄的，是一片假警报 —— 而假警报比没有警报
  // 更糟，报错一次没人信，之后真的打错字也不会有人看。
  const unknownGroups =
    groupOptions.length === 0
      ? []
      : qyUnknownGroupNames(
          [...new Set(groupScopeEntries.map(qyNormalizeGroupName))],
          groupOptions
        )
  // 文本框是自由编辑的，名单里完全可能出现重复项（后端保存时才折叠去重），
  // 所以徽章的 key 必须带序号 —— 在这里一次性算好，JSX 里只剩渲染。
  const groupScopeBadges = groupScopeEntries.map((name, index) => ({
    key: `${name}#${index}`,
    name,
    unknown: unknownGroups.includes(qyNormalizeGroupName(name)),
  }))

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
    <Form {...form}>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={
          props.rule == null ? t('qy_vio_rule_create') : t('qy_vio_rule_edit')
        }
        description={t('qy_vio_rule_form_desc')}
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
              form='qy-violation-rule-form'
              disabled={saveMutation.isPending}
            >
              {t('qy_common_submit')}
            </Button>
          </>
        }
      >
        <form
          id='qy-violation-rule-form'
          className='space-y-4'
          onSubmit={form.handleSubmit((values) => saveMutation.mutate(values))}
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

          {/* 违规类型。它决定这条规则的命中累加到**哪一个计数桶** ——
              同一类型下多条规则的命中会加到一起，而每个类型有自己的次数阈值。
              不选 = 落到「未分类」兜底类型（阈值 0，即不单独触发处置）。 */}
          <FormField
            control={form.control}
            name='category_id'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_vio_field_category')}</FormLabel>
                <FormControl>
                  <Select
                    value={String(field.value ?? 0)}
                    onValueChange={(value) =>
                      field.onChange(Number(value) || 0)
                    }
                  >
                    <SelectTrigger>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value='0'>
                        {t('qy_vio_field_category_unset')}
                      </SelectItem>
                      {(categoryQuery.data?.items ?? []).map((row) => (
                        <SelectItem
                          key={row.category.id}
                          value={String(row.category.id)}
                        >
                          {row.category.name}
                          {row.category.enabled && row.category.threshold > 0
                            ? ` · ${t('qy_vcat_threshold_value', {
                                count: row.category.threshold,
                                hours: row.category.window_hours,
                              })}`
                            : ` · ${t('qy_vcat_threshold_off')}`}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </FormControl>
                <FormDescription>
                  {t('qy_vio_field_category_desc')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

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

          {/* 状态码作用域。
                它是「status_code + 正文」写成一条规则的全部机制:正文由匹配方式判、
                状态码由这里判,两者是 AND。放在模型/分组作用域旁边而不是塞进
                pattern 里,是因为它就是第三道作用域闸,心智模型完全一致。
                prompt 阶段没有上游响应,所以那一档下整个字段隐藏 —— 摆一个
                填了也永不生效的格子,只会让人以为自己配对了。 */}
          {phase !== 'prompt' && (
            <FormField
              control={form.control}
              name='status_scope'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_vio_field_status_scope')}</FormLabel>
                  <FormControl>
                    <Input placeholder='400 或 400,403 或 400-499' {...field} />
                  </FormControl>
                  <FormDescription>
                    {t('qy_vio_field_status_scope_desc')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}

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

          {/* 分组作用域。
                原来这里是一个裸文本框：打错一个字母，这条规则就静默挂在一个
                不存在的分组上 —— 保存成功、界面正常、线上永不命中，而且没有
                任何信号。换成「带元数据的下拉 + 保留自由输入 + 未定义分组软
                告警」，口径与划转分组规则页完全一致。 */}
          <FormField
            control={form.control}
            name='group_scope'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('qy_vio_field_group_scope')}</FormLabel>
                {/* 选一个就追加一项。下拉解决「站点有哪些分组、它们什么样」，
                      下面的文本框解决「站点已经不认的历史分组仍要能配」——
                      后者恰恰是最需要被规则覆盖的那批账号。 */}
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
                    field.onChange(
                      qyAppendViolationGroupScope(field.value, picked)
                    )
                  }
                  emptyText='qy_trg_group_picker_empty'
                  placeholder={t('qy_vio_group_scope_pick')}
                />
                <FormControl>
                  <Input placeholder='default,vip' {...field} />
                </FormControl>

                {groupScopeBadges.length > 0 && (
                  <div className='flex flex-wrap gap-1'>
                    {groupScopeBadges.map((badge) => (
                      <Badge
                        key={badge.key}
                        variant={badge.unknown ? 'warning' : 'secondary'}
                        className='font-normal'
                        title={
                          badge.unknown
                            ? t('qy_vio_group_scope_unknown_hint')
                            : undefined
                        }
                      >
                        {badge.name}
                        {/* 只靠颜色区分「站点定义过 / 没定义过」，色觉障碍用户
                              拿到的是一串一模一样的名字。 */}
                        {badge.unknown && (
                          <span className='sr-only'>
                            {' '}
                            {t('qy_vio_group_scope_unknown_hint')}
                          </span>
                        )}
                      </Badge>
                    ))}
                  </div>
                )}

                {/* 清单的三种非正常状态各自说清楚。任何一种都**不**禁用上面的
                      文本框：把人卡在一个拉不到的下拉前面，等于让他配不了规则。 */}
                {groupQuery.isPending && (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_vio_group_scope_loading')}
                  </p>
                )}
                {groupQuery.isError && (
                  <p className='text-warning text-xs'>
                    {t('qy_vio_group_scope_failed')}
                  </p>
                )}
                {groupQuery.isSuccess && groupOptions.length === 0 && (
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_vio_group_scope_empty')}
                  </p>
                )}

                {/* 软告警，不是错误：不禁用提交，也不进 zod schema。 */}
                {unknownGroups.length > 0 && (
                  <p className='text-warning text-xs'>
                    {t('qy_vio_group_scope_unknown', {
                      groups: unknownGroups.join('、'),
                    })}
                  </p>
                )}

                <FormDescription>
                  {t('qy_vio_field_group_scope_desc')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

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

          {/* 置信度下限只对 ai_review 有意义,所以只在选了它时出现。
              常显会让人以为每条正则也有"把握程度",而它一个字节都不生效。
              它是 AI 审核唯一的误判闸:模型判"违规"但只有 0.3 的把握时,
              照着扣费封号是不可接受的,而没有这道闸唯一的调节手段就是
              把整条规则切回影子 —— 那等于关掉它。 */}
          {matchType === 'ai_review' && (
            <FormField
              control={form.control}
              name='ai_min_confidence'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('qy_vio_field_ai_min_confidence')}</FormLabel>
                  <FormControl>
                    <Input {...field} inputMode='decimal' />
                  </FormControl>
                  <FormDescription>
                    {t('qy_vio_field_ai_min_confidence_desc')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}

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

          {/* 模式是这一页最重的一个字段：它单独决定这条规则要不要真的扣钱、封号。
              刻意做成下拉单选而不是开关 —— 「影子 / 真实」是二选一，
              而开关的心智是「要不要打开某个东西」，后者会让人看着一个关着的开关
              以为规则没生效。全局模式开关已删除，这里就是唯一的入口。 */}
          <FormField
            control={form.control}
            name='mode'
            render={({ field }) => (
              <FormItem className='rounded-lg border p-3'>
                <FormLabel>{t('qy_vio_field_mode')}</FormLabel>
                <Select value={field.value} onValueChange={field.onChange}>
                  <FormControl>
                    <SelectTrigger className='w-full sm:w-72'>
                      <SelectValue />
                    </SelectTrigger>
                  </FormControl>
                  <SelectContent>
                    {QY_VIOLATION_MODES.map((mode) => (
                      <SelectItem key={mode} value={mode}>
                        {t(`qy_vio_mode_${mode}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <FormDescription>
                  {field.value === 'enforce'
                    ? t('qy_vio_field_mode_enforce_desc')
                    : t('qy_vio_field_mode_shadow_desc')}
                </FormDescription>
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

          <QyRuleTester
            getValues={form.getValues}
            phase={phase}
            matchType={matchType}
          />
        </form>
      </QyResponsiveDialog>
    </Form>
  )
}

/** 开关行。三个布尔字段的排版完全一致，抽出来避免三份重复的 JSX。 */
function QyRuleSwitchField(props: {
  control: Control<QyViolationRuleFormValues>
  name: 'archive_context' | 'case_sensitive' | 'enabled'
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
 *
 * # 它问什么，由规则说了算
 *
 * 这块面板曾经不论规则是什么，都固定问「样本文本 + 模型 + 分组」。那三格对
 * 大多数规则都是错的问题：一条错误码规则要的是错误码，一条上游规则要的是
 * 上游正文与状态码，一条频率规则一段文本都不看。而模型和分组根本不参与
 * 内容匹配，它们只决定「这条规则在不在作用域内」—— 把它们摆在最显眼的位置，
 * 等于把试跑引向一个它回答不了的问题。
 *
 * 现在字段由 `qyRuleTestInputs(phase, match_type)` 决定，跑完之后改用后端回的
 * `inputs`（后端说了算，两边不一致时以线上判据为准）。
 */
function QyRuleTester(props: {
  getValues: UseFormGetValues<QyViolationRuleFormValues>
  phase: QyViolationPhase
  matchType: QyViolationMatchType
}) {
  const { t } = useTranslation()
  const [requestText, setRequestText] = useState('')
  const [upstreamText, setUpstreamText] = useState('')
  const [rejectReason, setRejectReason] = useState('')
  const [statusCode, setStatusCode] = useState('')
  const [errorCode, setErrorCode] = useState('')
  const [rateCount, setRateCount] = useState('')
  const [aiVerdict, setAiVerdict] = useState('')
  const [aiCategory, setAiCategory] = useState('')
  const [aiConfidence, setAiConfidence] = useState('')
  const [model, setModel] = useState('')
  const [group, setGroup] = useState('')
  const [result, setResult] = useState<QyViolationRuleTestResult | null>(null)

  const inputs = qyRuleTestInputs(props.phase, props.matchType)
  const asks = (id: QyViolationTestInput) => inputs.includes(id)

  const testMutation = useMutation({
    mutationFn: () =>
      testQyViolationRule({
        rule: qyViolationRuleToPayload(props.getValues()),
        // 只发这条规则会读的字段，其余一律发空：一个上游样本被同时灌进
        // 请求上下文，会让 prompt 规则凭空「命中」—— 一个捏造出来的绿灯。
        request_text: asks('request_text') ? requestText : '',
        upstream_text: asks('upstream_text') ? upstreamText : '',
        reject_reason: asks('reject_reason') ? rejectReason : '',
        status_code: asks('status_code') ? Number(statusCode.trim()) || 0 : 0,
        error_code: asks('error_code') ? errorCode.trim() : '',
        rate_count: asks('rate_count') ? Number(rateCount.trim()) || 0 : 0,
        ai_verdict: asks('ai_verdict') ? aiVerdict : '',
        ai_category: asks('ai_category') ? aiCategory.trim() : '',
        ai_confidence: asks('ai_confidence') ? aiConfidence.trim() : '',
        model,
        group,
      }),
    onSuccess: setResult,
    onError: (error) => {
      setResult(null)
      toast.error(qyOpsErrorMessage(error, t))
    },
  })

  // 「能不能跑」只看内容输入：模型与分组留空的语义是「不限」，
  // 拿它们当必填会把一次本来跑得动的试跑锁死。
  const filled: Record<QyViolationTestInput, string> = {
    request_text: requestText,
    upstream_text: upstreamText,
    reject_reason: rejectReason,
    status_code: statusCode,
    error_code: errorCode,
    rate_count: rateCount,
    ai_verdict: aiVerdict,
    ai_category: aiCategory,
    ai_confidence: aiConfidence,
    model,
    group,
  }
  const canRun = qyRuleTestContentInputs(inputs).some(
    (id) => filled[id].trim() !== ''
  )

  return (
    <div className='space-y-3 rounded-lg border p-3'>
      <div>
        <h3 className='text-sm font-medium'>{t('qy_vio_test_title')}</h3>
        <p className='text-muted-foreground text-xs'>
          {props.matchType === 'request_rate'
            ? t('qy_vio_test_rate_desc')
            : t('qy_vio_test_desc')}
        </p>
      </div>

      {asks('request_text') && (
        <QyTestField label={t('qy_vio_test_input_request_text')}>
          <Textarea
            rows={3}
            value={requestText}
            onChange={(event) => setRequestText(event.target.value)}
            placeholder={t('qy_vio_test_input_request_text_ph')}
          />
        </QyTestField>
      )}
      {asks('upstream_text') && (
        <QyTestField label={t('qy_vio_test_input_upstream_text')}>
          <Textarea
            rows={3}
            value={upstreamText}
            onChange={(event) => setUpstreamText(event.target.value)}
            placeholder={t('qy_vio_test_input_upstream_text_ph')}
          />
        </QyTestField>
      )}
      {asks('reject_reason') && (
        <QyTestField
          label={t('qy_vio_test_input_reject_reason')}
          description={t('qy_vio_test_input_reject_reason_desc')}
        >
          <Input
            value={rejectReason}
            onChange={(event) => setRejectReason(event.target.value)}
            placeholder={t('qy_vio_test_input_reject_reason_ph')}
          />
        </QyTestField>
      )}
      {asks('ai_verdict') && (
        <QyTestField
          label={t('qy_vio_test_input_ai_verdict')}
          description={t('qy_vio_test_input_ai_verdict_desc')}
        >
          <Select
            value={aiVerdict}
            onValueChange={(value) => setAiVerdict(value ?? '')}
          >
            <SelectTrigger>
              <SelectValue placeholder={t('qy_vio_test_ai_verdict_none')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='violation'>
                {t('qy_vio_test_ai_verdict_violation')}
              </SelectItem>
              <SelectItem value='clean'>
                {t('qy_vio_test_ai_verdict_clean')}
              </SelectItem>
            </SelectContent>
          </Select>
        </QyTestField>
      )}
      <div className='flex flex-wrap gap-2'>
        {asks('ai_category') && (
          <Input
            className='w-40'
            value={aiCategory}
            onChange={(event) => setAiCategory(event.target.value)}
            placeholder={t('qy_vio_test_input_ai_category')}
          />
        )}
        {asks('ai_confidence') && (
          <Input
            className='w-40'
            inputMode='decimal'
            value={aiConfidence}
            onChange={(event) => setAiConfidence(event.target.value)}
            placeholder={t('qy_vio_test_input_ai_confidence')}
          />
        )}
        {asks('status_code') && (
          <Input
            className='w-40'
            inputMode='numeric'
            value={statusCode}
            onChange={(event) => setStatusCode(event.target.value)}
            placeholder={t('qy_vio_test_input_status_code')}
          />
        )}
        {asks('error_code') && (
          <Input
            className='w-56'
            value={errorCode}
            onChange={(event) => setErrorCode(event.target.value)}
            placeholder={t('qy_vio_test_input_error_code')}
          />
        )}
        {asks('rate_count') && (
          <Input
            className='w-40'
            inputMode='numeric'
            value={rateCount}
            onChange={(event) => setRateCount(event.target.value)}
            placeholder={t('qy_vio_test_input_rate_count')}
          />
        )}
      </div>

      <div className='space-y-2 border-t pt-2'>
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_test_scope_desc')}
        </p>
        <div className='flex flex-wrap gap-2'>
          <Input
            className='w-40'
            value={model}
            onChange={(event) => setModel(event.target.value)}
            placeholder={t('qy_vio_test_model_optional')}
          />
          <Input
            className='w-40'
            value={group}
            onChange={(event) => setGroup(event.target.value)}
            placeholder={t('qy_vio_test_group_optional')}
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
      </div>

      {result != null && <QyRuleTestResult result={result} />}
    </div>
  )
}

/** 试跑输入的一格。标签 + 可选说明 + 控件，五个格子共用同一份排版。 */
function QyTestField(props: {
  label: string
  description?: string
  children: React.ReactNode
}) {
  return (
    <div className='space-y-1'>
      <p className='text-xs font-medium'>{props.label}</p>
      {props.description != null && (
        <p className='text-muted-foreground text-xs'>{props.description}</p>
      )}
      {props.children}
    </div>
  )
}

/**
 * 试跑结论。**三态必须彼此可分**，这是整块面板的关键。
 *
 * 只回一个「命中 / 未命中」时，「规则压根不作用于你填的模型/分组/状态码」和
 * 「规则确实作用于它、但模式串没匹配上」长得一模一样 —— 管理员分不清该改
 * 作用域还是该改模式串，只能两边乱改。所以这里先说落在哪一态，
 * 不在作用域时还要点名是哪一道闸，未命中时还要点名哪几格没填。
 */
function QyRuleTestResult(props: { result: QyViolationRuleTestResult }) {
  const { t } = useTranslation()
  const { result } = props
  const tone =
    result.outcome === 'matched'
      ? 'text-success'
      : result.outcome === 'no_match'
        ? 'text-muted-foreground'
        : 'text-warning'

  return (
    <div className='bg-muted/40 space-y-1 rounded-md p-2 text-xs'>
      <p className={tone}>{t(`qy_vio_test_outcome_${result.outcome}`)}</p>
      {result.outcome === 'out_of_scope' && result.scope_fail !== '' && (
        <p className='text-warning'>
          {t(`qy_vio_test_scope_fail_${result.scope_fail}`)}
        </p>
      )}
      {result.outcome === 'no_match' && result.blank_inputs.length > 0 && (
        <p className='text-warning'>
          {t('qy_vio_test_blank_inputs', {
            fields: result.blank_inputs
              .map((id) => t(`qy_vio_test_input_${id}`))
              .join('、'),
          })}
        </p>
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
  )
}
