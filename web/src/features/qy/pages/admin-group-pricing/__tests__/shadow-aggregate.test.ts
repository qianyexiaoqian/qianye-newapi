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

import {
  qyAggregateShadow,
  qyShadowRange,
  type QyGpRangePreset,
} from '../lib/shadow-aggregate'
import type { QyGpShadowSegment } from '../types'

/**
 * 对账页要回答的是「切换后这个月会多收还是少收多少」。这里保护的是两条会让
 * 那个答案变成假精确的行为：
 *
 *   1. **不可折算的段只贡献请求数，不贡献金额**。后端已经把这类段的
 *      `delta_quota` 置 0 并单独计数（旧值为 0、或计价口径切换时按比例折算
 *      不成立），前端一旦顺手把它们的金额也加进去，合计就会看起来完整而实际
 *      漏了一块 —— 这正是本扩展历次审计里反复出现的那类缺陷。
 *   2. **区间末端对齐到整分钟**。不对齐会让 queryKey 每次渲染都变，
 *      react-query 把它当成新查询无限重取，把对账页变成一个打日志库的循环。
 */

function segment(
  overrides: Partial<QyGpShadowSegment> & {
    group_name: string
    model_name: string
  }
): QyGpShadowSegment {
  return {
    mode: 'ratio',
    old_value: '1',
    new_value: '1.2',
    exact: true,
    requests: 0,
    actual_quota: 0,
    request_share: '1.000000',
    share_is_exact: true,
    attributed_quota: 0,
    factor: '1.200000',
    delta_quota: 0,
    ...overrides,
  }
}

describe('qyAggregateShadow', () => {
  test('sums requests and money across segments of the same model', () => {
    const buckets = qyAggregateShadow(
      [
        segment({
          group_name: 'default',
          model_name: 'gpt-4o',
          requests: 100,
          attributed_quota: 1000,
          delta_quota: 200,
        }),
        segment({
          group_name: 'svip',
          model_name: 'gpt-4o',
          requests: 50,
          attributed_quota: 400,
          delta_quota: 80,
        }),
      ],
      'model'
    )

    assert.equal(buckets.length, 1)
    assert.equal(buckets[0].label, 'gpt-4o')
    assert.equal(buckets[0].requests, 150)
    assert.equal(buckets[0].attributed_quota, 1400)
    assert.equal(buckets[0].delta_quota, 280)
    assert.equal(buckets[0].inexact_requests, 0)
  })

  test('keeps groups apart when aggregating by group', () => {
    const buckets = qyAggregateShadow(
      [
        segment({
          group_name: 'default',
          model_name: 'gpt-4o',
          requests: 100,
          delta_quota: 200,
        }),
        segment({
          group_name: 'svip',
          model_name: 'gpt-4o',
          requests: 50,
          delta_quota: 80,
        }),
      ],
      'group'
    )

    assert.deepEqual(
      buckets.map((bucket) => [bucket.label, bucket.delta_quota]),
      [
        ['default', 200],
        ['svip', 80],
      ]
    )
  })

  test('counts non-reconcilable segments as requests only, never as money', () => {
    const buckets = qyAggregateShadow(
      [
        segment({
          group_name: 'default',
          model_name: 'gpt-4o',
          requests: 40,
          attributed_quota: 800,
          delta_quota: 160,
        }),
        segment({
          group_name: 'default',
          model_name: 'gpt-4o',
          exact: false,
          inexact_reason: '旧值为 0',
          requests: 60,
          // 后端对不可折算的段仍会带上这两个字段的零值；即便它们非零，
          // 前端也绝不能把它们并进合计。
          attributed_quota: 9999,
          delta_quota: 9999,
        }),
      ],
      'model'
    )

    assert.equal(buckets.length, 1)
    assert.equal(buckets[0].requests, 100)
    assert.equal(buckets[0].attributed_quota, 800)
    assert.equal(buckets[0].delta_quota, 160)
    assert.equal(buckets[0].inexact_requests, 60)
  })

  test('orders by absolute difference so large under-charges are not buried', () => {
    const buckets = qyAggregateShadow(
      [
        segment({
          group_name: 'default',
          model_name: 'small-gain',
          requests: 10,
          delta_quota: 100,
        }),
        segment({
          group_name: 'default',
          model_name: 'big-loss',
          requests: 10,
          delta_quota: -5000,
        }),
      ],
      'model'
    )

    assert.deepEqual(
      buckets.map((bucket) => bucket.label),
      ['big-loss', 'small-gain']
    )
  })

  test('returns an empty list when there are no segments', () => {
    assert.deepEqual(qyAggregateShadow(undefined, 'model'), [])
    assert.deepEqual(qyAggregateShadow([], 'group'), [])
  })
})

describe('qyShadowRange', () => {
  const presets: [QyGpRangePreset, number][] = [
    ['24h', 24 * 3600],
    ['7d', 7 * 24 * 3600],
    ['30d', 30 * 24 * 3600],
  ]

  for (const [preset, seconds] of presets) {
    test(`spans exactly ${preset} and ends on a whole minute`, () => {
      const range = qyShadowRange(preset)
      assert.equal(range.end - range.start, seconds)
      // 未对齐的末端会让 queryKey 每次渲染都变，react-query 无限重取。
      assert.equal(range.end % 60, 0)
    })
  }

  test('stays inside the 31-day window the backend accepts', () => {
    const range = qyShadowRange('30d')
    assert.ok(range.end - range.start < 31 * 86400)
  })
})
