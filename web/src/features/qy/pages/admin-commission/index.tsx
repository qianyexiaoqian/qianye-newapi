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
import { Info, ScrollText } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
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
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import { qyAdminCommissionConfigQuery, qyUpdateCommissionConfig } from './api'
import { qyCommissionFieldMeta } from './lib/fields'
import type { QyCommissionEffective } from './types'

/**
 * 佣金配置页。
 *
 * 权限只要求 ADMIN，与后端 `AdminAuth` 一致。设计文档建议提到 SUPER_ADMIN，
 * 但后端没跟着收紧 —— 前端单方面加门槛只会让普通管理员在侧边栏看得见、
 * 点进去吃 403，而他们其实调得动这些参数。要收紧应当先改后端。
 *
 * **每一次保存都会在后端写审计**（费率直接决定平台出血速度），因此提交前
 * 强制二次确认并复述"改了哪几项、从多少到多少"。
 */
export function QyAdminCommission() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminCommissionConfigQuery())
  const config = query.data

  const [draft, setDraft] = useState<Record<string, string>>({})
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿：
  // 保留旧草稿会让管理员基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    if (config == null) return
    const next: Record<string, string> = {}
    for (const key of config.editable_keys) {
      next[key] = String(
        config.effective[key as keyof QyCommissionEffective] ?? 0
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

  const changes = collectChanges(config?.effective, draft)
  const invalidKey = findInvalid(draft)

  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('qy_nav_a_commission')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/commission-records' />}
        >
          <ScrollText aria-hidden='true' />
          {t('qy_nav_a_commission_records')}
        </Button>
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {config != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)] lg:items-start'>
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
                          qyCommissionFieldMeta(invalidKey)?.labelKey ??
                            invalidKey
                        ),
                      })}
                    </p>
                  )}
                </CardContent>
              </Card>

              <Card data-card-hover='false'>
                <CardHeader>
                  <CardTitle>{t('qy_cm_yaml_title')}</CardTitle>
                  <CardDescription>{t('qy_cm_yaml_desc')}</CardDescription>
                </CardHeader>
                <CardContent className='space-y-3'>
                  <Alert>
                    <Info />
                    <AlertTitle>{t('qy_cm_yaml_note_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_cm_yaml_note_desc')}
                    </AlertDescription>
                  </Alert>
                  <dl className='divide-border divide-y text-sm'>
                    {Object.entries(config.yaml_readonly).map(
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
                          </dd>
                        </div>
                      )
                    )}
                  </dl>
                </CardContent>
              </Card>
            </div>
          )}
        </QyPageBoundary>
      </SectionPageLayout.Content>

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
                  {change.from} → <strong>{change.to}</strong>
                </span>
              </li>
            ))}
          </ul>
        }
        onConfirm={() => {
          const patch: Record<string, number> = {}
          for (const change of changes) patch[change.key] = change.to
          saveMutation.mutate(patch)
        }}
      />
    </SectionPageLayout>
  )
}

function ConfigField(props: {
  fieldKey: string
  value: string
  overridden: boolean
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const meta = qyCommissionFieldMeta(props.fieldKey)
  const label = meta == null ? props.fieldKey : t(meta.labelKey)
  const numeric = Number(props.value)
  const invalid =
    !Number.isInteger(numeric) ||
    (meta != null && (numeric < meta.min || numeric > meta.max))

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
      <Input
        id={`qy-cm-${props.fieldKey}`}
        inputMode='numeric'
        value={props.value}
        aria-invalid={invalid}
        onChange={(event) => props.onChange(event.target.value)}
      />
      <p className='text-muted-foreground text-xs'>
        {meta == null ? props.fieldKey : t(meta.hintKey)}
        {meta?.unit === 'bps' && Number.isFinite(numeric)
          ? ` (${numeric / 100}%)`
          : ''}
      </p>
    </div>
  )
}

/** 只挑出真正改动过的键。未改动的键不该出现在 PUT 里，也不该污染审计。 */
function collectChanges(
  effective: QyCommissionEffective | undefined,
  draft: Record<string, string>
): { key: string; from: number; to: number }[] {
  if (effective == null) return []
  const out: { key: string; from: number; to: number }[] = []
  for (const [key, raw] of Object.entries(draft)) {
    const next = Number(raw)
    if (!Number.isInteger(next)) continue
    const current = effective[key as keyof QyCommissionEffective]
    if (typeof current === 'number' && current !== next) {
      out.push({ key, from: current, to: next })
    }
  }
  return out
}

function findInvalid(draft: Record<string, string>): string | null {
  for (const [key, raw] of Object.entries(draft)) {
    const value = Number(raw)
    const meta = qyCommissionFieldMeta(key)
    if (!Number.isInteger(value)) return key
    if (meta != null && (value < meta.min || value > meta.max)) return key
  }
  return null
}
