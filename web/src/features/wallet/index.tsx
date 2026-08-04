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
import { getRouteApi } from '@tanstack/react-router'
import { Crown, WalletCards } from 'lucide-react'
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  QyWalletTransferPanel,
  QyWalletTransferTrigger,
} from '@/features/qy/wallet-entry'
import {
  QY_WALLET_TRANSFER_TAB,
  useQyWalletTransferTab,
} from '@/features/qy/wallet-entry/tab'
import { useStatus } from '@/hooks/use-status'
import { useSystemConfig } from '@/hooks/use-system-config'
import { cn } from '@/lib/utils'

import { BillingHistoryDialog } from './components/dialogs/billing-history-dialog'
import { CreemConfirmDialog } from './components/dialogs/creem-confirm-dialog'
import { PaymentConfirmDialog } from './components/dialogs/payment-confirm-dialog'
import { RechargeFormCard } from './components/recharge-form-card'
import { SubscriptionPlansCard } from './components/subscription-plans-card'
import { WalletStatsCard } from './components/wallet-stats-card'
import {
  DEFAULT_DISCOUNT_RATE,
  DEFAULT_WALLET_TAB,
  PAYMENT_TYPES,
  WALLET_TAB_VALUES,
  type WalletTab,
} from './constants'
import {
  useTopupInfo,
  usePayment,
  useRedemption,
  useCreemPayment,
  useWaffoPayment,
  useWaffoPancakePayment,
} from './hooks'
import {
  getDefaultPaymentType,
  getMinTopupAmount,
  dispatchSelectedPayment,
  refreshCurrentUser,
  subscribeToTopupReturn,
} from './lib'
import type {
  UserWalletData,
  PaymentMethod,
  PresetAmount,
  CreemProduct,
  WaffoPayMethod,
} from './types'

interface WalletProps {
  initialShowHistory?: boolean
  initialTab?: WalletTab
}

const walletRoute = getRouteApi('/_authenticated/wallet/')

export function Wallet(props: WalletProps) {
  const { t } = useTranslation()
  const [user, setUser] = useState<UserWalletData | null>(null)
  const [userLoading, setUserLoading] = useState(true)
  const [topupAmount, setTopupAmount] = useState(0)
  const [selectedPreset, setSelectedPreset] = useState<number | null>(null)
  const [selectedPaymentMethod, setSelectedPaymentMethod] =
    useState<PaymentMethod>()
  const [selectedWaffoMethodIndex, setSelectedWaffoMethodIndex] = useState<
    number | null
  >(null)
  const [paymentLoading, setPaymentLoading] = useState<string | null>(null)
  const [confirmDialogOpen, setConfirmDialogOpen] = useState(false)
  const [billingDialogOpen, setBillingDialogOpen] = useState(false)
  const [redemptionCode, setRedemptionCode] = useState('')
  const [creemDialogOpen, setCreemDialogOpen] = useState(false)
  const [selectedCreemProduct, setSelectedCreemProduct] =
    useState<CreemProduct | null>(null)
  const [showSubscriptionPanel, setShowSubscriptionPanel] = useState(true)
  const [activeTab, setActiveTab] = useState<WalletTab>(
    props.initialTab ?? DEFAULT_WALLET_TAB
  )

  const navigate = walletRoute.useNavigate()
  // qy 扩展的「余额划转」格。扩展关掉时 `visible` 恒为 false，下面三处
  // 全部退化成上游原样（触发器/面板返回 null，`active` 恒为 false）。
  const qyTransferTab = useQyWalletTransferTab()
  const { status } = useStatus()
  const { currency } = useSystemConfig()
  const { topupInfo, presetAmounts, loading: topupLoading } = useTopupInfo()

  // Calculate effective exchange rate - when display type is USD, use rate of 1
  const effectiveUsdExchangeRate = useMemo(() => {
    return currency?.quotaDisplayType === 'USD'
      ? 1
      : currency?.usdExchangeRate || 1
  }, [currency?.quotaDisplayType, currency?.usdExchangeRate])
  const {
    amount: paymentAmount,
    calculating,
    processing,
    calculatePaymentAmount,
    processPayment,
  } = usePayment()
  const { redeeming, redeemCode } = useRedemption()
  const { processing: creemProcessing, processCreemPayment } = useCreemPayment()
  const { processing: waffoProcessing, processWaffoPayment } = useWaffoPayment()
  const { processing: pancakeProcessing, processWaffoPancakePayment } =
    useWaffoPancakePayment()

  // Fetch and refresh user data.
  //
  // 这里原本自己 `getSelf()` 然后只 `setUser(...)` 到上面那个组件 state —— 于是
  // 钱包页显示新余额、概览页（读 auth-store）显示旧余额，两块界面各说各话。
  // 现在同一次请求由 refreshCurrentUser 写回 auth-store，返回值再喂给局部 state。
  const fetchUser = useCallback(async () => {
    try {
      setUserLoading(true)
      const refreshed = await refreshCurrentUser()
      if (refreshed) {
        setUser(refreshed as UserWalletData)
      }
    } finally {
      setUserLoading(false)
    }
  }, [])

  useEffect(() => {
    fetchUser()
  }, [fetchUser])

  // 在线支付是在**另一个标签页**里完成的，到账走服务端回调，本页收不到任何事件。
  // 所以发起支付后打上标记，等用户切回本页时补一次刷新。标记不清除：回调可能比
  // 用户切回来更晚，用户再切走一次又切回来时还得能刷新到。
  const topupInFlightRef = useRef(false)
  useEffect(
    () =>
      subscribeToTopupReturn(() => {
        if (!topupInFlightRef.current) return
        void fetchUser()
      }),
    [fetchUser]
  )

  useEffect(() => {
    if (props.initialShowHistory) {
      setBillingDialogOpen(true)
      // Must go through the router: a raw `replaceState(pathname)` drops the
      // whole query string (including `?tab=`) and leaves the router's own
      // search state out of sync with the address bar.
      void navigate({
        search: (prev) => ({ ...prev, show_history: undefined }),
        replace: true,
      })
    }
  }, [props.initialShowHistory, navigate])

  // URL -> state, so browser back/forward and shared deep links stay honoured.
  useEffect(() => {
    const next = props.initialTab ?? DEFAULT_WALLET_TAB
    setActiveTab((prev) => (prev === next ? prev : next))
  }, [props.initialTab])

  // state -> URL. The default tab is written as `undefined` so the common case
  // keeps a clean URL, and `replace` keeps tab flipping out of the history
  // stack (Back should leave the wallet, not undo a tab switch).
  const handleTabChange = useCallback(
    (value: WalletTab) => {
      setActiveTab(value)
      void navigate({
        search: (prev) => ({
          ...prev,
          tab: value === DEFAULT_WALLET_TAB ? undefined : value,
        }),
        replace: true,
      })
    },
    [navigate]
  )

  // Base UI types the tab value as `any` and may emit `null` when it falls back
  // automatically, so whitelist the value before it can reach the URL.
  //
  // qy 的「余额划转」格不走 `?tab=`：它的取值不在 `WALLET_TAB_VALUES` 里，
  // 而路由 schema 的 `.catch(DEFAULT_WALLET_TAB)` 会把未登记的取值改写回
  // `funds`，标签会立刻自己弹回去。它的状态载体是 URL hash，见
  // `features/qy/wallet-entry/tab.ts`。
  const handleTabValueChange = useCallback(
    (value: string | null) => {
      if (value === QY_WALLET_TRANSFER_TAB) {
        qyTransferTab.activate()
        return
      }
      const next = WALLET_TAB_VALUES.find((tab) => tab === value)
      if (!next) return
      qyTransferTab.clear()
      handleTabChange(next)
    },
    [handleTabChange, qyTransferTab]
  )

  // `?tab=plans` on a site with no plans configured: fall back silently once
  // the plans card reports itself unavailable, instead of showing a blank tab.
  useEffect(() => {
    if (!showSubscriptionPanel && activeTab === 'plans') {
      handleTabChange(DEFAULT_WALLET_TAB)
    }
  }, [showSubscriptionPanel, activeTab, handleTabChange])

  // Initialize topup amount when topup info is loaded
  const topupAmountInitializedRef = useRef(false)
  useEffect(() => {
    if (topupInfo && !topupAmountInitializedRef.current) {
      topupAmountInitializedRef.current = true
      const minTopup = getMinTopupAmount(topupInfo)
      setTopupAmount(minTopup)

      // Calculate initial payment amount with default payment type
      const defaultPaymentType = getDefaultPaymentType(topupInfo)
      calculatePaymentAmount(minTopup, defaultPaymentType)
    }
  }, [topupInfo, calculatePaymentAmount])

  // Get current payment type (selected or default)
  const getCurrentPaymentType = useCallback(() => {
    return selectedPaymentMethod?.type || getDefaultPaymentType(topupInfo)
  }, [selectedPaymentMethod, topupInfo])

  // Handle preset selection
  const handleSelectPreset = (preset: PresetAmount) => {
    setTopupAmount(preset.value)
    setSelectedPreset(preset.value)
    calculatePaymentAmount(preset.value, getCurrentPaymentType())
  }

  // Handle topup amount change
  const handleTopupAmountChange = (amount: number) => {
    setTopupAmount(amount)
    setSelectedPreset(null)
    calculatePaymentAmount(amount, getCurrentPaymentType())
  }

  // Handle payment method selection
  const handlePaymentMethodSelect = async (method: PaymentMethod) => {
    setSelectedPaymentMethod(method)
    setSelectedWaffoMethodIndex(null)
    setPaymentLoading(method.type)

    try {
      // Validate minimum topup
      const minTopup = getMinTopupAmount(topupInfo)
      if (topupAmount < minTopup) {
        return
      }

      // Calculate payment amount and show confirmation dialog
      await calculatePaymentAmount(topupAmount, method.type)
      setConfirmDialogOpen(true)
    } finally {
      setPaymentLoading(null)
    }
  }

  // Handle payment confirmation
  const handlePaymentConfirm = async () => {
    if (!selectedPaymentMethod) return

    const success = await dispatchSelectedPayment(
      selectedPaymentMethod,
      topupAmount,
      selectedWaffoMethodIndex,
      {
        regular: processPayment,
        waffo: processWaffoPayment,
        waffoPancake: processWaffoPancakePayment,
      }
    )

    if (success) {
      // `success` 只代表"支付页已经打开"，不代表已付款：钱在另一个标签页里付，
      // 所以这里的 `fetchUser()` 必然拿回旧余额。真正到账后的刷新靠上面那个
      // 「切回本页」订阅，这一句只留着覆盖极少数即时到账的通道。
      topupInFlightRef.current = true
      setConfirmDialogOpen(false)
      await fetchUser()
    }
  }

  // Handle redemption
  const handleRedeem = async () => {
    if (!redemptionCode) return

    const success = await redeemCode(redemptionCode)
    if (success) {
      setRedemptionCode('')
      await fetchUser()
    }
  }

  // Handle Creem product selection
  const handleCreemProductSelect = (product: CreemProduct) => {
    setSelectedCreemProduct(product)
    setCreemDialogOpen(true)
  }

  // Handle Creem payment confirmation
  const handleCreemConfirm = async () => {
    if (!selectedCreemProduct) return

    const success = await processCreemPayment(selectedCreemProduct.productId)
    if (success) {
      // 同上：creem 走 `window.open(checkout_url)`，此刻还没付款。
      topupInFlightRef.current = true
      setCreemDialogOpen(false)
      setSelectedCreemProduct(null)
      await fetchUser()
    }
  }

  const handleWaffoMethodSelect = async (
    method: WaffoPayMethod,
    index: number
  ) => {
    const loadingKey = `waffo-${index}`
    setSelectedPaymentMethod({
      name: method.name,
      type: PAYMENT_TYPES.WAFFO,
      icon: method.icon,
    })
    setSelectedWaffoMethodIndex(index)
    setPaymentLoading(loadingKey)

    try {
      await calculatePaymentAmount(topupAmount, PAYMENT_TYPES.WAFFO)
      setConfirmDialogOpen(true)
    } finally {
      setPaymentLoading(null)
    }
  }

  // Get discount rate for current topup amount
  const getDiscountRate = useCallback(() => {
    return topupInfo?.discount?.[topupAmount] || DEFAULT_DISCOUNT_RATE
  }, [topupInfo, topupAmount])

  const handleSubscriptionAvailabilityChange = useCallback(
    (available: boolean) => {
      setShowSubscriptionPanel(available)
    },
    []
  )

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>{t('Wallet')}</SectionPageLayout.Title>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-4 sm:gap-5'>
            <WalletStatsCard user={user} loading={userLoading} />

            <Tabs
              value={qyTransferTab.active ? QY_WALLET_TRANSFER_TAB : activeTab}
              onValueChange={handleTabValueChange}
              className='gap-4'
            >
              {/*
                这排标签原本整条挂在 `showSubscriptionPanel` 之下 —— 站点没配
                订阅套餐时它压根不渲染。qy 的「余额划转」格进来之后不能再这样：
                否则"有没有配套餐"会决定"看不看得到划转"，两件无关的事。
                所以条件放宽成"还剩几格"，而「订阅套餐」那一格自己保留原条件。
              */}
              {(showSubscriptionPanel || qyTransferTab.visible) && (
                <TabsList
                  aria-label={t('Wallet')}
                  className={cn(
                    'grid w-full sm:inline-flex sm:w-auto',
                    showSubscriptionPanel && qyTransferTab.visible
                      ? 'grid-cols-3'
                      : 'grid-cols-2'
                  )}
                >
                  <TabsTrigger value='funds' className='gap-1.5 px-3'>
                    <WalletCards className='size-3.5' />
                    {t('Add Funds')}
                  </TabsTrigger>
                  {showSubscriptionPanel && (
                    <TabsTrigger value='plans' className='gap-1.5 px-3'>
                      <Crown className='size-3.5' />
                      {t('Subscription Plans')}
                    </TabsTrigger>
                  )}
                  <QyWalletTransferTrigger />
                </TabsList>
              )}

              {/*
                keepMounted is load-bearing, not cosmetic: Base UI 1.6.0's
                Tabs.Panel returns null when hidden (`keepMounted || mounted`),
                so without it every tab switch would remount both cards and
                refire their fetches, and the purchase dialog mounted inside
                SubscriptionPlansCard would be torn down mid-payment.
              */}
              <TabsContent value='funds' keepMounted>
                <div id='wallet-add-funds' className='scroll-mt-4'>
                  <RechargeFormCard
                    topupInfo={topupInfo}
                    presetAmounts={presetAmounts}
                    selectedPreset={selectedPreset}
                    onSelectPreset={handleSelectPreset}
                    topupAmount={topupAmount}
                    onTopupAmountChange={handleTopupAmountChange}
                    paymentAmount={paymentAmount}
                    calculating={calculating}
                    onPaymentMethodSelect={handlePaymentMethodSelect}
                    paymentLoading={paymentLoading}
                    redemptionCode={redemptionCode}
                    onRedemptionCodeChange={setRedemptionCode}
                    onRedeem={handleRedeem}
                    redeeming={redeeming}
                    topupLink={topupInfo?.topup_link}
                    loading={topupLoading}
                    priceRatio={(status?.price as number) || 1}
                    usdExchangeRate={effectiveUsdExchangeRate}
                    onOpenBilling={() => setBillingDialogOpen(true)}
                    creemProducts={topupInfo?.creem_products}
                    enableCreemTopup={topupInfo?.enable_creem_topup}
                    onCreemProductSelect={handleCreemProductSelect}
                    enableWaffoTopup={topupInfo?.enable_waffo_topup}
                    waffoPayMethods={topupInfo?.waffo_pay_methods}
                    waffoMinTopup={topupInfo?.waffo_min_topup}
                    onWaffoMethodSelect={handleWaffoMethodSelect}
                    enableWaffoPancakeTopup={
                      topupInfo?.enable_waffo_pancake_topup
                    }
                  />
                </div>
              </TabsContent>

              <TabsContent value='plans' keepMounted>
                <SubscriptionPlansCard
                  topupInfo={topupInfo}
                  onAvailabilityChange={handleSubscriptionAvailabilityChange}
                  userQuota={user?.quota}
                  onPurchaseSuccess={fetchUser}
                />
              </TabsContent>

              {/* qy 扩展的第三格。刻意不加 keepMounted：里面那三张标签各自
                  带查询，一进钱包页就全挂上等于白打三个接口。 */}
              <QyWalletTransferPanel />
            </Tabs>

            {/*
              Tabs 之外原本还挂着两块，现在都不在钱包页上了（qy 需求：钱包页瘦身）：
                · 「推荐计划」(AffiliateRewardsCard) —— 整块搬去「推广佣金」页，
                  由 `features/qy/pages/affiliate/components/referral-program-card`
                  复用**同一个**组件渲染，这里不再渲染第二份。
                · qy 的「增长与结算」入口卡 (QyWalletSections) —— 已删除。
              划转仍在上面那排标签里（`QyWalletTransferTrigger` / `QyWalletTransferPanel`）。
            */}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <PaymentConfirmDialog
        open={confirmDialogOpen}
        onOpenChange={setConfirmDialogOpen}
        onConfirm={handlePaymentConfirm}
        topupAmount={topupAmount}
        paymentAmount={paymentAmount}
        paymentMethod={selectedPaymentMethod}
        calculating={calculating}
        processing={processing || waffoProcessing || pancakeProcessing}
        discountRate={getDiscountRate()}
        usdExchangeRate={effectiveUsdExchangeRate}
      />

      <BillingHistoryDialog
        open={billingDialogOpen}
        onOpenChange={setBillingDialogOpen}
      />

      <CreemConfirmDialog
        open={creemDialogOpen}
        onOpenChange={setCreemDialogOpen}
        onConfirm={handleCreemConfirm}
        product={selectedCreemProduct}
        processing={creemProcessing}
      />
    </>
  )
}
