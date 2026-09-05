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
import type { QyStatus } from '../../../lib/types'
import type {
  QyStardustAccrualStatus,
  QyStardustHoldReason,
  QyStardustLedgerKind,
} from '../types'

/**
 * 星屑流水 / 日桶在界面上的**展示口径**。用户端三张标签与管理端四张标签共用，
 * 所以放在 `.ts` 里而不是某一个组件文件里：同一个 `held` 在用户端是灰色、
 * 在管理端是红色，用户与运营对着同一行会以为是两回事。
 */

/**
 * 流水种类，顺序与契约 §2 的枚举逐字一致。筛选下拉按这个顺序渲染；
 * 文案走 `t(qySdKindKey(kind))`，键在 `QY_DYNAMIC_KEYS` 里登记。
 */
export const QY_SD_LEDGER_KINDS: readonly QyStardustLedgerKind[] = [
  'consume_rebate',
  // 下线消费返（D-14）：邀请人按自己的分组档、就下线当日消费拿的那一份。
  // 它与 consume_rebate 是两笔不同的账（一笔给消费者自己、一笔给他的上线）。
  'invite_consume',
  'invite_topup',
  'invite_redeem',
  'invite_register',
  'plan_buyer',
  'plan_inviter',
  'lot_stake',
  'lot_prize',
  'lot_refund',
  'mall_order',
  'mall_refund',
  'manual',
]

/** 日桶的三种状态，顺序 = 一桶的生命周期。管理端筛选下拉按它渲染。 */
export const QY_SD_ACCRUAL_STATUSES: readonly QyStardustAccrualStatus[] = [
  'computed',
  'settled',
  'held',
]

/** `kind` → i18n 键。未知 kind 得到一个不存在的键，调用方要带 `defaultValue`。 */
export function qySdKindKey(kind: string): string {
  return `qy_sd_kind_${kind}`
}

/**
 * 日桶状态 → 全站统一徽章的颜色 + 文字键。
 *
 * 颜色复用 `QyStatusBadge` 已有的三档而不是自己挑：`computed` 是"算出来了、
 * 等下一次结算发"（待处理，呼吸），`settled` 是已入账（成功），`held` 是被
 * 扣住、要人来看（冻结）。文字用本模块自己的键，因为「已成功」放在一桶消费
 * 返上说不通 —— 用户要读到的是「已发放」。
 */
export function qySdAccrualBadge(status: string): {
  status: QyStatus
  labelKey: string
} {
  switch (status) {
    case 'computed':
      return { status: 'pending', labelKey: 'qy_sd_accrual_st_computed' }
    case 'settled':
      return { status: 'success', labelKey: 'qy_sd_accrual_st_settled' }
    case 'held':
      return { status: 'frozen', labelKey: 'qy_sd_accrual_st_held' }
    default:
      // 后端新增状态时只是"不好看"（中性徽章 + 原样字符串），绝不能崩。
      return { status, labelKey: '' }
  }
}

/**
 * 暂缓原因 → i18n 键。三种已知原因各有一句人话（design-15 §4.1）；
 * 空串表示没有被扣住，返回 `null`；未知原因回落到带原文的通用句。
 */
export function qySdHoldReasonKey(reason: QyStardustHoldReason): string | null {
  switch (reason) {
    case '':
      return null
    case 'overdraft':
      return 'qy_sd_hold_overdraft'
    case 'account_removed':
      return 'qy_sd_hold_account_removed'
    case 'account_disabled':
      return 'qy_sd_hold_account_disabled'
    default:
      return 'qy_sd_hold_other'
  }
}

/**
 * 桶日 `YYYYMMDD` → `YYYY-MM-DD`。
 *
 * 桶日是结算日界下的一个**日键**，不是某个时刻：把它喂给 dayjs 会按浏览器时区
 * 解释成一个瞬间，在 UTC-7 的机器上渲染成前一天。所以这里只做字符串切分，
 * 认不出形状的原样返回（后端保证它是八位数字，认不出说明契约变了，不猜）。
 */
export function qySdFormatDay(bucketDate: string): string {
  if (!/^\d{8}$/.test(bucketDate)) return bucketDate
  return `${bucketDate.slice(0, 4)}-${bucketDate.slice(4, 6)}-${bucketDate.slice(6, 8)}`
}

/** 万分比 → 百分数文本：`10000` → `100`，`1250` → `12.5`。 */
export function qySdBpsPercent(bps: number): string {
  const percent = bps / 100
  return Number.isInteger(percent)
    ? String(percent)
    : String(Number(percent.toFixed(4)))
}
