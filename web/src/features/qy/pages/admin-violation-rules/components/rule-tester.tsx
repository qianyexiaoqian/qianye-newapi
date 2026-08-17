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
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import type { UseFormGetValues } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import { qyOpsErrorMessage } from '../../ops/errors'
import { formatQyMicros } from '../../ops/format'
import { testQyViolationRule } from '../api'
import {
  QY_VIOLATION_MATCH_TYPES,
  QY_VIOLATION_PHASES,
  qyViolationRuleToPayload,
  type QyViolationRuleFormValues,
} from '../lib/rule-form'
import {
  qyRuleTestAbsentDimensions,
  qyRuleTestContentInputs,
  qyRuleTestInputs,
} from '../lib/rule-test'
import type {
  QyViolationMatchType,
  QyViolationPhase,
  QyViolationRuleTestResult,
  QyViolationTestInput,
} from '../types'

/**
 * 规则试跑面板。
 *
 * 用 `getValues()` 而不是订阅表单：试跑是「点一下才发生」的动作，
 * 订阅会让每敲一个字符都重渲染整块面板。
 *
 * # 它问什么，由规则说了算
 *
 * 这块面板曾经不论规则是什么，都固定问「样本文本 + 模型 + 分组」。那三格对
 * 大多数规则都是错的问题：一条错误码规则要的是错误码，一条上游规则要的是
 * 上游正文与状态码，一条频率规则一段文本都不看。而模型和分组根本不参与
 * 内容匹配，它们只决定「这条规则在不在作用域内」—— 把它们摆在最显眼的位置，
 * 等于把试跑引向一个它回答不了的问题。
 *
 * 现在字段由 `qyRuleTestInputs(phase, match_type)` 决定，跑完之后改用后端回的
 * `inputs`（后端说了算，两边不一致时以线上判据为准）。
 *
 * # 但「字段跟着规则走」还不够 —— 它必须自己说清楚
 *
 * 上一版把规则读不到的维度**静默省略**掉了。库里 34 条规则有 30 条落在 prompt
 * 阶段，随手点开一条，面板上就只剩「请求上下文」孤零零一格。项目方看到的正是
 * 这一屏，得出的结论是「上游返回文本、返回代码、状态码这几样根本没做」——
 * 而它们一直都在，只是这条规则不读。
 *
 * 所以这块面板现在自带三样东西，缺一样都会退回那个误解：
 *
 *  1. **判据摆在原地**：这条规则此刻的生效阶段与匹配方式直接写在试跑区里，
 *     不必回到表单上方去找；
 *  2. **缺席的维度可见**：读不到的那几样列出来，并逐条写明**为什么**读不到
 *     （「阶段是转发前，那时上游还没返回」）。它们是**只读说明**，绝不是能填
 *     的格子 —— 填了不生效比不显示更糟，那正是上一轮修掉的毛病；
 *  3. **改判据的路径就在手边**：那两个选择器在这里也有一份，改完这一格、
 *     那些说明当场跟着变。
 */
export function QyRuleTester(props: {
  getValues: UseFormGetValues<QyViolationRuleFormValues>
  phase: QyViolationPhase
  matchType: QyViolationMatchType
  /** 就地改判据。写回的是表单里同一个字段，所以两处选择器永远一致。 */
  onPhaseChange: (phase: QyViolationPhase) => void
  onMatchTypeChange: (matchType: QyViolationMatchType) => void
}) {
  const { t } = useTranslation()
  const [requestText, setRequestText] = useState('')
  const [upstreamText, setUpstreamText] = useState('')
  const [rejectReason, setRejectReason] = useState('')
  const [statusCode, setStatusCode] = useState('')
  const [errorCode, setErrorCode] = useState('')
  const [rateCount, setRateCount] = useState('')
  const [aiVerdict, setAiVerdict] = useState('')
  const [aiCategory, setAiCategory] = useState('')
  const [aiConfidence, setAiConfidence] = useState('')
  const [model, setModel] = useState('')
  const [group, setGroup] = useState('')
  const [result, setResult] = useState<QyViolationRuleTestResult | null>(null)

  const inputs = qyRuleTestInputs(props.phase, props.matchType)
  const asks = (id: QyViolationTestInput) => inputs.includes(id)
  const absent = qyRuleTestAbsentDimensions(props.phase, props.matchType)
  // 两个判据的人话名字。缺席说明要点名的就是它们 —— 「这条规则的阶段是
  // 『转发前』」远比「prompt 阶段不读上游」更容易接上管理员手里那个下拉框。
  const criteria = {
    match: t(`qy_vio_match_${props.matchType}`),
    phase: t(`qy_vio_phase_${props.phase}`),
  }
  // Base UI 的 `<Select.Value />` 只会从 Root 的 `items` 里查译名；不传 items
  // 时它渲染的是**原始取值**（关着的下拉上写着 `prompt`、`keyword`）。这块面板
  // 的立身之本就是「判据一眼能读懂」，所以这两个下拉必须把对照表交出去。
  const phaseLabels = Object.fromEntries(
    QY_VIOLATION_PHASES.map((value) => [value, t(`qy_vio_phase_${value}`)])
  )
  const matchLabels = Object.fromEntries(
    QY_VIOLATION_MATCH_TYPES.map((value) => [value, t(`qy_vio_match_${value}`)])
  )

  const testMutation = useMutation({
    mutationFn: () =>
      testQyViolationRule({
        rule: qyViolationRuleToPayload(props.getValues()),
        // 只发这条规则会读的字段，其余一律发空：一个上游样本被同时灌进
        // 请求上下文，会让 prompt 规则凭空「命中」—— 一个捏造出来的绿灯。
        request_text: asks('request_text') ? requestText : '',
        upstream_text: asks('upstream_text') ? upstreamText : '',
        reject_reason: asks('reject_reason') ? rejectReason : '',
        status_code: asks('status_code') ? Number(statusCode.trim()) || 0 : 0,
        error_code: asks('error_code') ? errorCode.trim() : '',
        rate_count: asks('rate_count') ? Number(rateCount.trim()) || 0 : 0,
        ai_verdict: asks('ai_verdict') ? aiVerdict : '',
        ai_category: asks('ai_category') ? aiCategory.trim() : '',
        ai_confidence: asks('ai_confidence') ? aiConfidence.trim() : '',
        model,
        group,
      }),
    onSuccess: setResult,
    onError: (error) => {
      setResult(null)
      toast.error(qyOpsErrorMessage(error, t))
    },
  })

  // 「能不能跑」只看内容输入：模型与分组留空的语义是「不限」，
  // 拿它们当必填会把一次本来跑得动的试跑锁死。
  const filled: Record<QyViolationTestInput, string> = {
    request_text: requestText,
    upstream_text: upstreamText,
    reject_reason: rejectReason,
    status_code: statusCode,
    error_code: errorCode,
    rate_count: rateCount,
    ai_verdict: aiVerdict,
    ai_category: aiCategory,
    ai_confidence: aiConfidence,
    model,
    group,
  }
  const canRun = qyRuleTestContentInputs(inputs).some(
    (id) => filled[id].trim() !== ''
  )

  return (
    <div className='space-y-3 rounded-lg border p-3'>
      <div>
        <h3 className='text-sm font-medium'>{t('qy_vio_test_title')}</h3>
        <p className='text-muted-foreground text-xs'>
          {props.matchType === 'request_rate'
            ? t('qy_vio_test_rate_desc')
            : t('qy_vio_test_desc')}
        </p>
      </div>

      {/* 判据条。
          面板问哪几格，全部由这两项决定 —— 所以它们必须**摆在这里**，而不是
          让人凭记忆回到表单上方去核对。这两个选择器写回的是表单里同一个字段，
          上下两处永远一致；改完这里，下面的格子与说明当场跟着变。 */}
      <div
        className='bg-muted/40 space-y-2 rounded-md border p-2'
        data-qy-test-criteria=''
      >
        <p className='text-xs'>{t('qy_vio_test_criteria_now', criteria)}</p>
        <div className='flex flex-wrap gap-2'>
          <label className='space-y-1 text-xs'>
            <span className='text-muted-foreground'>
              {t('qy_vio_field_phase')}
            </span>
            <Select
              items={phaseLabels}
              value={props.phase}
              onValueChange={(value) =>
                props.onPhaseChange(value as QyViolationPhase)
              }
            >
              <SelectTrigger className='w-48'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {QY_VIOLATION_PHASES.map((phase) => (
                  <SelectItem key={phase} value={phase}>
                    {t(`qy_vio_phase_${phase}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
          <label className='space-y-1 text-xs'>
            <span className='text-muted-foreground'>
              {t('qy_vio_field_match_type')}
            </span>
            <Select
              items={matchLabels}
              value={props.matchType}
              onValueChange={(value) =>
                props.onMatchTypeChange(value as QyViolationMatchType)
              }
            >
              <SelectTrigger className='w-48'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {QY_VIOLATION_MATCH_TYPES.map((matchType) => (
                  <SelectItem key={matchType} value={matchType}>
                    {t(`qy_vio_match_${matchType}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </label>
        </div>
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_test_criteria_desc')}
        </p>
      </div>

      {/* AI 审核那一档单独解释一句：它问的是**模型给出的结论**，不是上下文。
          少了这句，看到三格「结论 / 类型 / 置信度」的人会以为面板忘了问文本。 */}
      {props.matchType === 'ai_review' && (
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_test_ai_desc')}
        </p>
      )}

      {asks('request_text') && (
        <QyTestField label={t('qy_vio_test_input_request_text')}>
          <Textarea
            rows={3}
            value={requestText}
            onChange={(event) => setRequestText(event.target.value)}
            placeholder={t('qy_vio_test_input_request_text_ph')}
          />
        </QyTestField>
      )}
      {asks('upstream_text') && (
        <QyTestField label={t('qy_vio_test_input_upstream_text')}>
          <Textarea
            rows={3}
            value={upstreamText}
            onChange={(event) => setUpstreamText(event.target.value)}
            placeholder={t('qy_vio_test_input_upstream_text_ph')}
          />
        </QyTestField>
      )}
      {asks('reject_reason') && (
        <QyTestField
          label={t('qy_vio_test_input_reject_reason')}
          description={t('qy_vio_test_input_reject_reason_desc')}
        >
          <Input
            value={rejectReason}
            onChange={(event) => setRejectReason(event.target.value)}
            placeholder={t('qy_vio_test_input_reject_reason_ph')}
          />
        </QyTestField>
      )}
      {asks('ai_verdict') && (
        <QyTestField
          label={t('qy_vio_test_input_ai_verdict')}
          description={t('qy_vio_test_input_ai_verdict_desc')}
        >
          <Select
            value={aiVerdict}
            onValueChange={(value) => setAiVerdict(value ?? '')}
          >
            <SelectTrigger>
              <SelectValue placeholder={t('qy_vio_test_ai_verdict_none')} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='violation'>
                {t('qy_vio_test_ai_verdict_violation')}
              </SelectItem>
              <SelectItem value='clean'>
                {t('qy_vio_test_ai_verdict_clean')}
              </SelectItem>
            </SelectContent>
          </Select>
        </QyTestField>
      )}
      {/* 类型与置信度带**标签**，不是只有 placeholder：这两格一旦被填过，
          placeholder 就消失了，而 AI 审核那一档恰恰是最需要看懂三格分别是
          什么的一档 —— 「类型白名单」与「置信度下限」是两道完全不同的闸。 */}
      <div className='flex flex-wrap items-end gap-2'>
        {asks('ai_category') && (
          <QyTestField label={t('qy_vio_test_input_ai_category')}>
            <Input
              className='w-40'
              value={aiCategory}
              onChange={(event) => setAiCategory(event.target.value)}
              placeholder={t('qy_vio_test_input_ai_category')}
            />
          </QyTestField>
        )}
        {asks('ai_confidence') && (
          <QyTestField label={t('qy_vio_test_input_ai_confidence')}>
            <Input
              className='w-40'
              inputMode='decimal'
              value={aiConfidence}
              onChange={(event) => setAiConfidence(event.target.value)}
              placeholder={t('qy_vio_test_input_ai_confidence')}
            />
          </QyTestField>
        )}
        {asks('status_code') && (
          <QyTestField label={t('qy_vio_test_input_status_code')}>
            <Input
              className='w-40'
              inputMode='numeric'
              value={statusCode}
              onChange={(event) => setStatusCode(event.target.value)}
              placeholder={t('qy_vio_test_input_status_code')}
            />
          </QyTestField>
        )}
        {asks('error_code') && (
          <QyTestField label={t('qy_vio_test_input_error_code')}>
            <Input
              className='w-56'
              value={errorCode}
              onChange={(event) => setErrorCode(event.target.value)}
              placeholder={t('qy_vio_test_input_error_code')}
            />
          </QyTestField>
        )}
        {asks('rate_count') && (
          <QyTestField label={t('qy_vio_test_input_rate_count')}>
            <Input
              className='w-40'
              inputMode='numeric'
              value={rateCount}
              onChange={(event) => setRateCount(event.target.value)}
              placeholder={t('qy_vio_test_input_rate_count')}
            />
          </QyTestField>
        )}
      </div>

      {absent.length > 0 && (
        <QyAbsentDimensions absent={absent} criteria={criteria} />
      )}

      <div className='space-y-2 border-t pt-2'>
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_test_scope_desc')}
        </p>
        <div className='flex flex-wrap gap-2'>
          <Input
            className='w-40'
            value={model}
            onChange={(event) => setModel(event.target.value)}
            placeholder={t('qy_vio_test_model_optional')}
          />
          <Input
            className='w-40'
            value={group}
            onChange={(event) => setGroup(event.target.value)}
            placeholder={t('qy_vio_test_group_optional')}
          />
          <Button
            type='button'
            variant='secondary'
            disabled={testMutation.isPending || !canRun}
            onClick={() => testMutation.mutate()}
          >
            {t('qy_vio_test_run')}
          </Button>
        </div>
      </div>

      {result != null && <QyRuleTestResult result={result} />}
    </div>
  )
}

/**
 * 这条规则**读不到**的维度。
 *
 * 整块面板里唯一一处**只读**区域，而且必须一直是只读的：把这几样做成可填的
 * 输入，管理员会填、会看到「未命中」、然后去改一个本来就对的模式串 ——
 * 填了不生效比不显示更糟。所以这里一个 `Input`/`Textarea`/`Select` 都没有，
 * 只有名字和理由。
 *
 * 折叠但**摘要里就把名字念出来**：收起时看得见这些维度存在（那正是这次要修的
 * 误解），展开才是逐条的理由，不至于把六七行说明糊在主流程上。
 */
function QyAbsentDimensions(props: {
  absent: { id: QyViolationTestInput; reason: string }[]
  criteria: { match: string; phase: string }
}) {
  const { t } = useTranslation()
  return (
    <details className='rounded-md border border-dashed p-2 text-xs'>
      <summary className='text-muted-foreground cursor-pointer'>
        {t('qy_vio_test_absent_summary', {
          fields: props.absent
            .map((row) => t(`qy_vio_test_input_${row.id}`))
            .join('、'),
        })}
      </summary>
      <p className='text-muted-foreground mt-2'>
        {t('qy_vio_test_absent_readonly')}
      </p>
      <ul className='mt-1 space-y-1'>
        {props.absent.map((row) => (
          <li key={row.id} className='text-muted-foreground'>
            <span className='text-foreground/70 font-medium'>
              {t(`qy_vio_test_input_${row.id}`)}
            </span>
            {' — '}
            {t(`qy_vio_test_absent_${row.reason}`, props.criteria)}
          </li>
        ))}
      </ul>
    </details>
  )
}

/** 试跑输入的一格。标签 + 可选说明 + 控件，五个格子共用同一份排版。 */
function QyTestField(props: {
  label: string
  description?: string
  children: React.ReactNode
}) {
  return (
    <div className='space-y-1'>
      <p className='text-xs font-medium'>{props.label}</p>
      {props.description != null && (
        <p className='text-muted-foreground text-xs'>{props.description}</p>
      )}
      {props.children}
    </div>
  )
}

/**
 * 试跑结论。**三态必须彼此可分**，这是整块面板的关键。
 *
 * 只回一个「命中 / 未命中」时，「规则压根不作用于你填的模型/分组/状态码」和
 * 「规则确实作用于它、但模式串没匹配上」长得一模一样 —— 管理员分不清该改
 * 作用域还是该改模式串，只能两边乱改。所以这里先说落在哪一态，
 * 不在作用域时还要点名是哪一道闸，未命中时还要点名哪几格没填。
 */
function QyRuleTestResult(props: { result: QyViolationRuleTestResult }) {
  const { t } = useTranslation()
  const { result } = props
  // 三态各自的颜色。命中是绿的，没命中是灰的，其余（不在作用域 / 扫描超时）
  // 是黄的 —— 后两者都表示「这次试跑没能回答你的问题」。
  const tones: Record<string, string> = {
    matched: 'text-success',
    no_match: 'text-muted-foreground',
  }
  const tone = tones[result.outcome] ?? 'text-warning'

  return (
    <div className='bg-muted/40 space-y-1 rounded-md p-2 text-xs'>
      <p className={tone}>{t(`qy_vio_test_outcome_${result.outcome}`)}</p>
      {result.outcome === 'out_of_scope' && result.scope_fail !== '' && (
        <p className='text-warning'>
          {t(`qy_vio_test_scope_fail_${result.scope_fail}`)}
        </p>
      )}
      {result.outcome === 'no_match' && result.blank_inputs.length > 0 && (
        <p className='text-warning'>
          {t('qy_vio_test_blank_inputs', {
            fields: result.blank_inputs
              .map((id) => t(`qy_vio_test_input_${id}`))
              .join('、'),
          })}
        </p>
      )}
      {result.terms.length > 0 && (
        <p className='break-all'>
          {t('qy_vio_test_terms', { terms: result.terms.join(', ') })}
        </p>
      )}
      {result.snippet !== '' && (
        <p className='break-all'>
          {t('qy_vio_test_snippet', { snippet: result.snippet })}
        </p>
      )}
      {result.elapsed_us != null && (
        <p>
          {t('qy_vio_test_elapsed', {
            elapsed: formatQyMicros(result.elapsed_us),
          })}
        </p>
      )}
    </div>
  )
}
