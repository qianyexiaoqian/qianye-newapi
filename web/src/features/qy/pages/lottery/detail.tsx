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
import { QyLotBallResultCard } from './components/lottery-ball-result-card'
import { QyLotCover } from './components/lottery-cover'
import { QyLotEligibilityCard } from './components/lottery-eligibility-card'
import { QyLotEntryDialog } from './components/lottery-entry-dialog'
import { QyLotFairnessPanel } from './components/lottery-fairness-panel'
import { QyLotFinePrint } from './components/lottery-fine-print'
import { QyLotGuessBoard } from './components/lottery-guess-board'
import { QyLotRosterCard } from './components/lottery-roster-card'
import { QyLotRulesList } from './components/lottery-rules-list'
import { QyLotSpecTable } from './components/lottery-spec-table'
import { qyLotBallPoolOf } from './lib/ball'
import {
  qyLotActivityBadgeStatus,
  qyLotCountdown,
  qyLotOutcomeKey,
} from './lib/display'
import { hasQyLotGuessBoard } from './lib/guess'
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

  // 玩法被运营隐藏时不再受理新参与。老后端不下发这个字段，按可参与处理 ——
  // 真正说了算的仍然是后端在活动行锁与主库行锁里的那两次判定。
  const playOpen = activity?.play_open !== false
  const open = activity != null && isQyLotOpen(activity, now) && playOpen
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
          回程要带上标签的 hash，否则竞猜/双色球用户永远落回第一张标签
          （抽奖）—— 他刚才看的那一场不在那张列表里（那张标签发的是
          `lane='draw'`，它排除双色球），得再点一次并重新翻页。

          判据与后端的 `hallLanes` 是同一套：竞猜看 `kind`，双色球看
          `draw_mode`，其余归抽奖。三张夹恰好把活动分完，所以这里不需要
          "找不到就回落"的第四支。
        */}
        <Button
          size='sm'
          variant='outline'
          render={
            <Link
              {...qyTabTarget(
                activity?.kind === 'guess'
                  ? '/qy/lottery-guess'
                  : activity?.draw_mode === 'ball'
                    ? '/qy/lottery-ball'
                    : '/qy/lottery'
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
                {/* 结局揭晓之后，状态与结局是同一件事 —— 颜色与文字合进一枚
                    徽章，见 `QyStatusBadge` 的 `label`。 */}
                <QyStatusBadge
                  status={qyLotActivityBadgeStatus(
                    activity.status,
                    activity.outcome
                  )}
                  label={outcomeKey == null ? undefined : t(outcomeKey)}
                />
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

              {/*
                「本期开奖 · 我中了没有」排在两列网格**之前**，也就是统计格
                下面的第一块。理由是项目方那句原话：「双色球我想要实现的就是你
                买彩票一样，中不中。」——"中不中"是这一页要回答的第一个问题，
                把它放进左列意味着它排在活动说明与奖级表后面，窄屏上要滚过
                一屏半才看得到。

                它在**已结束**状态下同样渲染（这一整块不看 status），开奖号、
                我的号、命中情况因此不会随着活动结束而消失。
              */}
              {isBall && <QyLotBallResultCard activity={activity} />}

              {/*
                两列都要 `min-w-0`。窄屏下这张网格塌成一列，而一列网格的轨道是
                **auto** —— 轨道里的项默认 `min-width: auto`，于是奖级表那 5 列
                （档位 / 命中要求 / 奖金形态 / 预算份数 / 中奖概率）把整列撑到
                660px 宽，`StaticDataTable` 自己那层 `overflow-x-auto` 因为父级
                没有宽度约束而永远不触发。表现是 375px 下整块内容区跟着横向滚，
                标题、状态、按钮全都要左右拖着看。实测 375px：加这两个类之前
                列宽 692px，之后 327px，表格改在自己那层里滚。
              */}
              <div className='grid gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)] lg:items-start'>
                <div className='min-w-0 space-y-4'>
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
                        // 一句话把彩池的三件要害说完：钱从哪来、平台抽多少、
                        // 什么情况下原样退回。改造前这里是两段分开的话
                        // （手续费一段、全对/全错一段），加起来 48 个字却
                        // **始终没有说出**「奖池 = 全部押注之和」——
                        // 而那正是"赢家的钱来自输家"的全部内容。
                        <CardDescription>
                          {t('qy_lot_guess_pool_desc', {
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
                      {/*
                        竞猜走盘口而不是表格。三列平铺的事实（选项 / 投注额 /
                        人次）不解释任何事；分布条与实时赔率会自己讲清"钱从
                        押错的人那里来、押的人越多每份越少"。
                        拿不到 `bet_quota` 时（证据链端点不下发它）回落到原来
                        那张表 —— 那时整块盘口都是未知数，画一排 0% 的条子是
                        一个**错的**数，比没有数更糟。
                      */}
                      {activity.kind === 'guess' &&
                      hasQyLotGuessBoard(activity.spec) ? (
                        <QyLotGuessBoard
                          spec={activity.spec}
                          poolQuota={activity.pool_quota}
                          feeBps={activity.fee_bps}
                          stakeQuota={activity.stake_quota}
                          winOptNo={activity.win_opt_no}
                          /*
                            结果一旦公布，盘口就必须从「押中约得」切到
                            「已按此赔付 / 未中 / 原样退回」。判据取
                            settling|finished 而不是 win_opt_no > 0：流局与
                            取消那几种结局根本没有获胜选项，但它们同样已经
                            有了结果（全额退回），此时再显示前瞻赔率一样是
                            在写一个从未发生过的数。
                          */
                          resultAnnounced={
                            activity.status === 'settling' ||
                            activity.status === 'finished'
                          }
                        />
                      ) : (
                        <QyLotSpecTable
                          kind={activity.kind}
                          spec={activity.spec}
                          winOptNo={activity.win_opt_no}
                          ballPool={
                            isBall ? qyLotBallPoolOf(activity) : undefined
                          }
                          poolOpenQuota={activity.pool_open_quota ?? 0}
                        />
                      )}
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
                        // 而被误伤的人完全无法自证。上面那一行「同 IP 只算一人
                        // 已开启」已经把事实摆出来了，这段是它的后果说明 ——
                        // 只有真的共用出口的人才需要读，所以折起来。
                        <QyLotFinePrint label={t('qy_lot_dedup_ip_label')}>
                          <p>{t('qy_lot_dedup_ip_note')}</p>
                        </QyLotFinePrint>
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
                    {!playOpen && (
                      // 置灰的按钮必须带上原因，而且要说清"已参与的不受影响"——
                      // 用户看到自己参加过的那一场突然不能再买，第一反应是
                      // 自己那笔钱出事了。
                      <p className='text-muted-foreground text-xs'>
                        {t('qy_lot_play_hidden_note')}
                      </p>
                    )}
                    {activity.my_entry_count > 0 && (
                      <p className='text-muted-foreground text-xs'>
                        {t('qy_lot_my_entry_count', {
                          count: activity.my_entry_count,
                        })}
                      </p>
                    )}
                  </div>
                </div>

                <div className='min-w-0 space-y-4'>
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
