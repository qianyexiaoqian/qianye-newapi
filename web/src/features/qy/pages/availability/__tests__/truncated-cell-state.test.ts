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
import { test } from 'node:test'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { getQyAvailStateStyle, qyAvailCellState } from '../constants'
import type { QyAvailCell } from '../types'

/**
 * 后端 `max_series_per_query` 默认 200，本页一页 30 个模型，于是**只要有 ≥7 个
 * 可见分组，每一页都必然被截断**。截断前前端对没查到的格子一律渲染成「未提供」
 * ——「我们没查」被写成了「该分组不提供这个模型」，一个肯定断言。
 *
 * 这组用例守的就是那条降级：不在 `covered_models` 里的模型，整行必须是「未知」。
 *
 * 刻意不套 `describe`：本仓库的 `bun test` 跑在 bun 的 node:test 兼容层上，
 * 一个文件在别的文件的用例执行期间被加载时 `describe()` 会直接抛
 * NotImplementedError（基线里已有 77 个文件因此报错）。平铺的 `test()` 不受影响。
 */

function cellOf(state: QyAvailCell['state']): QyAvailCell {
  return {
    group: 'vip',
    model: 'gpt-5',
    availability: null,
    state,
    req_total: 0,
    counted: 0,
    success: 0,
    excluded_total: 0,
    has_channel: false,
    avg_latency_ms: null,
    avg_ttft_ms: null,
    avg_tps: null,
    latency_samples: 0,
    ttft_samples: 0,
    speed_samples: 0,
  }
}

test('未被覆盖的模型渲染成未知，而不是未提供', () => {
  assert.equal(qyAvailCellState(undefined, false), 'unknown')
  // 就算响应里恰好还留着这一格，模型没被覆盖就不该拿它下结论。
  assert.equal(qyAvailCellState(cellOf('not_offered'), false), 'unknown')
  assert.equal(qyAvailCellState(cellOf('ok'), false), 'unknown')
})

test('已覆盖但格子缺失时仍然是未知', () => {
  // 后端保证被覆盖的模型整行都在；真的缺了说明这一格没查到，不是「不提供」。
  assert.equal(qyAvailCellState(undefined, true), 'unknown')
})

test('已覆盖的格子原样沿用后端给出的状态', () => {
  for (const state of [
    'ok',
    'degraded',
    'down',
    'low_sample',
    'no_data',
    'not_offered',
  ] as const) {
    assert.equal(qyAvailCellState(cellOf(state), true), state)
  }
})

test('未知与未提供必须是两段不同的文案', () => {
  const unknown = getQyAvailStateStyle(qyAvailCellState(undefined, false))
  const notOffered = getQyAvailStateStyle('not_offered')
  assert.equal(unknown.labelKey, 'qy_avl_state_unknown')
  assert.notEqual(
    unknown.labelKey,
    notOffered.labelKey,
    '两者文案相同的话，降级在界面上根本看不出来'
  )
})

test('截断提示与未覆盖提示在两种语言里都在', () => {
  const enMap = en as Record<string, string>
  const zhMap = zh as Record<string, string>
  // 降级本身没有意义，除非页面同时说明「为什么是未知」。
  for (const key of ['qy_avl_not_measured', 'qy_avl_truncated']) {
    assert.ok(enMap[key], `en.json 缺少 ${key}`)
    assert.ok(zhMap[key], `zh.json 缺少 ${key}`)
  }
})
