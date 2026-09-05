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
import { queryOptions } from '@tanstack/react-query'

import { qyGet, qyPost } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage, QyPageParams } from '../../lib/types'
import type {
  QyMallOrder,
  QyMallOrderAddressInput,
  QyMallOrderDetail,
  QyMallOrderInput,
  QyMallOrderReceipt,
  QyMallOrdersParams,
  QyMallProduct,
  QyMallProductDetail,
} from './types'

/**
 * 星屑商城（用户端）。路径与契约 §4 逐字一致；后端路由落地之前它们在
 * `route-contract.test.ts` 的 `MISS_EXEMPT` 里，落地之后请把那一条豁免删掉。
 *
 * 下单成功后**必须** `invalidateQueries({ queryKey: qyKeys.all })`：余额、商品的
 * `my_count` / `sold`、订单列表同时变了。套餐商品还动主库（用户分组 / 订阅），
 * 那一档另外调 `useQyAfterMoneyChange()`。
 */

export function qyMallProductsQuery(params: QyPageParams) {
  return queryOptions({
    queryKey: qyKeys.mallProducts(params),
    queryFn: () => qyGet<QyPage<QyMallProduct>>('/mall/products', params),
    staleTime: 15_000,
  })
}

export function qyMallProductQuery(productNo: string) {
  return queryOptions({
    queryKey: qyKeys.mallProduct(productNo),
    queryFn: () =>
      qyGet<QyMallProductDetail>(
        `/mall/products/${encodeURIComponent(productNo)}`
      ),
    staleTime: 10_000,
    enabled: productNo !== '',
  })
}

/** 下单。`client_request_id` 每次打开弹窗生成一次、重试沿用（幂等键）。 */
export function createQyMallOrder(
  body: QyMallOrderInput
): Promise<QyMallOrderReceipt> {
  return qyPost<QyMallOrderReceipt>('/mall/orders', body)
}

export function qyMallOrdersQuery(params: QyMallOrdersParams) {
  return queryOptions({
    queryKey: qyKeys.mallOrders(params),
    queryFn: () => qyGet<QyPage<QyMallOrder>>('/mall/orders', params),
    staleTime: 15_000,
  })
}

/**
 * 中奖者给抽奖所得的实物奖品单补填收货地址，**只能填一次**。
 *
 * 非奖品单 403 `qy_ml_not_prize_order`、已有地址 409 `qy_ml_address_exists`，
 * 两个 code 都登记在 `QY_ERROR_CODE_I18N`。成功后调用方全量失效：这张单的
 * `address_missing` 变了，列表与详情都要重取。
 */
export function setQyMallOrderAddress(
  orderNo: string,
  body: QyMallOrderAddressInput
): Promise<QyMallOrder> {
  return qyPost<QyMallOrder>(
    `/mall/orders/${encodeURIComponent(orderNo)}/address`,
    body
  )
}

export function qyMallOrderQuery(orderNo: string) {
  return queryOptions({
    queryKey: qyKeys.mallOrder(orderNo),
    queryFn: () =>
      qyGet<QyMallOrderDetail>(`/mall/orders/${encodeURIComponent(orderNo)}`),
    staleTime: 10_000,
    enabled: orderNo !== '',
  })
}

/**
 * 揭示兑换码。走验密中间件（header `X-Qy-Pay-Password`），**不进 react-query
 * 缓存**：码只在用户主动点开的那一刻拿一次，与抽奖文本奖同一条纪律。
 */
export function revealQyMallOrderCode(
  orderNo: string,
  payPassword: string
): Promise<{ code: string }> {
  return qyGet<{ code: string }>(
    `/mall/orders/${encodeURIComponent(orderNo)}/code`,
    undefined,
    { headers: { 'X-Qy-Pay-Password': payPassword } }
  )
}

/** 取消（仅实物且 `paid`）。退回的星屑走 `refund_ledger_no` 那一条流水。 */
export function cancelQyMallOrder(orderNo: string): Promise<{
  order_no: string
  status: 'cancelled'
  refund_ledger_no: string
}> {
  return qyPost(`/mall/orders/${encodeURIComponent(orderNo)}/cancel`)
}
