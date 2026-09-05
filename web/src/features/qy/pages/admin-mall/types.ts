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
import type {
  QyMallOrder,
  QyMallOrderSource,
  QyMallOrderStatus,
  QyMallProduct,
  QyMallProductKind,
} from '../mall/types'

/**
 * 商城管理（管理端）的 DTO。与契约 §5 逐字对应；行形状在用户端之上多几列，
 * 所以直接从 `pages/mall/types.ts` 派生。
 */

export type QyMallCodeStock = {
  unused: number
  issued: number
  revoked: number
}

export type QyMallAdminProduct = QyMallProduct & {
  enabled: boolean
  sort_order: number
  code_stock: QyMallCodeStock
  created_at: number
  updated_at: number
  /**
   * 上传封面的引用（契约 §5 之外，后端 `adminProductView` 实际下发）。
   * 编辑表单靠它回填「当前已有一张图」并在 PUT 时原样带回；缺键按空串处理。
   */
  cover_ref?: string
}

/**
 * 封面上传的响应。路径形状照抽奖封面（`POST /admin/mall/covers`），
 * 契约 §5 未列出上传端点 —— 见 `api.ts` 的 `uploadQyMallCover`。
 */
export type QyMallCoverUpload = {
  ref: string
  mime_type: string
  size: number
  created_at: number
}

export type QyMallAdminProductsParams = QyPageParams & {
  kind?: QyMallProductKind
  enabled?: boolean
}

/** 创建 / 更新请求体。更新时 `kind` 不可改（后端忽略）。 */
export type QyMallProductInput = {
  kind: QyMallProductKind
  title: string
  description: string
  cover_ref?: string
  /** ≥ 1，整数星屑。 */
  price: number
  /** -1 = 不限。 */
  stock: number
  /** 0 = 不限购。 */
  per_user_limit: number
  sale_start_at: number
  sale_end_at: number
  enabled: boolean
  sort_order: number
  plan_id?: number
}

export type QyMallCodesUploadResult = {
  accepted: number
  rejected: { index: number; reason: string }[]
}

export type QyMallAdminOrder = QyMallOrder & {
  user_id: number
  username: string
  fund_order_no: string
  /** 下单时的商品号快照（契约 §5 之外，后端 `adminOrderView` 实际下发）。 */
  product_no?: string
}

export type QyMallAdminOrdersParams = QyPageParams & {
  status?: QyMallOrderStatus
  kind?: QyMallProductKind
  user_id?: number
  /** 只看抽奖所得 / 只看兑换的（后端 `?source=`）。 */
  source?: QyMallOrderSource
}

export type QyMallShipInput = {
  tracking_no: string
  ship_note?: string
  /** 为 true 时发货后直接完结;对已 shipped 的单只做完结(后端 fulfill.go)。 */
  done?: boolean
}

/** 地址明文（sensitiveReads，每次读都写审计）。 */
export type QyMallOrderAddress = {
  address: string
  contact: string
}

/** 套餐订单跨库两阶段的人工裁决（RootActionGate）。 */
export type QyMallAdjudicateInput = {
  verdict: 'applied' | 'not_applied'
  reason: string
}
