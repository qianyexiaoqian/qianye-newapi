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
import type { QyLotActivityDetail } from '../types'

/**
 * 「这一次提交最多还能加几注」以及**是哪一条闸门定的**。
 *
 * ## 为什么必须把"哪一条"也算出来
 *
 * 三条闸门会在完全不同的时刻绑住同一次提交，而用户能做的事完全不同：
 *
 *  - `per_request` 单次批量上限 —— 这一批到顶了，**再提交一次**就能接着买；
 *  - `per_user`    每人参与上限 —— 这一场你买够了，再提交多少次都没用；
 *  - `total`       全场名额     —— 手快有手慢无，可能在你选号的这一分钟里被别人买光。
 *
 * 把它们合成一个数、说同一句"还能买 N 注"，用户读完会做出错的下一个动作。
 * 后端在活动行锁内也正是分三个错误码回答的（`qy_lot_too_many_picks` /
 * `qy_lot_user_cap` / `qy_lot_cap_reached`），这里只是把同一件事提到按下确认之前。
 *
 * ## 它只是提示，绝不是放行依据
 *
 * 权威判定在后端的活动行锁内。这里的数来自一次详情查询，用户盯着选号盘的这几
 * 分钟里全场名额可能已经变了 —— 所以后端仍然会拒，而那时回执里的
 * `accepted / total_quota / failed_code` 负责把"买成了几注"说清楚。
 */
export type QyLotSeatBinding = 'none' | 'per_request' | 'per_user' | 'total'

export type QyLotSeatCap = {
  /** 这一次提交最多能放几注。恒 ≥ 0。 */
  cap: number
  /** 定出 `cap` 的那一条闸门。`cap` 由多条并列定出时取语义最重的那条（见下）。 */
  binding: QyLotSeatBinding
  /** 单次批量上限（活动级可配，后端 `max_picks_per_request`）。 */
  perRequestCap: number
  /** 我在本场还能买几注；`null` = 本场没有每人上限。 */
  myRemaining: number | null
  /** 全场还剩几个名额；`null` = 本场没有全场上限。 */
  totalRemaining: number | null
}

/**
 * 老后端不下发 `max_picks_per_request` 时退回 1 注。
 *
 * 一个不下发这个字段的后端根本不认识 `picks`，多发几注只会被整批 400 —— 退回
 * 一个乐观的默认（比如 10）会让每一次多注提交都失败，而失败的原因在界面上看
 * 不出来。
 */
const LEGACY_PER_REQUEST_CAP = 1

/**
 * 算出这一次提交的可用注数与绑住它的那条闸门。
 *
 * 闸门相等时取**更重**的那一条：`total` > `per_user` > `per_request`。理由是
 * 用户的下一个动作按最重的那条来 —— 全场只剩 10 个名额而单次上限也是 10 时，
 * 说"一次最多买 10 注"会让他以为再提交一次还能买。
 */
export function qyLotSeatCap(
  activity: Pick<
    QyLotActivityDetail,
    'max_picks_per_request' | 'my_entries_remaining' | 'total_entries_remaining'
  >
): QyLotSeatCap {
  const perRequestCap = activity.max_picks_per_request ?? LEGACY_PER_REQUEST_CAP
  // `null` 与 `undefined` 都是"本场没有这道闸门"。老后端不下发
  // `total_entries_remaining`，那一支必须与"没有全场上限"完全同义。
  const myRemaining = activity.my_entries_remaining ?? null
  const totalRemaining = activity.total_entries_remaining ?? null

  let cap = Math.max(0, perRequestCap)
  let binding: QyLotSeatBinding = perRequestCap > 0 ? 'per_request' : 'none'
  if (myRemaining != null && myRemaining <= cap) {
    cap = Math.max(0, myRemaining)
    binding = 'per_user'
  }
  if (totalRemaining != null && totalRemaining <= cap) {
    cap = Math.max(0, totalRemaining)
    binding = 'total'
  }
  return { cap, binding, perRequestCap, myRemaining, totalRemaining }
}

/**
 * 一次 N 注提交要在服务端跑多久（秒，向上取整）。
 *
 * N 注在服务端是 N 次**串行**扣费：每一注一张独立资金单、一条链环、一份可复算
 * 回执 —— 那正是"每一注各自可复算"的实现方式，不能为了快合并掉。所以耗时与 N
 * 成正比，满配 999 注是一次三十几秒的请求，而**用户必须在按下确认之前知道这件
 * 事**：一个转了半分钟的按钮与一个卡死的页面，在屏幕上长得一模一样。
 *
 * 每注毫秒数由后端下发（管理端配置的 `entry_batch_ms_per_pick`，实测均值），
 * 用户端拿不到那份配置，所以这里带一个与后端同源的默认值。写死一份不同的数
 * 会让"预计 36 秒"在后端调整之后继续印着一个不再成立的秒数。
 */
export const QY_LOT_MEASURED_MS_PER_PICK = 36

export function qyLotBatchSeconds(
  picks: number,
  msPerPick = QY_LOT_MEASURED_MS_PER_PICK
): number {
  if (picks <= 0 || msPerPick <= 0) return 0
  return Math.ceil((picks * msPerPick) / 1000)
}
