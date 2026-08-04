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

import { qyGmOrphansCsv } from '../lib/csv'
import type { QyGmOrphanRow } from '../types'

function row(overrides: Partial<QyGmOrphanRow> = {}): QyGmOrphanRow {
  return {
    group: 'default → vip',
    user_group: 'default',
    model_group: 'vip',
    count: 3,
    enabled_count: 2,
    active_30d: 1,
    reason: '无权访问 vip 分组',
    samples: [],
    ...overrides,
  }
}

describe('qyGmOrphansCsv', () => {
  test('汇总行与样本行都出，样本行不重复计数', () => {
    const csv = qyGmOrphansCsv([
      {
        category: 'orphan_tokens',
        rows: [
          row({
            samples: [
              {
                token_id: 7,
                token_name: 'my key',
                key_masked: 'sk-****ab',
                user_id: 42,
                username: 'alice',
                group: 'vip',
                status: 1,
                accessed_time: 1_700_000_000,
              },
            ],
          }),
        ],
      },
    ])
    const lines = csv.split('\n')
    assert.equal(lines.length, 3, '表头 + 汇总行 + 样本行')
    assert.match(lines[1], /"orphan_tokens","default → vip","3","2","1"/)
    assert.match(lines[2], /"orphan_tokens","default → vip",,,,,"7","my key"/)
  })

  test('前导 = 的令牌名被中和，Excel 打开时不会执行它', () => {
    const csv = qyGmOrphansCsv([
      {
        category: 'orphan_tokens',
        rows: [
          row({
            samples: [
              {
                token_id: 1,
                token_name: '=1+1',
                key_masked: 'sk-****',
                user_id: 1,
                username: '@cmd',
                group: 'vip',
                status: 1,
                accessed_time: 0,
              },
            ],
          }),
        ],
      },
    ])
    assert.ok(csv.includes(`"'=1+1"`), '公式注入未被中和')
    assert.ok(csv.includes(`"'@cmd"`), '用户名同样是自由输入，必须一起中和')
  })

  test('含双引号的分组名被转义而不是把这一行截断', () => {
    const csv = qyGmOrphansCsv([
      { category: 'orphan_users', rows: [row({ group: 'a"b' })] },
    ])
    assert.ok(csv.includes('"a""b"'))
  })

  test('没有样本的分组仍然出汇总行 —— 样本数有上限，空不等于没有令牌', () => {
    const csv = qyGmOrphansCsv([
      { category: 'deprecated_tokens', rows: [row({ samples: [] })] },
    ])
    assert.equal(csv.split('\n').length, 2)
  })
})
