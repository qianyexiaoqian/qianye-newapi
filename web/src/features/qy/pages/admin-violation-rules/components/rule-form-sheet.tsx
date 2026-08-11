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
import { useEffect } from 'react'
import { useForm, type Control } from 'react-hook-form'
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
import { createQyViolationRule, updateQyViolationRule } from '../api'
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
import type { QyViolationRule } from '../types'
import { QyRuleTester } from './rule-tester'

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
              不选 = 落到「未分类」兜底类型。

              这一格上一轮做出来了，项目方还是回了「怎么没有绑定违规类型的
              选项」。三处原因，都在这里修：
                1. 它是整张表单上**唯一**一个触发器没带 `w-full` 的下拉
                   （其余七个都有）。共享组件的默认宽度是 `w-fit`，于是它在
                   一整行的空白里缩成一枚一百来像素的小药丸 —— 视觉上不像
                   一个字段，像一枚徽标。
                2. `<FormControl>` 包的是 `<Select>` 而不是 `<SelectTrigger>`，
                   而 `Select` 根本不渲染 DOM 节点：`id` / `aria-describedby`
                   全部落空，点标签「违规类型」四个字没有任何反应。
                3. 下拉里同一个桶出现了两次 —— 合成的 `0`「未分类(兜底)」，
                   以及清单里真实的那一行兜底类型。保存时后端
                   `resolveRuleCategory` 把 0 改写成兜底类型的真实 id，
                   于是存之前显示前者、重新打开显示后者，同一条规则的同一格
                   会**自己变一次**。这里统一按兜底那一行显示。 */}
          <FormField
            control={form.control}
            name='category_id'
            render={({ field }) => {
              const rows = categoryQuery.data?.items ?? []
              const fallbackId = categoryQuery.data?.fallback_id ?? 0
              // 0 = 没显式选。后端保存时会把它落到兜底类型，所以界面此刻就按
              // 兜底那一行显示，让「存之前」与「存之后」是同一句话。
              const effectiveId = field.value > 0 ? field.value : fallbackId
              const selected = rows.find(
                (row) => row.category.id === effectiveId
              )
              return (
                <FormItem>
                  <FormLabel>{t('qy_vio_field_category')}</FormLabel>
                  <Select
                    value={String(effectiveId)}
                    onValueChange={(value) =>
                      field.onChange(Number(value) || 0)
                    }
                  >
                    <FormControl>
                      <SelectTrigger className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                    </FormControl>
                    <SelectContent>
                      {/* 清单还没到（加载中 / 拉取失败）时给一格占位。少了它，
                          Base UI 查不到译名就把取值原样渲染成一个裸数字。 */}
                      {rows.length === 0 && (
                        <SelectItem value={String(effectiveId)}>
                          {t('qy_vio_field_category_unset')}
                        </SelectItem>
                      )}
                      {/* 规则指向的类型已归档、已不在清单里时同理：宁可写
                          「已归档 #12」也不要漏一个孤零零的 12 出去。 */}
                      {rows.length > 0 && selected == null && (
                        <SelectItem value={String(effectiveId)}>
                          {t('qy_vio_field_category_missing', {
                            id: effectiveId,
                          })}
                        </SelectItem>
                      )}
                      {rows.map((row) => (
                        <SelectItem
                          key={row.category.id}
                          value={String(row.category.id)}
                        >
                          {row.category.name}
                          {row.category.is_fallback
                            ? ` · ${t('qy_vcat_flag_fallback')}`
                            : ''}
                          {row.threshold_state === 'active'
                            ? ` · ${t('qy_vcat_threshold_value', {
                                count: row.category.threshold,
                                hours: row.category.window_hours,
                              })}`
                            : ` · ${t('qy_vcat_threshold_off')}`}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {/* 项目方问的原话是「这个规则触发了，计次了，应该加到哪里？」。
                      答案不能只躺在下方小字里 —— 上一轮就是这么放的，他没读到。
                      这一条按当前选择实时改写，并且在「记了也不会触发任何处置」
                      的那一档换成告警色：一条规则默认落到一个阈值为 0 的桶，
                      是运营最容易误以为「配了就会封」的地方。 */}
                  <p
                    className={
                      selected != null && selected.threshold_state === 'active'
                        ? 'text-muted-foreground rounded-md border border-dashed px-3 py-2 text-xs'
                        : 'text-warning border-warning/40 bg-warning/5 rounded-md border px-3 py-2 text-xs'
                    }
                  >
                    {selected == null
                      ? t('qy_vio_field_category_dest_unknown')
                      : selected.threshold_state === 'active'
                        ? t('qy_vio_field_category_dest_active', {
                            name: selected.category.name,
                            count: selected.category.threshold,
                            hours: selected.category.window_hours,
                          })
                        : t(
                            selected.category.is_fallback
                              ? 'qy_vio_field_category_dest_fallback_idle'
                              : 'qy_vio_field_category_dest_idle',
                            { name: selected.category.name }
                          )}
                  </p>
                  <FormDescription>
                    {t('qy_vio_field_category_desc')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )
            }}
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

          {/* 试跑面板里那两个判据选择器写回的就是这里的同一个表单字段 ——
              不是一份副本。所以「在试跑区就地改阶段」与「回到上面改」完全等价，
              改完保存的也是同一份规则。 */}
          <QyRuleTester
            getValues={form.getValues}
            phase={phase}
            matchType={matchType}
            onPhaseChange={(value) =>
              form.setValue('phase', value, {
                shouldDirty: true,
                shouldValidate: true,
              })
            }
            onMatchTypeChange={(value) =>
              form.setValue('match_type', value, {
                shouldDirty: true,
                shouldValidate: true,
              })
            }
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
