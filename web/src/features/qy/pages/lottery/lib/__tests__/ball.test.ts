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

import type { QyLotTier } from '../../types'
import {
  isQyLotBallPickComplete,
  isQyLotBallPoolValid,
  qyLotBallFormatPick,
  qyLotBallMatchProbability,
  qyLotBallRandomPick,
  qyLotBallTierOdds,
  qyLotBallUnreachableTiers,
  qyLotChoose,
  type QyLotBallPool,
} from '../ball'

/**
 * 双色球的选号格式与**中奖概率**。
 *
 * ## 这组用例守的是什么
 *
 * 各档概率是这套玩法里唯一向用户承诺的数字，而它**刻意不由后端下发**——后端给
 * 一个数就等于管理员在这件事上有了撒谎的接口。代价是：这份前端实现是那个数字
 * 的唯一来源，它算错了没有任何下游会发现，用户也无从对照。
 *
 * 所以断言全部用**手算出来的精确分数**，而不是"看起来差不多"的容差比较，并且
 * 逐条对齐后端 `MatchTier` 的语义：tier 升序、命中即停、一张票只中一档。
 */

/** 红 12 选 3、蓝 4 选 1。C(12,3)=220，C(4,1)=4，联合分母 880。 */
const POOL: QyLotBallPool = {
  redPool: 12,
  redPick: 3,
  bluePool: 4,
  bluePick: 1,
}

function tier(no: number, red: number, blue: number): QyLotTier {
  return {
    tier: no,
    name: `第 ${no} 档`,
    amount_quota: 0,
    count: 1,
    red_match: red,
    blue_match: blue,
  }
}

/** 浮点相等。分母都是精确整数，1e-12 已经远严于任何实现差异。 */
function assertClose(actual: number, expected: number, note: string): void {
  assert.ok(
    Math.abs(actual - expected) < 1e-12,
    `${note}: 期望 ${expected}，实际 ${actual}`
  )
}

describe('qyLotChoose', () => {
  test('在号池上界内给出精确整数', () => {
    // 逐步乘除而不是阶乘：36! 早就超过 MAX_SAFE_INTEGER，而这几个值都是精确的。
    assert.equal(qyLotChoose(12, 3), 220)
    assert.equal(qyLotChoose(4, 1), 4)
    assert.equal(qyLotChoose(36, 8), 30_260_340)
    assert.equal(qyLotChoose(33, 6), 1_107_568)
    assert.equal(qyLotChoose(5, 0), 1)
    assert.equal(qyLotChoose(5, 5), 1)
  })

  test('越界一律给 0 而不是抛错或 NaN', () => {
    assert.equal(qyLotChoose(3, 4), 0)
    assert.equal(qyLotChoose(3, -1), 0)
  })
})

describe('qyLotBallMatchProbability', () => {
  test('红球命中数的分布与手算的分数逐个相等', () => {
    // C(3,k)·C(9,3−k)/C(12,3)
    assertClose(qyLotBallMatchProbability(12, 3, 3), 1 / 220, '红中 3')
    assertClose(qyLotBallMatchProbability(12, 3, 2), 27 / 220, '红中 2')
    assertClose(qyLotBallMatchProbability(12, 3, 1), 108 / 220, '红中 1')
    assertClose(qyLotBallMatchProbability(12, 3, 0), 84 / 220, '红中 0')
  })

  test('整个分布之和恰为 1', () => {
    let sum = 0
    for (let hits = 0; hits <= 3; hits += 1) {
      sum += qyLotBallMatchProbability(12, 3, hits)
    }
    assertClose(sum, 1, '命中数分布之和')
  })
})

describe('qyLotBallTierOdds', () => {
  /** 一等 红3蓝1、二等 红3蓝0、三等 红2蓝1、四等 红1蓝0。 */
  const TIERS = [tier(1, 3, 1), tier(2, 3, 0), tier(3, 2, 1), tier(4, 1, 0)]

  test('逐档概率等于手算值，且"命中即停"的顺序被真的执行了', () => {
    // 手算（分母 880 = 220 × 4）：1 / 3 / 27 / 513。
    const odds = qyLotBallTierOdds(POOL, TIERS)
    assert.deepEqual(
      odds.map((item) => item.tier),
      [1, 2, 3, 4]
    )
    assertClose(odds[0].probability, 1 / 880, '一等奖')
    assertClose(odds[1].probability, 3 / 880, '二等奖')
    assertClose(odds[2].probability, 27 / 880, '三等奖')
    // 四等奖是"红中 1 个以上、且没有被前三档吃掉"的全部剩余，而不是 P(红≥1)。
    // 这一位若算成 P(红≥1) = 544/880，就是把前三档的概率又重复计了一遍 ——
    // 而那正是不按 MatchTier 的语义逐格归档时会犯的错。
    assertClose(odds[3].probability, 513 / 880, '四等奖')
  })

  test('未中奖的概率不会被摊进任何一档', () => {
    const won = qyLotBallTierOdds(POOL, TIERS).reduce(
      (sum, item) => sum + item.probability,
      0
    )
    // 红球一个都没中的那 84/220（= 336/880）不属于任何一档。
    assertClose(won, (880 - 336) / 880, '中奖概率合计')
  })

  test('odds 是"多少注中一注"，一等奖是 880', () => {
    assert.equal(qyLotBallTierOdds(POOL, [tier(1, 3, 1)])[0].odds, 880)
  })

  test('号池非法时返回空数组，而不是一列全是 0 的概率', () => {
    // 一列全是 0 会被读成"这一档一定不中"，那比不显示更糟。
    assert.deepEqual(
      qyLotBallTierOdds({ ...POOL, redPick: 0 }, [tier(1, 1, 1)]),
      []
    )
    assert.deepEqual(
      qyLotBallTierOdds({ ...POOL, redPool: 1 }, [tier(1, 1, 1)]),
      []
    )
  })

  test('永远开不出来的奖级概率为 0 且 odds 为 0', () => {
    // 二等奖的门槛不严于一等奖，MatchTier 命中即停之后它一个人都轮不到。
    const odds = qyLotBallTierOdds(POOL, [tier(1, 1, 0), tier(2, 3, 1)])
    assert.equal(odds[1].probability, 0)
    assert.equal(odds[1].odds, 0)
  })
})

describe('qyLotBallUnreachableTiers', () => {
  test('认出"等级数小的那一档门槛不严于等级数大的那一档"', () => {
    assert.deepEqual(
      qyLotBallUnreachableTiers([tier(1, 1, 0), tier(2, 3, 1)]),
      [{ higher: 1, lower: 2 }]
    )
  })

  test('门槛逐级放宽的奖级表没有冲突', () => {
    assert.deepEqual(
      qyLotBallUnreachableTiers([
        tier(1, 3, 1),
        tier(2, 3, 0),
        tier(3, 2, 1),
        tier(4, 1, 0),
      ]),
      []
    )
  })
})

describe('qyLotBallFormatPick', () => {
  test('两位补零、升序、逗号分隔、竖线分组 —— 与后端 FormatPick 逐字节一致', () => {
    // 这份字节就是进哈希链的那一份。格式差一个字符，用户手里的凭据就与链上的
    // 对不上，而他会把这件事读成"平台改了我的号"。
    assert.equal(
      qyLotBallFormatPick({ reds: [12, 3, 5], blues: [2] }),
      '03,05,12|02'
    )
    assert.equal(qyLotBallFormatPick({ reds: [1], blues: [16, 9] }), '01|09,16')
  })
})

describe('qyLotBallRandomPick', () => {
  // 机选只决定"买哪一组号"，不参与任何结果判定，所以这里断言的是**形状**而
  // 不是分布 —— 断言随机分布必然是一个会偶发失败的测试。
  //
  // 形状断言此前是"同一个号池跑 20 轮"：那 20 轮全部走 sampleDistinct 的同一条
  // 分支，不多覆盖一个判断，反过来真正危险的边界（take===poolSize 时拒绝采样
  // 会不会空转、take===1、poolSize===1）一轮都碰不到，而失败会表现为偶发红。
  // 换成按 (poolSize, take) 的边界表驱动，确定性地覆盖同样的东西并且更多。
  const shapeCases: {
    blue: [number, number]
    name: string
    red: [number, number]
  }[] = [
    { name: '常规号池', red: [12, 3], blue: [4, 1] },
    {
      name: '取满整池（红）：拒绝采样必须收敛，不能空转',
      red: [3, 3],
      blue: [4, 1],
    },
    { name: '取满整池（红蓝同时）', red: [6, 6], blue: [2, 2] },
    { name: '池子只有一个球', red: [1, 1], blue: [1, 1] },
    { name: '只取一个', red: [33, 1], blue: [16, 1] },
    { name: '后端上界 36/8 与 16/2', red: [36, 8], blue: [16, 2] },
  ]

  for (const tc of shapeCases) {
    test(`${tc.name}：个数、去重、升序、范围`, () => {
      const pool = {
        redPool: tc.red[0],
        redPick: tc.red[1],
        bluePool: tc.blue[0],
        bluePick: tc.blue[1],
      }
      const pick = qyLotBallRandomPick(pool)

      assert.equal(pick.reds.length, pool.redPick)
      assert.equal(pick.blues.length, pool.bluePick)
      assert.equal(new Set(pick.reds).size, pool.redPick, '红球出现重号')
      assert.equal(new Set(pick.blues).size, pool.bluePick, '蓝球出现重号')
      assert.deepEqual(
        [...pick.reds].sort((a, b) => a - b),
        pick.reds,
        '红球必须升序 —— 归一化后的字节就是进哈希链的字节'
      )
      assert.deepEqual(
        [...pick.blues].sort((a, b) => a - b),
        pick.blues
      )
      assert.ok(Math.min(...pick.reds) >= 1, '红球越出下界')
      assert.ok(Math.max(...pick.reds) <= pool.redPool, '红球越出池子')
      assert.ok(Math.min(...pick.blues) >= 1, '蓝球越出下界')
      assert.ok(Math.max(...pick.blues) <= pool.bluePool, '蓝球越出池子')
      assert.equal(isQyLotBallPickComplete(pick, pool), true)
    })
  }

  test('取满整池时结果与随机数无关，恒等于全池', () => {
    // 这一条不依赖任何随机性：poolSize === take 时唯一合法的答案就是全池，
    // 所以它可以断言**确切的值**而不只是形状。sampleDistinct 一旦在这个边界
    // 上少取一个或者死循环，这里立刻红。
    const pick = qyLotBallRandomPick({
      redPool: 5,
      redPick: 5,
      bluePool: 2,
      bluePick: 2,
    })
    assert.deepEqual(pick.reds, [1, 2, 3, 4, 5])
    assert.deepEqual(pick.blues, [1, 2])
  })
})

describe('isQyLotBallPoolValid', () => {
  test('与后端 ValidateBallPool 同一组边界', () => {
    assert.equal(isQyLotBallPoolValid(POOL), true)
    assert.equal(isQyLotBallPoolValid({ ...POOL, redPool: 1 }), false)
    assert.equal(isQyLotBallPoolValid({ ...POOL, redPool: 37 }), false)
    assert.equal(isQyLotBallPoolValid({ ...POOL, redPick: 9 }), false)
    // 选数不能超过池大小。
    assert.equal(
      isQyLotBallPoolValid({
        redPool: 2,
        redPick: 3,
        bluePool: 4,
        bluePick: 1,
      }),
      false
    )
    assert.equal(isQyLotBallPoolValid({ ...POOL, bluePick: 3 }), false)
    assert.equal(isQyLotBallPoolValid({ ...POOL, bluePool: 17 }), false)
  })
})
