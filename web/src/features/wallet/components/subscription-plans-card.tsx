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
import { Crown, RefreshCw, Sparkles, Check } from 'lucide-react'
import { useState, useEffect, useMemo, useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import {
  StatusBadge,
  dotColorMap,
  textColorMap,
} from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader } from '@/components/ui/card'
import { Progress } from '@/components/ui/progress'
import { Separator } from '@/components/ui/separator'
import { Skeleton } from '@/components/ui/skeleton'
import { TitledCard } from '@/components/ui/titled-card'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import {
  QyMyEntitlementSummary,
  QySubscriptionEntitlement,
} from '@/features/qy/plan-entitlement/buyer-entitlement'
import {
  qyPlanDisclosure,
  useQyMyEntitlements,
} from '@/features/qy/plan-entitlement/use-my-entitlements'
import {
  getPublicPlans,
  getSelfSubscriptionFull,
} from '@/features/subscriptions/api'
import { SubscriptionPurchaseDialog } from '@/features/subscriptions/components/dialogs/subscription-purchase-dialog'
import { buildPlanFacts, formatPlanPrice } from '@/features/subscriptions/lib'
import type {
  PlanRecord,
  UserSubscriptionRecord,
} from '@/features/subscriptions/types'
import { formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { PaymentMethod, TopupInfo } from '../types'

interface SubscriptionPlansCardProps {
  topupInfo: TopupInfo | null
  onAvailabilityChange?: (available: boolean) => void
  userQuota?: number
  onPurchaseSuccess?: () => void | Promise<void>
}

function getEpayMethods(payMethods: PaymentMethod[] = []): PaymentMethod[] {
  return payMethods.filter(
    (m) => m?.type && m.type !== 'stripe' && m.type !== 'creem'
  )
}

export function SubscriptionPlansCard({
  topupInfo,
  onAvailabilityChange,
  userQuota,
  onPurchaseSuccess,
}: SubscriptionPlansCardProps) {
  const { t } = useTranslation()

  const [plans, setPlans] = useState<PlanRecord[]>([])
  const [activeSubscriptions, setActiveSubscriptions] = useState<
    UserSubscriptionRecord[]
  >([])
  const [allSubscriptions, setAllSubscriptions] = useState<
    UserSubscriptionRecord[]
  >([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  const [purchaseOpen, setPurchaseOpen] = useState(false)
  const [selectedPlan, setSelectedPlan] = useState<PlanRecord | null>(null)

  // 「买了能多用哪个模型分组」「这笔余额只能花在什么上」。上游的套餐接口不带
  // 这两条事实，而它们正是本站某些套餐**唯一的卖点**与唯一的使用限制。
  const entitlements = useQyMyEntitlements()

  const enableStripe = !!topupInfo?.enable_stripe_topup
  const enableCreem = !!topupInfo?.enable_creem_topup
  const enableWaffoPancake = !!topupInfo?.enable_waffo_pancake_topup
  const enableOnlineTopUp = !!topupInfo?.enable_online_topup
  const epayMethods = useMemo(
    () => getEpayMethods(topupInfo?.pay_methods),
    [topupInfo?.pay_methods]
  )

  const fetchPlans = useCallback(async () => {
    try {
      const res = await getPublicPlans()
      if (res.success) {
        setPlans(res.data || [])
      }
    } catch {
      setPlans([])
    }
  }, [])

  const fetchSelfSubscription = useCallback(async () => {
    try {
      const res = await getSelfSubscriptionFull()
      if (res.success && res.data) {
        setActiveSubscriptions(res.data.subscriptions || [])
        setAllSubscriptions(res.data.all_subscriptions || [])
      }
    } catch {
      // ignore
    }
  }, [])

  useEffect(() => {
    const init = async () => {
      setLoading(true)
      await Promise.all([fetchPlans(), fetchSelfSubscription()])
      setLoading(false)
    }
    init()
  }, [fetchPlans, fetchSelfSubscription])

  const handleRefresh = async () => {
    setRefreshing(true)
    try {
      entitlements.reload()
      await fetchSelfSubscription()
    } finally {
      setRefreshing(false)
    }
  }

  const hasActive = activeSubscriptions.length > 0
  const hasAny = allSubscriptions.length > 0
  const isAvailable = loading || plans.length > 0 || hasAny

  const planPurchaseCountMap = useMemo(() => {
    const map = new Map<number, number>()
    for (const sub of allSubscriptions) {
      const planId = sub?.subscription?.plan_id
      if (!planId) continue
      map.set(planId, (map.get(planId) || 0) + 1)
    }
    return map
  }, [allSubscriptions])

  useEffect(() => {
    onAvailabilityChange?.(isAvailable)
  }, [isAvailable, onAvailabilityChange])

  const planTitleMap = useMemo(() => {
    const map = new Map<number, string>()
    for (const p of plans) {
      if (p?.plan?.id) {
        map.set(p.plan.id, p.plan.title || '')
      }
    }
    return map
  }, [plans])

  const getRemainingDays = (sub: UserSubscriptionRecord) => {
    const endTime = sub?.subscription?.end_time || 0
    if (!endTime) return 0
    const now = Date.now() / 1000
    return Math.max(0, Math.ceil((endTime - now) / 86400))
  }

  const getUsagePercent = (sub: UserSubscriptionRecord) => {
    const total = Number(sub?.subscription?.amount_total || 0)
    const used = Number(sub?.subscription?.amount_used || 0)
    if (total <= 0) return 0
    return Math.round((used / total) * 100)
  }

  if (loading) {
    return (
      <Card data-card-hover='false' className='gap-0 overflow-hidden py-0'>
        <CardHeader className='border-b p-3 !pb-3 sm:p-5 sm:!pb-5'>
          <Skeleton className='h-6 w-32' />
        </CardHeader>
        <CardContent className='space-y-4 p-3 sm:p-5'>
          <Skeleton className='h-20 w-full' />
          {/* Mirrors the real plan grid below so nothing jumps on load. */}
          <div className='grid grid-cols-1 gap-3 2xl:grid-cols-2 2xl:gap-4'>
            {['first', 'second', 'third'].map((key) => (
              <Skeleton key={key} className='h-56 w-full' />
            ))}
          </div>
        </CardContent>
      </Card>
    )
  }

  if (plans.length === 0 && !hasAny) {
    return null
  }

  return (
    <>
      <TitledCard
        title={t('Subscription Plans')}
        description={t('Subscribe to a plan for model access')}
        icon={<Crown className='h-4 w-4' />}
        iconTone='warning'
        disableHoverEffect
        contentClassName='space-y-4 sm:space-y-5'
      >
        {/* My subscriptions & the (fixed) billing order */}
        <div className='rounded-xl border p-3 sm:p-4'>
          <div className='flex flex-wrap items-center justify-between gap-2.5 sm:gap-3'>
            <div className='flex min-w-0 flex-wrap items-center gap-2'>
              <span className='text-sm font-medium'>
                {t('My Subscriptions')}
              </span>
              <span className='flex items-center gap-1.5 text-xs font-medium'>
                <span
                  className={cn(
                    'size-1.5 shrink-0 rounded-full',
                    hasActive ? dotColorMap.success : dotColorMap.neutral
                  )}
                  aria-hidden='true'
                />
                {hasActive ? (
                  <span className={cn(textColorMap.success)}>
                    {activeSubscriptions.length} {t('active')}
                  </span>
                ) : (
                  <span className='text-muted-foreground'>
                    {t('No Active')}
                  </span>
                )}
                {allSubscriptions.length > activeSubscriptions.length && (
                  <>
                    <span className='text-muted-foreground/30'>·</span>
                    <span className='text-muted-foreground'>
                      {allSubscriptions.length - activeSubscriptions.length}{' '}
                      {t('expired')}
                    </span>
                  </>
                )}
              </span>
            </div>
            <Button
              variant='ghost'
              size='icon'
              className='h-8 w-8 shrink-0'
              onClick={handleRefresh}
              disabled={refreshing}
            >
              <RefreshCw
                className={`h-3.5 w-3.5 ${refreshing ? 'animate-spin' : ''}`}
              />
            </Button>
          </div>

          {/* 扣费顺序的唯一说明位。

              这里此前是一个「计费偏好」下拉（订阅优先 / 钱包优先 / 仅用订阅 /
              仅用钱包），扣费顺序现在写死为「套餐优先」，下拉已撤。撤掉一个用过
              的开关而不说明，用户看到的是"我设的仅用钱包不见了、钱从哪扣不知道"，
              所以在**下拉原来所在的那一格**留一句静态说明：找不到开关的人正是
              在这里找。放在「我的订阅」这一块里还有第二个理由 —— 判据里那个
              「余额够不够这次扣费」说的就是下面列出的每条订阅的剩余额度，说明
              与它所描述的数字同框，不必来回翻页。

              无条件渲染（不看有没有订阅）：没有订阅的人得到的是"会走钱包"，
              这同样是他需要知道的那一半。 */}
          <p className='text-muted-foreground mt-2 text-xs leading-5'>
            {t('qy_billing_order_fixed')}
          </p>

          <QyMyEntitlementSummary result={entitlements} />

          {hasAny && (
            <>
              <Separator className='my-3' />
              <div className='max-h-64 space-y-3 overflow-y-auto pr-1'>
                {allSubscriptions.map((sub) => {
                  const subscription = sub.subscription
                  const totalAmount = Number(subscription?.amount_total || 0)
                  const usedAmount = Number(subscription?.amount_used || 0)
                  const remainAmount =
                    totalAmount > 0 ? Math.max(0, totalAmount - usedAmount) : 0
                  const planTitle =
                    planTitleMap.get(subscription?.plan_id) || ''
                  const remainDays = getRemainingDays(sub)
                  const usagePercent = getUsagePercent(sub)
                  const now = Date.now() / 1000
                  const isExpired = (subscription?.end_time || 0) < now
                  const isCancelled = subscription?.status === 'cancelled'
                  const isActive =
                    subscription?.status === 'active' && !isExpired
                  const nextResetTime = subscription?.next_reset_time ?? 0
                  let statusBadge = (
                    <StatusBadge
                      label={t('Expired')}
                      variant='neutral'
                      copyable={false}
                    />
                  )
                  if (isActive) {
                    statusBadge = (
                      <StatusBadge
                        label={t('Active')}
                        variant='success'
                        copyable={false}
                      />
                    )
                  } else if (isCancelled) {
                    statusBadge = (
                      <StatusBadge
                        label={t('Cancelled')}
                        variant='neutral'
                        copyable={false}
                      />
                    )
                  }

                  let endTimeLabel = t('Expired at')
                  if (isActive) {
                    endTimeLabel = t('Until')
                  } else if (isCancelled) {
                    endTimeLabel = t('Cancelled at')
                  }

                  return (
                    <div
                      key={subscription?.id}
                      className='bg-background rounded-md border p-3 text-xs'
                    >
                      <div className='flex items-center justify-between'>
                        <div className='flex items-center gap-2'>
                          <span className='font-medium'>
                            {planTitle
                              ? `${planTitle} · ${t('Subscription')} #${subscription?.id}`
                              : `${t('Subscription')} #${subscription?.id}`}
                          </span>
                          {statusBadge}
                        </div>
                        {isActive && (
                          <span className='text-muted-foreground'>
                            {t('{{count}} days remaining', {
                              count: remainDays,
                            })}
                          </span>
                        )}
                      </div>
                      <div className='text-muted-foreground mt-1.5'>
                        {endTimeLabel}{' '}
                        {new Date(
                          (subscription?.end_time || 0) * 1000
                        ).toLocaleString()}
                      </div>
                      {isActive && nextResetTime > 0 && (
                        <div className='text-muted-foreground mt-1'>
                          {t('Next reset')}:{' '}
                          {new Date(nextResetTime * 1000).toLocaleString()}
                        </div>
                      )}
                      <div className='text-muted-foreground mt-1'>
                        {t('Total Quota')}:{' '}
                        {totalAmount > 0 ? (
                          <Tooltip>
                            <TooltipTrigger
                              render={<span className='cursor-help' />}
                            >
                              {formatQuota(usedAmount)}/
                              {formatQuota(totalAmount)} · {t('Remaining')}{' '}
                              {formatQuota(remainAmount)}
                            </TooltipTrigger>
                            <TooltipContent>
                              {t('Raw Quota')}: {usedAmount}/{totalAmount} ·{' '}
                              {t('Remaining')} {remainAmount}
                            </TooltipContent>
                          </Tooltip>
                        ) : (
                          t('Unlimited')
                        )}
                        {totalAmount > 0 && (
                          <span className='ml-2'>
                            {t('Used')} {usagePercent}%
                          </span>
                        )}
                      </div>
                      <QySubscriptionEntitlement
                        result={entitlements}
                        subscriptionId={subscription?.id}
                      />
                      {totalAmount > 0 && isActive && (
                        <Progress value={usagePercent} className='mt-2 h-1.5' />
                      )}
                    </div>
                  )
                })}
              </div>
            </>
          )}

          {!hasAny && (
            <p className='text-muted-foreground mt-2 text-xs'>
              {t('Subscribe to a plan for model access')}
            </p>
          )}
        </div>

        {/* Available plans grid */}
        {plans.length > 0 ? (
          <div className='grid grid-cols-1 gap-3 2xl:grid-cols-2 2xl:gap-4'>
            {plans.map((p, index) => {
              const plan = p?.plan
              if (!plan) return null
              const isPopular = index === 0 && plans.length > 1
              const limit = Number(plan.max_purchase_per_user || 0)
              const count = planPurchaseCountMap.get(plan.id) || 0
              const reached = limit > 0 && count >= limit
              const facts = buildPlanFacts(plan, t, {
                includeLegacyGroupRewrite: true,
                purchaseCount: count,
                entitlement: qyPlanDisclosure(entitlements, plan.id),
              })

              return (
                <Card
                  key={plan.id}
                  data-card-hover='false'
                  className={cn(isPopular && 'border-primary/70 shadow-sm')}
                >
                  <CardContent className='flex h-full flex-col p-3.5 sm:p-4'>
                    <div className='mb-2 flex items-start justify-between gap-3'>
                      <div className='min-w-0'>
                        {/* No truncate: a clipped plan name is unusable.
                            overflow-wrap guards against unbroken strings. */}
                        <h4 className='font-semibold [overflow-wrap:anywhere]'>
                          {plan.title || t('Subscription Plans')}
                        </h4>
                        {plan.subtitle && (
                          <Tooltip>
                            {/* Clamped rather than fully expanded: cards sit in
                                a stretch grid, so one long subtitle would drag
                                every sibling card taller. The uncut text lives
                                in the purchase dialog (and this tooltip). */}
                            <TooltipTrigger
                              render={
                                <p className='text-muted-foreground mt-0.5 line-clamp-3 cursor-help text-xs leading-relaxed [overflow-wrap:anywhere] whitespace-pre-line' />
                              }
                            >
                              {plan.subtitle}
                            </TooltipTrigger>
                            <TooltipContent className='max-w-xs whitespace-pre-line'>
                              {plan.subtitle}
                            </TooltipContent>
                          </Tooltip>
                        )}
                      </div>
                      {isPopular && (
                        <StatusBadge
                          variant='info'
                          copyable={false}
                          className='shrink-0'
                        >
                          <Sparkles className='h-3 w-3' />
                          {t('Recommended')}
                        </StatusBadge>
                      )}
                    </div>

                    <div className='py-2'>
                      <span className='text-primary text-2xl font-bold'>
                        {formatPlanPrice(plan)}
                      </span>
                    </div>

                    <div className='flex-1 space-y-1.5 pb-3'>
                      {facts.map((fact) => (
                        <div
                          key={fact.id}
                          className='text-muted-foreground flex items-center gap-2 text-xs'
                        >
                          <Check className='text-primary h-3 w-3 shrink-0' />
                          <span className='[overflow-wrap:anywhere]'>
                            {fact.label}: {fact.value}
                          </span>
                        </div>
                      ))}
                    </div>

                    <Separator className='mb-3' />

                    {reached ? (
                      <Tooltip>
                        <TooltipTrigger render={<div />}>
                          <Button variant='outline' className='w-full' disabled>
                            {t('Limit Reached')}
                          </Button>
                        </TooltipTrigger>
                        <TooltipContent>
                          {t('Purchase limit reached')} ({count}/{limit})
                        </TooltipContent>
                      </Tooltip>
                    ) : (
                      <Button
                        variant='outline'
                        className='w-full'
                        onClick={() => {
                          setSelectedPlan(p)
                          setPurchaseOpen(true)
                        }}
                      >
                        {t('Subscribe Now')}
                      </Button>
                    )}
                  </CardContent>
                </Card>
              )
            })}
          </div>
        ) : (
          <p className='text-muted-foreground py-4 text-center text-sm'>
            {t('No plans available')}
          </p>
        )}
      </TitledCard>

      <SubscriptionPurchaseDialog
        open={purchaseOpen}
        onOpenChange={(open) => {
          setPurchaseOpen(open)
          if (!open) {
            // 买完之后解锁清单会变，必须与订阅列表一起重取：只刷新其中一个，
            // 用户会看到"多了一条订阅、但解锁的分组还是买之前那些"。
            entitlements.reload()
            fetchSelfSubscription()
          }
        }}
        plan={selectedPlan}
        /* beforePayment：掏钱那一屏上，「还在读」必须说出来并且把付款按钮一起
           挡住。整行省略与"这个套餐不解锁任何分组"在视觉上完全一样，而后者是
           一句后端从没说过的话。列表那边（line 557）刻意保持沉默，理由见
           qyPlanDisclosure 的注释。 */
        entitlement={qyPlanDisclosure(entitlements, selectedPlan?.plan?.id, {
          beforePayment: true,
        })}
        enableStripe={enableStripe}
        enableCreem={enableCreem}
        enableWaffoPancake={enableWaffoPancake}
        enableOnlineTopUp={enableOnlineTopUp}
        epayMethods={epayMethods}
        userQuota={userQuota}
        onPurchaseSuccess={onPurchaseSuccess}
        purchaseLimit={
          selectedPlan?.plan?.max_purchase_per_user
            ? Number(selectedPlan.plan.max_purchase_per_user)
            : undefined
        }
        purchaseCount={
          selectedPlan?.plan?.id
            ? planPurchaseCountMap.get(selectedPlan.plan.id)
            : undefined
        }
      />
    </>
  )
}
