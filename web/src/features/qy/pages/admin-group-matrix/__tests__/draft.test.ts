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
  qyGmBuildChanges,
  qyGmCaseNearMisses,
  qyGmCellKey,
  qyGmCountChanges,
  qyGmDraftFingerprint,
  qyGmGrantedOf,
  qyGmHasRevoke,
  qyGmIndexCells,
  qyGmInvalidCells,
  qyGmParseRatio,
  qyGmRatioDraftOf,
  type QyGmDraftEntry,
} from '../lib/draft'
import type { QyGmCell } from '../types'

/**
 * 草稿层的行为契约。
 *
 * 这一层的每一条规则错了都直接是钱：`null` 与 `0` 混淆会让一个本该免费的分组
 * 按兜底价扣；「回到原值仍算改动」会让保存与预览的指纹对不上而白吃 409；
 * 动作列表排序不稳定会让同一份草稿两次提交出不同的 JSON。
 */

function cell(
  overrides: Partial<QyGmCell> & Pick<QyGmCell, 'user_group' | 'model_group'>
): QyGmCell {
  return {
    granted: false,
    ratio: null,
    source: 'inherit',
    inherited_from: '1',
    ...overrides,
  }
}

function draftOf(
  entries: [string, QyGmDraftEntry][]
): Map<string, QyGmDraftEntry> {
  return new Map(entries)
}

describe('qyGmParseRatio', () => {
  test('空输入判为非法（继承由输入框留空表达，不经过本函数）', () => {
    assert.equal(qyGmParseRatio(''), 'invalid')
    assert.equal(qyGmParseRatio('   '), 'invalid')
  })

  test('显式 0 是合法值而不是空值', () => {
    assert.equal(qyGmParseRatio('0'), 0)
    assert.equal(qyGmParseRatio('0.0'), 0)
  })

  test('负数被拒 —— 负倍率会变成给用户返钱', () => {
    assert.equal(qyGmParseRatio('-1'), 'invalid')
    assert.equal(qyGmParseRatio('-0.5'), 'invalid')
  })

  test('科学计数与十六进制被拒，即使 JS 认得它们', () => {
    assert.equal(qyGmParseRatio('1e3'), 'invalid')
    assert.equal(qyGmParseRatio('0x10'), 'invalid')
    assert.equal(qyGmParseRatio('Infinity'), 'invalid')
  })

  test('超过上限的值被拒，不让荒谬的乘数走完整条链路', () => {
    assert.equal(qyGmParseRatio('1000'), 1000)
    assert.equal(qyGmParseRatio('1001'), 'invalid')
  })
})

describe('qyGmRatioDraftOf', () => {
  test('服务端是继承时输入框留空，绝不预填兜底值', () => {
    const source = cell({
      user_group: 'vip',
      model_group: 'pool',
      granted: true,
      source: 'inherit',
      inherited_from: '0.3',
    })
    assert.deepEqual(qyGmRatioDraftOf(source, undefined), { kind: 'inherit' })
  })

  test('服务端配了显式 0 时回显 0，而不是退化成继承', () => {
    const source = cell({
      user_group: 'vip',
      model_group: 'pool',
      granted: true,
      ratio: 0,
      source: 'override',
    })
    assert.deepEqual(qyGmRatioDraftOf(source, undefined), {
      kind: 'set',
      raw: '0',
    })
  })

  test('草稿覆盖服务端值', () => {
    const source = cell({
      user_group: 'vip',
      model_group: 'pool',
      granted: true,
      ratio: 0.5,
      source: 'override',
    })
    assert.deepEqual(qyGmRatioDraftOf(source, { ratio: { kind: 'inherit' } }), {
      kind: 'inherit',
    })
  })
})

describe('qyGmBuildChanges', () => {
  const server = qyGmIndexCells([
    cell({ user_group: 'vip', model_group: 'pool', granted: true }),
    cell({
      user_group: 'vip',
      model_group: 'paid',
      granted: true,
      ratio: 0.5,
      source: 'override',
    }),
    cell({ user_group: 'free', model_group: 'pool', granted: false }),
  ])

  test('把可选性改动翻译成 grant / revoke', () => {
    const changes = qyGmBuildChanges(
      draftOf([
        [qyGmCellKey('free', 'pool'), { granted: true }],
        [qyGmCellKey('vip', 'pool'), { granted: false }],
      ]),
      server
    )
    assert.deepEqual(changes, [
      {
        user_group: 'free',
        model_group: 'pool',
        action: 'grant',
        ratio: null,
      },
      {
        user_group: 'vip',
        model_group: 'pool',
        action: 'revoke',
        ratio: null,
      },
    ])
  })

  test('改回原值不产出任何动作 —— 否则「未保存改动」的数字会骗人', () => {
    const changes = qyGmBuildChanges(
      draftOf([
        [qyGmCellKey('vip', 'pool'), { granted: true }],
        [
          qyGmCellKey('vip', 'paid'),
          { granted: true, ratio: { kind: 'set', raw: '0.5' } },
        ],
      ]),
      server
    )
    assert.deepEqual(changes, [])
  })

  test('显式 0 产出 set_ratio 而不是被当成空值丢掉', () => {
    const changes = qyGmBuildChanges(
      draftOf([
        [qyGmCellKey('vip', 'pool'), { ratio: { kind: 'set', raw: '0' } }],
      ]),
      server
    )
    assert.deepEqual(changes, [
      {
        user_group: 'vip',
        model_group: 'pool',
        action: 'set_ratio',
        ratio: 0,
      },
    ])
  })

  test('本来就是继承的格子清空输入不产出 clear_ratio', () => {
    const changes = qyGmBuildChanges(
      draftOf([[qyGmCellKey('vip', 'pool'), { ratio: { kind: 'inherit' } }]]),
      server
    )
    assert.deepEqual(changes, [])
  })

  test('有覆盖的格子清空输入产出 clear_ratio', () => {
    const changes = qyGmBuildChanges(
      draftOf([[qyGmCellKey('vip', 'paid'), { ratio: { kind: 'inherit' } }]]),
      server
    )
    assert.deepEqual(changes, [
      {
        user_group: 'vip',
        model_group: 'paid',
        action: 'clear_ratio',
        ratio: null,
      },
    ])
  })

  test('非法输入不产出动作，由 qyGmInvalidCells 单独拦截', () => {
    const draft = draftOf([
      [qyGmCellKey('vip', 'pool'), { ratio: { kind: 'set', raw: 'abc' } }],
    ])
    assert.deepEqual(qyGmBuildChanges(draft, server), [])
    assert.deepEqual(qyGmInvalidCells(draft), [qyGmCellKey('vip', 'pool')])
  })

  test('动作列表顺序稳定，与草稿插入顺序无关', () => {
    const forward = qyGmBuildChanges(
      draftOf([
        [qyGmCellKey('vip', 'pool'), { granted: false }],
        [qyGmCellKey('free', 'pool'), { granted: true }],
      ]),
      server
    )
    const reversed = qyGmBuildChanges(
      draftOf([
        [qyGmCellKey('free', 'pool'), { granted: true }],
        [qyGmCellKey('vip', 'pool'), { granted: false }],
      ]),
      server
    )
    assert.deepEqual(forward, reversed)
    assert.equal(
      qyGmDraftFingerprint(forward),
      qyGmDraftFingerprint(reversed),
      '同一份草稿两次提交必须得到同一个指纹，否则闸门会把合法保存拦成 409'
    )
  })

  test('同一格同时改可选性与倍率，两条动作都产出', () => {
    const changes = qyGmBuildChanges(
      draftOf([
        [
          qyGmCellKey('free', 'pool'),
          { granted: true, ratio: { kind: 'set', raw: '0.2' } },
        ],
      ]),
      server
    )
    assert.deepEqual(
      changes.map((change) => change.action),
      ['set_ratio', 'grant']
    )
  })
})

describe('qyGmCountChanges', () => {
  test('撤销单独计数，不与放开和改价混在一起', () => {
    const counts = qyGmCountChanges([
      { user_group: 'a', model_group: 'x', action: 'grant', ratio: null },
      { user_group: 'b', model_group: 'x', action: 'revoke', ratio: null },
      { user_group: 'c', model_group: 'x', action: 'set_ratio', ratio: 1 },
      { user_group: 'd', model_group: 'x', action: 'clear_ratio', ratio: null },
    ])
    assert.deepEqual(counts, { grant: 1, revoke: 1, reprice: 2, total: 4 })
  })

  test('只有撤销才触发预览闸门', () => {
    assert.equal(
      qyGmHasRevoke([
        { user_group: 'a', model_group: 'x', action: 'grant', ratio: null },
        { user_group: 'c', model_group: 'x', action: 'set_ratio', ratio: 1 },
      ]),
      false
    )
    assert.equal(
      qyGmHasRevoke([
        { user_group: 'b', model_group: 'x', action: 'revoke', ratio: null },
      ]),
      true
    )
  })
})

describe('qyGmGrantedOf', () => {
  test('服务端没有这个格子时按不可选处理', () => {
    assert.equal(qyGmGrantedOf(undefined, undefined), false)
  })

  test('草稿优先于服务端', () => {
    const source = cell({
      user_group: 'vip',
      model_group: 'pool',
      granted: true,
    })
    assert.equal(qyGmGrantedOf(source, { granted: false }), false)
  })
})

describe('qyGmCaseNearMisses', () => {
  test('仅大小写不同的名字被列出来，但不折叠成一个', () => {
    const pairs = qyGmCaseNearMisses(
      [
        {
          name: 'VIP',
          user_count: 1,
          active_token_count: 0,
          managed: false,
          scope_state: 'unset',
          mode: 'shadow',
          allow_auto: true,
        },
      ],
      [{ name: 'vip', base_ratio: '1', has_channels: true }]
    )
    assert.deepEqual(pairs, [{ left: 'VIP', right: 'vip' }])
  })

  test('两个轴上同名（大小写也相同）不算近似项', () => {
    const pairs = qyGmCaseNearMisses(
      [
        {
          name: 'vip',
          user_count: 1,
          active_token_count: 0,
          managed: false,
          scope_state: 'unset',
          mode: 'shadow',
          allow_auto: true,
        },
      ],
      [{ name: 'vip', base_ratio: '1', has_channels: true }]
    )
    assert.deepEqual(pairs, [])
  })
})
