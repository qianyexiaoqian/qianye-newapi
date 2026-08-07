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
import {
  isQyLotVoided,
  qyLotTiers,
  type QyLotProof,
  type QyLotProofEntry,
  type QyLotTier,
} from '../types'
import {
  qyLotBallDraw,
  qyLotBandOf,
  qyLotBands,
  qyLotFinalSeed,
  qyLotParsePick,
  qyLotRollPpm,
  qyLotTicket,
  type QyLotBand,
} from './verify'

/**
 * 「为什么是这个结果」——**单人复算**。
 *
 * ## 它解决的是什么
 *
 * 名次制里"我没中"的证明是一个否定式：你不在名单里。概率制更糟 —— 中不中
 * 只取决于一个第三方看不见的随机数，如果这一步不能被本人当场复算，那么
 * 「历史公正查询」就退化成了平台的一面之词，整套承诺-揭示白做。
 *
 * 所以这里把落选的证明改写成一个**正数**：
 *   · 名次制：你的票面是 0x1a3f…，全场按票面升序你排第 187 名，一等奖取前 3、
 *     二等奖取第 4–13 名，187 在这之外。
 *   · 概率制：你的摇号结果 r = 384217，各档区间是 [0,1000) 与 [1000,11000)，
 *     384217 落在全部区间之外 —— 这就是你没中的全部原因。
 *   · 双色球：你选 03 05 12 | 08，本期开奖 03 09 12 | 05，命中红 2 蓝 0。
 *
 * ## 落选者与中奖者走同一行代码
 *
 * 下面每一个分支都对 roster 里的**任意一条** entry 一视同仁地算，没有任何
 * 只在失败时才走的路径。平台因此无法制造一个"只有失败者看不到"的暗门。
 *
 * ## 一个字节都不信后端
 *
 * `final_seed`、票面、名次、摇号量、开奖号全部在这里重算，输入只有证据链里
 * 那几个公开字段。信后端下发的判定，等于让"自己验"退化成"让平台说它验过了"。
 */

/** 复算不出结论时的原因。**绝不用一个含糊的空结果掩盖它们**。 */
export type QyLotExplainBlocked =
  /** 条目没取全（分页）——链断与被篡改在结果上无法区分，不能猜。 */
  | 'incomplete'
  /** 这条记录不在有效名单里（报名失败 / 封盘时未落定），压根没参与抽取。 */
  | 'not_in_roster'
  /** 种子还没揭示，任何人（包括平台）都还算不出结果。 */
  | 'not_revealed'
  /** 整场取消 / 流局，没有开出结果。 */
  | 'voided'

export type QyLotExplainRank = {
  mode: 'rank'
  ticket: string
  /** 1 起算的名次（去重之后的有效名次）。 */
  rank: number
  /** 参与排名的票数（`allow_multi_win=false` 时已去重）。 */
  rankedTotal: number
  /** 每档占据的名次区间，1 起算、左闭右闭。 */
  tierRanges: { tier: number; name: string; from: number; to: number }[]
  hitTier: number | null
}

export type QyLotExplainProb = {
  mode: 'prob'
  ticket: string
  /** 摇号量，0…999999。 */
  roll: number
  bands: (QyLotBand & { name: string })[]
  hitTier: number | null
}

export type QyLotExplainBall = {
  mode: 'ball'
  /** 本期开奖号（本地从种子复算，不是后端下发的）。 */
  drawnReds: number[]
  drawnBlues: number[]
  myReds: number[]
  myBlues: number[]
  matchRed: number
  matchBlue: number
  /** 各档的命中要求，供用户逐条对照。 */
  tierNeeds: { tier: number; name: string; red: number; blue: number }[]
  /**
   * 命中的奖级；`null` = 一档都没中（这是一等公民结果，不是异常分支）。
   *
   * 它按后端 `MatchTier` 的同一条规则本地判定：tier 升序、`红命中 ≥ red_match
   * && 蓝命中 ≥ blue_match` 命中即停、一张票只中一档。这一步是
   * (matchRed, matchBlue, 门槛表) 的纯函数，不可能算出与后端不同的结果。
   *
   * **刻意只给档位，不给金额**：浮动奖档发多少取决于本期奖池与同档中签人数，
   * 那两个量不在这份证据链里，本地复算不了。给一个半真的金额比不给更糟。
   */
  hitTier: number | null
}

export type QyLotExplain =
  | ({ blocked: null } & (
      | QyLotExplainBall
      | QyLotExplainProb
      | QyLotExplainRank
    ))
  | { blocked: QyLotExplainBlocked; mode: null }

/** 排序用：字节序升序。ASCII 范围内 JS 的字符串比较就是字节序。 */
function byteCompare(a: string, b: string): number {
  if (a < b) return -1
  if (a > b) return 1
  return 0
}

function tierName(tiers: QyLotTier[], tier: number): string {
  return tiers.find((item) => item.tier === tier)?.name ?? ''
}

export async function explainQyLotResult(
  proof: QyLotProof,
  entryNo: string
): Promise<QyLotExplain> {
  // 与 `verifyQyLotProof` 同一条口径：分页取回的那一份验不了，也解释不了。
  if (proof.entries.length !== proof.total) {
    return { blocked: 'incomplete', mode: null }
  }
  if (proof.seed === '') return { blocked: 'not_revealed', mode: null }
  // 取消 / 流局的场次没有开出结果，复算出的"本应中奖名单"不是事实，
  // 拿它去解释用户的结果就是在编造一个从未发生过的开奖。
  if (isQyLotVoided(proof.outcome)) return { blocked: 'voided', mode: null }

  const roster = proof.entries
    .filter((entry) => entry.status === 'success')
    .sort((a, b) => byteCompare(a.entry_no, b.entry_no))
  const mine = roster.find((entry) => entry.entry_no === entryNo)
  if (mine == null) return { blocked: 'not_in_roster', mode: null }

  const finalSeed = await qyLotFinalSeed(proof)
  const tiers = qyLotTiers(proof.spec)

  if (proof.draw_mode === 'ball') {
    const redPool = proof.ball_red_pool ?? 0
    const bluePool = proof.ball_blue_pool ?? 0
    if (redPool === 0) return { blocked: 'voided', mode: null }
    const drawnReds = await qyLotBallDraw(
      finalSeed,
      proof.act_no,
      'red',
      redPool,
      proof.ball_red_pick ?? 0
    )
    const drawnBlues = await qyLotBallDraw(
      finalSeed,
      proof.act_no,
      'blue',
      bluePool,
      proof.ball_blue_pick ?? 0
    )
    const { reds, blues } = qyLotParsePick(mine.pick ?? '')
    const matchRed = reds.filter((ball) => drawnReds.includes(ball)).length
    const matchBlue = blues.filter((ball) => drawnBlues.includes(ball)).length
    const tierNeeds = tiers
      .map((tier) => ({
        tier: tier.tier,
        name: tier.name,
        red: tier.red_match ?? 0,
        blue: tier.blue_match ?? 0,
      }))
      .sort((a, b) => a.tier - b.tier)
    return {
      blocked: null,
      mode: 'ball',
      drawnReds,
      drawnBlues,
      myReds: reds,
      myBlues: blues,
      matchRed,
      matchBlue,
      tierNeeds,
      // 与后端 MatchTier 逐字对应：tier 升序、命中即停、一张票只中一档。
      // 金额仍然不算（见 QyLotExplainBall.hitTier 的说明）。
      hitTier:
        tierNeeds.find((need) => matchRed >= need.red && matchBlue >= need.blue)
          ?.tier ?? null,
    }
  }

  const ticket = await qyLotTicket(finalSeed, proof.act_no, mine.entry_no)

  if (proof.draw_mode === 'prob') {
    const bands = qyLotBands(tiers).map((band) => ({
      ...band,
      name: tierName(tiers, band.tier),
    }))
    const roll = qyLotRollPpm(ticket)
    return {
      blocked: null,
      mode: 'prob',
      ticket,
      roll,
      bands,
      hitTier: qyLotBandOf(bands, roll)?.tier ?? null,
    }
  }

  // 名次制（`lot-v1` 的唯一行为，`draw_mode` 缺省也走这里）。
  const ticketed: { entry: QyLotProofEntry; ticket: string }[] = []
  for (const entry of roster) {
    ticketed.push({
      entry,
      ticket: await qyLotTicket(finalSeed, proof.act_no, entry.entry_no),
    })
  }
  ticketed.sort(
    (a, b) =>
      byteCompare(a.ticket, b.ticket) ||
      byteCompare(a.entry.entry_no, b.entry.entry_no)
  )
  let ranked = ticketed.map((item) => item.entry)
  if (!proof.allow_multi_win) {
    const seen = new Set<string>()
    ranked = ranked.filter((entry) => {
      if (seen.has(entry.user_ref)) return false
      seen.add(entry.user_ref)
      return true
    })
  }

  const tierRanges: QyLotExplainRank['tierRanges'] = []
  let cursor = 1
  for (const tier of tiers) {
    if (tier.count <= 0) continue
    tierRanges.push({
      tier: tier.tier,
      name: tier.name,
      from: cursor,
      to: cursor + tier.count - 1,
    })
    cursor += tier.count
  }

  const index = ranked.findIndex((entry) => entry.entry_no === mine.entry_no)
  // 去重把这张票挤掉了（同一个人更靠前的那张已经占位）——它确实没有名次，
  // 而这本身就是答案：`allow_multi_win=false` 时每人只算最靠前的一张。
  const rank = index < 0 ? 0 : index + 1
  return {
    blocked: null,
    mode: 'rank',
    ticket,
    rank,
    rankedTotal: ranked.length,
    tierRanges,
    hitTier:
      tierRanges.find((range) => rank >= range.from && rank <= range.to)
        ?.tier ?? null,
  }
}
