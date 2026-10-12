/*
 * 「最近请求」状态条与「近期已恢复」的判定。
 *
 * 可用率是整段时间的累计：一次故障之后要很久才爬回阈值，格子一直是红的 ——
 * 项目方原话「恢复了，可用率上不去，被当做不可用」。状态条把最近 24 小时逐小时
 * 画出来，「近期已恢复」把"已经好了"直接说出来。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { qyAvailRecentSlots, qyAvailRecovered } from '../constants'
import type { QyAvailCell, QyAvailDefinition } from '../types'

const HOUR = 3600
// 某个整点之后 20 分钟：当前这一小时（最后一格）是 NOW_HOUR。
const NOW_HOUR = 1_791_849_600
const NOW = NOW_HOUR + 1200

const DEFINITION = {
  ok_threshold: 99,
  degraded_threshold: 95,
} as QyAvailDefinition

function cell(state: string, hours: [number, number][]): QyAvailCell {
  return {
    state,
    recent_hours: hours.map(([ts, rate]) => ({ ts, success_rate: rate })),
  } as QyAvailCell
}

describe('状态条的格子', () => {
  test('按距现在几小时定位：24 格，最后一格是当前小时，没有请求的小时是 null', () => {
    const slots = qyAvailRecentSlots(
      [
        { ts: NOW_HOUR - 23 * HOUR, success_rate: 50 },
        { ts: NOW_HOUR - 2 * HOUR, success_rate: 80 },
        { ts: NOW_HOUR, success_rate: 100 },
      ],
      NOW
    )
    assert.equal(slots.length, 24)
    assert.equal(slots[0], 50)
    assert.equal(slots[21], 80)
    assert.equal(slots[22], null, '中间的空档不能被压没')
    assert.equal(slots[23], 100)
  })

  test('超出 24 小时的点与没有成功率的点都不进格子', () => {
    const slots = qyAvailRecentSlots(
      [
        { ts: NOW_HOUR - 24 * HOUR, success_rate: 10 },
        { ts: NOW_HOUR, success_rate: null },
      ],
      NOW
    )
    assert.ok(slots.every((rate) => rate == null))
  })
})

describe('近期已恢复', () => {
  test('整段判为不可用、当前小时已回到可用阈值以上：已恢复', () => {
    assert.equal(
      qyAvailRecovered(
        cell('down', [
          [NOW_HOUR - 3 * HOUR, 0],
          [NOW_HOUR, 100],
        ]),
        DEFINITION,
        NOW
      ),
      true
    )
  })

  test('当前小时还没有请求时看上一小时', () => {
    assert.equal(
      qyAvailRecovered(
        cell('degraded', [[NOW_HOUR - HOUR, 99.5]]),
        DEFINITION,
        NOW
      ),
      true
    )
  })

  test('最近一小时仍低于阈值、或最近两小时没有请求：不下结论', () => {
    assert.equal(
      qyAvailRecovered(cell('down', [[NOW_HOUR, 90]]), DEFINITION, NOW),
      false
    )
    assert.equal(
      qyAvailRecovered(
        cell('down', [[NOW_HOUR - 2 * HOUR, 100]]),
        DEFINITION,
        NOW
      ),
      false,
      '两小时前的绿格不足以说「现在好了」'
    )
  })

  test('本来就可用的格子不标「已恢复」', () => {
    assert.equal(
      qyAvailRecovered(cell('ok', [[NOW_HOUR, 100]]), DEFINITION, NOW),
      false
    )
  })
})
