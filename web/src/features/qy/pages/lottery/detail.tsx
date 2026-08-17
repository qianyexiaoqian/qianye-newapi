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
import { ArrowLeft } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { QyStatusBadge } from '../../components/qy-status-badge'
import { formatQyQuotaLedger } from '../../lib/format'
import { qyTabTarget } from '../../lib/pages'
import { QyStatGrid } from '../components/qy-stat-grid'
import { QY_EMPTY_TEXT, formatQyDuration, formatQyTs } from '../ops/format'
import { QyKeyValue } from '../ops/qy-ops-ui'
import { qyLotActivityQuery, qyLotEligibilityQuery } from './api'
import { QyLotCover } from './components/lottery-cover'
import { QyLotEligibilityCard } from './components/lottery-eligibility-card'
import { QyLotEntryDialog } from './components/lottery-entry-dialog'
import { QyLotFairnessPanel } from './components/lottery-fairness-panel'
import { QyLotRosterCard } from './components/lottery-roster-card'
import { QyLotRulesList } from './components/lottery-rules-list'
import { QyLotSpecTable } from './components/lottery-spec-table'
import { qyLotBallPoolOf } from './lib/ball'
import {
  qyLotActivityBadgeStatus,
  qyLotCountdown,
  qyLotOutcomeKey,
} from './lib/display'
import { useQyNowSeconds } from './lib/use-now'
import { isQyLotOpen } from './types'

/**
 * 活动详情。
 *
 * 一屏之内要同时承担两件事：**让人敢参加**（条件说清、代价说清）与
 * **让人能复核**（承诺、冻结、揭示、结算四步的证据）。第二件事不是附赠 ——
 * 项目方要的「历史公正查询」落点就在这里，已结束的活动打开后看到的仍是完整
 * 的证据链，而不是一句"活动已结束"。
 */
export function QyLotteryDetail() {
  const { t } = useTranslation()
  const { actNo } = useParams({ from: '/_authenticated/qy/lottery/$actNo/' })
  const now = useQyNowSeconds()
  const [joinOpen, setJoinOpen] = useState(false)

  const query = useQuery(qyLotActivityQuery(actNo))
  const activity = query.data

  const open = activity != null && isQyLotOpen(activity, now)
  const eligibility = useQuery(qyLotEligibilityQuery(actNo, open))

  const countdown =
    activity == null ? null : qyLotCountdown(activity, activity.status, now)
  const outcomeKey = activity == null ? null : qyLotOutcomeKey(activity.outcome)
  const isBall = activity?.draw_mode === 'ball'

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {activity?.title ?? t('qy_nav_lottery')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        {/*
          回程要带上标签的 hash，否则竞猜用户永远落回第一张标签（抽奖）——
          他刚才看的那一场不在那张列表里（kind='draw' 是写死的），
          得再点一次「竞猜」并重新翻页。
        */}
        <Button
          size='sm'
          variant='outline'
          render={
            <Link
              {...qyTabTarget(
                activity?.kind === 'guess' ? '/qy/lottery-guess' : '/qy/lottery'
              )}
            />
          }
        >
          <ArrowLeft aria-hidden='true' />
          {t('qy_lot_back_to_hall')}
        </Button>
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {activity != null && (
            <div className='space-y-4'>
              {/*
                头图。大厅卡片上是什么图，点进来就还是什么图 —— 两处不一致会让
                用户以为自己点错了。没配封面时这里落在同一个兜底上（玩法图标 +
                渐变），而不是塌成一条空白：空白与"还在加载"长得一样。
              */}
              <QyLotCover activity={activity} variant='hero' />

              <div className='flex flex-wrap items-center gap-2'>
                <Badge variant='outline'>
                  {isBall
                    ? t('qy_lot_mode_ball')
                    : t(`qy_lot_kind_${activity.kind}`)}
                </Badge>
                {isBall && (
                  <Badge variant='outline'>
                    {t('qy_lot_ball_issue_no', { no: activity.issue_no ?? 0 })}
                  </Badge>
                )}
                <QyStatusBadge
                  status={qyLotActivityBadgeStatus(
                    activity.status,
                    activity.outcome
                  )}
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
                    key: 'stake',
                    label: t('qy_lot_stake'),
                    value: formatQyQuotaLedger(activity.stake_quota),
                  },
                  {
                    // 双色球的「奖池」是本期真正可派发的那一份（开局基数 +
                    // 本期投注入池部分），不是本期收到的投注额。两者在滚存了
                    // 几期之后可以差出一个数量级，而这正是用户用来决定要不要
                    // 参与的那个数。
                    key: 'pool',
                    label: isBall
                      ? t('qy_lot_ball_pool_open')
                      : t('qy_lot_pool'),
                    value: formatQyQuotaLedger(
                      isBall
                        ? (activity.pool_open_quota ?? 0)
                        : activity.pool_quota
                    ),
                    emphasis: true,
                  },
                  {
                    key: 'entries',
                    label: t('qy_lot_entries_count'),
                    value: activity.active_count,
                    hint:
                      activity.min_entries_to_hold > 0
                        ? t('qy_lot_min_entries_hint', {
                            count: activity.min_entries_to_hold,
                          })
                        : undefined,
                  },
                  {
                    key: 'countdown',
                    label:
                      countdown == null
                        ? t('qy_lot_draw_at')
                        : t(countdown.labelKey),
                    value:
                      countdown == null
                        ? formatQyTs(activity.draw_at)
                        : formatQyDuration(countdown.seconds),
                  },
                ]}
              />

              <div className='grid gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)] lg:items-start'>
                <div className='space-y-4'>
                  {activity.intro.trim() !== '' && (
                    <Card data-card-hover='false'>
                      <CardHeader>
                        <CardTitle>{t('qy_lot_intro_title')}</CardTitle>
                      </CardHeader>
                      <CardContent>
                        {/* 纯文本渲染，不解析 HTML/Markdown：活动简介是管理端
                            自由输入的字段，当富文本渲染就是一个存储型 XSS 面。 */}
                        <p className='text-sm break-words whitespace-pre-wrap'>
                          {activity.intro}
                        </p>
                      </CardContent>
                    </Card>
                  )}

                  <Card data-card-hover='false'>
                    <CardHeader>
                      <CardTitle>
                        {activity.kind === 'draw'
                          ? t('qy_lot_prizes_title')
                          : t('qy_lot_options_title')}
                      </CardTitle>
                      {activity.kind === 'guess' && (
                        <CardDescription>
                          {t('qy_lot_fee_desc', {
                            percent: (activity.fee_bps / 100).toFixed(2),
                          })}
                        </CardDescription>
                      )}
                      {isBall && (
                        <CardDescription>
                          {t('qy_lot_ball_pool_desc', {
                            redPick: activity.ball_red_pick ?? 0,
                            redPool: activity.ball_red_pool ?? 0,
                            bluePick: activity.ball_blue_pick ?? 0,
                            bluePool: activity.ball_blue_pool ?? 0,
                          })}
                        </CardDescription>
                      )}
                    </CardHeader>
                    <CardContent className='space-y-3'>
                      {/* 开奖号必须与奖级表同屏：用户点进来的第一件事是"开的是
                          哪几个号"，让他去「我的参与」里点弹窗才看得到，等于把
                          结果藏起来。这一串来自后端的 ball_result，而它可由
                          why-result 弹窗用公开种子当场复算出同一组号。 */}
                      {isBall && (activity.ball_result ?? '') !== '' && (
                        <div className='rounded-lg border p-3'>
                          <p className='text-muted-foreground text-xs'>
                            {t('qy_lot_ball_result')}
                          </p>
                          <p className='mt-1 font-mono text-lg break-all tabular-nums'>
                            {activity.ball_result}
                          </p>
                          <p className='text-muted-foreground mt-1 text-xs'>
                            {t('qy_lot_ball_result_verify_note')}
                          </p>
                        </div>
                      )}
                      <QyLotSpecTable
                        kind={activity.kind}
                        spec={activity.spec}
                        winOptNo={activity.win_opt_no}
                        ballPool={
                          isBall ? qyLotBallPoolOf(activity) : undefined
                        }
                        poolOpenQuota={activity.pool_open_quota ?? 0}
                      />
                      {isBall && (
                        // 概率是本地算的这件事必须写出来。否则用户会默认它和别的
                        // 数字一样是平台报的，而"这个数不需要相信平台"正是双色球
                        // 唯一但决定性的优势。
                        <p className='text-muted-foreground text-xs'>
                          {t('qy_lot_ball_odds_local_note')}
                        </p>
                      )}
                      {isBall && (activity.series_no ?? '') !== '' && (
                        <QyKeyValue label={t('qy_lot_ball_series_no')}>
                          <span className='font-mono text-xs break-all'>
                            {activity.series_no}
                          </span>
                        </QyKeyValue>
                      )}
                      {activity.kind === 'guess' && (
                        // 「全部猜错怎么办」必须在下注之前就写清楚，否则事后
                        // 无论怎么处理都会被指控临时改规则。
                        <p className='text-muted-foreground text-xs'>
                          {t('qy_lot_no_winner_note')}
                        </p>
                      )}
                      {/* 结果依据只有竞猜录了结果之后才有。用 ?? '' 兜底而不是
                          直接 .trim()：老版本后端不下发这个字段，一次 undefined
                          就会让整个详情页崩在渲染里。 */}
                      {(activity.result_evidence ?? '').trim() !== '' && (
                        <QyKeyValue label={t('qy_lot_result_evidence')}>
                          <span className='break-all'>
                            {activity.result_evidence}
                          </span>
                        </QyKeyValue>
                      )}
                    </CardContent>
                  </Card>

                  <Card data-card-hover='false'>
                    <CardHeader>
                      <CardTitle>{t('qy_lot_rules_title')}</CardTitle>
                      <CardDescription>
                        {t('qy_lot_rules_frozen_desc')}
                      </CardDescription>
                    </CardHeader>
                    <CardContent className='space-y-3'>
                      <QyLotRulesList rulesText={activity.rules_text} />
                      <div>
                        <QyKeyValue label={t('qy_lot_max_per_user')}>
                          {activity.max_entries_per_user === 0
                            ? t('qy_common_unlimited')
                            : activity.max_entries_per_user}
                        </QyKeyValue>
                        <QyKeyValue label={t('qy_lot_cooldown')}>
                          {activity.cooldown_seconds === 0
                            ? t('qy_common_none')
                            : formatQyDuration(activity.cooldown_seconds)}
                        </QyKeyValue>
                        <QyKeyValue label={t('qy_lot_dedup_ip')}>
                          {activity.dedup_ip
                            ? t('qy_common_on')
                            : t('qy_common_off')}
                        </QyKeyValue>
                        <QyKeyValue label={t('qy_lot_allow_multi_win')}>
                          {activity.allow_multi_win
                            ? t('qy_common_on')
                            : t('qy_common_off')}
                        </QyKeyValue>
                        <QyKeyValue label={t('qy_lot_settle_deadline')}>
                          {activity.settle_deadline === 0
                            ? QY_EMPTY_TEXT
                            : formatQyTs(activity.settle_deadline)}
                        </QyKeyValue>
                      </div>
                      {activity.dedup_ip && (
                        // 去重的代价必须对用户明说：家庭/公司共用出口时会误伤，
                        // 而被误伤的人完全无法自证。
                        <p className='text-muted-foreground text-xs'>
                          {t('qy_lot_dedup_ip_note')}
                        </p>
                      )}
                    </CardContent>
                  </Card>

                  <div className='space-y-3'>
                    {open && (
                      <QyLotEligibilityCard
                        eligibility={eligibility.data}
                        isLoading={eligibility.isLoading}
                      />
                    )}
                    <Button
                      // 按钮亮不亮只是"别让用户白按一次"。真正说了算的是后端在
                      // 活动行锁与主库行锁里的那两次判定 —— 这里放行不代表能报名。
                      disabled={!open}
                      onClick={() => setJoinOpen(true)}
                    >
                      {open
                        ? t('qy_lot_join_title')
                        : t('qy_lot_join_unavailable')}
                    </Button>
                    {activity.my_entry_count > 0 && (
                      <p className='text-muted-foreground text-xs'>
                        {t('qy_lot_my_entry_count', {
                          count: activity.my_entry_count,
                        })}
                      </p>
                    )}
                  </div>
                </div>

                <div className='space-y-4'>
                  <QyLotFairnessPanel activity={activity} />
                  <QyLotRosterCard activity={activity} />
                </div>
              </div>
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>

      {activity != null && (
        <QyLotEntryDialog
          activity={activity}
          open={joinOpen}
          onOpenChange={setJoinOpen}
        />
      )}
    </QySectionPageLayout>
  )
}
