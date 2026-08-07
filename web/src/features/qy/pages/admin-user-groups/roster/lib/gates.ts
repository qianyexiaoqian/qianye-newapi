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
import type {
  QyUgrImpact,
  QyUgrMigrationDiff,
  QyUgrUsableChange,
} from '../types'

/**
 * 删除按钮此刻能不能按,以及为什么不能。
 *
 * ── 为什么把它抽成一个纯函数 ──
 *
 * 这四道闸门里有三道在后端也各有一份(`deletability`、迁移目标必填、
 * `LosesEverything` 的强制勾选),而两边各写一遍必然漂移。漂移的方向永远是
 * **按钮亮着、点下去 400** —— 运营会以为是系统坏了,重试三次,然后去问人。
 *
 * 所以这里只做一件事:把后端已经算好的结论(`deletable` / `block_reason` /
 * `diff.loses_everything`)翻译成按钮状态。**一个判据都不自己发明**,
 * 唯一属于前端的那条是"影响面还没拉回来"。
 */
export type QyUgrDeleteBlock =
  /** 服务端说了不能删(套餐引用 / block 残留 / default 档)。 */
  | 'blocked'
  /** 影响面还在路上。此时的一切数字都还不存在。 */
  | 'loading'
  /** 目标分组一个模型分组都用不了,必须显式勾选覆盖。 */
  | 'needs_ack'
  /** 这一档还有人,迁移目标必填。 */
  | 'needs_target'

export function qyUgrDeleteBlock(
  impact: QyUgrImpact | null,
  target: string,
  ack: boolean
): QyUgrDeleteBlock | null {
  if (impact == null) return 'loading'
  if (!impact.deletable) return 'blocked'
  if (impact.users > 0 && target === '') return 'needs_target'
  // `loses_everything` 只在 diff 真的算过(带 target 请求过一次)之后才可信。
  // 没有 diff 时不拦:此时要么没人要迁,要么目标还没选 —— 后者已经被上一条拦住。
  if (impact.diff?.loses_everything === true && !ack) return 'needs_ack'
  return null
}

/**
 * 把可用清单的变化按方向分成三堆。
 *
 * 三堆的后果完全不同,混在一个列表里读不出来:
 *
 *	removed  这批人手上指向这些模型分组的令牌,在迁移完成的那一刻同时 403
 *	added    他们多了几个池子可选 —— 通常是好事,但也可能是一次意外的开放
 *	repriced 从下一秒开始的账单变化。数字是**十进制字符串**,不是 number
 */
export function qyUgrGroupChanges(diff: QyUgrMigrationDiff | null | undefined) {
  const removed: QyUgrUsableChange[] = []
  const added: QyUgrUsableChange[] = []
  const repriced: QyUgrUsableChange[] = []
  for (const change of diff?.changes ?? []) {
    if (change.kind === 'removed') removed.push(change)
    else if (change.kind === 'added') added.push(change)
    else repriced.push(change)
  }
  return { added, removed, repriced }
}
