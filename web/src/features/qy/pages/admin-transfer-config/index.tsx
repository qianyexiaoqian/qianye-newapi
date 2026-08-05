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
import { Info, ScrollText, ShieldAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
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
import { useSystemConfigStore } from '@/stores/system-config-store'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyUsdScaleNotice } from '../../components/qy-usd-scale-notice'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import {
  qyFormatQuotaAsUsd,
  qyQuotaDraftText,
  qyQuotaDraftValue,
  qyUsdScale,
  type QyUsdScale,
} from '../../lib/quota-usd'
import { qyAdminTransferConfigQuery, qyUpdateTransferConfig } from './api'
import { qyBpsToPercentText, qyTransferFieldMeta } from './lib/fields'
import type {
  QyTransferAdminConfig,
  QyTransferBound,
  QyTransferEffective,
} from './types'

/**
 * 划转门槛配置页。
 *
 * transfer 曾是唯一一个门槛全在 YAML 的资金模块 —— 调一次单笔上限要改文件、
 * 重启进程。裁决 5 把运营参数划进 `qy_settings`，这一页就是它的管理面。
 *
 * 权限只要求 ADMIN，与后端 `AdminAuth` 一致。**每一次保存都会在后端写审计**
 * （门槛直接决定一个账号一天能转走多少），因此提交前强制二次确认并复述
 * 「改了哪几项、从多少到多少」。
 *
 * 页面分两块：可在线改的门槛、YAML 只读段。取值区间一律来自接口的 `bounds`，
 * 前端不自己抄一份。
 *
 * **跨字段规则（`min_quota` 不得大于 `max_per_tx_quota`）刻意不在前端复现。**
 * 后端 `config.ValidateTransfer` 是它唯一的定义，YAML 加载与这个接口共用；
 * 前端再写一遍就是第二份，而两份规则里先漂移的那份通常是没人再看的这份。
 * 非法组合会得到一个带原因的 400，由 `qyErrorMessage` 原样弹给运营。
 */
export function QyAdminTransferConfig() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminTransferConfigQuery())
  const config = query.data

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_transfer_config')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          variant='outline'
          size='sm'
          render={<Link to='/qy/admin/transfer-records' />}
        >
          <ScrollText aria-hidden='true' />
          {t('qy_nav_a_transfer_records')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {config != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)] lg:items-start'>
              <EditableSettingsCard config={config} />
              <YamlReadonlyCard config={config} />
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

/**
 * 一处改动的复述：键、旧值、新值。确认弹窗与 PUT 请求共用它。
 *
 * `from` / `to` 是**存储用的额度整数**，不是界面上那串 USD。请求体与审计
 * 认的都是这个数，复述给运营看的 USD 只是它的一个渲染。
 */
type QyConfigChange = { key: string; from: number; to: number }

function EditableSettingsCard(props: { config: QyTransferAdminConfig }) {
  const { config } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  // 换算率来自站点展示配置，运营改得动。它变了，这一页已存门槛的**语义**就变了
  // （同样的 500000 额度从 $1 变成别的），所以下面必须把当前换算率显示出来。
  const quotaPerUnit = useSystemConfigStore(
    (state) => state.config.currency.quotaPerUnit
  )
  const scale = useMemo(() => qyUsdScale(quotaPerUnit), [quotaPerUnit])

  const [draft, setDraft] = useState<Record<string, string>>({})
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿：
  // 保留旧草稿会让管理员基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    const next: Record<string, string> = {}
    for (const key of config.editable_keys) {
      next[key] = qyQuotaDraftText(
        effectiveValue(config.effective, key),
        scale,
        isUsdField(key, scale)
      )
    }
    setDraft(next)
  }, [config, scale])

  const saveMutation = useMutation({
    mutationFn: qyUpdateTransferConfig,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_tc_saved'))
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminTransferConfig(),
      })
      // 门槛改动会同时让用户端的 /transfer/limits 过期。这里不知道有没有别的
      // 视图在缓存里，qy 的 key 统一以 'qy' 开头正是为了这一刻能全量失效。
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const changes = collectChanges(config, draft, scale)
  const invalidKey = findInvalid(config, draft, scale)

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_tc_editable_title')}</CardTitle>
          <CardDescription>{t('qy_tc_editable_desc')}</CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          {!config.effective_valid && (
            <Alert variant='destructive'>
              <ShieldAlert />
              <AlertTitle>{t('qy_tc_invalid_state_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_tc_invalid_state_desc')}
              </AlertDescription>
            </Alert>
          )}

          <QyUsdScaleNotice scale={scale} />

          {/* 需求 3-C 的口径必须写在运营看得见的地方：0 是「不限」，
              而「不限」的直接后果是用户端整块不显示今日剩余量。 */}
          <Alert>
            <Info />
            <AlertTitle>{t('qy_tc_zero_note_title')}</AlertTitle>
            <AlertDescription>{t('qy_tc_zero_note_desc')}</AlertDescription>
          </Alert>

          {config.editable_keys.map((key) => (
            <ConfigField
              key={key}
              fieldKey={key}
              value={draft[key] ?? ''}
              bound={config.bounds[key] ?? null}
              scale={scale}
              overriddenFrom={
                config.overrides[key] == null
                  ? null
                  : effectiveValue(config.yaml_defaults, key)
              }
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
            {t('qy_tc_save')}
          </Button>
          {invalidKey != null && (
            <p className='text-destructive text-sm'>
              {t('qy_tc_invalid_value', {
                field: t(
                  qyTransferFieldMeta(invalidKey)?.labelKey ?? invalidKey
                ),
              })}
            </p>
          )}
        </CardContent>
      </Card>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_tc_confirm_title')}
        description={t('qy_tc_confirm_desc')}
        confirmText={t('qy_tc_save')}
        isLoading={saveMutation.isPending}
        details={
          <ul className='space-y-1 text-sm'>
            {changes.map((change) => (
              <li key={change.key} className='flex justify-between gap-3'>
                <span className='text-muted-foreground'>
                  {t(qyTransferFieldMeta(change.key)?.labelKey ?? change.key)}
                </span>
                <span className='tabular-nums'>
                  {displayValue(change.key, change.from, scale)} →{' '}
                  <strong>{displayValue(change.key, change.to, scale)}</strong>
                </span>
              </li>
            ))}
          </ul>
        }
        onConfirm={() => {
          // 请求体仍然是**额度整数**：存储层一个字都没动，界面只是换了个单位
          // 让人填。改存储会牵动后端已有的区间校验、跨字段校验、审计快照，
          // 以及划转自己那条资金路径 —— 那不是一次展示层改动该付的代价。
          const patch: Record<string, string> = {}
          for (const change of changes) patch[change.key] = String(change.to)
          saveMutation.mutate(patch)
        }}
      />
    </>
  )
}

function ConfigField(props: {
  fieldKey: string
  value: string
  bound: QyTransferBound | null
  scale: QyUsdScale
  /** 该项已被运营覆盖时，YAML 里的原值（额度整数）；未覆盖为 `null`。 */
  overriddenFrom: number | null
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const meta = qyTransferFieldMeta(props.fieldKey)
  const label = meta == null ? props.fieldKey : t(meta.labelKey)
  const asUsd = isUsdField(props.fieldKey, props.scale)
  const parsed = qyQuotaDraftValue(props.value, props.scale, asUsd)
  const invalid =
    parsed == null ||
    (props.bound != null &&
      (parsed < props.bound.min || parsed > props.bound.max))

  return (
    <div className='space-y-1.5'>
      <Label htmlFor={`qy-tc-${props.fieldKey}`}>
        {label}
        {props.overriddenFrom != null && (
          <span className='text-muted-foreground ms-1 text-xs'>
            {t('qy_tc_overridden', {
              yaml: displayValue(
                props.fieldKey,
                props.overriddenFrom,
                props.scale
              ),
            })}
          </span>
        )}
      </Label>
      <div className='flex items-center gap-2'>
        <Input
          id={`qy-tc-${props.fieldKey}`}
          inputMode={asUsd ? 'decimal' : 'numeric'}
          value={props.value}
          aria-invalid={invalid}
          onChange={(event) => props.onChange(event.target.value)}
        />
        <span
          className='text-muted-foreground shrink-0 text-sm'
          aria-hidden='true'
        >
          {asUsd ? 'USD' : meta != null && t(`qy_tc_unit_${meta.unit}`)}
        </span>
      </div>
      <p className='text-muted-foreground text-xs'>
        {meta == null ? props.fieldKey : t(meta.hintKey)}
        {meta?.unit === 'bps' && parsed != null && (
          <>
            {' '}
            {t('qy_tc_f_fee_bps_percent', {
              percent: qyBpsToPercentText(parsed),
            })}
          </>
        )}
        {/* 存进库的仍然是额度整数，把它显示出来，运营复核审计与后端日志时
            才对得上号 —— 那边从头到尾都是这个数。 */}
        {asUsd && parsed != null && (
          <> {t('qy_cfg_usd_equals_quota', { quota: parsed })}</>
        )}
        {props.bound != null && (
          <>
            {' '}
            {t('qy_tc_range', {
              min: displayValue(props.fieldKey, props.bound.min, props.scale),
              max: displayValue(props.fieldKey, props.bound.max, props.scale),
            })}
          </>
        )}
        {asUsd && (
          <>
            {' '}
            {t('qy_cfg_usd_step_hint', {
              step: qyFormatQuotaAsUsd(1, props.scale),
            })}
          </>
        )}
      </p>
    </div>
  )
}

function YamlReadonlyCard(props: { config: QyTransferAdminConfig }) {
  const { t } = useTranslation()
  const yaml = props.config.yaml_readonly
  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_tc_yaml_title')}</CardTitle>
        <CardDescription>{t('qy_tc_yaml_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        <Alert>
          <ShieldAlert />
          <AlertTitle>{t('qy_tc_yaml_note_title')}</AlertTitle>
          <AlertDescription>{t('qy_tc_yaml_note_desc')}</AlertDescription>
        </Alert>
        <dl className='divide-border divide-y text-sm'>
          {Object.entries(yaml).map(([key, value]) => (
            <div
              key={key}
              className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'
            >
              <dt className='text-muted-foreground font-mono text-xs'>{key}</dt>
              <dd className='font-medium tabular-nums'>
                {typeof value === 'boolean'
                  ? t(value ? 'qy_common_on' : 'qy_common_off')
                  : String(value)}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  )
}

/** 从生效值/YAML 默认值里取一个键。两份形状相同，取法也就只该有一份。 */
function effectiveValue(source: QyTransferEffective, key: string): number {
  return source[key as keyof QyTransferEffective] ?? 0
}

/**
 * 该字段是否按 USD 录入。
 *
 * 只有金额字段（`unit === 'quota'`）才换算；笔数、秒数、小时、万分比本来就
 * 不是钱，换算过去只会得到一个没有意义的小数。换算率不可用时全部退回额度
 * 单位 —— 理由见 `lib/quota-usd.ts` 的文件头。
 */
function isUsdField(key: string, scale: QyUsdScale): boolean {
  return scale.usable && qyTransferFieldMeta(key)?.unit === 'quota'
}

/** 把一个存储额度渲染成界面上该有的样子：金额字段带 `$`，其余按原数。 */
function displayValue(key: string, quota: number, scale: QyUsdScale): string {
  return isUsdField(key, scale)
    ? qyFormatQuotaAsUsd(quota, scale)
    : String(quota)
}

/**
 * 只挑出真正改动过的键。未改动的键不该出现在 PUT 里，也不该污染审计。
 *
 * 比较发生在**额度整数**上，不是在界面那串 USD 上：草稿文本由
 * `qyQuotaDraftText` 生成、由 `qyQuotaDraftValue` 读回，这一对函数往返无损
 * （见 `lib/__tests__/quota-usd.test.ts`），所以运营什么都不改直接保存时
 * 这里一条改动都挑不出来，审计里也就不会多出一条什么都没改的门槛变更。
 *
 * 非法值直接跳过而不是带着一起发：后端整批校验，一个非法值会让**整个**
 * 请求 400，那意味着运营改对的另外五项也一起没保存。
 */
function collectChanges(
  config: QyTransferAdminConfig,
  draft: Record<string, string>,
  scale: QyUsdScale
): QyConfigChange[] {
  const out: QyConfigChange[] = []
  for (const [key, raw] of Object.entries(draft)) {
    const next = parseDraft(config, key, raw, scale)
    if (next == null) continue
    const current = effectiveValue(config.effective, key)
    if (next !== current) out.push({ key, from: current, to: next })
  }
  return out
}

function findInvalid(
  config: QyTransferAdminConfig,
  draft: Record<string, string>,
  scale: QyUsdScale
): string | null {
  for (const [key, raw] of Object.entries(draft)) {
    if (parseDraft(config, key, raw, scale) == null) return key
  }
  return null
}

/** 草稿文本 → 存储额度，并按后端下发的区间卡一次。非法返回 `null`。 */
function parseDraft(
  config: QyTransferAdminConfig,
  key: string,
  raw: string,
  scale: QyUsdScale
): number | null {
  const value = qyQuotaDraftValue(raw, scale, isUsdField(key, scale))
  if (value == null) return null
  const bound = config.bounds[key]
  if (bound != null && (value < bound.min || value > bound.max)) return null
  return value
}
