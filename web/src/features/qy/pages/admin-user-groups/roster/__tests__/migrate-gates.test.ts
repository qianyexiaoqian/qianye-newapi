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
import { join } from 'node:path'
import { describe, test } from 'node:test'

import {
  QY_UGR_MIGRATE_GATE_KEY,
  qyUgrMigrateBlock,
  qyUgrMigrateEntry,
  qyUgrRefillSources,
} from '../lib/gates'
import type { QyUgrImpact, QyUgrMigrationDiff, QyUgrResidue } from '../types'

/**
 * 「一键迁移」的入口闸门与按钮闸门。
 *
 * ── 这一组锁防的是同一件事：迁移被删除的闸门传染 ──
 *
 * 项目方原话：「既然 default 的用户分组无法删除，那么你就在用户分组这里增加一个
 * 用户分组迁移的功能」。迁移入口存在的全部理由就是 `default` 删不掉而那 700 个人
 * 必须能挪走。把删除那四条（default / 套餐引用 / block 残留 / 没有目标）搬过来，
 * 这个按钮会恰好在最需要它的那一行上灰掉 —— 而界面看起来完全正常，只是那一行
 * 什么都做不了。这正是这次被报上来的那一幕，换了个入口重演一次。
 */

function impact(patch: Partial<QyUgrImpact>): QyUgrImpact {
  return {
    blocking: [],
    blocking_plans: [],
    deletable: true,
    empty_group_tokens: 0,
    name: 'default',
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
    from: 'default',
    loses_everything: false,
    to: '新手档',
    unchanged: 0,
    ...patch,
  }
}

describe('qyUgrMigrateEntry', () => {
  test('default 这一行必须能按 —— 它删不掉,而这个入口就是为它加的', () => {
    assert.deepEqual(
      qyUgrMigrateEntry({ registered: true, user_count: 707 }),
      { enabled: true, noteKey: null },
      'default 的 deletable 恒为 false。迁移入口一旦照抄删除的判据,' +
        '站上人最多的那一档就一个人都挪不走 —— 而那正是这次报上来的缺陷本身'
    )
  })

  test('未登记但有人挂着 —— 后端不要求登记行,入口不许拦', () => {
    assert.equal(
      qyUgrMigrateEntry({ registered: false, user_count: 37 }).enabled,
      true,
      '历史遗留分组(只在 users.group 里)恰恰是最需要被清空的那一类'
    )
  })

  test('一个人都没有时关按钮,并说清为什么', () => {
    // 后端 adminMigrateUserGroup 对 0 人直接 400:没有东西可迁。
    // 让按钮亮着的表现是点下去吃一个他看不懂的 400。
    const entry = qyUgrMigrateEntry({ registered: true, user_count: 0 })
    assert.equal(entry.enabled, false)
    assert.equal(
      entry.noteKey,
      'qy_ugr_migrate_no_users',
      '关掉按钮时必须同时给出文案:一个按不动又不解释的按钮正是本次投诉'
    )
  })

  test('既没登记行、也没人挂着 —— 迁移端点同样寻址不到', () => {
    const entry = qyUgrMigrateEntry({ registered: false, user_count: 0 })
    assert.equal(entry.enabled, false)
    assert.equal(entry.noteKey, 'qy_ug_delete_not_addressable')
  })
})

describe('qyUgrMigrateBlock', () => {
  test('影响面还没到达时按钮必须是禁用的', () => {
    assert.equal(qyUgrMigrateBlock(null, '', false), 'loading')
  })

  test('**不看 deletable** —— 那是删除的结论,与"人能不能挪走"无关', () => {
    // default 的真实形状:deletable=false + 一段拒绝理由 + 707 个人。
    const undeletable = impact({
      block_code: 'upstream_default',
      block_reason: 'default 是 users.group 这一列的数据库默认值',
      deletable: false,
      users: 707,
    })
    assert.equal(
      qyUgrMigrateBlock(undeletable, '新手档', false),
      null,
      '选好目标之后迁移键必须是亮的。跟着 deletable 灰掉等于把这个功能关掉'
    )
  })

  test('没选目标时拦住 —— 后端对空目标是 400', () => {
    assert.equal(
      qyUgrMigrateBlock(impact({ users: 707 }), '', false),
      'needs_target'
    )
  })

  test('0 人时拦住 —— 弹窗打开之后被别人挪空了也算', () => {
    assert.equal(
      qyUgrMigrateBlock(impact({ users: 0 }), '新手档', false),
      'no_users'
    )
  })

  test('目标一个模型分组都用不了时必须显式勾选', () => {
    const doomed = impact({
      diff: diff({ loses_everything: true }),
      users: 707,
    })
    assert.equal(qyUgrMigrateBlock(doomed, '新手档', false), 'needs_ack')
    assert.equal(qyUgrMigrateBlock(doomed, '新手档', true), null)
  })

  test('目标能用时不弹那道闸门 —— 常驻的红勾选框会让真正的风险失去信号', () => {
    const fine = impact({ diff: diff({ unchanged: 5 }), users: 707 })
    assert.equal(qyUgrMigrateBlock(fine, '新手档', false), null)
  })
})

describe('迁移闸门的每一条都有话说', () => {
  const i18nDir = join(import.meta.dirname, '../../../../../../i18n/qy')

  test('四条闸门在 zh / en 两份语言包里都有文案', () => {
    // 漏掉一条的表现是屏幕上出现一个原样的键名,或者更糟 —— 一个按不动、
    // 也不解释的按钮。穷尽 Record 挡得住"忘了加分支",挡不住"加了键没加文案"。
    for (const lang of ['zh', 'en']) {
      const bundle = JSON.parse(
        readFileSync(join(i18nDir, `${lang}.json`), 'utf8')
      ) as Record<string, string>
      for (const key of Object.values(QY_UGR_MIGRATE_GATE_KEY)) {
        assert.ok(
          typeof bundle[key] === 'string' && bundle[key].trim() !== '',
          `${lang}.json 缺少闸门文案 ${key}`
        )
      }
      assert.ok(
        typeof bundle.qy_ugr_migrate_no_users === 'string',
        `${lang}.json 缺少表行上那句「这一档现在没有人」`
      )
      // 「迁完之后源分组还在」这句话是这条链路上唯一会被读反的东西:
      // 「迁移」的直觉是"挪完就没了",而这里源分组连同全部配置都留着。
      assert.ok(
        typeof bundle.qy_ugr_migrate_keeps_source_desc === 'string',
        `${lang}.json 缺少「迁完之后源分组仍然存在」的说明`
      )
    }
  })
})

/* ── 「迁完之后还会有人被放回来」必须在按下确认之前说完 ─────────────────── */

function residue(patch: Partial<QyUgrResidue>): QyUgrResidue {
  return {
    disposition: 'keep',
    label: '某处配置',
    module: 'system',
    rows: 0,
    table: 'options',
    ...patch,
  }
}

describe('qyUgrRefillSources', () => {
  test('处置为 rewrite 且有行的残留必须列进来 —— 那就是「新注册用户默认分组」', () => {
    // default 几乎必然同时是「新注册用户默认分组」，而这个入口存在的唯一理由
    // 就是把 default 这一档的人挪走。少了这一条，运营在按下确认之前看不到
    // 「迁完之后每一个新注册用户仍然会落回来」——而那正是他决定「要不要先去改
    // 注册默认分组」的前提。放在动作之后（成功 toast）等于没给他做这个决定。
    const got = qyUgrRefillSources(
      impact({
        residues: [
          residue({
            disposition: 'rewrite',
            label: '新注册用户默认分组',
            rows: 1,
            table: 'options',
          }),
        ],
      })
    )
    assert.deepEqual(
      got.residues.map((row) => row.label),
      ['新注册用户默认分组']
    )
  })

  test('两类来源同时出现时一条都不许掉 —— 只画一半比什么都不画更糟', () => {
    const got = qyUgrRefillSources(
      impact({
        blocking_plans: ['月卡', '年卡'],
        residues: [
          residue({ disposition: 'rewrite', label: '注册默认分组', rows: 1 }),
        ],
      })
    )
    assert.deepEqual(got.plans, ['月卡', '年卡'])
    assert.equal(got.residues.length, 1)
  })

  test('rows=0 的 rewrite 与其它三种处置都不算「会放人」', () => {
    // clean / keep / block 是配置与历史值，它们不产生新用户；rows=0 的那条
    // 是一个没有命中任何行的探测结果。把它们混进来会让每一次迁移都弹一段
    // 「这一档还会被填回来」，而运营对这段话脱敏之后，真的那一条也就看不见了。
    const got = qyUgrRefillSources(
      impact({
        residues: [
          residue({ disposition: 'rewrite', label: '空探测', rows: 0 }),
          residue({ disposition: 'block', label: '冻结值', rows: 3 }),
          residue({ disposition: 'clean', label: '待清理', rows: 5 }),
          residue({ disposition: 'keep', label: '历史值', rows: 7 }),
        ],
      })
    )
    assert.deepEqual(got.residues, [])
  })

  test('影响面还没回来时给两个空数组，不炸', () => {
    assert.deepEqual(qyUgrRefillSources(null), { plans: [], residues: [] })
  })

  test('两条 refill 文案在 zh / en 两份语言包里都有', () => {
    const dir = join(import.meta.dirname, '../../../../../../i18n/qy')
    for (const lang of ['zh', 'en']) {
      const bundle = JSON.parse(
        readFileSync(join(dir, `${lang}.json`), 'utf8')
      ) as Record<string, string>
      for (const key of [
        'qy_ugr_migrate_refill_residues',
        'qy_ugr_migrate_refill_residue_item',
      ]) {
        assert.ok(
          typeof bundle[key] === 'string' && bundle[key].trim() !== '',
          `${lang}.json 缺少 ${key}`
        )
      }
    }
  })
})
