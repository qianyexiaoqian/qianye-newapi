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
import { Link } from '@tanstack/react-router'
import { Info, Pencil, Plus, RefreshCw, ScrollText, Trash2 } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySdAmount } from '../../components/qy-sd-amount'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useStardustName } from '../../hooks/use-stardust-name'
import { qyErrorMessage } from '../../lib/api'
import { qyArray } from '../../lib/array'
import { qyTabTarget } from '../../lib/pages'
import { qyKeys } from '../../lib/query-keys'
import {
  qyAdminCommissionConfigQuery,
  qyAdminCommissionHealthQuery,
  qyDeleteCommissionGroupRate,
  qyRerunDailySettle,
  qyUpdateCommissionConfig,
  qyUpsertCommissionGroupRate,
} from './api'
import {
  qyCommissionFieldMeta,
  qyIsValidNullablePercent,
  qyIsValidPercent,
  qyNormalizePercent,
} from './lib/fields'
import type {
  QyCommissionAdminConfig,
  QyCommissionEffective,
  QyCommissionGroupRate,
  QyCommissionRateOverlap,
  QyDailySettleRun,
} from './types'

/**
 * 佣金配置页（D-15 从 git HEAD 恢复；法币折算卡删除，加自动入账参数）。
 *
 * 权限只要求 ADMIN，与后端 `AdminAuth` 一致。要收紧应当先改后端。
 *
 * **每一次保存都会在后端写审计**（费率直接决定平台出血速度），因此提交前
 * 强制二次确认并复述"改了哪几项、从多少到多少"。
 *
 * 页面分四块：全局默认费率与运营参数（含自动入账门槛 / 周期）、YAML 只读段、
 * 结算与入账调度、分组差异化费率表。比例一律以百分比呈现与提交，不出现任何
 * 万分比；金额一律是站内额度 —— 这一页上没有任何法币键。
 */
export function QyAdminCommission() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminCommissionConfigQuery())
  const config = query.data

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_commission')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          // 佣金审核已收进「结算台」的选择夹，直接 to 旧地址也到得了（重定向），
          // 但那是先离开再被弹回来的一次白闪。走 qyTabTarget 直达宿主 + 标签。
          render={<Link {...qyTabTarget('/qy/admin/commission-records')} />}
        >
          <ScrollText aria-hidden='true' />
          {t('qy_nav_a_commission_records')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {config != null && (
            <div className='space-y-4'>
              <div className='grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)] lg:items-start'>
                <EditableSettingsCard config={config} />
                <YamlReadonlyCard config={config} />
              </div>
              <DailySettleCard />
              <GroupRatesCard config={config} />
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

/** 一处改动的复述：键、旧值、新值。确认弹窗与 PUT 请求共用它。 */
type QyConfigChange = { key: string; from: string; to: string }

function EditableSettingsCard(props: { config: QyCommissionAdminConfig }) {
  const { config } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [draft, setDraft] = useState<Record<string, string>>({})
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿：
  // 保留旧草稿会让管理员基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    const next: Record<string, string> = {}
    for (const key of qyArray(config.editable_keys)) {
      const raw = config.effective[key as keyof QyCommissionEffective] ?? ''
      next[key] = String(raw)
    }
    setDraft(next)
  }, [config])

  const saveMutation = useMutation({
    mutationFn: qyUpdateCommissionConfig,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_cm_saved'))
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminCommissionConfig(),
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const percentKeys = new Set(qyArray(config.percent_keys))
  // 可空的百分比键（目前只有兑换码档）。空在这里不是"没填完"，而是一个
  // 要提交上去的取值："取消这一档，跟随充值档"。
  const nullablePercentKeys = new Set(qyArray(config.nullable_percent_keys))
  const followsTopupLabel = t('qy_cm_f_redemption_follows', {
    percent: config.effective.topup_rate_percent,
  })
  const changes = collectChanges(
    config,
    draft,
    percentKeys,
    nullablePercentKeys
  )
  const invalidKey = findInvalid(
    config,
    draft,
    percentKeys,
    nullablePercentKeys
  )

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_cm_editable_title')}</CardTitle>
          <CardDescription>{t('qy_cm_editable_desc')}</CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          {qyArray(config.editable_keys).map((key) => (
            <ConfigField
              key={key}
              fieldKey={key}
              value={draft[key] ?? ''}
              isPercent={percentKeys.has(key)}
              nullable={nullablePercentKeys.has(key)}
              // 留空时旁边要写清楚"那实际是几个点"。只画一个空输入框的话，
              // 运营看不出这一档实际生效的是什么，而那正是他点进来要看的数。
              emptyMeansText={
                nullablePercentKeys.has(key)
                  ? t('qy_cm_f_redemption_follows', {
                      percent: config.effective.topup_rate_percent,
                    })
                  : null
              }
              overridden={config.overrides[key] != null}
              onChange={(value) =>
                setDraft((prev) => ({ ...prev, [key]: value }))
              }
            />
          ))}
          <Button
            disabled={
              changes.length === 0 ||
              invalidKey != null ||
              saveMutation.isPending
            }
            onClick={() => setConfirmOpen(true)}
          >
            {t('qy_cm_save')}
          </Button>
          {invalidKey != null && (
            <p className='text-destructive text-sm'>
              {t('qy_cm_invalid_value', {
                field: t(
                  qyCommissionFieldMeta(invalidKey)?.labelKey ?? invalidKey
                ),
              })}
            </p>
          )}
        </CardContent>
      </Card>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_cm_confirm_title')}
        description={t('qy_cm_confirm_desc')}
        confirmText={t('qy_cm_save')}
        isLoading={saveMutation.isPending}
        details={
          <ul className='space-y-1 text-sm'>
            {changes.map((change) => (
              <li key={change.key} className='flex justify-between gap-3'>
                <span className='text-muted-foreground'>
                  {t(qyCommissionFieldMeta(change.key)?.labelKey ?? change.key)}
                </span>
                <span className='tabular-nums'>
                  {formatChangeValue(
                    change.key,
                    change.from,
                    percentKeys,
                    followsTopupLabel
                  )}{' '}
                  →{' '}
                  <strong>
                    {formatChangeValue(
                      change.key,
                      change.to,
                      percentKeys,
                      followsTopupLabel
                    )}
                  </strong>
                </span>
              </li>
            ))}
          </ul>
        }
        onConfirm={() => {
          const patch: Record<string, string> = {}
          // 空串是**要发出去的取值**，不是"跳过这一项"：后端据此删掉
          // qy_settings 里那行覆盖，让该档回到跟随充值档。
          for (const change of changes) patch[change.key] = change.to
          saveMutation.mutate(patch)
        }}
      />
    </>
  )
}

function ConfigField(props: {
  fieldKey: string
  value: string
  isPercent: boolean
  /** 留空是否合法。为真时空表示"没单独配"，不是"没填完"。 */
  nullable?: boolean
  /** 留空时在提示里补的那一句（"跟随充值档 10%"）。 */
  emptyMeansText?: string | null
  overridden: boolean
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const stardustUnit = useStardustName()
  const meta = qyCommissionFieldMeta(props.fieldKey)
  const label = meta == null ? props.fieldKey : t(meta.labelKey)
  const parsed = props.isPercent
    ? null
    : parseIntegerDraft(props.fieldKey, props.value)
  // 输入框右侧的单位。星屑那一族印运营配的单位名（改过就跟着改），比例印 %，
  // 天数 / 周期这类纯计数什么都不印。
  let unitLabel: string | null = null
  if (meta?.unit === 'stardust') unitLabel = stardustUnit
  else if (props.isPercent) unitLabel = '%'
  const emptyDraft = props.value.trim() === ''
  const invalid = props.isPercent
    ? !(props.nullable === true
        ? qyIsValidNullablePercent(props.value)
        : qyIsValidPercent(props.value))
    : parsed == null
  const isEmpty = props.nullable === true && emptyDraft

  return (
    <div className='space-y-1.5'>
      <Label htmlFor={`qy-cm-${props.fieldKey}`}>
        {label}
        {props.overridden && (
          <span className='text-muted-foreground ms-1 text-xs'>
            {t('qy_cm_overridden')}
          </span>
        )}
      </Label>
      <div className='flex items-center gap-2'>
        <Input
          id={`qy-cm-${props.fieldKey}`}
          inputMode='decimal'
          value={props.value}
          aria-invalid={invalid}
          onChange={(event) => props.onChange(event.target.value)}
        />
        {unitLabel != null && (
          <span className='text-muted-foreground text-sm' aria-hidden='true'>
            {unitLabel}
          </span>
        )}
      </div>
      <p className='text-muted-foreground text-xs'>
        {meta == null ? props.fieldKey : t(meta.hintKey)}
        {/* 留空当前意味着什么，必须写在旁边：一个空输入框本身回答不了
            "那这一档实际返几个点"，而这正是运营点进来要看的那个数。 */}
        {isEmpty && props.emptyMeansText != null && (
          <> {props.emptyMeansText}</>
        )}
      </p>
    </div>
  )
}

function YamlReadonlyCard(props: { config: QyCommissionAdminConfig }) {
  const { t } = useTranslation()
  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_cm_yaml_title')}</CardTitle>
        <CardDescription>{t('qy_cm_yaml_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <Alert>
          <Info />
          <AlertTitle>{t('qy_cm_yaml_note_title')}</AlertTitle>
          <AlertDescription>{t('qy_cm_yaml_note_desc')}</AlertDescription>
        </Alert>
        <dl className='divide-border divide-y text-sm'>
          {Object.entries(props.config.yaml_readonly ?? {}).map(
            ([key, value]) => (
              <div
                key={key}
                className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'
              >
                <dt className='text-muted-foreground font-mono text-xs'>
                  {key}
                </dt>
                <dd className='font-medium tabular-nums'>
                  {typeof value === 'boolean'
                    ? t(value ? 'qy_common_on' : 'qy_common_off')
                    : String(value)}
                  {key.endsWith('_percent') ? '%' : ''}
                </dd>
              </div>
            )
          )}
        </dl>
      </CardContent>
    </Card>
  )
}

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
 */
function GroupRatesCard(props: { config: QyCommissionAdminConfig }) {
  const { config } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()

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

  const draftInvalid =
    draft != null &&
    (draft.groupName.trim() === '' ||
      !qyIsValidPercent(draft.topup) ||
      !qyIsValidPercent(draft.consume) ||
      // 开关打开就必须填出一个合法比例；关着的时候输入框里剩什么都不算数。
      (draft.redemptionEnabled && !qyIsValidPercent(draft.redemption)))

  const groupRates = qyArray(config.group_rates)

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
          <table className='w-full min-w-[36rem] text-sm'>
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
                    colSpan={7}
                    className='text-muted-foreground py-3 text-center'
                  >
                    {t('qy_cm_gr_empty')}
                  </td>
                </tr>
              )}
              {groupRates.map((rule) => (
                <tr key={rule.group_name}>
                  <td className='py-2 pe-3 font-mono text-xs'>
                    {rule.group_name}
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
                        onClick={() =>
                          setDraft({
                            groupName: rule.group_name,
                            topup: rule.topup_rate_percent,
                            consume: rule.consume_rate_percent,
                            redemptionEnabled:
                              rule.redemption_rate_percent != null,
                            redemption: rule.redemption_rate_percent ?? '',
                            enabled: rule.enabled,
                            remark: rule.remark,
                            locked: true,
                          })
                        }
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
            onClick={() => setDraft({ ...EMPTY_GROUP_DRAFT })}
          >
            <Plus aria-hidden='true' />
            {t('qy_cm_gr_add')}
          </Button>
        ) : (
          <div className='border-border space-y-3 rounded-md border p-3'>
            <div className='grid gap-3 sm:grid-cols-2'>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-cm-gr-group'>{t('qy_cm_gr_group')}</Label>
                <Input
                  id='qy-cm-gr-group'
                  value={draft.groupName}
                  disabled={draft.locked}
                  aria-invalid={draft.groupName.trim() === ''}
                  onChange={(e) =>
                    setDraft({ ...draft, groupName: e.target.value })
                  }
                />
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
              <div className='space-y-1.5'>
                <Label htmlFor='qy-cm-gr-topup'>
                  {t('qy_cm_f_topup_rate')}
                </Label>
                <div className='flex items-center gap-2'>
                  <Input
                    id='qy-cm-gr-topup'
                    inputMode='decimal'
                    value={draft.topup}
                    aria-invalid={!qyIsValidPercent(draft.topup)}
                    onChange={(e) =>
                      setDraft({ ...draft, topup: e.target.value })
                    }
                  />
                  <span className='text-muted-foreground text-sm'>%</span>
                </div>
              </div>
              <div className='space-y-1.5'>
                <Label htmlFor='qy-cm-gr-consume'>
                  {t('qy_cm_f_consume_rate')}
                </Label>
                <div className='flex items-center gap-2'>
                  <Input
                    id='qy-cm-gr-consume'
                    inputMode='decimal'
                    value={draft.consume}
                    aria-invalid={!qyIsValidPercent(draft.consume)}
                    onChange={(e) =>
                      setDraft({ ...draft, consume: e.target.value })
                    }
                  />
                  <span className='text-muted-foreground text-sm'>%</span>
                </div>
              </div>
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
 * 草稿文本 → 存储用的整数，并按字段元数据的区间卡一次。非法返回 `null`。
 *
 * 只收非负整数字面量：星屑没有小数，一个带小数点的输入是运营填错了，判非法
 * 让他看见红字，比替他四舍五入成另一个金额好得多。
 *
 * D-16 之前这里还有一条 USD 通道：金额字段那时是**额度**（500000 = $1），运营
 * 不该去数零，所以按美元录入再换算回额度。星屑本身就是人直接读的整数，那一层
 * 连同换算率可用性判定一起没了。
 */
function parseIntegerDraft(key: string, raw: string): number | null {
  const s = raw.trim()
  if (!/^\d+$/.test(s)) return null
  const value = Number(s)
  if (!Number.isSafeInteger(value)) return null
  const meta = qyCommissionFieldMeta(key)
  if (value < (meta?.min ?? 0)) return null
  if (meta != null && value > meta.max) return null
  return value
}

/**
 * 只挑出真正改动过的键。未改动的键不该出现在 PUT 里，也不该污染审计。
 *
 * 百分比按规范化后的字面量比较（"10.250" 与 "10.25" 是同一个费率），
 * 不转成 Number 再比 —— 那会让 10.25 与 10.249999999999998 判成不同。
 *
 * 金额字段比较的是整数字面量，界面上填的就是存进去的那个数，往返无损，
 * 所以运营什么都不改直接保存时这里一条改动都挑不出来。
 */
function collectChanges(
  config: QyCommissionAdminConfig,
  draft: Record<string, string>,
  percentKeys: Set<string>,
  nullablePercentKeys: Set<string>
): QyConfigChange[] {
  const out: QyConfigChange[] = []
  for (const [key, raw] of Object.entries(draft)) {
    const current = String(
      config.effective[key as keyof QyCommissionEffective] ?? ''
    )
    if (percentKeys.has(key)) {
      // 可空键的空串是一个**取值**（"取消这一档"），必须走完整的比较与提交，
      // 不能跟"填了一半的非法输入"一起被 continue 掉 —— 那样运营清空输入框
      // 再保存，会得到一个成功提示和一份原封不动的配置。
      if (nullablePercentKeys.has(key) && raw.trim() === '') {
        if (current !== '') out.push({ key, from: current, to: '' })
        continue
      }
      if (!qyIsValidPercent(raw)) continue
      const next = qyNormalizePercent(raw)
      if (next !== qyNormalizePercent(current)) {
        out.push({ key, from: current, to: next })
      }
      continue
    }
    const parsed = parseIntegerDraft(key, raw)
    if (parsed == null) continue
    const next = String(parsed)
    if (next !== current) out.push({ key, from: current, to: next })
  }
  return out
}

function findInvalid(
  config: QyCommissionAdminConfig,
  draft: Record<string, string>,
  percentKeys: Set<string>,
  nullablePercentKeys: Set<string>
): string | null {
  // `config` 仍然进参：后端多下发一个前端不认识的键时，它的值就是判据 ——
  // 这里不认识的键按"原样字面量"处理，只要草稿等于当前值就不算非法。
  for (const [key, raw] of Object.entries(draft)) {
    if (percentKeys.has(key)) {
      const ok = nullablePercentKeys.has(key)
        ? qyIsValidNullablePercent(raw)
        : qyIsValidPercent(raw)
      if (!ok) return key
      continue
    }
    if (qyCommissionFieldMeta(key) == null) {
      const current = String(
        config.effective[key as keyof QyCommissionEffective] ?? ''
      )
      if (raw.trim() !== current) return key
      continue
    }
    if (parseIntegerDraft(key, raw) == null) return key
  }
  return null
}

/**
 * 复述一处改动的值。`value` 是存储用的字面量（百分比字符串或星屑整数）。
 *
 * 空的百分比要复述成 `emptyLabel`（"跟随充值档 10%"）而不是一个孤零零的
 * `%`：确认弹窗是运营在动费率之前看到的最后一屏，"3% → %" 读不出来这一次
 * 改的是什么，而它改的恰恰是一整档比例。
 */
function formatChangeValue(
  key: string,
  value: string,
  percentKeys: Set<string>,
  emptyLabel: string
): string {
  if (percentKeys.has(key)) {
    return value.trim() === '' ? emptyLabel : `${value}%`
  }
  return value
}

/**
 * 结算与入账调度。
 *
 * 三段各答一个问题：
 *
 *   · **今天这一跑成了没有**（一日一结算，跑挂了当天剩下所有人的佣金都要等到明天）
 *   · **自动入账累计发了多少**（D-16 起入账是本地事务，没有在途、没有挂起，
 *     也就没有要人裁决的单子）
 *   · **是不是有两条线在给同一笔基数各返一次**（`rate_overlap`）——
 *     这一段最容易被忽略，因为两条线各自的配置页都显示正常
 */
function DailySettleCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminCommissionHealthQuery())
  const snapshot = query.data?.daily_settle
  const credit = query.data?.credit
  const overlap = query.data?.rate_overlap
  const [confirmOpen, setConfirmOpen] = useState(false)

  const rerunMutation = useMutation({
    mutationFn: qyRerunDailySettle,
    onSuccess: (data) => {
      toast.success(
        data.rearmed ? t('qy_cm_ds_rerun_done') : t('qy_cm_ds_rerun_noop')
      )
      void queryClient.invalidateQueries({
        queryKey: qyKeys.adminCommissionHealth(),
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    // 锚点 id：佣金审核那一屏撤掉了「立即结算」，取而代之的那段说明里有一条
    // 「结算卡住时的手动补救在这里」的链接，落点就是这张卡。没有 id 的话那条
    // 链接只能把人扔到设置页顶部，而这一页很长。
    <Card data-card-hover='false' id='qy-daily-settle' className='scroll-mt-4'>
      <CardHeader>
        <CardTitle>{t('qy_cm_ds_title')}</CardTitle>
        <CardDescription>{t('qy_cm_ds_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        {snapshot == null ? (
          <p className='text-muted-foreground text-sm'>{t('qy_cm_ds_none')}</p>
        ) : (
          <>
            <div className='grid gap-2 text-sm sm:grid-cols-2'>
              <QyDailySettleField
                label={t('qy_cm_ds_today')}
                value={`${snapshot.today} (UTC${snapshot.day_offset_minutes >= 0 ? '+' : ''}${snapshot.day_offset_minutes / 60})`}
              />
              <QyDailySettleField
                label={t('qy_cm_ds_ran_today')}
                value={
                  snapshot.ran_today ? t('qy_cm_ds_yes') : t('qy_cm_ds_no')
                }
              />
            </div>
            <QyDailySettleRunView
              title={t('qy_cm_ds_current')}
              run={snapshot.current}
            />
            <QyDailySettleRunView
              title={t('qy_cm_ds_previous')}
              run={snapshot.previous}
            />
          </>
        )}
        <div>
          <Button
            variant='outline'
            size='sm'
            disabled={rerunMutation.isPending}
            onClick={() => setConfirmOpen(true)}
          >
            <RefreshCw aria-hidden='true' />
            {t('qy_cm_ds_rerun')}
          </Button>
        </div>

        {/* 自动入账那一段。取不到（后端暂不下发）就说取不到，不画一排 0：
            “0 笔”与“不知道有几笔”对运营是两个完全不同的结论。 */}
        <div className='border-border space-y-2 rounded border p-3 text-sm'>
          <p className='font-medium'>{t('qy_cm_credit_title')}</p>
          <p className='text-muted-foreground text-xs'>
            {t('qy_cm_credit_desc')}
          </p>
          {credit == null ? (
            <p className='text-muted-foreground'>{t('qy_cm_credit_none')}</p>
          ) : (
            <div className='grid gap-2 sm:grid-cols-2'>
              <QyDailySettleField
                label={t('qy_cm_credit_rows')}
                value={String(credit.credited_rows)}
              />
              <QyDailySettleField
                label={t('qy_cm_credit_total')}
                value={<QySdAmount amount={credit.credited_stardust} />}
              />
            </div>
          )}
        </div>

        <RateOverlapNotice overlap={overlap} />
      </CardContent>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_cm_ds_rerun')}
        description={t('qy_cm_ds_rerun_confirm')}
        confirmText={t('qy_cm_ds_rerun')}
        isLoading={rerunMutation.isPending}
        onConfirm={() => {
          setConfirmOpen(false)
          rerunMutation.mutate()
        }}
      />
    </Card>
  )
}

/**
 * 「两条线在给同一笔基数各返一次」的告警。
 *
 * 它刻意画成 destructive 而不是一句灰字提示：这不是一条可以扫一眼跳过的说明，
 * 而是"平台现在每一笔下线消费都在付双倍"。但它也刻意**不提供任何一键修复** ——
 * 留哪一条线是运营的决定（即时小额 vs 延迟大额，两条并存也可能是故意的），
 * 代码替他挑一条清零，才是真正会造成资损的那种"帮忙"。
 *
 * 后端不下发这一段（旧版本）时整块不渲染：编一个"没有重叠"出来，比不说更糟。
 */
function RateOverlapNotice(props: { overlap?: QyCommissionRateOverlap }) {
  const { t } = useTranslation()
  const overlap = props.overlap
  if (overlap == null || overlap.ok || overlap.sources.length === 0) return null
  return (
    <Alert variant='destructive'>
      <Info />
      <AlertTitle>{t('qy_cm_overlap_title')}</AlertTitle>
      <AlertDescription className='space-y-1'>
        <p>{t('qy_cm_overlap_desc')}</p>
        <ul className='space-y-0.5'>
          {overlap.sources.map((row) => (
            <li key={row.source} className='font-mono text-xs'>
              {t(`qy_cm_overlap_src_${row.source}`, {
                defaultValue: row.source,
              })}
              {': '}
              {t('qy_cm_overlap_pair', {
                commission: row.commission_rate_bps / 100,
                invite: row.invite_rate_bps / 100,
              })}
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}

function QyDailySettleField(props: { label: string; value: ReactNode }) {
  return (
    <div className='flex items-baseline justify-between gap-3'>
      <span className='text-muted-foreground'>{props.label}</span>
      <span className='font-mono text-xs'>{props.value}</span>
    </div>
  )
}

function QyDailySettleRunView(props: {
  title: string
  run?: QyDailySettleRun
}) {
  const { t } = useTranslation()
  if (props.run == null) {
    return (
      <div className='text-sm'>
        <span className='text-muted-foreground'>{props.title}</span>
        <span className='ml-2'>{t('qy_cm_ds_none')}</span>
      </div>
    )
  }
  const run = props.run
  return (
    <div className='border-border space-y-1 rounded border p-3 text-sm'>
      <div className='flex items-baseline justify-between gap-3'>
        <span className='font-medium'>{props.title}</span>
        <span className='font-mono text-xs'>{run.run_date}</span>
      </div>
      <QyDailySettleField label={t('qy_cm_ds_status')} value={run.status} />
      <QyDailySettleField
        label={t('qy_cm_ds_attempts')}
        value={String(run.attempts)}
      />
      <QyDailySettleField
        label={t('qy_cm_ds_processed')}
        value={String(run.processed)}
      />
      <QyDailySettleField
        label={t('qy_cm_ds_failed')}
        value={String(run.failed)}
      />
      {run.remark !== '' && (
        <p className='text-muted-foreground text-xs'>{run.remark}</p>
      )}
    </div>
  )
}
