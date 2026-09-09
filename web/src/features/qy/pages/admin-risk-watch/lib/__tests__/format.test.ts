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
  qyRwPercentText,
  qyRwPercentToBps,
  qyRwProgressText,
  qyRwRemainingSeconds,
  qyRwRetentionKind,
} from '../format'
import type { QyRwTask } from '../../types'

/** 只填这几条用例真正要用的格,其余走类型断言 —— 完整的 Task 有 20 多格。 */
function task(patch: Partial<QyRwTask>): QyRwTask {
  return { captured: 0, max_records: 0, ends_at: 0, ...patch } as QyRwTask
}

describe('记录概率的两次换算', () => {
  // 后端一律 bps 整数,界面一律百分比。换算写两遍的后果是其中一处忘了除,
  // 而 0.01% 与 1% 在这一页上相差一百倍的存储量。
  test('bps 转百分比去掉无意义的尾零,但保留小数位', () => {
    assert.equal(qyRwPercentText(10000), '100%')
    assert.equal(qyRwPercentText(100), '1%')
    assert.equal(qyRwPercentText(1), '0.01%')
    assert.equal(qyRwPercentText(0), '0%')
  })

  test('百分比转 bps 向下取整,绝不把概率抬上去', () => {
    assert.equal(qyRwPercentToBps('10'), 1000)
    assert.equal(qyRwPercentToBps('0.01'), 1)
    // 0.004% 若四舍五入成 0.01% 就是在管理员没同意的情况下把抽样量翻倍。
    assert.equal(qyRwPercentToBps('0.004'), 0)
    assert.equal(qyRwPercentToBps('999'), 10000, '上界夹在 100%')
    assert.equal(qyRwPercentToBps('abc'), 0, '非法输入落到 0,由后端拒绝')
  })
})

describe('时间窗还剩多久', () => {
  // null 与 0 必须分开:前者是"永远跑",后者是"跑完了",而它们在界面上是
  // 两句完全不同的话。折成同一个值会让永久监听显示成"已结束"。
  test('没有终点时返回 null', () => {
    assert.equal(qyRwRemainingSeconds(task({ ends_at: 0 }), 1000), null)
  })

  test('还没到终点时返回剩余秒数', () => {
    assert.equal(qyRwRemainingSeconds(task({ ends_at: 1600 }), 1000), 600)
  })

  test('已过终点时返回 0 而不是负数', () => {
    assert.equal(qyRwRemainingSeconds(task({ ends_at: 900 }), 1000), 0)
  })
})

describe('已抓条数的分母', () => {
  test('不限条数时只显示已抓数,不显示 0 作分母', () => {
    assert.equal(qyRwProgressText(task({ captured: 37, max_records: 0 })), '37')
  })

  test('有上限时显示分子分母', () => {
    assert.equal(
      qyRwProgressText(task({ captured: 37, max_records: 500 })),
      '37 / 500'
    )
  })
})

describe('保留期的三个语义值', () => {
  // null / 0 / 正整数在这一格上分别是"跟随全局""永久保留""自己配了 N 天"。
  // 折成两种的话,一份要跟着仲裁走完的取证材料会显示成"30 天后清理"。
  test('null 是跟随全局默认', () => {
    assert.equal(qyRwRetentionKind(task({ retention_days: null })), 'default')
  })

  test('0 是永久保留', () => {
    assert.equal(qyRwRetentionKind(task({ retention_days: 0 })), 'forever')
  })

  test('正整数是自己配的天数', () => {
    assert.equal(qyRwRetentionKind(task({ retention_days: 7 })), 'custom')
  })
})
