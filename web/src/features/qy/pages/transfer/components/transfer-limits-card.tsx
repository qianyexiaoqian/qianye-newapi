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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../../components/qy-amount-text'
import type { QyTransferGroupPolicy, QyTransferLimits } from '../types'

type TransferLimitsCardProps = {
  limits: QyTransferLimits
}

/**
 * 划转门槛与今日余量。
 *
 * 全部数字直接取后端下发值，前端一个都不推导 —— 日限额的自然日边界按服务器
 * 时区切分，用客户端时间本地推算会在跨日前后给出错的"今日剩余"。
 */
export function TransferLimitsCard(props: TransferLimitsCardProps) {
  const { t } = useTranslation()
  const limits = props.limits

  const rows: { key: string; label: string; value: ReactNode }[] = [
    {
      key: 'transferable',
      label: t('qy_tr_transferable'),
      value: <QyAmountText quota={limits.transferable_quota} />,
    },
    {
      key: 'range',
      label: t('qy_tr_per_tx_range'),
      value: (
        <span className='inline-flex items-center gap-1'>
          <QyAmountText quota={limits.min_quota} />
          <span className='text-muted-foreground'>~</span>
          {limits.max_per_tx_quota > 0 ? (
            <QyAmountText quota={limits.max_per_tx_quota} />
          ) : (
            <span>{t('qy_common_unlimited')}</span>
          )}
        </span>
      ),
    },
  ]

  // 分组范围排在"今日剩余"之前：它决定"能不能转给这个人"，而剩余额度只决定
  // "还能转多少"。一个转不出去的对象，剩多少额度都没用。
  //
  // `my_group` 为空表示后端没读到用户主库行，此时一个字都不显示 ——
  // 拿一份不属于他的规则去提示，比不提示更糟。
  const groupPolicy = limits.group_policy
  if (groupPolicy != null && groupPolicy.my_group !== '') {
    rows.push({
      key: 'my-group',
      label: t('qy_tr_my_group'),
      value: <Badge variant='outline'>{groupPolicy.my_group}</Badge>,
    })
    if (groupPolicy.policy === 'allow_list') {
      rows.push({
        key: 'group-allowed',
        label: t('qy_tr_group_allowed'),
        value: <GroupList groups={groupPolicy.allowed_groups} />,
      })
    }
    if (groupPolicy.policy === 'deny_list') {
      rows.push({
        key: 'group-denied',
        label: t('qy_tr_group_denied'),
        value: <GroupList groups={groupPolicy.denied_groups} />,
      })
    }
    if (groupPolicy.policy === 'blocked') {
      rows.push({
        key: 'group-blocked',
        label: t('qy_tr_group_scope'),
        value: (
          <span className='text-destructive'>{t('qy_tr_group_blocked')}</span>
        ),
      })
    }
  }

  // 限额为 0 表示"不限"，此时展示"剩余 0"会被理解成"已经用光了"。
  if (limits.daily_max_quota > 0) {
    rows.push({
      key: 'daily-quota',
      label: t('qy_tr_daily_left_quota'),
      value: <QyAmountText quota={limits.remaining_daily_quota} />,
    })
  }
  if (limits.daily_max_count > 0) {
    rows.push({
      key: 'daily-count',
      label: t('qy_tr_daily_left_count'),
      value: t('qy_tr_count_value', {
        left: limits.remaining_daily_count,
        max: limits.daily_max_count,
      }),
    })
  }
  if (limits.fee_bps > 0) {
    rows.push({
      key: 'fee',
      label: t('qy_tr_fee_rate'),
      value: t('qy_tr_fee_value', { percent: limits.fee_bps / 100 }),
    })
    if (limits.fee_min_quota > 0) {
      rows.push({
        key: 'fee-min',
        label: t('qy_tr_fee_min'),
        value: <QyAmountText quota={limits.fee_min_quota} />,
      })
    }
  }
  if (limits.cooldown_until > 0) {
    rows.push({
      key: 'cooldown',
      label: t('qy_tr_cooldown_until'),
      value: formatTimestampToDate(limits.cooldown_until),
    })
  }

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_tr_limits_title')}</CardTitle>
        <CardDescription>{t('qy_tr_limits_desc')}</CardDescription>
      </CardHeader>
      <CardContent>
        <dl className='divide-border divide-y text-sm'>
          {rows.map((row) => (
            <div
              key={row.key}
              className='flex items-center justify-between gap-3 py-2 first:pt-0 last:pb-0'
            >
              <dt className='text-muted-foreground'>{row.label}</dt>
              <dd className='min-w-0 truncate text-right font-medium'>
                {row.value}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  )
}

/**
 * 分组名单。
 *
 * 换行显示而不是逗号拼接：分组名可以很长，一行 `default, vip, svip, partner`
 * 在窄屏上会被 `truncate` 截掉尾巴，而被截掉的那个恰恰可能是用户要转的那个组。
 */
function GroupList(props: { groups: QyTransferGroupPolicy['allowed_groups'] }) {
  const { t } = useTranslation()
  if (props.groups.length === 0) {
    return <span className='text-muted-foreground'>{t('qy_common_none')}</span>
  }
  return (
    <span className='flex flex-wrap justify-end gap-1'>
      {props.groups.map((group) => (
        <Badge key={group} variant='secondary' className='font-normal'>
          {group}
        </Badge>
      ))}
    </span>
  )
}
