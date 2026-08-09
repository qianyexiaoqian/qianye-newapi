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
  qyUgrDeleteBlock,
  qyUgrDeleteEntry,
  qyUgrGroupChanges,
  qyUgrRenameEntryBlock,
} from '../lib/gates'
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
    renamable: true,
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

/*
  ── 入口级闸门只许照抄后端真的有的那一条 ──────────────────────────────

  这一组防的是前端**自己发明拒绝**。它比"漏一条闸门"更难发现：界面看起来
  完全正常，只是某一类行永远做不了某个动作，而给出的理由指向一条走不通的路。

  两条真实判据（逐字对齐 `qianye/modules/groupns/usergroup_admin.go`）：

    删除  `adminDeleteUserGroup` → `lookupUserGroup`：**不要求登记行**，
          只要求"有登记行 或 users.group 里有人挂着"。注释原文写着
          「只在 users.group 里的历史遗留分组同样要能删」。
    改名  `adminRenameUserGroup` 第一句 `Take(&before)`：没有登记行直接 400。
*/
describe('qyUgrDeleteEntry', () => {
  test('未登记但有人挂着 —— 后端明说这一类要能删,入口不许拦', () => {
    const entry = qyUgrDeleteEntry({
      name: '历史遗留档',
      registered: false,
      user_count: 37,
    })
    assert.equal(
      entry.enabled,
      true,
      '这正是后端注释点名"最需要被清掉的那一类"。拦住它等于要求运营先把一个' +
        '正要删掉的分组建出来 —— 一整步毫无意义的操作'
    )
    assert.equal(entry.noteKey, null)
  })

  test('default 能打开弹窗 —— 后端那段拒绝理由只有弹窗渲染得出来', () => {
    const entry = qyUgrDeleteEntry({
      name: 'default',
      registered: true,
      user_count: 12,
    })
    assert.equal(
      entry.enabled,
      true,
      '删除弹窗是全站唯一渲染 impact.block_reason 的地方。入口就关掉的话,' +
        '后端 deletability() 对 default 写的那段理由与替代做法永远到不了屏幕,' +
        '运营读到的只是前端另写的一份没有任何一致性检查的副本'
    )
    assert.equal(
      entry.noteKey,
      'qy_ug_delete_default_note',
      '按钮能按不等于删得掉 —— 行上仍要有一句短的先说清楚'
    )
  })

  test('大小写与空白按后端 normalizeGroupKey 折叠', () => {
    assert.equal(
      qyUgrDeleteEntry({ name: ' Default ', registered: true, user_count: 1 })
        .noteKey,
      'qy_ug_delete_default_note'
    )
  })

  test('既没登记行、也没人挂着 —— 删除端点寻址不到,这一条才真的关按钮', () => {
    const entry = qyUgrDeleteEntry({
      name: '只剩倍率键',
      registered: false,
      user_count: 0,
    })
    assert.equal(entry.enabled, false)
    assert.equal(
      entry.noteKey,
      'qy_ug_delete_not_addressable',
      '关掉按钮时**必须**同时给出文案：一个按不动又不解释的按钮正是本次投诉'
    )
  })

  test('普通的已登记分组：能按,且不留一句常驻的废话', () => {
    const entry = qyUgrDeleteEntry({
      name: 'qy-canary-ug',
      registered: true,
      user_count: 3,
    })
    assert.deepEqual(entry, { enabled: true, noteKey: null })
  })
})

describe('qyUgrRenameEntryBlock', () => {
  test('改名确实要求登记行 —— 端点第一句就 Take(&before)', () => {
    assert.equal(
      qyUgrRenameEntryBlock({ name: '历史遗留档', registered: false }),
      'unregistered'
    )
  })

  test('未登记的 default 先说 default —— 补完登记它照样改不了名', () => {
    assert.equal(
      qyUgrRenameEntryBlock({ name: 'default', registered: false }),
      'upstream_default'
    )
  })

  test('已登记的普通分组不拦 —— block 残留那一条由影响面在弹窗里给', () => {
    assert.equal(
      qyUgrRenameEntryBlock({ name: 'qy-canary-ug', registered: true }),
      null
    )
  })
})

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
