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
import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { ArrowLeft, ExternalLink } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { formatQyQuotaLedger } from '../../lib/format'
import { QyStatGrid } from '../components/qy-stat-grid'
import { QyLotRulesList } from '../lottery/components/lottery-rules-list'
import { QyLotSpecTable } from '../lottery/components/lottery-spec-table'
import {
  qyLotActivityBadgeStatus,
  qyLotOutcomeKey,
} from '../lottery/lib/display'
import { QY_EMPTY_TEXT, formatQyTs } from '../ops/format'
import { QyKeyValue } from '../ops/qy-ops-ui'
import { qyAdminLotActivityQuery } from './api'
import { QyLotCancelDialog } from './components/lottery-cancel-dialog'
import { QyLotEntriesTab } from './components/lottery-entries-tab'
import { QyLotEventsTab } from './components/lottery-events-tab'
import { QyLotFulfillQueueTab } from './components/lottery-fulfill-queue-tab'
import { QyLotGuessResultDialog } from './components/lottery-guess-result-dialog'
import { QyLotPayoutsTab } from './components/lottery-payouts-tab'
import { QyLotPublishDialog } from './components/lottery-publish-dialog'

/**
 * 活动详情（管理端）。
 *
 * ## 这一页刻意**没有**的按钮
 *
 * 没有「提前截止」，没有「立即开奖」，没有「重抽」，没有删除名单里的某一条。
 * 这不是没做完 —— 封盘与开奖都由定时任务在承诺过的时刻触发，管理员一旦能挑
 * 时刻，选时攻击就回来了。他能做的只有「整场取消」，而取消必然全额退款、
 * 必然公示、必然写审计：**他只能「不开」，不能「挑一个开」**。
 *
 * 唯一的例外是竞猜结果录入 —— 那是链下事实，没有任何算法能替代人，所以它被
 * 做成"一次性、强制附证据、写完不可改"。
 */
export function QyAdminLotteryDetail() {
  const { t } = useTranslation()
  const { actNo } = useParams({
    from: '/_authenticated/qy/admin/lottery/$actNo/',
  })

  const [publishOpen, setPublishOpen] = useState(false)
  const [cancelOpen, setCancelOpen] = useState(false)
  const [resultOpen, setResultOpen] = useState(false)

  const query = useQuery(qyAdminLotActivityQuery(actNo))
  const view = query.data
  const activity = view?.activity
  // 对外稳定的获胜编号在选项行上（`is_winner`），不在活动行上：活动行只有
  // 自增 id，而自增 id 跨环境不稳定，进不了证据链。
  const winOptNo = view?.options.find((option) => option.is_winner)?.opt_no ?? 0
  const spec =
    activity?.kind === 'draw' ? (view?.prizes ?? []) : (view?.options ?? [])
  // 配了文本奖才有履行队列。`prize_type` 是 `lot-v2` 才有的列，老活动读到
  // `undefined` —— 这里的 `=== 'text'` 因此天然对历史活动为假，不需要额外分支。
  const hasTextPrize = (view?.prizes ?? []).some(
    (prize) => prize.prize_type === 'text'
  )

  const canPublish = activity?.status === 'draft'
  // 取消在**终态之前**都允许：进行中要止损、封盘后发现条件写错了同样要止损。
  // 已 finished 的场次不可再动 —— 那时钱已经发完了。
  const canCancel =
    activity != null &&
    activity.status !== 'finished' &&
    activity.outcome === ''
  const canSetResult =
    activity != null &&
    activity.kind === 'guess' &&
    activity.status === 'locked' &&
    winOptNo === 0

  const outcomeKey = activity == null ? null : qyLotOutcomeKey(activity.outcome)
  // 双色球不是一个新的 kind，而是活动行上的一列。判据只有这一处，
  // 与列表页那一列（index.tsx）用同一个字段。
  const isBall = activity?.draw_mode === 'ball'

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {activity?.title ?? t('qy_nav_a_lottery')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button
          size='sm'
          variant='outline'
          render={<Link to='/qy/admin/lottery' />}
        >
          <ArrowLeft aria-hidden='true' />
          {t('qy_lot_a_back')}
        </Button>
        {activity != null && activity.status !== 'draft' && (
          <Button
            size='sm'
            variant='outline'
            render={
              <Link
                to='/qy/lottery/$actNo'
                params={{ actNo: activity.act_no }}
              />
            }
          >
            <ExternalLink aria-hidden='true' />
            {t('qy_lot_a_view_public')}
          </Button>
        )}
        {canPublish && (
          <Button size='sm' onClick={() => setPublishOpen(true)}>
            {t('qy_lot_publish_title')}
          </Button>
        )}
        {canSetResult && (
          <Button size='sm' onClick={() => setResultOpen(true)}>
            {t('qy_lot_result_title')}
          </Button>
        )}
        {canCancel && (
          <Button
            size='sm'
            variant='destructive'
            onClick={() => setCancelOpen(true)}
          >
            {t('qy_lot_cancel_title')}
          </Button>
        )}
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {activity != null && (
            <div className='space-y-4'>
              {/* 双色球的 kind 也是 'draw'，只渲染 qy_lot_kind_${kind} 的话
                  这一屏恒写「抽奖」—— 而列表页那一列（index.tsx）已经写着
                  「双色球 · 第 N 期」。同一个人在两屏上会对同一场活动得出两个
                  不同的玩法结论，这是最没道理的一种不一致。 */}
              <div className='flex flex-wrap items-center gap-2'>
                <Badge variant='outline'>
                  {isBall
                    ? `${t('qy_lot_mode_ball')} · ${t('qy_lot_ball_issue_no', { no: activity.issue_no ?? 0 })}`
                    : t(`qy_lot_kind_${activity.kind}`)}
                </Badge>
                <QyStatusBadge
                  status={qyLotActivityBadgeStatus(activity.status)}
                />
                {outcomeKey != null && (
                  <Badge variant='secondary'>{t(outcomeKey)}</Badge>
                )}
                <span className='text-muted-foreground font-mono text-xs'>
                  {activity.act_no}
                </span>
              </div>

              {/* 号池 / 期次 / 本期可派发池子 / 开奖号。
                  真正处于风险敞口的是 pool_open_quota（本期最坏要发多少），
                  而下面那张统计格里一个都没有它 —— 运营在开奖前无法从这一页
                  判断本期的支出上界。接口整行下发了这些字段，只是没渲染。 */}
              {isBall && (
                <div className='border-border/60 grid gap-3 rounded-md border p-3 sm:grid-cols-2 lg:grid-cols-4'>
                  <div className='flex flex-col gap-0.5'>
                    <span className='text-muted-foreground text-xs'>
                      {t('qy_lot_ball_series_no')}
                    </span>
                    <span className='font-mono text-sm'>
                      {activity.series_no || '-'}
                    </span>
                  </div>
                  <div className='flex flex-col gap-0.5'>
                    <span className='text-muted-foreground text-xs'>
                      {t('qy_lot_ball_pool_label')}
                    </span>
                    <span className='text-sm tabular-nums'>
                      {t('qy_lot_ball_pool_desc', {
                        redPool: activity.ball_red_pool ?? 0,
                        redPick: activity.ball_red_pick ?? 0,
                        bluePool: activity.ball_blue_pool ?? 0,
                        bluePick: activity.ball_blue_pick ?? 0,
                      })}
                    </span>
                  </div>
                  <div className='flex flex-col gap-0.5'>
                    <span className='text-muted-foreground text-xs'>
                      {t('qy_lot_ball_pool_open')}
                    </span>
                    <span className='text-sm tabular-nums'>
                      {formatQyQuotaLedger(activity.pool_open_quota ?? 0)}
                    </span>
                  </div>
                  <div className='flex flex-col gap-0.5'>
                    <span className='text-muted-foreground text-xs'>
                      {t('qy_lot_ball_result')}
                    </span>
                    <span className='font-mono text-sm'>
                      {activity.ball_result || '-'}
                    </span>
                  </div>
                </div>
              )}

              <QyStatGrid
                items={[
                  {
                    key: 'pool',
                    label: t('qy_lot_a_stat_pool'),
                    value: formatQyQuotaLedger(activity.pool_quota),
                  },
                  {
                    key: 'payout',
                    label: t('qy_lot_a_stat_payout'),
                    value: formatQyQuotaLedger(activity.payout_quota),
                  },
                  {
                    key: 'refund',
                    label: t('qy_lot_a_stat_refund'),
                    value: formatQyQuotaLedger(activity.refund_quota),
                  },
                  {
                    // 转人工的那部分必须单独摆出来：它是"平台还欠着、只是
                    // 发不出去"的钱，而收尾时的 payout_quota 只统计已到账的。
                    key: 'held',
                    label: t('qy_lot_a_stat_held'),
                    value: formatQyQuotaLedger(view?.economics.held_quota ?? 0),
                    emphasis: (view?.economics.held_quota ?? 0) > 0,
                    hint: t('qy_lot_a_stat_held_hint'),
                  },
                  {
                    key: 'net',
                    label: t('qy_lot_a_net'),
                    value: formatQyQuotaLedger(view?.economics.net_quota ?? 0),
                    emphasis: true,
                    hint: t('qy_lot_a_net_hint'),
                  },
                ]}
              />

              <Tabs defaultValue='overview'>
                <TabsList>
                  <TabsTrigger value='overview'>
                    {t('qy_lot_a_tab_overview')}
                  </TabsTrigger>
                  <TabsTrigger value='entries'>
                    {t('qy_lot_a_tab_entries')}
                  </TabsTrigger>
                  <TabsTrigger value='payouts'>
                    {t('qy_lot_a_tab_payouts')}
                  </TabsTrigger>
                  {/* 文本奖履行队列只在这一场真的配了文本奖时才出现。
                      恒显示一张空队列，会让运营每天多点一次去确认"还是空的"；
                      而只要配了，它就必须显式存在 —— 没有这张队列，文本奖会
                      静默烂掉，而用户会以为是抽奖作弊了。 */}
                  {hasTextPrize && (
                    <TabsTrigger value='fulfill'>
                      {t('qy_lot_a_tab_fulfill_queue')}
                    </TabsTrigger>
                  )}
                  <TabsTrigger value='events'>
                    {t('qy_lot_a_tab_events')}
                  </TabsTrigger>
                </TabsList>

                <TabsContent value='overview'>
                  <div className='grid gap-4 lg:grid-cols-2 lg:items-start'>
                    <div className='space-y-3 rounded-lg border p-3'>
                      <h4 className='text-sm font-medium'>
                        {t('qy_lot_a_commit_title')}
                      </h4>
                      {/* 三个哈希 + 名单哈希是这一页最重要的四行：它们一旦
                          写入就只读，事后任何改动都会让验证脚本算出不一样的值。
                          **种子不在这里**——前端从头到尾没有拿到它的通道。 */}
                      <QyKeyValue label={t('qy_lot_commit_hash')}>
                        <span className='font-mono text-xs'>
                          {activity.commit_hash || QY_EMPTY_TEXT}
                        </span>
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_rules_hash')}>
                        <span className='font-mono text-xs'>
                          {activity.rules_hash || QY_EMPTY_TEXT}
                        </span>
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_spec_hash')}>
                        <span className='font-mono text-xs'>
                          {activity.spec_hash || QY_EMPTY_TEXT}
                        </span>
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_roster_hash')}>
                        <span className='font-mono text-xs'>
                          {activity.roster_hash || QY_EMPTY_TEXT}
                        </span>
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_chain_head')}>
                        <span className='font-mono text-xs'>
                          {activity.chain_head || QY_EMPTY_TEXT}
                        </span>
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_algo')}>
                        {activity.algo || QY_EMPTY_TEXT}
                      </QyKeyValue>
                    </div>

                    <div className='space-y-3 rounded-lg border p-3'>
                      <h4 className='text-sm font-medium'>
                        {t('qy_lot_a_timeline_title')}
                      </h4>
                      <QyKeyValue label={t('qy_common_created_at')}>
                        {formatQyTs(activity.created_at)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_published_at')}>
                        {activity.published_at === 0
                          ? QY_EMPTY_TEXT
                          : formatQyTs(activity.published_at)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_open_at')}>
                        {formatQyTs(activity.open_at)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_close_at')}>
                        {formatQyTs(activity.close_at)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_draw_at')}>
                        {formatQyTs(activity.draw_at)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_revealed_at')}>
                        {activity.revealed_at === 0
                          ? QY_EMPTY_TEXT
                          : formatQyTs(activity.revealed_at)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_settle_deadline')}>
                        {activity.settle_deadline === 0
                          ? QY_EMPTY_TEXT
                          : formatQyTs(activity.settle_deadline)}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_a_counts')}>
                        {t('qy_lot_a_counts_value', {
                          active: activity.active_count,
                          pending: activity.pending_count,
                          seq: activity.entry_seq,
                        })}
                      </QyKeyValue>
                      <QyKeyValue label={t('qy_lot_a_stat_fee')}>
                        <QyAmountText quota={activity.platform_fee_quota} />
                      </QyKeyValue>
                      {activity.cancel_reason !== '' && (
                        <QyKeyValue label={t('qy_lot_cancel_reason')}>
                          {activity.cancel_reason}
                        </QyKeyValue>
                      )}
                    </div>

                    <div className='space-y-3 rounded-lg border p-3'>
                      <h4 className='text-sm font-medium'>
                        {activity.kind === 'draw'
                          ? t('qy_lot_prizes_title')
                          : t('qy_lot_options_title')}
                      </h4>
                      <QyLotSpecTable
                        kind={activity.kind}
                        spec={spec}
                        winOptNo={winOptNo}
                      />
                      {activity.result_evidence !== '' && (
                        <QyKeyValue label={t('qy_lot_result_evidence')}>
                          <span className='break-all'>
                            {activity.result_evidence}
                          </span>
                        </QyKeyValue>
                      )}
                    </div>

                    <div className='space-y-3 rounded-lg border p-3'>
                      <h4 className='text-sm font-medium'>
                        {t('qy_lot_rules_title')}
                      </h4>
                      <QyLotRulesList rulesText={activity.rules_text} />
                    </div>
                  </div>
                </TabsContent>

                <TabsContent value='entries'>
                  <QyLotEntriesTab actNo={actNo} />
                </TabsContent>
                <TabsContent value='payouts'>
                  <QyLotPayoutsTab actNo={actNo} />
                </TabsContent>
                {hasTextPrize && (
                  <TabsContent value='fulfill'>
                    <QyLotFulfillQueueTab actNo={actNo} />
                  </TabsContent>
                )}
                <TabsContent value='events'>
                  <QyLotEventsTab actNo={actNo} />
                </TabsContent>
              </Tabs>
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>

      {activity != null && (
        <>
          <QyLotPublishDialog
            activity={activity}
            open={publishOpen}
            onOpenChange={setPublishOpen}
          />
          <QyLotCancelDialog
            activity={activity}
            open={cancelOpen}
            onOpenChange={setCancelOpen}
          />
          <QyLotGuessResultDialog
            activity={activity}
            options={view?.options ?? []}
            open={resultOpen}
            onOpenChange={setResultOpen}
          />
        </>
      )}
    </QySectionPageLayout>
  )
}
