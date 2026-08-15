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
import { z } from 'zod'

import {
  parseQuotaFromDollars,
  quotaUnitsToEditableAmount,
} from '@/lib/format'

import {
  REDEMPTION_PRODUCT_TYPE,
  REDEMPTION_VALIDATION,
  ERROR_MESSAGES,
  getRedemptionFormErrorMessages,
  getRedemptionProductType,
  type RedemptionProductType,
} from '../constants'
import type { RedemptionFormData, Redemption } from '../types'

// ============================================================================
// Form Schema (use getRedemptionFormSchema(t) in components for i18n messages)
// ============================================================================

export function getRedemptionFormSchema(t: TFunction) {
  const msg = getRedemptionFormErrorMessages(t)
  return z
    .object({
      name: z
        .string()
        .min(REDEMPTION_VALIDATION.NAME_MIN_LENGTH, msg.NAME_LENGTH_INVALID)
        .max(REDEMPTION_VALIDATION.NAME_MAX_LENGTH, msg.NAME_LENGTH_INVALID),
      product_type: z.enum([
        REDEMPTION_PRODUCT_TYPE.QUOTA,
        REDEMPTION_PRODUCT_TYPE.PLAN,
        REDEMPTION_PRODUCT_TYPE.USER_GROUP,
      ]),
      product_id: z.number(),
      quota_dollars: z.number().min(0, t('Quota must be a positive number')),
      expired_time: z.date().optional(),
      count: z
        .number()
        .min(REDEMPTION_VALIDATION.COUNT_MIN, msg.COUNT_INVALID)
        .max(REDEMPTION_VALIDATION.COUNT_MAX, msg.COUNT_INVALID)
        .optional(),
    })
    // 必填项跟着商品类型走，所以校验必须挂在整个对象上而不是单个字段上：
    // 套餐码要 product_id，余额码要正数额度，两者互不相干。
    // 后端 AddRedemption 有同样的两条检查——这里只是让运营早一步看到，
    // 不是唯一的闸门。
    .superRefine((values, ctx) => {
      if (values.product_type === REDEMPTION_PRODUCT_TYPE.QUOTA) {
        if (values.quota_dollars <= 0) {
          ctx.addIssue({
            code: z.ZodIssueCode.custom,
            path: ['quota_dollars'],
            message: t(ERROR_MESSAGES.QUOTA_POSITIVE),
          })
        }
        return
      }
      if (values.product_id <= 0) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          path: ['product_id'],
          message: t(ERROR_MESSAGES.PLAN_REQUIRED),
        })
      }
    })
}

export type RedemptionFormValues = {
  name: string
  product_type: RedemptionProductType
  product_id: number
  quota_dollars: number
  expired_time?: Date
  count?: number
}

// ============================================================================
// Form Defaults
// ============================================================================

export const REDEMPTION_FORM_DEFAULT_VALUES: RedemptionFormValues = {
  name: '',
  product_type: REDEMPTION_PRODUCT_TYPE.QUOTA,
  product_id: 0,
  quota_dollars: 10,
  expired_time: undefined,
  count: 1,
}

// ============================================================================
// Form Data Transformation
// ============================================================================

/**
 * Transform form data to API payload
 */
export function transformFormDataToPayload(
  data: RedemptionFormValues
): RedemptionFormData {
  const isQuota = data.product_type === REDEMPTION_PRODUCT_TYPE.QUOTA
  return {
    name: data.name,
    // 套餐码的额度是死数据，后端建码时也会强制落 0；这里一并送 0，
    // 免得列表里显示一个永远不会发出去的金额。
    quota: isQuota ? parseQuotaFromDollars(data.quota_dollars) : 0,
    expired_time: data.expired_time
      ? Math.floor(data.expired_time.getTime() / 1000)
      : 0,
    count: data.count || 1,
    product_type: data.product_type,
    product_id: isQuota ? 0 : data.product_id,
  }
}

/**
 * Transform redemption data to form defaults
 */
export function transformRedemptionToFormDefaults(
  redemption: Redemption
): RedemptionFormValues {
  return {
    name: redemption.name,
    product_type: getRedemptionProductType(redemption.product_type),
    product_id: redemption.product_id ?? 0,
    quota_dollars: quotaUnitsToEditableAmount(redemption.quota),
    expired_time:
      redemption.expired_time > 0
        ? new Date(redemption.expired_time * 1000)
        : undefined,
    count: 1,
  }
}
