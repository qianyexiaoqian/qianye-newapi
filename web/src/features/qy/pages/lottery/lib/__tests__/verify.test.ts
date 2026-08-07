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

import type { QyLotProof, QyLotProofEntry } from '../../types'
import {
  qyLotBallDraw,
  qyLotChainNext,
  qyLotCommitHash,
  qyLotFinalSeed,
  qyLotParsePick,
  qyLotRosterHash,
  qyLotRulesHash,
  qyLotSpecHash,
  qyLotSpecLines,
  qyLotSplitPool,
  verifyQyLotProof,
} from '../verify'

/**
 * `lot-v1` 的**跨实现**回归测试。
 *
 * 下面这份向量不是从本文件的实现里跑出来的 —— 那等于用被测代码证明被测代码。
 * 它由一份只依赖 `hashlib` / `hmac` 的独立 Python 实现算出（与仓库里
 * `qianye/docs/lottery-verify.py` 同一套编码），逐字节抄进来。
 *
 * 因此这个文件真正锁住的是**线上协议**：
 *   · 分隔符是 `0x1F` 而不是 `|`；
 *   · `bool` 编码是 `"true"` / `"false"`；
 *   · 名单行之间用 `\n`、字段之间用 SEP（唯一一处两级分隔）；
 *   · `final_seed` 绑定 `roster_hash`（少了它，知道种子的人能提前锁定名次）；
 *   · `allow_multi_win=false` 时同一个 `user_ref` 只保留最靠前的一张，
 *     且被跳过的票**不消耗随机性**。
 *
 * 任何一处编码改动都会让本用例整片变红，而不是悄悄换掉一套所有人都在用的协议。
 */

const ENTRIES: QyLotProofEntry[] = [
  {
    seq: 1,
    entry_no: 'E0001',
    user_ref: 'ua',
    opt_no: 0,
    amount: 50,
    status: 'success',
    prev_hash:
      'e2dacfa0b3eaa85d934a908a31d1d8eb702347aa1b1b57947cbfde660d8af529',
    chain_hash:
      'f35a7983f90a41faa460cf539cf96ffffb42056114aa81d80e70a177840a655d',
    order_no: 'FO-E0001',
  },
  {
    seq: 2,
    entry_no: 'E0002',
    user_ref: 'ub',
    opt_no: 0,
    amount: 50,
    status: 'success',
    prev_hash:
      'f35a7983f90a41faa460cf539cf96ffffb42056114aa81d80e70a177840a655d',
    chain_hash:
      '4d114e23d717e484ef29f9d4b56d2aaf23587f3cce93dbd5e40fbe206bb348e3',
    order_no: 'FO-E0002',
  },
  {
    // 失败条目**永久留在链上并占一个 seq**：删掉即破链。
    seq: 3,
    entry_no: 'E0003',
    user_ref: 'ua',
    opt_no: 0,
    amount: 50,
    status: 'failed',
    prev_hash:
      '4d114e23d717e484ef29f9d4b56d2aaf23587f3cce93dbd5e40fbe206bb348e3',
    chain_hash:
      '255c763d5e46f5464ae604a59d3fd30a7c52ad711e7f589cdfeed09b0b29ed6a',
    order_no: 'FO-E0003',
  },
  {
    seq: 4,
    entry_no: 'E0004',
    user_ref: 'ua',
    opt_no: 0,
    amount: 50,
    status: 'success',
    prev_hash:
      '255c763d5e46f5464ae604a59d3fd30a7c52ad711e7f589cdfeed09b0b29ed6a',
    chain_hash:
      '997978b3c12e08ae773d48bd4183628e83c4bd4c446e58991722620e842accfa',
    order_no: 'FO-E0004',
  },
  {
    seq: 5,
    entry_no: 'E0005',
    user_ref: 'uc',
    opt_no: 0,
    amount: 50,
    status: 'success',
    prev_hash:
      '997978b3c12e08ae773d48bd4183628e83c4bd4c446e58991722620e842accfa',
    chain_hash:
      'b02d05e0fc74c92a42835c7f18d66885086f765e3049b0de70bb5865fadd9470',
    order_no: 'FO-E0005',
  },
]

function drawProof(): QyLotProof {
  return {
    algo: 'lot-v1',
    act_no: 'LOTTESTACT01',
    kind: 'draw',
    status: 'finished',
    outcome: 'drawn',
    title: 'vector',
    rules_text: '{"min_quota":100}',
    rules_hash:
      'dd32d119f5a973dcb2833dceaee06096d49869e7c5081d6e2d5af72f52d4264b',
    // 线上的 spec 是**扁平数组**（抽奖填 tier 那一组字段）。这份形状本身就是
    // 协议的一部分：它逐行进 spec_hash，三份实现逐字节对齐。
    spec: [
      { tier: 1, name: 'T1', amount_quota: 1000, count: 1 },
      { tier: 2, name: 'T2', amount_quota: 100, count: 2 },
    ],
    spec_hash:
      'eb4eb47aee6304b6be83ee82dce4a3a2317c736781c3b58c2db3f5d342899503',
    stake_quota: 50,
    open_at: 1000,
    close_at: 2000,
    draw_at: 3000,
    settle_deadline: 4000,
    allow_multi_win: false,
    fee_bps: 0,
    no_winner_policy: 'refund_all',
    min_entries_to_hold: 0,
    seed: '00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff',
    commit_hash:
      'e2dacfa0b3eaa85d934a908a31d1d8eb702347aa1b1b57947cbfde660d8af529',
    chain_head:
      'b02d05e0fc74c92a42835c7f18d66885086f765e3049b0de70bb5865fadd9470',
    entries: ENTRIES.map((entry) => ({ ...entry })),
    total: ENTRIES.length,
    roster_hash:
      '8bc376b8b73d7e12a02e3bcbbe5eab0aa232932a366f80c429b474ec19d82fe7',
    roster_count: 4,
    winners: [
      // E0001 与 E0004 同属 ua：E0004 名次更靠前，E0001 被跳过。
      { pos: 0, tier: 1, entry_no: 'E0004', user_ref: 'ua', amount: 1000 },
      { pos: 1, tier: 2, entry_no: 'E0002', user_ref: 'ub', amount: 100 },
      { pos: 2, tier: 2, entry_no: 'E0005', user_ref: 'uc', amount: 100 },
    ],
    pool_quota: 0,
    win_opt_no: 0,
    fee_quota: 0,
    payouts: [],
    locked_at: 2001,
    revealed_at: 3001,
    settled_at: 3002,
  }
}

function statusOf(steps: { key: string; status: string }[], key: string) {
  return steps.find((step) => step.key === key)?.status
}

describe('lot-v1 证据链验证（抽奖）', () => {
  test('与独立实现算出的向量逐项一致', async () => {
    const steps = await verifyQyLotProof(drawProof())
    assert.deepEqual(
      steps.map((step) => [step.key, step.status]),
      [
        ['rules', 'ok'],
        ['spec', 'ok'],
        ['commit', 'ok'],
        ['chain', 'ok'],
        ['roster', 'ok'],
        ['result', 'ok'],
      ]
    )
  })

  test('改动任意一条已成交条目的金额 → 链立刻断，且不会误判成"名单没变"', async () => {
    const proof = drawProof()
    proof.entries[1].amount = 51
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'fail')
    assert.equal(statusOf(steps, 'roster'), 'fail')
  })

  test('抽掉一条失败条目 → seq 出现空洞，链断', async () => {
    const proof = drawProof()
    proof.entries = proof.entries.filter((entry) => entry.seq !== 3)
    proof.total = proof.entries.length
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'fail')
  })

  test('换掉种子（哈希不改）→ 承诺对不上', async () => {
    const proof = drawProof()
    proof.seed = 'ff'.repeat(32)
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'commit'), 'fail')
  })

  test('偷偷把一个人从中奖名单换掉 → 复算结果不一致', async () => {
    const proof = drawProof()
    proof.winners[0] = {
      pos: 0,
      tier: 1,
      entry_no: 'E0001',
      user_ref: 'ua',
      amount: 1000,
    }
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('只拿到一页时不验链与名单，而是如实标 skipped', async () => {
    const proof = drawProof()
    proof.entries = proof.entries.slice(0, 2)
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'skipped')
    assert.equal(statusOf(steps, 'roster'), 'skipped')
    assert.equal(statusOf(steps, 'result'), 'skipped')
  })

  test('尚未揭示种子时，承诺与结果标 skipped 而不是 ok', async () => {
    const proof = drawProof()
    proof.seed = ''
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'commit'), 'skipped')
    assert.equal(statusOf(steps, 'result'), 'skipped')
    // 链与名单不依赖种子，仍然要验 —— 它们正是"揭示之前就已冻结"的证据。
    assert.equal(statusOf(steps, 'chain'), 'ok')
    assert.equal(statusOf(steps, 'roster'), 'ok')
  })
})

function bet(entryNo: string, optNo: number, amount: number): QyLotProofEntry {
  return {
    seq: 0,
    entry_no: entryNo,
    user_ref: entryNo,
    opt_no: optNo,
    amount,
    status: 'success',
    prev_hash: '',
    chain_hash: '',
    order_no: '',
  }
}

describe('竞猜奖池分配', () => {
  test('守恒：逐笔截断 + 残差归 entry_no 最大的赢家', () => {
    // 池 1000、费率 5% → fee 50、net 950；赢家 3:2:1（按 entry_no 升序）。
    const roster = [
      bet('B1', 1, 300),
      bet('B2', 1, 200),
      bet('B3', 1, 100),
      bet('B4', 2, 400),
    ]
    const result = qyLotSplitPool(roster, 1, 500)
    assert.equal(result.refundedAll, false)
    assert.equal(result.fee, 50)
    assert.deepEqual(result.payouts, [
      { entry_no: 'B1', amount: 475 },
      { entry_no: 'B2', amount: 316 },
      // 残差 950-475-316=159，比 950*100/600=158 多 1 —— 归最后一位。
      { entry_no: 'B3', amount: 159 },
    ])
    const paid = result.payouts.reduce((sum, item) => sum + item.amount, 0)
    assert.equal(paid + result.fee, 1000)
  })

  test('全部猜错 → 全额退回本金，手续费一分不收', () => {
    const roster = [bet('B1', 1, 300), bet('B2', 2, 700)]
    const result = qyLotSplitPool(roster, 3, 2000)
    assert.equal(result.refundedAll, true)
    assert.equal(result.fee, 0)
    assert.deepEqual(result.payouts, [
      { entry_no: 'B1', amount: 300 },
      { entry_no: 'B2', amount: 700 },
    ])
  })

  test('无输家（所有人都押中）→ 同样全额退回，不能让人人因手续费而亏钱', () => {
    const roster = [bet('B1', 1, 300), bet('B2', 1, 700)]
    const result = qyLotSplitPool(roster, 1, 2000)
    assert.equal(result.refundedAll, true)
    assert.equal(result.fee, 0)
  })

  test('1 单位分给 3 个赢家：不产生 0.33，也不多发一分', () => {
    const roster = [
      bet('B1', 1, 1),
      bet('B2', 1, 1),
      bet('B3', 1, 1),
      bet('B4', 2, 1),
    ]
    const result = qyLotSplitPool(roster, 1, 0)
    const paid = result.payouts.reduce((sum, item) => sum + item.amount, 0)
    assert.equal(paid + result.fee, 4)
    assert.deepEqual(
      result.payouts.map((item) => item.amount),
      [1, 1, 2]
    )
  })
})

describe('取消 / 流局的抽奖不能被判成"名单对不上"', () => {
  test('整场取消 → 结果一步标 skipped，而不是红叉', async () => {
    // 取消之后种子照样公开（commit_hash 仍要可复算），但没有开出结果，
    // winners 恒为空。拿它去比对复算名单只会得到一个必然的失败 ——
    // 官方工具在完全诚实的数据上指控平台作弊，比不提供验证更糟。
    const proof = drawProof()
    proof.status = 'settling'
    proof.outcome = 'cancelled'
    proof.winners = []
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'ok')
    assert.equal(statusOf(steps, 'roster'), 'ok')
    assert.equal(statusOf(steps, 'result'), 'skipped')
  })

  test('人数不足流局 → 同样不判失败', async () => {
    const proof = drawProof()
    proof.outcome = 'void_min_entries'
    proof.winners = []
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'skipped')
  })
})

describe('竞猜结算：被排除条目的退款不能污染逐笔赔付的比对', () => {
  /** 现算一份自洽的竞猜证据链：链从 commit_hash 起步，名单哈希取自同一份数据。 */
  async function guessProof(): Promise<QyLotProof> {
    const actNo = 'LOTGUESS0001'
    const commit = 'aa'.repeat(32)
    const raw = [
      { entry_no: 'G1', user_ref: 'u1', opt_no: 1, amount: 600 },
      { entry_no: 'G2', user_ref: 'u2', opt_no: 2, amount: 400 },
    ]
    const entries: QyLotProofEntry[] = []
    let prev = commit
    for (const [index, item] of raw.entries()) {
      const entry: QyLotProofEntry = {
        seq: index + 1,
        entry_no: item.entry_no,
        user_ref: item.user_ref,
        opt_no: item.opt_no,
        amount: item.amount,
        status: 'success',
        prev_hash: prev,
        chain_hash: '',
        order_no: '',
      }
      entry.chain_hash = await qyLotChainNext(prev, actNo, entry)
      prev = entry.chain_hash
      entries.push(entry)
    }
    // 名单按 entry_no 的字节序排 —— 与后端 roster 行的顺序同一条规则。
    const roster = [...entries].sort((a, b) =>
      a.entry_no.localeCompare(b.entry_no)
    )
    return {
      algo: 'lot-v1',
      act_no: actNo,
      kind: 'guess',
      status: 'finished',
      outcome: 'drawn',
      title: 'guess',
      rules_text: '{}',
      rules_hash: '',
      spec: [
        { opt_no: 1, label: 'A', is_catch_all: false },
        { opt_no: 2, label: 'B', is_catch_all: true },
      ],
      spec_hash: '',
      stake_quota: 100,
      open_at: 1,
      close_at: 2,
      draw_at: 3,
      settle_deadline: 4,
      allow_multi_win: false,
      fee_bps: 500,
      no_winner_policy: 'refund_all',
      min_entries_to_hold: 0,
      seed: '11'.repeat(32),
      commit_hash: commit,
      chain_head: prev,
      entries,
      total: entries.length,
      roster_hash: await qyLotRosterHash(actNo, commit, roster),
      roster_count: roster.length,
      winners: [],
      pool_quota: 1000,
      win_opt_no: 1,
      fee_quota: 50,
      payouts: [
        { entry_no: 'G1', kind: 'win', amount: 950, status: 'paid' },
        // 封盘时还没落定的那一条走退款，它**不是**分配结果的一部分。
        { entry_no: 'GX', kind: 'refund', amount: 100, status: 'paid' },
      ],
      locked_at: 2,
      revealed_at: 3,
      settled_at: 4,
    }
  }

  test('赔付比对只看 kind=win，退款不参与', async () => {
    const steps = await verifyQyLotProof(await guessProof())
    assert.equal(statusOf(steps, 'roster'), 'ok')
    assert.equal(statusOf(steps, 'result'), 'ok')
  })

  test('真的少发了一笔赔付时仍然报错', async () => {
    const proof = await guessProof()
    proof.payouts[0].amount = 900
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'fail')
  })
})

/**
 * 双色球的 `result` 一步曾经是一整条 `skipped`。
 *
 * 那是这份面板上最贵的一个洞：平台把某一期的 `ball_result` 直接改库写成另一组
 * 号（payout 表不动），用户点「公正性验证」会看到 commit / rules / spec / roster
 * 四步全绿、result 一栏写着 `skipped(draw_mode=ball)` —— **没有一个红叉**。
 * 想发现这件事，用户得先在详情页记下开奖号，再打开「为什么是这个结果」弹窗
 * 看本地复算值，逐个数字比对，而两处的号码格式还不一样。
 *
 * 开奖号是 `(final_seed, act_no, 号池)` 的纯函数、档位是 `(开奖号, 选号, 门槛)`
 * 的纯函数，两者的输入全部在证据链里。所以这一节钉的是：**这两件事必须被自动
 * 比对，而且改任何一边都要变红。**
 */
describe('双色球：开奖号与中奖档位必须被自动比对', () => {
  const SEED =
    '00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff'
  const ACT_NO = 'LOTBALLACT01'

  /** 一期已开奖的双色球。链、名单、承诺全部由库函数现算，断言只针对 result。 */
  async function ballProof(): Promise<QyLotProof> {
    const picks: Record<string, string> = {
      B0001: '01,02,03|01',
      B0002: '04,05,06|02',
      B0003: '02,04,07|03',
    }
    const base = {
      algo: 'lot-v2',
      act_no: ACT_NO,
      kind: 'draw' as const,
      status: 'finished' as const,
      outcome: 'drawn' as const,
      title: 'ball',
      rules_text: '{"min_quota":0}',
      spec: [
        {
          tier: 1,
          name: 'T1',
          amount_quota: 0,
          count: 0,
          red_match: 3,
          blue_match: 1,
          pool_share_bps: 6000,
        },
        {
          tier: 2,
          name: 'T2',
          amount_quota: 0,
          count: 0,
          red_match: 3,
          blue_match: 0,
          pool_share_bps: 2000,
        },
        {
          tier: 3,
          name: 'T3',
          amount_quota: 100,
          count: 10,
          red_match: 1,
          blue_match: 0,
        },
      ],
      stake_quota: 50,
      open_at: 1000,
      close_at: 2000,
      draw_at: 3000,
      settle_deadline: 4000,
      allow_multi_win: true,
      fee_bps: 0,
      no_winner_policy: 'refund_all',
      min_entries_to_hold: 0,
      seed: SEED,
      draw_mode: 'ball' as const,
      series_no: 'LS-t',
      issue_no: 1,
      pool_seed_quota: 100_000,
      pool_carry_quota: 0,
      pool_open_quota: 100_000,
      pool_share_bps: 7000,
      ball_red_pool: 12,
      ball_red_pick: 3,
      ball_blue_pool: 4,
      ball_blue_pick: 1,
      pool_quota: 0,
      win_opt_no: 0,
      fee_quota: 0,
      payouts: [],
      locked_at: 2001,
      revealed_at: 3001,
      settled_at: 3002,
    }

    const rules_hash = await qyLotRulesHash(base.rules_text)
    const spec_hash = await qyLotSpecHash(
      qyLotSpecLines({ ...base, spec: base.spec } as unknown as QyLotProof),
      base.algo
    )
    const commit_hash = await qyLotCommitHash(
      { ...base, rules_hash, spec_hash } as unknown as QyLotProof,
      rules_hash,
      spec_hash
    )

    const entries: QyLotProofEntry[] = []
    let chain = commit_hash
    let seq = 1
    for (const [entryNo, pick] of Object.entries(picks)) {
      const row: QyLotProofEntry = {
        seq,
        entry_no: entryNo,
        user_ref: `u${seq}`,
        opt_no: 0,
        amount: 50,
        status: 'success',
        prev_hash: chain,
        chain_hash: '',
        order_no: `BO-${entryNo}`,
        pick,
      }
      chain = await qyLotChainNext(chain, ACT_NO, row, base.algo)
      row.chain_hash = chain
      entries.push(row)
      seq += 1
    }

    // 名单按 entry_no 的字节序排 —— 与后端 roster 行的顺序同一条规则。
    const roster = [...entries].sort((a, b) =>
      a.entry_no.localeCompare(b.entry_no)
    )
    const roster_hash = await qyLotRosterHash(
      ACT_NO,
      commit_hash,
      roster,
      base.algo
    )

    // 用与生产同一条规则算出**本应**的中奖名单，作为"诚实数据"的那一份。
    const proof = {
      ...base,
      rules_hash,
      spec_hash,
      commit_hash,
      chain_head: chain,
      entries,
      total: entries.length,
      roster_hash,
      roster_count: roster.length,
      winners: [],
      ball_result: '',
    } as unknown as QyLotProof

    const finalSeed = await qyLotFinalSeed(proof)
    const reds = await qyLotBallDraw(finalSeed, ACT_NO, 'red', 12, 3)
    const blues = await qyLotBallDraw(finalSeed, ACT_NO, 'blue', 4, 1)
    const pad = (list: number[]) =>
      list.map((ball) => String(ball).padStart(2, '0')).join(',')
    proof.ball_result = `${pad(reds)}|${pad(blues)}`

    const needs = [
      { tier: 1, red: 3, blue: 1 },
      { tier: 2, red: 3, blue: 0 },
      { tier: 3, red: 1, blue: 0 },
    ]
    proof.winners = []
    for (const row of roster) {
      const { reds: my, blues: myBlue } = qyLotParsePick(row.pick ?? '')
      const mr = my.filter((ball) => reds.includes(ball)).length
      const mb = myBlue.filter((ball) => blues.includes(ball)).length
      const hit = needs.find((need) => mr >= need.red && mb >= need.blue)
      if (hit != null) {
        proof.winners.push({
          pos: proof.winners.length,
          tier: hit.tier,
          entry_no: row.entry_no,
          user_ref: row.user_ref,
          amount: 100,
        })
      }
    }
    return proof
  }

  test('诚实数据：result 通过，并注明金额没有复算', async () => {
    const proof = await ballProof()
    // 名单为空时"复算 == 公布"会平凡成立，这一节的三条篡改用例也就失去意义。
    assert.ok(
      proof.winners.length > 0,
      '固定的 seed 下这一期必须真的开出中奖位，否则整节都是空转'
    )
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'ok')
    assert.equal(statusOf(steps, 'roster'), 'ok')
    assert.equal(statusOf(steps, 'result'), 'ok')
    // 「验了什么、没验什么」必须写在结果里。一个不带说明的绿勾会让用户以为
    // 金额也被验过了，而浮动奖的金额本来就不在证据链里。
    const detail = steps.find((step) => step.key === 'result')?.detail ?? ''
    assert.ok(
      detail.includes('amounts-not-checked'),
      `result 的说明必须写明金额未复算，实际是 ${detail}`
    )
  })

  test('改一个开奖号 → result 立刻红（此前是一条 skipped，永远不会红）', async () => {
    const proof = await ballProof()
    const [redPart = '', bluePart = ''] = (proof.ball_result ?? '').split('|')
    const reds = redPart.split(',').map((cell) => Number.parseInt(cell, 10))
    // 换掉一个红球，换成一个当前没被摇中的号 —— 平台改库最省事的那一种改法。
    const swap = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12].find(
      (ball) => !reds.includes(ball)
    )
    assert.ok(swap != null, '12 选 3 的号池里必然还有没被摇中的号')
    reds[0] = swap
    proof.ball_result = `${[...reds]
      .sort((a, b) => a - b)
      .map((ball) => String(ball).padStart(2, '0'))
      .join(',')}|${bluePart}`

    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('开奖号不动、悄悄往中奖名单里塞一个人 → result 也要红', async () => {
    const proof = await ballProof()
    proof.winners.push({
      pos: proof.winners.length,
      tier: 1,
      entry_no: 'B0001',
      user_ref: 'u1',
      amount: 60_000,
    })
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('把某个人的中奖档位改高一档 → result 也要红', async () => {
    const proof = await ballProof()
    assert.ok(proof.winners.length > 0, 'fixture 应当至少开出一个中奖位')
    proof.winners[0].tier = 1
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'fail')
  })
})
