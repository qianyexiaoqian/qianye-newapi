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
import { Info, RefreshCw, ScrollText } from 'lucide-react'
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
import { Switch } from '@/components/ui/switch'
import { formatTimestampToDate } from '@/lib/format'

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
  qyRerunDailySettle,
  qyUpdateCommissionConfig,
} from './api'
import { QyCmGroupRatesCard } from './components/group-rates-card'
import { QyCmRegisterRewardCard } from './components/register-reward-card'
import {
  QY_COMMISSION_SECTIONS,
  qyCommissionFieldMeta,
  qyCommissionSectionOf,
  qyIsValidNullablePercent,
  qyIsValidPercent,
  qyNormalizePercent,
} from './lib/fields'
import type {
  QyCommissionAdminConfig,
  QyCommissionEffective,
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
 * 页面分五块：运营参数（按分区：给上线的下线充值 / 消费 / 兑换码比例、下线买套餐的
 * 在线 / 余额 / 兑换码三档、计佣口径开关、结算与风控）、下线注册奖、YAML 只读段、
 * 结算与入账调度、分组差异化费率表。一切给上线的返利都在这一页（星屑配置页只管
 * 用户自己得到的返还）。比例一律以百分比呈现与提交，不出现任何
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
                <div className='space-y-4'>
                  <QyCmRegisterRewardCard />
                  <YamlReadonlyCard config={config} />
                </div>
              </div>
              <DailySettleCard />
              <QyCmGroupRatesCard config={config} />
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
      next[key] = currentValue(config, key)
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
  // 开关键（"1" / "0"）。旧后端不下发 bool_keys 时为空集，不影响其余字段。
  const boolKeys = new Set(qyArray(config.bool_keys))
  const followsTopupLabel = t('qy_cm_f_redemption_follows', {
    percent: config.effective.topup_rate_percent,
  })
  const changes = collectChanges(
    config,
    draft,
    percentKeys,
    nullablePercentKeys,
    boolKeys
  )
  const invalidKey = findInvalid(
    config,
    draft,
    percentKeys,
    nullablePercentKeys,
    boolKeys
  )
  const editableKeys = qyArray(config.editable_keys)

  return (
    <>
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_cm_editable_title')}</CardTitle>
          <CardDescription>{t('qy_cm_editable_desc')}</CardDescription>
        </CardHeader>
        <CardContent className='space-y-6'>
          {/* 分区渲染：先「给上线返多少」（下线充值 / 消费 / 兑换码，与下线买套餐的
              三条渠道），再计佣口径开关，最后结算与风控。一张平铺的长表单里，运营
              找不到"余额买套餐返几个点"这种问题的答案。 */}
          {QY_COMMISSION_SECTIONS.map((section) => {
            const keys = editableKeys.filter(
              (key) => qyCommissionSectionOf(key) === section
            )
            if (keys.length === 0) return null
            return (
              <section
                key={section}
                className='space-y-4'
                data-qy-cm-section={section}
              >
                <div className='space-y-0.5 border-b pb-1.5'>
                  <h3 className='text-sm font-semibold'>
                    {t(`qy_cm_sec_${section}`)}
                  </h3>
                  <p className='text-muted-foreground text-xs'>
                    {t(`qy_cm_sec_${section}_desc`)}
                  </p>
                </div>
                {keys.map((key) =>
                  boolKeys.has(key) ? (
                    <SwitchField
                      key={key}
                      fieldKey={key}
                      checked={draft[key] === '1'}
                      overridden={config.overrides[key] != null}
                      onChange={(on) =>
                        setDraft((prev) => ({ ...prev, [key]: on ? '1' : '0' }))
                      }
                    />
                  ) : (
                    <ConfigField
                      key={key}
                      fieldKey={key}
                      value={draft[key] ?? ''}
                      isPercent={percentKeys.has(key)}
                      nullable={nullablePercentKeys.has(key)}
                      // 留空时旁边要写清楚"那实际是几个点"。只画一个空输入框的话，
                      // 运营看不出这一档实际生效的是什么，而那正是他点进来要看的数。
                      emptyMeansText={
                        nullablePercentKeys.has(key) ? followsTopupLabel : null
                      }
                      overridden={config.overrides[key] != null}
                      onChange={(value) =>
                        setDraft((prev) => ({ ...prev, [key]: value }))
                      }
                    />
                  )
                )}
              </section>
            )
          })}
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
                  {formatChangeValue(change.key, change.from, {
                    percentKeys,
                    boolKeys,
                    emptyLabel: followsTopupLabel,
                    onOff: [t('qy_common_on'), t('qy_common_off')],
                  })}{' '}
                  →{' '}
                  <strong>
                    {formatChangeValue(change.key, change.to, {
                      percentKeys,
                      boolKeys,
                      emptyLabel: followsTopupLabel,
                      onOff: [t('qy_common_on'), t('qy_common_off')],
                    })}
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

/**
 * 计佣口径开关。说明文案必须写清「打开之后哪笔钱不再计佣」—— 这三个开关以前只能
 * 改 YAML，运营在这里第一次看到它们，名字本身回答不了这个问题。
 */
function SwitchField(props: {
  fieldKey: string
  checked: boolean
  overridden: boolean
  onChange: (on: boolean) => void
}) {
  const { t } = useTranslation()
  const meta = qyCommissionFieldMeta(props.fieldKey)
  const id = `qy-cm-${props.fieldKey}`
  return (
    <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
      <div className='min-w-0 space-y-0.5'>
        <Label htmlFor={id}>
          {meta == null ? props.fieldKey : t(meta.labelKey)}
          {props.overridden && (
            <span className='text-muted-foreground ms-1 text-xs'>
              {t('qy_cm_overridden')}
            </span>
          )}
        </Label>
        <p className='text-muted-foreground text-xs'>
          {meta == null ? props.fieldKey : t(meta.hintKey)}
        </p>
      </div>
      <Switch
        id={id}
        checked={props.checked}
        onCheckedChange={(checked) => props.onChange(checked)}
      />
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
 * 一个键此刻的生效值，按草稿的字面量形状给出。开关是布尔，草稿里存 "1" / "0"
 * （与后端落库的形状一致）；其余原样转字符串。
 */
function currentValue(config: QyCommissionAdminConfig, key: string): string {
  const raw = config.effective[key as keyof QyCommissionEffective]
  if (typeof raw === 'boolean') return raw ? '1' : '0'
  return String(raw ?? '')
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
  nullablePercentKeys: Set<string>,
  boolKeys: Set<string>
): QyConfigChange[] {
  const out: QyConfigChange[] = []
  for (const [key, raw] of Object.entries(draft)) {
    const current = currentValue(config, key)
    if (boolKeys.has(key)) {
      if (raw !== current) out.push({ key, from: current, to: raw })
      continue
    }
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
  nullablePercentKeys: Set<string>,
  boolKeys: Set<string>
): string | null {
  // `config` 仍然进参：后端多下发一个前端不认识的键时，它的值就是判据 ——
  // 这里不认识的键按"原样字面量"处理，只要草稿等于当前值就不算非法。
  for (const [key, raw] of Object.entries(draft)) {
    if (boolKeys.has(key)) {
      if (raw !== '1' && raw !== '0') return key
      continue
    }
    if (percentKeys.has(key)) {
      const ok = nullablePercentKeys.has(key)
        ? qyIsValidNullablePercent(raw)
        : qyIsValidPercent(raw)
      if (!ok) return key
      continue
    }
    if (qyCommissionFieldMeta(key) == null) {
      if (raw.trim() !== currentValue(config, key)) return key
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
 * 改的是什么，而它改的恰恰是一整档比例。开关复述成「已开启 / 已关闭」，不是 1 / 0。
 */
function formatChangeValue(
  key: string,
  value: string,
  opts: {
    percentKeys: Set<string>
    boolKeys: Set<string>
    emptyLabel: string
    onOff: [string, string]
  }
): string {
  if (opts.boolKeys.has(key)) {
    return value === '1' ? opts.onOff[0] : opts.onOff[1]
  }
  if (opts.percentKeys.has(key)) {
    return value.trim() === '' ? opts.emptyLabel : `${value}%`
  }
  return value
}

/**
 * 结算与入账调度。
 *
 * 两段各答一个问题：
 *
 *   · **今天这一跑成了没有**（一日一结算，跑挂了当天剩下所有人的佣金都要等到明天）
 *   · **自动入账累计发了多少**（D-16 起入账是本地事务，没有在途、没有挂起，
 *     也就没有要人裁决的单子）
 */
function DailySettleCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminCommissionHealthQuery())
  const snapshot = query.data?.daily_settle
  const credit = query.data?.credit
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
            {/* 「跑了却处理 0 人」最常见的原因是佣金都还在持有期：一日一结算只吸收已成熟的行。
                不在这里说出来，连续几天的 0 看起来就像结算坏了。 */}
            {snapshot.holding != null && snapshot.holding.rows > 0 && (
              <Alert>
                <Info />
                <AlertTitle>{t('qy_cm_ds_holding_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_cm_ds_holding_desc', {
                    rows: snapshot.holding.rows,
                    amount: snapshot.holding.amount,
                    time: formatTimestampToDate(
                      snapshot.holding.earliest_mature_at
                    ),
                    days: snapshot.payout_day_offset,
                  })}
                </AlertDescription>
              </Alert>
            )}
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
      <QyDailySettleField
        label={t('qy_cm_ds_granted')}
        value={<QySdAmount amount={run.granted} />}
      />
      {run.remark !== '' && (
        <p className='text-muted-foreground text-xs'>{run.remark}</p>
      )}
    </div>
  )
}
