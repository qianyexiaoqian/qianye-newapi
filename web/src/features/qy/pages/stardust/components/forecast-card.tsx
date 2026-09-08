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
import { RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

import { QyAmountText } from '../../../components/qy-amount-text'
import { QySdAmount } from '../../../components/qy-sd-amount'
import { useStardustName } from '../../../hooks/use-stardust-name'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { formatQyTs } from '../../ops/format'
import { QyKeyValue } from '../../ops/qy-ops-ui'
import { qyStardustForecastQuery, qyStardustForecastRefresh } from '../api'
import { qySdBpsPercent, qySdHoldReasonKey } from '../lib/display'
import type { QyStardustForecast, QyStardustForecastLine } from '../types'

/**
 * 「明日预计到账」——「余额」标签上昨日摘要旁边的那张卡。
 *
 * # 为什么要有它
 *
 * 消费返是"今天花、明天结"：今天一整天的消费要到下一个日界 + 结算延迟才落账。
 * 在此之前用户端唯一能看到的数字是**昨日摘要**，于是每一个白天盯着的都是一个
 * 不会动的数 —— 「我今天花了这么多，到底会返多少」在界面上没有任何答案。
 *
 * # 它是估算，不是账
 *
 * 口径与日结逐字相同（同一批排除项、同一条截断、同一次 floor），但今天还没过完，
 * 分组、比例、账号状态在结算那一刻都还可能变。所以卡片上必须同时写着**数据截至
 * 哪一刻**与**什么时候结算**，绝不能让它看起来像余额那样是实时的。
 *
 * # 一小时一份 + 手动刷新
 *
 * 缓存与节流都在服务端（估算要扫 LOG_DB）：一小时自动重算一份，用户可以按刷新
 * 请它提前重算，但两次真正的重算之间有最小间隔。按钮据 `refresh_after` 按住 ——
 * 不禁用的话用户会以为"按了没反应"，而后端其实是原样退回了同一份。
 */
export function QyStardustForecastCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyStardustForecastQuery())
  const forecast = query.data

  const refresh = useMutation({
    mutationFn: qyStardustForecastRefresh,
    onSuccess: (next) =>
      queryClient.setQueryData(qyKeys.stardustForecast(), next),
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  // 节流窗口内后端会原样退回旧的那一份，按钮此时按下去只会让人以为坏了。
  // 时刻取渲染这一帧的钟：`refresh_after` 一过，下一次重渲染（刷新落地、
  // 切标签回来）按钮就自己活过来，不为一颗按钮挂一个每秒跑的定时器。
  const throttled =
    forecast != null && Date.now() / 1000 < forecast.refresh_after
  const busy = query.isFetching || refresh.isPending

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_sd_fc_title')}</CardTitle>
        <CardDescription>
          {forecast == null
            ? t('qy_sd_fc_desc_unknown')
            : t('qy_sd_fc_desc', { time: formatQyTs(forecast.settle_at) })}
        </CardDescription>
        <CardAction>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => refresh.mutate()}
            disabled={busy || throttled}
          >
            <RefreshCw
              aria-hidden='true'
              className={busy ? 'animate-spin' : undefined}
            />
            {t('qy_common_refresh')}
          </Button>
        </CardAction>
      </CardHeader>
      <CardContent className='space-y-3'>
        {query.isPending && <Skeleton className='h-24 w-full' />}
        {query.isError && (
          <p className='text-muted-foreground text-sm'>
            {qyErrorMessage(query.error, t)}
          </p>
        )}
        {forecast != null && <ForecastBody forecast={forecast} />}
      </CardContent>
    </Card>
  )
}

function ForecastBody(props: { forecast: QyStardustForecast }) {
  const { t } = useTranslation()
  const f = props.forecast
  const holdKey = qySdHoldReasonKey(f.hold_reason)

  return (
    <>
      <QySdAmount
        amount={f.estimated_total}
        signed
        variant='hero'
        className='text-3xl'
      />
      {holdKey != null && (
        <p className='text-destructive text-sm'>
          {t('qy_sd_fc_hold', {
            reason: t(holdKey, { reason: f.hold_reason }),
          })}
        </p>
      )}
      <ForecastLine
        label={t('qy_sd_kind_consume_rebate')}
        baseLabel={t('qy_sd_fc_base_mine')}
        line={f.consume}
      />
      {/* 下线消费返只在它对我成立时出现：功能关着、或我这一档比例是 0 时，
          摆一行永远为 0 的数只会让人以为是坏了。 */}
      {f.invite.applies && (
        <ForecastLine
          label={t('qy_sd_kind_invite_consume')}
          baseLabel={t('qy_sd_fc_base_invitees')}
          line={f.invite}
          note={f.invite.counted ? undefined : t('qy_sd_fc_invitees_too_many')}
        />
      )}
      <p className='text-muted-foreground text-xs'>
        {t('qy_sd_fc_as_of', { time: formatQyTs(f.computed_at) })}
      </p>
    </>
  )
}

/**
 * 一条获得线：四个中间量 + 这一条预计发出去的整数。
 *
 * 四个中间量全部摆出来，理由与昨日摘要那张卡相同：只印一个 0 的话，「我明明消费
 * 了一整天」没有答案 —— 答案通常是计提的零头还没攒够 1 颗。基数是**额度**口径
 * （消费返的分母，走 `QyAmountText`），计提与余数是全精度 decimal 字符串，
 * 只展示、不运算。
 */
function ForecastLine(props: {
  label: string
  baseLabel: string
  line: QyStardustForecastLine
  note?: string
}) {
  const { t } = useTranslation()
  const unit = useStardustName()

  return (
    <div>
      <QyKeyValue label={props.baseLabel}>
        <QyAmountText quota={props.line.base_quota} />
      </QyKeyValue>
      <QyKeyValue label={t('qy_sd_yesterday_rate')}>
        {t('qy_sd_percent', { percent: qySdBpsPercent(props.line.rate_bps) })}
      </QyKeyValue>
      <QyKeyValue label={t('qy_sd_yesterday_gross')}>
        {t('qy_sd_amount_with_unit', { amount: props.line.gross, unit })}
      </QyKeyValue>
      <QyKeyValue label={t('qy_sd_carry_label')}>
        {t('qy_sd_amount_with_unit', { amount: props.line.carry, unit })}
      </QyKeyValue>
      <QyKeyValue label={t('qy_sd_fc_line_estimated', { line: props.label })}>
        <QySdAmount amount={props.line.estimated} signed />
      </QyKeyValue>
      {props.note != null && (
        <p className='text-muted-foreground pt-1 text-xs'>{props.note}</p>
      )}
    </div>
  )
}
