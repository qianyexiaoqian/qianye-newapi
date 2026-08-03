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

import { qyArray } from '@/features/qy/lib/array'

/**
 * 提现审核页整页白屏那一次的原样：后端 `handleAdminStats` 在库里没有
 * pending/approved/paying 单据时把 nil 切片序列化成了 `null`，
 * 前端 `stats.buckets.find(...)` 抛
 * `Cannot read properties of null (reading 'find')`。
 *
 * 断言刻意从「调 .find 会不会抛」这一侧写，而不是只比 `qyArray(null)` 等于
 * `[]`：真正让页面白屏的是那次方法调用，而不是那个值长什么样。
 */
describe('qyArray', () => {
  test('null / undefined 上调数组方法不再抛异常', () => {
    const fromNull = null as unknown as { status: string; count: number }[]
    assert.doesNotThrow(() =>
      qyArray(fromNull).find((bucket) => bucket.status === 'pending')
    )
    assert.equal(
      qyArray(fromNull).find((bucket) => bucket.status === 'pending'),
      undefined
    )
    assert.deepEqual(qyArray(undefined), [])
  })

  test('契约违约的其他形状也被挡住', () => {
    // `?? []` 只挡 null/undefined，这三个值会原样穿过去、.map 照样炸。
    for (const broken of [{}, 'items', 42]) {
      const value = broken as unknown as number[]
      assert.doesNotThrow(() => qyArray(value).map((n) => n + 1))
      assert.deepEqual(qyArray(value), [])
    }
  })

  test('真数组原样返回（同一个引用，不破坏 useMemo 依赖）', () => {
    const buckets = [{ status: 'pending', count: 3 }]
    assert.equal(qyArray(buckets), buckets)
    assert.equal(
      qyArray(buckets).find((bucket) => bucket.status === 'pending')?.count,
      3
    )
  })

  test('空数组不会被当成缺失值替换掉', () => {
    const empty: number[] = []
    assert.equal(qyArray(empty), empty)
  })
})
