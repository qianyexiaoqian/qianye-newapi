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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import {
  qyLotRollPpm,
  qyLotWheelPick,
  qyLotWheelTicket,
  verifyQyLotProof,
  type QyLotProofSpin,
  type QyLotProofTier,
  type QyLotVerifyStep,
  type QyLotWheelProof,
} from '../verify'

/**
 * 转盘（`draw_mode='wheel'`）的跨实现验证。
 *
 * ## 两份材料都不是本文件的实现跑出来的
 *
 *   · `GOLDEN` 里的票面与编码由 `qianye/docs/lottery-verify.py` 的 `wheel_ticket` /
 *     `wheel_pick` 独立算出（纯标准库），Go 侧 `wheel_golden_test.go` 钉的是同一组；
 *   · `fixtures/wheel-proof.json` 是 Go 的端到端用例
 *     （`wheel_e2e_db_test.go`，`QY_WHEEL_PROOF_OUT`）从真实的证据链端点导出的
 *     一整场转盘：五转覆盖真实档中奖、文本奖、谢谢参与、库存耗尽落空与耗尽封盘。
 *     同一份文件由 Python 脚本验过「全部通过」；这里要求浏览器端得出同一结论。
 *
 * 三份实现在同一组输入上逐位一致，这个文件锁的就是那一致性。
 *
 * ## 篡改一定要红
 *
 * 全绿只证明"诚实数据能过"。下面每一条篡改各改证据链里的**一个**量，断言对应的
 * 步骤变红 —— 少一条红叉，就是平台在那一处可以事后改结果而没人看得见。
 */

const SEED = '00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff'

/** 与 Go / Python 三方逐位一致的黄金向量。 */
const GOLDEN = {
  ticket: 'da6c1c477d8450e535ab2ac86afaf7a6104228a450165ef31acc799beec9289c',
  ppm: 853_212,
  emptySeedTicket:
    'bd8210ffc88f7b286e70cd2cb58bdad77a539874654eca8d356835ed6cae9960',
  emptySeedPpm: 740_265,
} as const

function loadFixture(): QyLotWheelProof {
  const raw = readFileSync(
    new URL('./fixtures/wheel-proof.json', import.meta.url),
    'utf8'
  )
  return JSON.parse(raw) as QyLotWheelProof
}

function statusOf(steps: QyLotVerifyStep[], key: string): string {
  const found = steps.find((item) => item.key === key)
  assert.ok(found, `missing step ${key}`)
  return found.status
}

/** fixture 里的转动记录；缺了就是 fixture 坏了，直接失败而不是静默跳过。 */
function spinsOf(proof: QyLotWheelProof): QyLotProofSpin[] {
  assert.ok(proof.spins, 'fixture has no spins')
  return proof.spins
}

function tiersOf(proof: QyLotWheelProof): QyLotProofTier[] {
  assert.ok(proof.tiers, 'fixture has no tiers')
  return proof.tiers
}

describe('wheel 跨实现黄金向量', () => {
  test('票面与 Go / Python 逐位一致', async () => {
    const ticket = await qyLotWheelTicket(SEED, 'LOTTESTACT01', 7, 'abc-XYZ_09')
    assert.equal(ticket, GOLDEN.ticket)
    assert.equal(qyLotRollPpm(ticket), GOLDEN.ppm)
  })

  test('空 client_seed 按空分量进原像', async () => {
    const ticket = await qyLotWheelTicket(SEED, 'LOTTESTACT01', 1, '')
    assert.equal(ticket, GOLDEN.emptySeedTicket)
    assert.equal(qyLotRollPpm(ticket), GOLDEN.emptySeedPpm)
  })

  test('链原像里的结果编码与 Go / Python 逐字一致', () => {
    assert.equal(
      qyLotWheelPick({
        tier: 2,
        ppm: 384_217,
        exhausted_tier: 0,
        client_seed: 'abc-XYZ_09',
      }),
      'w|2|384217|0|abc-XYZ_09'
    )
    assert.equal(
      qyLotWheelPick({ tier: 0, ppm: 5, exhausted_tier: 3, client_seed: '' }),
      'w|0|5|3|'
    )
  })

  test('种子未揭示时算不出票面，必须报错而不是算一个假的', async () => {
    await assert.rejects(() => qyLotWheelTicket('', 'A', 1, 'x'))
  })
})

describe('wheel 端到端 fixture', () => {
  test('Go 导出的证据链在浏览器端六步全绿', async () => {
    const proof = loadFixture()
    assert.equal(proof.draw_mode, 'wheel')
    assert.equal(proof.spins?.length, 5)
    const steps = await verifyQyLotProof(proof)
    assert.deepEqual(
      steps.map((item) => [item.key, item.status]),
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

  test('fixture 本身覆盖了转盘的四种结局', () => {
    const spins = loadFixture().spins ?? []
    // 真实档中奖、文本奖、谢谢参与、摇中已发完的档落空。
    assert.deepEqual(
      spins.map((spin) => [spin.tier, spin.exhausted_tier]),
      [
        [1, 0],
        [2, 0],
        [0, 0],
        [0, 2],
        [1, 0],
      ]
    )
  })

  /**
   * 转盘的排期**不进承诺原像**：open_at / close_at / draw_at 改成什么，承诺照样
   * 对得上。这是刻意的 —— 票面里没有时刻，排期只是"什么时候收转"，运营可以在
   * 发布后延期、提前收转、立即开始（`PUT …/schedule`）。这条断言把"改了排期
   * 不会让一场诚实的转盘变红"钉死；批次玩法（`verify-v2.test.ts`）仍然会红。
   */
  test('改 open_at / close_at / draw_at → 承诺仍然通过，六步全绿', async () => {
    const proof = loadFixture()
    proof.open_at -= 3600
    proof.close_at += 86_400
    proof.draw_at += 86_400
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'commit'), 'ok')
    assert.deepEqual(
      steps.map((item) => item.status),
      ['ok', 'ok', 'ok', 'ok', 'ok', 'ok']
    )
  })

  /**
   * 把"摇中已发完落空"的那一转改成"中了"：库存重放与链环同时露馅。
   * 这正是转盘最值得防的一手 —— 事后给某个人补一档奖。
   */
  test('篡改一转的结果 → 链与复算双双变红', async () => {
    const proof = loadFixture()
    const spin = spinsOf(proof)[3]
    spin.tier = 2
    spin.exhausted_tier = 0
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'fail')
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('删掉一转的记录 → 链推不动', async () => {
    const proof = loadFixture()
    proof.spins = spinsOf(proof).filter((spin) => spin.seq !== 3)
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'fail')
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('改一转的 client_seed → 票面变了，链与复算都对不上', async () => {
    const proof = loadFixture()
    spinsOf(proof)[0].client_seed = 'someone-else'
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'fail')
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('公布的库存终态与重放对不上 → 复算变红', async () => {
    const proof = loadFixture()
    tiersOf(proof)[0].stock_left = 1
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'chain'), 'ok')
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('少一笔派奖 → 复算变红（中了却没发）', async () => {
    const proof = loadFixture()
    proof.payouts = proof.payouts.slice(1)
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  /**
   * 拿掉服务端派生的「谢谢参与」行：承诺先红（它进 spec 原像），复算也红
   * （摇号轴没有铺满，转盘的协议里没有"落在全部区间之外"这个结果）。
   */
  test('拿掉 none 行 → 承诺与复算都变红', async () => {
    const proof = loadFixture()
    proof.spec = proof.spec.filter((item) => item.prize_type !== 'none')
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'spec'), 'fail')
    assert.equal(statusOf(steps, 'commit'), 'fail')
    assert.equal(statusOf(steps, 'result'), 'fail')
  })

  test('种子未揭示 → 承诺与复算如实跳过，绝不显示成通过', async () => {
    const proof = loadFixture()
    proof.seed = ''
    const steps = await verifyQyLotProof(proof)
    assert.equal(statusOf(steps, 'commit'), 'skipped')
    assert.equal(statusOf(steps, 'chain'), 'ok')
    assert.equal(statusOf(steps, 'roster'), 'ok')
    assert.equal(statusOf(steps, 'result'), 'skipped')
  })
})
