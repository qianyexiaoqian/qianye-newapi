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
  QyLotEntryStatus,
  QyLotMissing,
  QyLotOutcome,
  QyLotPayoutStatus,
  QyLotStatus,
} from '../types'

/**
 * 抽奖 / 竞猜的取值 → 展示。
 *
 * 集中在一个文件是因为同一个 `settling` 在大厅、详情、我的记录、管理端列表里
 * 各出现一次；四处各写一份 `switch` 是本仓反复出现的漂移形状，而在这里
 * "结算中"被误写成"已结束"，用户会以为钱不会到账了。
 */

/**
 * 活动状态 → 全站统一徽章的取值。
 *
 * `settling`（已开奖、正在逐笔发钱）刻意映射成 `paying` 而不是 `processing`：
 * 用户此刻最需要知道的是"钱在路上"，而不是一个含糊的"处理中"。
 */
const ACTIVITY_BADGE: Record<string, QyStatus> = {
  draft: 'pending',
  published: 'processing',
  locked: 'frozen',
  settling: 'paying',
  finished: 'success',
}

export function qyLotActivityBadgeStatus(status: QyLotStatus): QyStatus {
  return ACTIVITY_BADGE[status] ?? status
}

/**
 * 参与条目状态 → 徽章取值。
 *
 * `excluded` 映射成 `frozen` 而不是 `failed`：它表示"封盘时这一笔还没落定"，
 * 钱**可能已经扣了**，退款由资金单的终态驱动。显示成失败会让用户以为
 * 什么都没发生，然后发现余额少了。
 */
const ENTRY_BADGE: Record<string, QyStatus> = {
  pending: 'pending',
  success: 'success',
  failed: 'failed',
  excluded: 'frozen',
  refunded: 'reversed',
}

export function qyLotEntryBadgeStatus(status: QyLotEntryStatus): QyStatus {
  return ENTRY_BADGE[status] ?? status
}

/**
 * 派奖行状态 → 徽章取值。
 *
 * `held` 是"自动重试已放弃、等人工"，用告警色（`uncertain`）而不是失败色：
 * 这笔钱是用户赢的，平台没有放弃它，只是需要有人来处理。
 */
const PAYOUT_BADGE: Record<string, QyStatus> = {
  planned: 'pending',
  paying: 'paying',
  paid: 'paid',
  failed: 'failed',
  held: 'uncertain',
}

export function qyLotPayoutBadgeStatus(status: QyLotPayoutStatus): QyStatus {
  return PAYOUT_BADGE[status] ?? status
}

/** 结局 → i18n key。空结局（进行中）返回 `null`，调用方不渲染这一格。 */
export function qyLotOutcomeKey(outcome: QyLotOutcome): string | null {
  if (outcome === '') return null
  return `qy_lot_outcome_${outcome}`
}

/**
 * 一条没满足的条件 → i18n key。
 *
 * 未登记的 `code` 回落到一句通用文案并把 code 原样附在后面 —— 后端新增条件时
 * 前端最多"说得笼统"，绝不能显示成空白让用户对着一个看不懂的空行发呆。
 */
export function qyLotMissingKey(missing: QyLotMissing): string {
  return `qy_lot_miss_${missing.code}`
}

/**
 * 未开始 / 进行中 / 已结束三档倒计时的目标时刻与语义。
 *
 * 返回 `null` 表示没有倒计时可显示（已封盘、已结束）。把"倒计到哪一刻"这件事
 * 收在这里，是因为大厅卡片与详情页头必须显示同一个数字：一处倒计到 `close_at`、
 * 另一处倒计到 `draw_at`，用户会以为自己看错了。
 */
export function qyLotCountdown(
  activity: { open_at: number; close_at: number; draw_at: number },
  status: QyLotStatus,
  nowSeconds: number
): { labelKey: string; seconds: number } | null {
  if (status === 'published') {
    if (nowSeconds < activity.open_at) {
      return {
        labelKey: 'qy_lot_countdown_open',
        seconds: activity.open_at - nowSeconds,
      }
    }
    if (nowSeconds < activity.close_at) {
      return {
        labelKey: 'qy_lot_countdown_close',
        seconds: activity.close_at - nowSeconds,
      }
    }
  }
  if (status === 'locked' && nowSeconds < activity.draw_at) {
    return {
      labelKey: 'qy_lot_countdown_draw',
      seconds: activity.draw_at - nowSeconds,
    }
  }
  return null
}
