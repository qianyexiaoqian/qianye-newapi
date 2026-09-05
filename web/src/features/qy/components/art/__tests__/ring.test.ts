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

import { QY_RING_CIRCUMFERENCE, qyRingClamp, qyRingDashOffset } from '../ring'

/**
 * 倒计时环的方向:`fraction` 是**剩余**比例,剩得越少弧越短。
 * 画反了在浏览器里只是"看起来怪",这里把方向钉住。
 */
describe('倒计时环的弧长', () => {
  test('满环 offset 为 0,空环 offset 等于整个周长', () => {
    assert.equal(qyRingDashOffset(1), 0)
    assert.equal(qyRingDashOffset(0), QY_RING_CIRCUMFERENCE)
  })

  test('剩得越少,offset 越大(弧越短)', () => {
    assert.ok(qyRingDashOffset(0.25) > qyRingDashOffset(0.75))
    assert.ok(
      Math.abs(qyRingDashOffset(0.5) - QY_RING_CIRCUMFERENCE / 2) < 1e-9
    )
  })

  test('越界与非数按端点处理:>1 满环、<0 与 NaN 空环', () => {
    assert.equal(qyRingClamp(1.7), 1)
    assert.equal(qyRingClamp(-0.2), 0)
    assert.equal(qyRingClamp(Number.NaN), 0)
    assert.equal(
      qyRingDashOffset(Number.POSITIVE_INFINITY),
      QY_RING_CIRCUMFERENCE
    )
  })
})
