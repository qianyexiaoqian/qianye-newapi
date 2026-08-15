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
  qyGmCellKey,
  qyGmCellKeyUserGroup,
  qyGmIndexCells,
  type QyGmDraftEntry,
} from '../../admin-group-matrix/lib/draft'
import type {
  QyGmCell,
  QyGmMatrixResponse,
  QyGmUserGroup,
} from '../../admin-group-matrix/types'
import {
  qyUgFilterGroups,
  qyUgGrantedCount,
  qyUgGrantedModelGroups,
  qyUgResolveSelected,
} from '../lib/rows'

/**
 * 主从式那一页多出来的三条规则。每一条错了都是静默的：
 * 选中项指向一个已消失的分组 → 右侧每一格保存时被后端拒，界面上看不出原因；
 * 搜索不折叠大小写 → 运营得出「这个分组不存在」的结论；
 * 计数把清单里的悬空项算进去 → 一个实际为空的范围显示成「已设定范围 · 1 个」。
 */

function userGroup(
  name: string,
  overrides: Partial<QyGmUserGroup> = {}
): QyGmUserGroup {
  return {
    name,
    display_name: '',
    note: '',
    enabled: true,
    sort_order: 0,
    registered: true,
    observed: true,
    topup_ratio: null,
    topup_ratio_effective: '1',
    model_groups: [],
    user_count: 0,
    active_token_count: 0,
    managed: false,
    scope_state: 'unset',
    // 影子档下线之后后端只下发这一个值：未设范围的行也带 enforce
    // （空串会让老前端的二值枚举落进"两个选项都没选中"）。
    mode: 'enforce',
    scope_enforced: false,
    allow_auto: true,
    scope_note: '',
    self_excluded: false,
    self_inserted: false,
    ...overrides,
  }
}

function cell(
  userGroupName: string,
  modelGroup: string,
  granted: boolean
): QyGmCell {
  return {
    user_group: userGroupName,
    model_group: modelGroup,
    granted,
    ratio: null,
    source: 'inherit',
    inherited_from: '1',
    note: '',
    effective_note: '',
    note_pending: false,
    note_source: 'group_name',
  }
}

function matrix(
  userGroups: QyGmUserGroup[],
  modelGroups: string[]
): QyGmMatrixResponse {
  return {
    user_groups: userGroups,
    model_groups: modelGroups.map((name) => ({
      name,
      base_ratio: '1',
      has_channels: true,
  grantable: true,
      display_name: '',
      note: '',
      user_selectable: false,
      usable_description: '',
    })),
    cells: [],
    base_ratio_hash: 'h',
    snapshot: { loaded: true, age_seconds: 0, version: 1 },
    warnings: [],
  }
}

describe('qyUgFilterGroups', () => {
  const groups = [userGroup('default'), userGroup('VIP'), userGroup('浅夜专属')]

  test('空查询原样返回同一个数组引用', () => {
    assert.equal(qyUgFilterGroups(groups, '   '), groups)
  })

  test('折叠大小写与两侧空白，与后端 Normalize 同口径', () => {
    // 运营输入 `VIP` 搜不到 `vip` 时得出的结论是「这个分组不存在」，
    // 然后去新建一个 —— 而新建一个分组同时也会造出一个模型分组。
    assert.deepEqual(
      qyUgFilterGroups(groups, ' vip ').map((group) => group.name),
      ['VIP']
    )
  })

  test('按子串匹配，非 ASCII 名字同样命中', () => {
    assert.deepEqual(
      qyUgFilterGroups(groups, '专属').map((group) => group.name),
      ['浅夜专属']
    )
  })

  test('搜不到时返回空数组而不是全部', () => {
    assert.deepEqual(qyUgFilterGroups(groups, 'nope'), [])
  })
})

describe('qyUgResolveSelected', () => {
  const groups = [userGroup('default'), userGroup('vip')]

  test('选中项仍在清单里就保持不动', () => {
    assert.equal(qyUgResolveSelected(groups, 'vip'), 'vip')
  })

  test('选中项已从清单里消失时回落到第一项', () => {
    // 另一个管理员在「模型分组定价」里删掉了这一档。停在原名字上会让右侧渲染
    // 一个不存在的分组，运营在上面配的每一格都会在保存时被后端拒绝。
    assert.equal(qyUgResolveSelected(groups, 'gone'), 'default')
  })

  test('还没选过时取第一项', () => {
    assert.equal(qyUgResolveSelected(groups, null), 'default')
  })

  test('一个分组都没有时返回 null，由调用方渲染空态', () => {
    assert.equal(qyUgResolveSelected([], 'vip'), null)
  })
})

describe('qyUgGrantedCount', () => {
  const data = matrix([userGroup('vip', { managed: true })], ['fast', 'slow'])

  test('数的是服务端状态与草稿合并后的结果', () => {
    const serverCells = qyGmIndexCells([
      cell('vip', 'fast', true),
      cell('vip', 'slow', false),
    ])
    const draft = new Map<string, QyGmDraftEntry>([
      [qyGmCellKey('vip', 'slow'), { granted: true }],
    ])
    assert.equal(qyUgGrantedCount(data, serverCells, draft, 'vip'), 2)
  })

  test('草稿里的撤销立刻反映到计数上', () => {
    const serverCells = qyGmIndexCells([cell('vip', 'fast', true)])
    const draft = new Map<string, QyGmDraftEntry>([
      [qyGmCellKey('vip', 'fast'), { granted: false }],
    ])
    assert.equal(qyUgGrantedCount(data, serverCells, draft, 'vip'), 0)
  })

  test('清单里引用了已从倍率表删掉的模型分组时不计入', () => {
    // 该项已经不可达，算进来会把一个实际为空的范围显示成「已设定范围 · 1 个」，
    // 而真正该被看见的是后端另发的那条「引用了已消失的模型分组」告警。
    const serverCells = qyGmIndexCells([cell('vip', 'retired', true)])
    assert.equal(qyUgGrantedCount(data, serverCells, new Map(), 'vip'), 0)
  })
})

describe('qyUgGrantedModelGroups', () => {
  // 「用户分组」那张表的【可用模型分组】列直接列名字（需求 4）。列错名字比列错
  // 个数更糟：运营会据此认定某一档人能用某个池子，而实际不能。
  const data = matrix(
    [userGroup('vip', { managed: true })],
    ['免费の渠道', '浅夜の梦专属号池', 'slow']
  )

  test('按列轴顺序给出名字，草稿合并后生效', () => {
    const serverCells = qyGmIndexCells([
      cell('vip', '免费の渠道', true),
      cell('vip', '浅夜の梦专属号池', false),
      cell('vip', 'slow', false),
    ])
    const draft = new Map<string, QyGmDraftEntry>([
      [qyGmCellKey('vip', '浅夜の梦专属号池'), { granted: true }],
    ])
    assert.deepEqual(qyUgGrantedModelGroups(data, serverCells, draft, 'vip'), [
      '免费の渠道',
      '浅夜の梦专属号池',
    ])
  })

  test('已从倍率表删掉的模型分组不出现在名单里', () => {
    // 它已经不可达。列出来会让运营以为这一档还能用它，而唯一说了实话的是
    // 后端另发的那条「引用了已消失的模型分组」告警。
    const serverCells = qyGmIndexCells([cell('vip', 'retired', true)])
    assert.deepEqual(
      qyUgGrantedModelGroups(data, serverCells, new Map(), 'vip'),
      []
    )
  })

  test('与徽章上的计数同源：长度恒等于 qyUgGrantedCount', () => {
    // 表里写 3 个、点开弹窗只列出 2 个，运营会认为页面坏了并重配一遍 ——
    // 而重配的动作恰好是撤销与改价。
    const serverCells = qyGmIndexCells([
      cell('vip', '免费の渠道', true),
      cell('vip', 'slow', true),
    ])
    const draft = new Map<string, QyGmDraftEntry>([
      [qyGmCellKey('vip', 'slow'), { granted: false }],
    ])
    assert.equal(
      qyUgGrantedModelGroups(data, serverCells, draft, 'vip').length,
      qyUgGrantedCount(data, serverCells, draft, 'vip')
    )
  })
})

describe('qyGmCellKeyUserGroup', () => {
  test('取回复合键里的用户分组，名字含分隔符以外的任何字符都不受影响', () => {
    // 左列表的「本次草稿动过」标记完全依赖它。返回错值时标记会静默消失，
    // 而标记消失恰好长得像「没有未保存改动」。
    assert.equal(
      qyGmCellKeyUserGroup(qyGmCellKey('浅夜:の/组', 'fast')),
      '浅夜:の/组'
    )
  })

  test('没有分隔符时整串就是分组名，不返回空字符串', () => {
    assert.equal(qyGmCellKeyUserGroup('default'), 'default')
  })
})
