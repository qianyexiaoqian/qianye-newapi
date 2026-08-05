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
import { Info, ShieldAlert } from 'lucide-react'
import { useEffect, useId, useMemo, useState } from 'react'
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
import { qyAdminLotConfigQuery, updateQyLotConfig } from '../admin-lottery/api'
import type { QyLotAdminConfig, QyLotEffective } from '../admin-lottery/types'
import { QyKeyValue } from '../ops/qy-ops-ui'

/** 布尔型开关。它在 `qy_settings` 里存的是 `0` / `1`，不是 `true` / `false`。 */
const BOOLEAN_KEYS = new Set<keyof QyLotEffective>(['show_entry'])

/**
 * 金额字段：界面按 USD 录入与显示，存储仍是额度整数。
 *
 * 按 `_quota` 后缀判定而不是维护一张清单：后端这一段的键名一律以它结尾
 * （`max_total_prize_quota` / `large_prize_alert_quota` / YAML 只读的
 * `max_stake_quota`、`pay_password_threshold_quota`），新增一个金额键时
 * 界面自动跟随，不会漏成一个还要数零的输入框。
 */
function isUsdField(key: string, scale: QyUsdScale): boolean {
  return scale.usable && key.endsWith('_quota')
}

/**
 * 抽奖 / 竞猜配置。
 *
 * 需求原文「系统设置前端是否显示」的落点就是这一页最上面那个开关。它与其余几项
 * 一样住在 `qy_settings`（scope=lottery），改一次影响之后每一场 —— 所以这一页
 * 并进上游「系统设置」抽屉，而不是留在根侧栏。
 *
 * ## 关掉展示 ≠ 关掉功能
 *
 * `show_entry=0` 只让**用户侧**的两行入口消失，进行中的活动照常封盘、开奖、
 * 派奖，已参与的用户也照常能通过链接进详情页看结果。真要停掉整个功能得改 YAML
 * 的 `enabled` —— 那是 YAML 只读段，不在这一页。界面必须把这个界线说清楚，
 * 否则运营会以为自己把活动停了。
 *
 * ## 为什么不在前端复现跨字段规则
 *
 * `default_guess_fee_bps ≤ max_guess_fee_bps` 这类约束由后端 `ValidateLottery`
 * 唯一定义，YAML 加载与这个接口共用它。前端再写一遍就是第二份，而两份规则里
 * 先漂移的通常是没人再看的这份。非法组合会得到一个带原因的 400。
 */
export function QyAdminLotteryConfig() {
  const { t } = useTranslation()
  const query = useQuery(qyAdminLotConfigQuery())

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_lottery_config')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/lottery' />}
        >
          {t('qy_nav_a_lottery')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {query.data != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)] lg:items-start'>
              <EditableCard config={query.data} />
              <YamlCard config={query.data} />
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
 * `from` / `to` 是**存储用的整数**（金额字段就是额度），不是界面上那串 USD。
 * 请求体与审计认的都是这个数。
 */
type QyLotChange = { key: string; from: number; to: number }

function EditableCard(props: { config: QyLotAdminConfig }) {
  const { config } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // 奖品总额上限这类字段原来要填 500000000 —— 写错一个零就是直接资损。
  // 换成 USD 录入，存储不变。换算率来自站点展示配置。
  const quotaPerUnit = useSystemConfigStore(
    (state) => state.config.currency.quotaPerUnit
  )
  const scale = useMemo(() => qyUsdScale(quotaPerUnit), [quotaPerUnit])

  const [draft, setDraft] = useState<Record<string, string>>({})
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿：保留旧草稿会让管理员
  // 基于过期基线做修改，把别人刚改的值又覆盖回去。
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

  const save = useMutation({
    mutationFn: (patch: Record<string, string>) => updateQyLotConfig(patch),
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_lot_cfg_saved'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  // 比较发生在**存储用的整数**上，不是界面那串 USD：草稿文本由
  // `qyQuotaDraftText` 生成、由 `qyQuotaDraftValue` 读回，往返无损（见
  // `lib/__tests__/quota-usd.test.ts`），所以运营什么都不改直接保存时这里
  // 一条改动都挑不出来，审计里也就不会多出一条什么都没改的配置变更。
  const changes: QyLotChange[] = config.editable_keys
    .map((key) => ({
      key,
      from: effectiveValue(config.effective, key),
      to: parseDraft(config, key, draft[key] ?? '', scale),
    }))
    .filter(
      (change): change is QyLotChange =>
        change.to != null && change.to !== change.from
    )

  const invalidKey = config.editable_keys.find(
    (key) => parseDraft(config, key, draft[key] ?? '', scale) == null
  )

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_lot_cfg_editable_title')}</CardTitle>
          <CardDescription>{t('qy_lot_cfg_editable_desc')}</CardDescription>
        </CardHeader>
        <CardContent className='space-y-4'>
          {!config.effective_valid && (
            // 只可能来自有人绕过接口直接改了 `qy_settings`。此时后端已经把功能
            // 停掉了，而这一页是修复那一行的唯一界面 —— 不说出来，运营只会看到
            // 「抽奖怎么用不了了」。
            <Alert variant='destructive'>
              <ShieldAlert />
              <AlertTitle>{t('qy_lot_cfg_invalid_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_lot_cfg_invalid_desc')}
              </AlertDescription>
            </Alert>
          )}

          <Alert>
            <Info />
            <AlertTitle>{t('qy_lot_cfg_show_entry_title')}</AlertTitle>
            <AlertDescription>
              {t('qy_lot_cfg_show_entry_note')}
            </AlertDescription>
          </Alert>

          <QyUsdScaleNotice scale={scale} />

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
              changes.length === 0 || invalidKey != null || save.isPending
            }
            onClick={() => setConfirmOpen(true)}
          >
            {t('qy_lot_cfg_save')}
          </Button>
          {invalidKey != null && (
            <p className='text-destructive text-sm'>
              {t('qy_lot_cfg_invalid_field', { key: invalidKey })}
            </p>
          )}
        </CardContent>
      </Card>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_lot_cfg_confirm_title')}
        description={t('qy_lot_cfg_confirm_desc')}
        isLoading={save.isPending}
        details={
          <div>
            {changes.map((change) => (
              <QyKeyValue
                key={change.key}
                label={t(`qy_lot_cfg_k_${change.key}`, {
                  defaultValue: change.key,
                })}
              >
                {`${displayValue(change.key, change.from, scale)} → ${displayValue(change.key, change.to, scale)}`}
              </QyKeyValue>
            ))}
          </div>
        }
        onConfirm={() => {
          // 只提交改动过的键：全量提交会污染「谁在什么时候把奖品上限从 500 万
          // 改成 5000 万」的追溯轨迹。
          //
          // 请求体仍然是**存储用的整数**：界面只是换了个单位让人填，存储层、
          // 后端区间校验与审计快照一个字都没动。
          save.mutate(
            Object.fromEntries(
              changes.map((change) => [change.key, String(change.to)])
            )
          )
        }}
      />
    </>
  )
}

function ConfigField(props: {
  fieldKey: string
  value: string
  bound: { min: number; max: number } | null
  scale: QyUsdScale
  /** 该项已被运营覆盖时，YAML 里的原值（存储用的整数）；未覆盖为 `null`。 */
  overriddenFrom: number | null
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const asUsd = isUsdField(props.fieldKey, props.scale)
  const parsed = qyQuotaDraftValue(props.value, props.scale, asUsd)
  // 字段级判定必须与保存判定（parseDraft）**同一口径**，区间也算进来。
  //
  // 少了区间那一半的表现是：填一个超出后端区间的金额，输入框不标红、下面还照常
  // 渲染一行"= N 额度"，看上去这个值已经被接受了，只有保存键悄悄置灰 ——
  // 而这一页的字段是「单场奖品总额上限」这一类，抽奖派奖是净增发，
  // 写错一个零就是直接资损。运营会在几个字段里逐个试，或者以为页面坏了。
  // 划转页同名逻辑本来就是这个口径，两页不该各说各的。
  const outOfRange =
    parsed != null &&
    props.bound != null &&
    (parsed < props.bound.min || parsed > props.bound.max)
  const invalid = parsed == null || outOfRange
  const label = t(`qy_lot_cfg_k_${props.fieldKey}`, {
    defaultValue: props.fieldKey,
  })
  const hint = t(`qy_lot_cfg_h_${props.fieldKey}`, { defaultValue: '' })

  // 布尔项渲染成开关而不是让运营去填 0 / 1：「前端是否显示」是这一页最常被
  // 点到的一项，做成数字输入框既难认又容易填错。
  if (BOOLEAN_KEYS.has(props.fieldKey as keyof QyLotEffective)) {
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

  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{label}</Label>
      <div className='flex items-center gap-2'>
        <Input
          id={id}
          inputMode={asUsd ? 'decimal' : 'numeric'}
          value={props.value}
          aria-invalid={invalid}
          onChange={(event) =>
            // 金额字段要放行小数点，否则 USD 根本填不进去；非金额字段维持
            // 原来的「只留数字」。真正的判定在 parseDraft，这里只是少让人
            // 敲出一串明显没用的字符。
            props.onChange(
              asUsd
                ? event.target.value.replaceAll(/[^\d.]/g, '')
                : event.target.value.replaceAll(/\D/g, '')
            )
          }
        />
        <span
          className='text-muted-foreground shrink-0 text-sm'
          aria-hidden='true'
        >
          {asUsd ? 'USD' : ''}
        </span>
      </div>
      <div className='text-muted-foreground space-y-0.5 text-xs'>
        {hint !== '' && <p>{hint}</p>}
        {/* 存进库的仍然是额度整数。显示出来，运营翻审计与后端日志时才对得上号。
            超出区间时**不显示**这一行：一个存不进去的值旁边写着"= N 额度 存"，
            读起来就是"已经接受了"。 */}
        {asUsd && parsed != null && !outOfRange && (
          <p>
            {t('qy_cfg_usd_equals_quota', { quota: parsed })}{' '}
            {t('qy_cfg_usd_step_hint', {
              step: qyFormatQuotaAsUsd(1, props.scale),
            })}
          </p>
        )}
        {props.bound != null && (
          // 超出区间时把区间这一行标红：唯一的另一处线索是保存键下面那句带
          // **原始键名**的提示，而键名不是字段上方那个中文标签，运营对不上号。
          <p className={outOfRange ? 'text-destructive' : undefined}>
            {t('qy_common_amount_range', {
              min: displayValue(props.fieldKey, props.bound.min, props.scale),
              max: displayValue(props.fieldKey, props.bound.max, props.scale),
            })}
          </p>
        )}
        {props.overriddenFrom != null && (
          <p>
            {t('qy_lot_cfg_overridden', {
              value: displayValue(
                props.fieldKey,
                props.overriddenFrom,
                props.scale
              ),
            })}
          </p>
        )}
      </div>
    </div>
  )
}

/** 把一个存储用的整数渲染成界面上该有的样子：金额字段带 `$`，其余按原数。 */
function displayValue(key: string, value: number, scale: QyUsdScale): string {
  return isUsdField(key, scale)
    ? qyFormatQuotaAsUsd(value, scale)
    : String(value)
}

/** 草稿文本 → 存储用的整数，并按后端下发的区间卡一次。非法返回 `null`。 */
function parseDraft(
  config: QyLotAdminConfig,
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

function YamlCard(props: { config: QyLotAdminConfig }) {
  const { t } = useTranslation()
  const yaml = props.config.yaml_readonly
  const quotaPerUnit = useSystemConfigStore(
    (state) => state.config.currency.quotaPerUnit
  )
  const scale = useMemo(() => qyUsdScale(quotaPerUnit), [quotaPerUnit])

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_lot_cfg_yaml_title')}</CardTitle>
        <CardDescription>{t('qy_lot_cfg_yaml_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <QyKeyValue label={t('qy_lot_cfg_k_enabled')}>
          {yaml.enabled ? t('qy_common_on') : t('qy_common_off')}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_proof_public')}>
          {yaml.proof_public ? t('qy_common_on') : t('qy_common_off')}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_pay_pwd_threshold')}>
          {displayValue(
            'pay_password_threshold_quota',
            yaml.pay_password_threshold_quota,
            scale
          )}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_close_grace')}>
          {yaml.entry_close_grace_seconds}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_reveal_delay')}>
          {yaml.reveal_delay_seconds}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_payout_max_attempts')}>
          {yaml.payout_max_attempts}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_max_total_entries_hard')}>
          {yaml.max_total_entries_hard}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_max_prize_tiers')}>
          {yaml.max_prize_tiers}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_max_options')}>
          {yaml.max_options}
        </QyKeyValue>
        {/* 单笔扣款上限必须看得见：它是"一次报名/投注最多能从主额度扣走多少"
            的硬闸门，而竞猜的单注上限没填时兜的就是它。 */}
        <QyKeyValue label={t('qy_lot_cfg_k_max_stake_quota')}>
          {displayValue('max_stake_quota', yaml.max_stake_quota, scale)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_spend_lookback')}>
          {yaml.spend_max_lookback_days}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_cfg_k_spend_ready_from')}>
          {yaml.spend_ready_from === 0
            ? t('qy_lot_cfg_spend_not_ready')
            : String(yaml.spend_ready_from)}
        </QyKeyValue>
        {yaml.spend_ready_from === 0 && (
          // 日消费表还没回填完，此时「近 N 日消费」条件会全员误拒。
          // 这句话必须在配置页出现，而不是等运营创建活动时被 400 顶回来。
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('qy_lot_cfg_spend_not_ready_note')}
          </p>
        )}
      </CardContent>
    </Card>
  )
}

/** 按键读取生效值。未知键返回 0 —— 后端加了新键而前端还没认识它时不该崩。 */
function effectiveValue(effective: QyLotEffective, key: string): number {
  const record = effective as unknown as Record<string, number | undefined>
  return record[key] ?? 0
}
