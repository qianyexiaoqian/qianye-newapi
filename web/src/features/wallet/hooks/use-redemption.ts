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
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { REDEMPTION_PRODUCT_TYPE } from '@/features/redemption-codes/constants'
import { formatQuota } from '@/lib/format'

import { redeemTopupCode } from '../api'
import { refreshCurrentUser } from '../lib'
import type { RedemptionResponse } from '../types'

// ============================================================================
// Redemption Hook
// ============================================================================

/** 兑换成功的提示文案：三种商品各说各的，"已到账 $0.00" 对套餐码毫无意义。 */
function redemptionSuccessMessage(response: RedemptionResponse): string {
  const detail = response.redeem
  if (detail && detail.product_type !== REDEMPTION_PRODUCT_TYPE.QUOTA) {
    if (detail.upgrade_group) {
      return i18next.t('qy_redemption_redeemed_plan_with_group', {
        plan: detail.plan_title ?? '',
        group: detail.upgrade_group,
      })
    }
    return i18next.t('qy_redemption_redeemed_plan', {
      plan: detail.plan_title ?? '',
    })
  }
  return i18next.t('Redemption successful! Added: {{quota}}', {
    quota: formatQuota(detail?.quota ?? response.data ?? 0),
  })
}

export function useRedemption() {
  const [redeeming, setRedeeming] = useState(false)

  const redeemCode = useCallback(async (code: string): Promise<boolean> => {
    if (!code || code.trim() === '') {
      toast.error(i18next.t('Please enter a redemption code'))
      return false
    }

    try {
      setRedeeming(true)
      const response = await redeemTopupCode({ key: code })

      if (response.success) {
        // 判据是 success，不是 `response.data`：套餐码的 data 是 0（它确实一分
        // 额度都没加），用它当真值判断会把一次成功的套餐兑换报成失败。
        toast.success(redemptionSuccessMessage(response))
        // 这里原本是裸 `await getSelf()`：新余额拉回来了却没人接，auth-store
        // 还是旧值，概览页于是显示"充值没到账"。必须走 refreshCurrentUser。
        // 套餐码同样要刷：它可能刚把用户分组升了。
        await refreshCurrentUser()
        return true
      }

      toast.error(response.message || i18next.t('Redemption failed'))
      return false
    } catch (_error) {
      toast.error(i18next.t('Redemption failed'))
      return false
    } finally {
      setRedeeming(false)
    }
  }, [])

  return {
    redeeming,
    redeemCode,
  }
}
