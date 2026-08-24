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
import type { QyLotTier } from '../types'

/**
 * 双色球的选号与**中奖概率**。
 *
 * ## 概率为什么必须在这里算，而不是让后端下发一个数
 *
 * 双色球唯一但决定性的优势是：各档中奖概率是号池四元组与命中门槛的**组合数
 * 结果**，是一个谁都能自己验的数。后端若下发 `win_rate: 0.0031`，那就退化成
 * 一句必须相信平台的话——管理员在这件事上就有了撒谎的接口。所以
 * `/lottery/activities/:act_no` 与 `/lottery/series/:series_no` 都**刻意不下发
 * 任何概率数字**（见 `qianye/modules/lottery/series.go` 的 `handleGetSeries`），
 * 这个文件就是那条决定的另一半。
 *
 * ## 与后端 `MatchTier` 的对应关系
 *
 * 后端定档是「按 tier 升序遍历，`红命中 ≥ red_match && 蓝命中 ≥ blue_match`
 * 命中即停，一张票只中一档」。所以本文件**不写闭式公式**，而是把 (红命中数,
 * 蓝命中数) 的全部格子枚举出来（最多 9×3 格），每一格按同一条规则找出它归属
 * 的奖级再累加概率。这样"前端算的"与"后端判的"是同一套规则的两次实现，
 * 而不是一个近似值。
 *
 * ## 随机数
 *
 * 机选用 `crypto.getRandomValues` + 拒绝采样，**只决定用户想买哪一组号**，
 * 不参与任何结果判定。开奖号一律来自后端的 `final_seed`，由 `lib/verify.ts`
 * 复算——前端从不自己造随机数去决定任何结果。
 */

/** 号池四元组。它进每期的 commit 原像，因此期与期之间不可变。 */
export type QyLotBallPool = {
  redPool: number
  redPick: number
  bluePool: number
  bluePick: number
}

/** 一组选号。红蓝两组各自升序、组内不重复。 */
export type QyLotBallPick = {
  reds: number[]
  blues: number[]
}

/** 号池上界，与 `qianye/modules/lottery/ball.go` 的四个常量逐字对应。 */
export const QY_LOT_BALL_MAX = {
  redPool: 36,
  redPick: 8,
  bluePool: 16,
  bluePick: 2,
} as const

/** 号池四元组是否合法（与后端 `ValidateBallPool` 同一组判据）。 */
export function isQyLotBallPoolValid(pool: QyLotBallPool): boolean {
  return (
    pool.redPool >= 2 &&
    pool.redPool <= QY_LOT_BALL_MAX.redPool &&
    pool.redPick >= 1 &&
    pool.redPick <= QY_LOT_BALL_MAX.redPick &&
    pool.redPick <= pool.redPool &&
    pool.bluePool >= 1 &&
    pool.bluePool <= QY_LOT_BALL_MAX.bluePool &&
    pool.bluePick >= 1 &&
    pool.bluePick <= QY_LOT_BALL_MAX.bluePick &&
    pool.bluePick <= pool.bluePool
  )
}

/** 活动详情/大厅卡片上的那四列 → 号池四元组。 */
export function qyLotBallPoolOf(source: {
  ball_red_pool?: number
  ball_red_pick?: number
  ball_blue_pool?: number
  ball_blue_pick?: number
}): QyLotBallPool {
  return {
    redPool: source.ball_red_pool ?? 0,
    redPick: source.ball_red_pick ?? 0,
    bluePool: source.ball_blue_pool ?? 0,
    bluePick: source.ball_blue_pick ?? 0,
  }
}

/**
 * 选号 → 规范化串 `03,05,12|02`。
 *
 * **这就是进哈希链的那份字节**（后端 `FormatPick`）：两位补零、升序、逗号分隔、
 * 竖线分组。前端提交的串与它不一致时后端会归一化，但回执与"我的参与"上显示的
 * 一律是后端返回的那一份——否则用户拿手里的串去比对链会得出"平台改了我的号"。
 */
export function qyLotBallFormatPick(pick: QyLotBallPick): string {
  const pad = (numbers: number[]) =>
    [...numbers]
      .sort((a, b) => a - b)
      .map((value) => String(value).padStart(2, '0'))
      .join(',')
  return `${pad(pick.reds)}|${pad(pick.blues)}`
}

/**
 * 规范化串 `03,05,12|02` → 选号。**解析不了就当没有号**，绝不抛。
 *
 * 与 `lib/verify.ts` 的 `qyLotParsePick` 是刻意分开的两个函数，因为它们服务的是
 * 两件相反的事：
 *
 *  - 复算路径（verify / explain）必须**抛**。一个格式不对的 `pick` 意味着链的
 *    原像推不出来，把它悄悄当成空号会让"验证通过"变成一句假话。
 *  - 展示路径必须**不抛**。开奖号与选号在展示时会流过大厅卡片、详情页、
 *    「我的参与」的每一行 —— 其中任何一处渲染期抛异常，塌掉的是整棵 React 树，
 *    用户看到的是白屏而不是"这一格没有号"。而这两条串来自数据库里的历史行，
 *    格式漂移（老算法、人工改库、截断）不是不可能。
 *
 * 返回 `null` 而不是 `{reds: [], blues: []}`：调用方要能把"没有号"与"号是空的"
 * 分开——前者不该渲染任何一颗球，后者在这套玩法里根本不存在。
 */
export function qyLotBallSafeParsePick(pick: string): QyLotBallPick | null {
  const text = pick.trim()
  if (text === '' || !text.includes('|')) return null
  const [redPart = '', bluePart = ''] = text.split('|')
  const parse = (part: string): number[] | null => {
    if (part === '') return []
    const out: number[] = []
    for (const cell of part.split(',')) {
      if (!/^\d{1,2}$/.test(cell)) return null
      out.push(Number.parseInt(cell, 10))
    }
    return out
  }
  const reds = parse(redPart)
  const blues = parse(bluePart)
  if (reds == null || blues == null) return null
  if (reds.length === 0 && blues.length === 0) return null
  return { reds, blues }
}

/**
 * 我的号里哪几个被开出来了。
 *
 * 与后端 `MatchTier` 读的是同一件事的两个投影：那边只数**个数**（命中几红几蓝
 * 决定档位），这里要的是**具体是哪几个**（决定界面上哪几颗球高亮）。个数由
 * `hits.reds.length` 得出，所以两者不可能各说各话。
 *
 * 两边任何一边缺席（还没开奖 / 已取消 / 这一格根本不是双色球票）都返回空集合
 * —— 一颗都不高亮，而不是全高亮。
 */
export function qyLotBallHits(
  mine: QyLotBallPick | null,
  drawn: QyLotBallPick | null
): QyLotBallPick {
  if (mine == null || drawn == null) return { blues: [], reds: [] }
  return {
    reds: mine.reds.filter((ball) => drawn.reds.includes(ball)),
    blues: mine.blues.filter((ball) => drawn.blues.includes(ball)),
  }
}

/** 选号是否已选满。按钮亮不亮只看这一条，真正的校验在后端。 */
export function isQyLotBallPickComplete(
  pick: QyLotBallPick,
  pool: QyLotBallPool
): boolean {
  return (
    pick.reds.length === pool.redPick && pick.blues.length === pool.bluePick
  )
}

/**
 * 机选一组号。
 *
 * 用 `crypto.getRandomValues` + 拒绝采样从 `[0, bound)` 取无偏整数，再做部分
 * Fisher–Yates 抽样。`Math.random()` 在这里不是安全问题（机选只决定买哪组号，
 * 不决定谁中奖），但它在某些引擎上有可观察的模式，而"用户按了三次机选拿到相近
 * 的号"会被理解成平台在操纵——这个误解的代价远高于用 WebCrypto 的成本。
 */
export function qyLotBallRandomPick(pool: QyLotBallPool): QyLotBallPick {
  return {
    reds: sampleDistinct(pool.redPool, pool.redPick),
    blues: sampleDistinct(pool.bluePool, pool.bluePick),
  }
}

function sampleDistinct(poolSize: number, take: number): number[] {
  if (poolSize <= 0 || take <= 0) return []
  const count = Math.min(take, poolSize)
  const balls = Array.from({ length: poolSize }, (_, index) => index + 1)
  for (let i = 0; i < count; i += 1) {
    const j = i + randomBelow(poolSize - i)
    const swap = balls[i]
    balls[i] = balls[j]
    balls[j] = swap
  }
  return balls.slice(0, count).sort((a, b) => a - b)
}

/** `[0, bound)` 上的无偏整数。拒绝采样，不取模。 */
function randomBelow(bound: number): number {
  if (bound <= 1) return 0
  // 2^32 不能被 bound 整除时，最高的那一小段值会让某些余数多出现一次。
  // 落进那一段就重摇——这是消掉模偏置的标准做法，代价是极小的期望重试次数。
  const limit = Math.floor(0x1_0000_0000 / bound) * bound
  const buffer = new Uint32Array(1)
  for (;;) {
    crypto.getRandomValues(buffer)
    const value = buffer[0]
    if (value < limit) return value % bound
  }
}

// ─────────────────────────── 组合数与概率 ───────────────────────────

/**
 * 二项式系数 C(n, k)。
 *
 * 用逐步乘除而不是阶乘：`36!` 早就超过 `Number.MAX_SAFE_INTEGER`，而
 * `C(36, 8)` 只有八位数。每一步的中间值 `result * (n - k + i) / i` 恒为整数
 * （前 i 个连续整数之积必被 i! 整除），所以在号池上界内不会有浮点误差。
 */
export function qyLotChoose(n: number, k: number): number {
  if (k < 0 || k > n || n < 0) return 0
  const take = Math.min(k, n - k)
  let result = 1
  for (let i = 1; i <= take; i += 1) {
    result = (result * (n - take + i)) / i
  }
  return Math.round(result)
}

/**
 * 超几何分布：池子 `poolSize` 里开出 `pickCount` 个，我也选 `pickCount` 个，
 * 恰好命中 `hits` 个的概率。
 *
 *     C(pickCount, hits) × C(poolSize − pickCount, pickCount − hits) / C(poolSize, pickCount)
 */
export function qyLotBallMatchProbability(
  poolSize: number,
  pickCount: number,
  hits: number
): number {
  const total = qyLotChoose(poolSize, pickCount)
  if (total === 0) return 0
  return (
    (qyLotChoose(pickCount, hits) *
      qyLotChoose(poolSize - pickCount, pickCount - hits)) /
    total
  )
}

/** 一档的中奖概率。`probability` 是 [0,1]，`odds` 是"多少注中一注"（0 = 不可能）。 */
export type QyLotBallTierOdds = {
  tier: number
  probability: number
  odds: number
}

/**
 * 逐档中奖概率。
 *
 * 枚举 (红命中 r, 蓝命中 b) 的每一格，按后端 `MatchTier` 的规则
 * （tier 升序、命中即停）判定它归属哪一档，再把该格的概率
 * `P(r) × P(b)` 累加进去。红蓝两组独立，所以联合概率就是乘积。
 *
 * 号池非法（尚未选定期次系列、或后端没下发）时返回空数组：显示一列全是 0 的
 * 概率，比不显示更容易被读成"这一档一定不中"。
 */
export function qyLotBallTierOdds(
  pool: QyLotBallPool,
  tiers: QyLotTier[]
): QyLotBallTierOdds[] {
  if (!isQyLotBallPoolValid(pool)) return []
  const ordered = [...tiers].sort((a, b) => a.tier - b.tier)
  const totals = new Map<number, number>()
  for (const tier of ordered) totals.set(tier.tier, 0)

  for (let red = 0; red <= pool.redPick; red += 1) {
    const redProbability = qyLotBallMatchProbability(
      pool.redPool,
      pool.redPick,
      red
    )
    if (redProbability === 0) continue
    for (let blue = 0; blue <= pool.bluePick; blue += 1) {
      const blueProbability = qyLotBallMatchProbability(
        pool.bluePool,
        pool.bluePick,
        blue
      )
      if (blueProbability === 0) continue
      const hit = ordered.find(
        (tier) => red >= (tier.red_match ?? 0) && blue >= (tier.blue_match ?? 0)
      )
      if (hit == null) continue
      totals.set(
        hit.tier,
        (totals.get(hit.tier) ?? 0) + redProbability * blueProbability
      )
    }
  }

  return ordered.map((tier) => {
    const probability = totals.get(tier.tier) ?? 0
    return {
      tier: tier.tier,
      probability,
      odds: probability > 0 ? Math.round(1 / probability) : 0,
    }
  })
}

/**
 * 一份奖级表是不是"某一档永远开不出来"。
 *
 * 后端 `checkBallTierReachability` 会直接 400 拒绝，这里复现同一条判据只是为了
 * 让管理端在按下创建之前就说清是哪两档冲突，而不是让运营填完四步再被顶回来。
 * 返回的是**冲突对**：等级数小的那一档门槛不严于等级数大的那一档。
 */
export function qyLotBallUnreachableTiers(
  tiers: QyLotTier[]
): { higher: number; lower: number }[] {
  const ordered = [...tiers].sort((a, b) => a.tier - b.tier)
  const conflicts: { higher: number; lower: number }[] = []
  for (let i = 0; i < ordered.length; i += 1) {
    for (let j = i + 1; j < ordered.length; j += 1) {
      if (
        (ordered[i].red_match ?? 0) <= (ordered[j].red_match ?? 0) &&
        (ordered[i].blue_match ?? 0) <= (ordered[j].blue_match ?? 0)
      ) {
        conflicts.push({ higher: ordered[i].tier, lower: ordered[j].tier })
      }
    }
  }
  return conflicts
}
