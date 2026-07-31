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
import { Info, Pencil, Plus, ScrollText, Trash2 } from 'lucide-react'
import { useEffect, useState } from 'react'
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
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import {
  qyAdminCommissionConfigQuery,
  qyDeleteCommissionGroupRate,
  qyUpdateCommissionConfig,
  qyUpsertCommissionGroupRate,
} from './api'
import {
  qyCommissionFieldMeta,
  qyIsValidPercent,
  qyNormalizePercent,
} from './lib/fields'
import type {
  QyCommissionAdminConfig,
  QyCommissionEffective,
  QyCommissionGroupRate,
} from './types'

/**
 * 佣金配置页。
 *
 * 权限只要求 ADMIN，与后端 `AdminAuth` 一致。设计文档建议提到 SUPER_ADMIN，
 * 但后端没跟着收紧 —— 前端单方面加门槛只会让普通管理员在侧边栏看得见、
 * 点进去吃 403，而他们其实调得动这些参数。要收紧应当先改后端。
 *
 * **每一次保存都会在后端写审计**（费率直接决定平台出血速度），因此提交前
 * 强制二次确认并复述"改了哪几项、从多少到多少"。
 *
 * 页面分三块：全局默认费率与运营参数、分组差异化费率表、YAML 只读段。
 * 比例一律以百分比呈现与提交，不出现任何万分比。
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
          render={<Link to='/qy/admin/commission-records' />}
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
    for (const key of config.editable_keys) {
      next[key] = String(
        config.effective[key as keyof QyCommissionEffective] ?? ''
      )
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

  const percentKeys = new Set(config.percent_keys)
  const changes = collectChanges(config, draft, percentKeys)
  const invalidKey = findInvalid(draft, percentKeys)

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_cm_editable_title')}</CardTitle>
          <CardDescription>{t('qy_cm_editable_desc')}</CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          {config.editable_keys.map((key) => (
            <ConfigField
              key={key}
              fieldKey={key}
              value={draft[key] ?? ''}
              isPercent={percentKeys.has(key)}
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
                  {formatChangeValue(change.from, percentKeys.has(change.key))}{' '}
                  →{' '}
                  <strong>
                    {formatChangeValue(change.to, percentKeys.has(change.key))}
                  </strong>
                </span>
              </li>
            ))}
          </ul>
        }
        onConfirm={() => {
          const patch: Record<string, string> = {}
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
  overridden: boolean
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const meta = qyCommissionFieldMeta(props.fieldKey)
  const label = meta == null ? props.fieldKey : t(meta.labelKey)
  const invalid = props.isPercent
    ? !qyIsValidPercent(props.value)
    : !isValidInteger(props.value, meta?.min ?? 0, meta?.max)

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
        {props.isPercent && (
          <span className='text-muted-foreground text-sm' aria-hidden='true'>
            %
          </span>
        )}
      </div>
      <p className='text-muted-foreground text-xs'>
        {meta == null ? props.fieldKey : t(meta.hintKey)}
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
          {Object.entries(props.config.yaml_readonly).map(([key, value]) => (
            <div
              key={key}
              className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'
            >
              <dt className='text-muted-foreground font-mono text-xs'>{key}</dt>
              <dd className='font-medium tabular-nums'>
                {typeof value === 'boolean'
                  ? t(value ? 'qy_common_on' : 'qy_common_off')
                  : String(value)}
                {key.endsWith('_percent') ? '%' : ''}
              </dd>
            </div>
          ))}
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
  enabled: boolean
  remark: string
  /** 编辑既有规则时锁住分组名：改名等于删一条加一条，两条审计比一条清楚。 */
  locked: boolean
}

const EMPTY_GROUP_DRAFT: QyGroupRateDraft = {
  groupName: '',
  topup: '',
  consume: '',
  enabled: true,
  remark: '',
  locked: false,
}

/**
 * 分组差异化费率表。
 *
 * 表头必须写清口径（按下线分组）与回落规则（没配的分组走全局默认），
 * 否则运营会以为"只有表里这几个分组返佣"。
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
      !qyIsValidPercent(draft.consume))

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
          <AlertDescription>{t('qy_cm_gr_scope_desc')}</AlertDescription>
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
                  {t('qy_cm_gr_enabled')}
                </th>
                <th className='py-1.5 pe-3 font-medium'>
                  {t('qy_cm_gr_remark')}
                </th>
                <th className='py-1.5 font-medium'>{t('qy_common_actions')}</th>
              </tr>
            </thead>
            <tbody className='divide-border divide-y'>
              {config.group_rates.length === 0 && (
                <tr>
                  <td
                    colSpan={6}
                    className='text-muted-foreground py-3 text-center'
                  >
                    {t('qy_cm_gr_empty')}
                  </td>
                </tr>
              )}
              {config.group_rates.map((rule) => (
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
              {pendingDelete.consume_rate_percent}%
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
 * 只挑出真正改动过的键。未改动的键不该出现在 PUT 里，也不该污染审计。
 *
 * 百分比按规范化后的字面量比较（"10.250" 与 "10.25" 是同一个费率），
 * 不转成 Number 再比 —— 那会让 10.25 与 10.249999999999998 判成不同。
 */
function collectChanges(
  config: QyCommissionAdminConfig,
  draft: Record<string, string>,
  percentKeys: Set<string>
): QyConfigChange[] {
  const out: QyConfigChange[] = []
  for (const [key, raw] of Object.entries(draft)) {
    const current = String(
      config.effective[key as keyof QyCommissionEffective] ?? ''
    )
    if (percentKeys.has(key)) {
      if (!qyIsValidPercent(raw)) continue
      const next = qyNormalizePercent(raw)
      if (next !== qyNormalizePercent(current)) {
        out.push({ key, from: current, to: next })
      }
      continue
    }
    const meta = qyCommissionFieldMeta(key)
    if (!isValidInteger(raw, meta?.min ?? 0, meta?.max)) continue
    const next = String(Number(raw.trim()))
    if (next !== current) out.push({ key, from: current, to: next })
  }
  return out
}

function findInvalid(
  draft: Record<string, string>,
  percentKeys: Set<string>
): string | null {
  for (const [key, raw] of Object.entries(draft)) {
    if (percentKeys.has(key)) {
      if (!qyIsValidPercent(raw)) return key
      continue
    }
    const meta = qyCommissionFieldMeta(key)
    if (!isValidInteger(raw, meta?.min ?? 0, meta?.max)) return key
  }
  return null
}

function isValidInteger(raw: string, min: number, max?: number): boolean {
  const value = Number(raw.trim())
  if (raw.trim() === '' || !Number.isInteger(value)) return false
  if (value < min) return false
  return max == null || value <= max
}

function formatChangeValue(value: string, isPercent: boolean): string {
  return isPercent ? `${value}%` : value
}
