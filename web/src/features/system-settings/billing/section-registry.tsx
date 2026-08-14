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
import { ModelGroupsSection } from '../groups/model-groups-section'
import { TokenDefaultGroupsSection } from '../groups/token-default-groups-section'
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
    ── 原「分组定价」一页现在是**两项** ──

    项目方原话（第一轮）：「用户分组、模型分组，既然已经分出来了就请你彻底分
    出来……」；（本轮）「你现在前端怎么感觉搞得一团糟……比如，用户分组这一页，
    这两个可以合并成一个。明明一个很简单的问题为什么这么搞这么复杂？」
    「简单一点：用户分组：注册用户数，充值倍率，可用模型分组，用户分组备注。
    编辑、删除。一个列表框即可。」

    判据仍然是配置的**主语**，只是从三档收成两档：
      · 主语是一档人   → user-groups
      · 主语是一批渠道 → model-groups
    而 (用户分组, 模型分组) 这一对不再独占一页 —— 它是「一档人」的一个属性，
    编辑面是 user-groups 那张表行内的配置弹窗。

    ── 原来第三项 `group-matrix` 为什么下线 ──

    它当时留着的理由有两条，本轮两条都不成立了：

     1. 「它是 `UserUsableGroups` 的唯一编辑器」—— 那份 map 的键现在是模型分组
        表上的「用户可选」开关列（项目方点名的四列之一），它的主语本来就是一批
        渠道，不是一对分组。
     2. 「整列批量 / 跨档对比 / 孤儿令牌基线在弹窗里表达不出来」—— 这三件确实
        表达不出来，但它们是**排查**动作不是**配置**动作，把它们摆在「计费与
        支付」的第三个菜单项上，正是项目方说的那种"一团糟"。它们整体留在
        `/qy/admin/group-matrix` 这条既有路由上（原本就是给普通管理员的入口，
        现在成为唯一入口），定位为高级/诊断视图。

    `titleKey` 全部走 qy 语言包：上游 7 个 locale 文件是高频合并冲突区，
    而这两页的命名是本 fork 自己的口径。两份资源合并进同一个命名空间，
    `t()` 解析方式与上游键完全一致。
  */
  {
    id: 'user-groups',
    titleKey: 'qy_gs_user_groups_title',
    build: (settings: BillingSettings) => (
      <UserGroupsSection defaultValues={getUserGroupDefaults(settings)} />
    ),
  },
  {
    /*
      「令牌默认分组」单独成页而不是并进「用户分组」页：那一页的表行来自扩展的
      分组矩阵接口，`group_matrix` 关掉时整张表是空的，编辑器会跟着消失 ——
      而这项配置与可用范围无关，必须在扩展未启用时照样能配。
      归属登记见 `../groups/lib/group-options.ts` 的 `TOKEN_DEFAULT_PAGE_KEYS`。
    */
    id: 'token-default-groups',
    titleKey: 'qy_gs_token_default_title',
    build: (settings: BillingSettings) => (
      <TokenDefaultGroupsSection
        defaultValues={{ TokenDefaultGroups: settings.TokenDefaultGroups }}
      />
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
