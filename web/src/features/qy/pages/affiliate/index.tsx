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
import type { TFunction } from 'i18next'
import { ShieldAlert, TriangleAlert } from 'lucide-react'
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
import { QySdAmount } from '../../components/qy-sd-amount'
import { QySdDecimal } from '../../components/qy-sd-decimal'
import { useQyConfig } from '../../hooks/use-qy-config'
import { useStardustName } from '../../hooks/use-stardust-name'
import { qyFormatAtDayline } from '../../lib/dayline'
import { formatSdDecimal, formatSdWithUnit } from '../../lib/format-sd'
import { QyStatGrid, type QyStatItem } from '../components/qy-stat-grid'
import { qySdBpsPercent } from '../stardust/lib/display'
import {
  qyAffiliateCodeQuery,
  qyCommissionSummaryQuery,
  qyInviteSummaryQuery,
} from './api'
import { InviteLinkCard } from './components/invite-link-card'
import { QyInviteKindBreakdown } from './components/kind-breakdown'
import { QyReferralProgramCard } from './components/referral-program-card'
import type { QyCommissionSummary, QyInviteSummary } from './types'

/**
 * 「概览」—— 「我的推广」选择夹的第一张标签（D-16）。
 *
 * 两条线**并行**，而且发的是**同一种钱**（星屑）：
 *
 *   · 上半：**推广佣金** —— 待结算 / 可用（待入账）/ 已入账三格、下次入账时间、
 *     我的费率。到期自动记入星屑余额，没有申请、没有审核 —— 这一屏上没有「提现」。
 *   · 下半：**邀请返还** —— 五种来源的分布、昨日已返 / 暂缓、今日待返基数、
 *     合规声明状态。
 *
 * D-15 时上半记的是「星辉」（站内余额的展示名），所以这一屏上曾有两种单位并排。
 * D-16 之后两边都是星屑，全部走 `QySdAmount` —— 唯二仍按额度印的是**消费基数**
 * （昨日下线消费 / 今日待返基数）：那是分母，不是返给谁的钱。
 *
 * 佣金关掉、邀请返照开的站点上，上半整段不渲染（也不发请求），下半照旧。
 */
export function QyAffiliateOverviewBody() {
  const { t } = useTranslation()
  const unit = useStardustName()
  const config = useQyConfig()
  const commissionOn = config.features.commission
  const summaryQuery = useQuery(qyInviteSummaryQuery())
  const commissionQuery = useQuery({
    ...qyCommissionSummaryQuery(),
    enabled: commissionOn,
  })
  const codeQuery = useQuery(qyAffiliateCodeQuery())

  const summary = summaryQuery.data
  const stats: QyStatItem[] =
    summary == null
      ? []
      : [
          {
            key: 'total',
            label: t('qy_inv_total_all'),
            value: <QySdAmount amount={summary.totals.all} variant='hero' />,
            hint: t('qy_inv_total_all_hint', { unit }),
            emphasis: true,
          },
          {
            key: 'invitees',
            label: t('qy_inv_invitee_count'),
            value: summary.invitee_count,
            hint:
              summary.blocked_count > 0
                ? t('qy_inv_blocked_count_hint', {
                    count: summary.blocked_count,
                  })
                : t('qy_inv_blocked_none_hint'),
          },
          {
            // 昨日：基数是额度、已返 / 暂缓是星屑。两个星屑数都要在 —— 只给已返，
            // 被暂缓的那部分看起来就像被吞了。
            key: 'yesterday',
            label: t('qy_inv_yesterday_base'),
            value: <QyAmountText quota={summary.yesterday.base_quota} />,
            hint: t('qy_inv_yesterday_hint', {
              granted: formatSdWithUnit(summary.yesterday.granted, unit),
              held: formatSdWithUnit(summary.yesterday.held, unit),
            }),
          },
          {
            key: 'pending',
            label: t('qy_inv_pending_today'),
            value: <QyAmountText quota={summary.pending_today_base_quota} />,
            hint: t('qy_inv_pending_today_hint', {
              percent: qySdBpsPercent(summary.rate.invite_consume_bps),
            }),
          },
        ]

  return (
    <div className='space-y-3'>
      {/*
        上游「推荐计划」卡（原本在钱包页底部）。刻意放在 `QyPageBoundary`
        **外面**：它读的是主库 `users.aff_*`，与 qy 的两条接口没有依赖关系，
        放进去的话 `/invite/summary` 一挂，推荐计划会跟着一起消失。
      */}
      <QyReferralProgramCard />

      {commissionOn && (
        <section className='space-y-3' data-section='commission'>
          <h3 className='text-sm font-medium'>{t('qy_aff_xh_title')}</h3>
          <QyPageBoundary query={commissionQuery}>
            {commissionQuery.data != null && (
              <CommissionOverview
                summary={commissionQuery.data}
                inviteLinkCode={codeQuery.data ?? ''}
                inviteLinkLoading={codeQuery.isLoading}
              />
            )}
          </QyPageBoundary>
        </section>
      )}

      <section className='space-y-3' data-section='stardust'>
        <h3 className='text-sm font-medium'>{t('qy_aff_sd_title')}</h3>
        <QyPageBoundary query={summaryQuery}>
          {summary != null && (
            <div className='space-y-3'>
              {!summary.compliance_confirmed && (
                // 合规门关着时后端不发任何邀请返。这句话必须在数字之前出现，
                // 否则一个下线活跃、自己却始终是 0 的邀请人会认为平台吞了钱。
                <Alert variant='destructive'>
                  <ShieldAlert />
                  <AlertTitle>{t('qy_inv_compliance_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_inv_compliance_desc')}
                  </AlertDescription>
                </Alert>
              )}

              <QyStatGrid items={stats} />

              <QyInviteKindBreakdown totals={summary.totals} />

              <div className='grid gap-3 lg:grid-cols-2 lg:items-start'>
                {/* 佣金关着时邀请链接卡落在这里；佣金开着时它已经在上半出现过，
                    同一张卡出现两次只会让人以为是两个不同的码。 */}
                {!commissionOn && (
                  <InviteLinkCard
                    code={codeQuery.data ?? ''}
                    isLoading={codeQuery.isLoading}
                  />
                )}
                <RateCard summary={summary} />
              </div>
            </div>
          )}
        </QyPageBoundary>
      </section>
    </div>
  )
}

/**
 * 推广佣金的三格 + 规则卡。
 *
 * 三格是钱在账本里的三个阶段：**待结算**（已计佣、还没过成熟期）→ **可用**
 * （已成熟，等自动入账攒够门槛）→ **已入账**（已经记进星屑余额）。少任何一格，
 * "我用了一天怎么没到账"就答不全 —— 钱可能正停在前两格之一。
 */
function CommissionOverview(props: {
  summary: QyCommissionSummary
  inviteLinkCode: string
  inviteLinkLoading: boolean
}) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const summary = props.summary

  // 余数与待结算是 decimal 字符串（后端全精度下发，前端只展示不运算）。要插进
  // 句子里的走 `formatSdDecimal` + 单位，能放组件的地方走 `QySdDecimal`；
  // 两者是同一份去零逻辑，不会出现同一个数在提示里和格子里长得不一样。
  const carry = `${formatSdDecimal(summary.unsettled_amount)} ${unit}`
  const nextCreditHint =
    summary.next_credit_at > 0
      ? t('qy_aff_next_credit_value', {
          min: formatSdWithUnit(summary.min_credit_stardust, unit),
          time: qyFormatAtDayline(
            summary.next_credit_at,
            summary.policy.day_offset_minutes
          ),
        })
      : t('qy_aff_next_credit_unknown', {
          min: formatSdWithUnit(summary.min_credit_stardust, unit),
        })

  const stats: QyStatItem[] = [
    {
      key: 'pending',
      label: t('qy_aff_pending_settle'),
      value: <QySdDecimal value={summary.pending_mature} />,
      hint:
        summary.pending_earliest_mature_at > 0
          ? t('qy_aff_pending_settle_hint', {
              date: formatTimestampToDate(summary.pending_earliest_mature_at),
              carry,
            })
          : t('qy_aff_pending_settle_none_hint', { carry }),
    },
    {
      key: 'available',
      label: t('qy_aff_available_credit'),
      value: <QySdAmount amount={summary.available} variant='hero' />,
      hint: nextCreditHint,
      emphasis: true,
    },
    {
      key: 'credited',
      label: t('qy_aff_credited'),
      value: <QySdAmount amount={summary.credited} variant='hero' />,
      hint: t('qy_aff_credited_hint', {
        // 累计是含已入账的总数，与"当前可用"分开展示，避免用户把两者相加。
        value: formatSdWithUnit(summary.total_earned, unit),
      }),
    },
  ]

  return (
    <div className='space-y-3'>
      {summary.debt_blocked && (
        <Alert variant='destructive'>
          <TriangleAlert />
          <AlertTitle>{t('qy_aff_debt_credit_title')}</AlertTitle>
          <AlertDescription>{t('qy_aff_debt_credit_desc')}</AlertDescription>
        </Alert>
      )}

      <QyStatGrid items={stats} />

      <div className='grid gap-3 lg:grid-cols-2 lg:items-start'>
        <InviteLinkCard
          code={props.inviteLinkCode}
          isLoading={props.inviteLinkLoading}
        />
        <PolicyCard summary={summary} />
      </div>
    </div>
  )
}

/**
 * 「你走哪一档」那一格的三种说法：读不到分组 / 命中分组档 / 回落全站默认。
 * 三个分支各是一句不同的话，写成嵌套三元只会让下一个人改错方向。
 */
function rateTierLabel(summary: QyCommissionSummary, t: TFunction): string {
  if (summary.rate.group === '') return t('qy_aff_rate_tier_unknown')
  if (summary.rate.group_matched) {
    return t('qy_aff_rate_tier_matched', { group: summary.rate.group })
  }
  return t('qy_aff_rate_tier_fallback', { group: summary.rate.group })
}

/**
 * 返佣规则说明（佣金侧）。
 *
 * 比例后端以 bps（万分比整数）下发，这里只在展示时除以 100 换成百分比 ——
 * 全链路用整数是为了让"5% 到底是多少"可复现，前端不要把它变回浮点再传回去。
 *
 * 三个比例是**这个账号自己**的生效值：费率按推广人所在的用户分组解析，
 * 所以这一页必须同时回答"我走哪一档、为什么"。那句解释由 rate.group /
 * rate.group_matched 两位事实驱动，前端不复刻回落规则。
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
      // 「你走哪一档」。group 为空表示后端这次没解析出账号分组，此时既不说
      // 命中也不说回落：编一个分组名比不说更糟。
      key: 'tier',
      label: t('qy_aff_rate_tier'),
      value: rateTierLabel(summary, t),
    },
    {
      key: 'holding',
      label: t('qy_aff_holding_days'),
      value: t('qy_aff_days_value', { days: summary.policy.holding_days }),
    },
    {
      // 「T+N 到账」按**当前配置**算，只对此后新产生的消费成立；N 由后端算好
      // （`payout_day_offset = holding_days + 1`），前端不复刻那条规则。
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
    {
      // 「已经挣到的那批钱什么时候成熟」是账本上写着的事实，成熟期逐行冻结，
      // 运营改一次 holding_days 不会追溯已冻结的行；后端下发 0 = 没有需要等的。
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
      value: <QySdAmount amount={summary.policy.min_settle_stardust} />,
    },
    {
      // 自动入账的门槛与"最小结算额度"是两道门：前者是余额攒到多少才记进星屑，
      // 后者是计佣攒到多少才落成余额。两个都显示，少一个就解释不了"可用里有钱
      // 为什么还没到账"。
      key: 'min-credit',
      label: t('qy_aff_min_credit'),
      value: <QySdAmount amount={summary.min_credit_stardust} />,
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
          <li>{t('qy_aff_credit_note')}</li>
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

/**
 * 我的费率（星屑侧）。
 *
 * 四个数全部是**这个账号自己**的生效值：费率按邀请人自己所在的用户分组解析
 * （既有拍板：与下线在哪个分组无关），所以这一页必须同时回答"我走哪一档"，
 * 否则一个走 vip 档的人看到一个数字却不知道它从哪来。比例后端以 bps 下发，
 * 这里只在展示时换成百分数 —— 全链路用整数是为了让"5% 到底是多少"可复现。
 */
function RateCard(props: { summary: QyInviteSummary }) {
  const { t } = useTranslation()
  const unit = useStardustName()
  const summary = props.summary

  const rows = [
    {
      key: 'group',
      label: t('qy_inv_rate_group'),
      value:
        summary.rate.group === ''
          ? t('qy_inv_rate_group_unknown')
          : summary.rate.group,
    },
    {
      key: 'consume',
      label: t('qy_sd_kind_invite_consume'),
      value: t('qy_sd_percent', {
        percent: qySdBpsPercent(summary.rate.invite_consume_bps),
      }),
    },
    {
      key: 'topup',
      label: t('qy_sd_kind_invite_topup'),
      value: t('qy_sd_percent', {
        percent: qySdBpsPercent(summary.rate.invite_topup_bps),
      }),
    },
    {
      key: 'redeem',
      label: t('qy_sd_kind_invite_redeem'),
      value: t('qy_sd_percent', {
        percent: qySdBpsPercent(summary.rate.invite_redeem_bps),
      }),
    },
    {
      key: 'register',
      label: t('qy_sd_kind_invite_register'),
      value: <QySdAmount amount={summary.rate.invite_register_stardust} />,
    },
    {
      key: 'compliance',
      label: t('qy_inv_compliance_label'),
      value: summary.compliance_confirmed
        ? t('qy_inv_compliance_ok')
        : t('qy_inv_compliance_missing'),
    },
  ]

  return (
    <Card data-card-hover='false'>
      <CardHeader>
        <CardTitle>{t('qy_inv_rate_title')}</CardTitle>
        <CardDescription>{t('qy_inv_rate_desc', { unit })}</CardDescription>
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
          <li>{t('qy_inv_rate_scope_note')}</li>
          {/* 「次日到账」要说清是谁的次日：站点配 0 时，国内用户的"次日"其实
              是北京时间早上 8 点。 */}
          <li>
            {summary.day_offset_minutes === 0
              ? t('qy_inv_settle_note_utc')
              : t('qy_inv_settle_note')}
          </li>
        </ul>
      </CardContent>
    </Card>
  )
}
