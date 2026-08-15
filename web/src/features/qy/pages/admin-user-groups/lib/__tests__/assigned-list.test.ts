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
 * 「这一档能用哪些模型分组」这份列表的三条投影规则。
 *
 * 三条都是"改错了界面照常渲染、只是显示的东西不对"的形状，而这一份列表现在
 * 是运营判断一档人能用什么的**唯一**依据（上一版那个平铺全部分组 + 前置开关
 * 的形态已经下线）。
 */
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  qyGmCellKey,
  type QyGmDraftEntry,
} from '../../../admin-group-matrix/lib/draft'
import type {
  QyGmCell,
  QyGmMatrixResponse,
  QyGmModelGroup,
  QyGmUserGroup,
} from '../../../admin-group-matrix/types'
import { qyUgListView } from '../assigned-list'

const UG = 'qy-canary-ug'

function modelGroup(name: string): QyGmModelGroup {
  return {
    name,
    base_ratio: '1',
    has_channels: true,
  grantable: true,
    display_name: '',
    note: '',
    user_selectable: true,
    usable_description: '',
  }
}

function cell(name: string, patch: Partial<QyGmCell>): QyGmCell {
  return {
    user_group: UG,
    model_group: name,
    granted: false,
    ratio: null,
    source: 'inherit',
    inherited_from: '1',
    note: '',
    effective_note: name,
    note_pending: false,
    note_source: 'group_name',
    ...patch,
  }
}

function userGroup(
  scopeState: QyGmUserGroup['scope_state'],
  modelGroups: string[]
): QyGmUserGroup {
  return {
    name: UG,
    display_name: '',
    note: '',
    enabled: true,
    sort_order: 0,
    registered: true,
    observed: true,
    topup_ratio: null,
    topup_ratio_effective: '1',
    model_groups: modelGroups,
    user_count: 12,
    active_token_count: 3,
    managed: scopeState !== 'unset',
    scope_state: scopeState,
    // 影子档下线之后后端只下发这一个值（未设范围的行也是），
    // 有范围行 = 清单立即生效。
    mode: 'enforce',
    scope_enforced: scopeState !== 'unset',
    allow_auto: false,
    scope_note: '',
    self_excluded: false,
    self_inserted: false,
  }
}

const COLUMNS = ['pool-a', 'pool-b', 'pool-c'].map(modelGroup)

function matrix(cells: QyGmCell[]): QyGmMatrixResponse {
  return {
    user_groups: [],
    model_groups: COLUMNS,
    cells,
    base_ratio_hash: 'h',
    snapshot: { loaded: true, age_seconds: 1, version: 1 },
    warnings: [],
  }
}

function index(cells: QyGmCell[]): Map<string, QyGmCell> {
  return new Map(cells.map((item) => [qyGmCellKey(UG, item.model_group), item]))
}

function draftOf(
  entries: Array<[string, QyGmDraftEntry]>
): Map<string, QyGmDraftEntry> {
  return new Map(entries.map(([name, entry]) => [qyGmCellKey(UG, name), entry]))
}

describe('没有自己的清单时，列表读的是实际可用名单而不是残留的 grant 行', () => {
  /*
    撤销接管只删 scope 行，`qy_group_grants` 刻意保留（重新接管时它就是现成的
    清单）。所以一档没有清单的分组身上完全可能挂着一批上一次配的 grant 行。
    照 `granted` 画，列表显示的是一份**谁也没在用**的旧清单，而运营会照着它
    判断"这一档现在能用什么"—— 界面上没有任何东西会提示这份名单是过期的。
  */
  const cells = [
    cell('pool-a', { granted: true }),
    cell('pool-b', { granted: true }),
  ]
  const row = userGroup('unset', ['pool-a', 'pool-c'])
  const view = qyUgListView(matrix(cells), index(cells), new Map(), row)

  test('列表 = model_groups（上游实际可用），来源标成 fallback', () => {
    assert.deepEqual(
      view.items.map((item) => [item.name, item.origin]),
      [
        ['pool-a', 'fallback'],
        ['pool-c', 'fallback'],
      ]
    )
    assert.equal(view.scoped, false)
    assert.equal(view.memberCount, 2)
  })

  test('残留 grant 行的那一项落进「可添加」，不冒充已分配', () => {
    assert.deepEqual(
      view.addable.map((entry) => entry.column.name),
      ['pool-b']
    )
  })

  test('草稿不参与判定 —— 没有清单的那一档写不进 grant', () => {
    const withDraft = qyUgListView(
      matrix(cells),
      index(cells),
      draftOf([['pool-b', { granted: true }]]),
      row
    )
    assert.deepEqual(
      withDraft.items.map((item) => item.name),
      ['pool-a', 'pool-c'],
      '让草稿改变显示，界面会先变成一个后端必然拒绝的样子'
    )
  })
})

describe('有自己的清单时，列表 = grant ∪ 草稿', () => {
  const cells = [
    cell('pool-a', { granted: true }),
    cell('pool-b', { granted: false }),
  ]
  const row = userGroup('set', ['pool-a'])

  test('草稿里的 grant / revoke 当场反映到列表上', () => {
    const view = qyUgListView(
      matrix(cells),
      index(cells),
      draftOf([
        ['pool-a', { granted: false }],
        ['pool-b', { granted: true }],
      ]),
      row
    )
    assert.deepEqual(
      view.items.map((item) => [item.name, item.origin]),
      [['pool-b', 'scope']]
    )
    assert.deepEqual(
      view.addable.map((entry) => entry.column.name),
      ['pool-a', 'pool-c']
    )
    assert.equal(view.memberCount, 1)
  })
})

describe('仅经套餐可达的模型分组不进清单，落在候选项那一侧', () => {
  /*
    项目方原话：「为何套餐可达的分组会直接插入在用户分组列内，不要这样」。

    它此前被 push 进 `items`，于是「这一档能用的模型分组」里混着一批**这一档
    根本没有授权**的行。这张表回答的是"我给这一档配了什么"，而套餐解锁挂在
    **人**身上、与用户分组无关，两者并排会让运营以为撤销那一行就能收回权限 ——
    撤不回，那一行根本不是他配的。

    但它也不能从这一屏整个消失：买了对应套餐的用户照样能用它，**而且那一格的
    倍率正在扣钱**。抹掉之后站内没有任何一页显示过这一档人能到达它 —— 几周后
    运营为别的目的把那一列的兜底倍率从 1.0 调到 3.0，这批人当场按 3 倍扣费。
    所以它落进候选项，并在那一侧带上让它可达的套餐名。
  */
  const cells = [
    cell('pool-a', { granted: true }),
    cell('pool-c', {
      granted: false,
      reachable_via: 'plan',
      plan_titles: ['月卡'],
    }),
  ]
  const row = userGroup('set', ['pool-a'])
  const view = qyUgListView(matrix(cells), index(cells), new Map(), row)

  test('它不进列表 —— 那张表只回答"我给这一档配了什么"', () => {
    assert.deepEqual(
      view.items.map((item) => [item.name, item.origin]),
      [['pool-a', 'scope']],
      '把它列进来，运营会以为撤销那一行就能收回权限 —— 那一行根本不是他配的'
    )
  })

  test('它落进「可添加」，并带上解锁它的套餐名', () => {
    assert.deepEqual(
      view.addable.map((entry) => [entry.column.name, entry.planTitles]),
      [
        ['pool-b', []],
        ['pool-c', ['月卡']],
      ],
      '套餐名是「这一档人已经能到达它、倍率正在扣钱」在这一屏上唯一的落款'
    )
  })

  test('它不计入 memberCount', () => {
    /*
      memberCount 是「再移一项就一个都不剩了」那道二选一确认的判据。把屏幕上
      那些候选项也算进来，清空清单的那一次点击就不会弹出确认 —— 而「回落全局
      清单」与「一个都不给用」的两个错误方向分别是整档全放行和整档 403。
    */
    assert.equal(view.memberCount, 1)
  })
})

describe('列表只走列轴', () => {
  test('清单引用了一个已从分组倍率表消失的模型分组时，不列出来', () => {
    const row = userGroup('unset', ['pool-a', '早就删掉的池子'])
    const view = qyUgListView(matrix([]), index([]), new Map(), row)
    assert.deepEqual(
      view.items.map((item) => item.name),
      ['pool-a'],
      '它已经不可达了，列出来只会让人以为这一档还能用它'
    )
  })
})
