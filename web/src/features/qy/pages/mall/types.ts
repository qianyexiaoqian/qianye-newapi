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

import type { QyPageParams } from '../../lib/types'

/**
 * 星屑商城（用户端）的 DTO。与契约 `stardust-api-contract.md` §4 逐字对应。
 * `price` 是整数星屑。
 */

/** 商品形态：套餐 / 兑换码 / 实物。 */
export type QyMallProductKind = 'code' | 'physical' | 'plan' | (string & {})

/** 套餐商品附带的套餐摘要（只有 `kind='plan'` 下发）。 */
export type QyMallPlanBrief = {
  title: string
  price_amount: number
  upgrade_group: string
  no_quota: boolean
  duration_unit: string
  duration_value: number
}

export type QyMallProduct = {
  product_no: string
  kind: QyMallProductKind
  title: string
  description: string
  /** 空串 = 没配封面。 */
  cover_url: string
  price: number
  /** -1 = 不限库存。 */
  stock: number
  sold: number
  /** 0 = 不限购。 */
  per_user_limit: number
  sale_start_at: number
  sale_end_at: number
  plan_id: number
  plan: QyMallPlanBrief | null
  available: boolean
  /** 我已经买过几件。 */
  my_count: number
}

/** 套餐商品下单前的预览：这一单会新开 / 续期 / 顶替 / 拒绝。 */
export type QyMallPlanPreview = {
  action: 'extend' | 'new' | 'reject' | 'supersede' | (string & {})
  superseded_groups: string[]
  seat_available: boolean
  reason: string
}

export type QyMallProductDetail = QyMallProduct & {
  preview: QyMallPlanPreview | null
}

export type QyMallOrderInput = {
  product_no: string
  client_request_id: string
  pay_password?: string
  address?: string
  contact?: string
  /** 套餐商品：把预览里看到的动作原样带回，后端比对不一致就 `qy_ml_plan_state_changed`。 */
  expect_action?: string
  expect_superseded?: string[]
}

/**
 * 订单状态。三种商品各走各的：
 *   code     → done | revoked
 *   physical → paid | shipped | done | cancelled | failed
 *   plan     → paid | done | failed | held
 */
export type QyMallOrderStatus =
  | 'cancelled'
  | 'done'
  | 'failed'
  | 'held'
  | 'paid'
  | 'revoked'
  | 'shipped'
  | (string & {})

export type QyMallOrderReceipt = {
  order_no: string
  kind: QyMallProductKind
  status: QyMallOrderStatus
  price: number
  replayed: boolean
  /** 兑换码类：码已经可以揭示（要验支付密码）。 */
  code_available: boolean
}

/**
 * 订单来源。`mall` = 用星屑兑换的；`lottery` = 抽奖 / 转盘中的商品奖
 * （价格恒 0、没有扣款流水、`ref_no` 指回出款号）。
 */
export type QyMallOrderSource = 'lottery' | 'mall' | (string & {})

export type QyMallOrder = {
  order_no: string
  kind: QyMallProductKind
  title: string
  price: number
  status: QyMallOrderStatus
  tracking_no: string
  ship_note: string
  fail_reason: string
  user_subscription_id: number
  sub_renewed: boolean
  source: QyMallOrderSource
  /** 抽奖所得的单：生成它的那笔出款号；商城兑换的单为空串。 */
  ref_no: string
  /**
   * 实物奖品单还没补填收货地址。只对 `source='lottery' && kind='physical'`
   * 有意义：兑换的实物单下单时就必须带地址，这里恒为 false。
   */
  address_missing: boolean
  created_at: number
  fulfilled_at: number
  updated_at: number
}

/** 「我的订单」的筛选：只看抽奖所得 / 只看兑换的。 */
export type QyMallOrdersParams = QyPageParams & {
  source?: QyMallOrderSource
}

/** 中奖者给实物奖品单补填一次收货地址（`POST /mall/orders/:no/address`）。 */
export type QyMallOrderAddressInput = {
  address: string
  contact: string
}

export type QyMallOrderEvent = {
  at: number
  action: string
  note: string
}

/** 订单详情：不含码、不含地址明文。 */
export type QyMallOrderDetail = QyMallOrder & {
  events: QyMallOrderEvent[]
}
