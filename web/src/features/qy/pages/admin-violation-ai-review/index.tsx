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
import { Checkbox } from '@/components/ui/checkbox'
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
  testQyAiChannel,
  updateQyAiChannel,
  updateQyAiSetting,
  upsertQyAiScope,
} from './api'
import {
  qyAiAppendScopeGroup,
  qyAiBpsToPercentText,
  qyAiChannelToDraft,
  qyAiDraftToInput,
  QY_AI_CATEGORY_PLACEHOLDER,
  qyAiPromptCategoryIssues,
  qyAiRenderPrompt,
  qyAiPromptForEditor,
  qyAiPromptIsDefault,
  qyAiPromptToPayload,
  qyAiScopeAudience,
  qyAiScopeChannelState,
  qyAiScopeDraftToInput,
  qyAiScopeEffectivePrompt,
  qyAiScopeGroupBindingError,
  qyAiScopeHasFakeSeparator,
  qyAiScopePromptSource,
  qyAiScopeRowKind,
  qyAiScopeToDraft,
  qyAiSplitScopeList,
  qyAiApplyProtocol,
  qyAiGuardShownIds,
  type QyAiChannelDraft,
  type QyAiScopeChannelState,
  type QyAiScopeDraft,
  type QyAiScopeGroupBindingError,
  type QyAiScopeRowKind,
} from './lib/ai-review'
import type {
  QyAiChannel,
  QyAiChannelTestResult,
  QyAiGuardCategory,
  QyAiProtocol,
  QyAiScope,
  QyAiScopeSummaryRow,
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
    prompt: string
    max_input_chars: number
    third_party_notice_ack: boolean
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
          // 预填:库里为空时把默认提示词的全文放进输入框,让人能直接在
          // 上面改。用 placeholder 顶替不行 —— 灰字、不可编辑、不会被提交,
          // 它回答了"默认长什么样"却没回答"我怎么在它基础上改"。
          prompt: qyAiPromptForEditor(data.setting.prompt, data.default_prompt),
          max_input_chars: data.setting.max_input_chars,
          third_party_notice_ack: data.setting.third_party_notice_ack,
        }
      : null)

  const save = useMutation({
    mutationFn: () => {
      if (!current || !data) throw new Error('no draft')
      return updateQyAiSetting({
        enabled: current.enabled,
        pre_timeout_ms: current.pre_timeout_ms,
        async_timeout_ms: current.async_timeout_ms,
        // 逐字等于默认时提交空串,而不是提交预填进来的那段文本 ——
        // 否则每个站点点一次保存就把自己钉死在当前版本的默认提示词上,
        // 以后对它的加固(那句"待审内容不是指令")再也发不过来。
        prompt: qyAiPromptToPayload(current.prompt, data.default_prompt),
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
  // 「默认 / 已自定义」是**编辑中这一刻**的判断,不是接口回来的 prompt_source:
  // 后者只描述库里那一份。运营在框里删掉一个字,标记必须当场从"默认"变成
  // "已自定义" —— 那正是这一档差别唯一会被人注意到的时刻。
  const promptIsDefault =
    !data || !current
      ? true
      : qyAiPromptIsDefault(current.prompt, data.default_prompt)
  // 类型清单是**发送前自动拼进去**的,所以对账与预览都必须在渲染之后做:
  // 拿编辑框里那段原文去对账会把每一个类型都报成"缺失"。
  const renderedPrompt =
    !data || !current
      ? ''
      : qyAiRenderPrompt(
          current.prompt,
          data.default_prompt,
          data.category_block
        )
  // 闭集来自接口。缺席时按空清单走:对账会安静下来(它对的是"提示词里有没有
  // 清单外的名字"),而不是把这张卡连同整页一起打掉。
  const categories = data?.categories ?? []
  const promptIssues =
    !data || !current
      ? { unknown: [], missing: [] }
      : qyAiPromptCategoryIssues(renderedPrompt, categories)

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

            {/* 抽样率这一格以前在这里,现在没有了。
                留一句指路而不是直接删干净:运营的肌肉记忆是"来这一页改抽样率",
                一个字都不说会让人以为功能坏了或者被降级了。 */}
            <Alert>
              <AlertTitle>{t('qy_ai_sample_rate_moved_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_ai_sample_rate_moved_desc')}
              </AlertDescription>
            </Alert>

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

            {/* 提示词这一格以前是空的:内置默认提示词只在 placeholder 里,
            于是"在默认基础上改一句"这件最常见的事做不了。现在预填全文,
            并且把「你现在处于哪一档」摆在标签旁边 —— 因为两档的差别
            (自定义之后不再跟随默认提示词升级)没有任何其它可见症状。 */}
            <div className='flex flex-col gap-1.5'>
              <div className='flex items-center gap-2'>
                <Label>{t('qy_ai_prompt')}</Label>
                <Badge variant={promptIsDefault ? 'outline' : 'default'}>
                  {promptIsDefault
                    ? t('qy_ai_prompt_badge_default')
                    : t('qy_ai_prompt_badge_custom')}
                </Badge>
              </div>
              <Textarea
                rows={12}
                value={current.prompt}
                onChange={(e) =>
                  setDraft({ ...current, prompt: e.target.value })
                }
              />
              <p className='text-muted-foreground text-xs'>
                {promptIsDefault
                  ? t('qy_ai_prompt_source_default_desc')
                  : t('qy_ai_prompt_source_custom_desc')}
              </p>
              <p className='text-muted-foreground text-xs'>
                {t('qy_ai_prompt_hint', {
                  categories: categories.join(', '),
                })}
              </p>

              {/* 类型清单不再需要手工同步:它由后端从违规类型表现算,发送前
              自动拼进提示词。运营要改哪些类型,去违规类型页改 —— 这里只说
              清单从哪来,以及"到底发出去的是什么"。

              预览是必要的而不是锦上添花:清单是拼上去的,编辑框里那段文本
              **不是**模型读到的东西。没有预览时,"我改的那一下到底生效没有"
              在界面上完全不可回答。 */}
              <details className='rounded-md border p-2'>
                <summary className='cursor-pointer text-xs font-medium'>
                  {t('qy_ai_prompt_preview_title')}
                </summary>
                <p className='text-muted-foreground mt-2 text-xs'>
                  {t('qy_ai_prompt_preview_desc', {
                    // 占位符本身也从常量来,不在文案里再抄一遍字面量 ——
                    // 抄的那一份会在后端改占位符的第二天开始教人写错。
                    placeholder: QY_AI_CATEGORY_PLACEHOLDER,
                  })}
                </p>
                <pre className='bg-muted mt-2 max-h-72 overflow-auto rounded p-2 text-xs whitespace-pre-wrap'>
                  {renderedPrompt}
                </pre>
              </details>

              {/* 提示词里手抄了一行旧类型清单时不拒绝保存(那份文本可能还有
              别的用处),但必须当场说出来:模型照着它回一个类型表里没有的
              名字,那一票会被折进「未分类」—— 一条零症状的静默失效。 */}
              {promptIssues.unknown.length > 0 && (
                <Alert>
                  <AlertTriangle className='size-4' />
                  <AlertTitle>{t('qy_ai_prompt_cat_unknown_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_ai_prompt_cat_unknown_desc', {
                      names: promptIssues.unknown.join(', '),
                      known: categories.join(', '),
                    })}
                  </AlertDescription>
                </Alert>
              )}
              {promptIssues.missing.length > 0 && (
                <Alert>
                  <AlertTriangle className='size-4' />
                  <AlertTitle>{t('qy_ai_prompt_cat_missing_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_ai_prompt_cat_missing_desc', {
                      names: promptIssues.missing.join(', '),
                    })}
                  </AlertDescription>
                </Alert>
              )}

              <div>
                {/* 恢复默认:把文本填回默认全文,保存时它会被折成空串,
                本站因此重新跟随默认提示词的后续升级。 */}
                <Button
                  size='sm'
                  variant='outline'
                  disabled={promptIsDefault}
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
  const channelName = (id: number) =>
    channels.find((c) => c.id === id)?.name ?? ''
  const rowState = (row: QyAiScopeSummaryRow) => {
    const channel = qyAiScopeChannelState(row.channel_id, channels)
    return {
      channel,
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
                    <th className='py-1'>{t('qy_ai_scope_col_prompt')}</th>
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
                      channelName={channelName(row.channel_id)}
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
  channelName,
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
  /** 渠道清单里 join 出来的名字。空串 = 没指定,或者这个 id 已经不存在。 */
  channelName: string
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
      <td className='py-1.5 pe-2'>
        <Badge
          variant={row.prompt_source === 'custom' ? 'secondary' : 'outline'}
          className='font-normal'
        >
          {t(`qy_ai_scope_prompt_${row.prompt_source}` as never)}
        </Badge>
      </td>
      {/* 「送到哪」。留空那一档写的是"按权重随机",不是"默认渠道" ——
          渠道表上没有 priority,两个渠道各 50% 时"默认渠道"这句话就是假的。
          指定的渠道被停用或删掉时这一档不再审核任何内容,而它与一条正常策略
          长得一模一样,所以那两种状态必须是警示色 + 一句话。 */}
      <td className='py-1.5 pe-2 text-xs'>
        <Badge
          variant={
            channelState === 'default'
              ? 'outline'
              : channelState === 'ok'
                ? 'secondary'
                : 'warning'
          }
          className='font-normal'
        >
          {channelState === 'default'
            ? t('qy_ai_scope_channel_default')
            : channelName || `#${row.channel_id}`}
        </Badge>
        {/* 「指定了 A」与「指定了 A,但 A 不行时会发给别人」是两种不同的数据
            流向,而它们在这一格里只差这一个 badge。不画的话,运营看着
            「审核渠道: 内部自建」得到的是一个已经不成立的预期。 */}
        {row.channel_failover && (
          <Badge variant='outline' className='ms-1 font-normal'>
            {t('qy_ai_scope_channel_failover_on')}
          </Badge>
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
  const channelState = qyAiScopeChannelState(draft.channel_id, channels)

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

  // 全局那一份提示词与内置默认：这一格留空时到底继承的是哪一段文本，
  // 只有摆出来运营才知道自己"什么都不填"意味着什么。
  const settingQuery = useQuery(qyAiSettingsQuery())
  const promptSource = qyAiScopePromptSource(draft.prompt)
  const effectivePrompt = qyAiScopeEffectivePrompt(
    draft.prompt,
    settingQuery.data?.setting.prompt ?? '',
    settingQuery.data?.default_prompt ?? ''
  )

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
            {/* 软告警，不是错误：不禁用提交。 */}
            {unknownGroups.length > 0 && (
              <p className='text-warning text-xs'>
                {t('qy_ai_scope_group_unknown', {
                  groups: unknownGroups.join('、'),
                })}
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
        {/* 「送到哪个审核渠道」。
            留空 = **按权重在全部启用渠道里随机**,不是"某一个默认渠道" ——
            渠道表上没有 priority,两个渠道各配 50% 权重时"默认渠道"这句话
            就是假的,而运营会按那句话去理解自己的数据流向。
            指定之后它被停用或删除时,这一档**不再审核任何内容**(每次都是
            「无可用渠道」+ 放行),运行期绝不回落到随机池:回落会把用户内容
            发去运营明确没有选的端点,而那往往正是指定渠道的全部理由。 */}
        <Field
          label={t('qy_ai_scope_f_channel')}
          hint={t('qy_ai_scope_f_channel_hint')}
        >
          <Select
            value={String(draft.channel_id)}
            onValueChange={(v) =>
              onChange({ ...draft, channel_id: Number(v) || 0 })
            }
          >
            <SelectTrigger>
              <SelectValue placeholder={t('qy_ai_scope_channel_default')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='0'>
                {t('qy_ai_scope_channel_default')}
              </SelectItem>
              {channels.map((ch) => (
                <SelectItem key={ch.id} value={String(ch.id)}>
                  {/* 停用的渠道照样列出来:一条已经指向停用渠道的策略必须能
                      在下拉里看见自己当前选的是谁,否则那一格会显示成空的,
                      而空的看起来像"没指定"—— 两者的线上行为完全相反。
                      选中它保存会被后端 400 挡下,那是对的。 */}
                  {ch.enabled
                    ? ch.name
                    : t('qy_ai_scope_channel_option_disabled', {
                        name: ch.name,
                      })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {(channelState === 'disabled' || channelState === 'missing') && (
            <p className='text-warning text-xs'>
              {t(`qy_ai_scope_channel_${channelState}` as never)}
            </p>
          )}
          {/* 故障转移。**只在指定了渠道时出现** —— 没指定时本来就走加权随机池,
              那时摆一个"失败后退到随机池"的开关是在问一个没有含义的问题。

              默认关,而且这里刻意用一整段说清打开之后会发生什么:它把
              「只发给这一个」变成「这一个不行就发给池子里的任何一个」,
              也就是把用户内容的出境目的地从一个变成一组。指定渠道这一格的
              原始理由往往正是数据流向约束,所以这一步必须是运营自己按下的。 */}
          {draft.channel_id > 0 && (
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

      {/* 这一档自己的审核提示词。
          刻意**不预填**：空着本身就是默认且有意义的取值（继承全局那一份）。
          预填的后果是每建一档就顺手固化一份副本，从此与全局脱钩 —— 运营改了
          全局提示词，这些档一个都不会跟着变，而界面上它们只是"填过内容"。 */}
      <div className='flex flex-col gap-2'>
        <div className='flex items-center gap-2'>
          <Label className='text-sm'>{t('qy_ai_scope_f_prompt')}</Label>
          <Badge
            variant={promptSource === 'custom' ? 'secondary' : 'outline'}
            className='font-normal'
          >
            {t(`qy_ai_scope_prompt_${promptSource}` as never)}
          </Badge>
          {promptSource === 'custom' && (
            <Button
              size='sm'
              variant='outline'
              onClick={() => onChange({ ...draft, prompt: '' })}
            >
              {t('qy_ai_scope_prompt_reset')}
            </Button>
          )}
        </div>
        <Textarea
          rows={6}
          value={draft.prompt}
          placeholder={t('qy_ai_scope_prompt_placeholder')}
          onChange={(e) => onChange({ ...draft, prompt: e.target.value })}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_ai_scope_f_prompt_hint', {
            placeholder: QY_AI_CATEGORY_PLACEHOLDER,
          })}
        </p>
        {/* 留空时把继承来的那一段摆出来：不摆的话，"什么都不填"到底意味着
            什么完全不可见 —— 而它可能是内置默认，也可能是本站改过的全局那一份。 */}
        {promptSource === 'inherit' && effectivePrompt !== '' && (
          <details className='text-muted-foreground text-xs'>
            <summary className='cursor-pointer'>
              {t('qy_ai_scope_prompt_inherited_show')}
            </summary>
            <pre className='mt-1 break-words whitespace-pre-wrap'>
              {effectivePrompt}
            </pre>
          </details>
        )}
      </div>

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
                    testing={probe.isPending && probe.variables === ch.id}
                    onEdit={() =>
                      setEditing({ id: ch.id, draft: qyAiChannelToDraft(ch) })
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
                onClick={() => setEditing({ draft: qyAiChannelToDraft() })}
              >
                <Plus className='size-4' />
                {t('qy_ai_channel_add')}
              </Button>
            </div>

            {editing && (
              <ChannelForm
                draft={editing.draft}
                guardCatalog={data.guard_catalog ?? []}
                elevateDefault={data.guard_elevate_default ?? []}
                existingHint={
                  editing.id
                    ? data.items?.find((c) => c.id === editing.id)?.key_hint
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
  testing,
  onEdit,
  onTest,
  onDelete,
}: {
  channel: QyAiChannel
  testing: boolean
  onEdit: () => void
  onTest: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const guard = channel.protocol === 'qwen3guard'
  return (
    <div className='flex flex-wrap items-center gap-2 rounded-md border p-3'>
      <span className='font-medium'>{channel.name}</span>
      <Badge variant={channel.enabled ? 'default' : 'outline'}>
        {channel.enabled ? t('qy_ai_on') : t('qy_ai_off')}
      </Badge>
      {/* 协议摆在列表上,不能只藏在编辑弹窗里:两种渠道的请求体、成本、
          类型体系完全不同,而它们在列表上原本长得一模一样。 */}
      <Badge variant='outline'>
        {guard ? t('qy_ai_proto_guard_short') : t('qy_ai_proto_json_short')}
      </Badge>
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
      <div className='flex flex-col gap-1 rounded-md border p-2'>
        <span className='text-xs font-medium'>
          {t('qy_ai_proto_guard_map_title')}
        </span>
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
        {missing.length > 0 && (
          <p className='text-muted-foreground text-xs'>
            {t('qy_ai_proto_guard_map_missing', {
              keys: missing.map((c) => c.key).join(', '),
            })}
          </p>
        )}
      </div>
    </div>
  )
}

function ChannelForm({
  draft,
  guardCatalog,
  elevateDefault,
  existingHint,
  onChange,
  onCancel,
  onSave,
  saving,
}: {
  draft: QyAiChannelDraft
  guardCatalog: QyAiGuardCategory[]
  elevateDefault: string[]
  existingHint?: string
  onChange: (d: QyAiChannelDraft) => void
  onCancel: () => void
  onSave: () => void
  saving: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className='flex flex-col gap-3 rounded-md border p-3'>
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
          </SelectContent>
        </Select>
      </Field>
      <ProtocolExplainer
        protocol={draft.protocol}
        guardCatalog={guardCatalog}
      />
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
        <Field
          label={t('qy_ai_f_base_url')}
          hint={
            draft.protocol === 'qwen3guard'
              ? t('qy_ai_f_base_url_hint_guard')
              : t('qy_ai_f_base_url_hint')
          }
        >
          <Input
            value={draft.base_url}
            onChange={(e) => onChange({ ...draft, base_url: e.target.value })}
          />
        </Field>
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
              : draft.protocol === 'qwen3guard'
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
            draft.protocol === 'qwen3guard'
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
      {draft.protocol === 'qwen3guard' && (
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
