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
import {
  AlertTriangle,
  ChevronRight,
  KeyRound,
  Plus,
  Trash2,
  Zap,
} from 'lucide-react'
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
import { Checkbox } from '@/components/ui/checkbox'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { ComboboxInput } from '@/components/ui/combobox-input'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../components/qy-responsive-dialog'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import {
  qyGroupOptionLabel,
  qyGroupOptionsQuery,
  qyNormalizeGroupName,
  qyUnknownGroupNames,
} from '../../lib/group-options'
import { qyKeys } from '../../lib/query-keys'
import { qyAdminViolationCategoriesQuery } from '../admin-violation-categories/api'
import {
  createQyAiChannel,
  deleteQyAiChannel,
  deleteQyAiScope,
  qyAiChannelsQuery,
  qyAiScopesQuery,
  qyAiSettingsQuery,
  qyAiStatsQuery,
  qyCyberSettingsQuery,
  testQyAiChannel,
  updateQyAiChannel,
  updateQyAiSetting,
  updateQyCyberSetting,
  upsertQyAiScope,
} from './api'
import {
  QY_AI_EMAIL_VARS,
  qyAiAppendScopeGroup,
  qyAiBpsToPercentText,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  qyAiPromptCategoryIssues,
  qyAiRenderPrompt,
  qyAiPromptIsDefault,
  qyAiScopeAudience,
  qyAiScopeChannelState,
  qyAiScopeDraftToInput,
  qyAiScopeGroupBindingError,
  qyAiScopeHasFakeSeparator,
  qyAiScopeRowKind,
  qyAiScopeToDraft,
  qyAiSplitScopeList,
  qyAiApplyProtocol,
  qyAiGuardShownIds,
  qyAiIsGuardProtocol,
  qyAiProtocolHasCategories,
  qyAiRenderEmailPreview,
  type QyAiChannelDraft,
  type QyAiScopeChannelState,
  type QyAiScopeDraft,
  type QyAiScopeGroupBindingError,
  type QyAiScopeRowKind,
} from './lib/ai-review'
import {
  QY_AI_GRANITE_RISKS,
  type QyAiChannel,
  type QyAiChannelTestResult,
  type QyAiGraniteRisk,
  type QyAiGuardCategory,
  type QyAiProtocol,
  type QyAiScope,
  type QyAiScopeSummaryRow,
} from './types'

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
        {/* 逐条明细在自己那一页上(「风控与审计」组里,挨着违规记录)。
            这一页管的是"送不送审、送到哪、留多久",那一页回答"到底审了什么"——
            两种读者、两种打开频率,所以是两页而不是这一页底下的第五张卡。 */}
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/violation-ai-logs' />}
        >
          {t('qy_ai_go_logs')}
        </Button>
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
          {/* 作用域排在设置之后、渠道之前:先决定"审谁、审多少",
              再决定"送到哪里"。反过来排的话,第一次打开这一页的人会先
              配好渠道,然后以为功能已经在跑了。 */}
          <AiScopeCard />
          <AiChannelsCard />
          <AiCostCard />
          {/* cyber 会话屏蔽:与 AI 审核是两套东西(那个判内容,这个认上游拒绝码
              并拉黑整条会话)。放在最后,作为一块独立的附加防护。 */}
          <CyberBlockCard />
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
    pre_timeout_ms: number
    async_timeout_ms: number
    max_input_chars: number
    third_party_notice_ack: boolean
    log_content: boolean
    log_content_violation_full: boolean
    log_content_max_chars: number
    log_retention_days: number
  }>(null)

  const data = query.data
  // 判据是 `data?.setting` 而不是 `data`:这一页由四张卡拼成,它们同在一棵
  // 树上、共用路由那一层的错误边界。任何一张卡在渲染中途抛异常,整页都会被
  // 换成错误态 —— 一份少了 `setting` 的降级响应会连带打掉作用域卡与渠道卡,
  // 而运营看到的是"AI 审核这一页打不开了"。少一张卡远好过少一整页。
  const current =
    draft ??
    (data?.setting
      ? {
          enabled: data.setting.enabled,
          pre_timeout_ms: data.setting.pre_timeout_ms,
          async_timeout_ms: data.setting.async_timeout_ms,
          max_input_chars: data.setting.max_input_chars,
          third_party_notice_ack: data.setting.third_party_notice_ack,
          log_content: data.setting.log_content,
          log_content_violation_full: data.setting.log_content_violation_full,
          log_content_max_chars: data.setting.log_content_max_chars,
          log_retention_days: data.setting.log_retention_days,
        }
      : null)

  const save = useMutation({
    mutationFn: () => {
      if (!current || !data) throw new Error('no draft')
      return updateQyAiSetting({
        enabled: current.enabled,
        pre_timeout_ms: current.pre_timeout_ms,
        async_timeout_ms: current.async_timeout_ms,
        max_input_chars: current.max_input_chars,
        third_party_notice_ack: current.third_party_notice_ack,
        log_content: current.log_content,
        log_content_violation_full: current.log_content_violation_full,
        log_content_max_chars: current.log_content_max_chars,
        log_retention_days: current.log_retention_days,
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

            {/* **提示词不在这一页了。** 2026-09-06 起它住在每个审核渠道上
            (「审核渠道」卡 → 编辑 → 审核提示词),理由是提示词与协议绑死:
            护栏协议压根不发提示词,而挂在全局/作用域上就允许"一份提示词被分发到
            一个根本不读提示词的渠道" —— 配得出来、不报错、完全不生效。 */}

            {/* ── 审核日志 ──

            三格放在一起,因为它们回答的是同一个问题:「这次审核在我们自己的
            库里留下什么、留多久」。分散到别处的话,运营改保留期时看不见
            "留了内容"这件事,而这两者的隐私含义是绑在一起的。

            保留期没有"永久"这一档:这张表按**被抽中的请求数**增长,给它一个
            永久选项等于在设置页上放一个几个月后撑爆磁盘的开关。 */}
            <div className='flex flex-col gap-3 rounded-md border p-3'>
              <div>
                <p className='text-sm font-medium'>{t('qy_ai_log_title')}</p>
                <p className='text-muted-foreground text-xs'>
                  {eff.log_db_separate
                    ? t('qy_ai_log_db_separate')
                    : t('qy_ai_log_db_shared')}
                </p>
              </div>

              <label className='flex items-center gap-2'>
                <Switch
                  checked={current.log_content}
                  onCheckedChange={(v) =>
                    setDraft({ ...current, log_content: v })
                  }
                />
                <span className='text-sm'>{t('qy_ai_log_content')}</span>
              </label>
              <p className='text-muted-foreground -mt-1 text-xs'>
                {t('qy_ai_log_content_hint')}
              </p>

              {/* 违规行单独一档。它**不是**上面那个开关的子选项 —— 上面关掉时
                  它照样生效,所以必须并排画、并排说,不能做成缩进的从属项。 */}
              <label className='flex items-center gap-2'>
                <Switch
                  checked={current.log_content_violation_full}
                  onCheckedChange={(v) =>
                    setDraft({ ...current, log_content_violation_full: v })
                  }
                />
                <span className='text-sm'>
                  {t('qy_ai_log_content_violation_full')}
                </span>
              </label>
              <p className='text-muted-foreground -mt-1 text-xs'>
                {t('qy_ai_log_content_violation_full_hint')}
              </p>

              <div className='grid gap-4 sm:grid-cols-2'>
                <div className='flex flex-col gap-1.5'>
                  <Label>{t('qy_ai_log_content_max')}</Label>
                  <Input
                    type='number'
                    value={current.log_content_max_chars}
                    onChange={(e) =>
                      setDraft({
                        ...current,
                        log_content_max_chars: Number(e.target.value) || 0,
                      })
                    }
                  />
                  <p className='text-muted-foreground text-xs'>
                    {/* 用 `?.` 读一个类型上必有的字段不是多余的:这一页由五张卡
                        拼成、共用路由那一层的错误边界,任何一张卡在渲染中途抛
                        异常都会把整页换成错误态。滚动升级时前端可能先于后端到位,
                        那一刻这个字段确实不在 —— 少一行提示远好过少一整页。 */}
                    {t('qy_ai_log_content_max_hint', {
                      min: eff.log_content_range?.min ?? 100,
                      max: eff.log_content_range?.max ?? 32000,
                    })}
                  </p>
                </div>

                <div className='flex flex-col gap-1.5'>
                  <Label>{t('qy_ai_log_retention')}</Label>
                  <Input
                    type='number'
                    value={current.log_retention_days}
                    onChange={(e) =>
                      setDraft({
                        ...current,
                        log_retention_days: Number(e.target.value) || 0,
                      })
                    }
                  />
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_ai_log_retention_hint', {
                      max: eff.max_log_retention_days ?? 365,
                    })}
                  </p>
                </div>
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

// ───────────────────────────── cyber 会话屏蔽 ─────────────────────────────

/**
 * cyber 会话自动屏蔽。
 *
 * ## 它和上面的 AI 审核不是一回事
 *
 * AI 审核判**请求内容**违不违规;这里认的是**上游的拒绝码**(如 OpenAI Codex
 * 后端的 cyber_policy)。上游拒绝一次之后,把**这条会话**在本地拉黑,该会话
 * 后续任何请求(哪怕正常内容)进网关即 403「请开启新会话」,不再打上游 ——
 * 命中过的会话不能继续无限重试探测。
 *
 * ## 三个开关的层次
 *
 * · YAML `violation.enabled` 是基础设施级总闸,关着时本功能一律不生效;
 * · 这里的启用开关是运营的日常开关;
 * · 作用分组决定"哪些模型分组的流量"受屏蔽(空 = 全部),与 AI 审核作用域
 *   同口径,比的是请求实际使用的分组。
 */
function CyberBlockCard() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const query = useQuery(qyCyberSettingsQuery())
  const groupQuery = useQuery(qyGroupOptionsQuery())
  const categoryQuery = useQuery(qyAdminViolationCategoriesQuery())
  const categories = (categoryQuery.data?.items ?? []).map((row) => ({
    id: row.category.id,
    name: row.category.name,
  }))
  const [draft, setDraft] = useState<null | {
    enabled: boolean
    group_scope: string
    group_scope_mode: 'include' | 'exclude'
    ttl_seconds: number
    trigger_codes: string
    count_toward_ban: boolean
    category_id: number
  }>(null)

  const data = query.data
  const current =
    draft ??
    (data?.setting
      ? {
          enabled: data.setting.enabled,
          group_scope: data.setting.group_scope,
          group_scope_mode: data.setting.group_scope_mode,
          ttl_seconds: data.setting.ttl_seconds,
          trigger_codes: data.setting.trigger_codes,
          count_toward_ban: data.setting.count_toward_ban,
          category_id: data.setting.category_id,
        }
      : null)

  const save = useMutation({
    mutationFn: () => {
      if (!current) throw new Error('no draft')
      return updateQyCyberSetting({
        enabled: current.enabled,
        group_scope: current.group_scope,
        group_scope_mode: current.group_scope_mode,
        ttl_seconds: current.ttl_seconds,
        trigger_codes: current.trigger_codes,
        count_toward_ban: current.count_toward_ban,
        category_id: current.category_id,
      })
    },
    onSuccess: () => {
      toast.success(t('qy_ai_cyber_saved'))
      setDraft(null)
      void qc.invalidateQueries({
        queryKey: qyKeys.adminViolationCyberSettings(),
      })
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  const groupOptions = groupQuery.data?.options ?? []
  const groupEntries = current ? qyAiSplitScopeList(current.group_scope) : []
  const unknownGroups =
    groupOptions.length === 0
      ? []
      : qyUnknownGroupNames(
          [...new Set(groupEntries.map(qyNormalizeGroupName))],
          groupOptions
        )
  const groupBadges = groupEntries.map((name, index) => ({
    key: `${name}#${index}`,
    name,
    unknown: unknownGroups.includes(qyNormalizeGroupName(name)),
  }))

  const eff = data?.effective

  return (
    <QyPageBoundary query={query}>
      {data && current && eff ? (
        <Card>
          <CardHeader>
            <CardTitle>{t('qy_ai_cyber_title')}</CardTitle>
            <CardDescription>{t('qy_ai_cyber_desc')}</CardDescription>
          </CardHeader>
          <CardContent className='flex flex-col gap-4'>
            {/* 开了设置、但 YAML 基础设施总闸关着 —— 表单显示"开"、实际不生效。
                这是这一档唯一会"配了不生效且没有信号"的状态,必须说出来。 */}
            {current.enabled && !eff.module_on && (
              <Alert>
                <AlertTriangle className='size-4' />
                <AlertTitle>{t('qy_ai_cyber_module_off_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_ai_cyber_module_off_desc')}
                </AlertDescription>
              </Alert>
            )}

            <label className='flex items-center gap-2'>
              <Switch
                checked={current.enabled}
                onCheckedChange={(v) => setDraft({ ...current, enabled: v })}
              />
              <span className='text-sm font-medium'>
                {t('qy_ai_cyber_enabled')}
              </span>
            </label>

            <div className='flex flex-col gap-1.5'>
              <Label>{t('qy_ai_cyber_groups_label')}</Label>
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_cyber_groups_hint')}
              </p>
              <ComboboxInput
                options={groupOptions.map((option) => ({
                  value: option.name,
                  label: qyGroupOptionLabel(
                    option,
                    groupQuery.data?.probe_ok === true,
                    t
                  ),
                }))}
                value=''
                onValueChange={(picked) =>
                  setDraft({
                    ...current,
                    group_scope: qyAiAppendScopeGroup(
                      current.group_scope,
                      picked
                    ),
                  })
                }
                emptyText='qy_trg_group_picker_empty'
                placeholder={t('qy_ai_scope_group_pick')}
              />
              {/* 下拉给"现在有哪些分组",文本框给"站点已不认的历史分组仍要能配"。
                  留空 = 全部模型分组(与 AI 审核作用域不同,这里空是允许的)。 */}
              <Input
                placeholder='default,vip'
                value={current.group_scope}
                onChange={(e) =>
                  setDraft({ ...current, group_scope: e.target.value })
                }
              />
              {groupBadges.length > 0 && (
                <div className='flex flex-wrap gap-1'>
                  {groupBadges.map((badge) => (
                    <Badge
                      key={badge.key}
                      variant={badge.unknown ? 'warning' : 'secondary'}
                      className='font-normal'
                      title={
                        badge.unknown
                          ? t('qy_ai_scope_group_unknown_hint')
                          : undefined
                      }
                    >
                      {badge.name}
                      {badge.unknown && (
                        <span className='sr-only'>
                          {' '}
                          {t('qy_ai_scope_group_unknown_hint')}
                        </span>
                      )}
                    </Badge>
                  ))}
                </div>
              )}
              {groupBadges.length === 0 && (
                <p className='text-muted-foreground text-xs'>
                  {t('qy_ai_cyber_groups_all')}
                </p>
              )}
              <div className='flex gap-2'>
                {(['include', 'exclude'] as const).map((m) => (
                  <Button
                    key={m}
                    size='sm'
                    variant={
                      current.group_scope_mode === m ? 'default' : 'outline'
                    }
                    onClick={() =>
                      setDraft({ ...current, group_scope_mode: m })
                    }
                  >
                    {t(`qy_ai_scope_mode_${m}` as never)}
                  </Button>
                ))}
              </div>
            </div>

            <div className='flex flex-col gap-1.5'>
              <Label>{t('qy_ai_cyber_ttl_label')}</Label>
              <div className='flex items-center gap-2'>
                <Input
                  type='number'
                  min={0}
                  className='max-w-40'
                  value={current.ttl_seconds}
                  onChange={(e) =>
                    setDraft({
                      ...current,
                      ttl_seconds: Number(e.target.value) || 0,
                    })
                  }
                />
                <span className='text-muted-foreground text-sm'>
                  {t('qy_ai_cyber_seconds')}
                </span>
              </div>
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_cyber_ttl_hint', { def: eff.default_ttl })}
              </p>
            </div>

            {/*
              触发过滤规则,**可编辑 + 可还原默认**。new-api 不像 sub2api 自持号池,
              我们唯一的信号是上游透传回来的错误 —— 标准 {"error":{"code":"cyber_policy"}}
              下匹配是准的,但不同渠道可能包成别的形状,所以把这份判据交给运营。
            */}
            <div className='flex flex-col gap-1.5'>
              <div className='flex items-center justify-between gap-2'>
                <Label>{t('qy_ai_cyber_triggers_label')}</Label>
                <Button
                  type='button'
                  size='sm'
                  variant='outline'
                  disabled={
                    current.trigger_codes === data.default_trigger_codes
                  }
                  onClick={() =>
                    setDraft({
                      ...current,
                      trigger_codes: data.default_trigger_codes,
                    })
                  }
                >
                  {t('qy_ai_cyber_triggers_restore')}
                </Button>
              </div>
              <Textarea
                className='font-mono text-xs'
                rows={3}
                placeholder={data.default_trigger_codes}
                value={current.trigger_codes}
                onChange={(e) =>
                  setDraft({ ...current, trigger_codes: e.target.value })
                }
              />
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_cyber_triggers_hint')}
              </p>
            </div>

            {/* 计入自动封号计数 + 计数类型绑定。开了之后每次拉黑落一条违规记录,
                推进该用户的计数,达「处置策略」阈值即自动受限/封号。 */}
            <div className='flex flex-col gap-2'>
              <label className='flex items-center gap-2'>
                <Switch
                  checked={current.count_toward_ban}
                  onCheckedChange={(v) =>
                    setDraft({ ...current, count_toward_ban: v })
                  }
                />
                <span className='text-sm font-medium'>
                  {t('qy_ai_cyber_count_ban')}
                </span>
              </label>
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_cyber_count_ban_hint')}
              </p>
              {current.count_toward_ban && (
                <div className='flex flex-col gap-1.5'>
                  <Label>{t('qy_ai_cyber_category')}</Label>
                  <Select
                    value={String(current.category_id)}
                    onValueChange={(v) =>
                      setDraft({ ...current, category_id: Number(v) || 0 })
                    }
                  >
                    <SelectTrigger>
                      <SelectValue
                        placeholder={t('qy_ai_cyber_category_none')}
                      />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value='0'>
                        {t('qy_ai_cyber_category_none')}
                      </SelectItem>
                      {categories.map((cat) => (
                        <SelectItem key={cat.id} value={String(cat.id)}>
                          {cat.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_ai_cyber_category_hint')}
                  </p>
                </div>
              )}
            </div>

            <div className='flex justify-end'>
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

// ───────────────────────────── 作用域与分档抽样 ─────────────────────────────

/**
 * 「现在哪些分组在被 AI 审核监控、各自抽多少」。
 *
 * ## 为什么这一页需要一张汇总表,而不是一个策略列表
 *
 * 运营真正要回答的问题是关于**整体**的:一条 50% 的策略被排在一条"全站 1%"
 * 的策略后面时,它的真实抽样率是 0 —— 而在一个普通的策略列表上,这两条
 * 看起来一模一样。所以表格按**匹配顺序**排,末尾恒为兜底档(它就是设置页
 * 上那个抽样率),并且把"永远匹配不到"直接标出来。
 *
 * ## 两个时机是两列,不是一列
 *
 * 转发前审核同步、加延迟;转发后审核异步、只花钱。项目方的原话是
 * 「AI审核基本都是后审核,秋后算账的」—— 后审核可以对全站开到 10%,
 * 而转发前通常只敢对最可疑的一两个分组开。一列表达不了这个差别。
 */
function AiScopeCard() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const query = useQuery(qyAiScopesQuery())
  /**
   * 编辑中的那一条。`null` = 弹窗关着。
   *
   * 改造前这是一段内联表单,展开在表格下面。换成弹窗是项目方点名的
   * (「添加作用域策略,编辑作用域策略改成弹窗的方式」),而它解决的是一个
   * 实在的问题:这张表单有十来格(两个作用域、两个抽样率、提示词、类型、
   * 渠道),内联展开之后表格被推到屏幕外,"我正在改的是哪一行"完全看不见。
   */
  const [editing, setEditing] = useState<null | QyAiScopeDraft>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiScopes() })
    // 设置卡上的「生效状态」跟着策略表变:一条策略都没有时整份 AI 配置不生效,
    // 只失效一个,那张卡会继续说"已生效"。
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiSettings() })
  }

  const save = useMutation({
    mutationFn: (draft: QyAiScopeDraft) =>
      upsertQyAiScope(qyAiScopeDraftToInput(draft)),
    onSuccess: () => {
      toast.success(t('qy_ai_saved'))
      setEditing(null)
      invalidate()
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  /**
   * 列表上的一键启停。
   *
   * 走的是同一个 upsert 接口、同一份请求体(整条策略原样回传,只翻 enabled)——
   * 不另开一个 PATCH:那会是第二条写入路径,而漏抄的那一份大概率是审计,
   * 而"谁在什么时候把这一档打开了"正是本页最需要留痕的一件事
   * (打开它就是让一批用户的请求内容开始被发往第三方)。
   */
  const toggle = useMutation({
    mutationFn: (row: QyAiScope) =>
      upsertQyAiScope(
        qyAiScopeDraftToInput({
          ...qyAiScopeToDraft(row),
          enabled: !row.enabled,
        })
      ),
    onSuccess: () => {
      toast.success(t('qy_ai_saved'))
      invalidate()
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  const remove = useMutation({
    mutationFn: (id: number) => deleteQyAiScope(id),
    onSuccess: () => {
      toast.success(t('qy_ai_scope_deleted'))
      invalidate()
    },
    onError: (e) => toast.error(qyErrorMessage(e, t)),
  })

  // 违规类型清单。复用**已有的**违规类型页那个 query，不另开一个端点：
  // 同一份事实开两个接口，迟早会出现两页各自认为对方是错的那种状态。
  //
  // 它与分组清单同性质 —— **只是输入辅助**：拉不到时下面的选择器会退化成
  // 「只能选不指定」并给出提示，绝不阻止保存已有配置。
  const categoryQuery = useQuery(qyAdminViolationCategoriesQuery())
  // 只取这一格用得上的三样。**不**把整行 QyViolationCategory 传下去：
  // 那一行里有 `remark`（内部备注）与 `ai_guidance`（判定说明），
  // 两者都不该出现在一个"挑一个类型"的下拉里。
  const categories = (categoryQuery.data?.items ?? []).map((row) => ({
    id: row.category.id,
    name: row.category.name,
    is_fallback: row.category.is_fallback,
  }))
  const categoryName = (id: number) =>
    categories.find((c) => c.id === id)?.name ?? ''

  const data = query.data
  // `summary` 在类型上是必填的，这里仍然按可选读：类型说的是「后端应该给」，
  // 运行期拿到的是「后端这次给了什么」。缺了它直读一层会在渲染中途抛
  // TypeError，把整张 AI 审核页打成白屏 —— 而这一页正是用来回答
  // 「现在到底哪些分组在被监控」的，白屏时连"读不出来"都说不了。
  const monitoredAll = data?.summary ?? []
  // 渠道清单同理按可选读。它只是 join 用的辅助,拉不到时每一格会退回
  // 「指定的渠道查不到」的告警态 —— 那比把一条已经停止工作的策略画成正常的好。
  const channels = data?.channels ?? []
  // 清单没拉到时退回显示 id:显示一个空格会让人以为这一档没指定,
  // 而它其实指定了 —— 那是两种完全不同的处置。
  const channelNames = (ids: number[]) =>
    (ids ?? []).map((id) => channels.find((c) => c.id === id)?.name || `#${id}`)
  const rowState = (row: QyAiScopeSummaryRow) => {
    const channel = qyAiScopeChannelState(row.channel_ids, channels)
    return {
      channel,
      // partial 不算失效:清单里还剩至少一个能发,这一档照常审核。
      // 报"渠道不可用"会把人引去修一个没有停摆的东西。
      channelBroken: channel === 'disabled' || channel === 'missing',
      kind: qyAiScopeRowKind(row, {
        channelBroken: channel === 'disabled' || channel === 'missing',
      }),
    }
  }
  const monitored = monitoredAll.filter((r) => rowState(r).kind === 'active')

  return (
    <QyPageBoundary query={query}>
      {data ? (
        <Card>
          <CardHeader>
            <CardTitle>{t('qy_ai_scope_title')}</CardTitle>
            <CardDescription>{t('qy_ai_scope_desc')}</CardDescription>
          </CardHeader>
          <CardContent className='flex flex-col gap-4'>
            {/* 一句话回答"现在到底有没有人在被监控"。零档时它是最重要的一行:
                策略配得再漂亮,只要每一档抽样率都是 0,线上就是完全不跑。
                全局抽样率下线之后这条更硬:**表是空的就等于完全不审核**,
                再没有一个看不见的默认值在后面兜着。 */}
            {monitored.length === 0 ? (
              <Alert>
                <AlertTriangle className='size-4' />
                <AlertTitle>{t('qy_ai_scope_none_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_ai_scope_none_desc')}
                </AlertDescription>
              </Alert>
            ) : (
              <p className='text-muted-foreground text-sm'>
                {t('qy_ai_scope_monitored_count', { count: monitored.length })}
              </p>
            )}

            <div className='overflow-x-auto'>
              <table className='w-full text-sm'>
                <thead>
                  <tr className='text-muted-foreground text-left'>
                    <th className='py-1'>{t('qy_ai_scope_col_name')}</th>
                    <th className='py-1'>{t('qy_ai_scope_col_audience')}</th>
                    <th className='py-1'>{t('qy_ai_scope_col_pre')}</th>
                    <th className='py-1'>{t('qy_ai_scope_col_async')}</th>
                    {/* 「问什么」「送到哪」与「记成哪一类」是这一档的另外三半，
                        它们与抽样率一样没有任何用户可见的症状 —— 只能摆在表上。 */}
                    <th className='py-1'>{t('qy_ai_scope_col_chgroup')}</th>
                    <th className='py-1'>{t('qy_ai_scope_col_channel')}</th>
                    <th className='py-1'>{t('qy_ai_scope_col_category')}</th>
                    <th className='py-1'>{t('qy_ai_scope_col_state')}</th>
                    <th className='py-1' />
                  </tr>
                </thead>
                <tbody>
                  {monitoredAll.map((row) => (
                    <ScopeRow
                      key={row.id}
                      row={row}
                      kind={rowState(row).kind}
                      categoryName={categoryName(row.category_id)}
                      channelNames={channelNames(row.channel_ids)}
                      channelState={rowState(row).channel}
                      toggling={toggle.isPending}
                      onToggle={() => {
                        const src = data.items?.find((s) => s.id === row.id)
                        if (src) toggle.mutate(src)
                      }}
                      onEdit={() => {
                        const src = data.items?.find((s) => s.id === row.id)
                        if (src) setEditing(qyAiScopeToDraft(src))
                      }}
                      onDelete={() => remove.mutate(row.id)}
                    />
                  ))}
                </tbody>
              </table>
            </div>

            <div>
              <Button
                size='sm'
                variant='outline'
                onClick={() => setEditing(qyAiScopeToDraft())}
              >
                <Plus className='size-4' />
                {t('qy_ai_scope_add')}
              </Button>
            </div>

            <ScopeFormDialog
              draft={editing}
              categories={categories}
              categoriesLoaded={categoryQuery.isSuccess}
              channels={channels}
              onChange={setEditing}
              onClose={() => setEditing(null)}
              onSave={(draft) => save.mutate(draft)}
              saving={save.isPending}
            />
          </CardContent>
        </Card>
      ) : null}
    </QyPageBoundary>
  )
}

function ScopeRow({
  row,
  kind,
  categoryName,
  channelNames,
  channelState,
  toggling,
  onToggle,
  onEdit,
  onDelete,
}: {
  row: QyAiScopeSummaryRow
  kind: QyAiScopeRowKind
  /** 违规类型清单里 join 出来的名字。空串 = 没指定，或者清单没拉到。 */
  categoryName: string
  /** 渠道清单里 join 出来的名字,顺序与 `row.channel_ids` 一致(轮询按它转)。 */
  channelNames: string[]
  channelState: QyAiScopeChannelState
  toggling: boolean
  onToggle: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const aud = qyAiScopeAudience(row)
  const channelBroken =
    channelState === 'disabled' || channelState === 'missing'

  // 「在监控谁」拼在这里而不是在 lib 里:文案要过 i18next,拼好的字符串
  // 没法翻译。lib 只回答结构(全部/名单/方向),这里负责把它变成一句话。
  const audience = aud.allGroups
    ? t('qy_ai_scope_aud_all_groups')
    : aud.exclude
      ? t('qy_ai_scope_aud_exclude', { groups: aud.groups.join(', ') })
      : t('qy_ai_scope_aud_include', { groups: aud.groups.join(', ') })

  return (
    <tr className='border-t align-top'>
      <td className='py-1.5 pe-2'>
        <span className='font-medium'>{row.name}</span>
        <span className='text-muted-foreground ms-2 text-xs'>
          #{row.priority}
        </span>
      </td>
      <td className='text-muted-foreground py-1.5 pe-2 text-xs'>
        <div>{audience}</div>
        {!aud.allModels && (
          <div>
            {t('qy_ai_scope_aud_models', { models: aud.models.join(', ') })}
          </div>
        )}
        {/* 存量的"没绑分组"行。新的写入已经不接受这种配置,所以它只可能是
            升级前留下来的 —— 而它没有被自动停用(静默关掉一条正在生效的风控
            比留着它更危险),也就是说**启用中的那些还在按全站匹配**。
            两种状态分开说:一个是"正在全站送审",一个只是"开不起来"。 */}
        {row.group_unbound && (
          <div className='text-warning mt-1'>
            {row.enabled
              ? t('qy_ai_scope_unbound_active')
              : t('qy_ai_scope_unbound_idle')}
          </div>
        )}
      </td>
      <td className='py-1.5 pe-2'>
        {qyAiBpsToPercentText(row.pre_sample_rate_bps)}%
      </td>
      <td className='py-1.5 pe-2'>
        {qyAiBpsToPercentText(row.async_sample_rate_bps)}%
      </td>
      {/* 「用哪个池子」。它与右边那一列(指定渠道)是同一件事的两种写法:
          填了分组就在组内分发,指定了渠道就只发给那几个。两格都空是**存量行**
          ——那时按老口径发给全部启用渠道,而新存的策略再也不允许这样。 */}
      <td className='py-1.5 pe-2 text-xs'>
        {row.channel_group ? (
          <Badge variant='secondary' className='font-normal'>
            {row.channel_group}
          </Badge>
        ) : (
          <span className='text-muted-foreground'>
            {row.channel_ids.length > 0
              ? t('qy_ai_scope_chgroup_pinned')
              : t('qy_ai_scope_chgroup_legacy')}
          </span>
        )}
      </td>
      {/* 「送到哪」。留空那一档写的是"全部启用渠道",不是"默认渠道" ——
          渠道表上没有 priority,两个渠道各 50% 时"默认渠道"这句话就是假的。
          指定的渠道全都被停用或删掉时这一档不再审核任何内容,而它与一条正常
          策略长得一模一样,所以那两种状态必须是警示色 + 一句话。
          只坏了一部分是另一回事:这一档没停摆,但实际在扛的比运营选的少。 */}
      <td className='py-1.5 pe-2 text-xs'>
        <div className='flex flex-wrap items-center gap-1'>
          {channelState === 'default' ? (
            <Badge variant='outline' className='font-normal'>
              {t('qy_ai_scope_channel_default')}
            </Badge>
          ) : (
            channelNames.map((name, i) => (
              <Badge
                key={row.channel_ids[i]}
                variant={channelState === 'ok' ? 'secondary' : 'warning'}
                className='font-normal'
              >
                {name}
              </Badge>
            ))
          )}
          {/* 分发方式只在**真的有两个以上**渠道时才画:一个渠道时轮询与随机
              是同一件事,而多一个 badge 会让人以为自己配了点什么。 */}
          {(row.channel_ids ?? []).length > 1 && (
            <Badge variant='outline' className='font-normal'>
              {t(`qy_ai_scope_mode_${row.channel_mode}` as never)}
            </Badge>
          )}
          {/* 「指定了 A」与「指定了 A,但 A 不行时会发给别人」是两种不同的数据
              流向,而它们在这一格里只差这一个 badge。不画的话,运营看着
              「审核渠道: 内部自建」得到的是一个已经不成立的预期。 */}
          {row.channel_failover && (
            <Badge variant='outline' className='font-normal'>
              {t('qy_ai_scope_channel_failover_on')}
            </Badge>
          )}
        </div>
        {channelState === 'partial' && (
          <div className='text-warning mt-1'>
            {t('qy_ai_scope_channel_partial')}
          </div>
        )}
        {channelBroken && (
          <div className='text-warning mt-1'>
            {t(
              row.channel_failover
                ? 'qy_ai_scope_channel_broken_failover'
                : (`qy_ai_scope_channel_${channelState}` as never)
            )}
          </div>
        )}
      </td>
      <td className='text-muted-foreground py-1.5 pe-2 text-xs'>
        {row.category_id > 0
          ? /* 清单没拉到时退回显示 id：显示一个空格会让人以为这一档没指定，
               而它其实指定了 —— 那是两种完全不同的处置。 */
            categoryName || `#${row.category_id}`
          : t('qy_ai_scope_category_none')}
      </td>
      <td className='py-1.5 pe-2'>
        <Badge variant={kind === 'active' ? 'default' : 'outline'}>
          {t(`qy_ai_scope_kind_${kind}` as never)}
        </Badge>
      </td>
      <td className='py-1.5'>
        <div className='flex items-center gap-2'>
          {/* 启停留在列表上(不进弹窗):它是这一页唯一一个高频、单字段的动作,
              而"先打开弹窗、改一个开关、再点保存"要三次点击。 */}
          <Switch
            checked={row.enabled}
            disabled={toggling}
            onCheckedChange={onToggle}
            aria-label={t('qy_ai_scope_f_enabled')}
          />
          <Button size='sm' variant='outline' onClick={onEdit}>
            {t('qy_ai_edit')}
          </Button>
          <Button size='sm' variant='outline' onClick={onDelete}>
            <Trash2 className='size-4' />
          </Button>
        </div>
      </td>
    </tr>
  )
}

/**
 * 新建 / 编辑一条作用域策略的弹窗。
 *
 * `draft` 为 null = 关着。用一个可空草稿而不是 `open` + `draft` 两个状态:
 * 两个状态必然出现"开着但草稿是空"的第三种组合,而那一帧会在表单里读到
 * undefined。
 *
 * 外壳用 `QyResponsiveDialog`(桌面居中 / 移动侧出 / 头尾固定、正文自己滚)。
 * 这张表单有十来格,而「保存」必须始终够得着 —— 长表单里按钮跟着正文滚走时,
 * 用户会以为没有保存键。
 */
function ScopeFormDialog({
  draft,
  categories,
  categoriesLoaded,
  channels,
  onChange,
  onClose,
  onSave,
  saving,
}: {
  draft: QyAiScopeDraft | null
  categories: { id: number; name: string; is_fallback: boolean }[]
  categoriesLoaded: boolean
  channels: { id: number; name: string; enabled: boolean; model: string }[]
  onChange: (d: QyAiScopeDraft) => void
  onClose: () => void
  onSave: (d: QyAiScopeDraft) => void
  saving: boolean
}) {
  const { t } = useTranslation()
  // 「必须绑定分组」这一条挡在保存键上,而不是等后端回 400:那一格是这张表单
  // 上唯一一个填错了就让整条策略开不起来的地方,而 400 的措辞出现在弹窗外面
  // 的 toast 里 —— 人正看着表单,提示却在别处。
  const bindingError = draft ? qyAiScopeGroupBindingError(draft) : null
  return (
    <QyResponsiveDialog
      open={draft !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title={
        draft?.id ? t('qy_ai_scope_edit_title') : t('qy_ai_scope_create_title')
      }
      description={t('qy_ai_scope_form_desc')}
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button variant='outline' onClick={onClose}>
            {t('qy_ai_cancel')}
          </Button>
          <Button
            disabled={saving || draft === null || bindingError !== null}
            onClick={() => {
              if (draft && bindingError === null) onSave(draft)
            }}
          >
            {t('qy_ai_save')}
          </Button>
        </>
      }
    >
      {draft && (
        <ScopeForm
          draft={draft}
          bindingError={bindingError}
          categories={categories}
          categoriesLoaded={categoriesLoaded}
          channels={channels}
          onChange={onChange}
        />
      )}
    </QyResponsiveDialog>
  )
}

function ScopeForm({
  draft,
  bindingError,
  categories,
  categoriesLoaded,
  channels,
  onChange,
}: {
  draft: QyAiScopeDraft
  /** 「必须绑定分组」的校验结果,null = 通过。保存键与这一格的提示共用它。 */
  bindingError: QyAiScopeGroupBindingError
  categories: { id: number; name: string; is_fallback: boolean }[]
  categoriesLoaded: boolean
  channels: { id: number; name: string; enabled: boolean; model: string }[]
  onChange: (d: QyAiScopeDraft) => void
}) {
  const { t } = useTranslation()
  const fakeSep =
    qyAiScopeHasFakeSeparator(draft.group_scope) ||
    qyAiScopeHasFakeSeparator(draft.model_scope)
  const channelState = qyAiScopeChannelState(draft.channel_ids, channels)

  /**
   * 分组候选清单。**复用**违规规则页与划转分组规则页共用的那一份
   * (`features/qy/lib/group-options`，走已有的 `GET /admin/transfer/group-rules`)，
   * 不新开端点、不另写一份取数：同一份事实开两个来源，迟早会出现两页各自
   * 认为对方是错的那种状态。
   *
   * 它**永远只是输入辅助**：拉不到、过期、名字不在里面，都不阻止保存 ——
   * 历史分组（倍率表里已删、users 里还有人挂着）恰恰是最需要被监控的那批。
   */
  const groupQuery = useQuery(qyGroupOptionsQuery())
  const groupOptions = groupQuery.data?.options ?? []
  const groupEntries = qyAiSplitScopeList(draft.group_scope)
  // 清单为空（拉取失败，或者站点真的一个分组都没定义）时一律不算未定义分组：
  // 那会把每一个名字都标成黄的，是一片假警报 —— 而假警报比没有警报更糟。
  const unknownGroups =
    groupOptions.length === 0
      ? []
      : qyUnknownGroupNames(
          [...new Set(groupEntries.map(qyNormalizeGroupName))],
          groupOptions
        )
  const groupBadges = groupEntries.map((name, index) => ({
    key: `${name}#${index}`,
    name,
    unknown: unknownGroups.includes(qyNormalizeGroupName(name)),
  }))

  // 已有的审核渠道分组:下拉里列出来,免得运营手打一个不存在的名字 ——
  // 那不会报错,只会让这一档一个渠道都挑不到(no_channel),而界面上完全正常。
  const channelQuery = useQuery(qyAiChannelsQuery())
  const channelGroups = [
    ...new Set(
      (channelQuery.data?.items ?? [])
        .map((ch) => ch.group)
        .filter((g) => g.trim() !== '')
    ),
  ]

  return (
    <div className='flex flex-col gap-3'>
      <div className='grid gap-3 sm:grid-cols-2'>
        <Field label={t('qy_ai_scope_f_name')}>
          <Input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value })}
          />
        </Field>
        <Field
          label={t('qy_ai_scope_f_priority')}
          hint={t('qy_ai_scope_f_priority_hint')}
        >
          <Input
            type='number'
            value={draft.priority}
            onChange={(e) =>
              onChange({ ...draft, priority: Number(e.target.value) || 0 })
            }
          />
        </Field>
        {/* 分组作用域。**必填**。
            原来这里是一个裸文本框：打错一个字母，这一档就静默挂在一个不存在的
            分组上 —— 保存成功、界面正常、线上一个请求都不监控，而且没有任何
            信号。换成「带元数据的下拉 + 保留自由输入 + 未定义分组软告警」，
            口径与违规规则页那一格完全一致（共用 features/qy/lib/group-options）。

            留空曾经的含义是"全部分组",现在不再被接受(项目方:「强制绑定分组,
            全站模型还是太高了一点」)—— 一条覆盖全站的策略把抽样率这唯一的成本
            闸门作用在所有用户身上,而它在列表上与一条只盯一个分组的策略长得一样。 */}
        <Field
          label={t('qy_ai_scope_f_groups')}
          hint={t('qy_ai_scope_f_groups_hint')}
          required
        >
          <div className='flex flex-col gap-2'>
            <ComboboxInput
              options={groupOptions.map((option) => ({
                value: option.name,
                label: qyGroupOptionLabel(
                  option,
                  groupQuery.data?.probe_ok === true,
                  t
                ),
              }))}
              value=''
              onValueChange={(picked) =>
                onChange({
                  ...draft,
                  group_scope: qyAiAppendScopeGroup(draft.group_scope, picked),
                })
              }
              emptyText='qy_trg_group_picker_empty'
              placeholder={t('qy_ai_scope_group_pick')}
            />
            {/* 下拉解决「站点现在有哪些分组」，文本框解决「站点已经不认的历史
                分组仍要能配」—— 后者恰恰是最需要被审的那批账号。 */}
            <Input
              placeholder='default,vip'
              value={draft.group_scope}
              onChange={(e) =>
                onChange({ ...draft, group_scope: e.target.value })
              }
            />
            {groupBadges.length > 0 && (
              <div className='flex flex-wrap gap-1'>
                {groupBadges.map((badge) => (
                  <Badge
                    key={badge.key}
                    variant={badge.unknown ? 'warning' : 'secondary'}
                    className='font-normal'
                    title={
                      badge.unknown
                        ? t('qy_ai_scope_group_unknown_hint')
                        : undefined
                    }
                  >
                    {badge.name}
                    {/* 只靠颜色区分「站点定义过 / 没定义过」，色觉障碍用户拿到
                        的是一串一模一样的名字。 */}
                    {badge.unknown && (
                      <span className='sr-only'>
                        {' '}
                        {t('qy_ai_scope_group_unknown_hint')}
                      </span>
                    )}
                  </Badge>
                ))}
              </div>
            )}
            {/* 三种非正常状态各自说清楚。任何一种都**不**禁用上面的文本框：
                把人卡在一个拉不到的下拉前面，等于让他配不了作用域。 */}
            {groupQuery.isPending && (
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_scope_group_loading')}
              </p>
            )}
            {groupQuery.isError && (
              <p className='text-warning text-xs'>
                {t('qy_ai_scope_group_failed')}
              </p>
            )}
            {groupQuery.isSuccess && groupOptions.length === 0 && (
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_scope_group_empty')}
              </p>
            )}
            {/* 这一条是错误,不是告警:保存键同时是灰的。它必须说清"为什么必填",
                否则运营的第一反应是"以前留空就行,现在坏了"。 */}
            {bindingError === 'empty' && (
              <p className='text-destructive text-xs' role='alert'>
                {t('qy_ai_scope_group_required')}
              </p>
            )}
          </div>
        </Field>
        <Field
          label={t('qy_ai_scope_f_mode')}
          hint={t('qy_ai_scope_f_mode_hint')}
        >
          <div className='flex flex-col gap-2'>
            <div className='flex gap-2'>
              {(['include', 'exclude'] as const).map((m) => (
                <Button
                  key={m}
                  size='sm'
                  variant={draft.group_scope_mode === m ? 'default' : 'outline'}
                  onClick={() => onChange({ ...draft, group_scope_mode: m })}
                >
                  {t(`qy_ai_scope_mode_${m}` as never)}
                </Button>
              ))}
            </div>
            {/* 排除 = 名单之外的全部分组,与"留空"是同一件事的另一种写法,
                所以启用中的策略同样不接受它。按钮不禁用:一条存量的 exclude
                策略要能被打开、看清、改成包含 —— 禁用按钮只会让人卡在原地。 */}
            {bindingError === 'exclude' && (
              <p className='text-destructive text-xs' role='alert'>
                {t('qy_ai_scope_mode_exclude_blocked')}
              </p>
            )}
          </div>
        </Field>
        <Field
          label={t('qy_ai_scope_f_models')}
          hint={t('qy_ai_scope_f_models_hint')}
        >
          <Input
            value={draft.model_scope}
            onChange={(e) =>
              onChange({ ...draft, model_scope: e.target.value })
            }
          />
        </Field>
        <Field
          label={t('qy_ai_scope_f_pre')}
          hint={t('qy_ai_scope_f_pre_hint')}
        >
          <Input
            inputMode='decimal'
            value={draft.prePercent}
            onChange={(e) => onChange({ ...draft, prePercent: e.target.value })}
          />
        </Field>
        <Field
          label={t('qy_ai_scope_f_async')}
          hint={t('qy_ai_scope_f_async_hint')}
        >
          <Input
            inputMode='decimal'
            value={draft.asyncPercent}
            onChange={(e) =>
              onChange({ ...draft, asyncPercent: e.target.value })
            }
          />
        </Field>
        {/* 「命中一律记为」。
            它覆盖规则自己绑的类型，而模型返回的 category 永不直接决定记录类型 ——
            后者逐次调用波动，而类型计数是封号判据的一条线。留「不指定」时行为
            与这一格出现之前完全一致。 */}
        <Field
          label={t('qy_ai_scope_f_category')}
          hint={t('qy_ai_scope_f_category_hint')}
        >
          <Select
            value={String(draft.category_id)}
            onValueChange={(v) =>
              onChange({ ...draft, category_id: Number(v) || 0 })
            }
          >
            <SelectTrigger>
              <SelectValue placeholder={t('qy_ai_scope_category_none')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='0'>
                {t('qy_ai_scope_category_none')}
              </SelectItem>
              {categories.map((c) => (
                <SelectItem key={c.id} value={String(c.id)}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {/* 清单拉不到时说出来，而不是显示一个只有「不指定」的下拉：
              后者看起来像"这个站点没有违规类型"，那是一句谎话。 */}
          {!categoriesLoaded && (
            <p className='text-muted-foreground text-xs'>
              {t('qy_ai_scope_category_loading')}
            </p>
          )}
        </Field>
        {/* 「送到哪几个审核渠道」。
            留空 = **在全部启用渠道之间分发**,不是"某一个默认渠道" ——
            渠道表上没有 priority,两个渠道各配 50% 权重时"默认渠道"这句话
            就是假的,而运营会按那句话去理解自己的数据流向。
            指定之后这一档只发给清单里的那几个;它们**全部**被停用或删除时,
            这一档不再审核任何内容(每次都是「无可用渠道」+ 放行),运行期绝不
            回落到其余渠道:回落会把用户内容发去运营明确没有选的端点,
            而那往往正是指定渠道的全部理由。 */}
        <Field
          label={t('qy_ai_scope_f_channel')}
          hint={t('qy_ai_scope_f_channel_hint')}
        >
          {channels.length === 0 ? (
            /* 一个渠道都没有时说出来,而不是画一个空框:空框看起来像
               "这一格不用填",而它其实是"这个站点还没配审核渠道"。 */
            <p className='text-muted-foreground text-xs'>
              {t('qy_ai_scope_channel_none')}
            </p>
          ) : (
            <div className='grid gap-1.5 sm:grid-cols-2'>
              {channels.map((ch) => (
                <Label
                  key={ch.id}
                  htmlFor={`qy-ai-scope-ch-${ch.id}`}
                  className='flex items-center gap-2 text-sm font-normal'
                >
                  <Checkbox
                    id={`qy-ai-scope-ch-${ch.id}`}
                    checked={draft.channel_ids.includes(ch.id)}
                    onCheckedChange={(checked) =>
                      onChange({
                        ...draft,
                        // 勾选顺序就是轮询顺序:后勾的排在后面。
                        channel_ids:
                          checked === true
                            ? [...draft.channel_ids, ch.id]
                            : draft.channel_ids.filter((id) => id !== ch.id),
                      })
                    }
                  />
                  {/* 停用的渠道照样列出来:一条已经指向停用渠道的策略必须能
                      在这里看见自己当前选的是谁,否则那一格会显示成空的,
                      而空的看起来像"没指定"—— 两者的线上行为完全相反。
                      勾着它保存会被后端 400 挡下,那是对的。 */}
                  <span className='min-w-0 truncate' title={ch.model}>
                    {ch.enabled
                      ? ch.name
                      : t('qy_ai_scope_channel_option_disabled', {
                          name: ch.name,
                        })}
                  </span>
                </Label>
              ))}
            </div>
          )}
          {channelState === 'partial' && (
            <p className='text-warning text-xs'>
              {t('qy_ai_scope_channel_partial')}
            </p>
          )}
          {(channelState === 'disabled' || channelState === 'missing') && (
            <p className='text-warning text-xs'>
              {t(`qy_ai_scope_channel_${channelState}` as never)}
            </p>
          )}
          {/* 分发方式。**只在勾了两个以上时出现** —— 一个渠道时轮询与加权随机
              是同一件事,那时摆一个单选是在问一个没有答案的问题。 */}
          {draft.channel_ids.length > 1 && (
            <div className='mt-2 flex flex-col gap-1.5'>
              <Label className='text-sm'>{t('qy_ai_scope_f_dispatch')}</Label>
              <Select
                value={draft.channel_mode}
                onValueChange={(v) =>
                  onChange({
                    ...draft,
                    channel_mode:
                      v === 'round_robin' ? 'round_robin' : 'weighted',
                  })
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='weighted'>
                    {t('qy_ai_scope_mode_weighted')}
                  </SelectItem>
                  <SelectItem value='round_robin'>
                    {t('qy_ai_scope_mode_round_robin')}
                  </SelectItem>
                </SelectContent>
              </Select>
              <p className='text-muted-foreground text-xs'>
                {t(`qy_ai_scope_mode_${draft.channel_mode}_hint` as never)}
              </p>
            </div>
          )}
          {/* 故障转移。**只在指定了渠道时出现** —— 没指定时本来就在全部启用
              渠道之间分发,那时摆一个"失败后退到随机池"的开关是在问一个没有
              含义的问题。

              默认关,而且这里刻意用一整段说清打开之后会发生什么:它把
              「只发给这几个」变成「它们不行就发给池子里的任何一个」,
              也就是把用户内容的出境目的地从一组变成全部。指定渠道这一格的
              原始理由往往正是数据流向约束,所以这一步必须是运营自己按下的。 */}
          {draft.channel_ids.length > 0 && (
            <label className='mt-2 flex items-start gap-2'>
              <Switch
                checked={draft.channel_failover}
                onCheckedChange={(v) =>
                  onChange({ ...draft, channel_failover: v })
                }
              />
              <span className='text-sm'>
                {t('qy_ai_scope_f_failover')}
                <span className='text-muted-foreground block text-xs'>
                  {t(
                    draft.channel_failover
                      ? 'qy_ai_scope_f_failover_on'
                      : 'qy_ai_scope_f_failover_off'
                  )}
                </span>
              </span>
            </label>
          )}
        </Field>
        <Field label={t('qy_ai_scope_f_remark')}>
          <Input
            value={draft.remark}
            onChange={(e) => onChange({ ...draft, remark: e.target.value })}
          />
        </Field>
      </div>

      {/* 审核渠道分组:这一档的用户内容会流到哪个池子。
          它与下面的「指定审核渠道」是二选一(启用时两个都空会被后端 400)——
          旧的"两个都空 = 发给全部启用渠道"已经取消:那意味着之后新启用的
          任何一个渠道都会自动开始收到用户内容,而没有人按下过那个动作。

          下拉里列的是**已有渠道上出现过的分组名**。手打一个不存在的名字不会
          报错,只会让这一档一个渠道都挑不到(no_channel),而界面上完全正常。 */}
      <Field
        label={t('qy_ai_scope_f_chgroup')}
        hint={t('qy_ai_scope_f_chgroup_hint')}
      >
        <ComboboxInput
          value={draft.channel_group}
          onValueChange={(v) => onChange({ ...draft, channel_group: v })}
          options={channelGroups.map((g) => ({ value: g, label: g }))}
          placeholder={t('qy_ai_scope_f_chgroup_ph')}
        />
      </Field>

      {/* 全角逗号是中文输入法下最容易发生的一次手滑,而后端不认它:
          `vip,svip` 会被当成一个分组名去精确匹配,永远匹配不到任何人,
          并且不会有任何报错。 */}
      {fakeSep && (
        <Alert>
          <AlertTriangle className='size-4' />
          <AlertTitle>{t('qy_ai_scope_sep_title')}</AlertTitle>
          <AlertDescription>{t('qy_ai_scope_sep_desc')}</AlertDescription>
        </Alert>
      )}

      {/* 启停这一格**不在弹窗里** —— 它留在列表上作为一键动作。
          放两处会出现"弹窗里关掉、列表上还开着"的那一帧,而这个开关决定的是
          一批用户的请求内容会不会被发往第三方。 */}
      <label className='flex items-center gap-2'>
        <Switch
          checked={draft.enabled}
          onCheckedChange={(v) => onChange({ ...draft, enabled: v })}
        />
        <span className='text-sm'>{t('qy_ai_scope_f_enabled')}</span>
      </label>
    </div>
  )
}

// ───────────────────────────── 渠道 ─────────────────────────────

function AiChannelsCard() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const query = useQuery(qyAiChannelsQuery())
  // 内置默认提示词全文:新建/编辑渠道时用来**预填**输入框,并在提交时把
  // "逐字等于默认"折回空串。取不到时传空串 —— 那时输入框是空的,而空 = 用
  // 内置默认,行为不变(只是运营看不见默认长什么样)。
  const defaultPrompt = useQuery(qyAiSettingsQuery()).data?.default_prompt ?? ''
  // 违规类型清单只为把每一行的「计次记为」显示成名字。与作用域卡共用同一个
  // query key,react-query 会去重,同一页上不会多打一次请求。
  const categoryQuery = useQuery(qyAdminViolationCategoriesQuery())
  const categoryName = (id: number) =>
    (categoryQuery.data?.items ?? []).find((r) => r.category.id === id)
      ?.category.name ?? ''
  const [editing, setEditing] = useState<null | {
    id?: number
    draft: QyAiChannelDraft
  }>(null)

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiChannels() })
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiSettings() })
    // 作用域那张卡也要跟着失效:它的渠道下拉用的是**作用域接口**里下发的
    // 那一份清单(那份清单同时带着 enabled,是"指定的渠道被停用了"唯一的
    // 判据来源)。少了这一行,新建一个渠道之后作用域表单里根本选不到它 ——
    // 两张卡在同一页上同时挂着,作用域那个 query 不会重新挂载,而
    // refetchOnWindowFocus 是关的,于是那份清单会一直停在进页面那一刻,
    // 直到整页刷新。症状是"我明明加了护栏渠道,这里为什么选不到"。
    void qc.invalidateQueries({ queryKey: qyKeys.adminViolationAiScopes() })
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

  /**
   * 试跑结果要**留在页面上**,不能只弹一个 toast。
   *
   * toast 几秒后就没了,而这个按钮真正要回答的问题(协议对不对、上游到底
   * 回了什么)只有对着原始响应才答得出来 —— 尤其是护栏模型:官方没有给出
   * OpenAI 兼容端点上的字段级规格,真机对不上时唯一的办法就是照着原文调。
   */
  const [probed, setProbed] = useState<null | {
    id: number
    result: QyAiChannelTestResult
  }>(null)

  const probe = useMutation({
    mutationFn: (id: number) => testQyAiChannel(id),
    onSuccess: (r, id) => {
      setProbed({ id, result: r })
      toast.success(
        t('qy_ai_test_result', {
          outcome: r.outcome,
          latency: r.latency_ms,
          tokens: r.tokens.total,
        })
      )
    },
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
              {/* `items` 缺席时按空列表走(与设置卡同一条理由):这一页四张卡
                  共用路由那一层的错误边界,一份降级响应不该把整页打掉。 */}
              {(data.items ?? []).map((ch) => (
                <div key={ch.id} className='flex flex-col gap-2'>
                  <ChannelRow
                    channel={ch}
                    categoryName={categoryName(ch.category_id)}
                    testing={probe.isPending && probe.variables === ch.id}
                    onEdit={() =>
                      setEditing({
                        id: ch.id,
                        draft: qyAiChannelToDraft(ch, defaultPrompt),
                      })
                    }
                    onTest={() => probe.mutate(ch.id)}
                    onDelete={() => remove.mutate(ch.id)}
                  />
                  {probed?.id === ch.id && (
                    <ChannelTestPanel
                      result={probed.result}
                      onDismiss={() => setProbed(null)}
                    />
                  )}
                </div>
              ))}
              {(data.items ?? []).length === 0 && (
                <p className='text-muted-foreground text-sm'>
                  {t('qy_ai_channels_empty')}
                </p>
              )}
            </div>

            <div>
              <Button
                size='sm'
                variant='outline'
                onClick={() =>
                  setEditing({
                    draft: qyAiChannelToDraft(undefined, defaultPrompt),
                  })
                }
              >
                <Plus className='size-4' />
                {t('qy_ai_channel_add')}
              </Button>
            </div>

            <ChannelFormDialog
              editing={editing}
              guardCatalog={data.guard_catalog ?? []}
              elevateDefault={data.guard_elevate_default ?? []}
              existingHint={
                editing?.id
                  ? data.items?.find((c) => c.id === editing.id)?.key_hint
                  : undefined
              }
              onChange={(d) =>
                setEditing((cur) => (cur ? { ...cur, draft: d } : cur))
              }
              onClose={() => setEditing(null)}
              onSave={() => save.mutate()}
              saving={save.isPending}
            />
          </CardContent>
        </Card>
      ) : null}
    </QyPageBoundary>
  )
}

/**
 * 协议 → 列表徽章的 i18n 键。
 *
 * 写成表而不是三元链:每加一条协议,漏改这里的表现是列表上出现一个
 * 空徽章,而不是编译错误 —— 所以键的类型钉死在 QyAiProtocol 上,
 * 加了协议不补这一行就过不了 tsc。
 */
const QY_AI_PROTOCOL_TAGS: Record<QyAiProtocol, string> = {
  json_prompt: 'qy_ai_proto_json_short',
  qwen3guard: 'qy_ai_proto_guard_short',
  granite_guardian: 'qy_ai_proto_granite_short',
  llama_guard: 'qy_ai_proto_llama_short',
}

function ChannelRow({
  channel,
  categoryName,
  testing,
  onEdit,
  onTest,
  onDelete,
}: {
  channel: QyAiChannel
  /** 「计次记为」那一格指向的类型名。清单还没拉到时是空串。 */
  categoryName: string
  testing: boolean
  onEdit: () => void
  onTest: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const guard = channel.protocol === 'qwen3guard'
  const granite = channel.protocol === 'granite_guardian'
  // 查表而不是嵌套三元:协议还会继续加(护栏模型不止两家),而一串三元
  // 每加一档就再嵌一层,读的人分不清哪个冒号配哪个问号。
  const protocolTag = QY_AI_PROTOCOL_TAGS[channel.protocol]
  return (
    <div className='flex flex-wrap items-center gap-2 rounded-md border p-3'>
      <span className='font-medium'>{channel.name}</span>
      {/* 分组摆在名字后面,不能只藏在编辑弹窗里:作用域策略选的是**分组**,
          所以"这个渠道属于哪个池子"决定了它会不会被用到 —— 一个填错分组的
          渠道在列表上与一个正常的长得完全一样,而它一条流量都收不到。
          未分组的渠道同样要标出来:它只有被显式指定时才会被用到。 */}
      {channel.group ? (
        <Badge variant='secondary'>{channel.group}</Badge>
      ) : (
        <Badge variant='outline' className='text-muted-foreground'>
          {t('qy_ai_f_group_none')}
        </Badge>
      )}
      <Badge variant={channel.enabled ? 'default' : 'outline'}>
        {channel.enabled ? t('qy_ai_on') : t('qy_ai_off')}
      </Badge>
      {/* 协议摆在列表上,不能只藏在编辑弹窗里:三种渠道的请求体、成本、
          类型体系完全不同,而它们在列表上原本长得一模一样。 */}
      <Badge variant='outline'>{t(protocolTag as never)}</Badge>
      {/* Granite 的"审哪一类"住在风险名上,而它决定了这个渠道**判得出什么**
          —— 一个选了 violence 的渠道对越狱内容一律判 No(实测,见后端
          aireview_granite.go)。藏在编辑弹窗里的话,列表上两个判据完全不同的
          渠道长得一模一样。默认档不画:每一行都挂一个「harm」只是噪声。 */}
      {granite && channel.risk_name && channel.risk_name !== 'harm' && (
        <Badge variant='outline'>
          {t(`qy_ai_risk_${channel.risk_name}` as never)}
        </Badge>
      )}
      {/* 收紧档改变的是"多少内容会被判违规",与启停同一个量级的事实。
          宽松档是零值,不画 —— 每一行都挂一个「有争议: 放行」只是噪声。 */}
      {guard && channel.guard_controversial === 'unsafe' && (
        <Badge variant='outline'>{t('qy_ai_f_controversial_strict_tag')}</Badge>
      )}
      {guard && channel.guard_controversial === 'sensitive' && (
        <Badge variant='outline'>
          {t('qy_ai_f_controversial_sensitive_tag')}
        </Badge>
      )}
      {/* 停用了类别的渠道必须在列表上看得出来:少勾一类等于那一类的判定
          全部降档,而它在列表上原本与九类全开的渠道长得一模一样。 */}
      {guard && (channel.guard_categories ?? []).length > 0 && (
        <Badge variant='outline'>
          {t('qy_ai_f_guard_cats_tag', {
            n: (channel.guard_categories ?? []).length,
          })}
        </Badge>
      )}
      {/* 「计次记为」改的是这个渠道判出的违规计到谁的封号线上 —— 与启停同一个
          量级的事实。藏在编辑弹窗里的话,两个计数落点完全不同的渠道在列表上
          长得一模一样。不指定(0)是零值,不画:每一行都挂一个「按规则记」只是噪声。
          清单没拉到就显示 id,而不是显示空白 —— 空白看起来像"这一格没配"。 */}
      {channel.category_id > 0 && (
        <Badge variant='outline'>
          {t('qy_ai_f_category_tag', {
            name: categoryName || `#${channel.category_id}`,
          })}
        </Badge>
      )}
      <span className='text-muted-foreground text-xs'>{channel.base_url}</span>
      <span className='text-muted-foreground text-xs'>{channel.model}</span>
      {/* 密钥只显示掩码。接口本来就不下发明文,这里显示的是后端写入时算好的尾 4 位。 */}
      <span className='text-muted-foreground text-xs'>
        {channel.has_key
          ? t('qy_ai_key_set', { hint: channel.key_hint })
          : t('qy_ai_key_none')}
      </span>
      {/* 地址改过、而密钥还绑在旧地址上。这一行照样显示「已配置密钥」，
          可它一次都不会被调用 —— 不标出来就是一次静默的风控缺口。 */}
      {channel.key_bound_elsewhere && (
        <Badge variant='destructive'>{t('qy_ai_key_rebind_needed')}</Badge>
      )}
      <span className='text-muted-foreground text-xs'>
        {t('qy_ai_weight_label', { weight: channel.weight })}
      </span>
      <div className='ms-auto flex gap-2'>
        <Button size='sm' variant='outline' disabled={testing} onClick={onTest}>
          <Zap className='size-4' />
          {testing ? t('qy_ai_testing') : t('qy_ai_test')}
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

/**
 * 试跑回执。**原始响应是这一块的主角**,不是那几个统计数字。
 *
 * 协议对不上时界面上只有一个 `bad_json`,而它的三种成因(地址指到了别的
 * 服务 / 协议选错了 / 这个部署的输出格式与官方示例不同)长得完全一样 ——
 * 只有对着上游原文才分得开。护栏模型尤其:官方没有给出 OpenAI 兼容端点上的
 * 字段级规格,真机对不上时唯一的办法就是照着这一段调后端的正则。
 */
function ChannelTestPanel({
  result,
  onDismiss,
}: {
  result: QyAiChannelTestResult
  onDismiss: () => void
}) {
  const { t } = useTranslation()
  const ok = result.outcome === 'clean' || result.outcome === 'violation'
  return (
    <div className='ms-4 flex flex-col gap-2 rounded-md border border-dashed p-3'>
      <div className='flex flex-wrap items-center gap-2'>
        <Badge variant={ok ? 'default' : 'outline'}>{result.outcome}</Badge>
        <span className='text-muted-foreground text-xs'>
          {t('qy_ai_test_latency', {
            latency: result.latency_ms,
            budget: result.timeout_ms,
          })}
        </span>
        <span className='text-muted-foreground text-xs'>
          {t('qy_ai_test_tokens', {
            prompt: result.tokens.prompt,
            completion: result.tokens.completion,
          })}
        </span>
        {ok && (
          <span className='text-muted-foreground text-xs'>
            {t('qy_ai_test_verdict', {
              violated: result.violated
                ? t('qy_ai_test_violated')
                : t('qy_ai_test_clean'),
              category: result.category || '-',
              confidence: result.confidence,
            })}
          </span>
        )}
        <Button
          size='sm'
          variant='ghost'
          className='ms-auto'
          onClick={onDismiss}
        >
          {t('qy_ai_test_dismiss')}
        </Button>
      </div>

      {/* 冷启动:护栏模型首次调用要把权重加载进显存,超时是预期的。
          不说这一句的话,第一次试跑的超时看起来与"地址填错了"完全一样。 */}
      {result.hint === 'cold_start' && (
        <Alert>
          <AlertTriangle className='size-4' />
          <AlertTitle>{t('qy_ai_test_cold_start_title')}</AlertTitle>
          <AlertDescription>{t('qy_ai_test_cold_start_desc')}</AlertDescription>
        </Alert>
      )}

      {/* 模型回了一个本站类型表里没有的标识。护栏协议下这不是"提示词脱节",
          而是"本站还没建这个类型" —— 两者的下一步完全不同,所以文案要分开。 */}
      {result.raw_category && (
        <Alert>
          <AlertTriangle className='size-4' />
          <AlertTitle>{t('qy_ai_test_raw_category_title')}</AlertTitle>
          <AlertDescription>
            {result.protocol === 'qwen3guard'
              ? t('qy_ai_test_raw_category_guard', {
                  raw: result.raw_category,
                })
              : t('qy_ai_test_raw_category_json', { raw: result.raw_category })}
          </AlertDescription>
        </Alert>
      )}

      {result.reason && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_ai_test_reason', { reason: result.reason })}
        </p>
      )}

      <div className='flex flex-col gap-1'>
        <Label className='text-xs'>{t('qy_ai_test_raw_response')}</Label>
        <p className='text-muted-foreground text-xs'>
          {t('qy_ai_test_raw_response_hint')}
        </p>
        {/* overflow-x-auto:上游的错误页可能是一整行几百字符的 HTML,
            让它把整页撑出横向滚动条是本仓明确禁止的。 */}
        <pre className='bg-muted max-h-64 overflow-auto rounded-md p-2 text-xs whitespace-pre-wrap'>
          {result.raw_response || t('qy_ai_test_raw_response_empty')}
        </pre>
      </div>
    </div>
  )
}

/**
 * 两条审核路线的对照卡。
 *
 * 项目方明确要求"界面上要能选、要说清区别"。只给一个下拉框是不够的:
 * 两条路的**成本差一个数量级、类型体系完全不同**,而这两点在选之前看不出来,
 * 选错之后也不会报错 —— 只会表现为"账单比预想的高"或"这一类的计数一直是 0"。
 */
function ProtocolExplainer({
  protocol,
  guardCatalog,
}: {
  protocol: QyAiProtocol
  guardCatalog: QyAiGuardCategory[]
}) {
  const { t } = useTranslation()
  // hook 要在提前 return 之前无条件调用。
  const [mapOpen, setMapOpen] = useState(false)
  if (protocol === 'granite_guardian') {
    // 单独一段而不是并进 json_prompt 那一句:它**一次只审一种风险**,
    // 类别来自"问了什么"而不是模型的回答,而且没有置信度
    // (`ai_min_confidence` 配了也筛不掉什么)—— 这几件事只在这里说得出来,
    // 而选错的表现是"某一类的计数一直是 0"。
    return (
      <p className='text-muted-foreground text-xs'>
        {t('qy_ai_proto_granite_desc')}
      </p>
    )
  }
  if (protocol === 'llama_guard') {
    // 单独一段:它与 Qwen3Guard 同样是一次多标签,但类别是**另一套分类法**
    // (13 类 S 码,折进本站九类时是多对一)。不说这一句的话,运营会拿
    // Qwen3Guard 的九类去核对它的判定,而对不上是设计使然。
    return (
      <p className='text-muted-foreground text-xs'>
        {t('qy_ai_proto_llama_desc')}
      </p>
    )
  }
  if (protocol !== 'qwen3guard') {
    return (
      <p className='text-muted-foreground text-xs'>
        {t('qy_ai_proto_json_desc')}
      </p>
    )
  }
  const missing = guardCatalog.filter((c) => !c.present)
  return (
    <div className='flex flex-col gap-2'>
      <p className='text-muted-foreground text-xs'>
        {t('qy_ai_proto_guard_desc')}
      </p>
      {/* 对照表**默认不铺开**。项目方原话:「你写的这一堆在前端页面干嘛,
          还觉得页面不够乱吗?」—— 九行 `类别 → 标识` 里真正需要运营动手的
          只有对不上的那几条(它们的判定会折进兜底「未分类」),其余八九行是
          一份"一切正常"的清单,而正常状态一行字就说完了。完整对照收进
          折叠位,想核对的人点一下就有,不再占着表单最显眼的位置。

          `guardCatalog` 为空(接口回滚 / 旧后端)时两句都不说:此时
          `missing` 也是空,照着 `missing.length === 0` 去说"九类都对得上"
          是一句凭空捏造的结论。 */}
      {guardCatalog.length > 0 && (
        <>
          {missing.length === 0 ? (
            <p className='text-muted-foreground text-xs'>
              {t('qy_ai_proto_guard_map_ok')}
            </p>
          ) : (
            <p className='text-destructive text-xs'>
              {t('qy_ai_proto_guard_map_missing', {
                keys: missing.map((c) => c.key).join(', '),
              })}
            </p>
          )}
          <Collapsible open={mapOpen} onOpenChange={setMapOpen}>
            <CollapsibleTrigger className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs'>
              <ChevronRight
                aria-hidden='true'
                className={cn(
                  'size-3 transition-transform',
                  mapOpen && 'rotate-90'
                )}
              />
              {t('qy_ai_proto_guard_map_title')}
            </CollapsibleTrigger>
            <CollapsibleContent className='mt-1.5 flex flex-col gap-1.5'>
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_proto_guard_map_hint')}
              </p>
              <div className='flex flex-wrap gap-1'>
                {guardCatalog.map((c) => (
                  <Badge
                    key={c.id}
                    variant={c.present ? 'outline' : 'destructive'}
                    title={c.key}
                  >
                    {c.label} → {c.key}
                  </Badge>
                ))}
              </div>
            </CollapsibleContent>
          </Collapsible>
        </>
      )}
    </div>
  )
}

/**
 * 新建 / 编辑一条审核渠道的弹窗。
 *
 * `editing` 为 null = 关着 —— 与同页的 `ScopeFormDialog` 同一个形状:一个
 * 可空草稿,而不是 `open` + `draft` 两个状态(两个状态必然出现"开着但草稿
 * 是空"的第三种组合,而那一帧会在表单里读到 undefined)。
 *
 * 外壳用 `QyResponsiveDialog`(桌面居中 / 移动侧出 / 头尾固定、正文自己滚)。
 * 改造前这张表单是**内联**画在渠道卡正文里的:十来格字段加两组九选复选框
 * 直接把整张渠道列表挤到屏幕外,而「保存 / 取消」落在最底下 —— 编辑一条
 * 已有渠道时,人要先滚过所有字段才够得着保存键,也看不见自己在改哪一条。
 */
function ChannelFormDialog({
  editing,
  guardCatalog,
  elevateDefault,
  existingHint,
  onChange,
  onClose,
  onSave,
  saving,
}: {
  editing: { id?: number; draft: QyAiChannelDraft } | null
  guardCatalog: QyAiGuardCategory[]
  elevateDefault: string[]
  existingHint?: string
  onChange: (d: QyAiChannelDraft) => void
  onClose: () => void
  onSave: () => void
  saving: boolean
}) {
  const { t } = useTranslation()
  return (
    <QyResponsiveDialog
      open={editing !== null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title={
        editing?.id
          ? t('qy_ai_channel_edit_title')
          : t('qy_ai_channel_create_title')
      }
      // 十格字段加两组九选复选框,窄窗口里复选框会被压成一列。
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button variant='outline' onClick={onClose}>
            {t('qy_ai_cancel')}
          </Button>
          <Button disabled={saving || editing === null} onClick={onSave}>
            {t('qy_ai_save')}
          </Button>
        </>
      }
    >
      {editing && (
        <ChannelForm
          draft={editing.draft}
          guardCatalog={guardCatalog}
          elevateDefault={elevateDefault}
          existingHint={existingHint}
          onChange={onChange}
        />
      )}
    </QyResponsiveDialog>
  )
}

function ChannelForm({
  draft,
  guardCatalog,
  elevateDefault,
  existingHint,
  onChange,
}: {
  draft: QyAiChannelDraft
  guardCatalog: QyAiGuardCategory[]
  elevateDefault: string[]
  existingHint?: string
  onChange: (d: QyAiChannelDraft) => void
}) {
  const { t } = useTranslation()
  // 类型闭集与内置默认提示词都从设置接口来 —— 它是这两样的唯一权威来源。
  // 在这里各取一次(而不是从上层一路传下来)是因为这张表单可能被独立打开,
  // 而漏传的表现是预览区空白、对账恒为"没问题"。
  const settingQuery = useQuery(qyAiSettingsQuery())
  const categories = settingQuery.data?.categories ?? []
  const categoryBlock = settingQuery.data?.category_block ?? ''
  // 「计次记为」那一格要的是 id,而上面那份 `categories` 是拼进提示词的**名字**
  // 闭集,两者不是一回事。这里复用违规类型页那个 query(与作用域表单同一份),
  // 不另开端点:同一份事实开两个来源,迟早会出现两页各自认为对方是错的那种状态。
  const categoryQuery = useQuery(qyAdminViolationCategoriesQuery())
  // 只取下拉用得上的两样。**不**把整行传下去:那一行里有 `remark`(内部备注)
  // 与 `ai_guidance`(判定说明),两者都不该出现在一个"挑一个类型"的下拉里。
  const categoryOptions = (categoryQuery.data?.items ?? []).map((row) => ({
    id: row.category.id,
    name: row.category.name,
  }))
  // 「默认 / 已自定义」是**编辑中这一刻**的判断,不是接口回来的 prompt_source:
  // 后者只描述库里那一份。运营在框里删掉一个字,标记必须当场翻档 ——
  // 那正是这一档差别(自定义之后不再跟随默认提示词升级)唯一会被注意到的时刻。
  const promptIsDefault = qyAiPromptIsDefault(draft.prompt, draft.defaultPrompt)
  const renderedPrompt = qyAiRenderPrompt(
    draft.prompt,
    draft.defaultPrompt,
    categoryBlock
  )
  const promptIssues = qyAiPromptCategoryIssues(renderedPrompt, categories)
  // 护栏协议不读提示词。说出来而不是把那一格藏掉:藏掉之后换回通用模型
  // 会发现提示词"没了",而它其实一直在库里。
  const guardProtocol = qyAiIsGuardProtocol(draft.protocol)
  // 本地部署那几句提示(地址、超时、密钥可留空)对**三条**护栏协议都成立 ——
  // 它们说的是"这类模型通常挂在本机 Ollama / vLLM 上",与是哪一家无关。
  // 早先只按 qwen3guard 判,于是 Granite 渠道拿到的是云端模型那套说明。
  const localGuard = guardProtocol
  // 外框、内边距与「保存 / 取消」都归 ChannelFormDialog 管:按钮必须留在
  // 弹窗的 footer 上,跟着正文滚出屏幕的保存键等于没有保存键。
  return (
    <div className='flex flex-col gap-3'>
      <Field label={t('qy_ai_f_protocol')} hint={t('qy_ai_f_protocol_hint')}>
        <Select
          value={draft.protocol}
          onValueChange={(v) =>
            onChange(qyAiApplyProtocol(draft, v as QyAiProtocol))
          }
        >
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='json_prompt'>{t('qy_ai_proto_json')}</SelectItem>
            <SelectItem value='qwen3guard'>{t('qy_ai_proto_guard')}</SelectItem>
            <SelectItem value='granite_guardian'>
              {t('qy_ai_proto_granite')}
            </SelectItem>
            <SelectItem value='llama_guard'>
              {t('qy_ai_proto_llama')}
            </SelectItem>
          </SelectContent>
        </Select>
      </Field>
      {/* 「这两个我该选哪个」在下拉框里答不出来。最要紧的一句是**它根本不是
          二选一**:后端按权重在所有启用渠道之间分流(aireview_guard.go),
          同一个站点两种渠道都配上是预期用法 —— 便宜的护栏模型接住标准安全
          类目,通用模型去管护栏模型判不出来的本站特有违规。 */}
      <div className='bg-muted/40 flex flex-col gap-2 rounded-md p-2.5'>
        <ProtocolExplainer
          protocol={draft.protocol}
          guardCatalog={guardCatalog}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_ai_proto_choose_hint')}
        </p>
      </div>
      <div className='grid gap-3 sm:grid-cols-2'>
        <Field label={t('qy_ai_f_name')}>
          <Input
            value={draft.name}
            onChange={(e) => onChange({ ...draft, name: e.target.value })}
          />
        </Field>
        {/* 审核渠道分组:一组可以互相顶替的端点。作用域按它选池子,故障转移
            也只在组内补位 —— 留空是"未分组"这一档,不是"属于所有分组"。 */}
        <Field label={t('qy_ai_f_group')} hint={t('qy_ai_f_group_hint')}>
          <Input
            value={draft.group}
            placeholder={t('qy_ai_f_group_ph')}
            onChange={(e) => onChange({ ...draft, group: e.target.value })}
          />
        </Field>
        <Field label={t('qy_ai_f_model')}>
          <Input
            value={draft.model}
            onChange={(e) => onChange({ ...draft, model: e.target.value })}
          />
        </Field>
        <Field
          label={t('qy_ai_f_base_url')}
          hint={
            localGuard
              ? t('qy_ai_f_base_url_hint_guard')
              : t('qy_ai_f_base_url_hint')
          }
        >
          <Input
            value={draft.base_url}
            placeholder={localGuard ? 'http://localhost:11434/v1' : undefined}
            onChange={(e) => onChange({ ...draft, base_url: e.target.value })}
          />
        </Field>
        {/* Granite 一次只审**一种**风险,而审哪一种就是这一格。
            它不是提示词:发出去的是一个裸的风险名,模板拿它精确匹配一份
            内置的风险定义(见后端 aireview_granite.go)。
            选错的后果是**漏判**而不是报错 —— 实测同一段越狱文本在
            violence 档下判 No、在 jailbreak / harm 档下判 Yes —— 所以每一档
            都要有一句说明,而不是只给一个下拉框。 */}
        {draft.protocol === 'granite_guardian' && (
          <Field
            label={t('qy_ai_f_risk')}
            hint={t(`qy_ai_risk_${draft.risk_name || 'harm'}_hint` as never)}
          >
            <Select
              value={draft.risk_name || 'harm'}
              onValueChange={(v) =>
                onChange({ ...draft, risk_name: v as QyAiGraniteRisk })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {QY_AI_GRANITE_RISKS.map((r) => (
                  <SelectItem key={r} value={r}>
                    {t(`qy_ai_risk_${r}` as never)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        )}
        {/* 只在护栏协议下画:通用模型那条路根本没有 Controversial 这一档,
            画一个存不下去的输入框只会让人以为它生效了。 */}
        {draft.protocol === 'qwen3guard' && (
          <Field
            label={t('qy_ai_f_controversial')}
            hint={t('qy_ai_f_controversial_hint')}
          >
            <Select
              value={draft.guard_controversial || 'safe'}
              onValueChange={(v) =>
                onChange({
                  ...draft,
                  guard_controversial: v as 'safe' | 'sensitive' | 'unsafe',
                })
              }
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='safe'>
                  {t('qy_ai_f_controversial_safe')}
                </SelectItem>
                <SelectItem value='sensitive'>
                  {t('qy_ai_f_controversial_sensitive')}
                </SelectItem>
                <SelectItem value='unsafe'>
                  {t('qy_ai_f_controversial_unsafe')}
                </SelectItem>
              </SelectContent>
            </Select>
          </Field>
        )}
        {/* 密钥输入是**三态**:不碰 = 保持原值(占位符显示掩码),
            填空 = 清除,填新值 = 换新。见 lib/ai-review.ts。 */}
        <Field
          label={t('qy_ai_f_api_key')}
          hint={
            existingHint
              ? t('qy_ai_f_api_key_hint_existing', { hint: existingHint })
              : localGuard
                ? // 本地 Ollama / vLLM 通常没有密钥。不说这一句的话,运营会
                  // 以为这一格是必填,而后端从来不要求它。
                  t('qy_ai_f_api_key_hint_local')
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
        <Field
          label={t('qy_ai_f_timeout')}
          hint={
            localGuard
              ? t('qy_ai_f_timeout_hint_guard')
              : t('qy_ai_f_timeout_hint')
          }
        >
          <Input
            type='number'
            value={draft.timeout_ms}
            onChange={(e) =>
              onChange({ ...draft, timeout_ms: Number(e.target.value) || 0 })
            }
          />
        </Field>
        <Field label={t('qy_ai_f_price_in')}>
          <Input
            value={draft.price_in_per_m}
            onChange={(e) =>
              onChange({ ...draft, price_in_per_m: e.target.value })
            }
          />
        </Field>
        <Field label={t('qy_ai_f_price_out')}>
          <Input
            value={draft.price_out_per_m}
            onChange={(e) =>
              onChange({ ...draft, price_out_per_m: e.target.value })
            }
          />
        </Field>
        {/* 两个价格框共用一行说明:同一句话逐字挂两遍只是噪声。 */}
        <p className='text-muted-foreground text-xs sm:col-span-2'>
          {t('qy_ai_f_price_hint')}
        </p>
      </div>
      {/* 九类启用清单对 Qwen3Guard 与 Llama Guard 都生效(两者都一次给出
          多标签)。Granite 不画:它一次只审一种风险,"审哪一类"住在风险名上,
          给它留一份九类清单等于给运营一个勾了不生效的开关。 */}
      {qyAiProtocolHasCategories(draft.protocol) && (
        <GuardCategoryPickers
          draft={draft}
          guardCatalog={guardCatalog}
          elevateDefault={elevateDefault}
          onChange={onChange}
        />
      )}
      <label className='flex items-center gap-2'>
        <Switch
          checked={draft.enabled}
          onCheckedChange={(v) => onChange({ ...draft, enabled: v })}
        />
        <span className='text-sm'>{t('qy_ai_f_enabled')}</span>
      </label>

      {/* ── 审核提示词 ──

          2026-09-06 从「全局设置 / 作用域」整体搬到这里。理由是它与**协议**绑死:
          护栏协议压根不发提示词(aiRequestPayload 对它们只发一条 user 消息),
          而挂在别处就允许"一份提示词被分发到一个根本不读提示词的渠道" ——
          配得出来、不报错、完全不生效。

          所以护栏协议下**整块不渲染**,只留一句话说明为什么没有这一格。
          画一个填了不生效的输入框比不画更糟:运营会对着它以为判据换过了,
          而线上一个字都没送出去。

          Granite 是个例外中的例外:它**确实**收一条 system 消息,但那不是提示词
          而是**风险名槽** —— 模板拿它去精确匹配一份内置的风险定义
          (见后端 aireview_granite.go 引的那段 Modelfile)。所以它在这里同样
          不画提示词框,改判据要用下面那个「审核风险」下拉。 */}
      {guardProtocol ? (
        <p className='text-muted-foreground text-xs'>
          {t('qy_ai_prompt_none_for_guard')}
        </p>
      ) : (
        <div className='flex flex-col gap-1.5'>
          <div className='flex items-center gap-2'>
            <Label className='text-sm'>{t('qy_ai_prompt')}</Label>
            <Badge variant={promptIsDefault ? 'outline' : 'default'}>
              {promptIsDefault
                ? t('qy_ai_prompt_badge_default')
                : t('qy_ai_prompt_badge_custom')}
            </Badge>
            {!promptIsDefault && (
              <Button
                size='sm'
                variant='outline'
                onClick={() =>
                  onChange({ ...draft, prompt: draft.defaultPrompt })
                }
              >
                {t('qy_ai_prompt_reset')}
              </Button>
            )}
          </div>
          <Textarea
            rows={10}
            value={draft.prompt}
            onChange={(e) => onChange({ ...draft, prompt: e.target.value })}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_ai_prompt_hint', { categories: categories.join(', ') })}
          </p>
          {/* 类型清单是**发送前自动拼进去**的,编辑框里那段文本不是模型读到的
              东西。没有预览时,"我改的那一下到底生效没有"完全不可回答。 */}
          <details className='rounded-md border p-2'>
            <summary className='cursor-pointer text-xs font-medium'>
              {t('qy_ai_prompt_preview_title')}
            </summary>
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('qy_ai_prompt_preview_desc')}
            </p>
            <pre className='bg-muted mt-2 max-h-72 overflow-auto rounded p-2 text-xs whitespace-pre-wrap'>
              {renderedPrompt}
            </pre>
          </details>
          {promptIssues.unknown.length > 0 && (
            <Alert>
              <AlertTriangle className='size-4' />
              <AlertTitle>{t('qy_ai_prompt_cat_unknown_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_ai_prompt_cat_unknown_desc', {
                  names: promptIssues.unknown.join(', '),
                })}
              </AlertDescription>
            </Alert>
          )}
        </div>
      )}

      {/* 拦截文案:命中拦截时**直接显示给终端用户**的一句话。
          留空 = 沿用规则自己配的那一份(本地词表规则没有渠道,只可能用它)。 */}
      <Field
        label={t('qy_ai_f_block_message')}
        hint={t('qy_ai_f_block_message_hint')}
      >
        <Input
          value={draft.block_message}
          placeholder={t('qy_ai_f_block_message_ph')}
          onChange={(e) =>
            onChange({ ...draft, block_message: e.target.value })
          }
        />
      </Field>

      {/* 「计次记为」:这个渠道判出的违规,计次加到哪个违规类型上。

          它是护栏协议那条路的落点。护栏模型的类别是训练时钉死的,本站类型表里
          没有同名标识的那几类会折进兜底「未分类」,而兜底类型的阈值出厂是 0 ——
          判了、记了,却一次都不推进封号线。要么去类型页把缺的标识逐个建出来,
          要么在这里指定一个类型,让这个渠道判出来的每一条都记进它并计次。

          优先级是**作用域 > 渠道 > 规则**:作用域那一格写的是「一律记为」,
          而同一个渠道会被多档作用域用到。留「不指定」时行为与这一格出现之前
          完全一致。 */}
      <Field label={t('qy_ai_f_category')} hint={t('qy_ai_f_category_hint')}>
        <Select
          value={String(draft.category_id)}
          onValueChange={(v) =>
            onChange({ ...draft, category_id: Number(v) || 0 })
          }
        >
          <SelectTrigger>
            <SelectValue placeholder={t('qy_ai_scope_category_none')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='0'>{t('qy_ai_scope_category_none')}</SelectItem>
            {categoryOptions.map((cat) => (
              <SelectItem key={cat.id} value={String(cat.id)}>
                {cat.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {/* 清单拉不到时说出来,而不是显示一个只有「不指定」的下拉:后者看起来
            像"这个站点没有违规类型",那是一句谎话。文案与作用域那一格共用 ——
            同一句话抄两遍,改的时候只会改一遍。 */}
        {!categoryQuery.isSuccess && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_ai_scope_category_loading')}
          </p>
        )}
      </Field>

      <ChannelEmailNotice draft={draft} onChange={onChange} />
    </div>
  )
}

/**
 * 「判违规就给用户发一封邮件」那一段。
 *
 * ══════════════ 为什么两格模板留空不预填 ══════════════
 *
 * 与提示词那一格刻意相反:提示词要在内置默认的基础上改,所以预填全文;
 * 而这两格留空的含义是「跟随内置模板走」。预填进来的话,每个渠道保存一次
 * 就把自己钉死在当前版本的默认邮件上,以后对默认模板的任何改动都发不过来。
 * 想改的人自己粘一份进来 —— 预览区里那一份就是内置默认长什么样的答案。
 *
 * ══════════════ 预览为什么用 iframe 而不是 dangerouslySetInnerHTML ══════════════
 *
 * 这段 HTML 是管理员自己写的,不是外部输入 —— 但它会被原样发出去,而管理端
 * 与它同源。用 sandbox 过的 iframe 渲染,等于预览里的脚本、表单、外链都跑不动,
 * 而"发出去之后长什么样"这个问题照样答得上。
 */
function ChannelEmailNotice({
  draft,
  onChange,
}: {
  draft: QyAiChannelDraft
  onChange: (next: QyAiChannelDraft) => void
}) {
  const { t } = useTranslation()
  const previewBody = qyAiRenderEmailPreview(
    draft.email_body.trim() || t('qy_ai_email_body_builtin'),
    true
  )
  const previewSubject = qyAiRenderEmailPreview(
    draft.email_subject.trim() || t('qy_ai_email_subject_builtin'),
    false
  )

  return (
    <div className='flex flex-col gap-3 rounded-md border p-3'>
      <label className='flex items-start gap-2'>
        <Switch
          checked={draft.notify_email}
          onCheckedChange={(v) => onChange({ ...draft, notify_email: v })}
        />
        <span className='flex flex-col gap-0.5'>
          <span className='text-sm font-medium'>{t('qy_ai_email_notify')}</span>
          <span className='text-muted-foreground text-xs'>
            {t('qy_ai_email_notify_hint')}
          </span>
        </span>
      </label>

      {draft.notify_email && (
        <>
          <Field
            label={t('qy_ai_email_subject')}
            hint={t('qy_ai_email_subject_hint')}
          >
            <Input
              value={draft.email_subject}
              placeholder={t('qy_ai_email_subject_builtin')}
              onChange={(e) =>
                onChange({ ...draft, email_subject: e.target.value })
              }
            />
          </Field>

          <Field
            label={t('qy_ai_email_body')}
            hint={t('qy_ai_email_body_hint')}
          >
            <Textarea
              className='font-mono text-xs'
              rows={10}
              value={draft.email_body}
              placeholder={t('qy_ai_email_body_ph')}
              onChange={(e) =>
                onChange({ ...draft, email_body: e.target.value })
              }
            />
          </Field>

          {/* 占位符表由 QY_AI_EMAIL_VARS 派生,不手抄:抄本会在下一次加占位符
              时过期,而过期的表现是运营照着界面写了一个不会被替换的 {{xxx}}。 */}
          <div className='flex flex-col gap-1.5'>
            <p className='text-xs font-medium'>{t('qy_ai_email_vars')}</p>
            <div className='flex flex-wrap gap-1'>
              {QY_AI_EMAIL_VARS.map((key) => (
                <code
                  key={key}
                  className='bg-muted rounded px-1.5 py-0.5 text-[11px]'
                >
                  {`{{${key}}}`}
                </code>
              ))}
            </div>
            <p className='text-muted-foreground text-xs'>
              {t('qy_ai_email_vars_hint')}
            </p>
          </div>

          <details className='rounded-md border p-2'>
            <summary className='cursor-pointer text-xs font-medium'>
              {t('qy_ai_email_preview_title')}
            </summary>
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('qy_ai_email_preview_desc')}
            </p>
            <p className='mt-2 text-xs'>
              <span className='text-muted-foreground'>
                {t('qy_ai_email_preview_subject')}
              </span>{' '}
              <span className='font-medium'>{previewSubject}</span>
            </p>
            <iframe
              title={t('qy_ai_email_preview_title')}
              sandbox=''
              srcDoc={previewBody}
              className='bg-background mt-2 h-64 w-full rounded border'
            />
          </details>
        </>
      )}
    </div>
  )
}

/**
 * 护栏渠道的两张类别清单:**启用哪几类**,以及 sensitive 档下**哪几类升级成拦截**。
 *
 * ═══════════ 为什么两张都是"空 = 默认",而默认各不相同 ═══════════
 *
 * 一格都不勾在两张表上是两个不同的意思,而这是本页最容易被误读的一处,
 * 所以两处都把默认值**画出来**而不是只写在说明里:
 *
 *   启用类别   空 = 九类全启用。它必须是这个方向 —— 空是存量渠道的取值,
 *              而"空 = 一个都不启用"会让升级那一秒起所有护栏渠道的判定
 *              全部降档,界面上却一切正常。
 *   升级类别   空 = 参考实现(sub2api)的三类,后端下发的 elevateDefault
 *              就是那三个。想要"完全不升级"请把「有争议」改回放行档 ——
 *              一张空的升级表配 sensitive 档,等价于放行档,而界面上它
 *              写着"命中敏感类别时拦截",那是一句假话。
 *
 * 停用一个类别**不等于**把那一类的判定丢掉:后端在「Unsafe 且解析出的类别
 * 全被停用」时仍然判违规,只把置信度从 0.95 降到 0.6,交给规则上的
 * ai_min_confidence 决定要不要吃。这一句写在界面上,否则运营会以为取消勾选
 * 等于让那一类彻底不生效。
 */
function GuardCategoryPickers({
  draft,
  guardCatalog,
  elevateDefault,
  onChange,
}: {
  draft: QyAiChannelDraft
  guardCatalog: QyAiGuardCategory[]
  elevateDefault: string[]
  onChange: (d: QyAiChannelDraft) => void
}) {
  const { t } = useTranslation()
  const toggle = (list: string[], id: string, on: boolean) =>
    on ? [...list, id] : list.filter((x) => x !== id)
  // 启用清单为空时九类全启用,所以复选框全部显示为勾上的 —— 显示"全不勾"
  // 会让运营以为这个渠道什么都不审。取消其中一个时,把"全启用"这个隐式
  // 状态展开成显式的八项,否则第一次取消会变成"只启用这一项"。
  const allEnabled = draft.guard_categories.length === 0
  const enabledIds = qyAiGuardShownIds(
    draft.guard_categories,
    guardCatalog.map((c) => c.id)
  )
  const elevateOn = draft.guard_controversial === 'sensitive'
  const elevateEmpty = draft.guard_elevate.length === 0
  const elevateIds = qyAiGuardShownIds(draft.guard_elevate, elevateDefault)
  return (
    <div className='flex flex-col gap-3 rounded-md border p-3'>
      <div className='flex flex-col gap-1.5'>
        <Label>{t('qy_ai_f_guard_cats')}</Label>
        <p className='text-muted-foreground text-xs'>
          {allEnabled
            ? t('qy_ai_f_guard_cats_hint_all')
            : t('qy_ai_f_guard_cats_hint_subset')}
        </p>
        <div className='grid gap-1 sm:grid-cols-3'>
          {guardCatalog.map((c) => (
            <Label
              key={c.id}
              htmlFor={`qy-ai-cat-${c.id}`}
              className='flex items-center gap-2 text-sm font-normal'
            >
              <Checkbox
                id={`qy-ai-cat-${c.id}`}
                checked={enabledIds.includes(c.id)}
                // 最后一格不许取消:空清单在后端的含义是「九类全启用」,
                // 于是取消最后一个会跳回全启用 —— 一次点了却反向生效的操作。
                // 要整个停掉这个渠道请用下面的启用开关。
                disabled={enabledIds.length === 1 && enabledIds.includes(c.id)}
                onCheckedChange={(checked) =>
                  onChange({
                    ...draft,
                    guard_categories: toggle(
                      enabledIds,
                      c.id,
                      checked === true
                    ),
                  })
                }
              />
              <span className='min-w-0 truncate' title={c.key}>
                {t(`qy_ai_guard_cat_${c.id}`, { defaultValue: c.label })}
              </span>
            </Label>
          ))}
        </div>
      </div>
      {elevateOn && (
        <div className='flex flex-col gap-1.5'>
          <Label>{t('qy_ai_f_guard_elevate')}</Label>
          <p className='text-muted-foreground text-xs'>
            {elevateEmpty
              ? t('qy_ai_f_guard_elevate_hint_default')
              : t('qy_ai_f_guard_elevate_hint')}
          </p>
          <div className='grid gap-1 sm:grid-cols-3'>
            {guardCatalog.map((c) => (
              <Label
                key={c.id}
                htmlFor={`qy-ai-elev-${c.id}`}
                className='flex items-center gap-2 text-sm font-normal'
              >
                <Checkbox
                  id={`qy-ai-elev-${c.id}`}
                  checked={elevateIds.includes(c.id)}
                  // 同上:空清单 = 参考实现的三类。想要「完全不升级」请把
                  // 上面的「有争议」改回放行档 —— 一张空的升级表配 sensitive
                  // 档等价于放行档,而那一格写着"命中敏感类别时拦截"。
                  disabled={
                    elevateIds.length === 1 && elevateIds.includes(c.id)
                  }
                  onCheckedChange={(checked) =>
                    onChange({
                      ...draft,
                      guard_elevate: toggle(elevateIds, c.id, checked === true),
                    })
                  }
                />
                <span className='min-w-0 truncate'>
                  {t(`qy_ai_guard_cat_${c.id}`, { defaultValue: c.label })}
                </span>
              </Label>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function Field({
  label,
  hint,
  required,
  children,
}: {
  label: string
  hint?: string
  /**
   * 必填。星号只是记号,真正拦住保存的是弹窗那一层的校验 —— 但没有它,
   * 「为什么保存键是灰的」要靠人往下读一行小字才答得出来。
   */
  required?: boolean
  children: React.ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-col gap-1.5'>
      <Label>
        {label}
        {required && (
          <span className='text-destructive ms-0.5' aria-hidden='true'>
            *
          </span>
        )}
        {/* 星号只有颜色和形状,读屏软件念不出"必填"。 */}
        {required && <span className='sr-only'> {t('qy_ai_f_required')}</span>}
      </Label>
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
