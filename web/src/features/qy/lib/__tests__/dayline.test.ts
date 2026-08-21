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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

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

/*
 * 接线：结算说明那一句必须用 dayline 这一套渲染，不能退回本地时区。
 *
 * 纯函数写对了、调用点没接上是本仓的头号形状。这里逐字扫那一段：
 * 两个数出自同一个偏移，且 next 不再走 formatTimestampToDate。
 */
describe('佣金审核那一段结算说明的接线', () => {
  test('日界与下一轮开跑用同一个偏移渲染', () => {
    const src = readFileSync(
      fileURLToPath(
        new URL(
          '../../pages/admin-commission-records/index.tsx',
          import.meta.url
        )
      ),
      'utf-8'
    )
    const at = src.indexOf("t('qy_cm_auto_settle'")
    assert.ok(at >= 0, '自动结算那一段整块不见了')
    // 窗口只取这一次 t() 调用本身,到「手动补救」那一段为止 ——
    // 放宽到定长会把下面表格列里合法的 formatTimestampToDate 一起圈进来。
    const end = src.indexOf('qy_cm_auto_settle_fallback', at)
    assert.ok(end > at, '「手动补救仍然在」那一段不见了')
    // 注释里会提到 formatTimestampToDate(那正是这段改动的说明),
    // 逐行剔掉注释再判,否则守卫会被自己的注释绊倒。
    const block = src
      .slice(at, end)
      .split(String.fromCharCode(10))
      .filter((line) => !line.trim().startsWith('//'))
      .join(String.fromCharCode(10))

    assert.ok(
      block.includes('qyDaylineLabel(settleSnapshot.day_offset_minutes)'),
      '日界标签必须由 qyDaylineLabel 出，不要再手拼一次 UTC±N'
    )
    assert.ok(
      block.includes('qyFormatAtDayline('),
      '「下一轮最早 … 开跑」必须按日界那个偏移渲染'
    )
    assert.ok(
      !block.includes('formatTimestampToDate('),
      '这一句里不许再出现 formatTimestampToDate —— 它按浏览器本地时区渲染，' +
        '会让同一句话里的两个数落在两套时区系里'
    )
  })
})
