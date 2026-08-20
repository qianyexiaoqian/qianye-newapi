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
import type { StatusVariant } from '@/components/status-badge'

import type { QyStatus } from './types'

/**
 * 全站统一的单据状态色板。
 *
 * 所有 qy 页面必须复用 `QyStatusBadge`，禁止各页自造颜色 —— 同一个
 * "pending" 在划转页是黄色、在提现页是灰色，用户会以为是两回事。
 *
 * `uncertain` 用告警色 + 呼吸动画是刻意的：它表示"钱可能已经动了但结果不可判定"，
 * 用失败色会误导用户去重复提交。
 */
type QyStatusStyle = {
  variant: StatusVariant
  labelKey: string
  pulse?: boolean
}

const STATUS_STYLES: Record<string, QyStatusStyle> = {
  pending: {
    variant: 'warning',
    labelKey: 'qy_common_st_pending',
    pulse: true,
  },
  processing: {
    variant: 'info',
    labelKey: 'qy_common_st_processing',
    pulse: true,
  },
  paying: { variant: 'info', labelKey: 'qy_common_st_paying', pulse: true },
  approved: { variant: 'info', labelKey: 'qy_common_st_approved' },
  success: { variant: 'success', labelKey: 'qy_common_st_success' },
  paid: { variant: 'success', labelKey: 'qy_common_st_paid' },
  failed: { variant: 'danger', labelKey: 'qy_common_st_failed' },
  rejected: { variant: 'danger', labelKey: 'qy_common_st_rejected' },
  cancelled: { variant: 'neutral', labelKey: 'qy_common_st_cancelled' },
  frozen: { variant: 'neutral', labelKey: 'qy_common_st_frozen' },
  reversed: { variant: 'violet', labelKey: 'qy_common_st_reversed' },
  // 结果不可判定 —— 资金系统的"交给人"出口，必须显眼但不能是失败色。
  uncertain: {
    variant: 'warning',
    labelKey: 'qy_common_st_uncertain',
    pulse: true,
  },
  // 主库 COMMIT 已发出、结局不明，系统正在用探针复判。
  //
  // 用 info + 呼吸而不是 warning：它与 uncertain 的差别正是"还轮不到人管"——
  // 用同一个告警色会把对账台的注意力分散到一批几十秒内就会自己收敛的单上，
  // 而 uncertain 才是真正需要人的那一档。也不能用 failed 的红：钱很可能已经动了。
  in_doubt: {
    variant: 'info',
    labelKey: 'qy_common_st_in_doubt',
    pulse: true,
  },

  // ── 工单（qianye/modules/ticket/status.go）──
  // 键名与其他单据不重叠，所以直接并进这张表而不是让工单页自己挑颜色 ——
  // "同一个状态两个页面两种颜色"正是这张表存在的理由。
  //
  // 配色的分界是**在等谁**：等客服的两档用告警色 + 呼吸（它们是待办），
  // 等用户的一档用信息色（球在对方那边），关闭用中性。
  open: { variant: 'warning', labelKey: 'qy_common_st_open', pulse: true },
  user_replied: {
    variant: 'warning',
    labelKey: 'qy_common_st_user_replied',
    pulse: true,
  },
  replied: { variant: 'info', labelKey: 'qy_common_st_replied' },
  closed: { variant: 'neutral', labelKey: 'qy_common_st_closed' },
}

const UNKNOWN_STYLE: QyStatusStyle = {
  variant: 'neutral',
  labelKey: '',
}

/**
 * 取状态样式。未登记的状态回落为中性徽章并原样显示字符串 ——
 * 后端新增枚举值时前端只应"不好看"，绝不能崩。
 */
export function getQyStatusStyle(
  status: QyStatus | null | undefined
): QyStatusStyle {
  if (status == null || status === '') return UNKNOWN_STYLE
  return STATUS_STYLES[status] ?? UNKNOWN_STYLE
}

/**
 * `qy_fund_orders.status`（int8）→ 稳定英文标识。
 *
 * 与后端 `qianye/model/fund_order.go` 的 `StatusName()` 保持一致：
 * 1 号位刻意留空，GORM 的 int8 零值 0 就是最安全的 pending。
 */
export function qyFundOrderStatusName(status: number): QyStatus {
  switch (status) {
    case 0:
      return 'pending'
    case 2:
      return 'success'
    case 3:
      return 'failed'
    case 4:
      return 'uncertain'
    case 5:
      return 'reversed'
    case 6:
      return 'in_doubt'
    default:
      return 'unknown'
  }
}
