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
/*
 * 结算日界的时间显示：日界标签与「下一轮开跑」的时刻必须落在同一个时区系里。
 *
 * 原先那句话里两个数出自两套系统：日界由 day_offset_minutes 直接拼成 UTC±N，
 * 时刻走 formatTimestampToDate（浏览器本地时区）。在 UTC-7 的机器上渲染成
 * 「结算日界 UTC+0 … 下一轮最早 2026-08-21 17:00:00 开跑」——日期比日界日期
 * 还早一天，读者只能自己换算才知道 17:00 就是 UTC 零点。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import { qyDaylineLabel, qyFormatAtDayline } from '../dayline'

describe('结算日界的标签', () => {
  test('整点偏移不带小数尾巴', () => {
    assert.equal(qyDaylineLabel(0), 'UTC+0')
    assert.equal(qyDaylineLabel(480), 'UTC+8')
    assert.equal(qyDaylineLabel(-420), 'UTC-7')
  })

  test('半点时区照实写出来', () => {
    assert.equal(qyDaylineLabel(330), 'UTC+5.5')
    assert.equal(qyDaylineLabel(-210), 'UTC-3.5')
  })
})

describe('下一轮开跑的时刻', () => {
  // 1787356800 = 2026-08-22T00:00:00Z，也就是 day_offset_minutes=0 时的下一个日界。
  const nextRun = 1787356800

  test('日界在 UTC 零点时，时刻就该显示成那一天的 00:00:00', () => {
    assert.equal(qyFormatAtDayline(nextRun, 0), '2026-08-22 00:00:00 (UTC+0)')
  })

  test('日界在 UTC+8 时，同一个瞬间按 UTC+8 的墙钟写', () => {
    assert.equal(
      qyFormatAtDayline(1787328000, 480),
      '2026-08-22 00:00:00 (UTC+8)'
    )
  })

  test('负偏移同样成立', () => {
    assert.equal(
      qyFormatAtDayline(1787382000, -420),
      '2026-08-22 00:00:00 (UTC-7)'
    )
  })

  test('时区后缀必须带上 —— 不带的话读者分不出这是本地时还是日界时区', () => {
    const rendered = qyFormatAtDayline(nextRun, 0)
    assert.ok(
      rendered.includes('UTC'),
      '同一句话里「日界 UTC+0」与这个时刻并排出现，时刻不标时区就是两套口径'
    )
  })

  test('拿不到时刻时给空串，而不是 1970 那一天', () => {
    assert.equal(qyFormatAtDayline(0, 0), '')
    assert.equal(qyFormatAtDayline(-1, 0), '')
    assert.equal(qyFormatAtDayline(Number.NaN, 0), '')
  })

  test('渲染结果与运行机器的本地时区无关', () => {
    // 同一个瞬间、同一个偏移，无论进程 TZ 是什么都必须得到同一串。
    const before = process.env.TZ
    try {
      process.env.TZ = 'America/Los_Angeles'
      const a = qyFormatAtDayline(nextRun, 0)
      process.env.TZ = 'Asia/Shanghai'
      const b = qyFormatAtDayline(nextRun, 0)
      assert.equal(a, b)
      assert.equal(a, '2026-08-22 00:00:00 (UTC+0)')
    } finally {
      process.env.TZ = before
    }
  })
})

// 「佣金审核那一段结算说明」的接线走查已随佣金审核页整体删除（D-14）：
// 星屑日结的调度状态由 admin-stardust 那一页渲染，它自己的测试守着 dayline 的接线。
