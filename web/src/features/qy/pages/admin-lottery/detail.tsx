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
              <div className='flex flex-wrap items-center gap-2'>
                <Badge variant='outline'>
                  {t(`qy_lot_kind_${activity.kind}`)}
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
