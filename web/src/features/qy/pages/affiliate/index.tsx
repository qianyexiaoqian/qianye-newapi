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
import { TriangleAlert, Users, Wallet } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { formatTimestampToDate } from '@/lib/format'

import { QyAmountText } from '../../components/qy-amount-text'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { useQyConfig } from '../../hooks/use-qy-config'
import { QyFiatText } from '../components/qy-fiat-text'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { qyAffiliateCodeQuery, qyCommissionSummaryQuery } from './api'
import { InviteLinkCard } from './components/invite-link-card'
import type { QyCommissionSummary } from './types'

/**
 * 我的邀请（推广概览）。
 *
 * 这一屏必须同时回答三个用户最常问的问题：
 *   1. 我的邀请链接是什么 → `InviteLinkCard`
 *   2. 我一共赚了多少、能提多少 → 统计网格
 *   3. **为什么我用了一整天却没佣金** → "未结算余数"与"未成熟"两项
 *
 * 第 3 项是刻意展示的：佣金按 decimal 全精度累计、满 1 额度才落账，
 * 不把余数摆出来的话，小额用户会一直看到 0 并认为平台吞了钱。
 */
export function QyAffiliate() {
  const { t } = useTranslation()
  const config = useQyConfig()
  const summaryQuery = useQuery(qyCommissionSummaryQuery())
  const codeQuery = useQuery(qyAffiliateCodeQuery())

  const summary = summaryQuery.data
  const stats: QyStatItem[] =
    summary == null
      ? []
      : [
          {
            key: 'available',
            label: t('qy_aff_available'),
            value: <QyAmountText quota={summary.available_quota} />,
            hint: (
              <QyFiatText
                amount={summary.available_fiat}
                currency={summary.fiat_currency}
              />
            ),
            emphasis: true,
          },
          {
            key: 'frozen',
            label: t('qy_aff_frozen'),
            value: <QyAmountText quota={summary.frozen_quota} />,
            hint: t('qy_aff_frozen_hint'),
          },
          {
            key: 'earned',
            label: t('qy_aff_total_earned'),
            value: <QyAmountText quota={summary.total_earned_quota} />,
            hint: t('qy_aff_withdrawn_hint', {
              // 已提现是累计值，与"当前可提"分开展示，避免用户把两者相加。
              value: summary.withdrawn_quota,
            }),
          },
          {
            key: 'invitees',
            label: t('qy_aff_invitees'),
            value: summary.invitee_count,
            hint: t('qy_aff_pending_hint', {
              pending: summary.pending_mature_quota,
              carry: summary.unsettled_amount,
            }),
          },
        ]

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_affiliate')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Actions>
        <Button variant='outline' size='sm' render={<Link to='/qy/invitees' />}>
          <Users aria-hidden='true' />
          {t('qy_nav_invitees')}
        </Button>
        {config.features.withdraw && (
          <Button size='sm' render={<Link to='/qy/withdraw' />}>
            <Wallet aria-hidden='true' />
            {t('qy_nav_withdraw')}
          </Button>
        )}
      </QySectionPageLayout.Actions>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={summaryQuery}>
          {summary != null && (
            <div className='space-y-4'>
              {summary.debt_blocked && (
                <Alert variant='destructive'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_aff_debt_title')}</AlertTitle>
                  <AlertDescription>{t('qy_aff_debt_desc')}</AlertDescription>
                </Alert>
              )}

              <QyStatGrid items={stats} />

              <div className='grid gap-4 lg:grid-cols-2 lg:items-start'>
                <InviteLinkCard
                  code={codeQuery.data ?? ''}
                  isLoading={codeQuery.isLoading}
                />
                <PolicyCard summary={summary} />
              </div>
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>
    </QySectionPageLayout>
  )
}

/**
 * 返佣规则说明。
 *
 * 比例后端以 bps（万分比整数）下发，这里只在展示时除以 100 换成百分比 ——
 * 全链路用整数是为了让"5% 到底是多少"可复现，前端不要把它变回浮点再传回去。
 */
function PolicyCard(props: { summary: QyCommissionSummary }) {
  const { t } = useTranslation()
  const summary = props.summary

  const rows = [
    {
      key: 'topup',
      label: t('qy_aff_rate_topup'),
      value: t('qy_aff_rate_value', { percent: summary.rate.topup_bps / 100 }),
    },
    {
      key: 'consume',
      label: t('qy_aff_rate_consume'),
      value: t('qy_aff_rate_value', {
        percent: summary.rate.consume_bps / 100,
      }),
    },
    {
      key: 'holding',
      label: t('qy_aff_holding_days'),
      value: t('qy_aff_days_value', { days: summary.policy.holding_days }),
    },
    {
      key: 'min-settle',
      label: t('qy_aff_min_settle'),
      value: <QyAmountText quota={summary.policy.min_settle_quota} />,
    },
    {
      key: 'last-settled',
      label: t('qy_aff_last_settled'),
      value: formatTimestampToDate(summary.last_settled_at),
    },
  ]

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_aff_policy_title')}</CardTitle>
        <CardDescription>{t('qy_aff_policy_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-3'>
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
        <ul className='text-muted-foreground list-inside list-disc space-y-1 text-xs'>
          {summary.policy.exclude_redemption && (
            <li>{t('qy_aff_exclude_redemption')}</li>
          )}
          {summary.policy.exclude_subscription && (
            <li>{t('qy_aff_exclude_subscription')}</li>
          )}
        </ul>
      </CardContent>
    </Card>
  )
}
