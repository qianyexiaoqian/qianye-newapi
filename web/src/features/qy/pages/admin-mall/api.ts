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

import { qyDelete, qyGet, qyPost, qyPut } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyMallAdjudicateInput,
  QyMallAdminOrder,
  QyMallAdminOrdersParams,
  QyMallAdminProduct,
  QyMallAdminProductsParams,
  QyMallCodesUploadResult,
  QyMallCoverUpload,
  QyMallOrderAddress,
  QyMallProductInput,
  QyMallShipInput,
} from './types'

/**
 * 商城管理（管理端）。路径与契约 §5 逐字一致；后端路由落地之前它们在
 * `route-contract.test.ts` 的 `MISS_EXEMPT` 里，落地之后请把那一条豁免删掉。
 *
 * 退星屑的三个动作（fail / revoke-code / 用户端 cancel）成功后都要
 * `invalidateQueries({ queryKey: qyKeys.all })`。
 */

function productPath(productNo: string, suffix = ''): string {
  return `/admin/mall/products/${encodeURIComponent(productNo)}${suffix}`
}

function orderPath(orderNo: string, suffix = ''): string {
  return `/admin/mall/orders/${encodeURIComponent(orderNo)}${suffix}`
}

export function qyAdminMallProductsQuery(params: QyMallAdminProductsParams) {
  return queryOptions({
    queryKey: qyKeys.adminMallProducts(params),
    queryFn: () =>
      qyGet<QyPage<QyMallAdminProduct>>('/admin/mall/products', params),
    staleTime: 15_000,
  })
}

/** `kind='plan'` 且 outbox 关闭时 400 `qy_ml_plan_needs_outbox`。 */
export function createQyMallProduct(
  body: QyMallProductInput
): Promise<QyMallAdminProduct> {
  return qyPost<QyMallAdminProduct>('/admin/mall/products', body)
}

export function updateQyMallProduct(
  productNo: string,
  body: QyMallProductInput
): Promise<QyMallAdminProduct> {
  return qyPut<QyMallAdminProduct>(productPath(productNo), body)
}

/** 有未完结订单时 409 `qy_ml_has_open_orders`。 */
export function deleteQyMallProduct(productNo: string): Promise<unknown> {
  return qyDelete<unknown>(productPath(productNo))
}

/**
 * 上传一张商品封面，得到 `cover_ref`（随后放进商品的创建 / 修改请求体）。
 *
 * **契约 §5 没有列出这个端点**：后端 `qianye/modules/mall/model.go` 已经有
 * `qy_ml_cover` 表与 `qy_ml_cover_*` 七个错误码，公开取图走
 * `GET /api/qy/mall/covers/:ref`（契约 §5 末行），上传 / 退还两条按抽奖封面的
 * 形状（`POST /admin/lottery/covers` multipart `file`、`DELETE …/covers/:ref`）
 * 先行写死 —— 后端路由落地后核对路径与响应字段。
 */
export function uploadQyMallCover(file: File): Promise<QyMallCoverUpload> {
  const form = new FormData()
  form.append('file', file)
  return qyPost<QyMallCoverUpload>('/admin/mall/covers', form)
}

/**
 * 退还一张**还没绑到任何商品上**的上传。
 *
 * 待用上传有配额（`qy_ml_cover_pending_limit`）：换图不退，十次"选了又换"之后
 * 运营在宽限期到期前再也传不了图。只退本组件这一轮传上去的那几张。
 */
export function discardQyMallCover(ref: string): Promise<unknown> {
  return qyDelete<unknown>(`/admin/mall/covers/${encodeURIComponent(ref)}`)
}

/** 批量入库兑换码（≤ code_upload_max）。码本身不进任何日志。 */
export function uploadQyMallCodes(
  productNo: string,
  codes: string[]
): Promise<QyMallCodesUploadResult> {
  return qyPost<QyMallCodesUploadResult>(productPath(productNo, '/codes'), {
    codes,
  })
}

export function qyAdminMallOrdersQuery(params: QyMallAdminOrdersParams) {
  return queryOptions({
    queryKey: qyKeys.adminMallOrders(params),
    queryFn: () =>
      qyGet<QyPage<QyMallAdminOrder>>('/admin/mall/orders', params),
    staleTime: 15_000,
  })
}

export function shipQyMallOrder(
  orderNo: string,
  body: QyMallShipInput
): Promise<unknown> {
  return qyPost<unknown>(orderPath(orderNo, '/ship'), body)
}

/** 标失败并退星屑。 */
export function failQyMallOrder(
  orderNo: string,
  reason: string
): Promise<unknown> {
  return qyPost<unknown>(orderPath(orderNo, '/fail'), { reason })
}

/** 兑换码类：码标 revoked、退星屑。 */
export function revokeQyMallOrderCode(
  orderNo: string,
  reason: string
): Promise<unknown> {
  return qyPost<unknown>(orderPath(orderNo, '/revoke-code'), { reason })
}

/** 地址明文。每次读都写审计，**不进 react-query 缓存**：只在点开的那一刻拿一次。 */
export function revealQyMallOrderAddress(
  orderNo: string
): Promise<QyMallOrderAddress> {
  return qyGet<QyMallOrderAddress>(orderPath(orderNo, '/address'))
}

/** 套餐订单跨库两阶段的人工裁决（RootActionGate：超级管理员专属）。 */
export function adjudicateQyMallOrder(
  orderNo: string,
  body: QyMallAdjudicateInput
): Promise<unknown> {
  return qyPost<unknown>(orderPath(orderNo, '/adjudicate'), body)
}
