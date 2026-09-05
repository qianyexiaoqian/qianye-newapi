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

import type { QyStatus, QyTimelineItem } from '../../../lib/types'
import type {
  QyMallOrder,
  QyMallOrderEvent,
  QyMallOrderStatus,
  QyMallProductDetail,
  QyMallProductKind,
} from '../types'

/**
 * 「要买这一件」—— 从商品详情弹窗交给下单弹窗的那一份意图。
 *
 * 套餐商品把用户在详情里**看到并确认过**的预览动作原样带过去（`expect_*`），
 * 后端在主库事务里复核，不一致即 `qy_ml_plan_state_changed`。它们必须在
 * 打开下单弹窗那一刻定死，而不是提交时再读一次详情 —— 再读一次得到的可能
 * 已经是另一个结论，而用户确认的是上一个。
 */
export type QyMallOrderTarget = {
  product: QyMallProductDetail
  expectAction?: string
  expectSuperseded?: string[]
}

/**
 * 订单在界面上怎么说：徽章颜色、文案、时间线。
 *
 * 三种商品共用一套状态字面量（契约 §4），但同一个 `paid` 在实物上是「待发货」、
 * 在套餐上是「订阅处理中」—— 文字必须按 kind 分开说，颜色则借用全站色板里
 * 语义最近的那一档（`QyStatusBadge` 只认那张表）。
 */

export type QyMallOrderStatusView = {
  /** 交给 `QyStatusBadge` 定颜色的那个状态。 */
  status: QyStatus
  /** 徽章上的文字。未登记的状态为 `null`，徽章原样显示字符串。 */
  labelKey: string | null
}

const STATUS_VIEW: Record<string, QyMallOrderStatusView> = {
  // 已付款还没到终态：实物等发货、套餐等主库发订阅。info + 呼吸 = "在处理"。
  paid: { status: 'processing', labelKey: 'qy_ml_st_paid' },
  shipped: { status: 'approved', labelKey: 'qy_ml_st_shipped' },
  done: { status: 'success', labelKey: 'qy_ml_st_done' },
  cancelled: { status: 'cancelled', labelKey: 'qy_ml_st_cancelled' },
  failed: { status: 'failed', labelKey: 'qy_ml_st_failed' },
  // 「待核对」：钱扣了、主库动没动不知道，交给人。与 uncertain 同一档告警色，
  // 绝不能用失败色 —— 用户会以为没成功而再买一次。
  held: { status: 'uncertain', labelKey: 'qy_ml_st_held' },
  revoked: { status: 'reversed', labelKey: 'qy_ml_st_revoked' },
}

/**
 * 奖品单（`source='lottery'`）在同一个 `paid` 下还多两种处境，都要单独说：
 *   · 实物奖品单还没补填地址 —— 下一步在**用户**手里，不是"等发货"；
 *   · 兑换码奖品单 `paid` = 中奖时库存里没有未用码，等管理员补码后自动发放。
 */
export type QyMallOrderPrizeFlags = Pick<
  QyMallOrder,
  'address_missing' | 'source'
>

export function qyMallOrderStatusView(
  status: QyMallOrderStatus,
  kind: QyMallProductKind,
  prize?: QyMallOrderPrizeFlags
): QyMallOrderStatusView {
  const view = STATUS_VIEW[status]
  if (view == null) return { status, labelKey: null }
  if (status === 'paid' && kind === 'physical') {
    // 地址还没填时说"待发货"是错的：卡住这张单的是用户自己那一步。
    if (prize?.address_missing === true) {
      return { status: 'pending', labelKey: 'qy_ml_st_address_missing' }
    }
    // 实物的「已支付」对用户意味着"等发货"，说成"处理中"他会去问客服在处理什么。
    return { status: view.status, labelKey: 'qy_ml_st_paid_physical' }
  }
  if (status === 'paid' && kind === 'code' && prize?.source === 'lottery') {
    return { status: view.status, labelKey: 'qy_ml_st_code_pending' }
  }
  return view
}

/** 这张单是不是抽奖 / 转盘中的奖品（价格恒 0，不是"免费商品"）。 */
export function isQyMallPrizeOrder(
  order: Pick<QyMallOrder, 'source'>
): boolean {
  return order.source === 'lottery'
}

/** 事件流水的动作标签。未知动作原样显示，后端加新动作时前端不该白屏。 */
export function qyMallEventKey(action: string): string {
  return `qy_ml_ev_${action}`
}

/** 未完结的三个状态（与后端 `openStatuses` 同一份）。 */
const OPEN_STATUSES: ReadonlySet<string> = new Set(['paid', 'shipped', 'held'])

/** 这三种动作是"往回走"，节点画成失败色。 */
const REVERSAL_ACTIONS: ReadonlySet<string> = new Set([
  'cancel',
  'fail',
  'revoke_code',
])

/**
 * 订单时间线：已发生的事件逐条画出来，**未到达的下一步保留灰色占位**。
 *
 * 只画已发生的部分会让一张「已支付」的实物单看起来像卡死了 —— 其实只是还没
 * 轮到发货。占位节点按 kind × status 各说各的："等发货"、"等订阅发放"、
 * "待核对" 三句不能塌成一句"处理中"。
 *
 * 事件在 `events` 里按时间正序，`(at, action)` 在一张单上唯一，可以做 key。
 * 后端 note 是中文硬编码（"已支付 100 星屑"），原样作为 description 显示。
 */
export function buildQyMallTimeline(
  order: Pick<QyMallOrder, 'created_at' | 'kind' | 'status'> &
    Partial<QyMallOrderPrizeFlags>,
  events: QyMallOrderEvent[],
  t: TFunction
): QyTimelineItem[] {
  const open = OPEN_STATUSES.has(order.status)
  const items: QyTimelineItem[] = events.map((event, index) => {
    const isLast = index === events.length - 1
    let state: QyTimelineItem['state'] = 'done'
    if (REVERSAL_ACTIONS.has(event.action)) state = 'failed'
    else if (isLast && open) state = 'current'
    return {
      key: `${event.at}-${event.action}-${index}`,
      title: t(qyMallEventKey(event.action), { defaultValue: event.action }),
      timestamp: event.at,
      state,
      description: event.note === '' ? undefined : event.note,
    }
  })

  // 一条事件都没有（后端还没写、或极旧的单）：至少把下单那一刻画出来，
  // 否则时间线整块消失，用户连"这单什么时候下的"都看不到。
  if (items.length === 0) {
    items.push({
      key: 'created',
      title: t('qy_ml_ev_pay'),
      timestamp: order.created_at,
      state: open ? 'current' : 'done',
    })
  }

  const next = nextStepKey(order)
  if (next != null) {
    items.push({
      key: `next-${order.status}`,
      title: t(next),
      state: order.status === 'held' ? 'current' : 'pending',
    })
  }
  return items
}

/** 未完结的单接下来会发生什么。终态返回 `null`。 */
function nextStepKey(
  order: Pick<QyMallOrder, 'kind' | 'status'> & Partial<QyMallOrderPrizeFlags>
): string | null {
  if (order.status === 'held') return 'qy_ml_tl_next_held'
  if (order.kind === 'physical') {
    // 奖品单没填地址之前，下一步是"你先填地址"，不是"等发货"。
    if (order.status === 'paid' && order.address_missing === true) {
      return 'qy_ml_tl_next_address'
    }
    if (order.status === 'paid') return 'qy_ml_tl_next_ship'
    if (order.status === 'shipped') return 'qy_ml_tl_next_done'
    return null
  }
  if (order.kind === 'code' && order.status === 'paid') {
    return 'qy_ml_tl_next_code'
  }
  if (order.kind === 'plan' && order.status === 'paid') {
    return 'qy_ml_tl_next_plan'
  }
  return null
}
