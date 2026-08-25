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
import type { QyLotDraft } from './draft'

/**
 * 建活动向导里每一个数值字段的**推荐值与判据**。
 *
 * ## 它替换掉的形态
 *
 * 项目方原话：「创建活动，你不告诉我要怎么设置推荐值？一堆这种『固定奖级的
 * 预算（额度 × 份数）必须不小于全场参与上限，否则超募时会有中奖者被摊薄到 0
 * 而拿不到钱』很烦啊」
 *
 * 那句话描述的循环是：填完 → 提交 → 被拒 → 读一段解释 → 改 → 再提交。而它
 * 之所以烦，是因为那条判据只有三个量（份数 × 单份 ≥ 全场参与上限），**给定
 * 其中两个，第三个是算出来的**——从来就没有需要运营去猜的余地。所以这里把
 * 每一条这样的不等式解出来，界面直接把答案填进去。
 *
 * ## 为什么推荐值与校验判据必须是同一个函数
 *
 * 一个"界面说可以、提交被拒"的推荐值比不给推荐值更糟：前者会让人以为界面坏了，
 * 而且再也不信任何一个自动填。所以 {@link qyLotTierBudgetShort} 是这条判据在
 * 前端的**唯一**实现——`draft.ts` 的跨步校验、字段旁边的实时提示、以及自动填
 * 按钮全部经过它，而每一个 `*Floor` 都以"喂回这个判据必然为假"为契约
 * （`__tests__/advice.test.ts` 逐条断言，后端同一条见
 * `qianye/modules/lottery/advice_test.go`）。
 *
 * ## 零值一律是"此刻还算不出来"
 *
 * 运营清空某一格的瞬间，另一格的推荐值就没有意义了（ceil(cap / 0) 不是一个数）。
 * 这些函数在那种时候返回 `0`，调用方据此**不渲染**推荐值那一行，而不是渲染一个
 * `NaN` 或者 `Infinity`。
 */

/** 向上取整的整数除法。两个入参都必须为正，调用方先判。 */
function ceilDiv(numerator: number, denominator: number): number {
  return Math.ceil(numerator / denominator)
}

/**
 * 本场理论上可能出现的最大有效票数。
 *
 * `max_total_entries` 填 0 时后端会归一成系统硬上限（名单冻结必须有上界，
 * 见 `buildActivity`），所以推荐值也必须按硬上限算 —— 按 0 算会得到一个
 * "界面填好、提交被拒"的推荐值。
 */
export function qyLotEntriesCap(
  draft: QyLotDraft,
  maxTotalEntriesHard: number | undefined
): number {
  if (draft.max_total_entries > 0) return draft.max_total_entries
  return maxTotalEntriesHard ?? 0
}

/**
 * 「这一档超募时会有人被摊薄到 0」——**判据本身**。
 *
 * 后端两处（概率制的 `normalizeWinPpm`、双色球的 `checkBallTierInput`）用的是
 * 同一条 `count × amount < entriesCap`。`entriesCap <= 0` 时后端根本不会走到
 * 这条判定（它一定是正数），所以这里也返回 `false` 而不是"拿不准就报错"。
 */
export function qyLotTierBudgetShort(
  entriesCap: number,
  count: number,
  amountQuota: number
): boolean {
  if (entriesCap <= 0) return false
  return amountQuota * count < entriesCap
}

/**
 * 单份至少要填多少额度 —— 由份数与全场参与上限**解出来**的那个数。
 *
 * 向上取整，不是向下：向下会得到一个恰好差一格的推荐值，而那正是"界面说 OK、
 * 后端拒绝"的典型来源。
 */
export function qyLotTierAmountFloor(
  entriesCap: number,
  count: number
): number {
  if (entriesCap <= 0 || count <= 0) return 0
  return ceilDiv(entriesCap, count)
}

/** 同一条不等式的另一个解：单份定死时，份数至少要几份。 */
export function qyLotTierCountFloor(
  entriesCap: number,
  amountQuota: number
): number {
  if (entriesCap <= 0 || amountQuota <= 0) return 0
  return ceilDiv(entriesCap, amountQuota)
}

/**
 * 概率制：这一档还能分到多少 ppm。
 *
 * Σ 各档概率 ≤ 100%（后端 `buildPrizes` 累加到 `PpmDen` 就拒），所以"这一格
 * 能填多大"完全由其余各档决定。剩余为 0 时返回 0 —— 那意味着概率已经分完，
 * 要给这一档腾地方只能先调小别的档。
 */
export function qyLotWinPpmHeadroom(draft: QyLotDraft, tierNo: number): number {
  const used = draft.tiers
    .filter((tier) => tier.tier !== tierNo)
    .reduce((sum, tier) => sum + Math.max(0, tier.win_ppm ?? 0), 0)
  return Math.max(0, 1_000_000 - used)
}

/**
 * 双色球浮动奖：这一档还能占池子的多少万分比。
 *
 * Σ 占池比例 ≤ 10000（后端 `checkBallPoolCovers`），同 {@link qyLotWinPpmHeadroom}。
 */
export function qyLotPoolShareHeadroom(
  draft: QyLotDraft,
  tierNo: number
): number {
  const used = draft.tiers
    .filter((tier) => tier.tier !== tierNo)
    .reduce((sum, tier) => sum + Math.max(0, tier.pool_share_bps ?? 0), 0)
  return Math.max(0, 10_000 - used)
}

/**
 * 竞猜单注上限的推荐值 = 单注额 × {@link QY_LOT_BET_MAX_MULTIPLE}。
 *
 * ## 为什么给一个非零默认值，而不是只加一句提醒
 *
 * `0 = 不限` 是后端的 wire 语义，改不得（改了会让所有已经配过 0 的活动换一个
 * 意思）。但"没填"与"我确实要不限"在表单上是同一个 0，而两者的代价差一个
 * 量级：没有上限时一个大户可以在封盘前几秒压满获胜选项吃掉整个奖池，散户的
 * 期望收益归零。让默认落在**安全的一侧**，想要不限的人手动清成 0 —— 那是一次
 * 刻意动作，而刻意动作正是这一格该有的成本。
 *
 * 倍数选 20 的理由是可以说出口的一句话：一个大户最多顶 20 个按单注额下注的
 * 普通参与者。它不是一条数学定理，所以界面上写的是"推荐值"而不是"必须"，
 * 而且这一格从头到尾都可以改。
 */
export const QY_LOT_BET_MAX_MULTIPLE = 20

/**
 * @param ceilingQuota 这一格的硬上界（系统上界与站点 `max_stake_quota` 里更紧的
 *   那一个；0 = 拿不到，此时不夹）。**必须传**：单注额 × 20 越过系统上界的场次
 *   （参与费 > 系统上界 ÷ 20）会算出一个后端必拒的推荐值，而一个"界面说可以、
 *   提交被拒"的推荐值比不给推荐值更糟 —— 那正是本文件开头写的存在理由。
 *   夹到上界之后倍数说不满 20，但它仍然是这一格能填的最大值，而且提交必过。
 */
export function qyLotRecommendedBetMax(
  stakeQuota: number,
  ceilingQuota: number
): number {
  if (stakeQuota <= 0) return 0
  const advised = stakeQuota * QY_LOT_BET_MAX_MULTIPLE
  if (ceilingQuota > 0 && advised > ceilingQuota) return ceilingQuota
  return advised
}

/**
 * 最低成场人数的推荐值 = 保本参与人数。
 *
 * 不足即流局、全额退款，是平台侧唯一的止损阀。填 0（默认）意味着"3 个人参加、
 * 平台净亏一个一等奖"也照开 —— 那不是一个人**选**出来的取值，而是没人告诉他
 * 该填什么。保本人数正好是 ⌈奖品总额 ÷ 参与费⌉，两个量表单上都有。
 *
 * 双色球不适用：浮动奖档的额度恒为 0，奖品总额算出来是一个只发几百额度的
 * 假数，而它的支出由期次池兜底。
 */
export function qyLotRecommendedMinEntries(
  draft: QyLotDraft,
  entriesCap: number
): number {
  if (draft.kind !== 'draw' || draft.draw_mode === 'ball') return 0
  const total = draft.tiers.reduce(
    (sum, tier) =>
      sum + Math.max(0, tier.amount_quota) * Math.max(0, tier.count),
    0
  )
  if (total <= 0 || draft.stake_quota <= 0) return 0
  const breakEven = ceilDiv(total, draft.stake_quota)
  // 保本人数超过**本场理论最大票数**时，这一场卖光了也保不了本。把它填进去
  // 等于一键造出一场数学上必然流局的活动（成场线永远够不到 → 全额退款、
  // 一场空开），而且 min_entries_to_hold 进承诺哈希、发布后改不了。
  //
  // 这种时候没有可推荐的值：要么调小奖品，要么调高参与费，那是运营的取舍，
  // 不是这条不等式能解出来的数。按本文件的零值口径返回 0 —— 调用方据此不渲染
  // 推荐值那一行，也不给按钮。
  if (entriesCap > 0 && breakEven > entriesCap) return 0
  return breakEven
}
