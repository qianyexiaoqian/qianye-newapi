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
import type { TFunction } from 'i18next'

import type { StatusBadgeProps } from '@/components/status-badge'

// ============================================================================
// Redemption Status Configuration
// ============================================================================

export const REDEMPTION_STATUS = {
  ENABLED: 1,
  DISABLED: 2,
  USED: 3,
} as const

export const REDEMPTION_STATUS_VALUES = Object.values(REDEMPTION_STATUS).map(
  (value) => String(value)
) as `${number}`[]

// labelKey values are i18n keys; use t(config.labelKey) in components
export const REDEMPTION_STATUSES: Record<
  number,
  Pick<StatusBadgeProps, 'variant'> & {
    labelKey: string
    value: number
  }
> = {
  [REDEMPTION_STATUS.ENABLED]: {
    labelKey: 'Unused',
    variant: 'success',
    value: REDEMPTION_STATUS.ENABLED,
  },
  [REDEMPTION_STATUS.DISABLED]: {
    labelKey: 'Disabled',
    variant: 'neutral',
    value: REDEMPTION_STATUS.DISABLED,
  },
  [REDEMPTION_STATUS.USED]: {
    labelKey: 'Used',
    variant: 'neutral',
    value: REDEMPTION_STATUS.USED,
  },
} as const

// ============================================================================
// Redemption Product Types
// ============================================================================

/**
 * 一张码只绑一种商品，不做组合包。取值与后端 `model.RedemptionProduct*` 一致。
 *
 * PLAN 与 USER_GROUP 在后端走的是**同一条发放路径**（都发一条 UserSubscription），
 * 差别整个落在绑定的那个套餐上：用户组商品绑的是纯商品档（`no_quota`），
 * 不带余额、只改用户分组。这里分成两项是为了运营能在列表里一眼看出卖的是什么。
 */
export const REDEMPTION_PRODUCT_TYPE = {
  QUOTA: 'quota',
  PLAN: 'plan',
  USER_GROUP: 'usergroup',
} as const

export type RedemptionProductType =
  (typeof REDEMPTION_PRODUCT_TYPE)[keyof typeof REDEMPTION_PRODUCT_TYPE]

/**
 * 把兑换码上的 product_type 归一成一个已知类型。
 *
 * 空串 / undefined 一律是余额码：这一列是后加的，存量码上没有值。
 * 与后端 `Redemption.ProductKind()` 是同一条规则，两边必须一起改。
 */
export function getRedemptionProductType(
  productType?: string
): RedemptionProductType {
  const kind = productType?.trim()
  if (
    kind === REDEMPTION_PRODUCT_TYPE.PLAN ||
    kind === REDEMPTION_PRODUCT_TYPE.USER_GROUP
  ) {
    return kind
  }
  return REDEMPTION_PRODUCT_TYPE.QUOTA
}

/** labelKey 是 qy i18n 的键，用 t(labelKey) 取文案。 */
export const REDEMPTION_PRODUCT_TYPE_LABEL_KEYS: Record<
  RedemptionProductType,
  string
> = {
  [REDEMPTION_PRODUCT_TYPE.QUOTA]: 'qy_redemption_product_quota',
  [REDEMPTION_PRODUCT_TYPE.PLAN]: 'qy_redemption_product_plan',
  [REDEMPTION_PRODUCT_TYPE.USER_GROUP]: 'qy_redemption_product_usergroup',
}

export function getRedemptionProductTypeOptions(t: TFunction) {
  return (
    Object.values(REDEMPTION_PRODUCT_TYPE) as RedemptionProductType[]
  ).map((value) => ({
    value,
    label: t(REDEMPTION_PRODUCT_TYPE_LABEL_KEYS[value]),
  }))
}

// Virtual status filter value for expired redemption codes
// Note: "Expired" is not a real DB status, it's computed from expired_time
export const REDEMPTION_FILTER_EXPIRED = 'expired'

export const REDEMPTION_FILTER_VALUES = [
  String(REDEMPTION_STATUS.ENABLED),
  String(REDEMPTION_STATUS.DISABLED),
  String(REDEMPTION_STATUS.USED),
  REDEMPTION_FILTER_EXPIRED,
] as const

export function getRedemptionStatusOptions(t: TFunction) {
  return [
    ...Object.values(REDEMPTION_STATUSES).map((config) => ({
      label: t(config.labelKey),
      value: String(config.value),
    })),
    {
      label: t('Expired'),
      value: REDEMPTION_FILTER_EXPIRED,
    },
  ]
}

// ============================================================================
// Validation Constants
// ============================================================================

export const REDEMPTION_VALIDATION = {
  NAME_MIN_LENGTH: 1,
  NAME_MAX_LENGTH: 20,
  COUNT_MIN: 1,
  COUNT_MAX: 100,
} as const

// ============================================================================
// Error Messages
// ============================================================================

// i18n keys; use t(ERROR_MESSAGES.xxx) when displaying. For form schema with interpolation use getRedemptionFormErrorMessages(t).
export const ERROR_MESSAGES = {
  UNEXPECTED: 'An unexpected error occurred',
  LOAD_FAILED: 'Failed to load redemption codes',
  SEARCH_FAILED: 'Failed to search redemption codes',
  CREATE_FAILED: 'Failed to create redemption code',
  UPDATE_FAILED: 'Failed to update redemption code',
  DELETE_FAILED: 'Failed to delete redemption code',
  DELETE_INVALID_FAILED: 'Failed to delete invalid redemption codes',
  STATUS_UPDATE_FAILED: 'Failed to update redemption code status',
  NAME_LENGTH_INVALID: 'Name must be between {{min}} and {{max}} characters',
  COUNT_INVALID: 'Count must be between {{min}} and {{max}}',
  EXPIRED_TIME_INVALID: 'Expired time cannot be earlier than current time',
  // qy i18n 键：商品类型是本 fork 加的能力，文案不进 locales/。
  PLAN_REQUIRED: 'qy_redemption_plan_required',
  QUOTA_POSITIVE: 'qy_redemption_quota_positive',
} as const

/** For form schema only: returns translated messages with interpolation. */
export function getRedemptionFormErrorMessages(t: TFunction) {
  return {
    NAME_LENGTH_INVALID: t(ERROR_MESSAGES.NAME_LENGTH_INVALID, {
      min: REDEMPTION_VALIDATION.NAME_MIN_LENGTH,
      max: REDEMPTION_VALIDATION.NAME_MAX_LENGTH,
    }),
    COUNT_INVALID: t(ERROR_MESSAGES.COUNT_INVALID, {
      min: REDEMPTION_VALIDATION.COUNT_MIN,
      max: REDEMPTION_VALIDATION.COUNT_MAX,
    }),
    EXPIRED_TIME_INVALID: t(ERROR_MESSAGES.EXPIRED_TIME_INVALID),
  } as const
}

// ============================================================================
// Success Messages (i18n keys; use t(SUCCESS_MESSAGES.xxx) when displaying)
// ============================================================================

export const SUCCESS_MESSAGES = {
  REDEMPTION_CREATED: 'Redemption code(s) created successfully',
  REDEMPTION_UPDATED: 'Redemption code updated successfully',
  REDEMPTION_DELETED: 'Redemption code deleted successfully',
  REDEMPTION_ENABLED: 'Redemption code enabled successfully',
  REDEMPTION_DISABLED: 'Redemption code disabled successfully',
  COPY_SUCCESS: 'Copied to clipboard',
} as const
