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
  formatInServerZone,
  formatServerZoneLabel,
  formatUtcOffsetLabel,
} from '../today-usage'

/**
 * 「今日消耗」悬浮里那两行字。
 *
 * 项目方口径：「以服务器的时间为准，即 0 点到 23 点 59 分 59 秒是今日的消耗。」
 * 于是这两行字自己有一个会静默出错的形状：**用浏览器时区渲染服务器的区间**。
 * 那样渲染出来的仍然是一个格式完全正确的时间，只是它说的不是服务器那边的
 * 0 点 —— 运营在上海看一台跑在 PST 的机器，会读到「今日 15:00 → 次日 14:59」，
 * 然后拿这个区间去解释为什么某一笔没算进来。
 *
 * 所以下面的用例全部固定偏移、不依赖运行测试那台机器的时区。
 */

/** 2026-08-22 00:00:00 -07:00(洛杉矶当天 0 点),以及它那一天的右开端点。 */
const LA_DAY_START = 1787382000
const LA_DAY_END = 1787468400

describe('按服务器本地时区渲染区间', () => {
  test('同一个时间戳,偏移不同读出来的钟点就不同', () => {
    // 这一条就是"别用浏览器时区"的判据:两个断言的输入完全一样,
    // 只有偏移不同,而结果差 7 小时。
    assert.equal(
      formatInServerZone(LA_DAY_START, -420),
      '2026-08-22 00:00:00',
      '服务器在 UTC-07:00 时,这一刻就是它的今日 0 点'
    )
    assert.equal(
      formatInServerZone(LA_DAY_START, 0),
      '2026-08-22 07:00:00',
      '同一刻在 UTC 机器上是早上 7 点 —— 偏移必须真的参与渲染'
    )
    assert.equal(formatInServerZone(LA_DAY_START, 480), '2026-08-22 15:00:00')
  })

  test('右端点减一秒才是当天最后一秒', () => {
    // dayEnd 是半开区间的右端(次日 0 点)。直接渲染它会在界面上写出
    // 「→ 次日 00:00:00」,看起来像是把明天也算了进来。
    assert.equal(formatInServerZone(LA_DAY_END, -420), '2026-08-23 00:00:00')
    assert.equal(
      formatInServerZone(LA_DAY_END - 1, -420),
      '2026-08-22 23:59:59'
    )
  })

  test('半小时时区不会被抹平', () => {
    // 1787337000 = 2026-08-22 00:00:00 +05:30(Asia/Kolkata 当天 0 点)。
    assert.equal(formatInServerZone(1787337000, 330), '2026-08-22 00:00:00')
  })

  test('拿不到数(NaN)时给空串,不给 Invalid Date', () => {
    assert.equal(formatInServerZone(Number.NaN, 0), '')
  })
})

describe('时区标签', () => {
  test('偏移渲染成 UTC±HH:MM', () => {
    const cases: [number, string][] = [
      [0, 'UTC+00:00'],
      [480, 'UTC+08:00'],
      [-420, 'UTC-07:00'],
      [330, 'UTC+05:30'],
      [-720, 'UTC-12:00'],
      [840, 'UTC+14:00'],
    ]
    for (const [minutes, want] of cases) {
      assert.equal(formatUtcOffsetLabel(minutes), want, `${minutes} 分钟`)
    }
  })

  test('缩写与偏移一起显示 —— 缩写单看有歧义', () => {
    // CST 既是中国标准时间也是美国中部标准时间:只写缩写等于没写。
    assert.equal(formatServerZoneLabel('CST', 480), 'CST (UTC+08:00)')
    assert.equal(formatServerZoneLabel('PDT', -420), 'PDT (UTC-07:00)')
    assert.equal(formatServerZoneLabel('UTC', 0), 'UTC (UTC+00:00)')
  })

  test('没有字母缩写的时区只留偏移', () => {
    // tzdata 里这类时区的"缩写"本来就是数字(-03 / +0530),
    // 再拼一遍偏移就成了 "-03 (UTC-03:00)"。
    assert.equal(formatServerZoneLabel('-03', -180), 'UTC-03:00')
    assert.equal(formatServerZoneLabel('+0530', 330), 'UTC+05:30')
    assert.equal(formatServerZoneLabel('', -420), 'UTC-07:00')
  })
})
