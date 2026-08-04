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
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import { qyAdminLotConfigQuery, updateQyLotConfig } from '../admin-lottery/api'
import type { QyLotAdminConfig, QyLotEffective } from '../admin-lottery/types'
import { QyKeyValue } from '../ops/qy-ops-ui'

/** 布尔型开关。它在 `qy_settings` 里存的是 `0` / `1`，不是 `true` / `false`。 */
const BOOLEAN_KEYS = new Set<keyof QyLotEffective>(['show_entry'])

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

/** 一处改动的复述：键、旧值、新值。确认弹窗与 PUT 请求共用它。 */
type QyLotChange = { key: string; from: string; to: string }

function EditableCard(props: { config: QyLotAdminConfig }) {
  const { config } = props
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [draft, setDraft] = useState<Record<string, string>>({})
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别人改过之后重新取到）时重置草稿：保留旧草稿会让管理员
  // 基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    const next: Record<string, string> = {}
    for (const key of config.editable_keys) {
      next[key] = String(effectiveValue(config.effective, key))
    }
    setDraft(next)
  }, [config])

  const save = useMutation({
    mutationFn: (patch: Record<string, string>) => updateQyLotConfig(patch),
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_lot_cfg_saved'))
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const changes: QyLotChange[] = config.editable_keys
    .map((key) => ({
      key,
      from: String(effectiveValue(config.effective, key)),
      to: draft[key] ?? '',
    }))
    .filter((change) => change.from !== change.to)

  const invalidKey = config.editable_keys.find((key) => {
    const value = draft[key]
    if (value == null || !/^\d+$/.test(value)) return true
    const bound = config.bounds[key]
    if (bound == null) return false
    const parsed = Number(value)
    return parsed < bound.min || parsed > bound.max
  })

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

          {config.editable_keys.map((key) => (
            <ConfigField
              key={key}
              fieldKey={key}
              value={draft[key] ?? ''}
              bound={config.bounds[key] ?? null}
              overriddenFrom={
                config.overrides[key] == null
                  ? null
                  : String(effectiveValue(config.yaml_defaults, key))
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
                {`${change.from} → ${change.to}`}
              </QyKeyValue>
            ))}
          </div>
        }
        onConfirm={() => {
          // 只提交改动过的键：全量提交会污染「谁在什么时候把奖品上限从 500 万
          // 改成 5000 万」的追溯轨迹。
          save.mutate(
            Object.fromEntries(changes.map((change) => [change.key, change.to]))
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
  overriddenFrom: string | null
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const id = useId()
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
      <Input
        id={id}
        inputMode='numeric'
        value={props.value}
        onChange={(event) =>
          props.onChange(event.target.value.replaceAll(/\D/g, ''))
        }
      />
      <div className='text-muted-foreground space-y-0.5 text-xs'>
        {hint !== '' && <p>{hint}</p>}
        {props.bound != null && (
          <p>
            {t('qy_common_amount_range', {
              min: props.bound.min,
              max: props.bound.max,
            })}
          </p>
        )}
        {props.overriddenFrom != null && (
          <p>{t('qy_lot_cfg_overridden', { value: props.overriddenFrom })}</p>
        )}
      </div>
    </div>
  )
}

function YamlCard(props: { config: QyLotAdminConfig }) {
  const { t } = useTranslation()
  const yaml = props.config.yaml_readonly

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
          {yaml.pay_password_threshold_quota}
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
          {yaml.max_stake_quota}
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
