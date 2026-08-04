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
import { Dices, Target, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { formatQyDuration, formatQyTs } from '../../ops/format'
import {
  qyLotActivityBadgeStatus,
  qyLotCountdown,
  qyLotOutcomeKey,
} from '../lib/display'
import type { QyLotActivityBrief } from '../types'

/**
 * 大厅里的一张活动卡。
 *
 * 一屏之内必须回答四个问题：这是什么（抽奖还是竞猜）、要花多少、现在到哪一步了、
 * 我参加过没有。少任何一个，用户都得点进去才知道，而大厅的意义就是不用点进去。
 */
export function QyLotActivityCard(props: {
  activity: QyLotActivityBrief
  nowSeconds: number
}) {
  const { activity } = props
  const { t } = useTranslation()

  const countdown = qyLotCountdown(activity, activity.status, props.nowSeconds)
  const outcomeKey = qyLotOutcomeKey(activity.outcome)

  return (
    <Card className='flex h-full flex-col'>
      <CardHeader className='space-y-2'>
        <div className='flex flex-wrap items-center gap-2'>
          <Badge variant='outline' className='gap-1'>
            {activity.kind === 'draw' ? (
              <Dices aria-hidden='true' className='size-3' />
            ) : (
              <Target aria-hidden='true' className='size-3' />
            )}
            {t(`qy_lot_kind_${activity.kind}`)}
          </Badge>
          <QyStatusBadge
            status={qyLotActivityBadgeStatus(activity.status)}
            className='shrink-0'
          />
          {outcomeKey != null && (
            <Badge variant='secondary'>{t(outcomeKey)}</Badge>
          )}
        </div>
        <h3 className='text-base font-medium break-words'>{activity.title}</h3>
      </CardHeader>

      <CardContent className='flex-1 space-y-2 text-sm'>
        <div className='flex items-center justify-between gap-2'>
          <span className='text-muted-foreground'>{t('qy_lot_stake')}</span>
          <QyAmountText quota={activity.stake_quota} />
        </div>
        <div className='flex items-center justify-between gap-2'>
          <span className='text-muted-foreground'>{t('qy_lot_pool')}</span>
          <QyAmountText quota={activity.pool_quota} variant='hero' />
        </div>
        <div className='flex items-center justify-between gap-2'>
          <span className='text-muted-foreground inline-flex items-center gap-1'>
            <Users aria-hidden='true' className='size-3.5' />
            {t('qy_lot_entries_count')}
          </span>
          <span className='tabular-nums'>{activity.active_count}</span>
        </div>
        <div className='flex items-center justify-between gap-2'>
          <span className='text-muted-foreground'>
            {countdown == null ? t('qy_lot_draw_at') : t(countdown.labelKey)}
          </span>
          <span className='tabular-nums'>
            {countdown == null
              ? formatQyTs(activity.draw_at)
              : formatQyDuration(countdown.seconds)}
          </span>
        </div>
      </CardContent>

      <CardFooter className='flex items-center justify-between gap-2'>
        {/* "我参加过 N 次"必须在卡片上就能看到：允许多次参与的活动里，
            用户最容易犯的错就是不记得自己已经买过几张而重复下单。 */}
        <span className='text-muted-foreground text-xs'>
          {activity.my_entry_count > 0
            ? t('qy_lot_my_entry_count', { count: activity.my_entry_count })
            : t('qy_lot_not_joined')}
        </span>
        <Button
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
