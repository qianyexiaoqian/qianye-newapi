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
import { z } from 'zod'

// ============================================================================
// Redemption Schema & Types
// ============================================================================

export const redemptionSchema = z.object({
  id: z.number(),
  user_id: z.number(),
  name: z.string(),
  key: z.string(),
  status: z.number(), // 1: enabled, 2: disabled, 3: used
  quota: z.number(),
  created_time: z.number(),
  redeemed_time: z.number(),
  expired_time: z.number(), // 0 for never expires
  used_user_id: z.number(),
  /**
   * 这张码兑换的是什么商品：'quota' | 'plan' | 'usergroup'。
   *
   * optional 不是防御性写法，是真实存在的状态：这一列是后加的，库里那批还没
   * 兑换的存量码是空串。判定一律走 `getRedemptionProductType()`，它把空串归到
   * 余额 —— 与后端 `Redemption.ProductKind()` 同一口径。
   */
  product_type: z.string().optional(),
  /** 商品类型是 plan / usergroup 时指向订阅套餐 id；余额码为 0。 */
  product_id: z.number().optional(),
})

export type Redemption = z.infer<typeof redemptionSchema>

// ============================================================================
// API Request/Response Types
// ============================================================================

export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface GetRedemptionsParams {
  p?: number
  page_size?: number
}

export interface GetRedemptionsResponse {
  success: boolean
  message?: string
  data?: {
    items: Redemption[]
    total: number
    page: number
    page_size: number
  }
}

export interface SearchRedemptionsParams {
  keyword?: string
  status?: string
  p?: number
  page_size?: number
}

export interface RedemptionFormData {
  id?: number
  name: string
  quota: number
  expired_time: number
  count?: number // Only for create
  status?: number // Only for status update
  // 商品类型只在创建时有意义：后端 Redemption.Update() 的字段白名单里没有它们，
  // 建码那一刻定死，与 key 同级。
  product_type?: string
  product_id?: number
}

// ============================================================================
// Dialog Types
// ============================================================================

export type RedemptionsDialogType = 'create' | 'update' | 'delete' | 'view'
