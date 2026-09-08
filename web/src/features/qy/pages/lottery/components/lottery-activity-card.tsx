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
import { Link } from '@tanstack/react-router'
import { CircleDot, Dices, Target, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'

import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import {
  qyLotActivityBadgeStatus,
  qyLotCountdown,
  qyLotOutcomeKey,
} from '../lib/display'
import type { QyLotActivityBrief } from '../types'
import { QyLotBallNumbers } from './lottery-ball-numbers'
import { QyLotCountdownRing } from './lottery-countdown-ring'
import { QyLotCover } from './lottery-cover'

/**
 * 卡片左上角那个图标。三种玩法必须一眼分得开，而 `draw_mode='ball'` 的 `kind`
 * 也是 `draw` —— 所以双色球在调用处单独判，不进这张表。
 */
const KIND_ICON: Partial<Record<string, typeof Dices>> = {
  draw: Dices,
  guess: Target,
}

/**
 * 大厅里的一张活动卡：封面 + 一行标题 + 奖池数字 + 倒计时环。
 *
 * 一屏之内必须回答四个问题：这是什么（抽奖 / 竞猜 / 双色球）、要花多少、
 * 现在到哪一步了、我参加过没有。少任何一个，用户都得点进去才知道，
 * 而大厅的意义就是不用点进去。
 *
 * ## 形状：先看图与数，再看字
 *
 * 玩法与状态徽章压在封面上，正文只剩三个数：奖池（最大）、参与费、参与数，
 * 外加一个倒计时环。规则与说明一律折进「详情」—— 大厅上它们只是让每张卡都
 * 长一截同样的字；双色球多留一行号池（那是中奖难度，不是说明）。
 *
 * ## 双色球为什么不是"换个徽章"就够了
 *
 * 同一张卡上那个最大的数含义整个变了：普通抽奖是 `pool_quota`（本场收到的
 * 投注额），双色球必须是 `pool_open_quota`（本期真正可派发的池子 = 开局基数 +
 * 本期投注入池部分）。滚存几期之后两者能差出一个数量级，而它正是用户用来决定
 * 要不要参与的那个数。已开出号码的期次把号画成球 —— 那是"中没中"的一半。
 */
export function QyLotActivityCard(props: {
  activity: QyLotActivityBrief
  nowSeconds: number
}) {
  const { activity } = props
  const { t } = useTranslation()

  const countdown = qyLotCountdown(activity, activity.status, props.nowSeconds)
  const outcomeKey = qyLotOutcomeKey(activity.outcome)
  const isBall = activity.draw_mode === 'ball'
  const KindIcon = isBall ? CircleDot : (KIND_ICON[activity.kind] ?? Target)

  return (
    // 悬停/聚焦时整张卡抬 2px、封面轻微推近（`.qy-fx-lift` / `.qy-fx-zoom`，
    // 纯 transform，不用投影 —— design-14 的零投影是硬约束）。大厅是一屏
    // 十几张卡的网格，"这一张可以点进去"此前只能靠边框色的微弱变化表达。
    <Card className='qy-fx-lift flex h-full flex-col overflow-hidden pt-0'>
      {/* 背景图压在卡片最顶上，与卡片同宽、无留白 —— 所以这张 Card 去掉了
          顶部 padding 并开了 overflow-hidden，否则图的直角会戳出圆角边框。
          没配封面时这里画的是兜底图案而不是空白：空白与"还在加载"长得一样。 */}
      <div className='relative overflow-hidden'>
        <QyLotCover activity={activity} />
        {/* 顶部压暗。徽章压在管理员随手配的封面上，底图是亮是暗无从预知 ——
            一张浅色封面会让「进行中」那枚徽章的白底与图糊在一起。徽章自己那层
            `bg-background/85` 只解决了半透明，解决不了图本身的对比度。 */}
        <span
          aria-hidden='true'
          className='qy-art-scrim pointer-events-none absolute inset-x-0 top-0 h-2/3'
        />
        <div className='absolute inset-x-2 top-2 flex flex-wrap items-center justify-between gap-1'>
          <span className='flex flex-wrap items-center gap-1'>
            <Badge variant='outline' className='bg-background/85 gap-1'>
              <KindIcon aria-hidden='true' className='size-3' />
              {isBall
                ? t('qy_lot_mode_ball')
                : t(`qy_lot_kind_${activity.kind}`)}
            </Badge>
            {isBall && (
              <Badge variant='outline' className='bg-background/85'>
                {t('qy_lot_ball_issue_no', { no: activity.issue_no ?? 0 })}
              </Badge>
            )}
          </span>
          {/* 结局揭晓之后，状态与结局是同一件事：颜色与文字合进一枚徽章。
              此前是两枚并排，一场取消的活动上写着「已取消 已取消(全额退款)」。 */}
          <QyStatusBadge
            status={qyLotActivityBadgeStatus(activity.status, activity.outcome)}
            label={outcomeKey == null ? undefined : t(outcomeKey)}
            className='bg-background/85 shrink-0'
          />
        </div>
      </div>
      <CardHeader>
        <h3 className='truncate text-base font-medium' title={activity.title}>
          {activity.title}
        </h3>
      </CardHeader>

      <CardContent className='flex-1 space-y-3 text-sm'>
        <div className='flex items-end justify-between gap-3'>
          <span className='flex min-w-0 flex-col leading-tight'>
            <span className='text-muted-foreground text-[11px]'>
              {isBall ? t('qy_lot_ball_pool_open') : t('qy_lot_pool')}
            </span>
            <QySdAmount
              amount={
                isBall ? (activity.pool_open_quota ?? 0) : activity.pool_quota
              }
              variant='hero'
              className='text-xl'
            />
          </span>
          <QyLotCountdownRing countdown={countdown} drawAt={activity.draw_at} />
        </div>
        <div className='grid grid-cols-2 gap-2 text-xs'>
          <span className='flex min-w-0 flex-col leading-tight'>
            <span className='text-muted-foreground text-[11px]'>
              {t('qy_lot_stake')}
            </span>
            <QySdAmount amount={activity.stake_quota} />
          </span>
          <span className='flex min-w-0 flex-col leading-tight'>
            <span className='text-muted-foreground inline-flex items-center gap-1 text-[11px]'>
              <Users aria-hidden='true' className='size-3' />
              {t('qy_lot_entries_count')}
            </span>
            <span className='tabular-nums'>{activity.active_count}</span>
          </span>
        </div>
        {/* 号池 = 中奖难度：「12 选 3」与「33 选 6」是两种游戏，这一行是双色球卡片
            不能折进详情的那一个数（admin-lottery/__tests__/play-visibility 钉着它）。 */}
        {isBall && (
          <p className='text-muted-foreground text-[11px] tabular-nums'>
            {t('qy_lot_ball_pool_desc', {
              redPick: activity.ball_red_pick ?? 0,
              redPool: activity.ball_red_pool ?? 0,
              bluePick: activity.ball_blue_pick ?? 0,
              bluePool: activity.ball_blue_pool ?? 0,
            })}
          </p>
        )}
        {isBall && (activity.ball_result ?? '') !== '' && (
          <div className='flex flex-col gap-1'>
            <span className='text-muted-foreground text-[11px]'>
              {t('qy_lot_ball_result')}
            </span>
            <QyLotBallNumbers
              size='sm'
              drawn
              pick={activity.ball_result ?? ''}
            />
          </div>
        )}
      </CardContent>

      <CardFooter className='flex items-center justify-between gap-2'>
        {/* "我参加过 N 次"必须在卡片上就能看到：允许多次参与的活动里，
            用户最容易犯的错就是不记得自己已经买过几张而重复下单。

            没参加过时**什么都不写**。「尚未参与」是一句零信息量的话 —— 大厅里
            绝大多数卡片都是这个状态，于是每一张卡上都挂着同一个四字标签，
            真正有内容的那几张（已参与 N 次）反而淹没在里面。 */}
        {activity.my_entry_count > 0 && (
          <span className='text-muted-foreground text-xs'>
            {t('qy_lot_my_entry_count', { count: activity.my_entry_count })}
          </span>
        )}
        <Button
          className='ms-auto'
          size='sm'
          variant='outline'
          render={
            <Link to='/qy/lottery/$actNo' params={{ actNo: activity.act_no }} />
          }
        >
          {t('qy_common_detail')}
        </Button>
      </CardFooter>
    </Card>
  )
}
