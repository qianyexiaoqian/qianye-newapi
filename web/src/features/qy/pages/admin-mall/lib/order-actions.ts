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
import type { QyMallOrder } from '../../mall/types'

/**
 * 管理员在一张订单上能做的事。
 *
 * 判据与后端 `qianye/modules/mall/order.go` 的状态机同源：
 *
 *   physical  paid → shipped(发货);shipped → done(标记完成,同一发货端点 done:true);
 *             paid | shipped → failed(标记失败,退星屑)
 *   code      done → revoked（撤回码，退星屑）
 *   plan      held → done | failed（裁决，超级管理员专属）
 *
 * 揭示地址只看 kind：地址密文到期会被清空（410 `qy_ml_address_pruned`），
 * 那是后端回答的事，前端不猜"过没过保留期"。
 *
 * 只算**该不该给按钮**。真正放行的是后端的 CAS（`WHERE status IN ?`），
 * 这里错了最多是多一颗点了吃 409 的按钮。
 */
export type QyMallAdminAction =
  | 'adjudicate'
  | 'complete'
  | 'fail'
  | 'reveal_address'
  | 'revoke_code'
  | 'ship'

export function qyMallAdminActions(
  order: Pick<QyMallOrder, 'kind' | 'status'>
): QyMallAdminAction[] {
  const out: QyMallAdminAction[] = []
  if (order.kind === 'physical') {
    if (order.status === 'paid') out.push('ship')
    if (order.status === 'shipped') out.push('complete')
    if (order.status === 'paid' || order.status === 'shipped') out.push('fail')
    out.push('reveal_address')
  }
  if (order.kind === 'code' && order.status === 'done') out.push('revoke_code')
  if (order.kind === 'plan' && order.status === 'held') out.push('adjudicate')
  return out
}
