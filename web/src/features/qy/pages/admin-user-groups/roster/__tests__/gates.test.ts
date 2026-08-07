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

import { qyUgrDeleteBlock, qyUgrGroupChanges } from '../lib/gates'
import type { QyUgrImpact, QyUgrMigrationDiff } from '../types'

/**
 * 删除闸门与差异分堆。
 *
 * ── 这两条锁防的是什么 ──
 *
 * 删除一个用户分组同时是一次批量改价与一次批量权限变更。四道闸门里有三道在
 * 后端也各有一份,前端这一份的唯一职责是**把服务端的结论翻译成按钮状态**。
 * 一旦有人在这里"顺手"加一条自己的判据,漂移的方向永远是同一个:
 * 按钮亮着、点下去 400,而运营会以为是系统坏了。
 *
 * 差异分堆同理:removed / added / repriced 三堆的后果方向完全不同
 * (令牌当场 403 / 意外开放 / 账单变化),混在一个列表里读不出来。
 */

function impact(patch: Partial<QyUgrImpact>): QyUgrImpact {
  return {
    blocking: [],
    blocking_plans: [],
    deletable: true,
    empty_group_tokens: 0,
    name: '临时档',
    residues: [],
    subscriptions: 0,
    targets: [],
    tokens: 0,
    users: 0,
    ...patch,
  }
}

function diff(patch: Partial<QyUgrMigrationDiff>): QyUgrMigrationDiff {
  return {
    changes: [],
    from: '临时档',
    loses_everything: false,
    to: '目标档',
    unchanged: 0,
    ...patch,
  }
}

describe('qyUgrDeleteBlock', () => {
  test('影响面还没到达时按钮必须是禁用的', () => {
    // 此时人数、令牌数、可用清单差**一个都还不存在**,而它们正是这次决定的全部依据。
    assert.equal(qyUgrDeleteBlock(null, '', false), 'loading')
  })

  test('服务端说不能删就是不能删,前端不复算原因', () => {
    const blocked = impact({ deletable: false, block_reason: '套餐还在卖它' })
    assert.equal(qyUgrDeleteBlock(blocked, '目标档', true), 'blocked')
  })

  test('还有用户而没选迁移目标 —— 这一档是需求原文点名的那道闸门', () => {
    // 直接删会让这批账号的 users.group 指向一个不存在的分组,
    // 而分组倍率对孤儿是 fail-open 返回 1.0:他们静默按原价扣费。
    assert.equal(
      qyUgrDeleteBlock(impact({ users: 3 }), '', false),
      'needs_target'
    )
    assert.equal(qyUgrDeleteBlock(impact({ users: 3 }), '目标档', false), null)
  })

  test('一个人都没有时不需要迁移目标', () => {
    assert.equal(qyUgrDeleteBlock(impact({ users: 0 }), '', false), null)
  })

  test('目标一个模型分组都用不了时必须显式勾选', () => {
    const doomed = impact({
      diff: diff({ loses_everything: true }),
      users: 700,
    })
    assert.equal(qyUgrDeleteBlock(doomed, '目标档', false), 'needs_ack')
    assert.equal(qyUgrDeleteBlock(doomed, '目标档', true), null)
  })

  test('目标能用时不弹这道闸门 —— 常驻的红勾选框会让真正的风险失去信号', () => {
    const fine = impact({ diff: diff({ unchanged: 5 }), users: 700 })
    assert.equal(qyUgrDeleteBlock(fine, '目标档', false), null)
  })
})

describe('qyUgrGroupChanges', () => {
  test('三堆各归各位,倍率保持十进制字符串原样', () => {
    const changes = qyUgrGroupChanges(
      diff({
        changes: [
          { kind: 'removed', model_group: '专属号池' },
          { kind: 'added', model_group: '免费渠道' },
          {
            from_ratio: '0.1',
            kind: 'repriced',
            model_group: '高速池',
            to_ratio: '1',
          },
        ],
      })
    )
    assert.deepEqual(
      changes.removed.map((change) => change.model_group),
      ['专属号池']
    )
    assert.deepEqual(
      changes.added.map((change) => change.model_group),
      ['免费渠道']
    )
    // 0.1 必须**原样**留着:走一遍 JSON number 往返会印成 0.10000000000000001,
    // 而运营正是照着这个数字判断"这次迁移是涨价还是降价"。
    assert.equal(changes.repriced[0]?.from_ratio, '0.1')
    assert.equal(changes.repriced[0]?.to_ratio, '1')
  })

  test('没有 diff 时三堆都是空的,而不是抛异常', () => {
    const changes = qyUgrGroupChanges(null)
    assert.equal(changes.added.length, 0)
    assert.equal(changes.removed.length, 0)
    assert.equal(changes.repriced.length, 0)
  })
})
