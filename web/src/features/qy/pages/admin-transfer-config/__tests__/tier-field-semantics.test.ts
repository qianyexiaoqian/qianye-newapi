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
import { readFileSync } from 'node:fs'
import { describe, test } from 'node:test'

import zh from '../../../../../i18n/qy/zh.json'
import { QY_TRANSFER_FIELDS, qyTransferFieldMeta } from '../lib/fields'

/**
 * tier-field-semantics.test.ts —— 「0 到底是什么意思」必须与后端守卫一致。
 *
 * 后端每一道闸门都写成 `if cfg.X > 0 { ... }`，也就是 **0 = 这道闸门不设**。
 * 前端的 `zeroMeansUnlimited` 是这句话在界面上的唯一载体：它决定分档列表里那个
 * `$0` 带不带「(不限)」角标。缺一位的后果不是显示不好看 —— 运营会把「我把这一档
 * 冻住了」读成事实，而真实后果正好相反：这一档从此没有单笔上限。
 */

/** 后端 `cfg.X > 0` 守卫覆盖的七个可分档门槛 —— 它们的 0 一律表示「不设这道闸门」。 */
const ZERO_MEANS_UNLIMITED = [
  'min_quota',
  'max_per_tx_quota',
  'daily_max_quota',
  'daily_max_count',
  'cooldown_seconds',
  'receiver_daily_max_in_count',
  'new_account_freeze_hours',
]

describe('门槛字段的 0 语义', () => {
  for (const key of ZERO_MEANS_UNLIMITED) {
    test(`${key} 的 0 必须被标成「不限」`, () => {
      const meta = qyTransferFieldMeta(key)
      assert.notEqual(meta, null, `${key} 没有登记元数据`)
      assert.equal(
        meta?.zeroMeansUnlimited,
        true,
        `${key} 缺 zeroMeansUnlimited：后端守卫是 cfg.${key} > 0，` +
          `0 会关掉这道闸门而不是冻住它，界面必须说清楚`
      )
    })
  }

  test('max_per_tx_quota 的字段提示不得声称 0 会让任何金额都不合法', () => {
    const hintKey = QY_TRANSFER_FIELDS.max_per_tx_quota.hintKey
    const hint = (zh as Record<string, string>)[hintKey]
    assert.equal(typeof hint, 'string')
    assert.ok(
      hint.includes('0'),
      '提示里必须交代 0 的含义 —— 它与其余六项一样是「关掉这道闸门」'
    )
  })
})

describe('分档接口的分组下拉命名空间', () => {
  /**
   * 同一个模块的两个端点曾经用同一个 `group_options` 承载两个命名空间：
   * 门槛分档页是用户分组、分组规则页是模型分组。按键名共享的 helper 从两个端点
   * 各拉一次就会把模型分组喂进划转的下拉，而两处各自 import 了不同的类型别名，
   * TypeScript 编译期看不出来。
   */
  test('门槛分档页只认 user_group_options', () => {
    const src = readFileSync(
      new URL('../group-limits-types.ts', import.meta.url),
      'utf8'
    )
    assert.ok(
      src.includes('user_group_options:'),
      '分档页的分组下拉必须叫 user_group_options'
    )
    assert.ok(
      !/^\s*group_options:/m.test(src),
      '分档页出现了 group_options —— 那个键在同模块的另一个端点上是模型分组'
    )
  })

  test('卡片消费的是 user_group_options', () => {
    const src = readFileSync(
      new URL('../components/group-limits-card.tsx', import.meta.url),
      'utf8'
    )
    assert.ok(src.includes('data.user_group_options'))
    assert.ok(!src.includes('data.group_options'))
  })
})
