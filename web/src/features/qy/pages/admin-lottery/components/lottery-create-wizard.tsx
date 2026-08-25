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
import { formatQyQuotaBound, formatQyQuotaLedger } from '../../../lib/format'
import { qyKeys } from '../../../lib/query-keys'
import {
  isQyLotBallPoolValid,
  qyLotBallTierOdds,
  type QyLotBallPool,
} from '../../lottery/lib/ball'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import {
  createQyLotActivity,
  qyAdminLotSeriesQuery,
  updateQyLotActivity,
} from '../api'
import {
  QY_LOT_BET_MAX_MULTIPLE,
  qyLotEntriesCap,
  qyLotPoolShareHeadroom,
  qyLotRecommendedBetMax,
  qyLotRecommendedMinEntries,
  qyLotTierAmountFloor,
  qyLotTierBudgetShort,
  qyLotTierCountFloor,
  qyLotWinPpmHeadroom,
} from '../lib/advice'
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
import type { QyLotAdminConfig, QyLotSeries, QyLotYamlReadonly } from '../types'
import { QyLotCoverField } from './lottery-cover-field'
import { QyLotFieldAdvice } from './lottery-field-advice'
import { QyLotRulesEditor } from './lottery-rules-editor'

const STEPS = ['basic', 'spec', 'rules', 'review'] as const
type QyLotStep = (typeof STEPS)[number]

/**
 * 活动向导 —— **建一份草稿，或改一份草稿**。
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
 *
 * ## 为什么编辑与创建是同一个组件，而不是第二份表单
 *
 * 项目方原话：「草稿活动为什么改不了删不了。」`PUT /lottery/activities/:act_no`
 * 一直都在，只是**从来没有前端调用方** —— 而它的请求体与创建**完全相同**
 * （后端 `activityInput` 是同一个结构，`draftUpdates` 一次写四十来列、
 * 奖档与选项两张从表整表删了重建）。
 *
 * 再写一份编辑表单意味着四十来个字段、二十来条跨步校验、三种玩法的归一化各存两份。
 * 那两份必然漂移，而漂移的表现是：某个字段在创建时校验得住、在编辑时校验不住，
 * 或者反过来在编辑时被静默清零 —— 而 PUT 是整体替换语义，被清零的那一列在
 * 界面上没有任何一格提示过。所以这里只多一个 `edit` 入参：
 * 初值从 {@link qyLotDraftFromActivity} 整份读回，提交打另一个端点，
 * 中间那四步一个字节都不分叉。
 */
export function QyLotActivityWizard(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  config: QyLotAdminConfig | undefined
  /**
   * 传了就是**改**这一份草稿，不传就是建新的。
   *
   * `draft` 由调用方从活动详情整份重建 —— 组件自己不拉那次请求，因为详情页
   * 已经拉过一次，再拉一次会让"表单里的值"与"页面上显示的值"来自两份快照。
   */
  edit?: { actNo: string; draft: QyLotDraft }
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const defaultFee = props.config?.effective.default_guess_fee_bps ?? 500
  const editing = props.edit
  const [draft, setDraft] = useState<QyLotDraft>(
    () => editing?.draft ?? qyLotEmptyDraft(defaultFee)
  )
  const [step, setStep] = useState<QyLotStep>('basic')
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 每次打开都重置。编辑时重置成**服务端此刻那一份**，而不是上次关掉时留在
  // 组件里的那份：草稿可以被另一个管理员改过，拿一份陈旧的表单整体提交上去
  // 等于把别人的改动无声地回滚掉（PUT 是整体替换）。
  const editActNo = editing?.actNo
  const editDraft = editing?.draft
  useEffect(() => {
    if (!props.open) return
    setDraft(editDraft ?? qyLotEmptyDraft(defaultFee))
    setStep('basic')
    setConfirmOpen(false)
  }, [props.open, defaultFee, editActNo, editDraft])

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

  const totalPrize = qyLotTotalPrizeQuota(draft)
  const breakEven = qyLotBreakEvenEntries(draft)
  const alertQuota = props.config?.effective.large_prize_alert_quota ?? 0
  // 阈值判据是 **>=**，与后端 `requireNetIssueConfirm` 逐字同源。一边 `>` 一边
  // `>=` 的表现是恰好等于阈值的那一场在界面上不弹确认、提交后吃一个 400，
  // 而那句 400 要求回填的正是界面刚刚决定不显示的那个数。
  const needsNetIssueConfirm = alertQuota > 0 && totalPrize >= alertQuota

  const mutation = useMutation({
    // 回执只在运营真的勾过那个不可逆确认框之后才带上（走到 onConfirm 就意味着
    // 勾过了）。无条件回填等于让这道确认自我满足 —— 一行代码都没少写，
    // 却什么都没拦住。
    mutationFn: () => {
      const echo = needsNetIssueConfirm ? totalPrize : 0
      return editing == null
        ? createQyLotActivity(qyLotDraftToInput(draft, echo))
        : updateQyLotActivity(editing.actNo, qyLotDraftToInput(draft, echo))
    },
    onSuccess: async (data) => {
      toast.success(editing == null ? t('qy_lot_created') : t('qy_lot_updated'))
      setConfirmOpen(false)
      props.onOpenChange(false)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
      // 建新的才跳详情页：下一步（发布）在那里，而且那一步才是不可逆的。
      // 改草稿本来就是在详情页上点开的，跳一次只会把滚动位置丢掉。
      if (editing != null) return
      await navigate({
        to: '/qy/admin/lottery/$actNo',
        params: { actNo: data.act_no },
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const index = STEPS.indexOf(step)

  return (
    <>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={
          editing == null ? t('qy_lot_create_title') : t('qy_lot_edit_title')
        }
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
                {editing == null
                  ? t('qy_lot_create_submit')
                  : t('qy_lot_edit_submit')}
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
            <SpecStep
              draft={draft}
              onChange={patch}
              series={series}
              yaml={props.config?.yaml_readonly}
              alertQuota={alertQuota}
              capQuota={props.config?.effective.max_total_prize_quota ?? 0}
              maxFeeBps={props.config?.effective.max_guess_fee_bps ?? 0}
            />
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
        title={
          editing == null
            ? t('qy_lot_create_confirm_title')
            : t('qy_lot_edit_confirm_title')
        }
        description={
          editing == null
            ? t('qy_lot_create_confirm_desc')
            : t('qy_lot_edit_confirm_desc')
        }
        isLoading={mutation.isPending}
        // 越过阈值就把这一屏升格成不可逆确认：强制勾选 + 把金额写在正文里。
        // 它替换掉的是原来那道「Σ 超过上限就 400」的硬拒绝 —— 硬拒绝拦不住手滑
        // （调大上限，同一个零照样发得出去），只能把「卡半天」推迟到更大的数字上。
        irreversible={needsNetIssueConfirm}
        irreversibleDesc={t('qy_lot_net_issue_confirm_desc', {
          amount: formatQyQuotaLedger(totalPrize),
        })}
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
  // 0 = 不限（默认）。判据与 lib/draft.ts 的 qy_lot_v_stake_over_cap 同一条。
  const stakeCap = props.config?.yaml_readonly.max_stake_quota ?? 0
  // 全站额度换算的整数上界。旧后端不下发它，此时不编一个数出来。
  const systemMax = props.config?.yaml_readonly.system_max_quota ?? 0

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
        {/*
          两种上限**分两行**说。

          系统上界是全站额度换算的整数上界（代码写死），改任何配置都放不开；策略上限
          是站点自己在 `lottery.max_stake_quota` 里配的，0 = 不限（默认）。
          合成一句"不得超过系统上限"之后，运营分不出是自己配错了还是系统不
          支持，于是会跑去配置页找一个根本不存在的开关。

          就地说，而不是等他走完四步在复核屏才看到一行红字 —— 那时时间、条件、
          奖档全都已经填完了。
        */}
        <QyLotFieldAdvice
          ranges={[
            systemMax > 0
              ? t('qy_lot_range_physical', {
                  amount: formatQyQuotaBound(systemMax),
                })
              : '',
            stakeCap > 0
              ? t('qy_lot_range_policy_stake', {
                  amount: formatQyQuotaBound(stakeCap),
                })
              : t('qy_lot_range_policy_stake_unlimited'),
          ]}
          problem={
            stakeCap > 0 && draft.stake_quota > stakeCap
              ? t('qy_lot_v_stake_over_cap')
              : undefined
          }
        />
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

/**
 * 「这一屏填完，平台最坏会发出去多少」——**填的时候就摆在眼前**，
 * 而不是等点保存被一句 400 顶回来。
 *
 * 这是本次改造的另一半。硬拒绝被拿掉之后，唯一还能挡住「多写一个零」的是
 * 运营自己看见那个数：Σ(数量 × 额度) 随着每一次敲键实时重算，越过二次确认
 * 阈值时**当场**说清「提交时会要求你确认」，而不是让他走完剩下两步再遇到。
 *
 * 三档措辞对应三种处境，不能塌成一句：
 *   · 站点配了硬顶且已经超了 —— 这一场提交不了，得改数字或改配置；
 *   · 越过二次确认阈值 —— 提交得了，但要多勾一次；
 *   · 都没有 —— 只报一个数，不制造焦虑。
 */
function NetIssueMeter(props: {
  draft: QyLotDraft
  alertQuota: number
  capQuota: number
}) {
  const { t } = useTranslation()
  const total = qyLotTotalPrizeQuota(props.draft)
  if (total <= 0) return null

  const overCap = props.capQuota > 0 && total > props.capQuota
  // >= 与后端 requireNetIssueConfirm 同源。
  const needsConfirm = props.alertQuota > 0 && total >= props.alertQuota
  const amount = formatQyQuotaLedger(total)

  return (
    <Alert variant={overCap ? 'destructive' : undefined}>
      <TriangleAlert />
      <AlertTitle>{t('qy_lot_net_issue_meter_title', { amount })}</AlertTitle>
      <AlertDescription>
        {overCap
          ? t('qy_lot_net_issue_meter_over_cap', {
              cap: formatQyQuotaLedger(props.capQuota),
            })
          : needsConfirm
            ? t('qy_lot_net_issue_meter_needs_confirm', {
                threshold: formatQyQuotaLedger(props.alertQuota),
              })
            : t('qy_lot_net_issue_meter_ok')}
      </AlertDescription>
    </Alert>
  )
}

function SpecStep(props: {
  draft: QyLotDraft
  onChange: (patch: Partial<QyLotDraft>) => void
  series: QyLotSeries | undefined
  /** 只读配置段。推荐值要靠它里面的硬上限算，前端不自己抄一份常量。 */
  yaml: QyLotYamlReadonly | undefined
  /** 二次确认阈值（0 = 不打扰）。见 {@link NetIssueMeter}。 */
  alertQuota: number
  /** 站点自选的单场硬顶（0 = 不限）。 */
  capQuota: number
  /** 竞猜手续费万分比的上界。 */
  maxFeeBps: number
}) {
  const { draft } = props
  const { t } = useTranslation()
  const id = useId()

  // 本场理论上可能出现的最大有效票数。`max_total_entries` 填 0 时后端归一成
  // 系统硬上限，所以推荐值也必须按硬上限算 —— 按 0 算会给出一个提交必被拒的
  // 推荐值，而那比不给推荐值更糟。
  const entriesCap = qyLotEntriesCap(draft, props.yaml?.max_total_entries_hard)
  // 全站额度换算的整数上界。旧后端不下发它，此时不编一个数出来。
  const systemMax = props.yaml?.system_max_quota ?? 0
  // 只有概率制与双色球会摊薄（名次制按名次切片，发满 N 份就停），所以那条
  // 「数量 × 单份 ≥ 全场参与上限」的判据只在这两支下成立。
  const budgetApplies = draft.draw_mode === 'prob'
  const minEntriesAdvice = qyLotRecommendedMinEntries(draft, entriesCap)
  // 竞猜单注上限的推荐值必须夹在这一格**真正能填**的上界之内：系统上界与站点
  // 自己配的 max_stake_quota 里更紧的那一个（任一为 0 = 那一道不设限）。不夹的
  // 话，参与费大于「系统上界 ÷ 20」的场次点一下「自动填」就得到一个后端必拒的
  // 值，而界面上一条红字都不会有。
  const stakeCeiling = props.yaml?.max_stake_quota ?? 0
  const betCeiling =
    systemMax > 0 && stakeCeiling > 0
      ? Math.min(systemMax, stakeCeiling)
      : Math.max(systemMax, stakeCeiling)
  const betMaxAdvice = qyLotRecommendedBetMax(draft.stake_quota, betCeiling)

  if (qyLotPlayOf(draft) === 'ball') {
    return (
      <BallSpecStep
        draft={draft}
        onChange={props.onChange}
        series={props.series}
        entriesCap={entriesCap}
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
                {/*
                  「固定奖级的预算必须不小于全场参与上限」这条被点名的报错，
                  在这里变成一颗按钮：单份下限 = ⌈全场参与上限 ÷ 份数⌉，
                  两个量表单上都有，从来就不需要运营去猜。判据与提交校验
                  同源（`qyLotTierBudgetShort`），所以按下去必然合法。
                */}
                <QyLotFieldAdvice
                  ranges={
                    systemMax > 0
                      ? [
                          t('qy_lot_range_physical', {
                            amount: formatQyQuotaBound(systemMax),
                          }),
                        ]
                      : []
                  }
                  advice={
                    budgetApplies &&
                    qyLotTierAmountFloor(entriesCap, tier.count) > 0
                      ? t('qy_lot_advice_tier_amount', {
                          amount: formatQyQuotaBound(
                            qyLotTierAmountFloor(entriesCap, tier.count)
                          ),
                          entries: entriesCap,
                        })
                      : undefined
                  }
                  onApply={
                    budgetApplies &&
                    qyLotTierAmountFloor(entriesCap, tier.count) > 0
                      ? () =>
                          props.onChange({
                            tiers: draft.tiers.map((item, i) =>
                              i === index
                                ? {
                                    ...item,
                                    amount_quota: qyLotTierAmountFloor(
                                      entriesCap,
                                      item.count
                                    ),
                                  }
                                : item
                            ),
                          })
                      : undefined
                  }
                  problem={
                    budgetApplies &&
                    qyLotTierBudgetShort(
                      entriesCap,
                      tier.count,
                      tier.amount_quota
                    )
                      ? t('qy_lot_v_prob_budget_short')
                      : undefined
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
                {/* 同一条不等式的另一个解：单份已经定死时，份数至少要几份。
                    两颗按钮都给，运营改哪一格都行。 */}
                <QyLotFieldAdvice
                  ranges={
                    props.yaml != null
                      ? [
                          t('qy_lot_range_count', {
                            max: props.yaml.max_total_entries_hard,
                          }),
                        ]
                      : []
                  }
                  advice={
                    budgetApplies &&
                    qyLotTierCountFloor(entriesCap, tier.amount_quota) > 0
                      ? t('qy_lot_advice_tier_count', {
                          shares: qyLotTierCountFloor(
                            entriesCap,
                            tier.amount_quota
                          ),
                          entries: entriesCap,
                        })
                      : undefined
                  }
                  onApply={
                    budgetApplies &&
                    qyLotTierCountFloor(entriesCap, tier.amount_quota) > 0
                      ? () =>
                          props.onChange({
                            tiers: draft.tiers.map((item, i) =>
                              i === index
                                ? {
                                    ...item,
                                    count: qyLotTierCountFloor(
                                      entriesCap,
                                      item.amount_quota
                                    ),
                                  }
                                : item
                            ),
                          })
                      : undefined
                  }
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
                {/* 这一格能填多大完全由其余各档决定（Σ ≤ 100%），所以"还剩多少"
                    就是它的推荐上界 —— 不必等 Σ 越过 100% 再被后端拒。 */}
                <QyLotFieldAdvice
                  ranges={[
                    t('qy_lot_range_win_ppm', {
                      max: qyLotWinPpmHeadroom(draft, tier.tier),
                    }),
                  ]}
                  problem={
                    (tier.win_ppm ?? 0) <= 0 ||
                    (tier.win_ppm ?? 0) > qyLotWinPpmHeadroom(draft, tier.tier)
                      ? t('qy_lot_v_win_ppm_range')
                      : undefined
                  }
                />
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

        <NetIssueMeter
          draft={draft}
          alertQuota={props.alertQuota}
          capQuota={props.capQuota}
        />

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
          {/* 推荐值 = 保本参与人数 ⌈奖品总额 ÷ 参与费⌉。两个量表单上都有，
              所以这一格同样不需要猜 —— 而它的默认 0 意味着"亏多少都照开"，
              那不是一个人选出来的取值，是没人告诉他该填什么。 */}
          <QyLotFieldAdvice
            ranges={[t('qy_lot_range_min_entries')]}
            advice={
              minEntriesAdvice > 0
                ? t('qy_lot_advice_min_entries', { count: minEntriesAdvice })
                : undefined
            }
            onApply={
              minEntriesAdvice > 0
                ? () =>
                    props.onChange({ min_entries_to_hold: minEntriesAdvice })
                : undefined
            }
          />
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
        <QyLotFieldAdvice
          ranges={
            props.maxFeeBps > 0
              ? [t('qy_lot_range_fee_bps', { max: props.maxFeeBps })]
              : []
          }
          problem={
            props.maxFeeBps > 0 && draft.fee_bps > props.maxFeeBps
              ? t('qy_lot_v_fee_over_cap')
              : undefined
          }
        />
      </div>

      {/*
        单注上下限。`0 = 不限`。

        没有这两格时提交体恒发 0，于是竞猜**配不出单注上限** —— 而没有上限时
        一个大户可以在封盘前几秒压满获胜选项吃掉整个奖池，散户的期望收益归零；
        没有下限则会有 1 单位的骚扰投注刷名单。两条代价都写在活动行的注释里，
        界面上却一直没有入口。它同时是编辑流程的必需品：PUT 是整体替换，
        一份用接口配过上限的草稿被界面改一次就会被静默清零。
      */}
      <div className='grid gap-3 sm:grid-cols-2'>
        <div className='space-y-1'>
          <Label htmlFor={`${id}-bet-min`}>{t('qy_lot_bet_min')}</Label>
          <QyAmountInput
            id={`${id}-bet-min`}
            value={draft.bet_min_quota}
            onChange={(quota) => props.onChange({ bet_min_quota: quota })}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_bet_min_hint')}
          </p>
          <QyLotFieldAdvice
            ranges={[t('qy_lot_range_bet_min')]}
            advice={
              draft.stake_quota > 0
                ? t('qy_lot_advice_bet_min', {
                    amount: formatQyQuotaBound(draft.stake_quota),
                  })
                : undefined
            }
            onApply={
              draft.stake_quota > 0
                ? () => props.onChange({ bet_min_quota: draft.stake_quota })
                : undefined
            }
          />
        </div>
        <div className='space-y-1'>
          <Label htmlFor={`${id}-bet-max`}>{t('qy_lot_bet_max')}</Label>
          <QyAmountInput
            id={`${id}-bet-max`}
            value={draft.bet_max_quota}
            onChange={(quota) => props.onChange({ bet_max_quota: quota })}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_bet_max_hint')}
          </p>
          {/*
            上一轮留下的那条建议在这里落地：**给一个非零推荐值**，而不是只加
            一句提醒。理由写在 `lib/advice.ts` 的 qyLotRecommendedBetMax 上 ——
            `0 = 不限` 是后端 wire 语义（改不得），但"没填"与"我确实要不限"
            在表单上是同一个 0，而两者的代价差一个量级。

            填 0 时那一行是**提示**而不是红字：不限是一个合法取值，把它渲染成
            错误会让人学会无视红字。
          */}
          <QyLotFieldAdvice
            ranges={[
              t('qy_lot_range_bet_max'),
              draft.bet_max_quota === 0 ? t('qy_lot_bet_max_zero_note') : '',
            ]}
            advice={
              betMaxAdvice > 0
                ? t('qy_lot_advice_bet_max', {
                    amount: formatQyQuotaBound(betMaxAdvice),
                    multiple: QY_LOT_BET_MAX_MULTIPLE,
                  })
                : undefined
            }
            onApply={
              betMaxAdvice > 0
                ? () => props.onChange({ bet_max_quota: betMaxAdvice })
                : undefined
            }
            problem={
              draft.bet_max_quota > 0 &&
              draft.bet_min_quota > draft.bet_max_quota
                ? t('qy_lot_v_bet_order')
                : undefined
            }
          />
        </div>
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
        {/* 竞猜是彩池再分配，平台数学上不可能倒贴，所以这里没有"保本人数"
            可推 —— 不编一个推荐值出来，只说清区间与 0 的含义。 */}
        <QyLotFieldAdvice ranges={[t('qy_lot_range_min_entries')]} />
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
  /** 本场理论最大有效票数。固定奖档的单份下限由它与份数解出来。 */
  entriesCap: number
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
                {/* Σ 占池比例 ≤ 100%，所以"还剩多少"就是这一格的上界。 */}
                <QyLotFieldAdvice
                  ranges={[
                    t('qy_lot_range_pool_share', {
                      max: qyLotPoolShareHeadroom(draft, tier.tier),
                    }),
                  ]}
                  problem={
                    (tier.pool_share_bps ?? 0) >
                    qyLotPoolShareHeadroom(draft, tier.tier)
                      ? t('qy_lot_v_ball_share_sum')
                      : undefined
                  }
                />
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
                  {/* 被点名的那条报错在双色球这一支同样变成一颗按钮。判据与
                      概率制**逐字同源**（后端也共用一个构造器）。 */}
                  <QyLotFieldAdvice
                    advice={
                      qyLotTierAmountFloor(props.entriesCap, tier.count) > 0
                        ? t('qy_lot_advice_tier_amount', {
                            amount: formatQyQuotaBound(
                              qyLotTierAmountFloor(props.entriesCap, tier.count)
                            ),
                            entries: props.entriesCap,
                          })
                        : undefined
                    }
                    onApply={
                      qyLotTierAmountFloor(props.entriesCap, tier.count) > 0
                        ? () =>
                            patchTier(index, {
                              amount_quota: qyLotTierAmountFloor(
                                props.entriesCap,
                                tier.count
                              ),
                            })
                        : undefined
                    }
                    problem={
                      qyLotTierBudgetShort(
                        props.entriesCap,
                        tier.count,
                        tier.amount_quota
                      )
                        ? t('qy_lot_v_ball_budget_short')
                        : undefined
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
                  <QyLotFieldAdvice
                    advice={
                      qyLotTierCountFloor(props.entriesCap, tier.amount_quota) >
                      0
                        ? t('qy_lot_advice_tier_count', {
                            shares: qyLotTierCountFloor(
                              props.entriesCap,
                              tier.amount_quota
                            ),
                            entries: props.entriesCap,
                          })
                        : undefined
                    }
                    onApply={
                      qyLotTierCountFloor(props.entriesCap, tier.amount_quota) >
                      0
                        ? () =>
                            patchTier(index, {
                              count: qyLotTierCountFloor(
                                props.entriesCap,
                                tier.amount_quota
                              ),
                            })
                        : undefined
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
          <>
            <QyKeyValue label={t('qy_lot_fee_bps')}>{draft.fee_bps}</QyKeyValue>
            {/* 单注上下限**不进** commit 原像，但它们仍然属于这一屏：
                发布之后没有任何接口能改它们（换封面那条只写两列封面），
                而它们决定一个大户能不能在封盘前几秒压满获胜选项吃掉奖池。
                后者正是不给它们开"发布后可改"入口的理由 —— 中途调上限
                等于看到某人下注之后再把他挡在门外。 */}
            <QyKeyValue label={t('qy_lot_bet_min')}>
              {draft.bet_min_quota === 0 ? (
                t('qy_lot_bet_unlimited')
              ) : (
                <QyAmountText quota={draft.bet_min_quota} />
              )}
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_bet_max')}>
              {draft.bet_max_quota === 0 ? (
                t('qy_lot_bet_unlimited')
              ) : (
                <QyAmountText quota={draft.bet_max_quota} />
              )}
            </QyKeyValue>
          </>
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
          {props.alertQuota > 0 &&
            props.totalPrize >= props.alertQuota && (
              // 判据是 **>=**，与后端 `requireNetIssueConfirm` 逐字同源。
              // 而且这一句必须把**金额写出来**：原来那句只说"超过了阈值"，
              // 而运营要判断的恰恰是"这个数是不是多了一个零"，不写出来就等于
              // 让他自己去乘一遍。
              <Alert className='mt-2'>
                <TriangleAlert />
                <AlertTitle>{t('qy_lot_large_prize_title')}</AlertTitle>
                <AlertDescription>
                  {t('qy_lot_large_prize_desc', {
                    amount: formatQyQuotaLedger(props.totalPrize),
                  })}
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
