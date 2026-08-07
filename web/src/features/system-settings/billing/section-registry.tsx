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
import { parseCurrencyDisplayType } from '@/lib/currency'

import { CheckinSettingsSection } from '../general/checkin-settings-section'
import { PricingSection } from '../general/pricing-section'
import { QuotaSettingsSection } from '../general/quota-settings-section'
import { GroupMatrixSection } from '../groups/group-matrix-section'
import { ModelGroupsSection } from '../groups/model-groups-section'
import { UserGroupsSection } from '../groups/user-groups-section'
import { PaymentSettingsSection } from '../integrations/payment-settings-section'
import { RatioSettingsCard } from '../models/ratio-settings-card'
import type { BillingSettings } from '../types'
import { createSectionRegistry } from '../utils/section-registry'

const getModelDefaults = (settings: BillingSettings) => ({
  ModelPrice: settings.ModelPrice,
  ModelRatio: settings.ModelRatio,
  CacheRatio: settings.CacheRatio,
  CreateCacheRatio: settings.CreateCacheRatio,
  CompletionRatio: settings.CompletionRatio,
  ImageRatio: settings.ImageRatio,
  AudioRatio: settings.AudioRatio,
  AudioCompletionRatio: settings.AudioCompletionRatio,
  ExposeRatioEnabled: settings.ExposeRatioEnabled,
  BillingMode: settings['billing_setting.billing_mode'],
  BillingExpr: settings['billing_setting.billing_expr'],
})

/**
 * 8 个分组配置项按**主语**拆到三页，每一项恰好有一个编辑器。
 *
 * 拆分依据与守卫测试见 `../groups/lib/group-options.ts`：
 * `USER_GROUP_PAGE_KEYS` / `GROUP_MATRIX_PAGE_KEYS` / `MODEL_GROUP_PAGE_KEYS`
 * 三份清单断言两两不交且并集恰好是 8 项，任何一处加错都会红。
 *
 * 下面三个 getter 里出现的**只读**字段（A 页的 `GroupGroupRatio`、
 * C 页的 `UserUsableGroups`）不违反那条约束：它们只被拿去建行 / 显示徽标，
 * 三页的保存都只写自己那几个键（见 `useGroupOptionSave` 的逐键差分）。
 */
const getUserGroupDefaults = (settings: BillingSettings) => ({
  TopupGroupRatio: settings.TopupGroupRatio,
  DefaultUseAutoGroup: settings.DefaultUseAutoGroup,
  GroupGroupRatio: settings.GroupGroupRatio,
  // 只读：A 页拿它判断「auto 清单是不是空的」。`DefaultUseAutoGroup` 归 A 页而
  // `AutoGroups` 归 C 页，拆页之后两者不再同屏 —— 打开开关而 auto 清单是空的时候，
  // 每个新注册用户的初始令牌都拿不到任何候选模型分组，第一次调用就失败，而 A 页
  // 上没有任何信号。编辑器仍然唯一（这里只读不写）。
  AutoGroups: settings.AutoGroups,
})

const getGroupMatrixDefaults = (settings: BillingSettings) => ({
  UserUsableGroups: settings.UserUsableGroups,
  GroupSpecialUsableGroup:
    settings['group_ratio_setting.group_special_usable_group'],
})

const getModelGroupDefaults = (settings: BillingSettings) => ({
  GroupRatio: settings.GroupRatio,
  AutoGroups: settings.AutoGroups,
  MaxTokenAutoGroups: settings.MaxTokenAutoGroups,
  UserUsableGroups: settings.UserUsableGroups,
})

const BILLING_SECTIONS = [
  {
    id: 'quota',
    titleKey: 'Quota Settings',
    build: (settings: BillingSettings) => (
      <QuotaSettingsSection
        defaultValues={{
          QuotaForNewUser: settings.QuotaForNewUser,
          PreConsumedQuota: settings.PreConsumedQuota,
          QuotaForInviter: settings.QuotaForInviter,
          QuotaForInvitee: settings.QuotaForInvitee,
          TopUpLink: settings.TopUpLink,
          general_setting: {
            docs_link: settings['general_setting.docs_link'],
          },
          quota_setting: {
            enable_free_model_pre_consume:
              settings['quota_setting.enable_free_model_pre_consume'],
          },
        }}
        complianceConfirmed={
          (settings['payment_setting.compliance_confirmed'] ?? false) &&
          settings['payment_setting.compliance_terms_version'] === 'v1'
        }
      />
    ),
  },
  {
    id: 'currency',
    titleKey: 'Currency & Display',
    build: (settings: BillingSettings) => (
      <PricingSection
        defaultValues={{
          QuotaPerUnit: settings.QuotaPerUnit,
          USDExchangeRate: settings.USDExchangeRate,
          DisplayInCurrencyEnabled: settings.DisplayInCurrencyEnabled,
          DisplayTokenStatEnabled: settings.DisplayTokenStatEnabled,
          general_setting: {
            quota_display_type: parseCurrencyDisplayType(
              settings['general_setting.quota_display_type']
            ),
            custom_currency_symbol:
              settings['general_setting.custom_currency_symbol'] ?? '¤',
            custom_currency_exchange_rate:
              settings['general_setting.custom_currency_exchange_rate'] ?? 1,
          },
        }}
      />
    ),
  },
  {
    id: 'model-pricing',
    titleKey: 'Model Pricing',
    build: (settings: BillingSettings) => (
      <RatioSettingsCard
        titleKey='Model Pricing'
        modelDefaults={getModelDefaults(settings)}
        toolPricesDefault={settings['tool_price_setting.prices']}
        visibleTabs={['models', 'unset-models', 'tool-prices', 'upstream-sync']}
      />
    ),
  },
  /*
    ── 原「分组定价」一页拆成下面三项 ──

    项目方原话：「用户分组、模型分组，既然已经分出来了就请你彻底分出来……
    在：系统设置-计费与支付-分组定价，你不应该拆开吗？用户分组、用户分组可用的
    模型分组配置、模型分组，这些很多都是原项目，就改了一点文案。」

    三项的顺序照项目方点名的顺序排。判据是配置的**主语**：
      · 主语是一档人             → user-groups
      · 主语是 (用户分组, 模型分组) → group-matrix
      · 主语是一批渠道           → model-groups

    `titleKey` 全部走 qy 语言包：上游 7 个 locale 文件是高频合并冲突区，
    而这三页的命名是本 fork 自己的口径。两份资源合并进同一个命名空间，
    `t()` 解析方式与上游键完全一致。

    ── 为什么 group-matrix 单独一项，而不是并进 user-groups ──

    危险的是两个**编辑器**编同一份数据，不是两个路由。可用范围与交叉倍率的
    编辑器只有矩阵那一个（它带着预览闸门与双库部分失败横幅），user-groups 上
    对应的两列是只读摘要 + 一条深链接。反过来，充值折扣只在 user-groups 上编辑，
    矩阵页一个字都不提它。
  */
  {
    id: 'user-groups',
    titleKey: 'qy_gs_user_groups_title',
    build: (settings: BillingSettings) => (
      <UserGroupsSection defaultValues={getUserGroupDefaults(settings)} />
    ),
  },
  {
    // 扩展关掉时这一项由 `withQyBillingSectionNavItems` 从抽屉里摘掉；section
    // 本身仍然登记在这里，深链接才不会被 `$section` 的白名单弹回默认页 ——
    // 点进来会看到「功能未启用」的中性空态，而不是一次莫名其妙的跳转。
    id: 'group-matrix',
    titleKey: 'qy_gs_group_matrix_title',
    build: (settings: BillingSettings) => (
      <GroupMatrixSection defaultValues={getGroupMatrixDefaults(settings)} />
    ),
  },
  {
    id: 'model-groups',
    titleKey: 'qy_gs_model_groups_title',
    build: (settings: BillingSettings) => (
      <ModelGroupsSection defaultValues={getModelGroupDefaults(settings)} />
    ),
  },
  {
    id: 'payment',
    titleKey: 'Payment Gateway',
    build: (settings: BillingSettings) => (
      <PaymentSettingsSection
        defaultValues={{
          PayAddress: settings.PayAddress,
          EpayId: settings.EpayId,
          EpayKey: settings.EpayKey,
          Price: settings.Price,
          MinTopUp: settings.MinTopUp,
          CustomCallbackAddress: settings.CustomCallbackAddress,
          PayMethods: settings.PayMethods,
          AmountOptions: settings['payment_setting.amount_options'],
          AmountDiscount: settings['payment_setting.amount_discount'],
          StripeApiSecret: settings.StripeApiSecret,
          StripeWebhookSecret: settings.StripeWebhookSecret,
          StripePriceId: settings.StripePriceId,
          StripeUnitPrice: settings.StripeUnitPrice,
          StripeMinTopUp: settings.StripeMinTopUp,
          StripePromotionCodesEnabled: settings.StripePromotionCodesEnabled,
          CreemApiKey: settings.CreemApiKey,
          CreemWebhookSecret: settings.CreemWebhookSecret,
          CreemTestMode: settings.CreemTestMode,
          CreemProducts: settings.CreemProducts,
        }}
        waffoDefaultValues={{
          WaffoEnabled: settings.WaffoEnabled ?? false,
          WaffoApiKey: settings.WaffoApiKey ?? '',
          WaffoPrivateKey: settings.WaffoPrivateKey ?? '',
          WaffoPublicCert: settings.WaffoPublicCert ?? '',
          WaffoSandboxPublicCert: settings.WaffoSandboxPublicCert ?? '',
          WaffoSandboxApiKey: settings.WaffoSandboxApiKey ?? '',
          WaffoSandboxPrivateKey: settings.WaffoSandboxPrivateKey ?? '',
          WaffoSandbox: settings.WaffoSandbox ?? false,
          WaffoMerchantId: settings.WaffoMerchantId ?? '',
          WaffoCurrency: settings.WaffoCurrency ?? 'USD',
          WaffoUnitPrice: settings.WaffoUnitPrice ?? 1,
          WaffoMinTopUp: settings.WaffoMinTopUp ?? 1,
          WaffoNotifyUrl: settings.WaffoNotifyUrl ?? '',
          WaffoReturnUrl: settings.WaffoReturnUrl ?? '',
          WaffoPayMethods: settings.WaffoPayMethods ?? '[]',
        }}
        waffoPancakeDefaultValues={{
          WaffoPancakeMerchantID: settings.WaffoPancakeMerchantID ?? '',
          WaffoPancakePrivateKey: settings.WaffoPancakePrivateKey ?? '',
          WaffoPancakeReturnURL: settings.WaffoPancakeReturnURL ?? '',
        }}
        waffoPancakeProvisionedStoreID={settings.WaffoPancakeStoreID ?? ''}
        waffoPancakeProvisionedProductID={settings.WaffoPancakeProductID ?? ''}
        complianceDefaults={{
          confirmed: settings['payment_setting.compliance_confirmed'] ?? false,
          termsVersion:
            settings['payment_setting.compliance_terms_version'] ?? '',
          confirmedAt: settings['payment_setting.compliance_confirmed_at'] ?? 0,
          confirmedBy: settings['payment_setting.compliance_confirmed_by'] ?? 0,
        }}
      />
    ),
  },
  {
    id: 'checkin',
    titleKey: 'Check-in Rewards',
    build: (settings: BillingSettings) => (
      <CheckinSettingsSection
        defaultValues={{
          enabled: settings['checkin_setting.enabled'],
          minQuota: settings['checkin_setting.min_quota'],
          maxQuota: settings['checkin_setting.max_quota'],
        }}
      />
    ),
  },
] as const

export type BillingSectionId = (typeof BILLING_SECTIONS)[number]['id']

const billingRegistry = createSectionRegistry<
  BillingSectionId,
  BillingSettings
>({
  sections: BILLING_SECTIONS,
  defaultSection: 'quota',
  basePath: '/system-settings/billing',
  urlStyle: 'path',
})

export const BILLING_SECTION_IDS = billingRegistry.sectionIds
export const BILLING_DEFAULT_SECTION = billingRegistry.defaultSection
export const getBillingSectionNavItems = billingRegistry.getSectionNavItems
export const getBillingSectionContent = billingRegistry.getSectionContent
export const getBillingSectionMeta = billingRegistry.getSectionMeta
