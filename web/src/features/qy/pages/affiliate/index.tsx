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
import { TriangleAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { formatQyQuotaLedger } from '../../lib/format'
import { QyFiatText } from '../components/qy-fiat-text'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { qyAffiliateCodeQuery, qyCommissionSummaryQuery } from './api'
import { InviteLinkCard } from './components/invite-link-card'
import { QyReferralProgramCard } from './components/referral-program-card'
import type { QyCommissionSummary } from './types'

/**
 * 我的邀请概览（「推广佣金」选择夹的第一张标签，需求 3）。
 *
 * 这一屏必须同时回答三个用户最常问的问题：
 *   1. 我的邀请链接是什么 → `InviteLinkCard`
 *   2. 我一共赚了多少、能提多少 → 统计网格
 *   3. **为什么我用了一整天却没佣金** → "未结算余数"与"未成熟"两项
 *
 * 第 3 项是刻意展示的：佣金按 decimal 全精度累计、满 1 额度才落账，
 * 不把余数摆出来的话，小额用户会一直看到 0 并认为平台吞了钱。
 */
export function QyAffiliateOverviewBody() {
  const { t } = useTranslation()
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
            /*
              这一行是整屏**唯一**一个不是站内额度的数：它按计佣当刻冻结的
              汇率折算，提现按它出款，不能与上面那个额度相加。所以它必须
              带一句说明 —— 站点把展示币种配成 CNY 时，上面那个额度渲染成
              `¥0.27`、这一个渲染成 `1.97 CNY`，只靠形状分不出两件事。
            */
            hint: (
              <span className='inline-flex items-center gap-1'>
                {t('qy_aff_fiat_label')}
                <QyFiatText
                  amount={summary.available_fiat}
                  currency={summary.fiat_currency}
                />
              </span>
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
              value: formatQyQuotaLedger(summary.withdrawn_quota),
            }),
          },
          {
            key: 'invitees',
            label: t('qy_aff_invitees'),
            value: summary.invitee_count,
            hint: t('qy_aff_pending_hint', {
              // 这两个也是站内额度（`pending` 是还没过成熟期的计佣、
              // `carry` 是不足 1 额度的余数），只是后端以 decimal 字符串
              // 下发。裸印出来就是 `未成熟 13517.0000000000`，而隔壁卡片
              // 上的可提现印的是 `$0.27` —— 同一种钱两种写法。
              pending: formatQyQuotaLedger(summary.pending_mature_quota),
              carry: formatQyQuotaLedger(summary.unsettled_amount),
            }),
          },
        ]

  return (
    <div className='space-y-3'>
      {/*
        上游「推荐计划」卡（原本在钱包页底部）。刻意放在 `QyPageBoundary`
        **外面**：它读的是主库 `users.aff_*`，与 qy 的佣金接口没有依赖关系，
        放进去的话 `/commission/summary` 一挂，推荐计划会跟着一起消失。
      */}
      <QyReferralProgramCard />

      <QyPageBoundary query={summaryQuery}>
        {summary != null && (
          <div className='space-y-3'>
            {summary.debt_blocked && (
              <Alert variant='destructive'>
                <TriangleAlert />
                <AlertTitle>{t('qy_aff_debt_title')}</AlertTitle>
                <AlertDescription>{t('qy_aff_debt_desc')}</AlertDescription>
              </Alert>
            )}

            <QyStatGrid items={stats} />

            <div className='grid gap-3 lg:grid-cols-2 lg:items-start'>
              <InviteLinkCard
                code={codeQuery.data ?? ''}
                isLoading={codeQuery.isLoading}
              />
              <PolicyCard summary={summary} />
            </div>
          </div>
        )}
      </QyPageBoundary>
    </div>
  )
}

/**
 * 返佣规则说明。
 *
 * 比例后端以 bps（万分比整数）下发，这里只在展示时除以 100 换成百分比 ——
 * 全链路用整数是为了让"5% 到底是多少"可复现，前端不要把它变回浮点再传回去。
 *
 * 三个比例是**这个账号自己**的生效值：费率按推广人所在的用户分组解析，
 * 所以这一页必须同时回答"我走哪一档、为什么"，否则一个走 vip 档的人看到
 * 一个数字却不知道它从哪来，客服会一直被问同一个问题。那句解释由
 * rate.group / rate.group_matched 两位事实驱动，前端不复刻回落规则。
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
      key: 'redemption',
      label: t('qy_aff_rate_redemption'),
      // 后端下发的已经是生效值（没单独配时等于充值档），这里不再回落一次 ——
      // 前端各算一遍回落规则，就是"看到的与生效的不一致"的标准起点。
      value: summary.rate.redemption_follows_topup
        ? t('qy_aff_rate_value_follows_topup', {
            percent: summary.rate.redemption_bps / 100,
          })
        : t('qy_aff_rate_value', {
            percent: summary.rate.redemption_bps / 100,
          }),
    },
    {
      // 「你走哪一档」。上面三行数字全部来自这一档，不写出来的话它们看起来
      // 像是全站统一的比例 —— 而配了分组档的站点上，那是一句错话。
      //
      // group 为空表示后端这次没解析出账号分组（主库读失败），此时既不说
      // 命中也不说回落：编一个分组名比不说更糟。
      key: 'tier',
      label: t('qy_aff_rate_tier'),
      value:
        summary.rate.group === ''
          ? t('qy_aff_rate_tier_unknown')
          : summary.rate.group_matched
            ? t('qy_aff_rate_tier_matched', { group: summary.rate.group })
            : t('qy_aff_rate_tier_fallback', { group: summary.rate.group }),
    },
    {
      key: 'holding',
      label: t('qy_aff_holding_days'),
      value: t('qy_aff_days_value', { days: summary.policy.holding_days }),
    },
    {
      // 「T+N 到账」。佣金改成一日一结算之后，"多久能拿到钱"不再取决于
      // 结算周期，而是一句固定的话：消费之后第 N 天的日界跑那一次。
      //
      // N 由后端算好（`payout_day_offset = holding_days + 1`），前端不复刻
      // 那条规则 —— 两边各算一遍的结果就是界面上写着一个会被追问的错数字。
      key: 'payout-eta',
      label: t('qy_aff_payout_eta'),
      value:
        summary.policy.day_offset_minutes === 0
          ? t('qy_aff_payout_eta_value_utc', {
              days: summary.policy.payout_day_offset,
            })
          : t('qy_aff_payout_eta_value', {
              days: summary.policy.payout_day_offset,
            }),
    },
    // 「已经挣到的那批钱什么时候成熟」。
    //
    // 上面那行 T+N 是按**当前配置**算的，只对此后新产生的消费成立；成熟期
    // 逐行冻结，运营改一次 holding_days 不会追溯已冻结的行。所以这一行给的是
    // 账本上写着的事实，而不是拿配置反算出来的日期 —— 少了它，改配置那天
    // 这一页对每个已经有在途佣金的人都在说一句差一天的话。
    //
    // 后端下发 0 的语义是"没有需要等的东西"（没有在途佣金，或在途的都已成熟），
    // 两者对用户是同一句话，所以合并成一行文案，不再细分。
    {
      key: 'pending-mature',
      label: t('qy_aff_pending_mature_at'),
      value:
        summary.pending_earliest_mature_at > 0 ? (
          formatTimestampToDate(summary.pending_earliest_mature_at)
        ) : (
          <span className='text-muted-foreground'>
            {t('qy_aff_pending_mature_none')}
          </span>
        ),
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
          <li>{t('qy_aff_rate_scope_note')}</li>
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
