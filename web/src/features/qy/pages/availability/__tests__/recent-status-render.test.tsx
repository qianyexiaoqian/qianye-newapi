/*
 * 热力图格子上的「最近请求」状态条与「近期已恢复」—— 真实渲染。
 *
 * 一个整段可用率判为不可用、但最近一小时已经全部成功的格子：数字照旧（累计口径），
 * 格子下面要有状态条，并写出「近期已恢复」。「不提供」的格子不画状态条。
 */
import assert from 'node:assert/strict'
import { after, test } from 'node:test'

import {
  cleanupQyStardustScreens,
  mountQyStardustScreen,
  zh,
} from '../../../__tests__/stardust-screen'
import { QyAvailabilityMatrix } from '../components/availability-matrix'
import { getQyAvailMetric } from '../constants'
import type { QyAvailCell, QyAvailMatrix } from '../types'

after(cleanupQyStardustScreens)

const NOW_HOUR = 1_791_849_600
const NOW = NOW_HOUR + 1200

function cellOf(
  group: string,
  model: string,
  patch: Partial<QyAvailCell>
): QyAvailCell {
  return {
    group,
    model,
    availability: null,
    state: 'no_data',
    req_total: 0,
    counted: 0,
    success: 0,
    excluded_total: 0,
    has_channel: true,
    avg_latency_ms: null,
    avg_ttft_ms: null,
    avg_tps: null,
    latency_samples: 0,
    ttft_samples: 0,
    speed_samples: 0,
    ...patch,
  }
}

const MATRIX = {
  start_ts: NOW - 86_400,
  end_ts: NOW,
  bucket_seconds: 300,
  granularity: '5m',
  definition: {
    numerator: '',
    denominator: [],
    excluded: [],
    count_client_errors: false,
    count_rate_limited: false,
    min_samples: 20,
    perf_min_samples: 5,
    ok_threshold: 99,
    degraded_threshold: 95,
    note: '',
  },
  groups: [
    { group: 'vip', desc: '' },
    { group: 'svip', desc: '' },
  ],
  models: ['gpt-x'],
  covered_models: ['gpt-x'],
  total_models: 1,
  page: 1,
  page_size: 30,
  truncated: false,
  cells: [
    cellOf('vip', 'gpt-x', {
      availability: 62.5,
      state: 'down',
      req_total: 80,
      counted: 80,
      success: 50,
      recent_hours: [
        { ts: NOW_HOUR - 3 * 3600, success_rate: 0 },
        { ts: NOW_HOUR, success_rate: 100 },
      ],
    }),
    cellOf('svip', 'gpt-x', { state: 'not_offered', has_channel: false }),
  ],
  overall: cellOf('', '', {}),
} as QyAvailMatrix

test('已恢复的格子：保留累计可用率，画出状态条并写「近期已恢复」；不提供的格子没有状态条', async () => {
  const screen = await mountQyStardustScreen({
    element: (
      <QyAvailabilityMatrix
        matrix={MATRIX}
        metric={getQyAvailMetric('availability')}
        onSelectCell={() => {}}
      />
    ),
    path: '/qy/availability',
    respond: () => undefined,
  })

  const text = screen.text()
  assert.ok(text.includes('62.5'), `累计可用率要保留：${text}`)
  assert.ok(text.includes(zh.qy_avl_recovered), '没写「近期已恢复」')

  const bars = document.querySelectorAll(
    `[role="img"][aria-label="${zh.qy_avl_recent_bar}"]`
  )
  assert.equal(bars.length, 1, '只有提供该模型的那一格有状态条')
  assert.equal(bars[0].children.length, 24, '24 格，一小时一格')

  const vip = [...document.querySelectorAll('button')].find((node) =>
    (node.getAttribute('title') ?? '').startsWith('gpt-x · vip')
  )
  assert.ok(vip != null)
  assert.ok(
    (vip.getAttribute('title') ?? '').includes(zh.qy_avl_recovered_hint),
    '悬停提示要解释为什么数字低却标了已恢复'
  )
})
