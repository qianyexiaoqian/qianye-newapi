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
import { AlertTriangle, KeyRound, Plus, Trash2, Zap } from 'lucide-react'
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
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import {
  createQyAiChannel,
  deleteQyAiChannel,
  qyAiChannelsQuery,
  qyAiSettingsQuery,
  qyAiStatsQuery,
  testQyAiChannel,
  updateQyAiChannel,
  updateQyAiSetting,
} from './api'
import {
  qyAiBpsToPercentText,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  qyAiPercentTextToBps,
  type QyAiChannelDraft,
} from './lib/ai-review'
import type { QyAiChannel } from './types'

/**
 * AI 内容审核。
 *
 * ## 这一页要说清的三件事(它们都不是默认显然的)
 *
 * 1. **转发前审核会给用户加延迟。** 它必须同步 —— 命中要能拦住请求,而拦截
 *    这件事没有异步版本。所以超时上限就是"审核服务变慢时全站最多慢多少",
 *    这一点写在超时输入框旁边,不藏在文档里。
 * 2. **审核失败一律放行。** 超时、非法回复、5xx、渠道全挂 —— 全部放行。
 *    风控的价值是拦住违规内容,不是在自己抖动时把正常用户一起拦死。
 * 3. **被抽中的请求内容会被发送到第三方。** 这是本功能唯一一个对用户有外部
 *    影响的事实,所以它是一个**必须勾过一次**的闸,而不是一句提示文案 ——
 *    提示文案会在下一次改版里被顺手删掉。
 *
 * ## 规则本身不在这一页
 *
 * "什么算违规、命中之后怎么处置"仍然是**违规规则**那一页的事:AI 只是第七种
 * 匹配方式(`ai_review`),它的模式(影子/真实)、处置动作、扣费、计数权重、
 * 违规类型、作用域全部沿用既有那一套。这一页只管"送不送审、送到哪、花了多少"。
 * 把处置也搬过来就是造第二套规则体系,而两套规则必然在某一天给出相反的结论。
 */
export function QyAdminViolationAiReview() {
  const { t } = useTranslation()
  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_violation_ai_review')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/violation-rules' />}
        >
          {t('qy_ai_go_rules')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        {/* 三张卡各自持有自己的 query:设置、渠道、成本是三个独立的读接口,
            合成一个边界会让成本统计慢拖住渠道列表的渲染。 */}
        <div className='flex flex-col gap-4'>
          <AiSettingCard />
          <AiChannelsCard />
          <AiCostCard />
        </div>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

// ───────────────────────────── 设置 ─────────────────────────────

function AiSettingCard() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const query = useQuery(qyAiSettingsQuery())
  const [draft, setDraft] = useState<null | {
    enabled: boolean
    percent: string
    pre_timeout_ms: number
    async_timeout_ms: number
    prompt: string
    max_input_chars: number
    third_party_notice_ack: boolean
  }>(null)

  const data = query.data
  const current =
    draft ??
    (data
      ? {
          enabled: data.setting.enabled,
          percent: qyAiBpsToPercentText(data.setting.sample_rate_bps),
          pre_timeout_ms: data.setting.pre_timeout_ms,
          async_timeout_ms: data.setting.async_timeout_ms,
          prompt: data.setting.prompt,
          max_input_chars: data.setting.max_input_chars,
          third_party_notice_ack: data.setting.third_party_notice_ack,
        }
      : null)

  const save = useMutation({
    mutationFn: () => {
      if (!current) throw new Error('no draft')
      return updateQyAiSetting({
        enabled: current.enabled,
        sample_rate_bps: qyAiPercentTextToBps(current.percent),
        pre_timeout_ms: current.pre_timeout_ms,
        async_timeout_ms: current.async_timeout_ms,
        prompt: current.prompt,
        max_input_chars: current.max_input_chars,
        third_party_notice_ack: current.third_party_notice_ack,
      })
    },
    onSuccess: () => {
      toast.success(t('qy_ai_saved'))
      setDraft(null)
      void qc.invalidateQueries({
        queryKey: qyKeys.adminViolationAiSettings(),
      })
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  const eff = data?.effective

  return (
    <QyPageBoundary query={query}>
      {data && current && eff ? (
        <Card>
          <CardHeader>
            <CardTitle>{t('qy_ai_settings_title')}</CardTitle>
            <CardDescription>{t('qy_ai_settings_desc')}</CardDescription>
          </CardHeader>
          <CardContent className='flex flex-col gap-4'>
            {/* 用户内容出境 —— 本功能唯一一个对用户有外部影响的事实。
            它是闸而不是提示:未勾选时后端会 400 拒绝启用。 */}
            <Alert>
              <AlertTriangle className='size-4' />
              <AlertTitle>{t('qy_ai_third_party_title')}</AlertTitle>
              <AlertDescription>
                <p>{t('qy_ai_third_party_desc')}</p>
                <label className='mt-2 flex items-center gap-2'>
                  <Switch
                    checked={current.third_party_notice_ack}
                    onCheckedChange={(v) =>
                      setDraft({ ...current, third_party_notice_ack: v })
                    }
                  />
                  <span className='text-sm'>{t('qy_ai_third_party_ack')}</span>
                </label>
              </AlertDescription>
            </Alert>

            {/* 生效状态与表单回显不是一回事:抽样率填了 30% 但一个渠道都没启用时,
            表单显示 30%、实际生效是"完全不跑"。没有这一行,那个差别看不见。 */}
            {!eff.active && (
              <Alert>
                <AlertTriangle className='size-4' />
                <AlertTitle>{t('qy_ai_not_effective_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_ai_not_effective_desc', {
                    channels: eff.channels,
                  })}
                </AlertDescription>
              </Alert>
            )}

            <label className='flex items-center gap-2'>
              <Switch
                checked={current.enabled}
                onCheckedChange={(v) => setDraft({ ...current, enabled: v })}
              />
              <span className='text-sm font-medium'>{t('qy_ai_enabled')}</span>
            </label>

            <div className='grid gap-4 sm:grid-cols-2'>
              <div className='flex flex-col gap-1.5'>
                <Label>{t('qy_ai_sample_rate')}</Label>
                <Input
                  value={current.percent}
                  inputMode='decimal'
                  onChange={(e) =>
                    setDraft({ ...current, percent: e.target.value })
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ai_sample_rate_hint')}
                </p>
              </div>

              <div className='flex flex-col gap-1.5'>
                <Label>{t('qy_ai_pre_timeout')}</Label>
                <Input
                  type='number'
                  value={current.pre_timeout_ms}
                  onChange={(e) =>
                    setDraft({
                      ...current,
                      pre_timeout_ms: Number(e.target.value) || 0,
                    })
                  }
                />
                {/* 转发前审核的代价必须写在这里,而不是文档里。 */}
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ai_pre_timeout_hint', { max: eff.max_pre_timeout })}
                </p>
              </div>

              <div className='flex flex-col gap-1.5'>
                <Label>{t('qy_ai_async_timeout')}</Label>
                <Input
                  type='number'
                  value={current.async_timeout_ms}
                  onChange={(e) =>
                    setDraft({
                      ...current,
                      async_timeout_ms: Number(e.target.value) || 0,
                    })
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ai_async_timeout_hint', {
                    max: eff.max_async_timeout,
                  })}
                </p>
              </div>

              <div className='flex flex-col gap-1.5'>
                <Label>{t('qy_ai_max_input')}</Label>
                <Input
                  type='number'
                  value={current.max_input_chars}
                  onChange={(e) =>
                    setDraft({
                      ...current,
                      max_input_chars: Number(e.target.value) || 0,
                    })
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ai_max_input_hint')}
                </p>
              </div>
            </div>

            <div className='flex flex-col gap-1.5'>
              <Label>{t('qy_ai_prompt')}</Label>
              <Textarea
                rows={8}
                value={current.prompt}
                placeholder={data.default_prompt}
                onChange={(e) =>
                  setDraft({ ...current, prompt: e.target.value })
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_prompt_hint', {
                  categories: data.categories.join(', '),
                })}
              </p>
              <div>
                <Button
                  size='sm'
                  variant='outline'
                  onClick={() =>
                    setDraft({ ...current, prompt: data.default_prompt })
                  }
                >
                  {t('qy_ai_prompt_reset')}
                </Button>
              </div>
            </div>

            {/* 失败方向必须在界面上说清楚:运营据此判断"审核挂了会不会影响用户"。 */}
            <Alert>
              <AlertTitle>{t('qy_ai_failopen_title')}</AlertTitle>
              <AlertDescription>{t('qy_ai_failopen_desc')}</AlertDescription>
            </Alert>

            <div>
              <Button disabled={save.isPending} onClick={() => save.mutate()}>
                {t('qy_ai_save')}
              </Button>
            </div>
          </CardContent>
        </Card>
      ) : null}
    </QyPageBoundary>
  )
}

// ───────────────────────────── 渠道 ─────────────────────────────

function AiChannelsCard() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const query = useQuery(qyAiChannelsQuery())
  const [editing, setEditing] = useState<null | {
    id?: number
    draft: QyAiChannelDraft
  }>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiChannels() })
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiSettings() })
  }

  const save = useMutation({
    mutationFn: () => {
      if (!editing) throw new Error('no draft')
      const body = qyAiDraftToInput(editing.draft)
      return editing.id
        ? updateQyAiChannel(editing.id, body)
        : createQyAiChannel(body)
    },
    onSuccess: () => {
      toast.success(t('qy_ai_saved'))
      setEditing(null)
      invalidate()
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  const remove = useMutation({
    mutationFn: (id: number) => deleteQyAiChannel(id),
    onSuccess: () => {
      toast.success(t('qy_ai_channel_deleted'))
      invalidate()
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  const probe = useMutation({
    mutationFn: (id: number) => testQyAiChannel(id),
    onSuccess: (r) =>
      toast.success(
        t('qy_ai_test_result', {
          outcome: r.outcome,
          latency: r.latency_ms,
          tokens: r.tokens.total,
        })
      ),
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  const data = query.data

  return (
    <QyPageBoundary query={query}>
      {data ? (
        <Card>
          <CardHeader>
            <CardTitle>{t('qy_ai_channels_title')}</CardTitle>
            <CardDescription>{t('qy_ai_channels_desc')}</CardDescription>
          </CardHeader>
          <CardContent className='flex flex-col gap-4'>
            {/* 没配 violation.ai_review_key 时提前说出来,而不是等运营点了保存才 400。 */}
            {!data.key_configured && (
              <Alert>
                <KeyRound className='size-4' />
                <AlertTitle>{t('qy_ai_key_missing_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_ai_key_missing_desc')}
                </AlertDescription>
              </Alert>
            )}

            <div className='flex flex-col gap-2'>
              {data.items.map((ch) => (
                <ChannelRow
                  key={ch.id}
                  channel={ch}
                  onEdit={() =>
                    setEditing({ id: ch.id, draft: qyAiChannelToDraft(ch) })
                  }
                  onTest={() => probe.mutate(ch.id)}
                  onDelete={() => remove.mutate(ch.id)}
                />
              ))}
              {data.items.length === 0 && (
                <p className='text-muted-foreground text-sm'>
                  {t('qy_ai_channels_empty')}
                </p>
              )}
            </div>

            <div>
              <Button
                size='sm'
                variant='outline'
                onClick={() => setEditing({ draft: qyAiChannelToDraft() })}
              >
                <Plus className='size-4' />
                {t('qy_ai_channel_add')}
              </Button>
            </div>

            {editing && (
              <ChannelForm
                draft={editing.draft}
                existingHint={
                  editing.id
                    ? data.items.find((c) => c.id === editing.id)?.key_hint
                    : undefined
                }
                onChange={(d) => setEditing({ ...editing, draft: d })}
                onCancel={() => setEditing(null)}
                onSave={() => save.mutate()}
                saving={save.isPending}
              />
            )}
          </CardContent>
        </Card>
      ) : null}
    </QyPageBoundary>
  )
}

function ChannelRow({
  channel,
  onEdit,
  onTest,
  onDelete,
}: {
  channel: QyAiChannel
  onEdit: () => void
  onTest: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-wrap items-center gap-2 rounded-md border p-3'>
      <span className='font-medium'>{channel.name}</span>
      <Badge variant={channel.enabled ? 'default' : 'outline'}>
        {channel.enabled ? t('qy_ai_on') : t('qy_ai_off')}
      </Badge>
      <span className='text-muted-foreground text-xs'>{channel.base_url}</span>
      <span className='text-muted-foreground text-xs'>{channel.model}</span>
      {/* 密钥只显示掩码。接口本来就不下发明文,这里显示的是后端写入时算好的尾 4 位。 */}
      <span className='text-muted-foreground text-xs'>
        {channel.has_key
          ? t('qy_ai_key_set', { hint: channel.key_hint })
          : t('qy_ai_key_none')}
      </span>
      <span className='text-muted-foreground text-xs'>
        {t('qy_ai_weight_label', { weight: channel.weight })}
      </span>
      <div className='ms-auto flex gap-2'>
        <Button size='sm' variant='outline' onClick={onTest}>
          <Zap className='size-4' />
          {t('qy_ai_test')}
        </Button>
        <Button size='sm' variant='outline' onClick={onEdit}>
          {t('qy_ai_edit')}
        </Button>
        <Button size='sm' variant='outline' onClick={onDelete}>
          <Trash2 className='size-4' />
        </Button>
      </div>
    </div>
  )
}

function ChannelForm({
  draft,
  existingHint,
  onChange,
  onCancel,
  onSave,
  saving,
}: {
  draft: QyAiChannelDraft
  existingHint?: string
  onChange: (d: QyAiChannelDraft) => void
  onCancel: () => void
  onSave: () => void
  saving: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-col gap-3 rounded-md border p-3'>
      <div className='grid gap-3 sm:grid-cols-2'>
        <Field label={t('qy_ai_f_name')}>
          <Input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value })}
          />
        </Field>
        <Field label={t('qy_ai_f_model')}>
          <Input
            value={draft.model}
            onChange={(e) => onChange({ ...draft, model: e.target.value })}
          />
        </Field>
        <Field label={t('qy_ai_f_base_url')} hint={t('qy_ai_f_base_url_hint')}>
          <Input
            value={draft.base_url}
            onChange={(e) => onChange({ ...draft, base_url: e.target.value })}
          />
        </Field>
        {/* 密钥输入是**三态**:不碰 = 保持原值(占位符显示掩码),
            填空 = 清除,填新值 = 换新。见 lib/ai-review.ts。 */}
        <Field
          label={t('qy_ai_f_api_key')}
          hint={
            existingHint
              ? t('qy_ai_f_api_key_hint_existing', { hint: existingHint })
              : t('qy_ai_f_api_key_hint_new')
          }
        >
          <Input
            type='password'
            autoComplete='off'
            value={draft.apiKey ?? ''}
            placeholder={existingHint ?? ''}
            onChange={(e) => onChange({ ...draft, apiKey: e.target.value })}
          />
        </Field>
        <Field label={t('qy_ai_f_weight')}>
          <Input
            type='number'
            value={draft.weight}
            onChange={(e) =>
              onChange({ ...draft, weight: Number(e.target.value) || 1 })
            }
          />
        </Field>
        <Field label={t('qy_ai_f_timeout')} hint={t('qy_ai_f_timeout_hint')}>
          <Input
            type='number'
            value={draft.timeout_ms}
            onChange={(e) =>
              onChange({ ...draft, timeout_ms: Number(e.target.value) || 0 })
            }
          />
        </Field>
        <Field label={t('qy_ai_f_price_in')} hint={t('qy_ai_f_price_hint')}>
          <Input
            value={draft.price_in_per_m}
            onChange={(e) =>
              onChange({ ...draft, price_in_per_m: e.target.value })
            }
          />
        </Field>
        <Field label={t('qy_ai_f_price_out')} hint={t('qy_ai_f_price_hint')}>
          <Input
            value={draft.price_out_per_m}
            onChange={(e) =>
              onChange({ ...draft, price_out_per_m: e.target.value })
            }
          />
        </Field>
      </div>
      <label className='flex items-center gap-2'>
        <Switch
          checked={draft.enabled}
          onCheckedChange={(v) => onChange({ ...draft, enabled: v })}
        />
        <span className='text-sm'>{t('qy_ai_f_enabled')}</span>
      </label>
      <div className='flex gap-2'>
        <Button size='sm' disabled={saving} onClick={onSave}>
          {t('qy_ai_save')}
        </Button>
        <Button size='sm' variant='outline' onClick={onCancel}>
          {t('qy_ai_cancel')}
        </Button>
      </div>
    </div>
  )
}

function Field({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <div className='flex flex-col gap-1.5'>
      <Label>{label}</Label>
      {children}
      {hint && <p className='text-muted-foreground text-xs'>{hint}</p>}
    </div>
  )
}

// ───────────────────────────── 成本 ─────────────────────────────

/**
 * 成本可见性。
 *
 * 四个数字缺一不可:调用次数(按结局分)、token、花费、**算不出钱的次数**。
 * 最后一个最容易被省掉,而省掉它之后,一个 $0 的总额会被当成"没花钱",
 * 它可能其实是"全站渠道都没填单价"。
 */
function AiCostCard() {
  const { t } = useTranslation()
  const [days, setDays] = useState(7)
  const query = useQuery(qyAiStatsQuery(days))
  const s = query.data

  return (
    <QyPageBoundary query={query}>
      {s ? (
        <Card>
          <CardHeader>
            <CardTitle>{t('qy_ai_cost_title')}</CardTitle>
            <CardDescription>{t('qy_ai_cost_desc')}</CardDescription>
          </CardHeader>
          <CardContent className='flex flex-col gap-4'>
            <div className='flex gap-2'>
              {[1, 7, 30].map((d) => (
                <Button
                  key={d}
                  size='sm'
                  variant={d === days ? 'default' : 'outline'}
                  onClick={() => setDays(d)}
                >
                  {t('qy_ai_last_days', { days: d })}
                </Button>
              ))}
            </div>

            <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
              <Stat
                label={t('qy_ai_stat_calls')}
                value={String(s.total_calls)}
              />
              <Stat
                label={t('qy_ai_stat_tokens')}
                value={String(s.total_tokens)}
              />
              <Stat
                label={t('qy_ai_stat_cost')}
                value={`$${s.total_cost_usd}`}
              />
              <Stat
                label={t('qy_ai_stat_violations')}
                value={String(s.violated_calls)}
              />
            </div>

            {s.unpriced_calls > 0 && (
              <Alert>
                <AlertTriangle className='size-4' />
                <AlertTitle>{t('qy_ai_unpriced_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_ai_unpriced_desc', { count: s.unpriced_calls })}
                </AlertDescription>
              </Alert>
            )}

            {/* 按结局分组是排障的全部信息:timeout 找网络、bad_json 找提示词、
            upstream_error 找渠道、no_channel 找配置。 */}
            <div className='overflow-x-auto'>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='text-muted-foreground text-left'>
                    <th className='py-1'>{t('qy_ai_col_outcome')}</th>
                    <th className='py-1'>{t('qy_ai_col_count')}</th>
                    <th className='py-1'>{t('qy_ai_col_tokens')}</th>
                    <th className='py-1'>{t('qy_ai_col_cost')}</th>
                  </tr>
                </thead>
                <tbody>
                  {s.by_outcome.map((row) => (
                    <tr key={row.outcome} className='border-t'>
                      <td className='py-1'>
                        {t(`qy_ai_outcome_${row.outcome}` as never)}
                      </td>
                      <td className='py-1'>{row.count}</td>
                      <td className='py-1'>{row.total_tokens}</td>
                      <td className='py-1'>${row.cost_usd}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </CardContent>
        </Card>
      ) : null}
    </QyPageBoundary>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className='rounded-md border p-3'>
      <p className='text-muted-foreground text-xs'>{label}</p>
      <p className='text-lg font-semibold'>{value}</p>
    </div>
  )
}
