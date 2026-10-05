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
  qyModelGroupRows,
  qyPlazaCardToPerfBadge,
  qyPricingPerfSource,
} from '../constants'
import type { QyAvailCell, QyAvailState, QyPlazaCard } from '../types'

/**
 * 模型广场详情页「性能」标签的口径。
 *
 * 项目方要求:各分组的模型状态必须与「模型可用率」页同一口径,未登录按注册默认分组。
 * 两件事钉在这里:什么时候用扩展口径、单个模型那张表留哪些分组。
 */

const base = {
  configLoading: false,
  extensionEnabled: true,
  availabilityOn: true,
}

describe('qyPricingPerfSource', () => {
  test('uses the extension availability whenever the feature is on', () => {
    // 项目方要求:登录与否都用扩展口径,所以判据里根本没有登录态 ——
    // 未登录访客的分组由后端按注册默认用户分组裁剪。
    assert.equal(qyPricingPerfSource(base), 'qy')
  })

  test('waits instead of flashing upstream numbers while the config loads', () => {
    assert.equal(
      qyPricingPerfSource({ ...base, configLoading: true }),
      'pending'
    )
  })

  test('falls back to upstream only when the availability feature is off', () => {
    assert.equal(
      qyPricingPerfSource({ ...base, availabilityOn: false }),
      'upstream'
    )
    assert.equal(
      qyPricingPerfSource({ ...base, extensionEnabled: false }),
      'upstream'
    )
  })
})

function cell(model: string, group: string, state: QyAvailState): QyAvailCell {
  return {
    model,
    group,
    state,
    availability: state === 'ok' ? 99.5 : null,
    req_total: 0,
    counted: 0,
    success: 0,
    excluded_total: 0,
    has_channel: state !== 'not_offered',
    avg_latency_ms: null,
    avg_ttft_ms: null,
    avg_tps: null,
    latency_samples: 0,
    ttft_samples: 0,
    speed_samples: 0,
  }
}

describe('qyModelGroupRows', () => {
  test('keeps only the requested model and drops groups that do not offer it', () => {
    const rows = qyModelGroupRows(
      [
        cell('gpt-x', 'pool-a', 'ok'),
        cell('gpt-x', 'pool-b', 'not_offered'),
        cell('other', 'pool-a', 'ok'),
      ],
      'gpt-x'
    )
    assert.deepEqual(
      rows.map((row) => row.group),
      ['pool-a']
    )
  })

  test('keeps groups with no recent data instead of hiding them', () => {
    // 无数据 / 样本不足是「能用、最近没人用」,删掉会被读成该分组没有这个模型。
    const rows = qyModelGroupRows(
      [cell('gpt-x', 'quiet', 'no_data'), cell('gpt-x', 'thin', 'low_sample')],
      'gpt-x'
    )
    assert.deepEqual(
      rows.map((row) => row.state),
      ['no_data', 'low_sample']
    )
  })

  test('returns an empty list when the model has no cells at all', () => {
    assert.deepEqual(qyModelGroupRows([], 'gpt-x'), [])
  })
})

function plazaCard(overrides: Partial<QyPlazaCard>): QyPlazaCard {
  return {
    model: 'gpt-x',
    availability: 99.72,
    state: 'ok',
    hours: [],
    avg_latency_ms: 21000,
    avg_ttft_ms: null,
    avg_tps: 23.6,
    latency_samples: 30,
    ttft_samples: 0,
    speed_samples: 30,
    ...overrides,
  }
}

describe('qyPlazaCardToPerfBadge', () => {
  test('passes the selected group availability straight to the status bar', () => {
    const badge = qyPlazaCardToPerfBadge(plazaCard({ availability: 99.72 }))
    assert.equal(badge.success_rate, 99.72)
    assert.equal(badge.avg_latency_ms, 21000)
    assert.equal(badge.avg_tps, 23.6)
  })

  test('shows no percentage instead of 0% when samples are insufficient', () => {
    // 0% 会被读成「这个分组全挂了」;NaN 让状态条显示「—%」。
    const badge = qyPlazaCardToPerfBadge(
      plazaCard({ availability: null, state: 'low_sample', avg_tps: null })
    )
    assert.ok(Number.isNaN(badge.success_rate))
    assert.equal(badge.avg_tps, 0)
  })

  test('drops hours without counted samples so they render as gray slots', () => {
    const badge = qyPlazaCardToPerfBadge(
      plazaCard({
        hours: [
          { ts: 3600, success_rate: 100 },
          { ts: 7200, success_rate: null },
          { ts: 10800, success_rate: 96.5 },
        ],
      })
    )
    assert.deepEqual(badge.recent_success_series, [
      { ts: 3600, success_rate: 100 },
      { ts: 10800, success_rate: 96.5 },
    ])
  })
})
