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
import type { QyMallProduct, QyMallProductKind } from '../types'

/**
 * 商品卡片与详情共用的**纯判定**。
 *
 * 放在 `.ts` 里而不是组件里，是因为"这件商品现在能不能买、不能买是为什么"
 * 这两句话在卡片、详情弹窗、下单弹窗三处都要说，而且每一处说错的代价都是
 * 用户按下去吃一个后端错误 —— 三处各写一遍必然漂移。这里只算**展示用**的
 * 结论，放行与否永远以 `POST /mall/orders` 的返回为准。
 */

/** 发售状态。0 = 不限，左闭右开，与后端 `saleWindowOpen` / 上游套餐同口径。 */
export type QyMallSaleState = 'ended' | 'on_sale' | 'upcoming'

export function qyMallSaleState(
  product: Pick<QyMallProduct, 'sale_end_at' | 'sale_start_at'>,
  nowSeconds: number
): QyMallSaleState {
  if (product.sale_start_at > 0 && nowSeconds < product.sale_start_at) {
    return 'upcoming'
  }
  if (product.sale_end_at > 0 && nowSeconds >= product.sale_end_at) {
    return 'ended'
  }
  return 'on_sale'
}

/**
 * 还剩几件；`null` = 不限。
 *
 * `code` 类的 `stock` 由后端换成了库存表里 `unused` 的条数（`effectiveStock`），
 * 它本身就是"还剩几枚"，不能再减 `sold` —— 减了会把一件还有 3 枚码的商品
 * 算成负数、显示成售罄。
 */
export function qyMallRemaining(
  product: Pick<QyMallProduct, 'kind' | 'sold' | 'stock'>
): number | null {
  if (product.kind === 'code') return Math.max(0, product.stock)
  if (product.stock < 0) return null
  return Math.max(0, product.stock - product.sold)
}

/** 为什么此刻不能买。`null` = 能买。 */
export type QyMallBuyBlock =
  | 'ended'
  | 'limit'
  | 'sold_out'
  | 'unavailable'
  | 'upcoming'

/**
 * 顺序是刻意的：售期 → 库存 → 限购 → 后端的 `available`。
 *
 * 前三条都能从字段上算出**具体原因**，只有算不出来时才回落到 `available=false`
 * 那句笼统的「暂不可兑换」（下架、套餐本身停售、探针没开都在这一档）。
 * `available` 由后端算，**不含限购**：它不知道是谁在看这一页，所以限购要在
 * 这里单独判 —— 后端给 `my_count` 正是为了这一步。
 */
export function qyMallBuyBlock(
  product: QyMallProduct,
  nowSeconds: number
): QyMallBuyBlock | null {
  const sale = qyMallSaleState(product, nowSeconds)
  if (sale !== 'on_sale') return sale
  if (qyMallRemaining(product) === 0) return 'sold_out'
  if (
    product.per_user_limit > 0 &&
    product.my_count >= product.per_user_limit
  ) {
    return 'limit'
  }
  if (!product.available) return 'unavailable'
  return null
}

/** 商品形态的文案键。未知形态原样显示，后端加新形态时前端不白屏。 */
export function qyMallKindKey(kind: QyMallProductKind): string {
  return `qy_ml_kind_${kind}`
}
