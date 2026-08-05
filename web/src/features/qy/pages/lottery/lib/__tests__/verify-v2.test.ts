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

import type { QyLotProofEntry, QyLotTier } from '../../types'
import {
  qyLotBallDraw,
  qyLotBandOf,
  qyLotBands,
  qyLotChainNext,
  qyLotParsePick,
  qyLotPickWinnersProb,
  qyLotRollPpm,
  qyLotRosterHash,
  qyLotSpecHash,
} from '../verify'

/**
 * `lot-v2` 的协议不变式与**跨实现**黄金向量。
 *
 * ## 向量从哪里来
 *
 * 下面 `GOLDEN` 里的每一个值都不是从本文件的实现里跑出来的 —— 那等于用被测
 * 代码证明被测代码，全绿的时候什么都没保证。它们由另外两份独立实现产出并
 * 逐字节抄进来：
 *
 *   · 生产实现 `qianye/modules/lottery/commit.go`（Go）；
 *   · 离线验证脚本 `qianye/docs/lottery-verify.py`（纯 Python，零第三方依赖）。
 *
 * 三份实现在同一组输入上逐位一致，这个文件锁的就是那一致性。任何一份改了
 * 编码（分隔符、字段顺序、域前缀、截断规则），这里都会立刻变红，而不是悄悄
 * 换掉一套所有人都在用的协议。
 *
 * ## 除向量之外还锁了什么
 *
 *   · 摇号量的边界可以**手算**（见每条注释），因此连向量都不必信；
 *   · 区间必须左闭右开且不重叠；
 *   · 超募均分必须**精确守恒**（差一个单位就是有人的钱不见了或平台倒贴）；
 *   · v2 的原像与 v1 **必须不同**（共用一个原像就等于版本号是假的）。
 *
 * ## 一处诚实的缺口
 *
 * `qyLotBallDraw` 只与 Go 对齐过，**Python 脚本尚未实现双色球摇号**
 * （它目前只覆盖到 commit 原像里的号池字段）。所以那一组断言现在是两方一致
 * 而不是三方，下面的用例里显式标了出来。
 */

const FINAL_SEED =
  '00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff'

function entry(entryNo: string, userRef: string): QyLotProofEntry {
  return {
    seq: 1,
    entry_no: entryNo,
    user_ref: userRef,
    opt_no: 0,
    amount: 50,
    status: 'success',
    prev_hash: '',
    chain_hash: '',
    order_no: '',
  }
}

const ROSTER = [
  entry('E0001', 'ua'),
  entry('E0002', 'ub'),
  entry('E0003', 'uc'),
  entry('E0004', 'ud'),
]

function tier(over: Partial<QyLotTier> & Pick<QyLotTier, 'tier'>): QyLotTier {
  return {
    name: `T${over.tier}`,
    amount_quota: 0,
    count: 0,
    prize_type: 'quota',
    win_ppm: 0,
    text_desc: '',
    red_match: 0,
    blue_match: 0,
    pool_share_bps: 0,
    ...over,
  }
}

/**
 * 三方一致的黄金向量（Go / Python / 本文件）。
 *
 * 产出方式见文件头。改这些数字之前先确认另外两份实现也改了 —— 否则线上会出现
 * "平台算的和用户自己验的不一样"，而那正是这套协议唯一不能出的错。
 */
const GOLDEN = {
  chainV1: '2dead9c00b0c544eb8bec8f4213dde523a7e15eb89ba523c36b42eef88da47c2',
  chainV2: '5e76c953c17ae56f6600e969f0ff270ba5cb7e66c2a20b66fe6ba5bfc856ca94',
  rosterV1: 'b29263411c1db24c8c60232381edee8813102e056cf905764a6c60407c4708b5',
  rosterV2: 'ab61d2c6d74a6fb26c301070dd90e4cab91f498a448e8b369b7b05a3ec90e854',
  specV2: '1525b902818f8ba684354676b458624e8a6ff0427669b6620b5f21fe354bbc74',
  /** 与 Go 一致；Python 脚本尚未实现摇号，这两行目前是**两方**一致。 */
  ballRed: [2, 4, 7, 9],
  ballBlue: [2, 5, 11],
} as const

/** 黄金向量用的那一条记录（v1 下 pick 恒为空串）。 */
const GOLDEN_LINE: QyLotProofEntry = {
  ...entry('E0001', 'ua'),
  pick: '03,05,12|08',
}

describe('lot-v2 跨实现黄金向量', () => {
  test('链推进与 Go / Python 逐位一致', async () => {
    assert.equal(
      await qyLotChainNext('', 'A', { ...GOLDEN_LINE, pick: '' }),
      GOLDEN.chainV1
    )
    assert.equal(
      await qyLotChainNext('', 'A', GOLDEN_LINE, 'lot-v2'),
      GOLDEN.chainV2
    )
  })

  test('名单哈希与 Go / Python 逐位一致', async () => {
    assert.equal(
      await qyLotRosterHash('A', 'C', [{ ...GOLDEN_LINE, pick: '' }]),
      GOLDEN.rosterV1
    )
    assert.equal(
      await qyLotRosterHash('A', 'C', [GOLDEN_LINE], 'lot-v2'),
      GOLDEN.rosterV2
    )
  })

  /**
   * 十个字段的顺序与恒等式位（quota 档的 `text_desc` 是空串、非 ball 的三列
   * 是 0）在这一个哈希里全部被钉死。少写一个占位就等于允许管理员在不动
   * 承诺的前提下把一档额度奖改成文本奖。
   */
  test('奖档行的十个字段顺序与 Go / Python 逐位一致', async () => {
    // 刻意用字面量拼原像而不是调 `qyLotSpecLines`：那个函数正是被测对象之一，
    // 拿它生成原像等于把"字段顺序对不对"这个问题整个绕过去。
    const line = [
      '1',
      'T1',
      'quota',
      '1000',
      '2',
      '1000',
      '',
      '0',
      '0',
      '0',
    ].join('')
    assert.equal(await qyLotSpecHash([line], 'lot-v2'), GOLDEN.specV2)
  })

  test('双色球摇号与生产实现（Go）逐位一致', async () => {
    assert.deepEqual(
      await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'red', 12, 4),
      [...GOLDEN.ballRed]
    )
    assert.deepEqual(
      await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'blue', 12, 3),
      [...GOLDEN.ballBlue]
    )
  })
})

describe('lot-v2 摇号量', () => {
  /**
   * `r = floor(u64 × 10^6 / 2^64)`，四个边界全部可以手算：
   *   · 0x0000000000000000 → 0
   *   · 0x8000000000000000 = 2^63 → 10^6 / 2 = 500000
   *   · 0xffff000000000000 = 65535·2^48 → 65535·10^6 / 65536 = 999984.74… → 999984
   *   · 0xffffffffffffffff → 999999（不可能等于 10^6，区间是左闭右开的）
   */
  test('缩放是精确的截断映射，边界可手算', () => {
    assert.equal(qyLotRollPpm(`0000000000000000${'0'.repeat(48)}`), 0)
    assert.equal(qyLotRollPpm(`8000000000000000${'0'.repeat(48)}`), 500_000)
    assert.equal(qyLotRollPpm(`ffff000000000000${'0'.repeat(48)}`), 999_984)
    assert.equal(qyLotRollPpm(`ffffffffffffffff${'f'.repeat(48)}`), 999_999)
  })

  test('只吃票面前 64 位，后面的字节不参与', () => {
    const head = '8000000000000000'
    assert.equal(
      qyLotRollPpm(`${head}${'0'.repeat(48)}`),
      qyLotRollPpm(`${head}${'f'.repeat(48)}`)
    )
  })

  /**
   * 畸形票面回落成 `PpmDen`（落在全部区间之外 = 不中），而**不是** 0。
   * 回落成 0 会让一张解不开的票直接中一等奖 —— 方向完全错误的失败。
   * 与后端 `RollPpm` 逐字同一条口径。
   */
  test('票面不是合法十六进制时回落成"不中"，而不是"必中"', () => {
    assert.equal(qyLotRollPpm('zz'), 1_000_000)
    assert.equal(qyLotRollPpm('00ff'), 1_000_000)
    assert.equal(qyLotRollPpm(''), 1_000_000)
  })
})

describe('lot-v2 概率区间', () => {
  test('按 tier 升序累加，左闭右开且互不相交', () => {
    const bands = qyLotBands([
      tier({ tier: 2, win_ppm: 10_000 }),
      tier({ tier: 1, win_ppm: 1000 }),
    ])
    assert.deepEqual(bands, [
      { tier: 1, loPpm: 0, hiPpm: 1000 },
      { tier: 2, loPpm: 1000, hiPpm: 11_000 },
    ])
    // 边界归属：999 是一等奖的最后一个值，1000 已经属于二等奖。
    assert.equal(qyLotBandOf(bands, 0)?.tier, 1)
    assert.equal(qyLotBandOf(bands, 999)?.tier, 1)
    assert.equal(qyLotBandOf(bands, 1000)?.tier, 2)
    assert.equal(qyLotBandOf(bands, 10_999)?.tier, 2)
  })

  /**
   * 「没有人中」是一等公民结果，不是异常分支 —— 项目方原话就是
   * 「可以写中奖概率，不是说必须要有中奖人」。
   */
  test('落在全部区间之外 = 没中，而不是回落到最后一档', () => {
    const bands = qyLotBands([tier({ tier: 1, win_ppm: 1000 })])
    assert.equal(qyLotBandOf(bands, 1000), null)
    assert.equal(qyLotBandOf(bands, 999_999), null)
  })

  test('概率之和超过 100% 时直接报错，不猜', () => {
    assert.throws(() =>
      qyLotBands([
        tier({ tier: 1, win_ppm: 600_000 }),
        tier({ tier: 2, win_ppm: 600_000 }),
      ])
    )
  })
})

describe('lot-v2 概率制派奖', () => {
  /**
   * `win_ppm = 1_000_000` 让全部四张票必中一等奖，因此命中集合与哈希无关、
   * 完全确定 —— 这样下面的金额可以逐个手算，而不用先知道任何 HMAC 的值。
   */
  test('未超募：每人拿到公示的金额', async () => {
    const tiers = [
      tier({ tier: 1, win_ppm: 1_000_000, count: 4, amount_quota: 10 }),
    ]
    const winners = await qyLotPickWinnersProb(
      FINAL_SEED,
      'LOTTESTACT01',
      ROSTER,
      tiers
    )
    assert.deepEqual(
      winners.map((winner) => [winner.entry_no, winner.amount]),
      [
        ['E0001', 10],
        ['E0002', 10],
        ['E0003', 10],
        ['E0004', 10],
      ]
    )
  })

  /**
   * 超募时**摊薄的是金额，不是概率**：四个人分 count×amount = 10 的预算。
   * 10/4 = 2 余 2，所以前三笔各 2、最后一笔 4（残差归 `entry_no` 字节序最大者，
   * 与竞猜奖池的口径逐字节相同）。
   *
   * 守恒式必须**精确**成立：多一个单位就是净增发超过了发布时校验过的上限，
   * 而那道上限（`Σ count × amount ≤ MaxTotalPrizeQuota`）正是概率模式不引入
   * 任何新发行风险的全部理由。
   */
  test('超募：预算均分，逐笔截断 + 残差归 entry_no 最大者，总额精确守恒', async () => {
    const tiers = [
      tier({ tier: 1, win_ppm: 1_000_000, count: 1, amount_quota: 10 }),
    ]
    const winners = await qyLotPickWinnersProb(
      FINAL_SEED,
      'LOTTESTACT01',
      ROSTER,
      tiers
    )
    assert.deepEqual(
      winners.map((winner) => [winner.entry_no, winner.amount]),
      [
        ['E0001', 2],
        ['E0002', 2],
        ['E0003', 2],
        ['E0004', 4],
      ]
    )
    assert.equal(
      winners.reduce((sum, winner) => sum + winner.amount, 0),
      10,
      '总支出必须恰好等于本档预算'
    )
  })

  /**
   * 文本奖的 `amount` 恒为 0，`count` 对它而言是实物份数而不是预算。
   * "均分 0" 没有意义，所以它不参与摊薄 —— 验证器如实列出全部命中者
   * （W > count 时那就是一次超发），而不是自作主张裁掉谁。
   */
  test('文本奖不被摊薄成 0 元，全部命中者都在名单里', async () => {
    const tiers = [
      tier({
        tier: 1,
        win_ppm: 1_000_000,
        count: 1,
        amount_quota: 0,
        prize_type: 'text',
        text_desc: '联系客服领取',
      }),
    ]
    const winners = await qyLotPickWinnersProb(
      FINAL_SEED,
      'LOTTESTACT01',
      ROSTER,
      tiers
    )
    assert.equal(winners.length, 4)
    assert.deepEqual(
      winners.map((winner) => winner.amount),
      [0, 0, 0, 0]
    )
  })

  test('概率为 0 的一档谁也中不了', async () => {
    const winners = await qyLotPickWinnersProb(
      FINAL_SEED,
      'LOTTESTACT01',
      ROSTER,
      [tier({ tier: 1, win_ppm: 0, count: 1, amount_quota: 10 })]
    )
    assert.deepEqual(winners, [])
  })
})

describe('lot-v2 双色球摇号', () => {
  test('结果确定、升序、无重复、恰好 k 个且都在号池内', async () => {
    const first = await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'red', 12, 4)
    const again = await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'red', 12, 4)
    assert.deepEqual(first, again)
    assert.equal(first.length, 4)
    assert.equal(new Set(first).size, 4)
    assert.deepEqual(
      first,
      [...first].sort((a, b) => a - b)
    )
    for (const ball of first) {
      assert.ok(ball >= 1 && ball <= 12)
    }
  })

  /**
   * 红蓝两组共用同一个 `final_seed`，靠**域里的颜色标签**分开。撞成同一组
   * 意味着蓝球完全由红球决定，那不是两次摇号。
   */
  test('红球与蓝球是两次独立的摇号，不是同一个序列的两段', async () => {
    const reds = await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'red', 12, 3)
    const blues = await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'blue', 12, 3)
    assert.notDeepEqual(reds, blues)
  })

  test('换一场活动就换一组号（act_no 进域）', async () => {
    const a = await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT01', 'red', 16, 5)
    const b = await qyLotBallDraw(FINAL_SEED, 'LOTTESTACT02', 'red', 16, 5)
    assert.notDeepEqual(a, b)
  })

  test('选号串按两位补零解析，格式不对直接报错', () => {
    assert.deepEqual(qyLotParsePick('03,05,12|08'), {
      reds: [3, 5, 12],
      blues: [8],
    })
    assert.throws(() => qyLotParsePick('3,5,12|8'))
  })
})

describe('lot-v2 原像与 v1 隔离', () => {
  /**
   * 两个版本共用同一个原像，等于版本号是假的：那样一来 v2 活动的证据链会被
   * v1 验证器判成"通过"，而它多出来的那些字段（选号、池子、概率）一个都没有
   * 进哈希 —— 事后随便改。
   */
  test('同一条记录在 v1 与 v2 下算出不同的链哈希', async () => {
    const line = { ...ROSTER[0], pick: '' }
    const v1 = await qyLotChainNext('', 'LOTTESTACT01', line)
    const v2 = await qyLotChainNext('', 'LOTTESTACT01', line, 'lot-v2')
    assert.notEqual(v1, v2)
  })

  /**
   * 选号必须进链与名单：不进的话，平台可以在开奖后把某个人的号改成中奖号，
   * 而链尾、seq 连续、名单重算三道校验会**照常全部通过**。
   */
  test('改掉选号 → v2 的链与名单立刻变，v1 则完全无感（因此 v1 不能办双色球）', async () => {
    const before = { ...ROSTER[0], pick: '03,05,12|08' }
    const after = { ...ROSTER[0], pick: '03,09,12|05' }

    assert.notEqual(
      await qyLotChainNext('', 'A', before, 'lot-v2'),
      await qyLotChainNext('', 'A', after, 'lot-v2')
    )
    assert.equal(
      await qyLotChainNext('', 'A', before),
      await qyLotChainNext('', 'A', after)
    )

    assert.notEqual(
      await qyLotRosterHash('A', 'C', [before], 'lot-v2'),
      await qyLotRosterHash('A', 'C', [after], 'lot-v2')
    )
    assert.equal(
      await qyLotRosterHash('A', 'C', [before]),
      await qyLotRosterHash('A', 'C', [after])
    )
  })
})
