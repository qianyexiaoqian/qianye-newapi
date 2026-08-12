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
/**
 * 违规阈值在界面上的两组文案键：处置动作、阈值三态。
 *
 * 放在共享 lib 而不是各页自己拼字符串，是因为这两组都同时出现在**管理端与
 * 用户端**：管理端说「到 5 次封号」，用户端也说「到 5 次封号」，两处各拼一份的
 * 结果是改一处、另一处继续说旧口径 —— 而这两句话说的都是"这个人会不会被封"。
 */

/** 与后端 `PolicyAction*` 同集合。 */
const ENFORCEMENT_ACTIONS = ['record', 'restrict', 'ban'] as const

/**
 * 统计窗口「不限期限」的哨兵值，与后端 `violation.WindowUnlimited` 同值。
 *
 * ── 为什么前端也必须认识它 ──
 * 后端把这个值**原样**下发（`window_hours: -1`），刻意不折成一个具体小时数：
 * 折成 24 是撒谎（用户会以为等一天就清零），而它本来就不是一个小时数。
 * 所以每一处显示窗口的地方都要先问 `qyWindowIsUnlimited`，再决定说
 * 「{{hours}} 小时内累计」还是「累计（不限时间）」。
 *
 * 直接把它当数字渲染的后果是界面上出现「-1 小时内累计」。
 */
export const QY_WINDOW_UNLIMITED = -1

/** 这个窗口是不是「不限期限」。判据是**恰好等于**哨兵，与后端逐字同构。 */
export function qyWindowIsUnlimited(hours: number | undefined): boolean {
  return hours === QY_WINDOW_UNLIMITED
}

/**
 * 窗口从 `before` 改成 `next` 是不是**变长了**。与后端 `windowWidens` 同形。
 *
 * 用途只有一个：决定要不要在保存前弹二次确认。裸的 `next > before` 在这里是错的
 * —— 哨兵是 -1，比任何正数都小，于是「24 小时 → 不限期限」这个最激进的改动会被
 * 判成放宽，二次确认与影响面预览一起被跳过。方向不对称：漏判会让一批存量账号在
 * 管理员毫不知情的情况下越线，误判只是多点一次。
 */
export function qyWindowWidens(before: number, next: number): boolean {
  if (qyWindowIsUnlimited(next)) return !qyWindowIsUnlimited(before)
  if (qyWindowIsUnlimited(before)) return false
  return qyWindowEffectiveHours(next) > qyWindowEffectiveHours(before)
}

/**
 * 把窗口折成**实际生效**的小时数。与后端 `effectiveWindowHours` 同形：
 * 0 与无法识别的负数一律回落 24（那是这一列历来的"没配"语义），
 * 哨兵原样返回 —— 它不是小时数，任何算术都是错的。
 */
export function qyWindowEffectiveHours(hours: number | undefined): number {
  if (hours === QY_WINDOW_UNLIMITED) return QY_WINDOW_UNLIMITED
  if (hours == null || !Number.isFinite(hours) || hours <= 0) return 24
  return hours
}

/**
 * 把后端下发的处置动作折成一个**一定存在**的 i18n 键。
 *
 * 不直接拿 `t(\`qy_vio_policy_action_${action}\`)`：`action` 是服务端下发的自由
 * 字符串，取到一个没登记的值时 i18next 会把键名原样吐到界面上，于是用户读到的是
 * `qy_vio_policy_action_undefined`。空串与未知值一律按 `ban` 读 —— 与
 * `violation-bans-tab` 既有口径一致，且方向是**保守**的：把"会被封"说成
 * "只记录"会让用户以为没事，那是这三个词里唯一不可接受的错法。
 */
export function qyEnforcementActionKey(action: string | undefined): string {
  const found = ENFORCEMENT_ACTIONS.find((a) => a === action)
  return `qy_vio_policy_action_${found ?? 'ban'}`
}

/**
 * 阈值三态的文案键。**三态由后端下发，这里只负责翻译，不负责推导。**
 *
 * `unset` 与 `disabled` 在封号判定上完全等价（都不出线），但对着管理员时它们是
 * 两句不同的话：前者是"你还没配"，后者是"你配了但关着"。把它们显示成同一个 0
 * 正是项目方看到的现象 —— 六个类型全是 0，看起来像"0 次就封"，而"到多少次封号"
 * 这件事在界面上等于不存在。
 */
export function qyThresholdStateKey(
  state: string | undefined,
  windowHours?: number
): string {
  // 「不限期限」不是第四态 —— 它是同一个 active/disabled 里**窗口那一半**的写法。
  // 两句文案里的 `{{hours}}` 在哨兵下会渲染成「-1 小时内 3 次」，所以带窗口的
  // 那两句各有一个不带小时数的孪生键。unset 那一句本来就不提窗口，不需要分叉。
  const unlimited = qyWindowIsUnlimited(windowHours)
  switch (state) {
    case 'active':
      return unlimited
        ? 'qy_vcat_threshold_value_unlimited'
        : 'qy_vcat_threshold_value'
    case 'disabled':
      return unlimited
        ? 'qy_vcat_threshold_disabled_unlimited'
        : 'qy_vcat_threshold_disabled'
    default:
      return 'qy_vcat_threshold_unset'
  }
}

/** 用户端「距离处置还剩」那一格该显示什么。 */
export type QyRemainingDisplay =
  /** 已经被处置了。此时任何倒计时都是假的。 */
  | { kind: 'banned' }
  /** 当前一条生效的线都没有。 */
  | { kind: 'none' }
  /** 还差 `remaining` 次，落在 `line` 那条线上。 */
  | { kind: 'countdown'; remaining: number; line: 'account' | 'category' }

/**
 * 决定「距离封号还剩」那一格显示什么。
 *
 * ── 为什么不能在组件里就地写三元 ──
 * 这一格曾经写成 `ban_threshold > 0 ? remaining : 不限`，而 `remaining` 当时
 * 只算账号总量线。于是在"账号线 10、某一类 3"这种普通配置下：页面头条写
 * 「距离封号还剩 8」，同一屏下方的公示卡片写「距离封号还差 1 次」，
 * 而用户下一次命中就被封了；封号之后那个数字还会变成 7 —— 一个已经 403 的人，
 * 页面头条告诉他还有 7 次机会。
 *
 * 三件事必须一起判，缺一件就会重新产生一个同屏矛盾：
 *   1. **已封优先**。被处置之后倒计时一律作废，不管数字是几。
 *   2. **有没有线**看 `remaining_line`，不看 `ban_threshold` —— 账号线关着、
 *      某一类开着，是完全合法的配置，那种站点上 `ban_threshold` 是 0。
 *   3. `remaining` 已经是**两条线的最小值**（后端 `nearestThresholdLine`），
 *      前端不要再拿它跟 `ban_threshold` 做任何算术。
 *
 * 后端字段缺失（旧版本）时回退到账号总量线那一套旧判据，宁可少显示也不要
 * 显示一个更大的数字。
 */
export function qyRemainingDisplay(summary: {
  ban_threshold: number
  remaining: number
  remaining_line?: 'none' | 'account' | 'category'
  banned?: boolean
}): QyRemainingDisplay {
  if (summary.banned === true) return { kind: 'banned' }
  const line =
    summary.remaining_line ??
    (summary.ban_threshold > 0 ? 'account' : ('none' as const))
  if (line === 'none') return { kind: 'none' }
  return { kind: 'countdown', line, remaining: summary.remaining }
}

/** 「还剩几次」那条线的名字。用户要知道自己撞的是哪一条。 */
export function qyRemainingLineKey(line: 'account' | 'category'): string {
  return line === 'category'
    ? 'qy_vio_my_remaining_by_category'
    : 'qy_vio_my_remaining_by_account'
}
