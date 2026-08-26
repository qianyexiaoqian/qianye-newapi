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
 * 竞猜单注上限：**本文件刻意不给它推荐值**，理由与其余每一格给推荐值的理由
 * 是同一条。
 *
 * 上一版这里有一个 `qyLotRecommendedBetMax = 单注额 × 20`，文案写着
 * 「一个大户最多顶 20 个按单注额下注的普通参与者」。它被整个删掉，
 * 因为那句话经不起查，而且它错的方向恰好是最坏的那一个。
 *
 * ## 一、那句话是假的：`bet_max_quota` 约束的是**一笔**投注，不是一个人
 *
 * 后端 `acceptAmount`（qianye/modules/lottery/entry.go）在**每一次报名请求**
 * 上比对 `BetMaxQuota`。一个人能报几次由 `max_entries_per_user` 决定，而 0 在
 * 那一格就是"不限"（`checkEligibility` 里那句 `MaxEntriesPerUser > 0 &&` 是
 * 唯一的判定，后端默认值也是 0；向导给新草稿预填 1，但那一格可以被清空）。
 * 一旦它是 0，同一个人开 20 笔顶格投注就是 400 个普通参与者的量，
 * 单注上限一格都没拦住。
 *
 * 真正能给"一个人最多押多少"封顶的是**两格的乘积**：
 * `bet_max_quota × max_entries_per_user`。这句话写进了
 * `qy_lot_bet_max_hint`，因为它是这一格唯一需要运营知道、而界面此前没说的事。
 *
 * 一个假的安全承诺比没有承诺更糟：读到"最多顶 20 个"的人会认为大户问题已经
 * 解决，于是**不去设**那个真正管用的每人次数上限。
 *
 * ## 二、就算它是真的，20 也没有出处
 *
 * 本文件开头写着这些函数的存在理由：判据只有三个量、给定其中两个第三个就是
 * **算出来的**。`qyLotTierAmountFloor`、`qyLotTierCountFloor`、
 * `qyLotWinPpmHeadroom`、`qyLotPoolShareHeadroom`、`qyLotRecommendedMinEntries`
 * 每一个都对应后端一条真实的不等式，喂回那条判据必然为假。
 *
 * `applyBetBounds` 对 `bet_max_quota` 只有三条判定：≤ `common.MaxQuota`、
 * ≤ `lottery.max_stake_quota`、≥ `bet_min_quota`。**没有任何不等式可解**，
 * 于是"推荐值"只能是一个凭空选的常数。
 *
 * 而它想控制的那个量本来就算不出来：竞猜按彩池分账，中奖者拿
 * `净池 × 自己的注额 ÷ 获胜方总注额`。一笔大注造成的伤害是**它占获胜方的
 * 比例**，取决于这一场到底来了多少人、怎么分边——两个在建活动那一刻谁都
 * 不知道的数。以单注额为尺度的倍数与那个比例没有固定关系：同样 20 倍，
 * 在一场 1000 人的活动里微不足道，在一场 5 个人的活动里就是全场。
 *
 * 按本文件的口径，这一格属于「运营决策」而不是「解出来的唯一解」，
 * 因此只给范围与后果（`qy_lot_range_bet_max` / `qy_lot_bet_max_hint` /
 * `qy_lot_bet_max_zero_note`），不给自动填。
 *
 * "填 0 = 不限，而没填也是 0"这个真实的坑由**那条零值提示**兜着，不需要
 * 一个编出来的默认值来兜——提示要求人看一眼再决定，编出来的默认值则替他
 * 决定了，而且是按一个假理由决定的。
 */

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
