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

import type { QyTimelineItem } from '../../../lib/types'
import type { QyWithdrawal } from '../../withdraw/types'

/**
 * 提现单的三段式时间线：提交 → 审核 → 打款/到账。
 *
 * 这是需求原文三个问题的直接答案，因此每一节点的 description 都必须落到
 * **具体的事实**上，不能只写状态名：
 *   - 什么时候拒绝的 → 审核节点的 `reviewed_at` + `reject_reason`
 *   - 什么时候打的款 → 打款节点的 `paid_at` + `payout_ref`
 *   - 为什么失败     → 打款节点的 `fail_reason`
 *
 * 未到达的节点保留灰色占位而不是隐藏：提现要走 3 步，只画已发生的部分会让
 * 用户以为流程卡死了，实际上只是还没轮到。
 *
 * 返回值的 description 一律是**纯字符串**，这样本函数可以待在 `.ts` 里被
 * 单独测试，也不必为了拼一行说明把整份时间线搬进组件。
 */
export function buildQyWithdrawTimeline(
  withdrawal: QyWithdrawal,
  t: TFunction
): QyTimelineItem[] {
  const status = withdrawal.status
  const rejected = status === 'rejected'
  const cancelled = status === 'cancelled'
  const failed = status === 'failed'
  const paid = status === 'paid'
  const reviewed = withdrawal.reviewed_at > 0

  const submit: QyTimelineItem = {
    key: 'submit',
    title: t('qy_wd_tl_submitted'),
    timestamp: withdrawal.created_at,
    state: 'done',
    description: t('qy_wd_tl_submitted_desc', { no: withdrawal.withdraw_no }),
  }

  const review: QyTimelineItem = {
    key: 'review',
    title: rejected ? t('qy_wd_tl_rejected') : t('qy_wd_tl_review'),
    timestamp: withdrawal.reviewed_at,
    state: reviewState(),
    description: reviewDescription(),
  }

  const payout: QyTimelineItem = {
    key: 'payout',
    title: payoutTitle(),
    timestamp: withdrawal.paid_at,
    state: payoutState(),
    description: payoutDescription(),
  }

  // 撤销是用户自己终止流程，后面两步永远不会发生，画出来只会误导。
  if (cancelled) {
    return [
      submit,
      {
        key: 'cancelled',
        title: t('qy_wd_tl_cancelled'),
        timestamp: withdrawal.updated_at,
        state: 'done',
        description: t('qy_wd_tl_cancelled_desc'),
      },
    ]
  }
  return [submit, review, payout]

  function reviewState(): QyTimelineItem['state'] {
    if (rejected) return 'failed'
    if (reviewed) return 'done'
    return status === 'pending' ? 'current' : 'pending'
  }

  function reviewDescription(): string {
    if (rejected) {
      return withdrawal.reject_reason === ''
        ? t('qy_wd_tl_rejected_no_reason')
        : t('qy_wd_tl_rejected_desc', { reason: withdrawal.reject_reason })
    }
    if (reviewed) return t('qy_wd_tl_approved_desc')
    return t('qy_wd_tl_review_waiting')
  }

  function payoutTitle(): string {
    if (failed) return t('qy_wd_tl_failed')
    return withdrawal.method === 'fiat'
      ? t('qy_wd_tl_payout')
      : t('qy_wd_tl_credit')
  }

  function payoutState(): QyTimelineItem['state'] {
    if (paid) return 'done'
    if (failed) return 'failed'
    if (status === 'approved' || status === 'paying') return 'current'
    return 'pending'
  }

  function payoutDescription(): string {
    if (paid) {
      return withdrawal.payout_ref === ''
        ? t('qy_wd_tl_paid_desc')
        : t('qy_wd_tl_paid_ref_desc', { ref: withdrawal.payout_ref })
    }
    if (failed) {
      return withdrawal.fail_reason === ''
        ? t('qy_wd_tl_failed_no_reason')
        : t('qy_wd_tl_failed_desc', { reason: withdrawal.fail_reason })
    }
    if (rejected) return t('qy_wd_tl_payout_skipped')
    return t('qy_wd_tl_payout_waiting')
  }
}

/** 事件流水的动作标签。未知动作原样显示，后端加新动作时前端不该白屏。 */
export function qyWithdrawActionKey(action: string): string {
  return `qy_wd_ev_${action}`
}
