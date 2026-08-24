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
import { qyLotOptions, type QyLotOption, type QyLotSpecItem } from '../types'

/**
 * 竞猜的实时赔率。
 *
 * # 为什么必须有这个文件
 *
 * 竞猜是**彩池制**：赢家分的是输家的钱，平台只从池子里切一刀手续费。界面上
 * 却只有一排单选按钮和一句「奖池制：投注总额扣掉手续费后按投注比例分给猜中的
 * 人」——那句话说的全对，但没有任何一个数字支撑它。用户（以及项目方自己）
 * 看到的就是「选一个就能赢」。
 *
 * 一个会随盘口变化的数（「现在押 $1，押中约得 $1.7」）比三段文字有用得多，
 * 而且它自己会把彩池的两条要害讲明白：
 *
 *   · 押的人越多，这个数越小 —— 因为同一个池子要分给更多人；
 *   · 没有对手盘时它恰好是 $1 —— 因为**没有输家就没有奖金**。
 *
 * # 口径必须与后端 SplitPool 逐字节一致
 *
 * 后端 `qianye/modules/lottery/commit.go` 的 `SplitPool`：
 *
 *   1. `winSum == pool`（全场都押中，含"只有我一个人下注"）或无人押中
 *      → **全额退回本金，手续费一分不收**；
 *   2. `fee = trunc(pool × feeBps / 10000)`，钳进 `[0, pool]`；
 *   3. `net = pool − fee`；
 *   4. 逐笔 `pay = trunc(net × amount / winSum)`，**残差归 entry_no 字节序
 *      最大的那一位赢家**。
 *
 * 这里对每一笔都取 `trunc`，包括本该拿残差的那一笔 —— 残差上界小于赢家人数
 * （以 quota 为单位，50 万 quota ≈ $1），所以本地估算**只会略低于**实际到账，
 * 绝不会高。宁可少报也不能多报：多报一个单位就是界面在替平台许一个它不一定
 * 兑得出的承诺。
 */

/** 万分比分母。与后端 feeBps 同一个刻度。 */
const BPS_DEN = 10000

export type QyLotGuessQuote = {
  /** 结算后这一注拿回多少额度（含本金）。 */
  payoutQuota: number
  /** 净赚多少（拿回 − 本金）。全额退回那一支恒为 0。 */
  profitQuota: number
  /** 倍数 = 拿回 / 本金。全额退回那一支恒为 1。 */
  multiple: number
  /**
   * 这一注会不会落进「原样退回」那一支。
   *
   * 触发它的是 `winSum == pool`：全场押的都是同一项（含"此刻只有我一个人
   * 下注"）。这不是异常，是彩池的定义 —— 没有输家就没有奖金可分。
   */
  refund: boolean
}

/**
 * 算一注押进某个选项、且该选项开出时能拿回多少。
 *
 * 传进来的 `poolQuota` / `optionBetQuota` 都是**还不含这一注**的当下值，
 * 函数内部再把它加进去：用户问的是"我押下去之后会怎样"。
 */
export function qyLotGuessQuote(input: {
  /** 当前奖池（全部选项的投注之和）。 */
  poolQuota: number
  /** 手续费万分比。 */
  feeBps: number
  /** 目标选项当前的投注额。 */
  optionBetQuota: number
  /** 这一注押多少。 */
  stakeQuota: number
}): QyLotGuessQuote {
  const stake = Math.max(0, Math.floor(input.stakeQuota))
  const pool = Math.max(0, Math.floor(input.poolQuota)) + stake
  const winSum = Math.max(0, Math.floor(input.optionBetQuota)) + stake

  // stake 为 0 时没有"这一注"可言；winSum >= pool 时全场都押中了这一项。
  // 后者用 >= 而不是 ==：盘口的两个数分别来自活动行的物化计数与选项行的
  // 聚合，理论上恒等，但只要有一次计数漂移，`==` 会让这里落进除法分支并
  // 报出一个大于池子的赔付数 —— 那是界面能犯的最坏的错。
  if (stake <= 0 || winSum >= pool) {
    return {
      payoutQuota: stake,
      profitQuota: 0,
      multiple: 1,
      refund: true,
    }
  }

  // 归一化**在乘之前**做，而且只做这一处。改造中一度是"先按原始 feeBps 乘出
  // rawFee，再把 rawFee 钳进 [0, pool]"，两道闸叠在一起 —— 后果不是更安全，
  // 而是任何一道都测不出来：把其中一道删掉，全部用例照样绿（实测 MF3
  // 17 pass / 0 fail，存活）。一个杀不掉的守卫等于没有守卫。
  const fee = Math.trunc((pool * clampFeeBps(input.feeBps)) / BPS_DEN)
  const net = pool - fee
  const payoutQuota = Math.trunc((net * stake) / winSum)

  return {
    payoutQuota,
    profitQuota: payoutQuota - stake,
    multiple: payoutQuota / stake,
    refund: false,
  }
}

/**
 * 算**已经发生**的那一次结算:这一注押在某个选项上,而该选项真的开出了,
 * 到手多少。
 *
 * 与 `qyLotGuessQuote` 的差别只有一处,却是全部:池子与该选项的投注额都**不再
 * 加上"这一注"**。前瞻赔率回答的是"我押下去之后会怎样",而结果已经公布之后
 * 再问这句话就是错的 —— 实测一场 pool=3000 / fee 5% / 甲 1000 乙 2000 的活动,
 * 甲胜,页面在「已判定获胜」徽章旁边写着「押中约得 ＄0.0038 ×1.90」,而真正
 * 打进账户的是 2850(＄0.0057 ×2.85):界面比实付少 33%,落选的那一项还挂着
 * 一个从未发生过的 ×1.27。
 *
 * 口径与后端 `SplitPool` 逐条一致:winSum 为 0(没人押中)或 winSum >= pool
 * (全场押中同一项)时全额退回、手续费一分不收。
 */
export function qyLotGuessSettledQuote(input: {
  /** 结算时的奖池(已含全部投注)。 */
  poolQuota: number
  feeBps: number
  /** 获胜选项的投注总额。 */
  winnerBetQuota: number
  /** 折算成"每一注"的口径,通常取活动单注额。 */
  stakeQuota: number
}): QyLotGuessQuote {
  const stake = Math.max(0, Math.floor(input.stakeQuota))
  const pool = Math.max(0, Math.floor(input.poolQuota))
  const winSum = Math.max(0, Math.floor(input.winnerBetQuota))

  if (stake <= 0 || winSum <= 0 || winSum >= pool) {
    return { payoutQuota: stake, profitQuota: 0, multiple: 1, refund: true }
  }

  const fee = Math.trunc((pool * clampFeeBps(input.feeBps)) / BPS_DEN)
  const net = pool - fee
  const payoutQuota = Math.trunc((net * stake) / winSum)
  return {
    payoutQuota,
    profitQuota: payoutQuota - stake,
    multiple: payoutQuota / stake,
    refund: false,
  }
}

/**
 * 把越界的 feeBps 收进 `[0, 10000]`。
 *
 * 后端在建活动时就把它挡在 `[0, max_guess_fee_bps]` 里，所以线上不会有越界
 * 值。这里仍然收一次是因为界面读的是**接口下发的数**：一个负数会让本地算出
 * 一个比池子还大的赔付，而用户会照着它下注；一个 NaN 会让整行赔率渲染成
 * `NaN`。
 *
 * 收在这里之后，`fee = trunc(pool × feeBps / 10000)` 对 `pool ≥ 0` 恒落在
 * `[0, pool]` 内，所以后面**不再有第二道钳位** —— 见上面那段注释。
 */
function clampFeeBps(feeBps: number): number {
  if (!Number.isFinite(feeBps)) return 0
  return Math.min(Math.max(Math.trunc(feeBps), 0), BPS_DEN)
}

/**
 * 一行盘口现在处于哪种处境。
 *
 * `pending` 之外的三种只在结果已经公布之后出现,而它们要显示的数完全不同:
 * 赢的那一项要写**已经发出去的**赔付,输的那一项一个赔率都不该有,
 * 全额退回那一场三种选项一律写"原样退回"。
 */
export type QyLotGuessOutcome = 'pending' | 'won' | 'lost' | 'refunded'

export type QyLotGuessRow = QyLotOption & {
  /** 这个选项占当前奖池的比例，0~1。池子为 0 时是 0。 */
  share: number
  /** 押一注的赔率:`pending` 时是前瞻值,`won` 时是已经发生的那一次结算。 */
  quote: QyLotGuessQuote
  outcome: QyLotGuessOutcome
}

/**
 * 把 spec 里的选项摊成「盘口 + 实时赔率」的一行。
 *
 * 分布本身就是最好的解释：看到「80% 的钱押在 A」就自然懂了押 A 赢不了多少，
 * 不需要任何一句说明文字。
 */
export function qyLotGuessBoard(input: {
  spec: QyLotSpecItem[] | undefined
  poolQuota: number
  feeBps: number
  stakeQuota: number
  /** 已公布的获胜选项号。0 / undefined = 还没录。 */
  winOptNo?: number
  /** 结果是否已经公布。为真时整块盘口从"前瞻"切换成"已发生"。 */
  resultAnnounced?: boolean
}): QyLotGuessRow[] {
  const pool = Math.max(0, Math.floor(input.poolQuota))
  const options = qyLotOptions(input.spec)
  const announced = input.resultAnnounced === true
  const winOptNo = input.winOptNo ?? 0

  const winnerBet = Math.max(
    0,
    Math.floor(
      options.find((option) => option.opt_no === winOptNo)?.bet_quota ?? 0
    )
  )
  // 全额退回的两支:没人押中(winnerBet 为 0)、全场押中同一项(winSum >= pool)。
  // 与后端 SplitPool 同一条判据,而且用 >= 不用 ==:两个数分别来自活动行的
  // 物化计数与选项行的聚合,一次计数漂移就会让 == 落进除法分支。
  const refundAll = announced && (winnerBet <= 0 || winnerBet >= pool)

  return options.map((option) => {
    const bet = Math.max(0, Math.floor(option.bet_quota ?? 0))
    const share = pool > 0 ? bet / pool : 0

    if (!announced) {
      return {
        ...option,
        share,
        outcome: 'pending' as const,
        quote: qyLotGuessQuote({
          poolQuota: pool,
          feeBps: input.feeBps,
          optionBetQuota: bet,
          stakeQuota: input.stakeQuota,
        }),
      }
    }

    const stake = Math.max(0, Math.floor(input.stakeQuota))
    if (refundAll) {
      return {
        ...option,
        share,
        outcome: 'refunded' as const,
        quote: {
          payoutQuota: stake,
          profitQuota: 0,
          multiple: 1,
          refund: true,
        },
      }
    }
    if (option.opt_no === winOptNo) {
      return {
        ...option,
        share,
        outcome: 'won' as const,
        quote: qyLotGuessSettledQuote({
          poolQuota: pool,
          feeBps: input.feeBps,
          winnerBetQuota: winnerBet,
          stakeQuota: input.stakeQuota,
        }),
      }
    }
    return {
      ...option,
      share,
      outcome: 'lost' as const,
      quote: {
        payoutQuota: 0,
        profitQuota: -stake,
        multiple: 0,
        refund: false,
      },
    }
  })
}

/**
 * 盘口数字是不是真的到手了。
 *
 * `bet_quota` 在证据链端点上不下发（它不进承诺原像），拿不到时整块盘口
 * 不渲染 —— 显示一排 0% 是一个**错的**数，比没有数更糟。
 */
export function hasQyLotGuessBoard(spec: QyLotSpecItem[] | undefined): boolean {
  return qyLotOptions(spec).some((option) => option.bet_quota != null)
}
