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
import { FerrisWheel, RotateCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'
import { cn } from '@/lib/utils'

import { QyMeterBar } from '../../../components/art/qy-meter-bar'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { QyLotCountdownRing } from '../../lottery/components/lottery-countdown-ring'
import { QyLotCover } from '../../lottery/components/lottery-cover'
import {
  qyLotActivityBadgeStatus,
  qyLotCountdown,
  qyLotOutcomeKey,
} from '../../lottery/lib/display'
import type { QyLotActivityBrief } from '../../lottery/types'
import { qyWheelRealTiers } from '../lib/spin'

/**
 * 转盘页里的一张期次卡。
 *
 * 一屏之内回答四个问题：每转多少、各档还剩几份、现在到哪一步了、我转过没有。
 * 「各档剩余 / 初始」是这张卡与大厅卡片最大的差别 —— 转盘的有效中奖率随库存
 * 下降（design-15 §7.4），那正是用户决定要不要转的那个数，而大厅卡片上那一个
 * `prize_total_quota` 装不下它。派生的「谢谢参与」行不在这里列：它没有库存。
 *
 * 剩余画成条而不只是两个数：`1 / 2` 与 `50 / 100` 读起来一样，条子把比例画出来；
 * 数字仍印在旁边，那是可复制、可读屏的一份。发完的档轨道打斜纹。
 */
export function QyWheelActivityCard(props: {
  activity: QyLotActivityBrief
  nowSeconds: number
}) {
  const { activity } = props
  const { t } = useTranslation()

  const countdown = qyLotCountdown(activity, activity.status, props.nowSeconds)
  const outcomeKey = qyLotOutcomeKey(activity.outcome)
  const tiers = qyWheelRealTiers(activity.tiers)

  return (
    // 与大厅卡片同一条：悬停/聚焦抬 2px、封面推近，纯 transform 不用投影。
    <Card className='qy-fx-lift flex h-full flex-col overflow-hidden pt-0'>
      <div className='relative overflow-hidden'>
        <QyLotCover activity={activity} />
        {/* 顶部压暗，让徽章在任何一张封面上都读得出来（大厅卡片同一条）。 */}
        <span
          aria-hidden='true'
          className='qy-art-scrim pointer-events-none absolute inset-x-0 top-0 h-2/3'
        />
        {/* 玩法与状态压在封面上：卡片正文只留给数字。 */}
        <div className='absolute inset-x-2 top-2 flex flex-wrap items-center justify-between gap-1'>
          <Badge variant='outline' className='bg-background/85 gap-1'>
            <FerrisWheel aria-hidden='true' className='size-3' />
            {t('qy_lot_play_wheel')}
          </Badge>
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
              {t('qy_lot_wheel_stake_label')}
            </span>
            <QySdAmount
              amount={activity.stake_quota}
              variant='hero'
              className='text-xl'
            />
          </span>
          <QyLotCountdownRing countdown={countdown} drawAt={activity.draw_at} />
        </div>
        {tiers.length > 0 && (
          <ul className='space-y-1.5' aria-label={t('qy_lot_wheel_stock')}>
            {tiers.map((tier) => {
              const left = tier.stock_left ?? tier.count
              const stockText = t('qy_lot_wheel_stock_value', {
                left,
                total: tier.count,
              })
              return (
                <li key={tier.tier} className='space-y-0.5'>
                  <div className='flex items-center justify-between gap-2 text-xs'>
                    <span className='text-muted-foreground min-w-0 truncate'>
                      {tier.name}
                    </span>
                    <span
                      className={cn(
                        'shrink-0 tabular-nums',
                        left <= 0 && 'text-muted-foreground'
                      )}
                    >
                      {stockText}
                    </span>
                  </div>
                  <QyMeterBar
                    value={left}
                    max={tier.count}
                    label={`${tier.name} ${stockText}`}
                  />
                </li>
              )
            })}
          </ul>
        )}
        <div className='text-muted-foreground flex items-center justify-between gap-2 text-xs'>
          <span className='inline-flex items-center gap-1'>
            <RotateCw aria-hidden='true' className='size-3.5' />
            {t('qy_lot_wheel_spin_total')}
          </span>
          <span className='text-foreground tabular-nums'>
            {activity.active_count}
          </span>
        </div>
      </CardContent>

      <CardFooter className='flex items-center justify-between gap-2'>
        {activity.my_entry_count > 0 && (
          <span className='text-muted-foreground text-xs'>
            {t('qy_lot_wheel_my_spin_count', {
              count: activity.my_entry_count,
            })}
          </span>
        )}
        {/* 详情仍是 `/qy/lottery/$actNo`：一场转盘就是一场活动，证据链、规则、
            公开名单与其余玩法共用同一张详情页，只是那一页按 draw_mode 换正文。 */}
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
