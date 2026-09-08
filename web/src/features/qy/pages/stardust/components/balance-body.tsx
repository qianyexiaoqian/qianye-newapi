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
import { Link } from '@tanstack/react-router'
import type { TFunction } from 'i18next'
import { Hourglass, ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

import { QyRollingNumber } from '../../../components/art/qy-rolling-number'
import { QyAmountText } from '../../../components/qy-amount-text'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { QyStatusBadge } from '../../../components/qy-status-badge'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyTabTarget } from '../../../lib/pages'
import { QyStatGrid, type QyStatItem } from '../../components/qy-stat-grid'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyStardustMeQuery } from '../api'
import {
  qySdAccrualBadge,
  qySdBpsPercent,
  qySdFormatDay,
  qySdHoldReasonKey,
} from '../lib/display'
import type { QyStardustMe, QyStardustYesterday } from '../types'
import { QyStardustForecastCard } from './forecast-card'

/**
 * 「余额」—— 星屑选择夹的第一张标签。
 *
 * 一屏要回答用户最常问的三件事：我现在有多少（大数字）、它们是怎么来怎么去的
 * （四个累计）、以及**为什么昨天用了一整天却没到账**（余数、暂缓状态、昨日摘要）。
 * 第三件事是刻意摆在首屏的：消费返按 decimal 全精度计提、满 1 星屑才入账，
 * 不把 carry 与 held 摆出来，小额用户会一直看到 0 并认为平台吞了钱。
 *
 * 下次结算时刻**只用后端下发的 `next_settle_at`**：日界与结算延迟都是服务端配置，
 * 前端没有任何能把它算对的输入（D-03）。
 */
export function QyStardustBalanceBody() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const query = useQuery(qyStardustMeQuery())
  const me = query.data

  return (
    <QyPageBoundary query={query}>
      {me != null && (
        <div className='space-y-3'>
          <HoldAlert me={me} />
          <BalanceCard me={me} />
          <QyStatGrid items={statItems(me, t)} />
          {/* 明日在昨日之前：昨天那一桶已成定局，今天这一桶才是用户此刻能
              影响的那一个。它自己取数（服务端一小时一份），所以挂在这里而不是
              塞进 /stardust/me —— 后者每进一次页面都要打，而估算要扫 LOG_DB。 */}
          <QyStardustForecastCard />
          <YesterdayCard yesterday={me.yesterday} unit={unit} />
          {me.pending_held_count > 0 && (
            <Alert>
              <Hourglass />
              <AlertTitle>{t('qy_sd_held_count_title')}</AlertTitle>
              <AlertDescription>
                <span>
                  {t('qy_sd_held_count_desc', { n: me.pending_held_count })}
                </span>
                {/* 跳到隔壁那张标签：走 qyTabTarget 直接落在宿主 + hash，
                    不会先离开宿主页再被重定向弹回来。 */}
                <Link
                  {...qyTabTarget('/qy/stardust-accruals')}
                  className='underline underline-offset-2'
                >
                  {t('qy_nav_stardust_accruals')}
                </Link>
              </AlertDescription>
            </Alert>
          )}
        </div>
      )}
    </QyPageBoundary>
  )
}

/**
 * 余额被扣住时的醒目提示。三种原因各说各的，因为用户能做的下一步完全不同：
 * 账户余额为负 → 先充值把余额补正；账号已停用 / 已注销 → 找客服，充值也没用。
 */
function HoldAlert(props: { me: QyStardustMe }) {
  const { t } = useTranslation()
  const reason = props.me.balance.hold_reason
  const key = qySdHoldReasonKey(reason)
  if (key == null) return null
  return (
    <Alert variant='destructive'>
      <ShieldAlert />
      <AlertTitle>{t('qy_sd_hold_title')}</AlertTitle>
      <AlertDescription>
        <span>{t(key, { reason })}</span>
        <span className='block'>{t('qy_sd_hold_desc')}</span>
      </AlertDescription>
    </Alert>
  )
}

function BalanceCard(props: { me: QyStardustMe }) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const me = props.me

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sd_balance_current', { unit })}</CardTitle>
        <CardDescription>{t('qy_sd_balance_desc', { unit })}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
        {/* 余额变了（转盘中奖、下单、结算到账）就滚一下：屏幕上永远只有真实的
            新值，动效只是旧值滚出去；首次挂载不动、缩减动效下不动。 */}
        <QyRollingNumber
          value={me.balance.available}
          render={(value) => (
            <QySdAmount amount={value} variant='hero' className='text-3xl' />
          )}
        />
        <div>
          {/* 余数是 decimal 字符串，只展示、不运算：它连同 gross 都是后端按
              全精度算出来的，前端一旦 parseFloat 再拼回去就会与账本对不上。 */}
          <QyKeyValue label={t('qy_sd_carry_label')}>
            {t('qy_sd_amount_with_unit', { amount: me.balance.carry, unit })}
          </QyKeyValue>
          <QyKeyValue label={t('qy_sd_next_settle')}>
            {formatQyTs(me.next_settle_at)}
          </QyKeyValue>
          <QyKeyValue label={t('qy_sd_scale')}>
            <span className='inline-flex flex-wrap items-center justify-end gap-1'>
              <span>{t('qy_sd_scale_one', { unit })}</span>
              <QyAmountText quota={me.quota_per_unit} />
            </span>
          </QyKeyValue>
        </div>
        <p className='text-muted-foreground text-xs'>
          {t('qy_sd_carry_hint', { unit })}
        </p>
      </CardContent>
    </Card>
  )
}

/** 四个累计。恒等式 可用 = 累计获得 − 累计支出 + 累计退回 + 累计调整。 */
function statItems(me: QyStardustMe, t: TFunction): QyStatItem[] {
  return [
    {
      key: 'earned',
      label: t('qy_sd_total_earned'),
      value: <QySdAmount amount={me.balance.total_earned} />,
    },
    {
      key: 'spent',
      label: t('qy_sd_total_spent'),
      value: <QySdAmount amount={me.balance.total_spent} />,
    },
    {
      key: 'refunded',
      label: t('qy_sd_total_refunded'),
      value: <QySdAmount amount={me.balance.total_refunded} />,
    },
    {
      key: 'adjusted',
      label: t('qy_sd_total_adjusted'),
      value: <QySdAmount amount={me.balance.total_adjusted} signed />,
      hint: t('qy_sd_total_adjusted_hint'),
    },
  ]
}

/**
 * 昨日摘要：「昨日消费 X 星辉 → 已发 Y 星屑」。
 *
 * 计提基数是**额度**（消费返的分母），所以走 `QyAmountText`；发出去的是星屑，
 * 走 `QySdAmount`。同一张卡上并排两种单位是刻意的：用户要看的正是"花了多少
 * 换回来多少"，两个数各按各的单位印，才不会被读成同一种钱。
 *
 * `settled_amount` 取的是流水行上真正发出去的整数，与 gross 差着 carry 那一截
 * 余数 —— 所以 gross 与已发两行都要摆出来，否则"为什么 7.4 只发了 7"没有答案。
 */
function YesterdayCard(props: {
  yesterday: QyStardustYesterday | null
  unit: string
}) {
  const { t } = useTranslation()
  const y = props.yesterday

  if (y == null) {
    return (
      <Card data-card-hover='false'>
        <CardHeader>
          <CardTitle>{t('qy_sd_yesterday_title')}</CardTitle>
          <CardDescription>{t('qy_sd_yesterday_none')}</CardDescription>
        </CardHeader>
      </Card>
    )
  }

  const badge = qySdAccrualBadge(y.status)
  const holdKey = qySdHoldReasonKey(y.hold_reason)

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sd_yesterday_title')}</CardTitle>
        <CardDescription>
          {t('qy_sd_yesterday_desc', { date: qySdFormatDay(y.bucket_date) })}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <QyKeyValue label={t('qy_sd_yesterday_base')}>
          <QyAmountText quota={y.base_quota} />
        </QyKeyValue>
        <QyKeyValue label={t('qy_sd_yesterday_rate')}>
          {t('qy_sd_percent', { percent: qySdBpsPercent(y.rate_bps) })}
        </QyKeyValue>
        <QyKeyValue label={t('qy_sd_yesterday_gross')}>
          {t('qy_sd_amount_with_unit', { amount: y.gross, unit: props.unit })}
        </QyKeyValue>
        <QyKeyValue label={t('qy_common_status')}>
          <QyStatusBadge
            status={badge.status}
            label={badge.labelKey === '' ? undefined : t(badge.labelKey)}
          />
        </QyKeyValue>
        {y.status === 'settled' && (
          <QyKeyValue label={t('qy_sd_yesterday_settled')}>
            <QySdAmount amount={y.settled_amount} signed />
          </QyKeyValue>
        )}
        {y.status === 'held' && holdKey != null && (
          <QyKeyValue label={t('qy_sd_yesterday_hold')}>
            {t(holdKey, { reason: y.hold_reason })}
          </QyKeyValue>
        )}
        {y.status === 'computed' && (
          <p className='text-muted-foreground mt-2 text-xs'>
            {t('qy_sd_yesterday_pending_note')}
          </p>
        )}
      </CardContent>
    </Card>
  )
}
