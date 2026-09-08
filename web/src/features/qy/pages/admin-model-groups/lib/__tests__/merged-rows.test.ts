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

import type { QyMgRow } from '../../types'
import {
  qyMgBuildRows,
  qyMgFreeNames,
  qyMgInvalidRatioNames,
  qyMgSerializeRatios,
  qyMgSerializeUsableGroups,
  qyMgSilentlyBilledNames,
  type QyMgMergedRow,
} from '../merged-rows'

/**
 * 合并成一张表之后，四条会静默出错的规则 —— 每一条的错误方向都是钱。
 *
 *  1. 并集必须含 `UserUsableGroups` 的键：只在全局可选清单里、没有兜底倍率的
 *     分组用户选得到，而 `GetGroupRatio` 找不到键时 fail-open 返回 1。
 *  2. `ratio === null`（不在倍率表里）不能被补成 1：那是替运营做定价决定。
 *  3. 清空倍率输入框不能变成 0（白送整个模型分组）；显式敲进去的 0 必须留着。
 *  4. 「用户可选」的开关**只动键**，`UserUsableGroups` 的 value 一个字都不改 ——
 *     备注只有一个来源（登记表的 `note`），这里再写一份就是第二个写入者。
 */

function registryRow(name: string, patch: Partial<QyMgRow> = {}): QyMgRow {
  return {
    name,
    display_name: name,
    note: '',
    enabled: true,
    sort_order: 0,
    base_ratio: 1,
    ratio_missing: false,
    has_route: true,
    channel_count: 1,
    legacy_dual: false,
    sources: ['group_ratio'],
    in_usable_groups: false,
    usable_description: '',
    auto_position: 0,
    registered: true,
    ...patch,
  }
}

function mergedRow(patch: Partial<QyMgMergedRow>): QyMgMergedRow {
  return {
    id: patch.name ?? 'x',
    name: 'x',
    ratio: '1',
    selectable: false,
    note: '',
    usableDescription: '',
    sources: [],
    hasRoute: null,
    channelCount: null,
    legacyDual: false,
    autoPosition: 0,
    registered: true,
    isNew: false,
    ...patch,
  }
}

describe('模型分组表：三个来源的并集', () => {
  test('只在全局可选清单里的名字仍然建行 —— 它正按凭空的 1.0 计费', () => {
    const rows = qyMgBuildRows({
      registry: [],
      groupRatios: { paid: 1.5 },
      usableGroups: { paid: '', ghost: '旧文案' },
      autoGroups: [],
    })
    const ghost = rows.find((row) => row.name === 'ghost')
    assert.ok(ghost != null, '缺兜底倍率的分组必须建行，否则界面上看不见它')
    assert.equal(ghost.ratio, null)
    assert.equal(ghost.selectable, true)
    assert.deepEqual(qyMgSilentlyBilledNames(rows), ['ghost'])
  })

  test('既不可选也没倍率的纯登记残留不进「静默计费」告警', () => {
    // 它不在任何计费路径上。报出来会让那条黄条变成常驻噪音，而噪音里的真警报
    // 没有人看。
    const rows = qyMgBuildRows({
      registry: [
        registryRow('leftover', {
          sources: ['registry_only'],
          has_route: false,
        }),
      ],
      groupRatios: {},
      usableGroups: {},
      autoGroups: [],
    })
    assert.deepEqual(qyMgSilentlyBilledNames(rows), [])
  })

  test('倍率取 option 侧而不是登记表快照；备注取登记表', () => {
    const rows = qyMgBuildRows({
      registry: [registryRow('pool', { base_ratio: 9, note: '号池说明' })],
      groupRatios: { pool: 0.3 },
      usableGroups: {},
      autoGroups: [],
    })
    assert.equal(rows[0].ratio, '0.3', '正在被编辑的是 option，快照不得盖回来')
    assert.equal(rows[0].note, '号池说明')
    assert.equal(rows[0].registered, true)
  })

  test('auto 位次从 1 起，不在清单里是 0', () => {
    const rows = qyMgBuildRows({
      registry: [],
      groupRatios: { a: 1, b: 1 },
      usableGroups: {},
      autoGroups: ['b'],
    })
    const byName = new Map(rows.map((row) => [row.name, row]))
    assert.equal(byName.get('b')?.autoPosition, 1)
    assert.equal(byName.get('a')?.autoPosition, 0)
  })
})

describe('模型分组表：兜底倍率的写回', () => {
  test('显式 0 原样写回，不被当成未设置丢掉', () => {
    const out = JSON.parse(
      qyMgSerializeRatios([
        mergedRow({ name: 'free', ratio: '0' }),
        mergedRow({ name: 'paid', ratio: '1.5' }),
      ])
    ) as Record<string, number>
    assert.equal(out.free, 0)
    assert.equal(out.paid, 1.5)
  })

  test('ratio === null 的行不写：替它补一个 1 等于替运营做定价决定', () => {
    const out = JSON.parse(
      qyMgSerializeRatios([mergedRow({ name: 'ghost', ratio: null })])
    ) as Record<string, number>
    assert.equal(Object.hasOwn(out, 'ghost'), false)
  })

  test('清空输入框落回 1 并被标成非法，绝不静默变成 0（白送）', () => {
    // 上游 `normalizeRatio` 是 `Number.isFinite(Number(v)) ? Number(v) : 1`，
    // 而 `Number('') === 0` —— 照抄的话清空一个格子就是一次静默的免费定价。
    const rows = [
      mergedRow({ name: 'x', ratio: 'abc' }),
      mergedRow({ name: 'y', ratio: '' }),
      mergedRow({ name: 'z', ratio: '0' }),
    ]
    const out = JSON.parse(qyMgSerializeRatios(rows)) as Record<string, number>
    assert.equal(out.x, 1)
    assert.equal(out.y, 1, '空输入框绝不能变成 0')
    assert.equal(out.z, 0, '显式敲进去的 0 仍然是显式免费')
    assert.deepEqual(qyMgInvalidRatioNames(rows), ['x', 'y'])
  })

  test('小数往返不漂移；空名与首尾空白被规范掉', () => {
    const out = JSON.parse(
      qyMgSerializeRatios([
        mergedRow({ name: 'a', ratio: '0.1' }),
        mergedRow({ name: '   ', ratio: '2' }),
        mergedRow({ name: '  c  ', ratio: '0.30' }),
      ])
    ) as Record<string, number>
    assert.deepEqual(out, { a: 0.1, c: 0.3 })
  })

  test('0 倍率单独列出（白送必须显式看见）；正在被清空的格子不算', () => {
    assert.deepEqual(
      qyMgFreeNames([
        mergedRow({ name: 'free', ratio: '0' }),
        mergedRow({ name: 'typing', ratio: '' }),
        mergedRow({ name: 'paid', ratio: '1' }),
      ]),
      ['free']
    )
  })
})

describe('模型分组表：「用户可选」只动键，不动 value', () => {
  test('勾上的写进清单，value 沿用历史原文', () => {
    const out = JSON.parse(
      qyMgSerializeUsableGroups([
        mergedRow({
          name: 'pool',
          selectable: true,
          usableDescription: '本站不留存数据',
          note: '运营新写的备注',
        }),
      ])
    ) as Record<string, string>
    assert.equal(
      out.pool,
      '本站不留存数据',
      '把 note 复制进 options 会让备注有第二个写入者：' +
        '此后改备注，用户看到的仍是复制品'
    )
  })

  test('勾掉的整条移出清单', () => {
    const out = JSON.parse(
      qyMgSerializeUsableGroups([
        mergedRow({ name: 'gone', selectable: false, usableDescription: 'x' }),
        mergedRow({ name: 'kept', selectable: true, usableDescription: 'y' }),
      ])
    ) as Record<string, string>
    assert.deepEqual(out, { kept: 'y' })
  })

  test('没有历史原文时写空串，不写 note —— 备注仍然只有一个来源', () => {
    const out = JSON.parse(
      qyMgSerializeUsableGroups([
        mergedRow({ name: 'fresh', selectable: true, note: '备注' }),
      ])
    ) as Record<string, string>
    assert.deepEqual(out, { fresh: '' })
  })
})

describe('模型分组表：服务端翻页之后，这一页只对自己持有的行负责', () => {
  /*
    ── 这一组用例守的是本轮最贵的那个失败方式 ──

    保存写的是**整份** `options.GroupRatio`（一个 JSON blob，没有按键写回的
    接口）。服务端切页之后表上只剩 10 行，而序列化的产物会被原样 PUT 回去 ——
    不带基线的一次保存会把第 2 页往后每一个模型分组的兜底倍率全部删掉，
    界面上只有一句绿色的「已保存」。那些分组随即落进 `GetGroupRatio` 的
    fail-open 分支，按凭空的 1.0 计费。
  */

  test('页外的键原样保留 —— 一次保存不会删掉没显示出来的分组', () => {
    const baseline = { onPage: 1, offPage: 2.5 }
    const out = JSON.parse(
      qyMgSerializeRatios(
        [mergedRow({ name: 'onPage', ratio: '0.4' })],
        baseline
      )
    ) as Record<string, number>
    assert.equal(out.onPage, 0.4, '本页改过的行按本页写')
    assert.equal(
      out.offPage,
      2.5,
      '不在本页的分组必须原样带过去 —— 丢掉它等于把那个池子改成按 1.0 静默计费'
    )
  })

  test('关掉「用户可选」仍然是一次删除，不会被基线原样带回来', () => {
    // 基线合并最容易写反的一处：只做 `{...baseline, ...本页}` 的话，
    // 关掉开关这个动作在产物里根本不存在，运营按完保存开关又亮回来。
    const out = JSON.parse(
      qyMgSerializeUsableGroups(
        [mergedRow({ name: 'gone', selectable: false })],
        { gone: '旧文案', kept: '别页的文案' }
      )
    ) as Record<string, string>
    assert.equal(Object.hasOwn(out, 'gone'), false)
    assert.equal(out.kept, '别页的文案')
  })

  test('一行都没改时，产物与基线逐键相同 —— 保存不会写一份自己的快照', () => {
    const baseline = { a: 1, b: 0, c: 0.3 }
    assert.deepEqual(
      JSON.parse(qyMgSerializeRatios([], baseline)) as Record<string, number>,
      baseline
    )
    const usable = { a: '', b: '文案' }
    assert.deepEqual(
      JSON.parse(qyMgSerializeUsableGroups([], usable)) as Record<
        string,
        string
      >,
      usable
    )
  })

  test('不给基线时行为与改造之前逐位相同（本页即全表）', () => {
    const out = JSON.parse(
      qyMgSerializeRatios([mergedRow({ name: 'only', ratio: '2' })])
    ) as Record<string, number>
    assert.deepEqual(out, { only: 2 })
  })

  test('names 给定时原样用它的顺序，不再本地并集 —— 否则第 2 页会长出全站的名字', () => {
    const rows = qyMgBuildRows({
      registry: [registryRow('g3'), registryRow('g4')],
      // 基线里有全站五个分组的倍率。翻页时它们**不能**变成行。
      groupRatios: { g1: 1, g2: 1, g3: 1, g4: 1, g5: 1 },
      usableGroups: { g5: '只在可选清单里' },
      autoGroups: [],
      names: ['g3', 'g4'],
    })
    assert.deepEqual(
      rows.map((row) => row.name),
      ['g3', 'g4']
    )
  })

  test('registered 取服务端字段，不是"它在不在返回的数组里"', () => {
    // 并集之后每一行都在数组里，而其中一部分没有登记行 —— 推错的方向是
    // 给一个没登记的名字亮起联动删除，而那条路径清不掉任何一处引用。
    const rows = qyMgBuildRows({
      registry: [registryRow('ghost', { registered: false })],
      groupRatios: {},
      usableGroups: { ghost: '' },
      autoGroups: [],
      names: ['ghost'],
    })
    assert.equal(rows[0].registered, false)
  })
})
