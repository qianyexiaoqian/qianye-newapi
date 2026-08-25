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
import { formatQyQuotaLedger } from '../../../lib/format'
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

/**
 * `finished` 那一档的颜色由**结局**决定，而不是由状态。
 *
 * `finished` 本身不是一个结局，它只表示"不再变了"。把它一律染成 `success`
 * 绿色，等于对一场取消退款、一场人数不够流局、一场逾期未结算的活动都说
 * "一切顺利" —— 而用户刚发现钱退回来了。六种结局里只有 `drawn` 是真的开出了奖。
 *
 * 三种 void 与 cancelled 都是全额退款，但语义不同：`cancelled` 是平台主动撤下
 * （用中性色，它不是异常），三种 `void_*` 是规则触发的作废（用 `reversed`
 * 紫色，与全站"已冲正"的资金语义一致 —— 那笔钱确实原路回去了）。
 *
 * 文字仍由 {@link qyLotOutcomeKey} 那一枚副徽章给出，六种结局各一句；
 * 这里只负责让颜色别说反话。
 */
const OUTCOME_BADGE: Record<string, QyStatus> = {
  drawn: 'success',
  cancelled: 'cancelled',
  void_min_entries: 'reversed',
  void_no_winner: 'reversed',
  void_all_correct: 'reversed',
  void_deadline: 'reversed',
}

export function qyLotActivityBadgeStatus(
  status: QyLotStatus,
  outcome?: QyLotOutcome
): QyStatus {
  if (status === 'finished' && outcome != null && outcome !== '') {
    // 后端将来新增一种结局时回落到 `success`（老行为），不会白屏、也不会
    // 把一个没见过的字符串当成状态色渲染出来。
    return OUTCOME_BADGE[outcome] ?? 'success'
  }
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
 * `need` / `have` 是站内额度的那几条缺失项。
 *
 * 后端把这两个数一律按 `int64` 下发，单位是什么只有条件本身知道：
 * `account_age` 是天、`violation_hits` 是次，而这四条是 quota。把 quota 原样
 * 渲染出来，用户看到的是「余额未达门槛（需 5000000，当前 4999998）」—— 一串
 * 他在钱包页从来没见过的大整数，既算不出还差多少、也不知道要充多少。
 *
 * 新增 quota 口径的缺失项时**必须**同步加进这里；漏掉的后果只是文案退回原始
 * 整数，typecheck 与运行时都不会报错，所以另有一条源码级测试守着。
 */
const QUOTA_VALUED_MISSING: ReadonlySet<string> = new Set([
  'balance',
  'stake',
  'used_quota',
  'recent_spend',
])

/**
 * 一条缺失项交给 i18n 插值的 `need` / `have`。
 *
 * quota 口径的走站内额度格式化，其余（天数、次数）原样透传 —— 单位由文案自己
 * 补。`null` / `undefined` 回落成空串而不是 `0`：「需 0」是一句会误导人的假话。
 * 非数字（后端理论上可以塞 bool / string）也原样透传：那时不存在可换算的额度，
 * 硬塞进格式化函数只会得到一个 `-`，比原文更没信息。
 */
export function qyLotMissingValues(missing: QyLotMissing): {
  need: QyLotMissing['need']
  have: QyLotMissing['have']
} {
  const quota = QUOTA_VALUED_MISSING.has(missing.code)
  const render = (value: QyLotMissing['need']) => {
    if (value == null) return ''
    if (!quota || typeof value !== 'number') return value
    return formatQyQuotaLedger(value)
  }
  return { need: render(missing.need), have: render(missing.have) }
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

/**
 * 公开名单里那一串长标识的**展示层**打码。
 *
 * ## 名单里到底暴露了什么
 *
 * 三样东西，没有一样是用户名或用户 ID：
 *
 *   · `entry_no` —— 报名单号，`LE<日期>-<16 位十六进制>`。
 *   · `user_ref` —— 每场活动**独立加盐**的稳定标识（`commit.go` 的
 *     `UserRef` = sha256(域串, ref_salt, user_id) 取前 32 个十六进制字符）。
 *     盐永不公开也不进 commit 原像，所以它跨场无法关联、也无法反查回用户 ID。
 *   · `amount` / `opt_no` / `pick` / 链哈希 —— 这一注本身的内容。
 *
 * 也就是说这份名单**本来就没有直接的个人身份**。仍然要打码，是因为标识虽不是
 * 身份，却是一条**场内的关联线**：谁把自己的报名回执发到群里（截图、晒中奖），
 * 谁在这一场里的每一注就都被钉在了一起。打掉中间段之后，肉眼扫一遍表格不再能
 * 顺手把几十行归并到同一个人身上。
 *
 * ## 硬约束：证据链的原值一个字节都不能动
 *
 * 打的是**展示层**。接口下发的仍然是完整的 `entry_no` / `user_ref`，本地复算
 * （`lib/verify.ts`）读的也是那一份原值，第三方拿证据链端点复算的更是原值 ——
 * 所以打码之后公正性验证照常成立。名单卡上另有一个"显示完整标识"的开关，
 * 要逐行比对哈希的人打开它即可，而那是一次刻意动作。
 *
 * ## 为什么保留首尾而不是整串换成 `***`
 *
 * 用户要能在几百行里找到**自己那一行**：报名回执上印的是完整单号，首 6 位与
 * 末 4 位足够他确认"这一行是我的"，却不足以让旁人把两行归并到一起。
 * 短到不够打码的串原样返回 —— 截断一个 8 字符的串只会让它既不可读也不安全。
 */
export function qyLotMaskRef(value: string): string {
  const head = 6
  const tail = 4
  // 少于 head + tail + 4 时打码没有意义：中间只剩两三个字符，遮住它既不减少
  // 可关联性，又让人认不出自己那一行。
  if (value.length < head + tail + 4) return value
  return `${value.slice(0, head)}…${value.slice(-tail)}`
}
