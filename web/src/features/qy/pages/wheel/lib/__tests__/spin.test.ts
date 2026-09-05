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

import type { QyLotSpecItem } from '../../../lottery/types'
import {
  isQyWheelClientSeedValid,
  isQyWheelSoldOut,
  qyWheelOutcomeOf,
  qyWheelRandomClientSeed,
  qyWheelRealTiers,
  qyWheelRotationFor,
  qyWheelSectorColor,
  qyWheelSectors,
} from '../spin'

/**
 * 转盘的纯逻辑：扇区几何要与后端 / 验证器的落档逐字同一条，指针停的那一格
 * 才是文字说的那一档；种子的合规判据要与后端 `validClientSeed` 同一条，
 * 否则一份"前端放行、后端 400"的种子会让用户在按下确认之后才被顶回来。
 */

const SPEC: QyLotSpecItem[] = [
  {
    tier: 1,
    name: '头奖',
    amount_quota: 500,
    count: 2,
    prize_type: 'quota',
    win_ppm: 300_000,
    stock_left: 1,
  },
  {
    tier: 2,
    name: '兑换码',
    count: 1,
    prize_type: 'text',
    win_ppm: 200_000,
    text_desc: '联系客服领取',
    stock_left: 0,
  },
  { tier: 3, name: '谢谢参与', prize_type: 'none', win_ppm: 500_000 },
]

describe('客户端种子', () => {
  test('默认随机 16 位，且落在后端认的字符集里', () => {
    const seed = qyWheelRandomClientSeed()
    assert.equal(seed.length, 16)
    assert.ok(isQyWheelClientSeedValid(seed))
    assert.match(seed, /^[0-9A-Za-z_-]{16}$/)
  })

  test('合规判据与后端 validClientSeed 同一条：≤64、仅 [0-9a-zA-Z_-]，空串合法', () => {
    assert.ok(isQyWheelClientSeedValid(''))
    assert.ok(isQyWheelClientSeedValid('abc-XYZ_09'))
    assert.ok(isQyWheelClientSeedValid('a'.repeat(64)))
    assert.ok(!isQyWheelClientSeedValid('a'.repeat(65)))
    // `|` 是链原像里 WheelPick 编码的分隔符，绝不能进种子。
    assert.ok(!isQyWheelClientSeedValid('a|b'))
    assert.ok(!isQyWheelClientSeedValid('有 空格'))
    assert.ok(!isQyWheelClientSeedValid(' abc'))
  })
})

describe('扇区几何', () => {
  test('按 tier 升序铺满 360 度，none 档也是一个扇区', () => {
    const sectors = qyWheelSectors(SPEC)
    assert.deepEqual(
      sectors.map((sector) => [sector.tier, sector.startDeg, sector.endDeg]),
      [
        [1, 0, 108],
        [2, 108, 180],
        [3, 180, 360],
      ]
    )
    assert.equal(sectors[2].isNone, true)
    assert.equal(sectors[2].colorIndex, -1)
    // 真实档按出现顺序占调色板下标，none 不占。
    assert.deepEqual(
      sectors.slice(0, 2).map((sector) => sector.colorIndex),
      [0, 1]
    )
  })

  test('概率之和越过 100% 时不画盘面（那是一张错的奖档表）', () => {
    const broken = SPEC.map((item) =>
      item.tier === 3 ? { ...item, win_ppm: 600_000 } : item
    )
    assert.deepEqual(qyWheelSectors(broken), [])
  })

  test('none 档恒为中性色，真实档在六色调色板里循环', () => {
    assert.equal(qyWheelSectorColor(-1), 'var(--muted)')
    assert.equal(qyWheelSectorColor(0), qyWheelSectorColor(6))
    assert.notEqual(qyWheelSectorColor(0), qyWheelSectorColor(1))
  })

  test('旋转角让摇号量那一点停在指针（12 点钟）下', () => {
    // 盘面上 ppm 那一点在 θ = ppm/1e6 × 360，顺时针转 R 度后落在 θ + R；
    // 要它回到 0（mod 360），R 必须 ≡ −θ。
    for (const ppm of [0, 4_097, 374_575, 999_505]) {
      const theta = (ppm / 1_000_000) * 360
      const rotation = qyWheelRotationFor(ppm)
      const landing = (((theta + rotation) % 360) + 360) % 360
      assert.ok(
        landing < 1e-9 || Math.abs(landing - 360) < 1e-9,
        `ppm ${ppm}: θ=${theta} R=${rotation} 落在 ${landing}`
      )
      assert.ok(rotation >= 720, '至少转过两整圈，动效才看得出"转过了"')
    }
  })
})

describe('结局分类', () => {
  test('中了 / 摇中但已发完 / 谢谢参与 是三个不同的结论', () => {
    assert.equal(qyWheelOutcomeOf({ result_tier: 1, exhausted_tier: 0 }), 'won')
    assert.equal(
      qyWheelOutcomeOf({ result_tier: 0, exhausted_tier: 2 }),
      'exhausted'
    )
    assert.equal(
      qyWheelOutcomeOf({ result_tier: 0, exhausted_tier: 0 }),
      'none'
    )
  })

  test('真实档去掉派生的 none 行；全部发完 = 已售罄', () => {
    assert.deepEqual(
      qyWheelRealTiers(SPEC).map((tier) => tier.tier),
      [1, 2]
    )
    assert.equal(isQyWheelSoldOut(SPEC), false)
    assert.equal(
      isQyWheelSoldOut(
        SPEC.map((item) =>
          item.prize_type === 'none' ? item : { ...item, stock_left: 0 }
        )
      ),
      true
    )
    // 老后端不下发 stock_left：不能把"没有这一列"读成"发完了"。
    assert.equal(isQyWheelSoldOut([]), false)
  })
})
