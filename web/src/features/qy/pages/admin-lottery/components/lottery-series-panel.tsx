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
import { Plus, TriangleAlert } from 'lucide-react'
import { useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

import { QyAmountInput } from '../../../components/qy-amount-input'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyErrorMessage } from '../../../lib/api'
import { qyArray } from '../../../lib/array'
import { formatQyQuotaBound } from '../../../lib/format'
import { qyKeys } from '../../../lib/query-keys'
import {
  QY_LOT_BALL_MAX,
  isQyLotBallPoolValid,
  qyLotBallTierOdds,
} from '../../lottery/lib/ball'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import {
  closeQyLotSeries,
  createQyLotSeries,
  fundQyLotSeries,
  qyAdminLotConfigQuery,
  qyAdminLotSeriesQuery,
} from '../api'
import type { QyLotSeries, QyLotSeriesInput } from '../types'
import { QyLotFieldAdvice } from './lottery-field-advice'

const PAGE_SIZE = 50

/**
 * 双色球期次系列。
 *
 * ## 为什么它不是"活动"的一部分
 *
 * 一个系列跨很多期。它持有三样活动行上没有的东西：
 *
 *   · **号池四元组**——期与期之间不可变，因为可变的号码空间等于可变的中奖概率，
 *     而"各档概率是组合数算出来的、不用相信平台"是双色球唯一但决定性的优势；
 *   · **跨期滚存的奖池**——某一期没派出去的部分滚进下一期，这正是"连滚三期"
 *     那种玩法体验的来源；
 *   · **累计发行上限 `issue_cap_quota`**——创建时冻结、此后任何接口都改不了。
 *
 * 第三条是这个界面存在的主要理由。单期的奖品总额上限拦不住滚存：每期注资到
 * 上限、连开 N 期无人中奖、某一期一次性发出去——每一期看起来都守住了，而平台
 * 实际净增发是 N 倍。把上限冻结在系列上，才能证明"一整个系列、无论开多少期、
 * 无论运气多差，累计净增发不超过创建时填的那个数"。
 *
 * 所以这一屏必须把 `headroom_quota`（还能再注多少）摆在最显眼处：它是运营在
 * 开新一期之前唯一需要先看的数。
 */
export function QyLotSeriesPanel() {
  const { t } = useTranslation()
  const [createOpen, setCreateOpen] = useState(false)
  const [fundTarget, setFundTarget] = useState<QyLotSeries | null>(null)
  const [closeTarget, setCloseTarget] = useState<QyLotSeries | null>(null)

  const query = useQuery(qyAdminLotSeriesQuery({ p: 1, page_size: PAGE_SIZE }))
  const items = qyArray(query.data?.items)

  return (
    <div className='space-y-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <p className='text-muted-foreground text-xs'>
          {t('qy_lot_ball_series_panel_note')}
        </p>
        <Button size='sm' onClick={() => setCreateOpen(true)}>
          <Plus aria-hidden='true' />
          {t('qy_lot_ball_series_create')}
        </Button>
      </div>

      <QyPageBoundary
        query={query}
        isEmpty={query.data != null && items.length === 0}
        emptyTitle={t('qy_lot_ball_series_empty')}
      >
        <div className='overflow-x-auto'>
          <StaticDataTable
            data={items}
            getRowKey={(row: QyLotSeries) => row.series_no}
            columns={[
              {
                id: 'title',
                header: t('qy_lot_ball_series'),
                cell: (row: QyLotSeries) => (
                  <span className='inline-flex flex-wrap items-center gap-1.5'>
                    <span className='break-words'>{row.title}</span>
                    <Badge
                      variant={row.status === 'open' ? 'outline' : 'secondary'}
                    >
                      {t(`qy_lot_ball_series_st_${row.status}`, {
                        defaultValue: row.status,
                      })}
                    </Badge>
                  </span>
                ),
              },
              {
                id: 'pool',
                header: t('qy_lot_ball_pool_label'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotSeries) =>
                  t('qy_lot_ball_pool_desc', {
                    redPick: row.red_pick,
                    redPool: row.red_pool,
                    bluePick: row.blue_pick,
                    bluePool: row.blue_pool,
                  }),
              },
              {
                id: 'share',
                header: t('qy_lot_ball_share_bps'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotSeries) =>
                  t('qy_lot_ball_pool_share', {
                    percent: (row.pool_share_bps / 100).toFixed(2),
                  }),
              },
              {
                id: 'current',
                header: t('qy_lot_ball_series_pool'),
                cell: (row: QyLotSeries) => (
                  <QyAmountText quota={row.pool_quota} />
                ),
              },
              {
                // 「还能再注多少」是整张表最重要的一列：它是这个系列在其整个
                // 生命周期里、平台累计净增发的剩余上界。
                id: 'headroom',
                header: t('qy_lot_ball_headroom'),
                cell: (row: QyLotSeries) => (
                  <QyAmountText quota={row.headroom_quota} />
                ),
              },
              {
                id: 'issue',
                header: t('qy_lot_ball_issue_seq'),
                cellClassName: 'tabular-nums',
                cell: (row: QyLotSeries) => row.issue_seq,
              },
              {
                id: 'actions',
                header: t('qy_common_actions'),
                cell: (row: QyLotSeries) => (
                  <span className='inline-flex flex-wrap gap-1.5'>
                    <Button
                      type='button'
                      size='sm'
                      variant='outline'
                      disabled={row.status !== 'open'}
                      onClick={() => setFundTarget(row)}
                    >
                      {t('qy_lot_ball_fund')}
                    </Button>
                    <Button
                      type='button'
                      size='sm'
                      variant='ghost'
                      disabled={row.status !== 'open'}
                      onClick={() => setCloseTarget(row)}
                    >
                      {t('qy_lot_ball_series_close')}
                    </Button>
                  </span>
                ),
              },
            ]}
          />
        </div>
      </QyPageBoundary>

      <SeriesCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
      <SeriesFundDialog
        series={fundTarget}
        onClose={() => setFundTarget(null)}
      />
      <SeriesCloseDialog
        series={closeTarget}
        onClose={() => setCloseTarget(null)}
      />
    </div>
  )
}

// ─────────────────────────── 新建系列 ───────────────────────────

const EMPTY_SERIES: QyLotSeriesInput = {
  title: '',
  // 默认给一个"一等奖概率落在千分之一到万分之一"的号池：真正的 33 选 6 + 16 选 1
  // 是 1772 万分之一，在一个 API 网关的用户体量下一等奖永远开不出、初始池子永远
  // 派不出去，而池子只涨不发在用户眼里就是骗局。
  red_pool: 12,
  red_pick: 3,
  blue_pool: 4,
  blue_pick: 1,
  pool_share_bps: 5000,
  issue_cap_quota: 0,
  seed_quota: 0,
}

function SeriesCreateDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<QyLotSeriesInput>(EMPTY_SERIES)
  const [confirmOpen, setConfirmOpen] = useState(false)
  // 两种上限的数都从这里来：系统上界（全站额度换算的整数上界）与策略上限
  // （站点自己配的 max_total_prize_quota）。前端不自己抄一份常量 ——
  // 抄一份的表现是后端某天改了口径而界面还在教人填旧的那个数。
  const configQuery = useQuery({
    ...qyAdminLotConfigQuery(),
    enabled: props.open,
  })
  const systemMax = configQuery.data?.yaml_readonly.system_max_quota ?? 0
  const policyCap = configQuery.data?.effective.max_total_prize_quota ?? 0

  const pool = {
    redPool: form.red_pool,
    redPick: form.red_pick,
    bluePool: form.blue_pool,
    bluePick: form.blue_pick,
  }
  const poolValid = isQyLotBallPoolValid(pool)
  // 头奖 = 红蓝全中。用一张"只有一等奖"的虚拟奖级表借 qyLotBallTierOdds 算它，
  // 就不必在这里再写一份组合数——两处各写一份必然漂移，而这个数正是管理员用来
  // 判断"这个号池会不会永远开不出一等奖"的唯一依据。
  const jackpot = poolValid
    ? qyLotBallTierOdds(pool, [
        {
          tier: 1,
          name: '',
          amount_quota: 0,
          count: 1,
          red_match: form.red_pick,
          blue_match: form.blue_pick,
        },
      ])[0]
    : undefined

  const mutation = useMutation({
    mutationFn: () => createQyLotSeries(form),
    onSuccess: async () => {
      toast.success(t('qy_lot_ball_series_created'))
      setConfirmOpen(false)
      props.onOpenChange(false)
      setForm(EMPTY_SERIES)
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const patch = (next: Partial<QyLotSeriesInput>) =>
    setForm((prev) => ({ ...prev, ...next }))

  const errors: string[] = []
  if (form.title.trim() === '') errors.push('qy_lot_v_ball_series_title')
  if (!poolValid) errors.push('qy_lot_v_ball_pool_invalid')
  if (form.issue_cap_quota <= 0) errors.push('qy_lot_v_ball_cap_required')
  // 两道上限都要在**提交之前**拦住，而且分开报。少了这两条，字段旁边的提示
  // 说"超了"而按钮照样能点，运营点下去吃一个 400 —— 那正是这一轮要消灭的
  // "界面说 OK、后端拒绝"。
  else if (systemMax > 0 && form.issue_cap_quota > systemMax) {
    errors.push('qy_lot_v_ball_cap_over_physical')
  } else if (policyCap > 0 && form.issue_cap_quota > policyCap) {
    errors.push('qy_lot_v_ball_cap_over_policy')
  }
  if (form.seed_quota < 0 || form.seed_quota > form.issue_cap_quota) {
    errors.push('qy_lot_v_ball_seed_over_cap')
  }
  if (form.pool_share_bps < 0 || form.pool_share_bps > 10_000) {
    errors.push('qy_lot_v_ball_share_range')
  }

  return (
    <>
      <QyResponsiveDialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={t('qy_lot_ball_series_create')}
        description={t('qy_lot_ball_series_create_desc')}
        footer={
          <>
            <Button
              type='button'
              variant='outline'
              onClick={() => props.onOpenChange(false)}
            >
              {t('qy_common_cancel')}
            </Button>
            <Button
              type='button'
              disabled={errors.length > 0 || mutation.isPending}
              onClick={() => setConfirmOpen(true)}
            >
              {t('qy_common_confirm')}
            </Button>
          </>
        }
      >
        <div className='space-y-3'>
          <div className='space-y-1'>
            <Label htmlFor={`${id}-title`}>{t('qy_lot_title_field')}</Label>
            <Input
              id={`${id}-title`}
              value={form.title}
              maxLength={60}
              onChange={(event) => patch({ title: event.target.value })}
            />
          </div>

          <div className='grid gap-2 sm:grid-cols-4'>
            <NumberField
              label={t('qy_lot_ball_red_pool')}
              value={form.red_pool}
              max={QY_LOT_BALL_MAX.redPool}
              onChange={(value) => patch({ red_pool: value })}
            />
            <NumberField
              label={t('qy_lot_ball_red_pick')}
              value={form.red_pick}
              max={QY_LOT_BALL_MAX.redPick}
              onChange={(value) => patch({ red_pick: value })}
            />
            <NumberField
              label={t('qy_lot_ball_blue_pool')}
              value={form.blue_pool}
              max={QY_LOT_BALL_MAX.bluePool}
              onChange={(value) => patch({ blue_pool: value })}
            />
            <NumberField
              label={t('qy_lot_ball_blue_pick')}
              value={form.blue_pick}
              max={QY_LOT_BALL_MAX.bluePick}
              onChange={(value) => patch({ blue_pick: value })}
            />
          </div>
          {/* 号池一经创建就不可改（它进每期的承诺原像）。所以头奖概率必须在
              这一刻就算给运营看：一个 1772 万分之一的号池在这个体量下等于
              "一等奖永远不会开出、池子只涨不发"，而那在用户眼里就是骗局。 */}
          <p className='text-muted-foreground text-xs tabular-nums'>
            {jackpot == null || jackpot.probability <= 0
              ? t('qy_lot_ball_pool_hint_invalid')
              : t('qy_lot_ball_jackpot_odds', {
                  percent: (jackpot.probability * 100).toPrecision(3),
                  odds: jackpot.odds,
                })}
          </p>

          <div className='space-y-1'>
            <Label htmlFor={`${id}-share`}>{t('qy_lot_ball_share_bps')}</Label>
            <Input
              id={`${id}-share`}
              inputMode='numeric'
              value={String(form.pool_share_bps)}
              onChange={(event) =>
                patch({ pool_share_bps: numberOf(event.target.value, 10_000) })
              }
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_ball_share_series_hint', {
                percent: (form.pool_share_bps / 100).toFixed(2),
              })}
            </p>
          </div>

          <div className='space-y-1'>
            <Label htmlFor={`${id}-cap`}>{t('qy_lot_ball_issue_cap')}</Label>
            <QyAmountInput
              id={`${id}-cap`}
              value={form.issue_cap_quota}
              onChange={(quota) => patch({ issue_cap_quota: quota })}
            />
            {/* 这是这张表单里唯一真正决定平台风险的数，而且**创建之后没有任何
                接口能改它**。说清楚它管的是整个系列而不是一期。 */}
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_ball_issue_cap_hint')}
            </p>
            {/*
              项目方原话：「发行上限不得超过系统上限 ＄4294.967294 额度 —— 越过
              它的系列在注满之后会永久开不出新一期，而没有任何接口能把池子降
              回来。**这是什么问题？**」以及后来那句「不要几千 USD 太少了」。

              那句抱怨的根在后端：`common.MaxQuota` 原先是 math.MaxInt32，理由
              写的是"额度列在数据库里是 32 位"——而三个方言上那些列都是 64 位，
              整条理由是假的。上界已按真实约束（float64 / JS 精确整数区间，以及
              资金路径上最大的那个未经检查的乘数）重定为 2^43，默认刻度下是
              ＄17,592,186.04。这里一个字都不抄：数从 system_max_quota 下发。

              那个数是**全站额度换算的整数上界**（common.MaxQuota，代码写死），不是
              任何人配出来的运营策略 —— 所以它与站点自己配的 max_total_prize_quota
              分两行说，而且在填的时候就说，不等提交被拒之后才解释一遍。
            */}
            <QyLotFieldAdvice
              ranges={[
                systemMax > 0
                  ? t('qy_lot_range_physical', {
                      amount: formatQyQuotaBound(systemMax),
                    })
                  : '',
                policyCap > 0
                  ? t('qy_lot_range_policy_issue_cap', {
                      amount: formatQyQuotaBound(policyCap),
                    })
                  : t('qy_lot_range_policy_issue_cap_unlimited'),
              ]}
              problem={
                systemMax > 0 && form.issue_cap_quota > systemMax
                  ? t('qy_lot_v_ball_cap_over_physical')
                  : policyCap > 0 && form.issue_cap_quota > policyCap
                    ? t('qy_lot_v_ball_cap_over_policy')
                    : undefined
              }
            />
          </div>

          <div className='space-y-1'>
            <Label htmlFor={`${id}-seed`}>{t('qy_lot_ball_seed_quota')}</Label>
            <QyAmountInput
              id={`${id}-seed`}
              value={form.seed_quota}
              onChange={(quota) => patch({ seed_quota: quota })}
            />
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_ball_seed_quota_hint')}
            </p>
          </div>

          {errors.length > 0 && (
            <Alert variant='destructive'>
              <TriangleAlert />
              <AlertTitle>{t('qy_lot_review_invalid_title')}</AlertTitle>
              <AlertDescription>
                <ul className='mt-1 space-y-1'>
                  {errors.map((key) => (
                    <li key={key}>{t(key)}</li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}
        </div>
      </QyResponsiveDialog>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_lot_ball_series_create')}
        description={t('qy_lot_ball_series_confirm_desc')}
        irreversible
        isLoading={mutation.isPending}
        details={
          <div>
            <QyKeyValue label={t('qy_lot_ball_pool_label')}>
              {t('qy_lot_ball_pool_desc', {
                redPick: form.red_pick,
                redPool: form.red_pool,
                bluePick: form.blue_pick,
                bluePool: form.blue_pool,
              })}
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_ball_issue_cap')}>
              <QyAmountText quota={form.issue_cap_quota} />
            </QyKeyValue>
            <QyKeyValue label={t('qy_lot_ball_seed_quota')}>
              <QyAmountText quota={form.seed_quota} />
            </QyKeyValue>
          </div>
        }
        onConfirm={() => mutation.mutate()}
      />
    </>
  )
}

// ─────────────────────────── 注资 / 关闭 ───────────────────────────

function SeriesFundDialog(props: {
  series: QyLotSeries | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const queryClient = useQueryClient()
  const [amount, setAmount] = useState(0)
  const [note, setNote] = useState('')

  const series = props.series
  const mutation = useMutation({
    mutationFn: () =>
      fundQyLotSeries(series?.series_no ?? '', { amount, note: note.trim() }),
    onSuccess: async () => {
      toast.success(t('qy_lot_ball_funded'))
      setAmount(0)
      setNote('')
      props.onClose()
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const overCap = series != null && amount > series.headroom_quota

  return (
    <QyConfirmDialog
      open={series != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_ball_fund')}
      description={t('qy_lot_ball_fund_desc')}
      irreversible
      isLoading={mutation.isPending}
      confirmDisabled={amount <= 0 || overCap}
      details={
        <div className='space-y-2'>
          <QyKeyValue label={t('qy_lot_ball_series')}>
            {series?.title ?? ''}
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_ball_headroom')}>
            <QyAmountText quota={series?.headroom_quota ?? 0} />
          </QyKeyValue>
          <div className='space-y-1'>
            <Label htmlFor={`${id}-amount`}>{t('qy_common_amount')}</Label>
            <QyAmountInput
              id={`${id}-amount`}
              value={amount}
              onChange={setAmount}
            />
            {/* 超过剩余上限时后端会直接拒（条件 UPDATE 的 WHERE 就是那条判据），
                但让运营在按下之前看到，比让他填完再吃一个 400 要好。 */}
            {overCap && (
              <p className='text-destructive text-xs'>
                {t('qy_lot_ball_fund_over_cap')}
              </p>
            )}
          </div>
          <div className='space-y-1'>
            <Label htmlFor={`${id}-note`}>{t('qy_common_remark')}</Label>
            <Textarea
              id={`${id}-note`}
              rows={2}
              value={note}
              onChange={(event) => setNote(event.target.value)}
            />
          </div>
        </div>
      }
      onConfirm={() => mutation.mutate()}
    />
  )
}

function SeriesCloseDialog(props: {
  series: QyLotSeries | null
  onClose: () => void
}) {
  const { t } = useTranslation()
  const id = useId()
  const queryClient = useQueryClient()
  const [reason, setReason] = useState('')

  const series = props.series
  const mutation = useMutation({
    mutationFn: () =>
      closeQyLotSeries(series?.series_no ?? '', { reason: reason.trim() }),
    onSuccess: async () => {
      toast.success(t('qy_lot_ball_series_closed'))
      setReason('')
      props.onClose()
      await queryClient.invalidateQueries({ queryKey: qyKeys.all })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <QyConfirmDialog
      open={series != null}
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={t('qy_lot_ball_series_close')}
      // 滚存余额作废这件事必须在按下之前说出来，而且它要同时出现在活动规则页上：
      // 它从来不是任何用户的钱（每一期的投注在当期就已完成再分配），但用户不会
      // 自己想到这一点，等争议时才解释就晚了。
      description={t('qy_lot_ball_series_close_desc')}
      irreversible
      isLoading={mutation.isPending}
      confirmDisabled={reason.trim() === ''}
      details={
        <div className='space-y-2'>
          <QyKeyValue label={t('qy_lot_ball_series')}>
            {series?.title ?? ''}
          </QyKeyValue>
          <QyKeyValue label={t('qy_lot_ball_forfeited_pool')}>
            <QyAmountText quota={series?.pool_quota ?? 0} />
          </QyKeyValue>
          <div className='space-y-1'>
            <Label htmlFor={`${id}-reason`}>{t('qy_common_reason')}</Label>
            <Textarea
              id={`${id}-reason`}
              rows={2}
              value={reason}
              maxLength={200}
              onChange={(event) => setReason(event.target.value)}
            />
          </div>
        </div>
      }
      onConfirm={() => mutation.mutate()}
    />
  )
}

// ─────────────────────────── 小组件 ───────────────────────────

function NumberField(props: {
  label: string
  value: number
  max: number
  onChange: (value: number) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        inputMode='numeric'
        value={String(props.value)}
        onChange={(event) =>
          props.onChange(numberOf(event.target.value, props.max))
        }
      />
    </div>
  )
}

/** 输入框里的数字，夹到 `[0, max]`。非数字字符一律丢弃，空串按 0。 */
function numberOf(raw: string, max: number): number {
  const digits = raw.replaceAll(/\D/g, '')
  if (digits === '') return 0
  return Math.min(Number(digits), max)
}
