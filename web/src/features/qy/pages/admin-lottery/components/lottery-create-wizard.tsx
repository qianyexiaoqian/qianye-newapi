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
import { useNavigate } from '@tanstack/react-router'
import { Plus, TriangleAlert, X } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
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

import { QyAmountInput } from '../../../components/qy-amount-input'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { qyKeys } from '../../../lib/query-keys'
import {
  isQyLotBallPoolValid,
  qyLotBallTierOdds,
  type QyLotBallPool,
} from '../../lottery/lib/ball'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { createQyLotActivity, qyAdminLotSeriesQuery } from '../api'
import { qyLotFromLocalInput, qyLotToLocalInput } from '../lib/datetime'
import {
  qyLotBreakEvenEntries,
  qyLotDraftForPlay,
  qyLotDraftToInput,
  qyLotEffectiveAllowMultiWin,
  qyLotEmptyDraft,
  qyLotNewOption,
  qyLotPlayOf,
  qyLotTotalPrizeQuota,
  qyLotTotalWinPpm,
  qyLotValidateDraft,
  type QyLotDraft,
  type QyLotPlay,
} from '../lib/draft'
import type { QyLotAdminConfig, QyLotSeries } from '../types'
import { QyLotCoverField } from './lottery-cover-field'
import { QyLotRulesEditor } from './lottery-rules-editor'

const STEPS = ['basic', 'spec', 'rules', 'review'] as const
type QyLotStep = (typeof STEPS)[number]

/**
 * 创建活动向导。
 *
 * ## 为什么是四步而不是一张长表
 *
 * 要填的字段有四十来个，一张长表的实际后果是运营滚到底、随手点保存，然后在
 * publish 那一刻才发现奖品总额多了一个零 —— 而 publish 是**不可逆**的。
 * 分步的价值全在最后一步：它把「即将被永久冻结的东西」与「本场最坏情况净增发
 * 多少额度」并排摆出来，再要一次刻意的确认。
 *
 * ## 创建 ≠ 发布
 *
 * 这个向导只产出 `draft`。草稿期一切可改、种子已生成但没有承诺。真正不可逆的
 * 是详情页上的「发布」——两件事分开是刻意的：合成一步的话，"我先建个草稿看看"
 * 这个再正常不过的动作就会变成一次无法撤销的承诺。
 */
export function QyLotCreateWizard(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  config: QyLotAdminConfig | undefined
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const defaultFee = props.config?.effective.default_guess_fee_bps ?? 500
  const [draft, setDraft] = useState<QyLotDraft>(() =>
    qyLotEmptyDraft(defaultFee)
  )
  const [step, setStep] = useState<QyLotStep>('basic')
  const [confirmOpen, setConfirmOpen] = useState(false)

  useEffect(() => {
    if (!props.open) return
    setDraft(qyLotEmptyDraft(defaultFee))
    setStep('basic')
    setConfirmOpen(false)
  }, [props.open, defaultFee])

  const patch = (next: Partial<QyLotDraft>) => {
    setDraft((prev) => ({ ...prev, ...next }))
  }

  // 期次系列只在真的要开双色球时才拉：它是一张管理端冷路径的表，普通抽奖的
  // 创建流程不该为它多打一次请求。
  const isBall = qyLotPlayOf(draft) === 'ball'
  const seriesQuery = useQuery({
    ...qyAdminLotSeriesQuery({ p: 1, page_size: 100 }),
    enabled: props.open && isBall,
  })
  const seriesList = qyArray(seriesQuery.data?.items)
  const series = seriesList.find((item) => item.series_no === draft.series_no)

  const errors = qyLotValidateDraft(
    draft,
    props.config?.yaml_readonly,
    props.config?.effective.max_total_prize_quota ?? 0,
    props.config?.effective.max_guess_fee_bps ?? 0,
    series
  )

  const mutation = useMutation({
    mutationFn: () => createQyLotActivity(qyLotDraftToInput(draft)),
    onSuccess: async (data) => {
      toast.success(t('qy_lot_created'))
      setConfirmOpen(false)
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      // 直接落到详情页：下一步（发布）在那里，而且那一步才是不可逆的。
      await navigate({
        to: '/qy/admin/lottery/$actNo',
        params: { actNo: data.act_no },
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const index = STEPS.indexOf(step)
  const totalPrize = qyLotTotalPrizeQuota(draft)
  const breakEven = qyLotBreakEvenEntries(draft)
  const alertQuota = props.config?.effective.large_prize_alert_quota ?? 0

  return (
    <>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={t('qy_lot_create_title')}
        description={t(`qy_lot_step_${step}_desc`)}
        contentClassName='sm:max-w-3xl'
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              disabled={index === 0}
              onClick={() => setStep(STEPS[Math.max(0, index - 1)])}
            >
              {t('qy_lot_step_prev')}
            </Button>
            {step === 'review' ? (
              <Button
                type='button'
                disabled={errors.length > 0 || mutation.isPending}
                onClick={() => setConfirmOpen(true)}
              >
                {t('qy_lot_create_submit')}
              </Button>
            ) : (
              <Button
                type='button'
                onClick={() =>
                  setStep(STEPS[Math.min(STEPS.length - 1, index + 1)])
                }
              >
                {t('qy_lot_step_next')}
              </Button>
            )}
          </>
        }
      >
        <div className='space-y-4'>
          <ol className='text-muted-foreground flex flex-wrap gap-2 text-xs'>
            {STEPS.map((item, position) => (
              <li
                key={item}
                className={
                  position === index ? 'text-foreground font-medium' : ''
                }
              >
                {position + 1}. {t(`qy_lot_step_${item}`)}
              </li>
            ))}
          </ol>

          {step === 'basic' && (
            <BasicStep
              draft={draft}
              config={props.config}
              onChange={patch}
              // 玩法切换不能走 `patch`：它要同时归位 `kind`、`draw_mode` 与
              // `series_no` 三个字段，而归位规则（哪个该清、哪个该留）在
              // `qyLotDraftForPlay` 里有一份带理由的实现，且有测试盯着。
              onSelectPlay={(next) =>
                setDraft((prev) => qyLotDraftForPlay(prev, next))
              }
              seriesList={seriesList}
              seriesLoading={seriesQuery.isLoading}
            />
          )}
          {step === 'spec' && (
            <SpecStep draft={draft} onChange={patch} series={series} />
          )}
          {step === 'rules' && (
            <QyLotRulesEditor draft={draft} onChange={patch} />
          )}
          {step === 'review' && (
            <ReviewStep
              draft={draft}
              errors={errors}
              totalPrize={totalPrize}
              breakEven={breakEven}
              alertQuota={alertQuota}
              series={series}
            />
          )}
        </div>
      </QyResponsiveDialog>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_lot_create_confirm_title')}
        description={t('qy_lot_create_confirm_desc')}
        isLoading={mutation.isPending}
        details={
          <div>
            <QyKeyValue label={t('qy_lot_play')}>
              {isBall ? t('qy_lot_mode_ball') : t(`qy_lot_kind_${draft.kind}`)}
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_stake')}>
              <QyAmountText quota={draft.stake_quota} />
            </QyKeyValue>
            {/* 双色球不摆"奖品总额 / 保本人数"：浮动奖档的额度恒为 0，那两个
                数会把一个由期次池兜底的玩法说成一个只发几百额度的小活动。 */}
            {draft.kind === 'draw' && !isBall && (
              <>
                <QyKeyValue label={t('qy_lot_total_prize')}>
                  <QyAmountText quota={totalPrize} />
                </QyKeyValue>
                <QyKeyValue label={t('qy_lot_break_even')}>
                  {breakEven}
                </QyKeyValue>
              </>
            )}
            {isBall && series != null && (
              <QyKeyValue label={t('qy_lot_ball_series_pool')}>
                <QyAmountText quota={series.pool_quota} />
              </QyKeyValue>
            )}
          </div>
        }
        onConfirm={() => mutation.mutate()}
      />
    </>
  )
}

// ───────────────────────────── 第一步 ─────────────────────────────

function BasicStep(props: {
  draft: QyLotDraft
  config: QyLotAdminConfig | undefined
  onChange: (patch: Partial<QyLotDraft>) => void
  onSelectPlay: (play: QyLotPlay) => void
  seriesList: QyLotSeries[]
  seriesLoading: boolean
}) {
  const { draft } = props
  const { t } = useTranslation()
  const id = useId()
  const play = qyLotPlayOf(draft)
  const isBall = play === 'ball'
  const openSeries = props.seriesList.filter((item) => item.status === 'open')

  return (
    <div className='space-y-3'>
      {/*
        ── 三个玩法并列，双色球不再埋在二级下拉里（需求 6）──

        项目方原话：「抽奖竞猜页面，没有发现"双色球"活动 UI 界面和配置活动
        界面。」它其实一直都在，只是要先选「抽奖」、再在「摇号方式」里选
        「双色球」—— 两层之下的东西等于不存在。

        做法是**只改呈现**：三张卡对应 `qyLotDraftForPlay` 的三个投影，落库仍是
        (kind, draw_mode) 两个字段，后端一个字节不改（`kind` 是生命周期任务的
        扫表维度，新增一个 kind 要在四处补分支，漏一处就是一条静默死路）。

        用卡片而不是下拉：三个选项各自的资金语义完全不同（抽奖是平台净增发、
        竞猜是奖池再分配、双色球是期次滚存），一行副标就能把差别说清楚，
        而下拉在收起状态下只剩一个词。
      */}
      <div className='space-y-1'>
        <Label>{t('qy_lot_play')}</Label>
        <div className='grid gap-2 sm:grid-cols-3'>
          <PlayOption
            active={play === 'draw'}
            title={t('qy_lot_kind_draw')}
            desc={t('qy_lot_kind_draw_hint')}
            onSelect={() => props.onSelectPlay('draw')}
          />
          <PlayOption
            active={play === 'guess'}
            title={t('qy_lot_kind_guess')}
            desc={t('qy_lot_kind_guess_hint')}
            onSelect={() => props.onSelectPlay('guess')}
          />
          <PlayOption
            active={isBall}
            title={t('qy_lot_mode_ball')}
            desc={t('qy_lot_mode_ball_hint')}
            onSelect={() => props.onSelectPlay('ball')}
          />
        </div>
      </div>

      {/*
        定档方式只剩两个选项，且只在「抽奖」下出现。

        `ball` 从这里拿走了 —— 它现在是一级玩法。留在这儿会出现两个入口指向
        同一个状态，而其中一个（这里）不会把 `series_no` 一起归位。
      */}
      {play === 'draw' && (
        <div className='space-y-1'>
          <Label htmlFor={`${id}-mode`}>{t('qy_lot_draw_mode')}</Label>
          <Select
            value={draft.draw_mode === 'prob' ? 'prob' : 'rank'}
            onValueChange={(value) =>
              props.onChange({
                draw_mode: value === 'prob' ? 'prob' : 'rank',
              })
            }
          >
            <SelectTrigger id={`${id}-mode`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='rank'>{t('qy_lot_mode_rank')}</SelectItem>
              <SelectItem value='prob'>{t('qy_lot_mode_prob')}</SelectItem>
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {draft.draw_mode === 'prob'
              ? t('qy_lot_mode_prob_hint')
              : t('qy_lot_mode_rank_hint')}
          </p>
        </div>
      )}

      {isBall && (
        <div className='space-y-1'>
          <Label htmlFor={`${id}-series`}>{t('qy_lot_ball_series')}</Label>
          <Select
            value={draft.series_no}
            onValueChange={(value) =>
              props.onChange({ series_no: value ?? '' })
            }
          >
            <SelectTrigger id={`${id}-series`}>
              <SelectValue placeholder={t('qy_lot_ball_series_placeholder')} />
            </SelectTrigger>
            <SelectContent>
              {openSeries.map((item) => (
                <SelectItem key={item.series_no} value={item.series_no}>
                  {t('qy_lot_ball_series_option', {
                    title: item.title,
                    redPick: item.red_pick,
                    redPool: item.red_pool,
                    bluePick: item.blue_pick,
                    bluePool: item.blue_pool,
                    issue: item.issue_seq + 1,
                  })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {/* 号池、投注入池比例、累计发行上限**全部由系列决定**，创建活动时
              没有任何字段能覆盖它们。号池若能逐期指定，"各档概率是组合数算
              出来的"这条主张就没了 —— 管理员可以每期换一个号码空间。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_ball_series_hint')}
          </p>
          {!props.seriesLoading && openSeries.length === 0 && (
            <Alert>
              <TriangleAlert />
              <AlertTitle>{t('qy_lot_ball_no_series_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_lot_ball_no_series_desc')}
              </AlertDescription>
            </Alert>
          )}
        </div>
      )}

      <div className='space-y-1'>
        <Label htmlFor={`${id}-title`}>{t('qy_lot_title_field')}</Label>
        <Input
          id={`${id}-title`}
          value={draft.title}
          maxLength={120}
          onChange={(event) => props.onChange({ title: event.target.value })}
        />
      </div>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-intro`}>{t('qy_lot_intro_field')}</Label>
        <Textarea
          id={`${id}-intro`}
          rows={3}
          value={draft.intro}
          onChange={(event) => props.onChange({ intro: event.target.value })}
        />
        {/* 简介按纯文本渲染，不解析富文本 —— 说明白，免得运营贴一段 HTML
            然后奇怪为什么没生效。 */}
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_intro_plain_hint')}
        </p>
      </div>

      {/* 背景图紧跟在简介之后：它与标题、简介同属"这张卡长什么样"，
          而后面几格全是钱与时刻。分开摆会让运营在四十来个字段里找不到它 ——
          「找不到」正是双色球入口那条反馈的形状。 */}
      <QyLotCoverField
        value={{ cover_ref: draft.cover_ref, cover_url: draft.cover_url }}
        config={props.config}
        onChange={(next) => props.onChange(next)}
      />

      <div className='space-y-1'>
        <Label htmlFor={`${id}-stake`}>{t('qy_lot_stake')}</Label>
        <QyAmountInput
          id={`${id}-stake`}
          value={draft.stake_quota}
          onChange={(quota) => props.onChange({ stake_quota: quota })}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_stake_hint')}
        </p>
      </div>

      <div className='grid gap-3 sm:grid-cols-2'>
        <TimeField
          label={t('qy_lot_open_at')}
          value={draft.open_at}
          onChange={(value) => props.onChange({ open_at: value })}
        />
        <TimeField
          label={t('qy_lot_close_at')}
          value={draft.close_at}
          onChange={(value) => props.onChange({ close_at: value })}
        />
        <TimeField
          label={t('qy_lot_draw_at')}
          hint={t('qy_lot_draw_at_hint')}
          value={draft.draw_at}
          onChange={(value) => props.onChange({ draw_at: value })}
        />
        <TimeField
          label={t('qy_lot_settle_deadline')}
          hint={t('qy_lot_settle_deadline_hint')}
          value={draft.settle_deadline}
          onChange={(value) => props.onChange({ settle_deadline: value })}
        />
      </div>
    </div>
  )
}

/**
 * 一级玩法的一张卡。
 *
 * 与 `admin-group-matrix` 的模式选项同一个形状（`aria-pressed` + 选中描边），
 * 这个仓库里"从几个互斥选项里挑一个、且每个都要带一行解释"就长这样。
 */
function PlayOption(props: {
  active: boolean
  title: string
  desc: string
  onSelect: () => void
}) {
  return (
    <button
      type='button'
      onClick={props.onSelect}
      aria-pressed={props.active}
      className={
        props.active
          ? 'border-primary bg-primary/5 rounded-lg border p-3 text-start'
          : 'hover:bg-muted/50 rounded-lg border p-3 text-start'
      }
    >
      <span className='block text-sm font-medium'>{props.title}</span>
      <span className='text-muted-foreground mt-1 block text-xs'>
        {props.desc}
      </span>
    </button>
  )
}

function TimeField(props: {
  label: string
  hint?: string
  value: number
  onChange: (value: number) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        type='datetime-local'
        value={qyLotToLocalInput(props.value)}
        onChange={(event) =>
          props.onChange(qyLotFromLocalInput(event.target.value))
        }
      />
      {props.hint != null && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
    </div>
  )
}

// ───────────────────────────── 第二步 ─────────────────────────────

function SpecStep(props: {
  draft: QyLotDraft
  onChange: (patch: Partial<QyLotDraft>) => void
  series: QyLotSeries | undefined
}) {
  const { draft } = props
  const { t } = useTranslation()
  const id = useId()

  if (qyLotPlayOf(draft) === 'ball') {
    return (
      <BallSpecStep
        draft={draft}
        onChange={props.onChange}
        series={props.series}
      />
    )
  }

  if (draft.kind === 'draw') {
    return (
      <div className='space-y-3'>
        {draft.tiers.map((tier, index) => (
          <div key={tier.tier} className='space-y-2 rounded-lg border p-3'>
            <div className='flex items-center justify-between'>
              <span className='text-sm font-medium'>
                {t('qy_lot_tier_no', { no: tier.tier })}
              </span>
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                aria-label={t('qy_common_delete')}
                disabled={draft.tiers.length <= 1}
                onClick={() =>
                  props.onChange({
                    tiers: draft.tiers.filter((_, i) => i !== index),
                  })
                }
              >
                <X aria-hidden='true' />
              </Button>
            </div>
            <div className='grid gap-2 sm:grid-cols-3'>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_name')}</Label>
                <Input
                  value={tier.name}
                  maxLength={80}
                  onChange={(event) =>
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index
                          ? { ...item, name: event.target.value }
                          : item
                      ),
                    })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_amount')}</Label>
                <QyAmountInput
                  value={tier.amount_quota}
                  onChange={(quota) =>
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index ? { ...item, amount_quota: quota } : item
                      ),
                    })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_count')}</Label>
                <Input
                  inputMode='numeric'
                  value={String(tier.count)}
                  onChange={(event) => {
                    const digits = event.target.value.replaceAll(/\D/g, '')
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index
                          ? {
                              ...item,
                              count: digits === '' ? 0 : Number(digits),
                            }
                          : item
                      ),
                    })
                  }}
                />
              </div>
            </div>

            {/*
              概率制的每档中奖概率。

              没有这一格，「概率制」在界面上就是一条死路：向导四步全绿、复核屏
              全绿、点确认必定吃一个 400（后端对 prob 强制 `win_ppm ∈ (0, 1e6]`），
              而界面上没有任何一格可以用来修正它。这与项目方两次反馈的「找不到
              双色球」是同一种缺陷 —— 选得到、填得完、走不通。
            */}
            {draft.draw_mode === 'prob' && (
              <div className='space-y-1'>
                <Label>{t('qy_lot_win_ppm_field')}</Label>
                <Input
                  inputMode='numeric'
                  value={String(tier.win_ppm ?? 0)}
                  onChange={(event) => {
                    const digits = event.target.value.replaceAll(/\D/g, '')
                    props.onChange({
                      tiers: draft.tiers.map((item, i) =>
                        i === index
                          ? {
                              ...item,
                              win_ppm: digits === '' ? 0 : Number(digits),
                            }
                          : item
                      ),
                    })
                  }}
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_lot_win_ppm_hint', {
                    percent: ((tier.win_ppm ?? 0) / 10000).toFixed(4),
                  })}
                </p>
              </div>
            )}
          </div>
        ))}

        {/*
          未中奖区间是一等公民，不是"配漏了"。把它明确摆出来，运营才知道
          Σ概率 < 100% 是允许的，也才看得见自己是不是把它配成了 0。
        */}
        {draft.draw_mode === 'prob' && (
          <div className='rounded-lg border p-3 text-sm'>
            <p>
              {t('qy_lot_win_ppm_sum', {
                percent: (qyLotTotalWinPpm(draft) / 10000).toFixed(4),
              })}
            </p>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t('qy_lot_win_ppm_sum_hint', {
                percent: (
                  Math.max(0, 1_000_000 - qyLotTotalWinPpm(draft)) / 10000
                ).toFixed(4),
              })}
            </p>
          </div>
        )}

        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={() =>
            props.onChange({
              tiers: [
                ...draft.tiers,
                {
                  tier:
                    draft.tiers.reduce(
                      (max, item) => Math.max(max, item.tier),
                      0
                    ) + 1,
                  name: '',
                  amount_quota: 0,
                  count: 1,
                  win_ppm: 0,
                },
              ],
            })
          }
        >
          <Plus aria-hidden='true' />
          {t('qy_lot_add_tier')}
        </Button>

        {/*
          「允许多次中奖」只对名次制是一个真开关。

          概率制下后端无条件把它置为 true（每张票独立摇号，按 user_ref 去重会
          让单票概率变成 1-(1-p)^k，公示的概率就不再为真）。留一个可交互的
          Switch 在这里，运营会关掉它、在复核屏看到「关」、点发布 —— 而落库与
          **承诺原像**里都是 true。复核屏那一格的标题是「即将被永久冻结」，
          在那里显示一个假值比不显示更糟。双色球分支早就是这么处理的。
        */}
        {draft.draw_mode === 'prob' ? (
          <div className='rounded-lg border p-3'>
            <Label>{t('qy_lot_allow_multi_win')}</Label>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t('qy_lot_allow_multi_win_forced')}
            </p>
          </div>
        ) : (
          <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
            <div className='min-w-0'>
              <Label htmlFor={`${id}-multi`}>
                {t('qy_lot_allow_multi_win')}
              </Label>
              <p className='text-muted-foreground text-xs'>
                {t('qy_lot_allow_multi_win_hint')}
              </p>
            </div>
            <Switch
              id={`${id}-multi`}
              checked={draft.allow_multi_win}
              onCheckedChange={(checked) =>
                props.onChange({ allow_multi_win: checked })
              }
            />
          </div>
        )}

        <div className='space-y-1'>
          <Label htmlFor={`${id}-min-entries`}>
            {t('qy_lot_min_entries_field')}
          </Label>
          <Input
            id={`${id}-min-entries`}
            inputMode='numeric'
            value={String(draft.min_entries_to_hold)}
            onChange={(event) => {
              const digits = event.target.value.replaceAll(/\D/g, '')
              props.onChange({
                min_entries_to_hold: digits === '' ? 0 : Number(digits),
              })
            }}
          />
          {/* 平台侧唯一的止损阀，而且对用户完全公平：不足即流局、全额退款。
              没有它就会出现"3 个人参加、平台净亏一个一等奖"。 */}
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_min_entries_hint_field')}
          </p>
        </div>
      </div>
    )
  }

  const catchAllCount = draft.options.filter(
    (option) => option.is_catch_all
  ).length

  return (
    <div className='space-y-3'>
      {draft.options.map((option, index) => (
        <div key={option.id} className='space-y-2 rounded-lg border p-3'>
          <div className='flex items-center gap-2'>
            <Input
              value={option.label}
              maxLength={80}
              placeholder={t('qy_lot_option_placeholder')}
              onChange={(event) =>
                props.onChange({
                  options: draft.options.map((item, i) =>
                    i === index ? { ...item, label: event.target.value } : item
                  ),
                })
              }
            />
            <Button
              type='button'
              variant='ghost'
              size='icon-sm'
              aria-label={t('qy_common_delete')}
              disabled={draft.options.length <= 2}
              onClick={() =>
                props.onChange({
                  options: draft.options.filter((_, i) => i !== index),
                })
              }
            >
              <X aria-hidden='true' />
            </Button>
          </div>
          <div className='flex items-center justify-between gap-3'>
            <Label className='text-xs'>{t('qy_lot_option_catch_all')}</Label>
            <Switch
              checked={option.is_catch_all}
              onCheckedChange={(checked) =>
                props.onChange({
                  options: draft.options.map((item, i) =>
                    i === index ? { ...item, is_catch_all: checked } : item
                  ),
                })
              }
            />
          </div>
        </div>
      ))}

      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={() =>
          props.onChange({
            options: [...draft.options, qyLotNewOption()],
          })
        }
      >
        <Plus aria-hidden='true' />
        {t('qy_lot_add_option')}
      </Button>

      {/* 没有兜底项时「全部猜错」会频繁发生，届时全场退款、平台零收益。
          这条代价必须印在界面上 —— 删掉它的人不会去读文档。 */}
      {catchAllCount === 0 && (
        <Alert>
          <TriangleAlert />
          <AlertTitle>{t('qy_lot_no_catch_all_title')}</AlertTitle>
          <AlertDescription>{t('qy_lot_no_catch_all_desc')}</AlertDescription>
        </Alert>
      )}

      <div className='space-y-1'>
        <Label htmlFor={`${id}-fee`}>{t('qy_lot_fee_bps')}</Label>
        <Input
          id={`${id}-fee`}
          inputMode='numeric'
          value={String(draft.fee_bps)}
          onChange={(event) => {
            const digits = event.target.value.replaceAll(/\D/g, '')
            props.onChange({ fee_bps: digits === '' ? 0 : Number(digits) })
          }}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_fee_bps_hint', {
            percent: (draft.fee_bps / 100).toFixed(2),
          })}
        </p>
      </div>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-guess-min`}>
          {t('qy_lot_min_entries_field')}
        </Label>
        <Input
          id={`${id}-guess-min`}
          inputMode='numeric'
          value={String(draft.min_entries_to_hold)}
          onChange={(event) => {
            const digits = event.target.value.replaceAll(/\D/g, '')
            props.onChange({
              min_entries_to_hold: digits === '' ? 0 : Number(digits),
            })
          }}
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_min_entries_guess_hint')}
        </p>
      </div>
    </div>
  )
}

// ────────────────────── 第二步（双色球的那一支） ──────────────────────

/**
 * 双色球奖级表。
 *
 * ## 为什么概率是这里自己算的
 *
 * 各档中奖概率是号池四元组与命中门槛的**组合数结果**。它不是一个可以由管理员
 * 输入的数：给它一个输入框（或者让后端下发一个数字）就等于允许在这件事上撒谎，
 * 而"概率不需要相信平台"是双色球唯一但决定性的优势。所以这一列由
 * `qyLotBallTierOdds` 按后端 `MatchTier` 的同一条规则当场枚举算出，管理员只能
 * 改门槛，改完立刻看到概率跟着变。
 *
 * ## 为什么金额有两种形态
 *
 * 固定奖（发满 N 份、每份 X）与浮动奖（占本期池子的万分比、由全部中签者均分）
 * 在后端是互斥的两支（`checkBallTierInput`）：一档同时写死金额又写占池比例时，
 * 到底按哪个发只能靠代码里的先后顺序回答。所以表单也做成互斥的单选，而不是
 * 让运营两个都填完再被 400 顶回来。
 */
function BallSpecStep(props: {
  draft: QyLotDraft
  onChange: (patch: Partial<QyLotDraft>) => void
  series: QyLotSeries | undefined
}) {
  const { draft, series } = props
  const { t } = useTranslation()
  const id = useId()

  const pool: QyLotBallPool = {
    redPool: series?.red_pool ?? 0,
    redPick: series?.red_pick ?? 0,
    bluePool: series?.blue_pool ?? 0,
    bluePick: series?.blue_pick ?? 0,
  }
  const poolKnown = isQyLotBallPoolValid(pool)
  const odds = new Map(
    qyLotBallTierOdds(pool, draft.tiers).map((item) => [item.tier, item])
  )

  const patchTier = (index: number, next: Partial<QyLotDraft['tiers'][0]>) => {
    props.onChange({
      tiers: draft.tiers.map((item, i) =>
        i === index ? { ...item, ...next } : item
      ),
    })
  }

  if (series == null) {
    return (
      <Alert>
        <TriangleAlert />
        <AlertTitle>{t('qy_lot_ball_no_series_title')}</AlertTitle>
        <AlertDescription>
          {t('qy_lot_ball_pick_series_first')}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <div className='space-y-3'>
      <div className='rounded-lg border p-3'>
        <QyKeyValue label={t('qy_lot_ball_series')}>{series.title}</QyKeyValue>
        <QyKeyValue label={t('qy_lot_ball_pool_label')}>
          {t('qy_lot_ball_pool_desc', {
            redPick: series.red_pick,
            redPool: series.red_pool,
            bluePick: series.blue_pick,
            bluePool: series.blue_pool,
          })}
        </QyKeyValue>
        {/* 「本期开局池子」= 系列上还没被任何一期取走的那一块。它在发布那一刻
            被原子取走并冻结进承诺，所以这里显示的是**预计值** —— 期间若有人
            再注资，实际开局池只会更大。 */}
        <QyKeyValue label={t('qy_lot_ball_series_pool')}>
          <QyAmountText quota={series.pool_quota} />
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_ball_share_bps')}>
          {t('qy_lot_ball_pool_share', {
            percent: (series.pool_share_bps / 100).toFixed(2),
          })}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_ball_headroom')}>
          <QyAmountText quota={series.headroom_quota} />
        </QyKeyValue>
      </div>

      {draft.tiers.map((tier, index) => {
        const floating = (tier.pool_share_bps ?? 0) > 0
        const item = odds.get(tier.tier)
        return (
          <div key={tier.tier} className='space-y-2 rounded-lg border p-3'>
            <div className='flex items-center justify-between'>
              <span className='text-sm font-medium'>
                {t('qy_lot_tier_no', { no: tier.tier })}
              </span>
              <Button
                type='button'
                variant='ghost'
                size='icon-sm'
                aria-label={t('qy_common_delete')}
                disabled={draft.tiers.length <= 1}
                onClick={() =>
                  props.onChange({
                    tiers: draft.tiers.filter((_, i) => i !== index),
                  })
                }
              >
                <X aria-hidden='true' />
              </Button>
            </div>

            <div className='grid gap-2 sm:grid-cols-3'>
              <div className='space-y-1'>
                <Label>{t('qy_lot_prize_name')}</Label>
                <Input
                  value={tier.name}
                  maxLength={40}
                  onChange={(event) =>
                    patchTier(index, { name: event.target.value })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label>{t('qy_lot_ball_red_match')}</Label>
                <Input
                  inputMode='numeric'
                  value={String(tier.red_match ?? 0)}
                  onChange={(event) =>
                    patchTier(index, {
                      red_match: digitsOf(event.target.value),
                    })
                  }
                />
              </div>
              <div className='space-y-1'>
                <Label>{t('qy_lot_ball_blue_match')}</Label>
                <Input
                  inputMode='numeric'
                  value={String(tier.blue_match ?? 0)}
                  onChange={(event) =>
                    patchTier(index, {
                      blue_match: digitsOf(event.target.value),
                    })
                  }
                />
              </div>
            </div>

            <div className='space-y-1'>
              <Label>{t('qy_lot_ball_prize_shape')}</Label>
              <Select
                value={floating ? 'floating' : 'fixed'}
                onValueChange={(value) =>
                  // 切形态时把另一支的字段清零：互斥的两支同时有值，后端会 400，
                  // 而运营看到的界面上两个数都还在，无从判断是哪个在起作用。
                  patchTier(
                    index,
                    value === 'floating'
                      ? { pool_share_bps: 1000, amount_quota: 0 }
                      : { pool_share_bps: 0, amount_quota: 0 }
                  )
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='fixed'>
                    {t('qy_lot_ball_fixed')}
                  </SelectItem>
                  <SelectItem value='floating'>
                    {t('qy_lot_ball_floating')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>

            {floating ? (
              <div className='space-y-1'>
                <Label>{t('qy_lot_ball_share_bps')}</Label>
                <Input
                  inputMode='numeric'
                  value={String(tier.pool_share_bps ?? 0)}
                  onChange={(event) =>
                    patchTier(index, {
                      pool_share_bps: digitsOf(event.target.value),
                    })
                  }
                />
                <p className='text-muted-foreground text-xs'>
                  {t('qy_lot_ball_share_hint', {
                    percent: ((tier.pool_share_bps ?? 0) / 100).toFixed(2),
                  })}
                </p>
              </div>
            ) : (
              <div className='grid gap-2 sm:grid-cols-2'>
                <div className='space-y-1'>
                  <Label>{t('qy_lot_prize_amount')}</Label>
                  <QyAmountInput
                    value={tier.amount_quota}
                    onChange={(quota) =>
                      patchTier(index, { amount_quota: quota })
                    }
                  />
                </div>
                <div className='space-y-1'>
                  <Label>{t('qy_lot_count_is_budget')}</Label>
                  <Input
                    inputMode='numeric'
                    value={String(tier.count)}
                    onChange={(event) =>
                      patchTier(index, { count: digitsOf(event.target.value) })
                    }
                  />
                </div>
              </div>
            )}

            {poolKnown && (
              // 概率跟着门槛实时变。它是本地算的，不来自任何接口 ——
              // 管理员在这件事上没有输入通道。
              <p className='text-muted-foreground text-xs tabular-nums'>
                {item == null || item.probability <= 0
                  ? t('qy_lot_ball_odds_never')
                  : t('qy_lot_ball_odds_admin', {
                      percent: (item.probability * 100).toPrecision(3),
                      odds: item.odds,
                    })}
              </p>
            )}
          </div>
        )
      })}

      <Button
        type='button'
        variant='outline'
        size='sm'
        onClick={() =>
          props.onChange({
            tiers: [
              ...draft.tiers,
              {
                tier:
                  draft.tiers.reduce(
                    (max, item) => Math.max(max, item.tier),
                    0
                  ) + 1,
                name: '',
                amount_quota: 0,
                count: 1,
                red_match: 0,
                blue_match: 0,
                pool_share_bps: 0,
              },
            ],
          })
        }
      >
        <Plus aria-hidden='true' />
        {t('qy_lot_add_tier')}
      </Button>

      <div className='space-y-1'>
        <Label htmlFor={`${id}-ball-min`}>
          {t('qy_lot_min_entries_field')}
        </Label>
        <Input
          id={`${id}-ball-min`}
          inputMode='numeric'
          value={String(draft.min_entries_to_hold)}
          onChange={(event) =>
            props.onChange({
              min_entries_to_hold: digitsOf(event.target.value),
            })
          }
        />
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_min_entries_hint_field')}
        </p>
      </div>

      {/* 双色球下"每张票独立、概率严格等于组合数给出的值"是全部主张，所以后端
          强制不去重（allow_multi_win 恒为真）。写出来，免得运营在规则页上配了
          一个不会生效的开关。 */}
      <p className='text-muted-foreground text-xs'>
        {t('qy_lot_ball_multi_win_note')}
      </p>
    </div>
  )
}

/** 输入框里的数字。非数字字符一律丢弃，空串按 0。 */
function digitsOf(raw: string): number {
  const digits = raw.replaceAll(/\D/g, '')
  return digits === '' ? 0 : Number(digits)
}

// ───────────────────────────── 第四步 ─────────────────────────────

function ReviewStep(props: {
  draft: QyLotDraft
  errors: string[]
  totalPrize: number
  breakEven: number
  alertQuota: number
  series: QyLotSeries | undefined
}) {
  const { draft } = props
  const { t } = useTranslation()
  const isBall = qyLotPlayOf(draft) === 'ball'

  return (
    <div className='space-y-3'>
      {props.errors.length > 0 && (
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('qy_lot_review_invalid_title')}</AlertTitle>
          <AlertDescription>
            <ul className='mt-1 space-y-1'>
              {props.errors.map((key) => (
                <li key={key}>{t(key)}</li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}

      {/* 发布之后再也改不了的那些字段单独列一屏。它们全部进 `commit_hash`
          原像 —— 事后改任何一项，验证脚本都会算出不一样的哈希。 */}
      <div className='rounded-lg border p-3'>
        <p className='mb-2 text-sm font-medium'>
          {t('qy_lot_review_frozen_title')}
        </p>
        {/* 双色球的 `kind` 也是 `draw`，照 kind 印会在最后一屏上写着「抽奖」，
            而这一屏的全部意义是"看清楚即将被永久冻结的东西"。 */}
        <QyKeyValue label={t('qy_lot_play')}>
          {isBall ? t('qy_lot_mode_ball') : t(`qy_lot_kind_${draft.kind}`)}
        </QyKeyValue>
        {draft.kind === 'draw' && !isBall && (
          <QyKeyValue label={t('qy_lot_draw_mode')}>
            {draft.draw_mode === 'prob'
              ? t('qy_lot_mode_prob')
              : t('qy_lot_mode_rank')}
          </QyKeyValue>
        )}
        {isBall && props.series != null && (
          <>
            <QyKeyValue label={t('qy_lot_ball_series')}>
              {props.series.title}
            </QyKeyValue>
            {/* 号池进每期的承诺原像，而它在系列上定死、期与期之间不可变 ——
                所以它出现在"即将被永久冻结"这一屏里是名副其实的。 */}
            <QyKeyValue label={t('qy_lot_ball_pool_label')}>
              {t('qy_lot_ball_pool_desc', {
                redPick: props.series.red_pick,
                redPool: props.series.red_pool,
                bluePick: props.series.blue_pick,
                bluePool: props.series.blue_pool,
              })}
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_ball_series_pool')}>
              <QyAmountText quota={props.series.pool_quota} />
            </QyKeyValue>
          </>
        )}
        <QyKeyValue label={t('qy_lot_stake')}>
          <QyAmountText quota={draft.stake_quota} />
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_open_at')}>
          {formatQyTs(draft.open_at)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_close_at')}>
          {formatQyTs(draft.close_at)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_draw_at')}>
          {formatQyTs(draft.draw_at)}
        </QyKeyValue>
        <QyKeyValue label={t('qy_lot_settle_deadline')}>
          {formatQyTs(draft.settle_deadline)}
        </QyKeyValue>
        {/* 显示的必须是**生效值**而不是草稿值：这一屏的标题是「即将被永久
            冻结」，而 rank 之外的两个玩法由后端强制置真并写进承诺原像。 */}
        <QyKeyValue label={t('qy_lot_allow_multi_win')}>
          {qyLotEffectiveAllowMultiWin(draft)
            ? t('qy_common_on')
            : t('qy_common_off')}
        </QyKeyValue>
        {draft.kind === 'guess' && (
          <QyKeyValue label={t('qy_lot_fee_bps')}>{draft.fee_bps}</QyKeyValue>
        )}
        <QyKeyValue label={t('qy_lot_min_entries_field')}>
          {draft.min_entries_to_hold}
        </QyKeyValue>
      </div>

      {isBall && (
        <div className='rounded-lg border p-3'>
          <p className='mb-2 text-sm font-medium'>
            {t('qy_lot_review_money_title')}
          </p>
          {/* 双色球的支出上界与普通抽奖不是一件事：浮动奖档的额度恒为 0，
              Σ(amount×count) 只覆盖固定奖那一部分，剩下的由期次池按比例出。
              摆一个"奖品总额"在这里会让运营以为最坏支出就是那个数。真正的
              硬约束在发布期由 checkBallPoolCovers 守：
              固定支出 + 开局池×Σ占比 ≤ 开局池。 */}
          <QyKeyValue label={t('qy_lot_ball_fixed_total')}>
            <QyAmountText quota={props.totalPrize} />
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_ball_share_total')}>
            {t('qy_lot_ball_pool_share', {
              percent: (
                draft.tiers.reduce(
                  (sum, tier) => sum + Math.max(0, tier.pool_share_bps ?? 0),
                  0
                ) / 100
              ).toFixed(2),
            })}
          </QyKeyValue>
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('qy_lot_ball_money_note')}
          </p>
        </div>
      )}

      {draft.kind === 'draw' && !isBall && (
        <div className='rounded-lg border p-3'>
          <p className='mb-2 text-sm font-medium'>
            {t('qy_lot_review_money_title')}
          </p>
          {/* 抽奖是「平台收参与费、平台出奖品」，两边不守恒是正常的：
              派奖是对用户额度的**净增发**，本仓不存在平台账户。 */}
          <QyKeyValue label={t('qy_lot_total_prize')}>
            <QyAmountText quota={props.totalPrize} />
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_break_even')}>
            {props.breakEven === 0 ? '-' : props.breakEven}
          </QyKeyValue>
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('qy_lot_break_even_note', { count: props.breakEven })}
          </p>
          {props.alertQuota > 0 && props.totalPrize > props.alertQuota && (
            <Alert className='mt-2'>
              <TriangleAlert />
              <AlertTitle>{t('qy_lot_large_prize_title')}</AlertTitle>
              <AlertDescription>
                {t('qy_lot_large_prize_desc')}
              </AlertDescription>
            </Alert>
          )}
        </div>
      )}

      <p className='text-muted-foreground text-xs'>
        {t('qy_lot_create_draft_note')}
      </p>
    </div>
  )
}
