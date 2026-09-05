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
 * 「只 PUT 改动键」与「按后端区间卡一次」—— 配置页与后端之间的两条契约。
 *
 * 审计里每一条配置变更都该是真的变了：什么都不改直接保存必须一条都挑不出来，
 * 改了一格就只有那一格进请求体。区间是 `lo` / `hi` 闭区间（契约 §3），两端都
 * 必须存在，与后端 `Bound.Contains` 同一条判定。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  qySdBoundContains,
  qySdConfigChanges,
  qySdConfigPatch,
  qySdInvalidKey,
  qySdParseDraft,
} from '../lib/draft'
import type { QyStardustAdminConfig } from '../types'

const CONFIG: QyStardustAdminConfig = {
  effective: {
    name: '星屑',
    show_entry: 1,
    consume_bps: 10_000,
    invite_consume_bps: 0,
    invite_topup_bps: 0,
    invite_redeem_bps: 0,
    invite_register_stardust: 0,
    held_alert_days: 7,
  },
  overrides: {},
  editable_keys: [
    'name',
    'show_entry',
    'consume_bps',
    'invite_consume_bps',
    'invite_topup_bps',
    'invite_redeem_bps',
    'invite_register_stardust',
    'held_alert_days',
  ],
  bounds: {
    show_entry: { lo: 0, hi: 1 },
    consume_bps: { lo: 0, hi: 10_000_000 },
    invite_consume_bps: { lo: 0, hi: 10_000_000 },
    invite_topup_bps: { lo: 0, hi: 10_000_000 },
    invite_redeem_bps: { lo: 0, hi: 10_000_000 },
    invite_register_stardust: { lo: 0, hi: 8_796_093_022_208 },
    held_alert_days: { lo: 0, hi: 365 },
  },
  yaml_readonly: {
    quota_per_unit: 500_000,
    settle_delay_minutes: 30,
    settle_interval_seconds: 300,
    exclude_subscription_consume: true,
    exclude_manual_topup: true,
    max_manual_adjust: 100_000,
    compliance_confirmed: false,
  },
}

/** 从生效值生成的草稿，与页面挂载时的初始草稿同一形状。 */
function pristine(): Record<string, string> {
  return {
    name: '星屑',
    show_entry: '1',
    consume_bps: '10000',
    invite_consume_bps: '0',
    invite_topup_bps: '0',
    invite_redeem_bps: '0',
    invite_register_stardust: '0',
    held_alert_days: '7',
  }
}

describe('只提交改动键', () => {
  test('什么都不改时一条改动都挑不出来，请求体为空', () => {
    const changes = qySdConfigChanges(CONFIG, pristine())
    assert.deepEqual(changes, [])
    assert.deepEqual(qySdConfigPatch(changes), {})
  })

  test('改一格万分比：只有那一格进请求体，且是数字', () => {
    const changes = qySdConfigChanges(CONFIG, {
      ...pristine(),
      consume_bps: '12000',
    })
    assert.deepEqual(changes, [
      { key: 'consume_bps', from: 10_000, to: 12_000 },
    ])
    assert.deepEqual(qySdConfigPatch(changes), { consume_bps: 12_000 })
  })

  test('单位名去两侧空白后比较；改了就以字符串进请求体', () => {
    assert.deepEqual(
      qySdConfigChanges(CONFIG, { ...pristine(), name: '  星屑  ' }),
      [],
      '只多了空白不算改动'
    )
    const changes = qySdConfigChanges(CONFIG, { ...pristine(), name: ' 星尘 ' })
    assert.deepEqual(changes, [{ key: 'name', from: '星屑', to: '星尘' }])
    assert.deepEqual(qySdConfigPatch(changes), { name: '星尘' })
  })

  test('开关从 1 改成 0 算一条改动，值是数字 0', () => {
    const changes = qySdConfigChanges(CONFIG, {
      ...pristine(),
      show_entry: '0',
    })
    assert.deepEqual(changes, [{ key: 'show_entry', from: 1, to: 0 }])
  })

  test('非法草稿不算改动，由 invalidKey 单独报出来挡住保存', () => {
    const draft = {
      ...pristine(),
      held_alert_days: '400',
      consume_bps: '12000',
    }
    assert.deepEqual(qySdConfigChanges(CONFIG, draft), [
      { key: 'consume_bps', from: 10_000, to: 12_000 },
    ])
    assert.equal(qySdInvalidKey(CONFIG, draft), 'held_alert_days')
    assert.equal(qySdInvalidKey(CONFIG, pristine()), null)
  })
})

describe('区间与单位名的判定', () => {
  test('lo / hi 是闭区间', () => {
    const bound = { lo: 0, hi: 365 }
    assert.ok(qySdBoundContains(bound, 0))
    assert.ok(qySdBoundContains(bound, 365))
    assert.ok(!qySdBoundContains(bound, 366))
    assert.ok(!qySdBoundContains(bound, -1))
  })

  test('数值键：空串、小数、负数、越界一律 null', () => {
    for (const raw of ['', '1.5', '-1', '10000001', 'abc']) {
      assert.equal(
        qySdParseDraft(CONFIG, 'consume_bps', raw),
        null,
        `"${raw}" 该被拒绝`
      )
    }
    assert.equal(
      qySdParseDraft(CONFIG, 'consume_bps', ' 10000000 '),
      10_000_000
    )
  })

  test('单位名：空白与超过 16 个字符被拒绝，按 code point 数而不是 UTF-16 码元', () => {
    assert.equal(qySdParseDraft(CONFIG, 'name', '   '), null)
    assert.equal(qySdParseDraft(CONFIG, 'name', '一'.repeat(17)), null)
    assert.equal(
      qySdParseDraft(CONFIG, 'name', '一'.repeat(16)),
      '一'.repeat(16)
    )
    // 16 个代理对字符：UTF-16 长度 32，但后端按 rune 数只算 16。
    const astral = '𝄞'.repeat(16)
    assert.equal(qySdParseDraft(CONFIG, 'name', astral), astral)
  })
})
