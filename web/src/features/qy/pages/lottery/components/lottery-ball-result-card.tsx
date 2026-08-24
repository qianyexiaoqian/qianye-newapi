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
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

import { QyAmountText } from '../../../components/qy-amount-text'
import { formatQyTs } from '../../ops/format'
import { qyLotSeriesQuery } from '../api'
import { qyLotBallHits, qyLotBallSafeParsePick } from '../lib/ball'
import { qyLotTiers, type QyLotActivityDetail } from '../types'
import { QyLotBallNumbers } from './lottery-ball-numbers'
import { QyLotFinePrint } from './lottery-fine-print'

/**
 * 「本期开奖 · 我中了没有」。
 *
 * ## 它解决的那个投诉
 *
 * 项目方原话：「关于双色球，已开奖的抽奖为什么不显示双色球号码？」以及
 * 「双色球我想要实现的就是你买彩票一样，中不中。」
 *
 * 实测复现下来，开奖号其实**是**显示的（详情页奖级表上面、大厅卡片上都有），
 * 缺的是另外半句：**我的号**。改造前这两半从来没有在同一屏出现过 ——
 *
 *   - 活动详情：有开奖号，没有我的号（公开名单那张表对 ball 的「选项」列
 *     恒显示 `-`，因为它渲染的是 `opt_no` 而不是 `pick`）；
 *   - 我的参与：有我的号，没有开奖号（接口 `myEntryView` 不带 `ball_result`）；
 *   - 唯一并排的地方是「为什么是这个结果」弹窗 —— 在另一张标签页的一个图标
 *     按钮后面，而且要先整份拉证据链再用 WebCrypto 复算。
 *
 * 于是"我中了没有"这句话在界面上无处可答。这张卡把两组号钉在同一屏、按彩票的
 * 习惯画成球、命中的高亮，并把结论直接写出来（中了第几档、赔多少；没中就写
 * 「未中奖」）。
 *
 * ## 为什么"没中"必须**写出来**
 *
 * 不显示与"没中"在屏幕上长得一模一样，而这两者对用户是完全不同的两件事
 * （后者是结论，前者是"页面是不是坏了"）。落选者与中奖者走同一段渲染、
 * 拿同一组输入，是这套玩法可信度的一部分。
 *
 * ## 零值口径（先定义再用）
 *
 *   - `ball_result === ''` → 这一期**还没开出号码**。取消 / 流局的场次
 *     reveal 从未执行，这里同样是空串 —— 所以此时一个字都不许写「未中奖」，
 *     写了就是把退款说成输钱。
 *   - `won_kind === ''` → 这张票没有派奖行。它等于"没中"**当且仅当**这一期
 *     真的开过奖（`ball_result !== ''`）且这张票是有效票（`status === 'success'`）。
 *   - `my_tickets` 为空 → 我没参与这一期。不写「尚未参与」那类零信息量的占位。
 */
export function QyLotBallResultCard(props: { activity: QyLotActivityDetail }) {
  const { activity } = props
  const { t } = useTranslation()

  const drawn = qyLotBallSafeParsePick(activity.ball_result ?? '')
  const tickets = activity.my_tickets ?? []
  const tiers = qyLotTiers(activity.spec)
  // 后端把 my_tickets 截到 50 条（api_user.go 的 myTicketsCap），而
  // my_entry_count 是同一个事务视图、同一组过滤条件下的**全量** COUNT。
  // 标题原先直接写 tickets.length，于是买了 60 张票的人会在同一屏上看到
  // 「我的号（50 注）」和「已参与 60 次」两个互相打架的数，而全屏没有一句话
  // 说明列表被截断 —— 第一反应是有 10 张票丢了。数据没丢，界面得说清。
  const totalTickets = Math.max(tickets.length, activity.my_entry_count ?? 0)
  const truncated = totalTickets > tickets.length

  // 「下一期什么时候」按定义不在本期的活动记录里。系列上还在售/待开的那一期
  // 就是它；一期都没有时后端给空对象，那时**如实说没有**，不编一个时间。
  const seriesNo = activity.series_no ?? ''
  const series = useQuery(qyLotSeriesQuery(seriesNo, seriesNo !== ''))
  const current = series.data?.current
  const nextIssueNo = current?.issue_no ?? 0
  // 本期自己还在售时，"下一期"指的就是本期 —— 那句话是废话，不显示。
  const nextIssue =
    nextIssueNo > 0 && current?.act_no !== activity.act_no ? current : null

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_lot_ball_draw_title')}</CardTitle>
      </CardHeader>
      <CardContent className='space-y-3'>
        <div className='rounded-lg border p-3'>
          <p className='text-muted-foreground text-xs'>
            {t('qy_lot_ball_result')}
          </p>
          {drawn == null ? (
            <p className='mt-1 text-sm'>{t('qy_lot_ball_await_draw')}</p>
          ) : (
            <>
              <QyLotBallNumbers
                className='mt-1'
                pick={activity.ball_result ?? ''}
              />
              {/* 规范化串留在球下面：进哈希链的是这份字节，用户拿它去比对
                  证据链。球是给人看的，串是给核对用的，两者缺一不可。 */}
              <p className='text-muted-foreground mt-1 font-mono text-xs break-all tabular-nums'>
                {activity.ball_result}
              </p>
            </>
          )}
          {/* 「这串号怎么来的、能不能自己验」是信任问题而不是决策问题：
              想核对的人点开，不想核对的人不必先读一段话才看到号码。 */}
          <QyLotFinePrint
            className='mt-1'
            label={t('qy_lot_ball_result_verify_label')}
          >
            <p>{t('qy_lot_ball_result_verify_note')}</p>
          </QyLotFinePrint>
        </div>

        {tickets.length > 0 && (
          <div className='space-y-2'>
            <p className='text-muted-foreground text-xs'>
              {t('qy_lot_ball_my_tickets', { count: totalTickets })}
              {truncated && (
                <span className='ml-1'>
                  {t('qy_lot_ball_my_tickets_truncated', {
                    shown: tickets.length,
                  })}
                </span>
              )}
            </p>
            <ul className='space-y-2'>
              {tickets.map((ticket) => {
                const mine = qyLotBallSafeParsePick(ticket.pick)
                const hits =
                  mine == null
                    ? { blues: [], reds: [] }
                    : qyLotBallHits(mine, drawn)
                const won = ticket.won_kind !== ''
                const tierName =
                  tiers.find((row) => row.tier === ticket.won_tier)?.name ?? ''
                return (
                  <li
                    key={ticket.entry_no}
                    className='flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-lg border p-3'
                  >
                    <QyLotBallNumbers pick={ticket.pick} hits={hits} />
                    <span className='text-muted-foreground font-mono text-xs break-all tabular-nums'>
                      {ticket.pick}
                    </span>
                    {drawn != null && (
                      <span className='text-xs tabular-nums'>
                        {t('qy_lot_ball_match_value', {
                          blue: hits.blues.length,
                          red: hits.reds.length,
                        })}
                      </span>
                    )}
                    {won ? (
                      <span className='inline-flex flex-wrap items-center gap-1.5'>
                        <Badge>
                          {t('qy_lot_tier_no', { no: ticket.won_tier })}
                          {tierName === '' ? '' : ` ${tierName}`}
                        </Badge>
                        {/* 文本奖在双色球里被后端拒绝（ball_admin.go），
                            所以这里恒是额度奖，直接写金额。 */}
                        <QyAmountText quota={ticket.won_amount} signed />
                      </span>
                    ) : drawn == null ? (
                      <span className='text-muted-foreground text-xs'>
                        {t('qy_lot_ball_await_draw')}
                      </span>
                    ) : ticket.status === 'success' ? (
                      // 「没中」是结论，必须写出来 —— 不显示与没中在屏幕上
                      // 长得一样，而后者才是这一页要回答的那句话。
                      <Badge variant='outline'>
                        {t('qy_lot_ball_not_won')}
                      </Badge>
                    ) : null}
                  </li>
                )
              })}
            </ul>
          </div>
        )}

        {/* 期次的三件事：这一期几号（徽章上已有）、上一期结转了多少、
            下一期什么时候。后两件此前一个都没有落点 —— 结转额度决定了这一期
            的池子为什么这么大，而"下一期什么时候"是买过彩票的人一定会问的。 */}
        <div className='text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs'>
          <span className='inline-flex items-center gap-1'>
            {t('qy_lot_ball_carry_in')}
            <QyAmountText quota={activity.pool_carry_quota ?? 0} />
          </span>
          <span>
            {nextIssue == null
              ? t('qy_lot_ball_next_none')
              : t('qy_lot_ball_next_issue', {
                  no: nextIssue.issue_no ?? 0,
                  time: formatQyTs(nextIssue.draw_at ?? 0),
                })}
          </span>
        </div>
      </CardContent>
    </Card>
  )
}
