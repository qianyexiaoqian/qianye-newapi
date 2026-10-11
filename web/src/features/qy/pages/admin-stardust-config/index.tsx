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
import type { TFunction } from 'i18next'
import { Info } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
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
import { Switch } from '@/components/ui/switch'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useStardustName } from '../../hooks/use-stardust-name'
import { formatQyQuotaLedger } from '../../lib/format'
import { formatSdWithUnit } from '../../lib/format-sd'
import { qyKeys } from '../../lib/query-keys'
import { QyKeyValue } from '../ops/qy-ops-ui'
import { qySdBpsPercent } from '../stardust/lib/display'
import { qyAdminStardustConfigQuery, updateQyStardustConfig } from './api'
import { QySdGroupRatesCard } from './components/group-rates-card'
import { QySdPlanRewardCard } from './components/plan-reward-card'
import {
  QY_SD_BOOLEAN_KEYS,
  QY_SD_COMMISSION_PAGE_KEYS,
  QY_SD_NAME_KEY,
  QY_SD_NAME_MAX_RUNES,
  qySdConfigChanges,
  qySdConfigPatch,
  qySdEffectiveValue,
  qySdInvalidKey,
  qySdIsBpsKey,
  qySdIsStardustKey,
  qySdParseDraft,
} from './lib/draft'
import { qySdAdminErrorMessage } from './lib/errors'
import type { QyStardustAdminConfig, QyStardustBound } from './types'

/**
 * 星屑配置：货币名 / 入口显隐 / 个人消费返比例 / 分组费率覆盖 / 买家本人的套餐返。
 *
 * 这一页只管**用户自己**得到的返还。一切给上线的返利（下线充值 / 兑换码 / 消费、
 * 下线注册奖、下线买套餐）都在「佣金配置」—— 注册奖的键仍存在星屑的运营参数里，
 * 但只在佣金配置页上编辑（`QY_SD_COMMISSION_PAGE_KEYS`）。
 *
 * 形状照 `admin-lottery-config`：可写段一张卡（按后端下发的 `editable_keys`
 * 渲染、按 `bounds` 校验、保存前复述改动键、只 PUT 改动键），YAML 只读段一张卡。
 * 同页再挂两块：按用户分组覆盖的比例表、按套餐配的返还。
 *
 * ## 为什么保存后全量失效
 *
 * 货币名一改，引导端点下发的 `stardust.name` 也变了：全站每一个星屑金额旁边的
 * 单位都要跟着换，而那些视图散在十几个页面里，只有 `qyKeys.all` 能一次冲干净。
 */
export function QyAdminStardustConfig() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminStardustConfigQuery())
  const bpsBound = query.data?.bounds.consume_bps ?? null

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_stardust_config')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/stardust' />}
        >
          {t('qy_nav_a_stardust')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {query.data != null && (
            <div className='space-y-4'>
              <div className='grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)] lg:items-start'>
                <EditableCard config={query.data} />
                <YamlCard config={query.data} />
              </div>
              <div className='grid gap-4 lg:grid-cols-2 lg:items-start'>
                <QySdGroupRatesCard bpsBound={bpsBound} />
                <QySdPlanRewardCard bpsBound={bpsBound} />
              </div>
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

function EditableCard(props: { config: QyStardustAdminConfig }) {
  const { config } = props
  const { t } = useTranslation()
  const unit = useStardustName()
  const queryClient = useQueryClient()

  const [draft, setDraft] = useState<Record<string, string>>({})
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿：保留旧草稿会让管理员
  // 基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    const next: Record<string, string> = {}
    for (const key of config.editable_keys) {
      next[key] = String(qySdEffectiveValue(config, key))
    }
    setDraft(next)
  }, [config])

  const save = useMutation({
    mutationFn: updateQyStardustConfig,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_sdadm_cfg_saved'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qySdAdminErrorMessage(error, t)),
  })

  const changes = qySdConfigChanges(config, draft)
  const invalidKey = qySdInvalidKey(config, draft)

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_sdadm_cfg_editable_title')}</CardTitle>
          <CardDescription>{t('qy_sdadm_cfg_editable_desc')}</CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          {/* 一切给上线的返利（下线充值 / 兑换码 / 消费 / 注册 / 买套餐）都在佣金配置。
              运营以前在这一页配过其中几档，这里要直说它们搬去了哪里。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_sdadm_cfg_invite_moved')}
          </p>

          <Alert>
            <Info />
            <AlertTitle>{t('qy_sdadm_cfg_show_entry_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_sdadm_cfg_show_entry_note')}
            </AlertDescription>
          </Alert>

          {config.editable_keys
            .filter((key) => !QY_SD_COMMISSION_PAGE_KEYS.has(key))
            .map((key) => (
              <ConfigField
                key={key}
                fieldKey={key}
                value={draft[key] ?? ''}
                bound={config.bounds[key] ?? null}
                unit={unit}
                overridden={config.overrides[key] != null}
                valid={qySdParseDraft(config, key, draft[key] ?? '') != null}
                onChange={(value) =>
                  setDraft((prev) => ({ ...prev, [key]: value }))
                }
              />
            ))}

          <Button
            disabled={
              changes.length === 0 || invalidKey != null || save.isPending
            }
            onClick={() => setConfirmOpen(true)}
          >
            {t('qy_sdadm_cfg_save')}
          </Button>
          {invalidKey != null && (
            <p className='text-destructive text-sm'>
              {t('qy_sdadm_cfg_invalid_field', {
                key: t(`qy_sdadm_cfg_k_${invalidKey}`, {
                  defaultValue: invalidKey,
                }),
              })}
            </p>
          )}
        </CardContent>
      </Card>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_sdadm_cfg_confirm_title')}
        description={t('qy_sdadm_cfg_confirm_desc')}
        isLoading={save.isPending}
        details={
          <div>
            {changes.map((change) => (
              <QyKeyValue
                key={change.key}
                label={t(`qy_sdadm_cfg_k_${change.key}`, {
                  defaultValue: change.key,
                })}
              >
                {`${displayValue(change.key, change.from, unit, t)} → ${displayValue(change.key, change.to, unit, t)}`}
              </QyKeyValue>
            ))}
          </div>
        }
        // 只提交改动过的键：全量提交会污染「谁在什么时候把比例从 100% 改成
        // 120%」的追溯轨迹 —— 审计里每一条配置变更都该是真的变了。
        onConfirm={() => save.mutate(qySdConfigPatch(changes))}
      />
    </>
  )
}

function ConfigField(props: {
  fieldKey: string
  value: string
  bound: QyStardustBound | null
  /** 星屑单位名，只给金额字段印在数字旁边。 */
  unit: string
  /** 该项已被运营覆盖（`overrides` 里有它），此刻生效的不再是配置文件的基线。 */
  overridden: boolean
  /** 与保存判定同一口径的字段级合法性（区间 / 单位名长度）。 */
  valid: boolean
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const label = t(`qy_sdadm_cfg_k_${props.fieldKey}`, {
    defaultValue: props.fieldKey,
  })
  const hint = t(`qy_sdadm_cfg_h_${props.fieldKey}`, { defaultValue: '' })

  // 布尔项渲染成开关而不是让运营去填 0 / 1：「前端是否显示」是这一页最常被
  // 点到的一项，做成数字输入框既难认又容易填错。
  if (QY_SD_BOOLEAN_KEYS.has(props.fieldKey)) {
    return (
      <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
        <div className='min-w-0'>
          <Label htmlFor={id}>{label}</Label>
          {hint !== '' && (
            <p className='text-muted-foreground text-xs'>{hint}</p>
          )}
        </div>
        <Switch
          id={id}
          checked={props.value !== '0' && props.value !== ''}
          onCheckedChange={(checked) => props.onChange(checked ? '1' : '0')}
        />
      </div>
    )
  }

  if (props.fieldKey === QY_SD_NAME_KEY) {
    return (
      <div className='space-y-1'>
        <Label htmlFor={id}>{label}</Label>
        <Input
          id={id}
          value={props.value}
          maxLength={QY_SD_NAME_MAX_RUNES * 2}
          aria-invalid={!props.valid}
          onChange={(event) => props.onChange(event.target.value)}
        />
        <div className='text-muted-foreground space-y-0.5 text-xs'>
          {hint !== '' && <p>{hint}</p>}
          <p className={props.valid ? undefined : 'text-destructive'}>
            {t('qy_sdadm_cfg_name_rule', { max: QY_SD_NAME_MAX_RUNES })}
          </p>
          {props.overridden && <p>{t('qy_sdadm_cfg_overridden')}</p>}
        </div>
      </div>
    )
  }

  const asStardust = qySdIsStardustKey(props.fieldKey)
  const asBps = qySdIsBpsKey(props.fieldKey)
  const numeric = /^\d+$/.test(props.value) ? Number(props.value) : null

  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{label}</Label>
      <div className='flex items-center gap-2'>
        <Input
          id={id}
          inputMode='numeric'
          value={props.value}
          aria-invalid={!props.valid}
          // 全部数值字段都是整数（万分比、天数、星屑）：只留数字。真正的判定在
          // qySdParseDraft，这里只是少让人敲出一串明显没用的字符。
          onChange={(event) =>
            props.onChange(event.target.value.replaceAll(/\D/g, ''))
          }
        />
        <span
          className='text-muted-foreground shrink-0 text-sm'
          aria-hidden='true'
        >
          {asStardust ? props.unit : ''}
        </span>
      </div>
      <div className='text-muted-foreground space-y-0.5 text-xs'>
        {hint !== '' && <p>{hint}</p>}
        {asBps && numeric != null && (
          <p>
            {t('qy_sdadm_cfg_bps_hint', { percent: qySdBpsPercent(numeric) })}
          </p>
        )}
        {props.bound != null && (
          // 超出区间时把区间这一行标红：另一处线索是保存键下面那句提示，
          // 而运营的眼睛此刻停在这一格上。
          <p className={props.valid ? undefined : 'text-destructive'}>
            {t('qy_common_amount_range', {
              min: boundText(props.fieldKey, props.bound.lo, props.unit),
              max: boundText(props.fieldKey, props.bound.hi, props.unit),
            })}
          </p>
        )}
        {props.overridden && <p>{t('qy_sdadm_cfg_overridden')}</p>}
      </div>
    </div>
  )
}

/** 区间端点的渲染：金额字段带单位，其余按原数。 */
function boundText(key: string, value: number, unit: string): string {
  return qySdIsStardustKey(key) ? formatSdWithUnit(value, unit) : String(value)
}

/** 把一个存储值渲染成界面上该有的样子：开关说开/关、金额带单位、万分比带百分数。 */
function displayValue(
  key: string,
  value: number | string,
  unit: string,
  t: TFunction
): string {
  if (typeof value === 'string') return value
  if (QY_SD_BOOLEAN_KEYS.has(key)) {
    return value === 1 ? t('qy_common_on') : t('qy_common_off')
  }
  if (qySdIsBpsKey(key)) {
    return t('qy_sdadm_cfg_bps_value', {
      bps: value,
      percent: qySdBpsPercent(value),
    })
  }
  return boundText(key, value, unit)
}

function YamlCard(props: { config: QyStardustAdminConfig }) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const yaml = props.config.yaml_readonly
  const yesNo = (value: boolean) =>
    value ? t('qy_sdadm_cfg_bool_true') : t('qy_sdadm_cfg_bool_false')

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sdadm_cfg_yaml_title')}</CardTitle>
        <CardDescription>{t('qy_sdadm_cfg_yaml_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        {/* 刻度：运营要的是"1 星屑现在等于多少额度"，所以额度按站内口径印出来，
            原始整数跟在旁边 —— 那是填进配置文件的数。 */}
        <QyKeyValue label={t('qy_sdadm_cfg_k_quota_per_unit', { unit })}>
          {t('qy_common_quota_with_amount', {
            quota: yaml.quota_per_unit,
            amount: formatQyQuotaLedger(yaml.quota_per_unit),
          })}
        </QyKeyValue>
        <QyKeyValue label={t('qy_sdadm_cfg_k_settle_delay_minutes')}>
          {t('qy_sdadm_cfg_minutes', { n: yaml.settle_delay_minutes })}
        </QyKeyValue>
        <QyKeyValue label={t('qy_sdadm_cfg_k_settle_interval_seconds')}>
          {t('qy_sdadm_cfg_seconds', { n: yaml.settle_interval_seconds })}
        </QyKeyValue>
        <QyKeyValue label={t('qy_sdadm_cfg_k_exclude_subscription_consume')}>
          {yesNo(yaml.exclude_subscription_consume)}
        </QyKeyValue>
        {/* 手调上限配成 0 就是"关掉手调"（后端 fail-closed），要明说，
            否则运营会在手调弹窗里反复吃 400。 */}
        <QyKeyValue label={t('qy_sdadm_cfg_k_max_manual_adjust')}>
          {yaml.max_manual_adjust === 0
            ? t('qy_sdadm_cfg_adjust_off')
            : formatSdWithUnit(yaml.max_manual_adjust, unit)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_sdadm_cfg_k_compliance_confirmed')}>
          {yesNo(yaml.compliance_confirmed)}
        </QyKeyValue>
        <p className='text-muted-foreground mt-2 text-xs'>
          {t('qy_sdadm_cfg_yaml_note')}
        </p>
      </CardContent>
    </Card>
  )
}
