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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import type { QyLotSpecItem } from '../../types'
import {
  hasQyLotGuessBoard,
  qyLotGuessBoard,
  qyLotGuessQuote,
  qyLotGuessSettledQuote,
} from '../guess'

/**
 * 竞猜实时赔率。
 *
 * 界面上那个「押中约得 X」是用户下注前唯一能拿来估价的数，而它由本文件的
 * 实现独家产出 —— 算错了没有任何下游会发现，用户也无从对照。所以这里的
 * 期望值全部**手算**写死，并且逐条对齐后端 `SplitPool` 的口径：
 *
 *   1. `winSum == pool`（含"只有我一注"）与无人押中 → 全额退回，零手续费；
 *   2. `fee = trunc(pool × feeBps / 10000)`；
 *   3. `pay = trunc((pool − fee) × mine / winSum)`，逐笔**向零截断**。
 *
 * 其中第 3 条与后端有一处**刻意的**差别：后端把逐笔截断的残差归给
 * entry_no 字节序最大的那一位赢家，这里对每一笔都截断。所以本地估算永远
 * ≤ 实际到账，绝不会高。「本地不许高于实际」那条单独有一个用例。
 */

describe('竞猜实时赔率与后端 SplitPool 同口径', () => {
  /*
    第一行就是 Go 侧那场 e2e 真打的账（guess_pool_e2e_db_test.go）：
    三人各 1000、A 独中，pool=3000、fee=150、net=2850，A 拿走全部 2850。
    两侧独立算出同一个数，界面上的赔率与真正到账的钱才是一回事。
  */
  const cases: {
    name: string
    pool: number
    feeBps: number
    optionBet: number
    stake: number
    payout: number
    multiple: number
    refund: boolean
  }[] = [
    {
      name: '三人各一注、我独中：net 全归我',
      // 我押之前池子里已有 2000（对家两注），我这一项还是 0
      pool: 2000,
      feeBps: 500,
      optionBet: 0,
      stake: 1000,
      // pool=3000 fee=trunc(3000×500/10000)=150 net=2850 winSum=1000 → 2850
      payout: 2850,
      multiple: 2.85,
      refund: false,
    },
    {
      name: '和另一个人平分：每人一半',
      pool: 2000,
      feeBps: 500,
      optionBet: 1000,
      stake: 1000,
      // pool=3000 fee=150 net=2850 winSum=2000 → trunc(2850×1000/2000)=1425
      payout: 1425,
      multiple: 1.425,
      refund: false,
    },
    {
      name: '押进人多的那一边：赔率跌到 1 倍出头',
      pool: 9000,
      feeBps: 500,
      optionBet: 8000,
      stake: 1000,
      // pool=10000 fee=500 net=9500 winSum=9000 → trunc(9500×1000/9000)=1055
      payout: 1055,
      multiple: 1.055,
      refund: false,
    },
    {
      name: '零手续费：net 就是整个池子',
      pool: 2000,
      feeBps: 0,
      optionBet: 1000,
      stake: 1000,
      // pool=3000 fee=0 net=3000 winSum=2000 → 1500
      payout: 1500,
      multiple: 1.5,
      refund: false,
    },
    {
      name: '池子空着，我是第一注：没有对手盘，原样退回',
      pool: 0,
      feeBps: 500,
      optionBet: 0,
      stake: 1000,
      payout: 1000,
      multiple: 1,
      refund: true,
    },
    {
      name: '全场都押在这一项：winSum == pool，原样退回',
      pool: 4000,
      feeBps: 500,
      optionBet: 4000,
      stake: 1000,
      payout: 1000,
      multiple: 1,
      refund: true,
    },
    {
      name: '费率上限 20%：赢家只分走八成',
      pool: 9000,
      feeBps: 2000,
      optionBet: 0,
      stake: 1000,
      // pool=10000 fee=2000 net=8000 winSum=1000 → 8000
      payout: 8000,
      multiple: 8,
      refund: false,
    },
    {
      name: '费率打满 100%：赢家一分不得，但也绝不为负',
      pool: 9000,
      feeBps: 10000,
      optionBet: 0,
      stake: 1000,
      payout: 0,
      multiple: 0,
      refund: false,
    },
  ]

  for (const item of cases) {
    test(item.name, () => {
      const quote = qyLotGuessQuote({
        poolQuota: item.pool,
        feeBps: item.feeBps,
        optionBetQuota: item.optionBet,
        stakeQuota: item.stake,
      })
      assert.equal(quote.payoutQuota, item.payout)
      assert.equal(quote.profitQuota, item.payout - item.stake)
      assert.equal(quote.multiple, item.multiple)
      assert.equal(quote.refund, item.refund)
    })
  }
})

describe('越界与脏数据一律不许算出一个诱人的数', () => {
  test('负费率被收进 0，而不是算出一个大于池子的赔付', () => {
    // 后端建活动时就把 feeBps 挡在 [0, max] 里，但界面读的是接口下发的数。
    // 若原样代入，net = pool − (−500) = 10500，赔率会比无手续费还高 ——
    // 而用户会照着它下注。
    const quote = qyLotGuessQuote({
      poolQuota: 9000,
      feeBps: -500,
      optionBetQuota: 0,
      stakeQuota: 1000,
    })
    assert.equal(quote.payoutQuota, 10000, '负费率应当按零费率处理')
  })

  test('费率超过 10000 也不会让赔付为负', () => {
    const quote = qyLotGuessQuote({
      poolQuota: 9000,
      feeBps: 99999,
      optionBetQuota: 0,
      stakeQuota: 1000,
    })
    assert.equal(quote.payoutQuota, 0)
    assert.ok(quote.profitQuota <= 0)
  })

  test('NaN 费率按零费率处理', () => {
    const quote = qyLotGuessQuote({
      poolQuota: 2000,
      feeBps: Number.NaN,
      optionBetQuota: 0,
      stakeQuota: 1000,
    })
    assert.equal(quote.payoutQuota, 3000)
  })

  test('选项聚合大于活动池子（计数漂移）时退回而不是报天价', () => {
    // 两个数分别来自活动行的物化计数与选项行的聚合，理论上恒等。
    // 漂移时若走除法分支，winSum < pool 会让 pay 大于整个池子。
    const quote = qyLotGuessQuote({
      poolQuota: 1000,
      feeBps: 500,
      optionBetQuota: 5000,
      stakeQuota: 1000,
    })
    assert.equal(quote.refund, true)
    assert.equal(quote.payoutQuota, 1000)
  })

  test('本地估算绝不高于后端逐笔截断 + 残差归一人的结果', () => {
    /*
      后端：net=1000，三个赢家各押 1，winSum=3。
      前两笔 trunc(1000×1/3)=333，最后一笔拿残差 1000−666=334。
      本地对每一笔都截断，所以给出 333 —— 比最幸运的那一位少 1，
      与其余两位相等。多报一个单位就是界面替平台许一个它不一定兑得出的承诺。
    */
    const quote = qyLotGuessQuote({
      poolQuota: 999,
      feeBps: 0,
      optionBetQuota: 2,
      stakeQuota: 1,
    })
    assert.equal(quote.payoutQuota, 333)
    assert.ok(quote.payoutQuota <= 334)
  })
})

describe('盘口分布', () => {
  const spec: QyLotSpecItem[] = [
    { opt_no: 2, label: '不会涨', bet_quota: 2000, bet_count: 2 },
    { opt_no: 1, label: '会涨', bet_quota: 8000, bet_count: 8 },
  ]

  test('按 opt_no 升序，占比之和为 1', () => {
    const rows = qyLotGuessBoard({
      spec,
      poolQuota: 10000,
      feeBps: 500,
      stakeQuota: 1000,
    })
    assert.deepEqual(
      rows.map((row) => row.opt_no),
      [1, 2]
    )
    assert.equal(rows[0]?.share, 0.8)
    assert.equal(rows[1]?.share, 0.2)
  })

  test('押人多的那一项赔率更低 —— 这就是彩池的全部道理', () => {
    const rows = qyLotGuessBoard({
      spec,
      poolQuota: 10000,
      feeBps: 500,
      stakeQuota: 1000,
    })
    const crowded = rows[0]
    const thin = rows[1]
    assert.ok(crowded != null && thin != null)
    // pool=11000 fee=550 net=10450
    // 会涨   winSum=9000 → trunc(10450×1000/9000)=1161
    // 不会涨 winSum=3000 → trunc(10450×1000/3000)=3483
    assert.equal(crowded.quote.payoutQuota, 1161)
    assert.equal(thin.quote.payoutQuota, 3483)
    assert.ok(
      crowded.quote.multiple < thin.quote.multiple,
      '押注更集中的一项必须赔得更少，否则分布条讲的故事是假的'
    )
  })

  test('池子为 0 时占比是 0 而不是 NaN', () => {
    const rows = qyLotGuessBoard({
      spec: [
        { opt_no: 1, label: '甲', bet_quota: 0, bet_count: 0 },
        { opt_no: 2, label: '乙', bet_quota: 0, bet_count: 0 },
      ],
      poolQuota: 0,
      feeBps: 500,
      stakeQuota: 1000,
    })
    for (const row of rows) {
      assert.equal(row.share, 0)
      assert.equal(row.quote.refund, true)
    }
  })

  test('证据链端点不下发盘口时整块不渲染', () => {
    // 那三个字段（opt_no/label/is_catch_all）才是进承诺原像的，
    // bet_quota 不进。拿不到时画一排 0% 是一个错的数。
    assert.equal(
      hasQyLotGuessBoard([
        { opt_no: 1, label: '甲', is_catch_all: false },
        { opt_no: 2, label: '乙', is_catch_all: true },
      ]),
      false
    )
    assert.equal(hasQyLotGuessBoard(spec), true)
    assert.equal(hasQyLotGuessBoard(undefined), false)
  })
})

describe('结算之后的赔付：算的是已经发生的那一次分配', () => {
  test('与 Go 侧真打的那一场逐单位相等', () => {
    /*
      guess_pool_e2e_db_test.go 里真打过的那一场（真扣真发、核对到主库额度）：
      pool 3000、fee_bps 500、甲 1000（唯一赢家）、乙 2000。

      独立算：fee = trunc(3000 × 500 / 10000) = 150；net = 2850；
      winSum = 1000；押 1000 的那个人 pay = trunc(2850 × 1000 / 1000) = 2850。
      库里 qy_lot_payout 那一行就是 2850，主库 users.quota +2850。

      前瞻口径（把"再押一注"加进去）在同一组输入上是 1900 —— 少 33%。
      这两个数互相排斥，所以这条断言不可能被"随便算一个数"蒙过去。
    */
    const settled = qyLotGuessSettledQuote({
      poolQuota: 3000,
      feeBps: 500,
      winnerBetQuota: 1000,
      stakeQuota: 1000,
    })
    assert.equal(settled.payoutQuota, 2850)
    assert.equal(settled.profitQuota, 1850)
    assert.equal(settled.refund, false)

    const forward = qyLotGuessQuote({
      poolQuota: 3000,
      feeBps: 500,
      optionBetQuota: 1000,
      stakeQuota: 1000,
    })
    assert.equal(forward.payoutQuota, 1900, '前瞻口径与结算口径必须真的不同')
  })

  test('没人押中 / 全场押中同一项都退回本金，手续费一分不收', () => {
    for (const winnerBet of [0, 3000, 4000]) {
      const quote = qyLotGuessSettledQuote({
        poolQuota: 3000,
        feeBps: 500,
        winnerBetQuota: winnerBet,
        stakeQuota: 1000,
      })
      assert.equal(quote.refund, true, `winnerBet=${winnerBet}`)
      assert.equal(quote.payoutQuota, 1000)
      assert.equal(quote.multiple, 1)
    }
  })

  test('本地估算只会比实际到账少，绝不会多', () => {
    // 逐笔向零截断：pool 999、三个赢家各押 1（winSum 3），后端把残差归给
    // entry_no 字节序最大的那一位（333/333/334），前端一律 333。
    const quote = qyLotGuessSettledQuote({
      poolQuota: 999,
      feeBps: 0,
      winnerBetQuota: 3,
      stakeQuota: 1,
    })
    assert.equal(quote.payoutQuota, 333)
  })
})
