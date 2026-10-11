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
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Info, Pencil, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import {
  qyDeleteCommissionGroupRate,
  qyUpsertCommissionGroupRate,
} from '../api'
import {
  qyIsValidNullablePercent,
  qyIsValidPercent,
  qyNormalizePercent,
} from '../lib/fields'
import type { QyCommissionAdminConfig, QyCommissionGroupRate } from '../types'

/** 分组规则上的三档套餐比例键（与后端 groupRateView 的字段名逐字一致）。 */
type QyPlanRateKey =
  | 'plan_order_rate_percent'
  | 'plan_balance_rate_percent'
  | 'plan_redemption_rate_percent'

const PLAN_RATE_FIELDS: readonly { key: QyPlanRateKey; labelKey: string }[] = [
  { key: 'plan_order_rate_percent', labelKey: 'qy_cm_f_plan_order_rate' },
  { key: 'plan_balance_rate_percent', labelKey: 'qy_cm_f_plan_balance_rate' },
  {
    key: 'plan_redemption_rate_percent',
    labelKey: 'qy_cm_f_plan_redemption_rate',
  },
]

/** 分组费率编辑表单的草稿。空的 groupName 表示"新增"。 */
type QyGroupRateDraft = {
  groupName: string
  topup: string
  consume: string
  /**
   * 本组是否单独设兑换码档。
   *
   * 用一个**独立的开关**而不是"输入框空着就算不配"：兑换码档的 0% 是合法
   * 配置，而空输入框与"填了 0"在一个只有输入框的界面上长得太像。开关关掉时
   * 提交 `null`，打开时提交 `redemption` 的内容（含 `'0'`）。
   */
  redemptionEnabled: boolean
  redemption: string
  /**
   * 套餐三档。**空串 = 本组不单独配**（提交 `null`，跟随全局套餐档），`'0'` 是显式 0%。
   * 输入框旁边写着留空时实际按几个点，空与 0 不会被看混。
   */
  plan: Record<QyPlanRateKey, string>
  enabled: boolean
  remark: string
  /** 编辑既有规则时锁住分组名：改名等于删一条加一条，两条审计比一条清楚。 */
  locked: boolean
}

const EMPTY_GROUP_DRAFT: QyGroupRateDraft = {
  groupName: '',
  topup: '',
  consume: '',
  redemptionEnabled: false,
  redemption: '',
  plan: {
    plan_order_rate_percent: '',
    plan_balance_rate_percent: '',
    plan_redemption_rate_percent: '',
  },
  enabled: true,
  remark: '',
  locked: false,
}

/**
 * 分组差异化费率表。
 *
 * 表头必须写清口径（按**推广人自己**的分组）与回落规则（没配的分组走全局默认），
 * 否则运营会以为"只有表里这几个分组返佣"。
 *
 * 口径这一句尤其不能含糊：2026-08-18 之前这一档按**下线**分组算，界面上写的
 * 也是那个口径。填表的人如果按旧口径理解，会给"客户档位"配费率，而实际生效的
 * 是"推广人档位" —— 每一行都保存成功、每一笔佣金都自洽，只是全站发错了钱。
 *
 * 分组从下拉里选（后端下发的 `user_groups`：登记的用户分组 ∪ 在用的 users.group），
 * 不再手填：打错一个字的规则一个人都不命中，界面却显示"已配置"。
 */
export function QyCmGroupRatesCard(props: { config: QyCommissionAdminConfig }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const config = props.config

  const [draft, setDraft] = useState<QyGroupRateDraft | null>(null)
  const [pendingDelete, setPendingDelete] =
    useState<QyCommissionGroupRate | null>(null)

  const refresh = async () => {
    await queryClient.invalidateQueries({
      queryKey: qyKeys.adminCommissionConfig(),
    })
  }

  const upsert = useMutation({
    mutationFn: qyUpsertCommissionGroupRate,
    onSuccess: async () => {
      setDraft(null)
      toast.success(t('qy_cm_gr_saved'))
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const remove = useMutation({
    mutationFn: qyDeleteCommissionGroupRate,
    onSuccess: async () => {
      setPendingDelete(null)
      toast.success(t('qy_cm_gr_deleted'))
      await refresh()
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const groupRates = qyArray(config.group_rates)
  // 旧后端不下发 user_groups 时为 null：那时退回手填，不让这张表变得不能用。
  const userGroups =
    config.user_groups == null ? null : qyArray(config.user_groups)
  const configured = new Set(groupRates.map((rule) => rule.group_name))
  const pickable = (userGroups ?? []).filter((name) => !configured.has(name))

  const draftInvalid =
    draft != null &&
    (draft.groupName.trim() === '' ||
      !qyIsValidPercent(draft.topup) ||
      !qyIsValidPercent(draft.consume) ||
      // 开关打开就必须填出一个合法比例；关着的时候输入框里剩什么都不算数。
      (draft.redemptionEnabled && !qyIsValidPercent(draft.redemption)) ||
      PLAN_RATE_FIELDS.some(
        (field) => !qyIsValidNullablePercent(draft.plan[field.key])
      ))

  const editRule = (rule: QyCommissionGroupRate) =>
    setDraft({
      groupName: rule.group_name,
      topup: rule.topup_rate_percent,
      consume: rule.consume_rate_percent,
      redemptionEnabled: rule.redemption_rate_percent != null,
      redemption: rule.redemption_rate_percent ?? '',
      plan: {
        plan_order_rate_percent: rule.plan_order_rate_percent ?? '',
        plan_balance_rate_percent: rule.plan_balance_rate_percent ?? '',
        plan_redemption_rate_percent: rule.plan_redemption_rate_percent ?? '',
      },
      enabled: rule.enabled,
      remark: rule.remark,
      locked: true,
    })

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_cm_gr_title')}</CardTitle>
        <CardDescription>
          {t('qy_cm_gr_desc', {
            topup: config.effective.topup_rate_percent,
            consume: config.effective.consume_rate_percent,
          })}
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <Alert>
          <Info />
          <AlertTitle>{t('qy_cm_gr_scope_title')}</AlertTitle>
          <AlertDescription>{t('qy_cm_gr_scope_desc_xh')}</AlertDescription>
        </Alert>

        <div className='overflow-x-auto'>
          <table className='w-full min-w-[60rem] text-sm'>
            <thead>
              <tr className='text-muted-foreground text-left'>
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_gr_group')}
                </th>
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_f_topup_rate')}
                </th>
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_f_consume_rate')}
                </th>
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_f_redemption_rate')}
                </th>
                {PLAN_RATE_FIELDS.map((field) => (
                  <th key={field.key} className='py-1.5 pe-3 font-medium'>
                    {t(field.labelKey)}
                  </th>
                ))}
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_gr_enabled')}
                </th>
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_gr_remark')}
                </th>
                <th className='py-1.5 font-medium'>{t('qy_common_actions')}</th>
              </tr>
            </thead>
            <tbody className='divide-border divide-y'>
              {groupRates.length === 0 && (
                <tr>
                  <td
                    colSpan={10}
                    className='text-muted-foreground py-3 text-center'
                  >
                    {t('qy_cm_gr_empty')}
                  </td>
                </tr>
              )}
              {groupRates.map((rule) => (
                <tr key={rule.group_name}>
                  <td className='py-2 pe-3 font-mono text-xs'>
                    <div className='space-y-1'>
                      <span className='block'>{rule.group_name}</span>
                      {userGroups != null &&
                        !userGroups.includes(rule.group_name) && (
                          <Badge variant='destructive'>
                            {t('qy_cm_gr_stale')}
                          </Badge>
                        )}
                    </div>
                  </td>
                  <td className='py-2 pe-3 tabular-nums'>
                    {rule.topup_rate_percent}%
                  </td>
                  <td className='py-2 pe-3 tabular-nums'>
                    {rule.consume_rate_percent}%
                  </td>
                  {/* null 与 "0" 必须画成两样东西。用 `!= null` 而不是真值
                      判断：`'0'` 在 JS 里是假值，写成 `rule.x ? … : 跟随`
                      会把一个显式 0% 的分组显示成"跟随充值档"，而那两者
                      在钱上差的正是一整档比例。 */}
                  <td className='py-2 pe-3 tabular-nums'>
                    {rule.redemption_rate_percent != null ? (
                      `${rule.redemption_rate_percent}%`
                    ) : (
                      <span className='text-muted-foreground'>
                        {t('qy_cm_gr_redemption_inherit', {
                          percent: groupEffectiveRedemptionPercent(
                            rule,
                            config
                          ),
                        })}
                      </span>
                    )}
                  </td>
                  {PLAN_RATE_FIELDS.map((field) => (
                    <td key={field.key} className='py-2 pe-3 tabular-nums'>
                      {rule[field.key] != null ? (
                        `${rule[field.key]}%`
                      ) : (
                        <span className='text-muted-foreground'>
                          {t('qy_cm_gr_plan_inherit', {
                            percent: groupInheritedPlanPercent(
                              field.key,
                              rule.topup_rate_percent,
                              config
                            ),
                          })}
                        </span>
                      )}
                    </td>
                  ))}
                  <td className='py-2 pe-3'>
                    {t(rule.enabled ? 'qy_common_on' : 'qy_cm_gr_fallback')}
                  </td>
                  <td className='text-muted-foreground py-2 pe-3'>
                    {rule.remark}
                  </td>
                  <td className='py-2'>
                    <div className='flex gap-1'>
                      <Button
                        variant='ghost'
                        size='sm'
                        aria-label={t('qy_common_edit')}
                        onClick={() => editRule(rule)}
                      >
                        <Pencil aria-hidden='true' />
                      </Button>
                      <Button
                        variant='ghost'
                        size='sm'
                        aria-label={t('qy_common_delete')}
                        onClick={() => setPendingDelete(rule)}
                      >
                        <Trash2 aria-hidden='true' />
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>

        {draft == null ? (
          <Button
            variant='outline'
            size='sm'
            onClick={() =>
              setDraft({
                ...EMPTY_GROUP_DRAFT,
                plan: { ...EMPTY_GROUP_DRAFT.plan },
              })
            }
          >
            <Plus aria-hidden='true' />
            {t('qy_cm_gr_add')}
          </Button>
        ) : (
          <div className='border-border space-y-3 rounded-md border p-3'>
            <div className='grid gap-3 sm:grid-cols-2'>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-cm-gr-group'>{t('qy_cm_gr_group')}</Label>
                {draft.locked || userGroups == null ? (
                  <Input
                    id='qy-cm-gr-group'
                    value={draft.groupName}
                    disabled={draft.locked}
                    aria-invalid={draft.groupName.trim() === ''}
                    onChange={(e) =>
                      setDraft({ ...draft, groupName: e.target.value })
                    }
                  />
                ) : (
                  <NativeSelect
                    id='qy-cm-gr-group'
                    className='w-full'
                    value={draft.groupName}
                    disabled={pickable.length === 0}
                    aria-invalid={draft.groupName === ''}
                    onChange={(e) =>
                      setDraft({ ...draft, groupName: e.target.value })
                    }
                  >
                    <NativeSelectOption value=''>
                      {pickable.length === 0
                        ? t('qy_cm_gr_group_none')
                        : t('qy_cm_gr_group_pick')}
                    </NativeSelectOption>
                    {pickable.map((name) => (
                      <NativeSelectOption key={name} value={name}>
                        {name}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                )}
                <p className='text-muted-foreground text-xs'>
                  {t('qy_cm_gr_group_hint')}
                </p>
              </div>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-cm-gr-remark'>{t('qy_cm_gr_remark')}</Label>
                <Input
                  id='qy-cm-gr-remark'
                  value={draft.remark}
                  onChange={(e) =>
                    setDraft({ ...draft, remark: e.target.value })
                  }
                />
              </div>
              <PercentInput
                id='qy-cm-gr-topup'
                label={t('qy_cm_f_topup_rate')}
                value={draft.topup}
                invalid={!qyIsValidPercent(draft.topup)}
                onChange={(value) => setDraft({ ...draft, topup: value })}
              />
              <PercentInput
                id='qy-cm-gr-consume'
                label={t('qy_cm_f_consume_rate')}
                value={draft.consume}
                invalid={!qyIsValidPercent(draft.consume)}
                onChange={(value) => setDraft({ ...draft, consume: value })}
              />
            </div>
            <div className='space-y-1.5'>
              <label className='flex items-center gap-2 text-sm'>
                <input
                  type='checkbox'
                  checked={draft.redemptionEnabled}
                  onChange={(e) => {
                    // 打开时给一个起始值，免得开关一开就是非法状态；
                    // 起始值取本组的充值档，也就是关掉时的实际口径。
                    const seed =
                      e.target.checked && draft.redemption === ''
                        ? draft.topup
                        : draft.redemption
                    setDraft({
                      ...draft,
                      redemptionEnabled: e.target.checked,
                      redemption: seed,
                    })
                  }}
                />
                {t('qy_cm_gr_redemption_toggle')}
              </label>
              {draft.redemptionEnabled ? (
                <div className='flex items-center gap-2'>
                  <Input
                    id='qy-cm-gr-redemption'
                    inputMode='decimal'
                    aria-label={t('qy_cm_f_redemption_rate')}
                    value={draft.redemption}
                    aria-invalid={!qyIsValidPercent(draft.redemption)}
                    onChange={(e) =>
                      setDraft({ ...draft, redemption: e.target.value })
                    }
                  />
                  <span className='text-muted-foreground text-sm'>%</span>
                </div>
              ) : (
                <p className='text-muted-foreground text-xs'>
                  {t('qy_cm_gr_redemption_off_hint', {
                    percent: qyIsValidPercent(draft.topup)
                      ? qyNormalizePercent(draft.topup)
                      : config.effective.topup_rate_percent,
                  })}
                </p>
              )}
            </div>

            {/* 下线买套餐的三档：本组单独设，留空 = 跟随全局套餐档。 */}
            <div className='space-y-2'>
              <p className='text-sm font-medium'>{t('qy_cm_gr_plan_title')}</p>
              <div className='grid gap-3 sm:grid-cols-3'>
                {PLAN_RATE_FIELDS.map((field) => (
                  <PercentInput
                    key={field.key}
                    id={`qy-cm-gr-${field.key}`}
                    label={t(field.labelKey)}
                    value={draft.plan[field.key]}
                    invalid={!qyIsValidNullablePercent(draft.plan[field.key])}
                    hint={t('qy_cm_gr_plan_hint', {
                      percent: groupInheritedPlanPercent(
                        field.key,
                        qyIsValidPercent(draft.topup)
                          ? qyNormalizePercent(draft.topup)
                          : config.effective.topup_rate_percent,
                        config
                      ),
                    })}
                    onChange={(value) =>
                      setDraft({
                        ...draft,
                        plan: { ...draft.plan, [field.key]: value },
                      })
                    }
                  />
                ))}
              </div>
            </div>

            <label className='flex items-center gap-2 text-sm'>
              <input
                type='checkbox'
                checked={draft.enabled}
                onChange={(e) =>
                  setDraft({ ...draft, enabled: e.target.checked })
                }
              />
              {t('qy_cm_gr_enabled_hint')}
            </label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_cm_f_percent_hint')}
            </p>
            <div className='flex gap-2'>
              <Button
                disabled={draftInvalid || upsert.isPending}
                onClick={() =>
                  upsert.mutate({
                    group_name: draft.groupName.trim(),
                    topup_rate_percent: qyNormalizePercent(draft.topup),
                    consume_rate_percent: qyNormalizePercent(draft.consume),
                    // 开关关着就显式发 null。这个接口是整行 upsert，
                    // 省掉这个字段与发 null 后端是同一个结果，但写出来
                    // 才看得见"这一次保存把兑换码档取消了"。
                    redemption_rate_percent: draft.redemptionEnabled
                      ? qyNormalizePercent(draft.redemption)
                      : null,
                    plan_order_rate_percent: planDraftValue(
                      draft.plan.plan_order_rate_percent
                    ),
                    plan_balance_rate_percent: planDraftValue(
                      draft.plan.plan_balance_rate_percent
                    ),
                    plan_redemption_rate_percent: planDraftValue(
                      draft.plan.plan_redemption_rate_percent
                    ),
                    enabled: draft.enabled,
                    remark: draft.remark,
                  })
                }
              >
                {t('qy_cm_save')}
              </Button>
              <Button variant='outline' onClick={() => setDraft(null)}>
                {t('qy_common_cancel')}
              </Button>
            </div>
          </div>
        )}
      </CardContent>

      <QyConfirmDialog
        open={pendingDelete != null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
        title={t('qy_cm_gr_delete_title')}
        description={t('qy_cm_gr_delete_desc', {
          topup: config.effective.topup_rate_percent,
          consume: config.effective.consume_rate_percent,
        })}
        confirmText={t('qy_common_delete')}
        isLoading={remove.isPending}
        details={
          pendingDelete == null ? null : (
            <p className='text-sm'>
              <span className='font-mono'>{pendingDelete.group_name}</span>：
              {pendingDelete.topup_rate_percent}% /{' '}
              {pendingDelete.consume_rate_percent}% /{' '}
              {pendingDelete.redemption_rate_percent != null
                ? `${pendingDelete.redemption_rate_percent}%`
                : t('qy_cm_gr_redemption_inherit', {
                    percent: groupEffectiveRedemptionPercent(
                      pendingDelete,
                      config
                    ),
                  })}
            </p>
          )
        }
        onConfirm={() => {
          if (pendingDelete != null) remove.mutate(pendingDelete.group_name)
        }}
      />
    </Card>
  )
}

function PercentInput(props: {
  id: string
  label: string
  value: string
  invalid: boolean
  hint?: string
  onChange: (value: string) => void
}) {
  return (
    <div className='space-y-1.5'>
      <Label htmlFor={props.id}>{props.label}</Label>
      <div className='flex items-center gap-2'>
        <Input
          id={props.id}
          inputMode='decimal'
          value={props.value}
          aria-invalid={props.invalid}
          onChange={(e) => props.onChange(e.target.value)}
        />
        <span className='text-muted-foreground text-sm'>%</span>
      </div>
      {props.hint != null && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
    </div>
  )
}

/** 套餐档草稿 → 提交值：空 = `null`（本组不单独配），其余规范化成百分比字面量。 */
function planDraftValue(raw: string): string | null {
  return raw.trim() === '' ? null : qyNormalizePercent(raw)
}

/**
 * 一条没单独配兑换码档的分组规则，实际按几个点返。
 *
 * 与后端 `redemptionRateUnits` 的取值顺序逐条对齐：本组没配 ⇒ 全局兑换码档
 * ⇒ 本组充值档。表格必须把这个数算出来写在"跟随"旁边，否则运营看着一列
 * "跟随"根本不知道跟到了哪儿 —— 而这一列决定的是平台真金白银要付多少。
 *
 * 注意判定用的是 `redemption_rate_follows_topup` 而不是
 * `redemption_rate_percent === ''`：显式 0% 的全局档必须压过本组充值档，
 * 而 `'0'` 与 `''` 在任何真值判断里都是同一边。
 */
function groupEffectiveRedemptionPercent(
  rule: QyCommissionGroupRate,
  config: QyCommissionAdminConfig
): string {
  if (!config.effective.redemption_rate_follows_topup) {
    return config.effective.redemption_rate_percent
  }
  return rule.topup_rate_percent
}

/**
 * 分组没单独配某一档套餐比例时，实际按几个点返。与后端 `rateUnitsFor` 对齐：
 *
 *   · 余额 / 兑换码两档：跟随全局套餐档；
 *   · 在线支付档：全局在线档配了就跟它，没配（跟随充值档）就是**本组**充值档 ——
 *     在线买套餐在这一档出现之前按充值档计，分组充值档优先。
 */
function groupInheritedPlanPercent(
  key: QyPlanRateKey,
  groupTopupPercent: string,
  config: QyCommissionAdminConfig
): string {
  if (key === 'plan_balance_rate_percent') {
    return config.effective.plan_balance_rate_percent
  }
  if (key === 'plan_redemption_rate_percent') {
    return config.effective.plan_redemption_rate_percent
  }
  if (!config.effective.plan_order_rate_follows_topup) {
    return config.effective.plan_order_rate_percent
  }
  return groupTopupPercent
}
