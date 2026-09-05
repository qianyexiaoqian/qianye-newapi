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
import type { QyLotHallPhase } from '../lottery/api'
import type { QyLotPrizeType } from '../lottery/types'

/**
 * 星屑转盘（`kind='draw', draw_mode='wheel'`）的 DTO。与契约 §6 逐字对应，
 * 分页形状按后端实际落地（design-15 §12.1）：参数 `p` / `page_size`，响应
 * `{items,total,p,page_size}` —— 与 lottery 模块其余列表同一套，不是星屑 / 商城
 * 那批新接口的 `page`。
 *
 * 活动列表与详情复用 `pages/lottery/types.ts` 的 `QyLotActivityBrief` /
 * `QyLotActivityDetail`：转盘就是一场活动，奖档多了 `stock_left` 与
 * `prize_type='none'`（服务端派生的"谢谢参与"行，`win_ppm = 1e6 − Σ其余`）。
 * 列表行对转盘额外带 `tiers[]`（卡片要画"剩余 / 初始"）。
 */

/** 转盘页的列表参数。`draw_mode=wheel` 由 `api.ts` 固定补上，不在这里。 */
export type QyWheelListParams = {
  p: number
  page_size: number
  /** `live` = 进行中（published/locked/settling），`ended` = 已结束。 */
  phase?: QyLotHallPhase
}

/** 「我的转动」分页参数。 */
export type QyWheelSpinPageParams = {
  p: number
  page_size: number
}

export type QyWheelSpinInput = {
  /** 每次打开转动弹窗生成一次、重试沿用（幂等键）。 */
  client_request_id: string
  /**
   * 客户端种子（≤64，`[0-9a-zA-Z_-]`），进票面原像与证据链。
   *
   * 它是这一转里**唯一由用户决定**的输入：前端默认随机生成一份、允许用户改，
   * 回执与「我的转动」都原样带着它 —— 事后拿证据链复算这一转时要用。
   */
  client_seed?: string
  pay_password?: string
}

export type QyWheelSpinResult = {
  entry_no: string
  seq: number
  /** 摇号量 ∈ [0, 999999]。 */
  ppm: number
  /** 中的档；0 = 未中（落在"谢谢参与"那一段，或摇中的档已发完）。 */
  result_tier: number
  /** 摇中却已发完、于是落空的那一档；0 = 没发生。 */
  exhausted_tier: number
  /** 中奖档的单份星屑；text / product / none 恒 0。 */
  amount: number
  prize_type: QyLotPrizeType
  /**
   * 商品奖（`prize_type='product'`）专属：中的是哪件商品、当场生成了哪张
   * 0 星屑的商城订单。其余档为空串。码 / 发货 / 订阅全在那张单上。
   */
  product_no?: string
  mall_order_no?: string
  /** 这一转的链环 —— 用户手里的凭据。 */
  chain_head: string
  /** 同一个请求号原样重放：拿回的是原来那一转，没有再摇一次。 */
  replayed: boolean
}

export type QyWheelMySpin = {
  entry_no: string
  seq: number
  ppm: number
  result_tier: number
  exhausted_tier: number
  amount: number
  prize_type: QyLotPrizeType
  /** 商品奖专属（同 {@link QyWheelSpinResult}）。 */
  product_no?: string
  mall_order_no?: string
  client_seed: string
  /** 这一转的链环（回执弹窗关掉就没了，这一份长期留着）。 */
  chain_hash: string
  created_at: number
}
